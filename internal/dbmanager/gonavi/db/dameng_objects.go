//go:build gonavi_full_drivers || gonavi_dameng_driver

package db

import (
	"fmt"

	"opscore/internal/dbmanager/gonavi/connection"
)

func (d *DamengDB) GetObjects(dbName string) ([]connection.DbObject, error) {
	query := `SELECT owner AS "OWNER", object_name AS "NAME", object_type AS "KIND" FROM all_objects WHERE object_type IN ('VIEW','FUNCTION','PROCEDURE','TRIGGER','SEQUENCE') ORDER BY owner, object_name`
	if escaped := escapeDamengMetadataLiteral(dbName); escaped != "" {
		query = fmt.Sprintf(`SELECT owner AS "OWNER", object_name AS "NAME", object_type AS "KIND" FROM all_objects WHERE object_type IN ('VIEW','FUNCTION','PROCEDURE','TRIGGER','SEQUENCE') AND owner = '%s' ORDER BY owner, object_name`, escaped)
	}
	data, _, err := d.Query(query)
	if err != nil {
		return nil, err
	}
	var objs []connection.DbObject
	for _, row := range data {
		owner := fmt.Sprintf("%v", row["OWNER"])
		name := fmt.Sprintf("%v", row["NAME"])
		kind := fmt.Sprintf("%v", row["KIND"])
		if name == "" || name == "<nil>" {
			continue
		}
		objs = append(objs, connection.DbObject{Kind: kind, Name: fmt.Sprintf("%s.%s", owner, name)})
	}
	return objs, nil
}

func (d *DamengDB) GetObjectDefinition(dbName, objectName, kind string) (string, error) {
	switch kind {
	case KindView, KindFunction, KindProcedure, KindTrigger, KindSequence:
	default:
		return "", fmt.Errorf("达梦不支持对象类型 %s 的 DDL", kind)
	}
	owner, name := splitSQLServerQualifiedName(objectName)
	schemaArg := "NULL"
	if owner != "dbo" {
		schemaArg = fmt.Sprintf("'%s'", escapeDamengMetadataLiteral(owner))
	}
	query := fmt.Sprintf("SELECT DBMS_METADATA.GET_DDL('%s', '%s', %s) AS DDL FROM dual",
		escapeDamengMetadataLiteral(kind), escapeDamengMetadataLiteral(name), schemaArg)
	data, _, err := d.Query(query)
	if err != nil {
		return "", err
	}
	if len(data) == 0 {
		return "", fmt.Errorf("对象 %q 不存在或无权限查看", objectName)
	}
	ddl := fmt.Sprintf("%v", data[0]["DDL"])
	if ddl == "" || ddl == "<nil>" {
		return "-- 无法获取 DDL(对象可能由系统或加密过程创建)", nil
	}
	return ddl, nil
}