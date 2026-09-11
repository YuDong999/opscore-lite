package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"opscore/internal/dbmanager/gonavi/connection"
)

// 对象类型(GetObjects 返回的 kind 取值, 与前端的对象分组一一对应)。
const (
	KindView             = "VIEW"
	KindMaterializedView = "MATERIALIZED VIEW"
	KindFunction         = "FUNCTION"
	KindProcedure        = "PROCEDURE"
	KindEvent            = "EVENT"
	KindTrigger          = "TRIGGER"
	KindSequence         = "SEQUENCE"
)

// splitSQLServerQualifiedName 拆分 "schema.name" 对象名, 缺省 schema 取 dbo。
// 亦被达梦(owner.name)复用。
func splitSQLServerQualifiedName(objectName string) (string, string) {
	objectName = strings.TrimSpace(objectName)
	if idx := strings.LastIndex(objectName, "."); idx > 0 && idx < len(objectName)-1 {
		return objectName[:idx], objectName[idx+1:]
	}
	return "dbo", objectName
}

// ===== MySQL / MariaDB (共享实现, MariaDB 在 mariadb_objects.go 挂载) =====

// collectMySQLObjects 枚举 MySQL/MariaDB 库内视图、函数、存储过程、事件与全库触发器。
// dbName 为空时按当前连接库过滤;返回的对象名与现有 GetTables 一致, 不携带 schema 前缀。
func collectMySQLObjects(conn *sql.DB, ctx context.Context, dbName string) ([]connection.DbObject, error) {
	var objs []connection.DbObject
	schema := normalizeMySQLIdentifierPart(dbName)

	rows, _, err := queryMetadataRowsWithArgs(conn, ctx, "mysql",
		"SELECT TABLE_NAME AS obj_name FROM information_schema.tables WHERE TABLE_SCHEMA = ? AND TABLE_TYPE = 'VIEW' ORDER BY TABLE_NAME", schema)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if name := fmt.Sprintf("%v", row["obj_name"]); name != "" && name != "<nil>" {
			objs = append(objs, connection.DbObject{Kind: KindView, Name: name})
		}
	}

	rows, _, err = queryMetadataRowsWithArgs(conn, ctx, "mysql",
		"SELECT ROUTINE_NAME AS obj_name, ROUTINE_TYPE AS obj_kind FROM information_schema.routines WHERE ROUTINE_SCHEMA = ? ORDER BY ROUTINE_NAME", schema)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		name := fmt.Sprintf("%v", row["obj_name"])
		kind := fmt.Sprintf("%v", row["obj_kind"])
		if name == "" || name == "<nil>" {
			continue
		}
		obj := connection.DbObject{Kind: KindFunction, Name: name}
		if kind == "PROCEDURE" {
			obj.Kind = KindProcedure
		}
		objs = append(objs, obj)
	}

	rows, _, err = queryMetadataRowsWithArgs(conn, ctx, "mysql",
		"SELECT EVENT_NAME AS obj_name FROM information_schema.events WHERE EVENT_SCHEMA = ? ORDER BY EVENT_NAME", schema)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if name := fmt.Sprintf("%v", row["obj_name"]); name != "" && name != "<nil>" {
			objs = append(objs, connection.DbObject{Kind: KindEvent, Name: name})
		}
	}

	rows, _, err = queryMetadataRowsWithArgs(conn, ctx, "mysql",
		"SELECT TRIGGER_NAME AS obj_name, EVENT_OBJECT_TABLE AS obj_table FROM information_schema.triggers WHERE TRIGGER_SCHEMA = ? ORDER BY TRIGGER_NAME", schema)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		name := fmt.Sprintf("%v", row["obj_name"])
		if name == "" || name == "<nil>" {
			continue
		}
		obj := connection.DbObject{Kind: KindTrigger, Name: name}
		if tbl := fmt.Sprintf("%v", row["obj_table"]); tbl != "" && tbl != "<nil>" {
			obj.Table = tbl
		}
		objs = append(objs, obj)
	}

	return objs, nil
}

func mysqlObjectDefinition(conn *sql.DB, ctx context.Context, dbName, objectName, kind string) (string, error) {
	schema := normalizeMySQLIdentifierPart(dbName)
	name := normalizeMySQLIdentifierPart(objectName)
	var showVerb string
	var ddlKey string
	switch kind {
	case KindView:
		showVerb = "SHOW CREATE VIEW"
		ddlKey = "Create View"
	case KindFunction:
		showVerb = "SHOW CREATE FUNCTION"
		ddlKey = "Create Function"
	case KindProcedure:
		showVerb = "SHOW CREATE PROCEDURE"
		ddlKey = "Create Procedure"
	case KindTrigger:
		showVerb = "SHOW CREATE TRIGGER"
		ddlKey = "Create Trigger"
	case KindEvent:
		showVerb = "SHOW CREATE EVENT"
		ddlKey = "Create Event"
	default:
		return "", fmt.Errorf("MySQL/MariaDB 不支持对象类型 %s 的 DDL", kind)
	}
	query := fmt.Sprintf("%s %s", showVerb, mysqlQualifiedTableIdentifier(schema, name))
	rows, _, err := queryMetadataRowsWithArgs(conn, ctx, "mysql", query)
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "", fmt.Errorf("对象 %q 不存在或无权限查看", objectName)
	}
	ddl := fmt.Sprintf("%v", rows[0][ddlKey])
	if ddl == "" || ddl == "<nil>" {
		return "-- 无法获取 DDL(对象可能由系统或加密过程创建)", nil
	}
	return ddl, nil
}

func (m *MySQLDB) GetObjects(dbName string) ([]connection.DbObject, error) {
	return collectMySQLObjects(m.conn, metadataContextFor(m), dbName)
}

func (m *MySQLDB) GetObjectDefinition(dbName, objectName, kind string) (string, error) {
	return mysqlObjectDefinition(m.conn, metadataContextFor(m), dbName, objectName, kind)
}

// ===== PostgreSQL =====

func (p *PostgresDB) GetObjects(dbName string) ([]connection.DbObject, error) {
	predicate := buildPGLikeVisibleRelationPredicate("c", "")
	var objs []connection.DbObject

	data, _, err := p.Query(fmt.Sprintf(`
SELECT n.nspname AS schema_name, c.relname AS obj_name, c.relkind AS obj_kind
FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE c.relkind IN ('v','m') AND %s
ORDER BY n.nspname, c.relname`, predicate))
	if err != nil {
		return nil, err
	}
	for _, row := range data {
		schema := fmt.Sprintf("%v", row["schema_name"])
		name := fmt.Sprintf("%v", row["obj_name"])
		kind := KindView
		if fmt.Sprintf("%v", row["obj_kind"]) == "m" {
			kind = KindMaterializedView
		}
		objs = append(objs, connection.DbObject{Kind: kind, Name: fmt.Sprintf("%s.%s", schema, name)})
	}

	data, _, err = p.Query(fmt.Sprintf(`
SELECT n.nspname AS schema_name, p.proname AS obj_name, p.prokind AS obj_kind
FROM pg_catalog.pg_proc p
JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
WHERE p.prokind IN ('f','p') AND %s
ORDER BY n.nspname, p.proname`, predicate))
	if err != nil {
		return nil, err
	}
	for _, row := range data {
		schema := fmt.Sprintf("%v", row["schema_name"])
		name := fmt.Sprintf("%v", row["obj_name"])
		kind := KindFunction
		if fmt.Sprintf("%v", row["obj_kind"]) == "p" {
			kind = KindProcedure
		}
		objs = append(objs, connection.DbObject{Kind: kind, Name: fmt.Sprintf("%s.%s", schema, name)})
	}

	data, _, err = p.Query(fmt.Sprintf(`
SELECT n.nspname AS schema_name, c.relname AS obj_name
FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE c.relkind = 'S' AND %s
ORDER BY n.nspname, c.relname`, predicate))
	if err != nil {
		return nil, err
	}
	for _, row := range data {
		schema := fmt.Sprintf("%v", row["schema_name"])
		name := fmt.Sprintf("%v", row["obj_name"])
		objs = append(objs, connection.DbObject{Kind: KindSequence, Name: fmt.Sprintf("%s.%s", schema, name)})
	}

	data, _, err = p.Query(fmt.Sprintf(`
SELECT t.tgname AS obj_name, c.relname AS tbl_name, n.nspname AS schema_name
FROM pg_catalog.pg_trigger t
JOIN pg_catalog.pg_class c ON c.oid = t.tgrelid
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE NOT t.tgisinternal AND %s
ORDER BY n.nspname, c.relname, t.tgname`, predicate))
	if err != nil {
		return nil, err
	}
	for _, row := range data {
		schema := fmt.Sprintf("%v", row["schema_name"])
		name := fmt.Sprintf("%v", row["obj_name"])
		tbl := fmt.Sprintf("%v", row["tbl_name"])
		obj := connection.DbObject{Kind: KindTrigger, Name: fmt.Sprintf("%s.%s", schema, name)}
		if tbl != "" && tbl != "<nil>" {
			obj.Table = fmt.Sprintf("%s.%s", schema, tbl)
		}
		objs = append(objs, obj)
	}

	return objs, nil
}

func (p *PostgresDB) GetObjectDefinition(dbName, objectName, kind string) (string, error) {
	ctx := metadataContextFor(p)
	var query string
	switch kind {
	case KindView:
		query = "SELECT pg_get_viewdef($1::regclass, true) AS ddl"
	case KindFunction, KindProcedure:
		query = "SELECT pg_get_functiondef((SELECT p.oid FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace WHERE n.nspname||'.'||p.proname = $1 AND p.prokind IN ('f','p') LIMIT 1)) AS ddl"
	case KindTrigger:
		query = "SELECT pg_get_triggerdef((SELECT t.oid FROM pg_catalog.pg_trigger t JOIN pg_catalog.pg_class c ON c.oid = t.tgrelid JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname||'.'||t.tgname = $1 AND NOT t.tgisinternal LIMIT 1)) AS ddl"
	case KindSequence:
		query = "SELECT 'CREATE SEQUENCE ' || $1::regclass::text || ';' AS ddl"
	default:
		return "", fmt.Errorf("PostgreSQL 不支持对象类型 %s 的 DDL", kind)
	}
	data, _, err := queryMetadataRowsWithArgs(p.conn, ctx, "postgres", query, objectName)
	if err != nil {
		return "", err
	}
	if len(data) == 0 {
		return "", fmt.Errorf("对象 %q 不存在或无权限查看", objectName)
	}
	ddl := fmt.Sprintf("%v", data[0]["ddl"])
	if ddl == "" || ddl == "<nil>" {
		return "-- 无法获取 DDL(对象可能由系统或加密过程创建)", nil
	}
	return ddl, nil
}