// DBService —— ADR-001 分层边界：thin handler 只依赖此接口，
// 实现委托给 GoNavi 底座（internal/dbmanager/gonavi/db）。
// 未来更换底座或拆分独立服务时，仅需替换实现，handlers 与前端契约不变。
package dbmanager

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	gonaviConnection "opscore/internal/dbmanager/gonavi/connection"
	gonavibase "opscore/internal/dbmanager/gonavi/db"
	syncpkg "opscore/internal/dbmanager/sync"
)

// DBService 数据库管理服务接口。
type DBService interface {
	// TestConnection 建立临时连接测试连通性，返回版本描述。
	TestConnection(ctx context.Context, conn *Connection) (string, error)
	// ListDatabases 列出可访问的数据库。
	ListDatabases(ctx context.Context, connID string) ([]string, error)
	// ListSchemas 列出连接所属引擎的命名空间(模式)。仅 库→模式→表 三级引擎有值,
	// 其余返回空 —— 前端据此动态决定是否渲染模式下拉(能力驱动, 非按引擎硬编码 UI)。
	ListSchemas(ctx context.Context, connID string) ([]string, error)
	// ListTables 列出指定库下的表/视图。
	ListTables(ctx context.Context, connID, database string) ([]TableInfo, error)
	// DescribeTable 返回列/索引/DDL。
	DescribeTable(ctx context.Context, connID, database, table string) ([]ColumnInfo, []IndexInfo, string, error)
	// ExecQuery 执行 SQL：SELECT 返回结果集(截断到 maxRows)，其他返回受影响行数。
	ExecQuery(ctx context.Context, connID, sqlText string, maxRows int, defaultDatabase string) (*QueryResult, error)
}

// GonaviService DBService 的 GoNavi 底座实现。
type GonaviService struct {
	pool *DatabasePool
}

func NewGonaviService(pool *DatabasePool) *GonaviService {
	return &GonaviService{pool: pool}
}

func (s *GonaviService) TestConnection(ctx context.Context, conn *Connection) (string, error) {
	db, err := openGonaviDatabase(conn)
	if err != nil {
		return "", err
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		return "", err
	}
	return probeServerVersion(db, string(conn.Info.Engine))
}

// probeServerVersion 尽力获取服务端版本；拿不到就返回通用成功描述。
func probeServerVersion(db gonavibase.Database, engine string) (string, error) {
	var probes []string
	switch EngineType(engine) {
	case EngineMySQL:
		probes = []string{"SELECT VERSION()"}
	case EnginePostgreSQL:
		probes = []string{"SELECT version()"}
	default:
		probes = nil
	}
	for _, q := range probes {
		rows, _, err := db.Query(q)
		if err != nil {
			continue
		}
		if len(rows) > 0 {
			for _, v := range rows[0] {
				if s, ok := v.(string); ok && s != "" {
					return s, nil
				}
				if v != nil {
					return fmt.Sprintf("%v", v), nil
				}
			}
		}
	}
	return "连接成功", nil
}

func (s *GonaviService) ListDatabases(ctx context.Context, connID string) ([]string, error) {
	db, _, err := s.pool.Acquire(connID)
	if err != nil {
		return nil, err
	}
	return db.GetDatabases()
}

func (s *GonaviService) ListSchemas(ctx context.Context, connID string) ([]string, error) {
	db, conn, err := s.pool.Acquire(connID)
	if err != nil {
		return nil, err
	}
	if !syncpkg.EngineHasSchema(string(conn.Info.Engine)) {
		return nil, nil // 能力缺失(如 MySQL 族): 空即"无模式层级"
	}
	rows, _, err := syncpkg.QueryRows(ctx, db, `SELECT nspname FROM pg_catalog.pg_namespace `+
		`WHERE nspname <> 'information_schema' AND nspname NOT LIKE 'pg|_%' ESCAPE '|' ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		if n, ok := row["nspname"].(string); ok && n != "" {
			out = append(out, n)
		}
	}
	return out, nil
}

func (s *GonaviService) ListTables(ctx context.Context, connID, database string) ([]TableInfo, error) {
	db, conn, err := s.pool.Acquire(connID)
	if err != nil {
		return nil, err
	}
	names, err := db.GetTables(database)
	if err != nil {
		return nil, err
	}
	out := make([]TableInfo, 0, len(names))
	commenter, hasComment := db.(gonavibase.TableCommentProvider)
	for _, n := range names {
		ti := TableInfo{Name: n, Type: "BASE TABLE", Schema: conn.Info.Config.Database}
		if hasComment {
			if c, err := commenter.GetTableComment(database, n); err == nil && c != "" {
				ti.Comment = c
			}
		}
		out = append(out, ti)
	}
	return out, nil
}

func (s *GonaviService) DescribeTable(ctx context.Context, connID, database, table string) ([]ColumnInfo, []IndexInfo, string, error) {
	db, _, err := s.pool.Acquire(connID)
	if err != nil {
		return nil, nil, "", err
	}
	colDefs, err := db.GetColumns(database, table)
	if err != nil {
		return nil, nil, "", fmt.Errorf("获取列失败: %w", err)
	}
	cols := make([]ColumnInfo, 0, len(colDefs))
	for _, c := range colDefs {
		ci := ColumnInfo{
			Name:     c.Name,
			Type:     c.Type,
			Nullable: strings.EqualFold(c.Nullable, "YES"),
			Key:      c.Key,
			Comment:  c.Comment,
		}
		if c.Default != nil {
			ci.Default = *c.Default
		}
		cols = append(cols, ci)
	}

	idxDefs, err := db.GetIndexes(database, table)
	if err != nil {
		// 索引获取失败不阻塞结构查看
		idxDefs = nil
	}
	idxs := aggregateIndexes(idxDefs)

	ddl, _ := db.GetCreateStatement(database, table)
	return cols, idxs, ddl, nil
}

// aggregateIndexes 把驱动返回的逐列索引行聚合成前端 IndexInfo 形态。
func aggregateIndexes(defs []gonaviConnection.IndexDefinition) []IndexInfo {
	if len(defs) == 0 {
		return nil
	}
	order := make([]string, 0, len(defs))
	byName := map[string]*IndexInfo{}
	for _, d := range defs {
		ii, ok := byName[d.Name]
		if !ok {
			ii = &IndexInfo{Name: d.Name}
			byName[d.Name] = ii
			order = append(order, d.Name)
		}
		ii.Columns = append(ii.Columns, d.ColumnName)
		if d.NonUnique == 0 {
			ii.Unique = true
		}
		if strings.EqualFold(d.Name, "PRIMARY") {
			ii.Primary = true
			ii.Unique = true
		}
	}
	out := make([]IndexInfo, 0, len(order))
	for _, n := range order {
		out = append(out, *byName[n])
	}
	return out
}

func (s *GonaviService) ExecQuery(ctx context.Context, connID, sqlText string, maxRows int, defaultDatabase string) (*QueryResult, error) {
	db, _, err := s.pool.Acquire(connID)
	if err != nil {
		return nil, err
	}
	if maxRows <= 0 {
		maxRows = MaxQueryRows
	}

	start := time.Now()
	res := &QueryResult{}

	// 标签绑定的库上下文: USE 是会话级的, 池化连接会漂移 ——
	// 优先钉住一个物理连接(SessionExecerProvider), 在同一会话内 USE+Query
	if strings.TrimSpace(defaultDatabase) != "" && validIdentifier(defaultDatabase) {
		dbgType := fmt.Sprintf("%T", db)
		fmt.Fprintln(os.Stderr, "[dbg] USE branch db=", defaultDatabase, "type=", dbgType)
		if sp, ok := db.(gonavibase.SessionExecerProvider); ok {
			fmt.Fprintln(os.Stderr, "[dbg] SessionExecerProvider OK")
			sess, serr := sp.OpenSessionExecer(ctx)
			if serr != nil {
				res.Error = serr.Error()
				return res, serr
			}
			defer func() { _ = sess.Close() }()
			if _, uerr := sess.Exec("USE " + defaultDatabase); uerr != nil {
				res.Error = uerr.Error()
				return res, uerr
			}
			qsess, qok := sess.(gonavibase.StatementQueryExecer)
			fmt.Fprintln(os.Stderr, "[dbg] StatementQueryExecer=", qok)
			if !qok {
				res.Error = "驱动会话不支持查询"
				return res, fmt.Errorf("驱动会话不支持查询")
			}
			rows, colNames, qerr := qsess.Query(sqlText)
			if qerr != nil {
				res.Error = qerr.Error()
				return res, qerr
			}
			res.Columns = colNames
			truncated := false
			for _, row := range rows {
				if len(res.Rows) >= maxRows {
					truncated = true
					break
				}
				vals := make([]any, len(colNames))
				for i, c := range colNames {
					vals[i] = row[c]
				}
				res.Rows = append(res.Rows, vals)
			}
			res.Truncated = truncated
			res.RowCount = len(res.Rows)
			res.DurationMs = time.Since(start).Milliseconds()
			return res, nil
		}
		// 驱动不支持固定会话: 无库上下文执行, SQL 需自带限定
	}

	// GoNavi Query/Exec 无 ctx 参数；语句超时由连接配置 QueryTimeout(秒)在驱动层生效。
	if isReadOnlySQL(sqlText) {
		// 只读多语句 → 逐条执行, 返回执行摘要(GoNavi 执行摘要同款信息结构)
		stmts := splitSQLStatements(sqlText)
		if len(stmts) > 1 {
			var summary []StatementResult
			for _, st := range stmts {
				stStart := time.Now()
				sr := StatementResult{SQL: st, Type: statementType(st)}
				rows, cols, err := db.Query(st)
				if err != nil {
					sr.Error = err.Error()
					sr.DurationMs = time.Since(stStart).Milliseconds()
					summary = append(summary, sr)
					res.Statements = summary
					res.Error = err.Error()
					res.DurationMs = time.Since(start).Milliseconds()
					return res, err
				}
				if len(cols) > 0 {
					sr.Rows = len(rows)
					// 最后一条成功语句的结果作为主结果集展示
					res.Columns = cols
					res.Rows = res.Rows[:0]
					truncated := false
					for _, row := range rows {
						if len(res.Rows) >= maxRows {
							truncated = true
							break
						}
						vals := make([]any, len(cols))
						for i, c := range cols {
							vals[i] = row[c]
						}
						res.Rows = append(res.Rows, vals)
					}
					res.Truncated = truncated
					res.RowCount = len(res.Rows)
				}
				sr.DurationMs = time.Since(stStart).Milliseconds()
				summary = append(summary, sr)
			}
			res.Statements = summary
			res.DurationMs = time.Since(start).Milliseconds()
			return res, nil
		}
		rows, colNames, err := db.Query(sqlText)
		if err != nil {
			res.Error = err.Error()
			return res, err
		}
		res.Columns = colNames
		truncated := false
		for _, row := range rows {
			if len(res.Rows) >= maxRows {
				truncated = true
				break
			}
			vals := make([]any, len(colNames))
			for i, c := range colNames {
				vals[i] = row[c]
			}
			res.Rows = append(res.Rows, vals)
		}
		res.Truncated = truncated
		res.RowCount = len(res.Rows)
	} else {
		affected, err := db.Exec(sqlText)
		if err != nil {
			res.Error = err.Error()
			return res, err
		}
		res.Affected = affected
		res.Columns = []string{"affected_rows"}
		res.Rows = [][]any{{affected}}
		res.RowCount = 1
	}
	res.DurationMs = time.Since(start).Milliseconds()
	return res, nil
}

// isReadOnlySQL 判定是否走查询通道（与风险分类保守判定一致的文本层规则）。
// 写语句判定的完整版在 risk.go；这里只决定 Query/Exec 通道分流。
func isReadOnlySQL(sqlText string) bool {
	trimmed := strings.TrimSpace(stripSQLComments(sqlText))
	if trimmed == "" {
		return true
	}
	// 跳过前导括号
	for len(trimmed) > 0 && (trimmed[0] == '(' || trimmed[0] == ' ' || trimmed[0] == '\t' || trimmed[0] == '\n') {
		trimmed = strings.TrimLeft(trimmed, "( \t\n\r")
	}
	upper := strings.ToUpper(trimmed)
	for _, p := range []string{"SELECT", "SHOW", "DESCRIBE", "DESC", "EXPLAIN", "WITH", "HELP"} {
		if strings.HasPrefix(upper, p) {
			// WITH ... INSERT/UPDATE/DELETE (可写 CTE) 归为写通道
			if p == "WITH" {
				u := strings.ToUpper(trimmed)
				if containsWord(u, "INSERT") || containsWord(u, "UPDATE") || containsWord(u, "DELETE") {
					return false
				}
			}
			return true
		}
	}
	return false
}

func containsWord(s, word string) bool {
	for i := 0; i+len(word) <= len(s); i++ {
		if s[i:i+len(word)] != word {
			continue
		}
		before := byte(' ')
		after := byte(' ')
		if i > 0 {
			before = s[i-1]
		}
		if i+len(word) < len(s) {
			after = s[i+len(word)]
		}
		if !isAlphaNumUnderscore(before) && !isAlphaNumUnderscore(after) {
			return true
		}
	}
	return false
}

func isAlphaNumUnderscore(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
}
