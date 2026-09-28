// Redis 键级写操作的口径测试。
//
// 这里能真验的是"我们自己这一层"的三件事: 意图 -> 命令文本的渲染(值不落文本)、
// 风险分级(删除比写入高)、以及操作白名单。真正"驱动是否照做"要靠真机(见
// redis_browse_live_test.go 那一路的做法)。
package dbmanager

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestRedisOpCommandTextNeverLeaksValue(t *testing.T) {
	secret := "topsecret-token-value"
	cases := []struct {
		body redisWriteOp
		want string
	}{
		{redisWriteOp{Op: "set", Key: "k", Value: secret}, "SET k <值 21 字节>"},
		{redisWriteOp{Op: "set", Key: "k", Value: secret, TTLSeconds: 60}, "SET k <值 21 字节> EX 60"},
		{redisWriteOp{Op: "expire", Key: "k", TTLSeconds: 60}, "EXPIRE k 60"},
		{redisWriteOp{Op: "expire", Key: "k", TTLSeconds: 0}, "PERSIST k"},
		{redisWriteOp{Op: "del", Key: "k"}, "DEL k"},
		{redisWriteOp{Op: "rename", Key: "a", ToKey: "b"}, "RENAME a b"},
	}
	for _, c := range cases {
		got := redisOpCommandText(c.body)
		if got != c.want {
			t.Errorf("op=%s: 文本 = %q, 期望 %q", c.body.Op, got, c.want)
		}
		if strings.Contains(got, secret) {
			t.Errorf("op=%s: 命令文本里出现了值本身 —— 它会进审计", c.body.Op)
		}
	}
}

func TestRedisOpCommandTextBase64CountsDecodedBytes(t *testing.T) {
	raw := []byte{0x01, 0x02, 0x03, 0x04, 0x05}
	body := redisWriteOp{Op: "set", Key: "bin", Value: base64.StdEncoding.EncodeToString(raw), ValueB64: true}
	got := redisOpCommandText(body)
	if got != "SET bin <值 5 字节>" {
		t.Errorf("base64 值应按解码后的字节数报: %q", got)
	}
}

func TestRedisOpRiskDeleteIsHigh(t *testing.T) {
	if r, _ := redisOpRisk("del"); r != RiskHigh {
		t.Errorf("删除应为 RiskHigh, 得到 %v", r)
	}
	for _, op := range []string{"set", "expire", "rename"} {
		if r, _ := redisOpRisk(op); r.AtLeast(RiskHigh) {
			t.Errorf("%s 不该是 High(rename 有目标已存在就拒这条兜着)", op)
		}
		if r, _ := redisOpRisk(op); !r.AtLeast(RiskMedium) {
			t.Errorf("%s 是写操作, 至少 RiskMedium", op)
		}
	}
}

// 白名单必须只含这四种: 面板能发的意图有限, 不能让"前端传什么就执行什么"。
// 尤其 FLUSHALL/FLUSHDB/CONFIG 这类**绝不能**出现在这里 —— 本产品不做无回滚的全库清空。
func TestRedisOpsWhitelistIsNarrow(t *testing.T) {
	want := map[string]bool{"set": true, "expire": true, "del": true, "rename": true}
	if len(redisOps) != len(want) {
		t.Fatalf("操作白名单大小 = %d, 期望 %d: %v", len(redisOps), len(want), redisOps)
	}
	for op := range want {
		if _, ok := redisOps[op]; !ok {
			t.Errorf("白名单缺 %s", op)
		}
	}
	for _, banned := range []string{"flushall", "flushdb", "config", "shutdown", "debug"} {
		if _, ok := redisOps[banned]; ok {
			t.Errorf("危险操作 %s 不该在白名单里", banned)
		}
	}
}

func TestRedisQuoteKeyForText(t *testing.T) {
	if got := redisQuoteKeyForText("plain:key"); got != "plain:key" {
		t.Errorf("普通键名不该加引号: %q", got)
	}
	if got := redisQuoteKeyForText("has space"); !strings.HasPrefix(got, `"`) {
		t.Errorf("带空格的键名要引起来, 否则审计文本看不出边界: %q", got)
	}
}
