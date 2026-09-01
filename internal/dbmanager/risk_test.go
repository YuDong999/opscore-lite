package dbmanager

import (
	"strings"
	"testing"

)

func TestClassifySQLRisk(t *testing.T) {
	cases := []struct {
		name    string
		engine  string
		sql     string
		want    SqlRisk
		comment string // 期望原因包含的关键词(空=不校验)
	}{
		// 只读
		{"简单查询", "mysql", "SELECT * FROM users", RiskSafe, ""},
		{"带注释的查询", "mysql", "-- 注释 DROP TABLE\nSELECT 1", RiskSafe, ""},
		{"块注释查询", "mysql", "/* DROP DATABASE x */ SHOW TABLES", RiskSafe, ""},
		{"SHOW", "mysql", "SHOW DATABASES", RiskSafe, ""},
		{"DESC", "mysql", "DESCRIBE users", RiskSafe, ""},
		{"EXPLAIN", "postgres", "EXPLAIN SELECT 1", RiskSafe, ""},
		{"PG WITH 查询", "postgres", "WITH t AS (SELECT 1) SELECT * FROM t", RiskSafe, ""},
		// 写
		{"INSERT", "mysql", "INSERT INTO t VALUES (1)", RiskMedium, ""},
		{"UPDATE 带 WHERE", "mysql", "UPDATE t SET a=1 WHERE id=1", RiskMedium, ""},
		{"DELETE 带 WHERE", "mysql", "DELETE FROM t WHERE id=1", RiskMedium, ""},
		{"PG 可写 CTE", "postgres", "WITH t AS (SELECT 1) INSERT INTO x SELECT * FROM t", RiskMedium, "CTE"},
		// DDL
		{"CREATE TABLE", "mysql", "CREATE TABLE t (id INT)", RiskHigh, ""},
		{"ALTER TABLE", "mysql", "ALTER TABLE t ADD COLUMN c INT", RiskHigh, ""},
		{"GRANT", "mysql", "GRANT ALL ON *.* TO u", RiskHigh, ""},
		// 破坏性
		{"DROP TABLE", "mysql", "DROP TABLE t", RiskCritical, ""},
		{"DROP DATABASE", "mysql", "DROP DATABASE db1", RiskCritical, ""},
		{"TRUNCATE", "mysql", "TRUNCATE TABLE t", RiskCritical, ""},
		{"UPDATE 无 WHERE", "mysql", "UPDATE t SET a=1", RiskCritical, "WHERE"},
		{"DELETE 无 WHERE", "mysql", "DELETE FROM t", RiskCritical, "WHERE"},
		// 未知/兜底
		{"未知关键字", "mysql", "MERGE INTO t USING s ON 1=1", RiskMedium, "未知"},
		{"空语句", "mysql", "   ", RiskSafe, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, reason := classifySQLRisk(c.engine, c.sql)
			if got != c.want {
				t.Errorf("classifySQLRisk(%q) = %s (reason=%q), want %s", c.sql, got, reason, c.want)
			}
			if c.comment != "" && !strings.Contains(reason, c.comment) {
				t.Errorf("reason = %q, 期望包含 %q", reason, c.comment)
			}
		})
	}
}

func TestForbiddenSwitch(t *testing.T) {
	cases := []struct {
		name    string
		engine  string
		sql     string
		blocked bool
	}{
		{"USE 开头", "mysql", "USE other_db; SELECT 1", true},
		{"USE 在子语句", "mysql", "SELECT 1; USE mysql", true},
		{"无 USE", "mysql", "SELECT * FROM users", false},
		{"字符串里的 use 不拦(降级为保守也接受)", "mysql", "SELECT * FROM t WHERE a='use'", false},
		{"PG SET search_path", "postgres", "SET search_path TO public", true},
		{"PG 普通 SET", "postgres", "SET statement_timeout = 0", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := forbiddenSwitchReason(c.engine, c.sql)
			if (got != "") != c.blocked {
				t.Errorf("forbiddenSwitchReason(%q) = %q, blocked=%v", c.sql, got, c.blocked)
			}
		})
	}
}

func TestIsProductionConnection(t *testing.T) {
	cases := []struct {
		name  string
		cname string
		cfg   ConnectionConfig
		want  bool
	}{
		{"显式 prod", "任意名字", ConnectionConfig{EnvTag: "prod"}, true},
		{"显式 dev", "名字里带 prod", ConnectionConfig{EnvTag: "dev"}, false},
		{"显式 staging", "x", ConnectionConfig{EnvTag: "staging"}, false},
		{"启发式-连接名", "生产 MySQL 主库", ConnectionConfig{}, true},
		{"启发式-主机名", "app", ConnectionConfig{Host: "db-prod-01.internal"}, true},
		{"启发式-英文名", "Production DB", ConnectionConfig{}, true},
		{"启发式-不匹配", "测试库", ConnectionConfig{Host: "10.0.0.5"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isProductionConnection(c.cname, c.cfg); got != c.want {
				t.Errorf("isProductionConnection(%q, %+v) = %v, want %v", c.cname, c.cfg, got, c.want)
			}
		})
	}
}

func TestWriteUnlockManager(t *testing.T) {
	w := NewWriteUnlockManager(30)
	if w.Remaining("c1") != 0 {
		t.Fatal("初始应为未解锁")
	}
	w.Unlock("c1", 1)
	if w.Remaining("c1") <= 0 || w.Remaining("c1") > 60 {
		t.Fatalf("解锁 1 分钟后剩余应 in (0,60], got %d", w.Remaining("c1"))
	}
	// 上限截断
	w.Unlock("c1", 999)
	if w.Remaining("c1") > 30*60 {
		t.Fatalf("解锁时长应被截断到 30 分钟, got %d", w.Remaining("c1"))
	}
	// 立即上锁
	w.Lock("c1")
	if w.Remaining("c1") != 0 {
		t.Fatal("Lock 后应为 0")
	}
	// 非法分钟数兜底
	w.Unlock("c2", -5)
	if w.Remaining("c2") <= 0 {
		t.Fatal("非法分钟数应回落默认 5 分钟")
	}
}

func TestAuditLogRingAndFilter(t *testing.T) {
	a := NewAuditLog(nil) // 无持久化, 纯内存
	for i := 0; i < auditMemMax+50; i++ {
		a.Append(AuditEntry{ConnID: "c1", SQL: "s", Risk: string(RiskMedium), Decision: "executed"})
	}
	if len(a.List("")) != auditMemMax {
		t.Fatalf("环形缓冲应截断到 %d, got %d", auditMemMax, len(a.List("")))
	}
	a.Append(AuditEntry{ConnID: "c2", SQL: "other", Decision: "denied"})
	entries := a.List("c2")
	if len(entries) != 1 || entries[0].ConnID != "c2" {
		t.Fatalf("按连接过滤失败: %+v", entries)
	}
	// 新的在前
	all := a.List("")
	if all[0].ConnID != "c2" {
		t.Fatalf("最新的记录应排在最前, got %+v", all[0])
	}
}

func TestSQLExcerptTruncation(t *testing.T) {
	a := NewAuditLog(nil)
	long := strings.Repeat("x", 2000)
	a.Append(AuditEntry{ConnID: "c1", SQL: long, Decision: "executed"})
	e := a.List("")[0]
	if len(e.SQL) > auditSQLExcerptMax+3 {
		t.Fatalf("SQL 摘录应截断, got len=%d", len(e.SQL))
	}
	if !strings.HasSuffix(e.SQL, "...") {
		t.Fatal("截断后应以 ... 结尾")
	}
}
