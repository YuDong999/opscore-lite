// 参数化查询(P1-9 方案 A: 服务端占位符替换)。
//
// 为什么不做"真 prepared": 底座执行接口是 ExecContext(ctx, query) / QueryContext(ctx, query),
// **没有 args 形参**; 要加就得动 StatementExecer 接口 + 所有 impl + driver-agent 的协议
// (清单估 8 人天, 且波及面远超 dbmanager)。方案 A 当天可落地且不动底座。
//
// 防注入的依据是**类型感知转义**(复用 sync.QuoteValue 那套方言规则), 不是数据库侧绑定 ——
// 这一点必须写清楚, 不能让人以为我们有真 prepared。口径与清单 ④ 的建议一致。
//
// 三个刻意的决定:
//  1. **只认 `:name`, 不认 `@name`** —— MySQL 的 `@x` 是用户变量, 当成占位符会把
//     `SET @x = 1` 这类语句改坏。Oracle/PG 的绑定变量写法本来就是 `:name`。
//  2. **params 为空时整段不重写** —— 用户没在用参数功能, 他的 SQL 里出现 `:` 是合法的
//     (JSON 路径、时间字面量等, 通常带引号)。只有真的传了参数才进入替换语义,
//     此时**任何对不上的 `:name` 都报错**(fail closed), 而不是留给数据库去报一句看不懂的错。
//  3. 字符串/注释/标识符里的 `:name` 一律不动 —— 复用分句器那套词法(引号/dollar-quote/
//     注释)。`::` 是 PG 的强制类型转换, 必须跳过。
package dbmanager

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	syncpkg "opscore/internal/dbmanager/sync"
)

// SQLParam 一个请求级参数。Value 一律以字符串传递, 由 Type 决定怎么变成字面量 ——
// JSON 里数字精度(19 位大整数)会被 float 吃掉, 用字符串才不丢。
type SQLParam struct {
	Name  string `json:"name"`
	Type  string `json:"type"`  // string(默认) | number | bool | null | raw
	Value string `json:"value"`
}

// ParamPlaceholderRE 的形式定义(实现里手写词法, 这里只作文档): `:name`, name 为
// [A-Za-z_][A-Za-z0-9_]*。

// SubstituteParams 把 SQL 里的 `:name` 换成方言感知的字面量。
// params 为空时原样返回(不进入替换语义)。
func SubstituteParams(sqlText string, params []SQLParam, dialect syncpkg.Dialect) (string, error) {
	if len(params) == 0 {
		return sqlText, nil
	}
	byName := make(map[string]SQLParam, len(params))
	for _, p := range params {
		if p.Name == "" {
			return "", fmt.Errorf("参数名不能为空")
		}
		byName[strings.ToLower(p.Name)] = p
	}

	var out strings.Builder
	used := map[string]bool{}
	n := len(sqlText)
	i := 0
	for i < n {
		ch := sqlText[i]

		// 字符串 / 标识符 / 注释: 整段抄过去, 里面的 `:name` 不动
		if ch == '\'' || ch == '"' || ch == '`' {
			end := skipQuoted(sqlText, i, ch)
			out.WriteString(sqlText[i:end])
			i = end
			continue
		}
		if ch == '-' && i+1 < n && sqlText[i+1] == '-' {
			end := skipToNewline(sqlText, i)
			out.WriteString(sqlText[i:end])
			i = end
			continue
		}
		if ch == '/' && i+1 < n && sqlText[i+1] == '*' {
			end := skipBlockComment(sqlText, i, dialect == syncpkg.DialectPostgres)
			out.WriteString(sqlText[i:end])
			i = end
			continue
		}
		if ch == '#' && dialect == syncpkg.DialectMySQL {
			end := skipToNewline(sqlText, i)
			out.WriteString(sqlText[i:end])
			i = end
			continue
		}
		if ch == '$' && dialect == syncpkg.DialectPostgres {
			if end := dollarTagEnd(sqlText, i); end > i {
				out.WriteString(sqlText[i:end])
				i = end
				continue
			}
		}
		// `::` 是 PG 的强制类型转换, 跳过
		if ch == ':' {
			if i+1 < n && sqlText[i+1] == ':' {
				out.WriteString("::")
				i += 2
				continue
			}
			if name, end := readPlaceholder(sqlText, i); name != "" {
				p, ok := byName[strings.ToLower(name)]
				if !ok {
					return "", fmt.Errorf("SQL 里的 :%s 没有对应参数(请在参数里补上, 或把它从语句里去掉)", name)
				}
				lit, err := paramLiteral(p, dialect)
				if err != nil {
					return "", fmt.Errorf("参数 %s: %w", name, err)
				}
				out.WriteString(lit)
				used[strings.ToLower(name)] = true
				i = end
				continue
			}
		}
		out.WriteByte(ch)
		i++
	}

	// 给了但没用上的参数: 报出来。静默忽略会让人以为"参数生效了"。
	var unused []string
	for name := range byName {
		if !used[name] {
			unused = append(unused, name)
		}
	}
	if len(unused) > 0 {
		sort.Strings(unused)
		return "", fmt.Errorf("这些参数在 SQL 里没用到: %s", strings.Join(unused, ", "))
	}
	return out.String(), nil
}

// readPlaceholder 从 i 处的 ':' 读一个占位符名。不是合法占位符时返回 ("", i)。
func readPlaceholder(s string, i int) (string, int) {
	if i >= len(s) || s[i] != ':' {
		return "", i
	}
	j := i + 1
	if j >= len(s) || !isParamStart(s[j]) {
		return "", i
	}
	for j < len(s) && isParamChar(s[j]) {
		j++
	}
	return s[i+1 : j], j
}

func isParamStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isParamChar(c byte) bool {
	return isParamStart(c) || (c >= '0' && c <= '9')
}

// paramLiteral 把参数变成 SQL 字面量。类型不认识时报错, 不猜。
func paramLiteral(p SQLParam, dialect syncpkg.Dialect) (string, error) {
	switch strings.ToLower(strings.TrimSpace(p.Type)) {
	case "", "string", "text":
		return syncpkg.QuoteValue(p.Value, dialect, ""), nil
	case "number":
		// 只放行"看起来是数"的: 直接内联意味着这里绝不能塞进非数字内容
		s := strings.TrimSpace(p.Value)
		if s == "" {
			return "NULL", nil
		}
		if !isNumericLiteralText(s) {
			return "", fmt.Errorf("%q 不是合法数字(要字符串请把类型改成 string)", p.Value)
		}
		return s, nil
	case "bool", "boolean":
		switch strings.ToLower(strings.TrimSpace(p.Value)) {
		case "1", "true", "t", "yes", "y":
			return syncpkg.QuoteValue(true, dialect, ""), nil
		case "0", "false", "f", "no", "n", "":
			return syncpkg.QuoteValue(false, dialect, ""), nil
		}
		return "", fmt.Errorf("%q 不是合法布尔值", p.Value)
	case "null":
		return "NULL", nil
	case "raw":
		// 明确要求"当 SQL 片段内联": 这是调用方自己承担责任的开关(如 IN (:ids) 传 "1,2,3")。
		// 面板上默认不出现这个选项。
		return strings.TrimSpace(p.Value), nil
	}
	return "", fmt.Errorf("未知的参数类型 %q(可用: string/number/bool/null/raw)", p.Type)
}

// isNumericLiteralText 判定一个字符串能不能当数字字面量裸发。
func isNumericLiteralText(s string) bool {
	if s == "" {
		return false
	}
	t := strings.TrimSpace(s)
	if _, err := strconv.ParseFloat(t, 64); err == nil {
		return true
	}
	// 再放行整型(19 位大整数 ParseFloat 会失真但能过; 这里额外确认全是数字)
	neg := false
	if strings.HasPrefix(t, "-") || strings.HasPrefix(t, "+") {
		neg = true
		t = t[1:]
	}
	if t == "" {
		return false
	}
	dot := false
	for _, c := range t {
		switch {
		case c >= '0' && c <= '9':
		case c == '.' && !dot:
			dot = true
		default:
			return false
		}
	}
	_ = neg
	return true
}

// skipQuoted 从 s[i] 的引号开始跳到结束(含结束引号)。
func skipQuoted(s string, i int, quote byte) int {
	n := len(s)
	i++
	for i < n {
		if s[i] == quote {
			if i+1 < n && s[i+1] == quote { // '' 转义
				i += 2
				continue
			}
			return i + 1
		}
		if s[i] == '\\' && i+1 < n { // 反斜杠转义保守处理: 两个一起跳, 免得 \' 被误判成结束
			i += 2
			continue
		}
		i++
	}
	return n
}

func skipToNewline(s string, i int) int {
	for i < len(s) && s[i] != '\n' {
		i++
	}
	return i
}

func skipBlockComment(s string, i int, nested bool) int {
	n := len(s)
	depth := 0
	for i < n {
		if strings.HasPrefix(s[i:], "/*") && (depth == 0 || nested) {
			depth++
			i += 2
			continue
		}
		if depth > 0 && strings.HasPrefix(s[i:], "*/") {
			depth--
			i += 2
			if depth == 0 {
				return i
			}
			continue
		}
		i++
	}
	return n
}
