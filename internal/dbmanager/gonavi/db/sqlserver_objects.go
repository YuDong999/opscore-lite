//go:build gonavi_full_drivers || gonavi_sqlserver_driver

package db

import (
	"fmt"
	"strings"

	"opscore/internal/dbmanager/gonavi/connection"
)

func (s *SqlServerDB) GetObjects(dbName string) ([]connection.DbObject, error) {
	safeDB := quoteBracket(dbName)
	query := fmt.Sprintf(`
SELECT s.name AS schema_name, o.name AS obj_name, o.type AS obj_kind
FROM [%[1]s].sys.objects o
JOIN [%[1]s].sys.schemas s ON o.schema_id = s.schema_id
WHERE o.type IN ('V','P','FN','IF','TF','FS','FT','TR','SO')
ORDER BY s.name, o.name`, safeDB)
	data, _, err := s.Query(query)
	if err != nil {
		return nil, err
	}
	var objs []connection.DbObject
	for _, row := range data {
		schema := fmt.Sprintf("%v", row["schema_name"])
		name := fmt.Sprintf("%v", row["obj_name"])
		kind := fmt.Sprintf("%v", row["obj_kind"])
		if name == "" || name == "<nil>" {
			continue
		}
		fullName := name
		if schema != "" && schema != "<nil>" {
			fullName = fmt.Sprintf("%s.%s", schema, name)
		}
		k := KindView
		switch kind {
		case "V":
			k = KindView
		case "FN", "IF", "TF", "FS", "FT":
			k = KindFunction
		case "P":
			k = KindProcedure
		case "TR":
			k = KindTrigger
		case "SO":
			k = KindSequence
		}
		objs = append(objs, connection.DbObject{Kind: k, Name: fullName})
	}
	return objs, nil
}

func (s *SqlServerDB) GetObjectDefinition(dbName, objectName, kind string) (string, error) {
	switch kind {
	case KindView, KindFunction, KindProcedure, KindTrigger, KindSequence:
	default:
		return "", fmt.Errorf("SQL Server 不支持对象类型 %s 的 DDL", kind)
	}
	schema, name := splitSQLServerQualifiedName(objectName)
	safeDB := quoteBracket(dbName)
	safeObj := fmt.Sprintf("[%s].[%s].[%s]", safeDB, quoteBracket(schema), quoteBracket(name))
	escaped := strings.ReplaceAll(safeObj, "'", "''")
	query := fmt.Sprintf("SELECT OBJECT_DEFINITION(OBJECT_ID('%s')) AS ddl", escaped)
	data, _, err := s.Query(query)
	if err != nil {
		return "", err
	}
	if len(data) == 0 {
		return "", fmt.Errorf("对象 %q 不存在或无权限查看", objectName)
	}
	ddl := fmt.Sprintf("%v", data[0]["ddl"])
	if ddl == "" || ddl == "<nil>" {
		return "-- 无法获取 DDL(对象可能为系统/加密对象)", nil
	}
	return ddl, nil
}