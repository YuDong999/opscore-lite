// 表数据分页浏览: /api/dbmanager/data
// 只读。标识符经白名单校验(字母数字下杠$), 值内联按方言转义, 复用 GoNavi 流式能力。
package dbmanager

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"opscore/internal/dbmanager/sync"
)

var reIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$]*$`)

// validIdentifier 校验库/表/列名, 防注入(不允许引号/点/空格等)。
func validIdentifier(s string) bool {
	return reIdentifier.MatchString(s)
}

func (h *Handlers) handleData(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	q := r.URL.Query()
	id := q.Get("id")
	if !reConnID.MatchString(id) {
		writeErr(w, "id 格式非法", http.StatusBadRequest)
		return
	}
	database, table := q.Get("database"), q.Get("table")
	if !validIdentifier(database) {
		writeErr(w, "database 名非法", http.StatusBadRequest)
		return
	}
	// PG 族表名可为 "schema.table" 两段(gonavi GetTables 返回带前缀名)
	tableSchema := ""
	if i := strings.Index(table, "."); i >= 0 {
		tableSchema, table = table[:i], table[i+1:]
	}
	if !validIdentifier(table) || (tableSchema != "" && !validIdentifier(tableSchema)) {
		writeErr(w, "database/table 名非法", http.StatusBadRequest)
		return
	}
	for _, col := range strings.Split(q.Get("orderBy"), ",") {
		if col != "" && !validIdentifier(col) {
			writeErr(w, "排序列名非法", http.StatusBadRequest)
			return
		}
	}
	if cond := q.Get("where"); cond != "" && (strings.Contains(strings.ToLower(cond), "union") || strings.Contains(cond, ";")) {
		writeErr(w, "where 条件非法", http.StatusBadRequest)
		return
	}

	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(q.Get("pageSize"))
	switch {
	case pageSize < 1:
		pageSize = 100
	case pageSize > 1000:
		pageSize = 1000
	}
	orderDir := strings.ToUpper(q.Get("orderDir"))
	if orderDir != "ASC" && orderDir != "DESC" {
		orderDir = "ASC"
	}
	offset := (page - 1) * pageSize

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	db, engine, err := h.pool.AcquireForSync(id)
	if err != nil {
		writeErr(w, "连接不可用: "+err.Error(), http.StatusBadRequest)
		return
	}
	dialect := sync.EngineDialect(engine)
	if dialect == "" {
		writeErr(w, "该引擎暂不支持数据浏览", http.StatusBadRequest)
		return
	}

	// 引号标识符
	qi := func(n string) string { return sync.QuoteIdent(n, dialect) }
	tn := qi(database) + "." + qi(table)
	if tableSchema != "" {
		// PG 族: "schema.table" 按两段引用(search_path 生效), 库前缀由连接上下文决定
		tn = qi(tableSchema) + "." + qi(table)
	}

	where := ""
	if w := strings.TrimSpace(q.Get("where")); w != "" {
		where = " WHERE " + w
	}
	order := ""
	if ob := strings.TrimSpace(q.Get("orderBy")); ob != "" {
		order = " ORDER BY " + qi(ob) + " " + orderDir
	}

	// 总行数
	totalRows, err := sync.QueryScalar(ctx, db, fmt.Sprintf("SELECT COUNT(*) AS c FROM %s%s", tn, where))
	if err != nil {
		writeErr(w, "统计行数失败: "+err.Error(), http.StatusBadRequest)
		return
	}
	// 页数据(结果上限 pageSize+1 判断截断, 实际用 LIMIT)
	dataSQL := fmt.Sprintf("SELECT * FROM %s%s%s LIMIT %d OFFSET %d", tn, where, order, pageSize, offset)
	rows, fields, err := sync.QueryRows(ctx, db, dataSQL)
	if err != nil {
		writeErr(w, "读取数据失败: "+err.Error(), http.StatusBadRequest)
		return
	}
	// 行转列式
	out := make([][]any, 0, len(rows))
	for _, row := range rows {
		line := make([]any, len(fields))
		for i, f := range fields {
			line[i] = row[f]
		}
		out = append(out, line)
	}
	writeJSON(w, map[string]any{
		"columns":   fields,
		"rows":      out,
		"total":     totalRows,
		"page":      page,
		"pageSize":  pageSize,
		"durationMs": 0, // 客户端自行计时
	})
}


// ===== /api/dbmanager/table-inserts =====
// GET ?id&database&table&maxRows -> 生成全表 INSERT 语句文本(借鉴 GoNavi 复制全表为 INSERT)

func (h *Handlers) handleTableInserts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	q := r.URL.Query()
	id := q.Get("id")
	if !reConnID.MatchString(id) {
		writeErr(w, "id 格式非法", http.StatusBadRequest)
		return
	}
	database, table := q.Get("database"), q.Get("table")
	if !validIdentifier(database) {
		writeErr(w, "database 名非法", http.StatusBadRequest)
		return
	}
	tableSchema := ""
	if i := strings.Index(table, "."); i >= 0 {
		tableSchema, table = table[:i], table[i+1:]
	}
	if !validIdentifier(table) || (tableSchema != "" && !validIdentifier(tableSchema)) {
		writeErr(w, "table 名非法", http.StatusBadRequest)
		return
	}
	maxRows := 1000
	if n, err := strconv.Atoi(q.Get("maxRows")); err == nil && n > 0 && n <= 10000 {
		maxRows = n
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	db, engine, err := h.pool.AcquireForSync(id)
	if err != nil {
		writeErr(w, "连接不可用: "+err.Error(), http.StatusBadRequest)
		return
	}
	dialect := sync.EngineDialect(engine)
	if dialect == "" {
		writeErr(w, "该引擎暂不支持", http.StatusBadRequest)
		return
	}

	cols, err := db.GetColumns(database, table)
	if err != nil {
		writeErr(w, "读取表结构失败: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(cols) == 0 {
		writeErr(w, "表不存在或无列", http.StatusBadRequest)
		return
	}
	colNames := make([]string, 0, len(cols))
	colBases := make(map[string]string, len(cols))
	for _, c := range cols {
		colNames = append(colNames, c.Name)
		lt := strings.ToLower(c.Type)
		switch {
		case strings.Contains(lt, "tinyint(1)"), strings.Contains(lt, "boolean"), strings.Contains(lt, "bool"):
			colBases[strings.ToLower(c.Name)] = "boolean"
		case strings.Contains(lt, "blob"), strings.Contains(lt, "binary"), strings.Contains(lt, "bytea"):
			colBases[strings.ToLower(c.Name)] = "bytea"
		}
	}

	tn := sync.QuoteIdent(table, dialect)
	if tableSchema != "" {
		tn = sync.QuoteIdent(tableSchema, dialect) + "." + tn
	} else {
		tn = sync.QuoteIdent(database, dialect) + "." + tn
	}
	sqlText := "SELECT * FROM " + tn
	rows, _, err := sync.QueryRows(ctx, db, sqlText)
	if err != nil {
		writeErr(w, "读取数据失败: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(rows) > maxRows {
		rows = rows[:maxRows]
	}

	var b strings.Builder
	const perStmt = 100
	for start := 0; start < len(rows); start += perStmt {
		end := start + perStmt
		if end > len(rows) {
			end = len(rows)
		}
		b.WriteString("INSERT INTO " + tn + " (")
		for i, c := range colNames {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(sync.QuoteIdent(c, dialect))
		}
		b.WriteString(") VALUES\n")
		for ri, row := range rows[start:end] {
			if ri > 0 {
				b.WriteString(",\n")
			}
			b.WriteByte('(')
			for ci, c := range colNames {
				if ci > 0 {
					b.WriteByte(',')
				}
				b.WriteString(sync.QuoteValue(row[c], dialect, colBases[strings.ToLower(c)]))
			}
			b.WriteByte(')')
		}
		b.WriteString(";\n\n")
	}
	writeJSON(w, map[string]any{
		"text":     b.String(),
		"rows":     len(rows),
		"truncated": len(rows) >= maxRows,
	})
}
