package handlers

// ── K8s 指标采集与历史 ──
// 实时: 直读 metrics.k8s.io(节点/Pod 用量)。
// 历史: 后台采样器每 15s 聚合一次集群总用量, 写入 <dataDir>/k8s_metrics.db(SQLite, 独立于 CentralStore,
// 独立模式 cmd/kubemod 亦可复用), 默认保留 7 天 —— 支持跨重启的趋势图。

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"opscore/internal/kubernetes"

	_ "modernc.org/sqlite"
)

var (
	metricsMu     sync.Mutex
	metricsDBPath string
)

func openMetricsDB() (*sql.DB, error) {
	if metricsDBPath == "" {
		return nil, fmt.Errorf("metrics db 未初始化")
	}
	db, err := sql.Open("sqlite", metricsDBPath+"?_journal_mode=WAL&_cache_size=1000")
	if err != nil {
		return nil, err
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS samples (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		ts       INTEGER NOT NULL,
		cluster  TEXT NOT NULL,
		cpu_milli INTEGER NOT NULL,
		mem_bytes INTEGER NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_samples ON samples(cluster, ts);`)
	return db, err
}

// StartK8sMetricsSampler 启动后台采样循环(每 15s; 每小时清理 7 天前数据)。
func StartK8sMetricsSampler(dataDir string) {
	_ = os.MkdirAll(dataDir, 0755)
	metricsMu.Lock()
	metricsDBPath = filepath.Join(dataDir, "k8s_metrics.db")
	metricsMu.Unlock()

	db, err := openMetricsDB()
	if err != nil {
		log.Printf("[K8S-METRICS] 初始化失败: %v", err)
		return
	}
	db.Close()

	go func() {
		tick := time.NewTicker(15 * time.Second)
		clean := time.NewTicker(time.Hour)
		defer tick.Stop()
		defer clean.Stop()
		for {
			select {
			case <-tick.C:
				sampleOnce()
			case <-clean.C:
				purgeOldSamples()
			}
		}
	}()
	log.Println("[K8S-METRICS] 采样器已启动(15s/次, 保留7天)")
}

var lastSource = map[string]string{}

func sampleOnce() {
	for _, id := range k8sMgr.ListIDs() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		b := collectMetrics(ctx, id)
		cancel()
		if prev, ok := lastSource[id]; !ok || prev != b.Source {
			log.Printf("[K8S-METRICS] 集群 %s 数据源: %s%s", id, b.Source, func() string {
				if b.Degraded {
					return " (降级: " + b.Reason + ")"
				}
				return ""
			}())
			lastSource[id] = b.Source
		}
		if len(b.Nodes) == 0 {
			continue
		}
		nodes := b.Nodes
		var cpuMilli, memBytes int64
		for _, n := range nodes {
			cpuMilli += n.CPUMilli
			memBytes += int64(n.MemMiB) * 1024 * 1024
		}
		metricsMu.Lock()
		db, err := openMetricsDB()
		metricsMu.Unlock()
		if err != nil {
			return
		}
		_, _ = db.Exec("INSERT INTO samples (ts, cluster, cpu_milli, mem_bytes) VALUES (?,?,?,?)",
			time.Now().Unix(), id, cpuMilli, memBytes)
		db.Close()
	}
}

func purgeOldSamples() {
	metricsMu.Lock()
	db, err := openMetricsDB()
	metricsMu.Unlock()
	if err != nil {
		return
	}
	_, _ = db.Exec("DELETE FROM samples WHERE ts < ?", time.Now().AddDate(0, 0, -7).Unix())
	db.Close()
}

// ===== 采集集合点: 谁可用用谁 =====

// MetricsBundle 一次采集的产物。Source:
//   "metrics-server"  主路径(metrics.k8s.io, 需要 metrics-server add-on; kubectl top/HPA 同源)
//   "kubelet-summary" 兜底路径(apiserver node proxy → kubelet /stats/summary, 无需任何 add-on; 含磁盘)
// Degraded=true 表示处于兜底态 —— kubectl top 与基于资源指标的 HPA 此时不可用, 前端应提示。
type MetricsBundle struct {
	Nodes    []kubernetes.NodeMetric
	Pods     []kubernetes.PodMetric
	Source   string
	Degraded bool
	Reason   string
}

func collectMetrics(ctx context.Context, clusterID string) MetricsBundle {
	nodes, err := k8sMgr.GetNodeMetrics(ctx, clusterID)
	if err == nil {
		pods, perr := k8sMgr.GetPodMetrics(ctx, clusterID, "")
		if perr != nil {
			pods = nil
		}
		return MetricsBundle{Nodes: nodes, Pods: pods, Source: "metrics-server"}
	}
	primaryErr := err.Error()
	sn, sp, _, serr := k8sMgr.SummaryMetrics(ctx, clusterID)
	if serr != nil {
		return MetricsBundle{Degraded: true, Reason: "metrics-server: " + primaryErr + "; kubelet summary: " + serr.Error()}
	}
	return MetricsBundle{Nodes: sn, Pods: sp, Source: "kubelet-summary", Degraded: true,
		Reason: "metrics-server 不可用(" + primaryErr + "), 已降级为 kubelet /stats/summary 直读"}
}

// ===== HTTP 端点 =====

// K8sNodeMetricsHandler GET ?cluster=
func K8sNodeMetricsHandler(w http.ResponseWriter, r *http.Request) {
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
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	b := collectMetrics(ctx, cluster)
	if len(b.Nodes) == 0 {
		WriteJSON(w, map[string]any{"ok": false, "error": b.Reason, "source": b.Source, "degraded": b.Degraded})
		return
	}
	WriteJSON(w, map[string]any{"ok": true, "nodes": b.Nodes,
		"source": b.Source, "degraded": b.Degraded, "reason": b.Reason})
}

// K8sPodMetricsHandler GET ?cluster=&ns=&top=500
func K8sPodMetricsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(k8sPluginID, w) {
		return
	}
	q := r.URL.Query()
	cluster, ns := q.Get("cluster"), q.Get("ns")
	if !reK8sClusterID.MatchString(cluster) || (ns != "" && ns != "all" && !reK8sNamespace.MatchString(ns)) {
		WriteJSON(w, map[string]any{"ok": false, "error": "参数非法"})
		return
	}
	if ns == "all" {
		ns = ""
	}
	top := 500
	fmt.Sscanf(q.Get("top"), "%d", &top)
	if top < 1 || top > 500 {
		top = 500
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	b := collectMetrics(ctx, cluster)
	if len(b.Pods) == 0 {
		WriteJSON(w, map[string]any{"ok": false, "error": b.Reason, "source": b.Source, "degraded": b.Degraded})
		return
	}
	pods := b.Pods
	if ns != "" {
		kept := pods[:0]
		for _, pd := range pods {
			if pd.Namespace == ns {
				kept = append(kept, pd)
			}
		}
		pods = kept
	}
	if len(pods) > top {
		pods = pods[:top]
	}
	WriteJSON(w, map[string]any{"ok": true, "pods": pods,
		"source": b.Source, "degraded": b.Degraded, "reason": b.Reason})
}

// K8sMetricsHistoryHandler GET ?cluster=&window=5m|15m|1h|6h → 降采样趋势点
func K8sMetricsHistoryHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(k8sPluginID, w) {
		return
	}
	q := r.URL.Query()
	cluster := q.Get("cluster")
	window := q.Get("window")
	dur := map[string]time.Duration{
		"5m": 5 * time.Minute, "15m": 15 * time.Minute,
		"1h": time.Hour, "6h": 6 * time.Hour,
	}[window]
	if dur == 0 {
		dur = time.Hour
	}
	since := time.Now().Add(-dur).Unix()

	metricsMu.Lock()
	db, err := openMetricsDB()
	metricsMu.Unlock()
	if err != nil {
		WriteJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	defer db.Close()
	rows, err := db.Query(
		`SELECT ts, cpu_milli, mem_bytes FROM samples WHERE cluster=? AND ts>=? ORDER BY ts`, cluster, since)
	if err != nil {
		WriteJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	defer rows.Close()
	type pt struct {
		Ts       int64   `json:"ts"`
		CPUMilli float64 `json:"cpu"`
		MemMiB   float64 `json:"memMiB"`
	}
	points := []pt{}
	for rows.Next() {
		var p pt
		var cm, mb int64
		if rows.Scan(&p.Ts, &cm, &mb) == nil {
			p.CPUMilli = float64(cm)
			p.MemMiB = float64(mb) / (1024 * 1024)
			points = append(points, p)
		}
	}
	// 点数过多时等距抽稀到 ~240 点
	const maxPts = 240
	if len(points) > maxPts {
		step := float64(len(points)) / maxPts
		out := make([]pt, 0, maxPts)
		for i := 0; i < maxPts; i++ {
			out = append(out, points[int(float64(i)*step)])
		}
		points = out
	}
	WriteJSON(w, map[string]any{"ok": true, "points": points, "window": window})
}

var _ = json.Marshal // keep json import if unused paths change
