// Pub/Sub 的真机测试(默认跳过, 设 OPS_TEST_REDIS 才跑)。
//
// 为什么这两个测试**只能**真机跑: 2026-09-29 实测抓到两处"看起来正常的假成功"——
//  1. 快照发的是 `PUBSUB CHANNELS ""` —— 空 glob 匹配不到任何频道名, 于是界面永远显示
//     "没有活动频道", 而请求本身是 200、没有错误。造假 client 测不出来, 因为假的那侧
//     根本不看 glob 语义。
//  2. "实时看一会儿"只收到第一条消息就断流: 服务端 PUBLISH 返回的订阅者数掉回 1,
//     而 drain 照样回 ok + 1 条。没有真的发布方, 单元测试看不见这件事。
//
// 所以这里的断言都是"数量": 发布方发了 N 条, 这边就必须收到接近 N 条, 而不是"收到过东西"。
package db

import (
	"context"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"opscore/internal/dbmanager/gonavi/connection"

	redis "github.com/redis/go-redis/v9"
)

// psLogger 把 go-redis 的内部日志引到测试输出里(订阅断流这类事只有它自己知道)。
// 只声明 Printf 这个方法 —— 与 redis.internal.Logging 结构相容即可, 不必 import 内部包。
type psLogger struct{ t *testing.T }

func (l psLogger) Printf(ctx context.Context, format string, v ...any) {
	l.t.Logf("go-redis: "+format, v...)
}

func newLiveRedis(t *testing.T) (*RedisDB, string) {
	t.Helper()
	addr := os.Getenv("OPS_TEST_REDIS")
	if addr == "" {
		t.Skip("未设 OPS_TEST_REDIS, 跳过 Redis 真机 Pub/Sub 测试")
	}
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("OPS_TEST_REDIS 格式应为 host:port, 收到 %q: %v", addr, err)
	}
	port, _ := strconv.Atoi(portStr)
	redis.SetLogger(psLogger{t})

	d := newRedisDB()
	if err := d.Connect(connection.ConnectionConfig{
		Host: host, Port: port, Password: os.Getenv("OPS_TEST_REDIS_PASSWORD"), Database: "db0",
		Timeout: 5, QueryTimeout: 5,
	}); err != nil {
		t.Fatalf("连不上测试 Redis(%s): %v", addr, err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d, addr
}

func liveRawClient(t *testing.T, addr string) *redis.Client {
	t.Helper()
	c := redis.NewClient(&redis.Options{Addr: addr, Password: os.Getenv("OPS_TEST_REDIS_PASSWORD")})
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// TestRedisPubSubLive 盯三件事: 快照看得见频道、订阅期间能持续收到、退出后真的退订了。
func TestRedisPubSubLive(t *testing.T) {
	d, addr := newLiveRedis(t)
	raw := liveRawClient(t, addr)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	const chName, patName = "ops:ps:a", "ops:ps:p.live"

	// 先确认"没人订阅时快照是空的" —— 与"命令发错了所以永远空"区分开
	snap0, err := d.PubSubSnapshotOf("db0")
	if err != nil {
		t.Fatalf("快照读取失败: %v", err)
	}
	if hasChannel(snap0, chName) {
		t.Logf("注意: 测试开始前 %s 上已有订阅者(可能上一次没退干净)", chName)
	}

	// 外部订阅者: 让快照必须有东西可看
	ext := raw.Subscribe(ctx, chName)
	defer func() { _ = ext.Close() }()
	if _, err := ext.Receive(ctx); err != nil {
		t.Fatalf("外部订阅建立失败: %v", err)
	}

	snap, err := d.PubSubSnapshotOf("db0")
	if err != nil {
		t.Fatalf("快照读取失败: %v", err)
	}
	if !hasChannel(snap, chName) {
		t.Errorf("快照没看到已存在的频道 %s(得到 %d 个: %v)—— 这就是 PUBSUB CHANNELS 空模式那个 bug",
			chName, len(snap.Channels), channelNames(snap))
	}

	// 发布方: drain 期间每 300ms 发一条(频道 + 模式各一路), 共 ~14 条
	pubCtx, pubCancel := context.WithCancel(context.Background())
	defer pubCancel()
	sent := make(chan int, 1)
	go func() {
		n := 0
		for i := 0; i < 14; i++ {
			select {
			case <-pubCtx.Done():
				sent <- n
				return
			case <-time.After(300 * time.Millisecond):
			}
			if err := raw.Publish(pubCtx, chName, "live-"+strconv.Itoa(i)).Err(); err == nil {
				n++
			}
			_ = raw.Publish(pubCtx, patName, "pat-"+strconv.Itoa(i)).Err()
		}
		sent <- n
	}()

	res, derr := d.PubSubDrain("db0", []string{chName}, []string{"ops:ps:*"}, 8)
	pubCancel()
	gotSent := <-sent
	if derr != nil {
		t.Fatalf("订阅失败: %v", derr)
	}

	// 断流那个 bug 的表现就是"只收到 1 条"
	if len(res.Messages) < gotSent/2 {
		t.Errorf("只收到 %d 条, 发布方发了 %d 条 —— 订阅中途断了(界面上这会显示成\"没有消息\")",
			len(res.Messages), gotSent)
	}
	var chanMsgs, patMsgs int
	for _, m := range res.Messages {
		switch {
		case m.Pattern != "":
			patMsgs++
		case m.Channel == chName:
			chanMsgs++
		}
	}
	if chanMsgs == 0 {
		t.Error("频道消息一条都没收到")
	}
	if patMsgs == 0 {
		t.Error("模式订阅(PSUBSCRIBE)一条都没收到 —— 只建了频道订阅就等于漏收")
	}

	// 退出后必须真的退订: 快照里该频道的订阅者数要回到"只有外部那一个"
	time.Sleep(500 * time.Millisecond)
	after, aerr := d.PubSubSnapshotOf("db0")
	if aerr != nil {
		t.Fatalf("退订后的快照失败: %v", aerr)
	}
	if c := findChannel(after, chName); c != nil && c.Subscribers > 1 {
		t.Errorf("drain 结束后 %s 仍有 %d 个订阅者, 期望只剩外部那 1 个 —— 没退订就是留了一条常驻订阅",
			chName, c.Subscribers)
	}
}

// TestRedisPubSubPublishLive 验发布: 有几个订阅者就该返回几个, 0 个不是错误。
func TestRedisPubSubPublishLive(t *testing.T) {
	d, addr := newLiveRedis(t)
	raw := liveRawClient(t, addr)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const chName = "ops:ps:pub"
	n, err := d.PublishTo("db0", chName, "nobody-listening")
	if err != nil {
		t.Fatalf("发布失败: %v", err)
	}
	if n != 0 {
		t.Logf("没人订阅却返回 %d(说明环境里有别的订阅者, 不影响后面的断言)", n)
	}

	sub := raw.Subscribe(ctx, chName)
	defer func() { _ = sub.Close() }()
	if _, err := sub.Receive(ctx); err != nil {
		t.Fatalf("订阅建立失败: %v", err)
	}
	got := make(chan string, 1)
	go func() {
		m, err := sub.ReceiveMessage(ctx)
		if err == nil {
			got <- m.Payload
		} else {
			got <- "ERR:" + err.Error()
		}
	}()
	time.Sleep(300 * time.Millisecond)

	n2, err2 := d.PublishTo("db0", chName, "console-says-hi")
	if err2 != nil {
		t.Fatalf("发布失败: %v", err2)
	}
	if n2 < 1 {
		t.Errorf("有 1 个订阅者却返回 %d —— 这个数字是界面提示\"几个订阅者收到\"的依据", n2)
	}
	select {
	case p := <-got:
		if p != "console-says-hi" {
			t.Errorf("订阅端收到的载荷 = %q", p)
		}
	case <-time.After(5 * time.Second):
		t.Error("发布成功但订阅端没收到消息")
	}
}

// TestRedisPubSubDrainSurvivesQuiet 盯的是"安静一会儿就失聪"。
//
// 2026-09-29 实测: 每 300ms 发一条时 drain 一切正常, 但**稀疏**流量时订阅会静默失聪 ——
// 服务端 `PUBSUB NUMSUB` 看到的订阅者数掉回 0, 而 drain 照样回 ok、里面只有断流前那几条,
// elapsed 还是走满了窗口。表现到界面上就是"这个频道没有消息", 真相却是"我已经收不到了"。
// 所以这里刻意用 10 秒的间隔(比任何心跳都长), 一条条数: 发了几条就必须收到几条。
func TestRedisPubSubDrainSurvivesQuiet(t *testing.T) {
	d, addr := newLiveRedis(t)
	raw := liveRawClient(t, addr)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	const chName = "ops:ps:quiet"
	const beats = 3
	sent := make(chan int, 1)
	go func() {
		n := 0
		for i := 0; i < beats; i++ {
			select {
			case <-ctx.Done():
				sent <- n
				return
			case <-time.After(10 * time.Second): // 每条之间空 10s, 长过心跳间隔
			}
			if err := raw.Publish(ctx, chName, "quiet-"+strconv.Itoa(i)).Err(); err == nil {
				n++
			}
		}
		sent <- n
	}()

	res, err := d.PubSubDrain("db0", []string{chName}, nil, 34)
	if err != nil {
		t.Fatalf("订阅失败: %v", err)
	}
	gotSent := <-sent
	t.Logf("发布 %d 条 / 收到 %d 条 / 实际盯了 %dms / 重订阅 %d 段 / interrupted=%v note=%q",
		gotSent, len(res.Messages), res.ElapsedMs, res.Slices, res.Interrupted, res.Note)
	// 段与段之间要重新订阅, 落在那几百毫秒里的消息会漏 1 条 —— 允许这一条, 但不允许"后半程全瞎"
	if len(res.Messages) < gotSent-1 {
		t.Errorf("安静期间掉线: 发了 %d 条只收到 %d 条 —— 中间那段没数据的静默把订阅弄丢了",
			gotSent, len(res.Messages))
	}
	if res.Interrupted {
		t.Errorf("被判中断: %s", res.Note)
	}
	if res.ElapsedMs < 33000 {
		t.Errorf("只盯了 %dms(窗口 34s)—— 提前返回就是断流被当成了正常结束", res.ElapsedMs)
	}
}

func hasChannel(s *PubSubSnapshot, name string) bool { return findChannel(s, name) != nil }

func findChannel(s *PubSubSnapshot, name string) *PubSubChannel {
	for i := range s.Channels {
		if s.Channels[i].Name == name {
			return &s.Channels[i]
		}
	}
	return nil
}

func channelNames(s *PubSubSnapshot) []string {
	out := make([]string, 0, len(s.Channels))
	for _, c := range s.Channels {
		out = append(out, c.Name)
	}
	return out
}
