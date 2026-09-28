//go:build !gonavi_full_drivers

package db

// optionalGoDriverBuildIncluded 回答"这个可选驱动是否真的编译进了当前二进制"。
//
// 这个函数的 lite 版与 full 版**曾经函数体一模一样**(都只查 optionalGoDrivers 这张
// 全量表), 于是它对 sqlite 谎报"已包含": 界面按"可选 + 已安装 + 运行时就绪"显示成可用,
// 而 databaseFactories 里根本没有 sqlite 条目(它由 database_sqlite_factory.go 在
// gonavi_sqlite_driver 标签下注册) → 用户建连接必然失败。
//
// 现在 lite 版如实回答: 只有带自己那个构建标签编进来的驱动才算包含。
// 各驱动的具体答案由 optional_driver_included_*.go 按标签给出(新驱动接入时加一个文件)。
func optionalGoDriverBuildIncluded(driverType string) bool {
	if _, ok := optionalGoDrivers[normalizeRuntimeDriverType(driverType)]; !ok {
		return false
	}
	return optionalDriverTaggedIncluded(driverType)
}
