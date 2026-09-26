package sync

import (
	"strings"
	"testing"

	gonaviConnection "opscore/internal/dbmanager/gonavi/connection"
)

func col(name, typ, nullable, key string, def *string, extra ...string) gonaviConnection.ColumnDefinition {
	c := gonaviConnection.ColumnDefinition{Name: name, Type: typ, Nullable: nullable, Key: key, Default: def}
	if len(extra) > 0 {
		c.Extra = extra[0]
	}
	return c
}

func s(v string) *string { return &v }

func idx(name, c string, seq, nonUnique int) gonaviConnection.IndexDefinition {
	return gonaviConnection.IndexDefinition{Name: name, ColumnName: c, SeqInIndex: seq, NonUnique: nonUnique}
}

// 同族比对: 加列/删列/改列, 以及"显示宽度不该算差异"。
func TestDiffTableSameFamily(t *testing.T) {
	src := TableSnapshot{Table: "t", Columns: []gonaviConnection.ColumnDefinition{
		col("id", "int", "NO", "PRI", nil),
		col("name", "varchar(64)", "YES", "", nil),   // 类型与目标一致(int(11) vs int 见下条)
		col("wid", "int(11)", "YES", "", nil),        // MySQL 老写法
		col("src_only", "text", "YES", "", s("abc")), // 目标没有 -> 新增
	}}
	dst := TableSnapshot{Table: "t", Columns: []gonaviConnection.ColumnDefinition{
		col("id", "int", "NO", "PRI", nil),
		col("name", "varchar(255)", "NO", "", nil),  // 类型 + 可空都不一致
		col("wid", "int", "YES", "", nil),           // 应与 int(11) 等价
		col("dst_only", "datetime", "YES", "", nil), // 源没有 -> 删除(破坏性)
	}}
	d := DiffTable(src, dst, DialectMySQL, DialectMySQL, "mysql", "`d`.`t`", DiffOptions{})
	kinds := map[string]Change{}
	for _, ch := range d.Changes {
		kinds[ch.Object] = ch
	}
	if _, ok := kinds["wid"]; ok {
		t.Errorf("int(11) 与 int 被判成差异: %+v", kinds["wid"].Detail)
	}
	add := kinds["src_only"]
	if add.Kind != "column_add" || len(add.Sqls) != 1 ||
		!strings.Contains(add.Sqls[0], "ADD COLUMN `src_only` text DEFAULT 'abc'") {
		t.Errorf("新增列不对: %+v", add)
	}
	drop := kinds["dst_only"]
	if drop.Kind != "column_drop" || !drop.Destructive {
		t.Errorf("删列必须标破坏性: %+v", drop)
	}
	mod := kinds["name"]
	// 源侧是可空: MySQL 的 MODIFY 是整条定义替换, 省略 NOT NULL 就等于改成可空
	if mod.Kind != "column_modify" || !strings.Contains(mod.Sqls[0], "MODIFY COLUMN `name` varchar(64)") ||
		strings.Contains(mod.Sqls[0], "NOT NULL") {
		t.Errorf("改成可空不该带 NOT NULL: %+v sqls=%v", mod.Detail, mod.Sqls)
	}
	// 反方向: 源侧 NOT NULL 必须体现在语句里
	rev := DiffTable(
		TableSnapshot{Table: "t", Columns: []gonaviConnection.ColumnDefinition{col("c", "int", "NO", "", nil)}},
		TableSnapshot{Table: "t", Columns: []gonaviConnection.ColumnDefinition{col("c", "int", "YES", "", nil)}},
		DialectMySQL, DialectMySQL, "mysql", "`d`.`t`", DiffOptions{})
	if len(rev.Changes) != 1 || !strings.Contains(rev.Changes[0].Sqls[0], "`c` int NOT NULL") {
		t.Errorf("改成 NOT NULL 没落到语句里: %+v", rev.Changes)
	}
	if d.Action != "modify" {
		t.Errorf("Action=%s", d.Action)
	}
}

func TestDiffTableNoDifference(t *testing.T) {
	cols := []gonaviConnection.ColumnDefinition{col("id", "bigint", "NO", "PRI", nil)}
	d := DiffTable(TableSnapshot{Table: "t", Columns: cols}, TableSnapshot{Table: "t", Columns: cols},
		DialectMySQL, DialectMySQL, "mysql", "`d`.`t`", DiffOptions{})
	if d.Action != "none" || len(d.Changes) != 0 || len(d.Sqls) != 0 {
		t.Errorf("完全相同的表被判有差异: %+v", d.Changes)
	}
}

// 索引必须按 SeqInIndex 定序: 两列索引在源/目标里返回顺序不同, 也不能算差异。
// (排错了会生成一次无谓的 DROP INDEX + CREATE INDEX, 大表上就是几分钟的锁。)
func TestDiffTableIndexColumnOrder(t *testing.T) {
	src := TableSnapshot{Table: "t", Indexes: []gonaviConnection.IndexDefinition{
		idx("ix_ab", "b", 2, 1), idx("ix_ab", "a", 1, 1), // 故意乱序返回
	}}
	dst := TableSnapshot{Table: "t", Indexes: []gonaviConnection.IndexDefinition{
		idx("ix_ab", "a", 1, 1), idx("ix_ab", "b", 2, 1),
	}}
	d := DiffTable(src, dst, DialectPostgres, DialectPostgres, "postgres", `"public"."t"`, DiffOptions{Indexes: true})
	if len(d.Changes) != 0 {
		t.Fatalf("同一索引因列序被判成差异: %+v", d.Changes)
	}
	// 列集合真的不同时, 要给 drop+create
	dst2 := TableSnapshot{Table: "t", Indexes: []gonaviConnection.IndexDefinition{idx("ix_ab", "a", 1, 1)}}
	d2 := DiffTable(src, dst2, DialectPostgres, DialectPostgres, "postgres", `"public"."t"`, DiffOptions{Indexes: true})
	var kinds []string
	for _, ch := range d2.Changes {
		kinds = append(kinds, ch.Kind)
	}
	if strings.Join(kinds, ",") != "index_drop,index_add" {
		t.Errorf("缺列的索引应重建(drop+create), 实得 %v", kinds)
	}
	if !d2.Changes[0].Destructive {
		t.Error("index_drop 应标破坏性")
	}
	if !strings.Contains(strings.Join(d2.Changes[1].Sqls, "|"), `CREATE INDEX "ix_ab" ON "public"."t" ("a", "b")`) {
		t.Errorf("create 索引语句或列序不对: %v", d2.Changes[1].Sqls)
	}
}

// 跨方言: 类型差异只报告不自动生成 MODIFY, 但可以生成"同族可判定"的那部分。
func TestDiffTableCrossFamilyDoesNotGuessTypes(t *testing.T) {
	src := TableSnapshot{Table: "t", Columns: []gonaviConnection.ColumnDefinition{
		col("status", "enum('a','b')", "YES", "", nil),
	}}
	dst := TableSnapshot{Table: "t", Columns: []gonaviConnection.ColumnDefinition{
		col("status", "text", "YES", "", nil),
	}}
	d := DiffTable(src, dst, DialectMySQL, DialectPostgres, "postgres", `"public"."t"`, DiffOptions{})
	if len(d.Changes) != 1 {
		t.Fatalf("应报一条差异: %+v", d.Changes)
	}
	if len(d.Changes[0].Sqls) != 0 {
		t.Errorf("跨方言类型差异不该自动生成语句: %v", d.Changes[0].Sqls)
	}
	if !strings.Contains(strings.Join(d.Notes, "|"), "跨方言") {
		t.Errorf("要留下为什么不生成的说明, Notes=%v", d.Notes)
	}
}

// 主键差异只报告: 自动改主键牵涉索引名和外键依赖, 容易把表改死。
func TestDiffTablePrimaryKeyReportOnly(t *testing.T) {
	src := TableSnapshot{Table: "t", Columns: []gonaviConnection.ColumnDefinition{
		col("a", "int", "NO", "PRI", nil), col("b", "int", "NO", "PRI", nil),
	}}
	dst := TableSnapshot{Table: "t", Columns: []gonaviConnection.ColumnDefinition{
		col("a", "int", "NO", "PRI", nil), col("b", "int", "NO", "", nil),
	}}
	d := DiffTable(src, dst, DialectMySQL, DialectMySQL, "mysql", "`d`.`t`", DiffOptions{})
	var found bool
	for _, ch := range d.Changes {
		if ch.Kind == "pk_modify" {
			found = true
			if len(ch.Sqls) != 0 {
				t.Errorf("主键差异不该生成语句: %v", ch.Sqls)
			}
		}
	}
	if !found {
		t.Errorf("没识别出主键列差异: %+v", d.Changes)
	}
}

// 只在目标侧的表 = 删表, 属破坏性; 只在源侧 = 建表(走跨方言 DDL 生成)。
func TestDiffTableCreateAndDropOnly(t *testing.T) {
	cols := []gonaviConnection.ColumnDefinition{col("id", "int", "NO", "PRI", nil)}
	cr := CreateOnlyDiff(TableSnapshot{Table: "t", Columns: cols}, DialectMySQL, DialectPostgres, "postgres", "d", "t")
	if cr.Action != "create" || len(cr.Sqls) != 1 || !strings.Contains(strings.ToUpper(cr.Sqls[0]), "CREATE TABLE") {
		t.Errorf("建表结果不对: %+v sqls=%v", cr.Action, cr.Sqls)
	}
	// apply 端是按 change 收集语句的: 表级变更若只挂在 td 上, 勾选后就会静默不执行
	if len(cr.Changes) != 1 || len(cr.Changes[0].Sqls) != 1 {
		t.Errorf("table_create 的语句必须同时挂在 change 上: %+v", cr.Changes)
	}
	if cr.Changes[0].Key == "" {
		t.Errorf("change 缺 key, 前端无法勾选回传: %+v", cr.Changes[0])
	}
	dr := DropOnlyDiff(TableSnapshot{Table: "t"}, DialectMySQL, "`d`.`t`")
	if dr.Action != "delete" || !dr.Changes[0].Destructive {
		t.Errorf("删表必须标破坏性: %+v", dr.Changes)
	}
}

// 建表语句必须自带列注释: GenerateCreateDDL 不管注释, 少了这一步就是
// "apply 完还剩一条注释差异", 比对永远收不了口(2026-09-26 真库实测抓到)。
func TestCreateOnlyDiffCarriesComments(t *testing.T) {
	withCmt := gonaviConnection.ColumnDefinition{Name: "b", Type: "varchar(20)", Nullable: "YES", Comment: "乙"}
	cr := CreateOnlyDiff(TableSnapshot{Table: "t", Columns: []gonaviConnection.ColumnDefinition{withCmt}},
		DialectMySQL, DialectPostgres, "postgres", "public", "t")
	joined := strings.Join(cr.Sqls, "\n")
	if !strings.Contains(joined, `COMMENT ON COLUMN "public"."t"."b" IS '乙'`) {
		t.Errorf("PG 目标的建表没带列注释, 差异不会收敛:\n%s", joined)
	}
	// MySQL 目标: 注释内联在列定义里(不再是建表后补一条 MODIFY)
	cr2 := CreateOnlyDiff(TableSnapshot{Table: "t", Columns: []gonaviConnection.ColumnDefinition{withCmt}},
		DialectMySQL, DialectMySQL, "mysql", "d", "t")
	j2 := strings.Join(cr2.Sqls, "\n")
	if !strings.Contains(j2, "`b` varchar(20) COMMENT '乙'") {
		t.Errorf("MySQL 目标的建表没内联列注释:\n%s", j2)
	}
	if len(cr2.Sqls) != 1 {
		t.Errorf("MySQL 目标只需一条建表语句: %v", cr2.Sqls)
	}
	// 二级索引也随建表一起发: 以前被丢掉, 于是建完再比还剩一条"索引缺失"差异
	withIdx := gonaviConnection.IndexDefinition{Name: "ix_b", ColumnName: "b", NonUnique: 1, SeqInIndex: 1}
	cr3 := CreateOnlyDiff(TableSnapshot{Table: "t", Columns: []gonaviConnection.ColumnDefinition{withCmt}, Indexes: []gonaviConnection.IndexDefinition{withIdx}},
		DialectMySQL, DialectMySQL, "mysql", "d", "t")
	if !strings.Contains(strings.Join(cr3.Sqls, "\n"), "CREATE INDEX `ix_b`") {
		t.Errorf("建表没带上二级索引: %v", cr3.Sqls)
	}
}

// 前端直接 .map(sqls): 没有语句的差异也必须给空数组, 不能给 null(资源模块踩过同一个坑)。
func TestDiffNeverEmitsNilSlices(t *testing.T) {
	c := col("a", "int", "YES", "", nil)
	d := DiffTable(TableSnapshot{Table: "t", Columns: []gonaviConnection.ColumnDefinition{c}},
		TableSnapshot{Table: "t", Columns: []gonaviConnection.ColumnDefinition{c}},
		DialectMySQL, DialectMySQL, "mysql", "`d`.`t`", DiffOptions{})
	if d.Sqls == nil || d.Notes == nil {
		t.Errorf("表级 nil 切片: sqls=%v notes=%v", d.Sqls, d.Notes)
	}
	// 只报告不生成的那条(pk_modify)也要有非 nil 的 sqls
	c2 := col("k", "int", "NO", "PRI", nil)
	d2 := DiffTable(TableSnapshot{Table: "t", Columns: []gonaviConnection.ColumnDefinition{c, c2}},
		TableSnapshot{Table: "t", Columns: []gonaviConnection.ColumnDefinition{c}},
		DialectMySQL, DialectMySQL, "mysql", "`d`.`t`", DiffOptions{})
	for _, ch := range d2.Changes {
		if ch.Sqls == nil {
			t.Errorf("%s/%s 的 sqls 是 nil", ch.Kind, ch.Object)
		}
	}
}
