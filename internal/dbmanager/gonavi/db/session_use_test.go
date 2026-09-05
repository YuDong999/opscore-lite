package db

import (
	"context"
	"testing"

	"opscore/internal/dbmanager/gonavi/connection"
)

// 验证 MySQL 会话路径: USE + Query 同连接生效(复现 No database selected)
func TestMySQLSessionUseQuery(t *testing.T) {
	m := &MySQLDB{}
	cfg := connection.ConnectionConfig{
		Host: "192.168.207.10", Port: 3306, User: "root", Password: "root123",
		Timeout: 10, QueryTimeout: 15,
	}
	if err := m.Connect(cfg); err != nil {
		t.Skipf("连不上: %v", err)
	}
	defer m.Close()
	ctx := context.Background()
	sp, ok := interface{}(m).(SessionExecerProvider)
	if !ok {
		t.Fatal("MySQLDB 未实现 SessionExecerProvider")
	}
	sess, err := sp.OpenSessionExecer(ctx)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	defer sess.Close()
	if _, err := sess.Exec("USE sync_src"); err != nil {
		t.Fatalf("USE: %v", err)
	}
	qsess, ok2 := sess.(StatementQueryExecer)
	if !ok2 { t.Fatal("no StatementQueryExecer") }
	rows, cols, err := qsess.Query("SELECT DATABASE() AS db, COUNT(*) AS n FROM users")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	t.Logf("cols=%v rows0=%v", cols, rows[0])
}
