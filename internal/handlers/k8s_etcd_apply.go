package handlers

// etcd 可视化操作(只碰控制面的启动参数与自身维护命令, 不走 kube-apiserver 之外的东西):
//   action=quota          调整 --quota-backend-bytes(改两处: /etc/kubernetes/manifests/etcd.yaml 与
//                         kube-system/kubeadm-config 的 ClusterConfiguration —— 只改前者会在 kubeadm upgrade 时被覆写回去)
//   action=quota-rollback 用最近一次改动前的备份把上限写回原值
//   action=restart-etcd   让配置生效: 删掉 etcd 镜像 Pod, kubelet 会按 manifest 当前内容重建
//   action=defrag         在 etcd pod 内跑 etcdctl defrag 回收碎片(compact 已由 auto-compaction-retention 负责)
//
// 契约(与 k8s 动作目录同风格): GET .../etcd-apply/preview 出"将要改什么"(dry-run),
// POST .../etcd-apply {confirm:true} 才真执行。
//
// 生效判定不等死: kubelet 重建 etcd 静态 Pod 实测 70 秒~5 分钟+, 所以只同步等 90 秒;
// 到点还没生效就返回 pending(stale=true, 带期望值与运行值), 界面据此给出「立即生效」按钮。
// 动手后**中途**失败才自动回滚, 结果里带 rolledBack。
//
// 硬约束:
//   1) 新 quota 必须 > 当前后端 DB 大小 × 1.2 —— 低于实际占用会立刻触发 etcd NOSPACE(整个集群只读);
//   2) 上限封顶 64GiB, 且必须 1MiB 对齐(etcd 参数是字节数, 但没人会填零头);
//   3) 改完核对 etcd_server_quota_backend_bytes == 新值; 生效前的失败一律回滚两处;
//   4) 远程脚本不能出现单引号(RunOnTarget 会把整段包进单引号, 见 target.go 的投递方式) —— 用 base64 传 YAML;
//   5) **不能碰 /etc/kubernetes/manifests 目录里的任何额外文件**: kubelet 会遍历该目录,
//      无扩展名的文件(如 sed -i 留下的 sedXXXX)会让它整批丢弃本次更新(2026-09-26 实测: 改完 manifest
//      kubelet 报 file.go:108 "Unable to process watch event", 之后连原地重写都不再触发重建)。
//      所以: 写文件用"sed 到 /tmp + cat 回原文件"(同 inode, 目录里不出现野文件), 备份也放 /tmp。

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	etcdQuotaMinStep = int64(1) << 20  // 1MiB 对齐
	etcdQuotaMax     = int64(64) << 30 // 64GiB 封顶
	etcdQuotaFactor  = 1.2             // 新值至少要 > 当前占用 × 1.2
	etcdKubeadmCM    = "kubeadm-config"
	etcdManifestPath = "/etc/kubernetes/manifests/etcd.yaml"
	etcdBackupDir    = "/tmp" // 备份绝不能落在 staticPodPath 里, 见硬约束 5
	etcdWaitSeconds  = 90     // 同步等待 kubelet 重建的上限, 到点如实返回"待生效"
)

type etcdApplyPreviewResp struct {
	OK       bool     `json:"ok"`
	Action   string   `json:"action"`
	Target   string   `json:"target"`
	Files    []string `json:"changes"`  // 每项一行"文件: 旧 → 新"的可读描述
	Commands []string `json:"commands"` // defrag / 重启 etcd 这类就是要执行的命令本身
	Warnings []string `json:"warnings,omitempty"`
	Error    string   `json:"error,omitempty"`
}

// etcdApplyState 当前状态。ConfBytes 与 RunningBytes 分开记:
// 前者是 manifest 里写的期望值, 后者是 etcd 进程真正在用的值 —— 两者不等就是"改了没生效"。
type etcdApplyState struct {
	ConfBytes    int64
	RunningBytes int64
	DBBytes      int64
	EtcdPod      string
	Manifest     string // manifest 原文(执行时按它改, 回滚也用它)
	CMYAML       string // 原始 kubeadm-config YAML
}

// etcdQuotaProbeScript 只取两行关键指标: 改完 quota 后用它轮询是否生效(比整份探测脚本快且不易被拖住)
const etcdQuotaProbeScript = `for d in /etc/kubernetes/pki/etcd /etc/etcd/pki /etc/ssl/etcd; do
  if [ -f "$d/server.crt" ] && [ -f "$d/server.key" ]; then
    curl -s --max-time 6 --cert "$d/server.crt" --key "$d/server.key" --cacert "$d/ca.crt" https://127.0.0.1:2379/metrics 2>/dev/null | grep -E "^etcd_server_quota_backend_bytes|^etcd_server_has_leader"
    break
  fi
done`

// parsePromValue 从 Prometheus 文本里取某个指标的数值(取不到返回 0)
func parsePromValue(text, name string) float64 {
	for _, ln := range strings.Split(text, "\n") {
		if n, v, ok := promSample(ln); ok && n == name {
			return v
		}
	}
	return 0
}

var reManifestQuota = regexp.MustCompile(`--quota-backend-bytes=[0-9]+`)
var reCMQuota = regexp.MustCompile(`(?m)^(\s*quota-backend-bytes:\s*)"?([0-9]+)"?`)

// cmQuota 从 kubeadm-config 的 YAML 文本里取 quota 值(取不到返回 0)
func cmQuota(yamlText string) int64 {
	m := reCMQuota.FindStringSubmatch(yamlText)
	if m == nil {
		return 0
	}
	n, _ := strconv.ParseInt(m[2], 10, 64)
	return n
}

func etcdAdminKubeconfigArg() string { return "--kubeconfig=/etc/kubernetes/admin.conf" }

// loadEtcdApplyState 采一次当前状态(manifest / 运行指标 / pod 名 / kubeadm-config), 预览与执行共用
func loadEtcdApplyState(t masterTarget) (*etcdApplyState, error) {
	out, err := runOnMaster(t, []string{"sh", "-c", etcdProbeScript})
	if err != nil {
		return nil, fmt.Errorf("探测 etcd 失败: %w", err)
	}
	sec := parseSections(out)
	if strings.TrimSpace(first(sec["OPSCORE_ETCD_PRESENT"])) != "yes" {
		return nil, fmt.Errorf("目标不是 etcd 静态 Pod 形态(kubeadm), 暂不支持可视化操作")
	}
	m := buildEtcdMember("", sec)
	st := &etcdApplyState{ConfBytes: int64(m.ConfQuotaBytes), RunningBytes: int64(m.QuotaBytes), DBBytes: int64(m.DBBytes)}
	if st.RunningBytes == 0 {
		st.RunningBytes = st.ConfBytes // 拿不到 metrics 时只能按已生效处理
	}
	// pod 名 / manifest 原文 / kubeadm-config 一起拉: 都是只读命令
	podOut, _ := runOnMaster(t, []string{"sh", "-c", "kubectl " + etcdAdminKubeconfigArg() + " -n kube-system get pod -l component=etcd -o name 2>/dev/null | head -1"})
	st.EtcdPod = strings.TrimPrefix(strings.TrimSpace(podOut), "pod/")
	// 期望值以 manifest 原文为准(探测段的 conf 会连带读 /etc/etcd 下的其他配置, 静态 Pod 形态下不如直接看文件)
	manOut, _ := runOnMaster(t, []string{"sh", "-c", "cat " + etcdManifestPath + " 2>/dev/null"})
	st.Manifest = manOut
	if mm := reManifestQuota.FindString(manOut); mm != "" {
		if n, e := strconv.ParseInt(strings.TrimPrefix(mm, "--quota-backend-bytes="), 10, 64); e == nil {
			st.ConfBytes = n
		}
	}
	if st.ConfBytes <= 0 {
		return nil, fmt.Errorf("没读到 manifest 里的 --quota-backend-bytes")
	}
	cmOut, cmErr := runOnMaster(t, []string{"sh", "-c", "kubectl " + etcdAdminKubeconfigArg() + " -n kube-system get cm " + etcdKubeadmCM + " -o yaml 2>/dev/null"})
	if cmErr != nil || !strings.Contains(cmOut, "quota-backend-bytes") {
		return nil, fmt.Errorf("读不到 kubeadm-config 里的 quota-backend-bytes(该集群可能不是 kubeadm 部署)")
	}
	st.CMYAML = cmOut
	return st, nil
}

// etcdQuotaValidate 硬约束校验(预览与执行都走这里, 避免两条路漂移)
func etcdQuotaValidate(next, dbBytes int64) error {
	if next <= 0 {
		return fmt.Errorf("目标值必须是正整数(字节)")
	}
	if next%etcdQuotaMinStep != 0 {
		return fmt.Errorf("目标值需按 1MiB 对齐")
	}
	if next > etcdQuotaMax {
		return fmt.Errorf("目标值超过 64GiB 上限")
	}
	if dbBytes > 0 && float64(next) < float64(dbBytes)*etcdQuotaFactor {
		return fmt.Errorf("目标值太小: 当前后端已占 %s, 至少要到 %s(撞上会触发 etcd NOSPACE, 整个集群变只读)",
			humanBytes(dbBytes), humanBytes(int64(float64(dbBytes)*etcdQuotaFactor)))
	}
	return nil
}

// humanBytes 复用 apps.go 里的实现(同一 package, 别写第二份)

// K8sEtcdApplyPreviewHandler GET ?cluster=&action=quota|defrag|restart-etcd&quotaBytes=
func K8sEtcdApplyPreviewHandler(w http.ResponseWriter, r *http.Request) {
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
	action := r.URL.Query().Get("action")
	target, err := resolveClusterMaster(cluster)
	if err != nil {
		WriteJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	resp := etcdApplyPreviewResp{OK: true, Action: action, Target: target.describe()}
	switch action {
	case "defrag", "restart-etcd", "restart-kubelet":
		st, serr := loadEtcdApplyState(target)
		if serr != nil || st.EtcdPod == "" {
			msg := "没找到 etcd Pod"
			if serr != nil {
				msg = serr.Error()
			}
			WriteJSON(w, map[string]any{"ok": false, "error": msg})
			return
		}
		if action == "defrag" {
			resp.Commands = []string{"kubectl -n kube-system exec " + st.EtcdPod + " -- etcdctl --endpoints=https://127.0.0.1:2379 --cacert=/etc/kubernetes/pki/etcd/ca.crt --cert=/etc/kubernetes/pki/etcd/server.crt --key=/etc/kubernetes/pki/etcd/server.key defrag"}
			resp.Warnings = append(resp.Warnings, "defrag 是阻塞式维护: 执行期间该成员不可读写(单控制面集群 = apiserver 短暂不可用), 小库几秒内完成")
		} else {
			resp.Commands = []string{"kubectl -n kube-system delete pod " + st.EtcdPod}
			resp.Files = []string{fmt.Sprintf("manifest 期望 %s · etcd 运行中 %s", humanBytes(st.ConfBytes), humanBytes(st.RunningBytes))}
			if action == "restart-kubelet" {
				resp.Commands = []string{"systemctl stop kubelet", "kubectl -n kube-system delete pod " + st.EtcdPod, "systemctl start kubelet"}
				resp.Warnings = append(resp.Warnings,
					"最后一档: 只有在「立即生效」之后 etcd 仍用旧值时才需要 —— 说明 kubelet 一直拿 API 里的旧镜像 Pod 规格重建, 不再读 manifest",
					"停/起 kubelet 不会动已经跑着的业务 Pod, 但该节点会短暂不上报状态")
			} else if st.ConfBytes == st.RunningBytes {
				resp.Warnings = append(resp.Warnings, "当前配置与运行值一致, 重启不会带来变化 —— 它只在「配置已改但 etcd 没重建」时用")
			} else {
				resp.Warnings = append(resp.Warnings, "删除镜像 Pod 后 kubelet 会按 manifest 当前内容重建 etcd: apiserver 中断约 1 分钟, 期间集群不可操作")
			}
		}
		WriteJSON(w, resp)
		return
	case "quota-rollback":
		st, err := loadEtcdApplyState(target)
		if err != nil {
			WriteJSON(w, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		bak, prev, berr := newestEtcdQuota(target)
		if berr != nil {
			WriteJSON(w, map[string]any{"ok": false, "error": berr.Error()})
			return
		}
		if prev == st.ConfBytes && prev == st.RunningBytes {
			WriteJSON(w, map[string]any{"ok": false, "error": "当前已是备份里的原值 " + humanBytes(prev) + "，无需回滚"})
			return
		}
		if verr := etcdQuotaValidate(prev, st.DBBytes); verr != nil {
			WriteJSON(w, map[string]any{"ok": false, "error": "回滚值不合法(备份可能过旧): " + verr.Error()})
			return
		}
		resp.Files = []string{
			fmt.Sprintf("/etc/kubernetes/manifests/etcd.yaml: --quota-backend-bytes=%d → %d (%s → %s)",
				st.ConfBytes, prev, humanBytes(st.ConfBytes), humanBytes(prev)),
			fmt.Sprintf("kube-system/%s: quota-backend-bytes: \"%d\" → \"%d\"", etcdKubeadmCM, st.ConfBytes, prev),
		}
		resp.Warnings = append(resp.Warnings,
			fmt.Sprintf("回滚值取自备份 %s", bak),
			"走的还是同一条改上限流程: kubelet 会重建 etcd 静态 Pod, apiserver 会抖")
		WriteJSON(w, resp)
		return
	case "quota":
		next, perr := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("quotaBytes")), 10, 64)
		if perr != nil {
			WriteJSON(w, map[string]any{"ok": false, "error": "quotaBytes 非法"})
			return
		}
		st, err := loadEtcdApplyState(target)
		if err != nil {
			WriteJSON(w, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		if verr := etcdQuotaValidate(next, st.DBBytes); verr != nil {
			WriteJSON(w, map[string]any{"ok": false, "error": verr.Error(), "currentBytes": st.ConfBytes, "dbBytes": st.DBBytes})
			return
		}
		if st.ConfBytes == next && st.RunningBytes != next {
			WriteJSON(w, map[string]any{"ok": false, "stale": true, "currentBytes": st.ConfBytes, "runningBytes": st.RunningBytes,
				"error": "manifest 已经是这个值, 但 etcd 还在用 " + humanBytes(st.RunningBytes) + " —— 改点「立即生效」就行, 不必再写一遍"})
			return
		}
		resp.Files = []string{
			fmt.Sprintf("/etc/kubernetes/manifests/etcd.yaml: --quota-backend-bytes=%d → %d (%s → %s)",
				st.ConfBytes, next, humanBytes(st.ConfBytes), humanBytes(next)),
			fmt.Sprintf("kube-system/%s: quota-backend-bytes: \"%d\" → \"%d\"", etcdKubeadmCM, st.ConfBytes, next),
		}
		resp.Warnings = append(resp.Warnings,
			"改完 kubelet 会重建 etcd 静态 Pod(单控制面集群 apiserver 会抖), 实测重建 70 秒起; 界面同步等约 90 秒, 没等到会提示「待生效」并可点「立即生效」",
			fmt.Sprintf("当前后端占用 %s, 新上限 %s", humanBytes(st.DBBytes), humanBytes(next)))
		if cmNow := cmQuota(st.CMYAML); cmNow > 0 && cmNow != st.ConfBytes {
			resp.Warnings = append(resp.Warnings, fmt.Sprintf("注意: 两处当前并不一致(manifest %s, kubeadm-config 里 %s), 本次会把两处都写成目标值",
				humanBytes(st.ConfBytes), humanBytes(cmNow)))
		}
		if st.RunningBytes != st.ConfBytes {
			resp.Warnings = append(resp.Warnings, fmt.Sprintf("注意: etcd 运行中是 %s, 与 manifest 的 %s 不一致 —— 上一次的改动还没生效",
				humanBytes(st.RunningBytes), humanBytes(st.ConfBytes)))
		}
		WriteJSON(w, resp)
		return
	}
	WriteJSON(w, map[string]any{"ok": false, "error": "action 只支持 quota/quota-rollback/defrag/restart-etcd/restart-kubelet"})
}

// K8sEtcdApplyHandler POST {cluster, action, quotaBytes, confirm}
func K8sEtcdApplyHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(k8sPluginID, w) {
		return
	}
	var body struct {
		Cluster    string `json:"cluster"`
		Action     string `json:"action"`
		QuotaBytes int64  `json:"quotaBytes"`
		Confirm    bool   `json:"confirm"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, "invalid body", http.StatusBadRequest)
		return
	}
	if !reK8sClusterID.MatchString(body.Cluster) {
		WriteJSON(w, map[string]any{"ok": false, "error": "cluster 参数非法"})
		return
	}
	if !body.Confirm {
		WriteJSON(w, map[string]any{"ok": false, "error": "需要 confirm=true 才执行(预览请走 /etcd-apply/preview)"})
		return
	}
	target, err := resolveClusterMaster(body.Cluster)
	if err != nil {
		WriteJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	st, err := loadEtcdApplyState(target)
	if err != nil {
		WriteJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	switch body.Action {
	case "defrag":
		if st.EtcdPod == "" {
			WriteJSON(w, map[string]any{"ok": false, "error": "没找到 etcd Pod"})
			return
		}
		start := time.Now()
		out, err := runOnMaster(target, []string{"sh", "-c", "kubectl " + etcdAdminKubeconfigArg() + " -n kube-system exec " + st.EtcdPod + " -- etcdctl --endpoints=https://127.0.0.1:2379 --cacert=/etc/kubernetes/pki/etcd/ca.crt --cert=/etc/kubernetes/pki/etcd/server.crt --key=/etc/kubernetes/pki/etcd/server.key --command-timeout=120s defrag 2>&1"})
		dur := time.Since(start).Round(time.Millisecond).String()
		if err != nil {
			log.Printf("[k8s-etcd] defrag 失败: %v out=%q", err, headForLog(out, 200))
			WriteJSON(w, map[string]any{"ok": false, "error": "defrag 失败: " + err.Error(), "output": headForLog(out, 300), "duration": dur})
			return
		}
		log.Printf("[k8s-etcd] defrag 完成 %s out=%q", dur, headForLog(out, 120))
		WriteJSON(w, map[string]any{"ok": true, "output": headForLog(out, 300), "duration": dur})
		return
	case "quota":
		if verr := etcdQuotaValidate(body.QuotaBytes, st.DBBytes); verr != nil {
			WriteJSON(w, map[string]any{"ok": false, "error": verr.Error()})
			return
		}
		WriteJSON(w, applyEtcdQuota(target, st, body.QuotaBytes))
		return
	case "quota-rollback":
		// 回滚 = 用最近一次改动前的备份, 走同一条"改成某个值"的路(校验/两处一致都在里面)。
		st, err := loadEtcdApplyState(target)
		if err != nil {
			WriteJSON(w, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		_, prev, berr := newestEtcdQuota(target)
		if berr != nil {
			WriteJSON(w, map[string]any{"ok": false, "error": berr.Error()})
			return
		}
		if prev == st.ConfBytes && prev == st.RunningBytes {
			WriteJSON(w, map[string]any{"ok": true, "noop": true, "quotaBytes": prev, "message": "当前已是备份里的原值, 无需回滚"})
			return
		}
		if verr := etcdQuotaValidate(prev, st.DBBytes); verr != nil {
			WriteJSON(w, map[string]any{"ok": false, "error": "回滚值不合法(备份可能过旧): " + verr.Error(), "previousBytes": prev})
			return
		}
		res := applyEtcdQuota(target, st, prev)
		res["rollbackTo"] = prev
		WriteJSON(w, res)
		return
	case "restart-etcd":
		if st.EtcdPod == "" {
			WriteJSON(w, map[string]any{"ok": false, "error": "没找到 etcd Pod"})
			return
		}
		if st.ConfBytes == st.RunningBytes {
			WriteJSON(w, map[string]any{"ok": true, "noop": true, "message": "配置与运行值一致, 没做重启"})
			return
		}
		// 删的是 API 里的镜像 Pod 对象: kubelet 发现镜像 Pod 没了, 会按 manifest 当前内容重建整个静态 Pod。
		if out, err := runOnMaster(target, []string{"sh", "-c", "kubectl " + etcdAdminKubeconfigArg() + " -n kube-system delete pod " + st.EtcdPod + " --wait=false 2>&1"}); err != nil {
			WriteJSON(w, map[string]any{"ok": false, "error": "删除镜像 Pod 失败: " + err.Error(), "output": headForLog(out, 200)})
			return
		}
		res := waitForEtcdQuota(target, st, st.ConfBytes, 150*time.Second)
		if res["pending"] == true {
			res["message"] = "已经重启过一次 etcd, 它仍用旧值 —— 这台 kubelet 拿的是 API 里的旧规格, 不再读 manifest, 要靠「重启 kubelet 生效」那一档"
		}
		WriteJSON(w, res)
		return
	case "restart-kubelet":
		// 只在「立即生效」也没把 etcd 换到新值时用这一档。为什么需要它(2026-09-26 实测):
		// kubelet 会用 API 里的镜像 Pod 规格覆盖它对 manifest 的解析结果 —— 一旦镜像 Pod 是旧值,
		// 之后每次重建都拿旧值, 文件改成什么都不生效。停 kubelet → 删镜像 Pod → 起 kubelet,
		// 它既没有内存态也没有镜像对象, 只能重新解析 manifest。重启 kubelet 不动已运行的业务 Pod。
		if st.EtcdPod == "" {
			WriteJSON(w, map[string]any{"ok": false, "error": "没找到 etcd Pod"})
			return
		}
		if st.ConfBytes == st.RunningBytes {
			WriteJSON(w, map[string]any{"ok": true, "noop": true, "message": "配置与运行值一致, 不用动 kubelet"})
			return
		}
		seq := "systemctl stop kubelet; " +
			"kubectl " + etcdAdminKubeconfigArg() + " -n kube-system delete pod " + st.EtcdPod + " --wait=false --timeout=15s 2>/dev/null; " +
			"sleep 2; systemctl start kubelet; echo kubelet-restarted"
		if out, err := runOnMaster(target, []string{"sh", "-c", seq}); err != nil {
			WriteJSON(w, map[string]any{"ok": false, "error": "重启 kubelet 失败: " + err.Error(), "output": headForLog(out, 200)})
			return
		}
		res := waitForEtcdQuota(target, st, st.ConfBytes, 180*time.Second)
		if res["pending"] == true {
			res["message"] = "kubelet 已重启, etcd 还没换上新值 —— 再刷新一次卡片; 仍是旧值就登节点看 systemctl status kubelet 与 /etc/kubernetes/manifests/etcd.yaml"
		}
		WriteJSON(w, res)
		return
	}
	WriteJSON(w, map[string]any{"ok": false, "error": "action 只支持 quota/quota-rollback/defrag/restart-etcd/restart-kubelet"})
}

// newestEtcdBackup 取 /tmp 下最近一次改动前的 manifest 备份(备份不放 staticPodPath, 见硬约束 5)
func newestEtcdBackup(t masterTarget) string {
	out, err := runOnMaster(t, []string{"sh", "-c", "ls -1t " + etcdBackupDir + "/opscore-etcd-manifest-*.bak 2>/dev/null | head -1"})
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// newestEtcdQuota 备份路径 + 备份里那份 manifest 的 quota 值(预览与回滚共用, 免得两条路算出不同的"原值")
func newestEtcdQuota(t masterTarget) (string, int64, error) {
	bak := newestEtcdBackup(t)
	if bak == "" {
		return "", 0, fmt.Errorf("找不到改动前的备份(只有走过可视化改动的节点才有备份), 无法回滚")
	}
	qOut, _ := runOnMaster(t, []string{"sh", "-c", "grep -m1 -o -- --quota-backend-bytes=[0-9]* " + bak})
	prev, _ := strconv.ParseInt(strings.TrimPrefix(strings.TrimSpace(qOut), "--quota-backend-bytes="), 10, 64)
	if prev <= 0 {
		return bak, 0, fmt.Errorf("备份里读不到原始 quota 值: %s", bak)
	}
	return bak, prev, nil
}

// applyEtcdQuota 真改: 备份 → 原地替换 manifest → 改 kubeadm-config → 等 etcd 回来核对新上限。
// 两处都改是因为只改 manifest 会在下次 kubeadm upgrade 时被覆写回去。
func applyEtcdQuota(t masterTarget, st *etcdApplyState, next int64) map[string]any {
	// 0) 动手之前把所有能判的都判掉 —— 实测教训: 先改了 manifest 才发现 ConfigMap 不匹配,
	//    那次白重启了一次 etcd(apiserver 跟着抖), 所以校验一律前置。
	if st.ConfBytes == next {
		if st.RunningBytes == next {
			return map[string]any{"ok": true, "quotaBytes": next, "dbBytes": st.DBBytes, "noop": true,
				"message": "目标值与当前一致, 未做任何改动"}
		}
		return map[string]any{"ok": true, "quotaBytes": next, "stale": true, "runningBytes": st.RunningBytes,
			"message": "manifest 已经是目标值, 但 etcd 还在用 " + humanBytes(st.RunningBytes) + " —— 点「立即生效」重启 etcd 即可, 不必重复改写"}
	}
	if !reCMQuota.MatchString(st.CMYAML) {
		return map[string]any{"ok": false, "error": "kubeadm-config 里没有 quota-backend-bytes(集群可能不是 kubeadm 部署), 未做任何改动"}
	}

	ts := time.Now().Format("20060102-150405")
	bak := etcdBackupDir + "/opscore-etcd-manifest-" + ts + ".bak"

	// 1) 备份 manifest 到 /tmp(不能留在 manifests 目录里)
	if out, err := runOnMaster(t, []string{"sh", "-c", "cp " + etcdManifestPath + " " + bak + " && echo ok"}); err != nil {
		return map[string]any{"ok": false, "error": "备份 manifest 失败: " + err.Error(), "output": headForLog(out, 200)}
	}

	// 2) 原地替换启动参数: sed 的结果先落 /tmp, 再 cat 回原文件(同 inode)。
	//    kubelet 会因文件内容变化重建 etcd 静态 Pod。
	sedExpr := "s/--quota-backend-bytes=[0-9]*/--quota-backend-bytes=" + strconv.FormatInt(next, 10) + "/"
	writeScript := "sed " + "\"" + sedExpr + "\" " + etcdManifestPath + " > /tmp/opscore-etcd-m.yaml && cat /tmp/opscore-etcd-m.yaml > " + etcdManifestPath +
		" && grep -o -- --quota-backend-bytes=[0-9]* " + etcdManifestPath
	out, err := runOnMaster(t, []string{"sh", "-c", writeScript})
	// 必须核对"文件里真的是新值": 只看退出码的话, sed 没匹配上(格式变了)也会静默继续 ——
	// 那样 kubelet 不会重建, 我们却以为改好了(实测踩过: 等 5 分钟 etcd 仍报旧值)。
	if err != nil || !strings.Contains(out, strconv.FormatInt(next, 10)) {
		_ = restoreManifest(t, bak)
		return map[string]any{"ok": false, "error": "改 manifest 后核对失败(文件里没出现新值), 已回滚", "output": headForLog(out, 200), "rolledBack": true}
	}

	// 3) 同步改 kubeadm-config(不改这处, 下次 kubeadm upgrade 会把 manifest 覆写回去)
	newCM := reCMQuota.ReplaceAllString(st.CMYAML, "${1}\""+strconv.FormatInt(next, 10)+"\"")
	if out, err := runOnMaster(t, []string{"sh", "-c", applyCMScript(newCM)}); err != nil {
		log.Printf("[k8s-etcd] 改 kubeadm-config 失败: %v out=%q", err, headForLog(out, 200))
		return finishEtcdQuota(t, st, next, bak, "改 kubeadm-config 失败, manifest 已回滚: "+err.Error(), true, st.ConfBytes)
	}

	// 4) 等 etcd 重建并核对新上限。kubelet 从"看到文件变化"到"新 pod Running"实测 70 秒~5 分钟+,
	//    所以只同步等 90 秒, 到点如实返回待生效(而不是自作主张回滚 —— 配置是对的, 回滚等于白折腾, 实测踩过两次)。
	return waitForEtcdQuota(t, st, next, etcdWaitSeconds*time.Second)
}

// waitForEtcdQuota 轮询运行中的 quota 直到等于 want; 超时不报错, 返回 pending/stale 让界面接手。
func waitForEtcdQuota(t masterTarget, st *etcdApplyState, want int64, wait time.Duration) map[string]any {
	deadline := time.Now().Add(wait)
	start := time.Now()
	for {
		if time.Now().After(deadline) {
			cur, _ := runOnMaster(t, []string{"sh", "-c", etcdQuotaProbeScript})
			running := int64(parsePromValue(cur, "etcd_server_quota_backend_bytes"))
			log.Printf("[k8s-etcd] quota 已改但尚未生效: 目标=%d 运行中=%d 等了=%s", want, running, time.Since(start).Round(time.Second))
			return map[string]any{"ok": false, "pending": true, "stale": true, "quotaBytes": want, "runningBytes": running,
				"previousBytes": st.ConfBytes, "waited": time.Since(start).Round(time.Second).String(),
				"message": "配置已改(manifest + kubeadm-config), 但 etcd 还没用上新值 —— kubelet 重建实测要 70 秒~5 分钟; 刷新卡片看进度, 等不来就点「立即生效」"}
		}
		time.Sleep(5 * time.Second)
		out, err := runOnMaster(t, []string{"sh", "-c", etcdQuotaProbeScript})
		if err != nil {
			log.Printf("[k8s-etcd] quota 等待中(%.0fs): 探测失败 %v", time.Since(start).Seconds(), err)
			continue
		}
		got := int64(parsePromValue(out, "etcd_server_quota_backend_bytes"))
		leader := parsePromValue(out, "etcd_server_has_leader")
		log.Printf("[k8s-etcd] quota 等待中(%.0fs): 运行中=%d leader=%v 目标=%d", time.Since(start).Seconds(), got, leader >= 1, want)
		if got == want && leader >= 1 {
			// 生效后再取一次完整状态(db 大小/碎片), 供界面刷新
			dbBytes := st.DBBytes
			if full, ferr := runOnMaster(t, []string{"sh", "-c", etcdProbeScript}); ferr == nil {
				if m := buildEtcdMember("", parseSections(full)); m.DBBytes > 0 {
					dbBytes = int64(m.DBBytes)
				}
			}
			log.Printf("[k8s-etcd] quota 已生效: %d (db=%d, 用时 %.0fs)", want, dbBytes, time.Since(start).Seconds())
			return map[string]any{"ok": true, "quotaBytes": want, "dbBytes": dbBytes, "duration": time.Since(start).Round(time.Second).String()}
		}
	}
}

// applyCMScript 用 base64 把改好的 YAML 送上去 apply(YAML 里有引号与缩进, 走命令行参数会被吃掉)
func applyCMScript(yamlText string) string {
	b64 := base64.StdEncoding.EncodeToString([]byte(yamlText))
	return "echo " + b64 + " | base64 -d > /tmp/opscore-kubeadm-cm.yaml && " +
		"kubectl " + etcdAdminKubeconfigArg() + " apply -f /tmp/opscore-kubeadm-cm.yaml && rm -f /tmp/opscore-kubeadm-cm.yaml"
}

// finishEtcdQuota 失败收尾: 回滚 manifest + ConfigMap, 并**读回来核对**两处都回到原值。
// 为什么要核对: 实测出现过"manifest 回滚了、ConfigMap 那次 apply 静默失败"——两处不一致会留隐患。
func finishEtcdQuota(t masterTarget, st *etcdApplyState, next int64, bak, reason string, rollbackCM bool, restoreQuota int64) map[string]any {
	errs := []string{}
	if err := restoreManifest(t, bak); err != nil {
		errs = append(errs, "manifest 回滚失败: "+err.Error())
	}
	if rollbackCM {
		if _, err := runOnMaster(t, []string{"sh", "-c", applyCMScript(st.CMYAML)}); err != nil {
			errs = append(errs, "kubeadm-config 回滚失败: "+err.Error())
		}
	}
	if verify, verr := loadEtcdApplyState(t); verr != nil {
		errs = append(errs, "回滚后核对失败: "+verr.Error())
	} else {
		if verify.ConfBytes != restoreQuota {
			if merr := restoreManifest(t, bak); merr != nil {
				errs = append(errs, "manifest 二次回滚失败: "+merr.Error())
			} else {
				errs = append(errs, "manifest 首次回滚未生效(已重试)")
			}
		}
		if rollbackCM {
			if got := cmQuota(verify.CMYAML); got != restoreQuota {
				if _, err := runOnMaster(t, []string{"sh", "-c", applyCMScript(st.CMYAML)}); err != nil {
					errs = append(errs, "kubeadm-config 二次回滚失败: "+err.Error())
				} else {
					errs = append(errs, fmt.Sprintf("kubeadm-config 首次回滚未生效(读到 %d, 已重试)", got))
				}
			}
		}
	}
	msg := reason + "(已回滚到 " + humanBytes(restoreQuota) + ")"
	if len(errs) > 0 {
		msg += "; 回滚过程有错: " + strings.Join(errs, "; ")
	}
	log.Printf("[k8s-etcd] quota 调整失败: %s", msg)
	return map[string]any{"ok": false, "error": msg, "rolledBack": len(errs) == 0}
}

func restoreManifest(t masterTarget, bak string) error {
	// cat 回写而不是 cp 覆盖: 保持同一 inode, 不往 manifests 目录里留野文件
	out, err := runOnMaster(t, []string{"sh", "-c", "cat " + bak + " > " + etcdManifestPath + " && echo ok"})
	if err != nil {
		return fmt.Errorf("%v out=%q", err, headForLog(out, 120))
	}
	return nil
}
