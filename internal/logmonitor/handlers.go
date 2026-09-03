package logmonitor

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"opscore/internal/registry"
)

const PluginID = "logmonitor"

// Handlers HTTP handler 集合
type Handlers struct {
	store   *Store
	service *Service
	dataDir string // 日志数据根目录
}

// Module 返回 registry.Module（供 main.go 集成）
func Module(store *Store, service *Service, dataDir string) *registry.Module {
	h := &Handlers{store: store, service: service, dataDir: dataDir}
	return &registry.Module{
		Manifest: registry.Manifest{
			ID:          PluginID,
			Name:        "日志监控",
			Icon:        "activity",
			RoutePath:   "/logmonitor",
			Group:       "plugin",
			Description: "日志采集 / 多条件检索 / 实时统计 / 分级存储",
		},
		Routes: []registry.Route{
			{Path: "/api/logmonitor/query", Handler: h.handleQuery},
			{Path: "/api/logmonitor/stats", Handler: h.handleStats},
			{Path: "/api/logmonitor/ingest", Handler: h.handleIngest},
			{Path: "/api/logmonitor/sources", Handler: h.handleSources},
			{Path: "/api/logmonitor/sources/save", Handler: h.handleSourceSave},
			{Path: "/api/logmonitor/sources/delete", Handler: h.handleSourceDelete},
			{Path: "/api/logmonitor/scan", Handler: h.handleScan},
			{Path: "/api/logmonitor/raw", Handler: h.handleRaw},
			{Path: "/api/logmonitor/delete", Handler: h.handleDelete},
		},
	}
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
	writeJSON(w, http.StatusOK, LogStatsResult{Stats: stats, Histogram: hist})
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
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		writeErr(w, http.StatusBadRequest, "JSON 解析失败: "+err.Error())
		return
	}

	if payload.Line != "" {
		e, err := h.service.Ingest(payload.Line, payload.Service, payload.Source)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"inserted": 1, "id": e.ID})
		return
	}
	if len(payload.Lines) > 0 {
		n, err := h.service.IngestBatch(payload.Lines, payload.Service, payload.Source)
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

// POST /api/logmonitor/scan  { "path":"...", "service":"", "source":"", "tailOnly":false }
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
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "JSON 解析失败: "+err.Error())
		return
	}
	if body.Path == "" {
		writeErr(w, http.StatusBadRequest, "path 不能为空")
		return
	}
	if _, err := os.Stat(body.Path); err != nil {
		writeErr(w, http.StatusBadRequest, "文件不存在: "+err.Error())
		return
	}
	if body.Source == "" {
		body.Source = "file"
	}
	svc := body.Service
	if svc == "" {
		svc = body.DefaultSvc
	}
	n, err := h.service.ScanFile(body.Path, svc, body.Source, body.TailOnly)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"scanned": n, "path": body.Path})
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

// readLogContent 按 metadata 读取文件指定偏移处的日志
func (h *Handlers) readLogContent(e *LogEntry) string {
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

var _ = fmt.Sprintf
