// Package dbmanager 数据库管理模块: 连接 CRUD + 元数据 + 查询执行 + 安全拦截。
// 分层(ADR-001): thin handler → DBService 接口 → GoNavi 底座(gonavi/db)。
// 路由前缀 /api/dbmanager/* —— 由 main.go 通过 registry.Route 挂载。
package dbmanager

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tealeg/xlsx"
	gonavidb "opscore/internal/dbmanager/gonavi/db"
	gonavistatus "opscore/internal/dbmanager/gonavi/db"
	syncpkg "opscore/internal/dbmanager/sync"
	"opscore/internal/registry"
)

const PluginID = "dbmanager"

// Module 返回 registry.Module(用于 main.go 集成)。
func Module(store *Store, pool *DatabasePool) *registry.Module {
	audit := NewAuditLog(store.Central)
	audit.loadFromDisk()
	svc := NewGonaviService(pool)
	h := &Handlers{store: store, pool: pool, svc: svc, unlock: NewWriteUnlockManager(30), audit: audit, sync: syncpkg.NewRunner(pool)}
	module := &registry.Module{
		Manifest: registry.Manifest{
			ID:          PluginID,
			Name:        "数据库管理",
			Icon:        "database",
			RoutePath:   "/dbmanager",
			Group:       "plugin",
			Description: "MySQL/PostgreSQL 连接管理、可视化查询、元数据浏览",
		},
		Routes: []registry.Route{
			{Path: "/api/dbmanager/connections", Handler: h.handleConnections},
			{Path: "/api/dbmanager/connections/test", Handler: h.handleTestConnection},
			{Path: "/api/dbmanager/query", Handler: h.handleQuery},
			{Path: "/api/dbmanager/export", Handler: h.handleExport},
			{Path: "/api/dbmanager/metadata", Handler: h.handleMetadata},
			{Path: "/api/dbmanager/schemas", Handler: h.handleSchemas},
			{Path: "/api/dbmanager/table-overview", Handler: h.handleTableOverview},
			{Path: "/api/dbmanager/fk-graph", Handler: h.handleFkGraph},
			{Path: "/api/dbmanager/table-import", Handler: h.handleTableImport},
			{Path: "/api/dbmanager/table-meta", Handler: h.handleTableMeta},
			{Path: "/api/dbmanager/describe", Handler: h.handleDescribe},
			{Path: "/api/dbmanager/write-unlock", Handler: h.handleWriteUnlock},
			{Path: "/api/dbmanager/write-lock", Handler: h.handleWriteLock},
			{Path: "/api/dbmanager/audit", Handler: h.handleAudit},
			{Path: "/api/dbmanager/engines", Handler: h.handleEngines},
			{Path: "/api/dbmanager/engine-config", Handler: h.handleEngineConfig},
			{Path: "/api/dbmanager/drivers", Handler: h.handleDrivers},
			{Path: "/api/dbmanager/slow-sql", Handler: h.handleSlowSQL},
			{Path: "/api/dbmanager/table-counts", Handler: h.handleTableCounts},
			{Path: "/api/dbmanager/table-status", Handler: h.handleTableStatus},
			{Path: "/api/dbmanager/explain", Handler: h.handleExplain},
			{Path: "/api/dbmanager/sync/engines", Handler: h.handleSyncEngines},
			{Path: "/api/dbmanager/sync/plan", Handler: h.handleSyncPlan},
			{Path: "/api/dbmanager/sync/run", Handler: h.handleSyncRun},
			{Path: "/api/dbmanager/sync/status", Handler: h.handleSyncStatus},
			{Path: "/api/dbmanager/sync/jobs", Handler: h.handleSyncJobs},
			{Path: "/api/dbmanager/sync/cancel", Handler: h.handleSyncCancel},
			{Path: "/api/dbmanager/data", Handler: h.handleData},
			{Path: "/api/dbmanager/table-inserts", Handler: h.handleTableInserts},
			{Path: "/api/dbmanager/apply-edit", Handler: h.handleApplyEdit},
			{Path: "/api/dbmanager/queries", Handler: h.handleQueries},
			{Path: "/api/dbmanager/queries/save", Handler: h.handleSaveQuery},
			{Path: "/api/dbmanager/queries/delete", Handler: h.handleDeleteQuery},
			{Path: "/api/dbmanager/drivers/install", Handler: h.handleDriverInstall},
		},
	}
	fmt.Printf("DEBUG: dbmanager module registered: %v\n", module.Manifest)
	return module
}

// ExportFormat 导出格式
type ExportFormat string

const (
	ExportCSV  ExportFormat = "csv"
	ExportJSON ExportFormat = "json"
	ExportXLSX ExportFormat = "xlsx"
)

// ExportRequest 导出请求结构
type ExportRequest struct {
	ID      string       `json:"id"`
	SQL     string       `json:"sql"`
	Format  ExportFormat `json:"format"`
	MaxRows int          `json:"maxRows"`
}

// Handlers HTTP handler 集合。查询/元数据经 DBService 接口(可替换实现)。
type Handlers struct {
	store  *Store
	pool   *DatabasePool // 仅用于连接变更后的失效清理
	svc    DBService
	unlock *WriteUnlockManager
	audit  *AuditLog
	sync   *syncpkg.Runner
}

var (
	// reConnID 连接 ID 格式校验(防注入)
	reConnID = regexp.MustCompile(`^[a-f0-9]{8,64}$`)
	// reDBName / reTableName 库/表名校验
	reDBName    = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
	reTableName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
)

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// writeJSONStatus 带结构化 code/risk 的拦截响应(前端按 code 分流处理)。
func writeJSONStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func methodNotAllowed(w http.ResponseWriter) {
	writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
}

// ===== /api/dbmanager/connections =====
// GET -> 列表; POST -> 创建; PUT -> 更新; DELETE -> 删除

func (h *Handlers) handleConnections(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		conns, err := h.store.List()
		if err != nil {
			writeErr(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"connections": conns})
	case http.MethodPost:
		var body struct {
			Name     string           `json:"name"`
			Engine   EngineType       `json:"engine"`
			Config   ConnectionConfig `json:"config"`
			Password string           `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, "invalid body", http.StatusBadRequest)
			return
		}
		if !engineTypeSupported(body.Engine) {
			writeErr(w, "不支持的引擎类型", http.StatusBadRequest)
			return
		}

		// 如果没有提供配置，使用默认配置
		if isEmptyConfig(body.Config) {
			body.Config = GetEngineDefaultConfig(body.Engine)
		}

		if !validConnConfig(body.Config, body.Engine) {
			writeErr(w, "连接配置校验失败", http.StatusBadRequest)
			return
		}
		conn, err := h.store.Create(body.Name, body.Engine, body.Config, body.Password)
		if err != nil {
			writeErr(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "connection": conn})
	case http.MethodPut:
		var body struct {
			ID       string           `json:"id"`
			Name     string           `json:"name"`
			Engine   EngineType       `json:"engine"`
			Config   ConnectionConfig `json:"config"`
			Password string           `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, "invalid body", http.StatusBadRequest)
			return
		}
		if !reConnID.MatchString(body.ID) {
			writeErr(w, "id 格式非法", http.StatusBadRequest)
			return
		}
		if !validConnConfig(body.Config, body.Engine) {
			writeErr(w, "连接配置校验失败", http.StatusBadRequest)
			return
		}
		conn, err := h.store.Update(body.ID, body.Name, body.Config, body.Password)
		if err != nil {
			writeErr(w, err.Error(), http.StatusBadRequest)
			return
		}
		h.pool.Release(body.ID)
		writeJSON(w, map[string]any{"ok": true, "connection": conn})
	case http.MethodDelete:
		var body struct {
			ID string `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, "invalid body", http.StatusBadRequest)
			return
		}
		if !reConnID.MatchString(body.ID) {
			writeErr(w, "id 格式非法", http.StatusBadRequest)
			return
		}
		h.pool.Release(body.ID)
		if err := h.store.Delete(body.ID); err != nil {
			writeErr(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	default:
		methodNotAllowed(w)
	}
}

func validConnConfig(c ConnectionConfig, engine EngineType) bool {
	// 文件型引擎(sqlite 等): database=文件路径, 无 host/port/username; 路径含 \:/ 等不适用 reDBName
	switch strings.ToLower(string(engine)) {
	case "sqlite":
		return strings.TrimSpace(c.Database) != ""
	}
	if strings.TrimSpace(c.Host) == "" || c.Port <= 0 || c.Port > 65535 || strings.TrimSpace(c.Username) == "" {
		return false
	}
	if c.Database != "" && !reDBName.MatchString(c.Database) {
		return false
	}
	switch c.EnvTag {
	case "", "dev", "staging", "prod":
	default:
		return false
	}
	return true
}

// GetEngineDefaultConfig 获取指定引擎的默认配置
func GetEngineDefaultConfig(engine EngineType) ConnectionConfig {
	meta, ok := engineMetas[engine]
	if !ok {
		return ConnectionConfig{SSLMode: "preferred", Options: make(map[string]string)}
	}
	sslMode := "preferred"
	switch meta.Category {
	case "vector", "search", "mq":
		sslMode = "disable"
	}
	return ConnectionConfig{
		Host:            "",
		Port:            meta.DefaultPort,
		Database:        meta.DefaultDB,
		Username:        "",
		SSLMode:         sslMode,
		EnvTag:          "",
		Options:         make(map[string]string),
		TimeoutSec:      15,
		QueryTimeoutSec: 30,
		MaxRows:         MaxQueryRows,
	}
}

// ===== /api/dbmanager/connections/test =====
// POST {id?, engine, config, password} -> 临时建立连接并返回 server version

func (h *Handlers) handleTestConnection(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var body struct {
		ID       string           `json:"id"`
		Engine   EngineType       `json:"engine"`
		Config   ConnectionConfig `json:"config"`
		Password string           `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, "invalid body", http.StatusBadRequest)
		return
	}

	var conn *Connection
	if body.ID != "" {
		// 测试已保存的连接
		if !reConnID.MatchString(body.ID) {
			writeErr(w, "id 格式非法", http.StatusBadRequest)
			return
		}
		var err error
		conn, err = h.store.Get(body.ID)
		if err != nil {
			writeErr(w, err.Error(), http.StatusNotFound)
			return
		}
	} else {
		// 测试新连接
		if !engineTypeSupported(body.Engine) {
			writeErr(w, "不支持的引擎类型", http.StatusBadRequest)
			return
		}
		if !validConnConfig(body.Config, body.Engine) {
			writeErr(w, "连接配置校验失败", http.StatusBadRequest)
			return
		}
		conn = &Connection{
			Info: ConnectionInfo{
				Engine: body.Engine,
				Config: body.Config,
			},
			Password: body.Password,
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	version, err := h.svc.TestConnection(ctx, conn)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "version": version})
}

// ===== /api/dbmanager/query =====
// POST {id, sql, maxRows?, confirm?} -> 执行 SQL 并返回结果
// 写操作经 ADR-003 拦截链: 风险分级 -> 生产库感知 -> 高危确认 -> 限时写解锁。

func (h *Handlers) handleQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var body struct {
		ID       string `json:"id"`
		SQL      string `json:"sql"`
		MaxRows  int    `json:"maxRows"`
		Confirm  bool   `json:"confirm"`
		Database string `json:"database"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, "invalid body", http.StatusBadRequest)
		return
	}
	if !reConnID.MatchString(body.ID) {
		writeErr(w, "id 格式非法", http.StatusBadRequest)
		return
	}
	fmt.Fprintln(os.Stderr, "[dbg-q] database=", body.Database)
	if strings.TrimSpace(body.SQL) == "" {
		writeErr(w, "SQL 不能为空", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	conn, err := h.store.Get(body.ID)
	if err != nil {
		writeErr(w, "获取连接失败: "+err.Error(), http.StatusBadRequest)
		return
	}

	// ── ADR-003 拦截链 ──
	risk, reason := classifySQLRisk(string(conn.Info.Engine), body.SQL)
	if risk.AtLeast(RiskMedium) {
		if blocked := h.interceptWrite(w, conn, body.ID, body.SQL, risk, reason, body.Confirm); blocked {
			return
		}
	}

	res, err := h.svc.ExecQuery(ctx, body.ID, body.SQL, body.MaxRows, body.Database)
	// 写操作记入审计(只读不记, 避免噪声)
	if risk.AtLeast(RiskMedium) {
		decision := "executed"
		detail := reason
		if err != nil {
			decision = "failed"
			detail = resErrText(res, err)
		}
		h.audit.Append(AuditEntry{
			ConnID:   body.ID,
			ConnName: conn.Info.Name,
			Engine:   string(conn.Info.Engine),
			SQL:      body.SQL,
			Risk:     string(risk),
			Decision: decision,
			Detail:   detail,
		})
	}
	if err != nil && res == nil {
		res = &QueryResult{Error: err.Error()}
	}
	writeJSON(w, res)
}

func resErrText(res *QueryResult, err error) string {
	if res != nil && res.Error != "" {
		return res.Error
	}
	return err.Error()
}

// interceptWrite ADR-003 写操作拦截链。返回 true 表示已拦截(响应已写出)。
// 顺序: 防切库(硬拦截) -> 高危确认(critical / 生产库高危) -> 限时写解锁。
func (h *Handlers) interceptWrite(w http.ResponseWriter, conn *Connection, connID, sqlText string, risk SqlRisk, reason string, confirmed bool) bool {
	auditDenied := func(detail string) {
		h.audit.Append(AuditEntry{
			ConnID:   connID,
			ConnName: conn.Info.Name,
			Engine:   string(conn.Info.Engine),
			SQL:      sqlText,
			Risk:     string(risk),
			Decision: "denied",
			Detail:   detail,
		})
	}

	// 1. 防切库: USE / SET search_path 直接拒绝, 无解锁通道
	if msg := forbiddenSwitchReason(string(conn.Info.Engine), sqlText); msg != "" {
		auditDenied(msg)
		writeJSONStatus(w, http.StatusForbidden, map[string]any{
			"error": msg,
			"code":  "blocked",
		})
		return true
	}

	prod := isProductionConnection(conn.Info.Name, conn.Info.Config)

	// 2. 高危确认: critical 一律需显式 confirm; 生产库 high 及以上也需确认
	needConfirm := risk == RiskCritical || (prod && risk.AtLeast(RiskHigh))
	if needConfirm && !confirmed {
		prefix := ""
		if prod {
			prefix = "【生产库】"
		}
		auditDenied("高危操作等待确认")
		writeJSONStatus(w, http.StatusForbidden, map[string]any{
			"error":  prefix + "高危操作(" + risk.HumanReadable() + "): " + reason + ", 需要二次确认",
			"code":   "confirm_required",
			"risk":   string(risk),
			"reason": reason,
		})
		return true
	}

	// 3. 限时写解锁: 未解锁或已过期的连接禁止写
	if rem := h.unlock.Remaining(connID); rem <= 0 {
		auditDenied("连接处于只读模式(未解锁)")
		writeJSONStatus(w, http.StatusForbidden, map[string]any{
			"error":  "写操作被拦截: 连接默认只读, 请先解锁写模式(限时有效)",
			"code":   "write_locked",
			"risk":   string(risk),
			"reason": reason,
		})
		return true
	}

	return false
}

// ===== /api/dbmanager/metadata =====
// GET ?id=...&type=databases|tables&database=... -> 拉取级联元数据

// handleSchemas GET ?id=... -> 连接引擎的命名空间列表(三级命名引擎), 其余引擎返回空数组。
func (h *Handlers) handleSchemas(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	id := r.URL.Query().Get("id")
	if !reConnID.MatchString(id) {
		writeErr(w, "id 格式非法", http.StatusBadRequest)
		return
	}
	schemas, err := h.svc.ListSchemas(r.Context(), id)
	if err != nil {
		writeErr(w, "列出模式失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if schemas == nil {
		schemas = []string{}
	}
	writeJSON(w, map[string]any{"schemas": schemas})
}

func (h *Handlers) handleMetadata(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	id := r.URL.Query().Get("id")
	if !reConnID.MatchString(id) {
		writeErr(w, "id 格式非法", http.StatusBadRequest)
		return
	}
	kind := r.URL.Query().Get("type")

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	switch kind {
	case "databases":
		names, err := h.svc.ListDatabases(ctx, id)
		if err != nil {
			writeErr(w, "列出数据库失败: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"databases": names})
	case "tables":
		database := r.URL.Query().Get("database")
		if !reDBName.MatchString(database) {
			writeErr(w, "database 格式非法", http.StatusBadRequest)
			return
		}
		tables, err := h.svc.ListTables(ctx, id, database)
		if err != nil {
			writeErr(w, "列出表失败: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"tables": tables})
	case "objects":
		database := r.URL.Query().Get("database")
		if !reDBName.MatchString(database) {
			writeErr(w, "database 格式非法", http.StatusBadRequest)
			return
		}
		objs, err := h.svc.ListObjects(ctx, id, database)
		if err != nil {
			writeErr(w, "列出对象失败: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"objects": objs})
	case "object-ddl":
		database := r.URL.Query().Get("database")
		objectName := r.URL.Query().Get("object")
		kind := r.URL.Query().Get("kind")
		if !reDBName.MatchString(database) || !reTableName.MatchString(objectName) {
			writeErr(w, "database/object 格式非法", http.StatusBadRequest)
			return
		}
		switch kind {
		case gonavistatus.KindView, gonavistatus.KindMaterializedView,
			gonavistatus.KindFunction, gonavistatus.KindProcedure,
			gonavistatus.KindEvent, gonavistatus.KindTrigger, gonavistatus.KindSequence:
		default:
			writeErr(w, "kind 必须是视图/函数/存储过程/事件/触发器/序列", http.StatusBadRequest)
			return
		}
		ddl, err := h.svc.GetObjectDefinition(ctx, id, database, objectName, kind)
		if err != nil {
			writeErr(w, "获取 DDL 失败: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"ddl": ddl})
	default:
		writeErr(w, "type 必须是 databases/tables/objects/object-ddl", http.StatusBadRequest)
	}
}

// ===== /api/dbmanager/describe =====
// GET ?id=...&database=...&table=... -> 列/索引/DDL

func (h *Handlers) handleDescribe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	id := r.URL.Query().Get("id")
	database := r.URL.Query().Get("database")
	table := r.URL.Query().Get("table")
	if !reConnID.MatchString(id) {
		writeErr(w, "id 格式非法", http.StatusBadRequest)
		return
	}
	if !reDBName.MatchString(database) || !reTableName.MatchString(table) {
		writeErr(w, "database/table 格式非法", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	conn, err := h.store.Get(id)
	if err != nil {
		writeErr(w, "获取连接失败: "+err.Error(), http.StatusBadRequest)
		return
	}
	cols, idxs, ddl, err := h.svc.DescribeTable(ctx, id, database, table)
	if err != nil {
		writeErr(w, "Describe 失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{
		"columns":  cols,
		"indexes":  idxs,
		"ddl":      ddl,
		"engine":   string(conn.Info.Engine),
		"database": database,
		"table":    table,
	})
}

// ===== /api/dbmanager/write-unlock =====
// GET ?id=... -> 查询解锁状态 {unlocked, remainingSec, maxMinutes}
// POST {id, minutes} -> 解锁写模式(限时)

func (h *Handlers) handleWriteUnlock(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		id := r.URL.Query().Get("id")
		if !reConnID.MatchString(id) {
			writeErr(w, "id 格式非法", http.StatusBadRequest)
			return
		}
		rem := h.unlock.Remaining(id)
		writeJSON(w, map[string]any{
			"unlocked":     rem > 0,
			"remainingSec": rem,
			"maxMinutes":   h.unlock.MaxMinutes(),
		})
	case http.MethodPost:
		var body struct {
			ID      string `json:"id"`
			Minutes int    `json:"minutes"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, "invalid body", http.StatusBadRequest)
			return
		}
		if !reConnID.MatchString(body.ID) {
			writeErr(w, "id 格式非法", http.StatusBadRequest)
			return
		}
		until := h.unlock.Unlock(body.ID, body.Minutes)
		writeJSON(w, map[string]any{
			"ok":           true,
			"unlockUntil":  until.Unix(),
			"remainingSec": h.unlock.Remaining(body.ID),
		})
	default:
		methodNotAllowed(w)
	}
}

// ===== /api/dbmanager/write-lock =====
// POST {id} -> 立即收回写权限

func (h *Handlers) handleWriteLock(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, "invalid body", http.StatusBadRequest)
		return
	}
	if !reConnID.MatchString(body.ID) {
		writeErr(w, "id 格式非法", http.StatusBadRequest)
		return
	}
	h.unlock.Lock(body.ID)
	writeJSON(w, map[string]any{"ok": true})
}

// ===== /api/dbmanager/engines =====
// GET -> 列出全部引擎元数据 + 当前 runtime 可用状态
//   status: "builtin" | "optional" | "disabled" | "unknown"

func (h *Handlers) handleEngines(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	all := AllEngineMetas()
	out := make([]map[string]any, 0, len(all))
	for _, m := range all {
		status, reason := driverStatus(string(m.Type))
		out = append(out, map[string]any{
			"type":          m.Type,
			"label":         m.Label,
			"short":         m.Short,
			"category":      m.Category,
			"defaultPort":   m.DefaultPort,
			"defaultDb":     m.DefaultDB,
			"hasSql":        m.HasSQL,
			"hasSchema":     m.HasSchema,
			"hasTable":      m.HasTable,
			"hasCollection": m.HasCollection,
			"supportsDml":   m.SupportsDML,
			"supportsDdl":   m.SupportsDDL,
			"color":         m.Color,
			"description":   m.Description,
			"status":        status,
			"reason":        reason,
		})
	}
	writeJSON(w, map[string]any{"engines": out})
}

// driverStatus 委托给 GoNavi 底座的运行时探测。
// 返回 (status, reason): status ∈ builtin / optional / disabled / unknown。
func driverStatus(t string) (string, string) {
	// mysql_agent 是 driver-agent 架构专用引擎(lite 版无 agent 二进制), 一律禁用,
	// 引导用户用原生 MySQL。存量 mysql_agent 连接仍可编辑。
	if t == "mysql_agent" {
		return "disabled", "请使用原生 MySQL 引擎(driver-agent 版本未随 lite 版提供)"
	}
	ok, reason := gonavistatus.DriverRuntimeSupportStatus(t)
	if !ok {
		return "disabled", reason
	}
	if gonavistatus.IsBuiltinDriver(t) {
		return "builtin", ""
	}
	return "optional", ""
}

// ===== /api/dbmanager/drivers =====
// GET -> 列出所有驱动的运行时状态
//   builtin: 内置驱动, 开箱即用
//   optional: 可选驱动, 已安装/可启用
//   disabled: 未安装/不可用

func (h *Handlers) handleDrivers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	all := AllEngineMetas()
	out := make([]map[string]any, 0, len(all))
	for _, m := range all {
		status, reason := driverStatus(string(m.Type))
		// 安装状态独立判定(标记文件), 不被连接可用性(status=disabled: agent 缺失/构建未启用)短路——
		// 安装真实发生就要如实显示"已安装"; 连接可用性与否由 status/reason 另行如实表达
		installed := gonavistatus.IsOptionalGoDriver(string(m.Type)) && gonavistatus.IsOptionalGoDriverInstalled(string(m.Type))
		out = append(out, map[string]any{
			"type":      m.Type,
			"label":     m.Label,
			"short":     m.Short,
			"category":  m.Category,
			"color":     m.Color,
			"status":    status,
			"reason":    reason,
			"installed": installed,
			"builtin":   status == "builtin",
		})
	}
	writeJSON(w, map[string]any{"drivers": out})
}

// ===== /api/dbmanager/engine-config =====
// GET ?engine=... -> 获取指定引擎类型的默认配置
// POST {engine} -> 获取指定引擎类型的默认配置

func (h *Handlers) handleEngineConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}

	var engine EngineType
	if r.Method == http.MethodGet {
		engineStr := r.URL.Query().Get("engine")
		if engineStr == "" {
			writeErr(w, "engine 参数不能为空", http.StatusBadRequest)
			return
		}
		engine = EngineType(engineStr)
	} else {
		var body struct {
			Engine EngineType `json:"engine"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, "invalid body", http.StatusBadRequest)
			return
		}
		engine = body.Engine
	}

	if !engineTypeSupported(engine) {
		writeErr(w, "不支持的引擎类型", http.StatusBadRequest)
		return
	}

	config := EngineTypeConfig(engine)
	writeJSON(w, map[string]any{
		"engine": engine,
		"config": config,
	})
}

// ===== /api/dbmanager/audit =====
// GET ?id=... -> 审计记录(新的在前, 可选按连接过滤)

func (h *Handlers) handleAudit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	id := r.URL.Query().Get("id")
	if id != "" && !reConnID.MatchString(id) {
		writeErr(w, "id 格式非法", http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"entries": h.audit.List(id)})
}

// ===== /api/dbmanager/export =====
// POST {id, sql, format, maxRows} -> 执行 SQL 并导出结果为文件
// 支持格式: csv, json, xlsx

func (h *Handlers) handleExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var body ExportRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, "invalid body", http.StatusBadRequest)
		return
	}
	if !reConnID.MatchString(body.ID) {
		writeErr(w, "id 格式非法", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(body.SQL) == "" {
		writeErr(w, "SQL 不能为空", http.StatusBadRequest)
		return
	}
	if body.MaxRows <= 0 {
		body.MaxRows = 10000 // 默认最大行数
	}

	// 执行查询
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	conn, err := h.store.Get(body.ID)
	if err != nil {
		writeErr(w, "获取连接失败: "+err.Error(), http.StatusBadRequest)
		return
	}

	// ── ADR-003 拦截链 ──
	risk, reason := classifySQLRisk(string(conn.Info.Engine), body.SQL)
	if risk.AtLeast(RiskMedium) {
		if blocked := h.interceptWrite(w, conn, body.ID, body.SQL, risk, reason, true); blocked {
			return
		}
	}

	res, err := h.svc.ExecQuery(ctx, body.ID, body.SQL, body.MaxRows, "")
	if err != nil && res == nil {
		res = &QueryResult{Error: err.Error()}
	}

	// 生成文件内容到内存(避免中途出错时已写出半截响应)
	var buf bytes.Buffer
	var contentType, ext string
	switch body.Format {
	case ExportCSV:
		err = h.exportToCSV(res, &buf)
		contentType, ext = "text/csv; charset=utf-8", "csv"
	case ExportJSON:
		err = h.exportToJSON(res, &buf)
		contentType, ext = "application/json; charset=utf-8", "json"
	case ExportXLSX:
		err = h.exportToXLSX(res, &buf)
		contentType, ext = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", "xlsx"
	default:
		writeErr(w, "不支持的导出格式", http.StatusBadRequest)
		return
	}
	if err != nil {
		writeErr(w, "导出失败: "+err.Error(), http.StatusInternalServerError)
		return
	}

	fileName := fmt.Sprintf("query_export_%s.%s", time.Now().Format("20060102_150405"), ext)
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, fileName))
	_, _ = w.Write(buf.Bytes())
}

func (h *Handlers) exportToCSV(res *QueryResult, buf *bytes.Buffer) error {
	if res.Error != "" {
		return fmt.Errorf("查询错误: %s", res.Error)
	}

	// UTF-8 BOM: 让 Excel 正确识别中文
	buf.WriteString("\xEF\xBB\xBF")
	writer := csv.NewWriter(buf)
	defer writer.Flush()

	// 写入表头
	if len(res.Columns) > 0 {
		if err := writer.Write(res.Columns); err != nil {
			return err
		}
	}

	// 写入数据
	for _, row := range res.Rows {
		srow := make([]string, len(row))
		for i, v := range row {
			if v == nil {
				srow[i] = ""
			} else {
				srow[i] = fmt.Sprintf("%v", v)
			}
		}
		if err := writer.Write(srow); err != nil {
			return err
		}
	}

	return nil
}

func (h *Handlers) exportToJSON(res *QueryResult, buf *bytes.Buffer) error {
	if res.Error != "" {
		return fmt.Errorf("查询错误: %s", res.Error)
	}

	exportData := map[string]any{
		"columns": res.Columns,
		"rows":    res.Rows,
		"count":   len(res.Rows),
	}

	encoder := json.NewEncoder(buf)
	encoder.SetIndent("", "  ")
	return encoder.Encode(exportData)
}

func (h *Handlers) exportToXLSX(res *QueryResult, buf *bytes.Buffer) error {
	if res.Error != "" {
		return fmt.Errorf("查询错误: %s", res.Error)
	}

	xlsxFile := xlsx.NewFile()
	sheet, err := xlsxFile.AddSheet("Sheet1")
	if err != nil {
		return err
	}

	// 写入表头
	if len(res.Columns) > 0 {
		row := sheet.AddRow()
		for _, col := range res.Columns {
			cell := row.AddCell()
			cell.Value = col
		}
	}

	// 写入数据
	for _, row := range res.Rows {
		xlsxRow := sheet.AddRow()
		for _, cell := range row {
			xlsxCell := xlsxRow.AddCell()
			if cell == nil {
				xlsxCell.Value = ""
			} else {
				xlsxCell.Value = fmt.Sprintf("%v", cell)
			}
		}
	}

	return xlsxFile.Write(buf)
}

// ===== /api/dbmanager/slow-sql =====
// GET ?id=...&limit=20 -> 慢 SQL 列表 (MySQL performance_schema / 各引擎适配)

func (h *Handlers) handleSlowSQL(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	id := r.URL.Query().Get("id")
	if !reConnID.MatchString(id) {
		writeErr(w, "id 格式非法", http.StatusBadRequest)
		return
	}
	limit := 20
	if l := r.URL.Query().Get("limit"); l != "" {
		fmt.Sscanf(l, "%d", &limit)
		if limit <= 0 || limit > 200 {
			limit = 20
		}
	}

	conn, err := h.store.Get(id)
	if err != nil {
		writeErr(w, err.Error(), http.StatusNotFound)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	_ = ctx

	db, _, err := h.pool.Acquire(id)
	if err != nil {
		writeErr(w, "获取连接失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer h.pool.Release(id)

	var rows []map[string]interface{}
	var cols []string

	switch conn.Info.Engine {
	case EngineMySQL:
		query := fmt.Sprintf(`
			SELECT DIGEST_TEXT, COUNT_STAR, SUM_TIMER_WAIT/1000000000 AS total_ms,
			       AVG_TIMER_WAIT/1000000000 AS avg_ms, MAX_TIMER_WAIT/1000000000 AS max_ms,
			       FIRST_SEEN, LAST_SEEN
			FROM performance_schema.events_statements_summary_by_digest
			WHERE SUM_TIMER_WAIT > 0
			ORDER BY SUM_TIMER_WAIT DESC
			LIMIT %d`, limit)
		data, colNames, err := db.Query(query)
		if err != nil {
			writeErr(w, "查询慢 SQL 失败: "+err.Error(), http.StatusInternalServerError)
			return
		}
		cols = colNames
		for _, row := range data {
			item := make(map[string]interface{})
			for _, c := range colNames {
				item[c] = row[c]
			}
			rows = append(rows, item)
		}
	default:
		writeJSON(w, map[string]any{
			"engine":  conn.Info.Engine,
			"rows":    []any{},
			"columns": []string{"digest_text", "count_star", "avg_ms", "total_ms", "max_ms", "first_seen", "last_seen"},
			"note":    fmt.Sprintf("%s 引擎暂不支持慢 SQL 采集", conn.Info.Engine),
		})
		return
	}

	writeJSON(w, map[string]any{
		"engine":  conn.Info.Engine,
		"rows":    rows,
		"columns": cols,
		"limit":   limit,
	})
}

// ===== /api/dbmanager/sync/* 跨库同步 =====
// 方言对(MySQL 族 ↔ PostgreSQL 族)的 Schema 迁移 + 全量/增量数据同步。
// 流程: POST plan 预览(类型映射/DDL/增量策略) → POST run 后台执行 → GET status 轮询进度。

func decodeSyncRequest(r *http.Request) (syncpkg.SyncRequest, error) {
	var req syncpkg.SyncRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return req, fmt.Errorf("invalid body: %w", err)
	}
	if !reConnID.MatchString(req.SourceID) || !reConnID.MatchString(req.TargetID) {
		return req, fmt.Errorf("连接 ID 格式非法")
	}
	if strings.TrimSpace(req.SourceDB) == "" || strings.TrimSpace(req.TargetDB) == "" {
		return req, fmt.Errorf("源库/目标库不能为空")
	}
	switch req.Mode {
	case syncpkg.ModeSchemaOnly, syncpkg.ModeSchemaFull, syncpkg.ModeSchemaFullIncr,
		syncpkg.ModeTruncateFull, syncpkg.ModeIncrOnly, syncpkg.ModeVerify:
	default:
		return req, fmt.Errorf("不支持的同步模式: %s", req.Mode)
	}
	return req, nil
}

// handleSyncPlan POST {sourceId, sourceDb, targetId, targetDb, tables?, mode} -> 迁移计划预览
func (h *Handlers) handleSyncPlan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	req, err := decodeSyncRequest(r)
	if err != nil {
		writeErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	plan, err := h.sync.BuildPlan(ctx, req)
	if err != nil {
		writeErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"plan": plan})
}

// handleSyncEngines GET -> 已接入同步方言族的引擎清单(能力驱动, 前端标注连接可同步性)
func (h *Handlers) handleSyncEngines(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	writeJSON(w, map[string]any{"engines": syncpkg.SyncCapableEngines()})
}

// handleSyncRun POST {同 plan 请求} -> 启动后台任务, 返回 jobId
func (h *Handlers) handleSyncRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	req, err := decodeSyncRequest(r)
	if err != nil {
		writeErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.SourceID == req.TargetID {
		writeErr(w, "源与目标不能是同一连接", http.StatusBadRequest)
		return
	}
	job := h.sync.Start(req, nil)
	writeJSON(w, map[string]any{"ok": true, "jobId": job.ID})
}

// handleSyncStatus GET ?id=... -> 任务进度
func (h *Handlers) handleSyncStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	id := r.URL.Query().Get("id")
	job := h.sync.Jobs().Get(id)
	if job == nil {
		writeErr(w, "任务不存在", http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]any{"job": job})
}

// handleSyncJobs GET -> 任务列表
func (h *Handlers) handleSyncJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	writeJSON(w, map[string]any{"jobs": h.sync.Jobs().List()})
}

// handleSyncCancel POST {id} -> 取消运行中任务
func (h *Handlers) handleSyncCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, "invalid body", http.StatusBadRequest)
		return
	}
	if !h.sync.Jobs().Cancel(body.ID) {
		writeErr(w, "任务不存在或已结束", http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// ===== /api/dbmanager/table-status =====
// GET ?id=...&database=...&table=... -> 表属性 (SHOW TABLE STATUS / information_schema)

// handleTableCounts GET ?id=&database= -> 整库各表行数(估算, 供树徽标; 与 dbx/gonavi 一致用 information_schema/pg_class)
// 返回 {counts: {表名: 行数}}; 表名与 listTables 一致(PG=限定名 schema.table, MySQL=裸名)。
func (h *Handlers) handleTableCounts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	q := r.URL.Query()
	id, database := q.Get("id"), q.Get("database")
	if !reConnID.MatchString(id) || !reDBName.MatchString(database) {
		writeErr(w, "id/database 格式非法", http.StatusBadRequest)
		return
	}
	db, conn, err := h.pool.Acquire(id)
	if err != nil {
		writeErr(w, "获取连接失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer h.pool.Release(id)

	esc := func(s string) string { return strings.ReplaceAll(s, "'", "''") }
	var sqlText string
	switch string(conn.Info.Engine) {
	case "mysql", "mariadb", "goldendb":
		// 精确 COUNT(*): information_schema.table_rows 估算受会话缓存(& ANALYZE 前为 0)影响, 易误导
		nameRows, _, err := syncpkg.QueryRows(r.Context(), db,
			"SELECT table_name FROM information_schema.TABLES WHERE table_schema = '"+esc(database)+"' AND table_type = 'BASE TABLE'")
		if err != nil {
			writeErr(w, "统计行数失败: "+err.Error(), http.StatusInternalServerError)
			return
		}
		tables := make([]string, 0, len(nameRows))
		for _, row := range nameRows {
			var n string
			switch v := row["table_name"].(type) {
			case string:
				n = v
			case []byte:
				n = string(v)
			}
			if n == "" {
				if v2, ok := row["TABLE_NAME"].(string); ok {
					n = v2
				} else if v3, ok := row["TABLE_NAME"].([]byte); ok {
					n = string(v3)
				}
			}
			if n != "" {
				tables = append(tables, n)
			}
		}
		counts := map[string]int64{}
		sem := make(chan struct{}, 8)
		var mu sync.Mutex
		var wg sync.WaitGroup
		qident := func(name string) string { return "`" + strings.ReplaceAll(name, "`", "``") + "`" }
		for _, t := range tables {
			wg.Add(1)
			sem <- struct{}{}
			go func(t string) {
				defer wg.Done()
				defer func() { <-sem }()
				cRows, _, e := syncpkg.QueryRows(r.Context(), db, "SELECT COUNT(*) AS c FROM "+qident(database)+"."+qident(t))
				if e != nil {
					return
				}
				var c int64
				for _, row := range cRows {
					raw, ok := row["c"]
					if !ok {
						raw = row["C"]
					}
					switch v := raw.(type) {
					case int64:
						c = v
					case int32:
						c = int64(v)
					case float64:
						c = int64(v)
					case string:
						if n, pe := strconv.ParseInt(v, 10, 64); pe == nil {
							c = n
						}
					case []byte:
						if n, pe := strconv.ParseInt(string(v), 10, 64); pe == nil {
							c = n
						}
					}
				}
				mu.Lock()
				counts[t] = c
				mu.Unlock()
			}(t)
		}
		wg.Wait()
		writeJSON(w, map[string]any{"counts": counts})
		return
	case "postgres", "opengauss", "kingbase", "highgo", "vastbase", "gaussdb":
		sqlText = `SELECT (n.nspname || '.' || c.relname) AS n, c.reltuples::bigint AS c ` +
			`FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace ` +
			`WHERE c.relkind IN ('r','p','v','m') AND n.nspname <> 'information_schema' AND n.nspname NOT LIKE 'pg|_%' ESCAPE '|'`
	default:
		writeJSON(w, map[string]any{"counts": map[string]int64{}})
		return
	}
	rows, _, err := syncpkg.QueryRows(r.Context(), db, sqlText)
	if err != nil {
		writeErr(w, "统计行数失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	counts := map[string]int64{}
	for _, row := range rows {
		// 驱动间值类型不一致(MySQL 常返回 []byte, PG 返回 string), 统一兼容
		var name string
		switch v := row["n"].(type) {
		case string:
			name = v
		case []byte:
			name = string(v)
		}
		if name == "" {
			continue
		}
		var cnt int64
		switch v := row["c"].(type) {
		case int64:
			cnt = v
		case int32:
			cnt = int64(v)
		case float64:
			cnt = int64(v)
		case string:
			if n, e := strconv.ParseInt(v, 10, 64); e == nil {
				cnt = n
			}
		case []byte:
			if n, e := strconv.ParseInt(string(v), 10, 64); e == nil {
				cnt = n
			}
		}
		counts[name] = cnt
	}
	writeJSON(w, map[string]any{"counts": counts})
}

// handleTableMeta GET ?id=&database=&table= -> 完整表信息(列/索引/外键/触发器/DDL)
func (h *Handlers) handleTableMeta(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	q := r.URL.Query()
	id, database, table := q.Get("id"), q.Get("database"), q.Get("table")
	if !reConnID.MatchString(id) || !reDBName.MatchString(database) || !reTableName.MatchString(table) {
		writeErr(w, "id/database/table 格式非法", http.StatusBadRequest)
		return
	}
	meta, err := h.svc.GetTableMeta(r.Context(), id, database, table)
	if err != nil {
		writeErr(w, "获取表信息失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"meta": meta})
}

// handleFkGraph GET ?id=&database= -> 库级表+外键关系(ER 图数据源)
// 返回 {tables:[{name}], edges:[{fromTable,fromColumn,toTable,toColumn}]}
// handleTableOverview GET ?id=&database= -> 库级表概览(GoNavi 同款字段)
// 返回 {overview:[{name,rows,dataSize,indexSize,engine,comment,createdAt,updatedAt}]}
func (h *Handlers) handleTableOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	q := r.URL.Query()
	id, database := q.Get("id"), q.Get("database")
	if !reConnID.MatchString(id) || !reDBName.MatchString(database) {
		writeErr(w, "id/database 格式非法", http.StatusBadRequest)
		return
	}
	db, conn, err := h.pool.Acquire(id)
	if err != nil {
		writeErr(w, "获取连接失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer h.pool.Release(id)

	esc := func(s string) string { return strings.ReplaceAll(s, "'", "''") }
	var sqlText string
	switch strings.ToLower(string(conn.Info.Engine)) {
	case "mysql", "mariadb", "goldendb":
		sqlText = "SELECT table_name AS name, IFNULL(table_rows, 0) AS table_rows, IFNULL(data_length, 0) AS data_size, " +
			"IFNULL(index_length, 0) AS index_size, engine, table_comment AS table_comment, " +
			"IFNULL(CAST(create_time AS CHAR), '') AS created_at, IFNULL(CAST(update_time AS CHAR), '') AS updated_at " +
			"FROM information_schema.TABLES WHERE table_schema = '" + esc(database) + "' AND table_type = 'BASE TABLE' ORDER BY table_name"
	case "postgres", "opengauss", "kingbase", "highgo", "vastbase", "gaussdb":
		sqlText = `SELECT c.relname AS name, GREATEST(c.reltuples, 0)::bigint AS rows,
			pg_total_relation_size(c.oid) - COALESCE(pg_indexes_size(c.oid), 0) AS data_size,
			COALESCE(pg_indexes_size(c.oid), 0) AS index_size,
			'PostgreSQL' AS engine,
			COALESCE(obj_description(c.oid), '') AS comment,
			'' AS created_at, '' AS updated_at
			FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE c.relkind IN ('r','p') AND n.nspname = '` + esc(database) + `' ORDER BY c.relname`
	default:
		writeErr(w, "该引擎暂不支持表概览", http.StatusBadRequest)
		return
	}
	rows, _, err := syncpkg.QueryRows(r.Context(), db, sqlText)
	if err != nil {
		writeErr(w, "读取表概览失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	get := func(row map[string]any, keys ...string) any {
		for _, k := range keys {
			if v, ok := row[k]; ok {
				return v
			}
		}
		return nil
	}
	type Row struct {
		Name      string `json:"name"`
		Rows      int64  `json:"rows"`
		DataSize  int64  `json:"dataSize"`
		IndexSize int64  `json:"indexSize"`
		Engine    string `json:"engine"`
		Comment   string `json:"comment"`
		CreatedAt string `json:"createdAt"`
		UpdatedAt string `json:"updatedAt"`
	}
	out := make([]Row, 0, len(rows))
	for _, row := range rows {
		str := func(v any) string {
			switch t := v.(type) {
			case string:
				return t
			case []byte:
				return string(t)
			}
			return ""
		}
		num := func(v any) int64 {
			switch t := v.(type) {
			case int64:
				return t
			case int32:
				return int64(t)
			case float64:
				return int64(t)
			case []byte:
				n, _ := strconv.ParseInt(string(t), 10, 64)
				return n
			case string:
				n, _ := strconv.ParseInt(t, 10, 64)
				return n
			}
			return 0
		}
		out = append(out, Row{
			Name:      str(get(row, "name", "NAME")),
			Rows:      num(get(row, "table_rows", "TABLE_ROWS")),
			DataSize:  num(get(row, "data_size", "DATA_SIZE")),
			IndexSize: num(get(row, "index_size", "INDEX_SIZE")),
			Engine:    str(get(row, "engine", "ENGINE")),
			Comment:   str(get(row, "comment", "COMMENT", "TABLE_COMMENT")),
			CreatedAt: str(get(row, "created_at", "CREATED_AT")),
			UpdatedAt: str(get(row, "updated_at", "UPDATED_AT")),
		})
	}
	writeJSON(w, map[string]any{"overview": out})
}

// handleTableImport POST {id, database, table, csv} -> 解析 CSV(首行=列名) 批量 INSERT
// 全部行在一个事务内执行, 任一行失败整体回滚。
func (h *Handlers) handleTableImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var body struct {
		ID       string `json:"id"`
		Database string `json:"database"`
		Table    string `json:"table"`
		CSV      string `json:"csv"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, "invalid body", http.StatusBadRequest)
		return
	}
	if !reConnID.MatchString(body.ID) || !reDBName.MatchString(body.Database) || !reTableName.MatchString(body.Table) {
		writeErr(w, "id/database/table 格式非法", http.StatusBadRequest)
		return
	}
	lines := strings.Split(strings.ReplaceAll(body.CSV, "\r\n", "\n"), "\n")
	lines = slices.DeleteFunc(lines, func(l string) bool { return strings.TrimSpace(l) == "" })
	if len(lines) < 2 {
		writeErr(w, "CSV 至少需要表头行 + 一行数据", http.StatusBadRequest)
		return
	}
	header := strings.Split(lines[0], ",")
	cols := make([]string, 0, len(header))
	for _, h2 := range header {
		name := strings.TrimSpace(h2)
		if strings.ContainsAny(name, "`'\";()-") || name == "" {
			writeErr(w, "列名含非法字符: "+name, http.StatusBadRequest)
			return
		}
		cols = append(cols, name)
	}
	quoted := make([]string, len(cols))
	for i, c := range cols {
		quoted[i] = "`" + c + "`"
	}
	prefix := "INSERT INTO `" + body.Database + "`.`" + body.Table + "` (" + strings.Join(quoted, ", ") + ") VALUES ("

	db, _, err := h.pool.Acquire(body.ID)
	if err != nil {
		writeErr(w, "获取连接失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer h.pool.Release(body.ID)

	// 事务: 优先走驱动事务接口(StatementExecer+Commit/Rollback), 不可用则逐行 Exec
	var tx gonavidb.TransactionExecer
	if tp, ok := db.(gonavidb.TransactionExecerProvider); ok {
		if t, terr := tp.OpenTransactionExecer(r.Context()); terr == nil {
			tx = t
			defer func() { _ = tx.Rollback() }() // Commit 成功后 Rollback 为 no-op
		}
	}
	execOne := func(query string) error {
		if tx != nil {
			_, err := tx.Exec(query)
			return err
		}
		_, err := db.Exec(query)
		return err
	}
	imported := 0
	for _, line := range lines[1:] {
		vals := strings.Split(line, ",")
		if len(vals) != len(cols) {
			writeErr(w, fmt.Sprintf("第 %d 行列数(%d)与表头(%d)不一致", imported+2, len(vals), len(cols)), http.StatusBadRequest)
			return
		}
		lits := make([]string, len(vals))
		for i, v := range vals {
			v = strings.TrimSpace(v)
			if v == "NULL" {
				lits[i] = "NULL"
			} else {
				lits[i] = "'" + strings.ReplaceAll(v, "'", "''") + "'"
			}
		}
		if err := execOne(prefix + strings.Join(lits, ", ") + ")"); err != nil {
			writeErr(w, fmt.Sprintf("第 %d 行写入失败: %v", imported+2, err), http.StatusInternalServerError)
			return
		}
		imported++
	}
	if tx != nil {
		if err := tx.Commit(); err != nil {
			writeErr(w, "提交事务失败: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	writeJSON(w, map[string]any{"ok": true, "imported": imported})
}

func (h *Handlers) handleFkGraph(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	q := r.URL.Query()
	id, database := q.Get("id"), q.Get("database")
	if !reConnID.MatchString(id) || !reDBName.MatchString(database) {
		writeErr(w, "id/database 格式非法", http.StatusBadRequest)
		return
	}
	db, conn, err := h.pool.Acquire(id)
	if err != nil {
		writeErr(w, "获取连接失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer h.pool.Release(id)

	var tablesSql, edgesSql string
	esc := func(s string) string { return strings.ReplaceAll(s, "'", "''") }
	switch strings.ToLower(string(conn.Info.Engine)) {
	case "mysql", "mariadb", "goldendb":
		tablesSql = "SELECT table_name AS n FROM information_schema.TABLES WHERE table_schema = '" + esc(database) + "' ORDER BY table_name"
		edgesSql = "SELECT TABLE_NAME AS ftable, COLUMN_NAME AS fcol, REFERENCED_TABLE_NAME AS ttable, REFERENCED_COLUMN_NAME AS tcol " +
			"FROM information_schema.KEY_COLUMN_USAGE WHERE TABLE_SCHEMA = '" + esc(database) + "' AND REFERENCED_TABLE_NAME IS NOT NULL"
	case "postgres", "opengauss", "kingbase", "highgo", "vastbase", "gaussdb":
		tablesSql = "SELECT tablename AS n FROM pg_catalog.pg_tables WHERE schemaname = '" + esc(database) + "' ORDER BY tablename"
		edgesSql = `SELECT ns.nspname || '.' || c.relname AS ftable, a.attname AS fcol,
			confrelid::regclass::text AS ttable, af.attname AS tcol
			FROM pg_constraint k
			JOIN pg_class c ON c.oid = k.conrelid
			JOIN pg_namespace ns ON ns.oid = c.relnamespace
			JOIN unnest(k.conkey) WITH ORDINALITY AS u(attnum, ord) ON true
			JOIN pg_attribute a ON a.attrelid = k.conrelid AND a.attnum = u.attnum
			JOIN unnest(k.confkey) WITH ORDINALITY AS uf(attnum, ord) ON uf.ord = u.ord
			JOIN pg_attribute af ON af.attrelid = k.confrelid AND af.attnum = uf.attnum
			WHERE k.contype = 'f' AND ns.nspname = '` + esc(database) + `'`
	default:
		writeErr(w, "该引擎暂不支持关系图", http.StatusBadRequest)
		return
	}
	parse := func(rows []map[string]any, keys ...string) []string {
		out := make([]string, 0, len(rows))
		for _, row := range rows {
			for _, k := range keys {
				switch v := row[k].(type) {
				case string:
					if v != "" {
						out = append(out, v)
						break
					}
				case []byte:
					if string(v) != "" {
						out = append(out, string(v))
						break
					}
				}
			}
		}
		return out
	}
	tableRows, _, err := syncpkg.QueryRows(r.Context(), db, tablesSql)
	if err != nil {
		writeErr(w, "读取表清单失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	tables := parse(tableRows, "n")
	edgeRows, _, err := syncpkg.QueryRows(r.Context(), db, edgesSql)
	if err != nil {
		writeErr(w, "读取外键失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	type Edge struct {
		FromTable  string `json:"fromTable"`
		FromColumn string `json:"fromColumn"`
		ToTable    string `json:"toTable"`
		ToColumn   string `json:"toColumn"`
	}
	edges := make([]Edge, 0, len(edgeRows))
	for _, row := range edgeRows {
		get := func(keys ...string) string {
			// MySQL 驱动返回信息库列名为大写(FTABLE 等), 同时兼容大小写
			for _, k := range keys {
				switch v := row[k].(type) {
				case string:
					if v != "" {
						return v
					}
				case []byte:
					if string(v) != "" {
						return string(v)
					}
				}
			}
			return ""
		}
		ft, fc, tt, tc := get("ftable", "FTABLE"), get("fcol", "FCOL"), get("ttable", "TTABLE"), get("tcol", "TCOL")
		// PG 的 ttable 是 regclass 文本(可能带 schema.), 归一为裸表名与 tables 对齐
		if i := strings.LastIndex(tt, "."); i >= 0 {
			tt = tt[i+1:]
		}
		if ft != "" && tt != "" {
			edges = append(edges, Edge{FromTable: ft, FromColumn: fc, ToTable: tt, ToColumn: tc})
		}
	}
	writeJSON(w, map[string]any{"tables": tables, "edges": edges})
}

func (h *Handlers) handleTableStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	id := r.URL.Query().Get("id")
	database := r.URL.Query().Get("database")
	table := r.URL.Query().Get("table")
	if !reConnID.MatchString(id) || !reDBName.MatchString(database) || !reTableName.MatchString(table) {
		writeErr(w, "id/database/table 格式非法", http.StatusBadRequest)
		return
	}

	db, _, err := h.pool.Acquire(id)
	if err != nil {
		writeErr(w, "获取连接失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer h.pool.Release(id)

	conn, err := h.store.Get(id)
	if err != nil {
		writeErr(w, "连接不存在", http.StatusNotFound)
		return
	}

	var cols []string
	var rows []map[string]interface{}

	switch conn.Info.Engine {
	case EngineMySQL, EngineMariaDB, EngineGoldendb:
		query := fmt.Sprintf(`
			SELECT TABLE_NAME, TABLE_TYPE, ENGINE, VERSION, ROW_FORMAT,
			       TABLE_ROWS, AVG_ROW_LENGTH, DATA_LENGTH, INDEX_LENGTH,
			       DATA_FREE, AUTO_INCREMENT, CREATE_TIME, UPDATE_TIME,
			       CHECK_TIME, TABLE_COLLATION, CHECKSUM, CREATE_OPTIONS, TABLE_COMMENT
			FROM information_schema.TABLES
			WHERE TABLE_SCHEMA = '%s' AND TABLE_NAME = '%s'`,
			strings.ReplaceAll(database, "'", "''"), strings.ReplaceAll(table, "'", "''"))
		data, colNames, err := db.Query(query)
		if err != nil {
			writeErr(w, "查询表属性失败: "+err.Error(), http.StatusInternalServerError)
			return
		}
		cols = colNames
		for _, row := range data {
			item := make(map[string]interface{})
			for _, c := range colNames {
				item[c] = row[c]
			}
			rows = append(rows, item)
		}
	case EnginePostgreSQL, EngineOpenGauss, EngineGaussDB, EngineKingbase, EngineHighgo, EngineVastbase:
		query := fmt.Sprintf(`
			SELECT relname AS table_name, 'BASE TABLE' AS table_type,
			       pg_size_pretty(pg_total_relation_size(quote_ident('%s')::regclass)) AS total_size,
			       pg_total_relation_size(quote_ident('%s')::regclass) AS total_bytes,
			       n_live_tup AS table_rows,
			       pg_size_pretty(pg_relation_size(quote_ident('%s')::regclass)) AS data_size,
			       pg_size_pretty(pg_indexes_size(quote_ident('%s')::regclass)) AS index_size
			FROM pg_stat_user_tables
			WHERE schemaname = '%s' AND relname = '%s'`,
			strings.ReplaceAll(table, "'", "''"), strings.ReplaceAll(table, "'", "''"),
			strings.ReplaceAll(table, "'", "''"), strings.ReplaceAll(table, "'", "''"),
			strings.ReplaceAll(database, "'", "''"), strings.ReplaceAll(table, "'", "''"))
		data, colNames, err := db.Query(query)
		if err != nil {
			writeErr(w, "查询表属性失败: "+err.Error(), http.StatusInternalServerError)
			return
		}
		cols = colNames
		for _, row := range data {
			item := make(map[string]interface{})
			for _, c := range colNames {
				item[c] = row[c]
			}
			rows = append(rows, item)
		}
	default:
		writeJSON(w, map[string]any{
			"engine":  conn.Info.Engine,
			"note":    fmt.Sprintf("%s 引擎暂不支持表属性查询", conn.Info.Engine),
			"columns": []string{"name", "value"},
			"rows": []any{
				map[string]any{"name": "引擎", "value": conn.Info.Engine},
				map[string]any{"name": "数据库", "value": database},
				map[string]any{"name": "表", "value": table},
			},
		})
		return
	}

	writeJSON(w, map[string]any{
		"engine":  conn.Info.Engine,
		"columns": cols,
		"rows":    rows,
	})
}

// ===== /api/dbmanager/explain =====
// POST {id, sql, format?} -> 执行 EXPLAIN 并返回原始结果
//   format: json (默认) | table | text

func (h *Handlers) handleExplain(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var body struct {
		ID     string `json:"id"`
		SQL    string `json:"sql"`
		Format string `json:"format"` // json | table | text
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, "invalid body", http.StatusBadRequest)
		return
	}
	if !reConnID.MatchString(body.ID) {
		writeErr(w, "id 格式非法", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(body.SQL) == "" {
		writeErr(w, "sql 不能为空", http.StatusBadRequest)
		return
	}

	conn, err := h.store.Get(body.ID)
	if err != nil {
		writeErr(w, err.Error(), http.StatusNotFound)
		return
	}

	db, _, err := h.pool.Acquire(body.ID)
	if err != nil {
		writeErr(w, "获取连接失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer h.pool.Release(body.ID)

	format := body.Format
	if format == "" {
		format = "json"
	}

	explainSQL := fmt.Sprintf("EXPLAIN FORMAT=%s %s", format, body.SQL)
	rows, cols, err := db.Query(explainSQL)
	if err != nil {
		writeErr(w, "EXPLAIN 执行失败: "+err.Error(), http.StatusInternalServerError)
		return
	}

	outRows := make([]map[string]interface{}, 0, len(rows))
	for _, row := range rows {
		item := make(map[string]interface{})
		for _, c := range cols {
			item[c] = row[c]
		}
		outRows = append(outRows, item)
	}

	writeJSON(w, map[string]any{
		"engine":  conn.Info.Engine,
		"sql":     body.SQL,
		"format":  format,
		"columns": cols,
		"rows":    outRows,
	})
}

// ===== /api/dbmanager/queries =====
// GET -> 列出全部保存的查询
func (h *Handlers) handleQueries(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	list, err := h.store.ListSavedQueries()
	if err != nil {
		writeErr(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, list)
}

// ===== /api/dbmanager/queries/save =====
// POST {name, sql, engine?, connId?} -> 保存查询
func (h *Handlers) handleSaveQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var body SavedQuery
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, "invalid body", http.StatusBadRequest)
		return
	}
	q, err := h.store.SaveQuery(body)
	if err != nil {
		writeErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, q)
}

// ===== /api/dbmanager/queries/delete =====
// POST {id} -> 删除查询
func (h *Handlers) handleDeleteQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, "invalid body", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(body.ID) == "" {
		writeErr(w, "id 不能为空", http.StatusBadRequest)
		return
	}
	if err := h.store.DeleteSavedQuery(body.ID); err != nil {
		writeErr(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// ===== /api/dbmanager/drivers/install =====
// POST {type: engineType} -> 触发可选驱动安装
func (h *Handlers) handleDriverInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var body struct {
		Type string `json:"type"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, "invalid body", http.StatusBadRequest)
		return
	}
	if !engineTypeSupported(EngineType(body.Type)) {
		writeErr(w, "不支持的引擎类型", http.StatusBadRequest)
		return
	}
	// 可选驱动实现已随主二进制编译: 安装=写 installed.json 标记(真实生效, 非占位)
	marker, err := gonavistatus.InstallOptionalGoDriverMarker(body.Type, "")
	if err != nil {
		writeErr(w, "驱动安装失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "type": body.Type, "marker": marker})
}
