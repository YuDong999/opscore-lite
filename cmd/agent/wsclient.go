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

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				return
			}
		}
	}()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-done:
			log.Print("连接断开, 准备重连")
			return
		case <-ticker.C:
		}

		select {
		case snap := <-w.snapCh:
			data, _ := json.Marshal(snap)
			msg, _ := json.Marshal(map[string]json.RawMessage{
				"type": json.RawMessage(`"snapshot"`),
				"data": data,
			})
			if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				log.Printf("发送失败: %v", err)
				return
			}
		default:
		}
	}
}
