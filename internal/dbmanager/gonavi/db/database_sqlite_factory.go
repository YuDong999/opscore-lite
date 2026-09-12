//go:build gonavi_sqlite_driver

package db

func init() {
	// sqlite: 纯 Go 驱动(modernc.org/sqlite)内嵌连接, 无需 driver-agent 子进程。
	// 编译期 -tags gonavi_sqlite_driver 启用; 配合 installed.json 标记构成完整"安装启用"。
	registerDatabaseFactory(func() Database { return &SQLiteDB{} }, "sqlite")
}
