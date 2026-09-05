package sync

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	gonavibase "opscore/internal/dbmanager/gonavi/db"
	gonaviConnection "opscore/internal/dbmanager/gonavi/connection"
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

		ddl, idxDDL, notes := GenerateCreateDDL(req.TargetDB, tr.target, cols, idx, srcDialect, dstDialect)
		tp.CreateDDL = ddl
		tp.IndexDDL = idxDDL
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

// QueryRows 导出查询(供 dbmanager 数据浏览接口使用)。
func QueryRows(ctx context.Context, db gonavibase.Database, sqlText string) ([]map[string]any, []string, error) {
	if qc, ok := db.(gonavibase.QueryContexter); ok {
		return qc.QueryContext(ctx, sqlText)
	}
	return db.Query(sqlText)
}
