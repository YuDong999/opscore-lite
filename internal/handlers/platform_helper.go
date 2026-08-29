package handlers

import (
	"opscore/internal/platform"
	"opscore/internal/remote"
)

// remoteProfile 探测(或读缓存)远程主机的平台能力.
// 这是"agent 根据探测结果复用清单中对应发行版本去获取对方主机数据"的落点:
// 任何远程采集前先取得 PlatformProfile, 后续命令与解析都按它路由.
func remoteProfile(hostID string, h remote.Host) platform.PlatformProfile {
	if p, ok := platform.GetCached(hostID); ok {
		return p
	}
	res := remotePool.Exec(h, map[string]string{"probe": platform.ProbeScript})
	p := platform.ParseProbe(res["probe"].Output)
	platform.SetCached(hostID, p)
	return p
}

// resolveDiskDevices 按探测结果与输出合法性解析磁盘列表:
// 优先 lsblk -J 的合法 JSON, 否则回退 -ln 平铺(老 util-linux 不支持 -J 时).
func resolveDiskDevices(res map[string]remote.Result, prof platform.PlatformProfile) []DeviceInfo {
	if dev, ok := res["devices"]; ok && dev.Error == "" && looksLikeLSBlkJSON(dev.Output) {
		return parseDevices(dev.Output)
	}
	if fb, ok := res["devices_fb"]; ok && fb.Error == "" {
		return parseDevicesFlat(fb.Output)
	}
	if dev, ok := res["devices"]; ok && dev.Error == "" {
		// 主命令 lsblk -J 在老版本会输出 usage 文本而非 JSON → 当作平铺解析
		return parseDevicesFlat(dev.Output)
	}
	return nil
}
