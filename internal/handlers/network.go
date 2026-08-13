package handlers

import (
	"net/http"
	"sort"
	"strconv"
	"strings"

	"opscore/internal/ansible"
	"opscore/internal/metrics"

	"github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"
)

// Iface 统一描述一个网络接口(本机/SSH 回退/agent 快照三路径共用)。
type Iface struct {
	Name    string   `json:"name"`
	MTU     int      `json:"mtu,omitempty"`
	Flags   []string `json:"flags,omitempty"`
	Addrs   []string `json:"addrs"`
	RxBytes uint64   `json:"rxBytes"`
	TxBytes uint64   `json:"txBytes"`
}

// Listen 统一描述一个监听端口(三路径共用,识别字段由 server 端对齐填充)。
type Listen struct {
	Protocol string `json:"protocol"`
	Local    string `json:"local"`
	Port     int    `json:"port"`
	PID      int32  `json:"pid"`
	// 身份以"实际占用进程"为准,端口常见服务仅作提示
	Process  string `json:"process"`  // 真实占用进程名(事实来源)
	Service  string `json:"service"`  // 由进程名识别出的服务(已确认身份)
	Category string `json:"category"` // 服务分类
	Icon     string `json:"icon"`     // 服务图标
	KnownAs  string `json:"knownAs"`  // 该端口"常见服务"提示(仅供参考)
	Verified bool   `json:"verified"` // 端口提示与进程身份一致 → 已确认
}

// Network 返回网络接口、流量统计与监听端口。
func Network(w http.ResponseWriter, r *http.Request) {
	if hostID := r.URL.Query().Get("host"); hostID != "" {
		if agentHub != nil {
			if snap, ok := agentHub.GetSnapshot(hostID); ok && (len(snap.Network.Interfaces) > 0 || len(snap.Network.Listeners) > 0) {
				// agent 只回传原始数据,端口/进程识别在 server 端对齐(深拷贝,不改动缓存快照)
				ifaces := make([]Iface, 0, len(snap.Network.Interfaces))
				for _, i := range snap.Network.Interfaces {
					ifaces = append(ifaces, Iface{Name: i.Name, Addrs: i.Addrs, RxBytes: i.RxBytes, TxBytes: i.TxBytes})
				}
				listens := make([]Listen, 0, len(snap.Network.Listeners))
				for _, li := range snap.Network.Listeners {
					l := Listen{Protocol: li.Protocol, Local: li.Local, Port: li.Port, PID: li.PID, Process: li.Process}
					if hint, ok := recognizePort(uint16(li.Port)); ok {
						l.KnownAs = hint.Label
					}
					if li.Process != "" {
						if meta, ok := recognizeProc(li.Process); ok {
							l.Service = meta.Label
							l.Category = meta.Category
							l.Icon = meta.Icon
							l.Verified = true
						}
					}
					listens = append(listens, l)
				}
				WriteJSON(w, map[string]any{"interfaces": ifaces, "listeners": listens})
				return
			}
		}
		// Agent 无数据 → 异步尝试恢复
		EnsureAgent(hostID)
		// SSH 回退
		remoteNetwork(w, hostID)
		return
	}
	// 获取每接口 IO 计数器
	ioMap := map[string]struct{ rx, tx uint64 }{}
	if counters, err := net.IOCounters(true); err == nil {
		for _, c := range counters {
			ioMap[c.Name] = struct{ rx, tx uint64 }{c.BytesRecv, c.BytesSent}
		}
	}

	var ifaces []Iface
	ifaceErr := ""
	if il, err := net.Interfaces(); err == nil {
		for _, i := range il {
			addrs := []string{}
			for _, a := range i.Addrs {
				addrs = append(addrs, a.Addr)
			}
			io := ioMap[i.Name]
			ifaces = append(ifaces, Iface{Name: i.Name, MTU: i.MTU, Flags: i.Flags, Addrs: addrs, RxBytes: io.rx, TxBytes: io.tx})
		}
	} else {
		// 即便接口采集失败,也把错误带回前端,避免"空白无提示"
		ifaceErr = err.Error()
	}

	var listens []Listen
	connErr := ""
	if conns, err := net.Connections("all"); err == nil {
		for _, c := range conns {
			if !strings.EqualFold(c.Status, "listen") {
				continue
			}
			port := int(c.Laddr.Port)
			local := c.Laddr.IP + ":" + strconv.Itoa(port)
			// gopsutil v4 中 Type 是套接字类型常量(1=TCP,2=UDP)
			protocol := "TCP"
			if c.Type == 2 {
				protocol = "UDP"
			}
			li := Listen{Protocol: protocol, Local: local, Port: port, PID: c.Pid}

			// 端口常见服务提示(仅供参考,绝不作结论)
			if hint, ok := recognizePort(uint16(port)); ok {
				li.KnownAs = hint.Label
			}

			// 关键:用 PID 反查真实进程名作为身份依据,再确认是否与端口提示相符
			if c.Pid > 0 {
				if p, perr := process.NewProcess(c.Pid); perr == nil {
					if nm, nerr := p.Name(); nerr == nil {
						li.Process = nm
						if meta, ok := recognizeProc(nm); ok {
							li.Service = meta.Label
							li.Category = meta.Category
							li.Icon = meta.Icon
						li.Verified = true
						}
					}
				}
			}
			listens = append(listens, li)
		}
	} else {
		connErr = err.Error()
	}

	resp := map[string]any{"interfaces": ifaces, "listeners": listens}
	if ifaceErr != "" {
		resp["ifaceError"] = ifaceErr
	}
	if connErr != "" {
		resp["listenError"] = connErr
	}
	WriteJSON(w, resp)
}

func remoteNetwork(w http.ResponseWriter, hostID string) {
	hosts := ansibleMgr.ListHosts()
	var h *ansible.Host
	for i := range hosts {
		if hosts[i].ID == hostID || hosts[i].Alias == hostID {
			h = &hosts[i]
			break
		}
	}
	if h == nil {
		WriteJSON(w, map[string]any{"interfaces": []any{}, "listeners": []any{}, "error": "未找到主机"})
		return
	}

	rmHost := resolveRemoteHost(*h)

	cmds := map[string]string{
		"ip_addr": `ip -o addr show 2>/dev/null`,
		"ip_stat": `awk 'NR%2==1{gsub(/:/,"",$2);name=$2} NR%2==0{print name,$1,$8}' /proc/net/dev 2>/dev/null`,
		"ss_tlnp": `ss -tlnp 2>/dev/null`,
		"ss_ulnp": `ss -ulnp 2>/dev/null`,
	}
	res := remotePool.Exec(rmHost, cmds)

	ifaceMap := map[string]*Iface{}
	if res["ip_addr"].Error == "" {
		for _, line := range strings.Split(strings.TrimSpace(res["ip_addr"].Output), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			parts := strings.Fields(line)
			if len(parts) < 4 {
				continue
			}
			ifaceName := strings.TrimRight(parts[1], ":")
			if ifaceMap[ifaceName] == nil {
				ifaceMap[ifaceName] = &Iface{Name: ifaceName, Addrs: []string{}}
			}
			if parts[2] == "inet" || parts[2] == "inet6" {
				addr := strings.Split(parts[3], "/")[0]
				found := false
				for _, a := range ifaceMap[ifaceName].Addrs {
					if a == addr {
						found = true
						break
					}
				}
				if !found {
					ifaceMap[ifaceName].Addrs = append(ifaceMap[ifaceName].Addrs, addr)
				}
			}
		}
	}
	if res["ip_stat"].Error == "" {
		for _, line := range strings.Split(strings.TrimSpace(res["ip_stat"].Output), "\n") {
			parts := strings.Fields(line)
			if len(parts) >= 3 {
				if ifaceMap[parts[0]] == nil {
					ifaceMap[parts[0]] = &Iface{Name: parts[0], Addrs: []string{}}
				}
				rx, _ := strconv.ParseUint(parts[1], 10, 64)
				tx, _ := strconv.ParseUint(parts[2], 10, 64)
				ifaceMap[parts[0]].RxBytes = rx
				ifaceMap[parts[0]].TxBytes = tx
			}
		}
	}
	ifaces := make([]Iface, 0, len(ifaceMap))
	for _, v := range ifaceMap {
		ifaces = append(ifaces, *v)
	}
	sort.Slice(ifaces, func(i, j int) bool { return ifaces[i].Name < ifaces[j].Name })

	listens := []Listen{}
	for _, key := range []string{"ss_tlnp", "ss_ulnp"} {
		if res[key].Error != "" {
			continue
		}
		proto := "TCP"
		if key == "ss_ulnp" {
			proto = "UDP"
		}
		for _, li := range metrics.ParseSS(res[key].Output, proto) {
			l := Listen{Protocol: li.Protocol, Local: li.Local, Port: li.Port, Process: li.Process}
			if hint, ok := recognizePort(uint16(li.Port)); ok {
				l.KnownAs = hint.Label
			}
			if li.Process != "" {
				if meta, ok := recognizeProc(li.Process); ok {
					l.Service = meta.Label
					l.Category = meta.Category
					l.Icon = meta.Icon
					l.Verified = true
				}
			}
			listens = append(listens, l)
		}
	}

	WriteJSON(w, map[string]any{"interfaces": ifaces, "listeners": listens})
}
