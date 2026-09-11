//go:build gonavi_full_drivers || gonavi_mariadb_driver

package db

import "opscore/internal/dbmanager/gonavi/connection"

func (m *MariaDB) GetObjects(dbName string) ([]connection.DbObject, error) {
	return collectMySQLObjects(m.conn, metadataContextFor(m), dbName)
}

func (m *MariaDB) GetObjectDefinition(dbName, objectName, kind string) (string, error) {
	return mysqlObjectDefinition(m.conn, metadataContextFor(m), dbName, objectName, kind)
}
