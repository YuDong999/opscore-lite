//go:build windows

package logmonitor

import "os"

// fileIdentity Windows 侧不提供 inode 语义, 恒返回空串。
// 影响面: 文件源的轮转判据在该平台退化为 size 比较(截断可识别, rename/create 不可识别)。
// Windows 仅作为部署平台, 不承担日志采集, 故不实现卷序列号+文件索引。
func fileIdentity(fi os.FileInfo) string { return "" }
