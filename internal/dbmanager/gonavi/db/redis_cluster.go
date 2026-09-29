// Cluster 模式下的"跨节点"操作修正。
//
// 背景(2026-09-28 真机实测发现): Redis Cluster 下 **SCAN 与 DBSIZE 是**按单节点**语义的
// —— go-redis 会把它们路由到任意一个节点, 而不是汇总所有节点。实测: 12 个键分布在
// 3 个主节点(4/3/5), 但 SCAN 只回了 3 个、DBSIZE 只回 5。
//
// 这比"少显示几个键"严重: 用户会以为"集群里就这些键", 进而基于不完整的视图做操作。
// 所以 cluster 模式下这两个操作必须**显式遍历所有主节点并汇总**, 而且要说清"这是汇总值"。
//
// 只读操作, 不涉及写锁; 但仍然逐节点 SCAN(不用 KEYS), 与单机同一条规矩。
package db

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	redis "github.com/redis/go-redis/v9"
)

// clusterNodeAddr 节点的可读地址(报错/汇总说明里要指出是哪一个)。
func clusterNodeAddr(node redis.UniversalClient) string {
	if oc, ok := node.(*redis.Client); ok && oc.Options().Addr != "" {
		return oc.Options().Addr
	}
	return "未知节点"
}

// isClusterTopology 当前连接是不是 cluster 模式。
func (e *RedisDB) isClusterTopology() bool { return e.topology == redisTopologyCluster }

// clusterPrimaryClients 取出**所有主节点**的 client(从节点不用扫, 数据以主为准)。
//
// 拿不到节点列表时返回错误而不是"退回单节点" —— 静默退回就是前面那个 bug 本身。
func (e *RedisDB) clusterPrimaryClients(db int) ([]redis.UniversalClient, error) {
	cc, err := e.clusterClient(db)
	if err != nil {
		return nil, err
	}
	ctx, cancel := e.ctx()
	defer cancel()
	// ForEachMaster 是并发回调, 收集要加锁; 任一节点失败会被它直接返回, 不静默跳过。
	var (
		mu    sync.Mutex
		out   []redis.UniversalClient
		first error
	)
	ferr := cc.ForEachMaster(ctx, func(_ context.Context, node *redis.Client) error {
		mu.Lock()
		out = append(out, node)
		mu.Unlock()
		return nil
	})
	if ferr != nil {
		first = ferr
	}
	if len(out) == 0 {
		if first != nil {
			return nil, fmt.Errorf("redis: 取不到 Cluster 主节点列表: %w", first)
		}
		return nil, fmt.Errorf("redis: 取不到 Cluster 主节点列表(集群可能还没就绪)")
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].(*redis.Client).Options().Addr < out[j].(*redis.Client).Options().Addr
	})
	return out, nil
}

// clusterAllNodeClients 取出**主节点 + 副本**的 client。
//
// 只有 Pub/Sub 这一路需要它: 订阅算在"客户端连在哪条连接"的那个节点上, **副本上也能订阅** ——
// 2026-09-29 实测: 连到副本(18004)的订阅者, 用"只扫主节点"的汇总根本看不见。
// 数据类操作(SCAN/DBSIZE)仍然只看主节点, 那是"以主为准"的另一条规矩。
func (e *RedisDB) clusterAllNodeClients(db int) ([]redis.UniversalClient, error) {
	cc, err := e.clusterClient(db)
	if err != nil {
		return nil, err
	}
	ctx, cancel := e.ctx()
	defer cancel()
	var (
		mu    sync.Mutex
		out   []redis.UniversalClient
		first error
	)
	collect := func(_ context.Context, node *redis.Client) error {
		mu.Lock()
		out = append(out, node)
		mu.Unlock()
		return nil
	}
	if ferr := cc.ForEachMaster(ctx, collect); ferr != nil {
		first = ferr
	}
	if ferr := cc.ForEachSlave(ctx, collect); ferr != nil && first == nil {
		first = ferr
	}
	if len(out) == 0 {
		if first != nil {
			return nil, fmt.Errorf("redis: 取不到 Cluster 节点列表: %w", first)
		}
		return nil, fmt.Errorf("redis: 取不到 Cluster 节点列表(集群可能还没就绪)")
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].(*redis.Client).Options().Addr < out[j].(*redis.Client).Options().Addr
	})
	return out, nil
}

// clusterClient 把通用客户端断言回 *redis.ClusterClient。
func (e *RedisDB) clusterClient(db int) (*redis.ClusterClient, error) {
	c, err := e.client(db)
	if err != nil {
		return nil, err
	}
	cc, ok := c.(*redis.ClusterClient)
	if !ok {
		return nil, fmt.Errorf("redis: 当前连接不是 Cluster 客户端, 无法按节点汇总")
	}
	return cc, nil
}

// scanKeysAllNodes 是 cluster 版的 scanKeys: 逐主节点 SCAN 后汇总去重排序。
//
// 每个节点各自有扫描上限(否则一个大集群会拖很久), 总计不超过 capN;
// 命中上限时由调用方按"可能不全"处理(与单机同一条口径)。
func (e *RedisDB) scanKeysAllNodes(db int, match string, capN int) ([]string, error) {
	nodes, err := e.clusterPrimaryClients(db)
	if err != nil {
		return nil, err
	}
	ctx, cancel := e.ctx()
	defer cancel()

	seen := map[string]bool{}
	out := make([]string, 0, 256)
	perNode := capN
	if len(nodes) > 1 {
		// 按节点数均分上限, 免得第一个节点就把额度吃满、后面的节点一个都不扫
		perNode = capN/len(nodes) + 1
	}
	for _, node := range nodes {
		var cursor uint64
		got := 0
		for {
			keys, next, serr := node.Scan(ctx, cursor, match, int64(redisDefaultScanCount)).Result()
			if serr != nil {
				return nil, fmt.Errorf("redis: 集群节点 SCAN 失败: %w", serr)
			}
			for _, k := range keys {
				if !seen[k] {
					seen[k] = true
					out = append(out, k)
					got++
				}
			}
			cursor = next
			if cursor == 0 || got >= perNode || len(out) >= capN {
				break
			}
		}
		if len(out) >= capN {
			break
		}
	}
	sort.Strings(out)
	if len(out) > capN {
		out = out[:capN]
	}
	return out, nil
}

// clusterDBSize 汇总所有主节点的 DBSIZE。
//
// 单机版 DBSIZE 只回一个节点的数 —— 在集群里那个数没有意义(它只是 1/N)。
func (e *RedisDB) clusterDBSize(db int) (int64, error) {
	nodes, err := e.clusterPrimaryClients(db)
	if err != nil {
		return 0, err
	}
	ctx, cancel := e.ctx()
	defer cancel()
	var total int64
	for _, node := range nodes {
		n, derr := node.DBSize(ctx).Result()
		if derr != nil {
			return 0, fmt.Errorf("redis: 集群节点 DBSIZE 失败: %w", derr)
		}
		total += n
	}
	return total, nil
}

// clusterNodeSummary 给出"这个集群有几个主节点、各有多少键"的可读摘要。
// 界面上的键数徽标用它, 让人一眼看出"这是 N 个节点汇总的"而不是单机数字。
func (e *RedisDB) clusterNodeSummary(db int) ([]map[string]interface{}, error) {
	nodes, err := e.clusterPrimaryClients(db)
	if err != nil {
		return nil, err
	}
	ctx, cancel := e.ctx()
	defer cancel()
	out := make([]map[string]interface{}, 0, len(nodes))
	for _, node := range nodes {
		addr := "未知节点"
		if oc, ok := node.(*redis.Client); ok {
			addr = oc.Options().Addr
		}
		n, derr := node.DBSize(ctx).Result()
		if derr != nil {
			n = -1 // 取不到的节点如实标 -1, 不假装 0
		}
		out = append(out, map[string]interface{}{"node": addr, "keys": n})
	}
	sort.Slice(out, func(i, j int) bool {
		return fmt.Sprint(out[i]["node"]) < fmt.Sprint(out[j]["node"])
	})
	return out, nil
}

// clusterScanNote 是给界面的诚实说明: 集群下"键列表"是逐节点汇总的。
func clusterScanNote(primaryCount int) string {
	if primaryCount <= 1 {
		return ""
	}
	return fmt.Sprintf("键列表由 %d 个主节点汇总(Cluster 的 SCAN/DBSIZE 是单节点语义, 不汇总会漏键)",
		primaryCount)
}

// clusterPubSubSnapshot 是 cluster 版的频道快照: 逐节点(主 + 副本)问 PUBSUB 再汇总。
//
// 与 SCAN/DBSIZE 是同一类问题(见本文件头): `PUBSUB CHANNELS/NUMSUB/NUMPAT` 只报**应答那个
// 节点**上的订阅情况。而 Redis Cluster 里普通频道是广播投递的 —— 订阅者连在哪个节点,
// 那个节点才知道它。所以"这个频道有几个订阅者"必须是各节点之和, 取一个节点等于报 1/N。
//
// 与那两个不同的是**副本也要扫**: 订阅挂在"客户端连的那条连接"上, 连到副本的订阅者只扫主看不到
// (2026-09-29 实测)。
//
// shard 频道(SSUBSCRIBE)相反: 它只在**所属槽的那个节点**投递, 汇总只是为了让人看得见,
// 不代表跨节点收得到 —— 这句得写在 note 里, 不然界面上的"看到了"会被当成"收得到"。
func (e *RedisDB) clusterPubSubSnapshot(db int) (*PubSubSnapshot, error) {
	nodes, err := e.clusterAllNodeClients(db)
	if err != nil {
		return nil, err
	}
	ctx, cancel := e.ctx()
	defer cancel()

	channels := map[string]bool{}
	shards := map[string]bool{}
	counts := map[string]int64{}
	shardCounts := map[string]int64{}
	out := &PubSubSnapshot{Channels: []PubSubChannel{}, ShardSupported: false}
	var failed []string
	for _, node := range nodes {
		addr := clusterNodeAddr(node)
		chans, cerr := node.PubSubChannels(ctx, redisPubSubGlobAll).Result()
		if cerr != nil {
			failed = append(failed, addr)
			continue
		}
		for _, c := range chans {
			channels[c] = true
		}
		if len(chans) > 0 {
			if n, nerr := node.PubSubNumSub(ctx, chans...).Result(); nerr == nil {
				for k, v := range n {
					counts[k] += v
				}
			}
		}
		if n, perr := node.PubSubNumPat(ctx).Result(); perr == nil {
			out.NumPat += n
		}
		// shard 频道是 7.0+ 才有: 任一节点支持就认为支持(同批部署的版本不会差一个大版本)。
		// 模式传 "*" —— 空串会发成 `PUBSUB SHARDCHANNELS ""`, 空 glob 匹配不到任何名字。
		if sc, serr := node.PubSubShardChannels(ctx, redisPubSubGlobAll).Result(); serr == nil {
			out.ShardSupported = true
			for _, c := range sc {
				shards[c] = true
			}
			if len(sc) > 0 {
				if n, nerr := node.PubSubShardNumSub(ctx, sc...).Result(); nerr == nil {
					for k, v := range n {
						shardCounts[k] += v
					}
				}
			}
		}
	}
	if len(failed) == len(nodes) {
		return nil, fmt.Errorf("redis: 所有节点的 PUBSUB 都失败了(第一个: %s)", failed[0])
	}

	for c := range channels {
		out.Channels = append(out.Channels, PubSubChannel{Name: c, Kind: "channel", Subscribers: counts[c]})
	}
	for c := range shards {
		out.Channels = append(out.Channels, PubSubChannel{Name: c, Kind: "shard", Subscribers: shardCounts[c]})
	}
	sort.Slice(out.Channels, func(i, j int) bool {
		if out.Channels[i].Subscribers != out.Channels[j].Subscribers {
			return out.Channels[i].Subscribers > out.Channels[j].Subscribers
		}
		return out.Channels[i].Name < out.Channels[j].Name
	})

	if len(nodes) > 1 {
		out.Note = fmt.Sprintf(
			"订阅数由 %d 个节点(含副本)汇总(Cluster 的 PUBSUB 是单节点语义); shard 频道只在所属节点投递, 汇总不等于跨节点收得到",
			len(nodes))
	}
	if len(failed) > 0 {
		out.Note = strings.TrimSpace(out.Note + fmt.Sprintf(" 有 %d 个节点没问到(%s)", len(failed), strings.Join(failed, ", ")))
	}
	return out, nil
}
