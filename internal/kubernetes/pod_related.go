package kubernetes

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

// PodLog 返回指定容器近 N 行日志(单次, 非流式)。
func (m *Manager) PodLog(ctx context.Context, clusterID, ns, name, container string, tail int64) (string, error) {
	cs, err := m.clientsetFor(clusterID)
	if err != nil {
		return "", err
	}
	opts := &corev1.PodLogOptions{Container: container}
	if tail > 0 {
		opts.TailLines = &tail
	}
	req := cs.CoreV1().Pods(ns).GetLogs(name, opts)
	b, err := req.Do(ctx).Raw()
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// PodExec 单次执行命令并回显 stdout/stderr。
func (m *Manager) PodExec(ctx context.Context, clusterID, ns, name, container string, command []string) (string, string, error) {
	if len(command) == 0 {
		return "", "", fmt.Errorf("command 不能为空")
	}
	cs, err := m.clientsetFor(clusterID)
	if err != nil {
		return "", "", err
	}
	cfg, err := m.RESTConfig(clusterID)
	if err != nil {
		return "", "", err
	}
	req := cs.CoreV1().RESTClient().Post().
		Resource("pods").Namespace(ns).Name(name).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Command:   command,
			Container: container,
			Stdin:     false,
			Stdout:    true,
			Stderr:    true,
			TTY:       false,
		}, scheme.ParameterCodec)
	exec, err := remotecommand.NewSPDYExecutor(cfg, "POST", req.URL())
	if err != nil {
		return "", "", err
	}
	var stdout, stderr bytes.Buffer
	err = exec.StreamWithContext(ctx, remotecommand.StreamOptions{Stdout: &stdout, Stderr: &stderr})
	return stdout.String(), stderr.String(), err
}

// PodRelated 返回 Pod 的关联只读信息: 命中它的 NetworkPolicy + 使用的 PVC 链路。
func (m *Manager) PodRelated(ctx context.Context, clusterID, ns, name string) (map[string]any, error) {
	cs, err := m.clientsetFor(clusterID)
	if err != nil {
		return nil, err
	}
	pod, err := cs.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	// NetworkPolicy 命中判定(labelSelector 匹配 Pod 标签)
	nps, _ := cs.NetworkingV1().NetworkPolicies(ns).List(ctx, metav1.ListOptions{})
	matched := make([]map[string]any, 0)
	if nps != nil {
		ls := labels.Set(pod.Labels)
		for _, np := range nps.Items {
			sel, err := metav1.LabelSelectorAsSelector(&np.Spec.PodSelector)
			if err != nil {
				continue
			}
			if sel.Matches(ls) {
				matched = append(matched, map[string]any{
					"name": np.Name, "namespace": np.Namespace,
					"ingress": len(np.Spec.Ingress), "egress": len(np.Spec.Egress),
				})
			}
		}
	}

	// PVC 链路: Pod volumes 中 pvc 类型的 claim → PVC 对象 → PV/SC
	pvcs := make([]map[string]any, 0)
	seen := map[string]bool{}
	for _, v := range pod.Spec.Volumes {
		if v.PersistentVolumeClaim == nil || v.PersistentVolumeClaim.ClaimName == "" {
			continue
		}
		claimName := v.PersistentVolumeClaim.ClaimName
		if seen[claimName] {
			continue
		}
		seen[claimName] = true
		pvc, err := cs.CoreV1().PersistentVolumeClaims(ns).Get(ctx, claimName, metav1.GetOptions{})
		if err != nil {
			pvcs = append(pvcs, map[string]any{"name": claimName, "error": err.Error()})
			continue
		}
		row := map[string]any{
			"name": pvc.Name, "namespace": pvc.Namespace,
			"status": string(pvc.Status.Phase),
			"capacity": pvcString(pvc.Status.Capacity.Storage()),
			"storageClass": scName(pvc.Spec.StorageClassName),
			"volumeName": pvc.Spec.VolumeName,
		}
		if pvc.Spec.VolumeName != "" {
			if pv, err := cs.CoreV1().PersistentVolumes().Get(ctx, pvc.Spec.VolumeName, metav1.GetOptions{}); err == nil {
				row["pvCapacity"] = pvcString(pv.Spec.Capacity.Storage())
				row["pvReclaim"] = string(pv.Spec.PersistentVolumeReclaimPolicy)
				row["pvStatus"] = string(pv.Status.Phase)
				if pv.Spec.CSI != nil {
					row["csiDriver"] = pv.Spec.CSI.Driver
				}
			}
		}
		// 若 PVC 无 sc, 尝试从 PV 读取
		if row["storageClass"] == "" && row["volumeName"] != "" {
			if pv, err := cs.CoreV1().PersistentVolumes().Get(ctx, fmt.Sprint(row["volumeName"]), metav1.GetOptions{}); err == nil {
				row["storageClass"] = pv.Spec.StorageClassName
			}
		}
		pvcs = append(pvcs, row)
	}
	return map[string]any{
		"networkPolicies": matched,
		"pvcs":            pvcs,
	}, nil
}

func pvcString(q interface{ String() string }) string {
	if q == nil {
		return "—"
	}
	s := q.String()
	if s == "0" || s == "" {
		return "—"
	}
	return s
}

func scName(p *string) string {
	if p == nil || *p == "" {
		return "—"
	}
	return *p
}

// ensure rest import used
var _ = rest.Config{}
var _ = netv1.NetworkPolicySpec{}
var _ = strings.Contains
