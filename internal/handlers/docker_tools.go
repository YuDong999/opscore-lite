package handlers

// ── 容器管理插件 · Docker 扩展能力 ──
// 镜像操作(pull/rmi) / 镜像源(daemon.json registry-mirrors) / Dockerfile 构建 /
// Compose 项目(up/down/ps) / Swarm 状态(只读)。
// 全部经 RunOnTarget 分发到所选主机, 与容器插件既有安全模型一致(白名单+审计)。

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

var (
	reDockerImage  = regexp.MustCompile(`^[a-zA-Z0-9._:/\[\]-]{1,200}$`)
	reComposeProj  = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
	dockerJSONPath = "/etc/docker/daemon.json"
	composeBaseDir = "/tmp/.opscore-compose"
	buildTmpPrefix = "/tmp/.opscore-build"
)

func dockerAudit(hostID, action, detail string, err error) {
	status := "ok"
	errMsg := ""
	if err != nil {
		status = "fail"
		errMsg = err.Error()
	}
	log.Printf("[DOCKER-AUDIT] target=%s action=%s detail=%q status=%s err=%q",
		displayTarget(hostID), action, detail, status, errMsg)
}

// ===== 镜像操作 =====

type dockerImageActionBody struct {
	Host   string `json:"host"`
	Image  string `json:"image"`
	Action string `json:"action"` // pull | remove
}

// DockerImageActionHandler POST {host,image,action}
func DockerImageActionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(containersPluginID, w) {
		return
	}
	var b dockerImageActionBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		WriteJSON(w, map[string]any{"ok": false, "error": "invalid body"})
		return
	}
	if !reDockerImage.MatchString(b.Image) || strings.Contains(b.Image, "..") {
		WriteJSON(w, map[string]any{"ok": false, "error": "非法镜像名"})
		return
	}
	var argv []string
	switch b.Action {
	case "pull":
		argv = []string{"docker", "pull", b.Image}
	case "remove":
		argv = []string{"docker", "rmi", "-f", b.Image}
	default:
		WriteJSON(w, map[string]any{"ok": false, "error": "action 必须是 pull/remove"})
		return
	}
	out, err := RunOnTarget(b.Host, argv)
	if strings.Contains(out, "no such command") || strings.Contains(out, "unknown command") {
		// podman 主机兜底尝试同构命令
		argv[0] = "podman"
		out, err = RunOnTarget(b.Host, argv)
	}
	if err != nil {
		msg := lastLines(out, 8)
		if msg == "" {
			msg = err.Error()
		}
		dockerAudit(b.Host, b.Action, b.Image, err)
		WriteJSON(w, map[string]any{"ok": false, "error": msg})
		return
	}
	dockerAudit(b.Host, b.Action, b.Image, nil)
	InvalidateRespCache("/api/plugins/containers/images")
	InvalidateRespCache("/api/plugins/containers/list")
	WriteJSON(w, map[string]any{"ok": true, "action": b.Action, "image": b.Image, "output": lastLines(out, 6)})
}

// ===== 镜像源 (daemon.json registry-mirrors) =====

type daemonJSON struct {
	RegistryMirrors    []string       `json:"registry-mirrors,omitempty"`
	InsecureRegistries []string       `json:"insecure-registries,omitempty"`
	Extra              map[string]any `json:"-"`
}

type registryMirrorsBody struct {
	Host               string   `json:"host"`
	RegistryMirrors    []string `json:"registry-mirrors"`
	InsecureRegistries []string `json:"insecure-registries"`
	Restart            bool     `json:"restart"`
}

var reRegistryURL = regexp.MustCompile(`^https?://[a-zA-Z0-9._:\[\]/-]{1,200}$|^[a-zA-Z0-9._:-]{1,200}$`)

// DockerRegistriesHandler GET 查看 / POST 更新镜像加速配置
func DockerRegistriesHandler(w http.ResponseWriter, r *http.Request) {
	if !pluginGuard(containersPluginID, w) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		hostID := HostIDFromRequest(r)
		out, _ := RunOnTarget(hostID, []string{"sh", "-c",
			"cat " + dockerJSONPath + " 2>/dev/null; echo __OPSCORE_INFO__; docker info 2>/dev/null | grep -A6 'Registry Mirrors' || true"})
		raw, info := "", ""
		if i := strings.Index(out, "__OPSCORE_INFO__"); i >= 0 {
			raw, info = out[:i], out[i+len("__OPSCORE_INFO__"):]
		} else {
			raw = out
		}
		var dj map[string]any
		_ = json.Unmarshal([]byte(strings.TrimSpace(raw)), &dj)
		mirrors, _ := dj["registry-mirrors"].([]any)
		insecure, _ := dj["insecure-registries"].([]any)
		WriteJSON(w, map[string]any{
			"ok":                  true,
			"registry-mirrors":    toStrSlice(mirrors),
			"insecure-registries": toStrSlice(insecure),
			"raw":                 strings.TrimSpace(raw),
			"info":                strings.TrimSpace(info),
			"note":                "修改后需重启 docker 生效(会中断该主机上的容器)",
		})
	case http.MethodPost:
		var b registryMirrorsBody
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			WriteJSON(w, map[string]any{"ok": false, "error": "invalid body"})
			return
		}
		for _, m := range append(append([]string{}, b.RegistryMirrors...), b.InsecureRegistries...) {
			if !reRegistryURL.MatchString(m) {
				WriteJSON(w, map[string]any{"ok": false, "error": "非法地址: " + m})
				return
			}
		}
		// 读旧配置保留其余字段
		oldRaw, _ := RunOnTarget(b.Host, []string{"sh", "-c", "cat " + dockerJSONPath + " 2>/dev/null || echo '{}'"})
		var cfg map[string]any
		_ = json.Unmarshal([]byte(strings.TrimSpace(oldRaw)), &cfg)
		if cfg == nil {
			cfg = map[string]any{}
		}
		setOrDel(cfg, "registry-mirrors", b.RegistryMirrors)
		setOrDel(cfg, "insecure-registries", b.InsecureRegistries)
		newRaw, _ := json.MarshalIndent(cfg, "", "  ")

		script := fmt.Sprintf(
			`cp %s %s.bak.$(date +%%s) 2>/dev/null || true
mkdir -p $(dirname %s)
echo %s | base64 -d > %s.tmp && mv %s.tmp %s`,
			dockerJSONPath, dockerJSONPath, dockerJSONPath,
			Shq(base64.StdEncoding.EncodeToString(newRaw)),
			dockerJSONPath, dockerJSONPath, dockerJSONPath)
		out, err := runScriptOnTarget(b.Host, script)
		if err != nil {
			dockerAudit(b.Host, "registry-mirrors-update", "", err)
			WriteJSON(w, map[string]any{"ok": false, "error": lastLines(out, 5) + err.Error()})
			return
		}
		restarted := false
		if b.Restart {
			rout, rerr := RunOnTarget(b.Host, []string{"sh", "-c",
				"systemctl restart docker 2>&1 || service docker restart 2>&1"})
			restarted = rerr == nil
			if rerr != nil {
				WriteJSON(w, map[string]any{"ok": true, "written": true, "restarted": false,
					"note": "配置已写入但重启失败: " + lastLines(rout, 3)})
				return
			}
		}
		dockerAudit(b.Host, "registry-mirrors-update", string(newRaw), nil)
		InvalidateRespCache("/api/plugins/containers/images")
		WriteJSON(w, map[string]any{"ok": true, "written": true, "restarted": restarted,
			"note": ternary(restarted, "已写入并重启 docker", "已写入, 重启 docker 后生效")})
	default:
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// ===== Dockerfile 构建 =====

type dockerBuildBody struct {
	Host       string `json:"host"`
	Tag        string `json:"tag"`        // 完整镜像 tag, 如 myapp:v1
	Dockerfile string `json:"dockerfile"` // 内容
}

var reDockerTag = regexp.MustCompile(`^[a-zA-Z0-9._:/\[\]-]{1,200}$`)

// DockerBuildHandler POST {host,tag,dockerfile}
func DockerBuildHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(containersPluginID, w) {
		return
	}
	var b dockerBuildBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil ||
		strings.TrimSpace(b.Dockerfile) == "" {
		WriteJSON(w, map[string]any{"ok": false, "error": "invalid body(tag/dockerfile)"})
		return
	}
	if !reDockerTag.MatchString(b.Tag) {
		WriteJSON(w, map[string]any{"ok": false, "error": "非法镜像 tag"})
		return
	}
	dir := fmt.Sprintf("%s-%d", buildTmpPrefix, time.Now().UnixNano()%1e9)
	script := fmt.Sprintf(
		`mkdir -p %[1]s
echo %[2]s | base64 -d > %[1]s/Dockerfile
cd %[1]s && timeout 600 docker build -t %[3]s . 2>&1
rc=$?
rm -rf %[1]s
exit $rc`,
		dir, Shq(base64.StdEncoding.EncodeToString([]byte(b.Dockerfile))), Shq(b.Tag))
	out, err := runScriptOnTarget(b.Host, script)
	if err != nil {
		dockerAudit(b.Host, "build", b.Tag, err)
		WriteJSON(w, map[string]any{"ok": false, "error": lastLines(out, 15), "output": lastLines(out, 40)})
		return
	}
	dockerAudit(b.Host, "build", b.Tag, nil)
	InvalidateRespCache("/api/plugins/containers/images")
	WriteJSON(w, map[string]any{"ok": true, "tag": b.Tag, "output": lastLines(out, 20)})
}

// ===== Compose 项目 =====

type composeBody struct {
	Host    string `json:"host"`
	Project string `json:"project"`
	Action  string `json:"action"`  // up | down | restart | ps
	Compose string `json:"compose"` // up 时必填(首次)
}

// DockerComposeHandler POST {host,project,action,compose}
// 文件持久化在目标主机 /tmp/.opscore-compose/<project>/docker-compose.yml,
// 后续 down/ps/restart 无需重复粘贴。
func DockerComposeHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(containersPluginID, w) {
		return
	}
	var b composeBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || !reComposeProj.MatchString(b.Project) {
		WriteJSON(w, map[string]any{"ok": false, "error": "invalid body(project 规则: 小写字母/数字/_-,≤63字符)"})
		return
	}
	dir := composeBaseDir + "/" + b.Project
	file := dir + "/docker-compose.yml"

	var script string
	switch b.Action {
	case "up":
		if strings.TrimSpace(b.Compose) == "" {
			WriteJSON(w, map[string]any{"ok": false, "error": "up 需要 compose YAML 内容"})
			return
		}
		script = fmt.Sprintf(
			`mkdir -p %[1]s
echo %[2]s | base64 -d > %[1]s/docker-compose.yml
timeout 600 docker compose -f %[3]s -p %[4]s up -d 2>&1`,
			dir, Shq(base64.StdEncoding.EncodeToString([]byte(b.Compose))), file, Shq(b.Project))
	case "down":
		script = fmt.Sprintf(`timeout 300 docker compose -f %[1]s -p %[2]s down 2>&1`, file, Shq(b.Project))
	case "restart":
		script = fmt.Sprintf(`timeout 600 docker compose -f %[1]s -p %[2]s restart 2>&1`, file, Shq(b.Project))
	case "ps":
		script = fmt.Sprintf(`docker compose -f %[1]s -p %[2]s ps --format json 2>/dev/null || docker ps -a --filter label=com.docker.compose.project=%[2]s --format '{{json .}}' 2>&1`, file, Shq(b.Project))
	default:
		WriteJSON(w, map[string]any{"ok": false, "error": "action 必须是 up/down/restart/ps"})
		return
	}
	out, err := runScriptOnTarget(b.Host, script)
	if err != nil && b.Action != "ps" { // ps 无项目时允许空结果
		dockerAudit(b.Host, "compose-"+b.Action, b.Project, err)
		WriteJSON(w, map[string]any{"ok": false, "error": lastLines(out, 10), "output": lastLines(out, 30)})
		return
	}
	dockerAudit(b.Host, "compose-"+b.Action, b.Project, nil)
	InvalidateRespCache("/api/plugins/containers/list")
	WriteJSON(w, map[string]any{"ok": true, "project": b.Project, "action": b.Action, "output": lastLines(out, 25)})
}

// ===== Swarm 状态 (只读) =====

// DockerSwarmHandler GET ?host=&view=status|nodes|services|stacks
func DockerSwarmHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(containersPluginID, w) {
		return
	}
	ServeCachedJSON(w, r, 5*time.Second, func() any { return swarmBuild(r) })
}

func swarmBuild(r *http.Request) any {
	hostID := HostIDFromRequest(r)
	view := r.URL.Query().Get("view")
	var cmds map[string]string
	switch view {
	case "status":
		cmds = map[string]string{
			"swarm": `docker info --format '{{json .Swarm}}' 2>/dev/null || echo '{"Error":"docker 不可用"}'`,
		}
	case "nodes":
		cmds = map[string]string{"nodes": `docker node ls --format '{{json .}}' 2>&1`}
	case "services":
		cmds = map[string]string{"services": `docker service ls --format '{{json .}}' 2>&1`, "stacks": `docker stack ls --format '{{json .}}' 2>&1`}
	default:
		cmds = map[string]string{
			"swarm":    `docker info --format '{{json .Swarm}}' 2>/dev/null`,
			"nodes":    `docker node ls --format '{{json .}}' 2>/dev/null`,
			"services": `docker service ls --format '{{json .}}' 2>/dev/null`,
			"stacks":   `docker stack ls --format '{{json .}}' 2>/dev/null`,
		}
	}
	res := execAppsCmds(hostID, cmds)
	out := map[string]any{"ok": true, "view": view}
	for k, v := range res {
		if v.Error != "" {
			out[k] = v.Error
			continue
		}
		lines := nonEmptyLines(v.Output)
		if k == "swarm" {
			var sw any
			if json.Unmarshal([]byte(strings.TrimSpace(v.Output)), &sw) == nil {
				out[k] = sw
				continue
			}
		}
		arr := make([]map[string]any, 0, len(lines))
		for _, l := range lines {
			var m map[string]any
			if json.Unmarshal([]byte(l), &m) == nil {
				arr = append(arr, m)
			}
		}
		if len(arr) > 0 {
			out[k] = arr
		} else {
			out[k] = lines // 原始行(如错误提示 "not a swarm manager")
		}
	}
	return out
}

// ===== 内部辅助 =====

func lastLines(s string, n int) string {
	s = strings.TrimRight(s, "\n\r ")
	if s == "" {
		return ""
	}
	parts := strings.Split(s, "\n")
	if len(parts) <= n {
		return s
	}
	return strings.Join(parts[len(parts)-n:], "\n")
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(l)
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func toStrSlice(arr []any) []string {
	out := make([]string, 0, len(arr))
	for _, v := range arr {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func setOrDel(cfg map[string]any, key string, vals []string) {
	if len(vals) == 0 {
		delete(cfg, key)
		return
	}
	cfg[key] = vals
}

func ternary(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}

// ===== 镜像拉取异步任务(进度轮询) =====

type pullJob struct {
	ID        string
	Host      string
	Image     string
	Output    []string // 保留最近输出行
	Done      bool
	Err       string
	StartedAt time.Time
}

var (
	pullJobsMu sync.Mutex
	pullJobs   = map[string]*pullJob{}
)

type pullAsyncBody struct {
	Host  string `json:"host"`
	Image string `json:"image"`
}

// DockerPullAsyncHandler POST 发起异步拉取, 返回 jobId
func DockerPullAsyncHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(containersPluginID, w) {
		return
	}
	var b pullAsyncBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || !reDockerImage.MatchString(b.Image) || strings.Contains(b.Image, "..") {
		WriteJSON(w, map[string]any{"ok": false, "error": "invalid body(image)"})
		return
	}
	jobID := fmt.Sprintf("p%d", time.Now().UnixNano())
	job := &pullJob{ID: jobID, Host: b.Host, Image: b.Image, StartedAt: time.Now()}
	pullJobsMu.Lock()
	// 清理 30 分钟前的旧任务
	for k, j := range pullJobs {
		if time.Since(j.StartedAt) > 30*time.Minute {
			delete(pullJobs, k)
		}
	}
	pullJobs[jobID] = job
	pullJobsMu.Unlock()

	go func() {
		out, err := RunOnTarget(job.Host, []string{"docker", "pull", b.Image})
		if err != nil && (strings.Contains(out, "no such command") || strings.Contains(out, "is not a docker command")) {
			out, err = RunOnTarget(job.Host, []string{"podman", "pull", b.Image})
		}
		pullJobsMu.Lock()
		for _, l := range strings.Split(out, "\n") {
			l = strings.TrimSpace(l)
			if l == "" || strings.HasPrefix(l, "Digest:") {
				continue
			}
			job.Output = append(job.Output, l)
			if len(job.Output) > 40 {
				job.Output = job.Output[len(job.Output)-40:]
			}
		}
		job.Done = true
		if err != nil {
			job.Err = lastLines(out, 3)
			if job.Err == "" {
				job.Err = err.Error()
			}
		}
		pullJobsMu.Unlock()
		dockerAudit(job.Host, "pull-async", b.Image, err)
		if err == nil {
			InvalidateRespCache("/api/plugins/containers/images")
		}
	}()
	WriteJSON(w, map[string]any{"ok": true, "jobId": jobID})
}

// DockerPullProgressHandler GET ?id= → 轮询任务进度
func DockerPullProgressHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(containersPluginID, w) {
		return
	}
	id := r.URL.Query().Get("id")
	pullJobsMu.Lock()
	job, ok := pullJobs[id]
	if !ok {
		pullJobsMu.Unlock()
		WriteJSON(w, map[string]any{"ok": false, "error": "任务不存在"})
		return
	}
	layersDone, layersTotal := pullLayerStats(job.Output)
	resp := map[string]any{
		"ok":         true,
		"done":       job.Done,
		"err":        job.Err,
		"image":      job.Image,
		"lines":      append([]string{}, job.Output...), // 拷贝
		"secs":       int(time.Since(job.StartedAt).Seconds()),
		"layersDone": layersDone, "layersTotal": layersTotal,
	}
	pullJobsMu.Unlock()
	WriteJSON(w, resp)
}

// pullLayerStats 从输出行统计拉取进度: 已完成层数/总可见层数
func pullLayerStats(lines []string) (done, total int) {
	status := map[string]string{} // id -> last status
	for _, l := range lines {
		f := strings.Fields(l)
		if len(f) < 2 {
			continue
		}
		id := f[0]
		if len(id) != 12 { // docker layer id 是 12 位
			continue
		}
		rest := strings.Join(f[1:], " ")
		status[id] = rest
	}
	total = len(status)
	for _, st := range status {
		if strings.Contains(st, "Pull complete") || strings.Contains(st, "Already exists") {
			done++
		} else if strings.HasPrefix(st, "Downloaded") || strings.Contains(st, "Download complete") {
			done++
		}
	}
	return done, total
}
