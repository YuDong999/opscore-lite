package handlers

// ── 容器管理插件 · Docker 扩展能力(对标 Docker Desktop) ──
// Volumes / Network 管理、镜像 tag/push、容器 stats 实时监控、镜像历史层查看。
// 全部经 RunOnTarget 分发, 与既有安全模型一致(pluginGuard+白名单+dockerAudit+读回验证)。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	reVolName     = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)
	reNetName     = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)
	reRepoTag     = regexp.MustCompile(`^[a-zA-Z0-9._:/\[\]-]{1,200}$`)
	reMemorySize  = regexp.MustCompile(`^[0-9]+[bBkKmMgGtT]?$`)
)

// ===================== Volumes 卷管理 =====================

// DockerVolumesHandler GET ?host= 卷列表 (docker volume ls → 每卷 inspect 挂载点)
func DockerVolumesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(containersPluginID, w) {
		return
	}
	ServeCachedJSON(w, r, 5*time.Second, func() any {
		hostID := HostIDFromRequest(r)
		out, err := RunOnTarget(hostID, []string{dockerOrPodman(hostID), "volume", "ls", "--format", `{{json .}}`})
		if err != nil {
			return map[string]any{"ok": false, "volumes": []any{}, "error": lastLines(out, 6)}
		}
		vols := []map[string]any{}
		for _, l := range nonEmptyLines(out) {
			var m map[string]any
			if json.Unmarshal([]byte(l), &m) != nil {
				continue
			}
			// 并行 inspect 每卷挂载点/驱动(只对 named volume)
			vs, _ := m["Name"].(string)
			if vs != "" && reVolName.MatchString(vs) {
				if io, ierr := RunOnTarget(hostID, []string{dockerOrPodman(hostID), "volume", "inspect", vs}); ierr == nil {
					var arr []map[string]any
					if json.Unmarshal([]byte(io), &arr) == nil && len(arr) > 0 {
						for k, v := range arr[0] {
							if k == "Driver" || k == "Mountpoint" || k == "Scope" || k == "CreatedAt" || k == "Labels" {
								m[k] = v
							}
						}
					}
				}
			}
			vols = append(vols, m)
		}
		return map[string]any{"ok": true, "volumes": vols}
	})
}

type volumeActionBody struct {
	Host   string `json:"host"`
	Name   string `json:"name"`
	Action string `json:"action"` // create | remove
}

// DockerVolumesActionHandler POST {host,name,action} 创建/删除卷
func DockerVolumesActionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(containersPluginID, w) {
		return
	}
	var b volumeActionBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || !reVolName.MatchString(b.Name) {
		WriteJSON(w, map[string]any{"ok": false, "error": "无效的卷名(字母/数字/._-,≤128字符, 不以-开头)"})
		return
	}
	argv := []string{dockerOrPodman(b.Host)}
	switch b.Action {
	case "create":
		argv = append(argv, "volume", "create", b.Name)
	case "remove":
		argv = append(argv, "volume", "rm", "-f", b.Name)
	default:
		WriteJSON(w, map[string]any{"ok": false, "error": "action 必须是 create/remove"})
		return
	}
	out, err := RunOnTarget(b.Host, argv)
	if err != nil {
		dockerAudit(b.Host, "volume-"+b.Action, b.Name, err)
		WriteJSON(w, map[string]any{"ok": false, "error": lastLines(out, 6)})
		return
	}
	dockerAudit(b.Host, "volume-"+b.Action, b.Name, nil)
	InvalidateRespCache("/api/plugins/containers/docker/volumes")
	WriteJSON(w, map[string]any{"ok": true, "action": b.Action, "name": b.Name})
}

// ===================== Networks 网络管理 =====================

// DockerNetworksHandler GET ?host= 网络列表(len 精确到 .3, 关联容器数)
func DockerNetworksHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(containersPluginID, w) {
		return
	}
	ServeCachedJSON(w, r, 5*time.Second, func() any {
		hostID := HostIDFromRequest(r)
		out, err := RunOnTarget(hostID, []string{dockerOrPodman(hostID), "network", "ls", "--format", `{{json .}}`})
		if err != nil {
			return map[string]any{"ok": false, "networks": []any{}, "error": lastLines(out, 6)}
		}
		nets := []map[string]any{}
		for _, l := range nonEmptyLines(out) {
			var m map[string]any
			if json.Unmarshal([]byte(l), &m) != nil {
				continue
			}
			// 兼容 docker(大写) / podman(小写) 两种 ls --format json 字段
			ni := str(m["ID"])
			if ni == "" {
				ni = str(m["id"])
			}
			nm := str(m["Name"])
			if nm == "" {
				nm = str(m["name"])
			}
			if len(ni) >= 12 {
				ni = ni[:12]
			}
			subnet, gw := "", ""
			if fl, ok := m["subnets"].([]any); ok && len(fl) > 0 {
				if fm, ok := fl[0].(map[string]any); ok {
					subnet, gw = str(fm["subnet"]), str(fm["gateway"])
				}
			} else if s, ok := m["IPAM"].(map[string]any); ok {
				if ss, ok := s["Config"].([]any); ok && len(ss) > 0 {
					if sm, ok := ss[0].(map[string]any); ok {
						subnet, gw = str(sm["Subnet"]), str(sm["Gateway"])
					}
				}
			}
			nets = append(nets, map[string]any{
				"Name":    nm,
				"Driver":  firstNonEmpty(str(m["Driver"]), str(m["driver"])),
				"ID":      ni,
				"Subnet":  subnet,
				"Gateway": gw,
			})
		}
		return map[string]any{"ok": true, "networks": nets}
	})
}

type networkActionBody struct {
	Host   string `json:"host"`
	Name   string `json:"name"`
	Driver string `json:"driver"`
	Action string `json:"action"` // create | remove
}

// DockerNetworksActionHandler POST {host,name,driver,action}
func DockerNetworksActionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(containersPluginID, w) {
		return
	}
	var b networkActionBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || !reNetName.MatchString(b.Name) {
		WriteJSON(w, map[string]any{"ok": false, "error": "无效的网络名(字母/数字/._-,≤128字符)"})
		return
	}
	argv := []string{dockerOrPodman(b.Host)}
	switch b.Action {
	case "create":
		argv = append(argv, "network", "create")
		if b.Driver != "" {
			argv = append(argv, "--driver", b.Driver)
		}
		argv = append(argv, b.Name)
	case "remove":
		argv = append(argv, "network", "rm", b.Name)
	default:
		WriteJSON(w, map[string]any{"ok": false, "error": "action 必须是 create/remove"})
		return
	}
	out, err := RunOnTarget(b.Host, argv)
	if err != nil {
		dockerAudit(b.Host, "network-"+b.Action, b.Name, err)
		WriteJSON(w, map[string]any{"ok": false, "error": lastLines(out, 6)})
		return
	}
	dockerAudit(b.Host, "network-"+b.Action, b.Name, nil)
	InvalidateRespCache("/api/plugins/containers/docker/networks")
	WriteJSON(w, map[string]any{"ok": true, "action": b.Action, "name": b.Name})
}

// ===================== 镜像 tag / push =====================

type imageTagBody struct {
	Host  string `json:"host"`
	Image string `json:"image"`
	Tag   string `json:"tag"`
}

// DockerImageTagHandler POST {host,image,tag} 打标签
func DockerImageTagHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(containersPluginID, w) {
		return
	}
	var b imageTagBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		WriteJSON(w, map[string]any{"ok": false, "error": "invalid body"})
		return
	}
	if !reRepoTag.MatchString(b.Image) || strings.Contains(b.Image, "..") ||
		!reRepoTag.MatchString(b.Tag) || strings.Contains(b.Tag, "..") {
		WriteJSON(w, map[string]any{"ok": false, "error": "非法镜像/tag 名"})
		return
	}
	out, err := RunOnTarget(b.Host, []string{dockerOrPodman(b.Host), "tag", b.Image, b.Tag})
	if err != nil {
		dockerAudit(b.Host, "image-tag", b.Image+" -> "+b.Tag, err)
		WriteJSON(w, map[string]any{"ok": false, "error": lastLines(out, 6)})
		return
	}
	dockerAudit(b.Host, "image-tag", b.Image+" -> "+b.Tag, nil)
	InvalidateRespCache("/api/plugins/containers/images")
	WriteJSON(w, map[string]any{"ok": true, "image": b.Image, "tag": b.Tag})
}

type imagePushBody struct {
	Host  string `json:"host"`
	Image string `json:"image"`
}

// DockerImagePushHandler POST {host,image} 推送到远端仓库(docker push)
func DockerImagePushHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(containersPluginID, w) {
		return
	}
	var b imagePushBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		WriteJSON(w, map[string]any{"ok": false, "error": "invalid body"})
		return
	}
	if !reRepoTag.MatchString(b.Image) || strings.Contains(b.Image, "..") {
		WriteJSON(w, map[string]any{"ok": false, "error": "非法镜像名(需要含仓库前缀, 如 registry.example.com/app:v1)"})
		return
	}
	out, err := RunOnTarget(b.Host, []string{dockerOrPodman(b.Host), "push", b.Image})
	if err != nil {
		dockerAudit(b.Host, "image-push", b.Image, err)
		WriteJSON(w, map[string]any{"ok": false, "error": lastLines(out, 10), "output": lastLines(out, 30)})
		return
	}
	dockerAudit(b.Host, "image-push", b.Image, nil)
	WriteJSON(w, map[string]any{"ok": true, "image": b.Image, "output": lastLines(out, 10)})
}

// ===================== 容器 stats 监控 =====================

// DockerContainerStatsHandler GET ?host=&name= 单容器实时 stats 快照(兼容 docker/podman 字段归一化)
func DockerContainerStatsHandler(w http.ResponseWriter, r *http.Request) {
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
	out, err := RunOnTarget(hostID, []string{dockerOrPodman(hostID), "stats", "--no-stream", "--format", `{{json .}}`, name})
	if err != nil {
		WriteJSON(w, map[string]any{"ok": false, "error": lastLines(out, 6)})
		return
	}
	var raw map[string]any
	if json.Unmarshal([]byte(strings.TrimSpace(out)), &raw) != nil {
		WriteJSON(w, map[string]any{"ok": true, "stats": nil, "raw": strings.TrimSpace(out)})
		return
	}

	st := map[string]any{}

	// CPU: docker "0.05%"(字符串带%) / podman CPU|AvgCPU(float 0~1)
	cpu := "—"
	if v, ok := raw["CPU%"].(string); ok {
		cpu = v
	} else {
		if f, ok := numFloat(raw["CPU"]); ok {
			cpu = fmt.Sprintf("%.2f%%", f*100)
		} else if f, ok := numFloat(raw["AvgCPU"]); ok && f > 0 {
			cpu = fmt.Sprintf("%.2f%%", f*100)
		}
	}
	st["cpuPct"] = cpu

	// 内存: docker "5.1MiB / 7.7GiB" / podman 字节数字
	if v, ok := raw["MemUsage"].(string); ok {
		parts := strings.SplitN(v, " / ", 2)
		st["memUsage"] = parts[0]
		if len(parts) > 1 {
			st["memLimit"] = parts[1]
		} else {
			st["memLimit"] = str(raw["MemLimit"])
		}
	} else {
		if u, ok := numFloat(raw["MemUsage"]); ok {
			st["memUsage"] = fmtHumanSize(u)
		}
		if l, ok := numFloat(raw["MemLimit"]); ok && l > 0 {
			st["memLimit"] = fmtHumanSize(l)
		}
	}
	if p, ok := numFloat(raw["MemPerc"]); ok && p > 0 {
		if p < 2 {
			st["memPerc"] = fmt.Sprintf("%.2f%%", p)
		} else {
			st["memPerc"] = fmt.Sprintf("%.2f%%", p/100)
		}
	} else if v, ok := raw["MemPerc"].(string); ok {
		st["memPerc"] = v
	}
	if st["memUsage"] == nil {
		st["memUsage"] = "—"
	}
	if st["memLimit"] == nil {
		st["memLimit"] = "—"
	}
	if st["memPerc"] == nil {
		st["memPerc"] = "—"
	}

	// 网络: docker "2.35kB / 1.2kB" / podman Network 结构或 BlockInput/Output 兜底
	netIO := str(raw["NetIO"])
	blkIO := str(raw["BlockIO"])
	if netIO == "" {
		netIO = "— / —"
	}
	st["netIO"] = netIO
	if blkIO == "" {
		bi, bo := float64(0), float64(0)
		if b, ok := numFloat(raw["BlockInput"]); ok {
			bi = b
		}
		if o, ok := numFloat(raw["BlockOutput"]); ok {
			bo = o
		}
		if bi > 0 || bo > 0 {
			blkIO = fmtHumanSize(bi) + " / " + fmtHumanSize(bo)
		} else if raw["BlockInput"] != nil || raw["BlockOutput"] != nil {
			blkIO = "0B / 0B"
		}
	}
	st["blockIO"] = blkIO

	st["pids"] = raw["PIDs"]
	if st["pids"] == nil {
		st["pids"] = raw["Pids"]
	}
	id := str(raw["ID"])
	if id == "" {
		id = str(raw["ContainerID"])
	}
	if len(id) >= 12 {
		id = id[:12]
	}
	st["id"] = id

	WriteJSON(w, map[string]any{"ok": true, "stats": st})
}

// ===================== 镜像历史层 =====================

// DockerImageHistoryHandler GET ?host=&image= 镜像层历史 (docker image history --no-trunc format json)
func DockerImageHistoryHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(containersPluginID, w) {
		return
	}
	q := r.URL.Query()
	hostID, image := q.Get("host"), q.Get("image")
	if !reRepoTag.MatchString(image) || strings.Contains(image, "..") {
		WriteJSON(w, map[string]any{"ok": false, "error": "非法镜像名"})
		return
	}
	out, err := RunOnTarget(hostID, []string{dockerOrPodman(hostID), "image", "history", "--no-trunc", "--format", `{{json .}}`, image})
	if err != nil {
		WriteJSON(w, map[string]any{"ok": false, "error": lastLines(out, 6)})
		return
	}
	hist := []map[string]any{}
	for _, l := range nonEmptyLines(out) {
		var m map[string]any
		if json.Unmarshal([]byte(l), &m) == nil {
			// 归一化大小: docker 字符串数字 / podman 直接 float64 字节
			var sizeB float64
			if n, ok := numFloat(m["Size"]); ok {
				sizeB = n
			} else if n, ok := numFloat(m["size"]); ok {
				sizeB = n
			}
			m["SizeBytes"] = sizeB
			// 时间: docker CreatedAt / podman created
			if m["CreatedAt"] == nil && m["created"] != nil {
				m["CreatedAt"] = m["created"]
			}
			// ID: docker ID / podman id
			if str(m["ID"]) == "" && str(m["id"]) != "" {
				m["ID"] = m["id"]
			}
			// 创建指令: 纯 nop 层标记为配置指令, 保留
			by := strings.TrimSpace(str(m["CreatedBy"]))
			if strings.HasPrefix(by, "/bin/sh -c #(nop) ") {
				m["CreatedBy"] = strings.TrimPrefix(by, "/bin/sh -c #(nop) ")
			} else {
				m["CreatedBy"] = strings.Join(nonEmptyLines(by), " ")
			}
			hist = append(hist, m)
		}
	}
	WriteJSON(w, map[string]any{"ok": true, "image": image, "history": hist})
}

// ===================== 辅助 =====================

// 运行时探测缓存: 避免每次请求都去远端跑 command -v(60s 过期)
var (
	rtCacheMu   sync.Mutex
	rtCache     = map[string]rtCacheEntry{}
	rtCacheTime = 60 * time.Second
)

type rtCacheEntry struct {
	rt   string
	last time.Time
}

// dockerOrPodman 返回目标主机的真实容器命令(docker/podman 兼容层),
// 若目标机只有 podman 则用 podman, 只有 kubelet 托管(containerd)则报错由调用方处理。
// 采用 command -v 探测并缓存 60s, 避免假设 "一定有 docker"。
func dockerOrPodman(hostID string) string {
	rtCacheMu.Lock()
	if e, ok := rtCache[hostID]; ok && time.Since(e.last) < rtCacheTime {
		rtCacheMu.Unlock()
		return e.rt
	}
	rtCacheMu.Unlock()

	rt := detectContainerCmd(hostID, "docker", "podman")
	rtCacheMu.Lock()
	rtCache[hostID] = rtCacheEntry{rt: rt, last: time.Now()}
	rtCacheMu.Unlock()
	return rt
}

// detectContainerCmd 按候选顺序返回目标主机上第一个存在的容器命令; 空表示全无
func detectContainerCmd(hostID string, cands ...string) string {
	if len(cands) == 0 {
		return ""
	}
	probe := ""
	for i, c := range cands {
		if i > 0 {
			probe += " || "
		}
		probe += "command -v " + c
	}
	out, err := RunOnTarget(hostID, []string{"sh", "-c", probe})
	if err != nil {
		return ""
	}
	full := strings.TrimSpace(out)
	for _, c := range cands {
		if full == c || full == "/usr/bin/"+c || full == "/bin/"+c || strings.HasSuffix(full, "/"+c) {
			return c
		}
	}
	return ""
}

// runtimeReadOnly 返回非空表示该运行时只读(K8s 托管), 写操作应拒绝
func runtimeReadOnly(rt string) string {
	if rt == "crictl" || rt == "ctr" {
		return "K8s 托管运行时(" + rt + ")仅支持只读, 写操作已禁用"
	}
	return ""
}

// parseMemArg 把 "512m" → "--memory 512m --memory-swap 512m"(swap=同值简单策略), 非法返回空
func parseMemArg(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" || !reMemorySize.MatchString(s) {
		return ""
	}
	return s
}

func fmtHumanSize(b float64) string {
	const unit = 1024.0
	if b < unit {
		return fmt.Sprintf("%.0f B", b)
	}
	div, exp := unit, 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", b/div, "KMGTPE"[exp])
}

// numFloat 从 any 取 float64(json 数字或数字字符串)
func numFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case string:
		f, e := strconv.ParseFloat(strings.TrimSpace(t), 64)
		if e != nil {
			return 0, false
		}
		return f, true
	default:
		return 0, false
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}