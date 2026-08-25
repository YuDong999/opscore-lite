package handlers

import (
	"net/http"
	"opscore/internal/module"
)

// pluginGuard 插件激活守卫: 未激活直接 403。
// 配合 /api/manifest 实时过滤(见 main.go)实现插件接入/停用热生效 ——
// 路由始终挂载, 是否可用由该守卫在运行时判定, 不再依赖重启。
func pluginGuard(id string, w http.ResponseWriter) bool {
	if module.IsPluginActive(id) {
		return true
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	w.Write([]byte(`{"error":"插件未激活"}`))
	return false
}
