package handlers

// ── 容器管理插件 · K8s 集群注册的远程凭据来源 ──
// 注册面板「从主机组主机拉取」: 经 RunOnTarget 在目标机上探测常见 kubeconfig 路径并取回
// 第一份, 供注册预填。凭据持久化仍由注册流程负责(<dataDir>/kubeconfigs/<id>.yaml, 0600);
// 后续集群操作依然是服务端 client-go 直连 apiserver, 远程拉取只解决凭据文件在哪。
// 与 k8s_certs.go 的 masterTarget 同一世界观: 凭据跟着主机走, 而不是假设部署机上有凭据。

import (
	"net/http"
	"sort"
	"strings"

	"k8s.io/client-go/tools/clientcmd"
)

// k8sRemoteKubeconfigScript 在目标机上按常见路径探测第一份 kubeconfig。
// 命中: 首行输出 __KCPATH__<路径>, 随后为文件内容; 未命中: 输出 __KC_NONE__。
const k8sRemoteKubeconfigScript = `for kc in "$HOME/.kube/config" /root/.kube/config /etc/kubernetes/admin.conf /etc/rancher/k3s/k3s.yaml; do [ -f "$kc" ] && { printf '%s\n' "__KCPATH__$kc"; cat "$kc"; exit 0; }; done; echo "__KC_NONE__"`

// K8sKubeconfigFromHostHandler POST /api/plugins/containers/k8s/kubeconfig/remote
// body: {"hostID":"..."}
func K8sKubeconfigFromHostHandler(w http.ResponseWriter, r *http.Request) {
	var b struct {
		HostID string `json:"hostID"`
	}
	if err := k8sJSONDecode(r, &b); err != nil || b.HostID == "" {
		WriteJSON(w, map[string]any{"ok": false, "error": "invalid body(hostID)"})
		return
	}
	if IsLocalTarget(b.HostID) {
		WriteJSON(w, map[string]any{"ok": false, "error": "本机请使用「读取服务器默认配置」"})
		return
	}
	out, err := RunOnTarget(b.HostID, []string{"sh", "-c", k8sRemoteKubeconfigScript})
	out = strings.TrimSpace(out)
	if err != nil && !strings.Contains(out, "__KC_") {
		WriteJSON(w, map[string]any{"ok": true, "found": false, "error": "远程读取失败: " + err.Error()})
		return
	}
	if !strings.HasPrefix(out, "__KCPATH__") {
		WriteJSON(w, map[string]any{"ok": true, "found": false, "error": "目标机上未发现 kubeconfig(已探测 ~/.kube/config、/root/.kube/config、/etc/kubernetes/admin.conf、k3s 默认路径)"})
		return
	}
	nl := strings.IndexByte(out, '\n')
	if nl <= 0 {
		WriteJSON(w, map[string]any{"ok": true, "found": false, "error": "远程输出异常"})
		return
	}
	path := strings.TrimPrefix(out[:nl], "__KCPATH__")
	data := []byte(out[nl+1:])
	cfg, lerr := clientcmd.Load(data)
	if lerr != nil {
		WriteJSON(w, map[string]any{"ok": true, "found": true, "path": path, "source": string(data), "error": "文件存在但解析失败: " + lerr.Error()})
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
	WriteJSON(w, map[string]any{"ok": true, "found": true, "path": path, "source": string(data), "contexts": contexts, "current": cfg.CurrentContext})
}
