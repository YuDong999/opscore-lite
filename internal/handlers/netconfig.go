package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"opscore/internal/remote"
)

func remoteNmcliGet(rmHost remote.Host, cmds map[string]string) map[string]string {
	res := remotePool.Exec(rmHost, cmds)
	out := map[string]string{}
	for k, v := range res {
		if v.Error != "" {
			out[k] = fmt.Sprintf("(%s error: %s)", k, v.Error)
		} else {
			out[k] = v.Output
		}
	}
	return out
}

// NetConnections 处理 GET /api/core/netconfig/connections — nmcli 连接列表 + WiFi
func NetConnections(w http.ResponseWriter, r *http.Request) {
	hostID := r.URL.Query().Get("host")
	if hostID != "" {
		h := resolveAnsibleHost(hostID)
		if h == nil {
			WriteJSON(w, map[string]any{"connections": "(host not found)", "wifi": ""})
			return
		}
		rmHost := resolveRemoteHost(*h)
		out := remoteNmcliGet(rmHost, map[string]string{
			"connections": "nmcli -t con show 2>/dev/null",
			"wifi":        "nmcli -t dev wifi 2>/dev/null",
		})
		WriteJSON(w, out)
		return
	}
	cons := runCapture("nmcli", "-t", "con", "show")
	wifiRaw := runCapture("nmcli", "-t", "dev", "wifi")
	WriteJSON(w, map[string]any{
		"connections": cons,
		"wifi":        wifiRaw,
	})
}

// NetConnectionAction 处理 POST /api/core/netconfig/connection — nmcli connection up/down/delete
func NetConnectionAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Action string `json:"action"`
		Name   string `json:"name"`
		SSID   string `json:"ssid"`
		PSK    string `json:"psk"`
		Device string `json:"device"`
		Host   string `json:"host"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Action == "" {
		WriteJSON(w, map[string]any{"ok": false, "error": "缺少 action"})
		return
	}

	if body.Host != "" {
		h := resolveAnsibleHost(body.Host)
		if h == nil {
			WriteJSON(w, map[string]any{"ok": false, "error": "未找到主机"})
			return
		}
		rmHost := resolveRemoteHost(*h)

		var cmd string
		switch body.Action {
		case "up":
			cmd = "nmcli con up " + quote(body.Name)
		case "down":
			cmd = "nmcli con down " + quote(body.Name)
		case "delete":
			cmd = "nmcli con delete " + quote(body.Name)
		case "wifi-connect":
			cmd = "nmcli dev wifi connect " + quote(body.SSID)
			if body.PSK != "" {
				cmd += " password " + quote(body.PSK)
			}
			if body.Device != "" {
				cmd += " ifname " + quote(body.Device)
			}
		default:
			WriteJSON(w, map[string]any{"ok": false, "error": "未知 action: " + body.Action})
			return
		}

		res := remotePool.Exec(rmHost, map[string]string{"out": cmd})
		if res["out"].Error != "" {
			WriteJSON(w, map[string]any{"ok": false, "error": res["out"].Error, "output": res["out"].Output})
		} else {
			WriteJSON(w, map[string]any{"ok": true, "output": strings.TrimSpace(res["out"].Output)})
		}
		return
	}

	var cmd *exec.Cmd
	switch body.Action {
	case "up":
		cmd = exec.Command("nmcli", "con", "up", body.Name)
	case "down":
		cmd = exec.Command("nmcli", "con", "down", body.Name)
	case "delete":
		cmd = exec.Command("nmcli", "con", "delete", body.Name)
	case "wifi-connect":
		args := []string{"dev", "wifi", "connect", body.SSID}
		if body.PSK != "" {
			args = append(args, "password", body.PSK)
		}
		if body.Device != "" {
			args = append(args, "ifname", body.Device)
		}
		cmd = exec.Command("nmcli", args...)
	default:
		WriteJSON(w, map[string]any{"ok": false, "error": "未知 action: " + body.Action})
		return
	}

	out, err := cmd.CombinedOutput()
	resp := map[string]any{"ok": err == nil}
	if err != nil {
		resp["error"] = err.Error()
	}
	resp["output"] = strings.TrimSpace(string(out))
	WriteJSON(w, resp)
}

func NetConfigHandler(w http.ResponseWriter, r *http.Request) {
	hostID := r.URL.Query().Get("host")

	if r.Method == "GET" {
		if hostID != "" {
			h := resolveAnsibleHost(hostID)
			if h == nil {
				WriteJSON(w, map[string]any{"interfaces": "(host not found)", "routes": "", "dns": "", "nm": "", "permission": "remote"})
				return
			}
			rmHost := resolveRemoteHost(*h)
			out := remoteNmcliGet(rmHost, map[string]string{
				"interfaces": "ip addr show 2>/dev/null",
				"routes":     "ip route show 2>/dev/null",
				"dns_resolvectl": "resolvectl status 2>/dev/null",
				"resolv":     "cat /etc/resolv.conf 2>/dev/null",
				"nm":         "nmcli -t dev status 2>/dev/null",
			})
			dns := out["dns_resolvectl"]
			if dns == "" || !strings.Contains(dns, "DNS") {
				dns = out["resolv"]
			}
			WriteJSON(w, map[string]any{
				"interfaces": out["interfaces"],
				"routes":     out["routes"],
				"dns":        dns,
				"nm":         out["nm"],
				"permission": "root",
			})
			return
		}

		ifaces := runCapture("ip", "addr", "show")
		routes := runCapture("ip", "route", "show")
		dns := runCapture("resolvectl", "status")
		if strings.HasPrefix(dns, "(resolvectl not found)") || !strings.Contains(dns, "DNS") {
			dns = runCapture("cat", "/etc/resolv.conf")
		}
		nm := runCapture("nmcli", "-t", "dev", "status")
		WriteJSON(w, map[string]any{
			"interfaces": ifaces,
			"routes":     routes,
			"dns":        dns,
			"nm":         nm,
			"permission": permLabel(),
		})
		return
	}

	// POST: parse body once, extract host from it
	var postBody struct {
		Action string `json:"action"`
		Device string `json:"device"`
		IP     string `json:"ip"`
		Mask   int    `json:"mask"`
		DNS    string `json:"dns"`
		Host   string `json:"host"`
	}
	if err := json.NewDecoder(r.Body).Decode(&postBody); err != nil {
		WriteJSON(w, map[string]any{"error": "请求格式错误", "permission": "root"})
		return
	}
	if postBody.Host != "" {
		hostID = postBody.Host
	}

	if hostID != "" {
		h := resolveAnsibleHost(hostID)
		if h == nil {
			WriteJSON(w, map[string]any{"error": "未找到主机", "permission": "remote"})
			return
		}
		rmHost := resolveRemoteHost(*h)

		var cmd string
		switch postBody.Action {
		case "set-ip":
			if postBody.Device == "" || postBody.IP == "" {
				WriteJSON(w, map[string]any{"error": "缺少 device 或 ip", "permission": "remote"})
				return
			}
			cidr := postBody.IP
			if postBody.Mask > 0 {
				cidr += "/" + strconv.Itoa(postBody.Mask)
			}
			cmd = fmt.Sprintf("nmcli connection modify %s ipv4.addresses %s ipv4.method manual && nmcli connection up %s 2>/dev/null || ip addr add %s dev %s", quote(postBody.Device), quote(cidr), quote(postBody.Device), quote(cidr), quote(postBody.Device))
		case "set-dns":
			if postBody.DNS == "" || postBody.Device == "" {
				WriteJSON(w, map[string]any{"error": "缺少 dns 或 device", "permission": "remote"})
				return
			}
			cmd = fmt.Sprintf("nmcli connection modify %s ipv4.dns %s && nmcli connection up %s 2>/dev/null || echo 'nameserver %s' > /etc/resolv.conf", quote(postBody.Device), quote(postBody.DNS), quote(postBody.Device), postBody.DNS)
		case "restart":
			if postBody.Device == "" {
				WriteJSON(w, map[string]any{"error": "缺少 device", "permission": "remote"})
				return
			}
			cmd = fmt.Sprintf("ip link set dev %s down && ip link set dev %s up", quote(postBody.Device), quote(postBody.Device))
		case "dhcp":
			if postBody.Device == "" {
				WriteJSON(w, map[string]any{"error": "缺少 device", "permission": "remote"})
				return
			}
			cmd = fmt.Sprintf("nmcli connection modify %s ipv4.method auto && nmcli connection up %s 2>/dev/null || dhclient -v %s", quote(postBody.Device), quote(postBody.Device), quote(postBody.Device))
		default:
			WriteJSON(w, map[string]any{"error": "未知操作: " + postBody.Action, "permission": "remote"})
			return
		}

		res := remotePool.Exec(rmHost, map[string]string{"out": cmd})
		if res["out"].Error != "" {
			WriteJSON(w, map[string]any{"error": res["out"].Error, "output": res["out"].Output, "permission": "root"})
		} else {
			WriteJSON(w, map[string]any{"ok": true, "output": strings.TrimSpace(res["out"].Output), "permission": "root"})
		}
		return
	}

	if !isRoot() {
		WriteJSON(w, map[string]any{"error": "需要 root 权限", "permission": "user"})
		return
	}

	var localCmd *exec.Cmd
	switch postBody.Action {
	case "set-ip":
		if postBody.Device == "" || postBody.IP == "" {
			WriteJSON(w, map[string]any{"error": "缺少 device 或 ip", "permission": "root"})
			return
		}
		cidr := postBody.IP
		if postBody.Mask > 0 {
			cidr += "/" + strconv.Itoa(postBody.Mask)
		}
		if hasBin("nmcli") {
			localCmd = exec.Command("nmcli", "connection", "modify", postBody.Device, "ipv4.addresses", cidr, "ipv4.method", "manual")
			localCmd.Run()
			localCmd = exec.Command("nmcli", "connection", "up", postBody.Device)
		} else {
			localCmd = exec.Command("ip", "addr", "add", cidr, "dev", postBody.Device)
		}

	case "set-dns":
		if postBody.DNS == "" || postBody.Device == "" {
			WriteJSON(w, map[string]any{"error": "缺少 dns 或 device", "permission": "root"})
			return
		}
		if hasBin("nmcli") {
			localCmd = exec.Command("nmcli", "connection", "modify", postBody.Device, "ipv4.dns", postBody.DNS)
			localCmd.Run()
			localCmd = exec.Command("nmcli", "connection", "up", postBody.Device)
		} else {
			f, err := os.Create("/etc/resolv.conf")
			if err != nil {
				WriteJSON(w, map[string]any{"error": "写 resolv.conf 失败: " + err.Error(), "permission": "root"})
				return
			}
			defer f.Close()
			f.WriteString("# managed by opscore\n")
			for _, ns := range strings.Fields(postBody.DNS) {
				f.WriteString("nameserver " + ns + "\n")
			}
			WriteJSON(w, map[string]any{"ok": true, "note": "直接写入 /etc/resolv.conf，可能被 systemd-resolved 覆盖", "permission": "root"})
			return
		}

	case "restart":
		if postBody.Device == "" {
			WriteJSON(w, map[string]any{"error": "缺少 device", "permission": "root"})
			return
		}
		exec.Command("ip", "link", "set", "dev", postBody.Device, "down").Run()
		exec.Command("ip", "link", "set", "dev", postBody.Device, "up").Run()
		WriteJSON(w, map[string]any{"ok": true, "note": "网卡已重启，如果无法连接请手动恢复", "permission": "root"})
		return

	case "dhcp":
		if postBody.Device == "" {
			WriteJSON(w, map[string]any{"error": "缺少 device", "permission": "root"})
			return
		}
		if hasBin("nmcli") {
			localCmd = exec.Command("nmcli", "connection", "modify", postBody.Device, "ipv4.method", "auto")
			localCmd.Run()
			localCmd = exec.Command("nmcli", "connection", "up", postBody.Device)
		} else {
			localCmd = exec.Command("dhclient", "-v", postBody.Device)
		}

	default:
		WriteJSON(w, map[string]any{"error": "未知操作: " + postBody.Action, "permission": "root"})
		return
	}

	out, err := localCmd.CombinedOutput()
	resp := map[string]any{"permission": "root"}
	if err != nil {
		resp["error"] = err.Error()
		resp["output"] = string(out)
	} else {
		resp["ok"] = true
		resp["output"] = string(out)
	}
	WriteJSON(w, resp)
}

func hasBin(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
