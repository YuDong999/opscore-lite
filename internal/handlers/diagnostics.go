package handlers

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

func permLabel() string {
	if os.Geteuid() == 0 {
		return "root"
	}
	return "user"
}

func isRoot() bool { return os.Geteuid() == 0 }

// stripSchemeAndPath 从 URL 中剥离协议前缀、userinfo、路径和查询参数，只保留 host[:port]
// 例: "https://user:pass@example.com:8443/path?q=1" → "example.com:8443"
func stripSchemeAndPath(target string) string {
	t := strings.TrimSpace(target)
	if t == "" {
		return t
	}
	lower := strings.ToLower(t)
	for _, prefix := range []string{"https://", "http://"} {
		if strings.HasPrefix(lower, prefix) {
			t = t[len(prefix):]
			break
		}
	}
	// 去掉 userinfo (user:pass@)
	if i := strings.Index(t, "@"); i >= 0 {
		t = t[i+1:]
	}
	// 去掉路径和查询参数
	if i := strings.Index(t, "/"); i >= 0 {
		t = t[:i]
	}
	return t
}

func DiagnosticsInfo(w http.ResponseWriter, r *http.Request) {
	WriteJSON(w, map[string]any{
		"permission": permLabel(),
		"features": []map[string]any{
			{"id": "network", "name": "网络诊断", "available": true},
			{"id": "login-audit", "name": "登录审计", "available": true},
			{"id": "updates", "name": "系统更新", "available": os.Geteuid() == 0},
		},
	})
}

func NetworkDiagHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	var body struct {
		Tool   string `json:"tool"`
		Target string `json:"target"`
		Port   int    `json:"port"`
		Count  int    `json:"count"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteJSON(w, map[string]any{"error": "请求格式错误", "permission": permLabel()})
		return
	}
	needTarget := !(body.Tool == "route" || body.Tool == "arp")
	if needTarget && body.Target == "" {
		WriteJSON(w, map[string]any{"error": "缺少 target", "permission": permLabel()})
		return
	}

	// 对 ping/traceroute/mtr/nc/dig 等工具剥离 URL scheme，但 curl 需要保留完整的 URL
	target := body.Target
	if body.Tool != "http" && needTarget {
		target = stripSchemeAndPath(body.Target)
	}

	var cmd *exec.Cmd
	switch body.Tool {
	case "ping":
		c := body.Count
		if c < 1 || c > 10 {
			c = 4
		}
		cmd = exec.Command("ping", "-c", strconv.Itoa(c), "-W", "3", target)
	case "traceroute":
		cmd = exec.Command("traceroute", "-n", "-m", "20", target)
	case "port":
		if body.Port < 1 || body.Port > 65535 {
			WriteJSON(w, map[string]any{"error": "端口范围 1-65535", "permission": permLabel()})
			return
		}
		cmd = exec.Command("nc", "-zvn", "-w", "3", target, strconv.Itoa(body.Port))
	case "dns":
		cmd = exec.Command("dig", "+short", target)
	case "dns-detail":
		cmd = exec.Command("dig", target)
	case "mtr":
		cmd = exec.Command("mtr", "-r", "-c", "5", "-n", target)
	case "http":
		cmd = exec.Command("curl", "-sI", "-o", "/dev/stderr", "-w", "%{http_code}\n%{content_type}\n%{size_download}", target)
	case "route":
		cmd = exec.Command("ip", "route", "show")
	case "arp":
		cmd = exec.Command("ip", "neigh", "show")
	default:
		WriteJSON(w, map[string]any{"error": "未知工具: " + body.Tool, "permission": permLabel()})
		return
	}

	out, err := cmd.CombinedOutput()
	resp := map[string]any{"output": string(out), "permission": permLabel()}
	if err != nil {
		resp["error"] = err.Error()
	}
	WriteJSON(w, resp)
}

func LoginAuditHandler(w http.ResponseWriter, r *http.Request) {
	last := runCapture("last", "-F", "-n", "30")
	lastb := ""
	sshd := ""
	if isRoot() {
		lastb = runCapture("lastb", "-F", "-n", "30")
		sshd = runCapture("journalctl", "-u", "sshd", "--no-pager", "-n", "30", "--since", "7 days ago")
	}
	WriteJSON(w, map[string]any{
		"last":       last,
		"lastb":      lastb,
		"sshd_logs":  sshd,
		"permission": permLabel(),
	})
}

func UpdatesHandler(w http.ResponseWriter, r *http.Request) {
	if !isRoot() {
		WriteJSON(w, map[string]any{"error": "需要 root 权限", "permission": "user"})
		return
	}
	updates := runCapture("dnf", "check-update", "--security", "-q")
	nr := runCapture("needs-restarting", "-r")
	WriteJSON(w, map[string]any{
		"updates":        updates,
		"needs_restart":  !strings.Contains(nr, "Reboot is not required"),
		"restart_detail": nr,
		"permission":     "root",
	})
}
