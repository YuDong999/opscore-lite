package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	gonaviConnection "opscore/internal/dbmanager/gonavi/connection"
)

func colDef(name, typ, nullable, key, extra string) gonaviConnection.ColumnDefinition {
	return gonaviConnection.ColumnDefinition{Name: name, Type: typ, Nullable: nullable, Key: key, Extra: extra}
}

func idxDef(name, column string, nonUnique, seq, subPart int) gonaviConnection.IndexDefinition {
	return gonaviConnection.IndexDefinition{Name: name, ColumnName: column, NonUnique: nonUnique, SeqInIndex: seq, SubPart: subPart}
}

func TestDetectDataKeysPrefersPrimaryKey(t *testing.T) {
	cols := []gonaviConnection.ColumnDefinition{
		colDef("note", "varchar(64)", "YES", "", ""),
		colDef("tenant_id", "bigint", "NO", "PRI", ""),
		colDef("id", "bigint", "NO", "PRI", ""),
	}
	got, src, err := DetectDataKeys(cols, nil)
	if err != nil {
		t.Fatalf("不该失败: %v", err)
	}
	if src != "主键" {
		t.Fatalf("键来源 = %s", src)
	}
	// 复合主键顺序按**表定义序**, 与 PRIMARY 元数据顺序一致; 反了会生成打不中索引的游标
	if strings.Join(got, ",") != "tenant_id,id" {
		t.Fatalf("键顺序错: %v", got)
	}
}

func TestDetectDataKeysFallsBackToUniqueIndex(t *testing.T) {
	cols := []gonaviConnection.ColumnDefinition{colDef("code", "varchar(32)", "NO", "UNI", "")}
	got, src, err := DetectDataKeys(cols, []gonaviConnection.IndexDefinition{idxDef("uq_code", "code", 0, 1, 0)})
	if err != nil {
		t.Fatalf("该接受唯一索引: %v", err)
	}
	if len(got) != 1 || got[0] != "code" || !strings.HasPrefix(src, "唯一索引") {
		t.Fatalf("got=%v src=%s", got, src)
	}
}

func TestDetectDataKeysRejectsUnsafeKeys(t *testing.T) {
	cases := []struct {
		name string
		cols []gonaviConnection.ColumnDefinition
		idxs []gonaviConnection.IndexDefinition
	}{
		{"可空列不能当分页键", []gonaviConnection.ColumnDefinition{colDef("code", "varchar(32)", "YES", "UNI", "")},
			[]gonaviConnection.IndexDefinition{idxDef("uq", "code", 0, 1, 0)}},
		{"前缀唯一不保证整值唯一", []gonaviConnection.ColumnDefinition{colDef("body", "text", "NO", "UNI", "")},
			[]gonaviConnection.IndexDefinition{idxDef("uq", "body", 0, 1, 10)}},
		{"非唯一索引", []gonaviConnection.ColumnDefinition{colDef("uid", "bigint", "NO", "MUL", "")},
			[]gonaviConnection.IndexDefinition{idxDef("ix", "uid", 1, 1, 0)}},
		{"没有键", []gonaviConnection.ColumnDefinition{colDef("a", "int", "NO", "", "")}, nil},
	}
	for _, c := range cases {
		if _, _, err := DetectDataKeys(c.cols, c.idxs); err == nil {
			t.Errorf("%s: 应该拒绝", c.name)
		}
	}
}

// 比"发现差异"更要紧的是**不造出假差异**: 下面每一对都是两侧同一条数据的不同表示法。
func TestCanonValueAbsorbsRepresentationDifferences(t *testing.T) {
	eq := func(mode DataValueMode, a, b any, what string) {
		t.Helper()
		ca, cb := canonValue(a, mode), canonValue(b, mode)
		if ca != cb {
			t.Errorf("%s: %v(%q) 与 %v(%q) 被判成不一样", what, a, ca, b, cb)
		}
	}
	eq(ModeNumeric, "10.50", 10.5, "decimal 尾部零")
	eq(ModeNumeric, "007", int64(7), "前导零")
	eq(ModeNumeric, "1e3", int64(1000), "科学计数")
	eq(ModeNumeric, "1234567890123456789", "1234567890123456789", "19 位大整数不许走 float")
	eq(ModeNumeric, "-0", int64(0), "负零")
	eq(ModeNumeric, "+8", int64(8), "正号")
	eq(ModeBool, true, "1", "boolean vs 1")
	eq(ModeBool, "t", int64(1), "PG 的 t")
	eq(ModeBool, false, "", "false vs 空")
	eq(ModeBlob, "0xDEADBEEF", []byte{0xde, 0xad, 0xbe, 0xef}, "hex 大小写")
	eq(ModeBlob, `\xdeadbeef`, "0xdeadbeef", `PG \x 前缀 vs GoNavi 0x 前缀`)
	eq(ModeTime, "2026-01-01T08:00:00+08:00", "2026-01-01 08:00:00", "带时区 vs 裸串")
	eq(ModeTime, "2026-01-01T00:00:00.000000Z", "2026-01-01 00:00:00", "6 位零小数")
	eq(ModeTime, "2026-01-01", "2026-01-01T00:00:00Z", "一侧 DATE 一侧 TIMESTAMP")
	eq(ModeTime, time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC), "2026-01-01 08:00:00 +0000 UTC", "time.Time vs 其字符串形态")
	eq(ModeDate, "2026-01-01T00:00:00+08:00", "2026-01-01", "date 只看日")
	eq(ModeText, "abc  ", "abc", "CHAR 尾部填充")
	eq(ModeText, "路径\\Windows", `路径\Windows`, "反斜杠原样")
}

// 归一过头会把真差异抹掉, 这几条必须仍然判为不同。
func TestCanonValueKeepsRealDifferences(t *testing.T) {
	ne := func(mode DataValueMode, a, b any, what string) {
		t.Helper()
		if canonValue(a, mode) == canonValue(b, mode) {
			t.Errorf("%s: %v 与 %v 被判成相同", what, a, b)
		}
	}
	ne(ModeText, nil, "", "NULL vs 空串是两回事")
	ne(ModeText, "1", "1.0", "文本列不许按数值折")
	ne(ModeNumeric, "1.5", "1.5001", "小数位差")
	ne(ModeTime, "2026-01-01 08:00:00", "2026-01-01 08:00:01", "差一秒")
	ne(ModeTime, "2026-01-01 08:00:00", "2026-01-02 08:00:00", "差一天")
	ne(ModeBool, true, false, "布尔")
	ne(ModeBlob, "0x00", "0x0000", "hex 长度差")
}

func TestPairDataColumnsRejectsNullableKeys(t *testing.T) {
	src := []gonaviConnection.ColumnDefinition{colDef("code", "varchar(32)", "YES", "", ""), colDef("v", "int", "NO", "", "")}
	dst := []gonaviConnection.ColumnDefinition{colDef("code", "varchar(32)", "NO", "", ""), colDef("v", "int", "NO", "", "")}
	// 手填键列也要卡可空: 一侧可空就足以让"回传的 NULL"与字符串 "NULL" 分不开
	if _, _, err := PairDataColumns(src, dst, []string{"code"}, nil); err == nil {
		t.Fatal("可空键应该被拒绝")
	}
	// 大小写不同的列名要能配上(MySQL→PG 常见), 且规范名取基准侧
	cols, skipped, err := PairDataColumns(
		[]gonaviConnection.ColumnDefinition{colDef("Id", "bigint", "NO", "PRI", ""), colDef("Name", "varchar(8)", "NO", "", "")},
		[]gonaviConnection.ColumnDefinition{colDef("id", "bigint", "NO", "PRI", ""), colDef("name", "varchar(8)", "NO", "", "")},
		[]string{"Id"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(skipped) != 0 {
		t.Fatalf("不该跳过: %v", skipped)
	}
	if cols[0].Name != "Id" || cols[0].DstName != "id" || !cols[0].IsKey {
		t.Fatalf("规范名/目标名错: %+v", cols[0])
	}
}

func TestPickModeRules(t *testing.T) {
	cases := []struct {
		a, b string
		want DataValueMode
	}{
		{"tinyint", "boolean", ModeBool}, // MySQL 的 0/1 与 PG 的 boolean 是同一件事
		{"varchar", "boolean", ModeBool}, // 文本存 "true"/"f" 时按布尔归一
		{"text", "blob", ModeText},       // 可打印 BLOB 两侧都已折成字符串
		{"date", "timestamp", ModeTime},  // 按日期比会把 12:30 和 13:40 看成相同
		{"decimal", "varchar", ModeText}, // 一侧文本一侧数值: 不替它决定谁对
		{"bigint", "bigint", ModeNumeric},
		{"json", "jsonb", ModeText},
	}
	for _, c := range cases {
		if got := pickMode(c.a, c.b); got != c.want {
			t.Errorf("pickMode(%s,%s)=%v 期望 %v", c.a, c.b, got, c.want)
		}
	}
}

func TestNumericKeyStaysBareButEscapesHostileText(t *testing.T) {
	cols, _, err := PairDataColumns(
		[]gonaviConnection.ColumnDefinition{colDef("id", "bigint", "NO", "PRI", ""), colDef("v", "varchar(10)", "NO", "", "")},
		[]gonaviConnection.ColumnDefinition{colDef("ID", "bigint", "NO", "PRI", ""), colDef("v", "varchar(10)", "NO", "", "")},
		[]string{"id"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	keys := []DataGridColumn{cols[0]}
	// 数值键裸写: 写成 '42' 某些库会对整列做隐式转换, 复合索引就走不动了
	sql := DataLookupSQL("`d`.`t`", cols, keys, [][]any{{"42"}, {int64(43)}}, DialectMySQL, "dst")
	if !strings.Contains(sql, "IN (42, 43)") {
		t.Fatalf("数值键没裸写: %s", sql)
	}
	// 两侧列名大小写不同 → 读目标侧时要 AS 成规范名, 否则行 map 取不到值
	if !strings.Contains(sql, "`ID` AS `id`") {
		t.Fatalf("缺列别名: %s", sql)
	}
	// 文本键里的引号必须被关在同一个字面量里(判据不是"串里没有 OR 1=1" —— 转义后那串文本还在)
	hostile := []DataGridColumn{{Name: "v", SrcName: "v", DstName: "v", SrcBase: "varchar", DstBase: "varchar", Mode: ModeText, IsKey: true}}
	sql2 := DataLookupSQL("`d`.`t`", cols, hostile, [][]any{{"a' OR 1=1--"}}, DialectMySQL, "dst")
	if !strings.HasSuffix(sql2, "IN ('a\\' OR 1=1--')") {
		t.Fatalf("文本键没被当成一个字面量: %s", sql2)
	}
}

func TestDataPageSQLExpandsKeysetLexicographically(t *testing.T) {
	cols := []DataGridColumn{
		{Name: "a", SrcName: "a", DstName: "a", SrcBase: "bigint", DstBase: "bigint", Mode: ModeNumeric, IsKey: true},
		{Name: "b", SrcName: "b", DstName: "b", SrcBase: "bigint", DstBase: "bigint", Mode: ModeNumeric, IsKey: true},
		{Name: "v", SrcName: "v", DstName: "v", SrcBase: "varchar", DstBase: "varchar", Mode: ModeText},
	}
	sql := DataPageSQL("`d`.`t`", cols, cols[:2], []any{int64(3), int64(7)}, DialectMySQL, "src", 500)
	want := "(`a` > 3) OR (`a` = 3 AND `b` > 7)"
	if !strings.Contains(sql, "WHERE "+want) {
		t.Fatalf("键集展开错:\n%s", sql)
	}
	if !strings.HasSuffix(sql, "ORDER BY `a`, `b` LIMIT 500") {
		t.Fatalf("排序/限量错:\n%s", sql)
	}
	// 行构造式 (a,b) > (3,7) 在 MySQL 上吃不到复合索引, 不该出现在这里
	if strings.Contains(sql, "(a,b) >") || strings.Contains(sql, "(`a`,`b`)") {
		t.Fatalf("用了行构造式: %s", sql)
	}
}

func TestRowsToStatements(t *testing.T) {
	srcCols := []gonaviConnection.ColumnDefinition{
		colDef("id", "bigint", "NO", "PRI", ""),
		colDef("name", "varchar(32)", "YES", "", ""),
		colDef("total", "decimal(10,2)", "NO", "", ""),
		colDef("sum_cents", "bigint", "NO", "", "STORED GENERATED"),
	}
	dstCols := []gonaviConnection.ColumnDefinition{
		colDef("id", "bigint", "NO", "PRI", ""),
		colDef("name", "varchar(32)", "YES", "", ""),
		colDef("total", "numeric", "NO", "", ""),
		colDef("sum_cents", "bigint", "NO", "", "STORED"),
	}
	cols, skipped, err := PairDataColumns(srcCols, dstCols, []string{"id"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(skipped) != 0 {
		t.Fatalf("不该有跳过的列: %v", skipped)
	}
	spec := &DataTableSpec{Table: "t", DstRef: `"d"."t"`, DstD: DialectPostgres, Columns: cols}

	srcRow := map[string]any{"id": int64(7), "name": any(nil), "total": "10.50", "sum_cents": int64(1050)}
	dstRow := map[string]any{"id": int64(7), "name": "x", "total": 10.5, "sum_cents": int64(9999)}

	// 值一样(total 10.50 vs 10.5)且只有生成列不同 → 不生成任何语句, 也不报差异
	_, cells, differs := RowsToStatements(spec, srcRow, map[string]any{"id": int64(7), "name": any(nil), "total": "10.5", "sum_cents": int64(1050)})
	if differs {
		t.Fatalf("生成列的差异不该产语句: %+v", cells)
	}

	sqls, cells, differs := RowsToStatements(spec, srcRow, dstRow)
	if !differs || len(sqls) != 1 {
		t.Fatalf("应生成 1 条: %v %+v", sqls, differs)
	}
	u := sqls[0]
	if !strings.HasPrefix(u, `UPDATE "d"."t" SET "name"=NULL`) {
		t.Fatalf("UPDATE 应以置 NULL 开头(name 一侧为 NULL 一侧为 x): %s", u)
	}
	if strings.Contains(u, "sum_cents") {
		t.Fatalf("生成列不能进 SET: %s", u)
	}
	if strings.Contains(u, `"total"=`) {
		t.Fatalf("10.50 与 10.5 是同一个值, 不该被 SET: %s", u)
	}
	if !strings.HasSuffix(u, `WHERE "id"=7`) {
		t.Fatalf("键谓词错(数值键须裸写): %s", u)
	}
	var nDiff int
	for _, c := range cells {
		if c.Differs {
			nDiff++
		}
	}
	// 生成列照实报"值不一样"(它确实不同), 但 SET 里没有它 —— 上面两条断言合起来就是这个分工
	if nDiff != 2 {
		t.Fatalf("cells 里 name 与 sum_cents 都该标 differs: %+v", cells)
	}

	// 目标缺行 → INSERT(带键, 不带生成列)
	ins, _, _ := RowsToStatements(spec, srcRow, nil)
	if len(ins) != 1 || !strings.HasPrefix(ins[0], `INSERT INTO "d"."t" ("id", "name", "total") VALUES (7, NULL, '10.50')`) {
		t.Fatalf("INSERT 错: %v", ins)
	}
	// 基准没有 → DELETE
	del, _, _ := RowsToStatements(spec, nil, dstRow)
	if len(del) != 1 || del[0] != `DELETE FROM "d"."t" WHERE "id"=7` {
		t.Fatalf("DELETE 错: %v", del)
	}
}

func TestDiffNeverSerializesNilCollections(t *testing.T) {
	// 前端对 .map/.length 的依赖不能靠"后端记得给空数组" —— JSON 出 null 整个面板就炸
	spec := &DataTableSpec{Table: "t", DstRef: "`t`", DstD: DialectMySQL,
		Columns: []DataGridColumn{
			{Name: "id", SrcName: "id", DstName: "id", SrcBase: "bigint", DstBase: "bigint", Mode: ModeNumeric, IsKey: true},
			{Name: "v", SrcName: "v", DstName: "v", SrcBase: "varchar", DstBase: "varchar", Mode: ModeText},
		}}
	f := &fakeFetch{src: nil, dst: nil}
	out, err := CompareTableData(context.Background(), spec, f)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(out)
	for _, k := range []string{`"rows":[]`, `"notes":[`, `"skipped":[`, `"keyColumns":[`, `"columns":[`} {
		if !strings.Contains(string(b), k) {
			t.Fatalf("序列化缺 %s: %s", k, b)
		}
	}
}

// ── 假取数: 让主循环脱离数据库可测 ──

type fakeFetch struct {
	src, dst []map[string]any
	keyName  string // 行标识列的规范名(默认 id)
	fold     map[string]bool
	pages    int
	lookups  int
	countErr error
}

// keyOf 取行的键值。int 走大小比较, 文本走字符串比较 —— 假实现只服务测试数据。
func (f *fakeFetch) keyOf(r map[string]any) any {
	if f.keyName == "" {
		return r["id"]
	}
	return r[f.keyName]
}

func (f *fakeFetch) Page(_ context.Context, side string, after []any, limit int) ([]map[string]any, error) {
	f.pages++
	rows := f.src
	if side == "dst" {
		rows = f.dst
	}
	out := make([]map[string]any, 0, limit)
	for _, r := range rows {
		if len(after) > 0 && !greaterInt(f.keyOf(r), after[0]) {
			continue
		}
		out = append(out, r)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// Lookup 的匹配口径可以按侧配成"不分大小写", 用来模拟 MySQL 的 CI 排序规则:
// 库自己认为 'A' 与 'a' 是同一个键, 于是会把那行返回, 剩下的认配落在 Go 侧。
func (f *fakeFetch) Lookup(_ context.Context, side string, tuples [][]any) ([]map[string]any, error) {
	f.lookups++
	rows := f.src
	if side == "dst" {
		rows = f.dst
	}
	want := map[string]bool{}
	for _, tp := range tuples {
		k := fmt.Sprint(tp[0])
		if f.fold[side] {
			k = strings.ToLower(k)
		}
		want[k] = true
	}
	var out []map[string]any
	for _, r := range rows {
		k := fmt.Sprint(f.keyOf(r))
		if f.fold[side] {
			k = strings.ToLower(k)
		}
		if want[k] {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeFetch) Count(_ context.Context, side string) (int64, error) {
	if f.countErr != nil {
		return 0, f.countErr
	}
	if side == "src" {
		return int64(len(f.src)), nil
	}
	return int64(len(f.dst)), nil
}

// greaterInt 假实现的"键集"判定: 真实实现把比较交给库, 测试里只按 int/字符串自己排。
func greaterInt(a, b any) bool {
	if s, ok := a.(string); ok {
		return s > fmt.Sprint(b)
	}
	return asInt64(a) > asInt64(b)
}

func asInt64(v any) int64 {
	switch t := v.(type) {
	case int:
		return int64(t)
	case int64:
		return t
	case string:
		n, _ := strconv.ParseInt(t, 10, 64)
		return n
	default:
		return 0
	}
}

func keySpec(t *testing.T) *DataTableSpec {
	t.Helper()
	cols, _, err := PairDataColumns(
		[]gonaviConnection.ColumnDefinition{colDef("id", "bigint", "NO", "PRI", ""), colDef("v", "varchar(32)", "YES", "", "")},
		[]gonaviConnection.ColumnDefinition{colDef("id", "bigint", "NO", "PRI", ""), colDef("v", "varchar(32)", "YES", "", "")},
		[]string{"id"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return &DataTableSpec{Table: "t", DstRef: "`d`.`t`", DstD: DialectMySQL, Columns: cols,
		Options: DataCompareOptions{BatchRows: 2}}
}

func row(id int, v any) map[string]any { return map[string]any{"id": int64(id), "v": v} }

func TestCompareTableDataClassifiesThreeKinds(t *testing.T) {
	spec := keySpec(t)
	f := &fakeFetch{
		src: []map[string]any{row(1, "a"), row(2, "same"), row(3, "changed"), row(5, "only-src")},
		dst: []map[string]any{row(1, "a"), row(2, "same"), row(3, "CHANGED"), row(4, "only-dst")},
	}
	out, err := CompareTableData(context.Background(), spec, f)
	if err != nil {
		t.Fatal(err)
	}
	if out.Summary.Inserts != 1 || out.Summary.Updates != 1 || out.Summary.Deletes != 1 {
		t.Fatalf("三分类错: %+v", out.Summary)
	}
	kinds := map[string]string{}
	for _, r := range out.Rows {
		kinds[r.ID[:strings.Index(r.ID, "|")]] = r.Kind
	}
	for _, k := range []string{"insert", "update", "delete"} {
		if kinds[k] != k {
			t.Fatalf("缺 %s: %+v", k, out.Rows)
		}
	}
	// 反向扫描必须跑(有差异时不能省)
	if f.pages < 3 {
		t.Fatalf("没跑反向扫描: pages=%d", f.pages)
	}
	if out.ScannedSrc != 4 || out.ScannedDst != 4 {
		t.Fatalf("扫描行数错: src=%d dst=%d", out.ScannedSrc, out.ScannedDst)
	}
}

func TestCompareTableDataSkipsReverseScanWhenSafe(t *testing.T) {
	spec := keySpec(t)
	f := &fakeFetch{
		src: []map[string]any{row(1, "a"), row(2, "b")},
		dst: []map[string]any{row(1, "a"), row(2, "b")},
	}
	out, err := CompareTableData(context.Background(), spec, f)
	if err != nil {
		t.Fatal(err)
	}
	if out.Summary.Total() != 0 {
		t.Fatalf("该一致: %+v", out.Summary)
	}
	if out.ScannedDst != 0 {
		t.Fatalf("行数一致且无差异时应跳过反向扫描: %d", out.ScannedDst)
	}
	if len(out.Notes) == 0 || !strings.Contains(out.Notes[0], "已跳过反向扫描") {
		t.Fatalf("要在界面上说清为什么少扫一遍: %v", out.Notes)
	}
}

// 行数统计失败时两侧都是 0, 于是 0==0 会把"没扫"伪装成"已一致" —— 这是最坏的一种假通过。
func TestCompareTableDataMustNotSkipWhenCountFailed(t *testing.T) {
	spec := keySpec(t)
	f := &fakeFetch{
		src: []map[string]any{row(1, "a")},
		dst: []map[string]any{row(1, "a"), row(9, "ghost")},
	}
	f.countErr = fmt.Errorf("权限不够")
	out, err := CompareTableData(context.Background(), spec, f)
	if err != nil {
		t.Fatal(err)
	}
	if out.ScannedDst == 0 {
		t.Fatalf("统计失败还跳过反向扫描 = 漏报 ghost 行")
	}
	if out.Summary.Deletes != 1 {
		t.Fatalf("该报出 delete: %+v", out.Summary)
	}
}

func TestCompareTableDataPagesAndHonorsMaxDiffs(t *testing.T) {
	spec := keySpec(t)
	var src, dst []map[string]any
	for i := 1; i <= 7; i++ {
		src = append(src, row(i, fmt.Sprintf("v%d", i)))
		dst = append(dst, row(i, "same"))
	}
	f := &fakeFetch{src: src, dst: dst}
	out, err := CompareTableData(context.Background(), spec, f)
	if err != nil {
		t.Fatal(err)
	}
	if out.Summary.Updates != 7 || out.Partial {
		t.Fatalf("7 条差异且没到上限: %+v partial=%v", out.Summary, out.Partial)
	}
	if out.ScannedSrc != 7 {
		t.Fatalf("分页扫描不完整: %d", out.ScannedSrc)
	}

	spec2 := keySpec(t)
	spec2.Options.MaxDiffs = 3
	f2 := &fakeFetch{src: src, dst: dst}
	out2, err := CompareTableData(context.Background(), spec2, f2)
	if err != nil {
		t.Fatal(err)
	}
	if !out2.Partial || len(out2.Rows) > 4 {
		t.Fatalf("该在上限处停住并如实报 partial: rows=%d partial=%v reason=%s", len(out2.Rows), out2.Partial, out2.Reason)
	}
	if out2.Reason == "" {
		t.Fatal("partial 必须带原因")
	}
}

func TestCompareTableDataRejectsDuplicateKeys(t *testing.T) {
	spec := keySpec(t)
	f := &fakeFetch{
		src: []map[string]any{row(1, "a")},
		dst: []map[string]any{row(1, "a"), row(1, "b")},
	}
	if _, err := CompareTableData(context.Background(), spec, f); err == nil {
		t.Fatal("目标侧键重复必须硬失败: 否则生成的 UPDATE 会打在两行上")
	} else if !strings.Contains(err.Error(), "键不唯一") {
		t.Fatalf("错误不该换个说法: %v", err)
	}
}

// 两侧键的排序规则不同(MySQL CI 与 PG CS 的常见后果): 库认为 'A' 与 'a' 是同一个键并把那行
// 返回了, Go 侧再按字节比就会判成"没配上", 于是同一行既算 insert 又算 delete。
// 兜底认配(小写键)就是为这一步准备的。
func TestCompareTableDataMatchesAcrossCollations(t *testing.T) {
	kc := DataGridColumn{Name: "code", SrcName: "code", DstName: "code", SrcBase: "varchar", DstBase: "varchar", Mode: ModeText, IsKey: true}
	vc := DataGridColumn{Name: "v", SrcName: "v", DstName: "v", SrcBase: "varchar", DstBase: "varchar", Mode: ModeText}
	spec := &DataTableSpec{Table: "t", DstRef: "`d`.`t`", DstD: DialectMySQL,
		Columns: []DataGridColumn{kc, vc}, Options: DataCompareOptions{BatchRows: 10}}
	f := &fakeFetch{
		keyName: "code",
		fold:    map[string]bool{"src": true, "dst": true},
		src:     []map[string]any{{"code": "A", "v": "x"}},
		dst:     []map[string]any{{"code": "a", "v": "x"}},
	}
	out, err := CompareTableData(context.Background(), spec, f)
	if err != nil {
		t.Fatal(err)
	}
	if out.Summary.Total() != 0 {
		t.Fatalf("两侧只是键的大小写表示不同, 不该报差异: %+v rows=%+v", out.Summary, out.Rows)
	}
}
