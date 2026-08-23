package agent

import (
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"opscore/internal/metrics"
)

type cachedSnapshot struct {
	Snap      metrics.Snapshot
	Timestamp time.Time
}

type agentConn struct {
	hostID   string
	conn     *websocket.Conn
	lastSeen time.Time
}

type AgentHub struct {
	mu     sync.RWMutex
	conns  map[string]*agentConn
	cache  map[string]cachedSnapshot
	alerts map[string]string // hostID → 告警消息 (如 "SSH 无法连接" / "Agent 部署失败")
}

type wsMessage struct {
	Type   string          `json:"type"`
	HostID string          `json:"hostID,omitempty"`
	Data   json.RawMessage `json:"data,omitempty"`
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true
		}
		u, err := url.Parse(origin)
		if err != nil {
			return false
		}
		switch u.Hostname() {
		case "localhost", "127.0.0.1", "::1":
			return true
		}
		return false
	},
}

func NewHub() *AgentHub {
	h := &AgentHub{
		conns:  make(map[string]*agentConn),
		cache:  make(map[string]cachedSnapshot),
		alerts: make(map[string]string),
	}
	go h.cleanLoop()
	return h
}

func (h *AgentHub) ServeWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[agent] upgrade failed: %v", err)
		return
	}

	var hostID string
	defer func() {
		if hostID != "" {
			h.mu.Lock()
			delete(h.conns, hostID)
			delete(h.cache, hostID)
			h.mu.Unlock()
			log.Printf("[agent] %s disconnected", hostID)
		}
		conn.Close()
	}()

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			break
		}

		var msg wsMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}

		switch msg.Type {
		case "register":
			hostID = msg.HostID
			h.mu.Lock()
			if old, ok := h.conns[hostID]; ok {
				old.conn.Close()
				delete(h.cache, hostID)
				log.Printf("[agent] %s replaced old connection", hostID)
			}
			delete(h.alerts, hostID)
			h.conns[hostID] = &agentConn{hostID: hostID, conn: conn, lastSeen: time.Now()}
			h.mu.Unlock()
			log.Printf("[agent] %s registered", hostID)
			conn.WriteJSON(map[string]string{"type": "registered"})

		case "snapshot":
			if hostID == "" {
				continue
			}
			var snap metrics.Snapshot
			if err := json.Unmarshal(msg.Data, &snap); err != nil {
				continue
			}
			h.mu.Lock()
			if c, ok := h.conns[hostID]; ok {
				c.lastSeen = time.Now()
			}
			h.cache[hostID] = cachedSnapshot{Snap: snap, Timestamp: time.Now()}
			h.mu.Unlock()
		}
	}
}

func (h *AgentHub) GetSnapshot(hostID string) (*metrics.Snapshot, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	c, ok := h.cache[hostID]
	if !ok {
		return nil, false
	}
	return &c.Snap, true
}

func (h *AgentHub) IsOnline(hostID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	c, ok := h.conns[hostID]
	if !ok {
		return false
	}
	return time.Since(c.lastSeen) < 30*time.Second
}

// OnlineIDs 返回最近 30 秒内活跃的 agent 主机 ID 列表。
func (h *AgentHub) OnlineIDs() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := []string{}
	for id, c := range h.conns {
		if time.Since(c.lastSeen) < 30*time.Second {
			out = append(out, id)
		}
	}
	return out
}

// NeedsUpdate 判断在线 agent 上报的版本是否与当前 server 期望版本一致。
func (h *AgentHub) NeedsUpdate(hostID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	c, ok := h.cache[hostID]
	if !ok {
		return false
	}
	return c.Snap.AgentVersion != metrics.AgentVersion
}

func (h *AgentHub) RemoveHost(hostID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if c, ok := h.conns[hostID]; ok {
		c.conn.Close()
		delete(h.conns, hostID)
	}
	delete(h.cache, hostID)
	delete(h.alerts, hostID)
}

func (h *AgentHub) SetAlert(hostID, msg string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.alerts[hostID] = msg
}

func (h *AgentHub) ClearAlert(hostID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.alerts, hostID)
}

func (h *AgentHub) GetAlerts() map[string]string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make(map[string]string, len(h.alerts))
	for k, v := range h.alerts {
		out[k] = v
	}
	return out
}

func (h *AgentHub) cleanLoop() {
	for {
		time.Sleep(15 * time.Second)
		h.mu.Lock()
		now := time.Now()
		for id, c := range h.conns {
			if now.Sub(c.lastSeen) > 60*time.Second {
				c.conn.Close()
				delete(h.conns, id)
				delete(h.cache, id)
				delete(h.alerts, id)
				log.Printf("[agent] %s cleaned (timeout)", id)
			}
		}
		h.mu.Unlock()
	}
}
