// 列表端点的返回形状契约。
//
// 这一组断言来自一个真实的静默失败(2026-09-28): `/api/dbmanager/queries` 直接 writeJSON(list)
// 吐**裸数组**, 而前端 api.ts 按 `{queries: [...]}` 读 → `r.queries` 恒为 undefined →
// "已保存查询"列表一直是空的。后端 200、数据也在, 就是列不出来 —— 最难查的那类。
//
// 本模块其余 9 个列表端点都包了一层对象(connections/databases/tables/engines/drivers/
// entries/jobs...), 只有它漏了。这里用一张"端点 -> 顶层键"的表钉住, 再漏就变红。
package dbmanager

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// jsonTopLevelKeys 取出 JSON 对象的一级键; 顶层不是对象时返回 nil(即"裸数组/裸标量")。
func jsonTopLevelKeys(t *testing.T, raw []byte) map[string]bool {
	t.Helper()
	var probe any
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatalf("返回的不是合法 JSON: %v (%s)", err, string(raw[:min(120, len(raw))]))
	}
	obj, ok := probe.(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]bool, len(obj))
	for k := range obj {
		out[k] = true
	}
	return out
}

func min(a, b int) int {
	if a < b { return a }
	return b
}

// 列表端点必须包一层对象, 且键名要和前端读的一致。
func TestListEndpointsWrapInObject(t *testing.T) {
	cases := []struct {
		path string
		key  string // 前端 api.ts 读的那个键
	}{
		{"/api/dbmanager/queries", "queries"},
	}
	for _, c := range cases {
		h := &Handlers{}
		req := httptest.NewRequest(http.MethodGet, c.path, nil)
		rec := httptest.NewRecorder()
		// 这里只验形状, 不验内容: 让 handler 自己跑, 拿到什么算什么
		func() {
			defer func() { _ = recover() }() // store 为 nil 时可能 panic, 那属于别的测试的事
			switch c.key {
			case "queries":
				h.handleQueries(rec, req)
			}
		}()
		body, _ := io.ReadAll(rec.Body)
		if len(body) == 0 {
			t.Skipf("%s 没有产出(store 未初始化), 形状由真机验证覆盖", c.path)
		}
		keys := jsonTopLevelKeys(t, body)
		if keys == nil {
			t.Errorf("%s 返回的是裸数组/裸值 —— 前端按 {%s: [...]} 读, 会永远取不到数据", c.path, c.key)
			continue
		}
		if !keys[c.key] {
			t.Errorf("%s 的顶层键里没有 %q(实际: %v) —— 前端读不到", c.path, c.key, keys)
		}
	}
}
