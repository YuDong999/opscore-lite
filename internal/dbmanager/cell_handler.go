// 单元格二进制读取(P1-13): 按主键定位一行, 取某一列的值, 支持分片续读。
//
// 为什么需要它: 数据网格为了不把界面撑爆, 对大值只给预览(见 scan_rows.go 的
// [BLOB preview: N/M bytes]); 用户的正当需求是"把这一格完整拿出来"或"下载成文件"。
// 整结果集导出覆盖不到这个场景(格子级的取回 / 续读)。
//
// 定位方式是**按主键重查**而不是"记住结果集里的第几行": 结果集可能带 ORDER BY、
// 也可能是多表拼接, 而主键是唯一能可靠指回某一行的东西 —— 与 apply-edit 同一口径。
//
// 只读, 但仍走与查询一致的路径(标识符白名单 + 方言引用 + 字面量转义), 值不落审计。
package dbmanager

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	gonaviDB "opscore/internal/dbmanager/gonavi/db"
	syncpkg "opscore/internal/dbmanager/sync"
)

const (
	// cellChunkDefault / cellChunkMax: 一次最多回 1MiB。base64 之后约 1.37MiB,
	// 仍在一次 HTTP 响应的合理范围内; 前端按返回的 nextOffset 续读, 不设总长上限。
	cellChunkDefault = 64 * 1024
	cellChunkMax     = 1024 * 1024
)

// cellReadBody: 要读哪一格。PkCols 与 Row 的语义与 apply-edit 完全一致(主键列名 → 值),
// 所以前端可以把它从自己已有的那行数据里直接拿出来。
type cellReadBody struct {
	ID       string         `json:"id"`
	Database string         `json:"database"`
	Table    string         `json:"table"`
	Schema   string         `json:"schema,omitempty"`
	Column   string         `json:"column"`
	PkCols   []string       `json:"pkCols"`
	Row      map[string]any `json:"row"`
	Offset   int            `json:"offset"`   // 字节偏移(从 0 开始)
	Limit    int            `json:"limit"`    // 本片字节数上限
	AsBase64 bool           `json:"asBase64"` // true=回 base64(二进制安全); 默认按文本回
}

// handleCellRead GET/POST -> 读一格的一个分片
func (h *Handlers) handleCellRead(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var body cellReadBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, "invalid body", http.StatusBadRequest)
		return
	}
	if !reConnID.MatchString(body.ID) {
		writeErr(w, "id 格式非法", http.StatusBadRequest)
		return
	}
	for _, n := range []string{body.Database, body.Table, body.Column} {
		if !validIdentifier(n) {
			writeErr(w, "库/表/列名非法", http.StatusBadRequest)
			return
		}
	}
	if body.Schema != "" && !validIdentifier(body.Schema) {
		writeErr(w, "模式名非法", http.StatusBadRequest)
		return
	}
	if len(body.PkCols) == 0 {
		writeErr(w, "缺少主键列: 没有主键就无法可靠地定位到某一格", http.StatusBadRequest)
		return
	}
	for _, pk := range body.PkCols {
		if !validIdentifier(pk) {
			writeErr(w, "主键列名非法", http.StatusBadRequest)
			return
		}
		if _, ok := body.Row[pk]; !ok {
			writeErr(w, "主键列 "+pk+" 的值缺失", http.StatusBadRequest)
			return
		}
	}
	// 主键里带 NULL 的话 WHERE 语义会变(要用 IS NULL), 这里直接拒 —— 定位不可靠就不猜
	for _, pk := range body.PkCols {
		if body.Row[pk] == nil {
			writeErr(w, "主键列 "+pk+" 的值是空的, 无法定位这一行", http.StatusBadRequest)
			return
		}
	}
	if body.Offset < 0 {
		writeErr(w, "offset 不能为负", http.StatusBadRequest)
		return
	}
	limit := body.Limit
	if limit <= 0 {
		limit = cellChunkDefault
	}
	if limit > cellChunkMax {
		limit = cellChunkMax
	}

	db, engine, err := h.pool.AcquireForSync(body.ID)
	if err != nil {
		writeErr(w, "连接不可用: "+err.Error(), http.StatusBadRequest)
		return
	}
	dialect := syncpkg.EngineDialect(engine)
	if dialect == "" {
		writeErr(w, "该引擎暂不支持单元格读取", http.StatusBadRequest)
		return
	}

	qi := func(n string) string { return syncpkg.QuoteIdent(n, dialect) }
	tn := qi(body.Database) + "." + qi(body.Table)
	if body.Schema != "" {
		tn = qi(body.Schema) + "." + qi(body.Table)
	}
	var conds []string
	for _, pk := range body.PkCols {
		conds = append(conds, fmt.Sprintf("%s = %s", qi(pk), gonaviDB.FormatLiteralForDialect(engine, body.Row[pk])))
	}

	// **必须用 r.Context(), 不能传 nil** —— sync.QueryRows 会把 ctx 原样交给驱动的
	// QueryContext, 而 database/sql 拿到 nil ctx 会直接空指针 panic(2026-09-28 实测踩到,
	// 日志里是 database/sql.(*DB).conn 的 nil deref)。顺便也让客户端断开能取消这次查询。
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	// 总长度先单独问一次: 前端要用它算"还有多少没读"和进度条。按方言取长度的函数不同。
	lenExpr := lengthExprFor(dialect, qi(body.Column))
	total := int64(-1)
	if rows, _, qerr := syncpkg.QueryRows(ctx, db, fmt.Sprintf("SELECT %s AS L FROM %s WHERE %s",
		lenExpr, tn, strings.Join(conds, " AND "))); qerr == nil && len(rows) > 0 {
		total = toInt64Loose(rows[0]["L"])
	}

	// 再取这一片。MySQL 系用 SUBSTRING, PG 用 substr(两者都从 1 开始计数, 与 offset 的 0 基换算 +1)
	chunkExpr := chunkExprFor(dialect, qi(body.Column), body.Offset, limit)
	sqlText := fmt.Sprintf("SELECT %s AS CHUNK FROM %s WHERE %s", chunkExpr, tn, strings.Join(conds, " AND "))
	rows, _, qerr := syncpkg.QueryRows(ctx, db, sqlText)
	if qerr != nil {
		writeErr(w, "读取单元格失败: "+qerr.Error(), http.StatusBadRequest)
		return
	}
	var raw []byte
	if len(rows) > 0 {
		switch v := rows[0]["CHUNK"].(type) {
		case nil:
			raw = nil
		case []byte:
			raw = v
		case string:
			raw = []byte(v)
		default:
			raw = []byte(fmt.Sprint(v))
		}
	}

	next := int64(-1)
	if total >= 0 && int64(body.Offset)+int64(len(raw)) < total {
		next = int64(body.Offset) + int64(len(raw))
	}

	out := map[string]any{
		"ok":         true,
		"offset":     body.Offset,
		"bytes":      len(raw),
		"total":      total, // -1 = 长度取不到(如某些大对象类型), 前端按"读完才停"
		"nextOffset": next,  // -1 = 读完了
		"column":     body.Column,
	}
	if body.AsBase64 {
		out["encoding"] = "base64"
		out["body"] = base64.StdEncoding.EncodeToString(raw)
	} else {
		out["text"] = string(raw)
	}
	writeJSON(w, out)
}

// lengthExprFor 返回"取字节长度"的表达式。方言不同函数名也不同。
// handleCellDownload 把一整格流式下载成文件(P1-13)。
//
// 与 cell/read 的区别: read 是"给界面看"(分片 + base64 可选), download 是"存成本地文件"。
// 这里边读边写响应, **不把整个值读进内存** —— 一个 2GB 的 LOB 不该把服务端撑爆。
// 文件名由调用方给, 但这里再做一次清洗: 去掉路径分隔与控制字符, 防止响应头注入/目录穿越。
func (h *Handlers) handleCellDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var body cellReadBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, "invalid body", http.StatusBadRequest)
		return
	}
	if !reConnID.MatchString(body.ID) {
		writeErr(w, "id 格式非法", http.StatusBadRequest)
		return
	}
	for _, n := range []string{body.Database, body.Table, body.Column} {
		if !validIdentifier(n) {
			writeErr(w, "库/表/列名非法", http.StatusBadRequest)
			return
		}
	}
	if body.Schema != "" && !validIdentifier(body.Schema) {
		writeErr(w, "模式名非法", http.StatusBadRequest)
		return
	}
	if len(body.PkCols) == 0 {
		writeErr(w, "缺少主键列: 没有主键就无法可靠地定位到某一格", http.StatusBadRequest)
		return
	}
	for _, pk := range body.PkCols {
		if !validIdentifier(pk) {
			writeErr(w, "主键列名非法", http.StatusBadRequest)
			return
		}
		if v, ok := body.Row[pk]; !ok || v == nil {
			writeErr(w, "主键列 "+pk+" 的值缺失或为空, 无法定位这一行", http.StatusBadRequest)
			return
		}
	}

	db, engine, err := h.pool.AcquireForSync(body.ID)
	if err != nil {
		writeErr(w, "连接不可用: "+err.Error(), http.StatusBadRequest)
		return
	}
	dialect := syncpkg.EngineDialect(engine)
	if dialect == "" {
		writeErr(w, "该引擎暂不支持单元格下载", http.StatusBadRequest)
		return
	}
	qi := func(n string) string { return syncpkg.QuoteIdent(n, dialect) }
	tn := qi(body.Database) + "." + qi(body.Table)
	if body.Schema != "" {
		tn = qi(body.Schema) + "." + qi(body.Table)
	}
	var conds []string
	for _, pk := range body.PkCols {
		conds = append(conds, fmt.Sprintf("%s = %s", qi(pk), gonaviDB.FormatLiteralForDialect(engine, body.Row[pk])))
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+safeDownloadName(body.Column)+`"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")

	// 同样必须用真 ctx(不能 nil, 见 handleCellRead 的注释); 下载可能很久, 给宽一点。
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()

	// 分片拉, 边拉边写: 每片 1MiB, 直到某片不足一片(读完了)
	const chunk = 1024 * 1024
	offset := 0
	for {
		sqlText := fmt.Sprintf("SELECT %s AS CHUNK FROM %s WHERE %s",
			chunkExprFor(dialect, qi(body.Column), offset, chunk), tn, strings.Join(conds, " AND "))
		rows, _, qerr := syncpkg.QueryRows(ctx, db, sqlText)
		if qerr != nil {
			// 响应体已经开始写了, 状态码改不了; 只能把错误追加进去再中断
			_, _ = w.Write([]byte("\n[读取中断: " + qerr.Error() + "]"))
			return
		}
		var buf []byte
		if len(rows) > 0 {
			switch v := rows[0]["CHUNK"].(type) {
			case []byte:
				buf = v
			case string:
				buf = []byte(v)
			case nil:
			default:
				buf = []byte(fmt.Sprint(v))
			}
		}
		if _, werr := w.Write(buf); werr != nil {
			return // 客户端断了(下载取消) —— 正常收尾
		}
		offset += len(buf)
		if len(buf) < chunk {
			return
		}
	}
}

// safeDownloadName 清洗下载文件名: 去路径分隔与控制字符, 兜底给个名字。
func safeDownloadName(column string) string {
	clean := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '/' || r == 0x5c || r == '"' || r == ':' || r == '*' ||
			r == '?' || r == '<' || r == '>' || r == '|' {
			return '_'
		}
		return r
	}, strings.TrimSpace(column))
	if clean == "" {
		clean = "cell"
	}
	if len(clean) > 80 {
		clean = clean[:80]
	}
	return clean + ".bin"
}

func lengthExprFor(dialect syncpkg.Dialect, col string) string {
	switch dialect {
	case syncpkg.DialectPostgres:
		// octet_length 对标量/二进制都成立; char_length 对 text 是字符数, 分片按字节走会对不上
		return "octet_length(" + col + ")"
	default:
		return "LENGTH(" + col + ")"
	}
}

// chunkExprFor 返回"取第 [offset, offset+limit) 段"的表达式。
// SQL 的 substring 下标从 1 开始, 所以 offset 要 +1。
func chunkExprFor(dialect syncpkg.Dialect, col string, offset, limit int) string {
	start := offset + 1
	switch dialect {
	case syncpkg.DialectPostgres:
		return fmt.Sprintf("substr(%s, %d, %d)", col, start, limit)
	default:
		return fmt.Sprintf("SUBSTRING(%s, %d, %d)", col, start, limit)
	}
}

// toInt64Loose 把驱动返回的各种数值形态转成 int64(取不到就给 -1)。
func toInt64Loose(v any) int64 {
	switch t := v.(type) {
	case nil:
		return -1
	case int64:
		return t
	case int:
		return int64(t)
	case int32:
		return int64(t)
	case []byte:
		if n, err := strconv.ParseInt(string(t), 10, 64); err == nil {
			return n
		}
	case string:
		if n, err := strconv.ParseInt(t, 10, 64); err == nil {
			return n
		}
	}
	return -1
}
