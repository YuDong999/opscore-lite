package ansible

import (
	"net"
	"os"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

// icmpPing 发送一次 ICMP Echo 探测, 返回往返耗时。
// 需要原始套接字权限 (Windows 管理员 / Linux root), 无权限或超时均返回错误, 由调用方降级为 TCP 探测。
func icmpPing(addr string, timeout time.Duration) (time.Duration, error) {	conn, err := icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		return 0, err
	}
	defer conn.Close()

	dst, err := net.ResolveIPAddr("ip4", addr)
	if err != nil {
		return 0, err
	}

	id := os.Getpid() & 0xffff
	msg := icmp.Message{
		Type: ipv4.ICMPTypeEcho,
		Code: 0,
		Body: &icmp.Echo{ID: id, Seq: 1, Data: []byte("opscore-ping")},
	}
	b, err := msg.Marshal(nil)
	if err != nil {
		return 0, err
	}

	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return 0, err
	}
	start := time.Now()
	if _, err := conn.WriteTo(b, dst); err != nil {
		return 0, err
	}

	rb := make([]byte, 1500)
	for {
		n, peer, err := conn.ReadFrom(rb)
		if err != nil {
			return 0, err
		}
		rm, err := icmp.ParseMessage(ipv4.ICMPTypeEchoReply.Protocol(), rb[:n])
		if err != nil {
			continue
		}
		if rm.Type == ipv4.ICMPTypeEchoReply {
			if e, ok := rm.Body.(*icmp.Echo); ok && e.ID == id && peer.String() == dst.String() {
				return time.Since(start), nil
			}
		}
	}
}

// PingIP 导出 ICMP 探测包装, 供网络拓扑扫描等复用。
// 无原始套接字权限 / 超时 / 不可达均返回错误。
func PingIP(addr string, timeout time.Duration) error {
	_, err := icmpPing(addr, timeout)
	return err
}
