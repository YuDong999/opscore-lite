// Redis 管理端点: 能力清单 + 键的写操作(SET/EXPIRE/DEL/RENAME)。
//
// 为什么不让前端拼命令文本: 值里可能带空格/引号/换行/二进制, 拼进 `SET k <value>` 就得
// 自己处理转义, 少转一个引号就是一条注入。这里前端只发**意图**(键名 + 值 + ttl), 命令由
// 驱动用 go-redis 的类型化调用发出(见 KeyValueWriter), 从根上没有转义这件事。
//
// 写入照样过 ADR-003 那条链: classifySQLRisk 判风险 -> interceptWrite 查写锁 -> 进审计。
// 审计文本用 sqlaudit.RedactQuery 脱敏(Redis 的值写在命令里, 原样进日志等于抄业务数据)。
package dbmanager

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	gonaviDB "opscore/internal/dbmanager/gonavi/db"
	"opscore/internal/dbmanager/gonavi/sqlaudit"
)

// redisWriteOp 是面板能发的四种写意图。
type redisWriteOp struct {
	Op         string `json:"op"` // set | expire | del | rename
	ID         string `json:"id"`
	Database   string `json:"database"` // db0..dbN
	Key        string `json:"key"`
	ToKey      string `json:"toKey,omitempty"`      // rename 用
	Value      string `json:"value,omitempty"`      // set 用
	ValueB64   bool   `json:"valueBase64,omitempty"` // set: value 是 base64(二进制值)
	TTLSeconds int    `json:"ttlSeconds,omitempty"` // set/expire: <=0 = 不过期 / 清除过期
	Confirm    bool   `json:"confirm"`
}

// redisOps 是允许的操作集合 —— 白名单在这里, 而不是"前端传什么就执行什么"。
var redisOps = map[string]string{
	"set":    "SET",
	"expire": "EXPIRE",
	"del":    "DEL",
	"rename": "RENAME",
}

// redisOpCommandText 把意图渲染成**给人看/给审计用**的命令文本(不是发给 redis 的载荷)。
// 值一律不出现: 要么换掉(脱敏), 要么只报字节数。
func redisOpCommandText(b redisWriteOp) string {
	key := redisQuoteKeyForText(b.Key)
	switch b.Op {
	case "set":
		n := len(b.Value)
		if b.ValueB64 {
			if raw, err := base64.StdEncoding.DecodeString(b.Value); err == nil {
				n = len(raw)
			}
		}
		if b.TTLSeconds > 0 {
			return fmt.Sprintf("SET %s <值 %d 字节> EX %d", key, n, b.TTLSeconds)
		}
		return fmt.Sprintf("SET %s <值 %d 字节>", key, n)
	case "expire":
		if b.TTLSeconds <= 0 {
			return fmt.Sprintf("PERSIST %s", key)
		}
		return fmt.Sprintf("EXPIRE %s %d", key, b.TTLSeconds)
	case "del":
		return fmt.Sprintf("DEL %s", key)
	case "rename":
		return fmt.Sprintf("RENAME %s %s", key, redisQuoteKeyForText(b.ToKey))
	}
	return ""
}

// redisQuoteKeyForText 让键名在文本里可读(带空格的键要看得出来边界)。
func redisQuoteKeyForText(k string) string {
	if k == "" {
		return "\"\""
	}
	if strings.ContainsAny(k, " \t\n\"") {
		return strconv.Quote(k)
	}
	return k
}

// redisOpRisk: 删除/改名是破坏性的, 比"写一个值"高一级; 其余按写操作。
func redisOpRisk(op string) (SqlRisk, string) {
	switch op {
	case "del":
		return RiskHigh, "Redis 键删除: 删掉就没了, 没有回收站"
	case "rename":
		return RiskMedium, "Redis 键改名: 源键就此消失(目标已存在会被拒)"
	case "set":
		return RiskMedium, "Redis 键值写入"
	case "expire":
		return RiskMedium, "Redis 键过期时间修改"
	}
	return RiskMedium, "Redis 键写入"
}

// ===== /api/dbmanager/redis/capabilities =====

func (h *Handlers) handleRedisCapabilities(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	conn, err := h.store.Get(r.URL.Query().Get("id"))
	if err != nil {
		writeErr(w, "连接不存在: "+err.Error(), http.StatusBadRequest)
		return
	}
	if !isRedisEngineName(string(conn.Info.Engine)) {
		writeErr(w, "该连接不是 Redis 数据源", http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "capability": map[string]any{
		"engine": "redis",
		// 六类值各自在网格里长什么样 —— 与驱动 redisColumnsFor 对齐(那边是权威, 这里只是提前告知)
		"valueKinds": []string{"string", "hash", "list", "set", "zset", "stream"},
		"ops":        []string{"set", "expire", "del", "rename"},
		// 诚实说明: 本产品不做没有回滚可言的全库清空
		"note": "写操作走与查询同一道写锁 + 审计。本产品不提供 FLUSHDB/FLUSHALL/CONFIG —— 清库请用 Redis 自己的客户端。",
	}})
}

// ===== /api/dbmanager/redis/write =====

func (h *Handlers) handleRedisWrite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var body redisWriteOp
	r.Body = http.MaxBytesReader(w, r.Body, 16<<20) // 值上限 16MB(redis 单值上限 512MB, 界面不必给那么大)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, "请求体不合法: "+err.Error(), http.StatusBadRequest)
		return
	}
	if !reConnID.MatchString(body.ID) {
		writeErr(w, "id 格式非法", http.StatusBadRequest)
		return
	}
	if _, ok := redisOps[body.Op]; !ok {
		writeErr(w, "不支持的操作: "+body.Op, http.StatusBadRequest)
		return
	}
	if !validKeyName(body.Database) || !validKeyName(body.Key) {
		writeErr(w, "库/键名非法", http.StatusBadRequest)
		return
	}
	if body.Op == "rename" && !validKeyName(body.ToKey) {
		writeErr(w, "目标键名非法", http.StatusBadRequest)
		return
	}
	if body.TTLSeconds < 0 {
		writeErr(w, "ttlSeconds 不能为负(要清除过期请传 0)", http.StatusBadRequest)
		return
	}

	conn, err := h.store.Get(body.ID)
	if err != nil {
		writeErr(w, "获取连接失败: "+err.Error(), http.StatusBadRequest)
		return
	}
	db, engine, err := h.pool.AcquireForSync(body.ID)
	if err != nil {
		writeErr(w, "连接不可用: "+err.Error(), http.StatusBadRequest)
		return
	}
	kv, ok := db.(gonaviDB.KeyValueWriter)
	if !ok {
		writeErr(w, "该驱动不支持键值写入", http.StatusBadRequest)
		return
	}

	// 审计/展示文本: 值不落日志(用引擎自带的 RedactQuery 兜一层, 双保险)
	text := redisOpCommandText(body)
	if red := sqlaudit.RedactQuery(engine, text); red != "" {
		text = red
	}
	risk, reason := redisOpRisk(body.Op)
	if h.interceptWrite(w, conn, body.ID, text, risk, reason, body.Confirm) {
		return
	}

	var execErr error
	var affected int64
	switch body.Op {
	case "set":
		val := []byte(body.Value)
		if body.ValueB64 {
			decoded, derr := base64.StdEncoding.DecodeString(body.Value)
			if derr != nil {
				execErr = fmt.Errorf("base64 值解不开: %w", derr)
				break
			}
			val = decoded
		}
		execErr = kv.SetKey(body.Database, body.Key, val, body.TTLSeconds)
		affected = 1
	case "expire":
		execErr = kv.ExpireKey(body.Database, body.Key, body.TTLSeconds)
		affected = 1
	case "del":
		affected, execErr = kv.DeleteKey(body.Database, body.Key)
	case "rename":
		execErr = kv.RenameKey(body.Database, body.Key, body.ToKey)
		affected = 1
	}

	decision, detail := "executed", reason
	if execErr != nil {
		decision, detail = "failed", execErr.Error()
	}
	h.audit.Append(AuditEntry{
		ConnID: body.ID, ConnName: conn.Info.Name, Engine: engine,
		SQL: text, Risk: string(risk), Decision: decision, Detail: detail,
	})
	if execErr != nil {
		writeJSON(w, map[string]any{"ok": false, "statement": text, "error": execErr.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "statement": text, "affected": affected})
}
