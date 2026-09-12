package logmonitor

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
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

// CollectDockerLogsSince 抓 Docker 容器自 sinceMs(毫秒时间戳) 之后的新日志。
// 依赖 docker logs --since=RFC3339 按时间绝对过滤，poller 增量采集去重用。
func CollectDockerLogsSince(name string, sinceMs int64) ([]string, error) {
	args := []string{"logs", "--tail", "2000"}
	if sinceMs > 0 {
		args = append(args, "--since", time.UnixMilli(sinceMs).UTC().Format(time.RFC3339Nano))
	}
	args = append(args, name)
	cmd := exec.Command("docker", args...)
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

// CollectK8sPodLogsSince 抓 K8S pod 自 sinceMs 之后的新日志。
// 用 kubectl logs --since-time=RFC3339 绝对时间过滤 + --timestamps=true 强制行首带时间戳，
// 保证每行可被 ParseLine 提取真实时间用于游标去重。
func CollectK8sPodLogsSince(kubeconfig, ns, pod string, sinceMs int64) ([]string, error) {
	args := []string{"--kubeconfig", kubeconfig, "logs", "-n", ns, pod, "--tail=2000", "--timestamps=true"}
	if sinceMs > 0 {
		args = append(args, "--since-time", time.UnixMilli(sinceMs).UTC().Format(time.RFC3339))
	}
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
// 本机路径: 使用已注册集群的 kubeconfig; 远程主机见 remote_runner.go。
func DiscoverK8sLogTargets(dataDir, clusterID string) ([]DiscoverPod, error) {
	return DiscoverK8sLogTargetsOn(dataDir, clusterID, "")
}

// DiscoverDockerContainers 返回本机全部 docker 容器(含容器名/镜像/状态)。
func DiscoverDockerContainers() ([]DiscoverContainer, error) {
	return DiscoverDockerContainersOn("")
}