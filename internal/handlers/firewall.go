package handlers

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ── 数据结构 ──

// FirewallStatus 描述当前主机的防火墙后端能力与运行状态。
type FirewallStatus struct {
	OS         string `json:"os"`
	Backend    string `json:"backend"`    // ufw | firewalld | netsh | unknown
	Running    bool   `json:"running"`    // 防火墙服务是否开启
	Manageable bool   `json:"manageable"` // 当前环境是否真正执行写入(否则只读/预览)
	Message    string `json:"message"`
}

// FirewallRule 是一条已存在的防火墙规则(只读展示用)。
type FirewallRule struct {
	Name      string `json:"name"`
	Direction string `json:"direction"`
	Action    string `json:"action"`
	Protocol  string `json:"protocol"`
	LocalPort string `json:"localPort"`
	RemoteIP  string `json:"remoteIP"`
}

// AuditEntry 是 ADR-002 审计链的单条记录:(actor, role, credential, action, params, result, ts)。
type AuditEntry struct {
	TS         string `json:"ts"`
	Target     string `json:"target"`          // 操作落点主机(审计核心字段)
	Verified   bool   `json:"verified"`        // 写操作后回读验证结果
	Actor      string `json:"actor"`
	Role       string `json:"role"`
	Credential string `json:"credential"`
	Action     string `json:"action"`
	Params     string `json:"params"`
	Result     string `json:"result"`
	DryRun     bool   `json:"dryRun"`
}

type fwAuditStore struct {
	mu  sync.Mutex
	log []AuditEntry
}

var fwAudits fwAuditStore

func (s *fwAuditStore) add(e AuditEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log = append(s.log, e)
	if len(s.log) > 50 {
		s.log = s.log[len(s.log)-50:]
	}
}

// ── 后端探测 ──

func detectBackend() (backend string, running bool, manageable bool, msg string) {
	switch runtime.GOOS {
	case "windows":
		return "netsh", netshFirewallOn(), false,
			"Windows 端为只读演示:真实开关 / 端口 / 黑白名单需在 Linux + 特权运行;此处仅展示命令预览"
	case "linux":
		if _, err := exec.LookPath("ufw"); err == nil {
			return "ufw", ufwActive(), true, ""
		}
		if _, err := exec.LookPath("firewall-cmd"); err == nil {
			return "firewalld", firewalldRunning(), true, ""
		}
		return "iptables-raw", false, false, "未检测到 ufw / firewalld,仅能读取 iptables(只读)"
	default:
		return "unknown", false, false, "不支持的平台(只读)"
	}
}

func netshFirewallOn() bool {
	out, err := exec.Command("netsh", "advfirewall", "show", "allprofiles", "state").Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "State") && strings.Contains(line, "ON") {
			return true
		}
	}
	return false
}

func ufwActive() bool {
	out, _ := exec.Command("ufw", "status").Output()
	return strings.Contains(string(out), "Status: active")
}

func firewalldRunning() bool {
	out, err := exec.Command("firewall-cmd", "--state").Output()
	return err == nil && strings.Contains(string(out), "running")
}

// ── 新增: 区域 / Rich-Rule / 端口转发 ──

func runCmd(cmd string) (string, error) {
	out, err := exec.Command("sh", "-c", cmd).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// ── 目标主机分发: 本机 sh -c / 远程经 RunOnTarget(单会话 SSH) ──

type fwExec func(cmd string) (string, error)

func newFwExec(hostID string) fwExec {
	if IsLocalTarget(hostID) {
		return runCmd
	}
	return func(cmd string) (string, error) {
		out, err := RunOnTarget(hostID, []string{"sh", "-c", cmd})
		return strings.TrimSpace(out), err
	}
}

func mustOut(ex fwExec, cmd string) string {
	o, _ := ex(cmd)
	return strings.TrimSpace(o)
}

// ufwActiveEx / firewalldRunningEx: 目标机版状态探测
func ufwActiveEx(ex fwExec) bool {
	return strings.Contains(mustOut(ex, "ufw status"), "Status: active")
}
func firewalldRunningEx(ex fwExec) bool {
	_, err := ex("firewall-cmd --state")
	return err == nil
}

// detectBackendFor 返回"目标主机"的防火墙后端与可写性。
// 本机走原 detectBackend; 远程经 SSH 探测(SSH 清单用户为 root, 视为可管理)。
//
// 探测容错: 单条组合命令一次往返; 失败(传输抖动)最多重试3次;
// 全部失败时回退到最近一次已知后端(fwBackendCache), 仍无才报 unknown。
func detectBackendFor(hostID string) (backend string, running bool, manageable bool, msg string) {
	if IsLocalTarget(hostID) {
		return detectBackend()
	}
	ex := newFwExec(hostID)

	if b := fwCachedBackend(hostID); b != "" {
		switch b {
		case "ufw":
			return b, ufwActiveEx(ex), true, ""
		case "firewalld":
			return b, firewalldRunningEx(ex), true, ""
		case "none":
			return "none", false, false, "目标主机未安装 ufw / firewalld"
		}
	}

	const probeCmd = `if command -v ufw >/dev/null 2>&1; then echo ufw; elif command -v firewall-cmd >/dev/null 2>&1; then echo firewalld; else echo none; fi`
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 300 * time.Millisecond)
		}
		out, err := RunOnTarget(hostID, []string{"sh", "-c", probeCmd})
		b := strings.TrimSpace(out)
		if err != nil || b == "" {
			lastErr = fmt.Errorf("探测输出空(err=%v)", err)
			continue // 传输抖动, 重试
		}
		fwSetBackendCache(hostID, b)
		switch b {
		case "ufw":
			return b, ufwActiveEx(ex), true, ""
		case "firewalld":
			return b, firewalldRunningEx(ex), true, ""
		default: // none: 目标确实没装
			return "none", false, false, "目标主机未检测到 ufw / firewalld"
		}
	}
	// 三次探测全失败
	if c := fwCachedBackend(hostID); c == "none" {
		return "none", false, false, "目标主机未检测到 ufw / firewalld"
	}
	_ = lastErr
	return "unknown", false, false, "目标主机探测失败(连接抖动), 请重试"
}

// fwBackendCache 按主机缓存最近已知防火墙后端, 吸收探测瞬间的传输抖动。
var (
	fwBackendMu    sync.Mutex
	fwBackendCache = map[string]fwBackendEntry{}
)

type fwBackendEntry struct {
	backend string
	at      time.Time
}

func fwSetBackendCache(hostID, backend string) {
	fwBackendMu.Lock()
	fwBackendCache[hostID] = fwBackendEntry{backend: backend, at: time.Now()}
	fwBackendMu.Unlock()
}

func fwCachedBackend(hostID string) string {
	fwBackendMu.Lock()
	defer fwBackendMu.Unlock()
	e, ok := fwBackendCache[hostID]
	if !ok || time.Since(e.at) > 15*time.Minute {
		return ""
	}
	return e.backend
}

// displayTarget 审计与响应里展示的目标主机名。
func displayTarget(hostID string) string {
	if IsLocalTarget(hostID) {
		return "本机(server)"
	}
	if h := resolveAnsibleHost(hostID); h != nil {
		name := h.Alias
		if name == "" {
			name = h.Hostname
		}
		if name == "" {
			name = h.Addr
		}
		return name + "(" + h.Addr + ")"
	}
	return hostID
}

// verifyFirewallState 写操作后的回读验证: 状态必须真实变化才返回 true。
// systemd 状态切换存在短暂过渡态(deactivating 等), 采用轮询窗口(最长6s)等待到位。
func verifyFirewallState(target, backend, action string) bool {
	var want string
	switch action {
	case "start", "restart":
		want = "active"
	case "stop":
		want = "inactive"
	default:
		return true // 规则类操作以命令退出码为准
	}

	check := func() string {
		switch backend {
		case "firewalld":
			// 注意: is-active 在 inactive 时退出码为 3, 只看输出文本
			out, _ := RunOnTarget(target, []string{"systemctl", "is-active", "firewalld"})
			return strings.TrimSpace(out)
		case "ufw":
			out, _ := RunOnTarget(target, []string{"ufw", "status"})
			if strings.Contains(out, "Status: active") {
				return "active"
			}
			return "inactive"
		default:
			return want // 无已知探测方式, 以命令退出码为准
		}
	}

	if backend != "firewalld" && backend != "ufw" {
		return true
	}
	deadline := time.Now().Add(6 * time.Second)
	var lastGot string
	for {
		lastGot = check()
		if lastGot == want {
			return true
		}
		if time.Now().After(deadline) {
			log.Printf("[FW-VERIFY] 目标=%s backend=%s action=%s want=%s 但持续读到 %q", displayTarget(target), backend, action, want, lastGot)
			return false
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// FirewallZones 处理 GET /api/core/firewall/zones
func FirewallZones(w http.ResponseWriter, r *http.Request) {
	ex := newFwExec(HostIDFromRequest(r))
	zones, _ := ex("firewall-cmd --get-zones")
	zone, _ := ex("firewall-cmd --get-default-zone")
	active, _ := ex("firewall-cmd --get-active-zones")
	WriteJSON(w, map[string]any{
		"all":     strings.Fields(zones),
		"default": zone,
		"active":  active,
	})
}

// FirewallRichRules 处理 GET /api/core/firewall/rich-rules
func FirewallRichRules(w http.ResponseWriter, r *http.Request) {
	ex := newFwExec(HostIDFromRequest(r))
	out, err := ex("firewall-cmd --list-rich-rules")
	if err != nil {
		WriteJSON(w, map[string]any{"rules": []string{}, "note": "firewalld 不可用"})
		return
	}
	lines := strings.Split(out, "\n")
	rules := make([]string, 0, len(lines))
	for _, l := range lines {
		if l = strings.TrimSpace(l); l != "" {
			rules = append(rules, l)
		}
	}
	WriteJSON(w, map[string]any{"rules": rules})
}

// FirewallForwardPorts 处理 GET /api/core/firewall/forward-ports
func FirewallForwardPorts(w http.ResponseWriter, r *http.Request) {
	ex := newFwExec(HostIDFromRequest(r))
	out, err := ex("firewall-cmd --list-forward-ports")
	if err != nil {
		WriteJSON(w, map[string]any{"ports": []string{}, "note": "firewalld 不可用"})
		return
	}
	lines := strings.Split(out, "\n")
	ports := make([]string, 0, len(lines))
	for _, l := range lines {
		if l = strings.TrimSpace(l); l != "" {
			ports = append(ports, l)
		}
	}
	WriteJSON(w, map[string]any{"ports": ports})
}

// ── 只读端点 ──

// FirewallStatusHandler 处理 GET /api/core/firewall
func FirewallStatusHandler(w http.ResponseWriter, r *http.Request) {
	hostID := HostIDFromRequest(r)
	b, running, m, msg := detectBackendFor(hostID)
	osName := runtime.GOOS
	if !IsLocalTarget(hostID) {
		osName = "linux(远程)"
	}
	st := FirewallStatus{OS: osName, Backend: b, Running: running, Manageable: m, Message: msg}
	st.Message += " · 目标: " + displayTarget(hostID)
	if st.Message == "" {
		st.Message = "可读写(环境支持)"
	}
	WriteJSON(w, st)
}

// FirewallRules 处理 GET /api/core/firewall/rules —— 真实读取当前规则(尽力而为)。
func FirewallRules(w http.ResponseWriter, r *http.Request) {
	hostID := HostIDFromRequest(r)
	var rules []FirewallRule
	if IsLocalTarget(hostID) && runtime.GOOS == "windows" {
		rules = parseNetshRules()
	} else {
		rules = parseLinuxRulesEx(newFwExec(hostID))
	}
	resp := map[string]any{"rules": rules, "count": len(rules)}
	if len(rules) == 0 {
		resp["note"] = "无规则 / 防火墙服务未运行(只读环境);在开启防火墙的 Windows 或 ufw 主机上可读取真实规则"
	}
	WriteJSON(w, resp)
}

// FirewallAudit 处理 GET /api/core/firewall/audit —— 返回内存中的审计链(演示)。
func FirewallAudit(w http.ResponseWriter, r *http.Request) {
	fwAudits.mu.Lock()
	defer fwAudits.mu.Unlock()
	WriteJSON(w, map[string]any{"entries": fwAudits.log})
}

func parseNetshRules() []FirewallRule {
	out, err := exec.Command("netsh", "advfirewall", "firewall", "show", "rule", "name=all").Output()
	if err != nil {
		return nil
	}
	// Windows netsh 使用 CRLF,先归一化再按空行切分规则块
	s := strings.ReplaceAll(string(out), "\r\n", "\n")
	blocks := strings.Split(s, "\n\n")
	var rules []FirewallRule
	for _, blk := range blocks {
		if !strings.Contains(blk, "Rule Name:") {
			continue
		}
		r := FirewallRule{}
		for _, line := range strings.Split(blk, "\n") {
			kv := strings.SplitN(line, ":", 2)
			if len(kv) != 2 {
				continue
			}
			k := strings.TrimSpace(kv[0])
			v := strings.TrimSpace(kv[1])
			switch k {
			case "Rule Name":
				r.Name = v
			case "Direction":
				r.Direction = v
			case "Action":
				r.Action = v
			case "Protocol":
				r.Protocol = v
			case "LocalPort":
				r.LocalPort = v
			case "RemoteIP":
				r.RemoteIP = v
			}
		}
		if r.Name != "" {
			rules = append(rules, r)
		}
		if len(rules) >= 150 { // 避免页面过长
			break
		}
	}
	return rules
}

func parseLinuxRules() []FirewallRule { return parseLinuxRulesEx(runCmd) }

// parseLinuxRulesEx 从目标主机读取 ufw / firewalld 规则并解析。
func parseLinuxRulesEx(ex fwExec) []FirewallRule {
	if mustOut(ex, "command -v ufw") != "" {
		out, err := ex("ufw status numbered")
		if err == nil {
			var rules []FirewallRule
			for _, line := range strings.Split(out, "\n") {
				line = strings.TrimSpace(line)
				if !strings.HasPrefix(line, "[") {
					continue
				}
				idx := strings.Index(line, "]")
				if idx < 0 {
					continue
				}
				fields := strings.Fields(strings.TrimSpace(line[idx+1:]))
				if len(fields) < 3 {
					continue
				}
				portProto := fields[0]
				r := FirewallRule{Name: "ufw:" + portProto, Action: fields[1], Direction: "IN"}
				if strings.Contains(portProto, "/") {
					pp := strings.SplitN(portProto, "/", 2)
					r.LocalPort, r.Protocol = pp[0], pp[1]
				} else {
					r.LocalPort = portProto
				}
				rules = append(rules, r)
			}
			return rules
		}
	}
	if mustOut(ex, "command -v firewall-cmd") != "" {
		var rules []FirewallRule
		if out, err := ex("firewall-cmd --list-ports"); err == nil {
			for _, tok := range strings.Fields(out) {
				pp := strings.SplitN(tok, "/", 2)
				r := FirewallRule{Name: "firewalld:" + tok, Action: "ALLOW", Direction: "IN"}
				if len(pp) == 2 {
					r.LocalPort, r.Protocol = pp[0], pp[1]
				} else {
					r.LocalPort = tok
				}
				rules = append(rules, r)
			}
		}
		if out, err := ex("firewall-cmd --list-services"); err == nil {
			for _, svc := range strings.Fields(out) {
				rules = append(rules, FirewallRule{
					Name: "firewalld:service:" + svc, Action: "ALLOW", Direction: "IN",
					LocalPort: svc, Protocol: "service",
				})
			}
		}
		return rules
	}
	return nil
}

// ── 写入端点(安全骨架) ──

type fwCmdParams struct {
	Host       string // 目标主机ID(空=本机); 切换主机后所有操作必须落在所选主机
	Action     string
	Port       string
	Proto      string
	CIDR       string
	Source     string
	Zone       string // 目标区域名
	RichRule   string // rich-rule 原文
	FwdSrcPort string // 端口转发-源端口
	FwdDest    string // 端口转发-目标地址(含端口,如 10.0.0.2:80)
	Reason     string
	DryRun     bool // 仅预览命令,绝不真正执行(前端二次确认前的预览用)
}

// ── 输入校验(白名单) ──

func validatePort(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	port, err := strconv.Atoi(s)
	if err != nil || port < 1 || port > 65535 {
		return ""
	}
	return s
}

func validateProto(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "":
		return "tcp"
	case "tcp", "udp", "sctp":
		return s
	}
	return ""
}

func validateCIDR(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsAny(s, ";|&`$(){}[]!<>'\"\n\r") {
		return ""
	}
	return s
}

func validateZone(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	for _, c := range s {
		ok := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_'
		if !ok {
			return ""
		}
	}
	return s
}

func validateRichRule(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "rule ") {
		return ""
	}
	if strings.ContainsAny(s, "'\"`;|$&<>(){}\\\n\r") {
		return ""
	}
	return s
}

// validateHostPort 校验 "10.0.0.2:80" 形式, 返回 (规范化值, 是否合法)。
func validateHostPort(s string) (string, bool) {
	s = strings.TrimSpace(s)
	dp := strings.SplitN(s, ":", 2)
	if len(dp) != 2 || dp[0] == "" || validatePort(dp[1]) == "" {
		return "", false
	}
	addr := dp[0]
	for _, c := range addr {
		ok := (c >= '0' && c <= '9') || c == '.' || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') || c == ':'
		if !ok {
			return "", false
		}
	}
	return addr + ":" + dp[1], true
}

// FirewallAction 处理 POST /api/core/firewall/action
// 设计原则(对应 ADR-002 红线):
//   - 每次写入都产生审计链记录;
//   - 当前环境不可写(manageable=false,如本机 Windows)时,只返回将执行的命令(dryRun),绝不真正改网络;
//   - 对可能把自己锁死的操作(关 SSH / RDP / 当前端口、封全网)标记 lockoutRisk。
func FirewallAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var p fwCmdParams
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil || p.Action == "" {
		WriteJSON(w, map[string]any{"ok": false, "error": "invalid body / action 必填"})
		return
	}
	if strings.TrimSpace(p.Reason) == "" {
		WriteJSON(w, map[string]any{"ok": false, "error": "必须填写操作原因(审计要求)"})
		return
	}

	target := strings.TrimSpace(p.Host)
	backend, _, manageable, msg := detectBackendFor(target)
	// 后端探测失败/不支持 ≠ 参数非法: 分流报错, 前端可提示重试
	switch backend {
	case "ufw", "firewalld", "netsh":
	default:
		WriteJSON(w, map[string]any{
			"ok":        false,
			"error":     "目标主机防火墙后端不可用(" + backend + "): " + msg,
			"target":    displayTarget(target),
			"retryable": true,
		})
		return
	}
	cmdArgs, lockRisk := buildFirewallCommand(backend, p)
	if cmdArgs == nil {
		WriteJSON(w, map[string]any{"ok": false, "error": "参数非法(port/cidr/proto/zone/rich-rule 格式校验失败)"})
		return
	}
	cmdStr := strings.Join(cmdArgs, " ")

	entry := AuditEntry{
		TS:         time.Now().Format(time.RFC3339),
		Actor:      "demo-anonymous",
		Role:       "demo",
		Target:     displayTarget(target),
		Credential: backend,
		Action:     p.Action,
		Params:     cmdStr,
		DryRun:     !manageable || p.DryRun,
	}

	// 预览(dryRun)或当前环境不可写:只回命令,绝不真正改网络。
	if !manageable || p.DryRun {
		entry.Result = "dry-run(未执行)"
		log.Printf("[FW-AUDIT] %s", mustJSON(entry))
		fwAudits.add(entry)
		WriteJSON(w, map[string]any{
			"ok":          false,
			"dryRun":      true,
			"command":     cmdStr,
			"lockoutRisk": lockRisk,
			"target":      entry.Target,
			"message":     "目标不可写或预览模式,未执行。" + msg,
			"audit":       entry,
		})
		return
	}

	// 真正执行: 统一经 RunOnTarget 分发到目标主机(本机 exec / 远程 SSH)。
	out, err := RunOnTarget(target, cmdArgs)
	entry.Result = "ok"
	if err != nil {
		entry.Result = "fail: " + strings.TrimSpace(out)
	}
	// 回读验证: 防止"显示已执行实际没执行"
	entry.Verified = err == nil && verifyFirewallState(target, backend, p.Action)
	if err == nil && !entry.Verified {
		entry.Result += "(回读不符)"
	}
	log.Printf("[FW-AUDIT] %s", mustJSON(entry))
	fwAudits.add(entry)
	WriteJSON(w, map[string]any{
		"ok":          err == nil,
		"verified":    entry.Verified,
		"target":      entry.Target,
		"command":     cmdStr,
		"lockoutRisk": lockRisk,
		"output":      strings.TrimSpace(out),
		"audit":       entry,
	})
}

// buildFirewallCommand 返回 (参数数组, 是否可能锁死自己)。参数直接交给 exec.Command 执行, 完全不经过 shell。
func buildFirewallCommand(backend string, p fwCmdParams) ([]string, bool) {
	proto := validateProto(p.Proto)
	switch p.Action {
	case "delete-rule":
		switch backend {
		case "netsh":
			return []string{"netsh", "advfirewall", "firewall", "delete", "rule", "name=" + p.Source}, false
		case "ufw":
			fields := strings.Fields(strings.TrimSpace(p.Source))
			if len(fields) == 0 {
				return nil, false
			}
			for _, f := range fields {
				if strings.ContainsAny(f, ";|&`$<>\n\r") {
					return nil, false
				}
			}
			return append([]string{"ufw", "delete"}, fields...), false
		case "firewalld":
			if strings.ContainsAny(p.Source, ";|&`$<>'\"\n\r") {
				return nil, false
			}
			return []string{"firewall-cmd", "--remove-port=" + p.Source, "--permanent"}, false
		}
	case "start":
		switch backend {
		case "ufw":
			return []string{"ufw", "enable"}, false
		case "firewalld":
			return []string{"systemctl", "start", "firewalld"}, false
		case "netsh":
			return []string{"netsh", "advfirewall", "set", "allprofiles", "state", "on"}, false
		}
	case "stop":
		switch backend {
		case "ufw":
			return []string{"ufw", "disable"}, false
		case "firewalld":
			return []string{"systemctl", "stop", "firewalld"}, false
		case "netsh":
			return []string{"netsh", "advfirewall", "set", "allprofiles", "state", "off"}, false
		}
	case "restart":
		switch backend {
		case "ufw":
			return []string{"systemctl", "restart", "ufw"}, false
		case "firewalld":
			return []string{"systemctl", "restart", "firewalld"}, false
		case "netsh":
			return []string{"netsh", "advfirewall", "set", "allprofiles", "state", "on"}, false
		}
	case "allow-port":
		port := validatePort(p.Port)
		if proto == "" || port == "" {
			return nil, false
		}
		switch backend {
		case "ufw":
			return []string{"ufw", "allow", port + "/" + proto}, false
		case "firewalld":
			return []string{"firewall-cmd", "--add-port=" + port + "/" + proto, "--permanent"}, false
		case "netsh":
			return []string{"netsh", "advfirewall", "firewall", "add", "rule", "name=opscore-allow-" + port, "dir=in", "action=allow", "protocol=" + proto, "localport=" + port}, false
		}
	case "deny-port":
		lock := p.Port == "22" || p.Port == "3389" || p.Port == "8080"
		port := validatePort(p.Port)
		if proto == "" || port == "" {
			return nil, false
		}
		switch backend {
		case "ufw":
			return []string{"ufw", "deny", port + "/" + proto}, lock
		case "firewalld":
			return []string{"firewall-cmd", "--add-rich-rule=rule port port=" + port + " protocol=" + proto + " reject", "--permanent"}, lock
		case "netsh":
			return []string{"netsh", "advfirewall", "firewall", "add", "rule", "name=opscore-deny-" + port, "dir=in", "action=block", "protocol=" + proto, "localport=" + port}, lock
		}
	case "allow-ip":
		cidr := validateCIDR(p.CIDR)
		if cidr == "" {
			return nil, false
		}
		switch backend {
		case "ufw":
			return []string{"ufw", "allow", "from", cidr}, false
		case "firewalld":
			return []string{"firewall-cmd", "--add-source=" + cidr, "--permanent"}, false
		case "netsh":
			return []string{"netsh", "advfirewall", "firewall", "add", "rule", "name=opscore-allow-ip", "dir=in", "action=allow", "remoteip=" + cidr}, false
		}
	case "deny-ip":
		lock := p.CIDR == "0.0.0.0/0" || p.CIDR == "::/0"
		cidr := validateCIDR(p.CIDR)
		if cidr == "" {
			return nil, false
		}
		switch backend {
		case "ufw":
			return []string{"ufw", "deny", "from", cidr}, lock
		case "firewalld":
			return []string{"firewall-cmd", "--add-rich-rule=rule source address=" + cidr + " reject", "--permanent"}, lock
		case "netsh":
			return []string{"netsh", "advfirewall", "firewall", "add", "rule", "name=opscore-deny-ip", "dir=in", "action=block", "remoteip=" + cidr}, lock
		}
	case "set-default-zone":
		if backend != "firewalld" {
			return []string{"echo", "only firewalld supports zone"}, false
		}
		zone := validateZone(p.Zone)
		if zone == "" {
			return nil, false
		}
		return []string{"firewall-cmd", "--set-default-zone=" + zone}, false
	case "add-rich-rule", "remove-rich-rule":
		if backend != "firewalld" {
			return []string{"echo", "only firewalld supports rich-rule"}, false
		}
		op := "--add-rich-rule="
		if p.Action == "remove-rich-rule" {
			op = "--remove-rich-rule="
		}
		rule := validateRichRule(p.RichRule)
		if rule == "" {
			return nil, false
		}
		args := []string{"firewall-cmd", op + rule, "--permanent"}
		if z := validateZone(p.Zone); z != "" {
			args = append(args, "--zone="+z)
		}
		return args, false
	case "add-forward-port", "remove-forward-port":
		if backend != "firewalld" {
			return []string{"echo", "only firewalld supports forward-port"}, false
		}
		dst, ok := validateHostPort(p.FwdDest)
		if proto == "" || !ok {
			return nil, false
		}
		srcPort := validatePort(p.FwdSrcPort)
		if srcPort == "" {
			return nil, false
		}
		dp := strings.SplitN(dst, ":", 2)
		toAddr, toPort := dp[0], dp[1]
		verb := "add"
		if p.Action == "remove-forward-port" {
			verb = "remove"
		}
		args := []string{"firewall-cmd", "--" + verb + "-forward-port=port=" + srcPort + ":proto=" + proto + ":toaddr=" + toAddr + ":toport=" + toPort, "--permanent"}
		if z := validateZone(p.Zone); z != "" {
			args = append(args, "--zone="+z)
		}
		return args, false
	}
	return nil, false
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
