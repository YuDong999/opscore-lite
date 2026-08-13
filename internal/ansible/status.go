package ansible

import (
	"net"
	"strconv"
	"strings"
	"time"
)

// CheckOnline 探测主机在线状态: TCP 连接管理端口, Linux SSH 端口额外读取 banner 确认 SSH 服务
func (h Host) CheckOnline() bool {
	if h.IsLocal {
		return true
	}
	addr := net.JoinHostPort(h.Addr, strconv.Itoa(h.defaultPort()))
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return false
	}
	defer conn.Close()
	// win (WinRM) 或非 22 端口: TCP 通即视为在线
	if h.Platform == "win" || h.defaultPort() != 22 {
		return true
	}
	conn.SetReadDeadline(time.Now().Add(1 * time.Second))
	buf := make([]byte, 8)
	n, _ := conn.Read(buf)
	return n >= 4 && strings.HasPrefix(string(buf[:n]), "SSH-")
}
