package handlers

import "testing"

func TestBackendFromProbeText(t *testing.T) {
	cases := map[string]string{
		"ufw":                     "ufw",
		"firewalld":               "firewalld",
		"iptables":                "iptables",
		"none":                    "none",
		" ufw\n":                  "ufw",
		"/usr/bin/ufw: 无效选项\n": "none", // 错误文本归 none
		"":                        "none",
		"firewalld\nextra":        "none", // 多行非纯后端名
		"iptables\n":              "iptables",
	}
	for in, want := range cases {
		if got := backendFromProbeText(in); got != want {
			t.Errorf("backendFromProbeText(%q)=%q want %q", in, got, want)
		}
	}
}

// TestBuildFirewallCommand_IptablesUnmanaged 守护: 裸 iptables 后端下不应生成可执行写命令。
func TestBuildFirewallCommand_IptablesUnmanaged(t *testing.T) {
	for _, action := range []string{"allow-port", "deny-port", "allow-ip", "deny-ip", "start", "stop", "restart"} {
		args, _ := buildFirewallCommand("iptables", fwCmdParams{Action: action, Port: "22", Proto: "tcp", CIDR: "1.2.3.4/32"})
		if args != nil {
			t.Errorf("iptables 后端不应生成 %s 命令, got %v", action, args)
		}
	}
}

// TestBuildFirewallCommand_Firewalld 守护 firewalld 各 action 命令拼装正确。
func TestBuildFirewallCommand_Firewalld(t *testing.T) {
	allow, _ := buildFirewallCommand("firewalld", fwCmdParams{Action: "allow-port", Port: "8080", Proto: "tcp"})
	if len(allow) == 0 || allow[0] != "firewall-cmd" {
		t.Errorf("allow-port firewalld 命令异常: %v", allow)
	}
	start, _ := buildFirewallCommand("firewalld", fwCmdParams{Action: "start"})
	if len(start) == 0 || start[0] != "systemctl" {
		t.Errorf("start firewalld 命令异常: %v", start)
	}
}
