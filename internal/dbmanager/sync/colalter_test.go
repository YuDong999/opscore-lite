package sync

import (
	"strings"
	"testing"
)

func TestBuildColumnAltersMySQL(t *testing.T) {
	cases := []struct {
		name string
		col  ColumnAlter
		want string
	}{
		{
			name: "加列: 字符串默认值要转义, 注释跟着走",
			col:  ColumnAlter{Kind: "add", Name: "note", Type: "VARCHAR(255)", Nullable: true, Default: "hello;world", Comment: "备注"},
			want: "ALTER TABLE `d`.`t` ADD COLUMN `note` VARCHAR(255) DEFAULT 'hello;world' COMMENT '备注'",
		},
		{
			name: "改列: 一列一条完整定义, 表达式默认值裸发",
			col:  ColumnAlter{Kind: "modify", Name: "ts", OrigName: "ts", Type: "TIMESTAMP", Nullable: false, Default: "CURRENT_TIMESTAMP"},
			want: "ALTER TABLE `d`.`t` MODIFY COLUMN `ts` TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP",
		},
		{
			name: "改名走 CHANGE(MariaDB 没有 RENAME COLUMN)",
			col:  ColumnAlter{Kind: "modify", Name: "b", OrigName: "a", Type: "INT", Nullable: true},
			want: "ALTER TABLE `d`.`t` CHANGE COLUMN `a` `b` INT",
		},
		{name: "删列", col: ColumnAlter{Kind: "drop", Name: "x"}, want: "ALTER TABLE `d`.`t` DROP COLUMN `x`"},
	}
	for _, c := range cases {
		got, err := BuildColumnAlters("mysql", DialectMySQL, "`d`.`t`", []ColumnAlter{c.col})
		if err != nil {
			t.Fatalf("%s: 非预期错误 %v", c.name, err)
		}
		if len(got) != 1 || got[0] != c.want {
			t.Errorf("%s:\n 得到 %v\n 期望 %s", c.name, got, c.want)
		}
	}
}

func TestBuildColumnAltersPostgresOrdering(t *testing.T) {
	got, err := BuildColumnAlters("postgresql", DialectPostgres, `"public"."users"`, []ColumnAlter{
		{Kind: "modify", Name: "nick", OrigName: "nickname", Type: "TEXT", Nullable: false, Comment: "昵称"},
	})
	if err != nil {
		t.Fatalf("非预期错误 %v", err)
	}
	want := []string{
		`ALTER TABLE "public"."users" RENAME COLUMN "nickname" TO "nick"`,
		`ALTER TABLE "public"."users" ALTER COLUMN "nick" TYPE TEXT`,
		`ALTER TABLE "public"."users" ALTER COLUMN "nick" SET NOT NULL`,
		`ALTER TABLE "public"."users" ALTER COLUMN "nick" DROP DEFAULT`,
		`COMMENT ON COLUMN "public"."users"."nick" IS '昵称'`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("顺序或内容不对:\n got=%v\nwant=%v", got, want)
	}
}

// 用户可编辑的只有三个自由文本: 类型、默认值、注释。类型走白名单, 后两者走字面量转义。
func TestBuildColumnAltersRejectsHostileInput(t *testing.T) {
	bad := []ColumnAlter{
		{Kind: "add", Name: "c", Type: "INT; DROP TABLE users --"},
		{Kind: "add", Name: "c", Type: "VARCHAR(255)) DEFAULT 'x'"},
		{Kind: "add", Name: `c" ; `, Type: "INT"},
		{Kind: "nope", Name: "c", Type: "INT"},
		{Kind: "modify", Name: "n", OrigName: "a; DELETE FROM t", Type: "INT"},
	}
	for _, c := range bad {
		if _, err := BuildColumnAlters("mysql", DialectMySQL, "`d`.`t`", []ColumnAlter{c}); err == nil {
			t.Errorf("应当拒绝但放过了: %+v", c)
		}
	}
	// 默认值里带引号/分号: 不能当表达式发出去
	got, err := BuildColumnAlters("mysql", DialectMySQL, "`d`.`t`",
		[]ColumnAlter{{Kind: "add", Name: "c", Type: "VARCHAR(32)", Nullable: true, Default: "a' OR 1=1 --"}})
	if err != nil {
		t.Fatalf("非预期错误 %v", err)
	}
	if !strings.Contains(got[0], "''") || strings.HasSuffix(got[0], "--") {
		t.Errorf("默认值没按字面量转义: %s", got[0])
	}
}
