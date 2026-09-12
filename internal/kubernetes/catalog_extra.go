package kubernetes

// 新增 Manager 方法: taint / set-env / set-resources / set-sa / expose / wait / create-*

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
)

// ──────────────────────── taint ────────────────────────

func (m *Manager) NodeTaintAdd(ctx context.Context, cluster, node, key, effect, value string) error {
	if effect == "" {
		effect = "NoSchedule"
	}
	dyn, err := m.DynamicClient(cluster)
	if err != nil {
		return err
	}
	u, err := dyn.Resource(gvrNodes).Get(ctx, node, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("获取节点失败: %w", err)
	}
	spec, _ := u.Object["spec"].(map[string]any)
	if spec == nil {
		spec = map[string]any{}
		u.Object["spec"] = spec
	}
	taints, _ := spec["taints"].([]any)
	if taints == nil {
		taints = []any{}
	}
	// 检查是否已存在完全相同的 taint(key+effect+value)
	for _, t := range taints {
		tm, _ := t.(map[string]any)
		if tm == nil {
			continue
		}
		if tm["key"] == key && tm["effect"] == effect && tm["value"] == value {
			return fmt.Errorf("taint %s=%s:%s 已存在", key, value, effect)
		}
	}
	newTaint := map[string]any{"key": key, "effect": effect}
	if value != "" {
		newTaint["value"] = value
	}
	taints = append(taints, newTaint)
	spec["taints"] = taints
	_, err = dyn.Resource(gvrNodes).Update(ctx, u, metav1.UpdateOptions{})
	return err
}

func (m *Manager) NodeTaintRemove(ctx context.Context, cluster, node, key, effect string) error {
	dyn, err := m.DynamicClient(cluster)
	if err != nil {
		return err
	}
	u, err := dyn.Resource(gvrNodes).Get(ctx, node, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("获取节点失败: %w", err)
	}
	spec, _ := u.Object["spec"].(map[string]any)
	if spec == nil {
		return fmt.Errorf("节点无 spec")
	}
	taints, _ := spec["taints"].([]any)
	var filtered []any
	removed := 0
	for _, t := range taints {
		tm, _ := t.(map[string]any)
		if tm == nil {
			filtered = append(filtered, t)
			continue
		}
		if tm["key"] == key && (effect == "" || tm["effect"] == effect) {
			removed++
			continue
		}
		filtered = append(filtered, t)
	}
	if removed == 0 {
		return fmt.Errorf("未找到匹配的 taint (key=%s, effect=%s)", key, effect)
	}
	spec["taints"] = filtered
	_, err = dyn.Resource(gvrNodes).Update(ctx, u, metav1.UpdateOptions{})
	return err
}

// ──────────────────── set-env / set-resources / set-sa ────────────────────

func (m *Manager) SetWorkloadEnv(ctx context.Context, cluster, res, ns, name, key, value string) error {
	u, err := m.getWorkload(ctx, cluster, res, ns, name)
	if err != nil {
		return err
	}
	for _, cpath := range []string{"containers", "initContainers"} {
		containers, found, _ := unstructured.NestedSlice(u.Object, "spec", "template", "spec", cpath)
		if !found {
			continue
		}
		changed := false
		for i, c := range containers {
			cm, ok := c.(map[string]any)
			if !ok {
				continue
			}
			envs, _ := cm["env"].([]any)
			if envs == nil {
				envs = []any{}
			}
			updated := false
			for j, e := range envs {
				em, ok := e.(map[string]any)
				if !ok {
					continue
				}
				if em["name"] == key {
					envs[j] = map[string]any{"name": key, "value": value}
					updated = true
					break
				}
			}
			if !updated {
				envs = append(envs, map[string]any{"name": key, "value": value})
			}
			cm["env"] = envs
			containers[i] = cm
			changed = true
		}
		if changed {
			_ = unstructured.SetNestedSlice(u.Object, containers, "spec", "template", "spec", cpath)
		}
	}
	return m.updateWorkload(ctx, cluster, res, ns, name, u)
}

func (m *Manager) SetWorkloadResources(ctx context.Context, cluster, res, ns, name, cpuReq, cpuLim, memReq, memLim string) error {
	if cpuReq == "" && cpuLim == "" && memReq == "" && memLim == "" {
		return fmt.Errorf("至少提供一个资源限制值")
	}
	u, err := m.getWorkload(ctx, cluster, res, ns, name)
	if err != nil {
		return err
	}
	containers, found, _ := unstructured.NestedSlice(u.Object, "spec", "template", "spec", "containers")
	if !found || len(containers) == 0 {
		return fmt.Errorf("未找到容器")
	}
	for i, c := range containers {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		resMap, _ := cm["resources"].(map[string]any)
		if resMap == nil {
			resMap = map[string]any{}
		}
		reqs, _ := resMap["requests"].(map[string]any)
		if reqs == nil {
			reqs = map[string]any{}
		}
		lims, _ := resMap["limits"].(map[string]any)
		if lims == nil {
			lims = map[string]any{}
		}
		if cpuReq != "" {
			reqs["cpu"] = cpuReq
		}
		if memReq != "" {
			reqs["memory"] = memReq
		}
		if cpuLim != "" {
			lims["cpu"] = cpuLim
		}
		if memLim != "" {
			lims["memory"] = memLim
		}
		if len(reqs) > 0 {
			resMap["requests"] = reqs
		}
		if len(lims) > 0 {
			resMap["limits"] = lims
		}
		cm["resources"] = resMap
		containers[i] = cm
	}
	_ = unstructured.SetNestedSlice(u.Object, containers, "spec", "template", "spec", "containers")
	return m.updateWorkload(ctx, cluster, res, ns, name, u)
}

func (m *Manager) SetWorkloadSA(ctx context.Context, cluster, res, ns, name, sa string) error {
	u, err := m.getWorkload(ctx, cluster, res, ns, name)
	if err != nil {
		return err
	}
	err = unstructured.SetNestedField(u.Object, sa, "spec", "template", "spec", "serviceAccountName")
	if err != nil {
		return err
	}
	return m.updateWorkload(ctx, cluster, res, ns, name, u)
}

func (m *Manager) getWorkload(ctx context.Context, cluster, res, ns, name string) (*unstructured.Unstructured, error) {
	dyn, err := m.DynamicClient(cluster)
	if err != nil {
		return nil, err
	}
	u, err := dyn.Resource(gvrOf(res)).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("获取 %s/%s 失败: %w", res, name, err)
	}
	return u, nil
}

func (m *Manager) updateWorkload(ctx context.Context, cluster, res, ns, name string, u *unstructured.Unstructured) error {
	dyn, err := m.DynamicClient(cluster)
	if err != nil {
		return err
	}
	_, err = dyn.Resource(gvrOf(res)).Namespace(ns).Update(ctx, u, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("更新 %s/%s 失败: %w", res, name, err)
	}
	return nil
}

// workloadSelectorLabels 获取 workload 的 spec.selector.matchLabels
func (m *Manager) workloadSelectorLabels(ctx context.Context, cluster, res, ns, name string) (map[string]string, error) {
	u, err := m.getWorkload(ctx, cluster, res, ns, name)
	if err != nil {
		return nil, err
	}
	labels, found, _ := unstructured.NestedStringMap(u.Object, "spec", "selector", "matchLabels")
	if !found || len(labels) == 0 {
		return nil, fmt.Errorf("未找到 selector.matchLabels")
	}
	return labels, nil
}

// workloadContainerPorts 获取第一个容器的 ports 列表
func (m *Manager) workloadContainerPorts(ctx context.Context, cluster, res, ns, name string) []int32 {
	u, err := m.getWorkload(ctx, cluster, res, ns, name)
	if err != nil {
		return nil
	}
	containers, _, _ := unstructured.NestedSlice(u.Object, "spec", "template", "spec", "containers")
	if len(containers) == 0 {
		return nil
	}
	cm, ok := containers[0].(map[string]any)
	if !ok {
		return nil
	}
	ports, _ := cm["ports"].([]any)
	var out []int32
	for _, p := range ports {
		pm, ok := p.(map[string]any)
		if !ok {
			continue
		}
		if n, ok := pm["containerPort"].(float64); ok {
			out = append(out, int32(n))
		}
	}
	return out
}

func workloadKind(res string) string {
	switch res {
	case "deployments":
		return "Deployment"
	case "statefulsets":
		return "StatefulSet"
	case "daemonsets":
		return "DaemonSet"
	}
	return ""
}

// ──────────────────── 创建类操作 ────────────────────

func (m *Manager) CreateHPA(ctx context.Context, cluster, ns, name, targetRes, targetName string, min, max int, cpuPercent int) error {
	if cpuPercent <= 0 || cpuPercent > 1000 {
		cpuPercent = 80
	}
	tKind := workloadKind(targetRes)
	if tKind == "" {
		return fmt.Errorf("仅支持 deployments/statefulsets/daemonsets 自动扩缩")
	}
	obj := map[string]any{
		"apiVersion": "autoscaling/v2",
		"kind":       "HorizontalPodAutoscaler",
		"metadata":   map[string]any{"name": name, "namespace": ns},
		"spec": map[string]any{
			"scaleTargetRef": map[string]any{
				"apiVersion": "apps/v1",
				"kind":       tKind,
				"name":       targetName,
			},
			"minReplicas": int32(min),
			"maxReplicas": int32(max),
			"metrics": []any{map[string]any{
				"type": "Resource",
				"resource": map[string]any{
					"name": "cpu",
					"target": map[string]any{
						"type":               "Utilization",
						"averageUtilization": int32(cpuPercent),
					},
				},
			}},
		},
	}
	_, _, _, err := m.UpsertObject(ctx, cluster, obj, true)
	return err
}

// UpdateHPA 更新已有 HPA: min/max + 扩容规则(CPU/内存)。样例可视化编辑的后端执行。
func (m *Manager) UpdateHPA(ctx context.Context, cluster, ns, name string, params map[string]any) error {
	spec, err := buildHPASpecPatch(params)
	if err != nil {
		return err
	}
	dyn, err := m.DynamicClient(cluster)
	if err != nil {
		return err
	}
	u, err := dyn.Resource(gvrHPAs).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("获取 HPA 失败: %w", err)
	}
	cur, _ := u.Object["spec"].(map[string]any)
	if cur == nil {
		cur = map[string]any{}
	}
	// 保留 scaleTargetRef 等既有字段, 仅覆盖 min/max/metrics
	for k, v := range spec {
		cur[k] = v
	}
	u.Object["spec"] = cur
	if _, err := dyn.Resource(gvrHPAs).Namespace(ns).Update(ctx, u, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("更新 HPA 失败: %w", err)
	}
	return nil
}

func (m *Manager) CreatePDB(ctx context.Context, cluster, ns, targetRes, targetName, pdbName, minAvailable, maxUnavailable string) error {
	labels, err := m.workloadSelectorLabels(ctx, cluster, targetRes, ns, targetName)
	if err != nil {
		return fmt.Errorf("获取 workload selector 失败: %w", err)
	}
	spec := map[string]any{
		"selector": map[string]any{"matchLabels": labels},
	}
	if minAvailable != "" {
		spec["minAvailable"] = intOrAny(minAvailable)
	} else if maxUnavailable != "" {
		spec["maxUnavailable"] = intOrAny(maxUnavailable)
	} else {
		return fmt.Errorf("必须指定 minAvailable 或 maxUnavailable")
	}
	obj := map[string]any{
		"apiVersion": "policy/v1",
		"kind":       "PodDisruptionBudget",
		"metadata":   map[string]any{"name": pdbName, "namespace": ns},
		"spec":       spec,
	}
	_, _, _, err = m.UpsertObject(ctx, cluster, obj, true)
	return err
}

func (m *Manager) CreateResourceQuota(ctx context.Context, cluster, ns, name string, cpu, mem, storage, pods string) error {
	hard := map[string]any{}
	if cpu != "" {
		hard["requests.cpu"] = cpu
		hard["limits.cpu"] = cpu
	}
	if mem != "" {
		hard["requests.memory"] = mem
		hard["limits.memory"] = mem
	}
	if storage != "" {
		hard["persistentvolumeclaims.storage"] = storage
	}
	if pods != "" {
		hard["pods"] = pods
	}
	if len(hard) == 0 {
		return fmt.Errorf("至少指定一个配额限制")
	}
	obj := map[string]any{
		"apiVersion": "v1",
		"kind":       "ResourceQuota",
		"metadata":   map[string]any{"name": name, "namespace": ns},
		"spec":       map[string]any{"hard": hard},
	}
	_, _, _, err := m.UpsertObject(ctx, cluster, obj, true)
	return err
}

func (m *Manager) CreateLimitRange(ctx context.Context, cluster, ns, name string, defaultCpu, defaultReqCpu, maxCpu, defaultMem, defaultReqMem, maxMem string) error {
	limits := map[string]any{"type": "Container"}
	if defaultCpu != "" {
		limits["default"] = map[string]any{"cpu": defaultCpu}
	}
	if defaultReqCpu != "" {
		limits["defaultRequest"] = map[string]any{"cpu": defaultReqCpu}
	}
	if maxCpu != "" {
		limits["max"] = map[string]any{"cpu": maxCpu}
	}
	if defaultMem != "" {
		if _, ok := limits["default"]; !ok {
			limits["default"] = map[string]any{}
		}
		limits["default"].(map[string]any)["memory"] = defaultMem
	}
	if defaultReqMem != "" {
		if _, ok := limits["defaultRequest"]; !ok {
			limits["defaultRequest"] = map[string]any{}
		}
		limits["defaultRequest"].(map[string]any)["memory"] = defaultReqMem
	}
	if maxMem != "" {
		if _, ok := limits["max"]; !ok {
			limits["max"] = map[string]any{}
		}
		limits["max"].(map[string]any)["memory"] = maxMem
	}
	if len(limits) == 1 { // 只有 type
		return fmt.Errorf("至少指定一个资源限制值")
	}
	obj := map[string]any{
		"apiVersion": "v1",
		"kind":       "LimitRange",
		"metadata":   map[string]any{"name": name, "namespace": ns},
		"spec":       map[string]any{"limits": []any{limits}},
	}
	_, _, _, err := m.UpsertObject(ctx, cluster, obj, true)
	return err
}

func (m *Manager) CreateNetworkPolicy(ctx context.Context, cluster, ns, name, podSelKey, podSelValue, ingressFromKey, ingressFromValue, ingressPorts string) error {
	podSel := map[string]any{}
	if podSelKey != "" {
		podSel["matchLabels"] = map[string]any{podSelKey: podSelValue}
	}
	spec := map[string]any{
		"podSelector": podSel,
		"policyTypes": []any{"Ingress"},
	}
	// build ingress rules
	var ingressRules []any
	ingRule := map[string]any{}
	if ingressFromKey != "" {
		ingRule["from"] = []any{map[string]any{
			"podSelector": map[string]any{"matchLabels": map[string]any{ingressFromKey: ingressFromValue}},
		}}
	}
	// parse ports "80/TCP,443/TCP"
	if ingressPorts != "" {
		var ports []any
		for _, pp := range strings.Split(ingressPorts, ",") {
			pp = strings.TrimSpace(pp)
			if pp == "" {
				continue
			}
			parts := strings.SplitN(pp, "/", 2)
			port := map[string]any{}
			if n, e := parsePort(parts[0]); e == nil {
				port["port"] = n
			}
			if len(parts) == 2 {
				port["protocol"] = strings.ToUpper(parts[1])
			}
			ports = append(ports, port)
		}
		if len(ports) > 0 {
			ingRule["ports"] = ports
		}
	}
	if len(ingRule) > 0 {
		ingressRules = append(ingressRules, ingRule)
	}
	if len(ingressRules) > 0 {
		spec["ingress"] = ingressRules
	}
	obj := map[string]any{
		"apiVersion": "networking.k8s.io/v1",
		"kind":       "NetworkPolicy",
		"metadata":   map[string]any{"name": name, "namespace": ns},
		"spec":       spec,
	}
	_, _, _, err := m.UpsertObject(ctx, cluster, obj, true)
	return err
}

func (m *Manager) CreateStorageClass(ctx context.Context, cluster, name, provisioner, reclaimPolicy, volumeBindingMode string, allowExpansion bool, params string) error {
	if provisioner == "" {
		return fmt.Errorf("必须指定 provisioner")
	}
	if reclaimPolicy == "" {
		reclaimPolicy = "Retain"
	}
	if volumeBindingMode == "" {
		volumeBindingMode = "Immediate"
	}
	spec := map[string]any{
		"provisioner":          provisioner,
		"reclaimPolicy":        reclaimPolicy,
		"volumeBindingMode":    volumeBindingMode,
		"allowVolumeExpansion": allowExpansion,
	}
	// parse params JSON {"key":"value"}
	if params != "" {
		var pm map[string]any
		if err := json.Unmarshal([]byte(params), &pm); err == nil && len(pm) > 0 {
			spec["parameters"] = pm
		}
	}
	meta := map[string]any{"name": name}
	// 标准集群用 spec 包装; 个别非标准集群(StorageClass 扁平 schema)需要字段平铺。
	// 先试标准 schema, 失败再试扁平(仅在本集群形状异常时触发)。
	obj := map[string]any{
		"apiVersion": "storage.k8s.io/v1",
		"kind":       "StorageClass",
		"metadata":   meta,
		"spec":       spec,
	}
	_, _, _, err := m.UpsertObject(ctx, cluster, obj, true)
	if err == nil {
		return nil
	}
	flatObj := map[string]any{
		"apiVersion": "storage.k8s.io/v1",
		"kind":       "StorageClass",
		"metadata":   meta,
	}
	for k, v := range spec {
		flatObj[k] = v
	}
	_, _, _, err2 := m.UpsertObject(ctx, cluster, flatObj, true)
	if err2 == nil {
		return nil
	}
	return err
}

func (m *Manager) CreatePersistentVolume(ctx context.Context, cluster, name, capacity, accessMode, storageClassName, mode, hostPath, nfsServer, nfsPath string) error {
	if capacity == "" {
		capacity = "1Gi"
	}
	if accessMode == "" {
		accessMode = "ReadWriteOnce"
	}
	if storageClassName == "" {
		storageClassName = "manual"
	}
	if mode == "" {
		mode = "hostPath"
	}
	// PersistentVolumeSource 在 spec 内内联(flat): hostPath/nfs/local 直接在 spec 层
	spec := map[string]any{
		"capacity":                      map[string]any{"storage": capacity},
		"accessModes":                   []any{accessMode},
		"persistentVolumeReclaimPolicy": "Retain",
		"storageClassName":              storageClassName,
	}
	switch mode {
	case "nfs":
		spec["nfs"] = map[string]any{"server": nfsServer, "path": nfsPath}
	case "local":
		spec["local"] = map[string]any{"path": hostPath}
	default: // hostPath
		spec["hostPath"] = map[string]any{"path": hostPath}
	}
	obj := map[string]any{
		"apiVersion": "v1",
		"kind":       "PersistentVolume",
		"metadata": map[string]any{
			"name": name,
		},
		"spec": spec,
	}
	_, _, _, err := m.UpsertObject(ctx, cluster, obj, true)
	return err
}

func (m *Manager) CreatePriorityClass(ctx context.Context, cluster, name string, value int, description string, globalDefault bool) error {
	obj := map[string]any{
		"apiVersion": "scheduling.k8s.io/v1",
		"kind":       "PriorityClass",
		"metadata":   map[string]any{"name": name},
		"value":      int32(value),
		"globalDefault": globalDefault,
	}
	if description != "" {
		obj["description"] = description
	}
	_, _, _, err := m.UpsertObject(ctx, cluster, obj, true)
	return err
}

func (m *Manager) CreateNamespaceObj(ctx context.Context, cluster, name string) error {
	obj := map[string]any{
		"apiVersion": "v1",
		"kind":       "Namespace",
		"metadata":   map[string]any{"name": name},
	}
	_, _, _, err := m.UpsertObject(ctx, cluster, obj, true)
	return err
}

func (m *Manager) CreateServiceFromWorkload(ctx context.Context, cluster, targetRes, ns, name, svcName, svcType string, port, targetPort int32) error {
	if svcName == "" {
		svcName = name
	}
	if svcType == "" {
		svcType = "ClusterIP"
	}
	labels, err := m.workloadSelectorLabels(ctx, cluster, targetRes, ns, name)
	if err != nil {
		return fmt.Errorf("获取 workload selector 失败: %w", err)
	}
	// 自动读取容器端口作为 targetPort
	if targetPort == 0 {
		ports := m.workloadContainerPorts(ctx, cluster, targetRes, ns, name)
		if len(ports) > 0 {
			targetPort = ports[0]
		} else {
			targetPort = 80
		}
	}
	if port == 0 {
		port = targetPort
	}
	svcPort := map[string]any{
		"port":       port,
		"targetPort": targetPort,
		"protocol":   "TCP",
	}
	if svcType == "NodePort" {
		svcPort["nodePort"] = 0 // let apiserver assign
	}
	obj := map[string]any{
		"apiVersion": "v1",
		"kind":       "Service",
		"metadata": map[string]any{
			"name":      svcName,
			"namespace": ns,
		},
		"spec": map[string]any{
			"type":     svcType,
			"selector": labels,
			"ports":    []any{svcPort},
		},
	}
	_, _, _, err = m.UpsertObject(ctx, cluster, obj, true)
	return err
}

// ──────────────────── wait ────────────────────

func (m *Manager) WaitForCondition(ctx context.Context, cluster, res, ns, name, condition string, timeoutSec int) error {
	if timeoutSec <= 0 {
		timeoutSec = 60
	}
	if timeoutSec > 180 {
		timeoutSec = 180
	}
	dyn, err := m.DynamicClient(cluster)
	if err != nil {
		return err
	}
	gvr := gvrOf(res)
	var dynRes dynamic.ResourceInterface = dyn.Resource(gvr)
	isCluster := clusterScopedResources[res]
	if !isCluster && ns != "" {
		dynRes = dyn.Resource(gvr).Namespace(ns)
	}
	deadline := time.Now().Add(time.Duration(timeoutSec) * time.Second)
	for {
		var nextSleep time.Duration = 0
		u, err := dynRes.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			// 资源不存在则继续等待(可能还在创建)
			if !apierrors.IsNotFound(err) {
				return fmt.Errorf("获取资源失败: %w", err)
			}
			nextSleep = 2 * time.Second
		} else {
			conds, found, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
			if found {
				for _, c := range conds {
					cm, ok := c.(map[string]any)
					if !ok {
						continue
					}
					if cm["type"] == condition && cm["status"] == "True" {
						return nil
					}
				}
			}
			if time.Now().After(deadline) {
				msg := fmt.Sprintf("等待条件 %s 超时(%ds)", condition, timeoutSec)
				if found && len(conds) > 0 {
					var condTypes []string
					for _, c := range conds {
						cm, ok := c.(map[string]any)
						if !ok {
							continue
						}
						condTypes = append(condTypes, fmt.Sprintf("%v=%v", cm["type"], cm["status"]))
					}
					msg += fmt.Sprintf(" 当前条件: [%s]", strings.Join(condTypes, ", "))
				}
				return fmt.Errorf("%s", msg)
			}
			nextSleep = 2 * time.Second
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(nextSleep):
		}
	}
}

func parsePort(s string) (int32, error) {
	s = strings.TrimSpace(s)
	var n int
	_, err := fmt.Sscanf(s, "%d", &n)
	return int32(n), err
}

// intOrAny: IntOrString 字段, 纯数字用 int, 百分比/小数用字符串
func intOrAny(s string) any {
	if n, err := parsePort(s); err == nil {
		return n
	}
	return s
}
