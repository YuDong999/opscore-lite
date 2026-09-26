package dbmanager

import (
	"strings"
	"testing"

	"opscore/internal/dbmanager/sync"
)

func TestBuildAlterSQLsMySQL(t *testing.T) {
	cases := []struct {
		name string
		col  alterCol
		want []string
	}{
		{
			name: "加列带注释与字符串默认值",
			col:  alterCol{Kind: "add", Name: "note", Type: "VARCHAR(255)", Nullable: true, Default: "a;b", Comment: "备注"},
			want: []string{"ALTER TABLE `d`.`t` ADD COLUMN `note` VARCHAR(255) DEFAULT 'a;b' COMMENT '备注'"},
		},
		{
			name: "改列走 MODIFY, 默认值表达式裸发",
			col:  alterCol{Kind: "modify", Name: "ts", OrigName: "ts", Type: "TIMESTAMP", Nullable: false, Default: "CURRENT_TIMESTAMP"},
			want: []string{"ALTER TABLE `d`.`t` MODIFY COLUMN `ts` TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP"},
		},
		{
			name: "改名走 CHANGE( MariaDB 没有 RENAME COLUMN )",
			col:  alterCol{Kind: "modify", Name: "b", OrigName: "a", Type: "INT", Nullable: true},
			want: []string{"ALTER TABLE `d`.`t` CHANGE COLUMN `a` `b` INT"},
		},
		{name: "删列", col: alterCol{Kind: "drop", Name: "x"}, want: []string{"ALTER TABLE `d`.`t` DROP COLUMN `x`"}},
	}
	for _, c := range cases {
		got, err := buildAlterSQLs("mysql", sync.DialectMySQL, "`d`.`t`", []alterCol{c.col})
		if err != nil {
			t.Fatalf("%s: 非预期错误 %v", c.name, err)
		}
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%s:\n 得到 %v\n 期望 %v", c.name, got, c.want)
		}
	}
}

func TestBuildAlterSQLsPostgresOrdering(t *testing.T) {
	got, err := buildAlterSQLs("postgresql", sync.DialectPostgres, `"public"."users"`, []alterCol{
		{Kind: "modify", Name: "nick", OrigName: "nickname", Type: "TEXT", Nullable: false, Default: "", Comment: "昵称"},
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

// 注入面只有三个自由文本: 列类型、默认值、注释。类型走白名单, 后两者走字面量转义。
func TestBuildAlterSQLsRejectsHostileInput(t *testing.T) {
	bad := []alterCol{
		{Kind: "add", Name: "c", Type: "INT; DROP TABLE users"},
		{Kind: "add", Name: "c", Type: "VARCHAR(255)) DEFAULT 'x'"},
		{Kind: "add", Name: "c\" ; ", Type: "INT"},
		{Kind: "nope", Name: "c", Type: "INT"},
	}
	for _, c := range bad {
		if _, err := buildAlterSQLs("mysql", sync.DialectMySQL, "`d`.`t`", []alterCol{c}); err == nil {
			t.Errorf("应当拒绝但放过了: %+v", c)
		}
	}
	// 默认值里带引号/分号: 不能变成表达式发出去
	got, err := buildAlterSQLs("mysql", sync.DialectMySQL, "`d`.`t`",
		[]alterCol{{Kind: "add", Name: "c", Type: "VARCHAR(8)", Nullable: true, Default: "a' OR 1=1 --"}})
	if err != nil {
		t.Fatalf("非预期错误 %v", err)
	}
	if strings.Contains(got[0], "OR 1=1") && !strings.Contains(got[0], "''") {
		t.Errorf("默认值没按字面量转义: %s", got[0])
	}
}
