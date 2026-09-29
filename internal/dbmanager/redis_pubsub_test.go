// /redis/pubsub 的入参解析测试。
//
// 这条路上真实发生过的事: handler 先用 query 里的 id 查连接, 查不到才想起 POST 的 id 在
// body 里, 于是同一个 body 被解两次 —— 第二次读到空, 返回"invalid body"。表现为"接口
// 存在但 POST 永远打不通", 只有 URL 补 ?id= 才走得了。下面把"body 只解一次、id 取自 body"
// 钉住, 这条不变量只能靠解析层保证。
//
// 真正"订阅是否收到消息"要链路另一端(见本轮真机实测记录)。
package dbmanager

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRedisPubSubPostTakesIDFromBody(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/dbmanager/redis/pubsub",
		strings.NewReader(`{"id":"aabbccddeeff0011","database":"db3","channels":["news","a:b"],"patterns":["k.*"],"seconds":5}`))
	req, perr := parseRedisPubSubRequest(r)
	if perr != "" {
		t.Fatalf("合法 POST 被判错: %s", perr)
	}
	if req.connID != "aabbccddeeff0011" {
		t.Errorf("id 应取自 body, 得到 %q —— 取不到就是当初那个双重解码", req.connID)
	}
	if !req.drain {
		t.Error("POST 应是订阅(drain), 不是快照")
	}
	if len(req.channels) != 2 || len(req.patterns) != 1 || req.seconds != 5 {
		t.Errorf("入参没解全: %#v", req)
	}
	if vmsg := checkRedisPubSubReq(req); vmsg != "" {
		t.Errorf("合法入参被判非法: %s", vmsg)
	}
}

func TestRedisPubSubGetIsSnapshot(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/dbmanager/redis/pubsub?id=aabbccddeeff0011&database=db0", nil)
	req, perr := parseRedisPubSubRequest(r)
	if perr != "" {
		t.Fatalf("GET 解析失败: %s", perr)
	}
	if req.drain {
		t.Error("GET 不该建立订阅")
	}
	if req.connID != "aabbccddeeff0011" || req.database != "db0" {
		t.Errorf("GET 入参: %#v", req)
	}
	if vmsg := checkRedisPubSubReq(req); vmsg != "" {
		t.Errorf("快照请求被判非法: %s", vmsg)
	}
}

func TestRedisPubSubReqValidation(t *testing.T) {
	ok := func(mut func(*redisPubSubReq)) *redisPubSubReq {
		r := &redisPubSubReq{connID: "aabbccddeeff0011", database: "db0", channels: []string{"news"}, seconds: 5, drain: true}
		mut(r)
		return r
	}
	cases := []struct {
		name string
		req  *redisPubSubReq
		want string
	}{
		{"id 非法", ok(func(r *redisPubSubReq) { r.connID = "../etc" }), "id 格式非法"},
		{"库名空", ok(func(r *redisPubSubReq) { r.database = "" }), "库名非法"},
		{"频道带控制字符", ok(func(r *redisPubSubReq) { r.channels = []string{"a\nb"} }), "频道/模式名非法"},
		{"模式带控制字符", ok(func(r *redisPubSubReq) { r.patterns = []string{"a\t*"} }), "频道/模式名非法"},
		{"没选频道", ok(func(r *redisPubSubReq) { r.channels = nil; r.patterns = nil }), "至少一个频道"},
		{"时长超上限", ok(func(r *redisPubSubReq) { r.seconds = 600 }), "订阅时长"},
		{"时长为负", ok(func(r *redisPubSubReq) { r.seconds = -1 }), "订阅时长"},
	}
	for _, c := range cases {
		got := checkRedisPubSubReq(c.req)
		if !strings.Contains(got, c.want) {
			t.Errorf("%s: 报错 = %q, 期望含 %q", c.name, got, c.want)
		}
	}
	// 模式名里的 * 与空格是合法的(redis 的 PSUBSCRIBE 就是这个形状), 别误伤
	if msg := checkRedisPubSubReq(ok(func(r *redisPubSubReq) {
		r.channels = nil
		r.patterns = []string{"news.*", "user 1:*"}
	})); msg != "" {
		t.Errorf("合法模式被拒: %s", msg)
	}
}
