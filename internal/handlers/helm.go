package handlers

// ── Helm 集成: 通过宿主机 helm 二进制 + 每集群 kubeconfig 管理 Release ──
// helm 未安装时返回明确错误; 命令一律走 exec.Command(非 shell), 规避注入。
// 仅开放运维子集: 列表 / 安装 / 升级 / 回滚 / 卸载 / 历史 / 当前 value / repo。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

var (
	helmBinary   = "/usr/local/bin/helm"
	helmCmdEnv   = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "HOME=/root"}
	reHelmName   = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)
	reHelmRepo   = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9-_/]*$`)
	reHelmChart  = regexp.MustCompile(`^[a-zA-Z0-9][\w./-]*$`)
	reHelmVer    = regexp.MustCompile(`^\d+(\.\d+)*$`)
	reHelmNS     = reK8sNamespace
	reHelmURL    = regexp.MustCompile(`^https?://[^\s]+$`)
)

func helmExec(clusterID string, args ...string) (string, error) {
	if _, err := os.Stat(helmBinary); err != nil {
		return "", fmt.Errorf("宿主未安装 helm 二进制(%s), 请先安装", helmBinary)
	}
	base := []string{"--kubeconfig", k8sKubePath(clusterID), "--kube-context", ""}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, helmBinary, append(base, args...)...)
	cmd.Env = helmCmdEnv
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if ctx.Err() == context.DeadlineExceeded {
		return text, errors.New("helm 命令超时(90s)")
	}
	if err != nil {
		return text, fmt.Errorf("helm %s: %v", strings.Join(args, " "), err)
	}
	return text, nil
}

// helmJSON: 运行 helm 并以 --output json 解析结果; 结果为纯文本时报错。
func helmJSON(clusterID string, args ...string) (json.RawMessage, error) {
	out, err := helmExec(clusterID, args...)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(strings.TrimSpace(out), "{") && !strings.HasPrefix(strings.TrimSpace(out), "[") {
		return nil, fmt.Errorf("helm 未返回 JSON: %s", ellipsis(out, 200))
	}
	return json.RawMessage(out), nil
}

func ellipsis(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// ===== 列表 / 历史 =====

// K8sHelmReleasesHandler GET ?cluster=&ns= — 列出 Release(all -A)。
func K8sHelmReleasesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(k8sPluginID, w) {
		return
	}
	cluster := r.URL.Query().Get("cluster")
	if !reK8sClusterID.MatchString(cluster) {
		WriteJSON(w, map[string]any{"ok": false, "error": "参数非法"})
		return
	}
	args := []string{"list", "-A", "--output", "json"}
	raw, err := helmJSON(cluster, args...)
	if err != nil {
		WriteJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	WriteJSON(w, map[string]any{"ok": true, "releases": json.RawMessage(raw)})
}

// K8sHelmHistoryHandler GET ?cluster=&name=&ns=
func K8sHelmHistoryHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(k8sPluginID, w) {
		return
	}
	q := r.URL.Query()
	cluster, name, ns := q.Get("cluster"), q.Get("name"), q.Get("ns")
	if !reK8sClusterID.MatchString(cluster) || !reHelmName.MatchString(name) || (ns != "" && !reHelmNS.MatchString(ns)) {
		WriteJSON(w, map[string]any{"ok": false, "error": "参数非法"})
		return
	}
	raw, err := helmJSON(cluster, "history", name, "-n", orNS(ns), "--output", "json")
	if err != nil {
		WriteJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	WriteJSON(w, map[string]any{"ok": true, "history": json.RawMessage(raw)})
}

// K8sHelmValuesHandler GET ?cluster=&name=&ns=
func K8sHelmValuesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(k8sPluginID, w) {
		return
	}
	q := r.URL.Query()
	cluster, name, ns := q.Get("cluster"), q.Get("name"), q.Get("ns")
	if !reK8sClusterID.MatchString(cluster) || !reHelmName.MatchString(name) || (ns != "" && !reHelmNS.MatchString(ns)) {
		WriteJSON(w, map[string]any{"ok": false, "error": "参数非法"})
		return
	}
	out, err := helmExec(cluster, "get", "values", name, "-n", orNS(ns), "--all")
	if err != nil {
		WriteJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	WriteJSON(w, map[string]any{"ok": true, "values": out})
}

// ===== 安装 / 升级 =====

type k8sHelmInstallBody struct {
	Cluster         string            `json:"cluster"`
	Name            string            `json:"name"`
	Namespace       string            `json:"namespace"`
	Chart           string            `json:"chart"` // repo/name 或 ./path
	Version         string            `json:"version"`
	Repo            string            `json:"repo"` // 可选 repo 添加 URL
	RepoName        string            `json:"repoName"`
	CreateNamespace bool              `json:"createNamespace"`
	Values          map[string]string `json:"values"` // key=value 覆写
	Atomic          bool              `json:"atomic"`
	Timeout         int               `json:"timeout"` // 秒
}

func k8sHelmInstallValidate(b *k8sHelmInstallBody) error {
	if !reK8sClusterID.MatchString(b.Cluster) || !reHelmName.MatchString(b.Name) {
		return fmt.Errorf("cluster/name 非法")
	}
	if !reHelmNS.MatchString(orNS(b.Namespace)) {
		return fmt.Errorf("namespace 非法")
	}
	if !reHelmChart.MatchString(b.Chart) || b.Chart == "" {
		return fmt.Errorf("chart 非法")
	}
	if b.Version != "" && !reHelmVer.MatchString(b.Version) {
		return fmt.Errorf("version 非法")
	}
	if b.RepoName != "" && !reHelmRepo.MatchString(b.RepoName) {
		return fmt.Errorf("repoName 非法")
	}
	if b.Repo != "" && !reHelmURL.MatchString(b.Repo) {
		return fmt.Errorf("repo url 非法")
	}
	return nil
}

// K8sHelmInstallHandler POST — 安装(不存在)/升级(已存在) Release。
func K8sHelmInstallHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(k8sPluginID, w) {
		return
	}
	var b k8sHelmInstallBody
	if err := k8sJSONDecode(r, &b); err != nil {
		WriteJSON(w, map[string]any{"ok": false, "error": "invalid body"})
		return
	}
	if err := k8sHelmInstallValidate(&b); err != nil {
		WriteJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	ns := orNS(b.Namespace)
	// 先加 repo(若提供)
	if b.Repo != "" && b.RepoName != "" {
		if out, err := helmExec(b.Cluster, "repo", "add", b.RepoName, b.Repo, "--force-update"); err != nil {
			WriteJSON(w, map[string]any{"ok": false, "error": "repo add 失败: " + out + " " + err.Error()})
			return
		}
		if _, err := helmExec(b.Cluster, "repo", "update"); err != nil {
			log.Printf("[HELM] repo update: %v", err)
		}
	}
	timeout := b.Timeout
	if timeout <= 0 {
		timeout = 300
	}
	args := []string{"upgrade", "--install", b.Name, b.Chart}
	args = append(args, "-n", ns, "--timeout", fmt.Sprintf("%ds", timeout))
	if b.CreateNamespace {
		args = append(args, "--create-namespace")
	}
	if b.Version != "" {
		args = append(args, "--version", b.Version)
	}
	for k, v := range b.Values {
		args = append(args, "--set", k+"="+v)
	}
	if b.Atomic {
		args = append(args, "--atomic")
	}
	out, err := helmExec(b.Cluster, args...)
	resp := map[string]any{"ok": err == nil}
	if err != nil {
		resp["error"] = out
		if out == "" {
			resp["error"] = err.Error()
		}
	} else {
		resp["message"] = cleanHelmOut(out)
	}
	log.Printf("[HELM-AUDIT] action=upgrade--install cluster=%s name=%s ns=%s chart=%s", b.Cluster, b.Name, ns, b.Chart)
	WriteJSON(w, resp)
}

// ===== 回滚 / 卸载 =====

type k8sHelmActionBody struct {
	Cluster   string `json:"cluster"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Revision  int    `json:"revision"`
}

// K8sHelmRollbackHandler POST {cluster,name,namespace,revision}
func K8sHelmRollbackHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(k8sPluginID, w) {
		return
	}
	var b k8sHelmActionBody
	if err := k8sJSONDecode(r, &b); err != nil {
		WriteJSON(w, map[string]any{"ok": false, "error": "invalid body"})
		return
	}
	if !reK8sClusterID.MatchString(b.Cluster) || !reHelmName.MatchString(b.Name) || !reHelmNS.MatchString(orNS(b.Namespace)) || b.Revision <= 0 {
		WriteJSON(w, map[string]any{"ok": false, "error": "参数非法"})
		return
	}
	out, err := helmExec(b.Cluster, "rollback", b.Name, fmt.Sprintf("%d", b.Revision), "-n", orNS(b.Namespace), "--wait")
	resp := map[string]any{"ok": err == nil}
	if err != nil {
		resp["error"] = orDefault(out, err.Error())
	} else {
		resp["message"] = cleanHelmOut(out)
	}
	log.Printf("[HELM-AUDIT] action=rollback cluster=%s name=%s revision=%d", b.Cluster, b.Name, b.Revision)
	WriteJSON(w, resp)
}

// K8sHelmUninstallHandler POST {cluster,name,namespace}
func K8sHelmUninstallHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(k8sPluginID, w) {
		return
	}
	var b k8sHelmActionBody
	if err := k8sJSONDecode(r, &b); err != nil {
		WriteJSON(w, map[string]any{"ok": false, "error": "invalid body"})
		return
	}
	if !reK8sClusterID.MatchString(b.Cluster) || !reHelmName.MatchString(b.Name) || !reHelmNS.MatchString(orNS(b.Namespace)) {
		WriteJSON(w, map[string]any{"ok": false, "error": "参数非法"})
		return
	}
	out, err := helmExec(b.Cluster, "uninstall", b.Name, "-n", orNS(b.Namespace))
	resp := map[string]any{"ok": err == nil}
	if err != nil {
		resp["error"] = orDefault(out, err.Error())
	} else {
		resp["message"] = cleanHelmOut(out)
	}
	log.Printf("[HELM-AUDIT] action=uninstall cluster=%s name=%s", b.Cluster, b.Name)
	WriteJSON(w, resp)
}

// ===== repo 列表 =====

// K8sHelmReposHandler GET ?cluster=
func K8sHelmReposHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(k8sPluginID, w) {
		return
	}
	cluster := r.URL.Query().Get("cluster")
	if !reK8sClusterID.MatchString(cluster) {
		WriteJSON(w, map[string]any{"ok": false, "error": "参数非法"})
		return
	}
	raw, err := helmJSON(cluster, "repo", "list", "--output", "json")
	if err != nil {
		WriteJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	WriteJSON(w, map[string]any{"ok": true, "repos": json.RawMessage(raw)})
}

// K8sHelmSearchHandler GET ?cluster=&q= — 在已 add 的 repo 中搜索 chart。
func K8sHelmSearchHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(k8sPluginID, w) {
		return
	}
	q := r.URL.Query()
	cluster := q.Get("cluster")
	kw := q.Get("q")
	if !reK8sClusterID.MatchString(cluster) || kw == "" || len(kw) > 80 {
		WriteJSON(w, map[string]any{"ok": false, "error": "参数非法"})
		return
	}
	// 搜索要求 repo 索引可用, 先 update 一次(后台), 再 search
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, helmBinary, "--kubeconfig", k8sKubePath(cluster), "--kube-context", "",
		"repo", "update")
	cmd.Env = helmCmdEnv
	_ = cmd.Run()
	raw, err := helmJSON(cluster, "search", "repo", kw, "--output", "json")
	if err != nil {
		WriteJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	WriteJSON(w, map[string]any{"ok": true, "charts": json.RawMessage(raw)})
}

// K8sHelmShowValuesHandler GET ?cluster=&chart=&version= — 展示 chart 默认 values。
func K8sHelmShowValuesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(k8sPluginID, w) {
		return
	}
	q := r.URL.Query()
	cluster, chart, ver := q.Get("cluster"), q.Get("chart"), q.Get("version")
	if !reK8sClusterID.MatchString(cluster) || !reHelmChart.MatchString(chart) ||
		(ver != "" && !reHelmVer.MatchString(ver)) {
		WriteJSON(w, map[string]any{"ok": false, "error": "参数非法"})
		return
	}
	args := []string{"show", "values", chart}
	if ver != "" {
		args = append(args, "--version", ver)
	}
	out, err := helmExec(cluster, args...)
	if err != nil {
		WriteJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	WriteJSON(w, map[string]any{"ok": true, "values": out})
}

// ===== 工具 =====

func orNS(ns string) string {
	if ns == "" {
		return "default"
	}
	return ns
}

func orDefault(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func cleanHelmOut(s string) string {
	s = strings.TrimSpace(s)
	if idx := strings.LastIndex(s, "\\n"); idx >= 0 {
		s = s[idx+2:]
	}
	lines := strings.Split(s, "\n")
	joined := make([]string, 0, len(lines))
	for _, l := range lines {
		if strings.Contains(l, "try a new command") && strings.Contains(l, "WARNING") {
			continue
		}
		joined = append(joined, l)
	}
	return strings.Join(joined, "\n")
}
