// 多语句执行摘要(借鉴 GoNavi QueryEditorResultsPanel 执行摘要):
// 只读多语句按顶层分号拆分逐条执行, 返回 statements[]; 出错即停。
package dbmanager

import (
	"strings"
)

// splitSQLStatements 按顶层分号拆分(感知单引号/反引号/注释, 不切字符串字面量内分号)。
func splitSQLStatements(sqlText string) []string {
	var out []string
	var cur strings.Builder
	inS, inB, inLineC, inBlockC := false, false, false, false
	for i := 0; i < len(sqlText); i++ {
		ch := sqlText[i]
		switch {
		case inLineC:
			if ch == '\n' {
				inLineC = false
			}
			cur.WriteByte(ch)
		case inBlockC:
			if ch == '*' && i+1 < len(sqlText) && sqlText[i+1] == '/' {
				inBlockC = false
			}
			cur.WriteByte(ch)
		case inS:
			if ch == '\\' && i+1 < len(sqlText) {
				cur.WriteByte(ch)
				i++
				cur.WriteByte(sqlText[i])
				continue
			}
			cur.WriteByte(ch)
			if ch == '\'' {
				inS = false
			}
		case inB:
			cur.WriteByte(ch)
			if ch == '`' {
				inB = false
			}
		case ch == '\'':
			inS = true
			cur.WriteByte(ch)
		case ch == '`':
			inB = true
			cur.WriteByte(ch)
		case ch == '-' && i+1 < len(sqlText) && sqlText[i+1] == '-':
			inLineC = true
			cur.WriteByte(ch)
		case ch == '/' && i+1 < len(sqlText) && sqlText[i+1] == '*':
			inBlockC = true
			cur.WriteByte(ch)
		case ch == ';':
			if strings.TrimSpace(cur.String()) != "" {
				out = append(out, strings.TrimSpace(cur.String()))
			}
			cur.Reset()
		default:
			cur.WriteByte(ch)
		}
	}
	if strings.TrimSpace(cur.String()) != "" {
		out = append(out, strings.TrimSpace(cur.String()))
	}
	return out
}

// StatementResult 单条语句执行结果(执行摘要行)。
type StatementResult struct {
	SQL        string `json:"sql"`
	Type       string `json:"type"` // SELECT / WRITE / DDL / OTHER
	Rows       int    `json:"rows"`
	Affected   int64  `json:"affected"`
	DurationMs int64  `json:"durationMs"`
	Error      string `json:"error,omitempty"`
}

// statementType 粗分类(与风险拦截共用关键词感知)。
func statementType(stmt string) string {
	t := strings.ToUpper(strings.TrimSpace(stripSQLComments(stmt)))
	switch {
	case strings.HasPrefix(t, "SELECT") || strings.HasPrefix(t, "WITH") ||
		strings.HasPrefix(t, "SHOW") || strings.HasPrefix(t, "EXPLAIN") ||
		strings.HasPrefix(t, "DESC") || strings.HasPrefix(t, "DESCRIBE"):
		return "SELECT"
	case strings.HasPrefix(t, "INSERT"), strings.HasPrefix(t, "UPDATE"),
		strings.HasPrefix(t, "DELETE"), strings.HasPrefix(t, "REPLACE"),
		strings.HasPrefix(t, "MERGE"):
		return "WRITE"
	case strings.HasPrefix(t, "CREATE"), strings.HasPrefix(t, "ALTER"),
		strings.HasPrefix(t, "DROP"), strings.HasPrefix(t, "TRUNCATE"),
		strings.HasPrefix(t, "RENAME"):
		return "DDL"
	}
	return "OTHER"
}
