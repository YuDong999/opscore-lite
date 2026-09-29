// Redis Pub/Sub 支持(第 5 项 v2 的一部分)。
//
// 为什么 Pub/Sub 要单独一层: 它是**长连接 + 推送**模型, 与"发一条命令拿一个结果"完全不同。
// 但 Redis 有个别的引擎没有的好处 —— `PUBSUB CHANNELS` / `PUBSUB NUMSUB` / `PUBSUB NUMPAT`
// 能在**不订阅**的情况下列出频道与订阅数(HISTORY 需要 7.4+)。所以"看有哪些频道"是纯只读,
// 不需要长连接; 只有"真的收消息"才要订阅。
//
// 两个刻意的边界:
//  1. **订阅不做成后台常驻**: 本产品是运维控制台, 不是消息消费端。开一个有限时长的
//     "实时看一会儿"(drain), 到点自动退订 —— 免得留下一条没人管的订阅把连接占着。
//  2. **PUBSUB 子命令是只读, 但 PUBLISH 是写**: 前者进只读白名单, 后者走写通道(带写锁+审计)。
package db

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	redis "github.com/redis/go-redis/v9"
)

// redisPubSubDrainMax 一次"实时看一会儿"的上限, 防止前端传个巨大值把连接占住。
const redisPubSubDrainMax = 60 // 秒

// redisPubSubGlobAll 是"列出全部频道"唯一正确的写法。
//
// go-redis 只有在 pattern == "*" 时才**省略**这个参数; 传 "" 会真的发 `PUBSUB CHANNELS ""`,
// 而空 glob 匹配不到任何频道名 —— 于是界面永远显示"没有活动频道", 请求却是 200、没有错误。
// 这个坑在本轮踩过两次(单机快照、集群汇总各一次), 所以调用点一律用常量, 不再手写字面量。
const redisPubSubGlobAll = "*"

// PubSubChannel 一个频道(或模式)及其订阅数。
type PubSubChannel struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"` // channel | pattern | shard
	Subscribers int64  `json:"subscribers"`
}

// PubSubSnapshot 是"不订阅也能看到"的全景。
type PubSubSnapshot struct {
	Channels []PubSubChannel `json:"channels"`
	// NumPat 是**模式订阅**的总数(PUBSUB NUMPAT), 它没有"某个模式有几个订阅者"的查询
	NumPat int64 `json:"numPat"`
	// ShardSupported 说明服务端是否支持 shard 频道(Redis 7.0+); 不支持时别假装有
	ShardSupported bool   `json:"shardSupported"`
	Note           string `json:"note,omitempty"`
}

// PubSubSnapshotOf 列出当前频道与订阅数(PUBSUB CHANNELS / NUMSUB / NUMPAT)。
//
// 这是**纯只读**, 且不需要建立订阅 —— 所以它能安全地挂在只读通道上。
//
// Cluster 走另一条路(见 clusterPubSubSnapshot): 这些命令在集群里也是单节点语义,
// 直接用 UniversalClient 发就只能看到"随便哪个节点"的订阅情况。
func (e *RedisDB) PubSubSnapshotOf(dbName string) (*PubSubSnapshot, error) {
	if e.isClusterTopology() {
		db, err := redisParseDB(dbName)
		if err != nil {
			return nil, err
		}
		return e.clusterPubSubSnapshot(db)
	}
	db, err := redisParseDB(dbName)
	if err != nil {
		return nil, err
	}
	c, err := e.client(db)
	if err != nil {
		return nil, err
	}
	ctx, cancel := e.ctx()
	defer cancel()

	out := &PubSubSnapshot{Channels: []PubSubChannel{}, Note: ""}
	chans, cerr := c.PubSubChannels(ctx, redisPubSubGlobAll).Result()
	if cerr != nil {
		return nil, fmt.Errorf("redis: PUBSUB CHANNELS 失败: %w", cerr)
	}
	// NUMSUB 一次可以问多个频道
	if len(chans) > 0 {
		counts, nerr := c.PubSubNumSub(ctx, chans...).Result()
		if nerr != nil {
			// 拿不到订阅数不该让整个快照失败 —— 频道列表本身就是有用的信息
			out.Note = "订阅数暂时取不到(" + nerr.Error() + ")"
			counts = map[string]int64{}
		}
		for _, name := range chans {
			out.Channels = append(out.Channels, PubSubChannel{
				Name: name, Kind: "channel", Subscribers: counts[name],
			})
		}
	}
	if n, nerr := c.PubSubNumPat(ctx).Result(); nerr == nil {
		out.NumPat = n
	}

	// 模式订阅: 没有"列出所有模式"的命令, 只能看总数。如实说明, 不编几个出来。
	if out.NumPat > 0 {
		out.Note = strings.TrimSpace(out.Note + fmt.Sprintf(
			" 另有 %d 个模式订阅(Redis 没有'列出模式'的命令, 只能看到总数)", out.NumPat))
	}

	// shard 频道是 7.0+ 才有; 探测一下, 免得界面上给一个必然报错的入口
	if shards, serr := c.PubSubShardChannels(ctx, redisPubSubGlobAll).Result(); serr == nil {
		out.ShardSupported = true
		shardCounts := map[string]int64{}
		if len(shards) > 0 {
			if n, nerr := c.PubSubShardNumSub(ctx, shards...).Result(); nerr == nil {
				shardCounts = n
			}
		}
		for _, name := range shards {
			out.Channels = append(out.Channels, PubSubChannel{
				Name: name, Kind: "shard", Subscribers: shardCounts[name],
			})
		}
	}
	sort.Slice(out.Channels, func(i, j int) bool {
		if out.Channels[i].Subscribers != out.Channels[j].Subscribers {
			return out.Channels[i].Subscribers > out.Channels[j].Subscribers
		}
		return out.Channels[i].Name < out.Channels[j].Name
	})
	return out, nil
}

// PubSubMessage 一条收到的消息。
type PubSubMessage struct {
	Channel    string `json:"channel"`
	Pattern    string `json:"pattern,omitempty"` // 由模式订阅收到时非空
	Payload    string `json:"payload"`
	PayloadB64 bool   `json:"payloadB64,omitempty"` // 非 UTF-8 载荷走 base64
	At         int64  `json:"at"`
}

// PubSubDrainResult 一次"实时看一会儿"的结果。
type PubSubDrainResult struct {
	Messages  []PubSubMessage `json:"messages"`
	Seconds   int             `json:"seconds"`
	Truncated bool            `json:"truncated"` // 到时间上限仍有消息(说明流量大)
	// ElapsedMs 实际盯了多久。配合 Interrupted 才能把"这段时间确实没消息"和
	// "订阅半路断了"分开 —— 后者如果也显示成"没有消息", 用户就以为频道安静, 实际自己已经失聪。
	ElapsedMs   int64  `json:"elapsedMs"`
	Slices      int    `json:"slices,omitempty"` // 重新订阅了几段(见 PubSubDrain 的说明)
	Interrupted bool   `json:"interrupted,omitempty"`
	Note        string `json:"note,omitempty"`
}

//  收到的是"整段一次订阅"。曾经为了绕开安静期读超时切成 7 段, 结果段缝里漏消息(实测 3 丢 1),
//  根因是用了 ReceiveMessage; 改用 Channel()(自带 ping 保活 + 自动重订阅)后不必切段。
//
// 所以这里不做心跳: 段够短就不需要保活, 加心跳反而把"读超时=坏连接"的重连叠加进来。
func (e *RedisDB) PubSubDrain(dbName string, channels []string, patterns []string, seconds int) (*PubSubDrainResult, error) {
	if len(channels) == 0 && len(patterns) == 0 {
		return nil, errors.New("redis: 要先给频道或模式")
	}
	if seconds <= 0 {
		seconds = 5
	}
	if seconds > redisPubSubDrainMax {
		seconds = redisPubSubDrainMax
	}
	db, err := redisParseDB(dbName)
	if err != nil {
		return nil, err
	}
	c, err := e.client(db)
	if err != nil {
		return nil, err
	}

	start := time.Now()
	out := &PubSubDrainResult{Messages: []PubSubMessage{}, Seconds: seconds}
	// **整段只订阅一次**(不再切片)。
	//
	// 为什么要说这件事: 第一版为了绕开"安静期读超时"把窗口切成了 7 段, 每段重新订阅 ——
	// 那等于**自己制造丢消息的机会**(段与段的缝隙里发的消息收不到, 真机实测 3 条丢 1 条)。
	// 根因其实是用了 ReceiveMessage 而它受建订阅时的读超时约束; 改用 Channel()
	// (go-redis 自带周期 ping 与自动重订阅)之后, 一次订阅就能稳稳盯满整段, 不需要切。
	out.Slices = 1
	if serr := e.drainSlice(c, channels, patterns, time.Duration(seconds)*time.Second, out); serr != nil {
		out.Interrupted = true
		out.Note = fmt.Sprintf("订阅失败: %s —— 之后收不到消息, 这**不等于**频道没有消息", serr.Error())
	}
	out.ElapsedMs = time.Since(start).Milliseconds()
	return out, nil
}

// drainSlice 建一次订阅、盯 window 这么久、退订, 期间消息追加进 out。
// 返回的 error 表示"这段没做成"(订阅没建立/读报错), 由调用方转成 interrupted 说明。
//
// **必须用 Channel() 而不是 ReceiveMessage()**: 2026-09-29 真机实测 —— 用 ReceiveMessage
// 时, 那条连接的读超时(建订阅时设的 window)一到就报 `i/o timeout`, 于是一段安静之后就
// 再也收不到消息, 而调用方还以为"这一段没消息"。Channel() 那条路 go-redis 会**周期 ping
// 保活并自动重订阅**, 静默期不会把订阅弄丢(见 pubsub.go 的 comment: "periodically sends
// ping messages ... re-subscribes if ping can not received")。
func (e *RedisDB) drainSlice(
	c redis.UniversalClient, channels, patterns []string, window time.Duration, out *PubSubDrainResult,
) error {
	// 订阅用独立的 context(它必须活够 window), 不套 e.ctx() 的查询超时。
	ctx, cancel := context.WithTimeout(context.Background(), window)
	defer cancel()

	ps := c.Subscribe(ctx, channels...)
	defer func() { _ = ps.Close() }() // **必须退订**: 不退订连接就一直挂着
	if len(patterns) > 0 {
		if err := ps.PSubscribe(ctx, patterns...); err != nil {
			return fmt.Errorf("模式订阅失败: %w", err)
		}
	}
	// 等订阅真正建立(否则头几条消息会漏)。
	//
	// 这里刻意用一次 Receive 拿 *Subscription: Channel() 是异步的, 不等它就返回的话
	// 调用方可能在订阅生效前就开始发消息。注意这一步之后**不能再**用 Receive 系列 API
	// (go-redis 的约定: Channel 与 Receive 互斥), 所以之后只从 channel 读。
	if _, err := ps.Receive(ctx); err != nil {
		return fmt.Errorf("订阅建立失败: %w", err)
	}

	// 保活间隔设成 1s(默认 3s): 我们的窗口本身就很短(切片几秒到 60s), 默认间隔在短窗口里
	// 可能一次都没跑到, 起不到保活作用。
	msgCh := ps.Channel(redis.WithChannelHealthCheckInterval(time.Second),
		redis.WithChannelSize(1024),
		redis.WithChannelSendTimeout(window))

	timer := time.NewTimer(window)
	defer timer.Stop()
	for {
		select {
		case msg, ok := <-msgCh:
			if !ok {
				// 通道关了: 连接彻底断了(重订阅也救不回来)
				return fmt.Errorf("订阅通道已关闭(连接断开且重订阅失败)")
			}
			out.Messages = append(out.Messages, redisPubSubMessage(msg))
			if len(out.Messages) >= 5000 {
				return nil
			}
		case <-timer.C:
			return nil // 这段到点, 正常结束
		case <-ctx.Done():
			return nil
		}
	}
}

// redisPubSubMessage 把 go-redis 的消息转成我们的结构(非 UTF-8 载荷走 base64)。
//
// 载荷可能不是文本(二进制消息), 直接铺到界面上就是乱码 —— 与单元格值同一条规矩:
// 非文本走 base64 并标注, 让人分得清"数据是二进制"还是"显示坏了"。
func redisPubSubMessage(m *redis.Message) PubSubMessage {
	out := PubSubMessage{Channel: m.Channel, Pattern: m.Pattern, At: time.Now().Unix()}
	if !redisLooksTextual(m.Payload) {
		out.PayloadB64 = true
		out.Payload = base64.StdEncoding.EncodeToString([]byte(m.Payload))
		return out
	}
	out.Payload = m.Payload
	return out
}

// RedisPubSubChannelCount 把订阅数转成可读文案(0 订阅也要显示 0, 不显示空)。
func RedisPubSubChannelCount(n int64) string { return strconv.FormatInt(n, 10) }

// PublishTo 往频道发一条消息, 返回**收到这条消息的订阅者数**(Redis 的 PUBLISH 返回值)。
//
// 返回"0 个订阅者"是有意义的信息(消息发出去了但没人听), 所以不把它当成错误。
func (e *RedisDB) PublishTo(dbName, channel, payload string) (int64, error) {
	db, err := redisParseDB(dbName)
	if err != nil {
		return 0, err
	}
	c, err := e.client(db)
	if err != nil {
		return 0, err
	}
	ctx, cancel := e.ctx()
	defer cancel()
	n, perr := c.Publish(ctx, channel, payload).Result()
	if perr != nil {
		return 0, fmt.Errorf("redis: PUBLISH 失败: %w", perr)
	}
	return n, nil
}
