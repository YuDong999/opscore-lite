package handlers

// ── 任务与存储 · 磁盘清理(规则驱动 + 调试Pod通道 chroot /host) ──
// 规则在所有已注册集群的匹配节点上, 经 kubectl debug 临时Pod + chroot /host 执行清理命令。
// 存储沿用仓库 JSON 惯例: <dataDir>/diskclean-{rules,schedules,logs}.json(日志上限200条)。
// 调度: 每 30s 轮询启用中的 schedule, cron 精确到分钟(支持 * , - /)。
// 安全: 命令为自由文本, safe 标记仅供前端提示; 执行走一次性调试Pod, 用完即删。

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type CleanRule struct {
	ID          string `json:"id"`
	NodePattern string `json:"nodePattern"`
	Path        string `json:"path"`
	Command     string `json:"command"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Safe        bool   `json:"safe"`
	Enabled     bool   `json:"enabled"`
}

type CleanSchedule struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Cron        string   `json:"cron"`
	NodePattern string   `json:"nodePattern"`
	RuleIDs     []string `json:"ruleIds"`
	Enabled     bool     `json:"enabled"`
	LastRunAt   int64    `json:"lastRunAt"`
	LastStatus  string   `json:"lastStatus"`
}

type CleanLog struct {
	ID          string   `json:"id"`
	ScheduleID  string   `json:"scheduleId"`
	RuleIDs     []string `json:"ruleIds"`
	Cluster     string   `json:"cluster"`
	Node        string   `json:"node"`
	BeforeAvail int64    `json:"beforeAvail"`
	AfterAvail  int64    `json:"afterAvail"`
	FreedBytes  int64    `json:"freedBytes"`
	Status      string   `json:"status"`
	Output      string   `json:"output"`
	CreatedAt   int64    `json:"createdAt"`
	DurationMs  int64    `json:"durationMs"`
}

var (
	dcMu        sync.Mutex
	dcDataDir   string
	dcRules     []CleanRule
	dcSchedules []CleanSchedule
	dcLogs      []CleanLog
	dcImage     = "busybox:1.36"
)

func dcLoadFile(path string, v any) {
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, v)
	}
}

func dcSaveFile(path string, v any) {
	if b, err := json.MarshalIndent(v, "", "  "); err == nil {
		_ = os.WriteFile(path, b, 0600)
	}
}

func dcID() string {
	return fmt.Sprintf("%x", time.Now().UnixNano())
}

func InitDiskClean(dataDir string) {
	dcDataDir = dataDir
	dcMu.Lock()
	dcLoadFile(filepath.Join(dataDir, "diskclean-rules.json"), &dcRules)
	dcLoadFile(filepath.Join(dataDir, "diskclean-schedules.json"), &dcSchedules)
	dcLoadFile(filepath.Join(dataDir, "diskclean-logs.json"), &dcLogs)
	dcMu.Unlock()
	go dcSchedulerLoop()
}

// ── 规则 CRUD ──

func DiskCleanRulesHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		dcMu.Lock()
		out := make([]CleanRule, len(dcRules))
		copy(out, dcRules)
		dcMu.Unlock()
		WriteJSON(w, map[string]any{"rules": out, "ok": true})
		return
	case http.MethodPost:
		if r.URL.Query().Get("_method") == "delete" {
			id := r.URL.Query().Get("id")
			dcMu.Lock()
			kept := dcRules[:0]
			for _, x := range dcRules {
				if x.ID != id {
					kept = append(kept, x)
				}
			}
			dcRules = kept
			dcSaveFile(filepath.Join(dcDataDir, "diskclean-rules.json"), dcRules)
			dcMu.Unlock()
			WriteJSON(w, map[string]any{"ok": true})
			return
		}
		var body struct {
			Rule CleanRule `json:"rule"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			WriteJSON(w, map[string]any{"error": err.Error()})
			return
		}
		ru := body.Rule
		if ru.Command == "" || ru.Path == "" {
			WriteJSON(w, map[string]any{"error": "path/command 不能为空"})
			return
		}
		dcMu.Lock()
		if ru.ID == "" {
			ru.ID = dcID()
			dcRules = append(dcRules, ru)
		} else {
			found := false
			for i := range dcRules {
				if dcRules[i].ID == ru.ID {
					dcRules[i] = ru
					found = true
				}
			}
			if !found {
				dcMu.Unlock()
				WriteJSON(w, map[string]any{"error": "规则不存在: " + ru.ID})
				return
			}
		}
		dcSaveFile(filepath.Join(dcDataDir, "diskclean-rules.json"), dcRules)
		dcMu.Unlock()
		WriteJSON(w, map[string]any{"ok": true, "rule": ru})
		return
	}
	w.Header().Set("Allow", "GET, POST")
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

// ── 调度 CRUD ──

func DiskCleanSchedulesHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		dcMu.Lock()
		out := make([]CleanSchedule, len(dcSchedules))
		copy(out, dcSchedules)
		dcMu.Unlock()
		WriteJSON(w, map[string]any{"schedules": out, "ok": true})
		return
	case http.MethodPost:
		if r.URL.Query().Get("_method") == "delete" {
			id := r.URL.Query().Get("id")
			dcMu.Lock()
			kept := dcSchedules[:0]
			for _, x := range dcSchedules {
				if x.ID != id {
					kept = append(kept, x)
				}
			}
			dcSchedules = kept
			dcSaveFile(filepath.Join(dcDataDir, "diskclean-schedules.json"), dcSchedules)
			dcMu.Unlock()
			WriteJSON(w, map[string]any{"ok": true})
			return
		}
		var body struct {
			Schedule CleanSchedule `json:"schedule"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			WriteJSON(w, map[string]any{"error": err.Error()})
			return
		}
		sc := body.Schedule
		if sc.Name == "" || strings.Fields(sc.Cron) == nil || len(strings.Fields(sc.Cron)) != 5 {
			WriteJSON(w, map[string]any{"error": "name/cron(5段) 不能为空"})
			return
		}
		dcMu.Lock()
		if sc.ID == "" {
			sc.ID = dcID()
			dcSchedules = append(dcSchedules, sc)
		} else {
			found := false
			for i := range dcSchedules {
				if dcSchedules[i].ID == sc.ID {
					dcSchedules[i] = sc
					found = true
				}
			}
			if !found {
				dcMu.Unlock()
				WriteJSON(w, map[string]any{"error": "调度不存在: " + sc.ID})
				return
			}
		}
		dcSaveFile(filepath.Join(dcDataDir, "diskclean-schedules.json"), dcSchedules)
		dcMu.Unlock()
		WriteJSON(w, map[string]any{"ok": true, "schedule": sc})
		return
	}
	w.Header().Set("Allow", "GET, POST")
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

// ── 执行 ──

func DiskCleanRunHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		NodePattern string   `json:"nodePattern"`
		RuleIDs     []string `json:"ruleIds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteJSON(w, map[string]any{"error": err.Error()})
		return
	}
	if body.NodePattern == "" {
		body.NodePattern = "*"
	}
	if len(body.RuleIDs) == 0 {
		WriteJSON(w, map[string]any{"error": "ruleIds 不能为空"})
		return
	}
	go dcExecute(body.NodePattern, body.RuleIDs, "")
	WriteJSON(w, map[string]any{"ok": true, "message": "已触发: " + body.NodePattern})
}

func DiskCleanLogsHandler(w http.ResponseWriter, r *http.Request) {
	dcMu.Lock()
	out := make([]CleanLog, len(dcLogs))
	copy(out, dcLogs)
	dcMu.Unlock()
	WriteJSON(w, map[string]any{"logs": out, "ok": true})
}

// ── 执行引擎 ──

func dcExecute(nodePattern string, ruleIDs []string, scheduleID string) {
	started := time.Now()
	dcMu.Lock()
	rules := make([]CleanRule, 0, len(ruleIDs))
	for _, id := range ruleIDs {
		for _, r := range dcRules {
			if r.ID == id && r.Enabled {
				rules = append(rules, r)
			}
		}
	}
	dcMu.Unlock()
	if len(rules) == 0 {
		return
	}
	kcs, _ := filepath.Glob(filepath.Join(dcDataDir, "kubeconfigs", "*.yaml"))
	sort.Strings(kcs)
	for _, kc := range kcs {
		cluster := strings.TrimSuffix(filepath.Base(kc), ".yaml")
		for _, node := range dcClusterNodes(kc) {
			ok, _ := path.Match(nodePattern, node)
			if !ok {
				continue
			}
			for _, r := range rules {
				dcRunOnNode(kc, cluster, node, r, scheduleID, started)
			}
		}
	}
	if scheduleID != "" {
		dcMu.Lock()
		for i := range dcSchedules {
			if dcSchedules[i].ID == scheduleID {
				dcSchedules[i].LastRunAt = time.Now().Unix()
				dcSchedules[i].LastStatus = "done"
			}
		}
		dcSaveFile(filepath.Join(dcDataDir, "diskclean-schedules.json"), dcSchedules)
		dcMu.Unlock()
	}
}

// dcClusterNodes 列出集群全部节点名。
func dcClusterNodes(kubeconfig string) []string {
	out, err := kubectl(kubeconfig, "get", "nodes", "-o", "jsonpath={range .items[*]}{.metadata.name}{\"\\n\"}{end}")
	if err != nil {
		return nil
	}
	var nodes []string
	for _, ln := range strings.Split(strings.TrimSpace(out), "\n") {
		if ln = strings.TrimSpace(ln); ln != "" {
			nodes = append(nodes, ln)
		}
	}
	return nodes
}

// dcRunOnNode 在节点上经调试Pod + chroot /host 执行一条规则, 记录执行日志。
func dcRunOnNode(kubeconfig, cluster, node string, r CleanRule, scheduleID string, started time.Time) {
	lg := CleanLog{ID: dcID(), ScheduleID: scheduleID, RuleIDs: []string{r.ID}, Cluster: cluster, Node: node, CreatedAt: time.Now().Unix()}
	defer func() {
		lg.DurationMs = time.Since(started).Milliseconds()
		dcMu.Lock()
		dcLogs = append(dcLogs, lg)
		if len(dcLogs) > 200 {
			dcLogs = dcLogs[len(dcLogs)-200:]
		}
		dcSaveFile(filepath.Join(dcDataDir, "diskclean-logs.json"), dcLogs)
		dcMu.Unlock()
	}()
	pod := node + "-opscore-dc"
	// 清理陈旧调试Pod(上次异常残留)
	_, _ = kubectl(kubeconfig, "delete", "pod", pod, "--ignore-not-found", "--wait=false", "--timeout=10s")
	// 创建调试Pod(挂载节点根文件系统到 /host)
	if _, err := kubectl(kubeconfig, "debug", "node/"+node, "--image="+dcImage, "-o", "name"); err != nil {
		lg.Status = "error"
		lg.Output = "创建调试Pod失败: " + err.Error()
		return
	}
	// 等 Pod Running(最多 60s)
	ready := false
	for i := 0; i < 20; i++ {
		time.Sleep(3 * time.Second)
		phase, err := kubectl(kubeconfig, "get", "pod", pod, "-o", "jsonpath={.status.phase}")
		if err == nil && strings.TrimSpace(phase) == "Running" {
			ready = true
			break
		}
	}
	defer func() { _, _ = kubectl(kubeconfig, "delete", "pod", pod, "--ignore-not-found", "--wait=false", "--timeout=15s") }()
	if !ready {
		lg.Status = "error"
		lg.Output = "调试Pod 未进入 Running(60s 超时)"
		return
	}
	// 前置可用空间
	beforeOut, err := kubectl(kubeconfig, "exec", pod, "--", "chroot", "/host", "df", "-B1", "/")
	if err == nil {
		lg.BeforeAvail = dcLastAvailBytes(beforeOut)
	}
	// 执行清理命令(工作目录 = 规则 path)
	cmd := "cd " + r.Path + " 2>/dev/null; " + r.Command
	out, err := kubectl(kubeconfig, "exec", pod, "--", "chroot", "/host", "sh", "-c", cmd)
	lg.Output = out
	if err != nil {
		lg.Status = "error"
		lg.Output += "\n[exit] " + err.Error()
		return
	}
	// 后置可用空间
	afterOut, err2 := kubectl(kubeconfig, "exec", pod, "--", "chroot", "/host", "df", "-B1", "/")
	if err2 == nil {
		lg.AfterAvail = dcLastAvailBytes(afterOut)
	}
	lg.FreedBytes = lg.AfterAvail - lg.BeforeAvail
	if lg.FreedBytes < 0 {
		lg.FreedBytes = 0
	}
	lg.Status = "ok"
}

func dcLastAvailBytes(dfOut string) int64 {
	fields := strings.Fields(strings.TrimSpace(dfOut))
	if len(fields) >= 4 {
		if v, err := strconv.ParseInt(fields[len(fields)-3], 10, 64); err == nil {
			return v
		}
	}
	return 0
}

func kubectl(kubeconfig string, args ...string) (string, error) {
	argv := append([]string{"--kubeconfig", kubeconfig}, args...)
	out, err := exec.Command("kubectl", argv...).CombinedOutput()
	return string(out), err
}

// ── 调度循环 ──

func dcSchedulerLoop() {
	for {
		time.Sleep(30 * time.Second)
		dcMu.Lock()
		now := time.Now()
		var due []CleanSchedule
		for _, s := range dcSchedules {
			if !s.Enabled {
				continue
			}
			if s.LastRunAt > 0 && now.Unix()-s.LastRunAt < 60 {
				continue // 同一分钟去重
			}
			if dcCronMatch(s.Cron, now) {
				due = append(due, s)
			}
		}
		dcMu.Unlock()
		for _, s := range due {
			log.Printf("[diskclean] 调度 %s 触发(cron=%s, nodes=%s)", s.Name, s.Cron, s.NodePattern)
			go dcExecute(s.NodePattern, s.RuleIDs, s.ID)
		}
	}
}

// dcCronMatch 最小 5 段 cron 匹配(分 时 日 月 周), 支持 * , - /。
func dcCronMatch(expr string, t time.Time) bool {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return false
	}
	vals := []int{t.Minute(), t.Hour(), t.Day(), int(t.Month()), int(t.Weekday())}
	for i, f := range fields {
		if !dcCronFieldMatch(f, vals[i]) {
			return false
		}
	}
	return true
}

func dcCronFieldMatch(f string, v int) bool {
	for _, part := range strings.Split(f, ",") {
		step := 1
		if i := strings.Index(part, "/"); i >= 0 {
			if step, _ = strconv.Atoi(part[i+1:]); step <= 0 {
				step = 1
			}
			part = part[:i]
		}
		if part == "*" {
			if step == 1 || v%step == 0 {
				return true
			}
			continue
		}
		lo, hi := v, v
		if i := strings.Index(part, "-"); i >= 0 {
			lo, _ = strconv.Atoi(part[:i])
			hi, _ = strconv.Atoi(part[i+1:])
		} else {
			n, err := strconv.Atoi(part)
			if err != nil || n < 0 {
				continue
			}
			lo, hi = n, n
		}
		if v >= lo && v <= hi && (v-lo)%step == 0 {
			return true
		}
	}
	return false
}
