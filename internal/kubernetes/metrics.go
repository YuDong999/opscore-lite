package kubernetes

// metrics.k8s.io 只读采集: 节点/Pod 实时 CPU 与内存用量(metrics-server 数据源)。
// 通过 kubeconfig 直读 API, 不依赖 Prometheus。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
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
	// kubelet summary(nodefs): 磁盘可用率与驱逐判定(驱逐线按节点实际 evictionHard 配置)
	DiskOK       bool    `json:"diskOK"`
	DiskAvailPct float64 `json:"diskAvailPct"`
	DiskUsedGiB  float64 `json:"diskUsedGiB"`
	DiskCapGiB   float64 `json:"diskCapGiB"`
	DiskEvictPct float64 `json:"diskEvictPct"` // nodefs.available 驱逐线(%, 默认 10 兜底)
	DiskPressure bool    `json:"diskPressure"` // Node condition DiskPressure=True
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
	hosts := map[string]string{}   // node name → InternalIP (kubelet summary)
	pressure := map[string]bool{}  // node name → DiskPressure condition
	if nodes != nil {
		for i := range nodes.Items {
			var n corev1.Node
			if runtime.DefaultUnstructuredConverter.FromUnstructured(nodes.Items[i].Object, &n) == nil {
				cpu := n.Status.Allocatable.Cpu().MilliValue()
				mem := n.Status.Allocatable.Memory().Value() / (1024 * 1024) // MiB
				alloc[n.Name] = [2]int64{cpu, mem}
				for _, a := range n.Status.Addresses {
					if a.Type == corev1.NodeInternalIP {
						hosts[n.Name] = a.Address
					}
				}
				for _, c := range n.Status.Conditions {
					if c.Type == corev1.NodeDiskPressure {
						pressure[n.Name] = c.Status == corev1.ConditionTrue
					}
				}
			}
		}
	}
	disk := m.kubeletNodeFS(ctx, clusterID, hosts)
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
		if d, ok := disk[it.GetName()]; ok {
			nm.DiskOK = true
			nm.DiskAvailPct = d.availPct
			nm.DiskUsedGiB = d.usedGiB
			nm.DiskCapGiB = d.capGiB
			nm.DiskEvictPct = d.evictPct
			nm.DiskPressure = pressure[it.GetName()]
		}
		out = append(out, nm)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

type nodeDisk struct {
	availPct float64
	usedGiB  float64
	capGiB   float64
	evictPct float64
}

// kubeletNodeFS 直连节点 10250 /stats/summary 取 nodefs 磁盘用量, /configz 取实际驱逐线。
// 复用 kubeconfig 的 TLS 材料(与 API server 同证书体系, kubelet 信任); 失败节点静默跳过。
func (m *Manager) kubeletNodeFS(ctx context.Context, clusterID string, hosts map[string]string) map[string]nodeDisk {
	out := map[string]nodeDisk{}
	if len(hosts) == 0 {
		return out
	}
	cfg, err := m.RESTConfig(clusterID)
	if err != nil {
		return out
	}
	rt, err := rest.TransportFor(cfg)
	if err != nil {
		return out
	}
	client := &http.Client{Timeout: 6 * time.Second, Transport: rt}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for name, ip := range hosts {
		wg.Add(1)
		go func(name, ip string) {
			defer wg.Done()
			d := nodeDisk{evictPct: 10}
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+ip+":10250/stats/summary", nil)
			if err != nil {
				return
			}
			resp, err := client.Do(req)
			if err != nil {
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return
			}
			var s struct {
				Node struct {
					FS struct {
						AvailableBytes int64 `json:"availableBytes"`
						CapacityBytes  int64 `json:"capacityBytes"`
					} `json:"fs"`
				} `json:"node"`
			}
			if json.NewDecoder(resp.Body).Decode(&s) != nil {
				return
			}
			avail := float64(s.Node.FS.AvailableBytes)
			cap := float64(s.Node.FS.CapacityBytes)
			d.usedGiB = (cap - avail) / (1 << 30)
			d.capGiB = cap / (1 << 30)
			if cap > 0 {
				d.availPct = avail / cap * 100
			}
			// 实际驱逐线: kubelet /configz → evictionHard.nodefs.available (e.g. "10%")
			if req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+ip+":10250/configz", nil); err == nil {
				if cr, err := client.Do(req); err == nil {
					if cr.StatusCode == http.StatusOK {
						var cz struct {
							Kubeletconfig struct {
								EvictionHard map[string]string `json:"evictionHard"`
							} `json:"kubeletconfig"`
						}
						if json.NewDecoder(cr.Body).Decode(&cz) == nil {
							if v := cz.Kubeletconfig.EvictionHard["nodefs.available"]; v != "" {
								if f, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(v), "%"), 64); err == nil && f > 0 {
									d.evictPct = f
								}
							}
						}
					}
					cr.Body.Close()
				}
			}
			mu.Lock()
			out[name] = d
			mu.Unlock()
		}(name, ip)
	}
	wg.Wait()
	return out
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

// ===== metrics-server 缺失时的兜底: API server node proxy → kubelet /stats/summary =====
// kubelet 内置 /stats/summary(含内嵌 cAdvisor 的节点/Pod 用量 + nodefs 磁盘), 有节点就有数据, 无需任何 add-on。
// 走 API server 的 node proxy(鉴权/证书由 apiserver→kubelet 既有体系处理), 不碰 kubelet 自签证书问题。

type summaryDisk struct {
	availPct float64
	usedGiB  float64
	capGiB   float64
	evictPct float64
}

// SummaryMetrics 兜底采集: 每节点经 apiserver proxy 读 /stats/summary,
// 返回节点/Pod 实时用量 + nodefs 磁盘(顺带补齐主路径里直连 10250 常失败的那列)。
func (m *Manager) SummaryMetrics(ctx context.Context, clusterID string) ([]NodeMetric, []PodMetric, map[string]summaryDisk, error) {
	dyn, err := m.DynamicClient(clusterID)
	if err != nil {
		return nil, nil, nil, err
	}
	nodeRes := dyn.Resource(gvrNodes)
	nodesList, err := nodeRes.List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("nodes: %w", err)
	}
	// node proxy 走 RESTClient 原始字节: 动态客户端会强制解码资源对象(要求 kind), 而 summary 不是 k8s 资源
	cfgBase, err := m.RESTConfig(clusterID)
	if err != nil {
		return nil, nil, nil, err
	}
	gv := schema.GroupVersion{Group: "", Version: "v1"}
	cfgV1 := *cfgBase
	cfgV1.GroupVersion = &gv
	cfgV1.APIPath = "/api"
	cfgV1.NegotiatedSerializer = scheme.Codecs.WithoutConversion()
	restClient, err := rest.RESTClientFor(&cfgV1)
	if err != nil {
		return nil, nil, nil, err
	}
	type nodeCtx struct {
		name   string
		alloc  [2]int64 // cpu milli, mem MiB
		press  bool
	}
	var targets []nodeCtx
	for i := range nodesList.Items {
		var n corev1.Node
		if runtime.DefaultUnstructuredConverter.FromUnstructured(nodesList.Items[i].Object, &n) != nil {
			continue
		}
		nc := nodeCtx{name: n.Name}
		nc.alloc[0] = n.Status.Allocatable.Cpu().MilliValue()
		nc.alloc[1] = n.Status.Allocatable.Memory().Value() / (1024 * 1024)
		for _, c := range n.Status.Conditions {
			if c.Type == corev1.NodeDiskPressure {
				nc.press = c.Status == corev1.ConditionTrue
			}
		}
		targets = append(targets, nc)
	}

	var mu sync.Mutex
	var nodesOut []NodeMetric
	var podsOut []PodMetric
	disks := map[string]summaryDisk{}
	var firstErr error
	var wg sync.WaitGroup
	for _, nc := range targets {
		wg.Add(1)
		go func(nc nodeCtx) {
			defer wg.Done()
			// 经 apiserver node proxy 读 /stats/summary; 首错只记录不中断其余节点
			res := restClient.Get().AbsPath("/api/v1/nodes/"+nc.name+"/proxy/stats/summary").Do(ctx)
			raw, err := res.Raw()
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("%s: HTTP %d: %w", nc.name, res.StatusCode, err)
				}
				mu.Unlock()
				return
			}
			var s struct {
				Node struct {
					CPU struct {
						UsageNanoCores int64 `json:"usageNanoCores"`
					} `json:"cpu"`
					Memory struct {
						WorkingSetBytes int64 `json:"workingSetBytes"`
					} `json:"memory"`
					FS struct {
						AvailableBytes int64 `json:"availableBytes"`
						CapacityBytes  int64 `json:"capacityBytes"`
					} `json:"fs"`
					Runtime struct {
						ImageFS struct {
							AvailableBytes int64 `json:"availableBytes"`
							CapacityBytes  int64 `json:"capacityBytes"`
						} `json:"imageFs"`
					} `json:"runtime"`
				} `json:"node"`
				Pods []struct {
					PodRef struct {
						Name      string `json:"name"`
						Namespace string `json:"namespace"`
					} `json:"podRef"`
					Containers []struct {
						Usage struct {
							CPU    any `json:"cpu"`
							Memory any `json:"memory"`
						} `json:"usage"`
					} `json:"containers"`
				} `json:"pods"`
			}
			if json.NewDecoder(bytes.NewReader(raw)).Decode(&s) != nil {
				return
			}
			nm := NodeMetric{
				Name:     nc.name,
				CPUMilli: s.Node.CPU.UsageNanoCores / 1_000_000,
				MemMiB:   float64(s.Node.Memory.WorkingSetBytes) / (1024 * 1024),
			}
			if nc.alloc[0] > 0 {
				nm.CPUPct = float64(nm.CPUMilli) / float64(nc.alloc[0]) * 100
			}
			if nc.alloc[1] > 0 {
				nm.MemPct = nm.MemMiB / float64(nc.alloc[1]) * 100
			}
			avail := float64(s.Node.FS.AvailableBytes)
			cap := float64(s.Node.FS.CapacityBytes)
			sd := summaryDisk{usedGiB: (cap - avail) / (1 << 30), capGiB: cap / (1 << 30), evictPct: 10}
			if cap > 0 {
				sd.availPct = avail / cap * 100
			}
			for _, pd := range s.Pods {
				pm := PodMetric{Namespace: pd.PodRef.Namespace, Name: pd.PodRef.Name}
				for _, ct := range pd.Containers {
					pm.CPUMilli += parseCPUMilli(ct.Usage.CPU)
					pm.MemMiB += float64(parseMemBytes(ct.Usage.Memory)) / (1024 * 1024)
				}
				mu.Lock()
				podsOut = append(podsOut, pm)
				mu.Unlock()
			}
			mu.Lock()
			nodesOut = append(nodesOut, nm)
			disks[nc.name] = sd
			mu.Unlock()
		}(nc)
	}
	wg.Wait()
	if len(nodesOut) == 0 && firstErr != nil {
		return nil, nil, nil, firstErr
	}
	sort.Slice(nodesOut, func(i, j int) bool { return nodesOut[i].Name < nodesOut[j].Name })
	sort.Slice(podsOut, func(i, j int) bool { return podsOut[i].CPUMilli > podsOut[j].CPUMilli })
	return nodesOut, podsOut, disks, nil
}
