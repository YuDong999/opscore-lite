package kubernetes

// 新增的写操作: 任意资源 patch / label / annotate / 边车 add/remove
// 这些动作是 action_catalog.go 里 catalog patch/label/annotate/ephemeral-add/ephemeral-remove
// 的实际实现。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// PatchResource 任意资源走 PATCH, 4 种 patchType
//   - "merge": application/merge-patch+json (RFC 7396)
//   - "strategic-merge": application/strategic-merge-patch+json (K8s 默认, 推荐)
//   - "json": application/json-patch+json (RFC 6902 JSON Patch)
//   - "json-patch": 同上 (别名)
func (m *Manager) PatchResource(ctx context.Context, clusterID, res, ns, name, patchType, patchBody string) error {
	if res == "overview" || res == "events" {
		return fmt.Errorf("资源类型 %q 不支持 patch", res)
	}
	if patchType == "" {
		patchType = "strategic-merge"
	}
	var pt types.PatchType
	switch patchType {
	case "merge":
		pt = types.MergePatchType
	case "strategic-merge", "strategic_merge", "strategic":
		pt = types.StrategicMergePatchType
	case "json", "json-patch", "json_patch":
		pt = types.JSONPatchType
	default:
		return fmt.Errorf("不支持的 patchType: %s (merge/strategic-merge/json)", patchType)
	}
	gvr, scope, err := m.ResolveGVR(clusterID, res)
	if err != nil {
		return fmt.Errorf("资源类型 %q 不支持 patch: %w", res, err)
	}
	effectiveNs := ns
	if scope == ScopeCluster {
		effectiveNs = ""
	}
	dyn, err := m.DynamicClient(clusterID)
	if err != nil {
		return err
	}
	_, err = dyn.Resource(gvr).Namespace(effectiveNs).Patch(ctx, name, pt, []byte(patchBody), metav1.PatchOptions{})
	return err
}

// LabelResource 任意资源打/改/删 label (走 strategic-merge patch)
//   - value 为空时移除该 label
//   - overwrite=false 时若 key 已存在则报错 (符合 kubectl label 行为)
func (m *Manager) LabelResource(ctx context.Context, clusterID, res, ns, name, key, value string, overwrite bool) error {
	return m.metaKV(ctx, clusterID, res, ns, name, "labels", key, value, overwrite)
}

// AnnotateResource 同 LabelResource 但操作 annotations
func (m *Manager) AnnotateResource(ctx context.Context, clusterID, res, ns, name, key, value string, overwrite bool) error {
	return m.metaKV(ctx, clusterID, res, ns, name, "annotations", key, value, overwrite)
}

func (m *Manager) metaKV(ctx context.Context, clusterID, res, ns, name, field, key, value string, overwrite bool) error {
	if res == "overview" || res == "events" {
		return fmt.Errorf("资源类型 %q 不支持", res)
	}
	if key == "" {
		return fmt.Errorf("缺少 key")
	}
	gvr, scope, err := m.ResolveGVR(clusterID, res)
	if err != nil {
		return fmt.Errorf("资源类型 %q 不支持: %w", res, err)
	}
	effectiveNs := ns
	if scope == ScopeCluster {
		effectiveNs = ""
	}
	dyn, err := m.DynamicClient(clusterID)
	if err != nil {
		return err
	}
	// 读现值, 检查 overwrite / value 处理
	cur, err := dyn.Resource(gvr).Namespace(effectiveNs).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("metaKV get(gvr=%s ns=%q name=%q): %w", gvr.String(), effectiveNs, name, err)
	}
	var meta map[string]any
	if cur.Object["metadata"] != nil {
		meta, _ = cur.Object["metadata"].(map[string]any)
	}
	if meta == nil {
		meta = map[string]any{}
	}
	md, _ := meta[field].(map[string]any)
	if md == nil {
		md = map[string]any{}
	}
	if _, exists := md[key]; exists && !overwrite && value != "" {
		return fmt.Errorf("%s.%s 已存在, 未启用 overwrite", field, key)
	}
	// 构造 patch
	//   删除: {"metadata":{"labels":{"$patch":"replace","k":null}}}
	//   写值: {"metadata":{"labels":{"k":"v"}}}
	var patchMap map[string]any
	if value == "" {
		patchMap = map[string]any{
			"metadata": map[string]any{
				field: map[string]any{
					key: nil,
				},
			},
		}
	} else {
		patchMap = map[string]any{
			"metadata": map[string]any{
				field: map[string]any{
					key: value,
				},
			},
		}
	}
	body, _ := json.Marshal(patchMap)
	_, err = dyn.Resource(gvr).Namespace(effectiveNs).Patch(ctx, name, types.MergePatchType, body, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("metaKV patch(gvr=%s ns=%q name=%q): %w", gvr.String(), effectiveNs, name, err)
	}
	return err
}

// EphemeralAdd 给 Pod 加一个临时容器 (kubectl debug 等价)
//   - K8s ≥ 1.23 强烈推荐传 targetContainerName, 共享其网络/PID 命名空间
//   - command 形式为 shell 字符串, 我们拆为 ["/bin/sh","-c",cmd]; 也允许空则原样
func (m *Manager) EphemeralAdd(ctx context.Context, clusterID, ns, pod, containerName, image, command, targetContainerName string) error {
	if !EphemeralContainersAllowed() {
		return fmt.Errorf("平台侧关闭了边车容器 (OPSCORE_K8S_EPHEMERAL_ALLOW=false 或未设置)")
	}
	cs, err := m.clientsetFor(clusterID)
	if err != nil {
		return err
	}
	p, err := cs.CoreV1().Pods(ns).Get(ctx, pod, metav1.GetOptions{})
	if err != nil {
		return err
	}
	// 重名检查
	for _, ec := range p.Spec.EphemeralContainers {
		if ec.Name == containerName {
			return fmt.Errorf("边车容器 %q 已存在", containerName)
		}
	}
	for _, c := range p.Spec.Containers {
		if c.Name == containerName {
			return fmt.Errorf("与已有容器 %q 重名", containerName)
		}
	}
	ec := corev1.EphemeralContainer{
		EphemeralContainerCommon: corev1.EphemeralContainerCommon{
			Name:    containerName,
			Image:   image,
			Stdin:   true,
			TTY:     true,
		},
		TargetContainerName: targetContainerName,
	}
	if strings.TrimSpace(command) != "" {
		ec.Command = []string{"/bin/sh", "-c", command}
	}
	p.Spec.EphemeralContainers = append(p.Spec.EphemeralContainers, ec)
	_, err = cs.CoreV1().Pods(ns).UpdateEphemeralContainers(ctx, pod, p, metav1.UpdateOptions{})
	return err
}

// EphemeralRemove 把指定边车容器从 Pod.Spec.EphemeralContainers 移除
// K8s 没有专门 delete subresource, 通过 UpdateEphemeralContainers 把对应 name 删掉即可
func (m *Manager) EphemeralRemove(ctx context.Context, clusterID, ns, pod, containerName string) error {
	if !EphemeralContainersAllowed() {
		return fmt.Errorf("平台侧关闭了边车容器")
	}
	cs, err := m.clientsetFor(clusterID)
	if err != nil {
		return err
	}
	p, err := cs.CoreV1().Pods(ns).Get(ctx, pod, metav1.GetOptions{})
	if err != nil {
		return err
	}
	kept := p.Spec.EphemeralContainers[:0]
	found := false
	for _, ec := range p.Spec.EphemeralContainers {
		if ec.Name == containerName {
			found = true
			continue
		}
		kept = append(kept, ec)
	}
	if !found {
		return fmt.Errorf("边车容器 %q 不存在", containerName)
	}
	p.Spec.EphemeralContainers = kept
	_, err = cs.CoreV1().Pods(ns).UpdateEphemeralContainers(ctx, pod, p, metav1.UpdateOptions{})
	return err
}
