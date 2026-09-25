package dbmanager

// apply-edit / apply-delete: 数据网格的行级写落库(dbx dataGrid cell edit 同构)。
// 契约: POST {id, database, table, pkCols[], row{}, setCol, setValue, confirm}
//   - confirm=false → 只返回将执行的 SQL 预览(needsConfirm), 不落库;
//   - confirm=true  → 走 interceptWrite 安全链(防切库/高危确认/限时写解锁)后执行。
// 无主键列、主键值缺失、标识符非法一律拒绝 —— UPDATE/DELETE 必须能唯一定位到行。
// 前端只说"哪一行、哪一列、置成什么", SQL 一律在这里拼: 手搓 SQL 会绕过标识符引用与写安全链
// (列名叫 order/from/user 时裸拼必炸, 值也不会按方言转义)。
// 复用: sync.QuoteIdent(标识符) + gonaviDB.FormatLiteralForDialect(值字面量, 按方言转义)
//      + classifySQLRisk/interceptWrite(写安全链) + AuditLog(审计)。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	gonaviDB "opscore/internal/dbmanager/gonavi/db"
	"opscore/internal/dbmanager/sync"
)

type applyEditBody struct {
	ID       string         `json:"id"`
	Database string         `json:"database"`
	Table    string         `json:"table"` // 允许 "schema.table" 两段(PG 族, 与 /data 同规则)
	PkCols   []string       `json:"pkCols"`
	Row      map[string]any `json:"row"`
	SetCol   string         `json:"setCol"`
	SetValue any            `json:"setValue"`
	Confirm  bool           `json:"confirm"`
}

// 行级写与表级 DDL 共用的第一段: id / database / table 的白名单与拆解("schema.table" 两段)。
// 抽成一处是因为这是防注入的唯一入口, 各端点写一份迟早漂移。
// 校验失败时已写好响应, 返回 ok=false。
func validateTableTarget(w http.ResponseWriter, id, rawDatabase, rawTable string) (database, table, tableSchema string, ok bool) {
	if !reConnID.MatchString(id) {
		writeErr(w, "id 格式非法", http.StatusBadRequest)
		return "", "", "", false
	}
	database, table = rawDatabase, rawTable
	if !validIdentifier(database) {
		writeErr(w, "database 名非法", http.StatusBadRequest)
		return "", "", "", false
	}
	if i := strings.Index(table, "."); i >= 0 {
		tableSchema, table = table[:i], table[i+1:]
	}
	if !validIdentifier(table) || (tableSchema != "" && !validIdentifier(tableSchema)) {
		writeErr(w, "database/table 名非法", http.StatusBadRequest)
		return "", "", "", false
	}
	return database, table, tableSchema, true
}

// 行级写(edit/delete)在前一段之外还要求: 有主键列, 且这一行确实带着每个主键的值 ——
// 没有唯一定位能力就不许写(表级 DDL 不走这里)。
func validateRowWriteIdentifiers(w http.ResponseWriter, body applyEditBody) (database, table, tableSchema string, ok bool) {
	database, table, tableSchema, ok = validateTableTarget(w, body.ID, body.Database, body.Table)
	if !ok {
		return "", "", "", false
	}
	if len(body.PkCols) == 0 {
		writeErr(w, "无主键列, 无法唯一定位行, 拒绝编辑", http.StatusBadRequest)
		return "", "", "", false
	}
	for _, pk := range body.PkCols {
		if !validIdentifier(pk) {
			writeErr(w, "主键列名非法: "+pk, http.StatusBadRequest)
			return "", "", "", false
		}
		if _, found := body.Row[pk]; !found {
			writeErr(w, "行数据缺少主键值: "+pk, http.StatusBadRequest)
			return "", "", "", false
		}
	}
	return database, table, tableSchema, true
}

func (h *Handlers) handleApplyEdit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var body applyEditBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, "invalid body", http.StatusBadRequest)
		return
	}
	database, table, tableSchema, ok := validateRowWriteIdentifiers(w, body)
	if !ok {
		return
	}
	if !validIdentifier(body.SetCol) {
		writeErr(w, "setCol 标识符非法", http.StatusBadRequest)
		return
	}

	conn, err := h.store.Get(body.ID)
	if err != nil {
		writeErr(w, "连接不存在: "+err.Error(), http.StatusBadRequest)
		return
	}
	db, engine, err := h.pool.AcquireForSync(body.ID)
	if err != nil {
		writeErr(w, "连接不可用: "+err.Error(), http.StatusBadRequest)
		return
	}
	dialect := sync.EngineDialect(engine)
	if dialect == "" {
		writeErr(w, "该引擎暂不支持在线编辑", http.StatusBadRequest)
		return
	}

	// 拼 UPDATE: 标识符过 validIdentifier 白名单 + 方言引用; 值字面量按方言转义
	qi := func(n string) string { return sync.QuoteIdent(n, dialect) }
	tn := qi(database) + "." + qi(table)
	if tableSchema != "" {
		tn = qi(tableSchema) + "." + qi(table)
	}
	var conds []string
	for _, pk := range body.PkCols {
		conds = append(conds, fmt.Sprintf("%s = %s", qi(pk), gonaviDB.FormatLiteralForDialect(engine, body.Row[pk])))
	}
	sqlText := fmt.Sprintf("UPDATE %s SET %s = %s WHERE %s",
		tn, qi(body.SetCol), gonaviDB.FormatLiteralForDialect(engine, body.SetValue), strings.Join(conds, " AND "))

	if !body.Confirm {
		writeJSON(w, map[string]any{"ok": true, "sql": sqlText, "needsConfirm": true, "affected": 0})
		return
	}

	risk, reason := classifySQLRisk(engine, sqlText)
	if h.interceptWrite(w, conn, body.ID, sqlText, risk, reason, true) {
		return
	}

	// GoNavi Query/Exec 无 ctx 参数; 语句超时由连接配置 QueryTimeout(秒)在驱动层生效
	affected, err := db.Exec(sqlText)

	decision := "executed"
	detail := reason
	if err != nil {
		decision = "failed"
		detail = err.Error()
	}
	h.audit.Append(AuditEntry{
		ConnID:   body.ID,
		ConnName: conn.Info.Name,
		Engine:   string(conn.Info.Engine),
		SQL:      sqlText,
		Risk:     string(risk),
		Decision: decision,
		Detail:   detail,
	})

	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "sql": sqlText, "affected": 0, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "sql": sqlText, "affected": affected})
}

// apply-delete: 按主键删除一行。DELETE 没有 SET 子句, 其余与 apply-edit 同一条链
// (同样的标识符白名单/方言引用/字面量转义/写安全链/审计)。
// 尾段与 apply-edit 逐行相同是有意的: 那段是已上线验证过的执行+审计路径, 不抽公共函数去动它。
func (h *Handlers) handleApplyDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var body applyEditBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, "invalid body", http.StatusBadRequest)
		return
	}
	database, table, tableSchema, ok := validateRowWriteIdentifiers(w, body)
	if !ok {
		return
	}

	conn, err := h.store.Get(body.ID)
	if err != nil {
		writeErr(w, "连接不存在: "+err.Error(), http.StatusBadRequest)
		return
	}
	db, engine, err := h.pool.AcquireForSync(body.ID)
	if err != nil {
		writeErr(w, "连接不可用: "+err.Error(), http.StatusBadRequest)
		return
	}
	dialect := sync.EngineDialect(engine)
	if dialect == "" {
		writeErr(w, "该引擎暂不支持在线编辑", http.StatusBadRequest)
		return
	}

	// 拼 DELETE: WHERE 只由主键列构成, 条件为空集已被 validateRowWriteIdentifiers 挡掉
	qi := func(n string) string { return sync.QuoteIdent(n, dialect) }
	tn := qi(database) + "." + qi(table)
	if tableSchema != "" {
		tn = qi(tableSchema) + "." + qi(table)
	}
	var conds []string
	for _, pk := range body.PkCols {
		conds = append(conds, fmt.Sprintf("%s = %s", qi(pk), gonaviDB.FormatLiteralForDialect(engine, body.Row[pk])))
	}
	sqlText := fmt.Sprintf("DELETE FROM %s WHERE %s", tn, strings.Join(conds, " AND "))

	if !body.Confirm {
		writeJSON(w, map[string]any{"ok": true, "sql": sqlText, "needsConfirm": true, "affected": 0})
		return
	}

	risk, reason := classifySQLRisk(engine, sqlText)
	if h.interceptWrite(w, conn, body.ID, sqlText, risk, reason, true) {
		return
	}

	// GoNavi Query/Exec 无 ctx 参数; 语句超时由连接配置 QueryTimeout(秒)在驱动层生效
	affected, err := db.Exec(sqlText)

	decision := "executed"
	detail := reason
	if err != nil {
		decision = "failed"
		detail = err.Error()
	}
	h.audit.Append(AuditEntry{
		ConnID:   body.ID,
		ConnName: conn.Info.Name,
		Engine:   string(conn.Info.Engine),
		SQL:      sqlText,
		Risk:     string(risk),
		Decision: decision,
		Detail:   detail,
	})

	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "sql": sqlText, "affected": 0, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "sql": sqlText, "affected": affected})
}
