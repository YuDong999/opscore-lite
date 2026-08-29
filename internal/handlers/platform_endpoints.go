package handlers

import (
	"net/http"

	"opscore/internal/platform"
)

// PlatformInventoryHandler GET /api/core/platform/inventory
// 返回多发行版清单(兼容性矩阵), 前端用于展示 opscore 计划支持的发行版基线.
func PlatformInventoryHandler(w http.ResponseWriter, r *http.Request) {
	WriteJSON(w, map[string]any{
		"inventory": platform.DistroInventory,
		"count":     len(platform.DistroInventory),
	})
}

// PlatformProfileHandler GET /api/core/platform/profile?host=ID
// 返回指定主机的平台能力探测结果(远程带缓存, 本机直接探测).
// agent 采集前即据此选择对应发行版的采集命令/解析策略.
func PlatformProfileHandler(w http.ResponseWriter, r *http.Request) {
	if hostID := r.URL.Query().Get("host"); hostID != "" {
		h := resolveAnsibleHost(hostID)
		if h == nil {
			writeErr(w, "未找到指定主机", http.StatusNotFound)
			return
		}
		rmHost := resolveRemoteHost(*h)
		prof := remoteProfile(hostID, rmHost)
		WriteJSON(w, prof)
		return
	}
	WriteJSON(w, platform.DetectLocal())
}
