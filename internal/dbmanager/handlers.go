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
	"regexp"
	"strings"
	"time"

	"github.com/tealeg/xlsx"
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
					{Path: "/api/dbmanager/describe", Handler: h.handleDescribe},
					{Path: "/api/dbmanager/write-unlock", Handler: h.handleWriteUnlock},
					{Path: "/api/dbmanager/write-lock", Handler: h.handleWriteLock},
				{Path: "/api/dbmanager/audit", Handler: h.handleAudit},
				{Path: "/api/dbmanager/engines", Handler: h.handleEngines},
				{Path: "/api/dbmanager/engine-config", Handler: h.handleEngineConfig},
				{Path: "/api/dbmanager/sync/plan", Handler: h.handleSyncPlan},
				{Path: "/api/dbmanager/sync/run", Handler: h.handleSyncRun},
				{Path: "/api/dbmanager/sync/status", Handler: h.handleSyncStatus},
				{Path: "/api/dbmanager/sync/jobs", Handler: h.handleSyncJobs},
				{Path: "/api/dbmanager/sync/cancel", Handler: h.handleSyncCancel},
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
			
			if !validConnConfig(body.Config) {
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
		if !validConnConfig(body.Config) {
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

func validConnConfig(c ConnectionConfig) bool {
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
		if !validConnConfig(body.Config) {
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
		ID      string `json:"id"`
		SQL     string `json:"sql"`
		MaxRows int    `json:"maxRows"`
		Confirm bool   `json:"confirm"`
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

	res, err := h.svc.ExecQuery(ctx, body.ID, body.SQL, body.MaxRows)
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
	default:
		writeErr(w, "type 必须是 databases 或 tables", http.StatusBadRequest)
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

	res, err := h.svc.ExecQuery(ctx, body.ID, body.SQL, body.MaxRows)
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
