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
	ToKey      string `json:"toKey,omitempty"`       // rename 用
	Value      string `json:"value,omitempty"`       // set 用
	ValueB64   bool   `json:"valueBase64,omitempty"` // set: value 是 base64(二进制值)
	TTLSeconds int    `json:"ttlSeconds,omitempty"`  // set/expire: <=0 = 不过期 / 清除过期
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
	// 拓扑与能力: 前端据此决定要不要显示"频道"页签、要不要警告多库限制。
	topology := "single"
	if r := strings.TrimSpace(conn.Info.Config.Topology); r != "" {
		topology = strings.ToLower(r)
	} else if len(splitHostsList(conn.Info.Config.Hosts)) > 1 {
		topology = "cluster"
	} else if strings.TrimSpace(conn.Info.Config.RedisSentinelMaster) != "" {
		topology = "sentinel"
	}
	writeJSON(w, map[string]any{"ok": true, "capability": map[string]any{
		"engine":   "redis",
		"topology": topology,
		// Cluster 只有 db0: 前端要把"多库"入口收起并说明原因, 而不是让用户点了才报错
		"multiDb": topology != "cluster",
		// 六类值各自在网格里长什么样 —— 与驱动 redisColumnsFor 对齐(那边是权威, 这里只是提前告知)
		"valueKinds": []string{"string", "hash", "list", "set", "zset", "stream"},
		"ops":        []string{"set", "expire", "del", "rename"},
		"pubsub":     true,
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

// ===== /api/dbmanager/redis/pubsub =====
// GET  ?id=&database=            -> 频道快照(纯只读, 不需要订阅)
// POST {id, database, channels, patterns, seconds} -> 实时看一会儿(有限时长, 到点自动退订)

// redisPubSubReq 是 GET(快照)与 POST(实时看一会儿)归一后的入参形状。
type redisPubSubReq struct {
	connID   string
	database string
	channels []string
	patterns []string
	seconds  int
	drain    bool // true = 真的订阅若干秒; false = 只读快照
}

// parseRedisPubSubRequest 按方法把入参解**一次**。
//
// 单独抽出来的原因是这里原先的写法: 先拿 query 里的 id 去查连接, 查不到才想起 POST 的 id
// 在 body 里, 于是同一个 body 被解两次 —— 第二次必然读到空, 返回"invalid body"。
// 也就是说 POST 只有在 URL 上补 ?id= 才走得通, 而这个条件前端并不会满足。
func parseRedisPubSubRequest(r *http.Request) (*redisPubSubReq, string) {
	switch r.Method {
	case http.MethodGet:
		return &redisPubSubReq{
			connID:   strings.TrimSpace(r.URL.Query().Get("id")),
			database: r.URL.Query().Get("database"),
		}, ""
	case http.MethodPost:
		var body struct {
			ID       string   `json:"id"`
			Database string   `json:"database"`
			Channels []string `json:"channels"`
			Patterns []string `json:"patterns"`
			Seconds  int      `json:"seconds"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return nil, "请求体不合法: " + err.Error()
		}
		return &redisPubSubReq{
			connID: body.ID, database: body.Database, channels: body.Channels,
			patterns: body.Patterns, seconds: body.Seconds, drain: true,
		}, ""
	default:
		return nil, "method not allowed"
	}
}

// redisPubSubDrainMaxSeconds 与驱动层的钳制同值 —— 界面选到 60 就够, 再长是常驻订阅了。
const redisPubSubDrainMaxSeconds = 60

// checkRedisPubSubReq 校验(两种方法共用)。返回空串表示通过。
func checkRedisPubSubReq(req *redisPubSubReq) string {
	if !reConnID.MatchString(req.connID) {
		return "id 格式非法"
	}
	if !validKeyName(req.database) {
		return "库名非法"
	}
	// 频道名与键名同一套校验(可能是 : 和 . 组成), 但**不允许控制字符** ——
	// 它会被拼进订阅命令与响应文本。
	for _, ch := range append(append([]string{}, req.channels...), req.patterns...) {
		if !validKeyName(ch) {
			return "频道/模式名非法: " + ch
		}
	}
	if req.drain && len(req.channels) == 0 && len(req.patterns) == 0 {
		return "要先选至少一个频道或模式"
	}
	if req.seconds < 0 || req.seconds > redisPubSubDrainMaxSeconds {
		return "订阅时长要在 0~60 秒之间(到期自动退订)"
	}
	return ""
}

func (h *Handlers) handleRedisPubSub(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 频道清单不该超过 1MB
	}
	req, perr := parseRedisPubSubRequest(r)
	if perr != "" {
		if perr == "method not allowed" {
			methodNotAllowed(w)
			return
		}
		writeErr(w, perr, http.StatusBadRequest)
		return
	}
	if vmsg := checkRedisPubSubReq(req); vmsg != "" {
		writeErr(w, vmsg, http.StatusBadRequest)
		return
	}

	conn, err := h.store.Get(req.connID)
	if err != nil {
		writeErr(w, "连接不存在: "+err.Error(), http.StatusBadRequest)
		return
	}
	if !isRedisEngineName(string(conn.Info.Engine)) {
		writeErr(w, "该连接不是 Redis 数据源", http.StatusBadRequest)
		return
	}
	db, _, aerr := h.pool.AcquireForSync(conn.Info.ID)
	if aerr != nil {
		writeErr(w, "连接不可用: "+aerr.Error(), http.StatusBadRequest)
		return
	}
	ps, ok := db.(gonaviDB.RedisPubSuber)
	if !ok {
		writeErr(w, "该驱动不支持 Pub/Sub", http.StatusBadRequest)
		return
	}

	if !req.drain {
		snap, serr := ps.PubSubSnapshotOf(req.database)
		if serr != nil {
			writeErr(w, "读取频道失败: "+serr.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "snapshot": snap})
		return
	}
	res, derr := ps.PubSubDrain(req.database, req.channels, req.patterns, req.seconds)
	if derr != nil {
		writeErr(w, "订阅失败: "+derr.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "result": res})
}

// ===== /api/dbmanager/redis/publish =====
// POST {id, database, channel, payload, confirm} -> 发布一条消息。
//
// PUBLISH 是**写**: 它会把消息推给所有订阅者(可能触发别人的业务), 所以走与 SET/DEL
// 同一条护栏链(风险分级 + 写锁 + 审计), 且审计里**不记消息正文**(可能含业务数据)。
func (h *Handlers) handleRedisPublish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var body struct {
		ID       string `json:"id"`
		Database string `json:"database"`
		Channel  string `json:"channel"`
		Payload  string `json:"payload"`
		Confirm  bool   `json:"confirm"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, "invalid body", http.StatusBadRequest)
		return
	}
	if !reConnID.MatchString(body.ID) {
		writeErr(w, "id 格式非法", http.StatusBadRequest)
		return
	}
	if !validKeyName(body.Database) || !validKeyName(body.Channel) {
		writeErr(w, "库/频道名非法", http.StatusBadRequest)
		return
	}
	if body.Payload == "" {
		writeErr(w, "消息正文不能为空", http.StatusBadRequest)
		return
	}
	conn, err := h.store.Get(body.ID)
	if err != nil {
		writeErr(w, "连接不存在: "+err.Error(), http.StatusBadRequest)
		return
	}
	db, engine, aerr := h.pool.AcquireForSync(body.ID)
	if aerr != nil {
		writeErr(w, "连接不可用: "+aerr.Error(), http.StatusBadRequest)
		return
	}
	ps, ok := db.(gonaviDB.RedisPubSuber)
	if !ok {
		writeErr(w, "该驱动不支持 Pub/Sub", http.StatusBadRequest)
		return
	}
	// 审计文本**不带正文**: 只记"往哪个频道发了多少字节"(与 MQ 的 PRODUCE 同一口径)
	auditText := fmt.Sprintf("PUBLISH %s <消息 %d 字节>", body.Channel, len(body.Payload))
	risk, reason := RiskMedium, "Redis 发布消息: 会推给该频道的所有订阅者"
	if h.interceptWrite(w, conn, body.ID, auditText, risk, reason, body.Confirm) {
		return
	}
	n, perr := ps.PublishTo(body.Database, body.Channel, body.Payload)
	decision, detail := "executed", reason
	if perr != nil {
		decision, detail = "failed", perr.Error()
	}
	h.audit.Append(AuditEntry{
		ConnID: body.ID, ConnName: conn.Info.Name, Engine: engine,
		SQL: auditText, Risk: string(risk), Decision: decision, Detail: detail,
	})
	if perr != nil {
		writeJSON(w, map[string]any{"ok": false, "statement": auditText, "error": perr.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "statement": auditText, "receivers": n})
}
