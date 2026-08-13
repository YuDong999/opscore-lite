package handlers

import "testing"

func TestParseRouteSegments(t *testing.T) {
	out := `default via 192.168.94.2 dev ens33 proto static metric 100
192.168.94.0/24 dev ens33 proto kernel scope link src 192.168.94.20
10.0.0.0/8 via 192.168.94.2 dev ens33 proto static metric 100
`
	segs, gw := parseRouteSegments(out)
	if gw != "192.168.94.2" {
		t.Fatalf("defaultGW = %q, want 192.168.94.2", gw)
	}
	if len(segs) != 2 {
		t.Fatalf("segments = %d, want 2 (%+v)", len(segs), segs)
	}
	c := segs[0]
	if c.CIDR != "192.168.94.0/24" || c.Via != "connected" || c.Iface != "ens33" || c.LocalIP != "192.168.94.20" {
		t.Errorf("connected seg = %+v", c)
	}
	if c.Gateway != "192.168.94.2" {
		t.Errorf("connected seg 应附加默认网关, got %q", c.Gateway)
	}
	s := segs[1]
	if s.CIDR != "10.0.0.0/8" || s.Via != "static" || s.Gateway != "192.168.94.2" {
		t.Errorf("static seg = %+v", s)
	}
}

func TestParseRouteSegmentsNoDefault(t *testing.T) {
	out := `192.168.1.0/24 dev eth0 proto kernel scope link src 192.168.1.10
`
	segs, gw := parseRouteSegments(out)
	if gw != "" {
		t.Fatalf("defaultGW = %q, want empty", gw)
	}
	if len(segs) != 1 {
		t.Fatalf("segments = %d, want 1", len(segs))
	}
	if segs[0].Gateway != "" {
		t.Errorf("无默认路由时 connected 段 gateway 应为空, got %q", segs[0].Gateway)
	}
	if segs[0].LocalIP != "192.168.1.10" {
		t.Errorf("src 解析失败: %+v", segs[0])
	}
}

func TestParseRouteSegmentsGarbage(t *testing.T) {
	segs, gw := parseRouteSegments("not a route\n192.168.99.0/99 dev bad\n\n")
	if gw != "" || len(segs) != 0 {
		t.Fatalf("garbage should be ignored, got segs=%v gw=%q", segs, gw)
	}
}

func TestParseNeighMAC(t *testing.T) {
	out := `192.168.94.22 dev ens33 lladdr 52:54:00:12:34:56 REACHABLE
192.168.94.1 dev ens33 lladdr 52:54:00:aa:bb:cc STALE
192.168.94.99 dev ens33 FAILED
fe80::1 dev ens33 lladdr 33:33:00:00:00:01 router
192.168.94.200 dev ens33 lladdr not-a-mac REACHABLE
`
	got := parseNeighMAC(out)
	if len(got) != 2 {
		t.Fatalf("neigh = %d entries, want 2 (%+v)", len(got), got)
	}
	if got["192.168.94.22"] != "52:54:00:12:34:56" {
		t.Errorf("22 mac = %q", got["192.168.94.22"])
	}
	if got["192.168.94.1"] != "52:54:00:aa:bb:cc" {
		t.Errorf("1 mac = %q", got["192.168.94.1"])
	}
	if _, ok := got["192.168.94.99"]; ok {
		t.Error("FAILED 条目不应记录")
	}
	if _, ok := got["fe80::1"]; ok {
		t.Error("IPv6 条目不应记录")
	}
	if _, ok := got["192.168.94.200"]; ok {
		t.Error("非法 MAC 不应记录")
	}
}

func TestTopologyBuilderDedup(t *testing.T) {
	b := newTopologyBuilder()
	// 三台远程主机共享网关/桥接地址 → 去重为唯一节点
	for _, gw := range []string{"192.168.94.2", "192.168.94.2", "192.168.94.2"} {
		b.AddDevice(TopoDevice{IP: gw, Type: "gateway", Source: "remote", Online: true})
	}
	for _, bridge := range []string{"172.17.0.1", "172.17.0.1", "172.17.0.1"} {
		b.AddDevice(TopoDevice{IP: bridge, Type: "host", Source: "remote", Online: true})
	}
	for _, self := range []string{"192.168.94.20", "192.168.94.22", "192.168.94.21"} {
		b.AddDevice(TopoDevice{IP: self, Type: "host", Source: "remote", Online: true})
	}
	// 本机 ARP 再上报一个已存在的 IP → 合并不重复
	b.AddDevice(TopoDevice{IP: "192.168.94.2", MAC: "52:54:00:aa:bb:cc", Type: "gateway", Source: "arp", Online: true})
	if got := len(b.Devices()); got != 5 {
		t.Fatalf("devices = %d, want 5 (去重失败: %+v)", got, b.Devices())
	}
	if d := b.Get("192.168.94.2"); d == nil || d.MAC != "52:54:00:aa:bb:cc" {
		t.Errorf("网关节点应合并 ARP 补的 MAC, got %+v", d)
	}
	// 连边去重: 重复加同一条边只保留一条
	b.AddLink("192.168.94.20", "192.168.94.2", "route")
	b.AddLink("192.168.94.20", "192.168.94.2", "route")
	b.AddLink("192.168.94.22", "192.168.94.2", "route")
	if got := len(b.Links()); got != 2 {
		t.Fatalf("links = %d, want 2 (去重失败: %+v)", got, b.Links())
	}
}

func TestTopologyBuilderMerge(t *testing.T) {
	b := newTopologyBuilder()
	b.AddDevice(TopoDevice{IP: "10.0.0.5", Source: "arp", Online: true})
	// 后到的远程/清单信息补齐到已有节点
	b.AddDevice(TopoDevice{IP: "10.0.0.5", Source: "ping", Online: true, MAC: "aa:bb:cc:dd:ee:ff", Alias: "web-1", HostID: "abc", Hostname: "web1"})
	b.AddDevice(TopoDevice{IP: "10.0.0.5", Type: "gateway"})
	d := b.Get("10.0.0.5")
	if d == nil {
		t.Fatal("merge 后节点应存在")
	}
	if d.MAC != "aa:bb:cc:dd:ee:ff" || d.Alias != "web-1" || d.HostID != "abc" || d.Hostname != "web1" {
		t.Errorf("字段未合并: %+v", d)
	}
	if d.Source != "ping" {
		t.Errorf("source 应升级为 ping, got %q", d.Source)
	}
	if d.Type != "gateway" {
		t.Errorf("type 应升级为 gateway, got %q", d.Type)
	}
	// 自环/空端点连边忽略
	b.AddLink("10.0.0.5", "10.0.0.5", "route")
	b.AddLink("", "10.0.0.5", "reach")
	if got := len(b.Links()); got != 0 {
		t.Fatalf("自环/空端点应忽略, got %d", got)
	}
}

func TestTopologyBuilderLldpKey(t *testing.T) {
	b := newTopologyBuilder()
	b.AddDevice(TopoDevice{Hostname: "core-sw1", Type: "switch", Source: "lldp", Online: true})
	b.AddDevice(TopoDevice{Hostname: "core-sw1", Type: "switch", Source: "lldp", Online: true})
	if got := len(b.Devices()); got != 1 {
		t.Fatalf("lldp 节点应去重, got %d", got)
	}
	if got := b.Devices()[0].IP; got != "" {
		t.Errorf("lldp 节点 IP 应为空, got %q", got)
	}
}
