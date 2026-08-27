// Package kubernetes 提供与宿主解耦的多集群管理能力。
// 参考 kubevision(MIT) 的 Manager 模式: 注册 kubeconfig → rest.Config + dynamic client,
// 上层 handlers 只依赖本包公开接口, 联邦模式(opscore-lite 插件)与独立模式(cmd/kubemod)共用。
package kubernetes

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/version"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const probeTimeout = 5 * time.Second

// Info 集群元信息(不含凭据)。
type Info struct {
	ID        string `json:"id"`
	APIServer string `json:"apiServer"`
	Version   string `json:"version"`
}

type clientSet struct {
	dynamicClient dynamic.Interface
	restConfig    *rest.Config
}

// Manager 管理多个已注册集群的客户端连接。并发安全。
type Manager struct {
	mu       sync.RWMutex
	clusters map[string]*clientSet
}

func NewManager() *Manager {
	return &Manager{clusters: make(map[string]*clientSet)}
}

// Add 解析 kubeconfig 字节并注册集群。解析或建连失败返回错误且不改变现有状态。
func (m *Manager) Add(id string, kubeconfigData []byte) error {
	restCfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfigData)
	if err != nil {
		return fmt.Errorf("parse kubeconfig %q: %w", id, err)
	}
	dyn, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		return fmt.Errorf("create dynamic client %q: %w", id, err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clusters[id] = &clientSet{dynamicClient: dyn, restConfig: restCfg}
	return nil
}

// Remove 注销集群。
func (m *Manager) Remove(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.clusters, id)
}

// DynamicClient 返回指定集群的 dynamic 客户端。
func (m *Manager) DynamicClient(id string) (dynamic.Interface, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cs, ok := m.clusters[id]
	if !ok {
		return nil, fmt.Errorf("cluster %q not found", id)
	}
	return cs.dynamicClient, nil
}

// RESTConfig 返回指定集群的 rest.Config 副本(用于按需构建 typed clientset 等)。
func (m *Manager) RESTConfig(id string) (*rest.Config, error) {
	m.mu.RLock()
	cs, ok := m.clusters[id]
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("cluster %q not found", id)
	}
	return rest.CopyConfig(cs.restConfig), nil
}

// Probe 探测 API Server 连通性并返回版本信息。仅访问 /api 与 /version, 只读凭据即可。
func (m *Manager) Probe(ctx context.Context, id string) (*Info, error) {
	cfg, err := m.RESTConfig(id)
	if err != nil {
		return nil, err
	}
	cfg.Timeout = probeTimeout
	dc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("create discovery client %q: %w", id, err)
	}
	pctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	if _, err := dc.RESTClient().Get().AbsPath("/api").DoRaw(pctx); err != nil {
		if apierrors.IsUnauthorized(err) {
			return nil, fmt.Errorf("authenticate to %s: %w", cfg.Host, err)
		}
		return nil, fmt.Errorf("connect to %s: %w", cfg.Host, err)
	}
	raw, err := dc.RESTClient().Get().AbsPath("/version").DoRaw(pctx)
	if err != nil {
		if apierrors.IsUnauthorized(err) {
			return nil, fmt.Errorf("authenticate to %s: %w", cfg.Host, err)
		}
		return nil, fmt.Errorf("get version of %s: %w", cfg.Host, err)
	}
	var vi version.Info
	if err := json.Unmarshal(raw, &vi); err != nil {
		return nil, fmt.Errorf("decode version of %s: %w", id, err)
	}
	return &Info{ID: id, APIServer: cfg.Host, Version: vi.GitVersion}, nil
}

// ListIDs 返回全部已注册集群 ID。
func (m *Manager) ListIDs() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ids := make([]string, 0, len(m.clusters))
	for id := range m.clusters {
		ids = append(ids, id)
	}
	return ids
}
