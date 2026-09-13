package handlers

// ── 任务与存储 · 磁盘清理(规则驱动 + 调试Pod通道 chroot /host) ──
// 规则在所有已注册集群的匹配节点上, 经 kubectl debug 临时Pod + chroot /host 执行清理命令。
// 存储沿用仓库 JSON 惯例: <dataDir>/diskclean-{rules,schedules,logs}.json(日志上限200条)。
// 调度: 每 30s 轮询启用中的 schedule, cron 精确到分钟(支持 * , - /)。
// 安全: 命令为自由文本, safe 标记仅供前端提示; 执行走一次性调试Pod, 用完即删。

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
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

// ── 执行引擎(目标主机分发) ──
// kubectl 驱动在主机组 Linux 主机上执行(调试Pod 钉在目标节点, chroot /host 清理节点磁盘),
// 部署机自身不需要任何 K8S 工具(Windows/无Docker 部署同样可用)。
// 收口时此"驱动宿主选择 + 分发执行"上收为公共能力通道(与 logmonitor remote_runner 同源)。

// dcExecutorHost 选定 kubectl 驱动宿主: 主机组第一个 Linux 主机(排除本机/Windows)。
func dcExecutorHost() (string, error) {
	for _, h := range ansibleMgr.ListHosts() {
		if h.IsLocal || h.Platform == "win" {
			continue
		}
		return h.ID, nil
	}
	return "", fmt.Errorf("主机组无可用 Linux 主机(磁盘清理的调试Pod 需在 Linux 主机上经 kubectl 驱动)")
}

// dcExecute 解析启用的规则并对匹配节点发起清理(经驱动宿主分发)。
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
	execHost, err := dcExecutorHost()
	if err != nil {
		log.Printf("[diskclean] %v", err)
		return
	}
	cluster, _ := dcSh(execHost, `kubectl config current-context 2>/dev/null`)
	cluster = strings.TrimSpace(cluster)
	if cluster == "" {
		cluster = execHost
	}
	for _, node := range dcClusterNodes(execHost) {
		ok, _ := path.Match(nodePattern, node)
		if !ok {
			continue
		}
		for _, r := range rules {
			dcRunOnNode(execHost, cluster, node, r, scheduleID, started)
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

// dcSh 在驱动宿主上执行一段 shell(经 RunOnTarget 分发)。
func dcSh(execHost, script string) (string, error) {
	return RunOnTarget(execHost, []string{"sh", "-c", script})
}

// dcClusterNodes 经驱动宿主列出集群全部节点名(驱动端 kubectl 用它自身的默认 kubeconfig)。
func dcClusterNodes(execHost string) []string {
	out, err := dcSh(execHost, `kubectl get nodes -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}'`)
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

// dcRunOnNode 在目标节点上经调试Pod + chroot /host 执行一条规则, 记录执行日志。
// 调试Pod 由驱动宿主的 kubectl 创建并钉在目标节点, 清理命令作用于节点根文件系统;
// 命令经 base64 过桥, 规避多层 shell 的引号地狱。
func dcRunOnNode(execHost, cluster, node string, r CleanRule, scheduleID string, started time.Time) {
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
	cmdB64 := base64.StdEncoding.EncodeToString([]byte("cd " + r.Path + " 2>/dev/null; " + r.Command))
	script := fmt.Sprintf(`kubectl delete pod %[1]s --ignore-not-found --wait=false >/dev/null 2>&1
kubectl debug node/%[2]s --image=%[3]s -o name >/dev/null 2>&1 || { echo "ERR:create"; exit 3; }
kubectl wait --for=condition=Ready pod/%[1]s --timeout=90s >/dev/null 2>&1 || { echo "ERR:wait"; exit 2; }
echo "BEFORE:$(kubectl exec %[1]s -- chroot /host df -B1 / 2>/dev/null | tail -1 | awk '{print $4}')"
kubectl exec %[1]s -- chroot /host sh -c "echo __CMD__ | base64 -d | sh" || echo "ERR:cmd"
echo "AFTER:$(kubectl exec %[1]s -- chroot /host df -B1 / 2>/dev/null | tail -1 | awk '{print $4}')"
kubectl delete pod %[1]s --ignore-not-found --wait=false >/dev/null 2>&1
echo "__DONE__"`, pod, node, dcImage)
	script = strings.ReplaceAll(script, "__CMD__", cmdB64)
	out, err := dcSh(execHost, script)
	lg.Output = strings.TrimSpace(out)
	status, output, before, after := dcParseNodeScript(lg.Output)
	lg.Status = status
	lg.Output = output
	lg.BeforeAvail = before
	lg.AfterAvail = after
	lg.FreedBytes = after - before
	if lg.FreedBytes < 0 {
		lg.FreedBytes = 0
	}
	if status == "error" && lg.Output == "" {
		lg.Output = "执行失败: " + err.Error()
	}
}

// dcParseNodeScript 解析节点脚本的标准标记(BEFORE:/AFTER:/__DONE__/ERR:)。
func dcParseNodeScript(out string) (status, output string, before, after int64) {
	var cmdOut []string
	for _, ln := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(ln, "BEFORE:"):
			before = dcAvailBytes(ln[len("BEFORE:"):])
		case strings.HasPrefix(ln, "AFTER:"):
			after = dcAvailBytes(ln[len("AFTER:"):])
		case strings.HasPrefix(ln, "ERR:"):
			return "error", strings.TrimPrefix(ln, "ERR:"), before, after
		}
		if strings.HasPrefix(ln, "BEFORE:") || strings.HasPrefix(ln, "AFTER:") {
			continue
		}
		cmdOut = append(cmdOut, ln)
	}
	return "ok", strings.TrimSpace(strings.Join(cmdOut, "\n")), before, after
}

func dcAvailBytes(s string) int64 {
	fields := strings.Fields(strings.TrimSpace(s))
	if len(fields) >= 4 {
		if v, err := strconv.ParseInt(fields[len(fields)-3], 10, 64); err == nil {
			return v
		}
	}
	return 0
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
