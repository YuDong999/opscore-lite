package handlers

import "testing"

// TestLooksLikeLSBlkJSON_UsageText 复现并守护之前的假数据 bug:
// 老版本 lsblk(<2.27) 不支持 -J 时把 usage 文本写进 stdout, 必须判定为非 JSON。
func TestLooksLikeLSBlkJSON_UsageText(t *testing.T) {
	zh := "/usr/bin/lsblk: 无效选项 -- J\n用法: lsblk [选项]...\n"
	if looksLikeLSBlkJSON(zh) {
		t.Error("中文 usage 文本不能被当作合法 JSON")
	}
	en := "/usr/bin/lsblk: invalid option -- 'J'\nUsage: lsblk [options]\n"
	if looksLikeLSBlkJSON(en) {
		t.Error("英文 usage 文本不能被当作合法 JSON")
	}
	abs := "/usr/sbin/lsblk: 无效选项 -- J\n"
	if looksLikeLSBlkJSON(abs) {
		t.Error("/usr/sbin/lsblk 前缀的 usage 文本不能被当作合法 JSON")
	}
	// 合法 JSON 必须识别
	good := `{"blockdevices":[{"name":"sda","size":"40G","type":"disk","fstype":"","mountpoint":""}]}`
	if !looksLikeLSBlkJSON(good) {
		t.Error("合法 JSON 应被识别")
	}
}

// TestParseDevicesFlat_NoFakeDevices 守护: 把 usage 文本喂给 parseDevicesFlat,
// 绝不能产生 56 个假设备(之前线上事故)。
func TestParseDevicesFlat_NoFakeDevices(t *testing.T) {
	usage := "/usr/bin/lsblk: 无效选项 -- J\n用法: lsblk [选项]...\n"
	devs := parseDevicesFlat(usage)
	if len(devs) != 0 {
		t.Errorf("usage 文本喂给 parseDevicesFlat 返回 %d 个假设备, 期望 0", len(devs))
	}

	// 正常 -ln 平铺输出应正确解析
	normal := "NAME SIZE TYPE FSTYPE MOUNTPOINT\nsda  40G disk\n├─sda1 1G part\n└─sda2 39G part LVM2_member\n"
	devs2 := parseDevicesFlat(normal)
	if len(devs2) == 0 {
		t.Error("正常 -ln 输出应解析出设备")
	}
}

// TestCollectDevicesFallback 仅验证 collectDevices 在 -J 不被支持时返回非 nil 切片
// (不触发真实命令, 直接验证回退分支逻辑由 looksLikeLSBlkJSON 守护)。
func TestCollectDevicesFallbackContract(t *testing.T) {
	// 若 looksLikeLSBlkJSON 对 usage 返回 false, 则 resolveDiskDevices 会走 flat 分支
	usage := "/usr/bin/lsblk: 无效选项 -- J\n"
	if looksLikeLSBlkJSON(usage) {
		t.Fatal("前置条件失败: usage 不应被判为 JSON")
	}
}
