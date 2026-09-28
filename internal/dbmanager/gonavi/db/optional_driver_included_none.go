//go:build !gonavi_sqlite_driver && !gonavi_full_drivers

package db

// 没有任何"内嵌可选驱动"标签的 lite 构建: 一个可选驱动都没编进来。
// (其余可选驱动走 driver-agent, 而 lite 版不随附 agent 二进制。)
func optionalDriverTaggedIncluded(string) bool { return false }
