package kubernetes

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

const testKubeconfig = `
apiVersion: v1
kind: Config
clusters:
- name: c1
  cluster:
    server: https://127.0.0.1:6443
    insecure-skip-tls-verify: true
contexts:
- name: ctx1
  context: {cluster: c1, user: u1}
current-context: ctx1
users:
- name: u1
  user: {token: t}
`

func newTestManager(t *testing.T, gvr schema.GroupVersionResource, listKind string, objs ...runtime.Object) (*Manager, string) {
	t.Helper()
	fake := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: listKind}, objs...)
	m := NewManager()
	m.clusters["test"] = &clientSet{dynamicClient: fake}
	return m, "test"
}

func podObj(name, ns, phase, node string, restarts int32) *unstructured.Unstructured {
	pod := &corev1.Pod{}
	pod.SetName(name)
	pod.SetNamespace(ns)
	pod.SetCreationTimestamp(metav1.NewTime(time.Now().Add(-3 * time.Hour)))
	pod.Status.Phase = corev1.PodPhase(phase)
	pod.Spec.NodeName = node
	pod.Status.PodIP = "10.0.0.9"
	for i := 0; i < int(restarts); i++ {
		pod.Status.ContainerStatuses = append(pod.Status.ContainerStatuses, corev1.ContainerStatus{RestartCount: 1})
	}
	u, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(pod)
	obj := &unstructured.Unstructured{Object: u}
	obj.SetAPIVersion("v1")
	obj.SetKind("Pod")
	return obj
}

func TestManagerAddInvalidKubeconfig(t *testing.T) {
	m := NewManager()
	if err := m.Add("bad", []byte("not a kubeconfig")); err == nil {
		t.Fatal("expected error for invalid kubeconfig")
	}
	if ids := m.ListIDs(); len(ids) != 0 {
		t.Fatalf("expected no clusters, got %v", ids)
	}
}

func TestManagerAddValidAndLifecycle(t *testing.T) {
	m := NewManager()
	if err := m.Add("c1", []byte(testKubeconfig)); err != nil {
		t.Fatalf("add: %v", err)
	}
	if ids := m.ListIDs(); len(ids) != 1 || ids[0] != "c1" {
		t.Fatalf("listIDs = %v", ids)
	}
	cfg, err := m.RESTConfig("c1")
	if err != nil || cfg.Host != "https://127.0.0.1:6443" {
		t.Fatalf("restConfig host=%q err=%v", cfg.Host, err)
	}
	m.Remove("c1")
	if _, err := m.DynamicClient("c1"); err == nil {
		t.Fatal("expected not-found after Remove")
	}
}

func TestListResourcesPods(t *testing.T) {
	m, id := newTestManager(t, gvrPods, "PodList",
		podObj("web-1", "default", "Running", "node-a", 2),
		podObj("db-0", "kube-system", "Pending", "", 0),
	)
	rows, err := m.ListResources(context.Background(), id, "pods", "")
	if err != nil {
		t.Fatalf("list pods: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(rows))
	}
	// 排序: kube-system < default? 不, 按字典序 default < kube-system
	if rows[0]["name"] != "web-1" {
		t.Fatalf("first row should be web-1(default), got %v", rows[0]["name"])
	}
	r := rows[0]
	if r["status"] != "Running" || r["node"] != "node-a" || r["restarts"] != int32(2) || r["ip"] != "10.0.0.9" {
		t.Fatalf("pod fields wrong: %+v", r)
	}
	if !strings.HasSuffix(r["age"].(string), "h") && !strings.HasSuffix(r["age"].(string), "m") {
		t.Fatalf("age format unexpected: %v", r["age"])
	}
}

func TestListResourcesUnsupported(t *testing.T) {
	m, id := newTestManager(t, gvrPods, "PodList")
	if _, err := m.ListResources(context.Background(), id, "widgets", ""); err == nil {
		t.Fatal("expected unsupported resource error")
	}
	if _, err := m.ListResources(context.Background(), "nope", "pods", ""); err == nil {
		t.Fatal("expected cluster-not-found error")
	}
}

func TestNsForClusterScoped(t *testing.T) {
	if nsFor("kube-system", "nodes") != "" {
		t.Fatal("nodes must ignore namespace")
	}
	if nsFor("kube-system", "pods") != "kube-system" {
		t.Fatal("pods must honor namespace")
	}
}

func TestHumanAge(t *testing.T) {
	now := time.Now()
	cases := map[string]struct {
		from time.Time
		want byte // 后缀字母
	}{
		"秒": {now.Add(-45 * time.Second), 's'},
		"分": {now.Add(-34 * time.Minute), 'm'},
		"时": {now.Add(-5 * time.Hour), 'h'},
		"天": {now.Add(-12 * 24 * time.Hour), 'd'},
	}
	for k, c := range cases {
		got := humanAge(c.from, now)
		if got[len(got)-1] != c.want {
			t.Fatalf("%s: want suffix %c got %q", k, c.want, got)
		}
	}
}
