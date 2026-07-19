package handlers

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"
)

// Network 返回网络接口、流量统计与监听端口。
func Network(w http.ResponseWriter, r *http.Request) {
	type Iface struct {
		Name  string   `json:"name"`
		MTU   int      `json:"mtu"`
		Flags []string `json:"flags"`
		Addrs []string `json:"addrs"`
	}
	var ifaces []Iface
	ifaceErr := ""
	if il, err := net.Interfaces(); err == nil {
		for _, i := range il {
			addrs := []string{}
			for _, a := range i.Addrs {
				addrs = append(addrs, a.Addr)
			}
			ifaces = append(ifaces, Iface{Name: i.Name, MTU: i.MTU, Flags: i.Flags, Addrs: addrs})
		}
	} else {
		ifaceErr = err.Error()
	}

	type Listen struct {
		Protocol string `json:"protocol"`
		Local    string `json:"local"`
		Port     int    `json:"port"`
		PID      int32  `json:"pid"`
		Process  string `json:"process"`
		Service  string `json:"service"`
		Category string `json:"category"`
		Icon     string `json:"icon"`
		KnownAs  string `json:"knownAs"`
		Verified bool   `json:"verified"`
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
			protocol := "TCP"
			if c.Type == 2 {
				protocol = "UDP"
			}
			li := Listen{Protocol: protocol, Local: local, Port: port, PID: c.Pid}
			if hint, ok := recognizePort(uint16(port)); ok {
				li.KnownAs = hint.Label
			}
			if c.Pid > 0 {
				if p, perr := process.NewProcess(c.Pid); perr == nil {
					if nm, nerr := p.Name(); nerr == nil {
						li.Process = nm
						if meta, ok := recognizeProc(nm); ok {
							li.Service = meta.Label
							li.Category = meta.Category
							li.Icon = meta.Icon
							if li.KnownAs != "" && (meta.Label == li.KnownAs || meta.Category == categoryOfPort(uint16(port))) {
								li.Verified = true
							}
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
