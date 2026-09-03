package logmonitor

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// collectLogLines 执行 cmd 并返回去尾空白后的行切片；错误时返回错误。
func collectLogLines(cmd *exec.Cmd) ([]string, error) {
	// 用 CombinedOutput: docker/kubectl 等 CLI 常把内容写到 stderr(控制台通道)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, err
	}
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	lines := make([]string, 0, 512)
	for sc.Scan() {
		ln := strings.TrimRight(sc.Text(), "\r")
		if ln != "" {
			lines = append(lines, ln)
		}
	}
	return lines, sc.Err()
}

// CollectDockerLogs 抓本机 Docker 容器日志尾部 <tail> 行。
// name 为容器名，tail<=0 时抓全量(危险，限制最多 5000 行)。
func CollectDockerLogs(name string, tail int) ([]string, error) {
	if tail <= 0 {
		tail = 200
	}
	if tail > 5000 {
		tail = 5000
	}
	cmd := exec.Command("docker", "logs", "--tail", strconv.Itoa(tail), name)
	lines, err := collectLogLines(cmd)
	if err != nil {
		return nil, err
	}
	return lines, nil
}

// CollectK8sPodLogs 用落盘 kubeconfig 抓指定 K8S pod 日志尾部 <tail> 行。
// kubeconfig 为绝对路径；ns/pod 定位唯一 pod。
func CollectK8sPodLogs(kubeconfig, ns, pod string, tail int) ([]string, error) {
	if tail <= 0 {
		tail = 200
	}
	if tail > 5000 {
		tail = 5000
	}
	args := []string{"--kubeconfig", kubeconfig, "logs", "-n", ns, pod, "--tail=" + strconv.Itoa(tail)}
	cmd := exec.Command("kubectl", args...)
	lines, err := collectLogLines(cmd)
	if err != nil {
		return nil, err
	}
	return lines, nil
}

// kubeconfigPathFor 定位已注册集群的 kubeconfig 落盘文件。
// logmonitor 的 dataDir 是 <base>/logs，kubeconfig 目录是其兄弟目录 <base>/kubeconfigs。
func kubeconfigPathFor(dataDir, clusterID string) string {
	base := filepath.Dir(dataDir)
	if clusterID == "" {
		// 未指定集群时，若只有一个 kubeconfig 则取之
		matches, err := filepath.Glob(filepath.Join(base, "kubeconfigs", "*.yaml"))
		if err == nil && len(matches) == 1 {
			return matches[0]
		}
		return ""
	}
	p := filepath.Join(base, "kubeconfigs", clusterID+".yaml")
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

// CollectClusters 返回已注册集群 ID 列表(基于 kubeconfigs 目录)。用于前端选择。
func CollectClusters(dataDir string) []string {
	base := filepath.Dir(dataDir)
	matches, _ := filepath.Glob(filepath.Join(base, "kubeconfigs", "*.yaml"))
	ids := make([]string, 0, len(matches))
	for _, m := range matches {
		id := strings.TrimSuffix(filepath.Base(m), ".yaml")
		if id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

// DiscoverContainer 表示一个可接入的本机 Docker 容器。
type DiscoverContainer struct {
	Name  string `json:"name"`
	Image string `json:"image"`
	State string `json:"state"`
}

// DiscoverPod 表示一个可接入的 K8S pod。
type DiscoverPod struct {
	Name       string   `json:"name"`
	Namespace  string   `json:"namespace"`
	ClusterID  string   `json:"clusterID"`
	Containers []string `json:"containers"`
}

// DiscoverK8sLogTargets 返回某集群的全部 pod, 每个 pod 列出其全部容器。
// 用 kubectl get pods -A -o json 取原始 JSON 再解析(兼容不同 kubectl 版本的 jsonpath 能力)。
func DiscoverK8sLogTargets(dataDir, clusterID string) ([]DiscoverPod, error) {
	kc := kubeconfigPathFor(dataDir, clusterID)
	if kc == "" {
		return nil, os.ErrNotExist
	}
	cmd := exec.Command("kubectl", "--kubeconfig", kc, "get", "pods", "-A", "-o", "json")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, err
	}
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

// DiscoverDockerContainers 返回本机全部 docker 容器(含容器名/镜像/状态)。
func DiscoverDockerContainers() ([]DiscoverContainer, error) {
	cmd := exec.Command("docker", "ps", "-a", "--format", "{{.Names}}\u007c{{.Image}}\u007c{{.State}}")
	lines, err := collectLogLines(cmd)
	if err != nil {
		return nil, err
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