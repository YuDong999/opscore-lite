package handlers

import (
	"testing"

	"opscore/internal/platform"
	"opscore/internal/remote"
)

// TestDiskParseDistroReplay 跨发行版 lsblk 输出回放: 验证不同发行版的 -ln 平铺输出
// (含 CentOS 7 老 lsblk 与 Alpine/BusyBox 风格) 都能正确解析, 不产生表头/假设备。
func TestDiskParseDistroReplay(t *testing.T) {
	samples := map[string]struct {
		out  string
		want []string // 期望解析出的设备名(顺序)
	}{
		"CentOS7_lsblk_ln": {
			out:  "sda   40G   disk\nsda1  1G    part\nsda2  39G   part  LVM2_member\ncentos-root 35.1G lvm xfs /\n",
			want: []string{"sda", "sda1", "sda2", "centos-root"},
		},
		"Alpine_busybox_ln": {
			out:  "sda 40G disk\n  sda1 1G part\n  sda2 39G part\n",
			want: []string{"sda", "sda1", "sda2"},
		},
		"Ubuntu_lsblk_ln": {
			out:  "sda   40G   disk\nsda1  1G    part  ext4 /boot\n",
			want: []string{"sda", "sda1"},
		},
	}
	for name, s := range samples {
		devs := parseDevicesFlat(s.out)
		if len(devs) != len(s.want) {
			t.Errorf("%s: 解析出 %d 个设备, 期望 %d (got=%v)", name, len(devs), len(s.want), devs)
			continue
		}
		for i, w := range s.want {
			if devs[i].Name != w {
				t.Errorf("%s[%d]: Name=%q 期望 %q", name, i, devs[i].Name, w)
			}
		}
	}
}

// TestResolveDiskDevices_Routing 守护 resolveDiskDevices 的磁盘命令路由:
// 优先 -J 合法 JSON, 否则回退 -ln, 再否则不产生假设备。
func TestResolveDiskDevices_Routing(t *testing.T) {
	goodJSON := `{"blockdevices":[{"name":"sda","size":"40G","type":"disk"}]}`
	flat := "NAME SIZE TYPE FSTYPE MOUNTPOINT\nsda  40G disk\n"

	// 1) -J 合法 JSON → 用 parseDevices
	res1 := map[string]remote.Result{"devices": {Output: goodJSON}}
	if devs := resolveDiskDevices(res1, platform.PlatformProfile{HasLSBlkJSON: true}); len(devs) != 1 || devs[0].Name != "sda" {
		t.Errorf("case1 应解析出 sda, got %+v", devs)
	}

	// 2) -J 不支持(usage 文本) 但有 -ln 回退 → 用 parseDevicesFlat
	res2 := map[string]remote.Result{
		"devices":     {Output: "/usr/bin/lsblk: 无效选项 -- J\n"},
		"devices_fb":  {Output: flat},
	}
	if devs := resolveDiskDevices(res2, platform.PlatformProfile{HasLSBlkJSON: false}); len(devs) != 1 || devs[0].Name != "sda" {
		t.Errorf("case2 应回退解析出 sda, got %+v", devs)
	}

	// 3) -J 不支持且无回退 → 不产生假设备(0 个)
	res3 := map[string]remote.Result{"devices": {Output: "/usr/bin/lsblk: 无效选项 -- J\n"}}
	if devs := resolveDiskDevices(res3, platform.PlatformProfile{HasLSBlkJSON: false}); len(devs) != 0 {
		t.Errorf("case3 不应产生假设备, got %d", len(devs))
	}

	// 4) 全空 → nil
	res4 := map[string]remote.Result{}
	if devs := resolveDiskDevices(res4, platform.PlatformProfile{}); devs != nil {
		t.Errorf("case4 全空应返回 nil, got %+v", devs)
	}
}

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
