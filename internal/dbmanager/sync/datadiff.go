package sync

// 数据对比(data diff): 同一张表在两侧的**行级**差异 —— 谁多了行、谁少了行、哪几列值不一样,
// 以及"把待改侧改成与基准侧一致"要发的语句。
//
// 与 dbx(我们的交互基准)的三处有意不同:
//   - dbx 把两侧整表灌进内存做 hash-join(它自己的文档也写着"不适合未过滤的大表); 这里是
//     "基准侧键集分页 + 按键回查另一侧", 常驻内存只有一批行, 代价是多一遍反向扫描。
//   - dbx 直接拿 JSON 值 != 比 —— 于是 int 的 1 与字符串 "1"、decimal 的 10.50 与 10.5、
//     时间的 RFC3339 与 '2026-01-01 00:00:00' 全被判成"不一样"。这里按列类型归一后再比。
//   - dbx 用 LIMIT/OFFSET 翻页(扫描期间数据一变就漏行/重行), 而且 source_truncated 写死
//     false、界面却写着"已完成全量比较"。这里超限如实报 partial。
//
// 一条贯穿的口径: **键的大小比较留在库里**。Go 从不判断两个键谁大谁小 —— 文本主键在 MySQL 的
// CI 排序规则下 'A'<'B', 而按字节序是 'B'<'A', 拿 Go 的判定去翻页就会与库的 ORDER BY 错位,
// 表现为"同一行既算多又算少"。所以排序和 > 比较都由同一条 ORDER BY/WHERE 在库里做,
// 两侧各自自洽, 与排序规则无关; 等值回查同理交给 =/IN。
//
// 另一条: 所有行 map 一律以**规范列名**(基准侧的名字)为键, 读目标侧时用 `AS` 别名对齐 ——
// 两侧列名大小写不同(常见于 MySQL→PG)时, 少了这一步整表都会被判成"对不上"。

import (
	"context"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	gonaviConnection "opscore/internal/dbmanager/gonavi/connection"
)

// DataValueMode 决定 canonValue 怎么折一个值。取两侧列类型里更强的那个,
// 因为"同值不同格式"是跨引擎比对里最常见的假差异来源。
type DataValueMode int

const (
	ModeText DataValueMode = iota
	ModeBool
	ModeNumeric
	ModeBlob
	ModeDate
	ModeTime
)

// DataGridColumn 一列在两侧的样子(名字可能大小写不同, 类型可能一宽一窄)。
type DataGridColumn struct {
	Name      string        `json:"name"` // 规范名 = 基准侧列名, 也是行 map 的键
	SrcName   string        `json:"-"`
	DstName   string        `json:"-"`
	SrcBase   string        `json:"-"`
	DstBase   string        `json:"-"`
	Mode      DataValueMode `json:"-"`
	IsKey     bool          `json:"isKey"`
	Generated bool          `json:"-"` // 生成列: 只比不写(写它会报 "cannot modify a generated column")
}

func colName(c DataGridColumn, side string) string {
	if side == "dst" {
		return c.DstName
	}
	return c.SrcName
}

func colBase(c DataGridColumn, side string) string {
	if side == "dst" {
		return c.DstBase
	}
	return c.SrcBase
}

// ── 行标识 ──

// DetectDataKeys 取行标识列: 主键优先, 其次"整列非空、非前缀的唯一索引"。
//
// 找不到就报错, 不拿第一列凑数(dbx 同样拒绝): 键猜错就等于把一行看成
// "一行多 + 一行少", 而那边生成的 DELETE 是真删数据。
func DetectDataKeys(cols []gonaviConnection.ColumnDefinition, idxs []gonaviConnection.IndexDefinition) ([]string, string, error) {
	var pk []string
	pkOrder := map[string]int{}
	for i, c := range cols {
		if strings.EqualFold(c.Key, "PRI") {
			pk = append(pk, c.Name)
			pkOrder[strings.ToLower(c.Name)] = i
		}
	}
	if len(pk) > 0 {
		sort.SliceStable(pk, func(i, j int) bool {
			return pkOrder[strings.ToLower(pk[i])] < pkOrder[strings.ToLower(pk[j])]
		})
		return pk, "主键", nil
	}

	nullable := map[string]bool{}
	for _, c := range cols {
		nullable[strings.ToLower(c.Name)] = !strings.EqualFold(c.Nullable, "NO")
	}
	byIdx := map[string][]gonaviConnection.IndexDefinition{}
	for _, i := range idxs {
		byIdx[i.Name] = append(byIdx[i.Name], i)
	}
	type cand struct {
		name string
		cols []string
	}
	var cands []cand
	for name, defs := range byIdx {
		if name == "" || strings.EqualFold(name, "PRIMARY") {
			continue
		}
		sort.SliceStable(defs, func(i, j int) bool { return defs[i].SeqInIndex < defs[j].SeqInIndex })
		ok := len(defs) > 0
		var cs []string
		for _, d := range defs {
			switch {
			case d.NonUnique != 0:
				ok = false
			case d.SubPart > 0:
				ok = false // 前缀唯一不代表整值唯一(前 10 个字符唯一, 值仍可重复)
			case strings.EqualFold(d.IndexType, "FULLTEXT"), strings.EqualFold(d.IndexType, "SPATIAL"):
				ok = false
			case nullable[strings.ToLower(d.ColumnName)]:
				ok = false // 可空的键在键集分页里没有确定位置(NULL 排前排后各家不同)
			}
			if !ok {
				break
			}
			cs = append(cs, d.ColumnName)
		}
		if ok {
			cands = append(cands, cand{name, cs})
		}
	}
	if len(cands) == 0 {
		return nil, "", fmt.Errorf("没有可用的行标识列: 既没有主键, 也没有\"所有列都非空的唯一索引\"。请先补主键, 或改用「只看结构 / 只按行数校验」")
	}
	sort.Slice(cands, func(i, j int) bool {
		if len(cands[i].cols) != len(cands[j].cols) {
			return len(cands[i].cols) < len(cands[j].cols)
		}
		return cands[i].name < cands[j].name
	})
	return cands[0].cols, "唯一索引 " + cands[0].name, nil
}

// PairDataColumns 按列名(不分大小写)配对两侧参与比较的列; 任一侧缺的列不参与并如实报告。
// 键列按 keyNames 的顺序排在最前, 且必须两侧都在 —— 否则无从对齐。
func PairDataColumns(srcCols, dstCols []gonaviConnection.ColumnDefinition, keyNames, ignored []string) ([]DataGridColumn, []string, error) {
	dstByName := map[string]gonaviConnection.ColumnDefinition{}
	for _, c := range dstCols {
		dstByName[strings.ToLower(c.Name)] = c
	}
	ig := map[string]bool{}
	for _, n := range ignored {
		ig[strings.ToLower(n)] = true
	}
	keySet := map[string]bool{}
	for _, k := range keyNames {
		keySet[strings.ToLower(k)] = true
	}

	var keys []DataGridColumn
	for _, k := range keyNames {
		s := findCol(srcCols, k)
		d, ok := dstByName[strings.ToLower(k)]
		if s == nil || !ok {
			return nil, nil, fmt.Errorf("键列 %s 在某一侧不存在, 无法按它对齐行", k)
		}
		// 手填的键也要卡可空性(自动探测那条路径已经卡了): 键里有 NULL 时
		// ① 键集分页没有确定位置(NULL 各家排前排后不同), ② 回传的展示值 "NULL" 与真正的
		// NULL 在界面与库之间走一圈就分不出来, 于是那一行永远被判成"已无差异"。
		if !strings.EqualFold(s.Nullable, "NO") || !strings.EqualFold(d.Nullable, "NO") {
			return nil, nil, fmt.Errorf("键列 %s 可以为空, 不能用来对齐行 —— 请换主键或非空唯一列", k)
		}
		kc := newDataColumn(s.Name, *s, d)
		kc.IsKey = true
		keys = append(keys, kc)
	}

	var rest []DataGridColumn
	var skipped []string
	for _, s := range srcCols {
		lo := strings.ToLower(s.Name)
		if keySet[lo] {
			continue
		}
		d, ok := dstByName[lo]
		if !ok {
			skipped = append(skipped, s.Name+"(目标侧没有这一列)")
			continue
		}
		if ig[lo] {
			skipped = append(skipped, s.Name+"(已排除)")
			continue
		}
		rest = append(rest, newDataColumn(s.Name, s, d))
	}
	if len(rest) == 0 {
		return nil, nil, fmt.Errorf("两侧没有任何可比较的非键列(键除外全部列都不同名或被排除)")
	}
	return append(keys, rest...), skipped, nil
}

func findCol(cols []gonaviConnection.ColumnDefinition, name string) *gonaviConnection.ColumnDefinition {
	for i := range cols {
		if strings.EqualFold(cols[i].Name, name) {
			return &cols[i]
		}
	}
	return nil
}

func newDataColumn(name string, s, d gonaviConnection.ColumnDefinition) DataGridColumn {
	sb, db := baseTypeOf(s.Type), baseTypeOf(d.Type)
	return DataGridColumn{
		Name:      name,
		SrcName:   s.Name,
		DstName:   d.Name,
		SrcBase:   sb,
		DstBase:   db,
		Mode:      pickMode(sb, db),
		Generated: isGeneratedExtra(s.Extra) || isGeneratedExtra(d.Extra),
	}
}

func isGeneratedExtra(extra string) bool {
	u := strings.ToUpper(extra)
	return strings.Contains(u, "GENERATED") || strings.Contains(u, "VIRTUAL") || strings.Contains(u, "STORED")
}

// pickMode 显式定的合并规则, 别按枚举大小排(第一版我就这么写错过一次):
//   - 两侧同型 → 该型
//   - 布尔与数值/文本混: 按布尔(MySQL tinyint(1) 与 PG boolean 是同一种东西的两种存法)
//   - 二进制与文本混: 按文本(可打印的 BLOB 两侧都已被驱动折成字符串, 按十六进制比反而对不上)
//   - 日期与时间戳混: 按时间戳(按日期比会把 12:30 和 13:40 看成相同)
//   - 数值与非数值混: 按文本(一侧文本一侧数值多半是脏数据或类型漂移, 折叠成数字会掩盖问题)
func pickMode(a, b string) DataValueMode {
	ma, mb := modeOfBase(a), modeOfBase(b)
	if ma == mb {
		return ma
	}
	set := func(ms ...DataValueMode) bool {
		for _, x := range ms {
			if x != ma && x != mb {
				return false
			}
		}
		return true
	}
	switch {
	case set(ModeBool, ModeNumeric), set(ModeBool, ModeText), set(ModeBool, ModeBlob):
		return ModeBool
	case set(ModeBlob, ModeText):
		return ModeText
	case set(ModeDate, ModeTime):
		return ModeTime
	default:
		return ModeText
	}
}

var numericBases = map[string]bool{
	"int": true, "integer": true, "bigint": true, "smallint": true, "mediumint": true, "tinyint": true,
	"decimal": true, "numeric": true, "float": true, "double": true, "real": true, "money": true,
	"int2": true, "int4": true, "int8": true, "float4": true, "float8": true, "number": true,
	"serial": true, "bigserial": true, "smallserial": true, "year": true,
}

var blobBases = map[string]bool{
	"blob": true, "tinyblob": true, "mediumblob": true, "longblob": true, "bytea": true,
	"binary": true, "varbinary": true, "image": true, "raw": true, "bfile": true,
}

var timeBases = map[string]bool{
	"datetime": true, "timestamp": true, "timestamptz": true, "time": true, "datetime2": true,
	"smalldatetime": true, "datetimeoffset": true, "abstime": true, "reltime": true,
}

func modeOfBase(base string) DataValueMode {
	b := strings.ToLower(strings.TrimSpace(base))
	switch {
	case b == "boolean" || b == "bool" || b == "bit":
		return ModeBool
	case b == "date":
		return ModeDate
	case timeBases[b] || strings.HasPrefix(b, "datetime"):
		return ModeTime
	case blobBases[b]:
		return ModeBlob
	case numericBases[b] || strings.HasPrefix(b, "dec") || strings.HasPrefix(b, "num"):
		return ModeNumeric
	default:
		return ModeText // enum/set/json/jsonb/uuid/text/varchar/domain 一律按文本
	}
}

// ── 值归一 ──

var reWall = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})[T ](\d{2}):(\d{2}):(\d{2})(\.\d+)?`)
var reDateOnly = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
var rePlainNum = regexp.MustCompile(`^[+-]?\d+(\.\d+)?$`)
var reHex = regexp.MustCompile(`^[0-9a-f]+$`)

// canonValue 把驱动归一后的值折成"跨引擎可比"的字符串。
//
// 时间只取**墙上时钟**(丢掉时区): 同一条 '2026-01-01 08:00:00', MySQL 的 DATETIME 不带时区,
// PG 的 timestamptz 会被会话时区换算后才打印出来, 按时刻比就造出一批假差异。
// 代价是"同一时刻、两侧存的时区不同"这种真差异看不出来 —— 这句必须挂在界面上, 不藏。
func canonValue(v any, mode DataValueMode) string {
	if v == nil {
		return "\x00NULL"
	}
	switch mode {
	case ModeNumeric:
		return canonNumeric(asString(v))
	case ModeBool:
		return canonBool(asString(v))
	case ModeBlob:
		// 驱动一般已经把 []byte 折成 "0x.."; 但自定义驱动/新引擎可能直接给原始字节, 这里兜住,
		// 否则原始字节会被当字符串塞进十六进制判定里, 两边怎么比都对不上。
		if b, ok := v.([]byte); ok {
			return hex.EncodeToString(b)
		}
		return canonHex(asString(v))
	case ModeTime:
		return canonWall(asString(v), false)
	case ModeDate:
		return canonWall(asString(v), true)
	default:
		s := asString(v)
		// 定长字符列两侧填充口径不同(MySQL 的 CHAR 补空格, PG 也补但截断规则不同),
		// 尾部空白算存储方式差异, 不算数据差异。
		return strings.TrimRight(s, " \t\r\n")
	}
}

func asString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []byte:
		return string(t)
	case bool:
		if t {
			return "1"
		}
		return "0"
	case float64:
		return strconv.FormatFloat(t, 'g', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(t), 'g', -1, 32)
	case int64, int32, int, uint64, uint32, uint:
		return fmt.Sprintf("%d", t)
	default:
		// time.Time 落到 String(): "2026-01-01 08:00:00 +0800 CST" —— canonWall 只取前面的墙上时钟
		return fmt.Sprintf("%v", v)
	}
}

// canonNumeric 折成最短精确十进制: 10.50 → 10.5, 007 → 7, 1e3 → 1000。
// 整数字符串自己处理(不走 float), 免得 19 位大整数被舍入成"看起来一样"。
func canonNumeric(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if !strings.ContainsAny(s, "eE") {
		neg := strings.HasPrefix(s, "-")
		d := strings.TrimPrefix(strings.TrimPrefix(s, "-"), "+")
		if i := strings.IndexByte(d, '.'); i >= 0 {
			d = strings.TrimRight(d, "0")
			d = strings.TrimSuffix(d, ".")
		}
		if isAllDigits(strings.TrimLeft(d, "0")) || strings.TrimLeft(d, "0") == "" {
			d = strings.TrimLeft(d, "0")
			if d == "" {
				d = "0"
			}
			if neg && d != "0" {
				d = "-" + d
			}
			return d
		}
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return strconv.FormatFloat(f, 'g', -1, 64)
	}
	return s // 数值列里的脏数据按原串比, 不替它编一个"应该的值"
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func canonBool(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "0", "false", "f", "no", "off":
		return "0"
	case "1", "true", "t", "yes", "on":
		return "1"
	}
	return strings.TrimSpace(s)
}

// canonHex 二进制列按十六进制小写比(两侧展示形态可能一边 0x.. 一边 \x..)。
func canonHex(s string) string {
	l := strings.ToLower(strings.TrimSpace(s))
	for _, p := range []string{"0x", `\x`, "x'", "'"} {
		if strings.HasPrefix(l, p) {
			h := strings.TrimSuffix(strings.TrimPrefix(l, p), "'")
			if reHex.MatchString(h) {
				return h
			}
		}
	}
	if reHex.MatchString(l) && len(l)%2 == 0 && looksLikeHexBytes(l) {
		return l
	}
	return l
}

// looksLikeHexBytes 防止把一个恰好全是十六进制字符的文本列(如 "deadbeef")当二进制展开。
// 只有 ModeBlob 会走到这里, 所以这层判定的意义是"别把已经是 hex 的串再加工", 保守返回 false 即可。
func looksLikeHexBytes(s string) bool { return len(s) >= 2 }

// canonWall 丢掉时区, 只留 "YYYY-MM-DD HH:MM:SS[.frac]"(小数秒去尾零)。
func canonWall(s string, dateOnly bool) string {
	s = strings.TrimSpace(s)
	if reDateOnly.MatchString(s) {
		if dateOnly {
			return s
		}
		return s + " 00:00:00" // 一侧 DATE 一侧 TIMESTAMP: 日期的时间部分按零点处理(pickMode 已注明)
	}
	m := reWall.FindStringSubmatch(s)
	if m == nil {
		if dateOnly {
			if i := strings.IndexAny(s, " T"); i > 0 {
				return s[:i]
			}
		}
		return s
	}
	base := m[1] + "-" + m[2] + "-" + m[3]
	if dateOnly {
		return base
	}
	frac := m[7]
	if frac != "" {
		frac = strings.TrimRight(frac, "0")
		if frac == "." {
			frac = ""
		}
	}
	return base + " " + m[4] + ":" + m[5] + ":" + m[6] + frac
}

func displayValue(v any) string {
	if v == nil {
		return "NULL"
	}
	s := asString(v)
	if r := []rune(s); len(r) > 200 {
		return string(r[:200]) + "…"
	}
	return s
}

// ── 读侧 SQL(全部后端拼) ──

// selectListFor 按侧取真实列名, 并 AS 成规范名 —— 于是两侧的行 map 键一致,
// 生成的语句又各自用各自的名字。
func selectListFor(cols []DataGridColumn, d Dialect, side string) string {
	parts := make([]string, 0, len(cols))
	for _, c := range cols {
		n := colName(c, side)
		if n == c.Name {
			parts = append(parts, quoteIdent(n, d))
			continue
		}
		parts = append(parts, quoteIdent(n, d)+" AS "+quoteIdent(c.Name, d))
	}
	return strings.Join(parts, ", ")
}

func orderListFor(keys []DataGridColumn, d Dialect, side string) string {
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, quoteIdent(colName(k, side), d))
	}
	return strings.Join(parts, ", ")
}

// keyLiteral 键值进谓词时的字面量。数值键裸写(不裸写的话某些库会对整列做隐式转换而丢掉索引),
// 二进制键还原成 hex 字面量, 其余走通用内联。
func keyLiteral(c DataGridColumn, v any, d Dialect, side string) string {
	if v == nil {
		return "NULL"
	}
	switch c.Mode {
	case ModeNumeric:
		if s := strings.TrimSpace(asString(v)); rePlainNum.MatchString(s) {
			return s
		}
	case ModeBlob:
		if h := canonHex(asString(v)); reHex.MatchString(h) && len(h)%2 == 0 && h != "" {
			return hexBlobFromHex(h, d)
		}
	}
	return quoteValue(v, d, colBase(c, side))
}

// DataPageSQL 键集分页读一批。after 为上一批最后一行的键值(规范名顺序), 空=从头。
// 只用 > 的字典序展开(a>x OR (a=x AND b>y)), 不用行构造式 —— 各家库对
// `(a,b) > (x,y)` 的支持与索引利用差别很大, 展开式全都吃索引。
func DataPageSQL(ref string, cols, keys []DataGridColumn, after []any, d Dialect, side string, limit int) string {
	var b strings.Builder
	b.WriteString("SELECT ")
	b.WriteString(selectListFor(cols, d, side))
	b.WriteString(" FROM ")
	b.WriteString(ref)
	if len(after) == len(keys) && len(keys) > 0 {
		b.WriteString(" WHERE ")
		for i := range keys {
			if i > 0 {
				b.WriteString(" OR ")
			}
			b.WriteString("(")
			for j := 0; j <= i; j++ {
				if j > 0 {
					b.WriteString(" AND ")
				}
				b.WriteString(quoteIdent(colName(keys[j], side), d))
				if j == i {
					b.WriteString(" > ")
				} else {
					b.WriteString(" = ")
				}
				b.WriteString(keyLiteral(keys[j], after[j], d, side))
			}
			b.WriteString(")")
		}
	}
	b.WriteString(" ORDER BY ")
	b.WriteString(orderListFor(keys, d, side))
	if limit > 0 {
		fmt.Fprintf(&b, " LIMIT %d", limit)
	}
	return b.String()
}

// DataLookupSQL 按键批量回查另一侧。单键用 IN; 复合键展开成 (a=.. AND b=..) OR ...,
// 因为行构造式 IN 在 MySQL 上吃不到复合索引。
func DataLookupSQL(ref string, cols, keys []DataGridColumn, tuples [][]any, d Dialect, side string) string {
	if len(keys) == 0 || len(tuples) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("SELECT ")
	b.WriteString(selectListFor(cols, d, side))
	b.WriteString(" FROM ")
	b.WriteString(ref)
	b.WriteString(" WHERE ")
	if len(keys) == 1 {
		k := keys[0]
		b.WriteString(quoteIdent(colName(k, side), d))
		b.WriteString(" IN (")
		for i, tp := range tuples {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(keyLiteral(k, tp[0], d, side))
		}
		b.WriteString(")")
		return b.String()
	}
	for _, tp := range tuples {
		b.WriteString("(")
		for j, k := range keys {
			if j > 0 {
				b.WriteString(" AND ")
			}
			b.WriteString(quoteIdent(colName(k, side), d))
			b.WriteString("=")
			b.WriteString(keyLiteral(k, tp[j], d, side))
		}
		b.WriteString(") OR ")
	}
	return strings.TrimSuffix(b.String(), " OR ")
}

// DataCountSQL 行数(用来如实报告"扫了多少 / 一共多少")。
func DataCountSQL(ref string) string { return "SELECT COUNT(*) AS c FROM " + ref }

// ── 写侧 SQL: 恒写目标侧, 用目标方言 ──

func rowKeyPredicate(keys []DataGridColumn, row map[string]any, d Dialect) string {
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%s", quoteIdent(k.DstName, d), keyLiteral(k, row[k.Name], d, "dst")))
	}
	return strings.Join(parts, " AND ")
}

// DataInsertSQL 基准侧的一行 → 目标侧 INSERT。生成列不写。
func DataInsertSQL(ref string, cols []DataGridColumn, row map[string]any, d Dialect) string {
	var names, vals []string
	for _, c := range cols {
		if c.Generated {
			continue
		}
		names = append(names, quoteIdent(c.DstName, d))
		vals = append(vals, quoteValue(row[c.Name], d, c.DstBase))
	}
	if len(names) == 0 {
		return ""
	}
	return fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", ref, strings.Join(names, ", "), strings.Join(vals, ", "))
}

// DataUpdateSQL 只 SET 真正不同的列 —— 全 SET 会把目标侧没参与比较的列一起覆盖掉,
// 两侧同时在写同一行时就是丢更新。
func DataUpdateSQL(ref string, changed []DataGridColumn, row map[string]any, keys []DataGridColumn, d Dialect) string {
	sets := make([]string, 0, len(changed))
	for _, c := range changed {
		if c.Generated {
			continue
		}
		sets = append(sets, fmt.Sprintf("%s=%s", quoteIdent(c.DstName, d), quoteValue(row[c.Name], d, c.DstBase)))
	}
	if len(sets) == 0 {
		return ""
	}
	return fmt.Sprintf("UPDATE %s SET %s WHERE %s", ref, strings.Join(sets, ", "), rowKeyPredicate(keys, row, d))
}

// DataDeleteSQL 一行一条 DELETE, WHERE 只用键。
func DataDeleteSQL(ref string, keys []DataGridColumn, row map[string]any, d Dialect) string {
	return "DELETE FROM " + ref + " WHERE " + rowKeyPredicate(keys, row, d)
}

// ── 比对主循环 ──

// DataFetch 把"取数"抽出去, 于是主循环能脱离数据库单测。
// 实现方(常驻 handler)负责按 side 选表引用/方言/真实列名, 并保证返回的 map 以规范名为键。
type DataFetch interface {
	Page(ctx context.Context, side string, after []any, limit int) ([]map[string]any, error)
	Lookup(ctx context.Context, side string, tuples [][]any) ([]map[string]any, error)
	Count(ctx context.Context, side string) (int64, error)
}

// DataCompareOptions 比对口径与上限。零值 = 默认(保守)。
type DataCompareOptions struct {
	BatchRows int   `json:"batchRows,omitempty"`
	MaxRows   int64 `json:"maxRows,omitempty"`
	MaxDiffs  int   `json:"maxDiffs,omitempty"`
}

func (o DataCompareOptions) withDefaults() DataCompareOptions {
	if o.BatchRows <= 0 {
		o.BatchRows = 1000 // 与 dbx 同档
	}
	if o.BatchRows > 5000 {
		o.BatchRows = 5000 // 一条 IN/OR 串太长, 也把内存钉住
	}
	if o.MaxRows <= 0 {
		o.MaxRows = 200000
	}
	if o.MaxDiffs <= 0 {
		o.MaxDiffs = 2000
	}
	return o
}

// DataTableSpec 一张表的比对输入。
type DataTableSpec struct {
	Table   string
	DstRef  string // 待改侧的限定表名: 生成的 INSERT/UPDATE/DELETE 都写这一侧
	DstD    Dialect
	Columns []DataGridColumn
	Options DataCompareOptions
	// OnProgress 每读完一批回调一次, 让界面能显示"已扫 N 行 / 已发现 M 条差异"。可为 nil。
	OnProgress func(side string, scanned int64) `json:"-"`
}

func (s *DataTableSpec) report(side string, n int64) {
	if s.OnProgress != nil {
		s.OnProgress(side, n)
	}
}

// Keys 键列(按顺序)。
func (s *DataTableSpec) Keys() []DataGridColumn {
	var out []DataGridColumn
	for _, c := range s.Columns {
		if c.IsKey {
			out = append(out, c)
		}
	}
	return out
}

// DataTableDiff 一张表的数据差异结果。
type DataTableDiff struct {
	Table      string          `json:"table"`
	Status     string          `json:"status"` // same | different | error(与 dbx 的每表状态同口径)
	Error      string          `json:"error,omitempty"`
	KeyColumns []string        `json:"keyColumns"`
	KeySource  string          `json:"keySource,omitempty"`
	Columns    []string        `json:"columns"`
	Skipped    []string        `json:"skipped"`
	SrcRows    int64           `json:"srcRows"`
	DstRows    int64           `json:"dstRows"`
	ScannedSrc int64           `json:"scannedSrc"`
	ScannedDst int64           `json:"scannedDst"`
	Summary    DataDiffSummary `json:"summary"`
	Rows       []DataRowDiff   `json:"rows"`
	Notes      []string        `json:"notes"`
	Partial    bool            `json:"partial"`
	Reason     string          `json:"reason,omitempty"`
}

// setStatus 给界面一个三态结论。partial 单列一档: "没扫完"既不是"一致"也不是"有差异",
// 报成 same 会让人以为两侧同步好了, 那比报错更危险。
func (d *DataTableDiff) setStatus() {
	switch {
	case d.Error != "":
		d.Status = "error"
	case d.Partial:
		d.Status = "partial"
	case d.Summary.Total() > 0:
		d.Status = "different"
	default:
		d.Status = "same"
	}
}

// ErrorDiff 让"这张表比不了"也长成正常结果的形状, 前端不必为错误分支单独兜字段。
func ErrorDiff(table, msg string) *DataTableDiff {
	return &DataTableDiff{Table: table, Status: "error", Error: msg,
		KeyColumns: []string{}, Columns: []string{}, Skipped: []string{}, Notes: []string{}, Rows: []DataRowDiff{}}
}

// DataDiffSummary 即 dbx 的 planSummary: inserts / updates / deletes。
type DataDiffSummary struct {
	Inserts int `json:"inserts"`
	Updates int `json:"updates"`
	Deletes int `json:"deletes"`
}

// Total 三分类合计。
func (s DataDiffSummary) Total() int { return s.Inserts + s.Updates + s.Deletes }

// DataCellDiff 一列的两侧值。
type DataCellDiff struct {
	Column  string `json:"column"`
	Src     string `json:"src"`
	Dst     string `json:"dst"`
	Differs bool   `json:"differs"`
	IsKey   bool   `json:"isKey,omitempty"`
}

// DataRowDiff 一行差异。Key 原样回传给 apply(不指望服务端内存里的结果还在)。
type DataRowDiff struct {
	ID    string         `json:"id"`
	Kind  string         `json:"kind"` // insert(目标缺行) | update(值不同) | delete(基准没有)
	Key   []DataCellDiff `json:"key"`
	Cells []DataCellDiff `json:"cells"` // insert 只有 src 列, delete 只有 dst 列, update 两侧都有
	Sqls  []string       `json:"sqls"`
}

// DiffRow 比较"配到同一键"的两侧行, 返回哪些列不同 + 界面要的逐列对照。
func DiffRow(cols []DataGridColumn, srcRow, dstRow map[string]any) ([]DataGridColumn, []DataCellDiff) {
	var changed []DataGridColumn
	cells := make([]DataCellDiff, 0, len(cols))
	for _, c := range cols {
		if c.IsKey {
			continue
		}
		diff := canonValue(srcRow[c.Name], c.Mode) != canonValue(dstRow[c.Name], c.Mode)
		if diff {
			changed = append(changed, c)
		}
		cells = append(cells, DataCellDiff{
			Column: c.Name, Src: displayValue(srcRow[c.Name]), Dst: displayValue(dstRow[c.Name]), Differs: diff,
		})
	}
	return changed, cells
}

// RowsToStatements 是"比对口径 → 语句"的唯一出口: 主循环与 apply 都走它,
// 于是预览里看到的语句和点下执行时发出的语句出自同一份代码。
// 返回 (语句, 逐列对照, 是否仍有差异)。srcRow/dstRow 哪个为 nil 就表示哪一侧缺行。
func RowsToStatements(spec *DataTableSpec, srcRow, dstRow map[string]any) ([]string, []DataCellDiff, bool) {
	keys := spec.Keys()
	switch {
	case dstRow == nil:
		sql := DataInsertSQL(spec.DstRef, spec.Columns, srcRow, spec.DstD)
		if sql == "" {
			return nil, nil, false
		}
		return []string{sql}, valueCells(spec.Columns, srcRow, nil), true
	case srcRow == nil:
		return []string{DataDeleteSQL(spec.DstRef, keys, dstRow, spec.DstD)}, valueCells(spec.Columns, nil, dstRow), true
	}
	changed, cells := DiffRow(spec.Columns, srcRow, dstRow)
	if len(changed) == 0 {
		return nil, cells, false
	}
	sql := DataUpdateSQL(spec.DstRef, changed, srcRow, keys, spec.DstD)
	if sql == "" {
		return nil, cells, false
	}
	return []string{sql}, cells, true
}

// CompareTableData 跑一张表: 先扫基准侧(找"目标缺行 + 值不一致"), 再扫目标侧(找"基准没有的行")。
func CompareTableData(ctx context.Context, spec *DataTableSpec, f DataFetch) (*DataTableDiff, error) {
	o := spec.Options.withDefaults()
	keys := spec.Keys()
	if len(keys) == 0 {
		return nil, fmt.Errorf("没有指定键列")
	}
	var allCols []string
	for _, c := range spec.Columns {
		allCols = append(allCols, c.Name)
	}
	out := &DataTableDiff{
		Table: spec.Table, Columns: allCols, Skipped: []string{}, Notes: []string{}, Rows: []DataRowDiff{},
	}
	defer out.setStatus() // 指针返回值, 任何 return 路径(含提前停止)都补齐三态, 不靠每处记得调
	for _, k := range keys {
		out.KeyColumns = append(out.KeyColumns, k.Name)
	}

	countsOK := true
	var cerr error
	if out.SrcRows, cerr = f.Count(ctx, "src"); cerr != nil {
		countsOK = false
		out.Notes = append(out.Notes, "基准侧行数统计失败: "+cerr.Error())
	}
	if out.DstRows, cerr = f.Count(ctx, "dst"); cerr != nil {
		countsOK = false
		out.Notes = append(out.Notes, "目标侧行数统计失败: "+cerr.Error())
	}

	// 第一遍: 基准侧 → 目标侧
	var srcCursor []any
	for {
		page, err := f.Page(ctx, "src", srcCursor, o.BatchRows)
		if err != nil {
			return nil, fmt.Errorf("读基准侧失败(已扫 %d 行): %w", out.ScannedSrc, err)
		}
		if len(page) == 0 {
			break
		}
		out.ScannedSrc += int64(len(page))
		spec.report("src", out.ScannedSrc)
		tuples := make([][]any, 0, len(page))
		for _, row := range page {
			tuples = append(tuples, keyTuple(row, keys))
		}
		matches, err := f.Lookup(ctx, "dst", tuples)
		if err != nil {
			return nil, fmt.Errorf("回查目标侧失败: %w", err)
		}
		idx, ierr := indexByKey(matches, keys, "目标")
		if ierr != nil {
			return nil, ierr
		}
		for _, row := range page {
			kc := keyCells(row, keys)
			id := cellsID(row, keys)
			dst, ok := idx[id]
			if !ok {
				dst, ok = idx[strings.ToLower(id)] // 两侧键的排序规则不同(一边 CI 一边 CS)时的兜底认配
			}
			sqls, cells, differs := RowsToStatements(spec, row, dst)
			if !differs {
				continue
			}
			kind := "update"
			if dst == nil {
				kind = "insert"
				out.Summary.Inserts++
			} else {
				out.Summary.Updates++
			}
			out.Rows = append(out.Rows, DataRowDiff{
				ID: kind + "|" + id, Kind: kind, Key: kc, Cells: cells, Sqls: sqls,
			})
			if len(out.Rows) >= o.MaxDiffs {
				out.Partial = true
				out.Reason = fmt.Sprintf("差异行数已达上限 %d, 已停止 —— 还有更多差异没列出来", o.MaxDiffs)
				break
			}
		}
		if out.Partial {
			break
		}
		srcCursor = keyTuple(page[len(page)-1], keys)
		if len(page) < o.BatchRows {
			break
		}
		if out.ScannedSrc >= o.MaxRows {
			out.Partial = true
			out.Reason = fmt.Sprintf("基准侧已扫 %d 行(上限 %d), 未扫完", out.ScannedSrc, o.MaxRows)
			break
		}
		if err := ctx.Err(); err != nil {
			return out, err
		}
	}

	// 第二遍: 目标侧 → 找基准侧没有的行。
	// 只有"第一遍完整扫完 + 一条差异都没有 + 两侧行数确实相等"才允许跳过; 行数统计没成功时
	// 不许跳(两侧都会算成 0, 于是 0==0 把真正的漏报伪装成"已一致")。
	if out.Partial && out.Reason != "" && strings.Contains(out.Reason, "上限") && out.Summary.Total() > 0 {
		out.Notes = append(out.Notes, "已按上限提前停止, 结果不是全表结论")
		return out, nil
	}
	skip := !out.Partial && out.Summary.Total() == 0 && countsOK && out.SrcRows == out.DstRows
	if skip {
		out.Notes = append(out.Notes, "两侧行数一致且基准侧无缺失、无值差, 已跳过反向扫描")
		return out, nil
	}
	var dstCursor []any
	for {
		page, err := f.Page(ctx, "dst", dstCursor, o.BatchRows)
		if err != nil {
			return nil, fmt.Errorf("读目标侧失败(已扫 %d 行): %w", out.ScannedDst, err)
		}
		if len(page) == 0 {
			break
		}
		out.ScannedDst += int64(len(page))
		spec.report("dst", out.ScannedDst)
		tuples := make([][]any, 0, len(page))
		for _, row := range page {
			tuples = append(tuples, keyTuple(row, keys))
		}
		matches, err := f.Lookup(ctx, "src", tuples)
		if err != nil {
			return nil, fmt.Errorf("回查基准侧失败: %w", err)
		}
		idx, ierr := indexByKey(matches, keys, "基准")
		if ierr != nil {
			return nil, ierr
		}
		for _, row := range page {
			id := cellsID(row, keys)
			if _, ok := idx[id]; ok {
				continue
			}
			if _, ok := idx[strings.ToLower(id)]; ok {
				continue
			}
			sqls, cells, differs := RowsToStatements(spec, nil, row)
			if !differs {
				continue
			}
			out.Rows = append(out.Rows, DataRowDiff{
				ID: "delete|" + id, Kind: "delete", Key: keyCells(row, keys), Cells: cells, Sqls: sqls,
			})
			out.Summary.Deletes++
			if len(out.Rows) >= o.MaxDiffs {
				out.Partial = true
				out.Reason = fmt.Sprintf("差异行数已达上限 %d, 已停止 —— 还有更多差异没列出来", o.MaxDiffs)
				break
			}
		}
		if out.Partial {
			break
		}
		dstCursor = keyTuple(page[len(page)-1], keys)
		if len(page) < o.BatchRows {
			break
		}
		if out.ScannedDst >= o.MaxRows {
			out.Partial = true
			out.Reason = fmt.Sprintf("目标侧已扫 %d 行(上限 %d), 未扫完", out.ScannedDst, o.MaxRows)
			break
		}
		if err := ctx.Err(); err != nil {
			return out, err
		}
	}
	return out, nil
}

func keyTuple(row map[string]any, keys []DataGridColumn) []any {
	tp := make([]any, len(keys))
	for i, k := range keys {
		tp[i] = row[k.Name]
	}
	return tp
}

func keyCells(row map[string]any, keys []DataGridColumn) []DataCellDiff {
	out := make([]DataCellDiff, 0, len(keys))
	for _, k := range keys {
		v := displayValue(row[k.Name])
		out = append(out, DataCellDiff{Column: k.Name, Src: v, Dst: v, IsKey: true})
	}
	return out
}

func cellsID(row map[string]any, keys []DataGridColumn) string {
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = canonValue(row[k.Name], k.Mode)
	}
	return strings.Join(parts, "\x1f")
}

// DataKeyID 一行键的标识。导出的理由: apply 要把"前端勾选的键"和"此刻从库里读出来的行"
// 对上, 这个口径必须和比对内部用的**完全同一个函数**, 否则大小写/前后空白会走成两套。
func DataKeyID(keys []DataGridColumn, row map[string]any) string { return cellsID(row, keys) }

// valueCells insert 只给"要写进去的值"(src 侧), delete 只给"要被删掉的东西"(dst 侧),
// 免得界面把同一行印两遍。
func valueCells(cols []DataGridColumn, srcRow, dstRow map[string]any) []DataCellDiff {
	var out []DataCellDiff
	for _, c := range cols {
		if c.IsKey {
			continue
		}
		cell := DataCellDiff{Column: c.Name}
		if srcRow != nil {
			cell.Src = displayValue(srcRow[c.Name])
		}
		if dstRow != nil {
			cell.Dst = displayValue(dstRow[c.Name])
		}
		out = append(out, cell)
	}
	return out
}

// indexByKey 建"键 → 行"。同键两次直接报错并点名列与值: 键不唯一时"一行配一行"根本不成立,
// 走下去生成的 UPDATE/DELETE 会打在不该打的行上(dbx 同样硬失败)。
func indexByKey(rows []map[string]any, keys []DataGridColumn, side string) (map[string]map[string]any, error) {
	out := make(map[string]map[string]any, len(rows))
	for _, r := range rows {
		id := cellsID(r, keys)
		if _, dup := out[id]; dup {
			return nil, fmt.Errorf("%s侧键不唯一: (%s)=%s 出现了不止一次, 无法逐行对齐 —— 请先为该表建主键",
				side, strings.Join(keyNames(keys), ", "), strings.ReplaceAll(id, "\x1f", ", "))
		}
		out[id] = r
		low := strings.ToLower(id)
		if low != id {
			out[low] = r // 供两侧排序规则不同时的兜底认配
		}
	}
	return out, nil
}

func keyNames(keys []DataGridColumn) []string {
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = k.Name
	}
	return out
}
