package handlers

import (
	"sync"
	"time"

	"opscore/internal/ansible"
	"opscore/internal/metrics"
)

// 远程主机快照缓存: 资源页每 2s 轮询 + 总览页并发拉取,
// 若每次都真实 SSH 采集(串行10条命令)会造成重复阻塞。
// 这里按 hostID 做 TTL 缓存, 并用 singleflight 合并同 host 的并发采集。

const snapshotTTL = 2 * time.Second

type snapCacheEntry struct {
	snap *metrics.Snapshot
	at   time.Time
}

type snapCall struct {
	wg   sync.WaitGroup
	snap *metrics.Snapshot
}

var (
	snapMu       sync.Mutex
	snapCache    = map[string]*snapCacheEntry{}
	snapInflight = map[string]*snapCall{}
)

// cachedRemoteSnapshot 返回远程主机快照:
//   - 缓存未过期 -> 直接返回(TTL 内多次轮询只执行一次 SSH)
//   - 已过期     -> 发起采集; 同一主机的并发请求合并等待同一份结果
//
// 仅包装 SSH 回退路径; agent 在线路径(resources/overview 里)仍优先于本函数。
func cachedRemoteSnapshot(h ansible.Host) *metrics.Snapshot {
	rmHost := resolveRemoteHost(h)
	now := time.Now()

	snapMu.Lock()
	if e, ok := snapCache[rmHost.ID]; ok && now.Sub(e.at) < snapshotTTL {
		snapMu.Unlock()
		return e.snap
	}
	if c, ok := snapInflight[rmHost.ID]; ok {
		snapMu.Unlock()
		c.wg.Wait()
		return c.snap
	}
	call := &snapCall{}
	call.wg.Add(1)
	snapInflight[rmHost.ID] = call
	snapMu.Unlock()

	snap := remoteResourceSnapshot(h)

	snapMu.Lock()
	snapCache[rmHost.ID] = &snapCacheEntry{snap: snap, at: time.Now()}
	delete(snapInflight, rmHost.ID)
	snapMu.Unlock()

	call.snap = snap
	call.wg.Done()
	return snap
}

// invalidateSnapshot 主机删除/编辑时清理缓存与在途连接, 避免指向旧地址。
func invalidateSnapshot(hostID string) {
	snapMu.Lock()
	delete(snapCache, hostID)
	snapMu.Unlock()
}
