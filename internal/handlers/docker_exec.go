package handlers

// ── 容器管理插件 · 容器内执行命令(一次性) ──
// docker exec <name> sh -c <cmd>, 输出截断返回。命令白名单字符校验防注入。

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
)

var reExecCmd = regexp.MustCompile(`^[\x20-\x7e]{1,500}$`) // 可打印 ASCII, 防控制字符注入

type dockerExecBody struct {
	Host string `json:"host"`
	Name string `json:"name"`
	Cmd  string `json:"cmd"`
}

// DockerExecHandler POST {host,name,cmd} → 容器内执行并回显输出
func DockerExecHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(containersPluginID, w) {
		return
	}
	var b dockerExecBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		WriteJSON(w, map[string]any{"ok": false, "error": "invalid body"})
		return
	}
	if !reContainerName.MatchString(b.Name) || !reExecCmd.MatchString(b.Cmd) {
		WriteJSON(w, map[string]any{"ok": false, "error": "参数非法(容器名/命令)"})
		return
	}
	out, err := RunOnTarget(b.Host, []string{"docker", "exec", b.Name, "sh", "-c", b.Cmd})
	rt := "docker"
	if err != nil && (strings.Contains(out, "no such command") || strings.Contains(out, "is not a docker command")) {
		out, err = RunOnTarget(b.Host, []string{"podman", "exec", b.Name, "sh", "-c", b.Cmd})
		rt = "podman"
	}
	if out == "" && err == nil {
		out = "(无输出)"
	}
	dockerAudit(b.Host, "exec-"+rt, b.Name+": "+b.Cmd, err)
	WriteJSON(w, map[string]any{
		"ok":    err == nil,
		"out":   truncateRunes(strings.TrimRight(out, "\n\r "), 8000),
		"error": ternary(err == nil, "", lastLines(out, 4)),
	})
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "\n…(已截断)"
}
