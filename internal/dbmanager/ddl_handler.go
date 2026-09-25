package dbmanager

// apply-ddl: 表级破坏性 DDL(删表 / 清空表)落库(dbx sidebar danger dialog 同构)。
// 契约: POST {id, database, table, action: "drop"|"truncate", confirm}
//   - confirm=false → 只返回将执行的 DDL 预览, 不执行;
//   - confirm=true  → 走 interceptWrite 安全链后执行(弹窗就是那次二次确认)。
//
// 为什么必须有这个端点(不只是"架构统一"): 此前前端自己拼 `DROP TABLE x` 丢给 /query,
// 而 /query 对 critical 语句要求请求体里带 confirm —— 前端在弹窗里确认了却没传, 后端回
// 403 confirm_required, 前端只判 write_locked 于是照样弹"删除完成"(实测: 表还在, 提示成功)。
// 这里把"用户已在预览弹窗点确认"显式翻译成 confirmed=true, 顺带让标识符引用回到后端:
// 前端那份 bt()/qt() 只认 mysql 系反引号, 其余一律双引号, 撞上方言差异就是静默错句。

import (
	"encoding/json"
	"net/http"

	"opscore/internal/dbmanager/sync"
)

type applyDDLBody struct {
	ID       string `json:"id"`
	Database string `json:"database"`
	Table    string `json:"table"`  // 允许 "schema.table" 两段(三级引擎)
	Action   string `json:"action"` // drop | truncate
	Confirm  bool   `json:"confirm"`
}

// DDL 动词白名单: 只有这两个能从这里出去 —— 建表/改表各有自己的路径, 不在"破坏性表级操作"里。
func ddlVerbFor(action string) (string, bool) {
	switch action {
	case "drop":
		return "DROP TABLE", true
	case "truncate":
		return "TRUNCATE TABLE", true
	}
	return "", false
}

func (h *Handlers) handleApplyDDL(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var body applyDDLBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, "invalid body", http.StatusBadRequest)
		return
	}
	verb, ok := ddlVerbFor(body.Action)
	if !ok {
		writeErr(w, "action 只支持 drop/truncate", http.StatusBadRequest)
		return
	}
	database, table, tableSchema, ok := validateTableTarget(w, body.ID, body.Database, body.Table)
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
		writeErr(w, "该引擎暂不支持在线 DDL", http.StatusBadRequest)
		return
	}

	qi := func(n string) string { return sync.QuoteIdent(n, dialect) }
	tableRef := qi(database) + "." + qi(table)
	if tableSchema != "" {
		tableRef = qi(tableSchema) + "." + qi(table)
	}
	sqlText := verb + " " + tableRef

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
