package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// K8sPodRelatedHandler GET ?cluster=&ns=&name= — 网络(命中策略) + 存储(PVC→PV→SC)只读归因。
func K8sPodRelatedHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(k8sPluginID, w) {
		return
	}
	cluster, _, ns, name, ok := k8sTarget(r)
	if !ok {
		WriteJSON(w, map[string]any{"ok": false, "error": "参数非法"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	rel, err := k8sMgr.PodRelated(ctx, cluster, ns, name)
	if err != nil {
		WriteJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	WriteJSON(w, map[string]any{"ok": true, "related": rel})
}

// K8sPodLogHandler GET ?cluster=&ns=&name=&container=&tail=  — 近 N 行日志(单次)。
func K8sPodLogHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(k8sPluginID, w) {
		return
	}
	cluster, _, ns, name, ok := k8sTarget(r)
	if !ok {
		WriteJSON(w, map[string]any{"ok": false, "error": "参数非法"})
		return
	}
	container := r.URL.Query().Get("container")
	var tail int64
	if s := r.URL.Query().Get("tail"); s != "" {
		fmt.Sscan(s, &tail)
		if tail <= 0 || tail > 5000 {
			tail = 200
		}
	} else {
		tail = 200
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	out, err := k8sMgr.PodLog(ctx, cluster, ns, name, container, tail)
	if err != nil {
		WriteJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	WriteJSON(w, map[string]any{"ok": true, "log": out})
}

type k8sPodExecBody struct {
	Cluster   string   `json:"cluster"`
	Ns        string   `json:"ns"`
	Name      string   `json:"name"`
	Container string   `json:"container"`
	Command   []string `json:"command"`
}

// K8sPodExecHandler POST {cluster,ns,name,container,command[]} — 单次命令回显(非交互).
func K8sPodExecHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(k8sPluginID, w) {
		return
	}
	var b k8sPodExecBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil ||
		!reK8sClusterID.MatchString(b.Cluster) || !reK8sNamespace.MatchString(b.Ns) ||
		!reK8sResName.MatchString(b.Name) || len(b.Command) == 0 {
		WriteJSON(w, map[string]any{"ok": false, "error": "invalid body(cluster/ns/name/command)"})
		return
	}
	if len(strings.Join(b.Command, " ")) > 4096 {
		WriteJSON(w, map[string]any{"ok": false, "error": "命令过长"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	stdout, stderr, err := k8sMgr.PodExec(ctx, b.Cluster, b.Ns, b.Name, b.Container, b.Command)
	resp := map[string]any{"ok": err == nil, "stdout": stdout, "stderr": stderr}
	if err != nil {
		resp["error"] = err.Error()
	}
	WriteJSON(w, resp)
}
