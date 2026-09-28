package db

import (
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"opscore/internal/dbmanager/gonavi/connection"

	redis "github.com/redis/go-redis/v9"
)

// BrowseKey 的分页语义测试 —— 这是"树里单击 Redis 键能像表一样翻页"的地基。
//
// 为什么挂真 Redis 而不是造假数据: BrowseKey 要证明的是**服务端区间读**的边界
// (LRANGE/ZRANGE/XRANGE 的 offset 语义、HGETALL+排序的切片、XLEN/LLen/ZCard 的总数),
// 这些都是跟 redis 自己的行为对齐才有意义的东西。造一个假 client 只能测出我自己写的假设。
//
// 默认跳过(本机/CI 没 redis)。要跑:
//   OPS_TEST_REDIS=127.0.0.1:6379 go test ./internal/dbmanager/gonavi/db/ -run TestRedisBrowseKey -v
// 可选 OPS_TEST_REDIS_PASSWORD。**只碰 ops:browse:* 前缀的键**, 跑完自己删干净。
func TestRedisBrowseKeyPagination(t *testing.T) {
	addr := os.Getenv("OPS_TEST_REDIS")
	if addr == "" {
		t.Skip("未设 OPS_TEST_REDIS, 跳过 Redis 真机分页测试")
	}
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("OPS_TEST_REDIS 格式应为 host:port, 收到 %q: %v", addr, err)
	}
	port, _ := strconv.Atoi(portStr)

	d := newRedisDB()
	if err := d.Connect(connection.ConnectionConfig{
		Host: host, Port: port, Password: os.Getenv("OPS_TEST_REDIS_PASSWORD"), Database: "db0",
		Timeout: 5, QueryTimeout: 5,
	}); err != nil {
		t.Fatalf("连不上测试 Redis(%s): %v", addr, err)
	}
	defer d.Close()

	// 直接用 go-redis 铺数据(与被测路径无关, 免得用被测代码造测试数据)
	raw := redis.NewClient(&redis.Options{Addr: addr, Password: os.Getenv("OPS_TEST_REDIS_PASSWORD")})
	defer raw.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	keys := []string{"ops:browse:hash", "ops:browse:list", "ops:browse:set", "ops:browse:zset",
		"ops:browse:stream", "ops:browse:str"}
	raw.Del(ctx, keys...)
	defer raw.Del(ctx, keys...)

	const n = 7
	for i := 0; i < n; i++ {
		raw.HSet(ctx, "ops:browse:hash", fmt.Sprintf("f%d", i), fmt.Sprintf("v%d", i))
		raw.RPush(ctx, "ops:browse:list", fmt.Sprintf("item%d", i))
		raw.SAdd(ctx, "ops:browse:set", fmt.Sprintf("member%d", i))
		raw.ZAdd(ctx, "ops:browse:zset", redis.Z{Score: float64(i), Member: fmt.Sprintf("m%d", i)})
		raw.XAdd(ctx, &redis.XAddArgs{Stream: "ops:browse:stream",
			Values: map[string]any{"i": strconv.Itoa(i)}})
	}
	raw.Set(ctx, "ops:browse:str", "hello", 0)

	cases := []struct {
		key   string
		total int
		cols  []string
		// 第二页(offset=3, limit=2)首行该在哪一列上等于什么 —— 证明 offset 真的生效了。
		// 列名要写对: list 的首列是 index 不是 value(第一版这里写错, 测试当场抓到)。
		p2Col  string
		p2Want string
	}{
		{"ops:browse:hash", n, []string{"field", "value"}, "field", "f3"},
		{"ops:browse:list", n, []string{"index", "value"}, "value", "item3"},
		{"ops:browse:set", n, []string{"value"}, "value", "member3"},
		{"ops:browse:zset", n, []string{"member", "score"}, "member", "m3"},
		// stream 的 id 是 <毫秒>-<序号>, 不能比字面量; 但**必须非空** —— 见下面单独那条断言
		{"ops:browse:stream", n, []string{"id", "fields"}, "", ""},
		{"ops:browse:str", 1, []string{"key", "type", "ttl", "value"}, "", ""},
	}
	for _, c := range cases {
		rows, cols, total, err := d.BrowseKey("db0", c.key, 0, 2)
		if err != nil {
			t.Errorf("%s: BrowseKey 出错: %v", c.key, err)
			continue
		}
		if total != c.total {
			t.Errorf("%s: total=%d, 期望 %d", c.key, total, c.total)
		}
		if len(cols) != len(c.cols) {
			t.Errorf("%s: 列=%v, 期望 %v", c.key, cols, c.cols)
		}
		wantRows := 2
		if c.total < 2 {
			wantRows = c.total
		}
		if len(rows) != wantRows {
			t.Errorf("%s: 首页 %d 行, 期望 %d 行", c.key, len(rows), wantRows)
		}
		// 第二页: offset 必须真的跳过去(这正是"翻页不重不漏"的判据)
		rows2, _, _, err := d.BrowseKey("db0", c.key, 3, 2)
		if err != nil {
			t.Errorf("%s: 第二页出错: %v", c.key, err)
			continue
		}
		if c.total > 3 && len(rows2) == 0 {
			t.Errorf("%s: 第二页为空", c.key)
			continue
		}
		if c.p2Col != "" && len(rows2) > 0 {
			if got := fmt.Sprint(rows2[0][c.p2Col]); got != c.p2Want {
				t.Errorf("%s: 第二页首行 %s=%q, 期望 %q(offset 没生效?)", c.key, c.p2Col, got, c.p2Want)
			}
		}
		// 越界页必须是空, 而不是报错或回绕
		rowsEnd, _, _, err := d.BrowseKey("db0", c.key, c.total+10, 2)
		if err != nil {
			t.Errorf("%s: 越界页报错(应为空): %v", c.key, err)
		} else if len(rowsEnd) != 0 {
			t.Errorf("%s: 越界页给了 %d 行, 期望 0", c.key, len(rowsEnd))
		}
	}

	// stream 的条目 ID 必须真的取到。
	// 这一条是补漏: 第一版测试对 stream 只判"第二页非空", 结果漏掉了 Values["__id__"]
	// 这个不存在的键(id 列全是 null, 值却对)。断言要盯住**每一列都有值**。
	srows, _, _, err := d.BrowseKey("db0", "ops:browse:stream", 0, 3)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if len(srows) == 0 {
		t.Fatal("stream: 没读到条目")
	}
	for i, r := range srows {
		if r["id"] == nil || fmt.Sprint(r["id"]) == "" {
			t.Errorf("stream 第 %d 条: id 为空(XMessage.ID 没取到?)", i)
		}
		f, ok := r["fields"].(map[string]interface{})
		if !ok || len(f) == 0 {
			t.Errorf("stream 第 %d 条: fields 为空: %#v", i, r["fields"])
		}
	}

	// 非 UTF-8 / 带控制字符的值走 base64 分支, 且 base64 能解回原文(不是乱码糊上去的)
	raw.Set(ctx, "ops:browse:bin", "A\x01\x02B", 0)
	defer raw.Del(ctx, "ops:browse:bin")
	brows, _, _, err := d.BrowseKey("db0", "ops:browse:bin", 0, 10)
	if err != nil {
		t.Fatalf("二进制值: %v", err)
	}
	if len(brows) != 1 {
		t.Fatalf("二进制值: 读到 %d 行, 期望 1", len(brows))
	}
	enc, ok := brows[0]["value"].(map[string]interface{})
	if !ok {
		t.Fatalf("二进制值没走 base64 分支, 而是 %#v", brows[0]["value"])
	}
	if enc["encoding"] != "base64" {
		t.Errorf("编码标注 = %v, 期望 base64", enc["encoding"])
	}
	decoded, derr := base64.StdEncoding.DecodeString(fmt.Sprint(enc["body"]))
	if derr != nil {
		t.Fatalf("base64 解不开: %v", derr)
	}
	if string(decoded) != "A\x01\x02B" {
		t.Errorf("base64 解回来是 %q, 期望原值", string(decoded))
	}

	// 不存在的键: 空行 + 有列名(网格要能画表头), total=0, 不报错
	rows, cols, total, err := d.BrowseKey("db0", "ops:browse:does-not-exist", 0, 10)
	if err != nil {
		t.Errorf("不存在的键应返回空页而不是报错: %v", err)
	}
	if len(rows) != 0 || total != 0 {
		t.Errorf("不存在的键: rows=%d total=%d, 期望 0/0", len(rows), total)
	}
	if len(cols) == 0 {
		t.Error("不存在的键也要给列名, 否则网格画不出表头")
	}

	// 只读校验: 铺了这么多数据, 全程不该有任何写命令 —— 用一个 GET 确认键还在(没被误删/误改)
	if v := raw.Get(ctx, "ops:browse:str").Val(); v != "hello" {
		t.Errorf("ops:browse:str 被改成了 %q(浏览不该有写副作用)", v)
	}
}
