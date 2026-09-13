package logmonitor

// 远程执行通道注入点: main 启动时注入 handlers.RunOnTarget, 使容器/pod 的
// 发现与预览可跟随全局主机上下文(?host=)分发到主机组主机。
// 为 nil 或 hostID 为空(本机语义)时, 保持原有本机 exec 行为不变。
// 持续采集 poller 仍在本机执行 —— 远程常驻采集属治理线 ce0f1c0 注入化采集通道的范围。
// 远程发现 K8S pod 使用目标主机自身的 kubeconfig(与服务端 kubeconfig-remote 端点同源探测),
// 不向远程落盘服务端凭据。

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

var RemoteRunner func(hostID string, argv []string) (string, error)

// SetRemoteRunner 由 main 注入, 规避 logmonitor -> handlers 依赖环。
func SetRemoteRunner(f func(hostID string, argv []string) (string, error)) {
	RemoteRunner = f
}

// remoteKubeconfigProbe 在目标机上按常见路径探测第一份 kubeconfig, 输出路径。
const remoteKubeconfigProbe = `for kc in "$HOME/.kube/config" /root/.kube/config /etc/kubernetes/admin.conf /etc/rancher/k3s/k3s.yaml; do [ -f "$kc" ] && { echo "$kc"; exit 0; }; done; exit 3`

// isRemoteHost 空串视为本机(与 handlers.IsLocalTarget 的空语义对齐)。
func isRemoteHost(hostID string) bool {
	return hostID != "" && RemoteRunner != nil
}

func splitLines(out string) []string {
	s := strings.TrimSpace(out)
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// DiscoverDockerContainersOn 发现 hostID(空=本机)上可接入的 Docker 容器。
func DiscoverDockerContainersOn(hostID string) ([]DiscoverContainer, error) {
	argv := []string{"docker", "ps", "-a", "--format", "{{.Names}}|{{.Image}}|{{.State}}"}
	var lines []string
	if isRemoteHost(hostID) {
		out, err := RemoteRunner(hostID, argv)
		if err != nil {
			return nil, err
		}
		lines = splitLines(out)
	} else {
		cmd := exec.Command(argv[0], argv[1:]...)
		var err error
		if lines, err = collectLogLines(cmd); err != nil {
			return nil, err
		}
	}
	out := make([]DiscoverContainer, 0, len(lines))
	for _, ln := range lines {
		parts := strings.SplitN(ln, "|", 3)
		if len(parts) != 3 {
			continue
		}
		out = append(out, DiscoverContainer{Name: parts[0], Image: parts[1], State: parts[2]})
	}
	return out, nil
}

// CollectDockerLogsOn 预览 hostID(空=本机)上指定容器的日志(只读, 不入持续采集)。
func CollectDockerLogsOn(hostID, name string, tail int) ([]string, error) {
	if tail <= 0 {
		tail = 200
	}
	if tail > 5000 {
		tail = 5000
	}
	if isRemoteHost(hostID) {
		out, err := RemoteRunner(hostID, []string{"docker", "logs", "--tail", strconv.Itoa(tail), name})
		if err != nil && strings.TrimSpace(out) == "" {
			return nil, err
		}
		return splitLines(out), nil
	}
	cmd := exec.Command("docker", "logs", "--tail", strconv.Itoa(tail), name)
	lines, err := collectLogLines(cmd)
	if err != nil {
		return nil, err
	}
	return lines, nil
}

// DiscoverK8sLogTargetsOn 发现 hostID(空=本机, 走已注册集群 kubeconfig)上的全部 pod。
// 远程路径使用目标主机自身的 kubeconfig, clusterID 仅用于结果标注。
func DiscoverK8sLogTargetsOn(dataDir, clusterID, hostID string) ([]DiscoverPod, error) {
	if !isRemoteHost(hostID) {
		kc := kubeconfigPathFor(dataDir, clusterID)
		if kc == "" {
			return nil, os.ErrNotExist
		}
		out, err := exec.Command("kubectl", "--kubeconfig", kc, "get", "pods", "-A", "-o", "json").CombinedOutput()
		if err != nil {
			return nil, err
		}
		return parseDiscoverPods(out, clusterID)
	}
	kcOut, err := RemoteRunner(hostID, []string{"sh", "-c", remoteKubeconfigProbe})
	if err != nil {
		return nil, fmt.Errorf("目标机未发现可用 kubeconfig: %w", err)
	}
	kc := strings.TrimSpace(kcOut)
	out, err := RemoteRunner(hostID, []string{"kubectl", "--kubeconfig", kc, "get", "pods", "-A", "-o", "json"})
	if err != nil {
		return nil, err
	}
	return parseDiscoverPods([]byte(out), clusterID)
}

// CollectK8sPodLogsOn 预览 hostID(空=本机)上指定 pod 的日志。
// 本机走已注册集群 kubeconfig; 远程走目标机自身 kubeconfig。
func CollectK8sPodLogsOn(hostID, dataDir, clusterID, ns, pod string, tail int) ([]string, error) {
	if tail <= 0 {
		tail = 200
	}
	if tail > 5000 {
		tail = 5000
	}
	if isRemoteHost(hostID) {
		kcOut, err := RemoteRunner(hostID, []string{"sh", "-c", remoteKubeconfigProbe})
		if err != nil {
			return nil, fmt.Errorf("目标机未发现可用 kubeconfig: %w", err)
		}
		kc := strings.TrimSpace(kcOut)
		out, err := RemoteRunner(hostID, []string{"kubectl", "--kubeconfig", kc, "logs", "-n", ns, pod, "--tail=" + strconv.Itoa(tail)})
		if err != nil && strings.TrimSpace(out) == "" {
			return nil, err
		}
		return splitLines(out), nil
	}
	kc := kubeconfigPathFor(dataDir, clusterID)
	if kc == "" {
		return nil, os.ErrNotExist
	}
	cmd := exec.Command("kubectl", "--kubeconfig", kc, "logs", "-n", ns, pod, "--tail="+strconv.Itoa(tail))
	lines, err := collectLogLines(cmd)
	if err != nil {
		return nil, err
	}
	return lines, nil
}

// parseDiscoverPods 解析 kubectl get pods -A -o json 的原始输出。
func parseDiscoverPods(out []byte, clusterID string) ([]DiscoverPod, error) {
	var parsed struct {
		Items []struct {
			Metadata struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
			Spec struct {
				Containers []struct {
					Name string `json:"name"`
				} `json:"containers"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, err
	}
	outPods := make([]DiscoverPod, 0, len(parsed.Items))
	for _, it := range parsed.Items {
		names := make([]string, 0, len(it.Spec.Containers))
		for _, c := range it.Spec.Containers {
			if c.Name != "" {
				names = append(names, c.Name)
			}
		}
		outPods = append(outPods, DiscoverPod{
			Name:       it.Metadata.Name,
			Namespace:  it.Metadata.Namespace,
			Containers: names,
			ClusterID:  clusterID,
		})
	}
	return outPods, nil
}
