package central

import (
	"encoding/json"
	"fmt"
)

type MigrationResult struct {
	OK      bool              `json:"ok"`
	Message string            `json:"message"`
	Keys    []string          `json:"keys,omitempty"`
	Errors  map[string]string `json:"errors,omitempty"`
}

func MigrateFromSQLite(src *SQLiteStore, dsn string) (*MigrationResult, error) {
	data, err := src.Export()
	if err != nil {
		return nil, fmt.Errorf("export from sqlite: %w", err)
	}

	dst, err := NewPostgres(dsn)
	if err != nil {
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	defer dst.Close()

	var keys []string
	for k := range data {
		keys = append(keys, k)
	}

	if err := dst.Import(data); err != nil {
		return nil, fmt.Errorf("import to postgres: %w", err)
	}

	return &MigrationResult{
		OK:      true,
		Message: fmt.Sprintf("成功迁移 %d 条配置到 PostgreSQL", len(data)),
		Keys:    keys,
	}, nil
}

type MigrationStatus struct {
	CurrentDB string `json:"currentDB"` // "sqlite" | "postgres"
	DSN       string `json:"dsn"`
	KeyCount  int    `json:"keyCount"`
}

func GetMigrationStatus(s CentralStore) (*MigrationStatus, error) {
	exp, err := s.Export()
	if err != nil {
		return nil, err
	}
	dbType := "unknown"
	switch s.(type) {
	case *SQLiteStore:
		dbType = "sqlite"
	case *PostgresStore:
		dbType = "postgres"
	}
	var raw json.RawMessage
	if err := json.Unmarshal([]byte(`"`+dbType+`"`), &raw); err != nil {
		return nil, err
	}
	return &MigrationStatus{
		CurrentDB: dbType,
		KeyCount:  len(exp),
	}, nil
}
