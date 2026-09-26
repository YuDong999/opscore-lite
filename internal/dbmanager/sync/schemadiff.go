package sync

// 结构对比(schema diff): 给两张表的**元数据快照**, 算出"把目标改成与源一致"要发什么语句。
//
// 这里刻意是纯函数: 不碰连接、不执行任何东西。读元数据与执行分别住在
// dbmanager 的 handler 与 execAllInTx 里, 于是"比对口径"可以单测锁住 —— 这类逻辑
// 一旦散到 HTTP 层, 就只能靠连真库才能验证, 而它恰恰是最容易悄悄错的地方
// (int(11) vs int、YES/NO vs true/false、索引按列展开成多行…)。
//
// 判定尺度对齐 dbx(我们的交互基准): 按**名字**配对(区分大小写)、默认值按原串比、
// MySQL 同族才剥显示宽度。与它不同的一处: 跨方言族的列差异只报告不自动生成 MODIFY
// —— 类型映射是有损的(serial/enum/jsonb…), 让用户在预览里看到"这条我们不替它决定"更安全。

import (
	"fmt"
	"sort"
	"strings"

	gonaviConnection "opscore/internal/dbmanager/gonavi/connection"
)

// TableSnapshot 一张表在某一侧的结构事实(全部来自驱动反射)。
type TableSnapshot struct {
	Table   string
	Columns []gonaviConnection.ColumnDefinition
	Indexes []gonaviConnection.IndexDefinition // 驱动给的是"每列一行", 要比逻辑索引必须先聚合
	FKs     []gonaviConnection.ForeignKeyDefinition
}

// DiffOptions 对比粒度。零值 = 只比列(类型/可空/默认), 这是最保守的默认。
type DiffOptions struct {
	Indexes     bool `json:"indexes"`
	ForeignKeys bool `json:"foreignKeys"`
	Comments    bool `json:"comments"`
}

// Change 一条差异。Sqls 为空表示"只报告、不替用户决定"(跨方言类型/自增/主键等)。
type Change struct {
	// Key 稳定标识"表|kind|对象", 执行时前端只回传选中的 key, 不回传 SQL ——
	// 否则就等于把 SQL 交给客户端, 前面那套"后端拼"的纪律就白立了。
	Key         string   `json:"key"`
	Kind        string   `json:"kind"`   // column_add | column_drop | column_modify | index_add | index_drop | fk_add | fk_drop | pk_modify | autoinc_differs
	Object      string   `json:"object"` // 列名 / 索引名 / 约束名
	Detail      []string `json:"detail"` // 人读的"哪几处不一样"
	Sqls        []string `json:"sqls"`
	Destructive bool     `json:"destructive"`
}

// TableDiff 一张表的对比结果。
type TableDiff struct {
	Table   string   `json:"table"`
	Action  string   `json:"action"` // create | modify | delete | none
	Changes []Change `json:"changes"`
	Sqls    []string `json:"sqls"`
	// Notes 收集"为什么某条差异没生成语句", 界面要原样显示, 别让用户以为漏了
	Notes []string `json:"notes"`
}

// logicalIndex 索引的聚合形态(名字 -> 列序列 + 是否唯一 + 类型)。
type logicalIndex struct {
	name    string
	cols    []string
	unique  bool
	idxtyp  string
	subpart map[string]int
}

// aggregateIndexes 把驱动"每列一行"的索引明细聚成逻辑索引。
// 列序必须按 SeqInIndex 定, 而不是驱动返回顺序 —— 列序是索引定义的一部分,
// 排错了会把"完全相同的两个索引"判成不一样(然后生成 drop+create)。
func aggregateIndexes(defs []gonaviConnection.IndexDefinition) []logicalIndex {
	type colAt struct {
		name string
		seq  int
		sub  int
	}
	type agg struct {
		cols   []colAt
		unique bool
		idxtyp string
	}
	byName := map[string]*agg{}
	var order []string
	for _, d := range defs {
		a, ok := byName[d.Name]
		if !ok {
			a = &agg{unique: d.NonUnique == 0, idxtyp: d.IndexType}
			byName[d.Name] = a
			order = append(order, d.Name)
		}
		a.cols = append(a.cols, colAt{name: d.ColumnName, seq: d.SeqInIndex, sub: d.SubPart})
	}
	out := make([]logicalIndex, 0, len(order))
	for _, n := range order {
		a := byName[n]
		sort.SliceStable(a.cols, func(i, j int) bool {
			if a.cols[i].seq != a.cols[j].seq {
				return a.cols[i].seq < a.cols[j].seq
			}
			return a.cols[i].name < a.cols[j].name // 序号并列时给个稳定序
		})
		li := logicalIndex{name: n, unique: a.unique, idxtyp: a.idxtyp, subpart: map[string]int{}}
		for _, c := range a.cols {
			li.cols = append(li.cols, c.name)
			if c.sub > 0 {
				li.subpart[c.name] = c.sub
			}
		}
		out = append(out, li)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

func (li logicalIndex) sig() string {
	// 只给一个稳定签名用于比较; 列序在前缀长度不同的时候也要能区分开
	parts := make([]string, 0, len(li.cols))
	for _, c := range li.cols {
		if n := li.subpart[c]; n > 0 {
			c = fmt.Sprintf("%s(%d)", c, n)
		}
		parts = append(parts, c)
	}
	return fmt.Sprintf("%s|unique=%v|type=%s|cols=%s", li.name, li.unique, li.idxtyp, strings.Join(parts, ","))
}

// sameType 判断两侧类型是否等价。同族才做归一比较, 跨族交给 mapColumn。
func sameType(a, b string, d Dialect) bool {
	return normalizeSameType(parseTypeName(a), d) == normalizeSameType(parseTypeName(b), d)
}

func sameDefault(a, b *string) bool {
	x, y := "", ""
	if a != nil {
		x = strings.TrimSpace(*a)
	}
	if b != nil {
		y = strings.TrimSpace(*b)
	}
	return x == y
}

func nullableOf(c gonaviConnection.ColumnDefinition) bool {
	return strings.EqualFold(c.Nullable, "YES")
}

func colToAlter(c gonaviConnection.ColumnDefinition) ColumnAlter {
	d := ""
	if c.Default != nil {
		d = *c.Default
	}
	return ColumnAlter{
		Kind: "modify", Name: c.Name, Type: c.Type, Nullable: nullableOf(c),
		Default: d, Comment: c.Comment,
	}
}

// DiffTable 比对同名表。srcD/dstD 是两侧的方言族; dstTableRef 用于生成语句。
// 返回的 TableDiff.Action 在无差异时为 none(调用方据此过滤)。
func DiffTable(src, dst TableSnapshot, srcD, dstD Dialect, dstEngine, dstTableRef string, opt DiffOptions) TableDiff {
	td := TableDiff{Table: src.Table}
	srcCols := map[string]gonaviConnection.ColumnDefinition{}
	dstCols := map[string]gonaviConnection.ColumnDefinition{}
	for _, c := range src.Columns {
		srcCols[c.Name] = c
	}
	for _, c := range dst.Columns {
		dstCols[c.Name] = c
	}

	// 1) 新增列: 按源顺序追加(列序对人是有意义的)
	for _, c := range src.Columns {
		if _, ok := dstCols[c.Name]; ok {
			continue
		}
		alt := colToAlter(c)
		alt.Kind = "add"
		ch := Change{Kind: "column_add", Object: c.Name, Detail: []string{"类型 " + c.Type, nullLabel(nullableOf(c))}}
		if alt.Default != "" {
			ch.Detail = append(ch.Detail, "默认 "+alt.Default)
		}
		// NOT NULL 新列在已有数据的表上会直接失败, 提示而不擅自补默认值
		if !alt.Nullable {
			ch.Detail = append(ch.Detail, "注意: 表里已有行时, NOT NULL 新列需要默认值或先回填")
		}
		ch.Sqls = buildAlters(dstEngine, dstD, dstTableRef, []ColumnAlter{alt}, &td)
		td.Changes = append(td.Changes, ch)
	}

	// 2) 删除列(破坏性)
	for _, c := range dst.Columns {
		if _, ok := srcCols[c.Name]; ok {
			continue
		}
		ch := Change{
			Kind: "column_drop", Object: c.Name, Destructive: true,
			Detail: []string{"目标侧多出这一列, 删除会丢数据"},
		}
		ch.Sqls = buildAlters(dstEngine, dstD, dstTableRef, []ColumnAlter{{Kind: "drop", Name: c.Name}}, &td)
		td.Changes = append(td.Changes, ch)
	}

	// 3) 同名列逐属性比
	for _, sc := range src.Columns {
		dc, ok := dstCols[sc.Name]
		if !ok {
			continue
		}
		var detail []string
		typeDiffers := !sameType(sc.Type, dc.Type, srcD) && !sameType(sc.Type, dc.Type, dstD)
		if typeDiffers {
			if srcD != dstD {
				mapped := mapColumn(sc, srcD, dstD)
				detail = append(detail, fmt.Sprintf("类型 %s vs %s(源端映射过来是 %s, 不自动生成: 跨方言类型映射有损)",
					dc.Type, sc.Type, mapped.Target))
			} else {
				detail = append(detail, "类型 "+dc.Type+" → "+sc.Type)
			}
		}
		if nullableOf(sc) != nullableOf(dc) {
			detail = append(detail, "可空 "+nullLabel(nullableOf(dc))+" → "+nullLabel(nullableOf(sc)))
		}
		if !sameDefault(sc.Default, dc.Default) {
			detail = append(detail, "默认 "+defLabel(dc.Default)+" → "+defLabel(sc.Default))
		}
		if opt.Comments && strings.TrimSpace(sc.Comment) != strings.TrimSpace(dc.Comment) {
			detail = append(detail, "注释 "+quoteOrNone(dc.Comment)+" → "+quoteOrNone(sc.Comment))
		}
		if strings.Contains(strings.ToLower(sc.Extra), "auto_increment") != strings.Contains(strings.ToLower(dc.Extra), "auto_increment") {
			detail = append(detail, "自增标记不一致(跨引擎不可移植, 只报告)")
			td.Changes = append(td.Changes, Change{Kind: "autoinc_differs", Object: sc.Name, Detail: detail})
			continue
		}
		if len(detail) == 0 {
			continue
		}
		ch := Change{Kind: "column_modify", Object: sc.Name, Detail: detail}
		// 只有"同族 + 属性可直接 ALTER"时才生成语句; 跨方言的类型差异不替用户决定
		if srcD == dstD {
			alt := colToAlter(sc)
			ch.Sqls = buildAlters(dstEngine, dstD, dstTableRef, []ColumnAlter{alt}, &td)
		} else if !typeDiffers {
			alt := colToAlter(sc)
			ch.Sqls = buildAlters(dstEngine, dstD, dstTableRef, []ColumnAlter{alt}, &td)
		} else {
			td.Notes = append(td.Notes, sc.Name+": 跨方言类型差异需人工确认")
		}
		td.Changes = append(td.Changes, ch)
	}

	// 4) 主键: 只报告。改主键牵涉索引名与外键依赖, 自动生成容易把表改死
	if pk := pkColumns(src.Columns); pk != pkColumns(dst.Columns) {
		td.Changes = append(td.Changes, Change{
			Kind: "pk_modify", Object: "PRIMARY",
			Detail: []string{"主键列 " + orNone(pkColumns(dst.Columns)) + " → " + orNone(pk)},
		})
		td.Notes = append(td.Notes, "主键差异不自动生成: 请用表结构编辑器逐项确认")
	}

	// 5) 索引 / 外键
	if opt.Indexes {
		diffIndexes(&td, src, dst, dstEngine, dstD, dstTableRef)
	}
	if opt.ForeignKeys {
		diffForeignKeys(&td, src, dst, dstD, dstTableRef)
	}

	if len(td.Changes) > 0 {
		td.Action = "modify"
	} else {
		td.Action = "none"
	}
	stampKeys(&td)
	for _, ch := range td.Changes {
		td.Sqls = append(td.Sqls, ch.Sqls...)
	}
	return td
}

// stampKeys 给每条变更打上稳定 key(表|kind|对象), 并把 Sqls 归一成非 nil。
// 两件事都是必须的:
//   - apply 端按 key 收集**变更级**语句, 所以建表/删表这类表级变更也必须把 SQL 挂在 change 上,
//     否则"勾了却没执行"是静默失败;
//   - 前端直接 .map(sqls), JSON 里的 null 会把它炸掉(资源模块踩过同一个坑) —— 一律给空数组。
func stampKeys(td *TableDiff) {
	seen := map[string]int{}
	for i := range td.Changes {
		base := td.Table + "|" + td.Changes[i].Kind + "|" + td.Changes[i].Object
		n := seen[base]
		seen[base]++
		if n > 0 {
			base = fmt.Sprintf("%s#%d", base, n)
		}
		td.Changes[i].Key = base
		if td.Changes[i].Sqls == nil {
			td.Changes[i].Sqls = []string{}
		}
	}
	if td.Sqls == nil {
		td.Sqls = []string{}
	}
	if td.Notes == nil {
		td.Notes = []string{}
	}
}

func buildAlters(engine string, d Dialect, tableRef string, cols []ColumnAlter, td *TableDiff) []string {
	sqls, err := BuildColumnAlters(engine, d, tableRef, cols)
	if err != nil {
		td.Notes = append(td.Notes, "语句生成失败: "+err.Error())
		return nil
	}
	return sqls
}

func pkColumns(cols []gonaviConnection.ColumnDefinition) string {
	var out []string
	for _, c := range cols {
		if strings.EqualFold(c.Key, "PRI") {
			out = append(out, c.Name)
		}
	}
	return strings.Join(out, ",")
}

func diffIndexes(td *TableDiff, src, dst TableSnapshot, engine string, d Dialect, tableRef string) {
	si := map[string]logicalIndex{}
	di := map[string]logicalIndex{}
	for _, i := range aggregateIndexes(src.Indexes) {
		si[i.name] = i
	}
	for _, i := range aggregateIndexes(dst.Indexes) {
		di[i.name] = i
	}
	for name, want := range si {
		got, ok := di[name]
		if ok && got.sig() == want.sig() {
			continue
		}
		if ok {
			ch := Change{Kind: "index_drop", Object: name, Destructive: true, Detail: []string{"定义变了: " + got.sig() + " → " + want.sig()}}
			ch.Sqls = dropIndexSQL(d, tableRef, name)
			td.Changes = append(td.Changes, ch)
		}
		if !ok || got.sig() != want.sig() {
			cols := make([]string, 0, len(want.cols))
			for _, c := range want.cols {
				cols = append(cols, QuoteIdent(c, d))
			}
			uniq := ""
			if want.unique {
				uniq = "UNIQUE "
			}
			td.Changes = append(td.Changes, Change{
				Kind:   "index_add",
				Object: name,
				Detail: []string{want.sig()},
				Sqls: []string{fmt.Sprintf("CREATE %sINDEX %s ON %s (%s)",
					uniq, QuoteIdent(name, d), tableRef, strings.Join(cols, ", "))},
			})
		}
	}
	for name := range di {
		if _, ok := si[name]; ok {
			continue
		}
		td.Changes = append(td.Changes, Change{
			Kind: "index_drop", Object: name, Destructive: true,
			Detail: []string{"目标侧多出的索引"},
			Sqls:   dropIndexSQL(d, tableRef, name),
		})
	}
}

// dropIndexSQL: MySQL 要带表名的 DROP INDEX, PG/标准是独立的 DROP INDEX(名字在 schema 内唯一)。
func dropIndexSQL(d Dialect, tableRef, name string) []string {
	if d == DialectMySQL {
		return []string{"ALTER TABLE " + tableRef + " DROP INDEX " + QuoteIdent(name, d)}
	}
	return []string{"DROP INDEX " + QuoteIdent(name, d)}
}

func diffForeignKeys(td *TableDiff, src, dst TableSnapshot, d Dialect, tableRef string) {
	type fk struct {
		name, cols, ref string
	}
	key := func(list []gonaviConnection.ForeignKeyDefinition) map[string]fk {
		byName := map[string]*fk{}
		for _, f := range list {
			nm := f.ConstraintName
			if nm == "" {
				nm = f.Name
			}
			g, ok := byName[nm]
			if !ok {
				g = &fk{name: nm}
				byName[nm] = g
			}
			if g.cols != "" {
				g.cols += ","
			}
			g.cols += f.ColumnName
			g.ref = fmt.Sprintf("%s(%s)", f.RefTableName, f.RefColumnName)
		}
		out := map[string]fk{}
		for n, g := range byName {
			out[n] = *g
		}
		return out
	}
	sfk, dfk := key(src.FKs), key(dst.FKs)
	for n, want := range sfk {
		got, ok := dfk[n]
		if ok && got.ref == want.ref && got.cols == want.cols {
			continue
		}
		if ok {
			td.Changes = append(td.Changes, Change{
				Kind: "fk_drop", Object: n, Destructive: true,
				Detail: []string{"外键指向变了: " + got.ref + " → " + want.ref},
				Sqls:   dropFKSQL(d, tableRef, n),
			})
		}
		if !ok {
			td.Changes = append(td.Changes, Change{
				Kind:   "fk_add",
				Object: n,
				Detail: []string{"外键 " + want.cols + " → " + want.ref},
				Sqls: []string{fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s FOREIGN KEY (%s) REFERENCES %s",
					tableRef, QuoteIdent(n, d), want.cols, want.ref)},
			})
		}
	}
	for n := range dfk {
		if _, ok := sfk[n]; ok {
			continue
		}
		td.Changes = append(td.Changes, Change{
			Kind: "fk_drop", Object: n, Destructive: true,
			Detail: []string{"目标侧多出的外键"}, Sqls: dropFKSQL(d, tableRef, n),
		})
	}
}

func dropFKSQL(d Dialect, tableRef, name string) []string {
	if d == DialectPostgres {
		return []string{"ALTER TABLE " + tableRef + " DROP CONSTRAINT " + QuoteIdent(name, d)}
	}
	return []string{"ALTER TABLE " + tableRef + " DROP FOREIGN KEY " + QuoteIdent(name, d)}
}

func nullLabel(b bool) string {
	if b {
		return "可空"
	}
	return "NOT NULL"
}

func defLabel(p *string) string {
	if p == nil || strings.TrimSpace(*p) == "" {
		return "无"
	}
	return *p
}

func quoteOrNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "无"
	}
	return `"` + s + `"`
}

func orNone(s string) string {
	if s == "" {
		return "无"
	}
	return s
}

// CreateOnlyDiff 源有目标无 —— 整表新建。跨方言走 GenerateCreateDDL(它自带类型映射)。
//
// 列注释不在这里拼了: GenerateCreateDDL 现在自己输出(MySQL 内联在列定义里, PG 走表外的
// COMMENT ON COLUMN), 以前这里手写过一份补刀, 两处口径迟早会漂 —— 漂的结果是"apply 完还剩
// 一条注释差异", 比对收不了口。
func CreateOnlyDiff(src TableSnapshot, srcD, dstD Dialect, dstEngine, dstDatabase, dstTable string) TableDiff {
	sql, idx, commentDDL, _ := GenerateCreateDDL(dstDatabase, src.Table, src.Columns, src.Indexes, srcD, dstD)
	sqls := append([]string{sql}, commentDDL...)
	sqls = append(sqls, idx...)
	td := TableDiff{
		Table:  src.Table,
		Action: "create",
		Changes: []Change{{Kind: "table_create", Object: src.Table,
			Detail: []string{fmt.Sprintf("目标侧没有这张表(%d 列)", len(src.Columns))},
			Sqls:   sqls}},
		Sqls: sqls,
	}
	stampKeys(&td)
	return td
}

// DropOnlyDiff 目标有源无。默认**不勾选**(破坏性), 语句留给用户显式选择。
func DropOnlyDiff(dst TableSnapshot, d Dialect, tableRef string) TableDiff {
	td := TableDiff{
		Table:  dst.Table,
		Action: "delete",
		Changes: []Change{{Kind: "table_drop", Object: dst.Table, Destructive: true,
			Detail: []string{"源侧没有这张表, 删除会连数据一起没了"},
			Sqls:   []string{"DROP TABLE " + tableRef}}},
		Sqls: []string{"DROP TABLE " + tableRef},
	}
	stampKeys(&td)
	return td
}
