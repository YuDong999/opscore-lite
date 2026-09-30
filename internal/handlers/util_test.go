package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// WriteJSON 必须在序列化失败时明确报 500, 而不是回 200 + 空 body。
//
// 2026-10-01 真机事故: /api/cicd/actions 的 ActionSpec 含 func 字段且无 json tag,
// 旧实现 `_ = json.NewEncoder(w).Encode(v)` 把 "unsupported type: func(...)" 丢掉,
// 接口回 HTTP 200 + Content-Length: 0 —— 前端动作下拉变空, 而后端日志里一点痕迹都没有。
func TestWriteJSONReportsEncodeFailure(t *testing.T) {
	w := httptest.NewRecorder()
	WriteJSON(w, map[string]any{"bad": func() {}})

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("编码失败应回 500, 实际 %d (以前是 200 + 空 body)", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("错误响应也得是合法 JSON: %v (%q)", err, w.Body.String())
	}
	if _, ok := body["error"]; !ok {
		t.Errorf("错误响应应带 error 字段: %s", w.Body.String())
	}
}

// 正常路径: 正确编码 + Content-Type + 结尾换行(方便 curl 读)。
func TestWriteJSONEncodesNormally(t *testing.T) {
	w := httptest.NewRecorder()
	WriteJSON(w, map[string]any{"ok": true, "n": 3})

	if w.Code != http.StatusOK {
		t.Fatalf("应回 200, 实际 %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type 不对: %q", ct)
	}
	if got := strings.TrimSpace(w.Body.String()); got != `{"n":3,"ok":true}` {
		t.Errorf("body = %s", got)
	}
}
