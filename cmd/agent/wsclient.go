package main

import (
	"encoding/json"
	"log"
	"time"

	"github.com/gorilla/websocket"
	"opscore/internal/metrics"
)

type wsClient struct {
	server string
	hostID string
	snapCh chan metrics.Snapshot
}

func newWSClient(server, hostID string) *wsClient {
	return &wsClient{
		server: server,
		hostID: hostID,
		snapCh: make(chan metrics.Snapshot, 16),
	}
}

func (w *wsClient) pushSnapshot(snap metrics.Snapshot) {
	select {
	case w.snapCh <- snap:
	default:
	}
}

func (w *wsClient) run(stop chan struct{}) {
	addr := w.server
	backoff := time.Second

	for {
		select {
		case <-stop:
			return
		default:
		}

		conn, _, err := websocket.DefaultDialer.Dial(addr, nil)
		if err != nil {
			log.Printf("连接失败(%s), %ds后重试: %v", addr, int(backoff.Seconds()), err)
			select {
			case <-stop:
				return
			case <-time.After(backoff):
			}
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
			continue
		}
		backoff = time.Second

		reg, _ := json.Marshal(map[string]string{"type": "register", "hostID": w.hostID})
		conn.WriteMessage(websocket.TextMessage, reg)

		log.Printf("已连接到 %s (hostID: %s)", addr, w.hostID)

		w.pump(conn, stop)
	}
}

func (w *wsClient) pump(conn *websocket.Conn, stop chan struct{}) {
	defer conn.Close()

	// 读截止: 每收到任何消息(注册回执/pong)续期; 服务端对 ping 会回 pong,
	// 因此只要链路活着, 读侧每 ~10s 内必有流量, 死链最迟 90s 被发现并重连。
	const readWait = 90 * time.Second
	conn.SetReadDeadline(time.Now().Add(readWait))
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(readWait)) })

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				return
			}
			conn.SetReadDeadline(time.Now().Add(readWait))
		}
	}()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	// 首次重采集(services/processes/network)可能耗时 >60s, 期间无快照可发 ——
	// 每 10s 补一个应用层心跳, 防止服务端把"正在慢采集"误判为掉线。
	const heartbeatEvery = 10 * time.Second
	lastSent := time.Now()

	for {
		select {
		case <-stop:
			return
		case <-done:
			log.Print("连接断开, 准备重连")
			return
		case <-ticker.C:
		}

		sent := false
		select {
		case snap := <-w.snapCh:
			data, _ := json.Marshal(snap)
			msg, _ := json.Marshal(map[string]json.RawMessage{
				"type": json.RawMessage(`"snapshot"`),
				"data": data,
			})
			conn.SetWriteDeadline(time.Now().Add(15 * time.Second))
			if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				log.Printf("发送失败: %v", err)
				return
			}
			lastSent = time.Now()
			sent = true
		default:
		}
		if !sent && time.Since(lastSent) >= heartbeatEvery {
			hb, _ := json.Marshal(map[string]string{"type": "ping"})
			conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if err := conn.WriteMessage(websocket.TextMessage, hb); err != nil {
				log.Printf("心跳发送失败: %v", err)
				return
			}
			lastSent = time.Now()
		}
	}
}
