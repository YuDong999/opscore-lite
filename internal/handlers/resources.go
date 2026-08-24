package handlers

import (
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"opscore/internal/ansible"
	"opscore/internal/metrics"
	"opscore/internal/remote"
)

func remoteResourceSnapshot(host ansible.Host) *metrics.Snapshot {
	rmHost := resolveRemoteHost(host)
	// 单条 SSH 会话一次往返采集全部指标(替代逐条开 session 的串行执行)
	r, err := remotePool.ExecScript(rmHost, remote.SnapshotScript)
	snap := &metrics.Snapshot{Timestamp: time.Now().Unix()}
	if err != nil {
		// 记录真实失败原因(拨号/会话/传输), 便于排查间歇性 502
		log.Printf("[resources] 主机 %s(%s:%d) 快照采集失败: %v", rmHost.Alias, rmHost.Addr, rmHost.Port, err)
		return snap
	}
	// 关键段缺失同样记一条(脚本异常/输出被截断)
	if r["Hostname"].Output == "" {
		log.Printf("[resources] 主机 %s(%s) 快照输出异常: sections=%d", rmHost.Alias, rmHost.Addr, len(r))
	}

	snap.Host.Hostname = r["Hostname"].Output
	snap.Host.Platform = r["OsRelease"].Output
	snap.Host.Uptime = parseUint64(r["Uptime"].Output)

	snap.CPU.Percent = parseFloat64(r["CpuUsage"].Output)
	snap.CPU.Cores = int(parseUint64(r["CpuCores"].Output))
	snap.CPU.Model = r["CpuModel"].Output
	// 每核占用: 与总占用共用同一组 /proc/stat 双采样, 空格分隔的百分比列表
	snap.CPU.PerCore = []float64{}
	for _, s := range strings.Fields(r["CpuPerCore"].Output) {
		snap.CPU.PerCore = append(snap.CPU.PerCore, parseFloat64(s))
	}

	memParts := strings.Fields(r["MemInfo"].Output)
	if len(memParts) >= 3 {
		snap.Memory.Total = parseUint64(memParts[0])
		snap.Memory.Used = parseUint64(memParts[1])
		snap.Memory.Free = parseUint64(memParts[2])
		if snap.Memory.Total > 0 {
			snap.Memory.UsedPercent = float64(snap.Memory.Used) / float64(snap.Memory.Total) * 100
		}
	}

	diskLines := strings.Split(strings.TrimSpace(r["DiskInfo"].Output), "\n")
	for _, line := range diskLines {
		parts := strings.Fields(line)
		if len(parts) >= 3 {
			total := parseUint64(parts[1])
			used := parseUint64(parts[2])
			snap.Disks = append(snap.Disks, metrics.DiskInfo{
				Mountpoint: parts[0],
				Total:      total,
				Used:       used,
				Fstype:     "",
			})
		}
	}

	snap.Net.ByNic = []metrics.NicIO{}
	netLines := strings.Split(strings.TrimSpace(r["NetDev"].Output), "\n")
	for _, line := range netLines {
		parts := strings.Fields(line)
		if len(parts) >= 3 && parts[0] != "lo" {
			rx := parseUint64(parts[1])
			tx := parseUint64(parts[2])
			snap.Net.ByNic = append(snap.Net.ByNic, metrics.NicIO{
				Name:    parts[0],
				RxRate:  rx,
				TxRate:  tx,
				RxTotal: rx,
				TxTotal: tx,
			})
		}
	}

	return snap
}

func parseUint64(s string) uint64 {
	v, _ := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
	return v
}

func parseFloat64(s string) float64 {
	v, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return v
}

func Resources(w http.ResponseWriter, r *http.Request) {
	hostID := r.URL.Query().Get("host")
	if hostID != "" {
		hosts := ansibleMgr.ListHosts()
		for _, h := range hosts {
			if h.ID == hostID || h.Alias == hostID {
				if agentHub != nil {
					if snap, ok := agentHub.GetSnapshot(hostID); ok {
						WriteJSON(w, snap)
						return
					}
				// Agent 在线但快照未就绪(刚注册/重启窗口): 不报错,
				// 继续走下方 SSH 回退, 前端始终拿到可用数据
				}
				snap := cachedRemoteSnapshot(h)
				if snap.CPU.Percent != 0 || snap.Host.Hostname != "" {
					WriteJSON(w, snap)
					return
				}
				writeErr(w, "无法连接到远程主机", http.StatusBadGateway)
				return
			}
		}
		writeErr(w, "未找到指定主机", http.StatusNotFound)
		return
	}
	WriteJSON(w, metrics.Get())
}
