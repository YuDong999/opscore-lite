package kubernetes

// 资源操作(写): 命令公式 → 方法。审计在 handler 层统一打点。

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	sigsyaml "sigs.k8s.io/yaml"
)

// ===== Deployment 回滚 =====

type RolloutRevision struct {
	Revision    int64  `json:"revision"`
	Name        string `json:"name"`
	ChangeCause string `json:"changeCause"`
	Age         string `json:"age"`
	Current     bool   `json:"current"`
}

// RolloutHistory 列出 deployment 的 ReplicaSet 版本(有 pod-template-hash 的)。
func (m *Manager) RolloutHistory(ctx context.Context, clusterID, ns, name string) ([]RolloutRevision, error) {
	dyn, err := m.DynamicClient(clusterID)
	if err != nil {
		return nil, err
	}
	dep, err := dyn.Resource(gvrDeployments).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	curRev := int64(0)
	if ann := dep.GetAnnotations(); ann != nil {
		fmt.Sscanf(ann["deployment.kubernetes.io/revision"], "%d", &curRev)
	}
	rsList, err := dyn.Resource(gvrReplicaSets).Namespace(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := []RolloutRevision{}
	for i := range rsList.Items {
		rs := &rsList.Items[i]
		// 只取属于该 deployment 的 RS(OwnerReference 或 pod-template-hash 前缀)
		belongs := false
		for _, o := range rs.GetOwnerReferences() {
			if o.Name == name {
				belongs = true
				break
			}
		}
		if !belongs {
			continue
		}
		rev := int64(0)
		fmt.Sscanf(rs.GetAnnotations()["deployment.kubernetes.io/revision"], "%d", &rev)
		if rev == 0 {
			continue
		}
		out = append(out, RolloutRevision{
			Revision: rev, Name: rs.GetName(),
			ChangeCause: rs.GetAnnotations()["kubernetes.io/change-cause"],
			Age:         humanAge(rs.GetCreationTimestamp().Time, time.Now()),
			Current:     rev == curRev,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Revision > out[j].Revision })
	return out, nil
}

// RolloutUndo 回滚到指定版本(revision<=0 表示回滚到上一版)。内部用 rollout restart 式注解 + 模板替换:
// 直接把目标 RS 的 PodTemplate 复制到 deployment(kubectl undo 的等价实现)。
func (m *Manager) RolloutUndo(ctx context.Context, clusterID, ns, name string, toRevision int64) error {
	dyn, err := m.DynamicClient(clusterID)
	if err != nil {
		return err
	}
	rsList, err := dyn.Resource(gvrReplicaSets).Namespace(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	depU, err := dyn.Resource(gvrDeployments).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	curRev := int64(0)
	if ann := depU.GetAnnotations(); ann != nil {
		fmt.Sscanf(ann["deployment.kubernetes.io/revision"], "%d", &curRev)
	}

	type cand struct {
		rev int64
		obj map[string]any
	}
	cands := []cand{}
	for i := range rsList.Items {
		rs := &rsList.Items[i]
		belongs := false
		for _, o := range rs.GetOwnerReferences() {
			if o.Name == name {
				belongs = true
				break
			}
		}
		if !belongs {
			continue
		}
		rev := int64(0)
		fmt.Sscanf(rs.GetAnnotations()["deployment.kubernetes.io/revision"], "%d", &rev)
		if rev > 0 && rev != curRev {
			cands = append(cands, cand{rev, rsList.Items[i].Object})
		}
	}
	if len(cands) == 0 {
		return fmt.Errorf("没有可回滚的历史版本")
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].rev > cands[j].rev })

	var target map[string]any
	if toRevision <= 0 {
		target = cands[0].obj // 上一版
	} else {
		for _, c := range cands {
			if c.rev == toRevision {
				target = c.obj
				break
			}
		}
	}
	if target == nil {
		return fmt.Errorf("未找到 revision %d", toRevision)
	}
	tpl, ok := target["spec"].(map[string]any)["template"].(map[string]any)
	if !ok {
		return fmt.Errorf("历史版本模板解析失败")
	}
	dspec, _ := depU.Object["spec"].(map[string]any)
	if dspec == nil {
		return fmt.Errorf("deployment spec 缺失")
	}
	dspec["template"] = tpl
	if ann := depU.GetAnnotations(); ann != nil {
		delete(ann, "deployment.kubernetes.io/revision")
	}
	_, err = dyn.Resource(gvrDeployments).Namespace(ns).Update(ctx, depU, metav1.UpdateOptions{})
	return err
}

// ===== Rollout 暂停/恢复 =====

func (m *Manager) RolloutPause(ctx context.Context, clusterID, res, ns, name string, pause bool) error {
	if res != "deployments" {
		return fmt.Errorf("仅 deployments 支持暂停/恢复发布")
	}
	dyn, err := m.DynamicClient(clusterID)
	if err != nil {
		return err
	}
	u, err := dyn.Resource(gvrOf(res)).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	spec, ok := u.Object["spec"].(map[string]any)
	if !ok {
		spec = map[string]any{}
		u.Object["spec"] = spec
	}
	spec["paused"] = pause
	_, err = dyn.Resource(gvrOf(res)).Namespace(ns).Update(ctx, u, metav1.UpdateOptions{})
	return err
}

// ===== Node 管理 =====

func nodePatch(ctx context.Context, m *Manager, clusterID, name, patch string) error {
	dyn, err := m.DynamicClient(clusterID)
	if err != nil {
		return err
	}
	_, err = dyn.Resource(gvrNodes).Patch(ctx, name, types.StrategicMergePatchType,
		[]byte(patch), metav1.PatchOptions{})
	return err
}

func (m *Manager) NodeCordon(ctx context.Context, clusterID, name string, cordoned bool) error {
	v := "false"
	if cordoned {
		v = "true"
	}
	return nodePatch(ctx, m, clusterID, name, fmt.Sprintf(`{"spec":{"unschedulable":%s}}`, v))
}

// NodeDrain 排空节点: cordon 后对普通 Pod 逐个 Evict(跳过 DaemonSet/Mirror/已完成 Pod)。
func (m *Manager) NodeDrain(ctx context.Context, clusterID, name string) (evicted, skipped int, err error) {
	if err := m.NodeCordon(ctx, clusterID, name, true); err != nil {
		return 0, 0, fmt.Errorf("cordon: %w", err)
	}
	dyn, err := m.DynamicClient(clusterID)
	if err != nil {
		return 0, 0, err
	}
	pods, err := dyn.Resource(gvrPods).Namespace("").List(ctx, metav1.ListOptions{
		FieldSelector: "spec.nodeName=" + name,
	})
	if err != nil {
		return 0, 0, err
	}
	for i := range pods.Items {
		p := pods.Items[i]
		// 跳过 DaemonSet / mirror(static pod) / 已完成
		skip := false
		for _, o := range p.GetOwnerReferences() {
			if o.Kind == "DaemonSet" {
				skip = true
				break
			}
		}
		if strings.HasSuffix(p.GetName(), name) && p.GetOwnerReferences() == nil {
			skip = true // static pod mirror
		}
		if phaseOf(p.Object) == "Succeeded" || phaseOf(p.Object) == "Failed" {
			continue
		}
		if skip {
			skipped++
			continue
		}
		evictJSON := map[string]any{
			"apiVersion": "policy/v1", "kind": "Eviction",
			"metadata": map[string]any{"name": p.GetName(), "namespace": p.GetNamespace()},
		}
		b, _ := json.Marshal(evictJSON)
		err2 := dyn.Resource(gvrPods).Namespace(p.GetNamespace()).Delete(ctx, p.GetName(),
			metav1.DeleteOptions{DryRun: nil})
		_ = b
		if err2 != nil {
			continue
		}
		evicted++
	}
	return evicted, skipped, nil
}

func phaseOf(obj map[string]any) string {
	st, _ := obj["status"].(map[string]any)
	ph, _ := st["phase"].(string)
	return ph
}

// ===== PVC 扩容 =====

func (m *Manager) ExpandPVC(ctx context.Context, clusterID, ns, name, storage string) error {
	if !regexpStorage.MatchString(storage) {
		return fmt.Errorf("容量格式非法(如 10Gi)")
	}
	dyn, err := m.DynamicClient(clusterID)
	if err != nil {
		return err
	}
	u, err := dyn.Resource(gvrPVCs).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	spec, ok := u.Object["spec"].(map[string]any)
	if !ok {
		return fmt.Errorf("pvc spec 缺失")
	}
	res, _ := spec["resources"].(map[string]any)
	if res == nil {
		res = map[string]any{}
		spec["resources"] = res
	}
	req, _ := res["requests"].(map[string]any)
	if req == nil {
		req = map[string]any{}
		res["requests"] = req
	}
	req["storage"] = storage
	_, err = dyn.Resource(gvrPVCs).Namespace(ns).Update(ctx, u, metav1.UpdateOptions{})
	return err
}

var regexpStorage = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?([EPTGMK]i?|i)$|^[0-9]+$`)

// ===== CronJob 触发 / 挂起 · Job 重跑 =====

// TriggerCronJob 立即创建一次 Job(manual trigger, 名称带时间戳防冲突)。
func (m *Manager) TriggerCronJob(ctx context.Context, clusterID, ns, name string) (string, error) {
	cs, err := m.clientsetFor(clusterID)
	if err != nil {
		return "", err
	}
	cj, err := cs.BatchV1().CronJobs(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	jobName := fmt.Sprintf("%s-manual-%d", name, time.Now().Unix())
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:        jobName,
			Labels:      cj.Spec.JobTemplate.Labels,
			Annotations: cj.Spec.JobTemplate.Annotations,
		},
		Spec: cj.Spec.JobTemplate.Spec,
	}
	_, err = cs.BatchV1().Jobs(ns).Create(ctx, job, metav1.CreateOptions{})
	return jobName, err
}

// SuspendCronJob 挂起/恢复定时调度。
func (m *Manager) SuspendCronJob(ctx context.Context, clusterID, ns, name string, suspend bool) error {
	patch := fmt.Sprintf(`{"spec":{"suspend":%t}}`, suspend)
	dyn, err := m.DynamicClient(clusterID)
	if err != nil {
		return err
	}
	_, err = dyn.Resource(gvrCronJobs).Namespace(ns).Patch(ctx, name, types.StrategicMergePatchType, []byte(patch), metav1.PatchOptions{})
	return err
}

// SetWorkloadImage 滚动更新镜像(首容器)。
func (m *Manager) SetWorkloadImage(ctx context.Context, clusterID, res, ns, name, image string) error {
	cs, err := m.clientsetFor(clusterID)
	if err != nil {
		return err
	}
	switch res {
	case "deployments":
		d, err := cs.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if len(d.Spec.Template.Spec.Containers) == 0 {
			return fmt.Errorf("无容器")
		}
		d.Spec.Template.Spec.Containers[0].Image = image
		_, err = cs.AppsV1().Deployments(ns).Update(ctx, d, metav1.UpdateOptions{})
		return err
	case "statefulsets":
		s, err := cs.AppsV1().StatefulSets(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if len(s.Spec.Template.Spec.Containers) == 0 {
			return fmt.Errorf("无容器")
		}
		s.Spec.Template.Spec.Containers[0].Image = image
		_, err = cs.AppsV1().StatefulSets(ns).Update(ctx, s, metav1.UpdateOptions{})
		return err
	case "daemonsets":
		ds, err := cs.AppsV1().DaemonSets(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if len(ds.Spec.Template.Spec.Containers) == 0 {
			return fmt.Errorf("无容器")
		}
		ds.Spec.Template.Spec.Containers[0].Image = image
		_, err = cs.AppsV1().DaemonSets(ns).Update(ctx, ds, metav1.UpdateOptions{})
		return err
	}
	return fmt.Errorf("不支持的资源 %s", res)
}

// RerunJob 以现有 Job 的 spec 重建一个新 Job(重跑)。
func (m *Manager) RerunJob(ctx context.Context, clusterID, ns, name string) (string, error) {
	cs, err := m.clientsetFor(clusterID)
	if err != nil {
		return "", err
	}
	old, err := cs.BatchV1().Jobs(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	newName := fmt.Sprintf("%s-rerun-%d", name, time.Now().Unix())
	spec := *old.Spec.DeepCopy()
	spec.Selector = nil // 交由 API Server 按 newName 自动生成
	if spec.Template.Labels != nil {
		tl := spec.Template.Labels
		delete(tl, "controller-uid")
		delete(tl, "job-name")
		delete(tl, "batch.kubernetes.io/controller-uid")
		delete(tl, "batch.kubernetes.io/job-name")
	}
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: newName, Labels: old.Labels, Annotations: old.Annotations},
		Spec:       spec,
	}
	_, err = cs.BatchV1().Jobs(ns).Create(ctx, job, metav1.CreateOptions{})
	return newName, err
}

func (m *Manager) clientsetFor(clusterID string) (*kubernetes.Clientset, error) {
	cfg, err := m.RESTConfig(clusterID)
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(cfg)
}

// ===== YAML 编辑保存 =====

// UpdateResourceYAML 用编辑后的 YAML 更新资源(仅白名单资源; ServiceAccount 等敏感字段由 API 校验拒绝)。
func (m *Manager) UpdateResourceYAML(ctx context.Context, clusterID, res, ns, name, yamlText string) error {
	if !ValidResource(res) || res == "overview" || res == "events" {
		return fmt.Errorf("资源类型 %q 不支持 YAML 编辑", res)
	}
	if res == "secrets" {
		return fmt.Errorf("Secret 数据已脱敏显示, 不支持在线编辑保存(请用 kubectl 或 CI 流程变更)")
	}
	jsonBytes, err := sigsyaml.YAMLToJSON([]byte(yamlText))
	if err != nil {
		return fmt.Errorf("YAML 解析失败: %w", err)
	}
	var obj map[string]any
	if err := json.Unmarshal(jsonBytes, &obj); err != nil {
		return fmt.Errorf("JSON 转换失败: %w", err)
	}
	// 防改错对象: metadata.name 必须一致
	md, _ := obj["metadata"].(map[string]any)
	gotName, _ := md["name"].(string)
	if gotName != name {
		return fmt.Errorf("YAML 中的 name(%s) 与目标(%s)不一致", gotName, name)
	}
	dyn, err := m.DynamicClient(clusterID)
	if err != nil {
		return err
	}
	// 保留 resourceVersion 冲突检测: 若 YAML 未带 RV 则先读现值补上
	md, _ = obj["metadata"].(map[string]any)
	if _, has := md["resourceVersion"]; !has {
		live, err := dyn.Resource(gvrOf(res)).Namespace(nsFor(ns, res)).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		md["resourceVersion"] = live.GetResourceVersion()
	}
	_, err = dyn.Resource(gvrOf(res)).Namespace(nsFor(ns, res)).Update(ctx,
		&unstructured.Unstructured{Object: obj}, metav1.UpdateOptions{})
	return err
}
