package metrics

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	ssPortRe = regexp.MustCompile(`:(\d+)$`)
	ssProcRe = regexp.MustCompile(`users:\(\("([^"]+)"`)
)

// ParseSS 解析 `ss -tlnp` / `ss -ulnp` 原始输出。
// 兼容新旧版 ss: 老版无 Netid 列(State 开头), 新版有(Netid 开头), 故不依赖固定列号,
// 用正则定位 "local:port" 与 users:(("进程名",pid=...,fd=...) 段。
func ParseSS(out string, proto string) []ListenInfo {
	var res []ListenInfo
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// 跳过列头行
		if strings.HasPrefix(line, "State") || strings.HasPrefix(line, "Netid") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 4 {
			continue
		}
		local := ""
		port := 0
		proc := ""
		for _, p := range parts {
			if m := ssPortRe.FindStringSubmatch(p); m != nil {
				local = p
				port, _ = strconv.Atoi(m[1])
			} else if m := ssProcRe.FindStringSubmatch(p); m != nil {
				proc = m[1]
			}
		}
		if port == 0 {
			continue
		}
		res = append(res, ListenInfo{Protocol: proto, Local: local, Port: port, Process: proc})
	}
	return res
}
