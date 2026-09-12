package handlers

// ── K8s 集群证书 · 二进制安装续签引擎 (openssl 可视化) ──
// kubeadm 装续签用 `kubeadm certs renew all`; 二进制装没有该工具, 这里用 openssl 复刻其本质:
//   "用现有 CA 把 leaf 证书再签一份更晚到期的"。
// 所有重签参数全部从旧证书读取(subject/issuer/SAN 原样复制), 复用私钥, 人只做勾选+确认。
// 安全护栏: 不动 CA(CA 过期=提示重建信任链, 不进引擎) / 不碰私钥 / 每节点执行前全量 tar 备份可回滚。
//
//   POST /api/plugins/containers/k8s/certs/binary/plan       {cluster}                 → 候选列表(只读推导)
//   POST /api/plugins/containers/k8s/certs/binary/renew      {cluster, confirm, certs}  → 逐控制面节点滚动执行
//   POST /api/plugins/containers/k8s/certs/binary/rollback   {cluster, confirm, backup} → 恢复备份

import (
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// binaryPlanHandler POST → 推导二进制集群可续签的候选证书列表(只读, 不写任何文件)。
func K8sBinaryPlanHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(k8sPluginID, w) {
		return
	}
	var b struct {
		Cluster string `json:"cluster"`
	}
	if err := k8sJSONDecode(r, &b); err != nil || !reK8sClusterID.MatchString(b.Cluster) {
		WriteJSON(w, map[string]any{"ok": false, "error": "invalid body"})
		return
	}
	target, err := resolveClusterMaster(b.Cluster)
	if err != nil {
		WriteJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	shape := probeInstallShape(target)
	if shape.Flavor != "binary" {
		WriteJSON(w, map[string]any{"ok": false, "error": fmt.Sprintf("该集群安装形态为 %s, 二进制续签引擎仅对 binary 形态适用", shape.Flavor), "master": target, "install": shape})
		return
	}
	plan, err := binaryPlan(target, shape.CertDir)
	if err != nil {
		WriteJSON(w, map[string]any{"ok": false, "error": err.Error(), "master": target, "install": shape})
		return
	}
	nodes := binaryControlPlaneNodes(target)
	WriteJSON(w, map[string]any{"ok": true, "master": target, "install": shape, "nodes": nodes, "plan": plan, "days": certRenewDays})
}

// binaryRenewHandler POST → 逐控制面节点滚动: 备份 → 重签勾选证书 → 按依赖重启 → 校验。
func K8sBinaryRenewHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(k8sPluginID, w) {
		return
	}
	var b struct {
		Cluster string   `json:"cluster"`
		Confirm string   `json:"confirm"`
		Certs   []string `json:"certs"`
	}
	if err := k8sJSONDecode(r, &b); err != nil || !reK8sClusterID.MatchString(b.Cluster) {
		WriteJSON(w, map[string]any{"ok": false, "error": "invalid body"})
		return
	}
	if b.Confirm != "renew-certs" {
		WriteJSON(w, map[string]any{"ok": false, "error": "需二次确认(confirm=renew-certs)才执行续签"})
		return
	}
	if len(b.Certs) == 0 {
		WriteJSON(w, map[string]any{"ok": false, "error": "未勾选任何证书"})
		return
	}
	target, err := resolveClusterMaster(b.Cluster)
	if err != nil {
		WriteJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	shape := probeInstallShape(target)
	if shape.Flavor != "binary" {
		WriteJSON(w, map[string]any{"ok": false, "error": fmt.Sprintf("该集群安装形态为 %s, 二进制续签引擎仅对 binary 形态适用", shape.Flavor)})
		return
	}
	nodes, err := binaryRenewRolling(b.Cluster, target, shape.CertDir, b.Certs)
	if err != nil {
		log.Printf("[K8S-AUDIT] action=certs-binary-renew cluster=%s ok=false err=%v", b.Cluster, err)
		WriteJSON(w, map[string]any{"ok": false, "error": err.Error(), "nodes": nodes})
		return
	}
	log.Printf("[K8S-AUDIT] action=certs-binary-renew cluster=%s ok=true certs=%d nodes=%d", b.Cluster, len(b.Certs), len(nodes))
	InvalidateRespCache("/api/plugins/containers/k8s")
	WriteJSON(w, map[string]any{"ok": true, "nodes": nodes})
}

// binaryRollbackHandler POST → 恢复某次续签前的证书目录备份。
func K8sBinaryRollbackHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(k8sPluginID, w) {
		return
	}
	var b struct {
		Cluster string `json:"cluster"`
		Confirm string `json:"confirm"`
		Backup  string `json:"backup"`
	}
	if err := k8sJSONDecode(r, &b); err != nil || !reK8sClusterID.MatchString(b.Cluster) {
		WriteJSON(w, map[string]any{"ok": false, "error": "invalid body"})
		return
	}
	if b.Confirm != "rollback" || b.Backup == "" {
		WriteJSON(w, map[string]any{"ok": false, "error": "需二次确认(confirm=rollback)并提供 backup 标识"})
		return
	}
	target, err := resolveClusterMaster(b.Cluster)
	if err != nil {
		WriteJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	shape := probeInstallShape(target)
	if shape.Flavor != "binary" {
		WriteJSON(w, map[string]any{"ok": false, "error": "非 binary 形态, 无可回滚备份"})
		return
	}
	if !strings.HasPrefix(b.Backup, binaryBackupPrefix+b.Cluster+"-") || filepathIsUnsafe(b.Backup) {
		WriteJSON(w, map[string]any{"ok": false, "error": "backup 标识非法"})
		return
	}
	if err := binaryRollback(target, shape.CertDir, b.Backup); err != nil {
		log.Printf("[K8S-AUDIT] action=certs-binary-rollback cluster=%s ok=false err=%v", b.Cluster, err)
		WriteJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	log.Printf("[K8S-AUDIT] action=certs-binary-rollback cluster=%s backup=%s ok=true", b.Cluster, b.Backup)
	InvalidateRespCache("/api/plugins/containers/k8s")
	WriteJSON(w, map[string]any{"ok": true})
}

// ===== 引擎 =====

const (
	binaryCertDir  = "/etc/kubernetes/ssl" // kubeasz 等二进制部署的常见证书目录
	certRenewDays  = 365
	binaryBackupPrefix = "k8s-cert-bak-"
)

// binCertPlan 一份候选证书(可续性已推导)
type binCertPlan struct {
	Path      string `json:"path"`
	Base      string `json:"name"`     // 文件名
	Issuer    string `json:"issuer"`   // issuer CN
	CA        string `json:"ca"`       // 对应 CA 私钥路径; "" = 不可续
	CAExpires string `json:"caExpires"` // 所用 CA 的到期时间(永续决策依据)
	Key       string `json:"key"`      // 对应私钥路径; "" = 缺私钥
	Expires   string `json:"expires"`
	Remain    string `json:"remain"`
	SAN       string `json:"san"` // 旧证书的 SAN(重签原样复制)
	Renewable bool   `json:"renewable"` // 能安全自动续: 有私钥 + 有 CA 私钥 + CA 晚于新证到期
	Reason    string `json:"reason,omitempty"`
}

// binNode 一个控制面节点(滚动目标)
type binNode struct {
	NodeName string `json:"nodeName"`
	HostID   string `json:"hostID,omitempty"` // 空 = 未在主机清单, 走本机或跳过
	Mode     string `json:"mode"`             // ssh | local | skip
	Status   string `json:"status"`           // pending | done | error
	Before   []string `json:"before,omitempty"`
	After    []string `json:"after,omitempty"`
	Steps    []string `json:"steps,omitempty"`
	Backup   string `json:"backup,omitempty"`
	Error    string `json:"error,omitempty"`
}

// binaryPlan 在目标机推导候选证书(单次 sh 收集, openssl 逐份解析)。
func binaryPlan(t masterTarget, certDir string) ([]binCertPlan, error) {
	out, err := runOnMaster(t, []string{"sh", "-c", binaryCollectSh(certDir)})
	if err != nil {
		// collect 脚本 may 退出非0: 个别文件解析失败; 用已有输出继续
	}
	certs := []binCertPlan{}
	for _, ln := range strings.Split(out, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		fld := strings.Split(ln, "\t")
		if len(fld) < 4 {
			continue
		}
		path, iname, inIssuer, cname, key := fld[0], fld[1], fld[2], fld[3], fld[4]
		if isCAFile(path) {
			continue
		}
		// 剩余时间 & SAN 从该证书逐一读取(仅对候选, 数量有限)
		info, ierr := inspectOneCert(t, path)
		c := binCertPlan{Path: path, Base: iname, Issuer: inIssuer, CA: cname, Key: key}
		if ierr == nil {
			c.Expires, c.Remain = info.Expires, info.Remain
			c.SAN = binaryCertSAN(t, path)
		} else {
			c.Remain = ierr.Error()
		}
		// CA 到期时间=新证有效期上限; CA 在续签时长内到期 → 算不可安全续(会产出一份比 CA 活得久的证)
		if c.CA != "" {
			c.CAExpires = binaryCAExpires(t, c.CA)
			if c.CAExpires != "" && strings.HasPrefix(c.CAExpires, "已过期") || (c.CAExpires != "" && remDays(c.CAExpires) < certRenewDays) {
				c.Reason = "所用 CA 将到期(不足 " + fmt.Sprint(certRenewDays) + " 天), 建议先重建 CA 信任链"
			}
		}
		if c.CA == "" || c.Key == "" {
			c.Reason = "缺 CA 私钥或本证书私钥, 无法自动续"
		} else if c.Reason == "" {
			c.Renewable = true
		}
		certs = append(certs, c)
	}
	return certs, nil
}

// binaryCollectSh 一次返回: path<TAB>文件名<TAB>issuerCN<TAB>ca key path<TAB>key path
func binaryCollectSh(certDir string) string {
	return `
D=` + shellQuote(certDir) + `
for f in "$D"/*.crt "$D"/etcd/*.crt "$D"/*.pem "$D"/etcd/*.pem; do
  [ -f "$f" ] || continue
  case "$f" in *ca*.crt|*ca*.pem|*key.pem|*.key) continue;; esac
  k=""
  for kk in "${f%.crt}.key" "${f%.crt}-key.pem" "${f%.pem}-key.pem"; do [ -f "$kk" ] && k="$kk" && break; done
  # 找对应 CA key: 证书 issuer CN == CA subject CN
  is=$(openssl x509 -in "$f" -noout -issuer 2>/dev/null | sed 's/.*CN=\([^,\/]*\).*/\1/')
  ca=""
  for cf in "$D"/ca.crt "$D"/etcd/ca.crt "$D"/front-proxy-ca.crt "$D"/ca.pem "$D"/etcd/ca.pem; do
    [ -f "$cf" ] || continue
    cs=$(openssl x509 -in "$cf" -noout -subject 2>/dev/null | sed 's/.*CN=\([^,\/]*\).*/\1/')
    if [ "$cs" = "$is" ]; then
      for ck in "${cf%.crt}.key" "${cf%.pem}-key.pem" "${cf%.crt}-key.pem"; do [ -f "$ck" ] && ca="$ck" && break; done
      break
    fi
  done
  printf '%s\t%s\t%s\t%s\t%s\n' "$f" "$(basename "$f")" "$is" "$ca" "$k"
done
`
}

// binaryCertSAN 抄旧证书的 subjectAltName(重签原样保留, 防止 apiserver 拓扑变化)
func binaryCertSAN(t masterTarget, path string) string {
	out, err := runOnMaster(t, []string{"sh", "-c",
		fmt.Sprintf(`openssl x509 -in %s -noout -ext subjectAltName 2>/dev/null | tail -n +2 | tr -d ' \n'`, shellQuote(path))})
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// caCertPath 由 CA 私钥路径推导对应证书文件(兼容 .key 与 -key.pem 两类命名)
func caCertPath(caKey string) string {
	switch {
	case strings.HasSuffix(caKey, "-key.pem"):
		return strings.TrimSuffix(caKey, "-key.pem") + ".pem"
	default:
		return strings.TrimSuffix(caKey, ".key") + ".crt"
	}
}

// binaryCAExpires 远端读该 CA 证书剩余时长(人类可读, 如 "345d23h" / "已过期 3d2h")
func binaryCAExpires(t masterTarget, caKey string) string {
	path := caCertPath(caKey)
	if path == "" {
		return ""
	}
	if c, err := inspectOneCert(t, path); err == nil {
		return c.Remain
	}
	return ""
}

// remDays 解析 humanRemain 文本剩余天数; 已过期 或 解析失败返回 0(触发"需重建"护栏)
func remDays(remain string) int {
	if strings.HasPrefix(remain, "已过期") {
		return 0
	}
	if i := strings.Index(remain, "d"); i > 0 {
		if d, err := strconv.Atoi(remain[:i]); err == nil {
			return d
		}
	}
	return 0
}

// binaryControlPlaneNodes 探测控制面节点(尽量用 kubectl; 失败则只有 primary 本机)
func binaryControlPlaneNodes(primary masterTarget) []binNode {
	nodes := []binNode{{NodeName: "<primary>", HostID: primary.HostID, Mode: primary.Mode, Status: "pending"}}
	if primary.HostID == "" {
		nodes[0].HostID = ""
	}
	out, err := runOnMaster(primary, []string{"sh", "-c",
		`for kc in /etc/kubernetes/admin.conf /etc/kubernetes/kubelet.conf; do [ -f "$kc" ] && { kubectl --kubeconfig=$kc get nodes -o go-template='{{range .items}}{{.metadata.name}}{{"\n"}}{{end}}' 2>/dev/null; break; }; done`})
	if err != nil || strings.TrimSpace(out) == "" {
		return nodes // 备用: 只有主节点
	}
	seen := map[string]bool{}
	res := []binNode{}
	for _, nn := range strings.Split(out, "\n") {
		if nn = strings.TrimSpace(nn); nn == "" {
			continue
		}
		mode, hostID := "skip", ""
		if primary.HostID != "" && nn == localHostname() {
			mode, hostID = "ssh", primary.HostID
		} else {
			if h := locateMasterHostByName(nn); h != "" {
				mode, hostID = "ssh", h
			}
		}
		if mode == "skip" {
			hostID = nn
		}
		res = append(res, binNode{NodeName: nn, HostID: hostID, Mode: mode, Status: "pending"})
		seen[nn] = true
	}
	if len(res) == 0 {
		return nodes
	}
	_ = seen
	return res
}

// binaryRenewRolling 逐节点: 全部先备份, 再逐节点重签+重启+校验(滚动, 一台绿再下一台)。
func binaryRenewRolling(cluster string, primary masterTarget, certDir string, picks []string) ([]binNode, error) {
	nodes := binaryControlPlaneNodes(primary)
	// 第一遍: 全部备份
	for i := range nodes {
		tt, err := nodeTarget(nodes[i], primary)
		if err != nil {
			nodes[i].Status, nodes[i].Error = "error", err.Error()
			continue
		}
		nodes[i].Backup, err = binaryBackup(tt, certDir, cluster)
		if err != nil {
			nodes[i].Status, nodes[i].Error = "error", "备份失败: " + err.Error()
		}
	}
	// 第二遍: 逐节点重签(先 etcd 后 apiserver 等, 由 restart 阶段控制)
	var lastErr error
	for i := range nodes {
		if nodes[i].Backup == "" {
			continue
		}
		tt, err := nodeTarget(nodes[i], primary)
		if err != nil {
			nodes[i].Status, nodes[i].Error = "error", err.Error()
			lastErr = err
			continue
		}
		before := certSnapshot(tt, certDir)
		steps, after, rerr := binaryRenewOneNode(tt, certDir, picks)
		nodes[i].Before, nodes[i].After, nodes[i].Steps = before, after, steps
		if rerr != nil {
			nodes[i].Status, nodes[i].Error = "error", rerr.Error()
			lastErr = rerr
			continue
		}
		// 本节点 apiserver/healthz 恢复校验(仅当该台是控制面且有 apiserver)
		if stat := binaryWaitHealthz(tt); stat != "" {
			nodes[i].Steps = append(nodes[i].Steps, "healthz="+stat)
			if strings.Contains(stat, "unreachable") {
				nodes[i].Status = "warn"
				lastErr = fmt.Errorf("节点 %s healthz 弹回: %s", nodes[i].NodeName, stat)
				continue
			}
		}
		nodes[i].Status = "done"
	}
	if lastErr != nil {
		return nodes, lastErr
	}
	for i := range nodes {
		if nodes[i].Status == "error" || nodes[i].Error != "" {
			return nodes, fmt.Errorf("存在失败的节点(%s), 详见 nodes", nodes[i].NodeName)
		}
	}
	return nodes, nil
}

// nodeTarget 把 binNode 解析为执行目标: 本机(target) 或 SSH(主机清单命中)。
func nodeTarget(n binNode, primary masterTarget) (masterTarget, error) {
	if n.Mode == "skip" {
		return masterTarget{}, fmt.Errorf("节点 %s 未在主机清单, 无法 SSH(可手动续或先在清单登记)", n.NodeName)
	}
	if n.Mode == "local" || n.HostID == "" {
		return masterTarget{Mode: "local", Reason: "本机作为控制面节点"}, nil
	}
	return masterTarget{Mode: "ssh", HostID: n.HostID, Reason: "滚动节点 " + n.NodeName}, nil
}

// binaryBackup 打包证书目录(每次续签前全量备份, 供一键回滚)
func binaryBackup(t masterTarget, certDir, cluster string) (string, error) {
	ts := time.Now().Format("20060102-150405")
	name := binaryBackupPrefix + cluster + "-" + ts
	out, err := runOnMaster(t, []string{"sh", "-c",
		fmt.Sprintf(`d=%s; mkdir -p /tmp/k8s-cert-bak && tar czf /tmp/k8s-cert-bak/%s.tar.gz -C "$(dirname "$d")" "$(basename "$d")" && echo "%s"`, shellQuote(certDir), name, name)})
	if err != nil {
		return "", fmt.Errorf("%s (out=%s)", err, strings.TrimSpace(out))
	}
	return strings.TrimSpace(out), nil
}

// binaryRenewOneNode 单节点: 对勾选的、本机存在的每份证书重签并覆盖, 再按依赖重启服务。
func binaryRenewOneNode(t masterTarget, certDir string, picks []string) ([]string, []string, error) {
	steps := []string{}
	after := []string{}
	for _, pth := range picks {
		pth = strings.TrimSpace(pth)
		if pth == "" {
			continue
		}
		// 该节点上存在才续(多节点目录可能不同)
		exist, _ := runOnMaster(t, []string{"sh", "-c", fmt.Sprintf(`[ -f %s ] && echo yes || echo no`, shellQuote(pth))})
		if strings.TrimSpace(exist) != "yes" {
			steps = append(steps, "跳过(本机无): "+pth)
			continue
		}
		renewed, _, a, err := binaryRenewOneCert(t, certDir, pth)
		after = append(after, a)
		if err != nil {
			return steps, after, fmt.Errorf("重签 %s 失败: %v", pth, err)
		}
		steps = append(steps, renewed)
	}
	// 按依赖重启(etcd → apiserver → cms/scheduler), 只重启涉及的服务
	needed := secNeededRestart(certDir, steps)
	for _, svc := range []string{"etcd", "kube-apiserver", "kube-controller-manager", "kube-scheduler"} {
		if _, ok := needed[svc]; !ok {
			continue
		}
		if _, err := runOnMaster(t, []string{"systemctl", "restart", svc}); err != nil {
			return steps, after, fmt.Errorf("重启 %s 失败: %v", svc, err)
		}
		steps = append(steps, "restart "+svc)
	}
	return steps, after, nil
}

// binaryRenewOneCert 用 openssl 把单份 leaf 用对应 CA 重签(抄旧 subject/扩展, 复用私钥)。
func binaryRenewOneCert(t masterTarget, certDir, path string) (string, string, string, error) {
	tmp := "/tmp/k8s-cert-renew-" + fmt.Sprint(time.Now().UnixNano())
	// 私钥与 CA 私钥都在远端按统一规则探测(与 plan collector 同一套命名匹配), 不在本地猜路径
	script := fmt.Sprintf(`
set -e
src=%s; D=%s; tmp=%s
base=${src%%.crt}; base=${base%%.pem}
key=""
for kk in "${base}.key" "${base}-key.pem" "${src%%.crt}.key"; do [ -f "$kk" ] && key="$kk" && break; done
[ -n "$key" ] || { echo "KEY_MISSING:$src"; exit 2; }
is=$(openssl x509 -in "$src" -noout -issuer 2>/dev/null | sed 's/.*CN=\([^,\/]*\).*/\1/')
caKey=""
for cf in "$D"/ca.crt "$D"/etcd/ca.crt "$D"/front-proxy-ca.crt "$D"/ca.pem "$D"/etcd/ca.pem; do
  [ -f "$cf" ] || continue
  cs=$(openssl x509 -in "$cf" -noout -subject 2>/dev/null | sed 's/.*CN=\([^,\/]*\).*/\1/')
  [ "$cs" = "$is" ] || continue
  for ck in "${cf%%.crt}.key" "${cf%%.pem}-key.pem" "${cf%%.crt}-key.pem"; do
    [ -f "$ck" ] && caKey="$ck" && break
  done
  [ -n "$caKey" ] && break
done
[ -n "$caKey" ] || { echo "CAKEY_MISSING:$src"; exit 3; }
ca=$cf
openssl x509 -in "$src" -x509toreq -signkey "$key" -out "$tmp.csr" 2>/dev/null || \
  openssl req -new -key "$key" -subj "$(openssl x509 -in "$src" -noout -subject | sed 's/subject=//')" -out "$tmp.csr"
# SAN 从旧证书显式抽出写 extfile(兼容老 openssl; 杜绝 -copy_extensions silently 丢 SAN)
san=$(openssl x509 -in "$src" -noout -ext subjectAltName 2>/dev/null | tail -n +2 | tr -d ' \t' | sed 's/IPAddress:/IP:/g')
if [ -n "$san" ]; then
  echo "subjectAltName=$san" > "$tmp.ext"
  openssl x509 -req -in "$tmp.csr" -CA "$ca" -CAkey "$caKey" -CAcreateserial -days %d -out "$tmp.crt" -extfile "$tmp.ext"
else
  openssl x509 -req -in "$tmp.csr" -CA "$ca" -CAkey "$caKey" -CAcreateserial -days %d -out "$tmp.crt"
fi
openssl x509 -in "$tmp.crt" -noout -enddate | sed 's/notAfter=//'
mv -f "$tmp.crt" "$src"
chmod --reference=%s %s 2>/dev/null || chmod 644 %s
rm -f "$tmp.csr" "$tmp.crt" "$tmp.ext"
`, shellQuote(path), shellQuote(certDir), tmp, certRenewDays, certRenewDays, shellQuote(path), shellQuote(path), shellQuote(path))
	before := binaryExpiresAt(t, path)
	out, err := runOnMaster(t, []string{"sh", "-c", script})
	if err != nil {
		if strings.Contains(out, "KEY_MISSING") {
			return "", before, before, fmt.Errorf("本机无此证书私钥(%s), 无法续签", filepathBase(path))
		}
		if strings.Contains(out, "CAKEY_MISSING") {
			return "", before, before, fmt.Errorf("本机无对应 CA 私钥(%s), 该证书不可自动续", filepathBase(path))
		}
		return "", before, before, fmt.Errorf("%s (out=%s)", err, t)
	}
	afterNew := strings.TrimSpace(out)
	if afterNew == "" {
		afterNew = binaryExpiresAt(t, path)
	}
	return "renew " + filepathBase(path) + " → " + afterNew, before, afterNew, nil
}

// certSnapshot 小扇面快照: 每份 *.crt 的到期时间(重签前后对比用)
func certSnapshot(t masterTarget, certDir string) []string {
	out, _ := runOnMaster(t, []string{"sh", "-c",
		`D=` + shellQuote(certDir) + `; for f in "$D"/*.crt "$D"/etcd/*.crt "$D"/*.pem; do [ -f "$f" ] || continue; printf '%s\t%s\n' "$(basename "$f")" "$(openssl x509 -in "$f" -noout -enddate 2>/dev/null | sed 's/notAfter=//')"; done`})
	lines := []string{}
	for _, ln := range strings.Split(out, "\n") {
		if ln = strings.TrimSpace(ln); ln != "" {
			lines = append(lines, ln)
		}
	}
	return lines
}

// binaryExpiresAt 单份证书到期行
func binaryExpiresAt(t masterTarget, path string) string {
	out, _ := runOnMaster(t, []string{"sh", "-c", fmt.Sprintf(`openssl x509 -in %s -noout -enddate 2>/dev/null | sed 's/notAfter=//'`, shellQuote(path))})
	return strings.TrimSpace(out)
}

// binaryWaitHealthz 本节点 apiserver /healthz 可及性(最多 ~40s, 纯 curl 无 nc 依赖)
func binaryWaitHealthz(t masterTarget) string {
	deadline := time.Now().Add(40 * time.Second)
	for time.Now().Before(deadline) {
		out, _ := runOnMaster(t, []string{"sh", "-c",
			`for p in 6443 8443; do c=$(curl -sk -o /dev/null -w '%{http_code}' https://127.0.0.1:$p/healthz 2>/dev/null); [ "$c" = "200" ] && echo "$c" && exit 0; done; sleep 3`})
		if strings.TrimSpace(out) == "200" {
			return "ready"
		}
		time.Sleep(4 * time.Second)
	}
	return "unreachable"
}

// secNeededRestart 从重签结果反推需要重启的服务(按证书名归类)
func secNeededRestart(certDir string, steps []string) map[string]bool {
	need := map[string]bool{}
	for _, s := range steps {
		if !strings.HasPrefix(s, "renew ") {
			continue
		}
		name := strings.TrimPrefix(s, "renew ")
		name = strings.TrimSuffix(name, " →")
		switch {
		case strings.Contains(name, "etcd"):
			need["etcd"] = true
		case strings.Contains(name, "apiserver") || strings.Contains(name, "front-proxy"):
			need["kube-apiserver"] = true
		case strings.Contains(name, "controller"):
			need["kube-controller-manager"] = true
		case strings.Contains(name, "scheduler"):
			need["kube-scheduler"] = true
		}
	}
	return need
}

// binaryRollback 恢复某次备份(先移走现目录再解包, 权限 644 兜底)
func binaryRollback(t masterTarget, certDir, backup string) error {
	_, err := runOnMaster(t, []string{"sh", "-c", fmt.Sprintf(`
set -e
b=/tmp/k8s-cert-bak/%s.tar.gz
[ -f "$b" ] || { echo "备份 $b 不存在"; exit 1; }
d=%s
rm -rf "$d.bak"
mv "$d" "$d.bak" || true
tar xzf "$b" -C "$(dirname "$d")"
chmod -R 644 "$d" 2>/dev/null || true
for f in "$d"/*key "$d"/*.key "$d"/*key.pem; do [ -f "$f" ] && chmod 600 "$f"; done 2>/dev/null || true
echo ok
`, backup, shellQuote(certDir))})
	return err
}

// binaryRenewRolling 用到的辅助
func filepathBase(p string) string { return p[strings.LastIndex(p, "/")+1:] }

func filepathIsUnsafe(p string) bool {
	return strings.Contains(p, "..") || strings.HasPrefix(p, "/") || strings.Contains(p, "\\")
}

func isCAFile(path string) bool {
	b := filepathBase(path)
	return strings.Contains(b, "ca") // ca.crt / front-proxy-ca.crt / etcd ca
}

// locateMasterHostByName 按主机名在清单反查(与 locateMasterHost 同源)
func locateMasterHostByName(name string) string {
	if ansibleMgr == nil {
		return ""
	}
	for _, h := range ansibleMgr.ListHosts() {
		if strings.EqualFold(h.Hostname, name) || strings.EqualFold(h.ID, name) {
			return h.ID
		}
	}
	return ""
}