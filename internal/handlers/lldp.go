package handlers

import (
	"encoding/json"
	"log"
	"net/http"
	"os/exec"
	"strings"
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
			out, _ := exec.Command("systemctl", "is-active", "lldpd").Output()
			lldpdRunning = strings.TrimSpace(string(out)) == "active"
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
	switch body.Action {
	case "install":
		cmd = exec.Command("sh", "-c", "yum install -y lldpd || apt-get install -y lldpd")
	case "start":
		cmd = exec.Command("systemctl", "start", "lldpd")
	case "stop":
		cmd = exec.Command("systemctl", "stop", "lldpd")
	case "restart":
		cmd = exec.Command("systemctl", "restart", "lldpd")
	case "enable":
		cmd = exec.Command("systemctl", "enable", "--now", "lldpd")
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
