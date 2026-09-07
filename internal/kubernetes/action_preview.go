package kubernetes

// action_preview.go: 为每个 catalog action 附加"将要执行的 kubectl 命令"预览生成器。
// 前端在表单下方展示该命令, 用户确认后仍走后端 Go 原生执行 (语义一致, 不 shell 执行)。
// Preview 只做"透明合同", 不负责执行; 流式 (requiresTTY) action 无预览。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// setPreviewName 给已注册 action 附加 Preview 生成器
func setPreviewName(name string, fn PreviewFunc) {
	if a, ok := ActionCatalog[name]; ok && fn != nil {
		a.Preview = fn
	}
}

func init() {
	// 通用 (任意资源)
	setPreviewName("delete", func(_ *Manager, rc RunCtx) (string, map[string]any, error) {
		cmd := kcBase(rc.Ns) + " delete " + rc.Res + "/" + rc.Name
		if bval(rc, "force") {
			cmd += " --grace-period=0 --force"
		}
		return cmd, nil, nil
	})
	setPreviewName("edit-yaml", func(_ *Manager, rc RunCtx) (string, map[string]any, error) {
		yaml := sval(rc, "yaml")
		if yaml != "" {
			return kcBase(rc.Ns) + " apply -f -\n" + yaml + "\nEOF", nil, nil
		}
		return kcBase(rc.Ns) + " apply -f -", nil, nil
	})
	setPreviewName("patch", func(_ *Manager, rc RunCtx) (string, map[string]any, error) {
		pt := sval(rc, "patchType")
		if pt == "" {
			pt = "strategic-merge"
		}
		p := sval(rc, "patch")
		return fmt.Sprintf("%s patch %s/%s --type=%s -p '%s'", kcBase(rc.Ns), rc.Res, rc.Name, pt, p), nil, nil
	})
	setPreviewName("label", func(_ *Manager, rc RunCtx) (string, map[string]any, error) {
		v := sval(rc, "value")
		op := kcBase(rc.Ns) + " label " + rc.Res + "/" + rc.Name + " "
		if v == "" {
			return op + sval(rc, "key") + "-", nil, nil
		}
		if bval(rc, "overwrite") {
			op += "--overwrite "
		}
		return op + sval(rc, "key") + "=" + v, nil, nil
	})
	setPreviewName("annotate", func(_ *Manager, rc RunCtx) (string, map[string]any, error) {
		cmd := kcBase(rc.Ns) + " annotate " + rc.Res + "/" + rc.Name + " "
		if bval(rc, "overwrite") {
			cmd += "--overwrite "
		}
		return cmd + sval(rc, "key") + "=" + sval(rc, "value"), nil, nil
	})

	// 工作负载 / 生命周期
	setPreviewName("scale", func(_ *Manager, rc RunCtx) (string, map[string]any, error) {
		return fmt.Sprintf("%s scale %s/%s --replicas=%d", kcBase(rc.Ns), rc.Res, rc.Name, ival(rc, "replicas")), nil, nil
	})
	setPreviewName("restart", func(_ *Manager, rc RunCtx) (string, map[string]any, error) {
		return fmt.Sprintf("%s rollout restart %s/%s", kcBase(rc.Ns), rc.Res, rc.Name), nil, nil
	})
	setPreviewName("rollback", func(_ *Manager, rc RunCtx) (string, map[string]any, error) {
		rev := ival(rc, "revision")
		if rev > 0 {
			return fmt.Sprintf("%s rollout undo %s/%s --to-revision=%d", kcBase(rc.Ns), rc.Res, rc.Name, rev), nil, nil
		}
		return fmt.Sprintf("%s rollout undo %s/%s", kcBase(rc.Ns), rc.Res, rc.Name), nil, nil
	})
	setPreviewName("pause", func(_ *Manager, rc RunCtx) (string, map[string]any, error) {
		return fmt.Sprintf("%s rollout pause deployments/%s", kcBase(rc.Ns), rc.Name), nil, nil
	})
	setPreviewName("resume", func(_ *Manager, rc RunCtx) (string, map[string]any, error) {
		return fmt.Sprintf("%s rollout resume deployments/%s", kcBase(rc.Ns), rc.Name), nil, nil
	})
	setPreviewName("suspend", func(_ *Manager, rc RunCtx) (string, map[string]any, error) {
		json := fmt.Sprintf(`{"spec":{"suspend":%v}}`, bval(rc, "suspend"))
		return fmt.Sprintf("%s patch cronjobs/%s -p '%s'", kcBase(rc.Ns), rc.Name, json), nil, nil
	})
	setPreviewName("trigger", func(_ *Manager, rc RunCtx) (string, map[string]any, error) {
		ts := tsNow()
		return fmt.Sprintf("%s create job --from=cronjob/%s %s-manual-%s", kcBase(rc.Ns), rc.Name, rc.Name, ts), nil, nil
	})
	setPreviewName("rerun", func(_ *Manager, rc RunCtx) (string, map[string]any, error) {
		ts := tsNow()
		return fmt.Sprintf("%s create job --from=job/%s %s-retry-%s", kcBase(rc.Ns), rc.Name, rc.Name, ts), nil, nil
	})
	setPreviewName("expand", func(_ *Manager, rc RunCtx) (string, map[string]any, error) {
		json := fmt.Sprintf(`{"spec":{"resources":{"requests":{"storage":"%s"}}}}`, sval(rc, "storage"))
		return fmt.Sprintf("%s patch persistentvolumeclaims/%s -p '%s'", kcBase(rc.Ns), rc.Name, json), nil, nil
	})
	setPreviewName("wait", func(_ *Manager, rc RunCtx) (string, map[string]any, error) {
		cond := sval(rc, "condition")
		if cond == "" {
			cond = "Available"
		}
		t := ival(rc, "timeoutSec")
		if t <= 0 {
			t = 60
		}
		return fmt.Sprintf("%s wait --for=condition=%s %s/%s --timeout=%ds", kcBase(rc.Ns), cond, rc.Res, rc.Name, t), nil, nil
	})
	setPreviewName("set-image", func(_ *Manager, rc RunCtx) (string, map[string]any, error) {
		return fmt.Sprintf("%s set image %s/%s *=%s", kcBase(rc.Ns), rc.Res, rc.Name, sval(rc, "image")), nil, nil
	})
	setPreviewName("set-env", func(_ *Manager, rc RunCtx) (string, map[string]any, error) {
		return fmt.Sprintf("%s set env %s/%s %s=%s", kcBase(rc.Ns), rc.Res, rc.Name, sval(rc, "key"), sval(rc, "value")), nil, nil
	})
	setPreviewName("set-resources", func(_ *Manager, rc RunCtx) (string, map[string]any, error) {
		req := collectKV(sval(rc, "cpuReq"), "cpu", sval(rc, "memReq"), "memory")
		lim := collectKV(sval(rc, "cpuLim"), "cpu", sval(rc, "memLim"), "memory")
		cmd := kcBase(rc.Ns) + " set resources " + rc.Res + "/" + rc.Name
		if req != "" {
			cmd += " --requests=" + req
		}
		if lim != "" {
			cmd += " --limits=" + lim
		}
		return cmd, nil, nil
	})
	setPreviewName("set-sa", func(_ *Manager, rc RunCtx) (string, map[string]any, error) {
		json := fmt.Sprintf(`{"spec":{"template":{"spec":{"serviceAccountName":"%s"}}}}`, sval(rc, "sa"))
		return fmt.Sprintf("%s patch %s/%s --type=merge -p '%s'", kcBase(rc.Ns), rc.Res, rc.Name, json), nil, nil
	})

	// 节点
	setPreviewName("cordon", func(_ *Manager, rc RunCtx) (string, map[string]any, error) {
		return "kubectl cordon " + rc.Name, nil, nil
	})
	setPreviewName("uncordon", func(_ *Manager, rc RunCtx) (string, map[string]any, error) {
		return "kubectl uncordon " + rc.Name, nil, nil
	})
	setPreviewName("drain", func(_ *Manager, rc RunCtx) (string, map[string]any, error) {
		cmd := "kubectl drain " + rc.Name
		if n := ival(rc, "graceSeconds"); n > 0 {
			cmd += fmt.Sprintf(" --grace-period=%d", n)
		}
		if bval(rc, "ignoreDaemonsets") {
			cmd += " --ignore-daemonsets"
		}
		if bval(rc, "deleteEmptyDirData") {
			cmd += " --delete-emptydir-data"
		}
		if bval(rc, "force") {
			cmd += " --force"
		}
		return cmd, nil, nil
	})
	setPreviewName("delete-node", func(_ *Manager, rc RunCtx) (string, map[string]any, error) {
		drain := "kubectl drain " + rc.Name
		if n := ival(rc, "graceSeconds"); n > 0 {
			drain += fmt.Sprintf(" --grace-period=%d", n)
		}
		if bval(rc, "ignoreDaemonsets") {
			drain += " --ignore-daemonsets"
		}
		if bval(rc, "deleteEmptyDirData") {
			drain += " --delete-emptydir-data"
		}
		if bval(rc, "force") {
			drain += " --force"
		}
		return drain + " && kubectl delete node " + rc.Name, nil, nil
	})
	setPreviewName("taint-add", func(_ *Manager, rc RunCtx) (string, map[string]any, error) {
		k := sval(rc, "key")
		e := sval(rc, "effect")
		v := sval(rc, "value")
		if v != "" {
			k = k + "=" + v
		}
		return fmt.Sprintf("kubectl taint nodes %s %s:%s", rc.Name, k, e), nil, nil
	})
	setPreviewName("taint-remove", func(_ *Manager, rc RunCtx) (string, map[string]any, error) {
		k := sval(rc, "key")
		if e := sval(rc, "effect"); e != "" {
			k = k + ":" + e
		}
		return fmt.Sprintf("kubectl taint nodes %s %s-", rc.Name, k), nil, nil
	})

	// 创建类
	setPreviewName("autoscale", func(_ *Manager, rc RunCtx) (string, map[string]any, error) {
		cmd := fmt.Sprintf("%s autoscale %s/%s --min=%d --max=%d", kcBase(rc.Ns), rc.Res, rc.Name,
			ival(rc, "min"), ival(rc, "max"))
		if pct := ival(rc, "cpuPercent"); pct > 0 {
			cmd += fmt.Sprintf(" --cpu-percent=%d", pct)
		}
		if n := sval(rc, "name"); n != "" {
			cmd += " --name=" + n
		}
		return cmd, nil, nil
	})
	setPreviewName("create-pdb", func(_ *Manager, rc RunCtx) (string, map[string]any, error) {
		cmd := fmt.Sprintf("%s create poddisruptionbudget %s --selector='(工作负载 selector, 后端自动读取)'",
			kcBase(rc.Ns), sval(rc, "pdbName"))
		if v := sval(rc, "minAvailable"); v != "" {
			cmd += " --min-available=" + v
		}
		if v := sval(rc, "maxUnavailable"); v != "" {
			cmd += " --max-unavailable=" + v
		}
		return cmd, nil, nil
	})
	setPreviewName("create-quota", func(_ *Manager, rc RunCtx) (string, map[string]any, error) {
		hard := collectKV(sval(rc, "cpu"), "cpu", sval(rc, "mem"), "memory", sval(rc, "storage"), "storage")
		if v := sval(rc, "pods"); v != "" {
			hard = joinKV(hard, "count/pods="+v)
		}
		cmd := fmt.Sprintf("%s create quota %s", kcBase(rc.Ns), sval(rc, "quotaName"))
		if hard != "" {
			cmd += " --hard=" + hard
		}
		return cmd, nil, nil
	})
	setPreviewName("create-limitrange", func(_ *Manager, rc RunCtx) (string, map[string]any, error) {
		max := collectKV(sval(rc, "maxCpu"), "cpu", sval(rc, "maxMem"), "memory")
		def := collectKV(sval(rc, "defaultCpu"), "cpu", sval(rc, "defaultMem"), "memory")
		defReq := collectKV(sval(rc, "defaultReqCpu"), "cpu", sval(rc, "defaultReqMem"), "memory")
		cmd := fmt.Sprintf("%s create limitrange %s", kcBase(rc.Ns), sval(rc, "lrName"))
		if max != "" {
			cmd += " --max=" + max
		}
		if def != "" {
			cmd += " --default=" + def
		}
		if defReq != "" {
			cmd += " --default-request=" + defReq
		}
		return cmd, nil, nil
	})
	setPreviewName("create-networkpolicy", func(_ *Manager, rc RunCtx) (string, map[string]any, error) {
		np := map[string]any{
			"apiVersion": "networking.k8s.io/v1",
			"kind":       "NetworkPolicy",
			"metadata": map[string]any{
				"name":      sval(rc, "npName"),
				"namespace": rc.Ns,
			},
			"spec": map[string]any{
				"podSelector": map[string]any{
					"matchLabels": map[string]any{sval(rc, "podSelKey"): sval(rc, "podSelValue")},
				},
				"policyTypes": []string{"Ingress"},
			},
		}
		ing := map[string]any{}
		if k := sval(rc, "ingressFromKey"); k != "" {
			ing["from"] = []map[string]any{{
				"podSelector": map[string]any{"matchLabels": map[string]any{k: sval(rc, "ingressFromValue")}},
			}}
		}
		if p := sval(rc, "ingressPorts"); p != "" {
			var ports []map[string]any
			for _, pp := range strings.Split(p, ",") {
				pp = strings.TrimSpace(pp)
				if pp == "" {
					continue
				}
				parts := strings.SplitN(pp, "/", 2)
				port := map[string]any{"port": parts[0]}
				if len(parts) == 2 {
					port["protocol"] = strings.ToUpper(parts[1])
				}
				ports = append(ports, port)
			}
			if len(ports) > 0 {
				ing["ports"] = ports
			}
		}
		if len(ing) > 0 {
			np["spec"].(map[string]any)["ingress"] = []map[string]any{ing}
		}
		b, err := json.MarshalIndent(np, "", "  ")
		if err != nil {
			return "", nil, err
		}
		return kcBase(rc.Ns) + " apply -f -\n" + string(b) + "\nEOF", nil, nil
	})
	setPreviewName("create-sc", func(_ *Manager, rc RunCtx) (string, map[string]any, error) {
		cmd := fmt.Sprintf("kubectl create storageclass %s --provisioner=%s --reclaim-policy=%s --volume-binding-mode=%s",
			sval(rc, "scName"), sval(rc, "provisioner"), sval(rc, "reclaimPolicy"), sval(rc, "volumeBindingMode"))
		if bval(rc, "allowExpansion") {
			cmd += " --allow-volume-expansion"
		}
		return cmd, nil, nil
	})
	setPreviewName("create-pv", func(_ *Manager, rc RunCtx) (string, map[string]any, error) {
		cmd := fmt.Sprintf("kubectl create persistentvolume %s --capacity=%s --access-modes=%s",
			sval(rc, "pvName"), sval(rc, "capacity"), sval(rc, "accessMode"))
		if sc := sval(rc, "storageClassName"); sc != "" {
			cmd += " --storage-class=" + sc
		}
		switch sval(rc, "mode") {
		case "nfs":
			cmd += fmt.Sprintf(" --nfs=%s:%s", sval(rc, "nfsServer"), sval(rc, "nfsPath"))
		case "local":
			cmd += " --host-path=" + sval(rc, "hostPath") + "   # local 体积类型需 YAML, 后端生成"
		default:
			cmd += " --host-path=" + sval(rc, "hostPath")
		}
		return cmd, nil, nil
	})
	setPreviewName("create-priorityclass", func(_ *Manager, rc RunCtx) (string, map[string]any, error) {
		cmd := fmt.Sprintf("kubectl create priorityclass %s --value=%d", sval(rc, "pcName"), ival(rc, "value"))
		if bval(rc, "globalDefault") {
			cmd += " --global-default"
		}
		if d := sval(rc, "description"); d != "" {
			cmd += fmt.Sprintf(" --description=%q", d)
		}
		return cmd, nil, nil
	})
	setPreviewName("create-namespace", func(_ *Manager, rc RunCtx) (string, map[string]any, error) {
		return "kubectl create namespace " + sval(rc, "nsName"), nil, nil
	})
	setPreviewName("expose", func(_ *Manager, rc RunCtx) (string, map[string]any, error) {
		cmd := fmt.Sprintf("%s expose %s/%s --type=%s --port=%d", kcBase(rc.Ns), rc.Res, rc.Name,
			sval(rc, "svcType"), ival(rc, "port"))
		if n := sval(rc, "svcName"); n != "" {
			cmd += " --name=" + n
		}
		if tp := ival(rc, "targetPort"); tp > 0 {
			cmd += fmt.Sprintf(" --target-port=%d", tp)
		}
		return cmd, nil, nil
	})

	// 调试
	setPreviewName("ephemeral-add", func(_ *Manager, rc RunCtx) (string, map[string]any, error) {
		cmd := fmt.Sprintf("%s debug %s -it --image=%s --container=%s", kcBase(rc.Ns), rc.Name, sval(rc, "image"), sval(rc, "containerName"))
		if t := sval(rc, "targetContainerName"); t != "" {
			cmd += " --target=" + t
		}
		cmd += " -- " + sval(rc, "command")
		return cmd, nil, nil
	})
	setPreviewName("ephemeral-remove", func(m *Manager, rc RunCtx) (string, map[string]any, error) {
		cname := sval(rc, "containerName")
		// 无参数时只展示操作说明 (表单未填容器名前不回填)
		if cname == "" {
			return "kubectl patch pod " + rc.Name + " --type=json -p '[{\"op\":\"remove\",\"path\":\"/spec/ephemeralContainers/N\"}]'\n# N = 要移除的边车容器在 spec.ephemeralContainers 中的下标", nil, nil
		}
		if m == nil {
			return "", nil, fmt.Errorf("ephemeral-remove 预览需要集群连接")
		}
		cs, err := m.Clientset(rc.Cluster)
		if err != nil {
			return "", nil, err
		}
		p, err := cs.CoreV1().Pods(rc.Ns).Get(rc.Ctx, rc.Name, metav1.GetOptions{})
		if err != nil {
			return "", nil, err
		}
		idx := -1
		for i, ec := range p.Spec.EphemeralContainers {
			if ec.Name == cname {
				idx = i
				break
			}
		}
		if idx < 0 {
			return "", nil, fmt.Errorf("边车容器 %q 在当前 Pod 中不存在", cname)
		}
		return fmt.Sprintf("kubectl -n %s patch pod %s --type=json -p '[{\"op\":\"remove\",\"path\":\"/spec/ephemeralContainers/%d\"}]'",
			rc.Ns, rc.Name, idx), nil, nil
	})

	// HPA 可视化编辑 (样例资源)
	setPreviewName("update-hpa", previewUpdateHPA)
}

// kcBase kubectl 前缀; ns 非空时带 -n
func kcBase(ns string) string {
	if ns != "" {
		return "kubectl -n " + ns
	}
	return "kubectl"
}

func sval(rc RunCtx, key string) string {
	switch v := rc.Params[key].(type) {
	case string:
		return v
	case float64:
		return fmt.Sprintf("%g", v)
	case bool:
		return fmt.Sprintf("%v", v)
	}
	return ""
}

func ival(rc RunCtx, key string) int {
	n, _ := toInt(rc.Params[key])
	return n
}

func bval(rc RunCtx, key string) bool {
	v, _ := rc.Params[key].(bool)
	return v
}

// collectKV 把 (value,name) 对中非空 value 收集为 name=value 形式
func collectKV(pairs ...string) string {
	out := make([]string, 0, 4)
	for i := 0; i+1 < len(pairs); i += 2 {
		if pairs[i] != "" {
			out = append(out, pairs[i+1]+"="+pairs[i])
		}
	}
	return strings.Join(out, ",")
}

func joinKV(cur, next string) string {
	if cur == "" {
		return next
	}
	return cur + "," + next
}

func tsNow() string {
	return fmt.Sprintf("%d", time.Now().Unix())
}

// ===== update-hpa (样例: HPA 可视化编辑) =====

// previewUpdateHPA 生成 patch 命令 + 回填当前 HPA 值作为表单默认值
func previewUpdateHPA(m *Manager, rc RunCtx) (string, map[string]any, error) {
	var defaults map[string]any
	if m != nil {
		defaults = currentHPADefaults(m, rc.Ctx, rc.Cluster, rc.Ns, rc.Name)
	}
	spec, err := buildHPASpecPatch(rc.Params)
	if err != nil {
		return "", defaults, nil // 参数未完整时不出命令, 仍返回 defaults 供回填
	}
	jsonBytes, err := json.Marshal(map[string]any{"spec": spec})
	if err != nil {
		return "", defaults, nil
	}
	cmd := fmt.Sprintf("%s patch horizontalpodautoscalers/%s --type=merge -p '%s'",
		kcBase(rc.Ns), rc.Name, string(jsonBytes))
	return cmd, defaults, nil
}

// currentHPADefaults 读取当前 HPA, 提取用于表单回填的字段
func currentHPADefaults(m *Manager, ctx context.Context, cluster, ns, name string) map[string]any {
	if m == nil {
		return nil
	}
	dyn, err := m.DynamicClient(cluster)
	if err != nil {
		return nil
	}
	obj, err := dyn.Resource(gvrHPAs).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil
	}
	out := map[string]any{}
	if v, ok, _ := unstructuredInt64s(obj.Object, "spec", "minReplicas"); ok {
		out["min"] = v
	}
	if v, ok, _ := unstructuredInt64s(obj.Object, "spec", "maxReplicas"); ok {
		out["max"] = v
	}
	metrics, _, _ := unstructuredSlice(obj.Object, "spec", "metrics")
	var cpuPct, memStr string
	mode := ""
	for _, mt := range metrics {
		mm, _ := mt.(map[string]any)
		if mm == nil {
			continue
		}
		if mm["type"] != "Resource" {
			continue
		}
		resName, _ := pathString(mm, "resource", "name")
		switch resName {
		case "cpu":
			mode = "cpu"
			if v, ok := pathInt(mm, "resource", "target", "averageUtilization"); ok {
				cpuPct = fmt.Sprintf("%d", v)
			}
		case "memory":
			if mode == "cpu" {
				mode = "both"
			} else {
				mode = "memory"
			}
			if v, ok := pathInt(mm, "resource", "target", "averageUtilization"); ok {
				memStr = fmt.Sprintf("%d%%", v)
			} else if s, _ := pathString(mm, "resource", "target", "averageValue"); s != "" {
				memStr = s
			}
		}
	}
	if cpuPct != "" {
		out["cpuPercent"] = cpuPct
	}
	if memStr != "" {
		out["memoryTarget"] = memStr
	}
	if mode != "" {
		out["metricMode"] = mode
	}
	return out
}

// unstructured 小工具 (避免暴露 internal/unstructured 细节)
func unstructuredInt64s(obj any, fields ...string) (int64, bool, error) {
	m, ok := obj.(map[string]any)
	if !ok {
		return 0, false, nil
	}
	cur := m
	for i, f := range fields {
		v, exists := cur[f]
		if !exists {
			return 0, false, nil
		}
		if i == len(fields)-1 {
			n, ok := numToInt64(v)
			return n, ok, nil
		}
		next, ok := v.(map[string]any)
		if !ok {
			return 0, false, nil
		}
		cur = next
	}
	return 0, false, nil
}

func unstructuredSlice(obj any, fields ...string) ([]any, bool, error) {
	m, ok := obj.(map[string]any)
	if !ok {
		return nil, false, nil
	}
	cur := m
	for i, f := range fields {
		v, exists := cur[f]
		if !exists {
			return nil, false, nil
		}
		if i == len(fields)-1 {
			s, ok := v.([]any)
			return s, ok, nil
		}
		next, ok := v.(map[string]any)
		if !ok {
			return nil, false, nil
		}
		cur = next
	}
	return nil, false, nil
}

func pathString(obj any, path ...string) (string, bool) {
	m, ok := obj.(map[string]any)
	if !ok {
		return "", false
	}
	cur := m
	for i, f := range path {
		v, exists := cur[f]
		if !exists {
			return "", false
		}
		if i == len(path)-1 {
			s, ok := v.(string)
			return s, ok
		}
		next, ok := v.(map[string]any)
		if !ok {
			return "", false
		}
		cur = next
	}
	return "", false
}

func pathInt(obj any, path ...string) (int64, bool) {
	m, ok := obj.(map[string]any)
	if !ok {
		return 0, false
	}
	cur := m
	for i, f := range path {
		v, exists := cur[f]
		if !exists {
			return 0, false
		}
		if i == len(path)-1 {
			return numToInt64(v)
		}
		next, ok := v.(map[string]any)
		if !ok {
			return 0, false
		}
		cur = next
	}
	return 0, false
}

func numToInt64(v any) (int64, bool) {
	switch x := v.(type) {
	case int:
		return int64(x), true
	case int32:
		return int64(x), true
	case int64:
		return x, true
	case float32:
		return int64(x), true
	case float64:
		return int64(x), true
	case json.Number:
		n, err := x.Int64()
		return n, err == nil
	}
	return 0, false
}

// buildHPASpecPatch 从表单参数构建 HPA spec merge-patch (min/max + metrics 规则)
func buildHPASpecPatch(params map[string]any) (map[string]any, error) {
	min, _ := toInt(params["min"])
	max, _ := toInt(params["max"])
	if min <= 0 {
		return nil, fmt.Errorf("最小副本必须 ≥ 1")
	}
	if max < min {
		return nil, fmt.Errorf("最大副本必须 ≥ 最小副本")
	}
	spec := map[string]any{
		"minReplicas": min,
		"maxReplicas": max,
	}
	mode := svalFromParams(params, "metricMode")
	if mode == "" {
		mode = "cpu"
	}
	cpuPct, _ := toInt(params["cpuPercent"])
	var metrics []any
	switch mode {
	case "memory":
		mem, err := memoryMetric(params)
		if err != nil {
			return nil, err
		}
		metrics = []any{mem}
	case "both":
		metrics = []any{cpuUtilMetric(cpuPct)}
		mem, err := memoryMetric(params)
		if err != nil {
			return nil, err
		}
		metrics = append(metrics, mem)
	default: // cpu
		metrics = []any{cpuUtilMetric(cpuPct)}
	}
	spec["metrics"] = metrics
	return spec, nil
}

func cpuUtilMetric(pct int) map[string]any {
	if pct <= 0 {
		pct = 80
	}
	return map[string]any{
		"type": "Resource",
		"resource": map[string]any{
			"name": "cpu",
			"target": map[string]any{
				"type":               "Utilization",
				"averageUtilization": pct,
			},
		},
	}
}

func memoryMetric(params map[string]any) (map[string]any, error) {
	mt := svalFromParams(params, "memoryTarget")
	if mt == "" {
		return nil, fmt.Errorf("扩容规则含内存时, 必须填写内存目标 (如 512Mi 或 80%%)")
	}
	if strings.HasSuffix(mt, "%") {
		pct, _ := toInt(strings.TrimSuffix(mt, "%"))
		if pct <= 0 {
			return nil, fmt.Errorf("内存目标百分号部分必须 ≥ 1")
		}
		return map[string]any{
			"type": "Resource",
			"resource": map[string]any{
				"name": "memory",
				"target": map[string]any{
					"type":               "Utilization",
					"averageUtilization": pct,
				},
			},
		}, nil
	}
	return map[string]any{
		"type": "Resource",
		"resource": map[string]any{
			"name": "memory",
			"target": map[string]any{
				"type":        "AverageValue",
				"averageValue": mt,
			},
		},
	}, nil
}

func svalFromParams(params map[string]any, key string) string {
	switch v := params[key].(type) {
	case string:
		return v
	case float64:
		return fmt.Sprintf("%g", v)
	case bool:
		return fmt.Sprintf("%v", v)
	}
	return ""
}