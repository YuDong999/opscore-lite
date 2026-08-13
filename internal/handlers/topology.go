package handlers

// ── 网络拓扑 ──
// GET /api/core/network/topology — 网段 / 网关 / 主机发现 / LLDP 设备 / 主机清单融合
// ?host=<id> — 以指定主机为视角构建拓扑(空 = 本机视角 + 远程汇聚)
// 数据源: ip route / ip neigh / 并发 ICMP 扫描(非 root 降级 ARP 只读) / lldpctl / 清单主机 SSH 汇聚 / 异步 PTR 反查

import (
	"context"
	"encoding/csv"
	"fmt"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"opscore/internal/ansible"
	"opscore/internal/remote"
)

type TopoSegment struct {
	CIDR     string `json:"cidr"`
	Gateway  string `json:"gateway,omitempty"`
	Iface    string `json:"iface,omitempty"`
	Via      string `json:"via"` // connected | static | remote
	LocalIP  string `json:"localIp,omitempty"`
	RemoteOf string `json:"remoteOf,omitempty"` // 该网段所属远程主机别名
}

type TopoDevice struct {
	IP          string `json:"ip"`
	MAC         string `json:"mac,omitempty"`
	Hostname    string `json:"hostname,omitempty"`
	Segment     string `json:"segment,omitempty"`
	Source      string `json:"source"` // entity | local | ping | arp | lldp | remote
	Type        string `json:"type"`   // host | gateway | switch | entity
	Online      bool   `json:"online"`
	InInventory bool   `json:"inInventory"`
	HostID      string `json:"hostId,omitempty"`
	Alias       string `json:"alias,omitempty"`
	Entity      string `json:"entity,omitempty"` // 所属主机实体 (实体节点为空)
	Iface       string `json:"iface,omitempty"`  // 网卡名
}

type TopoLink struct {
	From string `json:"from"`
	To   string `json:"to"`
	Type string `json:"type"` // route | reach | uplink
}

type TopologyResp struct {
	Segments []TopoSegment `json:"segments"`
	Devices  []TopoDevice  `json:"devices"`
	Links    []TopoLink    `json:"links"`
	Scanned  bool          `json:"scanned"` // 是否执行了主动扫描(非 root 降级时 false)
	Elapsed  string        `json:"elapsed"`
}

// PTR 反查缓存 (ip -> hostname)
var (
	ptrCacheMu sync.Mutex
	ptrCache   = map[string]string{}
)

// ===== 解析器 =====

// parseRouteSegments 解析 `ip route show` 输出, 返回网段列表与默认网关。
// 规则: default → 记录网关; 直连段(scope link)标 connected 并附默认网关; 经 via 的段标 static。
func parseRouteSegments(out string) (segs []TopoSegment, defaultGW string) {
	lines := strings.Split(out, "\n")
	for _, line := range lines {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "default" {
			for i := 1; i+1 < len(f); i++ {
				if f[i] == "via" {
					defaultGW = f[i+1]
				}
			}
		}
	}
	for _, line := range lines {
		f := strings.Fields(strings.TrimSpace(line))
		if len(f) < 2 || f[0] == "default" {
			continue
		}
		if _, _, err := net.ParseCIDR(f[0]); err != nil {
			continue
		}
		seg := TopoSegment{CIDR: f[0], Via: "connected"}
		for i := 1; i < len(f); i++ {
			switch f[i] {
			case "via":
				if i+1 < len(f) {
					seg.Gateway = f[i+1]
					seg.Via = "static"
				}
			case "dev":
				if i+1 < len(f) {
					seg.Iface = f[i+1]
				}
			case "src":
				if i+1 < len(f) {
					seg.LocalIP = f[i+1]
				}
			}
		}
		if seg.Via == "connected" {
			if seg.LocalIP == "" {
				seg.LocalIP = localIPv4()
			}
			if defaultGW != "" {
				seg.Gateway = defaultGW
			}
		}
		segs = append(segs, seg)
	}
	return segs, defaultGW
}

// parseNeighMAC 解析 `ip neigh show` 输出 → ip → mac (无 lladdr 或 FAILED 的跳过)
func parseNeighMAC(out string) map[string]string {
	res := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		ip := net.ParseIP(f[0])
		if ip == nil || ip.To4() == nil {
			continue // 仅 IPv4
		}
		for i := 1; i+1 < len(f); i++ {
			if f[i] == "lladdr" {
				if _, err := net.ParseMAC(f[i+1]); err == nil {
					res[f[0]] = f[i+1]
				}
			}
		}
	}
	return res
}

// ===== 主动扫描 =====

// scanSegment 并发 ICMP 探测整个网段, 返回在线 IP 集合。
// 无原始套接字权限返回错误, 由调用方降级为只读 ARP 表。
func scanSegment(cidr string, localIP string) (map[string]bool, error) {
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, err
	}
	if err := ansible.PingIP(localIP, 300*time.Millisecond); err != nil {
		return nil, err // 无权限
	}
	online := map[string]bool{}
	var mu sync.Mutex
	sem := make(chan struct{}, 64)
	var wg sync.WaitGroup
	for ip := ipnet.IP.Mask(ipnet.Mask); ipnet.Contains(ip); incIP(ip) {
		ipStr := ip.String()
		if ipStr == localIP {
			online[ipStr] = true
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(ipStr string) {
			defer wg.Done()
			defer func() { <-sem }()
			if ansible.PingIP(ipStr, 400*time.Millisecond) == nil {
				mu.Lock()
				online[ipStr] = true
				mu.Unlock()
			}
		}(ipStr)
	}
	wg.Wait()
	return online, nil
}

func incIP(ip net.IP) {
	for j := len(ip) - 1; j >= 0; j-- {
		ip[j]++
		if ip[j] > 0 {
			break
		}
	}
}

// ===== PTR 反查 (带缓存, 失败不缓存以便下次重试) =====

func lookupPTRCached(ip string) string {
	ptrCacheMu.Lock()
	if h, ok := ptrCache[ip]; ok {
		ptrCacheMu.Unlock()
		return h
	}
	ptrCacheMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	names, err := net.DefaultResolver.LookupAddr(ctx, ip)
	if err == nil && len(names) > 0 {
		h := strings.TrimSuffix(names[0], ".")
		ptrCacheMu.Lock()
		ptrCache[ip] = h
		ptrCacheMu.Unlock()
		return h
	}
	return ""
}

// ===== 去重构建器 =====

// TopologyBuilder 负责设备/连边的按 key 去重合并, 保证图中节点 id 唯一。
type TopologyBuilder struct {
	devByIP map[string]*TopoDevice
	order   []string
	links   []TopoLink
	linkSet map[string]bool
	Scanned bool
}

func newTopologyBuilder() *TopologyBuilder {
	return &TopologyBuilder{devByIP: map[string]*TopoDevice{}, linkSet: map[string]bool{}}
}

// topoKey 设备去重键: 实体用 entity:主机名, 优先 IP, 无 IP 用 lldp:主机名
func topoKey(d TopoDevice) string {
	if d.Type == "entity" {
		return "entity:" + d.Hostname
	}
	if d.IP != "" {
		return d.IP
	}
	return "lldp:" + d.Hostname
}

// AddDevice 去重合并设备, 同键已有则补齐缺失字段而非重复追加。
func (b *TopologyBuilder) AddDevice(d TopoDevice) {
	key := topoKey(d)
	if key == "" {
		return
	}
	if prev, ok := b.devByIP[key]; ok {
		if prev.MAC == "" && d.MAC != "" {
			prev.MAC = d.MAC
		}
		if prev.Hostname == "" && d.Hostname != "" {
			prev.Hostname = d.Hostname
		}
		if prev.Alias == "" && d.Alias != "" {
			prev.Alias = d.Alias
		}
		if prev.HostID == "" && d.HostID != "" {
			prev.HostID = d.HostID
		}
		if prev.Segment == "" && d.Segment != "" {
			prev.Segment = d.Segment
		}
		if prev.Entity == "" && d.Entity != "" {
			prev.Entity = d.Entity
		}
		if !prev.Online && d.Online {
			prev.Online = true
		}
		if d.InInventory {
			prev.InInventory = true
		}
		if d.Type == "gateway" && prev.Type != "gateway" {
			prev.Type = "gateway"
		}
		if prev.Source == "arp" && (d.Source == "ping" || d.Source == "local" || d.Source == "remote" || d.Source == "entity") {
			prev.Source = d.Source
		}
		return
	}
	b.devByIP[key] = &d
	b.order = append(b.order, key)
}

// Get 按 IP 查询已收录设备
func (b *TopologyBuilder) Get(ip string) *TopoDevice {
	return b.devByIP[ip]
}

// AddLink 去重连边 (忽略自环/空端点; 无向: A→B 与 B→A 视作同一条)
func (b *TopologyBuilder) AddLink(from, to, typ string) {
	if from == "" || to == "" || from == to {
		return
	}
	key := from + "→" + to
	if from > to {
		key = to + "→" + from
	}
	if !b.linkSet[key] {
		b.linkSet[key] = true
		b.links = append(b.links, TopoLink{From: from, To: to, Type: typ})
	}
}

func (b *TopologyBuilder) Devices() []TopoDevice {
	out := make([]TopoDevice, 0, len(b.order))
	for _, k := range b.order {
		out = append(out, *b.devByIP[k])
	}
	return out
}

func (b *TopologyBuilder) Links() []TopoLink {
	return b.links
}

// ===== 本机视角采集 =====

// localIfaceInfo 本机网卡信息
type localIfaceInfo struct {
	IP     string
	Prefix int
	Iface  string
}

// localRouteInfo 本机路由信息
type localRouteInfo struct {
	CIDR string
	GW   string
	Iface string
}

// collectLocalRoutesNeigh 采集本机网卡 / 路由 / 邻居 MAC, 兼容 Windows 与 Linux。
// Windows 用 PowerShell (Get-NetIPAddress / Get-NetRoute / arp -a), Linux 用 ip 命令。
func collectLocalRoutesNeigh() (ifaces []localIfaceInfo, routes []localRouteInfo, neighMAC map[string]string) {
	if runtime.GOOS == "windows" {
		return winLocalRoutesNeigh()
	}
	return linuxLocalRoutesNeigh()
}

func linuxLocalRoutesNeigh() ([]localIfaceInfo, []localRouteInfo, map[string]string) {
	var ifaces []localIfaceInfo
	var routes []localRouteInfo
	var neighMAC map[string]string
	rawIface := runCapture("ip", "-o", "addr", "show")
	for _, line := range strings.Split(rawIface, "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) < 3 {
			continue
		}
		rest := strings.TrimSpace(parts[2])
		f := strings.Fields(rest)
		if len(f) < 2 || f[0] != "inet" {
			continue
		}
		ip, ipnet, err := net.ParseCIDR(f[1])
		if err != nil {
			continue
		}
		ones, _ := ipnet.Mask.Size()
		ifaces = append(ifaces, localIfaceInfo{IP: ip.String(), Prefix: ones, Iface: strings.TrimSpace(parts[1])})
	}

	segs, defaultGW := parseRouteSegments(runCapture("ip", "route", "show"))
	for _, s := range segs {
		r := localRouteInfo{CIDR: s.CIDR, Iface: s.Iface}
		if s.Gateway != "" {
			r.GW = s.Gateway
		} else if s.Via == "connected" {
			r.GW = defaultGW
		}
		routes = append(routes, r)
	}
	neighMAC = parseNeighMAC(runCapture("ip", "neigh", "show"))
	return ifaces, routes, neighMAC
}

// winLocalRoutesNeigh 通过 PowerShell 采集本机网卡/路由/ARP 邻居 (Windows)。
func winLocalRoutesNeigh() ([]localIfaceInfo, []localRouteInfo, map[string]string) {
	var ifaces []localIfaceInfo
	rawIface := runPS("Get-NetIPAddress -AddressFamily IPv4 | Where-Object {$_.IPAddress -notmatch '^169\\.254' -and $_.IPAddress -ne '127.0.0.1'} | Select-Object IPAddress,PrefixLength,InterfaceAlias | ConvertTo-Csv -NoTypeInformation")
	for _, rec := range parsePSOut(rawIface) {
		ip := rec["IPAddress"]
		if net.ParseIP(ip) == nil || net.ParseIP(ip).To4() == nil {
			continue
		}
		var prefix int
		fmt.Sscanf(rec["PrefixLength"], "%d", &prefix)
		ifaces = append(ifaces, localIfaceInfo{IP: ip, Prefix: prefix, Iface: rec["InterfaceAlias"]})
	}

	var routes []localRouteInfo
	rawRoutes := runPS("Get-NetRoute -AddressFamily IPv4 | Select-Object DestinationPrefix,@{n='NH';e={$_.NextHop.ToString()}},InterfaceAlias | ConvertTo-Csv -NoTypeInformation")
	for _, rec := range parsePSOut(rawRoutes) {
		cidr := rec["DestinationPrefix"]
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			continue
		}
		gw := ""
		if ip := net.ParseIP(rec["NH"]); ip != nil && !ip.Equal(net.IPv4zero) {
			gw = rec["NH"]
		}
		routes = append(routes, localRouteInfo{CIDR: cidr, GW: gw, Iface: rec["InterfaceAlias"]})
	}

	neighMAC := map[string]string{}
	rawArp := runPS("arp -a")
	for _, line := range strings.Split(rawArp, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		ip := net.ParseIP(f[0])
		if ip == nil || ip.To4() == nil {
			continue
		}
		if _, err := net.ParseMAC(f[1]); err == nil {
			neighMAC[f[0]] = f[1]
		}
	}
	return ifaces, routes, neighMAC
}

// parsePSOut 解析 PowerShell ConvertTo-Csv 输出为 map 记录列表
func parsePSOut(raw string) []map[string]string {
	var out []map[string]string
	cr := csv.NewReader(strings.NewReader(strings.TrimSpace(raw)))
	records, err := cr.ReadAll()
	if err != nil || len(records) < 2 {
		return out
	}
	header := records[0]
	for _, rec := range records[1:] {
		m := map[string]string{}
		for i, h := range header {
			if i < len(rec) {
				m[h] = rec[i]
			}
		}
		out = append(out, m)
	}
	return out
}

// runPS 执行 PowerShell 单条命令并返回输出
func runPS(cmd string) string {
	return runCapture("powershell", "-NoProfile", "-NonInteractive", "-Command", cmd)
}

func collectLocalTopology(b *TopologyBuilder) []TopoSegment {
	// 本机网卡 / 路由 / 邻居
	ifaces, routes, neighMAC := collectLocalRoutesNeigh()

	// 本机实体节点
	entityName := "本机"
	// 尝试用主机名, 无则保留"本机"
	if hn := localHostname(); hn != "" {
		entityName = hn
	}
	b.AddDevice(TopoDevice{Type: "entity", Hostname: entityName, Source: "entity", Online: true})

	// 网卡节点 (平级, 挂在实体下)
	var segs []TopoSegment
	for _, ifc := range ifaces {
		b.AddDevice(TopoDevice{IP: ifc.IP, Iface: ifc.Iface, Type: "host", Source: "local", Online: true, Entity: entityName, Segment: "local"})
		b.AddLink("entity:"+entityName, ifc.IP, "iface")
	}

	// 每块网卡取所属网段的默认网关 (路由表中该网卡的 default 路由), 建立 网卡→网关 route 边
	gwByIface := map[string]string{}
	for _, r := range routes {
		if r.CIDR == "0.0.0.0/0" && r.GW != "" {
			if _, ok := gwByIface[r.Iface]; !ok {
				gwByIface[r.Iface] = r.GW
			}
		}
	}
	// 若某网卡无 default 路由, 用 connected 路由 (同网段 via 网关) 兜底
	for _, r := range routes {
		if r.CIDR == "0.0.0.0/0" || r.GW == "" {
			continue
		}
		if _, ok := gwByIface[r.Iface]; !ok {
			gwByIface[r.Iface] = r.GW
		}
	}

	for _, ifc := range ifaces {
		gw := gwByIface[ifc.Iface]
		if gw == "" {
			continue
		}
		// 过滤网络/广播地址等假网关 (如 TAP 适配器的 0.0.0.0/0 via 10.0.0.0)
		if !isValidGateway(gw, cidrOf(ifc)) {
			continue
		}
		b.AddDevice(TopoDevice{IP: gw, Type: "gateway", Source: "local", Online: true, Segment: cidrOf(ifc), Entity: entityName})
		b.AddLink(ifc.IP, gw, "route")
	}

	// 主动扫描各 /24 直连网段 (每块网卡); 无权限 → 降级 ARP 只读
	online := map[string]bool{}
	for _, ifc := range ifaces {
		ip := net.ParseIP(ifc.IP)
		if ip == nil {
			continue
		}
		cidr := cidrOf(ifc)
		if cidr == "" {
			continue
		}
		if _, ipnet, err := net.ParseCIDR(cidr); err == nil {
			if ones, _ := ipnet.Mask.Size(); ones == 24 {
				ips, err := scanSegment(cidr, ifc.IP)
				if err == nil {
					for ip := range ips {
						online[ip] = true
					}
					b.Scanned = true
				}
			}
		}
	}
	if !b.Scanned {
		for ip := range neighMAC {
			online[ip] = true
		}
	}

	// 清单索引
	invByAddr := map[string]ansible.Host{}
	for _, h := range ansibleMgr.ListHosts() {
		if h.Addr != "" {
			invByAddr[h.Addr] = h
		}
	}

	// 在线主机 (按所属网段归类) + 连边
	for ip := range online {
		if ip == localIPv4() {
			continue
		}
		d := TopoDevice{IP: ip, Type: "host", Online: true, Source: "ping"}
		for _, ifc := range ifaces {
			if _, ipnet, err := net.ParseCIDR(cidrOf(ifc)); err == nil && ipnet.Contains(net.ParseIP(ip)) {
				d.Segment = cidrOf(ifc)
				break
			}
		}
		b.AddDevice(d)
		// 直连边: 本机同网段网卡 → 主机 (物理可达, 不经过网关)
		for _, ifc := range ifaces {
			if _, ipnet, err := net.ParseCIDR(cidrOf(ifc)); err == nil && ipnet.Contains(net.ParseIP(ip)) {
				b.AddLink(ifc.IP, ip, "reach")
			}
		}
		// 网关边: 该网段网关 → 主机
		if d.Segment != "" {
			for _, ifc := range ifaces {
				if cidrOf(ifc) == d.Segment {
					if gw := gwByIface[ifc.Iface]; gw != "" {
						b.AddLink(gw, ip, "reach")
					}
				}
			}
		}
	}
	// ARP 表补 MAC 与未扫描到的设备
	for ip, mac := range neighMAC {
		if d := b.Get(ip); d != nil {
			if d.MAC == "" {
				d.MAC = mac
			}
		} else {
			d := TopoDevice{IP: ip, MAC: mac, Type: "host", Online: true, Source: "arp", Entity: entityName}
			b.AddDevice(d)
		}
	}

	// LLDP 交换设备
	for _, n := range parseLldpctl() {
		name := n.SysName
		if name == "" {
			name = n.ChassisID
		}
		if name == "" {
			continue
		}
		b.AddDevice(TopoDevice{IP: "", Hostname: name, Type: "switch", Source: "lldp", Online: true})
		b.AddLink(localIPv4(), "lldp:"+name, "uplink")
	}

	// 连边 + 清单融合 + PTR
	for _, key := range b.order {
		d := b.devByIP[key]
		if d.IP == "" {
			continue
		}
		if h, ok := invByAddr[d.IP]; ok {
			d.InInventory = true
			d.HostID = h.ID
			d.Alias = h.Alias
			if d.Hostname == "" {
				d.Hostname = h.Hostname
			}
		}
		if d.Hostname == "" && d.IP != "" {
			d.Hostname = lookupPTRCached(d.IP)
		}
	}

	return segs
}

// isValidGateway 过滤网络地址/广播地址等不可能作为网关的 IP
func isValidGateway(gw, cidr string) bool {
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return true
	}
	g := net.ParseIP(gw).To4()
	netIP := ipnet.IP.To4()
	if g == nil || netIP == nil {
		return false
	}
	ones, _ := ipnet.Mask.Size()
	if ones == 0 || ones == 32 {
		return true
	}
	if g.Equal(netIP) {
		return false
	}
	bc := make(net.IP, 4)
	copy(bc, netIP)
	for i := range bc {
		bc[i] |= ^ipnet.Mask[i]
	}
	return !g.Equal(bc)
}

func cidrOf(ifc localIfaceInfo) string {
	ones := ifc.Prefix
	if ones == 0 {
		ones = 24
	}
	ip := net.ParseIP(ifc.IP).To4()
	if ip == nil {
		return ""
	}
	mask := net.CIDRMask(ones, 32)
	return (&net.IPNet{IP: ip.Mask(mask), Mask: mask}).String()
}

// localHostname 返回本机主机名, 失败返回空串
func localHostname() string {
	hn, err := os.Hostname()
	if err != nil {
		return ""
	}
	return hn
}

// ===== 远程主机汇聚 (SSH 采集路由/邻居/hostname, 不做主动扫描) =====

func collectRemoteTopology(b *TopologyBuilder, h ansible.Host) []TopoSegment {
	if h.Platform == "win" || h.Addr == "" {
		return nil
	}
	rmHost := resolveRemoteHost(h)
	res := remotePool.Exec(rmHost, map[string]string{
		"routes": "ip route show 2>/dev/null",
		"neigh":  "ip neigh show 2>/dev/null",
		"host":   "hostname 2>/dev/null",
	})
	routesRaw := res["routes"].Output
	if res["routes"].Error != "" || strings.TrimSpace(routesRaw) == "" {
		return nil
	}

	alias := h.Alias
	if alias == "" {
		alias = h.Addr
	}
	if hn := strings.TrimSpace(res["host"].Output); hn != "" {
		ansibleMgr.SetHostname(h.ID, hn)
	}

	rsegs, defaultGW := parseRouteSegments(routesRaw)
	neighMAC := parseNeighMAC(res["neigh"].Output)

	// 桥节点 = 清单管理地址 (修复: 原取路由表首个 connected 段 LocalIP,
	// 多台 VM 均有 docker 172.17.0.0/16 → 全解析成 172.17.0.1 → 被去重合并成一个节点)
	remoteIP := h.Addr

	// 远程主机实体 (仅当与清单别名不同时; 通常管理地址即节点)
	if remoteIP != "" {
		b.AddDevice(TopoDevice{
			IP: remoteIP, Hostname: h.Hostname, Alias: alias, Type: "host",
			Source: "remote", Online: true, Segment: "remote",
		})
		b.AddLink(localIPv4(), remoteIP, "reach")

		// 远程主机默认网关: 建网关节点 (菱形), 网关→VM route 边。
		// 网关与本机网卡同 /24 → 本机网卡→网关 route 边 (VMnet8 场景宿主机无 .2 的 default 路由, 需补此边)
		if defaultGW != "" && defaultGW != remoteIP {
			b.AddDevice(TopoDevice{IP: defaultGW, Type: "gateway", Source: "remote", Online: true, Segment: "remote"})
			b.AddLink(remoteIP, defaultGW, "route")
			lv := net.ParseIP(localIPv4())
			gw := net.ParseIP(defaultGW)
			if lv != nil && gw != nil && lv.Mask(net.CIDRMask(24, 32)).Equal(gw.Mask(net.CIDRMask(24, 32))) {
				b.AddLink(localIPv4(), defaultGW, "route")
			}
		}
	}
	// 远程网段标注归属
	for i := range rsegs {
		rsegs[i].RemoteOf = alias
	}
	for ip, mac := range neighMAC {
		if ip == remoteIP {
			continue
		}
		b.AddDevice(TopoDevice{
			IP: ip, MAC: mac, Type: "host", Source: "remote", Online: true, Segment: "remote",
		})
		b.AddLink(remoteIP, ip, "reach")
	}
	return rsegs
}

// ===== 选中远程主机视角 =====

// remoteScanSegment 在远程主机上并发 ping 扫描直连/静态网段, 返回在线 IP。
// 仅扫描 /24 及更小掩码的网段 (如 K8s flannel /26); 非 IPv4 / 超大段 (>/24, 如 docker /16) 返回 nil 由调用方跳过。
func remoteScanSegment(h remote.Host, cidr string) map[string]bool {
	ip, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil
	}
	ones, bits := ipnet.Mask.Size()
	if bits != 32 || ones < 24 {
		return nil
	}
	hostCount := (1 << (32 - ones)) - 2
	if hostCount < 1 || hostCount > 254 {
		return nil
	}
	b4 := ip.To4()
	base := uint32(b4[0])<<24 | uint32(b4[1])<<16 | uint32(b4[2])<<8 | uint32(b4[3])
	mask := uint32(0xFFFFFFFF) << (32 - ones)
	netAddr := base & mask

	// 枚举网络内主机地址 (排除网络/广播地址), 并发 ping
	var targets []string
	for i := 1; i <= hostCount; i++ {
		v := netAddr + uint32(i)
		targets = append(targets, fmt.Sprintf("%d.%d.%d.%d", byte(v>>24), byte(v>>16), byte(v>>8), byte(v)))
	}
	cmd := "for ip in " + strings.Join(targets, " ") +
		"; do ( ping -c1 -W1 -q $ip >/dev/null 2>&1 && echo $ip ) & done; wait"
	res := remotePool.Exec(h, map[string]string{"scan": cmd})
	if res["scan"].Error != "" {
		return nil
	}
	out := map[string]bool{}
	for _, line := range strings.Split(res["scan"].Output, "\n") {
		line = strings.TrimSpace(line)
		if net.ParseIP(line) != nil {
			out[line] = true
		}
	}
	return out
}

// collectHostTopology 以指定远程主机为视角构建拓扑: 该主机为 self 节点,
// 主动扫描其 /24 直连网段 (失败降级为只读 ARP), 清单融合 + PTR。
func collectHostTopology(b *TopologyBuilder, h ansible.Host) []TopoSegment {
	if h.Platform == "win" || h.Addr == "" {
		return nil
	}
	rmHost := resolveRemoteHost(h)
	res := remotePool.Exec(rmHost, map[string]string{
		"routes": "ip route show 2>/dev/null",
		"neigh":  "ip neigh show 2>/dev/null",
		"host":   "hostname 2>/dev/null",
	})
	routesRaw := res["routes"].Output
	if res["routes"].Error != "" || strings.TrimSpace(routesRaw) == "" {
		return nil
	}

	alias := h.Alias
	if alias == "" {
		alias = h.Addr
	}
	if hn := strings.TrimSpace(res["host"].Output); hn != "" {
		ansibleMgr.SetHostname(h.ID, hn)
		if h.Hostname == "" {
			h.Hostname = hn
		}
	}

	segs, _ := parseRouteSegments(routesRaw)
	neighMAC := parseNeighMAC(res["neigh"].Output)

	// 当前视角主机 = 图中 self 节点 (source=local 供前端高亮)
	// 优先使用清单管理地址 (用户选中的主机), 无则回退路由出口 IP
	selfIP := h.Addr
	if selfIP == "" {
		for _, s := range segs {
			if s.Via == "connected" && s.LocalIP != "" {
				selfIP = s.LocalIP
				break
			}
		}
	}
	if selfIP == "" {
		return segs
	}
	b.AddDevice(TopoDevice{IP: selfIP, Hostname: h.Hostname, Alias: alias, Type: "host", Source: "local", Online: true})

	// 网关节点
	gwByCIDR := map[string]string{}
	for _, s := range segs {
		if s.Via != "connected" {
			continue
		}
		if s.Gateway != "" && s.Gateway != selfIP {
			gwByCIDR[s.CIDR] = s.Gateway
			b.AddDevice(TopoDevice{IP: s.Gateway, Type: "gateway", Source: "local", Online: true, Segment: s.CIDR})
			b.AddLink(selfIP, s.Gateway, "route")
		}
	}

	// 主动扫描各 /24 网段 (含静态段, 如 K8s pod 网段); 最多 16 段防耗时失控
	online := map[string]bool{}
	scannedSegs := 0
	for _, s := range segs {
		if s.CIDR == "0.0.0.0/0" || scannedSegs >= 16 {
			continue
		}
		ips := remoteScanSegment(rmHost, s.CIDR)
		if ips != nil {
			for ip := range ips {
				online[ip] = true
			}
			b.Scanned = true
			scannedSegs++
		}
	}
	// neigh 表补充: ping 可能漏/禁 ping 的主机也纳入, 并参与下方建边
	for ip := range neighMAC {
		if ip != selfIP {
			online[ip] = true
		}
	}

	// 清单索引
	invByAddr := map[string]ansible.Host{}
	for _, hh := range ansibleMgr.ListHosts() {
		if hh.Addr != "" {
			invByAddr[hh.Addr] = hh
		}
	}

	// 在线主机 (按所属网段归类) + 连边
	for ip := range online {
		if ip == selfIP {
			continue
		}
		d := TopoDevice{IP: ip, Type: "host", Online: true, Source: "ping"}
		for _, s := range segs {
			if _, ipnet, err := net.ParseCIDR(s.CIDR); err == nil && ipnet.Contains(net.ParseIP(ip)) {
				d.Segment = s.CIDR
				break
			}
		}
		b.AddDevice(d)
		b.AddLink(selfIP, ip, "reach")
		// 网关边: 该网段网关 → 主机 (与本地视角一致, 使同网段主机在网关下平级)
		if d.Segment != "" {
			if gw := gwByCIDR[d.Segment]; gw != "" {
				b.AddLink(gw, ip, "reach")
			}
		}
	}
	// ARP 补 MAC 与未扫描到的设备
	for ip, mac := range neighMAC {
		if ip == selfIP {
			continue
		}
		if d := b.Get(ip); d != nil {
			if d.MAC == "" {
				d.MAC = mac
			}
		} else {
			b.AddDevice(TopoDevice{IP: ip, MAC: mac, Type: "host", Online: true, Source: "arp", Segment: "remote"})
		}
	}

	// 清单融合 + PTR
	for _, key := range b.order {
		d := b.devByIP[key]
		if d.IP == "" {
			continue
		}
		if hh, ok := invByAddr[d.IP]; ok {
			d.InInventory = true
			d.HostID = hh.ID
			if d.Alias == "" {
				d.Alias = hh.Alias
			}
			if d.Hostname == "" {
				d.Hostname = hh.Hostname
			}
		}
		if d.Hostname == "" {
			d.Hostname = lookupPTRCached(d.IP)
		}
	}

	return segs
}

// ===== Handler =====

// topoCacheEntry ?host= 拓扑响应缓存
type topoCacheEntry struct {
	resp TopologyResp
	at   time.Time
}

var (
	topoCacheMu sync.Mutex
	topoCache   = map[string]topoCacheEntry{}
)

const topoCacheTTL = 30 * time.Second

// TopologyHandler 处理 GET /api/core/network/topology
// ?host=<id> 以指定主机为视角; 空/本机/未知 → 本机视角 + 远程汇聚。
// ?force=1 强制重新扫描, 忽略缓存。
func TopologyHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	start := time.Now()
	hostID := r.URL.Query().Get("host")
	force := r.URL.Query().Get("force") == "1"

	// 缓存命中 (TTL 内且未强制重扫) → 直接返回
	if !force {
		topoCacheMu.Lock()
		if e, ok := topoCache[hostID]; ok && time.Since(e.at) < topoCacheTTL {
			topoCacheMu.Unlock()
			WriteJSON(w, e.resp)
			return
		}
		topoCacheMu.Unlock()
	}

	b := newTopologyBuilder()
	resp := TopologyResp{}

	var h *ansible.Host
	if hostID != "" {
		h = resolveAnsibleHost(hostID)
	}
	if h == nil || h.IsLocal || h.Platform == "win" || h.Addr == "" {
		// 本机 / 未知 / win 远程: 本机视角 + 远程汇聚
		resp.Segments = collectLocalTopology(b)
		for _, hh := range ansibleMgr.ListHosts() {
			resp.Segments = append(resp.Segments, collectRemoteTopology(b, hh)...)
		}
	} else {
		resp.Segments = collectHostTopology(b, *h)
	}

	resp.Devices = b.Devices()
	resp.Links = b.Links()
	resp.Scanned = b.Scanned
	resp.Elapsed = time.Since(start).Round(time.Millisecond).String()

	topoCacheMu.Lock()
	topoCache[hostID] = topoCacheEntry{resp: resp, at: time.Now()}
	topoCacheMu.Unlock()

	WriteJSON(w, resp)
}
