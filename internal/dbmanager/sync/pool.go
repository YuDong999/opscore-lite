package sync

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	gonaviConnection "opscore/internal/dbmanager/gonavi/connection"
	gonavibase "opscore/internal/dbmanager/gonavi/db"
)

// Pool 同步模块对连接池的最小依赖(避免与 dbmanager 包循环导入)。
type Pool interface {
	// AcquireForSync 返回缓存的 gonavi Database 实例与引擎类型。
	AcquireForSync(connID string) (gonavibase.Database, string, error)
}

// Runner 同步执行器。
type Runner struct {
	pool Pool
	jobs *JobRegistry
	// OnFinish 任务进入终态后的回调(给审计用)。只挂一处: Start 的 goroutine 收尾时调用,
	// 覆盖"正常完成 / 失败 / panic / 被取消"全部出口 —— 分散在各处记审计迟早会漏一条。
	OnFinish func(*Job)
}

func NewRunner(pool Pool) *Runner {
	return &Runner{pool: pool, jobs: NewJobRegistry()}
}

func (r *Runner) Jobs() *JobRegistry { return r.jobs }

// BuildPlan 生成迁移计划: 方言判定 + 每表类型映射/DDL/增量策略。
func (r *Runner) BuildPlan(ctx context.Context, req SyncRequest) (*SyncPlan, error) {
	srcDB, srcEngine, err := r.pool.AcquireForSync(req.SourceID)
	if err != nil {
		return nil, fmt.Errorf("源连接不可用: %w", err)
	}
	dstDB, dstEngine, err := r.pool.AcquireForSync(req.TargetID)
	if err != nil {
		return nil, fmt.Errorf("目标连接不可用: %w", err)
	}
	_ = dstDB

	srcDialect := EngineDialect(srcEngine)
	dstDialect := EngineDialect(dstEngine)
	plan := &SyncPlan{
		SourceDialect: srcDialect,
		TargetDialect: dstDialect,
		Mode:          req.Mode,
	}
	if srcDialect == "" || dstDialect == "" {
		plan.Unsupported = append(plan.Unsupported,
			fmt.Sprintf("引擎对 %s → %s 暂不支持自动迁移(仅支持 MySQL 族/PostgreSQL 族)", srcEngine, dstEngine))
		return plan, nil
	}
	if req.SourceID == req.TargetID && strings.EqualFold(strings.TrimSpace(req.SourceDB), strings.TrimSpace(req.TargetDB)) {
		return nil, fmt.Errorf("源库与目标库相同, 拒绝同步")
	}

	// 表清单: TableMaps(自定义目标名)优先, 其次 Tables(同名), 都空=全库
	type tableRef struct{ source, target string }
	var tableRefs []tableRef
	for _, m := range req.TableMaps {
		if strings.TrimSpace(m.Source) != "" {
			tgt := strings.TrimSpace(m.Target)
			if tgt == "" {
				tgt = m.Source
			}
			tableRefs = append(tableRefs, tableRef{m.Source, tgt})
		}
	}
	if len(tableRefs) == 0 {
		for _, t := range req.Tables {
			if strings.TrimSpace(t) != "" {
				tableRefs = append(tableRefs, tableRef{t, t})
			}
		}
	}
	if len(tableRefs) == 0 {
		names, err := srcDB.GetTables(req.SourceDB)
		if err != nil {
			return nil, fmt.Errorf("列举源表失败: %w", err)
		}
		sort.Strings(names)
		for _, n := range names {
			tableRefs = append(tableRefs, tableRef{n, n})
		}
	}

	for _, tr := range tableRefs {
		t := tr.source
		tp := TablePlan{Source: tr.source, Target: tr.target}
		cols, err := srcDB.GetColumns(req.SourceDB, t)
		if err != nil {
			tp.Skipped, tp.SkipReason = true, "读取源表结构失败: "+err.Error()
			plan.Tables = append(plan.Tables, tp)
			continue
		}
		idx, _ := srcDB.GetIndexes(req.SourceDB, t)

		for _, c := range cols {
			tp.Columns = append(tp.Columns, mapColumn(c, srcDialect, dstDialect))
		}
		tp.SourcePK = primaryKeyOf(cols)

		ddl, idxDDL, commentDDL, notes := GenerateCreateDDL(EffectiveSchema(req, dstDialect), tr.target, cols, idx, srcDialect, dstDialect)
		tp.CreateDDL = ddl
		tp.IndexDDL = idxDDL
		tp.CommentDDL = commentDDL
		tp.Notes = notes
		if tr.target != tr.source {
			tp.Notes = append(tp.Notes, fmt.Sprintf("目标表为自定义名 %s (源 %s), 将自动建表", tr.target, tr.source))
		}

		// 增量策略探测
		tp.IncrColumn, tp.IncrStrategy = detectIncremental(cols, req.IncrementalColumn)
		if req.Mode == ModeIncrOnly && tp.IncrStrategy == IncrNone {
			tp.Skipped = true
			tp.SkipReason = "无可用增量列(需整数自增主键或时间戳列)"
		}
		plan.Tables = append(plan.Tables, tp)
	}
	return plan, nil
}

func primaryKeyOf(cols []gonaviConnection.ColumnDefinition) string {
	for _, c := range cols {
		if strings.EqualFold(c.Key, "PRI") {
			return c.Name
		}
	}
	return ""
}

// detectIncremental 自动探测增量列: 指定列 > 整数自增主键 > 常见时间戳列名。
func detectIncremental(cols []gonaviConnection.ColumnDefinition, explicit string) (string, IncrementalStrategy) {
	if strings.TrimSpace(explicit) != "" {
		for _, c := range cols {
			if strings.EqualFold(c.Name, strings.TrimSpace(explicit)) {
				pt := parseTypeName(c.Type)
				if isIntBase(pt.base) {
					return c.Name, IncrAutoIncrement
				}
				return c.Name, IncrTimestamp
			}
		}
		return explicit, IncrNone
	}
	for _, c := range cols {
		if strings.EqualFold(c.Key, "PRI") && strings.Contains(strings.ToLower(c.Extra), "auto_increment") && isIntBase(parseTypeName(c.Type).base) {
			return c.Name, IncrAutoIncrement
		}
	}
	// PG identity/serial 的 Extra 由实现而定, 再按列名兜底
	for _, c := range cols {
		if strings.EqualFold(c.Key, "PRI") && isIntBase(parseTypeName(c.Type).base) {
			return c.Name, IncrAutoIncrement
		}
	}
	candidates := []string{"updated_at", "update_time", "modified_at", "modify_time", "gmt_modified", "last_modified", "last_update", "mtime"}
	for _, cand := range candidates {
		for _, c := range cols {
			if strings.EqualFold(c.Name, cand) {
				return c.Name, IncrTimestamp
			}
		}
	}
	return "", IncrNone
}

func isIntBase(base string) bool {
	switch base {
	case "tinyint", "smallint", "mediumint", "int", "integer", "bigint", "int2", "int4", "int8":
		return true
	}
	return false
}

// QuoteIdent 导出标识符引用(供 dbmanager 数据浏览接口使用)。
func QuoteIdent(name string, d Dialect) string { return quoteIdent(name, d) }

// QueryScalar 查询单值(如 COUNT(*)), 返回 int64。
func QueryScalar(ctx context.Context, db gonavibase.Database, sqlText string) (int64, error) {
	rows, _, err := QueryRows(ctx, db, sqlText)
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	for _, v := range rows[0] {
		switch t := v.(type) {
		case int64:
			return t, nil
		case int:
			return int64(t), nil
		case uint64:
			return int64(t), nil
		case float64:
			return int64(t), nil
		case string:
			return strconv.ParseInt(t, 10, 64)
		}
	}
	return 0, nil
}

// previewMarkerPrefixes 是扫码层对大值给出的预览标记前缀(见 gonavi/db/scan_rows.go)。
// 它们只该出现在**给人看**的地方; 一旦流到"落成文件/语句"的路径就是数据损坏。
var previewMarkerPrefixes = []string{"[BLOB preview: ", "[CLOB preview: "}

// looksLikeTruncatedPreview 判断一个值是不是预览截断产物。
// **严格前缀匹配**: 只在值开头就是标记时才算 —— 真实数据里出现这串文字的概率极低,
// 但"把真数据误判成截断"比"漏判"更烦人, 所以不做宽松搜索。
func looksLikeTruncatedPreview(v any) bool {
	s, ok := v.(string)
	if !ok {
		return false
	}
	for _, p := range previewMarkerPrefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// ErrValueTruncatedForPreview 表示"这条数据被预览截断了, 不能当作完整值使用"。
// 它不是普通错误: 调用方应据此改用 QueryRowsUnbounded(或单元格分片读), 而不是继续写文件。
type errValueTruncated struct{ Column string }

func (e *errValueTruncated) Error() string {
	return "列 " + e.Column + " 的值被预览截断, 不能作为完整数据使用(该驱动未实现不截断查询; " +
		"请用单元格详情里的「下载」取回完整值)"
}

// LooksLikeTruncatedPreview 对外暴露同一个判定 —— dbmanager 侧的分流兜底也要用它,
// "什么算预览标记"只该有一处定义, 否则两边会漂移。
func LooksLikeTruncatedPreview(v any) bool { return looksLikeTruncatedPreview(v) }

// CheckNoPreviewTruncation 逐值检查一批行里有没有预览截断值, 命中就报错(含列名)。
// 产出数据的路径在拿不到 UnboundedQueryContexter 时用它兜底 —— 宁可不给这份数据,
// 也不给一份"看着成功、内容被截断"的导出。
func CheckNoPreviewTruncation(rows []map[string]any, cols []string) error {
	for _, row := range rows {
		for _, c := range cols {
			if looksLikeTruncatedPreview(row[c]) {
				return &errValueTruncated{Column: c}
			}
		}
	}
	return nil
}

// QueryRowsUnbounded 是"**要完整数据**"的查询出口(导出 / 生成 INSERT / 备份都用它)。
//
// 为什么不能直接用 QueryRows: 扫码层对超过阈值的大对象只给预览(那是对的 —— 界面不能被
// 几 MB 的二进制拖死), 但**拿预览去拼 SQL 或写文件就是把数据写坏**(2026-09-28 实测踩到:
// 5000 字节 BLOB 被写成 "[BLOB preview: 4096/5000 bytes] ZZZ...")。
//
// 实现顺序:
//  1. 驱动实现了 UnboundedQueryContexter → 走它(拿完整值, MySQL/PG/MariaDB/自定义等已实现);
//  2. 否则退回 QueryRows, 并**逐值检查是否拿到预览标记** —— 命中就报错, 绝不静默返回半截数据。
//     这样在还没适配的驱动上, 用户得到的是"这条取不了完整值"的明确提示, 而不是一份坏备份。
func QueryRowsUnbounded(ctx context.Context, db gonavibase.Database, sqlText string) ([]map[string]any, []string, error) {
	if uq, ok := db.(gonavibase.UnboundedQueryContexter); ok {
		return uq.QueryUnboundedContext(ctx, sqlText)
	}
	rows, cols, err := QueryRows(ctx, db, sqlText)
	if err != nil {
		return rows, cols, err
	}
	if err := CheckNoPreviewTruncation(rows, cols); err != nil {
		return nil, cols, err
	}
	return rows, cols, nil
}

// QueryRows 导出查询(供 dbmanager 数据浏览接口使用)。
func QueryRows(ctx context.Context, db gonavibase.Database, sqlText string) ([]map[string]any, []string, error) {
	if qc, ok := db.(gonavibase.QueryContexter); ok {
		return qc.QueryContext(ctx, sqlText)
	}
	return db.Query(sqlText)
}
