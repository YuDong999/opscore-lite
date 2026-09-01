//go:build gonavi_full_drivers || gonavi_duckdb_driver

package db

import (
	"errors"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"

	"opscore/internal/dbmanager/gonavi/connection"
	"opscore/internal/dbmanager/gonavi/shared/i18n"
)

type duckDBI18nEmptyRowsDriver struct{}

type duckDBI18nEmptyRowsConn struct{}

type duckDBI18nEmptyRowsStmt struct{}

type duckDBI18nEmptyRowsRows struct{}

var registerDuckDBI18nEmptyRowsDriverOnce sync.Once

func (duckDBI18nEmptyRowsDriver) Open(name string) (driver.Conn, error) {
	return duckDBI18nEmptyRowsConn{}, nil
}

func (duckDBI18nEmptyRowsConn) Prepare(query string) (driver.Stmt, error) {
	return duckDBI18nEmptyRowsStmt{}, nil
}

func (duckDBI18nEmptyRowsConn) Close() error { return nil }

func (duckDBI18nEmptyRowsConn) Begin() (driver.Tx, error) {
	return nil, fmt.Errorf("transactions are not supported in duckdb i18n empty rows test driver")
}

func (duckDBI18nEmptyRowsStmt) Close() error { return nil }

func (duckDBI18nEmptyRowsStmt) NumInput() int { return -1 }

func (duckDBI18nEmptyRowsStmt) Exec(args []driver.Value) (driver.Result, error) {
	return nil, fmt.Errorf("exec is not supported in duckdb i18n empty rows test driver")
}

func (duckDBI18nEmptyRowsStmt) Query(args []driver.Value) (driver.Rows, error) {
	return duckDBI18nEmptyRowsRows{}, nil
}

func (duckDBI18nEmptyRowsRows) Columns() []string {
	return []string{"sql"}
}

func (duckDBI18nEmptyRowsRows) Close() error { return nil }

func (duckDBI18nEmptyRowsRows) Next(dest []driver.Value) error {
	return io.EOF
}

func openDuckDBI18nEmptyRowsDB(t *testing.T) *sql.DB {
	t.Helper()

	registerDuckDBI18nEmptyRowsDriverOnce.Do(func() {
		sql.Register("duckdb_i18n_empty_rows", duckDBI18nEmptyRowsDriver{})
	})

	conn, err := sql.Open("duckdb_i18n_empty_rows", "")
	if err != nil {
		t.Fatalf("open duckdb_i18n_empty_rows test DB failed: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
	})
	return conn
}

func TestDuckDBRuntimeUsesCurrentLanguageForConnectionNotOpen(t *testing.T) {
	SetBackendLanguage(i18n.LanguageEnUS)
	t.Cleanup(func() {
		SetBackendLanguage(i18n.LanguageZhCN)
	})

	err := (&DuckDB{}).Ping()
	if err == nil {
		t.Fatal("expected Ping to fail when DuckDB connection is not open")
	}
	if err.Error() != "Connection is not open" {
		t.Fatalf("expected English not-open error, got %q", err.Error())
	}
}

func TestDuckDBBuildSupportStatusUsesCurrentLanguageWhenDriverIsUnavailable(t *testing.T) {
	if supported, _ := duckDBBuildSupportStatus(); supported {
		t.Skip("current build already includes DuckDB runtime support")
	}

	SetBackendLanguage(i18n.LanguageEnUS)
	t.Cleanup(func() {
		SetBackendLanguage(i18n.LanguageZhCN)
	})

	wantReason := fmt.Sprintf(
		"The current build does not include the DuckDB driver (platform=%s/%s). Enable CGO and use a supported platform (darwin/linux amd64|arm64, windows/amd64), or provide a custom library via -tags duckdb_use_lib / duckdb_use_static_lib",
		runtime.GOOS,
		runtime.GOARCH,
	)

	supported, reason := duckDBBuildSupportStatus()
	if supported {
		t.Fatal("expected DuckDB build support to stay unavailable in this test environment")
	}
	if reason != wantReason {
		t.Fatalf("expected English DuckDB-unavailable reason %q, got %q", wantReason, reason)
	}

	err := (&DuckDB{}).Connect(connection.ConnectionConfig{Type: "duckdb"})
	if err == nil {
		t.Fatal("expected DuckDB connect to fail when runtime support is unavailable")
	}

	wantConnectError := "DuckDB driver is unavailable: " + wantReason
	if err.Error() != wantConnectError {
		t.Fatalf("expected English DuckDB connect error %q, got %q", wantConnectError, err.Error())
	}
}

func TestDuckDBDDLMetadataErrorsUseCurrentLanguage(t *testing.T) {
	SetBackendLanguage(i18n.LanguageEnUS)
	t.Cleanup(func() {
		SetBackendLanguage(i18n.LanguageZhCN)
	})

	duck := &DuckDB{}

	tests := []struct {
		name string
		call func() error
		want string
	}{
		{
			name: "create statement table name required",
			call: func() error {
				_, err := duck.GetCreateStatement("", " ")
				return err
			},
			want: "Table name is required",
		},
		{
			name: "create statement not found",
			call: func() error {
				_, err := (&DuckDB{conn: openDuckDBI18nEmptyRowsDB(t)}).GetCreateStatement("main", "orders")
				return err
			},
			want: "The CREATE TABLE statement was not found",
		},
		{
			name: "columns table name required",
			call: func() error {
				_, err := duck.GetColumns("", " ")
				return err
			},
			want: "Table name is required",
		},
		{
			name: "indexes table name required",
			call: func() error {
				_, err := duck.GetIndexes("", " ")
				return err
			},
			want: "Table name is required",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			if err == nil {
				t.Fatal("expected DuckDB DDL metadata call to fail")
			}
			if err.Error() != tc.want {
				t.Fatalf("expected %q, got %q", tc.want, err.Error())
			}
		})
	}
}

func TestDuckDBApplyChangesErrorsUseCurrentLanguage(t *testing.T) {
	SetBackendLanguage(i18n.LanguageEnUS)
	t.Cleanup(func() {
		SetBackendLanguage(i18n.LanguageZhCN)
	})

	t.Run("delete failure", func(t *testing.T) {
		dbConn, state := openDuckDBRecordingDB(t)
		state.failDelete = errors.New("delete blocked")
		duckdb := &DuckDB{conn: dbConn}

		err := duckdb.ApplyChanges("main.events", connection.ChangeSet{
			Deletes: []map[string]interface{}{
				{"id": 1},
			},
		})
		if err == nil {
			t.Fatal("expected delete failure to bubble up")
		}
		if err.Error() != "Delete failed: delete blocked" {
			t.Fatalf("expected English delete failure, got %q", err.Error())
		}
	})

	t.Run("update key condition required", func(t *testing.T) {
		dbConn, _ := openDuckDBRecordingDB(t)
		duckdb := &DuckDB{conn: dbConn}

		err := duckdb.ApplyChanges("main.events", connection.ChangeSet{
			Updates: []connection.UpdateRow{{
				Values: map[string]interface{}{
					"name": "renamed",
				},
			}},
		})
		if err == nil {
			t.Fatal("expected update without keys to fail")
		}
		if err.Error() != "Update operation requires key conditions" {
			t.Fatalf("expected English update-key failure, got %q", err.Error())
		}
	})

	t.Run("update failure", func(t *testing.T) {
		dbConn, state := openDuckDBRecordingDB(t)
		state.failUpdate = errors.New("update blocked")
		duckdb := &DuckDB{conn: dbConn}

		err := duckdb.ApplyChanges("main.events", connection.ChangeSet{
			Updates: []connection.UpdateRow{{
				Keys: map[string]interface{}{
					"id": 1,
				},
				Values: map[string]interface{}{
					"name": "renamed",
				},
			}},
		})
		if err == nil {
			t.Fatal("expected update failure to bubble up")
		}
		if err.Error() != "Update failed: update blocked" {
			t.Fatalf("expected English update failure, got %q", err.Error())
		}
	})
}

func TestDuckDBUserVisibleRuntimeErrorsDoNotReintroduceInlineChinese(t *testing.T) {
	t.Helper()

	files := []string{"duckdb_impl.go", "duckdb_platform_unsupported.go"}
	disallowed := []string{
		string([]rune{0x44, 0x75, 0x63, 0x6b, 0x44, 0x42, 0x20, 0x9a71, 0x52a8, 0x4e0d, 0x53ef, 0x7528}),
		string([]rune{0x6253, 0x5f00, 0x6570, 0x636e, 0x5e93, 0x8fde, 0x63a5, 0x5931, 0x8d25}),
		string([]rune{0x8fde, 0x63a5, 0x5efa, 0x7acb, 0x540e, 0x9a8c, 0x8bc1, 0x5931, 0x8d25}),
		string([]rune{0x8fde, 0x63a5, 0x672a, 0x6253, 0x5f00}),
		string([]rune{0x5f53, 0x524d, 0x6784, 0x5efa, 0x4e0d, 0x5305, 0x542b, 0x20, 0x44, 0x75, 0x63, 0x6b, 0x44, 0x42, 0x20, 0x9a71, 0x52a8}),
		string([]rune{0x8868, 0x540d, 0x4e0d, 0x80fd, 0x4e3a, 0x7a7a}),
		string([]rune{0x672a, 0x627e, 0x5230, 0x5efa, 0x8868, 0x8bed, 0x53e5}),
		string([]rune{0x5220, 0x9664, 0x5931, 0x8d25}),
		string([]rune{0x66f4, 0x65b0, 0x64cd, 0x4f5c, 0x9700, 0x8981, 0x4e3b, 0x952e, 0x6761, 0x4ef6}),
		string([]rune{0x66f4, 0x65b0, 0x5931, 0x8d25}),
	}

	for _, file := range files {
		content, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s failed: %v", file, err)
		}
		source := string(content)
		for _, raw := range disallowed {
			if strings.Contains(source, raw) {
				t.Fatalf("%s still contains inline user-visible Chinese raw: %s", file, raw)
			}
		}
	}
}
