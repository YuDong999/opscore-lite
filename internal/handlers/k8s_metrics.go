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

func sampleOnce() {
	for _, id := range k8sMgr.ListIDs() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		nodes, err := k8sMgr.GetNodeMetrics(ctx, id)
		cancel()
		if err != nil || len(nodes) == 0 {
			continue
		}
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	nodes, err := k8sMgr.GetNodeMetrics(ctx, cluster)
	if err != nil {
		WriteJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	WriteJSON(w, map[string]any{"ok": true, "nodes": nodes})
}

// K8sPodMetricsHandler GET ?cluster=&ns=&top=10
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
	top := 10
	fmt.Sscanf(q.Get("top"), "%d", &top)
	if top < 1 || top > 100 {
		top = 10
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pods, err := k8sMgr.GetPodMetrics(ctx, cluster, ns)
	if err != nil {
		WriteJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if len(pods) > top {
		pods = pods[:top]
	}
	WriteJSON(w, map[string]any{"ok": true, "pods": pods})
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
