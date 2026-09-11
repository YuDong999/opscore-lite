//go:build gonavi_full_drivers || gonavi_sqlite_driver

package db

import (
	"fmt"

	"opscore/internal/dbmanager/gonavi/connection"
)

func (s *SQLiteDB) GetObjects(dbName string) ([]connection.DbObject, error) {
	data, _, err := s.Query("SELECT name AS obj_name, type AS obj_kind FROM sqlite_master WHERE type IN ('view','trigger') ORDER BY name")
	if err != nil {
		return nil, err
	}
	var objs []connection.DbObject
	for _, row := range data {
		name := fmt.Sprintf("%v", row["obj_name"])
		kind := fmt.Sprintf("%v", row["obj_kind"])
		if name == "" || name == "<nil>" {
			continue
		}
		k := KindView
		if kind == "trigger" {
			k = KindTrigger
		}
		objs = append(objs, connection.DbObject{Kind: k, Name: name})
	}
	return objs, nil
}

func (s *SQLiteDB) GetObjectDefinition(dbName, objectName, kind string) (string, error) {
	objType := "view"
	if kind == KindTrigger {
		objType = "trigger"
	} else if kind != KindView {
		return "", fmt.Errorf("SQLite 不支持对象类型 %s 的 DDL", kind)
	}
	data, _, err := s.Query(fmt.Sprintf(
		"SELECT sql AS ddl FROM sqlite_master WHERE type='%s' AND name='%s'", objType, escapeSQLiteStringLiteral(objectName)))
	if err != nil {
		return "", err
	}
	if len(data) == 0 {
		return "", fmt.Errorf("对象 %q 不存在", objectName)
	}
	ddl := fmt.Sprintf("%v", data[0]["ddl"])
	if ddl == "" || ddl == "<nil>" {
		return "-- 无法获取 DDL(对象可能已由内部创建)", nil
	}
	return ddl, nil
}
