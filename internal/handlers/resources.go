package handlers

import (
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
	r := remotePool.Exec(rmHost, remote.Cmds)

	snap := &metrics.Snapshot{Timestamp: time.Now().Unix()}

	snap.Host.Hostname = r["Hostname"].Output
	snap.Host.Platform = r["OsRelease"].Output
	snap.Host.Uptime = parseUint64(r["Uptime"].Output)

	snap.CPU.Percent = parseFloat64(r["CpuUsage"].Output)
	snap.CPU.Cores = int(parseUint64(r["CpuCores"].Output))
	snap.CPU.Model = r["CpuModel"].Output
	snap.CPU.PerCore = []float64{}

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
				if agentHub.IsOnline(hostID) {
					// 注意: 不能用 202 — 202 在 Response.ok(200-299) 范围内, 前端 getJSON 不会 reject,
					// 会把 {error:...} 当快照解析导致渲染崩溃(白屏)。用 503 让前端走错误分支。
					writeErr(w, "Agent 在线但尚无数据", http.StatusServiceUnavailable)
					return
				}
				}
				snap := remoteResourceSnapshot(h)
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
