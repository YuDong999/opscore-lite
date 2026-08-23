package handlers

import (
	"encoding/json"
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

// FirewallZones 处理 GET /api/core/firewall/zones
func FirewallZones(w http.ResponseWriter, r *http.Request) {
	zones, _ := runCmd("firewall-cmd --get-zones")
	zone, _ := runCmd("firewall-cmd --get-default-zone")
	active, _ := runCmd("firewall-cmd --get-active-zones")
	WriteJSON(w, map[string]any{
		"all":     strings.Fields(zones),
		"default": zone,
		"active":  active,
	})
}

// FirewallRichRules 处理 GET /api/core/firewall/rich-rules
func FirewallRichRules(w http.ResponseWriter, r *http.Request) {
	out, err := runCmd("firewall-cmd --list-rich-rules")
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
	out, err := runCmd("firewall-cmd --list-forward-ports")
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
	b, running, m, msg := detectBackend()
	st := FirewallStatus{OS: runtime.GOOS, Backend: b, Running: running, Manageable: m, Message: msg}
	if st.Message == "" {
		st.Message = "可读写(环境支持)"
	}
	WriteJSON(w, st)
}

// FirewallRules 处理 GET /api/core/firewall/rules —— 真实读取当前规则(尽力而为)。
func FirewallRules(w http.ResponseWriter, r *http.Request) {
	var rules []FirewallRule
	switch runtime.GOOS {
	case "windows":
		rules = parseNetshRules()
	default:
		rules = parseLinuxRules()
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

func parseLinuxRules() []FirewallRule {
	if _, err := exec.LookPath("ufw"); err == nil {
		out, err := exec.Command("ufw", "status", "numbered").Output()
		if err == nil {
			var rules []FirewallRule
			for _, line := range strings.Split(string(out), "\n") {
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
	return nil
}

// ── 写入端点(安全骨架) ──

type fwCmdParams struct {
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

	backend, _, manageable, _ := detectBackend()
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
			"message":     "当前环境为只读演示,未真正执行;该命令将在 Linux + 特权的目标主机上生效。",
			"audit":       entry,
		})
		return
	}

	// 真正执行(仅 Linux + 特权环境可达): 参数数组直传, 不经过 shell。
	out, err := exec.Command(cmdArgs[0], cmdArgs[1:]...).CombinedOutput()
	if err != nil {
		entry.Result = "fail: " + strings.TrimSpace(string(out))
	} else {
		entry.Result = "ok"
	}
	log.Printf("[FW-AUDIT] %s", mustJSON(entry))
	fwAudits.add(entry)
	WriteJSON(w, map[string]any{
		"ok":          err == nil,
		"command":     cmdStr,
		"lockoutRisk": lockRisk,
		"output":      strings.TrimSpace(string(out)),
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
			return []string{"ufw", "reload"}, false
		case "firewalld":
			return []string{"firewall-cmd", "--reload"}, false
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
