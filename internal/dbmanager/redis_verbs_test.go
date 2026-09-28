// Redis 的风险分级/通道分流测试。
//
// 这一组断言防的是同一种静默失效: Redis 的命令一个都不在 SQL 词表里, 一旦分类器退回"按首词判",
// GET/TTL/TYPE 就会被当成"未知语句类型 → 按写操作处理" → 只读锁把读也拦下, 而且 isReadOnlySQL=false
// 会把它们送到写通道去。MQ 那轮已经踩过一次(RocketMQ/Kafka 的 CONSUME), 所以两张表共用一个入口,
// 也共用一个测试。
package dbmanager

import (
	"testing"

	syncpkg "opscore/internal/dbmanager/sync"
)

func TestRedisReadCommandsAreClassifiedReadOnly(t *testing.T) {
	reads := []string{
		"GET user:1", "TTL user:1", "TYPE user:1", "EXISTS user:1", "DBSIZE", "INFO",
		"SCAN 0 MATCH user:* COUNT 50", "HGETALL cfg:app", "LRANGE queue 0 10",
		"SMEMBERS tags", "ZRANGE rank 0 5", "XRANGE stream:1 - +", "MEMORY USAGE big",
		"SELECT * FROM user:1", "SELECT 3",
	}
	for _, sql := range reads {
		if risk, reason := classifySQLRisk("redis", sql); risk != RiskSafe {
			t.Errorf("redis 读命令 %q 被判成 %v (%s), 期望 RiskSafe", sql, risk, reason)
		}
		if !isReadOnlySQL("redis", sql) {
			t.Errorf("redis 读命令 %q 会被送到写通道", sql)
		}
	}
}

func TestRedisWritesStillNeedUnlock(t *testing.T) {
	writes := []string{
		"SET user:1 alice", "DEL user:1", "UNLINK a b", "EXPIRE user:1 60", "HSET cfg:app k v",
		"LPUSH q x", "RENAME a b", "FLUSHALL", "CONFIG SET maxmemory 0",
	}
	for _, sql := range writes {
		if risk, _ := classifySQLRisk("redis", sql); risk == RiskSafe {
			t.Errorf("redis 写命令 %q 被判成只读 —— 只读锁就拦不住它了", sql)
		}
		if isReadOnlySQL("redis", sql) {
			t.Errorf("redis 写命令 %q 会被路由到查询通道", sql)
		}
	}
	// SQL 引擎不受这张表影响
	if risk, _ := classifySQLRisk("mysql", "GET user:1"); risk == RiskSafe {
		t.Error("mysql: GET 被判成只读了, Redis 的表泄漏到 SQL 引擎")
	}
}

// Redis 的值里带分号很常见(比如 "a;b"), 按 SQL 分句会被切成两条命令。
func TestRedisValuesNotSplitOnSemicolon(t *testing.T) {
	sql := "SET note 结论;待议"
	got := splitStatementsForEngine("redis", sql, syncpkg.DialectMySQL)
	if len(got) != 1 || got[0] != sql {
		t.Errorf("Redis 命令被按分号切开了: %#v", got)
	}
}
