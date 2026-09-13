package logmonitor

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"opscore/internal/registry"
)

const PluginID = "logmonitor"

// Handlers HTTP handler 集合
type Handlers struct {
	store    *Store
	service  *Service
	archiver *Archiver
	dataDir  string // 日志数据根目录
}

// Module 返回 registry.Module（供 main.go 集成）
func Module(store *Store, service *Service, archiver *Archiver, dataDir string) *registry.Module {
	h := &Handlers{store: store, service: service, archiver: archiver, dataDir: dataDir}
	return &registry.Module{
		Manifest: registry.Manifest{
			ID:            PluginID,
			Name:          "日志监控",
			Icon:          "activity",
			RoutePath:     "/logmonitor",
			Group:         "plugin",
			HostSensitive: true,
			Description:   "日志采集 / 多条件检索 / 实时统计 / 分级存储",
		},
		Routes: []registry.Route{
			{Path: "/api/logmonitor/query", Handler: h.handleQuery},
			{Path: "/api/logmonitor/stats", Handler: h.handleStats},
			{Path: "/api/logmonitor/stats/terms", Handler: h.handleTerms},
			{Path: "/api/logmonitor/ingest", Handler: h.handleIngest},
			{Path: "/api/logmonitor/sources", Handler: h.handleSources},
			{Path: "/api/logmonitor/sources/save", Handler: h.handleSourceSave},
			{Path: "/api/logmonitor/sources/delete", Handler: h.handleSourceDelete},
			{Path: "/api/logmonitor/sources/enabled", Handler: h.handleSourceSetEnabled},
			{Path: "/api/logmonitor/scan", Handler: h.handleScan},
			{Path: "/api/logmonitor/raw", Handler: h.handleRaw},
			{Path: "/api/logmonitor/delete", Handler: h.handleDelete},
			{Path: "/api/logmonitor/indexes", Handler: h.handleIndexes},
			{Path: "/api/logmonitor/indexes/save", Handler: h.handleIndexSave},
			{Path: "/api/logmonitor/indexes/get", Handler: h.handleIndexGet},
			{Path: "/api/logmonitor/indexes/delete", Handler: h.handleIndexDelete},
			{Path: "/api/logmonitor/indexes/stats", Handler: h.handleIndexStats},
			{Path: "/api/logmonitor/ilm/run", Handler: h.handleIlmRun},
			{Path: "/api/logmonitor/parsers", Handler: h.handleParsers},
			{Path: "/api/logmonitor/parsers/save", Handler: h.handleParsersSave},
			{Path: "/api/logmonitor/parsers/test", Handler: h.handleParsersTest},
			{Path: "/api/logmonitor/shards", Handler: h.handleShards},
			{Path: "/api/logmonitor/shards/save", Handler: h.handleShardsSave},
			{Path: "/api/logmonitor/shards/delete", Handler: h.handleShardsDelete},
			{Path: "/api/logmonitor/discover/containers", Handler: h.handleDiscoverContainers},
			{Path: "/api/logmonitor/discover/k8s", Handler: h.handleDiscoverK8s},
			{Path: "/api/logmonitor/discover/clusters", Handler: h.handleDiscoverClusters},
			{Path: "/api/logmonitor/alerts/rules", Handler: h.handleListAlertRules},
			{Path: "/api/logmonitor/alerts/rules/save", Handler: h.handleSaveAlertRule},
			{Path: "/api/logmonitor/alerts/rules/delete", Handler: h.handleDeleteAlertRule},
			{Path: "/api/logmonitor/alerts/channels", Handler: h.handleListAlertChannels},
			{Path: "/api/logmonitor/alerts/channels/save", Handler: h.handleSaveAlertChannel},
			{Path: "/api/logmonitor/alerts/channels/delete", Handler: h.handleDeleteAlertChannel},
			{Path: "/api/logmonitor/alerts/events", Handler: h.handleListAlertEvents},
		},
	}
}

// DiscoverContainersHandler 列表目标主机(默认本机, ?host= 跟随全局主机上下文)可接入的 Docker 容器。GET
func (h *Handlers) handleDiscoverContainers(w http.ResponseWriter, r *http.Request) {
	list, err := DiscoverDockerContainersOn(r.URL.Query().Get("host"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "发现容器失败: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"containers": list, "ok": true})
}

// DiscoverK8sHandler 列表某集群可接入的 pod。GET ?cluster=&host= (host 缺省本机)
func (h *Handlers) handleDiscoverK8s(w http.ResponseWriter, r *http.Request) {
	cluster := r.URL.Query().Get("cluster")
	if cluster == "" {
		writeErr(w, http.StatusBadRequest, "缺 cluster 参数")
		return
	}
	list, err := DiscoverK8sLogTargetsOn(h.dataDir, cluster, r.URL.Query().Get("host"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "发现 K8S pod 失败: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"pods": list, "ok": true})
}

// DiscoverClustersHandler 列表已注册的 K8S 集群。GET
func (h *Handlers) handleDiscoverClusters(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{"clusters": CollectClusters(h.dataDir), "ok": true})
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// GET /api/logmonitor/query?service=&level=&source=&keyword=&startTs=&endTs=&page=&pageSize=
func (h *Handlers) handleQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	q := r.URL.Query()

	query := &LogQuery{
		Service:  q.Get("service"),
		Level:    q.Get("level"),
		Source:   q.Get("source"),
		Keyword:  q.Get("keyword"),
		IndexID:  q.Get("indexId"),
		Page:     atoiDefault(q.Get("page"), 1),
		PageSize: atoiDefault(q.Get("pageSize"), 100),
	}
	if v := q.Get("startTs"); v != "" {
		query.StartTs = atoi64Default(v, 0)
	}
	if v := q.Get("endTs"); v != "" {
		query.EndTs = atoi64Default(v, 0)
	}

	result, err := h.store.Query(query)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "查询失败: "+err.Error())
		return
	}

	// 按需读取原始内容（仅当请求了 raw=1）
	if q.Get("raw") == "1" {
		for _, e := range result.Items {
			e.Raw = h.readLogContent(e)
		}
	}

	writeJSON(w, http.StatusOK, result)
}

// GET /api/logmonitor/stats?service=&startTs=&endTs=&bucketMs=
func (h *Handlers) handleStats(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	sq := &LogStatsQuery{Service: q.Get("service")}
	if v := q.Get("startTs"); v != "" {
		sq.StartTs = atoi64Default(v, 0)
	}
	if v := q.Get("endTs"); v != "" {
		sq.EndTs = atoi64Default(v, 0)
	}
	bucketMs := atoi64Default(q.Get("bucketMs"), 60000)

	stats, err := h.store.Stats(sq)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "统计失败: "+err.Error())
		return
	}
	hist, err := h.store.Histogram(sq, bucketMs)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "聚合失败: "+err.Error())
		return
	}
	if hist == nil {
		hist = []HistogramBucket{}
	}
	writeJSON(w, http.StatusOK, LogStatsResult{Stats: stats, Histogram: hist})
}

// GET /api/logmonitor/stats/terms?field=service&service=&level=&source=&keyword=&startTs=&endTs=&indexId=&size=
func (h *Handlers) handleTerms(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	tq := &LogTermsQuery{
		Field:   q.Get("field"),
		Service: q.Get("service"),
		Level:   q.Get("level"),
		Source:  q.Get("source"),
		Keyword: q.Get("keyword"),
		IndexID: q.Get("indexId"),
		Size:    atoiDefault(q.Get("size"), 10),
	}
	if v := q.Get("startTs"); v != "" {
		tq.StartTs = atoi64Default(v, 0)
	}
	if v := q.Get("endTs"); v != "" {
		tq.EndTs = atoi64Default(v, 0)
	}
	result, err := h.store.Terms(tq)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Terms 聚合失败: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// POST /api/logmonitor/ingest  { "line": "...", "service":"", "source":"" } 或 { "lines":[...] }
func (h *Handlers) handleIngest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "读取请求失败")
		return
	}
	var payload struct {
		Line    string   `json:"line"`
		Lines   []string `json:"lines"`
		Service string   `json:"service"`
		Source  string   `json:"source"`
		IndexID string   `json:"indexId"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		writeErr(w, http.StatusBadRequest, "JSON 解析失败: "+err.Error())
		return
	}

	if payload.Line != "" {
		e, err := h.service.Ingest(payload.Line, payload.Service, payload.Source, payload.IndexID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"inserted": 1, "id": e.ID})
		return
	}
	if len(payload.Lines) > 0 {
		n, err := h.service.IngestBatch(payload.Lines, payload.Service, payload.Source, payload.IndexID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"inserted": n})
		return
	}
	writeErr(w, http.StatusBadRequest, "需提供 line 或 lines")
}

// GET/POST /api/logmonitor/sources
func (h *Handlers) handleSources(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		sources, err := h.store.ListSources()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, sources)
		return
	}
	writeErr(w, http.StatusMethodNotAllowed, "GET only")
}

// POST /api/logmonitor/sources/save
func (h *Handlers) handleSourceSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var src LogSource
	if err := json.NewDecoder(r.Body).Decode(&src); err != nil {
		writeErr(w, http.StatusBadRequest, "JSON 解析失败: "+err.Error())
		return
	}
	if src.ID == "" {
		src.ID = "src_" + strconv.FormatInt(nowMs(), 10)
	}
	if src.Type == "" {
		src.Type = "file"
	}
	if err := h.store.SaveSource(&src); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, src)
}

// POST /api/logmonitor/sources/delete  { "id": "..." }
func (h *Handlers) handleSourceDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "JSON 解析失败: "+err.Error())
		return
	}
	if err := h.store.DeleteSource(body.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ok": "deleted"})
}

// POST /api/logmonitor/sources/enabled  { "id": "...", "enabled": true }
func (h *Handlers) handleSourceSetEnabled(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var body struct {
		ID      string `json:"id"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "JSON 解析失败: "+err.Error())
		return
	}
	if body.ID == "" {
		writeErr(w, http.StatusBadRequest, "id 不能为空")
		return
	}
	if err := h.store.SetSourceEnabled(body.ID, body.Enabled); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": body.Enabled})
}

// POST /api/logmonitor/scan  { "path":"...", "service":"", "source":"", "tailOnly":false, "namespace":"", "cluster":"" }
// source=file     → path 为本地文件路径
// source=container→ path 为本机 docker 容器名
// source=k8s      → path 为 K8S pod 名，namespace 指定命名空间，cluster 指定集群 ID
func (h *Handlers) handleScan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var body struct {
		Path        string `json:"path"`
		Service     string `json:"service"`
		Source      string `json:"source"`
		TailOnly    bool   `json:"tailOnly"`
		DefaultSvc  string `json:"defaultService"`
		IndexID     string `json:"indexId"`
		Namespace   string `json:"namespace"`
		Cluster     string `json:"cluster"`
		Host        string `json:"host"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "JSON 解析失败: "+err.Error())
		return
	}
	if body.Path == "" {
		writeErr(w, http.StatusBadRequest, "path 不能为空")
		return
	}
	if body.Source == "" {
		body.Source = "file"
	}
	body.Source = strings.ToLower(body.Source)
	svc := body.Service
	if svc == "" {
		svc = body.DefaultSvc
	}
	if svc == "" {
		svc = body.Source
	}
	// 容器/内各 pod：svc 为空时用它自己名字（容器名/pod名）做服务名，避免统一落成 "container"/"k8s"
	if body.Source == "container" || body.Source == "k8s" || body.Source == "k8spod" {
		if svc == body.Source { // 说明 body.Service 与 DefaultSvc 都为空，fallback 到了 source
			svc = body.Path
		}
	}

	var lines []string
	var err error
	switch body.Source {
	case "container":
		lines, err = CollectDockerLogsOn(body.Host, body.Path, 500)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "读取容器日志失败: "+err.Error())
			return
		}
	case "k8s", "k8spod":
		lines, err = CollectK8sPodLogsOn(body.Host, h.dataDir, body.Cluster, body.Namespace, body.Path, 500)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "读取 K8S pod 日志失败: "+err.Error())
			return
		}
	default:
		if _, err := os.Stat(body.Path); err != nil {
			writeErr(w, http.StatusBadRequest, "文件不存在: "+err.Error())
			return
		}
		n, serr := h.service.ScanFile(body.Path, svc, body.Source, body.TailOnly, body.IndexID)
		if serr != nil {
			writeErr(w, http.StatusInternalServerError, serr.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"scanned": n, "path": body.Path})
		return
	}
	// 容器 / K8S 采集: 批量 ingest 到目标索引(双写)
	if len(lines) == 0 {
		writeJSON(w, http.StatusOK, map[string]interface{}{"scanned": 0, "path": body.Path, "source": body.Source})
		return
	}
	n, ierr := h.service.IngestBatch(lines, svc, body.Source, body.IndexID)
	if ierr != nil {
		writeErr(w, http.StatusInternalServerError, ierr.Error())
		return
	}
	// 接入成功即注册为持续跟踪源(follow=1)，后台 poller 持续增量采集
	h.registerFollowSource(body.Path, body.Source, body.Namespace, body.Cluster, body.IndexID, svc)
	writeJSON(w, http.StatusOK, map[string]interface{}{"scanned": n, "path": body.Path, "source": body.Source})
}

// registerFollowSource 把一次性 scan 成功的容器/k8s 源持久化成 follow 源，供 poller 持续采集。
// 幂等：以 type:path 为唯一 key，重复接入只更新元数据，不重置游标。
func (h *Handlers) registerFollowSource(path, source, namespace, cluster, indexID, svc string) {
	if source != "container" && source != "k8s" && source != "k8spod" {
		return
	}
	sources, err := h.store.ListSources()
	if err != nil {
		return
	}
	id := source + ":" + path
	for _, s := range sources {
		if s.ID == id {
			// 重新接入：被停用的源 → 重新启用（游标保留续采，不重扫尾部）
			if !s.Enabled || s.IndexID != indexID {
				s.Enabled = true
				if s.IndexID != indexID {
					s.IndexID = indexID
					s.Service = svc
				}
				_ = h.store.SaveSource(s)
			}
			return
		}
	}
	src := &LogSource{
		ID:        id,
		Name:      path,
		Type:      source,
		Path:      path,
		Service:   svc,
		Enabled:   true,
		Follow:    true,
		IndexID:   indexID,
		Namespace: namespace,
		Cluster:   cluster,
		// 游标置为接入时刻：scan 已抓尾部入库，poller 从接入后增量抓取，避免首采 tail 重复
		LastTs: nowMs(),
	}
	_ = h.store.SaveSource(src)
}

// GET /api/logmonitor/raw?id=123  → 完整原始日志内容
func (h *Handlers) handleRaw(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "id 非法")
		return
	}
	e, err := h.store.ReadRaw(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "记录不存在")
		return
	}
	e.Raw = h.readLogContent(e)
	writeJSON(w, http.StatusOK, e)
}

// POST /api/logmonitor/delete  { "ids": [1,2,3] }
func (h *Handlers) handleDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var body BulkDeleteRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "JSON 解析失败: "+err.Error())
		return
	}
	if len(body.IDs) == 0 {
		writeErr(w, http.StatusBadRequest, "ids 不能为空")
		return
	}
	// 防注入：限制批量大小
	if len(body.IDs) > 1000 {
		writeErr(w, http.StatusBadRequest, "一次最多删除 1000 条")
		return
	}
	if err := h.store.BulkDelete(body.IDs); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"deleted": len(body.IDs)})
}

// GET /api/logmonitor/indexes  列表
func (h *Handlers) handleIndexes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	indexes, err := h.store.ListIndexes()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, indexes)
}

// POST /api/logmonitor/indexes/save  新建/更新索引
func (h *Handlers) handleIndexSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var idx LogIndex
	if err := json.NewDecoder(r.Body).Decode(&idx); err != nil {
		writeErr(w, http.StatusBadRequest, "JSON 解析失败: "+err.Error())
		return
	}
	if err := h.store.SaveIndex(&idx); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, idx)
}

// GET /api/logmonitor/indexes/get?id=...
func (h *Handlers) handleIndexGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		writeErr(w, http.StatusBadRequest, "id 不能为空")
		return
	}
	idx, err := h.store.GetIndex(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "索引不存在: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, idx)
}

// POST /api/logmonitor/indexes/delete  { "id": "..." }
func (h *Handlers) handleIndexDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "JSON 解析失败: "+err.Error())
		return
	}
	if err := h.store.DeleteIndex(body.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ok": "deleted"})
}

// GET /api/logmonitor/indexes/stats?id=...
func (h *Handlers) handleIndexStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		writeErr(w, http.StatusBadRequest, "id 不能为空")
		return
	}
	st, err := h.store.IndexStatsFor(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// POST /api/logmonitor/ilm/run  手动触发 ILM 淘汰(删过期元数据 + 联动删过期归档文件)
func (h *Handlers) handleIlmRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	cleanups, n, err := h.store.ApplyIlm()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// 联动删除各索引过期归档文件
	var archFiles int
	if h.archiver != nil {
		for _, c := range cleanups {
			del, _ := h.archiver.deleteBeforeDate(c.IndexID, c.CutoffDate)
			archFiles += del
		}
	}
	na, _ := h.store.ApplyIlmAll()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"deleted":          n,
		"deletedUnassigned": na,
		"archiveFiles":     archFiles,
	})
}

// GET /api/logmonitor/parsers → 当前解析规则(前端编辑/展示用)
func (h *Handlers) handleParsers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	rules, builtin, errStr := h.service.ParserInfo()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"rules":   rules,
		"builtin": builtin,
		"error":   errStr,
	})
}

// POST /api/logmonitor/parsers/save { "rules": [...] } → 写 parsers.json + 热载
func (h *Handlers) handleParsersSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var body struct {
		Rules []ParserRule `json:"rules"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "JSON 解析失败: "+err.Error())
		return
	}
	if len(body.Rules) == 0 {
		writeErr(w, http.StatusBadRequest, "规则列表不能为空")
		return
	}
	if err := h.service.ParserSave(body.Rules); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
}

// POST /api/logmonitor/parsers/test { "rule": {...}, "lines": [...] } → 逐行解析结果, 不改全局规则
func (h *Handlers) handleParsersTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var body struct {
		Rule  ParserRule `json:"rule"`
		Lines []string   `json:"lines"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "JSON 解析失败: "+err.Error())
		return
	}
	if len(body.Lines) == 0 {
		writeErr(w, http.StatusBadRequest, "请至少输入一行样例日志")
		return
	}
	results := h.service.ParserTest(body.Rule, body.Lines)
	writeJSON(w, http.StatusOK, map[string]interface{}{"results": results})
}

// GET /api/logmonitor/shards → 分片配置 + 分片状态列表
func (h *Handlers) handleShards(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"config": h.store.shardCfg,
		"shards": h.store.ListShards(h.store.shardCfg),
	})
}

// POST /api/logmonitor/shards/save { "config": {...} } → 写 shards.json + 生效
func (h *Handlers) handleShardsSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var body struct {
		Config ShardConfig `json:"config"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "JSON 解析失败: "+err.Error())
		return
	}
	path := filepath.Join(filepath.Dir(h.dataDir), "shards.json")
	if err := saveShardConfig(path, body.Config); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.store.shardCfg = loadShardConfig(path)
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "config": h.store.shardCfg})
}

// POST /api/logmonitor/shards/delete { "shard": "202607" } → 删除过期分片
func (h *Handlers) handleShardsDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var body struct {
		Shard string `json:"shard"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "JSON 解析失败: "+err.Error())
		return
	}
	if body.Shard == "" {
		writeErr(w, http.StatusBadRequest, "shard 不能为空")
		return
	}
	n, err := h.store.DropShard(body.Shard, h.store.shardCfg)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"deleted": n})
}

// readLogContent 按 metadata 读取日志内容。
// 优先：若该日志归属某索引且已归档, 从 zstd 归档读取原文；否则回读源文件。
func (h *Handlers) readLogContent(e *LogEntry) string {
	if e.IndexID != "" && h.archiver != nil {
		if raw := h.archiver.readByID(e.IndexID, e.Ts, e.ID); raw != "" {
			return raw
		}
	}
	if e.FilePath == "" || e.FilePath == "http-ingest" {
		return e.Summary
	}
	f, err := os.Open(e.FilePath)
	if err != nil {
		return e.Summary
	}
	defer f.Close()
	if _, err := f.Seek(e.Offset, io.SeekStart); err != nil {
		return e.Summary
	}
	buf := make([]byte, e.Size)
	if e.Size <= 0 || e.Size > 32*1024 {
		buf = make([]byte, 4096)
	}
	n, _ := io.ReadFull(f, buf)
	return strings.TrimRight(string(buf[:n]), "\r\n")
}

func atoiDefault(s string, def int) int {
	v, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return v
}

func atoi64Default(s string, def int64) int64 {
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return def
	}
	return v
}

// RequireStore 在 main.go 中调用以创建 store
func RequireStore(dbPath string) (*Store, error) {
	return NewStore(dbPath)
}

// ---------- Alert Rules ----------

func (h *Handlers) handleListAlertRules(w http.ResponseWriter, r *http.Request) {
	rules, err := h.store.ListAlertRules()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"rules": rules, "ok": true})
}

func (h *Handlers) handleSaveAlertRule(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var rule AlertRule
	if err := json.Unmarshal(body, &rule); err != nil {
		writeErr(w, http.StatusBadRequest, "JSON 解析失败")
		return
	}
	if rule.ID == "" { rule.ID = "ar_" + strconv.FormatInt(time.Now().UnixMilli(), 10) }
	rule.UpdatedAt = time.Now().UnixMilli()
	if err := h.store.SaveAlertRule(&rule); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rule)
}

func (h *Handlers) handleDeleteAlertRule(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		writeErr(w, http.StatusBadRequest, "缺 id")
		return
	}
	if err := h.store.DeleteAlertRule(id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

// ---------- Alert Channels ----------

func (h *Handlers) handleListAlertChannels(w http.ResponseWriter, r *http.Request) {
	chans, err := h.store.ListAlertChannels()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"channels": chans, "ok": true})
}

func (h *Handlers) handleSaveAlertChannel(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var ch AlertChannel
	if err := json.Unmarshal(body, &ch); err != nil {
		writeErr(w, http.StatusBadRequest, "JSON 解析失败")
		return
	}
	if ch.ID == "" { ch.ID = "ac_" + strconv.FormatInt(time.Now().UnixMilli(), 10) }
	if err := h.store.SaveAlertChannel(&ch); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ch)
}

func (h *Handlers) handleDeleteAlertChannel(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		writeErr(w, http.StatusBadRequest, "缺 id")
		return
	}
	if err := h.store.DeleteAlertChannel(id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

// ---------- Alert Events ----------

func (h *Handlers) handleListAlertEvents(w http.ResponseWriter, r *http.Request) {
	limit := atoiDefault(r.URL.Query().Get("limit"), 100)
	events, err := h.store.ListAlertEvents(limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"events": events, "ok": true})
}

var _ = fmt.Sprintf
