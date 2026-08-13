package central

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	_ "modernc.org/sqlite"
)

type SQLiteStore struct {
	db  *sql.DB
	mu  sync.RWMutex
	dsn string
}

func NewSQLite(dir string) (*SQLiteStore, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("central: mkdir: %w", err)
	}
	path := filepath.Join(dir, "opscore.db")
	dsn := path + "?_journal_mode=WAL&_cache_size=4000"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("central: open: %w", err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS meta (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL
	)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("central: init schema: %w", err)
	}
	return &SQLiteStore{db: db, dsn: dsn}, nil
}

func (s *SQLiteStore) get(key string) (string, error) {
	var val string
	err := s.db.QueryRow("SELECT value FROM meta WHERE key = ?", key).Scan(&val)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return val, err
}

func (s *SQLiteStore) set(key, val string) error {
	_, err := s.db.Exec("INSERT OR REPLACE INTO meta (key, value) VALUES (?, ?)", key, val)
	return err
}

func (s *SQLiteStore) Ping() error {
	return s.db.Ping()
}

func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

// ── 认证 ──

func (s *SQLiteStore) GetToken() (string, error) {
	return s.get("auth:token")
}

func (s *SQLiteStore) SetToken(token string) error {
	return s.set("auth:token", token)
}

// ── 模块状态 ──

func (s *SQLiteStore) GetModuleState(id string) (bool, error) {
	v, err := s.get("module:active:" + id)
	if err != nil {
		return false, err
	}
	return v == "true", nil
}

func (s *SQLiteStore) SetModuleState(id string, active bool) error {
	v := "false"
	if active {
		v = "true"
	}
	return s.set("module:active:"+id, v)
}

func (s *SQLiteStore) GetAllModuleStates() (map[string]bool, error) {
	rows, err := s.db.Query("SELECT key, value FROM meta WHERE key LIKE 'module:active:%'")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ret := map[string]bool{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		id := k[len("module:active:"):]
		ret[id] = v == "true"
	}
	return ret, rows.Err()
}

// ── 迁移 ──

func (s *SQLiteStore) Export() (map[string]json.RawMessage, error) {
	rows, err := s.db.Query("SELECT key, value FROM meta ORDER BY key")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]json.RawMessage{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = json.RawMessage(v)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) Import(data map[string]json.RawMessage) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for k, v := range data {
		val := string(v)
		if val == "" {
			val = "\"\""
		}
		if _, err := tx.Exec("INSERT OR REPLACE INTO meta (key, value) VALUES (?, ?)", k, val); err != nil {
			return fmt.Errorf("import key %s: %w", k, err)
		}
	}
	return tx.Commit()
}
