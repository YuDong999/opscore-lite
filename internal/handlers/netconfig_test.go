package handlers

import (
	"testing"

	"opscore/internal/platform"
)

// TestNetconfigCollectCmds 守护网络采集命令按能力路由:
// NetworkManager 存在才采 nmcli, systemd-resolved 存在才采 resolvectl。
func TestNetconfigCollectCmds(t *testing.T) {
	// 完整能力: 含 nmcli 连接/WiFi 与 resolvectl
	full := netconfigCollectCmds(platform.PlatformProfile{HasNM: true, HasResolvectl: true})
	if full["connections"] == "" || full["wifi"] == "" || full["nm"] == "" {
		t.Error("HasNM=true 应采集 connections/wifi/nm")
	}
	if full["dns_resolvectl"] == "" {
		t.Error("HasResolvectl=true 应采集 dns_resolvectl")
	}
	if full["interfaces"] == "" || full["routes"] == "" || full["resolv"] == "" {
		t.Error("interfaces/routes/resolv 应始终采集")
	}

	// 无 NetworkManager: 不采 nmcli, nm 字段给出提示
	noNM := netconfigCollectCmds(platform.PlatformProfile{HasNM: false, HasResolvectl: true})
	if _, ok := noNM["connections"]; ok {
		t.Error("HasNM=false 不应采集 connections")
	}
	if _, ok := noNM["wifi"]; ok {
		t.Error("HasNM=false 不应采集 wifi")
	}
	if noNM["nm"] == "" {
		t.Error("HasNM=false 应给出非 nmcli 提示文本")
	}

	// 无 systemd-resolved: 不采 dns_resolvectl, 但仍有 resolv.conf
	noRes := netconfigCollectCmds(platform.PlatformProfile{HasNM: true, HasResolvectl: false})
	if _, ok := noRes["dns_resolvectl"]; ok {
		t.Error("HasResolvectl=false 不应采集 dns_resolvectl")
	}
	if noRes["resolv"] == "" {
		t.Error("无 resolvectl 时仍应采集 resolv.conf")
	}
}
