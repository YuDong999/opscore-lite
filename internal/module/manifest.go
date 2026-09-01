package module

type Manifest struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Icon        string `json:"icon"`
	RoutePath   string `json:"routePath"`
	Group       string `json:"group"` // "core" | "plugin"
	Description string `json:"description"`
}

var coreModules = []Manifest{
	{ID: "resources", Name: "系统资源", Icon: "cpu", RoutePath: "/resources", Group: "core", Description: "CPU / 内存 / 磁盘 / 网络 实时多图式可视化"},
	{ID: "services", Name: "服务发现", Icon: "server", RoutePath: "/services", Group: "core", Description: "运行服务启停 / 重启,查看单元文件与日志位置"},
	{ID: "network", Name: "防火墙和网络", Icon: "network", RoutePath: "/network", Group: "core", Description: "网络接口 / 监听端口 / 防火墙状态与规则(高危,需确认+审计)"},
	{ID: "diagnostics", Name: "系统诊断", Icon: "activity", RoutePath: "/diagnostics", Group: "core", Description: "网络诊断 / 登录审计 / 系统更新"},
	{ID: "tasks", Name: "任务与存储", Icon: "clipboard", RoutePath: "/tasks", Group: "core", Description: "定时任务 / 磁盘挂载 / LVM 管理 / SMART 健康"},
	{ID: "plugins", Name: "插件中心", Icon: "puzzle", RoutePath: "/plugins", Group: "plugin", Description: "可插拔模块管理"},
	{ID: "containers", Name: "容器管理", Icon: "box", RoutePath: "/containers/docker", Group: "plugin", Description: "Docker 管理(启停/删除/日志/镜像/连接走向/策略修改) + Kubernetes 管理(只读)"},
	{ID: "dbmanager", Name: "数据库管理", Icon: "database", RoutePath: "/dbmanager", Group: "plugin", Description: "MySQL/PostgreSQL 连接管理、可视化查询、元数据浏览"},
}

func CoreModules() []Manifest {
	return coreModules
}

func ActiveModules() []Manifest {
	all := coreModules
	res := make([]Manifest, 0, len(all))
	for _, m := range all {
		if m.Group == "core" || (m.Group == "plugin" && (m.ID == "plugins" || IsPluginActive(m.ID))) {
			res = append(res, m)
		}
	}
	return res
}