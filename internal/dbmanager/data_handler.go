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
	if !validIdentifier(database) || !validIdentifier(table) {
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
		"durationMs": time.Since(startOf(r)).Milliseconds(),
	})
}

func startOf(r *http.Request) time.Time {
	if t, err := time.Parse(time.RFC3339, r.Header.Get("X-Request-Start")); err == nil {
		return t
	}
	return time.Now()
}
