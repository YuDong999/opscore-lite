package sync

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	gonavibase "opscore/internal/dbmanager/gonavi/db"
)

const (
	defaultBatchRows     = 500
	maxBatchSQLBytes     = 8 << 20
	defaultStreamTimeout = 6 * time.Hour
)

// execSQL 执行写语句: 优先 context 版(可取消), 回退同步版。
func execSQL(ctx context.Context, db gonavibase.Database, sqlText string) (int64, error) {
	if ec, ok := db.(gonavibase.ExecContexter); ok {
		return ec.ExecContext(ctx, sqlText)
	}
	return db.Exec(sqlText)
}

// queryRows 执行查询: 优先 context 版, 回退同步版。
func queryRows(ctx context.Context, db gonavibase.Database, sqlText string) ([]map[string]any, error) {
	if qc, ok := db.(gonavibase.QueryContexter); ok {
		rows, _, err := qc.QueryContext(ctx, sqlText)
		return rows, err
	}
	rows, _, err := db.Query(sqlText)
	return rows, err
}

// Run 执行同步任务(在后台 goroutine 中运行)。
func (r *Runner) Run(job *Job) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultStreamTimeout)
	job.cancel = cancel
	defer cancel()

	req := job.Request
	srcDB, srcEngine, err := r.pool.AcquireForSync(req.SourceID)
	if err != nil {
		r.fail(job, "源连接不可用: "+err.Error())
		return
	}
	dstDB, dstEngine, err := r.pool.AcquireForSync(req.TargetID)
	if err != nil {
		r.fail(job, "目标连接不可用: "+err.Error())
		return
	}
	srcDialect := EngineDialect(srcEngine)
	dstDialect := EngineDialect(dstEngine)
	if srcDialect == "" || dstDialect == "" {
		r.fail(job, fmt.Sprintf("引擎对 %s → %s 暂不支持自动迁移", srcEngine, dstEngine))
		return
	}

	// 计划: run 时现算, 保证与最新结构一致
	plan, err := r.BuildPlan(ctx, req)
	if err != nil {
		r.fail(job, err.Error())
		return
	}
	job.Plan = plan
	if len(plan.Unsupported) > 0 {
		r.fail(job, strings.Join(plan.Unsupported, "; "))
		return
	}

	// 初始化每表进度
	job.mu.Lock()
	for _, tp := range plan.Tables {
		st := "pending"
		if tp.Skipped {
			st = "skipped"
		}
		job.Tables = append(job.Tables, TableProgress{Table: tp.Target, Status: st})
	}
	job.mu.Unlock()

	for _, tp := range plan.Tables {
		if tp.Skipped {
			continue
		}
		if ctx.Err() != nil {
			return
		}
		var runErr error
		switch req.Mode {
		case ModeSchemaOnly:
			runErr = r.execSchema(ctx, dstDB, tp)
		case ModeSchemaFull:
			runErr = r.execSchemaThenFull(ctx, srcDB, dstDB, srcDialect, dstDialect, req, tp, job, req.Options.Truncate)
		case ModeSchemaFullIncr:
			runErr = r.execSchemaThenFull(ctx, srcDB, dstDB, srcDialect, dstDialect, req, tp, job, req.Options.Truncate)
		case ModeTruncateFull:
			runErr = r.execTruncateThenFull(ctx, srcDB, dstDB, srcDialect, dstDialect, req, tp, job)
		case ModeIncrOnly:
			runErr = r.execIncremental(ctx, srcDB, dstDB, srcDialect, dstDialect, req, tp, job)
		case ModeVerify:
			runErr = r.execVerify(ctx, srcDB, dstDB, srcDialect, dstDialect, req, tp, job)
		default:
			runErr = fmt.Errorf("未知模式 %s", req.Mode)
		}
		if runErr != nil {
			job.updateTable(tp.Target, func(p *TableProgress) { p.Status, p.Err = "failed", runErr.Error() })
			r.fail(job, fmt.Sprintf("表 %s 同步失败: %v", tp.Target, runErr))
			return
		}
		if req.Mode != ModeVerify {
			job.updateTable(tp.Target, func(p *TableProgress) { p.Status = "done" })
		}
	}
	r.finish(job)
}

// ── schema ──

func (r *Runner) execSchema(ctx context.Context, dstDB gonavibase.Database, tp TablePlan) error {
	if _, err := execSQL(ctx, dstDB, tp.CreateDDL); err != nil {
		// IF NOT EXISTS 下"已存在"不应失败, 但部分方言仍报错
		if !strings.Contains(err.Error(), "already exists") && !strings.Contains(err.Error(), "已存在") {
			return fmt.Errorf("建表失败: %w", err)
		}
	}
	for _, idx := range tp.IndexDDL {
		if _, err := execSQL(ctx, dstDB, idx); err != nil {
			// 索引失败仅记录, 不中断
			_ = err
		}
	}
	return nil
}

func (r *Runner) execSchemaThenFull(ctx context.Context, srcDB, dstDB gonavibase.Database, srcD, dstD Dialect, req SyncRequest, tp TablePlan, job *Job, truncate bool) error {
	job.updateTable(tp.Target, func(p *TableProgress) { p.Status = "creating" })
	if err := r.execSchema(ctx, dstDB, tp); err != nil {
		return err
	}
	if truncate {
		if err := truncateTarget(ctx, dstDB, dstD, req.TargetDB, tp.Target); err != nil {
			return fmt.Errorf("清空目标表失败: %w", err)
		}
	}
	return r.copyFull(ctx, srcDB, dstDB, srcD, dstD, req, tp, job, "")
}

func (r *Runner) execTruncateThenFull(ctx context.Context, srcDB, dstDB gonavibase.Database, srcD, dstD Dialect, req SyncRequest, tp TablePlan, job *Job) error {
	job.updateTable(tp.Target, func(p *TableProgress) { p.Status = "creating" })
	if err := truncateTarget(ctx, dstDB, dstD, req.TargetDB, tp.Target); err != nil {
		return fmt.Errorf("清空目标表失败: %w", err)
	}
	return r.copyFull(ctx, srcDB, dstDB, srcD, dstD, req, tp, job, "")
}

func truncateTarget(ctx context.Context, dstDB gonavibase.Database, d Dialect, schema, table string) error {
	tn := qualifiedName(schema, table, d)
	if _, err := execSQL(ctx, dstDB, "TRUNCATE TABLE "+tn); err == nil {
		return nil
	}
	// TRUNCATE 失败(外键引用等)回退 DELETE
	_, err := execSQL(ctx, dstDB, "DELETE FROM "+tn)
	return err
}

// ── 全量: 流式读 + 批量内联 INSERT ──

func (r *Runner) copyFull(ctx context.Context, srcDB, dstDB gonavibase.Database, srcD, dstD Dialect, req SyncRequest, tp TablePlan, job *Job, watermark string) error {
	job.updateTable(tp.Target, func(p *TableProgress) { p.Status = "copying" })

	var where string
	if watermark != "" {
		// 增量: 水位以源方言字面量内联
		where = fmt.Sprintf(" WHERE %s > '%s'", quoteIdent(tp.IncrColumn, srcD), escapeLiteral(watermark, srcD))
	} else if req.Options.WhereClause != "" {
		where = " WHERE " + req.Options.WhereClause
	}
	sqlText := fmt.Sprintf("SELECT * FROM %s%s", qualifiedName(req.SourceDB, tp.Source, srcD), where)

	batchRows := req.Options.BatchRows
	if batchRows <= 0 {
		batchRows = defaultBatchRows
	}
	maxRows := req.Options.MaxRows

	// 各列目标基础类型(供写入端做 boolean/bytea 归一)。按列名索引:
	// 流式读取的列序是 map 迭代序, 与表定义序不一致, 必须按名字对齐。
	colBases := map[string]string{}
	for _, m := range tp.Columns {
		colBases[strings.ToLower(m.Name)] = baseTypeOf(m.Target)
	}

	batcher := &batchWriter{
		dst:      dstDB,
		ctx:      ctx,
		schema:   req.TargetDB,
		table:    tp.Target,
		dstD:     dstD,
		colBases: colBases,
		batchMax: batchRows,
		job:      job,
	}
	stop := false
	var streamErr error
	err := streamQuery(ctx, srcDB, sqlText, &streamConsumer{
		onColumns: func(cols []string) error { batcher.columns = cols; return nil },
		onRow: func(row map[string]any) error {
			if maxRows > 0 && batcher.count >= maxRows {
				stop = true
				return errStopStream
			}
			return batcher.append(row)
		},
		onError: func(err error) error { streamErr = err; return err },
	})
	if streamErr != nil {
		return fmt.Errorf("流式读取失败: %w", streamErr)
	}
	if err != nil && !stop {
		return fmt.Errorf("流式读取失败: %w", err)
	}
	if err := batcher.flush(); err != nil {
		return err
	}
	return nil
}

// baseTypeOf 从目标列定义文本剥离约束后缀, 取基础类型名。
func baseTypeOf(target string) string {
	t := target
	for _, sep := range []string{" NOT NULL", " DEFAULT ", " GENERATED", " NULL"} {
		if i := strings.Index(t, sep); i > 0 {
			t = t[:i]
		}
	}
	return parseTypeName(t).base
}

var errStopStream = fmt.Errorf("达到 maxRows 上限, 停止流式读取")

// streamQuery 优先走驱动流式接口, 不支持则退化为缓冲查询逐行消费。
func streamQuery(ctx context.Context, db gonavibase.Database, sqlText string, consumer *streamConsumer) error {
	if sq, ok := db.(gonavibase.StreamQueryExecer); ok {
		return sq.StreamQueryContext(ctx, sqlText, consumer)
	}
	rows, err := queryRows(ctx, db, sqlText)
	if err != nil {
		return err
	}
	if len(rows) > 0 {
		cols := make([]string, 0, len(rows[0]))
		for c := range rows[0] {
			cols = append(cols, c)
		}
		if err := consumer.SetColumns(cols); err != nil {
			return err
		}
	}
	for _, row := range rows {
		if err := consumer.ConsumeRow(row); err != nil {
			return err
		}
	}
	return nil
}

type streamConsumer struct {
	onColumns func([]string) error
	onRow     func(map[string]any) error
	onError   func(error) error
}

func (c *streamConsumer) SetColumns(cols []string) error {
	if c.onColumns != nil {
		return c.onColumns(cols)
	}
	return nil
}

func (c *streamConsumer) ConsumeRow(row map[string]any) error {
	return c.onRow(row)
}

// batchWriter 累积行并按批写入目标。
type batchWriter struct {
	dst      gonavibase.Database
	ctx      context.Context
	schema   string
	table    string
	dstD     Dialect
	columns  []string
	colBases map[string]string // 列名(小写) → 目标基础类型
	rows     [][]any
	count    int64
	batchMax int
	job      *Job
}

func (b *batchWriter) append(row map[string]any) error {
	vals := make([]any, len(b.columns))
	for i, c := range b.columns {
		vals[i] = row[c]
	}
	b.rows = append(b.rows, vals)
	b.count++
	if len(b.rows) >= b.batchMax {
		return b.flush()
	}
	return nil
}

func (b *batchWriter) flush() error {
	if len(b.rows) == 0 {
		return nil
	}
	sqlText := buildInsertSQL(b.schema, b.table, b.columns, b.colBases, b.rows, b.dstD, maxBatchSQLBytes)
	b.rows = b.rows[:0]
	if _, err := execSQL(b.ctx, b.dst, sqlText); err != nil {
		preview := sqlText
		if len(preview) > 400 {
			preview = preview[:400] + "..."
		}
		return fmt.Errorf("批量写入失败(已复制 %d 行): %w; SQL 预览: %s", b.count, err, preview)
	}
	n := b.count
	b.job.updateTable(b.table, func(p *TableProgress) { p.RowsCopied = n })
	return nil
}

// ── 增量 ──

func (r *Runner) execIncremental(ctx context.Context, srcDB, dstDB gonavibase.Database, srcD, dstD Dialect, req SyncRequest, tp TablePlan, job *Job) error {
	if tp.IncrStrategy == IncrNone {
		return fmt.Errorf("表 %s 无可用增量列", tp.Target)
	}
	watermark, err := readWatermark(ctx, dstDB, dstD, req.TargetDB, tp)
	if err != nil {
		return fmt.Errorf("读取目标水位失败: %w", err)
	}
	return r.copyFull(ctx, srcDB, dstDB, srcD, dstD, req, tp, job, watermark)
}

// readWatermark 从目标表读当前水位(MAX(增量列))。
func readWatermark(ctx context.Context, db gonavibase.Database, d Dialect, schema string, tp TablePlan) (string, error) {
	q := fmt.Sprintf("SELECT MAX(%s) AS w FROM %s", quoteIdent(tp.IncrColumn, d), qualifiedName(schema, tp.Target, d))
	rows, err := queryRows(ctx, db, q)
	if err != nil {
		return "", err
	}
	if len(rows) == 0 || rows[0]["w"] == nil {
		return "", nil
	}
	return watermarkToString(rows[0]["w"]), nil
}

func watermarkToString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case time.Time:
		return t.Format("2006-01-02 15:04:05.999999")
	case []byte:
		return string(t)
	default:
		return fmt.Sprintf("%v", v)
	}
}

// ── 校验 ──

func (r *Runner) execVerify(ctx context.Context, srcDB, dstDB gonavibase.Database, srcD, dstD Dialect, req SyncRequest, tp TablePlan, job *Job) error {
	countOf := func(db gonavibase.Database, d Dialect, schema, table string) (int64, error) {
		rows, err := queryRows(ctx, db, fmt.Sprintf("SELECT COUNT(*) AS c FROM %s", qualifiedName(schema, table, d)))
		if err != nil {
			return 0, err
		}
		if len(rows) == 0 {
			return 0, nil
		}
		switch t := rows[0]["c"].(type) {
		case int64:
			return t, nil
		case int:
			return int64(t), nil
		case uint64:
			return int64(t), nil
		case float64:
			return int64(t), nil
		default:
			return strconv.ParseInt(fmt.Sprintf("%v", rows[0]["c"]), 10, 64)
		}
	}
	srcN, err := countOf(srcDB, srcD, req.SourceDB, tp.Source)
	if err != nil {
		return fmt.Errorf("统计源行数失败: %w", err)
	}
	dstN, err := countOf(dstDB, dstD, req.TargetDB, tp.Target)
	if err != nil {
		return fmt.Errorf("统计目标行数失败: %w", err)
	}
	job.mu.Lock()
	status := "done"
	errMsg := ""
	if srcN != dstN {
		status = "failed"
		errMsg = fmt.Sprintf("行数不一致: 源 %d / 目标 %d", srcN, dstN)
	}
	job.Tables = append(job.Tables, TableProgress{
		Table:      tp.Target + " (校验)",
		Status:     status,
		RowsCopied: dstN,
		Err:        errMsg,
	})
	job.mu.Unlock()
	if srcN != dstN {
		return fmt.Errorf("行数不一致: 源 %d / 目标 %d", srcN, dstN)
	}
	return nil
}

// ── job 状态流转 ──

func (r *Runner) fail(job *Job, msg string) {
	job.mu.Lock()
	now := time.Now()
	job.Status, job.Err, job.FinishedAt = "failed", msg, &now
	job.mu.Unlock()
}

func (r *Runner) finish(job *Job) {
	job.mu.Lock()
	now := time.Now()
	job.Status, job.FinishedAt = "done", &now
	job.mu.Unlock()
}
