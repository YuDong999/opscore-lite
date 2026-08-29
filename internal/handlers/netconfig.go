package handlers

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"golang.org/x/text/encoding/simplifiedchinese"

	"opscore/internal/platform"
	"opscore/internal/remote"
)

// validDNSList 校验逗号分隔的 DNS 服务器列表(每项须为合法 IP)。
func validDNSList(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	for _, part := range strings.Split(s, ",") {
		if net.ParseIP(strings.TrimSpace(part)) == nil {
			return false
		}
	}
	return true
}

// netconfigCollectCmds 按平台能力生成远程网络采集命令集: NetworkManager 存在才采
// nmcli 连接/WiFi, systemd-resolved 存在才采 resolvectl, 否则回退 ip/resolv.conf。
func netconfigCollectCmds(prof platform.PlatformProfile) map[string]string {
	cmds := map[string]string{
		"interfaces": "ip addr show 2>/dev/null",
		"routes":     "ip route show 2>/dev/null",
		"resolv":     "cat /etc/resolv.conf 2>/dev/null",
	}
	if prof.HasNM {
		cmds["connections"] = "nmcli -t con show 2>/dev/null"
		cmds["wifi"] = "nmcli -t dev wifi 2>/dev/null"
		cmds["nm"] = "nmcli -t dev status 2>/dev/null"
	} else {
		cmds["nm"] = "NetworkManager 未安装, 网络由 ip/系统配置管理"
	}
	if prof.HasResolvectl {
		cmds["dns_resolvectl"] = "resolvectl status 2>/dev/null"
	}
	return cmds
}

// remoteNmcliGet 多条命令合并为单条脚本一次 SSH 往返(哨兵分段),
// 替代逐条开 session 的串行执行; 传输层故障由 ExecScript 自动换新连接重试。
func remoteNmcliGet(rmHost remote.Host, cmds map[string]string) map[string]string {
	keys := make([]string, 0, len(cmds))
	var b strings.Builder
	for k, cmd := range cmds {
		keys = append(keys, k)
		b.WriteString("echo __OPSCORE_" + k + "__\n" + strings.TrimSpace(cmd) + "\n")
	}
	res, err := remotePool.ExecScript(rmHost, b.String())
	if err != nil {
		out := map[string]string{}
		for _, k := range keys {
			out[k] = fmt.Sprintf("(%s error: %v)", k, err)
		}
		return out
	}
	out := map[string]string{}
	for _, k := range keys {
		if v, ok := res[k]; ok && v.Error == "" {
			out[k] = v.Output
		} else {
			out[k] = fmt.Sprintf("(%s error: %s)", k, v.Error)
		}
	}
	return out
}

// gbkToUTF8 将 GBK 编码文本转为 UTF-8(中文 Windows 命令输出为 GBK/CP936)
func gbkToUTF8(s string) string {
	if s == "" || !hasHighByte(s) {
		return s
	}
	out, err := simplifiedchinese.GBK.NewDecoder().String(s)
	if err != nil {
		return s
	}
	return out
}

func hasHighByte(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return true
		}
	}
	return false
}

// windowsDnsLines 从 ipconfig /all 输出中提取 DNS 服务器行。
// 注意: 中文系统输出为 GBK 编码, 不能匹配中文字面量, 只依赖 ASCII 的 "DNS" 前缀与冒号
func windowsDnsLines(ipconfigOut string) string {
	var b strings.Builder
	lines := strings.Split(ipconfigOut, "\n")
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "DNS") && strings.Contains(t, ":") {
			b.WriteString(t + "\n")
			for j := i + 1; j < len(lines); j++ {
				n := strings.TrimSpace(lines[j])
				if n == "" || !isIPv4(n) {
					break
				}
				b.WriteString(n + "\n")
			}
		}
	}
	return strings.TrimSpace(b.String())
}

func isIPv4(s string) bool {
	parts := strings.Split(s, ".")
	if len(parts) != 4 {
		return false
	}
	for _, p := range parts {
		v, err := strconv.Atoi(p)
		if err != nil || v < 0 || v > 255 {
			return false
		}
	}
	return true
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
		prof := remoteProfile(hostID, rmHost)
		connsCmd := "nmcli -t con show 2>/dev/null"
		wifiCmd := "nmcli -t dev wifi 2>/dev/null"
		if !prof.HasNM {
			connsCmd = "ip -o addr show 2>/dev/null"
			wifiCmd = "echo 'NetworkManager 未安装, 无 WiFi 列表'"
		}
		out := remoteNmcliGet(rmHost, map[string]string{
			"connections": connsCmd,
			"wifi":        wifiCmd,
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
			prof := remoteProfile(hostID, rmHost)
			out := remoteNmcliGet(rmHost, netconfigCollectCmds(prof))
			dns := out["dns_resolvectl"]
			if dns == "" || !strings.Contains(dns, "DNS") {
				dns = out["resolv"]
			}
			WriteJSON(w, map[string]any{
				"interfaces": out["interfaces"],
				"routes":     out["routes"],
				"dns":        dns,
				"nm":         out["nm"],
				"platform":   prof,
				"permission": "root",
			})
			return
		}

		// Windows 本机: ipconfig / route print 只读展示, 无 nmcli
		if runtime.GOOS == "windows" {
			ifaces := gbkToUTF8(runCapture("ipconfig", "/all"))
			routes := gbkToUTF8(runCapture("route", "print"))
			WriteJSON(w, map[string]any{
				"interfaces": ifaces,
				"routes":     routes,
				"dns":        windowsDnsLines(ifaces),
				"nm":         "Windows: 网络由 netsh / 系统网络设置管理, 本页只读",
				"permission": "user",
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
				if postBody.Mask > 32 {
					WriteJSON(w, map[string]any{"error": "掩码非法(0-32)", "permission": "remote"})
					return
				}
				cidr += "/" + strconv.Itoa(postBody.Mask)
			}
			if !strings.Contains(cidr, "/") || net.ParseIP(strings.SplitN(cidr, "/", 2)[0]) == nil {
				WriteJSON(w, map[string]any{"error": "ip 格式非法", "permission": "remote"})
				return
			}
			cmd = fmt.Sprintf("nmcli connection modify %s ipv4.addresses %s ipv4.method manual && nmcli connection up %s 2>/dev/null || ip addr add %s dev %s", quote(postBody.Device), quote(cidr), quote(postBody.Device), quote(cidr), quote(postBody.Device))
		case "set-dns":
			if postBody.DNS == "" || postBody.Device == "" {
				WriteJSON(w, map[string]any{"error": "缺少 dns 或 device", "permission": "remote"})
				return
			}
			if !validDNSList(postBody.DNS) {
				WriteJSON(w, map[string]any{"error": "dns 格式非法(须为逗号分隔的 IP 地址)", "permission": "remote"})
				return
			}
			cmd = fmt.Sprintf("nmcli connection modify %s ipv4.dns %s && nmcli connection up %s 2>/dev/null || echo 'nameserver %s' > /etc/resolv.conf", quote(postBody.Device), quote(postBody.DNS), quote(postBody.Device), quote(postBody.DNS))
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
