// 备份的 HTTP 端点(P1-7)。
//
// 三个动作刻意分开, 因为它们的语义完全不同:
//
//	POST /backup/plan    —— **先探路**: 会走哪条(原生/内置)、包含什么、不含什么。让人先看清楚再决定。
//	POST /backup/run     —— 真跑一次。
//	GET  /backup/list    —— 历史记录(含 Mode/Includes/Excludes)。
//
// "先探路"是刻意的设计: 备份是"你以为它完整、其实缺触发器"会出大事的操作, 所以让用户
// 在跑之前就能看到这份备份将包含/不包含什么, 而不是跑完才发现。
package dbmanager

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// backupPlanBody /backup/plan 与 /backup/run 的请求体。
type backupPlanBody struct {
	ConnID       string   `json:"connId"`
	Database     string   `json:"database"`
	Tables       []string `json:"tables,omitempty"`
	HostID       string   `json:"hostId,omitempty"`
	Dir          string   `json:"dir,omitempty"`
	Keep         int      `json:"keep,omitempty"`
	ForceBuiltin bool     `json:"forceBuiltin,omitempty"`
}

// ===== POST /api/dbmanager/backup/plan =====
// 探路: 不执行任何备份, 只回答"如果现在跑, 会走哪条路径、包含/不含什么、写到哪"。
func (h *Handlers) handleBackupPlan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var body backupPlanBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, "invalid body", http.StatusBadRequest)
		return
	}
	conn, err := h.store.Get(body.ConnID)
	if err != nil {
		writeErr(w, "连接不存在: "+err.Error(), http.StatusBadRequest)
		return
	}
	if !validBackupTarget(body.Database) {
		writeErr(w, "库名非法", http.StatusBadRequest)
		return
	}
	// 库名存在性校验: 探路的全部价值是"开跑前就看清"。若库名根本不存在(打错字/选错连接)
	// 却照样返回一份"看起来没问题"的计划, 用户要等到真跑失败才发现 —— 那探路就白探了。
	// 只在**确实列得出库列表**时才判(文件型等引擎列不出库列表): 列不出来就不拦,
	// 宁可漏判也不能错杀本来能用的路径。
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	names, lerr := h.svc.ListDatabases(ctx, body.ConnID)
	cancel()
	if lerr == nil && len(names) > 0 && !backupListHasName(names, body.Database) {
		writeErr(w, "库不存在: "+body.Database+"（该连接上可访问的库: "+strings.Join(capNames(names, 12), ", ")+"）", http.StatusBadRequest)
		return
	}
	engine := string(conn.Info.Engine)
	dir := strings.TrimSpace(body.Dir)
	if dir == "" {
		dir = backupDefaultDir
	}
	if !validBackupPath(dir) {
		writeErr(w, "目标目录非法(只允许常规字符的绝对路径)", http.StatusBadRequest)
		return
	}

	mode := BackupModeBuiltin
	tool, version := "", ""
	if !body.ForceBuiltin {
		if t, v, ok := detectNativeBackupTool(body.HostID, engine); ok {
			mode, tool, version = BackupModeNative, t, v
		}
	}
	if body.ForceBuiltin && nativeToolCandidates(engine) == nil {
		// 显式要求内置但该引擎本来就没有原生工具: 正常, 不报错
		_ = body.ForceBuiltin
	}
	keep := body.Keep
	if keep <= 0 {
		keep = backupDefaultKeep
	}

	// 包一层 {ok, plan}: 本模块的 /backup/run 是 {ok, record}、/backup/list 是 {records},
	// 探路也照同一形状 —— 前端就能统一按 r.ok 判成败, 不靠"字段在不在"猜。
	// (第一版直接吐 plan 对象, 前端按 {plan:...} 读 → 永远 undefined, 真机才发现。)
	writeJSON(w, map[string]any{
		"ok": true,
		"plan": map[string]any{
			"engine":      engine,
			"database":    body.Database,
			"hostId":      body.HostID,
			"mode":        mode,
			"tool":        strings.TrimSpace(tool + " " + version),
			"dir":         dir,
			"keep":        keep,
			"includes":    mode.IncludesFor(engine), // 必看: 这份备份包含什么
			"excludes":    mode.ExcludesFor(engine), // 必看: 不含什么(空 = 完整)
			"consistency": mode.ConsistencyFor(engine),
			// 没有原生工具且引擎也不是 MySQL/PG 时, 说清为什么只能走内置
			"nativeCandidates": nativeToolCandidates(engine),
		},
	})
}

// ===== POST /api/dbmanager/backup/run =====
func (h *Handlers) handleBackupRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var body backupPlanBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, "invalid body", http.StatusBadRequest)
		return
	}
	// 备份会读整库(重操作), 也需要写目标机文件 —— 属于写路径, 走同一条拦截链:
	// 只读锁下不给跑(否则"只读连接"上却产生了文件系统写入), 并进审计。
	conn, err := h.store.Get(body.ConnID)
	if err != nil {
		writeErr(w, "连接不存在: "+err.Error(), http.StatusBadRequest)
		return
	}
	auditText := "BACKUP " + body.Database
	if h.interceptWrite(w, conn, body.ConnID, auditText, RiskMedium,
		"数据库备份: 读取整库并写入备份文件", true) {
		return
	}

	rec := h.runBackup(r.Context(), BackupRequest{
		ConnID:       body.ConnID,
		Database:     body.Database,
		Tables:       body.Tables,
		HostID:       body.HostID,
		Dir:          body.Dir,
		Keep:         body.Keep,
		ForceBuiltin: body.ForceBuiltin,
	})
	h.audit.Append(AuditEntry{
		ConnID: body.ConnID, ConnName: conn.Info.Name, Engine: string(conn.Info.Engine),
		SQL:      auditText + " -> " + rec.FilePath,
		Risk:     string(RiskMedium),
		Decision: map[bool]string{true: "executed", false: "failed"}[rec.Status == "ok"],
		Detail:   string(rec.Mode) + "; " + rec.Error,
	})
	h.saveBackupRecord(rec)

	// 保留策略: 只清"同连接 + 同库"的旧备份, 不误删别人的
	keep := body.Keep
	if keep <= 0 {
		keep = backupDefaultKeep
	}
	pruned := h.pruneBackups(rec.ConnID, rec.Database, keep, body.HostID)

	writeJSON(w, map[string]any{"ok": rec.Status == "ok", "record": rec, "pruned": pruned})
}

// ===== GET /api/dbmanager/backup/list =====
func (h *Handlers) handleBackupList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	connID := strings.TrimSpace(r.URL.Query().Get("id"))
	all := h.loadBackupRecords()
	out := make([]BackupRecord, 0, len(all))
	for _, rec := range all {
		if connID != "" && rec.ConnID != connID {
			continue
		}
		out = append(out, rec)
	}
	// 新的在前
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	writeJSON(w, map[string]any{"records": out, "defaultDir": backupDefaultDir, "defaultKeep": backupDefaultKeep})
}

// backupListHasName 判断库名是否出现在可访问库列表里(大小写不敏感 —— MySQL 在
// 不区分大小写的文件系统上, 库名大小写与 SHOW DATABASES 的返回可能不一致)。
func backupListHasName(names []string, want string) bool {
	want = strings.TrimSpace(want)
	for _, n := range names {
		if strings.EqualFold(n, want) {
			return true
		}
	}
	return false
}

// capNames 截断过长的库名列表(错误信息里不该把几十个系统库全铺出来)。
func capNames(names []string, max int) []string {
	if len(names) <= max {
		return names
	}
	out := append([]string{}, names[:max]...)
	return append(out, "……")
}
