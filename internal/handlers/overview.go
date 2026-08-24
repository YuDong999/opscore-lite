package handlers

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"opscore/internal/ansible"
	"opscore/internal/metrics"
)

const localHostID = "_local"

type OverviewHost struct {
	ID        string  `json:"id"`
	Alias     string  `json:"alias"`
	Addr      string  `json:"addr"`
	Online    bool    `json:"online"`
	CPU       float64 `json:"cpuPercent"`
	MemTotal  int64   `json:"memTotal"`
	MemUsed   int64   `json:"memUsed"`
	MemPct    float64 `json:"memPercent"`
	DiskTotal int64   `json:"diskTotal"`
	DiskUsed  int64   `json:"diskUsed"`
	DiskPct   float64 `json:"diskPercent"`
	NetRx     int64   `json:"netRx"`
	NetTx     int64   `json:"netTx"`
	Uptime    int64   `json:"uptime"`
	Hostname  string  `json:"hostname"`
	OS        string  `json:"os"`
	Alert     string  `json:"alert,omitempty"`
}

type nicSample struct {
	rx int64
	tx int64
	ts time.Time
}

var (
	lastNetMu sync.Mutex
	lastNet   = map[string]map[string]nicSample{}
)

func toInt64(s string) int64 {
	v, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return v
}

func toFloat64(s string) float64 {
	v, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return v
}

// netPrevOf 读取指定主机的上一轮网卡采样(并发安全)
func netPrevOf(hostID string) map[string]nicSample {
	lastNetMu.Lock()
	defer lastNetMu.Unlock()
	return lastNet[hostID]
}

func MultiOverview(w http.ResponseWriter, r *http.Request) {
	snap := metrics.Get()
	now := time.Now()

	// ── 本地本机 ──
	localMemTotal := int64(snap.Memory.Total)
	localMemUsed := int64(snap.Memory.Used)
	localDiskTotal := int64(0)
	localDiskUsed := int64(0)
	for _, d := range snap.Disks {
		localDiskTotal += int64(d.Total)
		localDiskUsed += int64(d.Used)
	}

	lastNetMu.Lock()
	netPrev := lastNet
	lastNet = map[string]map[string]nicSample{}
	lastNetMu.Unlock()

	localNetRx := int64(0)
	localNetTx := int64(0)
	localNicData := map[string]nicSample{}
	for _, n := range snap.Net.ByNic {
		if n.Name == "lo" {
			continue
		}
		localNicData[n.Name] = nicSample{rx: int64(n.RxTotal), tx: int64(n.TxTotal)}
		if prev, ok := netPrev[localHostID]; ok {
			if p, ok2 := prev[n.Name]; ok2 {
				elapsed := now.Sub(p.ts).Seconds()
				if elapsed > 0 && elapsed < 30 {
					rx := int64(n.RxTotal)
					tx := int64(n.TxTotal)
					if rx >= p.rx {
						localNetRx += int64(float64(rx-p.rx) / elapsed)
					}
					if tx >= p.tx {
						localNetTx += int64(float64(tx-p.tx) / elapsed)
					}
				}
			}
		}
	}
	localNicData["_ts"] = nicSample{ts: now}
	lastNetMu.Lock()
	lastNet[localHostID] = localNicData
	lastNetMu.Unlock()

	localPct := 0.0
	if localMemTotal > 0 {
		localPct = float64(localMemUsed) / float64(localMemTotal) * 100
	}
	diskPct := 0.0
	if localDiskTotal > 0 {
		diskPct = float64(localDiskUsed) / float64(localDiskTotal) * 100
	}

	// ── 远程主机: 并行探测, 单台离线/SSH超时只影响自己, 不拖慢整体; 按清单顺序落位 ──
	hosts := ansibleMgr.ListHosts()
	remotes := make([]OverviewHost, len(hosts))
	var wg sync.WaitGroup
	for i, h := range hosts {
		wg.Add(1)
		go func(idx int, hh ansible.Host) {
			defer wg.Done()
			remotes[idx] = remoteOverviewHost(hh, now)
		}(i, h)
	}
	wg.Wait()
	overview := append([]OverviewHost{{
		ID:        "本机",
		Alias:     "本机",
		Addr:      "127.0.0.1",
		Online:    true,
		CPU:       snap.CPU.Percent,
		MemTotal:  localMemTotal,
		MemUsed:   localMemUsed,
		MemPct:    localPct,
		DiskTotal: localDiskTotal,
		DiskUsed:  localDiskUsed,
		DiskPct:   diskPct,
		NetRx:     localNetRx,
		NetTx:     localNetTx,
		Uptime:    int64(snap.Host.Uptime),
		Hostname:  snap.Host.Hostname,
		OS:        snap.Host.Platform,
	}}, remotes...)

	out := map[string]interface{}{
		"hosts":   overview,
		"updated": now.Unix(),
	}

	if agentHub != nil {
		alerts := agentHub.GetAlerts()
		if len(alerts) > 0 {
			out["alerts"] = alerts
			for i := range overview {
				if msg, ok := alerts[overview[i].ID]; ok {
					overview[i].Alert = msg
				}
			}
		}
	}

	hasOffline := false
	for _, h := range overview {
		if !h.Online {
			hasOffline = true
			break
		}
	}
	if hasOffline {
		out["message"] = "部分主机离线，请检查 SSH 连接"
	}

	WriteJSON(w, out)
}

// remoteOverviewHost 采集单台远程主机: 优先 agent 缓存(毫秒级), 其次 SSH 回退。
// Agent 离线且 SSH 失败时返回 Online=false 的占位记录, 前端据此显示离线状态。
func remoteOverviewHost(h ansible.Host, now time.Time) OverviewHost {
	rmHost := resolveRemoteHost(h)

	if agentHub != nil {
		if snap, ok := agentHub.GetSnapshot(h.ID); ok {
			o := OverviewHost{
				ID:       rmHost.ID,
				Alias:    rmHost.Alias,
				Addr:     rmHost.Addr,
				Online:   true,
				CPU:      snap.CPU.Percent,
				MemTotal: int64(snap.Memory.Total),
				MemUsed:  int64(snap.Memory.Used),
				MemPct:   snap.Memory.UsedPercent,
				Uptime:   int64(snap.Host.Uptime),
				Hostname: snap.Host.Hostname,
				OS:       snap.Host.Platform,
			}
			for _, d := range snap.Disks {
				o.DiskTotal += int64(d.Total)
				o.DiskUsed += int64(d.Used)
			}
			if o.DiskTotal > 0 {
				o.DiskPct = float64(o.DiskUsed) / float64(o.DiskTotal) * 100
			}
			return o
		}
		if agentHub.IsOnline(h.ID) {
			return OverviewHost{ID: rmHost.ID, Alias: rmHost.Alias, Addr: rmHost.Addr}
		}
	}

	// Agent 离线 → SSH 回退: 走共享快照缓存(与资源页同一份 2s TTL 数据,
	// 单会话一次往返采集, 并发轮询由 singleflight 合并), 不再每次全量重跑
	o := OverviewHost{ID: rmHost.ID, Alias: rmHost.Alias, Addr: rmHost.Addr}
	snap := cachedRemoteSnapshot(h)

	if snap.Host.Hostname == "" {
		return o // 采集失败(传输层错误/主机不可达), 返回离线占位
	}

	o.Online = true
	o.CPU = snap.CPU.Percent

	o.MemTotal = int64(snap.Memory.Total)
	o.MemUsed = int64(snap.Memory.Used)
	if snap.Memory.Total > 0 {
		o.MemPct = float64(snap.Memory.Used) / float64(snap.Memory.Total) * 100
	}

	for _, d := range snap.Disks {
		o.DiskTotal += int64(d.Total)
		o.DiskUsed += int64(d.Used)
	}
	if o.DiskTotal > 0 {
		o.DiskPct = float64(o.DiskUsed) / float64(o.DiskTotal) * 100
	}

	nicData := map[string]nicSample{}
	for _, nic := range snap.Net.ByNic {
		nicData[nic.Name] = nicSample{rx: int64(nic.RxTotal), tx: int64(nic.TxTotal)}
	}

	if prev := netPrevOf(rmHost.ID); prev != nil {
		for iface, cur := range nicData {
			if p, ok2 := prev[iface]; ok2 {
				elapsed := now.Sub(p.ts).Seconds()
				if elapsed > 0 && elapsed < 30 {
					if cur.rx >= p.rx {
						o.NetRx += int64(float64(cur.rx-p.rx) / elapsed)
					}
					if cur.tx >= p.tx {
						o.NetTx += int64(float64(cur.tx-p.tx) / elapsed)
					}
				}
			}
		}
	}

	nicData["_ts"] = nicSample{ts: now}
	lastNetMu.Lock()
	lastNet[rmHost.ID] = nicData
	lastNetMu.Unlock()

	o.Uptime = int64(snap.Host.Uptime)
	o.Hostname = snap.Host.Hostname

	osLines := strings.Split(snap.Host.Platform, "\n")
	for _, l := range osLines {
		if strings.HasPrefix(l, "NAME=") {
			o.OS = strings.TrimPrefix(l, "NAME=")
		} else if strings.HasPrefix(l, "VERSION=") {
			o.OS += " " + strings.TrimPrefix(l, "VERSION=")
		}
	}

	return o
}
