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
	if !reK8sClusterID.MatchString(cluster) {
		return "", "", "", "", false
	}
	if res != "" && !kubernetes.ValidResource(res) && !kubernetes.IsCRDName(res) {
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

// K8sDescribeHandler GET ?cluster=&res=&ns=&name= — kubectl describe 风格只读文本。
func K8sDescribeHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(k8sPluginID, w) {
		return
	}
	cluster, res, ns, name, ok := k8sTarget(r)
	if !ok || res == "" {
		WriteJSON(w, map[string]any{"ok": false, "error": "参数非法"})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	text, err := k8sMgr.DescribeResource(ctx, cluster, res, ns, name)
	if err != nil {
		WriteJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	WriteJSON(w, map[string]any{"ok": true, "describe": text})
}

// K8sEventsAggregateHandler GET ?cluster=&ns= — 集群(或指定命名空间)事件按 reason+对象 聚合。
func K8sEventsAggregateHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(k8sPluginID, w) {
		return
	}
	cluster := r.URL.Query().Get("cluster")
	if !reK8sClusterID.MatchString(cluster) {
		WriteJSON(w, map[string]any{"ok": false, "error": "参数非法"})
		return
	}
	ns := r.URL.Query().Get("ns")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	rows, err := k8sMgr.AggregateEvents(ctx, cluster, ns)
	if err != nil {
		WriteJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	WriteJSON(w, map[string]any{"ok": true, "rows": rows})
}

// K8sObjectEventsHandler GET ?cluster=&res=&ns=&name= — 单个对象的关联事件(describe 风格尾段)。
func K8sObjectEventsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(k8sPluginID, w) {
		return
	}
	cluster, res, ns, name, ok := k8sTarget(r)
	if !ok || res == "" {
		WriteJSON(w, map[string]any{"ok": false, "error": "参数非法"})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	rows, err := k8sMgr.ObjectEvents(ctx, cluster, res, ns, name)
	if err != nil {
		WriteJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	WriteJSON(w, map[string]any{"ok": true, "rows": rows})
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
	Key       string `json:"key,omitempty"`    // label/annotate 键
	Value     string `json:"value,omitempty"`  // label/annotate 值
	Overwrite bool   `json:"overwrite,omitempty"` // label 覆盖已有
	// drain 扩展: 驱逐选项
	IgnoreDaemonsets  bool `json:"ignoreDaemonsets,omitempty"`
	DeleteEmptyDirData bool `json:"deleteEmptyDirData,omitempty"`
	GraceSeconds      int64 `json:"graceSeconds,omitempty"`
}

// legacyActionMap 把 legacy 端点 action 名映射成 catalog action 名
func legacyActionMap(action string) string {
	if action == "setImage" {
		return "set-image"
	}
	return action
}

// legacyParamsToCatalog 把 legacy body 字段按 catalog 参数名组装成 params。
// legacy 字段名与 catalog 参数名设计时已对齐, 直接一对一转存。
func legacyParamsToCatalog(b k8sResourceActionBody) map[string]any {
	params := map[string]any{}
	if b.Replicas != 0 {
		params["replicas"] = b.Replicas
	}
	if b.Revision != 0 {
		params["revision"] = b.Revision
	}
	if b.Suspend != nil {
		params["suspend"] = *b.Suspend
	}
	if b.Storage != "" {
		params["storage"] = b.Storage
	}
	if b.Image != "" {
		params["image"] = b.Image
	}
	if b.Force {
		params["force"] = b.Force
	}
	if b.Key != "" {
		params["key"] = b.Key
	}
	if b.Value != "" {
		params["value"] = b.Value
	}
	if b.Overwrite {
		params["overwrite"] = b.Overwrite
	}
	if b.IgnoreDaemonsets {
		params["ignoreDaemonsets"] = b.IgnoreDaemonsets
	}
	if b.DeleteEmptyDirData {
		params["deleteEmptyDirData"] = b.DeleteEmptyDirData
	}
	if b.GraceSeconds != 0 {
		params["graceSeconds"] = b.GraceSeconds
	}
	return params
}

// requireParams 按 catalog 的 Required/Pattern 元数据校验参数 (单一来源)
func requireParams(spec *kubernetes.ActionSpec, params map[string]any) error {
	return kubernetes.ValidateParams(spec, params)
}

// K8sResourceActionHandler POST {cluster,res,ns,name,action[,replicas]}
// 薄代理: legacy 参数转 catalog RunCtx → 查表 spec.Run (能力/校验单一来源)。
// 保留同名端点与字段, 方便存量调用；返回结构与旧版一致。
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
		!reK8sClusterID.MatchString(b.Cluster) || (!kubernetes.ValidResource(b.Res) && !kubernetes.IsCRDName(b.Res)) ||
		!reK8sResName.MatchString(b.Name) || (b.Ns != "" && !reK8sNamespace.MatchString(b.Ns)) {
		WriteJSON(w, map[string]any{"ok": false, "error": "invalid body"})
		return
	}
	catAction := legacyActionMap(b.Action)
	spec, ok := kubernetes.ActionCatalog[catAction]
	if !ok {
		WriteJSON(w, map[string]any{"ok": false, "error": "未知 action"})
		return
	}
	if !spec.AllowsRes(b.Res) {
		WriteJSON(w, map[string]any{"ok": false, "error": fmt.Sprintf("资源 %s 不支持操作 %s", b.Res, b.Action)})
		return
	}
	if spec.RequiresTTY {
		WriteJSON(w, map[string]any{"ok": false, "error": "流式操作请走专属端点"})
		return
	}
	// label 伪资源守卫: overview/events 不落集群 (保持 legacy 语义)
	if catAction == "label" && (b.Res == "overview" || b.Res == "events") {
		WriteJSON(w, map[string]any{"ok": false, "error": "资源类型不支持 label"})
		return
	}
	// 保留 drain 弃用: k8s delete-node 的 drain 选项由 drain 字段组传入, 无需额外校验
	params := legacyParamsToCatalog(b)
	if err := requireParams(spec, params); err != nil {
		WriteJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	rc := kubernetes.RunCtx{Ctx: ctx, Cluster: b.Cluster, Res: b.Res, Ns: b.Ns, Name: b.Name, Params: params}
	err := spec.Run(k8sMgr, rc)
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	plog := fmt.Sprintf("params=%v", params)
	log.Printf("[K8S-AUDIT] action=%s res=%s cluster=%s ns=%s name=%s %s err=%q",
		catAction, b.Res, b.Cluster, b.Ns, b.Name, plog, msg)
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

// ===== 批量资源写操作 =====

type k8sBatchActionBody struct {
	Cluster  string           `json:"cluster"`
	Res      string           `json:"res"`
	Targets  []map[string]string `json:"targets"`
	Action   string           `json:"action"`
	Replicas int32            `json:"replicas,omitempty"`
	Force    bool             `json:"force,omitempty"`
	Image    string           `json:"image,omitempty"`
}

// K8sBatchActionHandler POST {cluster,res,targets,action[,replicas,force,image]}
// 批量操作多个资源: 逐 target 走 catalog spec.Run, 汇总结果。
// 薄代理: 能力/校验取自 catalog, 与单资源端点一致。
func K8sBatchActionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(k8sPluginID, w) {
		return
	}
	var b k8sBatchActionBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil ||
		!reK8sClusterID.MatchString(b.Cluster) || (!kubernetes.ValidResource(b.Res) && !kubernetes.IsCRDName(b.Res)) ||
		len(b.Targets) == 0 {
		WriteJSON(w, map[string]any{"ok": false, "error": "invalid body"})
		return
	}
	catAction := legacyActionMap(b.Action)
	spec, ok := kubernetes.ActionCatalog[catAction]
	if !ok {
		WriteJSON(w, map[string]any{"ok": false, "error": "未知 action"})
		return
	}
	if !spec.AllowsRes(b.Res) {
		WriteJSON(w, map[string]any{"ok": false, "error": fmt.Sprintf("资源 %s 不支持批量操作 %s", b.Res, b.Action)})
		return
	}
	if spec.RequiresTTY {
		WriteJSON(w, map[string]any{"ok": false, "error": "流式操作不支持批量"})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	succeeded, failed := 0, 0
	var errs []string
	for _, t := range b.Targets {
		name := t["name"]
		tns := t["ns"]
		if !reK8sResName.MatchString(name) {
			failed++
			continue
		}
		if tns != "" && !reK8sNamespace.MatchString(tns) {
			failed++
			continue
		}
		params := map[string]any{}
		if b.Replicas != 0 {
			params["replicas"] = b.Replicas
		}
		if b.Force {
			params["force"] = b.Force
		}
		if b.Image != "" {
			params["image"] = b.Image
		}
		rc := kubernetes.RunCtx{Ctx: ctx, Cluster: b.Cluster, Res: b.Res, Ns: tns, Name: name, Params: params}
		if err := spec.Run(k8sMgr, rc); err != nil {
			failed++
			errs = append(errs, fmt.Sprintf("%s: %s", name, err.Error()))
		} else {
			succeeded++
		}
	}
	log.Printf("[K8S-AUDIT] batch action=%s res=%s cluster=%s count=%d ok=%d fail=%d",
		catAction, b.Res, b.Cluster, len(b.Targets), succeeded, failed)
	InvalidateRespCache("/api/plugins/containers/k8s")
	WriteJSON(w, map[string]any{
		"ok": failed == 0,
		"succeeded": succeeded,
		"failed": failed,
		"errors": errs,
	})
}
