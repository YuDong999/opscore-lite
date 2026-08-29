package handlers

import (
	"testing"

	"opscore/internal/platform"
)

func TestServiceActionCmd(t *testing.T) {
	cases := []struct {
		init   platform.InitSystem
		action string
		unit   string
		want   []string
	}{
		{platform.InitSystemd, "start", "foo", []string{"systemctl", "start", "foo"}},
		{platform.InitSystemd, "stop", "foo", []string{"systemctl", "stop", "foo"}},
		{platform.InitSystemd, "restart", "foo", []string{"systemctl", "restart", "foo"}},
		{platform.InitOpenRC, "start", "foo", []string{"rc-service", "foo", "start"}},
		{platform.InitOpenRC, "restart", "foo", []string{"rc-service", "foo", "restart"}},
		{platform.InitSysV, "start", "foo", []string{"service", "foo", "start"}},
	}
	for _, c := range cases {
		got := serviceActionCmd(platform.PlatformProfile{Init: c.init}, c.action, c.unit)
		if len(got) != len(c.want) {
			t.Fatalf("%v: 长度 %d 期望 %d (got=%v)", c, len(got), len(c.want), got)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%v: got %v want %v", c, got, c.want)
				break
			}
		}
	}
	// 未知 init → nil (调用方应报错, 而非执行错误命令)
	if got := serviceActionCmd(platform.PlatformProfile{Init: platform.InitUnknown}, "start", "foo"); got != nil {
		t.Errorf("未知 init 应返回 nil, got %v", got)
	}
}

func TestServiceEnableCmd(t *testing.T) {
	cases := []struct {
		init platform.InitSystem
		unit string
		want string
	}{
		{platform.InitSystemd, "foo", "systemctl enable --now foo"},
		{platform.InitOpenRC, "foo", "rc-update add foo && rc-service foo start"},
		{platform.InitSysV, "foo", "chkconfig foo on && service foo start"},
	}
	for _, c := range cases {
		got := serviceEnableCmd(platform.PlatformProfile{Init: c.init}, c.unit)
		if got != c.want {
			t.Errorf("%v: got %q want %q", c, got, c.want)
		}
	}
}

func TestServiceStatusCmd(t *testing.T) {
	if got := serviceStatusCmd(platform.PlatformProfile{Init: platform.InitSystemd}, "foo"); got[0] != "systemctl" {
		t.Errorf("systemd 状态命令应为 systemctl, got %v", got)
	}
	if got := serviceStatusCmd(platform.PlatformProfile{Init: platform.InitOpenRC}, "foo"); got[0] != "rc-service" {
		t.Errorf("openrc 状态命令应为 rc-service, got %v", got)
	}
}
