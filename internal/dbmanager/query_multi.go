// 多语句执行摘要(借鉴 GoNavi QueryEditorResultsPanel 执行摘要):
// 按顶层分号逐条拆分执行, 返回 statements[]; 出错即停。
package dbmanager

import (
	"strings"

	syncpkg "opscore/internal/dbmanager/sync"
)

// lexerRules 是分句要看的"方言差异"。看着像细节, 实际上每一条都会造成**错切**:
// 切错不是报错, 而是把一条语句变成两条能跑的语句(或者反过来), 那比崩掉危险得多。
type lexerRules struct {
	hashComment bool // MySQL 系才有 `#` 行注释(PG 里 # 是普通字符)
	backtick    bool // MySQL 系用反引号引用标识符
	dollarQuote bool // $tag$...$tag$ 只有 PG 有。MySQL 系必须关掉:
	// 那边脚本里的 `DELIMITER $$` 会让第一个 `$$` 被当成"开始且永不结束"的引用, 把后面整段吞进去。
	backslashEsc bool // 字符串里的 `\'` 算转义 —— **只有 MySQL 系成立**;
	// PG 在 standard_conforming_strings=on(默认)下 `'\'` 是"内容是反斜杠"的合法字符串,
	// 只有 E'...' 才启用反斜杠转义。照 MySQL 的规则切 PG, 会把一条语句劈成两条。
	nestedBlock bool // PG 的 /* */ 可以嵌套, MySQL 不行
	dashNeedsWS bool // MySQL 要求 `--` 后面是空白/行尾才算注释, 所以 `5--1` 是减法
}

func rulesFor(d syncpkg.Dialect) lexerRules {
	switch d {
	case syncpkg.DialectMySQL:
		return lexerRules{hashComment: true, backtick: true, backslashEsc: true, dashNeedsWS: true}
	case syncpkg.DialectPostgres:
		return lexerRules{nestedBlock: true, dollarQuote: true}
	default:
		return lexerRules{backtick: true, backslashEsc: true, dashNeedsWS: true}
	}
}

// isWordBoundary 判断 `--` 后面跟的是不是空白/行尾(MySQL 的注释规则)。
func isWordBoundary(s string, i int) bool {
	if i >= len(s) {
		return true
	}
	switch s[i] {
	case ' ', '\t', '\n', '\r':
		return true
	}
	return false
}

// splitSQLStatements 按顶层分隔符拆分语句(默认分号, 可被 MySQL 的 DELIMITER 改)。
// 引号/注释/dollar-quote 里的分隔符不算分隔符; 语句内的注释会跟着语句一起保留。
func splitSQLStatements(sqlText string, d syncpkg.Dialect) []string {
	r := rulesFor(d)
	delim := ";"
	var out []string
	var cur strings.Builder
	n := len(sqlText)

	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			out = append(out, s)
		}
		cur.Reset()
	}
	// 语句边界才可能遇到 DELIMITER(它不是 SQL 语句, 是客户端指令)
	tryDelimiter := func(i int) (int, bool) {
		if strings.TrimSpace(cur.String()) != "" {
			return i, false
		}
		rest := sqlText[i:]
		if len(rest) < 9 || !strings.EqualFold(rest[:9], "delimiter") {
			return i, false
		}
		if !isWordBoundary(rest, 9) {
			return i, false
		}
		line := rest
		if k := strings.IndexByte(line, '\n'); k >= 0 {
			line = line[:k]
		}
		nd := strings.TrimSpace(line[len("delimiter"):])
		if nd == "" {
			return i, false
		}
		delim = nd
		return i + len(line), true
	}

	for i := 0; i < n; {
		ch := sqlText[i]

		if ch == '\'' || ch == '"' || (r.backtick && ch == '`') {
			// 反斜杠转义: MySQL 系全局生效; PG 只有 E'...' / B'...' 前缀才生效
			esc := r.backslashEsc
			if !r.backslashEsc && (ch == '\'') && i > 0 {
				p := sqlText[i-1]
				esc = p == 'E' || p == 'e' || p == 'B' || p == 'b'
			}
			if consumed := consumeQuoted(&cur, sqlText, i, ch, esc); consumed > i {
				i = consumed
				continue
			}
		}
		if r.hashComment && ch == '#' {
			i = consumeToNewline(&cur, sqlText, i)
			continue
		}
		if ch == '-' && i+1 < n && sqlText[i+1] == '-' && (!r.dashNeedsWS || isWordBoundary(sqlText, i+2)) {
			i = consumeToNewline(&cur, sqlText, i)
			continue
		}
		if ch == '/' && i+1 < n && sqlText[i+1] == '*' {
			i = consumeBlockComment(&cur, sqlText, i, r.nestedBlock)
			continue
		}
		if r.dollarQuote && ch == '$' {
			// PG 的 $$ / $tag$ 引用: 里面的分号绝不切, 优先级高于一切
			if end := dollarTagEnd(sqlText, i); end > i {
				cur.WriteString(sqlText[i:end])
				i = end
				continue
			}
		}
		if j, ok := tryDelimiter(i); ok {
			i = j
			continue
		}
		if strings.HasPrefix(sqlText[i:], delim) {
			flush()
			i += len(delim)
			continue
		}
		cur.WriteByte(ch)
		i++
	}
	flush()
	return out
}

// consumeQuoted 从 sql[i] 的引号开始吃完整段字面量/标识符, 返回结束后的下标。
// 认两种结束方式: 紧跟的同一个引号(`'` 双写表示一个字符)与反斜杠转义(仅 esc=true)。
func consumeQuoted(buf *strings.Builder, sql string, i int, quote byte, esc bool) int {
	n := len(sql)
	if i >= n || sql[i] != quote {
		return i
	}
	buf.WriteByte(sql[i])
	i++
	for i < n {
		c := sql[i]
		if esc && c == '\\' && i+1 < n {
			buf.WriteByte(c)
			buf.WriteByte(sql[i+1])
			i += 2
			continue
		}
		if c == quote {
			if i+1 < n && sql[i+1] == quote { // '' 转义
				buf.WriteByte(c)
				buf.WriteByte(sql[i+1])
				i += 2
				continue
			}
			buf.WriteByte(c)
			return i + 1
		}
		buf.WriteByte(c)
		i++
	}
	return i
}

func consumeToNewline(buf *strings.Builder, sql string, i int) int {
	n := len(sql)
	for i < n && sql[i] != '\n' {
		buf.WriteByte(sql[i])
		i++
	}
	return i
}

func consumeBlockComment(buf *strings.Builder, sql string, i int, nested bool) int {
	n := len(sql)
	depth := 0
	for i < n {
		if strings.HasPrefix(sql[i:], "/*") && (depth == 0 || nested) {
			depth++
			buf.WriteString("/*")
			i += 2
			continue
		}
		if depth > 0 && strings.HasPrefix(sql[i:], "*/") {
			depth--
			buf.WriteString("*/")
			i += 2
			if depth == 0 {
				return i
			}
			continue
		}
		buf.WriteByte(sql[i])
		i++
	}
	return i
}

// dollarTagEnd 若 i 处是 PG 的 $tag$ 起始, 返回配平结束后的下标; 否则返回 i(表示不是)。
func dollarTagEnd(sql string, i int) int {
	n := len(sql)
	if i >= n || sql[i] != '$' {
		return i
	}
	j := i + 1
	for j < n && (sql[j] == '_' || (sql[j] >= '0' && sql[j] <= '9') || (sql[j] >= 'a' && sql[j] <= 'z') || (sql[j] >= 'A' && sql[j] <= 'Z')) {
		j++
	}
	if j >= n || sql[j] != '$' {
		return i // 不是 $tag$, 是普通字符(如 PG 的 $1 占位符)
	}
	tag := sql[i : j+1]
	k := j + 1
	for k < n {
		if sql[k] == '$' && strings.HasPrefix(sql[k:], tag) {
			return k + len(tag)
		}
		k++
	}
	return n // 没配平: 整段吃到结尾, 不猜
}

// classifyBatchRisk 取一批语句里的**最高**风险档。
// 多语句必须逐条判: classifySQLRisk 只看首个关键词, 于是 `SELECT 1; DROP TABLE t` 会被判成只读 ——
// 写锁、二次确认、审计三道就全被绕过去了。放开多语句写通道之前先补这一层。
func classifyBatchRisk(engine, sqlText string) (SqlRisk, string) {
	stmts := splitSQLStatements(sqlText, syncpkg.EngineDialect(engine))
	if len(stmts) == 0 {
		return classifySQLRisk(engine, sqlText) // 空/纯注释: 交给原判定给"空语句"
	}
	risk, reason := RiskSafe, ""
	for _, st := range stmts {
		if rk, rs := classifySQLRisk(engine, st); rk != risk && rk.AtLeast(risk) {
			risk, reason = rk, rs
		}
	}
	return risk, reason
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
