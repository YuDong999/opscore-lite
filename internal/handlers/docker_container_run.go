package handlers

// ── 容器管理插件 · 容器创建/重建 ──
// 端口映射 / 卷挂载 / 文件挂载(单文件 bind) / 环境变量 / 重启策略。
// Docker 不支持在运行中修改端口与挂载, "编辑"语义 = inspect 旧配置 → rm → 按新配置重建(Portainer 同方案)。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

var (
	reContainerKey = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	reHostPort     = regexp.MustCompile(`^[0-9]{1,5}$`)
	rePath         = regexp.MustCompile(`^[^"'` + "`" + `\x00-\x1f]{1,500}$`)
)

type portMapping struct {
	HostIP   string `json:"hostIp,omitempty"`
	HostPort string `json:"hostPort"`
	CtrlPort string `json:"ctrlPort"`
	Proto    string `json:"proto"` // tcp | udp
}

type volumeMapping struct {
	HostPath string `json:"hostPath"` // 卷名或宿主路径(文件/目录)
	CtrlPath string `json:"ctrlPath"`
	ReadOnly bool   `json:"readOnly"`
}

type envKV struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type containerRunBody struct {
	Host       string          `json:"host"`
	Name       string          `json:"name"`
	Image      string          `json:"image"`
	Command    []string        `json:"command,omitempty"` // 覆盖 CMD
	Ports      []portMapping   `json:"ports"`
	Volumes    []volumeMapping `json:"volumes"`
	Envs       []envKV         `json:"envs"`
	Restart    string          `json:"restart"` // no|on-failure|always|unless-stopped
	Network    string          `json:"network,omitempty"`
	RecreateOf string          `json:"recreateOf,omitempty"` // 非空=重建模式: 先 rm 该容器
	// ── Run 完整参数(Docker Desktop 对标) ──
	MemLimit      string   `json:"memLimit,omitempty"`      // 512m/1g…
	User          string   `json:"user,omitempty"`          // --user uid:gid 或名字
	Hostname      string   `json:"hostname,omitempty"`      // --hostname
	CapAdd        []string `json:"capAdd,omitempty"`        // --cap-add (如 SYS_ADMIN)
	Privileged    bool     `json:"privileged,omitempty"`    // --privileged
	Tmpfs         []string `json:"tmpfs,omitempty"`         // --tmpfs 挂载
	WorkDir       string   `json:"workdir,omitempty"`       // -w
	Labels        []string `json:"labels,omitempty"`        // -l key=val
	AutoHealth    bool     `json:"autoHealth,omitempty"`    // --health-cmd=curl loop (简化: curl -f http://127.0.0.1)
	AddIP         string   `json:"addIP,omitempty"`         // --ip 固定容器 IP(需自定义网络)
	ParentRestart int      `json:"restartDelay,omitempty"`  // 重启延时秒(仅 restart=on-failure 时生效)
}

// DockerContainerRunHandler POST 创建/重建容器
func DockerContainerRunHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(containersPluginID, w) {
		return
	}
	var b containerRunBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		WriteJSON(w, map[string]any{"ok": false, "error": "invalid body"})
		return
	}
	if !reContainerName.MatchString(b.Name) {
		WriteJSON(w, map[string]any{"ok": false, "error": "非法容器名"})
		return
	}
	if !reDockerImage.MatchString(b.Image) || strings.Contains(b.Image, "..") {
		WriteJSON(w, map[string]any{"ok": false, "error": "非法镜像名"})
		return
	}
	switch b.Restart {
	case "", "no", "on-failure", "always", "unless-stopped":
	default:
		WriteJSON(w, map[string]any{"ok": false, "error": "restart 必须是 no/on-failure/always/unless-stopped"})
		return
	}
	for _, p := range b.Ports {
		if !reHostPort.MatchString(p.HostPort) || !reHostPort.MatchString(p.CtrlPort) {
			WriteJSON(w, map[string]any{"ok": false, "error": "端口必须为数字: " + p.HostPort + "->" + p.CtrlPort})
			return
		}
		hp, _ := strconv.Atoi(p.HostPort)
		cp, _ := strconv.Atoi(p.CtrlPort)
		if hp < 1 || hp > 65535 || cp < 1 || cp > 65535 || (p.Proto != "" && p.Proto != "tcp" && p.Proto != "udp") {
			WriteJSON(w, map[string]any{"ok": false, "error": "端口范围 1-65535, proto 仅 tcp/udp"})
			return
		}
	}
	for _, v := range b.Volumes {
		badSrc := !rePath.MatchString(v.HostPath) || strings.HasPrefix(v.HostPath, "-")
		if badSrc || (!strings.HasPrefix(v.HostPath, "/")) {
			WriteJSON(w, map[string]any{"ok": false, "error": "宿主路径须为绝对路径: " + v.HostPath})
			return
		}
		if !rePath.MatchString(v.CtrlPath) || !strings.HasPrefix(v.CtrlPath, "/") {
			WriteJSON(w, map[string]any{"ok": false, "error": "容器内路径必须以 / 开头: " + v.CtrlPath})
			return
		}
		if !strings.HasPrefix(v.CtrlPath, "/") {
			WriteJSON(w, map[string]any{"ok": false, "error": "容器内路径必须以 / 开头: " + v.CtrlPath})
			return
		}
	}
	for _, e := range b.Envs {
		if e.Key == "" || !reContainerKey.MatchString(strings.ReplaceAll(e.Key, ".", "_")) {
			WriteJSON(w, map[string]any{"ok": false, "error": "非法环境变量名: " + e.Key})
			return
		}
	}

	rtCmd := dockerOrPodman(b.Host)
	if msg := runtimeReadOnly(rtCmd); msg != "" {
		WriteJSON(w, map[string]any{"ok": false, "error": msg})
		return
	}
	argv := []string{rtCmd, "run", "-d", "--name", b.Name}
	if b.Restart != "" && b.Restart != "no" {
		argv = append(argv, "--restart="+b.Restart)
	}
	if b.Network != "" && reContainerKey.MatchString(b.Network) {
		argv = append(argv, "--network", b.Network)
	}
	for _, p := range b.Ports {
		spec := ""
		if p.HostIP != "" && reHostPort.MatchString(p.HostIP) { // hostIp 字段复用为可选绑定 IP
			spec = p.HostIP + ":"
		}
		proto := p.Proto
		if proto == "" {
			proto = "tcp"
		}
		argv = append(argv, "-p", fmt.Sprintf("%s%s:%s/%s", spec, p.HostPort, p.CtrlPort, proto))
	}
	for _, v := range b.Volumes {
		mode := ""
		if v.ReadOnly {
			mode = ":ro"
		}
		argv = append(argv, "-v", fmt.Sprintf("%s:%s%s", v.HostPath, v.CtrlPath, mode))
	}
	for _, e := range b.Envs {
		argv = append(argv, "-e", fmt.Sprintf("%s=%s", e.Key, e.Value))
	}
	// ── Run 完整参数(Docker Desktop 对标) ──
	if b.MemLimit != "" {
		if m := parseMemArg(b.MemLimit); m != "" {
			argv = append(argv, "--memory", m, "--memory-swap", m)
		} else {
			WriteJSON(w, map[string]any{"ok": false, "error": "内存限制格式错误(如 512m/1g)"})
			return
		}
	}
	if b.User != "" && reContainerKey.MatchString(strings.ReplaceAll(b.User, ":", "_")) {
		argv = append(argv, "--user", b.User)
	}
	if b.Hostname != "" && reContainerKey.MatchString(b.Hostname) {
		argv = append(argv, "--hostname", b.Hostname)
	}
	if b.Privileged {
		argv = append(argv, "--privileged")
	}
	for _, ca := range b.CapAdd {
		if reContainerKey.MatchString(ca) {
			argv = append(argv, "--cap-add", ca)
		}
	}
	for _, tm := range b.Tmpfs {
		if rePath.MatchString(tm) && strings.HasPrefix(tm, "/") {
			argv = append(argv, "--tmpfs", tm)
		}
	}
	if b.WorkDir != "" && strings.HasPrefix(b.WorkDir, "/") && rePath.MatchString(b.WorkDir) {
		argv = append(argv, "-w", b.WorkDir)
	}
	for _, lb := range b.Labels {
		if reContainerKey.MatchString(strings.SplitN(lb, "=", 2)[0]) {
			argv = append(argv, "-l", lb)
		}
	}
	if b.AutoHealth {
		argv = append(argv, "--health-cmd", "curl -fsS http://127.0.0.1/ || exit 1",
			"--health-interval", "10s", "--health-retries", "5")
	}
	if b.AddIP != "" && reHostPort.MatchString(strings.ReplaceAll(b.AddIP, ".", "0")) {
		argv = append(argv, "--ip", b.AddIP)
	}
	if b.ParentRestart > 0 && b.Restart == "on-failure" {
		argv = append(argv, fmt.Sprintf("--restart=on-failure:%d", b.ParentRestart))
	}
	argv = append(argv, b.Image)
	argv = append(argv, b.Command...)

	// 重建模式: 先删旧容器(强制), 失败则中止
	if b.RecreateOf != "" {
		if !reContainerName.MatchString(b.RecreateOf) {
			WriteJSON(w, map[string]any{"ok": false, "error": "非法的待重建容器名"})
			return
		}
		out, rerr := RunOnTarget(b.Host, []string{rtCmd, "rm", "-f", b.RecreateOf})
		if rerr != nil && !strings.Contains(out, "No such object") {
			WriteJSON(w, map[string]any{"ok": false, "error": "删除旧容器失败: " + lastLines(out, 4)})
			return
		}
	}
	out, err := RunOnTarget(b.Host, argv)
	podmanFallback := false
	if err != nil && rtCmd != "podman" && (strings.Contains(out, "no such command") || strings.Contains(out, "is not a docker command")) {
		argv[0] = "podman"
		out, err = RunOnTarget(b.Host, argv)
		podmanFallback = true
	}
	if err != nil {
		dockerAudit(b.Host, "run", b.Name+"("+b.Image+")", err)
		WriteJSON(w, map[string]any{"ok": false, "error": lastLines(out, 8)})
		return
	}
	dockerAudit(b.Host, ternary(b.RecreateOf != "", "recreate", "run"),
		fmt.Sprintf("%s(%s) ports=%d volumes=%d envs=%d podman=%v", b.Name, b.Image,
			len(b.Ports), len(b.Volumes), len(b.Envs), podmanFallback), nil)
	InvalidateRespCache("/api/plugins/containers/list")
	InvalidateRespCache("/api/core/apps")
	WriteJSON(w, map[string]any{"ok": true, "name": b.Name, "id": strings.TrimSpace(lastLine(out))})
}

func lastLine(s string) string {
	s = strings.TrimRight(s, "\n\r ")
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		return s[i+1:]
	}
	return s
}

// ===== 容器配置读取(inspect → 结构化, 供编辑预填) =====

// DockerContainerConfigHandler GET ?host=&name= → 解析 docker inspect 为可编辑结构
func DockerContainerConfigHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(containersPluginID, w) {
		return
	}
	q := r.URL.Query()
	hostID, name := q.Get("host"), q.Get("name")
	if !reContainerName.MatchString(name) {
		WriteJSON(w, map[string]any{"ok": false, "error": "非法容器名"})
		return
	}
	rtCmd := dockerOrPodman(hostID)
	out, err := RunOnTarget(hostID, []string{rtCmd, "inspect", name})
	rt := rtCmd
	if err != nil && rtCmd != "podman" {
		out, err = RunOnTarget(hostID, []string{"podman", "inspect", name})
		rt = "podman"
		if err != nil {
			WriteJSON(w, map[string]any{"ok": false, "error": lastLines(out, 4)})
			return
		}
	}
	if err != nil {
		WriteJSON(w, map[string]any{"ok": false, "error": lastLines(out, 4)})
		return
	}
	var arr []map[string]any
	if jerr := json.Unmarshal([]byte(out), &arr); jerr != nil || len(arr) == 0 {
		WriteJSON(w, map[string]any{"ok": false, "error": "inspect 输出解析失败"})
		return
	}
	c := arr[0]

	cfg := map[string]any{
		"name":    strings.TrimPrefix(str(c["Name"]), "/"),
		"image":   str(deep(c, "Config", "Image")),
		"restart": str(deep(c, "HostConfig", "RestartPolicy", "Name")),
		"network": firstNetMode(c),
		"runtime": rt,
	}
	if mm, ok := deep(c, "HostConfig", "Memory").(float64); ok && mm > 0 {
		cfg["memLimit"] = fmt.Sprintf("%dm", int64(mm/1024/1024))
	}
	cfg["user"] = str(deep(c, "Config", "User"))
	cfg["hostname"] = str(deep(c, "Config", "Hostname"))
	cfg["workdir"] = str(deep(c, "Config", "WorkingDir"))
	if priv, ok := deep(c, "HostConfig", "Privileged").(bool); ok {
		cfg["privileged"] = priv
	}
	cfg["healthcheck"] = deep(c, "Config", "Healthcheck") != nil
	// 端口: HostConfig.PortBindings
	ports := []portMapping{}
	if bindings, ok := deep(c, "HostConfig", "PortBindings").(map[string]any); ok {
		for k, v := range bindings {
			proto := "tcp"
			ctrl := k
			if i := strings.Index(k, "/"); i >= 0 {
				ctrl, proto = k[:i], k[i+1:]
			}
			if list, ok := v.([]any); ok {
				for _, b0 := range list {
					bm, _ := b0.(map[string]any)
					ports = append(ports, portMapping{
						HostIP: str(bm["HostIP"]), HostPort: str(bm["HostPort"]),
						CtrlPort: ctrl, Proto: proto,
					})
				}
			}
		}
	}
	cfg["ports"] = ports
	// 卷/挂载: HostConfig.Binds (含文件挂载)
	vols := []volumeMapping{}
	if binds, ok := deep(c, "HostConfig", "Binds").([]any); ok {
		for _, b0 := range binds {
			bs, _ := b0.(string)
			parts := strings.Split(bs, ":")
			switch len(parts) {
			case 2:
				vols = append(vols, volumeMapping{HostPath: parts[0], CtrlPath: parts[1]})
			case 3:
				vols = append(vols, volumeMapping{HostPath: parts[0], CtrlPath: parts[1], ReadOnly: parts[2] == "ro"})
			}
		}
	}
	cfg["volumes"] = vols
	// 环境变量
	envs := []envKV{}
	if elist, ok := deep(c, "Config", "Env").([]any); ok {
		for _, e0 := range elist {
			es, _ := e0.(string)
			if i := strings.Index(es, "="); i > 0 && !strings.HasPrefix(es, "PATH=") {
				envs = append(envs, envKV{Key: es[:i], Value: es[i+1:]})
			}
		}
	}
	cfg["envs"] = envs
	cfg["cmd"] = deep(c, "Config", "Cmd")

	WriteJSON(w, map[string]any{"ok": true, "config": cfg})
}

// ---- 小工具 ----

func str(v any) string {
	if v == nil {
		return ""
	}
	s, _ := v.(string)
	return s
}

func deep(obj map[string]any, path ...string) any {
	var cur any = obj
	for _, k := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[k]
	}
	return cur
}

func firstNetMode(c map[string]any) string {
	if nm, ok := deep(c, "HostConfig", "NetworkMode").(string); ok {
		return nm
	}
	return ""
}
