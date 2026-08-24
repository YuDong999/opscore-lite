package metrics

import (
	"strings"
)

// NormUnitLine 去掉 systemctl 对异常单元(failed/not-found)输出的 ● 标记前缀,
// 否则列位整体右移一位, 解析全部错列。
func NormUnitLine(line string) string {
	line = strings.TrimSpace(line)
	if strings.HasPrefix(line, "\u25cf ") {
		line = strings.TrimPrefix(line, "\u25cf ")
	}
	return line
}

// MergeStoppedUnits 用 list-unit-files 全量枚举补齐"已安装但未加载(停止)"的服务。
// systemd 的 list-units 默认不含已卸载单元, 停止的服务会从列表消失;
// 这里把差集以 Status=inactive/SubStatus=dead 补进结果, 保证列表完整、状态筛选有意义。
// 模板单元(xxx@.service)无法直接操作, 不补。
// StoppedUnitStubs 从 list-unit-files 输出枚举"已安装但未被 list-units 收录"的停止单元。
// 调用方自行去重后转换为各自的 ServiceInfo 类型。
// 模板单元(xxx@.service)无法直接操作, 不产出。
// exclude: 调用方已存在的单元ID集(避免同一服务出现两行)
func StoppedUnitStubs(unitFilesOut string, exclude map[string]bool) []ServiceInfo {
	seen := map[string]bool{}
	for id := range exclude {
		seen[id] = true
	}
	out := []ServiceInfo{}
	for _, line := range strings.Split(strings.TrimSpace(unitFilesOut), "\n") {
		line = NormUnitLine(line)
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		unit := fields[0]
		if seen[unit] || strings.Contains(unit, "@") {
			continue
		}
		seen[unit] = true
		out = append(out, ServiceInfo{
			ID: unit, Name: unit,
			Status: "inactive", SubStatus: "dead",
			Description: unit,
		})
	}
	return out
}

// MergeStoppedUnits 同型合并(agent 与 server 共用同一 ServiceInfo 时使用)。
func MergeStoppedUnits(svcs []ServiceInfo, unitFilesOut string) []ServiceInfo {
	seen := map[string]bool{}
	for _, si := range svcs {
		seen[si.ID] = true
	}
	exclude := map[string]bool{}
	for _, si := range svcs {
		exclude[si.ID] = true
	}
	for _, stub := range StoppedUnitStubs(unitFilesOut, exclude) {
		svcs = append(svcs, stub)
	}
	return svcs
}
