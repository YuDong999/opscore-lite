package handlers

// ── etcd 容量/健康 + 负载均衡数据面(只读) ──
// 为什么走 SSH 而不是 API: etcd 的 backend 用量只在 /metrics 上(需要证书文件),
// 数据目录大小在节点文件系统上, ipvs/iptables/cilium bpf map 更是节点级信息 —— API 都拿不到。
// 目标主机复用集群证书那套解析: resolveClusterMaster(落库 masterHost / kubeconfig 反查清单)。

import (
	"context"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// etcdProbeScript 一条会话里取四段: etcd 启动参数 / 镜像版本 / 运行指标 / 数据目录 / 数据面。
// 只读; 证书目录按常见几处试探; 路径不来自用户输入。
//
// 注意: 本脚本经 RunOnTarget(argv) 投递, 会被整段包进单引号 —— **脚本内不能出现单引号**
// (会被外层 shell 吃掉一层转义, 变成语法错误)。所以 awk 用 grep 管道替代、字符串一律用双引号。
const etcdProbeScript = `echo __OPSCORE_ETCD_PRESENT__
[ -f /etc/kubernetes/manifests/etcd.yaml ] && echo yes || echo no
echo __OPSCORE_ETCD_CONF__
for f in /etc/kubernetes/manifests/etcd.yaml /etc/etcd/etcd.conf /etc/etcd/etcd.conf.yml; do
  [ -f "$f" ] || continue
  for k in quota-backend-bytes auto-compaction-retention max-snapshots max-wals snapshot-count election-timeout heartbeat-interval; do
    v=$(grep -oE "$k[=: ]+[A-Za-z0-9-]+" "$f" 2>/dev/null | head -1)
    [ -n "$v" ] && echo "$v"
  done
done
echo __OPSCORE_ETCD_IMAGE__
grep -m1 -oE "image: *[^ ]+" /etc/kubernetes/manifests/etcd.yaml 2>/dev/null
echo __OPSCORE_ETCD_METRICS__
for d in /etc/kubernetes/pki/etcd /etc/etcd/pki /etc/ssl/etcd; do
  if [ -f "$d/server.crt" ] && [ -f "$d/server.key" ]; then
    curl -s --max-time 6 --cert "$d/server.crt" --key "$d/server.key" --cacert "$d/ca.crt" https://127.0.0.1:2379/metrics 2>/dev/null
    break
  fi
done
echo __OPSCORE_ETCD_FILES__
stat -c "%s %Y" /var/lib/etcd/member/snap/db 2>/dev/null
du -sb /var/lib/etcd 2>/dev/null | cut -f1
echo __OPSCORE_DATAPLANE__
if [ -r /proc/net/ip_vs ]; then
  echo "ipvs_rs=$(grep -c -- "->" /proc/net/ip_vs)"
  echo "ipvs_conns=$(tail -n +2 /proc/net/ip_vs_conn 2>/dev/null | grep -c .)"
  echo "ipvs_tab_bits=$(cat /sys/module/ip_vs/parameters/conn_tab_bits 2>/dev/null)"
fi
if command -v cilium >/dev/null 2>&1; then
  echo "cilium_cli=yes"
  echo "cilium_lb_entries=$(cilium bpf lb list 2>/dev/null | grep -c .)"
fi
echo "cilium_cni=$(ls /etc/cni/net.d/*cilium* 2>/dev/null | wc -l | tr -d " ")"
echo "cilium_maps=$(ls -d /sys/fs/bpf/tc/globals/cilium_lb* /sys/fs/bpf/cilium/cilium_lb* 2>/dev/null | wc -l | tr -d " ")"
if command -v bpftool >/dev/null 2>&1; then
  echo "bpf_lb_max=$(bpftool map show 2>/dev/null | grep -A2 cilium_lb | grep max_entries | head -1 | tr -dc 0-9)"
fi
echo "iptables_rules=$(iptables-save 2>/dev/null | wc -l | tr -d " ")"
echo __OPSCORE_END__`

type etcdMember struct {
	Node          string  `json:"node"`
	Reachable     bool    `json:"reachable"`
	Source        string  `json:"source"` // metrics | files | none
	Version       string  `json:"version,omitempty"`
	QuotaBytes    uint64  `json:"quotaBytes,omitempty"`
	DBBytes       uint64  `json:"dbBytes,omitempty"`
	DBInUseBytes  uint64  `json:"dbInUseBytes,omitempty"`
	FragPct       float64 `json:"fragPct"`
	HasLeader     bool    `json:"hasLeader"`
	LeaderChanges uint64  `json:"leaderChanges"`
	DataDir       string  `json:"dataDir,omitempty"`
	DataDirBytes  uint64  `json:"dataDirBytes,omitempty"`
	DBFileBytes   uint64  `json:"dbFileBytes,omitempty"`
	Note          string  `json:"note,omitempty"`
}

type dplaneNode struct {
	Node            string `json:"node"`
	Target          string `json:"target,omitempty"`
	Raw             string `json:"raw,omitempty"` // 一段都没解析出来时的原始输出头(定位用)
	Kind            string `json:"kind"`          // cilium | ipvs | iptables | unknown
	Mode            string `json:"mode,omitempty"`
	RealServers     int    `json:"realServers"`
	IPVSConns       int    `json:"ipvsConns"`
	IPVSTabBits     int    `json:"ipvsTabBits"`
	IptablesRules   int    `json:"iptablesRules"`
	CiliumMaps      int    `json:"ciliumMaps"`
	CiliumLBEntries int    `json:"ciliumLbEntries"`
	BPFLBMax        int    `json:"bpfLbMax"`
	Note            string `json:"note,omitempty"`
}

// K8sEtcdHandler GET ?cluster= → etcd 容量/健康 + 数据面现状
func K8sEtcdHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(k8sPluginID, w) {
		return
	}
	cluster := r.URL.Query().Get("cluster")
	if !reK8sClusterID.MatchString(cluster) {
		WriteJSON(w, map[string]any{"ok": false, "error": "cluster 参数非法"})
		return
	}
	target, err := resolveClusterMaster(cluster)
	if err != nil {
		WriteJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	type nodeRef struct {
		name          string
		controlPlane  bool
		target        masterTarget
	}
	refs := k8sEtcdNodeRefs(cluster, target)
	if len(refs) == 0 {
		WriteJSON(w, map[string]any{"ok": false, "error": "没拿到节点清单（集群不可达？）"})
		return
	}

	members := []etcdMember{}
	planes := []dplaneNode{}
	tuning := map[string]string{}
	proxyMode := k8sProxyMode(cluster)
	var firstErr string
	for _, ref := range refs {
		if ref.target.Mode == "" {
			planes = append(planes, dplaneNode{Node: ref.name, Kind: "unknown", Note: "节点未在主机清单, 无法 SSH"})
			continue
		}
		out, err := runOnMaster(ref.target, []string{"sh", "-c", etcdProbeScript})
		if err != nil {
			log.Printf("[K8S-ETCD] %s(%s) 采集失败: %v", ref.name, ref.target.describe(), err)
			if firstErr == "" {
				firstErr = ref.name + " 采集失败: " + err.Error()
			}
			planes = append(planes, dplaneNode{Node: ref.name, Kind: "unknown", Note: "采集失败"})
			continue
		}
		sec := parseSections(out)
		if len(tuning) == 0 {
			for _, ln := range sec["OPSCORE_ETCD_CONF"] {
				if k, v, ok := strings.Cut(ln, "="); ok {
					tuning[strings.TrimSpace(k)] = strings.TrimSpace(v)
				}
			}
		}
		if strings.TrimSpace(first(sec["OPSCORE_ETCD_PRESENT"])) == "yes" {
			members = append(members, buildEtcdMember(ref.name, sec))
		}
		plane := buildDataplane(ref.name, proxyMode, sec["OPSCORE_DATAPLANE"])
		plane.Target = ref.target.describe()
		if len(sec["OPSCORE_DATAPLANE"]) == 0 {
			plane.Raw = headForLog(out, 200)
			log.Printf("[K8S-ETCD] %s(%s) 数据面段为空, 原始输出: %q", ref.name, ref.target.describe(), plane.Raw)
		}
		planes = append(planes, plane)
	}

	kind := "unknown"
	for _, p := range planes {
		switch p.Kind {
		case "cilium":
			kind = "cilium"
		case "ipvs":
			if kind != "cilium" {
				kind = "ipvs"
			}
		case "iptables":
			if kind == "unknown" {
				kind = "iptables"
			}
		}
	}

	resp := map[string]any{
		"ok":           true,
		"cluster":      cluster,
		"target":       target.describe(),
		"members":      members,
		"controlPlane": len(members),
		"ha":           len(members) > 1,
		"tuning":       tuning,
		"dataplane":    map[string]any{"kind": kind, "nodes": planes},
		"collectedAt":  time.Now().Unix(),
	}
	if len(members) == 0 && firstErr != "" {
		resp["reason"] = firstErr
	}
	WriteJSON(w, resp)
}

// k8sEtcdNodeRefs 列出节点并逐个解析到执行目标; 优先 API, 失败退回主控制面一台。
func k8sEtcdNodeRefs(cluster string, primary masterTarget) []struct {
	name         string
	controlPlane bool
	target       masterTarget
} {
	type ref = struct {
		name         string
		controlPlane bool
		target       masterTarget
	}
	var out []ref
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	if cs, err := k8sMgr.Clientset(cluster); err == nil && cs != nil {
		if nl, err := cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{}); err == nil {
			for _, n := range nl.Items {
				_, cp := n.Labels["node-role.kubernetes.io/control-plane"]
				if !cp {
					_, cp = n.Labels["node-role.kubernetes.io/master"]
				}
				out = append(out, ref{name: n.Name, controlPlane: cp, target: etcdNodeTarget(n.Name, primary)})
			}
		}
	}
	if len(out) == 0 {
		out = append(out, ref{name: localHostname(), controlPlane: true, target: primary})
	}
	return out
}

// etcdNodeTarget 把节点名解析成执行目标: 主控制面 → 沿用 primary; 其它 → 清单反查。
func etcdNodeTarget(name string, primary masterTarget) masterTarget {
	if primary.Mode == "ssh" && primary.HostID != "" {
		if h := locateMasterHostByName(name); h != "" {
			return masterTarget{Mode: "ssh", HostID: h, Reason: "节点 " + name}
		}
		if name == localHostname() {
			return primary
		}
	}
	if name == localHostname() {
		return masterTarget{Mode: "local", Reason: "本机节点"}
	}
	if h := locateMasterHostByName(name); h != "" {
		return masterTarget{Mode: "ssh", HostID: h, Reason: "节点 " + name}
	}
	return masterTarget{}
}

// buildEtcdMember 解析 etcd 各段输出。
func buildEtcdMember(node string, sec map[string][]string) etcdMember {
	m := etcdMember{Node: node, Reachable: true, Source: "none"}
	if img := strings.TrimSpace(first(sec["OPSCORE_ETCD_IMAGE"])); img != "" {
		if i := strings.LastIndex(img, ":"); i >= 0 {
			m.Version = strings.TrimSpace(img[i+1:])
		}
	}
	for _, ln := range sec["OPSCORE_ETCD_CONF"] {
		if k, v, ok := strings.Cut(ln, "="); ok && strings.TrimSpace(k) == "quota-backend-bytes" {
			m.QuotaBytes = parseUint64(strings.TrimSpace(v))
		}
	}
	// /metrics: 只需几个键; 值可能是科学计数法, 也可能是带标签的样本
	for _, ln := range sec["OPSCORE_ETCD_METRICS"] {
		name, val, ok := promSample(ln)
		if !ok {
			continue
		}
		switch name {
		case "etcd_mvcc_db_total_size_in_bytes":
			m.DBBytes = uint64(val)
			m.Source = "metrics"
		case "etcd_mvcc_db_total_size_in_use_in_bytes":
			m.DBInUseBytes = uint64(val)
		case "etcd_server_quota_backend_bytes":
			m.QuotaBytes = uint64(val)
		case "etcd_server_has_leader":
			m.HasLeader = val >= 1
		case "etcd_server_leader_changes_seen_total":
			m.LeaderChanges = uint64(val)
		case "etcd_server_version":
			if v := promLabel(ln, "server_version"); v != "" {
				m.Version = v
			}
		}
	}
	files := sec["OPSCORE_ETCD_FILES"]
	if len(files) > 0 {
		f := strings.Fields(files[0])
		if len(f) >= 1 {
			m.DBFileBytes = parseUint64(f[0])
		}
	}
	if len(files) > 1 {
		m.DataDirBytes = parseUint64(strings.Fields(files[1])[0])
	}
	m.DataDir = "/var/lib/etcd"
	if m.Source == "none" && m.DBFileBytes > 0 {
		m.Source = "files"
	}
	if m.DBBytes > 0 && m.DBInUseBytes > 0 && m.DBBytes >= m.DBInUseBytes {
		m.FragPct = float64(m.DBBytes-m.DBInUseBytes) / float64(m.DBBytes) * 100
	}
	switch {
	case m.Source == "none":
		m.Note = "既拿不到运行指标也读不到数据目录（证书/权限/路径不符）"
	case m.Source == "files":
		m.Note = "运行指标不可得，下面按数据目录推算（含预分配，偏大）"
	}
	return m
}

// buildDataplane 解析数据面段; mode 来自 API(kube-proxy ConfigMap), 其余来自节点。
func buildDataplane(node, mode string, lines []string) dplaneNode {
	p := dplaneNode{Node: node, Kind: "unknown", Mode: mode}
	for _, ln := range lines {
		k, v, ok := strings.Cut(strings.TrimSpace(ln), "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch k {
		case "cilium_cni":
			if atoi(v) > 0 {
				p.Kind = "cilium"
			}
		case "cilium_cli":
			if v == "yes" {
				p.Kind = "cilium"
			}
		case "cilium_maps":
			p.CiliumMaps = atoi(v)
			if p.CiliumMaps > 0 {
				p.Kind = "cilium"
			}
		case "cilium_lb_entries":
			p.CiliumLBEntries = atoi(v)
		case "bpf_lb_max":
			p.BPFLBMax = atoi(v)
			if p.BPFLBMax > 0 {
				p.Kind = "cilium"
			}
		case "ipvs_rs":
			p.RealServers = atoi(v)
		case "ipvs_conns":
			p.IPVSConns = atoi(v)
		case "ipvs_tab_bits":
			p.IPVSTabBits = atoi(v)
		case "iptables_rules":
			p.IptablesRules = atoi(v)
		}
	}
	if p.Kind != "cilium" {
		switch {
		case strings.Contains(mode, "ipvs"), p.RealServers > 0, p.IPVSTabBits > 0:
			p.Kind = "ipvs"
		case strings.Contains(mode, "iptables"):
			p.Kind = "iptables"
		case p.IptablesRules > 0:
			p.Kind = "iptables"
		}
	}
	if p.Kind == "cilium" && p.BPFLBMax == 0 {
		p.Note = "未取到 bpf map 容量(bpftool 缺失或无 cilium_lb map)"
	}
	if p.Kind == "ipvs" && p.RealServers == 0 && p.IPVSConns == 0 {
		p.Note = "没读到 ipvs 计数(/proc/net/ip_vs 不可读)"
	}
	return p
}

// k8sProxyMode 从 kube-proxy ConfigMap 读代理模式(ipvs/iptables/nftables), 失败返回空。
func k8sProxyMode(cluster string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cs, err := k8sMgr.Clientset(cluster)
	if err != nil || cs == nil {
		return ""
	}
	cm, err := cs.CoreV1().ConfigMaps("kube-system").Get(ctx, "kube-proxy", metav1.GetOptions{})
	if err != nil || cm == nil {
		return ""
	}
	for _, ln := range strings.Split(cm.Data["config.conf"], "\n") {
		if v, ok := cutPrefixFold(strings.TrimSpace(ln), "mode:"); ok {
			return strings.Trim(strings.TrimSpace(v), `"`)
		}
	}
	return ""
}

// cutPrefixFold 前缀匹配(不区分大小写), 返回剩余部分。
func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix) {
		return s[len(prefix):], true
	}
	return "", false
}

// parseSections 把哨兵分段的输出拆成 段名 → 行; 段名保留 OPSCORE_ 前缀(如 OPSCORE_DATAPLANE)。
func parseSections(out string) map[string][]string {
	sec := map[string][]string{}
	cur := ""
	for _, raw := range strings.Split(out, "\n") {
		ln := strings.TrimRight(raw, "\r")
		if strings.HasPrefix(ln, "__") && strings.HasSuffix(ln, "__") {
			name := strings.TrimSuffix(strings.TrimPrefix(ln, "__"), "__")
			if name == "OPSCORE_END" {
				cur = ""
				continue
			}
			cur = name
			continue
		}
		if cur != "" && strings.TrimSpace(ln) != "" {
			sec[cur] = append(sec[cur], ln)
		}
	}
	return sec
}

func first(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return lines[0]
}

// promSample 解析一行 Prometheus 文本样本: "name{labels} 1.23e+06" / "name 42"
func promSample(line string) (string, float64, bool) {
	i := strings.LastIndex(line, " ")
	if i <= 0 {
		return "", 0, false
	}
	name := line[:i]
	if b := strings.IndexByte(name, '{'); b >= 0 {
		name = name[:b]
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(line[i+1:]), 64)
	if err != nil {
		return "", 0, false
	}
	return name, v, true
}

// promLabel 取一行样本里某个标签的值(仅处理单层 kv)。
func promLabel(line, key string) string {
	i := strings.IndexByte(line, '{')
	j := strings.IndexByte(line, '}')
	if i < 0 || j <= i {
		return ""
	}
	for _, kv := range strings.Split(line[i+1:j], ",") {
		k, v, ok := strings.Cut(kv, "=")
		if ok && strings.TrimSpace(k) == key {
			return strings.Trim(strings.TrimSpace(v), `"`)
		}
	}
	return ""
}

func atoi(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

// headForLog 压平换行并截断, 便于把远端原始输出塞进一行日志/响应。
func headForLog(s string, n int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " | ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
