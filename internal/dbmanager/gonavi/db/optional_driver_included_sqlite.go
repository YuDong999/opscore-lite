//go:build gonavi_sqlite_driver && !gonavi_full_drivers

package db

// 本文件只在 -tags gonavi_sqlite_driver 的 lite 构建里参与编译, 与
// database_sqlite_factory.go 的注册条件保持一致 —— 那边注册工厂, 这边回答
// "包不包含", 两边同标签才不会再次漂移。
func optionalDriverTaggedIncluded(driverType string) bool {
	return normalizeRuntimeDriverType(driverType) == "sqlite"
}
