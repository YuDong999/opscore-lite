package dbmanager

// 数据对比(data diff)端点:
//
//	POST /api/dbmanager/data-diff          起比对任务(多表按同名配对)
//	GET  /api/dbmanager/data-diff/job      轮询进度与结果
//	POST /api/dbmanager/data-diff/cancel   取消
//	POST /api/dbmanager/data-diff/apply    按勾选的键重新读两侧 → 生成语句 → 执行
//
// 为什么比对要做成任务而不是一个请求: 大表要两遍扫描, 挂在一次 HTTP 里要么把人卡在转圈上,
// 要么被超时掐掉 —— 两种都拿不到结论, 而且"超时了到底比完没有"没人说得清。任务化之后有进度、
// 能取消, 撞到行数/差异数上限也能如实报 partial(dbx 那边 truncated 写死 false, 界面却写着
// "已完成全量比较")。
//
// apply 只收**键**: 既不收 SQL 也不收行内容。语句由这一刻从两侧重新读出来的行生成 ——
// 从看到差异到按下执行之间那一行可能被别人改过, 那就按新的算; 已经一致的键直接记为 stale 跳过,
// 不发一条空转的 UPDATE/DELETE。

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	gonaviDB "opscore/internal/dbmanager/gonavi/db"
	syncpkg "opscore/internal/dbmanager/sync"
)

const (
	maxDataDiffTables = 20   // 一次比 20 张以上该用同步任务, 不是"看一下"
	maxDataDiffRows   = 2000 // apply 单次最多处理的行数(每条语句一行)
	dataDiffJobTTL    = 30 * time.Minute
	dataDiffMaxJobs   = 32
)

type dataDiffEnd struct {
	ID       string `json:"id"`
	Database string `json:"database"`
	Schema   string `json:"schema,omitempty"` // PG 族的命名空间; MySQL 留空
}

func (e dataDiffEnd) qualified(t string) string {
	if e.Schema != "" {
		return e.Schema + "." + t
	}
	return t
}

func (e dataDiffEnd) namespace() string {
	if e.Schema != "" {
		return e.Schema
	}
	return e.Database
}

type dataDiffBody struct {
	Src           dataDiffEnd                `json:"src"`
	Dst           dataDiffEnd                `json:"dst"`
	Tables        []string                   `json:"tables"`
	KeyColumns    []string                   `json:"keyColumns,omitempty"` // 留空=自动取主键
	IgnoreColumns []string                   `json:"ignoreColumns,omitempty"`
	Options       syncpkg.DataCompareOptions `json:"options"`
}

// ── 任务注册表 ──

type dataDiffJob struct {
	ID         string `json:"id"`
	Body       dataDiffBody
	Status     string     `json:"status"` // running | done | failed | canceled
	Err        string     `json:"error,omitempty"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`

	mu       sync.Mutex
	current  string
	doneTabl int
	scannedS int64
	scannedD int64
	foundDif int
	tables   int
	results  []*syncpkg.DataTableDiff
	cancel   context.CancelFunc
}

func (j *dataDiffJob) snapshot() map[string]any {
	j.mu.Lock()
	defer j.mu.Unlock()
	res := j.results
	if res == nil {
		res = []*syncpkg.DataTableDiff{}
	}
	return map[string]any{
		"id": j.ID, "status": j.Status, "error": j.Err, "finished": j.FinishedAt != nil,
		"current": j.current, "tables": j.tables, "doneTables": j.doneTabl,
		"scannedSrc": j.scannedS, "scannedDst": j.scannedD, "diffs": j.foundDif,
		"results": res,
	}
}

func (j *dataDiffJob) progress(side string, scanned int64) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if side == "dst" {
		j.scannedD = scanned
	} else {
		j.scannedS = scanned
	}
}

func (j *dataDiffJob) addResult(d *syncpkg.DataTableDiff) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.results = append(j.results, d)
	j.doneTabl++
	j.foundDif += d.Summary.Total()
}

func (j *dataDiffJob) finish(status, msg string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	now := time.Now()
	j.Status, j.Err, j.FinishedAt = status, msg, &now
}

type dataDiffRegistry struct {
	mu   sync.Mutex
	jobs map[string]*dataDiffJob
}

func newDataDiffRegistry() *dataDiffRegistry {
	return &dataDiffRegistry{jobs: map[string]*dataDiffJob{}}
}

func (g *dataDiffRegistry) create(body dataDiffBody, tables int) *dataDiffJob {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	j := &dataDiffJob{
		ID:        fmt.Sprintf("ddiff_%d_%s", time.Now().Unix(), hex.EncodeToString(b)),
		Body:      body,
		Status:    "running",
		StartedAt: time.Now(),
		tables:    tables,
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	// 顺手清掉过期的: 比对结果只在"看完 → 点执行"这几分钟里有意义
	for id, old := range g.jobs {
		if old.FinishedAt != nil && time.Since(*old.FinishedAt) > dataDiffJobTTL {
			delete(g.jobs, id)
		}
	}
	if len(g.jobs) >= dataDiffMaxJobs {
		for id, old := range g.jobs {
			if old.FinishedAt != nil {
				delete(g.jobs, id)
				break
			}
		}
	}
	g.jobs[j.ID] = j
	return j
}

func (g *dataDiffRegistry) get(id string) *dataDiffJob {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.jobs[id]
}

func (g *dataDiffRegistry) cancel(id string) bool {
	j := g.get(id)
	if j == nil {
		return false
	}
	j.mu.Lock()
	cancel := j.cancel
	status := j.Status
	j.mu.Unlock()
	if status != "running" || cancel == nil {
		return false
	}
	cancel()
	return true
}

// ── 取数(实现 sync.DataFetch) ──

// diffRows 执行查询: 优先 context 版(可取消), 回退同步版 —— 与同步引擎同一套口径。
func diffRows(ctx context.Context, db gonaviDB.Database, sqlText string) ([]map[string]any, error) {
	if qc, ok := db.(gonaviDB.QueryContexter); ok {
		rows, _, err := qc.QueryContext(ctx, sqlText)
		return rows, err
	}
	rows, _, err := db.Query(sqlText)
	return rows, err
}

type dataSide struct {
	name string // "src" | "dst"
	db   gonaviDB.Database
	d    syncpkg.Dialect
	ref  string
	cols []syncpkg.DataGridColumn
	keys []syncpkg.DataGridColumn
}

func (s *dataSide) page(ctx context.Context, after []any, limit int) ([]map[string]any, error) {
	sqlText := syncpkg.DataPageSQL(s.ref, s.cols, s.keys, after, s.d, s.name, limit)
	return diffRows(ctx, s.db, sqlText)
}

func (s *dataSide) lookup(ctx context.Context, tuples [][]any) ([]map[string]any, error) {
	sqlText := syncpkg.DataLookupSQL(s.ref, s.cols, s.keys, tuples, s.d, s.name)
	if sqlText == "" {
		return nil, nil
	}
	return diffRows(ctx, s.db, sqlText)
}

func (s *dataSide) count(ctx context.Context) (int64, error) {
	rows, err := diffRows(ctx, s.db, syncpkg.DataCountSQL(s.ref))
	if err != nil || len(rows) == 0 {
		return 0, err
	}
	switch t := rows[0]["c"].(type) {
	case int64:
		return t, nil
	case float64:
		return int64(t), nil
	case nil:
		return 0, nil
	default:
		n, _ := strconv.ParseInt(fmt.Sprintf("%v", t), 10, 64)
		return n, nil
	}
}

type dataFetcher struct{ src, dst *dataSide }

func (f dataFetcher) Page(ctx context.Context, side string, after []any, limit int) ([]map[string]any, error) {
	return f.pick(side).page(ctx, after, limit)
}

func (f dataFetcher) Lookup(ctx context.Context, side string, tuples [][]any) ([]map[string]any, error) {
	return f.pick(side).lookup(ctx, tuples)
}

func (f dataFetcher) Count(ctx context.Context, side string) (int64, error) {
	return f.pick(side).count(ctx)
}

func (f dataFetcher) pick(side string) *dataSide {
	if side == "dst" {
		return f.dst
	}
	return f.src
}

// ── 两侧连接与单表规格 ──

type dataConn struct {
	end     dataDiffEnd
	db      gonaviDB.Database
	engine  string
	dialect syncpkg.Dialect
}

func (h *Handlers) openDataEnd(end dataDiffEnd) (*dataConn, error) {
	if end.ID == "" || strings.TrimSpace(end.Database) == "" {
		return nil, fmt.Errorf("缺少连接或库名")
	}
	if _, err := h.store.Get(end.ID); err != nil {
		return nil, fmt.Errorf("连接不存在: %w", err)
	}
	db, engine, err := h.pool.AcquireForSync(end.ID)
	if err != nil {
		return nil, fmt.Errorf("连接不可用: %w", err)
	}
	d := syncpkg.EngineDialect(engine)
	if d == "" {
		return nil, fmt.Errorf("该引擎暂不支持数据对比(v1 覆盖 MySQL 系与 PG 系)")
	}
	return &dataConn{end: end, db: db, engine: engine, dialect: d}, nil
}

func tableRef(c *dataConn, table string) string {
	qi := func(n string) string { return syncpkg.QuoteIdent(n, c.dialect) }
	ns := c.end.namespace()
	if ns == "" {
		return qi(table)
	}
	return qi(ns) + "." + qi(table)
}

// buildDataSide 读一张表两侧的列/索引元数据, 定键、配列, 生出两侧句柄。
// 结构与键的判定放在这里(而不是比对循环里), 于是比对与执行用的是同一份规格。
func buildDataSide(src, dst *dataConn, body dataDiffBody, table string) (*dataSide, *dataSide, string, []string, error) {
	srcCols, err := src.db.GetColumns(src.end.Database, src.end.qualified(table))
	if err != nil {
		return nil, nil, "", nil, fmt.Errorf("读基准侧列信息失败: %w", err)
	}
	dstCols, err := dst.db.GetColumns(dst.end.Database, dst.end.qualified(table))
	if err != nil {
		return nil, nil, "", nil, fmt.Errorf("读目标侧列信息失败: %w", err)
	}
	// 0 列 = 该侧没有这张表(驱动对不存在的表通常不报错只回空, 实测 PG)。
	// 不先卡这一步, 下面会把它比成"整表都要删/都要补"。
	if len(srcCols) == 0 {
		return nil, nil, "", nil, fmt.Errorf("基准侧没有表 %s", table)
	}
	if len(dstCols) == 0 {
		return nil, nil, "", nil, fmt.Errorf("目标侧没有表 %s —— 先用「结构对比」把表建出来再比数据", table)
	}

	keyNames, keySource := body.KeyColumns, "指定列"
	if len(keyNames) == 0 {
		srcIdx, ierr := src.db.GetIndexes(src.end.Database, src.end.qualified(table))
		if ierr != nil {
			return nil, nil, "", nil, fmt.Errorf("读基准侧索引失败: %w", ierr)
		}
		got, from, kerr := syncpkg.DetectDataKeys(srcCols, srcIdx)
		if kerr != nil {
			return nil, nil, "", nil, kerr
		}
		keyNames, keySource = got, from
	}
	cols, skipped, perr := syncpkg.PairDataColumns(srcCols, dstCols, keyNames, body.IgnoreColumns)
	if perr != nil {
		return nil, nil, "", nil, perr
	}
	var keys []syncpkg.DataGridColumn
	for _, c := range cols {
		if c.IsKey {
			keys = append(keys, c)
		}
	}
	mk := func(c *dataConn, side string) *dataSide {
		return &dataSide{name: side, db: c.db, d: c.dialect, ref: tableRef(c, table), cols: cols, keys: keys}
	}
	return mk(src, "src"), mk(dst, "dst"), keySource, skipped, nil
}

// compareOneTable 比一张表。返回的对象永远字段齐全(前端不必为错误分支兜 nil 切片)。
func (h *Handlers) compareOneTable(ctx context.Context, job *dataDiffJob, src, dst *dataConn, table string) *syncpkg.DataTableDiff {
	s, d, keySource, skipped, err := buildDataSide(src, dst, job.Body, table)
	if err != nil {
		return syncpkg.ErrorDiff(table, err.Error())
	}
	spec := &syncpkg.DataTableSpec{
		Table: table, DstRef: d.ref, DstD: d.d, Columns: s.cols, Options: job.Body.Options,
		OnProgress: func(side string, n int64) { job.progress(side, n) },
	}
	out, cerr := syncpkg.CompareTableData(ctx, spec, dataFetcher{src: s, dst: d})
	if cerr != nil {
		dd := syncpkg.ErrorDiff(table, cerr.Error())
		dd.KeyColumns = keyNamesOf(s.keys)
		if ctx.Err() != nil {
			dd.Error = "已取消: " + dd.Error
		}
		return dd
	}
	out.KeySource = keySource
	out.Skipped = append(out.Skipped, skipped...)
	if out.SrcRows != out.DstRows {
		out.Notes = append(out.Notes, fmt.Sprintf("行数: 基准 %d / 目标 %d", out.SrcRows, out.DstRows))
	}
	out.Notes = append(out.Notes, "时间列按墙上时钟比较(丢掉时区), 同一时刻不同时区的差异看不出来")
	for _, c := range s.cols {
		if c.Generated {
			out.Notes = append(out.Notes, "生成列 "+c.Name+" 只比不写")
			break
		}
	}
	return out
}

func keyNamesOf(keys []syncpkg.DataGridColumn) []string {
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k.Name)
	}
	return out
}

func (h *Handlers) runDataDiff(job *dataDiffJob) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	job.mu.Lock()
	job.cancel = cancel
	job.mu.Unlock()
	defer cancel()

	src, err := h.openDataEnd(job.Body.Src)
	if err != nil {
		job.finish("failed", "基准侧: "+err.Error())
		return
	}
	dst, err := h.openDataEnd(job.Body.Dst)
	if err != nil {
		job.finish("failed", "目标侧: "+err.Error())
		return
	}
	// 单表比不了(没主键 / 某一侧缺表)只让那张表进 error 状态, 其余表继续 —— 与 dbx 一致:
	// 一揽子对比里为一张建不了主键的表中断全部, 等于把已经拿到的结论丢掉。
	for _, t := range job.Body.Tables {
		if ctx.Err() != nil {
			break
		}
		job.mu.Lock()
		job.current = t
		job.mu.Unlock()
		job.addResult(h.compareOneTable(ctx, job, src, dst, t))
	}
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		job.finish("failed", "比对超过 2 小时已中止, 结果是半截的")
	case ctx.Err() != nil:
		job.finish("canceled", "已取消")
	default:
		job.finish("done", "")
	}
}

// ── 端点 ──

func (h *Handlers) handleDataDiff(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var body dataDiffBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, "invalid body", http.StatusBadRequest)
		return
	}
	var tables []string
	seen := map[string]bool{}
	for _, t := range body.Tables {
		t = strings.TrimSpace(t)
		if t == "" || seen[t] {
			continue
		}
		if !reTableName.MatchString(t) {
			writeErr(w, "表名不合法: "+t, http.StatusBadRequest)
			return
		}
		seen[t] = true
		tables = append(tables, t)
	}
	if len(tables) == 0 {
		writeErr(w, "请至少选择一张表", http.StatusBadRequest)
		return
	}
	if len(tables) > maxDataDiffTables {
		writeErr(w, fmt.Sprintf("一次最多对比 %d 张表", maxDataDiffTables), http.StatusBadRequest)
		return
	}
	if len(tables) > 1 && len(body.KeyColumns) > 0 {
		// 手填键列只对单表有意义: 多表共用一套键名, 迟早撞上"这表的主键叫 uid"而不是 id,
		// 与其悄悄按猜的键比(然后生成打到错行上的语句), 不如一次一张。
		writeErr(w, "手填键列只支持单表对比; 多表请留空, 按各表主键自动判定", http.StatusBadRequest)
		return
	}
	body.Tables = tables
	if body.Options.BatchRows <= 0 {
		body.Options.BatchRows = 1000
	}
	if body.Options.BatchRows > 5000 {
		body.Options.BatchRows = 5000
	}
	if body.Options.MaxDiffs <= 0 {
		body.Options.MaxDiffs = 2000
	}
	if body.Options.MaxRows <= 0 {
		body.Options.MaxRows = 200000
	}
	job := h.diffs.create(body, len(tables))
	go func() {
		defer func() {
			if p := recover(); p != nil {
				log.Printf("[data-diff] 任务 %s panic: %v", job.ID, p)
				job.finish("failed", fmt.Sprintf("比对内部异常: %v", p))
			}
		}()
		h.runDataDiff(job)
	}()
	writeJSON(w, map[string]any{"ok": true, "jobId": job.ID})
}

func (h *Handlers) handleDataDiffJob(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	id := r.URL.Query().Get("id")
	job := h.diffs.get(id)
	if job == nil {
		writeJSONStatus(w, http.StatusNotFound, map[string]any{"ok": false,
			"error": "比对任务不存在或已过期(超过 30 分钟), 请重新比对", "expired": true})
		return
	}
	out := job.snapshot()
	out["ok"] = true
	writeJSON(w, out)
}

func (h *Handlers) handleDataDiffCancel(w http.ResponseWriter, r *http.Request) {
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
	if !h.diffs.cancel(body.ID) {
		writeJSON(w, map[string]any{"ok": false, "error": "任务不存在或已经结束"})
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

type dataKeyCell struct {
	Column string `json:"column"`
	Value  string `json:"value"`
}

type dataDiffApplyBody struct {
	Src           dataDiffEnd `json:"src"`
	Dst           dataDiffEnd `json:"dst"`
	Table         string      `json:"table"`
	KeyColumns    []string    `json:"keyColumns,omitempty"`
	IgnoreColumns []string    `json:"ignoreColumns,omitempty"`
	Rows          []struct {
		Key []dataKeyCell `json:"key"`
	} `json:"rows"`
	Confirm bool `json:"confirm"`
}

func (h *Handlers) handleDataDiffApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var body dataDiffApplyBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, "invalid body", http.StatusBadRequest)
		return
	}
	if !reTableName.MatchString(strings.TrimSpace(body.Table)) {
		writeErr(w, "表名不合法", http.StatusBadRequest)
		return
	}
	if len(body.Rows) == 0 {
		writeErr(w, "没有勾选任何行", http.StatusBadRequest)
		return
	}
	if len(body.Rows) > maxDataDiffRows {
		writeErr(w, fmt.Sprintf("一次最多执行 %d 行(当前 %d 行), 请分批", maxDataDiffRows, len(body.Rows)), http.StatusBadRequest)
		return
	}
	if !body.Confirm {
		writeErr(w, "需要 confirm=true 才执行", http.StatusBadRequest)
		return
	}
	conn, err := h.store.Get(body.Dst.ID)
	if err != nil {
		writeErr(w, "目标连接不存在: "+err.Error(), http.StatusBadRequest)
		return
	}
	src, err := h.openDataEnd(body.Src)
	if err != nil {
		writeErr(w, "基准侧: "+err.Error(), http.StatusBadRequest)
		return
	}
	dst, err := h.openDataEnd(body.Dst)
	if err != nil {
		writeErr(w, "目标侧: "+err.Error(), http.StatusBadRequest)
		return
	}
	s, d, _, _, berr := buildDataSide(src, dst, dataDiffBody{KeyColumns: body.KeyColumns, IgnoreColumns: body.IgnoreColumns}, body.Table)
	if berr != nil {
		writeJSON(w, map[string]any{"ok": false, "error": berr.Error()})
		return
	}
	spec := &syncpkg.DataTableSpec{Table: body.Table, DstRef: d.ref, DstD: d.d, Columns: s.cols}

	// 客户端只回键。两侧都按这批键重新读一遍 —— 值以库里此刻的为准, 不信前端带来的。
	tuples := make([][]any, 0, len(body.Rows))
	for _, row := range body.Rows {
		if len(row.Key) != len(s.keys) {
			writeErr(w, "键列数与表当前定义不一致, 请重新比对", http.StatusBadRequest)
			return
		}
		byName := map[string]string{}
		for _, c := range row.Key {
			byName[c.Column] = c.Value
		}
		tp := make([]any, len(s.keys))
		for i, k := range s.keys {
			v, ok := byName[k.Name]
			if !ok {
				writeErr(w, "缺少键列 "+k.Name+" 的值, 请重新比对", http.StatusBadRequest)
				return
			}
			tp[i] = v
		}
		tuples = append(tuples, tp)
	}
	srcRows, err := s.lookup(r.Context(), tuples)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": "重新读基准侧失败: " + err.Error()})
		return
	}
	dstRows, err := d.lookup(r.Context(), tuples)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": "重新读目标侧失败: " + err.Error()})
		return
	}
	srcIdx := indexRowsByKey(srcRows, s.keys)
	dstIdx := indexRowsByKey(dstRows, s.keys)

	var sqls []string
	var stale []string
	kinds := map[string]int{}
	for _, tp := range tuples {
		id := syncpkg.DataKeyID(s.keys, rowFromTuple(s.keys, tp))
		sr, sok := srcIdx[id]
		dr, dok := dstIdx[id]
		if !sok && !dok {
			stale = append(stale, prettyKeyID(id)) // 两侧都没有了(被人删了或键值变了)
			continue
		}
		var sr2, dr2 map[string]any
		if sok {
			sr2 = sr
		}
		if dok {
			dr2 = dr
		}
		rs, _, differs := syncpkg.RowsToStatements(spec, sr2, dr2)
		if !differs {
			stale = append(stale, prettyKeyID(id))
			continue
		}
		switch {
		case dr2 == nil:
			kinds["insert"]++
		case sr2 == nil:
			kinds["delete"]++
		default:
			kinds["update"]++
		}
		sqls = append(sqls, rs...)
	}
	if len(sqls) == 0 {
		writeJSON(w, map[string]any{"ok": true, "noop": true, "stale": len(stale),
			"message": "勾选的行已经没有差异(可能刚被别人改过), 未执行任何语句"})
		return
	}

	risk, reason := RiskSafe, ""
	for _, sq := range sqls {
		if rk, rs := classifySQLRisk(dst.engine, sq); rk.AtLeast(risk) && rk != risk {
			risk, reason = rk, rs
		}
	}
	joined := strings.Join(sqls, ";\n")
	if h.interceptWrite(w, conn, body.Dst.ID, joined, risk, reason, true) {
		return
	}
	total, failedAt, execErr := execAllInTx(r.Context(), d.db, sqls)
	if execErr != nil {
		detail := execErr.Error()
		if failedAt > 0 {
			detail = fmt.Sprintf("第 %d 条失败: %v", failedAt, execErr)
			if syncpkg.EngineDialect(dst.engine) == syncpkg.DialectMySQL && !errors.Is(execErr, errNoTxDriver) {
				detail += "（MySQL 系逐条提交, 前 " + strconv.Itoa(failedAt-1) + " 条已生效）"
			}
		}
		h.audit.Append(AuditEntry{
			ConnID: body.Dst.ID, ConnName: conn.Info.Name, Engine: dst.engine,
			SQL: joined, Risk: string(risk), Decision: "failed", Detail: detail,
		})
		writeJSON(w, map[string]any{"ok": false, "affected": 0, "failedAt": failedAt,
			"rolledBack": failedAt == 0, "error": detail})
		return
	}
	h.audit.Append(AuditEntry{
		ConnID: body.Dst.ID, ConnName: conn.Info.Name, Engine: dst.engine,
		SQL: joined, Risk: string(risk), Decision: "executed",
		Detail: fmt.Sprintf("%s（数据对比 %s: insert %d / update %d / delete %d, %d 条同一事务提交; 勾选 %d 行中 %d 行已无差异被跳过)",
			reason, body.Table, kinds["insert"], kinds["update"], kinds["delete"], len(sqls), len(body.Rows), len(stale)),
	})
	writeJSON(w, map[string]any{"ok": true, "affected": total, "count": len(sqls),
		"stale": len(stale), "staleKeys": capStrings(stale, 20), "kinds": kinds})
}

// prettyKeyID 内部键标识用 \x1f 分隔(不会与数据冲突), 报给人看要换成逗号。
func prettyKeyID(id string) string { return strings.ReplaceAll(id, "\x1f", ", ") }

// indexRowsByKey 按规范键值建索引。与比对循环用同一个 DataKeyID, 于是"执行时认的键"
// 和"比对时认的键"不会因为大小写/前后空格走成两套。
func indexRowsByKey(rows []map[string]any, keys []syncpkg.DataGridColumn) map[string]map[string]any {
	out := make(map[string]map[string]any, len(rows))
	for _, r := range rows {
		id := syncpkg.DataKeyID(keys, r)
		out[id] = r
		out[strings.ToLower(id)] = r
	}
	return out
}

// rowFromTuple 把客户端回传的键拼成一行, 只为算出与索引一致的 id(值用字符串即可)。
func rowFromTuple(keys []syncpkg.DataGridColumn, tp []any) map[string]any {
	m := make(map[string]any, len(keys))
	for i, k := range keys {
		m[k.Name] = tp[i]
	}
	return m
}

// capStrings 空集合也要给 []: JSON 出 null 的话前端一 .length 就炸(全模块同一个口径)。
func capStrings(in []string, n int) []string {
	if in == nil {
		return []string{}
	}
	if len(in) <= n {
		return in
	}
	return in[:n]
}
