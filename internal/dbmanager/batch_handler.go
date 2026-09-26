package dbmanager

// apply-batch: 数据网格"待提交变更"整批落库(一个事务里做, 对应 dbx 的 保存/回滚 那排按钮)。
// 契约: POST {id, database, table, pkCols[], ops:[
//         {kind:update, row{}, setCol, setValue} | {kind:delete, row{}} | {kind:insert, values{}}
//       ], confirm}
//   insert 的 values 只带用户填过的列(键=列名), 未给的列交给表默认值/AUTO_INCREMENT。
//   - confirm=false → 只回将执行的语句列表(预览), 不落库;
//   - confirm=true  → 走 interceptWrite 写安全链后, 在**一个事务**里逐条执行, 任一条失败整体回滚。
//
// 为什么单独一个端点: 逐条走 apply-edit/apply-delete 是 N 个独立事务, 中途失败会在表里留下
// "改了一半"的状态; 网格一次改多格/删多行时, 用户期望全成或全不动。
// 语句仍一律由后端拼(标识符白名单 + 方言引用 + 字面量转义), 前端只说"哪一行哪一列改成什么"。
//
// 事务获取顺序: ① 驱动事务接口(Oracle/达梦这类文本 BEGIN 非法的引擎)
//               ② 钉一条物理会话走文本 BEGIN/COMMIT(MySQL/PG 系)
//               ③ 两者都没有 → 直接拒绝(不做非原子写入)。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	gonaviDB "opscore/internal/dbmanager/gonavi/db"
	"opscore/internal/dbmanager/sync"
)

// 一次提交的语句上限: 再多就该走导入(CSV)那条路了, 别拿 HTTP 请求当批量通道。
const maxBatchOps = 500

type batchOp struct {
	Kind     string         `json:"kind"` // update | delete | insert
	Row      map[string]any `json:"row"`  // update/delete: 主键定位(必须带齐 pkCols 的每个值)
	SetCol   string         `json:"setCol,omitempty"`
	SetValue any            `json:"setValue,omitempty"`
	Values   map[string]any `json:"values,omitempty"` // insert: 用户填过的列(其余走表默认值)
}

type applyBatchBody struct {
	ID       string    `json:"id"`
	Database string    `json:"database"`
	Table    string    `json:"table"`
	PkCols   []string  `json:"pkCols"`
	Ops      []batchOp `json:"ops"`
	Confirm  bool      `json:"confirm"`
}

func (h *Handlers) handleApplyBatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var body applyBatchBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, "invalid body", http.StatusBadRequest)
		return
	}
	if len(body.Ops) == 0 {
		writeErr(w, "ops 为空", http.StatusBadRequest)
		return
	}
	if len(body.Ops) > maxBatchOps {
		writeErr(w, fmt.Sprintf("一次最多 %d 条变更", maxBatchOps), http.StatusBadRequest)
		return
	}
	needPK := false
	for _, op := range body.Ops {
		if op.Kind != "insert" {
			needPK = true
			break
		}
	}
	if needPK && len(body.PkCols) == 0 {
		writeErr(w, "无主键列, 无法唯一定位行, 拒绝批量写入", http.StatusBadRequest)
		return
	}
	for _, pk := range body.PkCols {
		if !validIdentifier(pk) {
			writeErr(w, "主键列名非法: "+pk, http.StatusBadRequest)
			return
		}
	}
	for i, op := range body.Ops {
		switch op.Kind {
		case "update":
			if !validIdentifier(op.SetCol) {
				writeErr(w, fmt.Sprintf("第 %d 条 setCol 标识符非法", i+1), http.StatusBadRequest)
				return
			}
		case "delete":
		case "insert":
			if len(op.Values) == 0 {
				writeErr(w, fmt.Sprintf("第 %d 条 insert 没有任何列值", i+1), http.StatusBadRequest)
				return
			}
			for col := range op.Values {
				if !validIdentifier(col) {
					writeErr(w, fmt.Sprintf("第 %d 条列名非法: %s", i+1, col), http.StatusBadRequest)
					return
				}
			}
			continue // insert 不需要主键定位
		default:
			writeErr(w, fmt.Sprintf("第 %d 条 kind 只支持 update/delete/insert", i+1), http.StatusBadRequest)
			return
		}
		for _, pk := range body.PkCols {
			if _, found := op.Row[pk]; !found {
				writeErr(w, fmt.Sprintf("第 %d 条缺少主键值: %s", i+1, pk), http.StatusBadRequest)
				return
			}
		}
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
		writeErr(w, "该引擎暂不支持在线编辑", http.StatusBadRequest)
		return
	}

	qi := func(n string) string { return sync.QuoteIdent(n, dialect) }
	tn := qi(database) + "." + qi(table)
	if tableSchema != "" {
		tn = qi(tableSchema) + "." + qi(table)
	}
	sqls := make([]string, 0, len(body.Ops))
	for _, op := range body.Ops {
		if op.Kind == "insert" {
			cols := make([]string, 0, len(op.Values))
			for col := range op.Values {
				cols = append(cols, col)
			}
			sort.Strings(cols)
			names := make([]string, len(cols))
			vals := make([]string, len(cols))
			for j, col := range cols {
				names[j] = qi(col)
				vals[j] = gonaviDB.FormatLiteralForDialect(engine, op.Values[col])
			}
			sqls = append(sqls, fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
				tn, strings.Join(names, ", "), strings.Join(vals, ", ")))
			continue
		}
		var conds []string
		for _, pk := range body.PkCols {
			conds = append(conds, fmt.Sprintf("%s = %s", qi(pk), gonaviDB.FormatLiteralForDialect(engine, op.Row[pk])))
		}
		where := strings.Join(conds, " AND ")
		if op.Kind == "update" {
			sqls = append(sqls, fmt.Sprintf("UPDATE %s SET %s = %s WHERE %s",
				tn, qi(op.SetCol), gonaviDB.FormatLiteralForDialect(engine, op.SetValue), where))
		} else {
			sqls = append(sqls, fmt.Sprintf("DELETE FROM %s WHERE %s", tn, where))
		}
	}

	if !body.Confirm {
		writeJSON(w, map[string]any{"ok": true, "sqls": sqls, "needsConfirm": true, "affected": 0})
		return
	}

	// 整批按最高风险过安全链: 只要有一条是破坏性语句(critical), 整批就按 critical 处理
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

	// 事务句柄与逐条执行统一走 execAllInTx(apply-alter 用同一套, 不再各写一份回滚逻辑)
	total, failedAt, execErr := execAllInTx(r.Context(), db, sqls)
	if execErr != nil {
		detail := fmt.Sprintf("第 %d 条失败, 整批已回滚: %v", failedAt, execErr)
		if errors.Is(execErr, errNoTxDriver) {
			detail = execErr.Error()
		}
		h.audit.Append(AuditEntry{
			ConnID: body.ID, ConnName: conn.Info.Name, Engine: string(conn.Info.Engine),
			SQL: joined, Risk: string(risk), Decision: "failed", Detail: detail,
		})
		writeJSON(w, map[string]any{"ok": false, "affected": 0, "failedAt": failedAt, "rolledBack": !errors.Is(execErr, errNoTxDriver), "error": detail})
		return
	}

	h.audit.Append(AuditEntry{
		ConnID: body.ID, ConnName: conn.Info.Name, Engine: string(conn.Info.Engine),
		SQL: joined, Risk: string(risk), Decision: "executed",
		Detail: fmt.Sprintf("%s（%d 条变更已在同一事务提交）", reason, len(sqls)),
	})
	writeJSON(w, map[string]any{"ok": true, "affected": total, "count": len(sqls)})
}

// execAllInTx 在一个事务里跑完整批语句, 任一条失败就整体回滚。
// 事务来源按优先级: ① 驱动自带事务接口(Oracle/达梦这类文本 BEGIN 非法的引擎)
//
//	② 钉一条会话走文本 BEGIN/COMMIT  ③ 两者都没有 → 返回 errNoTxDriver(绝不做非原子写入)。
//
// failedAt 是 1 起的失败序号(0 = 没有逐条失败)。
func execAllInTx(ctx context.Context, db any, sqls []string) (total int64, failedAt int, err error) {
	var tx gonaviDB.TransactionExecer
	if tp, ok := db.(gonaviDB.TransactionExecerProvider); ok {
		if t, terr := tp.OpenTransactionExecer(ctx); terr == nil {
			tx = t
			defer func() { _ = tx.Rollback() }() // Commit 成功后 Rollback 为 no-op
		}
	}
	var sess gonaviDB.StatementExecer
	if tx == nil {
		sp, ok := db.(gonaviDB.SessionExecerProvider)
		if !ok {
			return 0, 0, errNoTxDriver
		}
		s, serr := sp.OpenSessionExecer(ctx)
		if serr != nil {
			return 0, 0, fmt.Errorf("打开事务会话失败: %w", serr)
		}
		sess = s
		defer func() { _ = sess.Close() }()
		if _, berr := sess.Exec("BEGIN"); berr != nil {
			return 0, 0, fmt.Errorf("开启事务失败: %w", berr)
		}
	}
	execOne := func(q string) (int64, error) {
		if tx != nil {
			return tx.Exec(q)
		}
		return sess.Exec(q)
	}
	rollback := func() {
		if tx != nil {
			_ = tx.Rollback()
			return
		}
		if sess != nil {
			_, _ = sess.Exec("ROLLBACK")
		}
	}
	for i, q := range sqls {
		n, eerr := execOne(q)
		if eerr != nil {
			rollback()
			return total, i + 1, eerr
		}
		total += n
	}
	if tx != nil {
		if cerr := tx.Commit(); cerr != nil {
			return total, 0, fmt.Errorf("提交失败: %w", cerr)
		}
		return total, 0, nil
	}
	if _, cerr := sess.Exec("COMMIT"); cerr != nil {
		rollback()
		return total, 0, fmt.Errorf("提交失败: %w", cerr)
	}
	return total, 0, nil
}

// errNoTxDriver 单独一个错误值: 调用方要按"驱动能力缺失"提示换路径, 而不是当成执行失败。
var errNoTxDriver = errors.New("该驱动不支持事务, 已拒绝整批写入(避免改一半)")
