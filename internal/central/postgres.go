package central

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

type PostgresStore struct {
	db  *sql.DB
	dsn string
}

func NewPostgres(dsn string) (*PostgresStore, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("central: open pg: %w", err)
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("central: ping pg: %w", err)
	}

	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS meta (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL
	)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("central: init pg schema: %w", err)
	}
	return &PostgresStore{db: db, dsn: dsn}, nil
}

func (s *PostgresStore) get(key string) (string, error) {
	var val string
	err := s.db.QueryRow("SELECT value FROM meta WHERE key = $1", key).Scan(&val)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return val, err
}

func (s *PostgresStore) set(key, val string) error {
	_, err := s.db.Exec("INSERT INTO meta (key, value) VALUES ($1, $2) ON CONFLICT (key) DO UPDATE SET value = $2", key, val)
	return err
}

func (s *PostgresStore) Ping() error {
	return s.db.Ping()
}

func (s *PostgresStore) Close() error {
	return s.db.Close()
}

// ── 认证 ──

func (s *PostgresStore) GetToken() (string, error) {
	return s.get("auth:token")
}

func (s *PostgresStore) SetToken(token string) error {
	return s.set("auth:token", token)
}

// ── 模块状态 ──

func (s *PostgresStore) GetModuleState(id string) (bool, error) {
	v, err := s.get("module:active:" + id)
	if err != nil {
		return false, err
	}
	return v == "true", nil
}

func (s *PostgresStore) SetModuleState(id string, active bool) error {
	v := "false"
	if active {
		v = "true"
	}
	return s.set("module:active:"+id, v)
}

func (s *PostgresStore) GetAllModuleStates() (map[string]bool, error) {
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

func (s *PostgresStore) Export() (map[string]json.RawMessage, error) {
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

func (s *PostgresStore) Import(data map[string]json.RawMessage) error {
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
		if _, err := tx.Exec("INSERT INTO meta (key, value) VALUES ($1, $2) ON CONFLICT (key) DO UPDATE SET value = $2", k, val); err != nil {
			return fmt.Errorf("import key %s: %w", k, err)
		}
	}
	return tx.Commit()
}
