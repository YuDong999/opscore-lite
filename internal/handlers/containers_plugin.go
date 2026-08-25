package handlers

// ── 容器管理插件 (Group: plugin, ID: containers) ──
// 列表/镜像复用 apps.go 的运行时探测与采集; 写操作经 RunOnTarget 分发到所选主机,
// 白名单防注入 + 回读验证(verified); 连接走向为 conntrack+ss 无侵入实现,
// 数据结构与未来 eBPF collector(Hubble/DeepFlow 式)兼容, 可平滑升级。

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"opscore/internal/remote"
)

const containersPluginID = "containers"

var reContainerName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

// ===== 列表 =====

// ContainerListHandler GET /api/plugins/containers/list?host=<id>
func ContainerListHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(containersPluginID, w) {
		return
	}
	ServeCachedJSON(w, r, 5*time.Second, func() any { return containersListBuild(r) })
}

func containersListBuild(r *http.Request) any {
	hostID := r.URL.Query().Get("host")
	rt, res := probeContainerRuntime(hostID, r.URL.Query().Get("rt"))
	cs := collectContainers(rt, res)
	if cs == nil {
		cs = []AppContainer{} // 避免 JSON null 导致前端 .length 抛错
	}
	resp := map[string]any{"runtime": rt, "containers": cs}
	if hostID == "" && runtime.GOOS == "windows" {
		resp["note"] = "本机为 Windows 管理端, 容器操作请选择远程 Linux 主机"
	}
	if rt == "" && !(hostID == "" && runtime.GOOS == "windows") {
		resp["note"] = "未检测到 docker / podman / containerd 运行时"
	}
	if rt == "crictl" || rt == "ctr" {
		resp["note"] = "K8s 托管运行时(" + rt + ")仅支持只读, 写操作已禁用以避免与 kubelet 编排冲突"
	}
	return resp
}

// probeContainerRuntime 探测目标主机容器运行时并采集 ps 输出。
// forceRt 非空时强制使用该运行时(K8s 页面指定 crictl/ctr), 但仍要求其可执行文件存在。
func probeContainerRuntime(hostID string, forceRt ...string) (string, map[string]remote.Result) {
	res := execAppsCmds(hostID, map[string]string{
		"docker_path": "command -v docker 2>/dev/null",
		"podman_path": "command -v podman 2>/dev/null",
		"crictl_path": "command -v crictl 2>/dev/null",
		"ctr_path":    "command -v ctr 2>/dev/null",
	})
	rt := ""
	if len(forceRt) > 0 && forceRt[0] != "" {
		if v := res[forceRt[0]+"_path"]; v.Error == "" && strings.TrimSpace(v.Output) != "" {
			rt = forceRt[0]
		}
	}
	if rt == "" {
		rt = detectRuntime(res)
	}
	psCmd := ""
	switch rt {
	case "docker", "podman":
		psCmd = rt + " ps -a --no-trunc --format '{{json .}}'"
	case "crictl":
		psCmd = "crictl ps -a -o json"
	case "ctr":
		psCmd = "ctr -n k8s.io c list -q 2>/dev/null"
	}
	res2 := execAppsCmds(hostID, map[string]string{"ps": psCmd})
	res["ps"] = res2["ps"]
	return rt, res
}

// ===== 写操作 =====

type containerActionBody struct {
	Host    string `json:"host"`    // 目标主机ID(空=本机)
	Name    string `json:"name"`    // 容器名
	Runtime string `json:"runtime"` // docker / podman
	Action  string `json:"action"`  // start | stop | restart | remove | update-policy
	Policy  string `json:"policy"`  // update-policy 用: no | on-failure | always | unless-stopped
}

// ContainerActionHandler POST /api/plugins/containers/action
func ContainerActionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(containersPluginID, w) {
		return
	}
	var b containerActionBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || b.Name == "" {
		WriteJSON(w, map[string]any{"ok": false, "error": "invalid body"})
		return
	}
	switch b.Action {
	case "start", "stop", "restart", "remove", "update-policy":
	default:
		WriteJSON(w, map[string]any{"ok": false, "error": "action 必须是 start/stop/restart/remove/update-policy"})
		return
	}
	if !reContainerName.MatchString(b.Name) {
		WriteJSON(w, map[string]any{"ok": false, "error": "非法的容器名"})
		return
	}
	if b.Runtime != "docker" && b.Runtime != "podman" {
		WriteJSON(w, map[string]any{"ok": false, "error": "写操作仅支持 docker / podman 运行时(K8s 托管运行时只读)"})
		return
	}

	var argv []string
	want := "" // 回读验证期望值(空=按状态验证)
	switch b.Action {
	case "start":
		argv = []string{b.Runtime, "start", b.Name}
	case "stop":
		argv = []string{b.Runtime, "stop", "-t", "10", b.Name}
	case "restart":
		argv = []string{b.Runtime, "restart", "-t", "10", b.Name}
	case "remove":
		argv = []string{b.Runtime, "rm", "-f", b.Name} // 运行中强制删除需 -f, 前端二次确认
	case "update-policy":
		switch b.Policy {
		case "no", "on-failure", "always", "unless-stopped":
		default:
			WriteJSON(w, map[string]any{"ok": false, "error": "policy 必须是 no/on-failure/always/unless-stopped"})
			return
		}
		argv = []string{b.Runtime, "update", "--restart=" + b.Policy, b.Name}
		want = b.Policy
	}
	out, err := RunOnTarget(b.Host, argv)
	if err != nil {
		WriteJSON(w, map[string]any{
			"ok": false, "error": strings.TrimSpace(out), "target": displayTarget(b.Host),
			"action": b.Action, "name": b.Name,
		})
		return
	}
	verified := verifyContainerState(b.Host, b.Runtime, b.Name, b.Action, want)
	log.Printf("[CONTAINER-AUDIT] target=%s runtime=%s action=%s name=%q policy=%q verified=%v",
		displayTarget(b.Host), b.Runtime, b.Action, b.Name, b.Policy, verified)
	InvalidateRespCache("/api/plugins/containers/list")
	InvalidateRespCache("/api/core/apps")
	WriteJSON(w, map[string]any{
		"ok": true, "verified": verified, "target": displayTarget(b.Host),
		"action": b.Action, "name": b.Name,
	})
}

// verifyContainerState 写操作回读: 状态必须真实变化才算成功(轮询窗口最长 8s 抗过渡态)。
// wantPolicy 非空时(update-policy)回读 RestartPolicy.Name 而非运行状态。
func verifyContainerState(hostID, rt, name, action, wantPolicy string) bool {
	if wantPolicy != "" {
		out, err := RunOnTarget(hostID, []string{rt, "inspect", "-f", "{{.HostConfig.RestartPolicy.Name}}", name})
		return err == nil && strings.TrimSpace(out) == wantPolicy
	}
	checkExists := func() (string, bool) {
		out, err := RunOnTarget(hostID, []string{rt, "inspect", "-f", "{{.State.Status}}", name})
		return strings.TrimSpace(out), err == nil
	}
	wantStatus := ""
	switch action {
	case "start", "restart":
		wantStatus = "running"
	case "stop":
		wantStatus = "exited"
	case "remove":
		// 验证标准: inspect 失败(对象不存在)
		deadline := time.Now().Add(8 * time.Second)
		for {
			if _, exists := checkExists(); !exists {
				return true
			}
			if time.Now().After(deadline) {
				return false
			}
			time.Sleep(500 * time.Millisecond)
		}
	}
	deadline := time.Now().Add(8 * time.Second)
	var got string
	for {
		got, _ = checkExists()
		if got == wantStatus {
			return true
		}
		if time.Now().After(deadline) {
			log.Printf("[CONTAINER-VERIFY] target=%s %s want=%q got=%q", displayTarget(hostID), name, wantStatus, got)
			return false
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// ===== 详情 =====

// ContainerDetailHandler GET /api/plugins/containers/detail?host=&runtime=&id=
// 复用 apps.go collectContainerDetail 的 inspect 解析(挂载/环境/网络/限额/重启策略)。
func ContainerDetailHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(containersPluginID, w) {
		return
	}
	id := r.URL.Query().Get("id")
	rt := r.URL.Query().Get("runtime")
	if id == "" || (rt != "docker" && rt != "podman" && rt != "crictl") {
		WriteJSON(w, map[string]any{"ok": false, "error": "参数非法(id/runtime)"})
		return
	}
	d := collectContainerDetail(rt, id, r.URL.Query().Get("host"))
	if d == nil {
		WriteJSON(w, map[string]any{"ok": false, "error": "该运行时不支持详情"})
		return
	}
	WriteJSON(w, map[string]any{"ok": d.Health != "down" || d.HealthNote == "", "container": d})
}

// ===== 镜像列表 =====

// ContainerImagesHandler GET /api/plugins/containers/images?host=<id>
func ContainerImagesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(containersPluginID, w) {
		return
	}
	ServeCachedJSON(w, r, 5*time.Second, func() any { return containerImagesBuild(r) })
}

func containerImagesBuild(r *http.Request) any {
	hostID := r.URL.Query().Get("host")
	rt, _ := probeContainerRuntime(hostID)
	type imageRow struct {
		Repo, Tag, ID, Size string
	}
	var images []map[string]string
	switch rt {
	case "docker", "podman":
		cmd := rt + " images --format '{{json .}}'"
		res := execAppsCmds(hostID, map[string]string{"images": cmd})
		if res["images"].Error == "" {
			for _, line := range strings.Split(res["images"].Output, "\n") {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				var j struct {
					Repository string `json:"Repository"`
					Tag        string `json:"Tag"`
					ID         string `json:"ID"`
					Size       string `json:"Size"`
				}
				if json.Unmarshal([]byte(line), &j) == nil {
					images = append(images, map[string]string{
						"repo": j.Repository, "tag": j.Tag, "id": shortID(j.ID), "size": j.Size,
					})
				}
			}
		}
	case "crictl":
		res := execAppsCmds(hostID, map[string]string{"images": "crictl images 2>/dev/null"})
		for _, line := range strings.Split(res["images"].Output, "\n")[1:] { // 跳过表头
			f := strings.Fields(strings.TrimSpace(line))
			if len(f) >= 4 {
				images = append(images, map[string]string{
					"repo": f[0], "tag": f[1], "id": shortID(f[2]), "size": f[len(f)-1],
				})
			}
		}
	}
	resp := map[string]any{"runtime": rt, "images": images}
	if rt == "" {
		resp["note"] = "未检测到可用容器运行时"
	}
	return resp
}

// ===== 日志 =====

// ContainerLogsHandler GET /api/plugins/containers/logs?host=&runtime=&name=&tail=
func ContainerLogsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(containersPluginID, w) {
		return
	}
	rt := r.URL.Query().Get("runtime")
	name := r.URL.Query().Get("name")
	if rt != "docker" && rt != "podman" {
		WriteJSON(w, map[string]any{"ok": false, "error": "日志查看仅支持 docker / podman 运行时"})
		return
	}
	if !reContainerName.MatchString(name) {
		WriteJSON(w, map[string]any{"ok": false, "error": "非法的容器名"})
		return
	}
	tail := 200
	if v, err := strconv.Atoi(r.URL.Query().Get("tail")); err == nil {
		tail = v
	}
	if tail < 1 {
		tail = 1
	}
	if tail > 500 {
		tail = 500 // 上限防护: 避免大日志拖垮 SSH 会话
	}
	out, err := RunOnTarget(r.URL.Query().Get("host"), []string{rt, "logs", "--tail", strconv.Itoa(tail), "--timestamps", name})
	WriteJSON(w, map[string]any{
		"ok": err == nil, "logs": out,
		"name": name, "target": displayTarget(r.URL.Query().Get("host")),
	})
}

// ===== 连接走向 (conntrack + ss 无侵入版; 未来可替换 eBPF collector) =====

const flowsScript = `echo __OPSCORE_CT__
(conntrack -L 2>/dev/null || cat /proc/net/nf_conntrack 2>/dev/null) | grep -v 'UNREPLIED' | head -400
echo __OPSCORE_MAP__
if command -v docker >/dev/null 2>&1; then
  for id in $(docker ps -q 2>/dev/null); do
    ips=$(docker inspect -f '{{range $k,$v := .NetworkSettings.Networks}}{{$v.IPAddress}} {{end}}' "$id" 2>/dev/null)
    name=$(docker inspect -f '{{.Name}}' "$id" 2>/dev/null | sed 's#^/##')
    echo "$ips$name"
  done
fi`

// ContainerFlowsHandler GET /api/plugins/containers/flows?host=<id>
// 返回节点(容器/内网/外网/本机)+边(聚合连接数), 前端 ECharts graph 渲染。
func ContainerFlowsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(containersPluginID, w) {
		return
	}
	ServeCachedJSON(w, r, 5*time.Second, func() any { return containerFlowsBuild(r) })
}

func containerFlowsBuild(r *http.Request) any {
	hostID := r.URL.Query().Get("host")
	out, err := runScriptOnTarget(hostID, flowsScript)
	if err != nil {
		return map[string]any{"nodes": []any{}, "edges": []any{}, "note": "采集失败: " + err.Error()}
	}
	ctSection, mapSection := splitSentinel(out, "__OPSCORE_CT__", "__OPSCORE_MAP__")

	ipToName := map[string]string{}
	for _, line := range strings.Split(mapSection, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 1 {
			continue
		}
		name := parts[len(parts)-1]
		for _, ip := range parts[:len(parts)-1] {
			if ip != "" && ip != "<no" && ip != "value>" {
				ipToName[ip] = name
			}
		}
	}

	type edgeKey struct{ src, dst, proto string }
	edgeCount := map[edgeKey]int{}
	nodeType := map[string]string{} // id -> container|internal|external|host
	classify := func(ip string) (string, string) {
		if n, ok := ipToName[ip]; ok {
			return n, "container"
		}
		if strings.HasPrefix(ip, "127.") {
			return "本机回环", "host"
		}
		if strings.HasPrefix(ip, "10.") || strings.HasPrefix(ip, "192.168.") || strings.HasPrefix(ip, "172.16.") ||
			strings.HasPrefix(ip, "172.17.") || strings.HasPrefix(ip, "172.18.") || strings.HasPrefix(ip, "172.19.") ||
			strings.HasPrefix(ip, "172.2") || strings.HasPrefix(ip, "172.30.") || strings.HasPrefix(ip, "172.31.") {
			return "内网:" + ip, "internal"
		}
		return "外网:" + ip, "external"
	}

	for _, line := range strings.Split(ctSection, "\n") {
		f := strings.Fields(strings.TrimSpace(line))
		if len(f) < 4 {
			continue
		}
		proto := f[0]
		var src, dst string
		for _, kv := range f {
			if strings.HasPrefix(kv, "src=") {
				src = strings.TrimPrefix(kv, "src=")
				break
			}
		}
		for _, kv := range f {
			if strings.HasPrefix(kv, "dst=") {
				dst = strings.TrimPrefix(kv, "dst=")
				break
			}
		}
		if src == "" || dst == "" || src == dst {
			continue
		}
		sName, sType := classify(src)
		dName, dType := classify(dst)
		nodeType[sName] = sType
		nodeType[dName] = dType
		edgeCount[edgeKey{sName, dName, proto}]++
	}

	nodes := []map[string]any{}
	for id, typ := range nodeType {
		nodes = append(nodes, map[string]any{"id": id, "name": id, "type": typ})
	}
	edges := []map[string]any{}
	for k, c := range edgeCount {
		edges = append(edges, map[string]any{"source": k.src, "target": k.dst, "proto": k.proto, "count": c})
	}
	note := ""
	if len(edges) == 0 {
		note = "未采集到连接记录(需要 conntrack 或 /proc/net/nf_conntrack 可读)"
	}
	return map[string]any{"nodes": nodes, "edges": edges, "note": note}
}

// runScriptOnTarget 单会话脚本执行: 本机 sh -c / 远程 ExecScript(哨兵分段)。
func runScriptOnTarget(hostID, script string) (string, error) {
	if IsLocalTarget(hostID) {
		if runtime.GOOS == "windows" {
			return "", fmt.Errorf("本机为 Windows 管理端, 请选择远程 Linux 主机")
		}
		o, err := exec.Command("sh", "-c", script).CombinedOutput()
		return string(o), err
	}
	rmHost, err := remoteHostByID(hostID)
	if err != nil {
		return "", err
	}
	res, err := remotePool.ExecScript(rmHost, script)
	if err != nil {
		return "", err
	}
	// 拼接所有 section 输出(ParseSections 按哨兵键 CT/MAP 切分, 这里取两段原文)
	var b strings.Builder
	for _, k := range []string{"CT", "MAP"} {
		if v, ok := res[k]; ok && v.Error == "" {
			b.WriteString("__OPSCORE_" + k + "__\n" + v.Output + "\n")
		}
	}
	return b.String(), nil
}

// splitSentinel 从合并输出中截取两哨兵之间的段落。
func splitSentinel(out, first, second string) (string, string) {
	i := strings.Index(out, first)
	j := strings.Index(out, second)
	if i < 0 {
		return "", ""
	}
	rest := out[i+len(first):]
	if j < 0 {
		return rest, ""
	}
	return rest[:strings.Index(rest, second)], out[j+len(second):]
}
