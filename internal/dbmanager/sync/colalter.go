package sync

// 列级 DDL 生成 —— 全站唯一的一份。
//
// 为什么在 sync 而不是 dbmanager 的某个 handler 里: "改一列该发什么语句"是**方言知识**
// (MySQL 系一列一条 MODIFY/CHANGE, PG 族要拆成 改名/类型/可空/默认/注释 多条),
// 表结构编辑器和结构对比都要用它。放两处就会有第五个"前端手搓 SQL"级别的漂移点。
//
// 调用方负责: 表引用(tableRef)已按方言引用好、标识符白名单校验、执行前的风险拦截与审计。

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	gonaviDB "opscore/internal/dbmanager/gonavi/db"
)

// ColumnAlter 一次列变更的**意图**(不是 SQL)。OrigName 仅 modify 用得上(改名时是旧名)。
type ColumnAlter struct {
	Kind     string // add | drop | modify
	Name     string
	OrigName string
	Type     string
	Nullable bool
	Default  string
	Comment  string
}

// 列类型白名单式校验: 类型名 + 可选(长度[,标度]) + 少量后缀词(int unsigned / timestamp with time zone)。
// 引号、分号、注释符都不在允许字符里 —— 这是用户可编辑字段, 不能当 SQL 片段用。
var reColType = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(\s*\(\s*\d{1,10}\s*(,\s*\d{1,10}\s*)?\s*\))?(\s+[A-Za-z][A-Za-z0-9_]*){0,4}$`)

// 允许**按表达式**发出去的默认值: 数字 / NULL / 布尔 / 当前时间族。其余一律转成字符串字面量。
// (用户填 'abc' 要得到 DEFAULT 'abc'; 填 CURRENT_TIMESTAMP 才得到裸表达式。)
var reDefaultExpr = regexp.MustCompile(`(?i)^(NULL|TRUE|FALSE|-?\d+(\.\d+)?|CURRENT_(TIMESTAMP|DATE|TIME)(\(\d*\))?|SYSDATE|LOCALTIME(STAMP)?(\(\d*\))?)$`)

// SafeColType 供 UI 侧先行校验复用(编辑器改类型时能立刻报错, 不用等一次往返)。
func SafeColType(t string) error {
	t = strings.TrimSpace(t)
	if t == "" {
		return errors.New("列类型不能为空")
	}
	if !reColType.MatchString(t) {
		return errors.New("列类型不合法: " + t)
	}
	return nil
}

// defaultClause 生成 DEFAULT 片段(含前导空格); 空字符串 = 不带默认值。
func defaultClause(engine, d string) string {
	d = strings.TrimSpace(d)
	if d == "" {
		return ""
	}
	if reDefaultExpr.MatchString(d) {
		return " DEFAULT " + d
	}
	return " DEFAULT " + gonaviDB.FormatLiteralForDialect(engine, d)
}

// BuildColumnAlters 按方言把列变更意图翻成语句列表(顺序即执行顺序)。
// d == DialectMySQL 时每个 modify 一列一条完整定义; 其余(标准/PG 族)一个属性一条。
func BuildColumnAlters(engine string, d Dialect, tableRef string, cols []ColumnAlter) ([]string, error) {
	qi := func(n string) string { return QuoteIdent(n, d) }
	mysql := d == DialectMySQL
	var out []string
	for i, c := range cols {
		if !validIdent(c.Name) {
			return nil, fmt.Errorf("第 %d 条列名非法: %s", i+1, c.Name)
		}
		switch c.Kind {
		case "drop":
			out = append(out, "ALTER TABLE "+tableRef+" DROP COLUMN "+qi(c.Name))
			continue
		case "add", "modify":
		default:
			return nil, fmt.Errorf("第 %d 条 kind 只支持 add/drop/modify", i+1)
		}
		if err := SafeColType(c.Type); err != nil {
			return nil, fmt.Errorf("第 %d 条: %w", i+1, err)
		}
		if c.Kind == "modify" && c.OrigName != "" && c.OrigName != c.Name && !validIdent(c.OrigName) {
			return nil, fmt.Errorf("第 %d 条原列名非法: %s", i+1, c.OrigName)
		}
		typ := strings.TrimSpace(c.Type)
		def := defaultClause(engine, c.Default)
		nullPart := ""
		if !c.Nullable {
			nullPart = " NOT NULL"
		}
		cmt := strings.TrimSpace(c.Comment)

		if mysql {
			// MySQL 系: 一列一条完整定义(MODIFY/CHANGE 都按新定义重建列)
			full := qi(c.Name) + " " + typ + nullPart + def
			if cmt != "" {
				full += " COMMENT " + gonaviDB.FormatLiteralForDialect(engine, cmt)
			}
			switch {
			case c.Kind == "add":
				out = append(out, "ALTER TABLE "+tableRef+" ADD COLUMN "+full)
			case c.OrigName != "" && c.OrigName != c.Name:
				// MariaDB 没有 RENAME COLUMN, CHANGE COLUMN 两边都认
				out = append(out, "ALTER TABLE "+tableRef+" CHANGE COLUMN "+qi(c.OrigName)+" "+full)
			default:
				out = append(out, "ALTER TABLE "+tableRef+" MODIFY COLUMN "+full)
			}
			continue
		}

		// 标准/PG 族: 一个属性一条语句, 顺序 = 改名 → 类型 → 可空 → 默认 → 注释
		if c.Kind == "add" {
			out = append(out, "ALTER TABLE "+tableRef+" ADD COLUMN "+qi(c.Name)+" "+typ+nullPart+def)
		} else {
			if c.OrigName != "" && c.OrigName != c.Name {
				out = append(out, "ALTER TABLE "+tableRef+" RENAME COLUMN "+qi(c.OrigName)+" TO "+qi(c.Name))
			}
			col := qi(c.Name)
			out = append(out, "ALTER TABLE "+tableRef+" ALTER COLUMN "+col+" TYPE "+typ)
			if c.Nullable {
				out = append(out, "ALTER TABLE "+tableRef+" ALTER COLUMN "+col+" DROP NOT NULL")
			} else {
				out = append(out, "ALTER TABLE "+tableRef+" ALTER COLUMN "+col+" SET NOT NULL")
			}
			if strings.TrimSpace(c.Default) != "" {
				out = append(out, "ALTER TABLE "+tableRef+" ALTER COLUMN "+col+" SET"+def)
			} else {
				out = append(out, "ALTER TABLE "+tableRef+" ALTER COLUMN "+col+" DROP DEFAULT")
			}
		}
		if cmt != "" {
			out = append(out, "COMMENT ON COLUMN "+tableRef+"."+qi(c.Name)+" IS "+gonaviDB.FormatLiteralForDialect(engine, cmt))
		}
	}
	if len(out) == 0 {
		return nil, errors.New("没有可执行的列变更")
	}
	return out, nil
}

// validIdent 标识符白名单: 只放字母数字下划线与 $, 长度封顶。
// (sync 包原本没有这个判断, 从 dbmanager 的口径抄一份 —— 两处规则要一致。)
func validIdent(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '$':
		default:
			return false
		}
	}
	return true
}
