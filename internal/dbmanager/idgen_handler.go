package dbmanager

// 生成值(ID 类)需要的两个后端能力。
//
//	next-id   : "递增 ID" 的起点 —— SELECT COALESCE(MAX(col),0)+1。语句在这里按方言拼(标识符白名单+引用),
//	            与 apply-edit/apply-delete/apply-ddl 同一套; 只读, 不动写锁。
//	id-worker : "雪花 ID" 的 workerId —— 前端拿不到本机 IP/进程号, 而"由机器派生"是业界最普遍的默认
//	            (MyBatis-Plus / Hutool 的 IdWorker 就是这么做的), 所以由后端算一次给出。
//	            纯随机是 dbx 的做法(它没有后端), 我们把它留作可选项而不是默认。

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"

	dbsync "opscore/internal/dbmanager/sync"
)

// 雪花布局里 workerId 占 10 bit(dbx 同构: (ts-epoch)<<22 | workerId<<12 | seq)
const snowflakeWorkerIDBits = 1024

var (
	idWorkerOnce   sync.Once
	idWorkerCached int64
)

// deriveWorkerID 用"本机 IPv4 末段 + 进程号"混进 10 bit 空间。
// 同一台机器重启后 PID 变、同机多实例 PID 不同 → 一般能错开; 跨机器则靠 IP 末段。
// 这不是强保证(那需要 ZK/DB 注册), 但对"桌面端填几个候选 ID"的场景足够, 且零配置。
func deriveWorkerID() int64 {
	var ipLast int64
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			n, ok := a.(*net.IPNet)
			if !ok || n.IP.IsLoopback() {
				continue
			}
			if v4 := n.IP.To4(); v4 != nil {
				ipLast = int64(v4[3])
				break
			}
		}
	}
	h := fnv.New64a()
	fmt.Fprintf(h, "%d|%d", ipLast, os.Getpid())
	return int64(h.Sum64() % snowflakeWorkerIDBits)
}

func (h *Handlers) handleIDWorker(w http.ResponseWriter, r *http.Request) {
	idWorkerOnce.Do(func() { idWorkerCached = deriveWorkerID() })
	writeJSON(w, map[string]any{
		"ok":       true,
		"workerId": idWorkerCached,
		"source":   "machine",
		"bits":     snowflakeWorkerIDBits,
	})
}

type nextIDBody struct {
	ID       string `json:"id"`
	Database string `json:"database"`
	Table    string `json:"table"`  // 允许 "schema.table" 两段
	Column   string `json:"column"` // 目标列(通常是自增/主键列)
}

func (h *Handlers) handleNextID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var body nextIDBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, "invalid body", http.StatusBadRequest)
		return
	}
	if !validIdentifier(body.Column) {
		writeErr(w, "列名非法: "+body.Column, http.StatusBadRequest)
		return
	}
	database, table, tableSchema, ok := validateTableTarget(w, body.ID, body.Database, body.Table)
	if !ok {
		return
	}
	if _, err := h.store.Get(body.ID); err != nil {
		writeErr(w, "连接不存在: "+err.Error(), http.StatusBadRequest)
		return
	}
	db, engine, err := h.pool.AcquireForSync(body.ID)
	if err != nil {
		writeErr(w, "连接不可用: "+err.Error(), http.StatusBadRequest)
		return
	}
	dialect := dbsync.EngineDialect(engine)
	if dialect == "" {
		writeErr(w, "该引擎暂不支持在线取号", http.StatusBadRequest)
		return
	}

	qi := func(n string) string { return dbsync.QuoteIdent(n, dialect) }
	tableRef := qi(database) + "." + qi(table)
	if tableSchema != "" {
		tableRef = qi(tableSchema) + "." + qi(table)
	}
	// COALESCE 让空表也给出 1; 别名大小写各驱动不一致, 读值时按多种拼法兜底
	sqlText := fmt.Sprintf("SELECT COALESCE(MAX(%s), 0) + 1 AS next_id FROM %s", qi(body.Column), tableRef)

	rows, _, err := db.Query(sqlText)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "sql": sqlText, "error": err.Error()})
		return
	}
	if len(rows) == 0 {
		writeJSON(w, map[string]any{"ok": false, "sql": sqlText, "error": "取号查询没有返回行"})
		return
	}
	next := idLiteralString(rowValueByName(rows[0], "next_id"))
	if next == "" {
		writeJSON(w, map[string]any{"ok": false, "sql": sqlText, "error": "该列不是可取号的数值列"})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "sql": sqlText, "next": next})
}

// rowValueByName 按列名取值, 忽略大小写与方言加的引号 —— 各家驱动返回的键拼法不统一
// (MySQL 给 next_id, 有的驱动给 NEXT_ID, PG 小写, 个别还带引号)。
func rowValueByName(row map[string]any, name string) any {
	if v, ok := row[name]; ok {
		return v
	}
	for k, v := range row {
		if strings.EqualFold(strings.Trim(k, `"`), name) {
			return v
		}
	}
	return nil
}

// idLiteralString 把驱动返回的数值规范成十进制字符串。
// 必须走字符串: 雪花 ID 是 19 位, 过一遍 JSON number 会被 JS 的 double 截掉精度。
func idLiteralString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case int64:
		return strconv.FormatInt(t, 10)
	case int:
		return strconv.Itoa(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case []byte:
		return string(t)
	case string:
		return t
	default:
		return fmt.Sprintf("%v", t)
	}
}
