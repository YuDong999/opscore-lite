package kubernetes

// metrics.k8s.io 只读采集: 节点/Pod 实时 CPU 与内存用量(metrics-server 数据源)。
// 通过 kubeconfig 直读 API, 不依赖 Prometheus。

import (
	"context"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	gvrNodeMetrics = schema.GroupVersionResource{Group: "metrics.k8s.io", Version: "v1beta1", Resource: "nodes"}
	gvrPodMetrics  = schema.GroupVersionResource{Group: "metrics.k8s.io", Version: "v1beta1", Resource: "pods"}
)

type NodeMetric struct {
	Name     string  `json:"name"`
	CPUMilli int64   `json:"cpuMilli"`
	MemMiB   float64 `json:"memMiB"`
	CPUPct   float64 `json:"cpuPct"` // 相对 allocatable
	MemPct   float64 `json:"memPct"`
}

type PodMetric struct {
	Namespace string  `json:"namespace"`
	Name      string  `json:"name"`
	CPUMilli  int64   `json:"cpuMilli"`
	MemMiB    float64 `json:"memMiB"`
}

// parseCPUMilli 解析 CPU 用量(如 "163742918n")为毫核。
func parseCPUMilli(v any) int64 {
	s, ok := v.(string)
	if !ok {
		return 0
	}
	q, err := resource.ParseQuantity(s)
	if err != nil {
		return 0
	}
	return q.MilliValue()
}

// parseMemBytes 解析内存用量(如 "3879Mi")为字节。
func parseMemBytes(v any) int64 {
	s, ok := v.(string)
	if !ok {
		return 0
	}
	q, err := resource.ParseQuantity(s)
	if err != nil {
		return 0
	}
	return q.Value()
}

// GetNodeMetrics 全部节点实时用量
func (m *Manager) GetNodeMetrics(ctx context.Context, clusterID string) ([]NodeMetric, error) {
	dyn, err := m.DynamicClient(clusterID)
	if err != nil {
		return nil, err
	}
	list, err := dyn.Resource(gvrNodeMetrics).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("node metrics: %w", err)
	}
	nodes, _ := dyn.Resource(gvrNodes).List(ctx, metav1.ListOptions{})
	alloc := map[string][2]int64{} // cpu milli, mem MiB
	if nodes != nil {
		for i := range nodes.Items {
			var n corev1.Node
			if runtime.DefaultUnstructuredConverter.FromUnstructured(nodes.Items[i].Object, &n) == nil {
				cpu := n.Status.Allocatable.Cpu().MilliValue()
				mem := n.Status.Allocatable.Memory().Value() / (1024 * 1024) // MiB
				alloc[n.Name] = [2]int64{cpu, mem}
			}
		}
	}
	out := make([]NodeMetric, 0, len(list.Items))
	for i := range list.Items {
		it := list.Items[i]
		usage, _ := it.Object["usage"].(map[string]any)
		nm := NodeMetric{
			Name:     it.GetName(),
			CPUMilli: parseCPUMilli(usage["cpu"]),
			MemMiB:   float64(parseMemBytes(usage["memory"])) / (1024 * 1024),
		}
		if a, ok := alloc[it.GetName()]; ok {
			if a[0] > 0 {
				nm.CPUPct = float64(nm.CPUMilli) / float64(a[0]) * 100
			}
			if a[1] > 0 {
				nm.MemPct = nm.MemMiB / float64(a[1]) * 100
			}
		}
		out = append(out, nm)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// GetPodMetrics 命名空间(空=全部)Pod 用量, 按 CPU 降序。
func (m *Manager) GetPodMetrics(ctx context.Context, clusterID, ns string) ([]PodMetric, error) {
	dyn, err := m.DynamicClient(clusterID)
	if err != nil {
		return nil, err
	}
	list, err := dyn.Resource(gvrPodMetrics).Namespace(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("pod metrics: %w", err)
	}
	out := make([]PodMetric, 0, len(list.Items))
	for i := range list.Items {
		it := list.Items[i]
		pm := PodMetric{Namespace: it.GetNamespace(), Name: it.GetName()}
		containers, _ := it.Object["containers"].([]any)
		for _, c := range containers {
			cm, _ := c.(map[string]any)
			usage, _ := cm["usage"].(map[string]any)
			pm.CPUMilli += parseCPUMilli(usage["cpu"])
			pm.MemMiB += float64(parseMemBytes(usage["memory"])) / (1024 * 1024)
		}
		out = append(out, pm)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CPUMilli > out[j].CPUMilli })
	return out, nil
}
