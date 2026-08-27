package handlers

// ── K8s 资源操作(写)与详情/YAML(读) ──
// 删 Pod / 扩缩容 / 滚动重启 / YAML 查看(Secret 脱敏) / Pod 详情。
// 全部过 pluginGuard 热生效守卫, 写操作带 [K8S-AUDIT] 审计。

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"opscore/internal/kubernetes"
)

var reK8sResName = reK8sNamespace // 同为 DNS-1123 子域规则

func k8sTarget(r *http.Request) (cluster, res, ns, name string, ok bool) {
	q := r.URL.Query()
	cluster, res, ns, name = q.Get("cluster"), q.Get("res"), q.Get("ns"), q.Get("name")
	if !reK8sClusterID.MatchString(cluster) || (res != "" && !kubernetes.ValidResource(res)) {
		return "", "", "", "", false
	}
	if ns != "" && !reK8sNamespace.MatchString(ns) {
		return "", "", "", "", false
	}
	if !reK8sResName.MatchString(name) {
		return "", "", "", "", false
	}
	return cluster, res, ns, name, true
}

// ===== Pod / 工作负载 详情 =====

// K8sPodDetailHandler GET ?cluster=&ns=&name=
func K8sPodDetailHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(k8sPluginID, w) {
		return
	}
	cluster, res, ns, name, ok := k8sTarget(r)
	if !ok {
		WriteJSON(w, map[string]any{"ok": false, "error": "参数非法"})
		return
	}
	if res == "" { // Pod 详情默认按 pods 处理
		res = "pods"
	}
	if res != "pods" {
		WriteJSON(w, map[string]any{"ok": false, "error": "仅支持 pods 详情"})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	detail, err := k8sMgr.PodDetail(ctx, cluster, ns, name)
	if err != nil {
		WriteJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	WriteJSON(w, map[string]any{"ok": true, "detail": detail})
}

// ===== YAML 查看 =====

// K8sYamlHandler GET ?cluster=&res=&ns=&name=
func K8sYamlHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(k8sPluginID, w) {
		return
	}
	cluster, res, ns, name, ok := k8sTarget(r)
	if !ok || res == "overview" || res == "events" {
		WriteJSON(w, map[string]any{"ok": false, "error": "参数非法"})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	y, err := k8sMgr.GetResourceYAML(ctx, cluster, res, ns, name)
	if err != nil {
		WriteJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	WriteJSON(w, map[string]any{"ok": true, "yaml": y})
}

// ===== 可视化创建(apply) =====

type k8sApplyBody struct {
	Cluster   string `json:"cluster"`
	YAML      string `json:"yaml"`
	Overwrite bool   `json:"overwrite"`
}

// K8sApplyHandler POST {cluster, yaml, overwrite} — 表单生成的 YAML 直接落集群。
func K8sApplyHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(k8sPluginID, w) {
		return
	}
	var b k8sApplyBody
	if err := k8sJSONDecode(r, &b); err != nil || !reK8sClusterID.MatchString(b.Cluster) ||
		len(strings.TrimSpace(b.YAML)) < 8 {
		WriteJSON(w, map[string]any{"ok": false, "error": "invalid body(cluster/yaml)"})
		return
	}
	if len(b.YAML) > 256<<10 {
		WriteJSON(w, map[string]any{"ok": false, "error": "YAML 过大(>256KB)"})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	kind, name, created, err := k8sMgr.ApplyResourceYAML(ctx, b.Cluster, b.YAML, b.Overwrite)
	if err != nil {
		WriteJSON(w, map[string]any{"ok": false, "error": err.Error(), "kind": kind, "name": name})
		return
	}
	action := "create"
	if !created {
		action = "update"
	}
	log.Printf("[K8S-AUDIT] action=apply/%s kind=%s name=%s overwrite=%v", action, kind, name, b.Overwrite)
	InvalidateRespCache("/api/plugins/containers/k8s")
	WriteJSON(w, map[string]any{"ok": true, "kind": kind, "name": name, "created": created})
}

// ===== 资源写操作 =====

type k8sResourceActionBody struct {
	Cluster  string `json:"cluster"`
	Res      string `json:"res"` // pods|deployments|statefulsets|jobs|cronjobs|...
	Ns       string `json:"ns"`
	Name     string `json:"name"`
	Action   string `json:"action"` // delete|scale|restart|rollback|pause|resume|suspend|trigger|rerun|expand|cordon|uncordon|drain|setImage
	Replicas int32  `json:"replicas,omitempty"`
	Revision int64  `json:"revision,omitempty"` // 回滚目标版本(0=上一版)
	Suspend  *bool  `json:"suspend,omitempty"`  // cronjob 挂起开关
	Storage  string `json:"storage,omitempty"`  // pvc 扩容目标容量
	Image    string `json:"image,omitempty"`    // setImage
	Force    bool   `json:"force,omitempty"`    // delete 时是否强制(grace=0)
}

// K8sResourceActionHandler POST {cluster,res,ns,name,action[,replicas]}
func K8sResourceActionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(k8sPluginID, w) {
		return
	}
	var b k8sResourceActionBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil ||
		!reK8sClusterID.MatchString(b.Cluster) || !kubernetes.ValidResource(b.Res) ||
		!reK8sResName.MatchString(b.Name) || (b.Ns != "" && !reK8sNamespace.MatchString(b.Ns)) {
		WriteJSON(w, map[string]any{"ok": false, "error": "invalid body"})
		return
	}
	switch b.Action {
	case "delete":
	case "scale":
		if b.Res != "deployments" && b.Res != "statefulsets" {
			WriteJSON(w, map[string]any{"ok": false, "error": "仅 deployments/statefulsets 支持扩缩容"})
			return
		}
		if b.Replicas < 0 || b.Replicas > 1000 {
			WriteJSON(w, map[string]any{"ok": false, "error": "replicas 范围 0-1000"})
			return
		}
	case "restart":
		if b.Res != "deployments" && b.Res != "statefulsets" {
			WriteJSON(w, map[string]any{"ok": false, "error": "仅 deployments/statefulsets 支持滚动重启"})
			return
		}
	case "rollback":
		if b.Res != "deployments" && b.Res != "statefulsets" {
			WriteJSON(w, map[string]any{"ok": false, "error": "仅 deployments/statefulsets 支持回滚"})
			return
		}
	case "pause", "resume":
		if b.Res != "deployments" {
			WriteJSON(w, map[string]any{"ok": false, "error": "仅 deployments 支持暂停/恢复发布"})
			return
		}
	case "suspend":
		if b.Res != "cronjobs" {
			WriteJSON(w, map[string]any{"ok": false, "error": "仅 cronjobs 支持 suspend"})
			return
		}
	case "trigger":
		if b.Res != "cronjobs" {
			WriteJSON(w, map[string]any{"ok": false, "error": "仅 cronjobs 支持立即触发"})
			return
		}
	case "rerun":
		if b.Res != "jobs" {
			WriteJSON(w, map[string]any{"ok": false, "error": "仅 jobs 支持重跑"})
			return
		}
	case "expand":
		if b.Res != "persistentvolumeclaims" {
			WriteJSON(w, map[string]any{"ok": false, "error": "仅 PVC 支持扩容"})
			return
		}
		if b.Storage == "" {
			WriteJSON(w, map[string]any{"ok": false, "error": "缺少 storage 参数"})
			return
		}
	case "cordon", "uncordon":
		if b.Res != "nodes" {
			WriteJSON(w, map[string]any{"ok": false, "error": "仅 nodes 支持 cordon/uncordon"})
			return
		}
	case "drain":
		if b.Res != "nodes" {
			WriteJSON(w, map[string]any{"ok": false, "error": "仅 nodes 支持 drain"})
			return
		}
	case "setImage":
		if b.Res != "deployments" && b.Res != "statefulsets" && b.Res != "daemonsets" {
			WriteJSON(w, map[string]any{"ok": false, "error": "仅 workload 支持镜像更新"})
			return
		}
		if strings.TrimSpace(b.Image) == "" {
			WriteJSON(w, map[string]any{"ok": false, "error": "缺少 image 参数"})
			return
		}
	default:
		WriteJSON(w, map[string]any{"ok": false, "error": "未知 action"})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	var err error
	extra := ""
	switch b.Action {
	case "delete":
		err = k8sMgr.DeleteResource(ctx, b.Cluster, b.Res, b.Ns, b.Name, b.Force)
	case "scale":
		err = k8sMgr.ScaleWorkload(ctx, b.Cluster, b.Res, b.Ns, b.Name, b.Replicas)
	case "restart":
		err = k8sMgr.RestartWorkload(ctx, b.Cluster, b.Res, b.Ns, b.Name)
	case "rollback":
		err = k8sMgr.RolloutUndo(ctx, b.Cluster, b.Ns, b.Name, b.Revision)
	case "pause":
		err = k8sMgr.RolloutPause(ctx, b.Cluster, "deployments", b.Ns, b.Name, true)
	case "resume":
		err = k8sMgr.RolloutPause(ctx, b.Cluster, "deployments", b.Ns, b.Name, false)
	case "suspend":
		sv := b.Suspend != nil && *b.Suspend
		err = k8sMgr.SuspendCronJob(ctx, b.Cluster, b.Ns, b.Name, sv)
		extra = fmt.Sprintf("suspend=%v", sv)
	case "trigger":
		var jn string
		jn, err = k8sMgr.TriggerCronJob(ctx, b.Cluster, b.Ns, b.Name)
		extra = "job=" + jn
	case "rerun":
		var jn string
		jn, err = k8sMgr.RerunJob(ctx, b.Cluster, b.Ns, b.Name)
		extra = "new=" + jn
	case "expand":
		err = k8sMgr.ExpandPVC(ctx, b.Cluster, b.Ns, b.Name, b.Storage)
		extra = "storage=" + b.Storage
	case "cordon":
		err = k8sMgr.NodeCordon(ctx, b.Cluster, b.Name, true)
	case "uncordon":
		err = k8sMgr.NodeCordon(ctx, b.Cluster, b.Name, false)
	case "drain":
		var evicted, skipped int
		evicted, skipped, err = k8sMgr.NodeDrain(ctx, b.Cluster, b.Name)
		extra = fmt.Sprintf("evicted=%d skipped(daemonset/static)=%d", evicted, skipped)
	case "setImage":
		err = k8sMgr.SetWorkloadImage(ctx, b.Cluster, b.Res, b.Ns, b.Name, b.Image)
		extra = "image=" + b.Image
	}
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	log.Printf("[K8S-AUDIT] action=%s res=%s cluster=%s ns=%s name=%s replicas=%d %s err=%q",
		b.Action, b.Res, b.Cluster, b.Ns, b.Name, b.Replicas, extra, msg)
	InvalidateRespCache("/api/plugins/containers/k8s")
	WriteJSON(w, map[string]any{"ok": err == nil, "error": msg})
}

// ===== 副本数查询(scale 弹窗预填用) =====

// K8sReplicasHandler GET ?cluster=&res=&ns=&name= → 当前副本数(预填 scale 弹窗)
func K8sReplicasHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(k8sPluginID, w) {
		return
	}
	cluster, res, ns, name, ok := k8sTarget(r)
	if !ok || (res != "deployments" && res != "statefulsets") {
		WriteJSON(w, map[string]any{"ok": false, "error": "参数非法"})
		return
	}
	spec, ready, err := k8sMgr.GetReplicas(r.Context(), cluster, res, ns, name)
	if err != nil {
		WriteJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	WriteJSON(w, map[string]any{"ok": true, "replicas": spec, "ready": ready})
}

// ===== Rollout 历史 =====

// K8sRolloutHistoryHandler GET ?cluster=&ns=&name= → ReplicaSet 版本列表
func K8sRolloutHistoryHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(k8sPluginID, w) {
		return
	}
	cluster, res, ns, name, ok := k8sTarget(r)
	if !ok || (res != "deployments" && res != "statefulsets") {
		WriteJSON(w, map[string]any{"ok": false, "error": "参数非法"})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	revs, err := k8sMgr.RolloutHistory(ctx, cluster, ns, name)
	if err != nil {
		WriteJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	WriteJSON(w, map[string]any{"ok": true, "revisions": revs})
}

// ===== YAML 编辑保存 =====

type k8sYamlSaveBody struct {
	Cluster string `json:"cluster"`
	Res     string `json:"res"`
	Ns      string `json:"ns"`
	Name    string `json:"name"`
	Yaml    string `json:"yaml"`
}

// K8sYamlSaveHandler POST 编辑后的 YAML 更新资源
func K8sYamlSaveHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(k8sPluginID, w) {
		return
	}
	var b k8sYamlSaveBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || b.Yaml == "" {
		WriteJSON(w, map[string]any{"ok": false, "error": "invalid body"})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	err := k8sMgr.UpdateResourceYAML(ctx, b.Cluster, b.Res, b.Ns, b.Name, b.Yaml)
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	log.Printf("[K8S-AUDIT] action=yaml-update res=%s cluster=%s ns=%s name=%s err=%q",
		b.Res, b.Cluster, b.Ns, b.Name, msg)
	InvalidateRespCache("/api/plugins/containers/k8s")
	WriteJSON(w, map[string]any{"ok": err == nil, "error": msg})
}
