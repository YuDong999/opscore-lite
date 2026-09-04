package handlers

import (
	"context"
	"net/http"
	"time"

	"opscore/internal/kubernetes"
)

// K8sCRDsHandler GET ?cluster=&refresh=1
// 返回集群已注册的 CRD 列表 (供前端资源下拉渲染 CRD 分组).
// refresh=1 强制重新发现 (绕开 5 分钟缓存).
func K8sCRDsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(k8sPluginID, w) {
		return
	}
	q := r.URL.Query()
	cluster := q.Get("cluster")
	if !reK8sClusterID.MatchString(cluster) {
		WriteJSON(w, map[string]any{"ok": false, "error": "invalid cluster"})
		return
	}
	refresh := q.Get("refresh") == "1"
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	infos, err := k8sMgr.DiscoverCRDs(ctx, cluster, refresh)
	if err != nil {
		WriteJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	WriteJSON(w, map[string]any{
		"ok":    true,
		"crds":  infos,
		"total": len(infos),
	})
}

// K8sCRDTypesHandler GET → 返回已注册集群 + 每个集群 CRD 短名清单 (前端启动时一次拉取用).
func K8sCRDTypesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(k8sPluginID, w) {
		return
	}
	clusters := k8sMgr.ListIDs()
	out := map[string][]string{}
	for _, c := range clusters {
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		infos, err := k8sMgr.DiscoverCRDs(ctx, c, false)
		cancel()
		if err != nil {
			continue
		}
		names := make([]string, 0, len(infos))
		for _, i := range infos {
			names = append(names, i.ShortName)
		}
		out[c] = names
	}
	WriteJSON(w, map[string]any{"ok": true, "clusters": out})
}

// 保留 kubernetes 引用 (供其他文件 import path 一致; 实际功能调用见 actions_extras.go / k8s_action_catalog.go).
var _ = kubernetes.ValidResource
