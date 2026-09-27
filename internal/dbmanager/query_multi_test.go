package dbmanager

import (
	"strings"
	"testing"

	syncpkg "opscore/internal/dbmanager/sync"
)

// 分句切错的后果不是报错, 而是"一条语句被劈成两条都能跑的东西"。
// 所以每条方言规则都要有断言钉住, 特别是下面这些真实会出现在脚本里的写法。
func TestSplitSQLStatements(t *testing.T) {
	my, pg := syncpkg.DialectMySQL, syncpkg.DialectPostgres
	cases := []struct {
		name string
		d    syncpkg.Dialect
		sql  string
		want []string
	}{
		{"普通两条", my, "SELECT 1; SELECT 2", []string{"SELECT 1", "SELECT 2"}},
		{"尾部分号不产生空语句", my, "SELECT 1;", []string{"SELECT 1"}},
		{"字符串里的分号", my, "SELECT 'a;b'; SELECT 2", []string{"SELECT 'a;b'", "SELECT 2"}},
		{"反引号标识符里的分号", my, "SELECT `a;b` FROM t", []string{"SELECT `a;b` FROM t"}},
		{"双引号标识符里的分号", pg, `SELECT "a;b" FROM t`, []string{`SELECT "a;b" FROM t`}},
		{"MySQL 反斜杠转义不结束字符串", my, `SELECT 'it\'s; ok'; SELECT 2`, []string{`SELECT 'it\'s; ok'`, "SELECT 2"}},
		{"'' 双写不结束字符串", pg, "SELECT 'it''s; ok'; SELECT 2", []string{"SELECT 'it''s; ok'", "SELECT 2"}},
		// PG 默认 standard_conforming_strings=on: 反斜杠不是转义符。
		// 若照 MySQL 规则处理, 这条会被切成两半 —— 两个半句单独看都是错的。
		{"PG 普通串里的反斜杠不转义", pg, `SELECT '\'; SELECT 2`, []string{`SELECT '\'`, "SELECT 2"}},
		{"PG E 串里的反斜杠转义", pg, `SELECT E'\'; SELECT 2`, []string{`SELECT E'\'; SELECT 2`}},
		{"行注释里的分号", pg, "SELECT 1 -- 注释; 不切\n; SELECT 2", []string{"SELECT 1 -- 注释; 不切", "SELECT 2"}},
		{"MySQL 的 # 行注释", my, "SELECT 1 # 注释; 不切\n; SELECT 2", []string{"SELECT 1 # 注释; 不切", "SELECT 2"}},
		{"PG 里 # 不是注释", pg, "SELECT '#;' AS a; SELECT 2", []string{"SELECT '#;' AS a", "SELECT 2"}},
		// MySQL 规定 `--` 后必须跟空白才算注释, 所以 5--1 是减法; PG 无此限制。
		{"MySQL 的 5--1 是算术不是注释", my, "SELECT 5--1;", []string{"SELECT 5--1"}},
		{"PG 的 5--1 是注释", pg, "SELECT 5--1", []string{"SELECT 5--1"}},
		{"块注释里的分号", my, "/* 一段; 说明 */ SELECT 1", []string{"/* 一段; 说明 */ SELECT 1"}},
		{"PG 嵌套块注释", pg, "/* a /* b; */ c */ SELECT 1; SELECT 2", []string{"/* a /* b; */ c */ SELECT 1", "SELECT 2"}},
		{"MySQL 块注释不嵌套", my, "/* a /* b; */ SELECT 1", []string{"/* a /* b; */ SELECT 1"}},
		{"PG dollar-quote 函数体", pg,
			"CREATE FUNCTION f() RETURNS int AS $$ BEGIN RETURN 1; END; $$ LANGUAGE plpgsql; SELECT 2",
			[]string{"CREATE FUNCTION f() RETURNS int AS $$ BEGIN RETURN 1; END; $$ LANGUAGE plpgsql", "SELECT 2"}},
		{"PG 带 tag 的 dollar-quote", pg,
			"DO $body$ BEGIN PERFORM 1; END $body$; SELECT 2",
			[]string{"DO $body$ BEGIN PERFORM 1; END $body$", "SELECT 2"}},
		{"PG 的 $1 占位符不是 dollar-quote", pg, "SELECT $1; SELECT 2", []string{"SELECT $1", "SELECT 2"}},
		{"MySQL DELIMITER 重定义", my,
			"DELIMITER $$\nCREATE PROCEDURE p() BEGIN SELECT 1; SELECT 2; END$$\nDELIMITER ;\nSELECT 3",
			[]string{"CREATE PROCEDURE p() BEGIN SELECT 1; SELECT 2; END", "SELECT 3"}},
		{"注释跟着语句走", my, "-- 头部说明\nSELECT 1; -- 尾注释\nSELECT 2",
			[]string{"-- 头部说明\nSELECT 1", "-- 尾注释\nSELECT 2"}},
		{"空输入", my, "   \n ;  ", nil},
	}
	for _, c := range cases {
		got := splitSQLStatements(c.sql, c.d)
		if len(got) != len(c.want) {
			t.Errorf("%s: 切成 %d 段, 期望 %d 段\n  输入: %q\n  实际: %#v", c.name, len(got), len(c.want), c.sql, got)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: 第 %d 段不符\n  期望 %q\n  实际 %q", c.name, i+1, c.want[i], got[i])
			}
		}
	}
}

// 放开多语句写通道后, 风险判定必须逐条取最高 —— 否则写锁/二次确认会被首条只读语句骗过去。
func TestClassifyBatchRisk(t *testing.T) {
	cases := []struct {
		engine string
		sql    string
		want   SqlRisk
		why    string
	}{
		{"mysql", "SELECT 1", RiskSafe, "纯只读"},
		{"mysql", "SELECT 1; DROP TABLE t", RiskCritical, "藏了一条 DROP"},
		{"mysql", "SELECT 1; DELETE FROM t", RiskCritical, "藏了一条无 WHERE 的 DELETE(全表删)"},
		{"mysql", "SELECT 1; DELETE FROM t WHERE id=1", RiskMedium, "藏了一条带 WHERE 的 DELETE"},
		{"mysql", "SELECT 1; INSERT INTO t VALUES (1)", RiskMedium, "藏了一条写入"},
		{"mysql", "SELECT 1; USE otherdb", RiskHigh, "藏了切库语句"},
		{"mysql", "/* DROP TABLE x */ SELECT 1", RiskSafe, "注释里的关键字不算"},
		{"postgres", "SELECT 1; TRUNCATE t", RiskCritical, "PG 的 TRUNCATE"},
	}
	for _, c := range cases {
		got, reason := classifyBatchRisk(c.engine, c.sql)
		if got != c.want {
			t.Errorf("%s: %q 判成 %s(期望 %s) reason=%q", c.why, c.sql, got, c.want, reason)
		}
	}
}

// 切完不该把任何字面量内容丢掉, 也不该把一条语句劈成两条。
// 注意每条要按方言列清楚: 反引号在 PG 里**不是**引用符号, 那里切它才是对的。
func TestSplitKeepsEveryLiteral(t *testing.T) {
	both := []syncpkg.Dialect{syncpkg.DialectMySQL, syncpkg.DialectPostgres}
	cases := []struct {
		sql string
		d   []syncpkg.Dialect
	}{
		{"SELECT 'a;b' FROM t WHERE x = 'c;d'", both},
		{"INSERT INTO t VALUES ('x;y'), ('z;w')", both},
		{"SELECT \"q;w\" FROM t", both},
		{"SELECT `q;w` FROM `t;t`", []syncpkg.Dialect{syncpkg.DialectMySQL}},
		{"CREATE FUNCTION f() RETURNS int AS $$ SELECT 1; $$ LANGUAGE plpgsql", []syncpkg.Dialect{syncpkg.DialectPostgres}},
	}
	for _, c := range cases {
		for _, d := range c.d {
			got := splitSQLStatements(c.sql, d)
			if len(got) != 1 {
				t.Errorf("单条语句被切开(%v): %q -> %q", d, c.sql, got)
				continue
			}
			for _, frag := range []string{"a;b", "c;d", "x;y", "z;w", "q;w", "t;t"} {
				if strings.Contains(c.sql, frag) && !strings.Contains(got[0], frag) {
					t.Errorf("分句丢了 %q (%v): %q", frag, d, got[0])
				}
			}
		}
	}
}
