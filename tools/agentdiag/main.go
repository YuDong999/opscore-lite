package main

import (
	"encoding/json"
	"fmt"
	"os"

	"opscore/internal/remote"
)

type host struct {
	ID       string `json:"id"`
	Addr     string `json:"addr"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	Password string `json:"password"`
}

func main() {
	data, err := os.ReadFile("bin/data/ansible_hosts.json")
	if err != nil {
		fmt.Println("read hosts:", err)
		return
	}
	var hosts []host
	if err := json.Unmarshal(data, &hosts); err != nil {
		fmt.Println("parse hosts:", err)
		return
	}

	for _, h := range hosts {
		rh := remote.Host{ID: h.ID, Addr: h.Addr, Port: h.Port, User: h.User, Password: h.Password}
		cmds := map[string]string{
			"ps":       "ps aux | grep -E 'opscore' | grep -v grep; echo ---",
			"listeners": "ss -tlnp 2>/dev/null | grep -E ':8088|:8089'; echo ---",
			"svcfile":  "cat /etc/systemd/system/opscore-agent.service 2>/dev/null; echo ---",
			"svc":      "systemctl status opscore-agent --no-pager -l 2>&1 | head -25; echo ---",
			"proddata": "ls -la /opt/opscore/data/ 2>/dev/null; cat /opt/opscore/data/ansible_hosts.json 2>/dev/null; echo ---",
			"produnit": "systemctl list-units --all 2>/dev/null | grep -iE 'opscore'; systemctl status opscore --no-pager -l 2>&1 | head -15; echo ---",
			"agentbin": "ls -la /opt/opscore/bin/ 2>/dev/null; ls -la /media/opscore-lite/bin/ 2>/dev/null; echo ---",
		}
		fmt.Printf("\n========== %s (%s) ==========\n", h.ID, h.Addr)
		res := remote.ExecOnHost(rh, cmds)
		for k, r := range res {
			fmt.Printf("---- %s ----\n%s\n", k, r.Output)
			if r.Error != "" {
				fmt.Printf("ERR: %s\n", r.Error)
			}
		}
	}
}
