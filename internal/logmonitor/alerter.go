package logmonitor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Alerter 告警评估器
type Alerter struct {
	store    *Store
	client   *http.Client
	mu       sync.Mutex
	running  bool
}

// NewAlerter 创建告警评估器
func NewAlerter(store *Store) *Alerter {
	return &Alerter{
		store:  store,
		client: &http.Client{Timeout: 5 * time.Second},
	}
}

// Evaluate 评估所有启用规则（周期性调用）
func (a *Alerter) Evaluate() {
	rules, err := a.store.ListAlertRules()
	if err != nil {
		log.Printf("[alerter] list rules: %v", err)
		return
	}
	for _, r := range rules {
		if !r.Enabled { continue }
		a.evaluateRule(r)
	}
}

// evaluateRule 评估单条规则
func (a *Alerter) evaluateRule(r *AlertRule) {
	// 解析 condition：支持 level=ERROR / service=order-api
	cond := strings.TrimSpace(r.Condition)
	if cond == "" { return }

	var whereClause string
	var arg string
	if strings.HasPrefix(cond, "level=") {
		arg = strings.TrimPrefix(cond, "level=")
		whereClause = "level = ?"
	} else if strings.HasPrefix(cond, "service=") {
		arg = strings.TrimPrefix(cond, "service=")
		whereClause = "service = ?"
	} else {
		return
	}

	windowMs := r.WindowMs
	if windowMs <= 0 { windowMs = 60000 }
	now := time.Now().UnixMilli()
	start := now - windowMs

	// 查询窗口内匹配数(跨分片累加, 窗口近 → 通常只命中热表)
	var cnt int64
	for _, t := range a.store.tablesForRange(start, now, "") {
		var c int64
		a.store.db.QueryRow(
			fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE %s AND ts >= ? AND ts <= ?", t, whereClause),
			arg, start, now,
		).Scan(&c)
		cnt += c
	}

	// 判断是否触发
	if cnt >= int64(r.CountThresh) && r.CountThresh > 0 {
		// 冷却期检查
		if r.LastFired > 0 && now - r.LastFired < r.CooldownMs {
			return // 冷却中
		}
		a.fire(r, cnt, arg)
	} else if r.State == "firing" {
		// 恢复
		a.resolve(r)
	}
}

// fire 触发告警
func (a *Alerter) fire(r *AlertRule, count int64, matchVal string) {
	// 记录事件
	ev := &AlertEvent{
		ID:       "ae_" + strconv.FormatInt(time.Now().UnixMilli(), 10),
		RuleID:   r.ID,
		RuleName: r.Name,
		Count:    int(count),
		FiredAt:  time.Now().UnixMilli(),
		Status:   "firing",
	}
	if strings.HasPrefix(r.Condition, "level=") {
		ev.Level = matchVal
	} else {
		ev.Service = matchVal
	}
	if _, err := a.store.db.Exec(
		`INSERT OR REPLACE INTO alert_events (id, rule_id, rule_name, level, service, count, fired_at, resolved_at, status) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ev.ID, ev.RuleID, ev.RuleName, ev.Level, ev.Service, ev.Count, ev.FiredAt, 0, ev.Status,
	); err != nil {
		log.Printf("[alerter] insert event: %v", err)
	}

	// 更新规则状态
	r.State = "firing"
	r.LastFired = time.Now().UnixMilli()
	if _, err := a.store.db.Exec("UPDATE alert_rules SET state='firing', last_fired=? WHERE id=?", r.LastFired, r.ID); err != nil {
		log.Printf("[alerter] update rule state: %v", err)
	}

	// 发送通知
	chans, _ := a.store.ListAlertChannels()
	for _, ch := range chans {
		if !ch.Enabled { continue }
		if !contains(r.Channels, ch.ID) { continue }
		a.sendWebhook(ch, r, ev)
	}
}

// resolve 恢复告警
func (a *Alerter) resolve(r *AlertRule) {
	r.State = "ok"
	if _, err := a.store.db.Exec("UPDATE alert_rules SET state='ok' WHERE id=?", r.ID); err != nil {
		log.Printf("[alerter] resolve rule: %v", err)
	}
	if _, err := a.store.db.Exec("UPDATE alert_events SET status='resolved', resolved_at=? WHERE rule_id=? AND status='firing'",
		time.Now().UnixMilli(), r.ID); err != nil {
		log.Printf("[alerter] resolve events: %v", err)
	}
}

// sendWebhook 发送 webhook 通知
func (a *Alerter) sendWebhook(ch *AlertChannel, r *AlertRule, ev *AlertEvent) {
	payload := map[string]interface{}{
		"rule":    r.Name,
		"ruleId":  r.ID,
		"level":   ev.Level,
		"service": ev.Service,
		"count":   ev.Count,
		"firedAt": time.UnixMilli(ev.FiredAt).Format(time.RFC3339),
		"message": fmt.Sprintf("[%s] 告警触发: %s 匹配数=%d", r.Name, r.Condition, ev.Count),
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequest(ch.Method, ch.URL, bytes.NewReader(body))
	if err != nil {
		log.Printf("[alerter] webhook req: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		log.Printf("[alerter] webhook send: %v", err)
		return
	}
	defer resp.Body.Close()
	log.Printf("[alerter] webhook sent to %s status=%d", ch.URL, resp.StatusCode)
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s { return true }
	}
	return false
}