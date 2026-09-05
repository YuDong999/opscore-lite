// Package kubernetes - discovery.go
//
// 动态 CRD 发现 + RESTMapper 缓存, 让 opscore 支持任意 CRD 实例的 List/Get/Patch/Delete/Edit-YAML
// 不需要预先在 ValidResource switch 里写死.
//
// 思路:
//  1. Manager.Add 注册集群时构造 1 个 memory-backed CachedDiscoveryInterface
//  2. RESTMapper 用 DeferredDiscoveryRESTMapper 包装, 首次访问时从 cache 取
//  3. DiscoveredCRDs 主动列所有已注册 CRD, 转为 CRDInfo (shortName / group / version / kind / plural / scope)
//  4. ResolveGVR(id, res) 统一入口: 内置走 fast path (switch); CRD 走 mapper 或缓存
//  5. ValidResource(res) 在 已有内置白名单 + 集群已发现 CRD 短名 之间取并集
//
// 失败处理: ResolveGVR 找不到 res → 返回 ErrNoSuchResource, handlers 直接 400/404.
package kubernetes

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/restmapper"
)

// ScopeKind 区分 namespaced vs cluster-scoped
type ScopeKind int

const (
	ScopeUnknown ScopeKind = iota
	ScopeNamespaced
	ScopeCluster
)

// CRDInfo 描述一个已注册的 CustomResourceDefinition, 供前端渲染分组.
type CRDInfo struct {
	// ShortName 形如 "applications.argoproj.io" / "ingressroutes.traefik.io"
	// 命名: <plural>.<group>; 跨 group 也不冲突
	ShortName string `json:"shortName"`
	// Group 形如 "argoproj.io"
	Group string `json:"group"`
	// Version 当前 served 版本 (e.g. v1alpha1)
	Version string `json:"version"`
	// Versions 该 CRD 支持的全部 served 版本
	Versions []string `json:"versions"`
	// Kind 形如 "Application"
	Kind string `json:"kind"`
	// Plural 形如 "applications"
	Plural string `json:"plural"`
	// Singular 形如 "application"
	Singular string `json:"singular"`
	// Scope "Namespaced" / "Cluster"
	Scope string `json:"scope"`
}

// ErrNoSuchResource 表示 res 既不是内置也不是已发现 CRD.
var ErrNoSuchResource = fmt.Errorf("resource not found in cluster")

// discoveryState 每集群一份
type discoveryState struct {
	mu              sync.RWMutex
	cachedDisc      discovery.CachedDiscoveryInterface
	restMapper      meta.RESTMapper
	crdCache        map[string]CRDInfo
	crdCacheAt      time.Time
	crdCacheTTL     time.Duration
	refreshInFlight bool
}

const defaultCRDCacheTTL = 5 * time.Minute

// initDiscovery 在 Manager.Add 时调用一次, 构造 cached discovery + deferred RESTMapper.
func (m *Manager) initDiscovery(id string) error {
	cfg, err := m.RESTConfig(id)
	if err != nil {
		return err
	}
	dc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return fmt.Errorf("create discovery client: %w", err)
	}
	cachedDisc := memory.NewMemCacheClient(dc)
	rm := restmapper.NewDeferredDiscoveryRESTMapper(cachedDisc)

	m.mu.Lock()
	defer m.mu.Unlock()
	cs, ok := m.clusters[id]
	if !ok {
		return fmt.Errorf("cluster %q not found", id)
	}
	cs.discState = &discoveryState{
		cachedDisc:  cachedDisc,
		restMapper:  rm,
		crdCache:    make(map[string]CRDInfo),
		crdCacheTTL: defaultCRDCacheTTL,
	}
	return nil
}

// discFor 获取/懒加载集群的 discovery state.
func (m *Manager) discFor(id string) (*discoveryState, error) {
	m.mu.RLock()
	cs, ok := m.clusters[id]
	state := cs.discState
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("cluster %q not found", id)
	}
	if state != nil {
		return state, nil
	}
	if err := m.initDiscovery(id); err != nil {
		return nil, err
	}
	m.mu.RLock()
	state = m.clusters[id].discState
	m.mu.RUnlock()
	return state, nil
}

// RestMapper 返回集群的 RESTMapper, 供 GVR 解析.
func (m *Manager) RestMapper(id string) (meta.RESTMapper, error) {
	state, err := m.discFor(id)
	if err != nil {
		return nil, err
	}
	return state.restMapper, nil
}

// DiscoverCRDs 列出集群已注册的 CRD; refresh=true 强制重发现.
func (m *Manager) DiscoverCRDs(_ context.Context, id string, refresh bool) ([]CRDInfo, error) {
	state, err := m.discFor(id)
	if err != nil {
		return nil, err
	}

	state.mu.RLock()
	if !refresh && time.Since(state.crdCacheAt) < state.crdCacheTTL && len(state.crdCache) > 0 {
		out := make([]CRDInfo, 0, len(state.crdCache))
		for _, v := range state.crdCache {
			out = append(out, v)
		}
		state.mu.RUnlock()
		return out, nil
	}
	state.mu.RUnlock()

	// 排他: 多个并发只触发 1 次刷新
	state.mu.Lock()
	if state.refreshInFlight {
		state.mu.Unlock()
		time.Sleep(200 * time.Millisecond)
		return m.DiscoverCRDs(nil, id, false)
	}
	state.refreshInFlight = true
	state.mu.Unlock()
	defer func() {
		state.mu.Lock()
		state.refreshInFlight = false
		state.mu.Unlock()
	}()

	// 列全集群所有 APIResource, 过滤 "Kind 是 X 形如 X 来自 CRD" 比较复杂.
	// 更准确做法: list apiextensions.k8s.io/v1/customresourcedefinitions 拿到所有 CRD spec.
	// 用 dynamic client 列 CRD (typed clientset 没有 ApiextensionsV1 直接方法, 因为 CRD
	// 自身在 dynamic.Interface 走 unstr typed path).
	dc, dcErr := m.DynamicClient(id)
	if dcErr != nil {
		return nil, fmt.Errorf("dynamic client: %w", dcErr)
	}
	u, err := dc.Resource(schema.GroupVersionResource{
		Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions",
	}).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		// v1beta1 fallback
		ub, errBeta := dc.Resource(schema.GroupVersionResource{
			Group: "apiextensions.k8s.io", Version: "v1beta1", Resource: "customresourcedefinitions",
		}).List(context.Background(), metav1.ListOptions{})
		if errBeta != nil {
			return nil, fmt.Errorf("list CRD: v1=%w / v1beta1=%v", err, errBeta)
		}
		u = ub
	}
	infos := make([]CRDInfo, 0, len(u.Items))
	for i := range u.Items {
		crd := &u.Items[i]
		spec, _, _ := unstructured.NestedMap(crd.Object, "spec")
		group, _, _ := unstructured.NestedString(spec, "group")
		scope, _, _ := unstructured.NestedString(spec, "scope")
		names, _, _ := unstructured.NestedMap(spec, "names")
		plural, _, _ := unstructured.NestedString(names, "plural")
		singular, _, _ := unstructured.NestedString(names, "singular")
		kind, _, _ := unstructured.NestedString(names, "kind")
		versionsRaw, _, _ := unstructured.NestedSlice(spec, "versions")
		versions := make([]string, 0, len(versionsRaw))
		served := ""
		for _, v := range versionsRaw {
			vm, ok := v.(map[string]any)
			if !ok {
				continue
			}
			n, _, _ := unstructured.NestedString(vm, "name")
			if n == "" {
				continue
			}
			versions = append(versions, n)
			if s, _, _ := unstructured.NestedBool(vm, "served"); s && served == "" {
				served = n
			}
		}
		if served == "" && len(versions) > 0 {
			served = versions[0]
		}
		infos = append(infos, CRDInfo{
			ShortName: plural + "." + group,
			Group:     group,
			Version:   served,
			Versions:  versions,
			Kind:      kind,
			Plural:    plural,
			Singular:  singular,
			Scope:     scope,
		})
	}

	state.mu.Lock()
	state.crdCache = make(map[string]CRDInfo, len(infos))
	for _, c := range infos {
		state.crdCache[c.ShortName] = c
	}
	state.crdCacheAt = time.Now()
	state.mu.Unlock()
	return infos, nil
}


// LookupCRD 按 shortName 在缓存里查.
func (m *Manager) LookupCRD(id, shortName string) (CRDInfo, bool) {
	state, err := m.discFor(id)
	if err != nil {
		return CRDInfo{}, false
	}
	state.mu.RLock()
	defer state.mu.RUnlock()
	info, ok := state.crdCache[shortName]
	return info, ok
}

// IsCRDName 判断 res 是否形如 "<plural>.<group>" (含点号).
func IsCRDName(res string) bool {
	return strings.Contains(res, ".")
}

// ResolveGVR 把 res 字符串转为 GVR + Scope.
// 优先级: 内置 switch (fast path) → RESTMapper 动态查 (覆盖 CRD).
func (m *Manager) ResolveGVR(id, res string) (schema.GroupVersionResource, ScopeKind, error) {
	// 1) 内置 fast path
	if gvr := gvrOf(res); !gvr.Empty() {
		scope := ScopeCluster
		if nsFor("", res) != "" {
			scope = ScopeNamespaced
		}
		return gvr, scope, nil
	}
	// 2) CRD 路径
	if !IsCRDName(res) {
		return schema.GroupVersionResource{}, ScopeUnknown, ErrNoSuchResource
	}
	// 优先查缓存; miss 时主动发现一次, 避免冷启 mapper 阻塞
	info, ok := m.LookupCRD(id, res)
	if !ok {
		if _, err := m.DiscoverCRDs(context.Background(), id, false); err == nil {
			info, ok = m.LookupCRD(id, res)
		}
	}
	if !ok {
		return schema.GroupVersionResource{}, ScopeUnknown, ErrNoSuchResource
	}
	return schema.GroupVersionResource{Group: info.Group, Version: info.Version, Resource: info.Plural},
		scopeFromString(info.Scope), nil
}

func scopeFromString(s string) ScopeKind {
	if s == "Cluster" {
		return ScopeCluster
	}
	return ScopeNamespaced
}

// ValidResourceIncludingCRD 校验 res 是否合法 (内置 OR 已发现 CRD 短名).
// 未发现时尝试懒发现一次 (小集群下不应有明显延迟).
func (m *Manager) ValidResourceIncludingCRD(id, res string) bool {
	if ValidResource(res) {
		return true
	}
	if !IsCRDName(res) {
		return false
	}
	// 检查缓存
	if _, ok := m.LookupCRD(id, res); ok {
		return true
	}
	// 懒发现一次
	if _, err := m.DiscoverCRDs(context.Background(), id, true); err == nil {
		_, ok := m.LookupCRD(id, res)
		return ok
	}
	return false
}
