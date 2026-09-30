package handlers

import (
	"encoding/json"
	"log"
	"net/http"
)

// WriteJSON 统一 JSON 响应。
//
// 关键: **先序列化到内存, 再写**。旧实现 `_ = json.NewEncoder(w).Encode(v)` 把错误丢掉,
// 一旦 v 里有编码不了的东西(func/chan/complex/NaN), 表现是 HTTP 200 + **空 body** ——
// 调用方只看到"没数据", 根本不知道是后端错了。
// 2026-10-01 真机撞到: /api/cicd/actions 的 ActionSpec.Build 是 func 字段,
// 整个动作库接口静默返回空, 前端动作下拉只剩"Shell 命令",
// 8 个动作(Docker 构建 / K8s 应用 / 蓝绿切换 / 金丝雀 …)全部不可选。
// 现在编码失败明确回 500 + 日志, 不再假装成功。
func WriteJSON(w http.ResponseWriter, v any) {
	buf, err := json.Marshal(v)
	if err != nil {
		log.Printf("[handlers] JSON encode failed: %v", err)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"server failed to encode response"}`))
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(append(buf, '\n'))
}
