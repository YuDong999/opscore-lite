package handlers

import (
	"encoding/json"
	"log"
	"net/http"
	"os/exec"
	"strings"

	"opscore/internal/platform"
)

type LldpNeighbor struct {
	Interface string `json:"interface"`
	ChassisID string `json:"chassisId"`
	PortID    string `json:"portId"`
	SysName   string `json:"sysName"`
	SysDesc   string `json:"sysDesc"`
	TTL       string `json:"ttl"`
	VLAN      string `json:"vlan"`
}

func LldpHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		lldpdInstalled := false
		lldpdRunning := false
		if _, err := exec.LookPath("lldpctl"); err == nil {
			lldpdInstalled = true
			out, _ := exec.Command("lldpctl").Output()
			lldpdRunning = strings.Contains(string(out), "LLDP")
		} else if _, err := exec.LookPath("lldpd"); err == nil {
			lldpdInstalled = true
			switch platform.DetectLocal().Init {
			case platform.InitSystemd:
				out, _ := exec.Command("systemctl", "is-active", "lldpd").Output()
				lldpdRunning = strings.TrimSpace(string(out)) == "active"
			case platform.InitOpenRC:
				out, _ := exec.Command("rc-service", "lldpd", "status").Output()
				lldpdRunning = strings.Contains(string(out), "started") || strings.Contains(string(out), "active")
			default:
				out, _ := exec.Command("pgrep", "-x", "lldpd").Output()
				lldpdRunning = strings.TrimSpace(string(out)) != ""
			}
		}

		neighbors := parseLldpctl()
		WriteJSON(w, map[string]any{
			"installed": lldpdInstalled,
			"running":   lldpdRunning,
			"neighbors": neighbors,
		})
		return
	}

	if r.Method != "POST" {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var body struct {
		Action string `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Action == "" {
		WriteJSON(w, map[string]any{"ok": false, "error": "缺少 action"})
		return
	}

	var cmd *exec.Cmd
	prof := platform.DetectLocal()
	switch body.Action {
	case "install":
		instCmd, ok := platform.PkgInstallCmd(prof, "lldpd")
		if !ok {
			WriteJSON(w, map[string]any{"ok": false, "error": "当前发行版不支持自动安装 lldpd"})
			return
		}
		cmd = exec.Command("sh", "-c", instCmd)
	case "start", "stop", "restart":
		argv := serviceActionCmd(prof, body.Action, "lldpd")
		if argv == nil {
			WriteJSON(w, map[string]any{"ok": false, "error": "当前初始化系统不支持服务管理"})
			return
		}
		cmd = exec.Command(argv[0], argv[1:]...)
	case "enable":
		cmd = exec.Command("sh", "-c", serviceEnableCmd(prof, "lldpd"))
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

func parseLldpctl() []LldpNeighbor {
	out, err := exec.Command("lldpctl", "-f", "keyvalue").Output()
	if err != nil {
		return nil
	}
	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return nil
	}

	// lldpctl -f keyvalue 输出:
	// lldp.eth0.chassis-id=xx
	// lldp.eth0.port-id=xx
	// lldp.eth0.vlan=xx
	entries := make(map[string]map[string]string)
	for _, line := range strings.Split(raw, "\n") {
		kv := strings.SplitN(line, "=", 2)
		if len(kv) != 2 {
			continue
		}
		parts := strings.SplitN(kv[0], ".", 3)
		if len(parts) < 3 {
			continue
		}
		iface := parts[1]
		key := parts[2]
		if entries[iface] == nil {
			entries[iface] = make(map[string]string)
		}
		entries[iface][key] = kv[1]
	}

	var neighbors []LldpNeighbor
	for iface, data := range entries {
		n := LldpNeighbor{
			Interface: iface,
			ChassisID: data["chassis-id"],
			PortID:    data["port-id"],
			SysName:   data["sys-name"],
			SysDesc:   data["sys-desc"],
			TTL:       data["ttl"],
			VLAN:      data["vlan"],
		}
		if n.SysName == "" && n.ChassisID == "" {
			continue
		}
		neighbors = append(neighbors, n)
	}
	return neighbors
}

func init() {
	log.Println("[lldp] handler loaded — supports lldpctl parsing + systemctl management")
}
