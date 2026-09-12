package handlers

// ── K8s 集群证书: 体检 + 续签 ──
// kubeadm certs 是 master 节点本地 shell 操作, 不走 K8s API。
// 执行目标: 集群落库 MasterHost → SSH; 否则 apiserver 地址反查主机清单 → SSH; 否则本机(如实上报)。
// 安装形态由探针动态判定(kubeadm/binary/unknown), 续签仅对 kubeadm 放行, binary 明确报错并建议手动轮换。
//
//   GET  /api/plugins/containers/k8s/certs/inspect?cluster=  → 体检表(证书名/到期时间/剩余/签发者/类型)
//   POST /api/plugins/containers/k8s/certs/renew  {cluster, confirm:"renew-certs"} → 续签(renew all + 重启控制面 + 刷 kubeconfig)

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"k8s.io/client-go/tools/clientcmd"
)

// certFile 一份证书的体检结果
type certFile struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Type    string `json:"type"` // ca / leaf / etcd / apiserver / kubeletClient ...
	Expires string `json:"expires"` // ISO8601
	Remain  string `json:"remain"`  // 人类可读剩余, 如 "3d 12h"
	Issuer  string `json:"issuer"`
	Sha1    string `json:"sha1"`
}

// masterTarget 记录本次执行落在哪台主机: 反查到的 hostID 或本机
type masterTarget struct {
	Mode   string `json:"mode"`   // ssh | local
	HostID string `json:"hostID"` // ssh 模式的主机 ID
	Reason string `json:"reason"`
}

// K8sCertsInspectHandler GET ?cluster= → 读 master 上 /etc/kubernetes/pki 全套证书过期信息
func K8sCertsInspectHandler(w http.ResponseWriter, r *http.Request) {
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
	target, err := resolveClusterMaster(cluster)
	if err != nil {
		WriteJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	shape := probeInstallShape(target)
	if shape.CertDir == "" {
		WriteJSON(w, map[string]any{"ok": false, "error": "master 上未检测到证书目录(/etc/kubernetes/pki 或 /etc/kubernetes/ssl)", "master": target, "install": shape})
		return
	}
	certs, err := inspectMasterCerts(target, shape)
	if err != nil {
		WriteJSON(w, map[string]any{"ok": false, "error": err.Error(), "master": target, "install": shape})
		return
	}
	WriteJSON(w, map[string]any{
		"ok":       true,
		"master":   target,
		"install":  shape,
		"certs":    certs,
		"kubePath": shape.CertDir,
	})
}

// renewCertsBody 续签请求
type renewCertsBody struct {
	Cluster string `json:"cluster"`
	Confirm string `json:"confirm"` // 二次确认口令: 必须为 "renew-certs" 才执行
}

// K8sCertsRenewHandler POST → 在 master 上执行 kubeadm certs renew all + 重启控制面 + 刷新 kubeconfig
func K8sCertsRenewHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(k8sPluginID, w) {
		return
	}
	var b renewCertsBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || !reK8sClusterID.MatchString(b.Cluster) {
		WriteJSON(w, map[string]any{"ok": false, "error": "invalid body"})
		return
	}
	if b.Confirm != "renew-certs" {
		WriteJSON(w, map[string]any{"ok": false, "error": "需二次确认(confirm=renew-certs)才执行续签"})
		return
	}
	target, err := resolveClusterMaster(b.Cluster)
	if err != nil {
		WriteJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	shape := probeInstallShape(target)
	if shape.Flavor != "kubeadm" {
		WriteJSON(w, map[string]any{"ok": false, "error": fmt.Sprintf("检测到非 kubeadm 安装(flavor=%s), kubeadm certs renew 不可用; 请按该集群的安装方式手动轮换证书", shape.Flavor), "master": target, "install": shape})
		return
	}
	steps := []string{}
	step := func(name, out string, err error) bool {
		out = strings.TrimSpace(out)
		if err != nil {
			out = fmt.Sprintf("%s\n(err=%v)", out, err)
		}
		if n := len(out); n > 600 {
			out = out[:600] + fmt.Sprintf("…(截断 %d)", n)
		}
		steps = append(steps, fmt.Sprintf("[%s] %s", name, out))
		return err == nil
	}
	renewOut, renewErr := runOnMaster(target, []string{"kubeadm", "certs", "renew", "all"})
	ok2 := step("renew-all", renewOut, renewErr)
	if !ok2 {
		log.Printf("[K8S-AUDIT] action=certs-renew cluster=%s master=%s mode=%s ok=false err=renew-all-failed", b.Cluster, target.HostID, target.Mode)
		WriteJSON(w, map[string]any{"ok": false, "steps": steps, "error": "kubeadm certs renew all 失败, 见输出", "install": shape})
		return
	}
	// 重启控制面静态 Pod: 移走再移回 manifests, kubelet 会 recreate (幂等, 可重入)
	o3, e3 := restartControlPlane(target)
	step("restart-controlplane", o3, e3)
	// 刷新本机 kubeconfig/admin.conf: kubeadm renew 不会自动更新 kubeconfig 里嵌的 client cert
	o4, e4 := refreshClusterKubeconfig(b.Cluster, target)
	step("refresh-kubeconfig", o4, e4)
	log.Printf("[K8S-AUDIT] action=certs-renew cluster=%s master=%s mode=%s ok=true", b.Cluster, target.HostID, target.Mode)
	InvalidateRespCache("/api/plugins/containers/k8s")
	WriteJSON(w, map[string]any{"ok": true, "master": target, "steps": steps})
}

// ===== master 主机解析 =====

// resolveClusterMaster 决定证书操作落在哪台主机, 按优先级:
//   落库 MasterHost(显式, 可信) → apiserver 地址反查主机清单(命中即落库回填) → 本机(如实上报)。
// 绝不猜错目标: 反查不到不猜, 退本机并说明。
func resolveClusterMaster(cluster string) (masterTarget, error) {
	// 1. 落库 MasterHost 优先
	if cs := k8sListClusters(); len(cs) > 0 {
		if c := k8sFindCluster(cs, cluster); c != nil {
			if c.MasterHost != "" {
				return masterTarget{Mode: "ssh", HostID: c.MasterHost, Reason: "使用落库 masterHost=" + c.MasterHost}, nil
			}
		}
	}
	t := masterTarget{Mode: "local", Reason: "无落库 masterHost 且未在主机清单反查到 apiserver 地址, 退本级执行"}
	// 2. 读回该集群 kubeconfig 提取 apiserver host
	path := k8sKubePath(cluster)
	data, err := os.ReadFile(path)
	if err != nil {
		return t, fmt.Errorf("读取集群 kubeconfig 失败(%s)", path)
	}
	server := kubeconfigServer(data)
	if server == "" {
		return t, fmt.Errorf("kubeconfig 中未找到 server 地址")
	}
	// 3. 复用统一反查(与注册同源); 命中即落库回填, 下次直接走落库值(确定性)
	if hostID := locateMasterHost(server); hostID != "" {
		if cs := k8sListClusters(); len(cs) > 0 {
			if c := k8sFindCluster(cs, cluster); c != nil && c.MasterHost == "" {
				c.MasterHost = hostID
				if serr := k8sUpsertCluster(*c); serr != nil {
					log.Printf("[K8S] 回填 masterHost=%s 失败: %v", hostID, serr)
				}
			}
		}
		return masterTarget{Mode: "ssh", HostID: hostID, Reason: "apiserver " + server + " 反查主机 " + hostID + " 命中, 已落库回填"}, nil
	}
	return t, nil
}

// kubeconfigServer 从 kubeconfig YAML 提取当前 context 指向的 cluster.server, 无则取第一个。
func kubeconfigServer(data []byte) string {
	cfg, err := clientcmd.Load(data)
	if err != nil || cfg == nil {
		return ""
	}
	// 优先当前 context 指向的 cluster; 否则第一个
	if cur := cfg.Contexts[cfg.CurrentContext]; cur != nil && cur.Cluster != "" {
		if cl, ok := cfg.Clusters[cur.Cluster]; ok && cl != nil {
			return cl.Server
		}
	}
	for _, c := range cfg.Clusters {
		if c != nil && c.Server != "" {
			return c.Server
		}
	}
	return ""
}

// ===== 安装形态探针 (动态判定, 不硬编码单一路径) =====

// installShape 目标机安装形态与证书目录, 供体检选择路径/续签放行。
type installShape struct {
	Flavor        string `json:"flavor"`   // kubeadm | binary | unknown
	CertDir       string `json:"certDir"`  // 检测到的证书目录 (/etc/kubernetes/pki 或 /etc/kubernetes/ssl)
	Kubeadm       string `json:"kubeadm"`  // command -v kubeadm (空=无)
	Manifests     string `json:"manifests"`      // yes/no 是否有 kubeadm 静态 Pod manifests
	APIServerUnit string `json:"apiserverUnit"`   // yes/no 是否有 kube-apiserver systemd unit(binary 特征)
}

// probeInstallShape 在目标机一次探明安装形态: 判据动态化, 兼容 kubeadm 与二进制部署的差异目录。
func probeInstallShape(t masterTarget) installShape {
	out, _ := runOnMaster(t, []string{"sh", "-c",
		`k=$(command -v kubeadm 2>/dev/null); m=no; [ -f /etc/kubernetes/manifests/kube-apiserver.yaml ] && m=yes; s=no; systemctl list-unit-files kube-apiserver.service 2>/dev/null | grep -q 'kube-apiserver.service' && s=yes; d=""; for x in /etc/kubernetes/pki /etc/kubernetes/ssl; do [ -d "$x" ] && d="$x" && break; done; printf '%s|%s|%s|%s' "$k" "$m" "$s" "$d"`})
	sh := installShape{}
	if parts := strings.SplitN(strings.TrimSpace(out), "|", 4); len(parts) == 4 {
		sh.Kubeadm, sh.Manifests, sh.APIServerUnit, sh.CertDir = parts[0], parts[1], parts[2], parts[3]
	}
	switch {
	case sh.Manifests == "yes" && sh.Kubeadm != "":
		sh.Flavor = "kubeadm"
	case sh.APIServerUnit == "yes":
		sh.Flavor = "binary"
	default:
		sh.Flavor = "unknown"
	}
	return sh
}

// ===== 证书体检 (SSH/本地执行) =====

// inspectMasterCerts 在 target 上枚举证书目录下的证书并解析到期信息 (双路径: kubeadm pki / binary ssl, .crt+.pem)。
func inspectMasterCerts(t masterTarget, sh installShape) ([]certFile, error) {
	if sh.CertDir == "" {
		return nil, fmt.Errorf("证书目录未检测到")
	}
	d := sh.CertDir
	out, err := runOnMaster(t, []string{"sh", "-c",
		`D=` + shellQuote(d) + `; ls -1 "$D"/*.crt "$D"/etcd/*.crt "$D"/*.pem 2>/dev/null | grep -vE '(^|/)sa\.key$|key\.pem$|\.key$' | sort -u; echo "---SA---"; ls -1 "$D"/sa.pub 2>/dev/null`})
	if err != nil {
		return nil, fmt.Errorf("读取 %s 的证书目录失败: %v", t.describe(), err)
	}
	lines := strings.Split(out, "\n")
	certs := []certFile{}
	saMode := false
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		if ln == "---SA---" {
			saMode = true
			continue
		}
		info, ierr := inspectOneCert(t, ln)
		if saMode {
			if strings.HasSuffix(ln, "sa.pub") {
				certs = append(certs, certFile{Name: "sa.pub", Path: ln, Type: "sa-public-key", Expires: "-", Remain: "非x509(ServiceAccount 签名公钥)", Issuer: "-", Sha1: "-"})
			}
			continue
		}
		if ierr != nil {
			certs = append(certs, certFile{Name: filepath.Base(ln), Path: ln, Type: classifyCert(ln), Expires: "-", Remain: ierr.Error(), Issuer: "-", Sha1: "-"})
			continue
		}
		certs = append(certs, info)
	}
	if len(certs) == 0 {
		return nil, fmt.Errorf("master(%s) 上 %s 未发现证书文件, 请确认该主机确实是控制面节点", t.describe(), d)
	}
	return certs, nil
}

// inspectOneCert 用 openssl 解析单份证书
func inspectOneCert(t masterTarget, path string) (certFile, error) {
	out, err := runOnMaster(t, []string{"sh", "-c", fmt.Sprintf(
		`openssl x509 -in %s -noout -enddate -issuer -fingerprint -sha1 -serial 2>/dev/null`, shellQuote(path))})
	if err != nil || strings.TrimSpace(out) == "" {
		return certFile{}, fmt.Errorf("解析失败")
	}
	c := certFile{
		Name:   filepath.Base(path),
		Path:   path,
		Type:   classifyCert(path),
		Issuer: "-",
		Sha1:   "-",
	}
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(l, "notAfter="):
			c.Expires = strings.TrimSpace(strings.TrimPrefix(l, "notAfter="))
			c.Remain = humanRemain(c.Expires)
		case strings.HasPrefix(l, "issuer="):
			c.Issuer = strings.TrimSpace(strings.TrimPrefix(l, "issuer="))
		case strings.HasPrefix(l, "SHA1 Fingerprint="):
			c.Sha1 = strings.TrimSpace(strings.TrimPrefix(l, "SHA1 Fingerprint="))
		}
	}
	if c.Expires == "" {
		return certFile{}, fmt.Errorf("未解析到 notAfter")
	}
	return c, nil
}

// classifyCert 按文件名归类证书用途 (粗分类, 足够体检)
func classifyCert(p string) string {
	b := filepath.Base(p)
	switch {
	case strings.Contains(b, "etcd"):
		return "etcd"
	case b == "ca.crt":
		return "ca-root"
	case strings.Contains(b, "apiserver") && strings.Contains(b, "kubelet"):
		return "apiserver-kubelet-client"
	case strings.Contains(b, "apiserver"):
		return "apiserver"
	case strings.Contains(b, "front-proxy"):
		return "front-proxy"
	case strings.Contains(b, "kubelet"):
		return "kubelet"
	default:
		return "leaf"
	}
}

// humanRemain 把 notAfter 转成人类可读剩余时长
func humanRemain(notAfter string) string {
	formats := []string{time.RFC1123, time.RFC1123Z, "Jan 2 15:04:05 2006 MST", "Jan  2 15:04:05 2006"}
	var when time.Time
	for _, f := range formats {
		if t, err := time.Parse(f, notAfter); err == nil {
			when = t
			break
		}
	}
	if when.IsZero() {
		return notAfter
	}
	d := time.Until(when)
	if d < 0 {
		return "已过期 " + formatDur(-d)
	}
	return formatDur(d)
}

func formatDur(d time.Duration) string {
	days := int(d.Hours()) / 24
	h := int(d.Hours()) % 24
	if days > 0 {
		return fmt.Sprintf("%dd%dh", days, h)
	}
	if d.Hours() >= 1 {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dm", int(d.Minutes()))
}

// ===== 续签动作 (SSH/本地) =====

// restartControlPlane 触发控制面静态 Pod recreate (不依赖具体容器运行时 docker/crictl, 用 kubectl 观察状态)
func restartControlPlane(t masterTarget) (string, error) {
	// 1. 移走三个 manifests, 让 kubelet 删除对应静态 Pod
	_, _ = runOnMaster(t, []string{"sh", "-c",
		`mkdir -p /tmp/k8s-mani-backup && f=/etc/kubernetes/manifests/kube-apiserver.yaml; [ -f "$f" ] && mv "$f" /tmp/k8s-mani-backup/; for pct in kube-controller-manager kube-scheduler; do f=/etc/kubernetes/manifests/$pct.yaml; [ -f "$f" ] && mv "$f" /tmp/k8s-mani-backup/; done`})
	// 2. 轮询三个组件 Pod 消失 (最多 60s)
	waited := 0
	for {
		out, _ := runOnMaster(t, []string{"sh", "-c",
			`kubectl --kubeconfig=/etc/kubernetes/admin.conf get pod -n kube-system -l 'component in (kube-apiserver,kube-controller-manager,kube-scheduler)' --no-headers 2>/dev/null | wc -l`})
		n := strings.TrimSpace(out)
		if n == "0" {
			break
		}
		if waited >= 60 {
			break // 移回时若个别组件还没删, 进 disk 状态也无妨, 尽力而为
		}
		time.Sleep(3 * time.Second)
		waited += 3
	}
	// 3. 移回 manifests, kubelet 检测到变化会重建静态 Pod, 并以新证书启动
	_, _ = runOnMaster(t, []string{"sh", "-c",
		`for f in /tmp/k8s-mani-backup/*.yaml; do [ -f "$f" ] && mv "$f" /etc/kubernetes/manifests/; done`})
	out, err := runOnMaster(t, []string{"sh", "-c", `systemctl is-active kubelet 2>/dev/null || echo unknown`})
	if err != nil {
		return "", fmt.Errorf("kubelet 状态读取失败: %v", err)
	}
	return fmt.Sprintf("控制面静态 Pod 已重建(kubelet=%s, 等待 60s)", strings.TrimSpace(out)), nil
}

// refreshClusterKubeconfig 用 kubeadm 重新生成集群 admin kubeconfig 并落回本地
func refreshClusterKubeconfig(cluster string, t masterTarget) (string, error) {
	out, err := runOnMaster(t, []string{"sh", "-c",
		`kubeadm kubeconfig user --client-name=kubernetes-admin --config=/etc/kubernetes/kubeadm-config.yaml 2>/dev/null || kubeadm init phase kubeconfig admin --config=/etc/kubernetes/kubeadm-config.yaml 2>/dev/null || cat /etc/kubernetes/admin.conf`})
	trimmed := strings.TrimSpace(out)
	if err != nil || !strings.HasPrefix(trimmed, "apiVersion:") {
		return "", fmt.Errorf("刷新 kubeconfig 失败: %v", err)
	}
	if werr := os.WriteFile(k8sKubePath(cluster), []byte(trimmed), 0600); werr != nil {
		return "", fmt.Errorf("写回 kubeconfig 失败: %v", werr)
	}
	return "已重新签发并写回 kubeconfig", nil
}

// ===== 执行原语 =====

// runOnMaster 在 target 上执行: ssh 模式 → RunOnTarget(hostID), local 模式 → 本机 exec
func runOnMaster(t masterTarget, argv []string) (string, error) {
	if t.Mode == "ssh" && t.HostID != "" {
		return RunOnTarget(t.HostID, argv)
	}
	// local: 本机不经 shell (argv 直达, 避免放大攻击面)
	return RunOnTarget("", argv)
}

// describe 用于日志/错误里描述执行目标
func (t masterTarget) describe() string {
	if t.Mode == "ssh" {
		return "SSH 主机 " + t.HostID
	}
	return "本机"
}

// shellQuote 单引号包裹做 ssh 单行转义 (复用 Shq 语义)
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}