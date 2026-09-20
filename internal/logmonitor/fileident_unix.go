//go:build !windows

package logmonitor

import (
	"os"
	"strconv"
	"syscall"
)

// fileIdentity 返回 "dev:ino" 形式的文件身份, 用于识别日志轮转(改名/重建会换 inode)。
// 取不到身份(非 unix 语义文件系统)返回空串, 调用方按"无身份信息"降级处理。
func fileIdentity(fi os.FileInfo) string {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	return strconv.FormatUint(uint64(st.Dev), 10) + ":" + strconv.FormatUint(st.Ino, 10)
}
