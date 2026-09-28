// MQ 引擎的"读"动词白名单。
//
// 为什么单独要这一份: 风险分类(risk.go)和 Query/Exec 通道分流(service.go)都按 SQL 首词判,
// 而 MQ 用的是伪 SQL —— CONSUME / UNSUBSCRIBE 不在 SQL 词表里, 于是:
//
//	① classifySQLRisk 落到 default 分支 → "未知语句类型(CONSUME), 按写操作处理" → 只读锁直接拦下;
//	② 就算解锁放行, isReadOnlySQL=false → 走 Exec 通道, 而 Exec 只收 JSON 发布命令
//	  → 报"Kafka 写入命令必须是 JSON"。
//
// 两条动词端到端跑不通(2026-09-27 复核)。SHOW / DESCRIBE / SELECT 开头的那些本来就是只读, 不用补。
//
// 口径与驱动层逐条对齐, 只在驱动确实会当读处理的引擎上生效:
//
//	kafka_impl.go:1188-1192, rocketmq_impl.go:1366-1371,
//	rabbitmq_impl.go:871-877, mqtt_impl.go:1582-1585
//
// 副作用方面核实过: Kafka 分组读取 CommitInterval=0 且全文件没有 CommitOffsets/CommitMessages
// (kafka_impl.go:1015) —— 只取不提交位点; RabbitMQ 取消息用 ack_requeue_true
// (rabbitmq_impl.go:814) —— 读完退回队列; MQTT 的 UNSUBSCRIBE 撤的是本连接自己的订阅
// (mqtt_impl.go:578), 不动 broker 上的数据。
package dbmanager

import (
	"regexp"
	"strings"

	syncpkg "opscore/internal/dbmanager/sync"
)

// MQ 名字段的引用形式: 双引号 / 反引号 / 裸标识符(与驱动一致)
const mqNamePat = `(?:"[^"]*"|` + "`" + `[^` + "`" + `]*` + "`" + `|[^\s;]+)`

func mqConsumeRE(group bool) *regexp.Regexp {
	if group { // 只有 Kafka 认 CONSUME GROUP g FROM t
		return regexp.MustCompile(`(?i)^\s*CONSUME(?:\s+GROUP\s+` + mqNamePat + `)?\s+FROM\s+` + mqNamePat)
	}
	return regexp.MustCompile(`(?i)^\s*CONSUME\s+FROM\s+` + mqNamePat)
}

// mqReadVerbs: 各 MQ 引擎额外的只读动词。空表/没有键 = 该引擎没有需要补的动词。
var mqReadVerbs = map[string][]*regexp.Regexp{
	"kafka":    {mqConsumeRE(true)},
	"rocketmq": {mqConsumeRE(false)},
	"rabbitmq": {mqConsumeRE(false)},
	"mqtt": {
		mqConsumeRE(false),
		regexp.MustCompile(`(?i)^\s*UNSUBSCRIBE\s+FROM\s+` + mqNamePat + `\s*;?\s*$`),
	},
}

// mqEngineKind 返回 MQ 引擎的规范键, 非 MQ 引擎返回 ""。
func mqEngineKind(engine string) string {
	switch strings.ToLower(strings.TrimSpace(engine)) {
	case "kafka", "rocketmq", "rabbitmq", "mqtt":
		return strings.ToLower(strings.TrimSpace(engine))
	}
	return ""
}

func isMQEngine(engine string) bool { return mqEngineKind(engine) != "" }

// isNoSQLReadStatement 判定"非 SQL 引擎"(MQ / Redis)的只读命令。
// Redis 的读命令一个都不在 SQL 词表里: 按首词判会全部落到 default → "未知语句类型, 按写操作处理"
// → 只读锁把 GET/TTL 也拦下, 还会因为 isReadOnlySQL=false 被送到 Exec 通道。
// 与 MQ 是同一个坑, 所以共用这个入口。
var redisReadWords = map[string]bool{
	"GET": true, "MGET": true, "STRLEN": true, "GETRANGE": true, "TTL": true, "PTTL": true,
	"TYPE": true, "EXISTS": true, "DBSIZE": true, "INFO": true, "SCAN": true, "HSCAN": true,
	"SSCAN": true, "ZSCAN": true, "HGET": true, "HMGET": true, "HGETALL": true, "HKEYS": true,
	"HVALS": true, "HLEN": true, "HEXISTS": true, "HSTRLEN": true, "LRANGE": true, "LLEN": true,
	"LINDEX": true, "SMEMBERS": true, "SCARD": true, "SISMEMBER": true, "ZRANGE": true,
	"ZCARD": true, "ZCOUNT": true, "ZSCORE": true, "XLEN": true, "XRANGE": true, "XREVRANGE": true,
	"MEMORY": true, "OBJECT": true, "KEYS": true, // KEYS 在引擎里被拒(服务端 O(N) 且阻塞), 这里只算"不是写"
}

func isNoSQLReadStatement(engine, sqlText string) bool {
	if isMQEngine(engine) {
		return isMQReadStatement(engine, sqlText)
	}
	if strings.ToLower(strings.TrimSpace(engine)) != "redis" {
		return false
	}
	t := strings.TrimSpace(stripSQLComments(sqlText))
	if t == "" {
		return false
	}
	// SELECT <n> 是切库, 由 forbiddenSwitchReason 单独判; SELECT * FROM <key> 是读键
	if strings.HasPrefix(strings.ToUpper(t), "SELECT") {
		return true
	}
	w := regexp.MustCompile(`^\s*([A-Za-z]+)`).FindStringSubmatch(t)
	if w == nil {
		return false
	}
	return redisReadWords[strings.ToUpper(w[1])]
}

func isRedisEngineName(engine string) bool {
	return strings.EqualFold(strings.TrimSpace(engine), "redis")
}

// isNoSQLEngine 是不是"没有 SQL 这回事"的引擎: 分句、通道分流、风险分类都要按这个走。
func isNoSQLEngine(engine string) bool {
	e := strings.ToLower(strings.TrimSpace(engine))
	return isMQEngine(e) || e == "redis"
}

// isMQReadStatement 判定这条伪 SQL 是不是该引擎的只读动词。
// 注意: 这里只放宽"走查询通道 + 不算写风险", 能不能真执行仍由驱动自己的语法决定。
func isMQReadStatement(engine, sqlText string) bool {
	re, ok := mqReadVerbs[mqEngineKind(engine)]
	if !ok {
		return false
	}
	t := strings.TrimSpace(sqlText)
	if t == "" {
		return false
	}
	for _, r := range re {
		if r.MatchString(t) {
			return true
		}
	}
	return false
}

// splitStatementsForEngine 按引擎决定要不要做顶层分号拆分。
// MQ 的伪 SQL 不是 SQL, 而发布命令是 JSON —— 载荷里的分号会被 SQL 分句器切坏
// (切开后每段都不是合法 JSON, 只得到一句没头没尾的报错), 所以整条原样交给驱动。
func splitStatementsForEngine(engine, sqlText string, d syncpkg.Dialect) []string {
	if isNoSQLEngine(engine) {
		return []string{strings.TrimSpace(sqlText)}
	}
	return splitSQLStatements(sqlText, d)
}
