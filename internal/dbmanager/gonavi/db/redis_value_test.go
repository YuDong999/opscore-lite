package db

import "testing"

// 二进制值必须走 base64 分支: 原样交给界面就是乱码, 用户分不清是数据坏了还是本来就这样。
func TestRedisLooksTextual(t *testing.T) {
	for _, ok := range []string{"你好-redis", "abc", "", "tab\tand\nnewline"} {
		if !redisLooksTextual(ok) {
			t.Errorf("应判为文本: %q", ok)
		}
	}
	for _, bad := range []string{"\x01\x02\x03binary", "nul\x00end", "\xff\xfe"} {
		if redisLooksTextual(bad) {
			t.Errorf("应判为非文本(走 base64): %q", bad)
		}
	}
	if got := redisInlineValue("\x01abc"); got == "\x01abc" {
		t.Error("控制字符值被原样返回了")
	}
}
