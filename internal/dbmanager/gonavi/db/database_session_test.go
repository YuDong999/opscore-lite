package db

import (
	"context"
	"database/sql"
	"reflect"
	"testing"
)

func openScanRowsDuplicateSQLConn(t *testing.T) *sql.Conn {
	t.Helper()

	registerScanRowsDuplicateDriverOnce.Do(func() {
		sql.Register(scanRowsDuplicateDriverName, scanRowsDuplicateDriver{})
	})

	dbConn, err := sql.Open(scanRowsDuplicateDriverName, "")
	if err != nil {
		t.Fatalf("open duplicate scan rows db failed: %v", err)
	}
	t.Cleanup(func() {
		_ = dbConn.Close()
	})

	conn, err := dbConn.Conn(context.Background())
	if err != nil {
		t.Fatalf("open sql conn failed: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
	})
	return conn
}

func TestSQLConnStatementExecerWithDialectDecodesOceanBaseOracleTimestamp(t *testing.T) {
	t.Parallel()

	conn := openScanRowsDuplicateSQLConn(t)
	execer, ok := NewSQLConnStatementExecerWithDialect(conn, oceanBaseOracleScanDialect).(StatementMultiResultQueryExecer)
	if !ok {
		t.Fatal("statement execer should support multi-result query")
	}

	results, err := execer.QueryMultiContext(context.Background(), "SELECT timestamp_precision_columns")
	if err != nil {
		t.Fatalf("query multi failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected one result set, got=%d", len(results))
	}
	if !reflect.DeepEqual(results[0].Columns, []string{"created_at"}) {
		t.Fatalf("unexpected columns: %v", results[0].Columns)
	}
	if len(results[0].Rows) != 1 {
		t.Fatalf("expected one row, got=%d", len(results[0].Rows))
	}
	if got := results[0].Rows[0]["created_at"]; got != "2026-06-16T12:34:56.123456Z" {
		t.Fatalf("statement execer should decode OceanBase Oracle TIMESTAMP(6), got=%v(%T)", got, got)
	}
}

func TestSQLConnTransactionExecerWithDialectDecodesOceanBaseOracleTimestamp(t *testing.T) {
	t.Parallel()

	conn := openScanRowsDuplicateSQLConn(t)
	execer, ok := NewSQLConnTransactionExecerWithDialect(conn, "COMMIT", "ROLLBACK", oceanBaseOracleScanDialect).(StatementMultiResultQueryExecer)
	if !ok {
		t.Fatal("transaction execer should support multi-result query")
	}

	results, err := execer.QueryMultiContext(context.Background(), "SELECT timestamp_precision_columns")
	if err != nil {
		t.Fatalf("query multi failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected one result set, got=%d", len(results))
	}
	if !reflect.DeepEqual(results[0].Columns, []string{"created_at"}) {
		t.Fatalf("unexpected columns: %v", results[0].Columns)
	}
	if len(results[0].Rows) != 1 {
		t.Fatalf("expected one row, got=%d", len(results[0].Rows))
	}
	if got := results[0].Rows[0]["created_at"]; got != "2026-06-16T12:34:56.123456Z" {
		t.Fatalf("transaction execer should decode OceanBase Oracle TIMESTAMP(6), got=%v(%T)", got, got)
	}
}
