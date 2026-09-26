package dbmanager

// apply-alter: 列级结构变更(加列/删列/改列)的语句由后端拼。
// 契约: POST {id, database, table, cols:[{kind:add|drop|modify, name, origName, type, nullable, default, comment}], confirm}
//   - confirm=false → 只回将执行的语句列表(预览), 不落库;
//   - confirm=true  → 走 interceptWrite 安全链后在一个事务里逐条执行。
//
// 为什么要有这个端点: 此前"表结构"编辑器在前端拼 ALTER 字符串(QUOTE_ID + 手搓 MODIFY/ALTER COLUMN),
// 与全站"写 SQL/DDL 一律后端拼"的口径不一致, 而且方言只分了两套、默认值直接裸拼 ——
// `DEFAULT now()` 这种能过, `DEFAULT 'a;b'` 就是语法层面的事故。
// 现在前端只说"哪一列变成什么样", 标识符引用、类型白名单、默认值/注释转义、方言分支都在后端。
//
// 语句生成本身住在 sync.BuildColumnAlters: 结构对比(schemadiff)要生成的是同一类语句,
// 两边共用一份才不会出现"编辑器改对了、对比那条路还是老规则"这种漂移。
//
// 一个诚实的限制: MySQL 系 DDL 会**隐式提交**, 所以整批里某条失败时, 前面的改动撤不回来
// —— 这不是"回滚失败"而是引擎能力。因此失败信息里明确说"前 N 条已生效", 不谎报 rolledBack。

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"opscore/internal/dbmanager/sync"
)

// 一次提交的列变更上限: 结构编辑器一次改几十列已经算多了, 再大就该走迁移工具。
const maxAlterCols = 100

type alterCol struct {
	Kind     string `json:"kind"` // add | drop | modify
	Name     string `json:"name"` // 目标列名(drop 时就是要删的列)
	OrigName string `json:"origName,omitempty"`
	Type     string `json:"type,omitempty"`
	Nullable bool   `json:"nullable"`
	Default  string `json:"default,omitempty"`
	Comment  string `json:"comment,omitempty"`
}

type applyAlterBody struct {
	ID       string     `json:"id"`
	Database string     `json:"database"`
	Table    string     `json:"table"`
	Cols     []alterCol `json:"cols"`
	Confirm  bool       `json:"confirm"`
}

// toSyncAlters 把请求体翻成"列变更意图", 交给共用的方言生成器。
func toSyncAlters(cols []alterCol) []sync.ColumnAlter {
	out := make([]sync.ColumnAlter, 0, len(cols))
	for _, c := range cols {
		out = append(out, sync.ColumnAlter{
			Kind: c.Kind, Name: c.Name, OrigName: c.OrigName, Type: c.Type,
			Nullable: c.Nullable, Default: c.Default, Comment: c.Comment,
		})
	}
	return out
}


func (h *Handlers) handleApplyAlter(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var body applyAlterBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, "invalid body", http.StatusBadRequest)
		return
	}
	if len(body.Cols) == 0 {
		writeErr(w, "cols 为空", http.StatusBadRequest)
		return
	}
	if len(body.Cols) > maxAlterCols {
		writeErr(w, fmt.Sprintf("一次最多 %d 条列变更", maxAlterCols), http.StatusBadRequest)
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
		writeErr(w, "该引擎暂不支持在线结构变更", http.StatusBadRequest)
		return
	}
	qi := func(n string) string { return sync.QuoteIdent(n, dialect) }
	tableRef := qi(database) + "." + qi(table)
	if tableSchema != "" {
		tableRef = qi(tableSchema) + "." + qi(table)
	}
	sqls, berr := sync.BuildColumnAlters(engine, dialect, tableRef, toSyncAlters(body.Cols))
	if berr != nil {
		writeErr(w, berr.Error(), http.StatusBadRequest)
		return
	}

	if !body.Confirm {
		writeJSON(w, map[string]any{"ok": true, "sqls": sqls, "needsConfirm": true})
		return
	}

	// 整批按最高风险过安全链(删列是 critical)
	risk, reason := RiskSafe, ""
	for _, s := range sqls {
		if rk, rs := classifySQLRisk(engine, s); rk.AtLeast(risk) && rk != risk {
			risk, reason = rk, rs
		}
	}
	joined := strings.Join(sqls, ";\n")
	if h.interceptWrite(w, conn, body.ID, joined, risk, reason, true) {
		return
	}

	total, failedAt, execErr := execAllInTx(r.Context(), db, sqls)
	if execErr != nil {
		// MySQL 系 DDL 隐式提交: 到失败点之前的语句已经落地, 说"已回滚"就是骗人
		rolledBack := failedAt == 0
		detail := execErr.Error()
		if failedAt > 0 {
			detail = fmt.Sprintf("第 %d 条失败: %v", failedAt, execErr)
			if sync.EngineDialect(string(conn.Info.Engine)) == sync.DialectMySQL && !errors.Is(execErr, errNoTxDriver) {
				detail += "（MySQL 系 DDL 会隐式提交, 前 " + fmt.Sprint(failedAt-1) + " 条已生效）"
			}
		}
		h.audit.Append(AuditEntry{
			ConnID: body.ID, ConnName: conn.Info.Name, Engine: string(conn.Info.Engine),
			SQL: joined, Risk: string(risk), Decision: "failed", Detail: detail,
		})
		writeJSON(w, map[string]any{"ok": false, "affected": 0, "failedAt": failedAt, "rolledBack": rolledBack, "error": detail})
		return
	}
	h.audit.Append(AuditEntry{
		ConnID: body.ID, ConnName: conn.Info.Name, Engine: string(conn.Info.Engine),
		SQL: joined, Risk: string(risk), Decision: "executed",
		Detail: fmt.Sprintf("%s（%d 条结构变更已在同一事务提交）", reason, len(sqls)),
	})
	writeJSON(w, map[string]any{"ok": true, "affected": total, "count": len(sqls)})
}
