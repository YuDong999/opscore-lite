package handlers

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// 通用 GET 响应缓存: 切换模块/主机的重复读请求在 TTL 内直接回放缓存体,
// 过期后经 singleflight 合并重建 —— 与前端 SWR 缓存配合, 消除切换卡顿。
// 仅用于"只读、幂等、短 TTL 可接受"的端点; 写操作与日志尾随绝不使用。

type respCacheEntry struct {
	body []byte
	at   time.Time
}

var (
	respMu       sync.Mutex
	respCache    = map[string]respCacheEntry{}
	respInflight = map[string]*respCall{}
)

type respCall struct {
	wg   sync.WaitGroup
	body []byte
}

// ServeCachedJSON 以 r.URL(含query) 为键缓存 build() 的 JSON 输出。
// 命中未过期 → 直接回放; 过期/缺失 → singleflight 合并并发重建。
func ServeCachedJSON(w http.ResponseWriter, r *http.Request, ttl time.Duration, build func() any) {
	key := "fw|" + r.URL.RequestURI()

	respMu.Lock()
	if e, ok := respCache[key]; ok && time.Since(e.at) < ttl {
		respMu.Unlock()
		writeCachedJSON(w, e.body)
		return
	}
	if c, ok := respInflight[key]; ok {
		respMu.Unlock()
		c.wg.Wait()
		writeCachedJSON(w, c.body)
		return
	}
	call := &respCall{}
	call.wg.Add(1)
	respInflight[key] = call
	respMu.Unlock()

	body, _ := json.Marshal(build())

	respMu.Lock()
	respCache[key] = respCacheEntry{body: body, at: time.Now()}
	delete(respInflight, key)
	respMu.Unlock()
	call.body = body
	call.wg.Done()

	writeCachedJSON(w, body)
}

func writeCachedJSON(w http.ResponseWriter, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.Write(body)
}
