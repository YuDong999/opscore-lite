//go:build !linux

package metrics

// CollectLimits 非 Linux 平台没有 /proc: 线程/conntrack/文件句柄这些计数拿不到,
// 返回 nil 让前端整块不显示 —— 而不是显示一排 0 假装采到了。
// (与日志监控的 fileIdentity 同一原则: Windows 只作部署平台, 不承担采集)
func CollectLimits() *LimitInfo { return nil }
