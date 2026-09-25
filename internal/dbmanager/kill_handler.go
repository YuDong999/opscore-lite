package dbmanager

// kill-session: 进程列表面板的「终止会话」。契约 POST {id, pid, confirm}
//   - confirm=false → 只回将执行的语句(预览), 不执行;
//   - confirm=true  → 走 interceptWrite 安全链 + 审计后执行。
//
// 为什么不让前端拼 KILL: 与 apply-* 同一规矩 —— 前端只报"哪个连接、哪个 pid",
// 语句由后端按引擎生成; pid 只接受正整数, 且**自我连接一律拒绝**(否则会把面板自己那条连接杀掉):
// MySQL 先用 information_schema 反查并排除 CONNECTION_ID(), PG 的语句自带 pid <> pg_backend_pid()。
// 注意 PG 这条是"SELECT 语义的写操作", classifySQLRisk 认不出(它按首词判为 safe),
// 所以这里显式按高风险处理, 不交给分类器。

import (
	"encoding/json"
	"net/http"
	"strconv"

	"opscore/internal/dbmanager/sync"
)

type killSessionBody struct {
	ID      string `json:"id"`
	PID     int64  `json:"pid"`
	Confirm bool   `json:"confirm"`
}

func (h *Handlers) handleKillSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var body killSessionBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, "invalid body", http.StatusBadRequest)
		return
	}
	if body.PID <= 0 {
		writeErr(w, "pid 非法", http.StatusBadRequest)
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
	pid := strconv.FormatInt(body.PID, 10)

	var checkSQL, killSQL string
	switch dialect {
	case sync.DialectMySQL:
		checkSQL = "SELECT ID FROM information_schema.PROCESSLIST WHERE ID = " + pid + " AND ID <> CONNECTION_ID()"
		killSQL = "KILL CONNECTION " + pid
	case sync.DialectPostgres:
		checkSQL = "SELECT pid FROM pg_stat_activity WHERE pid = " + pid + " AND pid <> pg_backend_pid()"
		killSQL = "SELECT pg_terminate_backend(" + pid + ") FROM pg_stat_activity WHERE pid = " + pid + " AND pid <> pg_backend_pid()"
	default:
		writeErr(w, "该引擎暂不支持终止会话", http.StatusBadRequest)
		return
	}

	rows, _, qerr := db.Query(checkSQL)
	if qerr != nil {
		writeErr(w, "检查会话失败: "+qerr.Error(), http.StatusBadRequest)
		return
	}
	if len(rows) == 0 {
		writeErr(w, "该会话不存在或不能终止本连接自身", http.StatusBadRequest)
		return
	}

	if !body.Confirm {
		writeJSON(w, map[string]any{"ok": true, "sql": killSQL, "needsConfirm": true, "affected": 0})
		return
	}

	risk, reason := RiskHigh, "终止数据库会话"
	if h.interceptWrite(w, conn, body.ID, killSQL, risk, reason, true) {
		return
	}

	affected, kerr := db.Exec(killSQL)

	decision, detail := "executed", reason
	if kerr != nil {
		decision, detail = "failed", kerr.Error()
	}
	h.audit.Append(AuditEntry{
		ConnID:   body.ID,
		ConnName: conn.Info.Name,
		Engine:   string(conn.Info.Engine),
		SQL:      killSQL,
		Risk:     string(risk),
		Decision: decision,
		Detail:   detail,
	})

	if kerr != nil {
		writeJSON(w, map[string]any{"ok": false, "sql": killSQL, "affected": 0, "error": kerr.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "sql": killSQL, "affected": affected})
}
