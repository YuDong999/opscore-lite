package kubernetes

// 通用只读资源采集: dynamic client 按 GVR 列表, 经 runtime 转换器映射到 typed 结构后提取精简行。
// 一期覆盖: pods / deployments / services / configmaps / secrets / nodes / events / namespaces。

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	scv1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	sigsyaml "sigs.k8s.io/yaml"
)

var (
	gvrPods          = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}
	gvrServices      = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "services"}
	gvrConfigMaps    = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "configmaps"}
	gvrSecrets       = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "secrets"}
	gvrNodes         = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "nodes"}
	gvrEvents        = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "events"}
	gvrNamespaces    = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "namespaces"}
	gvrPVs           = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "persistentvolumes"}
	gvrPVCs          = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "persistentvolumeclaims"}
	gvrDeployments   = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	gvrReplicaSets   = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "replicasets"}
	gvrStatefulSets  = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "statefulsets"}
	gvrDaemonSets    = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "daemonsets"}
	gvrIngresses     = schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"}
	gvrIngressClass  = schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "ingressclasses"}
	gvrNetPolicies   = schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "networkpolicies"}
	gvrJobs          = schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}
	gvrCronJobs      = schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "cronjobs"}
	gvrStorageClasse = schema.GroupVersionResource{Group: "storage.k8s.io", Version: "v1", Resource: "storageclasses"}
	gvrQuotas        = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "resourcequotas"}
	gvrSAs           = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "serviceaccounts"}
	gvrRoles         = schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "roles"}
	gvrClusterRoles  = schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles"}
	gvrRoleBindings  = schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "rolebindings"}
	gvrCRBindings    = schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterrolebindings"}
)

// ValidResource 校验资源名白名单。
func ValidResource(res string) bool {
	switch res {
	case "pods", "deployments", "statefulsets", "daemonsets", "jobs", "cronjobs",
		"services", "ingresses", "configmaps", "secrets",
		"persistentvolumes", "persistentvolumeclaims", "storageclasses",
		"nodes", "namespaces", "events",
		"networkpolicies", "resourcequotas", "ingressclasses",
		"serviceaccounts", "roles", "rolebindings", "clusterroles", "clusterrolebindings":
		return true
	}
	return false
}

func gvrOf(res string) schema.GroupVersionResource {
	switch res {
	case "pods":
		return gvrPods
	case "deployments":
		return gvrDeployments
	case "statefulsets":
		return gvrStatefulSets
	case "daemonsets":
		return gvrDaemonSets
	case "jobs":
		return gvrJobs
	case "cronjobs":
		return gvrCronJobs
	case "ingresses":
		return gvrIngresses
	case "ingressclasses":
		return gvrIngressClass
	case "networkpolicies":
		return gvrNetPolicies
	case "persistentvolumes":
		return gvrPVs
	case "persistentvolumeclaims":
		return gvrPVCs
	case "storageclasses":
		return gvrStorageClasse
	case "resourcequotas":
		return gvrQuotas
	case "services":
		return gvrServices
	case "configmaps":
		return gvrConfigMaps
	case "secrets":
		return gvrSecrets
	case "nodes":
		return gvrNodes
	case "events":
		return gvrEvents
	case "namespaces":
		return gvrNamespaces
	case "serviceaccounts":
		return gvrSAs
	case "roles":
		return gvrRoles
	case "clusterroles":
		return gvrClusterRoles
	case "rolebindings":
		return gvrRoleBindings
	case "clusterrolebindings":
		return gvrCRBindings
	}
	return schema.GroupVersionResource{}
}

// nsFor 决定列表的命名空间作用域: 集群级资源忽略 ns, 其余空串=All Namespaces。
func nsFor(ns, res string) string {
	switch res {
	case "nodes", "namespaces", "persistentvolumes", "storageclasses", "ingressclasses",
		"clusterroles", "clusterrolebindings":
		return ""
	default:
		return ns
	}
}

// ListResources 列出集群指定资源(精简行)。ns 为空表示全部命名空间。
func (m *Manager) ListResources(ctx context.Context, clusterID, res, ns string) ([]map[string]any, error) {
	// 优先: 内置 switch fast path; 失败回落 ResolveGVR (覆盖 CRD 短名)
	gvr := gvrOf(res)
	effectiveNs := nsFor(ns, res)
	if gvr.Empty() {
		crdGVR, scope, err := m.ResolveGVR(clusterID, res)
		if err != nil {
			return nil, fmt.Errorf("unsupported resource %q", res)
		}
		gvr = crdGVR
		if scope == ScopeCluster {
			effectiveNs = ""
		}
	}
	dyn, err := m.DynamicClient(clusterID)
	if err != nil {
		return nil, err
	}
	list, err := dyn.Resource(gvr).Namespace(effectiveNs).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list %s in %s: %w", res, clusterID, err)
	}

	now := time.Now()
	out := make([]map[string]any, 0, len(list.Items))
	for i := range list.Items {
		row, rerr := rowOf(&list.Items[i], res, now)
		if rerr == nil && row != nil {
			out = append(out, row)
		}
	}
	sortRows(out)
	return out, nil
}

// AggregateEvents 聚合全集群事件: 按 reason+对象 分组, 求和 count, 取最近时间。
// 返回行含 type/object/reason/namespace/count/lastSeen, 字母序, 由前端按优先级再排。
func (m *Manager) AggregateEvents(ctx context.Context, clusterID string) ([]map[string]any, error) {
	dyn, err := m.DynamicClient(clusterID)
	if err != nil {
		return nil, err
	}
	list, err := dyn.Resource(gvrEvents).Namespace("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list events in %s: %w", clusterID, err)
	}
	now := time.Now()
	type key struct {
		t, r, ns, obj string
	}
	agg := map[key]*map[string]any{}
	var order []key
	for i := range list.Items {
		var e corev1.Event
		if runtime.DefaultUnstructuredConverter.FromUnstructured(list.Items[i].Object, &e) != nil {
			continue
		}
		k := key{e.Type, e.Reason, e.Namespace, e.InvolvedObject.Kind + "/" + e.InvolvedObject.Name}
		row, ok := agg[k]
		if !ok {
			last := ""
			if !e.LastTimestamp.IsZero() {
				last = humanAge(e.LastTimestamp.Time, now)
			}
			agg[k] = &map[string]any{
				"type": e.Type, "reason": e.Reason, "object": k.obj,
				"namespace": k.ns, "count": e.Count, "lastSeen": last,
			}
			order = append(order, k)
		} else {
			(*row)["count"] = (*row)["count"].(int32) + e.Count
			if !e.LastTimestamp.IsZero() {
				(*row)["lastSeen"] = humanAge(e.LastTimestamp.Time, now)
			}
		}
	}
	out := make([]map[string]any, 0, len(order))
	for _, k := range order {
		out = append(out, *agg[k])
	}
	return out, nil
}

func rowOf(it *unstructured.Unstructured, res string, now time.Time) (map[string]any, error) {
	name := it.GetName()
	ns := it.GetNamespace()
	age := humanAge(it.GetCreationTimestamp().Time, now)
	switch res {
	case "pods":
		var p corev1.Pod
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(it.Object, &p); err != nil {
			return nil, err
		}
		restarts := int32(0)
		for _, cs := range p.Status.ContainerStatuses {
			restarts += cs.RestartCount
		}
		return map[string]any{
			"name": name, "namespace": ns, "status": string(p.Status.Phase),
			"node": p.Spec.NodeName, "restarts": restarts, "ip": p.Status.PodIP, "age": age,
		}, nil
	case "deployments":
		var d appsv1.Deployment
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(it.Object, &d); err != nil {
			return nil, err
		}
		return map[string]any{
			"name": name, "namespace": ns,
			"ready":     fmt.Sprintf("%d/%d", d.Status.ReadyReplicas, d.Status.Replicas),
			"updated":   d.Status.UpdatedReplicas,
			"available": d.Status.AvailableReplicas,
			"age":       age,
		}, nil
	case "services":
		var s corev1.Service
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(it.Object, &s); err != nil {
			return nil, err
		}
		extIP := ""
		for _, ing := range s.Status.LoadBalancer.Ingress {
			if ing.IP != "" {
				extIP = ing.IP
				break
			}
			if ing.Hostname != "" {
				extIP = ing.Hostname
				break
			}
		}
		ports := make([]string, 0, len(s.Spec.Ports))
		for _, p := range s.Spec.Ports {
			ports = append(ports, fmt.Sprintf("%d/%s", p.Port, p.Protocol))
		}
		return map[string]any{
			"name": name, "namespace": ns, "type": string(s.Spec.Type),
			"clusterIP": s.Spec.ClusterIP, "externalIP": extIP,
			"ports": strings.Join(ports, ","), "age": age,
		}, nil
	case "configmaps":
		var cm corev1.ConfigMap
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(it.Object, &cm); err != nil {
			return nil, err
		}
		return map[string]any{"name": name, "namespace": ns, "dataCount": len(cm.Data), "age": age}, nil
	case "secrets":
		var sec corev1.Secret
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(it.Object, &sec); err != nil {
			return nil, err
		}
		return map[string]any{
			"name": name, "namespace": ns, "type": string(sec.Type),
			"dataCount": len(sec.Data), "age": age,
		}, nil
	case "nodes":
		var n corev1.Node
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(it.Object, &n); err != nil {
			return nil, err
		}
		status := "Unknown"
		internalIP := ""
		for _, c := range n.Status.Conditions {
			if c.Type == corev1.NodeReady {
				if c.Status == corev1.ConditionTrue {
					status = "Ready"
				} else {
					status = "NotReady"
				}
			}
		}
		for _, a := range n.Status.Addresses {
			if a.Type == corev1.NodeInternalIP {
				internalIP = a.Address
				break
			}
		}
		roles := make([]string, 0)
		for k := range n.Labels {
			if strings.HasPrefix(k, "node-role.kubernetes.io/") {
				// label 值可为空串(kubectl 同样按 key 存在性判定角色)
				roles = append(roles, strings.TrimPrefix(k, "node-role.kubernetes.io/"))
			}
		}
		sort.Strings(roles)
		return map[string]any{
			"name": name, "status": status, "roles": strings.Join(roles, ","),
			"version":    n.Status.NodeInfo.KubeletVersion,
			"osImage":    n.Status.NodeInfo.OSImage,
			"internalIP": internalIP, "age": age,
		}, nil
	case "statefulsets", "daemonsets":
		row, err := workloadRow(it.Object, res)
		return row, err
	case "jobs", "cronjobs":
		row, err := batchRow(it.Object, res)
		return row, err
	case "ingresses":
		row, err := ingressRow(it.Object)
		return row, err
	case "persistentvolumes", "persistentvolumeclaims":
		row, err := volumeRow(it.Object, res)
		return row, err
	case "storageclasses":
		row, err := storageClassRow(it.Object)
		return row, err
	case "networkpolicies":
		return map[string]any{
			"name": name, "namespace": ns, "age": age,
		}, nil
	case "serviceaccounts":
		var sa corev1.ServiceAccount
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(it.Object, &sa); err != nil {
			return nil, err
		}
		secrets := make([]string, 0, len(sa.Secrets))
		for _, s := range sa.Secrets {
			secrets = append(secrets, s.Name)
		}
		return map[string]any{
			"name": name, "namespace": ns,
			"secrets": strings.Join(secrets, ","), "age": age,
		}, nil
	case "roles", "clusterroles":
		return map[string]any{
			"name": name, "namespace": ns, "rules": rbacRulesCount(it.Object), "age": age,
		}, nil
	case "rolebindings", "clusterrolebindings":
		subjects, roleRef := rbacBindSummary(it.Object)
		return map[string]any{
			"name": name, "namespace": ns,
			"role": roleRef, "subjects": subjects, "age": age,
		}, nil
	case "resourcequotas":
		row, err := quotaRow(it.Object)
		return row, err
	case "events":
		var e corev1.Event
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(it.Object, &e); err != nil {
			return nil, err
		}
		last := ""
		if !e.LastTimestamp.IsZero() {
			last = humanAge(e.LastTimestamp.Time, now)
		}
		return map[string]any{
			"namespace": ns, "type": e.Type, "reason": e.Reason,
			"object":  e.InvolvedObject.Kind + "/" + e.InvolvedObject.Name,
			"message": e.Message, "count": e.Count, "lastSeen": last,
		}, nil
	case "namespaces":
		var nsc corev1.Namespace
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(it.Object, &nsc); err != nil {
			return nil, err
		}
		return map[string]any{"name": name, "status": string(nsc.Status.Phase), "age": age}, nil
	case "ingressclasses":
		controller, _, _ := unstructured.NestedString(it.Object, "spec", "controller")
		return map[string]any{"name": name, "controller": controller, "age": age}, nil
	}
	// CRD fallback: 返回 name/namespace/age + spec/status 关键字段 (label/keys/phase)
	return crdRow(it.Object, name, ns, age)
}

// crdRow 通用 CRD 行: 不预设字段, 通用展示 name/namespace/age + 几个常见 spec 字段 (尽量通用).
func crdRow(obj map[string]any, name, ns, age string) (map[string]any, error) {
	row := map[string]any{"name": name, "namespace": ns, "age": age}
	// 常见 spec 字段: spec.foo / spec.image / spec.replicas / spec.schedule / spec.phase
	for _, p := range []string{"phase", "replicas", "image", "schedule", "host", "url", "provider"} {
		if v, ok, _ := unstructured.NestedString(obj, "spec", p); ok {
			row[p] = v
		}
	}
	if v, ok, _ := unstructured.NestedInt64(obj, "spec", "replicas"); ok {
		row["replicas"] = v
	}
	// 状态字段: status.phase / status.ready
	for _, p := range []string{"phase", "ready", "state", "conditions"} {
		if v, ok, _ := unstructured.NestedString(obj, "status", p); ok {
			row["status_"+p] = v
		}
	}
	// 顶层 labels 数 / annotations 数
	if l, ok, _ := unstructured.NestedMap(obj, "metadata", "labels"); ok {
		row["labels"] = len(l)
	}
	return row, nil
}

// rbacRulesCount 统计 Role/ClusterRole 的 rules 条数。
func rbacRulesCount(obj map[string]any) int {
	rules, _, _ := unstructured.NestedSlice(obj, "rules")
	return len(rules)
}

// rbacBindSummary 汇总 RoleBinding/ClusterRoleBinding 的 roleRef 与 subjects。
func rbacBindSummary(obj map[string]any) (subjects, roleRef string) {
	if k, _, _ := unstructured.NestedString(obj, "roleRef", "name"); k != "" {
		kr, _, _ := unstructured.NestedString(obj, "roleRef", "kind")
		roleRef = kr + "/" + k
	}
	subs, _, _ := unstructured.NestedSlice(obj, "subjects")
	parts := make([]string, 0, len(subs))
	for _, s := range subs {
		m, ok := s.(map[string]any)
		if !ok {
			continue
		}
		k := strings.Builder{}
		kind, _ := m["kind"].(string)
		nm, _ := m["name"].(string)
		nsName, _ := m["namespace"].(string)
		k.WriteString(kind)
		k.WriteString(":")
		if kind == "ServiceAccount" && nsName != "" {
			k.WriteString(nsName)
			k.WriteString("/")
		}
		k.WriteString(nm)
		parts = append(parts, k.String())
	}
	return strings.Join(parts, ", "), roleRef
}

func sortRows(rows []map[string]any) {
	sort.Slice(rows, func(i, j int) bool {
		ni, _ := rows[i]["name"].(string)
		nj, _ := rows[j]["name"].(string)
		si, iok := rows[i]["namespace"].(string)
		sj, jok := rows[j]["namespace"].(string)
		if iok && jok && si != sj {
			return si < sj
		}
		return ni < nj
	})
}

// humanAge 输出 kubectl 风格时长: 45s / 34m / 5h / 12d。
func humanAge(from, to time.Time) string {
	d := to.Sub(from)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// ---- 扩展资源行提取 ----

// workloadRow 提取 StatefulSet/DaemonSet(与 Deployment 同构的 ready/desired 字段)。
func workloadRow(obj map[string]any, res string) (map[string]any, error) {
	switch res {
	case "statefulsets":
		var d appsv1.StatefulSet
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj, &d); err != nil {
			return nil, err
		}
		return map[string]any{
			"name": d.Name, "namespace": d.Namespace,
			"ready": fmt.Sprintf("%d/%d", d.Status.ReadyReplicas, d.Status.Replicas),
			"age":   humanAge(d.CreationTimestamp.Time, time.Now()),
		}, nil
	default:
		var d appsv1.DaemonSet
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj, &d); err != nil {
			return nil, err
		}
		return map[string]any{
			"name": d.Name, "namespace": d.Namespace,
			"ready":     fmt.Sprintf("%d/%d", d.Status.NumberReady, d.Status.DesiredNumberScheduled),
			"available": d.Status.NumberAvailable,
			"age":       humanAge(d.CreationTimestamp.Time, time.Now()),
		}, nil
	}
}

func batchRow(obj map[string]any, res string) (map[string]any, error) {
	if res == "jobs" {
		var j batchv1.Job
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj, &j); err != nil {
			return nil, err
		}
		status := "Running"
		if j.Status.Succeeded > 0 {
			status = "Complete"
		} else if j.Status.Failed > 0 {
			status = "Failed"
		}
		return map[string]any{
			"name": j.Name, "namespace": j.Namespace, "status": status,
			"succeeded": fmt.Sprintf("%d/%d", j.Status.Succeeded, ptrDeref(j.Spec.Completions)),
			"age":       humanAge(j.CreationTimestamp.Time, time.Now()),
		}, nil
	}
	var c batchv1.CronJob
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj, &c); err != nil {
		return nil, err
	}
	last := "—"
	if c.Status.LastScheduleTime != nil {
		last = humanAge(c.Status.LastScheduleTime.Time, time.Now())
	}
	return map[string]any{
		"name": c.Name, "namespace": c.Namespace, "schedule": c.Spec.Schedule,
		"active": len(c.Status.Active), "lastSchedule": last,
		"age": humanAge(c.CreationTimestamp.Time, time.Now()),
	}, nil
}

func ingressRow(obj map[string]any) (map[string]any, error) {
	var i netv1.Ingress
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj, &i); err != nil {
		return nil, err
	}
	class := i.Spec.IngressClassName
	host := ""
	if len(i.Spec.Rules) > 0 && i.Spec.Rules[0].Host != "" {
		host = i.Spec.Rules[0].Host
	}
	return map[string]any{
		"name": i.Name, "namespace": i.Namespace, "class": string(ptrStr(class)),
		"host": host, "age": humanAge(i.CreationTimestamp.Time, time.Now()),
	}, nil
}

func volumeRow(obj map[string]any, res string) (map[string]any, error) {
	if res == "persistentvolumes" {
		var v corev1.PersistentVolume
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj, &v); err != nil {
			return nil, err
		}
		return map[string]any{
			"name": v.Name, "capacity": v.Spec.Capacity[corev1.ResourceStorage],
			"accessModes": accessModes(v.Spec.AccessModes), "reclaim": string(v.Spec.PersistentVolumeReclaimPolicy),
			"status": v.Status.Phase, "claim": claimRef(v.Spec.ClaimRef),
			"age": humanAge(v.CreationTimestamp.Time, time.Now()),
		}, nil
	}
	var v corev1.PersistentVolumeClaim
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj, &v); err != nil {
		return nil, err
	}
	return map[string]any{
		"name": v.Name, "namespace": v.Namespace, "status": v.Status.Phase,
		"volume": v.Spec.VolumeName, "capacity": v.Status.Capacity[corev1.ResourceStorage],
		"age": humanAge(v.CreationTimestamp.Time, time.Now()),
	}, nil
}

func storageClassRow(obj map[string]any) (map[string]any, error) {
	var sc scv1.StorageClass
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj, &sc); err != nil {
		return nil, err
	}
	return map[string]any{
		"name": sc.Name, "provisioner": sc.Provisioner,
		"reclaim":     ptrStr((*string)(sc.ReclaimPolicy)),
		"bindingMode": ptrStr((*string)(sc.VolumeBindingMode)),
		"default":     sc.Annotations["storageclass.kubernetes.io/is-default-class"],
		"age":         humanAge(sc.CreationTimestamp.Time, time.Now()),
	}, nil
}

func quotaRow(obj map[string]any) (map[string]any, error) {
	var q corev1.ResourceQuota
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj, &q); err != nil {
		return nil, err
	}
	cpuReq := q.Status.Used[corev1.ResourceRequestsCPU]
	cpuLim := q.Status.Hard[corev1.ResourceRequestsCPU]
	memReq := q.Status.Used[corev1.ResourceRequestsMemory]
	memLim := q.Status.Hard[corev1.ResourceRequestsMemory]
	return map[string]any{
		"name": q.Name, "namespace": q.Namespace,
		"cpu":    fmt.Sprintf("%s / %s", cpuReq.String(), cpuLim.String()),
		"memory": fmt.Sprintf("%s / %s", memReq.String(), memLim.String()),
		"age":    humanAge(q.CreationTimestamp.Time, time.Now()),
	}, nil
}

func ptrDeref(p *int32) int32 {
	if p == nil {
		return 0
	}
	return *p
}

func ptrStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func accessModes(modes []corev1.PersistentVolumeAccessMode) string {
	out := make([]string, 0, len(modes))
	for _, m := range modes {
		out = append(out, string(m))
	}
	return strings.Join(out, ",")
}

func claimRef(ref *corev1.ObjectReference) string {
	if ref == nil {
		return ""
	}
	return ref.Namespace + "/" + ref.Name
}

// ---- Pod 日志与容器枚举 ----

// PodLogs 获取 Pod 日志(tail 行数上限防护由 handler 层做)。container 为空取默认容器。
func (m *Manager) PodLogs(ctx context.Context, clusterID, ns, name, container string, tailLines int64, previous bool) (string, error) {
	cfg, err := m.RESTConfig(clusterID)
	if err != nil {
		return "", err
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return "", fmt.Errorf("create clientset %q: %w", clusterID, err)
	}
	opts := &corev1.PodLogOptions{
		Container:  container,
		TailLines:  &tailLines,
		Timestamps: true,
		Previous:   previous,
	}
	stream, err := cs.CoreV1().Pods(ns).GetLogs(name, opts).Stream(ctx)
	if err != nil {
		return "", fmt.Errorf("pod logs %s/%s in %s: %w", ns, name, clusterID, err)
	}
	defer stream.Close()
	b, err := io.ReadAll(stream)
	if err != nil {
		return "", fmt.Errorf("read pod logs %s/%s: %w", ns, name, err)
	}
	return string(b), nil
}

// ListPodContainers 列出 Pod 内容器名(日志容器选择用); 失败返回空切片。
func (m *Manager) ListPodContainers(ctx context.Context, clusterID, ns, name string) []string {
	dyn, err := m.DynamicClient(clusterID)
	if err != nil {
		return nil
	}
	u, err := dyn.Resource(gvrPods).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil
	}
	var p corev1.Pod
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &p); err != nil {
		return nil
	}
	names := make([]string, 0, len(p.Spec.Containers))
	for _, c := range p.Spec.Containers {
		names = append(names, c.Name)
	}
	return names
}

// ---- 概览仪表盘 ----

// Overview 返回集群概览计数(pod 状态分布/工作负载/节点/事件等), 供仪表盘卡片渲染。
func (m *Manager) Overview(ctx context.Context, clusterID string) (map[string]any, error) {
	dyn, err := m.DynamicClient(clusterID)
	if err != nil {
		return nil, err
	}
	count := func(gvr schema.GroupVersionResource, ns string) int {
		l, err := dyn.Resource(gvr).Namespace(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return -1
		}
		return len(l.Items)
	}
	out := map[string]any{}
	pods, err := dyn.Resource(gvrPods).Namespace("").List(ctx, metav1.ListOptions{})
	if err == nil {
		running, pending, failed, succeeded := 0, 0, 0, 0
		for i := range pods.Items {
			switch pods.Items[i].Object["status"].(map[string]any)["phase"] {
			case "Running":
				running++
			case "Pending":
				pending++
			case "Failed":
				failed++
			case "Succeeded":
				succeeded++
			}
		}
		out["podsTotal"] = len(pods.Items)
		out["podsRunning"] = running
		out["podsPending"] = pending
		out["podsFailed"] = failed
		out["podsSucceeded"] = succeeded
	}
	nodes, err := dyn.Resource(gvrNodes).List(ctx, metav1.ListOptions{})
	if err == nil {
		ready := 0
		for i := range nodes.Items {
			var n corev1.Node
			if runtime.DefaultUnstructuredConverter.FromUnstructured(nodes.Items[i].Object, &n) == nil {
				for _, c := range n.Status.Conditions {
					if c.Type == corev1.NodeReady && c.Status == corev1.ConditionTrue {
						ready++
					}
				}
			}
		}
		out["nodesTotal"] = len(nodes.Items)
		out["nodesReady"] = ready
	}
	for res, key := range map[string]string{
		"deployments": "deployments", "statefulsets": "statefulsets",
		"daemonsets": "daemonsets", "services": "services",
		"ingresses": "ingresses", "namespaces": "namespaces",
	} {
		out[key] = count(gvrOf(res), "") // ns 空 = 全部作用域(集群级资源忽略该参数)
	}
	warnEvents, err := dyn.Resource(gvrEvents).Namespace("").List(ctx, metav1.ListOptions{
		FieldSelector: "type=Warning",
	})
	if err == nil {
		out["warningEvents"] = len(warnEvents.Items)
	}
	return out, nil
}

// ---- 资详情 / 写操作(带审计由 handler 层负责) ----

// PodDetail 返回 Pod 结构化详情: 容器状态 / 条件 / 标签 / 相关事件。
func (m *Manager) PodDetail(ctx context.Context, clusterID, ns, name string) (map[string]any, error) {
	dyn, err := m.DynamicClient(clusterID)
	if err != nil {
		return nil, err
	}
	u, err := dyn.Resource(gvrPods).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get pod %s/%s: %w", ns, name, err)
	}
	var p corev1.Pod
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &p); err != nil {
		return nil, err
	}
	now := time.Now()
	containers := make([]map[string]any, 0, len(p.Status.ContainerStatuses))
	for _, cs := range p.Status.ContainerStatuses {
		state := "waiting"
		detail := ""
		if cs.State.Running != nil {
			state = "running"
			detail = humanAge(cs.State.Running.StartedAt.Time, now)
		} else if cs.State.Terminated != nil {
			state = "terminated"
			detail = fmt.Sprintf("exit=%d reason=%s", cs.State.Terminated.ExitCode, cs.State.Terminated.Reason)
		} else if cs.State.Waiting != nil {
			detail = cs.State.Waiting.Reason
		}
		idx := findContainerIdx(p.Spec.Containers, cs.Name)
		c := p.Spec.Containers[idx]
		// 资源限制可视化(拼接)
		resSummary := formatResources(c.Resources)
		// env/envFrom 可视化
		envKeys := make([]string, 0)
		for _, e := range c.Env { envKeys = append(envKeys, e.Name) }
		for _, ef := range c.EnvFrom { if ef.ConfigMapRef != nil { envKeys = append(envKeys, "from:cm/"+ef.ConfigMapRef.Name) } else if ef.SecretRef != nil { envKeys = append(envKeys, "from:secret/"+ef.SecretRef.Name) } }
		// volumeMounts 可视化
		mounts := make([]string, 0, len(c.VolumeMounts))
		for _, vm := range c.VolumeMounts { mounts = append(mounts, fmt.Sprintf("%s→%s", vm.Name, vm.MountPath)) }
		probe := func(pr *corev1.Probe) string {
			if pr == nil {
				return "—"
			}
			if pr.HTTPGet != nil {
				return fmt.Sprintf("HTTP %s:%d%s", pr.HTTPGet.Path, pr.HTTPGet.Port.IntVal, "")
			}
			if pr.Exec != nil {
				return "exec"
			}
			return fmt.Sprintf("%ds/间隔%ds", pr.InitialDelaySeconds, pr.PeriodSeconds)
		}
		containers = append(containers, map[string]any{
			"name": cs.Name, "image": cs.Image, "ready": cs.Ready,
			"restarts": cs.RestartCount, "state": state, "stateDetail": detail,
			"liveness": probe(c.LivenessProbe),
			"readiness": probe(c.ReadinessProbe),
			"resources": resSummary, "env": envKeys, "mounts": mounts,
			"ports":    containerPorts(c.Ports),
		})
	}
	conditions := make([]map[string]any, 0, len(p.Status.Conditions))
	for _, c := range p.Status.Conditions {
		conditions = append(conditions, map[string]any{
			"type": string(c.Type), "status": string(c.Status),
			"reason": c.Reason, "age": humanAge(c.LastTransitionTime.Time, now),
		})
	}
	labels := make([]map[string]string, 0, len(p.Labels))
	for k, v := range p.Labels {
		labels = append(labels, map[string]string{"key": k, "value": v})
	}
	sort.Slice(labels, func(i, j int) bool { return labels[i]["key"] < labels[j]["key"] })
	events, _ := dyn.Resource(gvrEvents).Namespace(ns).List(ctx, metav1.ListOptions{
		FieldSelector: "involvedObject.name=" + name,
	})
	evRows := make([]map[string]any, 0, 10)
	if events != nil {
		for i := range events.Items {
			var e corev1.Event
			if runtime.DefaultUnstructuredConverter.FromUnstructured(events.Items[i].Object, &e) == nil {
				evRows = append(evRows, map[string]any{
					"type": e.Type, "reason": e.Reason, "message": e.Message,
					"count": e.Count, "lastSeen": humanAge(e.LastTimestamp.Time, now),
				})
			}
		}
	}
	qos := p.Status.QOSClass
	// 调度相关摘要(人类可读)
	tols := make([]string, 0, len(p.Spec.Tolerations))
	for _, t := range p.Spec.Tolerations { tols = append(tols, fmt.Sprintf("%s:%s/%s", t.Key, t.Operator, t.Effect)) }
	vols := make([]map[string]any, 0, len(p.Spec.Volumes))
	for _, v := range p.Spec.Volumes {
		src := "emptyDir"
		switch {
		case v.ConfigMap != nil:
			src = "cm:" + v.ConfigMap.Name
		case v.Secret != nil:
			src = "secret:" + v.Secret.SecretName
		case v.PersistentVolumeClaim != nil:
			src = "pvc:" + v.PersistentVolumeClaim.ClaimName
		case v.HostPath != nil:
			src = "hostPath:" + v.HostPath.Path
		}
		vols = append(vols, map[string]any{"name": v.Name, "source": src})
	}
	return map[string]any{
		"name": p.Name, "namespace": p.Namespace, "node": p.Spec.NodeName,
		"ip": p.Status.PodIP, "hostIP": p.Status.HostIP, "phase": string(p.Status.Phase), "qos": string(qos),
		"scheduler":      p.Spec.SchedulerName,
		"serviceAccount": p.Spec.ServiceAccountName,
		"restartPolicy":  string(p.Spec.RestartPolicy),
		"hostNetwork":    p.Spec.HostNetwork,
		"createdAt":      humanAge(p.CreationTimestamp.Time, now),
		"labels":         labels,
		"containers":     containers,
		"conditions":     conditions,
		"events":         evRows,
		"nodeSelector":   p.Spec.NodeSelector,
		"tolerations":    tols,
		"affinitySummary": affinitySummary(p.Spec.Affinity),
		"volumes":        vols,
	}, nil
}

func findContainerIdx(cs []corev1.Container, name string) int {
	for i, c := range cs {
		if c.Name == name {
			return i
		}
	}
	return 0
}

func containerPorts(ps []corev1.ContainerPort) string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, fmt.Sprintf("%d/%s", p.ContainerPort, p.Protocol))
	}
	return strings.Join(out, ", ")
}

func formatResources(r corev1.ResourceRequirements) string {
	parts := []string{}
	if v, ok := r.Requests[corev1.ResourceCPU]; ok { parts = append(parts, "req:"+v.String()) }
	if v, ok := r.Requests[corev1.ResourceMemory]; ok { parts = append(parts, "req:"+v.String()) }
	if v, ok := r.Limits[corev1.ResourceCPU]; ok { parts = append(parts, "lim:"+v.String()) }
	if v, ok := r.Limits[corev1.ResourceMemory]; ok { parts = append(parts, "lim:"+v.String()) }
	if len(parts) == 0 { return "未限制" }
	return strings.Join(parts, " · ")
}

func affinitySummary(a *corev1.Affinity) string {
	if a == nil { return "—" }
	if a.PodAntiAffinity != nil && len(a.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution) > 0 {
		return "反亲和(分散)"
	}
	if a.NodeAffinity != nil { return "节点亲和已配置" }
	return "已配置"
}

// GetResourceYAML 返回资源对象 YAML(敏感字段脱敏: Secret 的 data)。
func (m *Manager) GetResourceYAML(ctx context.Context, clusterID, res, ns, name string) (string, error) {
	gvr := gvrOf(res)
	effectiveNs := nsFor(ns, res)
	if gvr.Empty() {
		crdGVR, scope, err := m.ResolveGVR(clusterID, res)
		if err != nil {
			return "", fmt.Errorf("unsupported resource %q", res)
		}
		gvr = crdGVR
		if scope == ScopeCluster {
			effectiveNs = ""
		}
	}
	dyn, err := m.DynamicClient(clusterID)
	if err != nil {
		return "", err
	}
	u, err := dyn.Resource(gvr).Namespace(effectiveNs).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	obj := u.Object
	// 与 kubectl get -o yaml 默认行为一致: 剥离 managedFields 记账元数据(f:/k: 噪音)
	if meta, ok := obj["metadata"].(map[string]any); ok {
		delete(meta, "managedFields")
	}
	// 脱敏: secrets 的 data 值替换为长度提示
	if res == "secrets" {
		if data, ok := obj["data"].(map[string]any); ok {
			masked := map[string]any{}
			for k, v := range data {
				if s, ok := v.(string); ok {
					masked[k] = fmt.Sprintf("**%d bytes**", len(s))
				}
			}
			obj["data"] = masked
		}
		delete(obj, "immutable")
	}
	out, err := sigsyaml.Marshal(obj)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// kindToRes: 可视化创建(apply)允许的 kind → 资源名映射。
var kindToRes = map[string]string{
	"Deployment":            "deployments",
	"StatefulSet":           "statefulsets",
	"DaemonSet":             "daemonsets",
	"Service":               "services",
	"ConfigMap":             "configmaps",
	"Secret":                "secrets",
	"CronJob":               "cronjobs",
	"Job":                   "jobs",
	"PersistentVolumeClaim": "persistentvolumeclaims",
	"Ingress":               "ingresses",
	"ServiceAccount":        "serviceaccounts",
	"Role":                  "roles",
	"ClusterRole":           "clusterroles",
	"RoleBinding":           "rolebindings",
	"ClusterRoleBinding":    "clusterrolebindings",
	"Namespace":             "namespaces",
}

// ApplyResourceYAML 创建(或覆盖更新)单个资源对象。
// 返回 (kind, name, created, error)。overwrite=false 时同名资源报错, 避免误覆盖。
func (m *Manager) ApplyResourceYAML(ctx context.Context, clusterID, yamlStr string, overwrite bool) (string, string, bool, error) {
	var obj map[string]any
	if err := sigsyaml.Unmarshal([]byte(yamlStr), &obj); err != nil {
		return "", "", false, fmt.Errorf("YAML 解析失败: %w", err)
	}
	kind, _ := obj["kind"].(string)
	meta, _ := obj["metadata"].(map[string]any)
	name, _ := meta["name"].(string)
	if kind == "" || name == "" {
		return "", "", false, fmt.Errorf("缺少 kind 或 metadata.name")
	}
	res, ok := kindToRes[kind]
	if !ok {
		return "", "", false, fmt.Errorf("暂不支持创建类型 %q (支持: Deployment/StatefulSet/DaemonSet/Service/ConfigMap/Secret/CronJob/Job/PVC/Ingress)", kind)
	}
	if kind == "Secret" {
		// type 字段缺失时 apiserver 默认 Opaque, 无需补; 但 stringData/data 都空时报错提醒
		data, _ := obj["data"].(map[string]any)
		sdata, _ := obj["stringData"].(map[string]any)
		if len(data) == 0 && len(sdata) == 0 {
			return "", "", false, fmt.Errorf("Secret 至少需要一条 data 或 stringData")
		}
	}
	dyn, err := m.DynamicClient(clusterID)
	if err != nil {
		return "", "", false, err
	}
	gvr := gvrOf(res)
	ns := nsFor(nsForMeta(meta), res)
	u := &unstructured.Unstructured{Object: obj}
	existing, getErr := dyn.Resource(gvr).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	switch {
	case getErr == nil:
		if !overwrite {
			return kind, name, false, fmt.Errorf("%s %q 已存在(勾选覆盖可更新)", kind, name)
		}
		u.SetResourceVersion(existing.GetResourceVersion())
		_, uerr := dyn.Resource(gvr).Namespace(ns).Update(ctx, u, metav1.UpdateOptions{})
		if uerr != nil {
			return kind, name, false, fmt.Errorf("更新失败: %w", uerr)
		}
		return kind, name, false, nil
	default:
		if !apierrors.IsNotFound(getErr) {
			return "", "", false, getErr
		}
		_, uerr := dyn.Resource(gvr).Namespace(ns).Create(ctx, u, metav1.CreateOptions{})
		if uerr != nil {
			return kind, name, true, fmt.Errorf("创建失败: %w", uerr)
		}
		return kind, name, true, nil
	}
}

// nsForMeta 从对象 metadata.namespace 取命名空间(缺省 default)。
func nsForMeta(meta map[string]any) string {
	if v, ok := meta["namespace"].(string); ok && v != "" {
		return v
	}
	return "default"
}

// DeleteResource 删除指定资源(内置白名单 + 集群已发现 CRD).
// deletableResources 是内置安全白名单, CRD 走兜底 (gvrOf 空即尝试 ResolveGVR).
var deletableResources = map[string]bool{
	"pods": true, "deployments": true, "statefulsets": true, "jobs": true, "cronjobs": true,
	"services": true, "configmaps": true, "ingresses": true, "namespaces": true,
	"serviceaccounts": true, "roles": true, "rolebindings": true,
	"clusterroles": true, "clusterrolebindings": true,
}

func (m *Manager) DeleteResource(ctx context.Context, clusterID, res, ns, name string, force bool) error {
	// 内置白名单 OR CRD 短名 (ResolveGVR 命中即允许)
	if !deletableResources[res] {
		if _, _, err := m.ResolveGVR(clusterID, res); err != nil {
			return fmt.Errorf("资源类型 %q 不允许删除", res)
		}
	}
	gvr := gvrOf(res)
	effectiveNs := nsFor(ns, res)
	if gvr.Empty() {
		crdGVR, scope, err := m.ResolveGVR(clusterID, res)
		if err != nil {
			return fmt.Errorf("资源类型 %q 不允许删除 (未发现 CRD)", res)
		}
		gvr = crdGVR
		if scope == ScopeCluster {
			effectiveNs = ""
		}
	}
	dyn, err := m.DynamicClient(clusterID)
	if err != nil {
		return err
	}
	opts := metav1.DeleteOptions{}
	if force {
		z := int64(0)
		opts.GracePeriodSeconds = &z
	}
	return dyn.Resource(gvr).Namespace(effectiveNs).Delete(ctx, name, opts)
}

// ScaleWorkload 调整 deployments/statefulsets 副本数(经 scale 子资源)。
func (m *Manager) ScaleWorkload(ctx context.Context, clusterID, res, ns, name string, replicas int32) error {
	if res != "deployments" && res != "statefulsets" {
		return fmt.Errorf("仅支持 deployments/statefulsets 扩缩容")
	}
	dyn, err := m.DynamicClient(clusterID)
	if err != nil {
		return err
	}
	sc, err := dyn.Resource(gvrOf(res)).Namespace(ns).Get(ctx, name, metav1.GetOptions{}, "scale")
	if err != nil {
		return fmt.Errorf("get scale %s/%s: %w", ns, name, err)
	}
	spec, ok := sc.Object["spec"].(map[string]any)
	if !ok {
		spec = map[string]any{}
		sc.Object["spec"] = spec
	}
	spec["replicas"] = replicas
	_, err = dyn.Resource(gvrOf(res)).Namespace(ns).Update(ctx, sc, metav1.UpdateOptions{}, "scale")
	return err
}

// RestartWorkload 滚动重启 deployments/statefulsets(打 restartedAt 注解触发滚动)。
func (m *Manager) RestartWorkload(ctx context.Context, clusterID, res, ns, name string) error {
	if res != "deployments" && res != "statefulsets" {
		return fmt.Errorf("仅支持 deployments/statefulsets 滚动重启")
	}
	dyn, err := m.DynamicClient(clusterID)
	if err != nil {
		return err
	}
	u, err := dyn.Resource(gvrOf(res)).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	ann := u.GetAnnotations()
	if ann == nil {
		ann = map[string]string{}
	}
	ann["kubectl.kubernetes.io/restartedAt"] = time.Now().Format(time.RFC3339)
	u.SetAnnotations(ann)
	_, err = dyn.Resource(gvrOf(res)).Namespace(ns).Update(ctx, u, metav1.UpdateOptions{})
	return err
}

// GetReplicas 查询 deployments/statefulsets 当前期望副本与就绪副本(scale 子资源)。
func (m *Manager) GetReplicas(ctx context.Context, clusterID, res, ns, name string) (spec int32, ready int64, err error) {
	if res != "deployments" && res != "statefulsets" {
		return 0, 0, fmt.Errorf("仅支持 deployments/statefulsets")
	}
	dyn, err := m.DynamicClient(clusterID)
	if err != nil {
		return 0, 0, err
	}
	sc, err := dyn.Resource(gvrOf(res)).Namespace(ns).Get(ctx, name, metav1.GetOptions{}, "scale")
	if err != nil {
		return 0, 0, err
	}
	if v, ok := sc.Object["spec"].(map[string]any)["replicas"].(int64); ok {
		spec = int32(v)
	}
	st, _ := sc.Object["status"].(map[string]any)
	ready, _ = st["replicas"].(int64)
	return spec, ready, nil
}

// DescribeResource 生成 kubectl describe 风格的只读文本: 对象 YAML + 关联事件。
func (m *Manager) DescribeResource(ctx context.Context, clusterID, res, ns, name string) (string, error) {
	if res == "overview" || res == "events" || res == "namespaces" {
		return "", fmt.Errorf("unsupported resource %q", res)
	}
	gvr := gvrOf(res)
	effNs := nsFor(ns, res)
	if gvr.Empty() {
		crdGVR, scope, err := m.ResolveGVR(clusterID, res)
		if err != nil {
			return "", fmt.Errorf("unsupported resource %q", res)
		}
		gvr = crdGVR
		if scope == ScopeCluster {
			effNs = ""
		}
	}
	dyn, err := m.DynamicClient(clusterID)
	if err != nil {
		return "", err
	}
	u, err := dyn.Resource(gvr).Namespace(effNs).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("get %s %s/%s: %w", res, effNs, name, err)
	}
	var b strings.Builder

	// ---- 头部 ----
	kind, _ := u.Object["kind"].(string)
	apiVer, _ := u.Object["apiVersion"].(string)
	fmt.Fprintf(&b, "Name:         %s\n", name)
	fmt.Fprintf(&b, "Namespace:    %s\n", effNs)
	fmt.Fprintf(&b, "Kind:         %s\n", kind)
	if apiVer != "" {
		fmt.Fprintf(&b, "APIVersion:   %s/%s\n", groupOf(kind), apiVer)
	}
	if l := u.GetLabels(); len(l) > 0 {
		fmt.Fprintf(&b, "Labels:       %s\n", labelsInline(l))
	}
	if a := u.GetAnnotations(); len(a) > 0 {
		fmt.Fprintf(&b, "Annotations:  %s\n", labelsInline(a))
	}
	fmt.Fprintf(&b, "Created:      %s\n", u.GetCreationTimestamp().Format(time.RFC3339))

	// ---- Pod 专属 ----
	if res == "pods" {
		var p corev1.Pod
		if runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &p) == nil {
			describePod(&b, &p)
		}
	}

	// ---- 对象 YAML ----
	obj := u.Object
	if meta, ok := obj["metadata"].(map[string]any); ok {
		delete(meta, "managedFields")
	}
	if res == "secrets" {
		if data, ok := obj["data"].(map[string]any); ok {
			masked := map[string]any{}
			for k, v := range data {
				if s, ok := v.(string); ok {
					masked[k] = fmt.Sprintf("**%d bytes**", len(s))
				}
			}
			obj["data"] = masked
		}
		delete(obj, "immutable")
	}
	fmt.Fprintf(&b, "\n---- Manifest ----\n")
	if y, err := sigsyaml.Marshal(obj); err == nil {
		b.WriteString(string(y))
	}

	// ---- 事件 ----
	sel := "involvedObject.name=" + name
	if strings.TrimSpace(ns) != "" {
		sel = "involvedObject.namespace=" + ns
	}
	evs, err := dyn.Resource(gvrEvents).Namespace(effNs).List(ctx, metav1.ListOptions{FieldSelector: sel})
	if err == nil {
		fmt.Fprintf(&b, "\n---- Events (%d) ----\n", len(evs.Items))
		if len(evs.Items) == 0 {
			fmt.Fprintf(&b, "  <none>\n")
		} else {
			sort.Slice(evs.Items, func(i, j int) bool {
				ti, _ := evs.Items[i].Object["lastTimestamp"].(string)
				tj, _ := evs.Items[j].Object["lastTimestamp"].(string)
				return ti > tj
			})
			fmt.Fprintf(&b, "  Type    Reason              Age   Message\n")
			for i := range evs.Items {
				o := evs.Items[i].Object
				typ, _ := o["type"].(string)
				reason, _ := o["reason"].(string)
				msg, _ := o["message"].(string)
				count, _ := o["count"].(int64)
				last, _ := o["lastTimestamp"].(string)
				age := humanAgeString(last)
				marker := "  "
				if typ == "Warning" {
					marker = "! "
				}
				fmt.Fprintf(&b, "%s%-6s %-20s %-6s %dx %s\n", marker, typ, reason, age, count, msg)
			}
		}
	}
	return b.String(), nil
}

// describePod 输出 kubectl describe pod 风格的容器/条件/卷诊断文本。
func describePod(b *strings.Builder, p *corev1.Pod) {
	now := time.Now()
	fmt.Fprintf(b, "Status:       %s\n", p.Status.Phase)
	if p.Spec.NodeName != "" {
		fmt.Fprintf(b, "Node:         %s\n", p.Spec.NodeName)
	}
	if p.Status.PodIP != "" {
		fmt.Fprintf(b, "IP:           %s\n", p.Status.PodIP)
	}
	if p.Status.HostIP != "" {
		fmt.Fprintf(b, "HostIP:       %s\n", p.Status.HostIP)
	}
	if q := p.Status.QOSClass; q != "" {
		fmt.Fprintf(b, "QoS Class:    %s\n", q)
	}
	if len(p.Status.InitContainerStatuses) > 0 {
		fmt.Fprintf(b, "\nInit Containers:\n")
		for _, cs := range p.Status.InitContainerStatuses {
			fmt.Fprintf(b, "  %-20s image=%s\n", cs.Name, cs.Image)
			fmt.Fprintf(b, "    State: %s\n", containerStateText(cs.State, now))
			fmt.Fprintf(b, "    Ready: %v  RestartCount: %d\n", cs.Ready, cs.RestartCount)
		}
	}
	fmt.Fprintf(b, "\nContainers:\n")
	for _, cs := range p.Status.ContainerStatuses {
		fmt.Fprintf(b, "  %-20s image=%s\n", cs.Name, cs.Image)
		fmt.Fprintf(b, "    State: %s\n", containerStateText(cs.State, now))
		fmt.Fprintf(b, "    Ready: %v  RestartCount: %d\n", cs.Ready, cs.RestartCount)
	}
	if len(p.Status.Conditions) > 0 {
		fmt.Fprintf(b, "\nConditions:\n")
		fmt.Fprintf(b, "  Type             Status  Reason            Message\n")
		for _, c := range p.Status.Conditions {
			fmt.Fprintf(b, "  %-16s %-7s %-18s %s\n", string(c.Type), string(c.Status), c.Reason, c.Message)
		}
	}
	if len(p.Spec.Volumes) > 0 {
		fmt.Fprintf(b, "\nVolumes:\n")
		for _, v := range p.Spec.Volumes {
			src := "emptyDir"
			switch {
			case v.ConfigMap != nil:
				src = "configmap/" + v.ConfigMap.Name
			case v.Secret != nil:
				src = "secret/" + v.Secret.SecretName
			case v.PersistentVolumeClaim != nil:
				src = "pvc/" + v.PersistentVolumeClaim.ClaimName
			case v.HostPath != nil:
				src = "hostPath:" + v.HostPath.Path
			}
			fmt.Fprintf(b, "  %s: %s\n", v.Name, src)
		}
	}
}

// containerStateText 人类可读的容器状态(kubectl describe 风格), 含等待原因。
func containerStateText(s corev1.ContainerState, now time.Time) string {
	if s.Running != nil {
		return "Running (started " + humanAge(s.Running.StartedAt.Time, now) + " ago)"
	}
	if s.Terminated != nil {
		return fmt.Sprintf("Terminated (exit=%d, reason=%s, started %s, finished %s)",
			s.Terminated.ExitCode, orDash(s.Terminated.Reason),
			s.Terminated.StartedAt.Time.Format("15:04:05"), s.Terminated.FinishedAt.Time.Format("15:04:05"))
	}
	if s.Waiting != nil {
		// 突出常见故障原因
		reason := s.Waiting.Reason
		if reason == "" {
			reason = "Wait"
		}
		return fmt.Sprintf("Waiting (%s)", reason)
	}
	return "Waiting"
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func groupOf(kind string) string {
	switch kind {
	case "Deployment", "StatefulSet", "DaemonSet", "ReplicaSet":
		return "apps"
	case "Job", "CronJob":
		return "batch"
	case "Ingress", "NetworkPolicy":
		return "networking.k8s.io"
	}
	return "v1"
}

func labelsInline(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+m[k])
	}
	return strings.Join(parts, ", ")
}

func humanAgeString(ts string) string {
	if ts == "" {
		return "?"
	}
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ts
	}
	return humanAge(t, time.Now())
}
