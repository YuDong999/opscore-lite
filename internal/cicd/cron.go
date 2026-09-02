package cicd

// 5 字段 cron(分 时 日 月 周)解析与匹配, 语义与 vixie crontab 一致:
// 支持 * 、*/n 、a 、a-b 、a,b,c 及范围步进 a-b/n; 日+周同时受限时按"或"匹配。

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"
)

// CronSpec 预解析的 cron 表达式(字段 → 命中集合)
type CronSpec struct {
	Minute  map[int]bool
	Hour    map[int]bool
	Dom     map[int]bool // day of month
	Month   map[int]bool
	Dow     map[int]bool // day of week, 0/7=周日
	domStar bool
	dowStar bool
}

// ParseCron 解析 5 字段 cron, 非法返回错误(保存流水线时做前置校验)
func ParseCron(expr string) (*CronSpec, error) {
	fields := strings.Fields(strings.TrimSpace(expr))
	if len(fields) != 5 {
		return nil, fmt.Errorf("cron 需为 5 个字段(分 时 日 月 周), 当前 %d 个", len(fields))
	}
	spec := &CronSpec{}
	parsers := []struct {
		f   string
		min int
		max int
		dst *map[int]bool
	}{
		{fields[0], 0, 59, &spec.Minute},
		{fields[1], 0, 23, &spec.Hour},
		{fields[2], 1, 31, &spec.Dom},
		{fields[3], 1, 12, &spec.Month},
		{fields[4], 0, 7, &spec.Dow},
	}
	for _, p := range parsers {
		set, star, err := parseCronField(p.f, p.min, p.max)
		if err != nil {
			return nil, fmt.Errorf("cron 字段 %q 无效: %w", p.f, err)
		}
		*p.dst = set
		if p.dst == &spec.Dom {
			spec.domStar = star
		}
		if p.dst == &spec.Dow {
			spec.dowStar = star
		}
	}
	return spec, nil
}

// parseCronField 解析单字段: 返回命中集合与是否为纯 *
func parseCronField(field string, min, max int) (map[int]bool, bool, error) {
	set := map[int]bool{}
	star := field == "*"
	for _, part := range strings.Split(field, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, false, fmt.Errorf("空子项")
		}
		step := 1
		// 范围步进: a-b/n 或 */n
		if idx := strings.Index(part, "/"); idx >= 0 {
			sv, err := strconv.Atoi(part[idx+1:])
			if err != nil || sv <= 0 {
				return nil, false, fmt.Errorf("步进 %q 无效", part[idx+1:])
			}
			step = sv
			part = part[:idx]
		}
		lo, hi := min, max
		switch {
		case part == "*":
			// 全范围(步进已剥离)
		case strings.Contains(part, "-"):
			bounds := strings.SplitN(part, "-", 2)
			l, err1 := strconv.Atoi(bounds[0])
			h, err2 := strconv.Atoi(bounds[1])
			if err1 != nil || err2 != nil || l > h {
				return nil, false, fmt.Errorf("范围 %q 无效", part)
			}
			lo, hi = l, h
		default:
			v, err := strconv.Atoi(part)
			if err != nil {
				return nil, false, fmt.Errorf("数值 %q 无效", part)
			}
			lo, hi = v, v
		}
		if lo < min || hi > max {
			return nil, false, fmt.Errorf("取值 %d-%d 超出范围 %d-%d", lo, hi, min, max)
		}
		for v := lo; v <= hi; v += step {
			set[v] = true
		}
	}
	if len(set) == 0 {
		return nil, false, fmt.Errorf("无有效取值")
	}
	return set, star, nil
}

// Match 判定给定时刻(分钟粒度)是否命中
func (c *CronSpec) Match(t time.Time) bool {
	if !c.Minute[t.Minute()] || !c.Hour[t.Hour()] || !c.Month[int(t.Month())] {
		return false
	}
	domHit := c.domStar || c.Dom[t.Day()]
	// 周日 0/7 归一
	dow := int(t.Weekday())
	dowHit := c.dowStar || c.Dow[dow] || (dow == 0 && c.Dow[7])
	// vixie 语义: 日与周都受限时, 命中其一即可
	if !c.domStar && !c.dowStar {
		return domHit || dowHit
	}
	return domHit && dowHit
}

// Describe 生成人类可读描述(简版, UI 展示用)
func (c *CronSpec) Describe() string {
	weekNames := map[int]string{0: "日", 1: "一", 2: "二", 3: "三", 4: "四", 5: "五", 6: "六", 7: "日"}
	// 逐字段找最简模式
	describe := func(set map[int]bool, min, max int, unit string, week bool) string {
		if len(set) == max-min+1 {
			return "每" + unit
		}
		// 步进检测: 等差且从 min 起
		step := detectStep(set, min, max)
		if step > 1 && len(set) == (max-min)/step+1 {
			return "每 " + strconv.Itoa(step) + " " + unit
		}
		vals := sortedKeys(set)
		parts := make([]string, 0, len(vals))
		for _, v := range vals {
			if week {
				parts = append(parts, weekNames[v])
			} else {
				parts = append(parts, strconv.Itoa(v))
			}
		}
		return unit + " " + strings.Join(parts, ",")
	}
	h := sortedKeys(c.Hour)
	m := sortedKeys(c.Minute)
	// 每天固定时刻: 小时/分钟均为列表
	if len(c.Dom) == 31 && len(c.Month) == 12 && c.dowStar {
		if len(h) == 1 && len(m) == 1 {
			return fmt.Sprintf("每天 %02d:%02d", h[0], m[0])
		}
		if step := detectStep(c.Minute, 0, 59); len(h) == 1 && step > 1 && len(m) == (60)/step {
			return fmt.Sprintf("每天 %02d 点起每 %d 分钟", h[0], step)
		}
	}
	out := describe(c.Minute, 0, 59, "分钟", false)
	out += " " + describe(c.Hour, 0, 23, "小时", false)
	if !c.domStar {
		out += " " + describe(c.Dom, 1, 31, "日", false)
	}
	if !c.dowStar {
		out += " 周" + strings.Join(mapWeeks(c.Dow), ",")
	}
	return out
}

func detectStep(set map[int]bool, min, max int) int {
	vals := sortedKeys(set)
	if len(vals) < 2 {
		return 0
	}
	step := vals[1] - vals[0]
	if step <= 1 {
		return 0
	}
	for i := 1; i < len(vals); i++ {
		if vals[i]-vals[i-1] != step {
			return 0
		}
	}
	return step
}

func sortedKeys(set map[int]bool) []int {
	out := make([]int, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func mapWeeks(set map[int]bool) []string {
	names := map[int]string{0: "日", 1: "一", 2: "二", 3: "三", 4: "四", 5: "五", 6: "六", 7: "日"}
	out := make([]string, 0, len(set))
	for _, v := range sortedKeys(set) {
		out = append(out, names[v])
	}
	return out
}

// ── cron 调度循环 ───────────────────────────────────────────

// startCronLoop 对齐到整分后每分钟扫描 cron 触发器(错过不补跑)
func (e *Engine) startCronLoop() {
	go func() {
		now := time.Now()
		time.Sleep(now.Truncate(time.Minute).Add(time.Minute).Sub(now))
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case now := <-ticker.C:
				e.fireCron(now)
			case <-e.stop:
				return
			}
		}
	}()
}

// fireCron 扫描全部流水线, 命中 cron 且本分钟未触发过的入队
func (e *Engine) fireCron(now time.Time) {
	minute := now.Truncate(time.Minute).Unix() / 60
	e.mu.RLock()
	var toFire []string
	for _, p := range e.pipes {
		spec, ok := e.crons[p.ID]
		if !ok || p.Trigger.Cron == "" {
			continue
		}
		if last, seen := e.lastFire[p.ID]; seen && last == minute {
			continue
		}
		if spec.Match(now) {
			toFire = append(toFire, p.ID)
		}
	}
	e.mu.RUnlock()
	for _, id := range toFire {
		e.mu.Lock()
		e.lastFire[id] = minute
		e.mu.Unlock()
		if _, err := e.Trigger(id, TriggerCron); err != nil {
			log.Printf("[cicd] cron 触发 %s: %v", id, err)
		}
	}
}
