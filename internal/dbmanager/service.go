// DBService —— ADR-001 分层边界：thin handler 只依赖此接口，
// 实现委托给 GoNavi 底座（internal/dbmanager/gonavi/db）。
// 未来更换底座或拆分独立服务时，仅需替换实现，handlers 与前端契约不变。
package dbmanager

import (
	"context"
	"fmt"
	"strings"
	"time"

	gonavibase "opscore/internal/dbmanager/gonavi/db"
	gonaviConnection "opscore/internal/dbmanager/gonavi/connection"
)

// DBService 数据库管理服务接口。
type DBService interface {
	// TestConnection 建立临时连接测试连通性，返回版本描述。
	TestConnection(ctx context.Context, conn *Connection) (string, error)
	// ListDatabases 列出可访问的数据库。
	ListDatabases(ctx context.Context, connID string) ([]string, error)
	// ListTables 列出指定库下的表/视图。
	ListTables(ctx context.Context, connID, database string) ([]TableInfo, error)
	// DescribeTable 返回列/索引/DDL。
	DescribeTable(ctx context.Context, connID, database, table string) ([]ColumnInfo, []IndexInfo, string, error)
	// ExecQuery 执行 SQL：SELECT 返回结果集(截断到 maxRows)，其他返回受影响行数。
	ExecQuery(ctx context.Context, connID, sqlText string, maxRows int) (*QueryResult, error)
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

func (s *GonaviService) ExecQuery(ctx context.Context, connID, sqlText string, maxRows int) (*QueryResult, error) {
	db, _, err := s.pool.Acquire(connID)
	if err != nil {
		return nil, err
	}
	if maxRows <= 0 {
		maxRows = MaxQueryRows
	}

	start := time.Now()
	res := &QueryResult{}

	// GoNavi Query/Exec 无 ctx 参数；语句超时由连接配置 QueryTimeout(秒)在驱动层生效。
	if isReadOnlySQL(sqlText) {
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
