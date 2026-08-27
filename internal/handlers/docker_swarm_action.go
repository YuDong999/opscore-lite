package handlers

// ── 容器管理插件 · Swarm 管理操作 ──
// init(初始化) / join-token(查看加入令牌) / leave(脱离, 高危确认) / scale(服务副本调整)
// 全部经 RunOnTarget 分发, 高危操作前端二次确认 + 审计。

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

var reSwarmService = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,126}$`)

type swarmActionBody struct {
	Host        string `json:"host"`
	Action      string `json:"action"` // init | token | leave | scale
	Role        string `json:"role"`   // token 用: worker | manager
	AdvertiseIP string `json:"advertiseIp,omitempty"`
	Service     string `json:"service,omitempty"` // scale 用
	Replicas    int    `json:"replicas,omitempty"`
	Force       bool   `json:"force,omitempty"` // leave 用
}

// DockerSwarmActionHandler POST
func DockerSwarmActionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(containersPluginID, w) {
		return
	}
	var b swarmActionBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		WriteJSON(w, map[string]any{"ok": false, "error": "invalid body"})
		return
	}
	var argv []string
	detail := ""
	switch b.Action {
	case "init":
		argv = []string{"docker", "swarm", "init"}
		if b.AdvertiseIP != "" {
			if !regexp.MustCompile(`^[0-9.]{7,15}$`).MatchString(b.AdvertiseIP) {
				WriteJSON(w, map[string]any{"ok": false, "error": "advertise-ip 必须为 IPv4 地址"})
				return
			}
			argv = append(argv, "--advertise-addr", b.AdvertiseIP)
		}
		detail = "advertise=" + b.AdvertiseIP
	case "token":
		switch b.Role {
		case "worker", "manager":
		default:
			WriteJSON(w, map[string]any{"ok": false, "error": "role 必须是 worker/manager"})
			return
		}
		argv = []string{"docker", "swarm", "join-token", b.Role, "-q"}
		detail = b.Role
	case "leave":
		argv = []string{"docker", "swarm", "leave"}
		if b.Force {
			argv = append(argv, "--force")
		}
		detail = ternary(b.Force, "force", "")
	case "scale":
		if !reSwarmService.MatchString(b.Service) || b.Replicas < 0 || b.Replicas > 1000 {
			WriteJSON(w, map[string]any{"ok": false, "error": "参数非法(service/replicas)"})
			return
		}
		argv = []string{"docker", "service", "scale", b.Service + "=" + strconv.Itoa(b.Replicas)}
		detail = b.Service + "=" + strconv.Itoa(b.Replicas)
	default:
		WriteJSON(w, map[string]any{"ok": false, "error": "action 必须是 init/token/leave/scale"})
		return
	}
	out, err := RunOnTarget(b.Host, argv)
	if err != nil {
		dockerAudit(b.Host, "swarm-"+b.Action, detail, err)
		WriteJSON(w, map[string]any{"ok": false, "error": lastLines(out, 5)})
		return
	}
	dockerAudit(b.Host, "swarm-"+b.Action, detail, nil)
	InvalidateRespCache("/api/plugins/containers/docker/swarm")
	resp := map[string]any{"ok": true, "action": b.Action, "output": lastLines(out, 6)}
	if b.Action == "token" {
		resp["token"] = strings.TrimSpace(lastLine(out))
	}
	WriteJSON(w, resp)
}
