package dbmanager

// 手动事务(编辑器"事务模式"): 一条物理连接上的事务跨多次 HTTP 请求活着,
// 用户逐条执行、最后自己决定 COMMIT 还是 ROLLBACK。
//
//	POST /api/dbmanager/tx/begin     {id, database}          → txId
//	POST /api/dbmanager/tx/execute   {txId, sql, confirm}    → 逐条结果(事务内可 SELECT)
//	POST /api/dbmanager/tx/commit    {txId, confirm}
//	POST /api/dbmanager/tx/rollback  {txId}
//	GET  /api/dbmanager/tx/status?txId=                       → 已执行列表 + 空闲剩余秒
//
// 交互口径照 dbx(我们的基准): 切换开关不建事务, **首次执行才 begin**;
// 空闲 5 分钟自动**回滚**(不是提交); 一条语句失败就把整笔回滚掉。
//
// 三处刻意比 dbx 严:
//  1. 每条语句照样过 classifyBatchRisk + 写锁 + 审计 —— 事务不是绕过护栏的后门;
//  2. MySQL 系的 **DDL 一律拒绝**在手动事务里执行: 它隐式提交, 答应"待会儿能回滚"是骗人;
//     要改结构走结构对比 / apply-ddl 那些明确的路径。
//  3. 过期回滚**不静默重试**: dbx 前端会偷偷重新 begin 再跑一次, 用户看到的是"我点了三次
//     执行", 实际库上发生过什么他已经不知道了。这里直接把"已过期回滚"报回去。

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	gonaviDB "opscore/internal/dbmanager/gonavi/db"
	syncpkg "opscore/internal/dbmanager/sync"
)

const (
	txIdleTimeout   = 5 * time.Minute
	txSweepInterval = 30 * time.Second
	txMaxStmtSQL    = 2000 // 摘要里每条语句留多少字
	txMaxStmts      = 500  // 一个事务最多记多少条(再多就该怀疑是不是在跑批)
	txMaxOpen       = 8    // 同时最多开几个事务: 每个都占着一条物理连接, 不设上限等于能自己把连接池饿死
)

type txStatement struct {
	Seq        int    `json:"seq"`
	SQL        string `json:"sql"`
	Type       string `json:"type"`
	Affected   int64  `json:"affected"`
	Rows       int    `json:"rows"`
	DurationMs int64  `json:"durationMs"`
	Error      string `json:"error,omitempty"`
}

// txSession 一个跨请求挂着的事务。txId 就是能力令牌(随机 16 字节), 没有它谁也别想提交别人的事务。
type txSession struct {
	ID       string
	ConnID   string
	Database string
	Engine   string
	Dialect  syncpkg.Dialect

	mu       sync.Mutex
	busy     bool
	tx       gonaviDB.TransactionExecer
	q        gonaviDB.StatementQueryExecer // 驱动不支持会话内查询时为 nil
	stmts    []txStatement
	affected int64
	lastAt   time.Time
	begunAt  time.Time
	done     bool
	endErr   string
}

func (s *txSession) snapshot() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	stmts := s.stmts
	if stmts == nil {
		stmts = []txStatement{}
	}
	return map[string]any{
		"txId": s.ID, "connId": s.ConnID, "database": s.Database, "engine": s.Engine,
		"statements": stmts, "affected": s.affected, "count": len(s.stmts),
		"idleSecLeft": int64(txIdleTimeout.Seconds() - time.Since(s.lastAt).Seconds()),
		"openSec":     int64(time.Since(s.begunAt).Seconds()),
		"finished":    s.done, "endError": s.endErr, "busy": s.busy,
	}
}

type txRegistry struct {
	mu   sync.Mutex
	byID map[string]*txSession
}

func newTxRegistry() *txRegistry { return &txRegistry{byID: map[string]*txSession{}} }

func (g *txRegistry) get(id string) *txSession {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.byID[id]
}

func (g *txRegistry) put(s *txSession) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.byID[s.ID] = s
}

func (g *txRegistry) drop(id string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.byID, id)
}

func (g *txRegistry) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.byID)
}

// reap 回收过期事务。返回被回收的会话, 由调用方去 Rollback(不能在持锁时做 IO)。
func (g *txRegistry) reap(now time.Time) []*txSession {
	g.mu.Lock()
	defer g.mu.Unlock()
	var dead []*txSession
	for id, s := range g.byID {
		s.mu.Lock()
		expired := now.Sub(s.lastAt) > txIdleTimeout
		if expired {
			s.done = true
			s.endErr = fmt.Sprintf("空闲超过 %d 秒, 已自动回滚", int(txIdleTimeout.Seconds()))
			dead = append(dead, s)
			delete(g.byID, id)
		}
		s.mu.Unlock()
	}
	return dead
}

// sweepLoop 定时回收。光靠"访问时顺便清"不够: 用户直接关掉页签, 事务就一直挂着,
// 在 PG 那边表现为 idle in transaction 挡住 vacuum / 撑住 xmin。
func (h *Handlers) sweepLoop() {
	tick := time.NewTicker(txSweepInterval)
	defer tick.Stop()
	for range tick.C {
		for _, s := range h.txns.reap(time.Now()) {
			_ = s.tx.Close() // 回滚并交还物理连接(见 rollbackLocked 的说明)
			h.audit.Append(AuditEntry{
				ConnID: s.ConnID, Engine: s.Engine, SQL: "ROLLBACK /* " + s.ID + " */",
				Risk: string(RiskMedium), Decision: "rolled_back",
				Detail: "手动事务空闲过期, 服务端自动回滚",
			})
		}
	}
}

func newTxID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("tx_%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

type txBeginBody struct {
	ID       string `json:"id"`
	Database string `json:"database"`
}

func (h *Handlers) handleTxBegin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var body txBeginBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, "invalid body", http.StatusBadRequest)
		return
	}
	conn, err := h.store.Get(body.ID)
	if err != nil {
		writeErr(w, "连接不存在: "+err.Error(), http.StatusBadRequest)
		return
	}
	engine := string(conn.Info.Engine)
	// 消息队列没有"事务"这个概念(投递即落 topic), 别让它落到下面那句泛化的"驱动没有事务接口"。
	if isMQEngine(engine) {
		writeErr(w, "消息队列没有事务: 发送消息是直接投递到 topic/queue, 用不到手动提交; 需要攒多条一起发请用「批量」", http.StatusBadRequest)
		return
	}
	if strings.ToLower(strings.TrimSpace(engine)) == "redis" {
		// Redis 服务端有 MULTI/EXEC, 但本版引擎没接 —— 说清楚"没接", 别用"该驱动没有事务接口"含糊过去
		writeErr(w, "Redis 事务(MULTI/EXEC)本版未实现, 请继续用自动提交; 需要一次改多个键可以用「批量」命令窗口逐条执行", http.StatusBadRequest)
		return
	}
	dialect := syncpkg.EngineDialect(engine)
	if dialect == "" {
		writeErr(w, "该引擎暂不支持手动事务(v1 覆盖 MySQL 系与 PG 系)", http.StatusBadRequest)
		return
	}
	db, _, err := h.pool.Acquire(body.ID)
	if err != nil {
		writeErr(w, "连接不可用: "+err.Error(), http.StatusBadRequest)
		return
	}
	tp, ok := db.(gonaviDB.TransactionExecerProvider)
	if !ok {
		writeErr(w, "该驱动没有事务接口, 只能继续用自动提交", http.StatusBadRequest)
		return
	}
	// 每个事务都独占一条物理连接(直到提交/回滚/过期), 所以要有上限:
	// 没有这道闸, 反复 begin 不放就能把连接池占干, 别的查询全排队。
	if h.txns.count() >= txMaxOpen {
		writeJSONStatus(w, http.StatusTooManyRequests, map[string]any{
			"ok": false, "code": "tx_too_many",
			"error": fmt.Sprintf("已有 %d 个事务挂着(上限 %d), 请先提交或回滚; 空闲满 %d 分钟的会自动回滚",
				h.txns.count(), txMaxOpen, int(txIdleTimeout.Minutes()))})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	tx, err := tp.OpenTransactionExecer(ctx)
	if err != nil {
		writeErr(w, "开启事务失败: "+err.Error(), http.StatusBadRequest)
		return
	}
	sess := &txSession{
		ID: newTxID(), ConnID: body.ID, Database: strings.TrimSpace(body.Database),
		Engine: engine, Dialect: dialect, tx: tx,
		q:      func() gonaviDB.StatementQueryExecer { q, _ := tx.(gonaviDB.StatementQueryExecer); return q }(),
		lastAt: time.Now(), begunAt: time.Now(),
	}
	h.txns.put(sess)
	out := sess.snapshot()
	out["ok"] = true
	out["idleTimeoutSec"] = int64(txIdleTimeout.Seconds())
	writeJSON(w, out)
}

type txExecBody struct {
	TxID    string `json:"txId"`
	SQL     string `json:"sql"`
	MaxRows int    `json:"maxRows"`
	Confirm bool   `json:"confirm"`
}

func (h *Handlers) handleTxExecute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var body txExecBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, "invalid body", http.StatusBadRequest)
		return
	}
	sess := h.txns.get(body.TxID)
	if sess == nil {
		writeJSONStatus(w, http.StatusNotFound, map[string]any{
			"ok": false, "code": "tx_gone", "error": "事务已不在服务端(已提交、已回滚或空闲过期), 里面的改动没有生效, 请重新开启事务"})
		return
	}
	if strings.TrimSpace(body.SQL) == "" {
		writeErr(w, "SQL 不能为空", http.StatusBadRequest)
		return
	}
	conn, err := h.store.Get(sess.ConnID)
	if err != nil {
		writeErr(w, "连接已不存在: "+err.Error(), http.StatusBadRequest)
		return
	}
	if body.MaxRows <= 0 {
		body.MaxRows = MaxQueryRows
	}

	// 同一事务串行执行: 两条语句并发进同一个事务, 谁先谁后决定结果, 而且回滚会互相打断。
	sess.mu.Lock()
	if sess.done {
		reason := sess.endErr
		sess.mu.Unlock()
		writeJSONStatus(w, http.StatusNotFound, map[string]any{
			"ok": false, "code": "tx_gone", "error": "事务已结束: " + reason})
		return
	}
	if sess.busy {
		sess.mu.Unlock()
		writeJSONStatus(w, http.StatusConflict, map[string]any{
			"ok": false, "code": "tx_busy", "error": "这个事务里还有上一条语句在跑, 等它结束再发"})
		return
	}
	sess.busy = true
	sess.lastAt = time.Now()
	sess.mu.Unlock()
	defer func() {
		sess.mu.Lock()
		sess.busy = false
		sess.mu.Unlock()
	}()

	// 分句 + 逐条判风险: 事务里同样不许"首条只读、后面藏写"蒙过护栏。
	stmts := splitStatementsForEngine(sess.Engine, body.SQL, sess.Dialect)
	risk, reason := classifyBatchRisk(sess.Engine, body.SQL)
	if risk.AtLeast(RiskMedium) {
		if blocked := h.interceptWrite(w, conn, sess.ConnID, body.SQL, risk, reason, body.Confirm); blocked {
			return
		}
	}
	// MySQL 系 DDL 隐式提交 → "回滚"是假的。宁可拒绝, 不给一个会骗人的按钮。
	if sess.Dialect == syncpkg.DialectMySQL {
		for _, st := range stmts {
			if ddlKeyword(st) {
				h.rollbackLocked(sess, "拒绝在手动事务里执行 DDL")
				writeErr(w, "MySQL 系的 DDL 会隐式提交, 放不进手动事务(该事务已回滚)——改结构请用「结构对比」或「执行 DDL」。",
					http.StatusBadRequest)
				return
			}
		}
	}

	results := make([]txStatement, 0, len(stmts))
	var lastCols []string
	var lastRows [][]any
	truncated := false
	for i, st := range stmts {
		start := time.Now()
		rec := txStatement{Seq: len(sess.stmts) + 1, SQL: clipSQL(st), Type: statementType(st)}
		if isReadOnlySQL(sess.Engine, st) {
			if sess.q == nil {
				h.failTx(w, sess, conn, st, "该驱动的事务句柄不支持查询, 请把 SELECT 放在事务外")
				return
			}
			rows, cols, qerr := sess.q.Query(st)
			rec.DurationMs = time.Since(start).Milliseconds()
			if qerr != nil {
				h.failTx(w, sess, conn, st, qerr.Error())
				return
			}
			rec.Rows = len(rows)
			lastCols = cols
			lastRows = nil
			for _, row := range rows {
				if len(lastRows) >= body.MaxRows {
					truncated = true
					break
				}
				vals := make([]any, len(cols))
				for j, c := range cols {
					vals[j] = row[c]
				}
				lastRows = append(lastRows, vals)
			}
		} else {
			aff, eerr := sess.tx.Exec(st)
			rec.DurationMs = time.Since(start).Milliseconds()
			if eerr != nil {
				h.failTx(w, sess, conn, st, eerr.Error())
				return
			}
			rec.Affected = aff
			sess.mu.Lock()
			sess.affected += aff
			sess.mu.Unlock()
		}
		_ = i
		results = append(results, rec)
	}

	sess.mu.Lock()
	sess.stmts = append(sess.stmts, results...)
	over := len(sess.stmts) > txMaxStmts
	if over {
		sess.stmts = sess.stmts[:txMaxStmts]
	}
	sess.mu.Unlock()

	out := sess.snapshot()
	out["ok"] = true
	out["batch"] = results
	out["columns"] = lastCols
	out["rows"] = orEmptyRows(lastRows)
	out["rowCount"] = len(lastRows)
	out["truncated"] = truncated
	if over {
		out["note"] = fmt.Sprintf("事务内语句数已达上限 %d, 摘要不再继续累积(事务仍然有效)", txMaxStmts)
	}
	writeJSON(w, out)
}

// failTx 一条语句失败 → 整笔回滚(dbx 同口径: 半截事务继续跑只会让结果更难推断)。
func (h *Handlers) failTx(w http.ResponseWriter, sess *txSession, conn *Connection, stmt, msg string) {
	h.rollbackLocked(sess, "语句失败, 整笔回滚")
	h.audit.Append(AuditEntry{
		ConnID: sess.ConnID, ConnName: conn.Info.Name, Engine: sess.Engine,
		SQL: clipSQL(stmt), Risk: string(RiskMedium), Decision: "failed",
		Detail: fmt.Sprintf("手动事务 %s 第 %d 条失败: %s; 整个事务已回滚", sess.ID, len(sess.stmts)+1, msg),
	})
	writeJSON(w, map[string]any{"ok": false, "rolledBack": true, "txId": sess.ID,
		"error": fmt.Sprintf("第 %d 条语句失败: %s —— 整个事务已回滚, 之前的改动没有生效", len(sess.stmts)+1, msg)})
}

func (h *Handlers) rollbackLocked(sess *txSession, why string) {
	sess.mu.Lock()
	if !sess.done {
		sess.done = true
		sess.endErr = why
	}
	sess.mu.Unlock()
	// Close 而不是 Rollback: Rollback 只结束事务, **不会**把 pin 住的那条物理连接还回池子。
	// 只调 Rollback 的话每开一个事务就漏一条连接, 几次之后 begin 直接卡在等连接上(实测)。
	_ = sess.tx.Close()
	h.txns.drop(sess.ID)
}

func ddlKeyword(stmt string) bool {
	t := strings.ToUpper(strings.TrimSpace(stripSQLComments(stmt)))
	for _, kw := range []string{"CREATE ", "ALTER ", "DROP ", "TRUNCATE ", "RENAME ", "CREATE_TABLE"} {
		if strings.HasPrefix(t, strings.TrimSpace(kw)+" ") || strings.HasPrefix(t, kw) {
			return true
		}
	}
	return false
}

func clipSQL(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > txMaxStmtSQL {
		return s[:txMaxStmtSQL] + "…"
	}
	return s
}

func orEmptyRows(rows [][]any) [][]any {
	if rows == nil {
		return [][]any{}
	}
	return rows
}

type txEndBody struct {
	TxID    string `json:"txId"`
	Confirm bool   `json:"confirm"`
}

func (h *Handlers) handleTxCommit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var body txEndBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, "invalid body", http.StatusBadRequest)
		return
	}
	sess := h.txns.get(body.TxID)
	if sess == nil {
		writeJSONStatus(w, http.StatusNotFound, map[string]any{
			"ok": false, "code": "tx_gone", "error": "事务不存在或已结束"})
		return
	}
	if !body.Confirm {
		// 提交是不可撤销的一步, 界面必须把"要提交 N 条、累计影响 M 行"摆出来再要一次确认。
		writeErr(w, "需要 confirm=true 才提交(提交后不可撤销)", http.StatusBadRequest)
		return
	}
	sess.mu.Lock()
	n, affected, stmts := len(sess.stmts), sess.affected, append([]txStatement(nil), sess.stmts...)
	sess.mu.Unlock()
	if n == 0 {
		// 一条都没执行就点提交: 回滚掉更干净(连接别带着空事务还池)
		h.rollbackLocked(sess, "空事务提交 → 回滚")
		writeJSON(w, map[string]any{"ok": true, "noop": true, "count": 0,
			"message": "这个事务里没有执行过任何语句, 已直接结束"})
		return
	}
	conn, cerr := h.store.Get(sess.ConnID)
	name := ""
	if cerr == nil {
		name = conn.Info.Name
	}
	if err := sess.tx.Commit(); err != nil {
		// 提交结果未知时 Close() 会把这条连接踢出池子(discard), 而不是带着没结论的事务还回去
		_ = sess.tx.Close()
		h.txns.drop(sess.ID)
		h.audit.Append(AuditEntry{
			ConnID: sess.ConnID, ConnName: name, Engine: sess.Engine,
			SQL: "COMMIT /* " + sess.ID + " */", Risk: string(RiskHigh), Decision: "failed",
			Detail: "提交失败: " + err.Error(),
		})
		writeJSON(w, map[string]any{"ok": false, "error": "提交失败: " + err.Error()})
		return
	}
	_ = sess.tx.Close() // 事务已结束, 这一步只是把 pin 住的连接交还池子
	h.txns.drop(sess.ID)
	h.audit.Append(AuditEntry{
		ConnID: sess.ConnID, ConnName: name, Engine: sess.Engine,
		SQL: "COMMIT /* " + sess.ID + " */", Risk: string(RiskHigh), Decision: "executed",
		Detail: fmt.Sprintf("手动事务提交: %d 条语句, 累计影响 %d 行; 语句: %s", n, affected, briefStmts(stmts)),
	})
	writeJSON(w, map[string]any{"ok": true, "count": n, "affected": affected})
}

func (h *Handlers) handleTxRollback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var body txEndBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, "invalid body", http.StatusBadRequest)
		return
	}
	sess := h.txns.get(body.TxID)
	if sess == nil {
		writeJSON(w, map[string]any{"ok": true, "noop": true, "message": "事务已经不存在了"})
		return
	}
	sess.mu.Lock()
	n := len(sess.stmts)
	sess.mu.Unlock()
	h.rollbackLocked(sess, "用户回滚")
	h.audit.Append(AuditEntry{
		ConnID: sess.ConnID, Engine: sess.Engine, SQL: "ROLLBACK /* " + sess.ID + " */",
		Risk: string(RiskMedium), Decision: "canceled",
		Detail: fmt.Sprintf("手动事务回滚, 丢弃 %d 条语句", n),
	})
	writeJSON(w, map[string]any{"ok": true, "discarded": n})
}

func (h *Handlers) handleTxStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	id := r.URL.Query().Get("txId")
	sess := h.txns.get(id)
	if sess == nil {
		writeJSON(w, map[string]any{"ok": true, "active": false})
		return
	}
	out := sess.snapshot()
	out["ok"] = true
	out["active"] = true
	writeJSON(w, out)
}

func briefStmts(stmts []txStatement) string {
	parts := make([]string, 0, 4)
	for i, s := range stmts {
		if i >= 4 {
			parts = append(parts, fmt.Sprintf("…共 %d 条", len(stmts)))
			break
		}
		t := s.Type
		if s.Error != "" {
			t += "(err)"
		}
		parts = append(parts, t)
	}
	return strings.Join(parts, ", ")
}
