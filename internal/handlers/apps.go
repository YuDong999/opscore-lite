package handlers

// ── 应用与容器检测: 容器( docker/podman/containerd ) + Nginx 站点 + 健康状态 + 访问统计 ──
// 只读检测, 不做任何写操作; 本机 os/exec, 远程 remotePool.Exec(SSH)。

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"opscore/internal/remote"
)

// ===== 数据结构 =====

type AppContainer struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Image         string            `json:"image"`
	State         string            `json:"state"`
	Status        string            `json:"status"`
	Runtime       string            `json:"runtime"` // docker / podman / crictl / ctr
	Health        string            `json:"health"`  // ok / warn / down
	HealthNote    string            `json:"healthNote"`
	CreatedAt     string            `json:"createdAt"`
	RestartCount  int               `json:"restartCount"`
	RestartPolicy string            `json:"restartPolicy"`
	Ports         []string          `json:"ports,omitempty"`
	Labels        map[string]string `json:"labels,omitempty"`
	PodSandbox    bool              `json:"podSandbox,omitempty"`
	// ---- 详情字段 (detail 接口) ----
	Mounts      []ContainerMount `json:"mounts,omitempty"`
	MemoryLimit int64            `json:"memoryLimit,omitempty"` // bytes, 0=不限
	CPULimit    int64            `json:"cpuLimit,omitempty"`    // nano cpu, 0=不限
	Env         []string         `json:"env,omitempty"`
	StartedAt   string           `json:"startedAt,omitempty"`
	ExitCode    int              `json:"exitCode,omitempty"`
	Pid         int64            `json:"pid,omitempty"`
	Networks    []string         `json:"networks,omitempty"`
}

type ContainerMount struct {
	Type        string `json:"type"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
	ReadOnly    bool   `json:"readOnly"`
}

type AppSite struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	ServerNames []string `json:"serverNames"`
	Listens     []string `json:"listens"`
	Type        string   `json:"type"` // proxy / static / unknown
	Root        string   `json:"root,omitempty"`
	ProxyPass   string   `json:"proxyPass,omitempty"`
	SSL         bool     `json:"ssl"`
	AccessLog   string   `json:"accessLog"`
	ConfigPath  string   `json:"configPath,omitempty"`
	NginxActive bool     `json:"nginxActive"`
	HttpCode    int      `json:"httpCode"`
	Health      string   `json:"health"` // ok / warn / down
	HealthNote  string   `json:"healthNote"`
	ProxyTarget string   `json:"proxyTarget,omitempty"` // 顺链探测: proxyPass 目标 host:port
	ProxyCode   int      `json:"proxyCode,omitempty"`   // 顺链探测: 上游 HTTP 状态码
	ProxyNote   string   `json:"proxyNote,omitempty"`   // 顺链探测: 上游无响应等说明
}

type NginxInfo struct {
	Installed bool   `json:"installed"`
	Version   string `json:"version"`
	Active    bool   `json:"active"`
}

type HealthSummary struct {
	Ok   int `json:"ok"`
	Warn int `json:"warn"`
	Down int `json:"down"`
	Skip int `json:"skip"` // 因前置条件不满足而未探测(如 nginx 未运行), 不算异常
}

type AppsResp struct {
	Nginx      NginxInfo        `json:"nginx"`
	Containers []AppContainer   `json:"containers"`
	Sites      []AppSite        `json:"sites"`
	Summary    HealthSummary    `json:"summary"`
	Runtime    string           `json:"runtime"`
	Note       string           `json:"note,omitempty"`
	Errors     []string         `json:"errors,omitempty"`
}

type IPCount struct {
	IP    string `json:"ip"`
	Count int    `json:"count"`
}

type StatusCount struct {
	Code  string `json:"code"`
	Count int    `json:"count"`
}

type SeriesPoint struct {
	T string `json:"t"`
	C int    `json:"c"`
}

type AppSiteStats struct {
	Site      string        `json:"site"`
	Win       string        `json:"win"`
	Total     int           `json:"total"`
	InWindow  int           `json:"inWindow"`
	TopIPs    []IPCount     `json:"topIPs"`
	Status    []StatusCount `json:"status"`
	Series    []SeriesPoint `json:"series"`
	Error     string        `json:"error,omitempty"`
}

// ===== 统一执行 (本机 / 远程) =====

func execAppsCmds(hostID string, cmds map[string]string) map[string]remote.Result {
	out := make(map[string]remote.Result, len(cmds))
	if hostID == "" {
		if runtime.GOOS == "windows" {
			note := "本机为 Windows 管理端, 容器/站点检测请选择远程 Linux 主机"
			for k := range cmds {
				out[k] = remote.Result{Error: note}
			}
			return out
		}
		for k, c := range cmds {
			o, err := exec.Command("sh", "-c", c).CombinedOutput()
			if err != nil {
				out[k] = remote.Result{Error: fmt.Sprintf("%v: %s", err, strings.TrimSpace(string(o)))}
			} else {
				out[k] = remote.Result{Output: strings.TrimSpace(string(o))}
			}
		}
		return out
	}
	h := resolveAnsibleHost(hostID)
	if h == nil || h.Platform == "win" || h.Addr == "" {
		for k := range cmds {
			out[k] = remote.Result{Error: "主机未找到或平台不支持"}
		}
		return out
	}
	return remotePool.Exec(resolveRemoteHost(*h), cmds)
}

// ===== 容器检测 =====

func detectRuntime(res map[string]remote.Result) string {
	for _, rt := range []string{"docker", "podman", "crictl", "ctr"} {
		if res[rt+"_path"].Error == "" && strings.TrimSpace(res[rt+"_path"].Output) != "" {
			return rt
		}
	}
	return ""
}

func collectContainers(runtime string, res map[string]remote.Result) []AppContainer {
	if runtime == "" {
		return nil
	}
	var list []AppContainer
	switch runtime {
	case "docker", "podman":
		raw := res["ps"]
		if raw.Error != "" {
			return nil
		}
		for _, line := range strings.Split(raw.Output, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var j struct {
				ID      string          `json:"ID"`
				Names   json.RawMessage `json:"Names"` // docker: "name" / podman: ["name"]
				Image   string          `json:"Image"`
				State   string          `json:"State"`
				Status  string          `json:"Status"`
				Created json.RawMessage `json:"Created"`  // podman
				CreatedAt json.RawMessage `json:"CreatedAt"` // docker
				Ports   json.RawMessage `json:"Ports"` // docker: "80->80/tcp" / podman: [{"HostPort":..}]
				Labels  json.RawMessage `json:"Labels"`
			}
			if err := json.Unmarshal([]byte(line), &j); err != nil {
				continue
			}
			name := firstContainerName(j.Names)
			created := firstStr(j.Created)
			if created == "" {
				created = firstStr(j.CreatedAt)
			}
			var labels map[string]string
			if len(j.Labels) > 0 && j.Labels[0] == '{' {
				_ = json.Unmarshal(j.Labels, &labels)
			}
			c := AppContainer{
				ID:        shortID(j.ID),
				Name:      name,
				Image:     j.Image,
				State:     j.State,
				Status:    j.Status,
				Runtime:   runtime,
				CreatedAt: created,
				Labels:    labels,
				Ports:     splitPortsFlex(j.Ports),
			}
			if c.Status == "" {
				c.Status = c.State
			}
			c.Health, c.HealthNote = containerHealth(j.State, 0)
			list = append(list, c)
		}
	case "crictl":
		raw := res["ps"]
		if raw.Error != "" {
			return nil
		}
		var j struct {
			Items []struct {
				ID     string `json:"id"`
				PodID  string `json:"podSandboxId"`
				Meta   struct {
					Name   string `json:"name"`
					Labels map[string]string `json:"labels"`
				} `json:"metadata"`
				State string `json:"state"`
				Image struct {
					Image string `json:"image"`
				} `json:"image"`
				CreatedAt int64 `json:"createdAt"`
			} `json:"items"`
		}
		if err := json.Unmarshal([]byte(raw.Output), &j); err != nil {
			return nil
		}
		for _, it := range j.Items {
			c := AppContainer{
				ID:        shortID(it.ID),
				Name:      it.Meta.Name,
				Image:     it.Image.Image,
				State:     crictlState(it.State),
				Status:    crictlState(it.State),
				Runtime:   "crictl",
				CreatedAt: unixMillis(it.CreatedAt),
				Labels:    it.Meta.Labels,
				PodSandbox: it.PodID == "",
			}
			c.Health, c.HealthNote = containerHealth(c.State, 0)
			list = append(list, c)
		}
	case "ctr":
		raw := res["ps"]
		if raw.Error != "" {
			return nil
		}
		for _, line := range strings.Split(raw.Output, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			list = append(list, AppContainer{
				ID: shortID(line), Name: line, State: "unknown", Status: "unknown", Runtime: "ctr",
				Health: "warn", HealthNote: "ctr 仅列出 ID, 详情有限",
			})
		}
	}
	return list
}

func containerHealth(state string, restartCount int) (health, note string) {
	switch state {
	case "running", "CONTAINER_RUNNING":
		if restartCount > 0 {
			return "warn", fmt.Sprintf("运行中但已重启 %d 次", restartCount)
		}
		return "ok", "运行中"
	case "restarting", "paused", "CONTAINER_PAUSED":
		return "warn", "状态: " + state
	case "exited", "dead", "created", "CONTAINER_EXITED", "CONTAINER_CREATED":
		return "down", "状态: " + state
	default:
		return "warn", "状态: " + state
	}
}

// containerDetail 获取容器 inspect 参数 (docker/podman 用 inspect; crictl 用 inspect)
func collectContainerDetail(runtime, id, hostID string) *AppContainer {
	cmds := map[string]string{}
	switch runtime {
	case "docker", "podman":
		cmds["inspect"] = runtime + " inspect " + id
	case "crictl":
		cmds["inspect"] = "crictl inspect " + id
	default:
		return nil
	}
	res := execAppsCmds(hostID, cmds)
	raw := res["inspect"]
	if raw.Error != "" {
		return &AppContainer{ID: id, Runtime: runtime, Health: "down", HealthNote: "inspect 失败: " + raw.Error}
	}

	if runtime == "crictl" {
		var j struct {
			Status struct {
				ID     string `json:"id"`
				State  string `json:"state"`
				Image  struct {
					Image string `json:"image"`
				} `json:"image"`
				CreatedAt int64             `json:"createdAt"`
				StartedAt int64             `json:"startedAt"`
				Labels    map[string]string `json:"labels"`
				Mounts    []struct {
					ContainerPath string `json:"containerPath"`
					HostPath      string `json:"hostPath"`
					Readonly      bool   `json:"readonly"`
				} `json:"mounts"`
				ExitCode int64 `json:"exitCode"`
				Pid      int64 `json:"pid"`
			} `json:"status"`
			Info struct {
				Config struct {
					Env []string `json:"env"`
				} `json:"config"`
			} `json:"info"`
		}
		if err := json.Unmarshal([]byte(raw.Output), &j); err != nil {
			return &AppContainer{ID: id, Runtime: runtime, Health: "down", HealthNote: "inspect 解析失败"}
		}
		state := crictlState(j.Status.State)
		c := &AppContainer{
			ID: shortID(j.Status.ID), Image: j.Status.Image.Image, State: state, Status: state,
			Runtime: "crictl", CreatedAt: unixMillis(j.Status.CreatedAt), StartedAt: unixMillis(j.Status.StartedAt),
			Labels: j.Status.Labels, ExitCode: int(j.Status.ExitCode), Pid: j.Status.Pid,
			Env: j.Info.Config.Env,
		}
		for _, m := range j.Status.Mounts {
			c.Mounts = append(c.Mounts, ContainerMount{Type: "bind", Source: m.HostPath, Destination: m.ContainerPath, ReadOnly: m.Readonly})
		}
		c.Health, c.HealthNote = containerHealth(state, 0)
		return c
	}

	// docker / podman inspect: JSON 数组
	var arr []json.RawMessage
	if err := json.Unmarshal([]byte(raw.Output), &arr); err != nil || len(arr) == 0 {
		return &AppContainer{ID: id, Runtime: runtime, Health: "down", HealthNote: "inspect 解析失败"}
	}
	var d struct {
		ID      string `json:"Id"`
		Created string `json:"Created"`
		Name    string `json:"Name"`
		Config  struct {
			Image  string            `json:"Image"`
			Env    []string          `json:"Env"`
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
		State struct {
			Status    string `json:"Status"`
			StartedAt string `json:"StartedAt"`
			ExitCode  int    `json:"ExitCode"`
			Pid       int    `json:"Pid"`
		} `json:"State"`
		HostConfig struct {
			RestartPolicy struct {
				Name string `json:"Name"`
			} `json:"RestartPolicy"`
			Memory   int64 `json:"Memory"`
			NanoCpus int64 `json:"NanoCpus"`
		} `json:"HostConfig"`
		RestartCount int `json:"RestartCount"`
		Mounts       []struct {
			Type        string `json:"Type"`
			Source      string `json:"Source"`
			Destination string `json:"Destination"`
			RW          bool   `json:"RW"`
		} `json:"Mounts"`
		NetworkSettings struct {
			Networks map[string]struct {
				Aliases []string `json:"Aliases"`
			} `json:"Networks"`
		} `json:"NetworkSettings"`
	}
	if err := json.Unmarshal(arr[0], &d); err != nil {
		return &AppContainer{ID: id, Runtime: runtime, Health: "down", HealthNote: "inspect 解析失败"}
	}
	c := &AppContainer{
		ID: shortID(d.ID), Name: strings.TrimPrefix(d.Name, "/"), Image: d.Config.Image,
		State: d.State.Status, Status: d.State.Status, Runtime: runtime,
		CreatedAt: fmtRFC3339(d.Created), StartedAt: fmtRFC3339(d.State.StartedAt),
		RestartCount: d.RestartCount, RestartPolicy: d.HostConfig.RestartPolicy.Name,
		MemoryLimit: d.HostConfig.Memory, CPULimit: d.HostConfig.NanoCpus,
		Env: d.Config.Env, Labels: d.Config.Labels,
		ExitCode: d.State.ExitCode, Pid: int64(d.State.Pid),
	}
	for _, m := range d.Mounts {
		c.Mounts = append(c.Mounts, ContainerMount{Type: m.Type, Source: m.Source, Destination: m.Destination, ReadOnly: !m.RW})
	}
	for n := range d.NetworkSettings.Networks {
		c.Networks = append(c.Networks, n)
	}
	c.Health, c.HealthNote = containerHealth(d.State.Status, d.RestartCount)
	return c
}

// ===== Nginx 站点 =====

type rawSite struct {
	serverNames []string
	listens     []string
	root        string
	proxyPass   string
	sslCert     string
	accessLog   string
	configPath  string
}

func parseNginxSites(out string) []rawSite {
	var sites []rawSite
	var cur *rawSite
	cfgPath := ""
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# configuration file ") {
			cfgPath = strings.Trim(strings.TrimPrefix(line, "# configuration file "), " :")
			continue
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if line == "server {" {
			cur = &rawSite{configPath: cfgPath}
			continue
		}
		if line == "}" {
			if cur != nil {
				sites = append(sites, *cur)
				cur = nil
			}
			continue
		}
		if cur == nil || !strings.HasSuffix(line, ";") {
			continue
		}
		body := strings.TrimSuffix(line, ";")
		f := strings.Fields(body)
		if len(f) == 0 {
			continue
		}
		switch f[0] {
		case "listen":
			port := listenPort(strings.Join(f[1:], " "))
			if port != "" && !containsStr(cur.listens, port) {
				cur.listens = append(cur.listens, port)
			}
		case "server_name":
			for _, v := range f[1:] {
				v = strings.Trim(v, `"`)
				if v == "_" || v == "" || v == "default_server" {
					continue
				}
				if !containsStr(cur.serverNames, v) {
					cur.serverNames = append(cur.serverNames, v)
				}
			}
		case "root":
			cur.root = f[1]
		case "proxy_pass":
			cur.proxyPass = strings.TrimSuffix(f[1], ";")
		case "ssl_certificate":
			cur.sslCert = f[1]
		case "access_log":
			if f[1] != "off" {
				cur.accessLog = f[1]
			}
		}
	}
	return sites
}

func listenPort(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "default_server", "")
	s = strings.TrimSpace(s)
	if i := strings.Index(s, " ssl"); i > 0 {
		s = s[:i]
	}
	if i := strings.Index(s, " http2"); i > 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if i := strings.LastIndex(s, ":"); i >= 0 {
		s = s[i+1:]
	}
	s = strings.Trim(s, "[]")
	if _, err := strconv.Atoi(s); err != nil {
		return ""
	}
	return s
}

func buildSites(nginxActive bool, raw []rawSite) []AppSite {
	out := make([]AppSite, 0, len(raw))
	for i, s := range raw {
		names := s.serverNames
		if len(names) == 0 {
			names = []string{fmt.Sprintf("server-%d", i+1)}
		}
		typ := "unknown"
		switch {
		case s.proxyPass != "":
			typ = "proxy"
		case s.root != "":
			typ = "static"
		}
		al := s.accessLog
		if al == "" {
			al = "/var/log/nginx/access.log"
		}
		out = append(out, AppSite{
			ID: names[0], Name: names[0], ServerNames: names, Listens: s.listens,
			Type: typ, Root: s.root, ProxyPass: s.proxyPass,
			SSL: s.sslCert != "", AccessLog: al, ConfigPath: s.configPath,
			NginxActive: nginxActive,
		})
	}
	return out
}

// probeSiteHealth 对站点各 listen 端口做本地只读 HTTP 探测; proxy 站点顺链探测上游目标
func probeSiteHealth(hostID string, sites []AppSite) {
	for i := range sites {
		s := &sites[i]
		if !s.NginxActive {
			s.Health = "skip"
			s.HealthNote = "nginx 未运行, 未探测(不计入异常)"
			continue
		}
		code, note := probePorts(hostID, s.Listens)
		s.HttpCode = code
		// proxy 站点: 解析 proxyPass 目标, 顺链探测上游, 用于定位 502/504 故障点
		if s.ProxyPass != "" && (code == 0 || code >= 400) {
			target := proxyTargetOf(s.ProxyPass)
			if target != "" {
				s.ProxyTarget = target
				pcode, pnote := probeTarget(hostID, target)
				s.ProxyCode, s.ProxyNote = pcode, pnote
			}
		}
		switch {
		case code >= 200 && code < 400:
			s.Health = "ok"
			s.HealthNote = fmt.Sprintf("HTTP %d", code)
		case code >= 400 && code < 500:
			s.Health = "warn"
			s.HealthNote = fmt.Sprintf("HTTP %d", code)
		case code >= 500:
			s.Health = "down"
			s.HealthNote = fmt.Sprintf("HTTP %d", code)
		default:
			s.Health = "down"
			s.HealthNote = note
		}
		// 站点异常但上游正常: 故障点在 nginx 站点配置, 补充说明
		if s.Health != "ok" && s.ProxyTarget != "" && s.ProxyCode >= 200 && s.ProxyCode < 400 {
			s.HealthNote += fmt.Sprintf(" (上游 %s 正常, 问题在站点/nginx 层)", s.ProxyTarget)
		}
	}
}

func probePorts(hostID string, ports []string) (int, string) {
	if len(ports) == 0 {
		return 0, "无监听端口"
	}
	// 各端口并发探测(此前串行, 多端口站点最坏 3s×N), 单端口超时 3s→1s
	type probeRes struct{ code int }
	ch := make(chan int, len(ports))
	for _, p := range ports {
		go func(p string) {
			cmds := map[string]string{"curl": fmt.Sprintf(`curl -s -o /dev/null -m 1 -w '%%{http_code}' http://127.0.0.1:%s/ 2>/dev/null`, p)}
			res := execAppsCmds(hostID, cmds)
			code, err := strconv.Atoi(strings.TrimSpace(res["curl"].Output))
			if err == nil && code > 0 {
				ch <- code
			} else {
				ch <- 0
			}
		}(p)
	}
	fallback := 0
	for range ports {
		code := <-ch
		if code >= 200 && code < 400 {
			return code, ""
		}
		if code > fallback {
			fallback = code
		}
	}
	if fallback > 0 {
		return fallback, ""
	}
	return 0, "端口无响应"
}

// proxyTargetOf 从 proxy_pass 中提取 host:port (无端口时补 80); unix socket 无法 curl 探测返回空
func proxyTargetOf(proxyPass string) string {
	p := strings.TrimSpace(proxyPass)
	if p == "" {
		return ""
	}
	if i := strings.Index(p, "://"); i >= 0 {
		p = p[i+3:]
	}
	if i := strings.IndexAny(p, "/?#"); i >= 0 {
		p = p[:i]
	}
	if p == "" {
		return ""
	}
	// unix socket: http://unix:/var/run/xxx.sock:/path
	if strings.HasPrefix(p, "unix:") {
		return ""
	}
	host := p
	if i := strings.LastIndex(p, ":"); i >= 0 && !strings.Contains(p[i+1:], "]") {
		host = p[:i]
		port := p[i+1:]
		if port != "" {
			return p
		}
		return host + ":80"
	}
	return host + ":80"
}

// probeTarget 对上游 host:port 做只读 HTTP 探测
func probeTarget(hostID, target string) (int, string) {
	cmds := map[string]string{"curl": fmt.Sprintf(`curl -s -o /dev/null -m 3 -w '%%{http_code}' http://%s/ 2>/dev/null`, target)}
	res := execAppsCmds(hostID, cmds)
	if res["curl"].Error != "" {
		return 0, "上游无响应 (连接失败)"
	}
	code, err := strconv.Atoi(strings.TrimSpace(res["curl"].Output))
	if err != nil || code <= 0 {
		return 0, "上游无响应"
	}
	return code, ""
}

// ===== 访问统计 =====

func collectSiteStats(hostID, site, accessLog, win string) AppSiteStats {
	st := AppSiteStats{Site: site, Win: win}
	window := time.Hour
	switch win {
	case "6h":
		window = 6 * time.Hour
	case "24h":
		window = 24 * time.Hour
	}
	cmds := map[string]string{"log": "tail -n 5000 " + accessLog + " 2>/dev/null"}
	res := execAppsCmds(hostID, cmds)
	if res["log"].Error != "" {
		st.Error = "读取日志失败: " + res["log"].Error
		return st
	}
	now := time.Now()
	layout := "02/Jan/2006:15:04:05 -0700"
	countByIP := map[string]int{}
	countByStatus := map[string]int{}
	byMinute := map[string]int{}
	start := now.Add(-window)
	st.Total = 0
	for _, line := range strings.Split(res["log"].Output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 9 {
			continue
		}
		ip := f[0]
		status := f[8]
		if status == "-" {
			status = "0"
		}
		countByIP[ip]++
		countByStatus[status]++
		st.Total++
		// 时间窗口
		tRaw := strings.Trim(f[3], "[]")
		if t, err := time.Parse(layout, tRaw); err == nil {
			if t.After(start) {
				st.InWindow++
				byMinute[t.Format("15:04")]++
			}
		}
	}
	// TOP IP
	ips := make([]IPCount, 0, len(countByIP))
	for k, v := range countByIP {
		ips = append(ips, IPCount{IP: k, Count: v})
	}
	sort.Slice(ips, func(a, b int) bool { return ips[a].Count > ips[b].Count })
	if len(ips) > 10 {
		ips = ips[:10]
	}
	st.TopIPs = ips
	// 状态码
	sc := make([]StatusCount, 0, len(countByStatus))
	for k, v := range countByStatus {
		sc = append(sc, StatusCount{Code: k, Count: v})
	}
	sort.Slice(sc, func(a, b int) bool { return sc[a].Count > sc[b].Count })
	st.Status = sc
	// 时间序列 (近窗口, 按分钟排序)
	times := make([]string, 0, len(byMinute))
	for k := range byMinute {
		times = append(times, k)
	}
	sort.Strings(times)
	for _, k := range times {
		st.Series = append(st.Series, SeriesPoint{T: k, C: byMinute[k]})
	}
	return st
}

// ===== Handlers =====

// AppsHandler GET /api/core/apps?host=<id>
// AppsHandler 应用与容器总览(读): 5s TTL 缓存 + singleflight,
// 切换主机/重复进入时秒回; 探测类冷访问由后台合并重建。
func AppsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ServeCachedJSON(w, r, 5*time.Second, func() any { return appsBuild(r) })
}

func appsBuild(r *http.Request) any {
	hostID := r.URL.Query().Get("host")

	cmds := map[string]string{
		"docker_path":  "command -v docker 2>/dev/null",
		"podman_path":  "command -v podman 2>/dev/null",
		"crictl_path":  "command -v crictl 2>/dev/null",
		"ctr_path":     "command -v ctr 2>/dev/null",
		"nginx_path":   "command -v nginx 2>/dev/null",
		"nginx_v":      "nginx -V 2>&1",
		"nginx_t":      "nginx -T 2>/dev/null",
		"nginx_active": "systemctl is-active nginx 2>/dev/null || true",
		"ps":           "", // 由运行时决定, 下面补
	}
	rt := ""
	{
		// 探测运行时后再补 ps 命令
		probe := map[string]string{
			"docker_path": cmds["docker_path"], "podman_path": cmds["podman_path"],
			"crictl_path": cmds["crictl_path"], "ctr_path": cmds["ctr_path"],
			"nginx_path": cmds["nginx_path"], "nginx_v": cmds["nginx_v"],
			"nginx_t": cmds["nginx_t"], "nginx_active": cmds["nginx_active"],
		}
		res := execAppsCmds(hostID, probe)
		rt = detectRuntime(res)
		psCmd := ""
		switch rt {
		case "docker", "podman":
			psCmd = rt + " ps -a --no-trunc --format '{{json .}}'"
		case "crictl":
			psCmd = "crictl ps -a -o json"
		case "ctr":
			psCmd = "ctr -n k8s.io c list -q 2>/dev/null"
		}
		probe["ps"] = psCmd
		res2 := execAppsCmds(hostID, map[string]string{"ps": psCmd})
		res["ps"] = res2["ps"]

		// 组装
		nginxActive := strings.TrimSpace(res["nginx_active"].Output) == "active"
		installed := res["nginx_path"].Error == "" && strings.TrimSpace(res["nginx_path"].Output) != ""

		containers := collectContainers(rt, res)
		sites := buildSites(nginxActive, parseNginxSites(res["nginx_t"].Output))
		probeSiteHealth(hostID, sites)

		ver := ""
		if v := strings.TrimSpace(res["nginx_v"].Output); v != "" {
			if i := strings.Index(v, "nginx/"); i >= 0 {
				ver = v[i+len("nginx/"):]
				if j := strings.IndexByte(ver, ' '); j > 0 {
					ver = ver[:j]
				}
			}
		}

		summary := HealthSummary{}
		for _, c := range containers {
			switch c.Health {
			case "ok":
				summary.Ok++
			case "warn":
				summary.Warn++
			default:
				summary.Down++
			}
		}
		for _, s := range sites {
			switch s.Health {
			case "ok":
				summary.Ok++
			case "warn":
				summary.Warn++
			case "skip":
				summary.Skip++
			default:
				summary.Down++
			}
		}

		resp := AppsResp{
			Nginx:      NginxInfo{Installed: installed, Version: ver, Active: nginxActive},
			Containers: containers,
			Sites:      sites,
			Summary:    summary,
			Runtime:    rt,
		}
		if hostID == "" && runtime.GOOS == "windows" {
			resp.Note = "本机为 Windows 管理端, 容器/站点检测请选择远程 Linux 主机"
		}
		if rt == "" && hostID != "" {
			resp.Errors = append(resp.Errors, "未检测到 docker / podman / containerd 运行时")
		}
		if !installed {
			resp.Errors = append(resp.Errors, "未检测到 nginx")
		}
		return resp
	}
}

// AppContainerDetailHandler GET /api/core/apps/containers/detail?id=<id>&runtime=<rt>&host=<host>
func AppContainerDetailHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	id := q.Get("id")
	rt := q.Get("runtime")
	hostID := q.Get("host")
	if id == "" || rt == "" {
		writeErr(w, "缺少 id / runtime 参数", http.StatusBadRequest)
		return
	}
	WriteJSON(w, collectContainerDetail(rt, id, hostID))
}

// AppSiteStatsHandler GET /api/core/apps/sites/stats?site=<name>&log=<accessLog>&win=<1h|6h|24h>&host=<host>
func AppSiteStatsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	WriteJSON(w, collectSiteStats(q.Get("host"), q.Get("site"), q.Get("log"), q.Get("win")))
}

// ===== helpers =====

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func splitPorts(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" || s == "[]" {
		return nil
	}
	parts := strings.Split(s, ", ")
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(strings.Trim(p, "[]"))
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// firstStr 解析 RawMessage 为字符串(JSON 字符串字段通用兜底)。
func firstStr(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return ""
}

// firstContainerName 兼容 docker("name") 与 podman(["name","alias"]) 的 Names 字段。
func firstContainerName(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	if raw[0] == '[' {
		var arr []string
		if json.Unmarshal(raw, &arr) == nil && len(arr) > 0 {
			return strings.TrimSpace(arr[0])
		}
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.Trim(strings.TrimSpace(s), "[]")
	}
	return ""
}

// splitPortsFlex 兼容 docker(字符串) 与 podman(数组) 的 Ports 字段。
func splitPortsFlex(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	if raw[0] == '[' {
		var arr []map[string]any
		if json.Unmarshal(raw, &arr) != nil {
			return nil
		}
		var out []string
		for _, m := range arr {
			hp, _ := m["HostPort"].(string)
			cb, _ := m["ContainerPort"].(string)
			if cb == "" {
				continue
			}
			if hp != "" {
				out = append(out, hp+"->"+cb)
			} else {
				out = append(out, cb)
			}
		}
		return out
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return splitPorts(s)
	}
	return nil
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func crictlState(s string) string {
	switch s {
	case "CONTAINER_RUNNING":
		return "running"
	case "CONTAINER_EXITED":
		return "exited"
	case "CONTAINER_CREATED":
		return "created"
	case "CONTAINER_PAUSED":
		return "paused"
	default:
		return strings.ToLower(s)
	}
}

func unixMillis(ms int64) string {
	if ms == 0 {
		return ""
	}
	return time.UnixMilli(ms).Format("2006-01-02 15:04:05")
}

// fmtRFC3339 将 docker inspect 返回的 RFC3339 时间转为本地可读格式, 解析失败时原样返回。
func fmtRFC3339(s string) string {
	if s == "" {
		return ""
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Local().Format("2006-01-02 15:04:05 -0700 MST")
		}
	}
	return s
}

// 让 net 包引用保留 (listenPort 解析用)
var _ = net.ParseIP
