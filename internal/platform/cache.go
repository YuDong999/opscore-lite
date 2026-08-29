package platform

import (
	"sync"
	"time"
)

// profileCache 按 hostID 缓存探测结果, 避免每次请求都跑探测脚本.
var (
	profMu    sync.RWMutex
	profCache = map[string]cachedProfile{}
	profTTL   = 10 * time.Minute
)

type cachedProfile struct {
	p   PlatformProfile
	at  time.Time
}

// GetCached 读取缓存(过期返回 false).
func GetCached(hostID string) (PlatformProfile, bool) {
	if hostID == "" {
		return PlatformProfile{}, false
	}
	profMu.RLock()
	c, ok := profCache[hostID]
	profMu.RUnlock()
	if ok && time.Since(c.at) < profTTL {
		return c.p, true
	}
	return PlatformProfile{}, false
}

// SetCached 写入缓存.
func SetCached(hostID string, p PlatformProfile) {
	if hostID == "" {
		return
	}
	profMu.Lock()
	profCache[hostID] = cachedProfile{p: p, at: time.Now()}
	profMu.Unlock()
}

// Invalidate 清除某主机缓存(如主机已变更或重连).
func Invalidate(hostID string) {
	if hostID == "" {
		return
	}
	profMu.Lock()
	delete(profCache, hostID)
	profMu.Unlock()
}

// SetTTL 调整缓存有效期(默认 10 分钟).
func SetTTL(d time.Duration) {
	profTTL = d
}
