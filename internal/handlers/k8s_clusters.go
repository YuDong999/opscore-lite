package handlers

// ── 容器管理插件 · Kubernetes 子模块 (联邦模式) ──
// 薄适配层: 参数校验 → internal/kubernetes 核心包; 元数据经 central 持久化,
// kubeconfig 凭据落盘 <dataDir>/kubeconfigs/<id>.yaml(0600) 不进 DB。
// 独立模式(cmd/kubemod)复用同一核心包, 本文件不依赖 SSH/主机分发体系。

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"opscore/internal/central"
	"opscore/internal/kubernetes"

	"k8s.io/client-go/tools/clientcmd"
)

var (
	k8sMgr     *kubernetes.Manager
	k8sKubeDir string
	k8sKubeMu  sync.Mutex
)

const k8sPluginID = containersPluginID // K8s 子功能归属容器管理插件, 复用同一热生效守卫

// k8sStoreFn 延迟取存储实例(InitK8s 时注入, 避免初始化顺序耦合)。
var k8sStoreFn func() central.CentralStore

var (
	reK8sClusterID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	reK8sNamespace = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)
)

// InitK8s 注入核心管理器、kubeconfig 目录与存储访问器, 并恢复已注册集群。
func InitK8s(mgr *kubernetes.Manager, kubeDir string, storeFn func() central.CentralStore) {
	k8sMgr = mgr
	k8sKubeDir = kubeDir
	k8sStoreFn = storeFn
	_ = os.MkdirAll(kubeDir, 0700)
	k8sRestore()
}

func k8sKubePath(id string) string { return filepath.Join(k8sKubeDir, id+".yaml") }

func k8sJSONDecode(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}

// ===== 启动恢复 =====

// k8sRestore 按 DB 记录重载集群; 文件缺失或解析失败仅标记 unreachable, 不阻塞启动。
func k8sRestore() {
	if k8sStoreFn == nil {
		return
	}
	clusters, err := k8sStoreFn().GetK8sClusters()
	if err != nil || len(clusters) == 0 {
		return
	}
	for i := range clusters {
		c := &clusters[i]
		data, ferr := os.ReadFile(k8sKubePath(c.ID))
		if ferr != nil {
			c.Status = "unreachable"
			log.Printf("[K8S] restore %s: kubeconfig 文件缺失", c.ID)
			continue
		}
		if aerr := k8sMgr.Add(c.ID, data); aerr != nil {
			c.Status = "unreachable"
			log.Printf("[K8S] restore %s: %v", c.ID, aerr)
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		if info, perr := k8sMgr.Probe(ctx, c.ID); perr == nil {
			c.Status, c.Version, c.APIServer = "ready", info.Version, info.APIServer
		} else {
			c.Status = "unreachable"
		}
		cancel()
	}
	if serr := k8sStoreFn().SetK8sClusters(clusters); serr != nil {
		log.Printf("[K8S] 持久化恢复状态失败: %v", serr)
	}
	log.Printf("[K8S] 已恢复 %d 个集群注册", len(clusters))
}

// ===== 集群注册 / 列表 =====

type k8sRegisterBody struct {
	Name       string `json:"name"`
	Kubeconfig string `json:"kubeconfig"` // YAML 明文或 base64(自动识别)
}

// K8sClustersHandler GET 列表 / POST 注册。
func K8sClustersHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
	case http.MethodPost:
	default:
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(k8sPluginID, w) {
		return
	}
	if r.Method == http.MethodGet {
		WriteJSON(w, map[string]any{"ok": true, "clusters": k8sListClusters()})
		return
	}
	var b k8sRegisterBody
	if err := k8sJSONDecode(r, &b); err != nil || b.Name == "" || b.Kubeconfig == "" {
		WriteJSON(w, map[string]any{"ok": false, "error": "invalid body(name/kubeconfig)"})
		return
	}
	id := k8sSanitizeID(b.Name)
	if !reK8sClusterID.MatchString(id) {
		WriteJSON(w, map[string]any{"ok": false, "error": "集群名仅允许小写字母/数字/中划线(≤63字符)"})
		return
	}
	data, derr := decodeKubeconfig(b.Kubeconfig)
	if derr != nil {
		WriteJSON(w, map[string]any{"ok": false, "error": derr.Error()})
		return
	}

	k8sKubeMu.Lock()
	defer k8sKubeMu.Unlock()

	if k8sFindCluster(k8sListClusters(), id) != nil {
		WriteJSON(w, map[string]any{"ok": false, "error": "同名集群已存在"})
		return
	}
	if aerr := k8sMgr.Add(id, data); aerr != nil {
		WriteJSON(w, map[string]any{"ok": false, "error": "kubeconfig 解析失败: " + aerr.Error()})
		return
	}
	// 注册即探测(6s 超时); 探测失败不回滚注册, 状态落库供前端提示
	status := "ready"
	apiServer, version := "", ""
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	if info, perr := k8sMgr.Probe(ctx, id); perr == nil {
		apiServer, version = info.APIServer, info.Version
	} else {
		status = "unreachable"
	}
	cancel()

	if werr := os.WriteFile(k8sKubePath(id), data, 0600); werr != nil {
		k8sMgr.Remove(id)
		WriteJSON(w, map[string]any{"ok": false, "error": "保存 kubeconfig 失败: " + werr.Error()})
		return
	}
	rec := central.K8sCluster{ID: id, Name: b.Name, APIServer: apiServer, Version: version, Status: status, CreatedAt: time.Now().Unix()}
	if uerr := k8sUpsertCluster(rec); uerr != nil {
		WriteJSON(w, map[string]any{"ok": false, "error": "持久化失败: " + uerr.Error()})
		return
	}
	log.Printf("[K8S-AUDIT] action=register cluster=%s status=%s api=%s", id, status, apiServer)
	InvalidateRespCache("/api/plugins/containers/k8s")
	WriteJSON(w, map[string]any{"ok": true, "cluster": rec})
}

// K8sDefaultKubeconfigHandler GET /api/plugins/containers/k8s/kubeconfig/default
// 返回服务器本机按固定优先级发现的默认 kubeconfig, 供注册面板一键预填。
func K8sDefaultKubeconfigHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(k8sPluginID, w) {
		return
	}
	path, data, err := discoverDefaultKubeconfig()
	if err != nil {
		WriteJSON(w, map[string]any{"ok": true, "found": false})
		return
	}
	resp := map[string]any{"ok": true, "found": true, "path": path, "source": string(data)}
	cfg, lerr := clientcmd.Load(data)
	if lerr != nil {
		WriteJSON(w, resp)
		return
	}
	type ctxView struct {
		Name    string `json:"name"`
		Cluster string `json:"cluster"`
		Server  string `json:"server"`
		Current bool   `json:"current"`
	}
	contexts := []ctxView{}
	for cName, c := range cfg.Contexts {
		server := ""
		if cl, ok := cfg.Clusters[c.Cluster]; ok {
			server = cl.Server
		}
		contexts = append(contexts, ctxView{Name: cName, Cluster: c.Cluster, Server: server, Current: cName == cfg.CurrentContext})
	}
	sort.Slice(contexts, func(i, j int) bool { return contexts[i].Name < contexts[j].Name })
	resp["contexts"] = contexts
	resp["current"] = cfg.CurrentContext
	WriteJSON(w, resp)
}

// discoverDefaultKubeconfig 按固定优先级返回服务器本机第一份存在的 kubeconfig。
// 顺序: $OPSCORE_KUBECONFIG → $KUBECONFIG(冒号多路径) → ~/.kube/config → /etc/kubernetes/admin.conf
func discoverDefaultKubeconfig() (string, []byte, error) {
	var paths []string
	if v := os.Getenv("KUBECONFIG"); v != "" {
		for _, p := range filepath.SplitList(v) {
			if p = strings.TrimSpace(p); p != "" {
				paths = append(paths, p)
			}
		}
	}
	if home, e := os.UserHomeDir(); e == nil {
		paths = append(paths, filepath.Join(home, ".kube", "config"))
	} else if u, ue := user.Current(); ue == nil {
		paths = append(paths, filepath.Join(u.HomeDir, ".kube", "config"))
	}
	paths = append(paths, "/root/.kube/config") // systemd 服务无 $HOME 时兜底
	paths = append(paths, "/etc/kubernetes/admin.conf")
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		return p, data, nil
	}
	return "", nil, fmt.Errorf("未找到默认 kubeconfig")
}

// ===== 集群操作 (删除 / 重探测) =====

type k8sClusterActionBody struct {
	ID     string `json:"id"`
	Action string `json:"action"` // delete | probe
}

func K8sClusterActionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(k8sPluginID, w) {
		return
	}
	var b k8sClusterActionBody
	if err := k8sJSONDecode(r, &b); err != nil || !reK8sClusterID.MatchString(b.ID) {
		WriteJSON(w, map[string]any{"ok": false, "error": "invalid body(id)"})
		return
	}
	switch b.Action {
	case "delete", "probe":
	default:
		WriteJSON(w, map[string]any{"ok": false, "error": "action 必须是 delete/probe"})
		return
	}

	k8sKubeMu.Lock()
	defer k8sKubeMu.Unlock()

	clusters := k8sListClusters()
	rec := k8sFindCluster(clusters, b.ID)
	if rec == nil {
		WriteJSON(w, map[string]any{"ok": false, "error": "集群不存在"})
		return
	}
	switch b.Action {
	case "delete":
		k8sMgr.Remove(b.ID)
		_ = os.Remove(k8sKubePath(b.ID))
		_ = k8sSaveClusters(k8sRemoveCluster(clusters, b.ID))
		log.Printf("[K8S-AUDIT] action=delete cluster=%s", b.ID)
		InvalidateRespCache("/api/plugins/containers/k8s")
		WriteJSON(w, map[string]any{"ok": true, "action": "delete", "id": b.ID})
	case "probe":
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		info, perr := k8sMgr.Probe(ctx, b.ID)
		cancel()
		if perr == nil {
			rec.Status, rec.Version, rec.APIServer = "ready", info.Version, info.APIServer
		} else {
			rec.Status = "unreachable"
		}
		_ = k8sUpsertCluster(*rec)
		log.Printf("[K8S-AUDIT] action=probe cluster=%s status=%s err=%v", b.ID, rec.Status, perr)
		msg := ""
		if perr != nil {
			msg = perr.Error()
		}
		WriteJSON(w, map[string]any{"ok": perr == nil, "cluster": rec, "error": msg})
	}
}

// ===== 资源列表 (只读) =====

// K8sResourcesHandler GET ?cluster=&res=&ns= (ns=all 或空 → 全部命名空间)
func K8sResourcesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(k8sPluginID, w) {
		return
	}
	ServeCachedJSON(w, r, 5*time.Second, func() any { return k8sResourcesBuild(r) })
}

func k8sResourcesBuild(r *http.Request) any {
	cluster := r.URL.Query().Get("cluster")
	res := r.URL.Query().Get("res")
	ns := strings.TrimSpace(r.URL.Query().Get("ns"))
	if ns == "all" {
		ns = ""
	}
	if ns != "" && !reK8sNamespace.MatchString(ns) {
		return map[string]any{"rows": []map[string]any{}, "note": "namespace 名称非法"}
	}
	if !kubernetes.ValidResource(res) {
		return map[string]any{"rows": []map[string]any{}, "note": "不支持的资源类型"}
	}
	if !reK8sClusterID.MatchString(cluster) {
		return map[string]any{"rows": []map[string]any{}, "note": "cluster 参数非法"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	rows, err := k8sMgr.ListResources(ctx, cluster, res, ns)
	if err != nil {
		return map[string]any{"rows": []map[string]any{}, "note": err.Error()}
	}
	return map[string]any{"rows": rows}
}

// K8sOverviewHandler GET ?cluster= → 概览仪表盘计数
func K8sOverviewHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(k8sPluginID, w) {
		return
	}
	ServeCachedJSON(w, r, 5*time.Second, func() any { return k8sOverviewBuild(r) })
}

func k8sOverviewBuild(r *http.Request) any {
	cluster := r.URL.Query().Get("cluster")
	if !reK8sClusterID.MatchString(cluster) {
		return map[string]any{"note": "cluster 参数非法"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := k8sMgr.Overview(ctx, cluster)
	if err != nil {
		return map[string]any{"note": err.Error()}
	}
	return out
}

// ===== Pod 日志与容器枚举 (只读) =====

// K8sPodLogsHandler GET ?cluster=&ns=&pod=&container=&tail=&previous=
func K8sPodLogsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(k8sPluginID, w) {
		return
	}
	q := r.URL.Query()
	cluster, ns, pod := q.Get("cluster"), q.Get("ns"), q.Get("pod")
	if !reK8sClusterID.MatchString(cluster) || !reK8sNamespace.MatchString(ns) ||
		!reContainerName.MatchString(pod) {
		WriteJSON(w, map[string]any{"ok": false, "error": "参数非法(cluster/ns/pod)"})
		return
	}
	tail := int64(200)
	if v, terr := strconv.ParseInt(q.Get("tail"), 10, 64); terr == nil && v > 0 {
		tail = v
	}
	if tail > 500 {
		tail = 500 // 上限防护对齐容器日志, 避免大日志拖垮响应
	}
	previous := q.Get("previous") == "1"
	container := q.Get("container")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	logs, err := k8sMgr.PodLogs(ctx, cluster, ns, pod, container, tail, previous)
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	WriteJSON(w, map[string]any{
		"ok": err == nil, "logs": logs, "pod": pod, "namespace": ns, "error": msg,
	})
}

// K8sPodContainersHandler GET ?cluster=&ns=&pod= → 容器名列表(日志容器选择)
func K8sPodContainersHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(k8sPluginID, w) {
		return
	}
	q := r.URL.Query()
	cluster, ns, pod := q.Get("cluster"), q.Get("ns"), q.Get("pod")
	if !reK8sClusterID.MatchString(cluster) || !reK8sNamespace.MatchString(ns) ||
		!reContainerName.MatchString(pod) {
		WriteJSON(w, map[string]any{"ok": false, "error": "参数非法(cluster/ns/pod)"})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	containers := k8sMgr.ListPodContainers(ctx, cluster, ns, pod)
	if containers == nil {
		containers = []string{}
	}
	WriteJSON(w, map[string]any{"ok": true, "containers": containers})
}

// ===== 内部辅助 =====

func decodeKubeconfig(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "apiVersion:") {
		return []byte(s), nil
	}
	data, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("kubeconfig 既非 YAML 也非合法 base64")
	}
	return data, nil
}

func k8sSanitizeID(name string) string {
	var b strings.Builder
	name = strings.ToLower(strings.TrimSpace(name))
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == ' ':
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

func k8sListClusters() []central.K8sCluster {
	if k8sStoreFn == nil {
		return []central.K8sCluster{}
	}
	cs, err := k8sStoreFn().GetK8sClusters()
	if err != nil || cs == nil {
		return []central.K8sCluster{}
	}
	return cs
}

func k8sFindCluster(cs []central.K8sCluster, id string) *central.K8sCluster {
	for i := range cs {
		if cs[i].ID == id {
			return &cs[i]
		}
	}
	return nil
}

func k8sRemoveCluster(cs []central.K8sCluster, id string) []central.K8sCluster {
	out := cs[:0]
	for _, c := range cs {
		if c.ID != id {
			out = append(out, c)
		}
	}
	return out
}

func k8sUpsertCluster(rec central.K8sCluster) error {
	cs := k8sListClusters()
	for i := range cs {
		if cs[i].ID == rec.ID {
			cs[i] = rec
			return k8sSaveClusters(cs)
		}
	}
	cs = append(cs, rec)
	return k8sSaveClusters(cs)
}

func k8sSaveClusters(cs []central.K8sCluster) error {
	if k8sStoreFn == nil {
		return fmt.Errorf("k8s store 未初始化")
	}
	return k8sStoreFn().SetK8sClusters(cs)
}
