package handlers

// K8s 资源 × 操作 目录 + 查表分发端点 (与 k8s_actions.go 的 legacy 单 action 端点并存,
// 互不影响, 便于逐步迁移)。
//
//   GET  /api/plugins/containers/k8s/action-catalog?cluster=&res=&all=  → 返回目录
//   POST /api/plugins/containers/k8s/action                              → 查表执行
//   GET  /api/plugins/containers/k8s/feature-flags                       → 平台侧开关(边车等)

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"opscore/internal/kubernetes"
)

// K8sActionCatalogHandler GET → 返回 action 目录
// query: cluster (可空) / res (可空) / all=1 (返回全量, 否则按 res 过滤)
func K8sActionCatalogHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(k8sPluginID, w) {
		return
	}
	q := r.URL.Query()
	res := q.Get("res")
	all := q.Get("all") == "1"
	var list []*kubernetes.ActionSpec
	if all || res == "" {
		for _, a := range kubernetes.ActionCatalog {
			list = append(list, a)
		}
	} else {
		if !kubernetes.ValidResource(res) && !kubernetes.IsCRDName(res) {
			WriteJSON(w, map[string]any{"ok": false, "error": "未知 res: " + res})
			return
		}
		list = kubernetes.CatalogByRes(res)
	}
	WriteJSON(w, map[string]any{
		"ok":        true,
		"actions":   stripFuncs(list),
		"ephemeral": kubernetes.EphemeralContainersAllowed(),
	})
}

// stripFuncs 把每个 action 的 Run 函数脱敏, 前端不能也不应见
func stripFuncs(in []*kubernetes.ActionSpec) []map[string]any {
	out := make([]map[string]any, 0, len(in))
	for _, a := range in {
		// 我们要序列化 ActionSpec 的 JSON tag, 但 Run 是 func 不能序列化。
		// 走手动构造避免 marshal 失败。
		item := map[string]any{
			"name":        a.Name,
			"label":       a.Label,
			"category":    a.Category,
			"allowedRes":  a.AllowedRes,
			"params":      a.Params,
			"requiresTTY": a.RequiresTTY,
			"description": a.Description,
		}
		_ = a
		out = append(out, item)
	}
	return out
}

// K8sActionHandler POST {cluster,res,ns,name,action,params} → 查表执行
func K8sActionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(k8sPluginID, w) {
		return
	}
	var b struct {
		Cluster string                 `json:"cluster"`
		Res     string                 `json:"res"`
		Ns      string                 `json:"ns"`
		Name    string                 `json:"name"`
		Action  string                 `json:"action"`
		Params  map[string]any         `json:"params"`
		Extra   map[string]any         `json:"extra,omitempty"` // 兼容老 schema 字段
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		WriteJSON(w, map[string]any{"ok": false, "error": "invalid body"})
		return
	}
	if !reK8sClusterID.MatchString(b.Cluster) || (!kubernetes.ValidResource(b.Res) && !kubernetes.IsCRDName(b.Res)) ||
		!reK8sResName.MatchString(b.Name) || (b.Ns != "" && !reK8sNamespace.MatchString(b.Ns)) {
		WriteJSON(w, map[string]any{"ok": false, "error": "invalid body"})
		return
	}
	spec, ok := kubernetes.ActionCatalog[b.Action]
	if !ok {
		WriteJSON(w, map[string]any{"ok": false, "error": "未知 action: " + b.Action})
		return
	}
	if !spec.AllowsRes(b.Res) {
		WriteJSON(w, map[string]any{"ok": false, "error": "资源 " + b.Res + " 不支持 action " + b.Action})
		return
	}
	// 合并 old schema 兼容字段 (replicas / revision / suspend / image / storage)
	merged := map[string]any{}
	for k, v := range b.Params {
		merged[k] = v
	}
	for k, v := range b.Extra {
		if _, exists := merged[k]; !exists {
			merged[k] = v
		}
	}
	if err := kubernetes.ValidateParams(spec, merged); err != nil {
		WriteJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	// 流式 (WS) action 走专门 WS handler, 不在这里执行
	if spec.RequiresTTY {
		WriteJSON(w, map[string]any{"ok": false, "error": "流式 action 需走 WS 端点 /pod/exec/ws 等"})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	rc := kubernetes.RunCtx{
		Ctx:     ctx,
		Cluster: b.Cluster,
		Res:     b.Res,
		Ns:      b.Ns,
		Name:    b.Name,
		Params:  merged,
	}
	err := spec.Run(k8sMgr, rc)
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	log.Printf("[K8S-AUDIT] action=%s via=catalog res=%s cluster=%s ns=%s name=%s params=%v err=%q",
		b.Action, b.Res, b.Cluster, b.Ns, b.Name, merged, msg)
	InvalidateRespCache("/api/plugins/containers/k8s")
	WriteJSON(w, map[string]any{"ok": err == nil, "error": msg})
}

// K8sFeatureFlagsHandler GET → 平台开关(边车允许?等)
func K8sFeatureFlagsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(k8sPluginID, w) {
		return
	}
	WriteJSON(w, map[string]any{
		"ok":        true,
		"ephemeral": kubernetes.EphemeralContainersAllowed(),
	})
}
