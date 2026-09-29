// Redis 引擎 v1（P0-1）。
//
// Redis 没有 schema、没有 SQL, 所以这一层的"伪 SQL"口径要单独定: 把界面已经在发的
// `SELECT * FROM <key> [LIMIT n]` 解释成"读这个键的值(集合类取前 n 条)", 于是
// 左树(库→键)与公共数据网格**不用改**就能看数据 —— 与 MQ 引擎走伪 SQL 是同一套路子。
// 写侧同理: 界面里能发的只有命令文本, 因此 Exec 只认固定的一小组命令, 且**绝不**把任意
// 文本转手发给服务端执行(见 redisWriteCommand 的白名单)。
//
// 三个刻意的取舍:
//  1. 每个 db index 一个 client, 不用 SELECT 切库: go-redis 的 Client 会把命令散到池子里,
//     连接级状态(SELECT 的当前库)在并发下会串。
//  2. 列键只用 SCAN, 永不发 KEYS: KEYS 在服务端是 O(N) 且阻塞, 生产库上会卡住整个 redis。
//     结果有硬上限, 超了如实标 partial, 不假装列全了。
//  3. 字节值不当字符串硬解: 非 UTF-8 走 base64 并标 encoding, 免得界面上显示出乱码还以为是数据本身。
package db

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"opscore/internal/dbmanager/gonavi/connection"

	redis "github.com/redis/go-redis/v9"
)

// Redis 的三种拓扑。**同一个实现支持三种**, 差别只在建 client 的 options
// (go-redis 的 UniversalClient 把三者统一了), 上层的 SCAN/GET/TTL 调用一字不改。
const (
	redisTopologySingle   = "single"   // 单机: 一个地址
	redisTopologySentinel = "sentinel" // 哨兵: 给哨兵地址列表 + master 名
	redisTopologyCluster  = "cluster"  // 集群: 给种子节点列表, 客户端自动跟随 MOVED/ASK
)

const (
	redisDefaultScanCount   = 500
	redisMaxKeysListed      = 2000 // 列键上限: 超了就标 partial, 不把大库整个拖进界面
	redisMaxValueItems      = 300  // 集合类一次最多渲染多少条
	redisMaxInlineValueSize = 256 << 10
)

// RedisDB 是引擎对外的句柄。
//
// 三种拓扑(single / sentinel / cluster)共用**一套实现**: go-redis 的 UniversalClient
// 把 *Client / *ClusterClient / *FailoverClient 统一成一个接口, 所以这里存的 client 类型
// 是 UniversalClient 而不是 *redis.Client —— 上层的 SCAN/GET/TTL 等调用一字不改。
// 具体建哪一种由 NewUniversalClient 按 options 判定(见 redisUniversalOptions)。
//
// 每个 db index 一个 client(见文件头取舍 1)。注意 **cluster 模式只有 db 0**:
// Redis Cluster 协议不支持多库, 所以 db index > 0 时这里直接报错而不是静默按 db0 跑。
type RedisDB struct {
	clientMu sync.Mutex
	clients  map[int]redis.UniversalClient
	uopts    *redis.UniversalOptions
	dbIndex  int
	timeout  time.Duration
	queryTO  time.Duration
	// topology 记录连接形态(single/sentinel/cluster), 供能力清单与错误文案用
	topology string
}

func newRedisDB() *RedisDB {
	return &RedisDB{clients: map[int]redis.UniversalClient{}, dbIndex: 0, topology: "single"}
}

func (e *RedisDB) ctx() (context.Context, context.CancelFunc) {
	if e.queryTO > 0 {
		return context.WithTimeout(context.Background(), e.queryTO)
	}
	return context.WithTimeout(context.Background(), 30*time.Second)
}

func (e *RedisDB) client(db int) (redis.UniversalClient, error) {
	e.clientMu.Lock()
	defer e.clientMu.Unlock()
	if c, ok := e.clients[db]; ok {
		return c, nil
	}
	if e.clients == nil {
		// 工厂是按 &RedisDB{} 裸建的, 没走构造函数 —— 这里兜住, 否则写 nil map 直接 panic
		e.clients = map[int]redis.UniversalClient{}
	}
	if e.uopts == nil {
		e.uopts = &redis.UniversalOptions{}
	}
	// cluster 只有 db0: 协议层面不支持多库。这里明确拒绝而不是静默按 db0 跑 ——
	// 静默会让用户以为自己在看 db3, 实际看的是 db0。
	if e.topology == redisTopologyCluster && db != 0 {
		return nil, fmt.Errorf("redis: Cluster 模式只有 db0(Redis Cluster 不支持多库), 无法切到 db%d", db)
	}
	opt := *e.uopts
	opt.DB = db
	c := redis.NewUniversalClient(&opt)
	e.clients[db] = c
	return c, nil
}

// Connect 建立连接(只验证默认库可用, 其余库按需建)。
func (e *RedisDB) Connect(config connection.ConnectionConfig) error {
	e.timeout = redisDialTimeout(config)
	e.queryTO = redisQueryTimeout(config)

	uopts, topo, err := redisUniversalOptions(config)
	if err != nil {
		return err
	}
	e.uopts = uopts
	e.topology = topo

	e.dbIndex = redisPickDB(config)
	if e.dbIndex < 0 {
		return fmt.Errorf("redis: 非法的库号 %d", e.dbIndex)
	}
	// cluster 只有 db0: 在这里就拦住, 免得用户以为自己连的是 db3
	if topo == redisTopologyCluster && e.dbIndex != 0 {
		return fmt.Errorf("redis: Cluster 模式只有 db0(Redis Cluster 协议不支持多库), "+
			"当前配置要求 db%d", e.dbIndex)
	}

	c, cerr := e.client(e.dbIndex)
	if cerr != nil {
		return cerr
	}
	ctx, cancel := context.WithTimeout(context.Background(), e.timeout+5*time.Second)
	defer cancel()
	if err := c.Ping(ctx).Err(); err != nil {
		_ = c.Close()
		e.clients = map[int]redis.UniversalClient{}
		return fmt.Errorf("redis: 连接失败: %w", err)
	}
	return nil
}

// redisUniversalOptions 按连接配置判定拓扑并生成 go-redis 的通用 options。
//
// 三条规则, 都"以显式配置为准, 拿不准就报错", 不猜:
//  1. topology=cluster 或给了多个 host → cluster(种子列表, 客户端自动跟随重定向);
//  2. topology=sentinel 或填了 sentinel master 名 → sentinel(要哨兵地址 + master 名);
//  3. 其余 → 单机。
func redisUniversalOptions(config connection.ConnectionConfig) (*redis.UniversalOptions, string, error) {
	hosts := normalizeRedisHosts(config)
	if len(hosts) == 0 {
		return nil, "", errors.New("redis: 缺少地址")
	}
	topo := strings.ToLower(strings.TrimSpace(config.Topology))
	master := strings.TrimSpace(config.RedisSentinelMaster)

	// 显式冲突要说清: 既说是 cluster 又给了 master 名, 只能拒绝
	if topo == redisTopologyCluster && master != "" {
		return nil, "", errors.New("redis: Cluster 模式不需要 Sentinel master 名, 请清掉其中一项")
	}

	uopts := &redis.UniversalOptions{
		Username: strings.TrimSpace(config.User),
		Password: config.Password,
		// 哨兵自身也可能是带密码的(与数据节点不同账号), 所以单独给
		SentinelUsername: strings.TrimSpace(config.RedisSentinelUser),
		SentinelPassword: config.RedisSentinelPassword,
		DialTimeout:      redisDialTimeout(config),
	}
	if config.UseSSL || strings.EqualFold(config.SSLMode, "skip-verify") {
		uopts.TLSConfig = &tls.Config{InsecureSkipVerify: strings.EqualFold(config.SSLMode, "skip-verify")}
	}

	switch {
	case topo == redisTopologyCluster || (topo == "" && len(hosts) > 1 && master == ""):
		uopts.Addrs = hosts
		// 默认让读也走主节点: 本产品是运维工具, 看的是"权威值", 不是缓存读
		uopts.ReadOnly = false
		return uopts, redisTopologyCluster, nil

	case topo == redisTopologySentinel || master != "":
		if master == "" {
			return nil, "", errors.New("redis: Sentinel 模式必须填 master 名")
		}
		uopts.Addrs = hosts // 哨兵地址列表
		uopts.MasterName = master
		return uopts, redisTopologySentinel, nil

	default:
		if len(hosts) > 1 && topo == "" {
			// 给了多个地址但没说是 cluster: 上面第一个 case 已覆盖, 这里兜底成 cluster 更合直觉
			uopts.Addrs = hosts
			return uopts, redisTopologyCluster, nil
		}
		uopts.Addrs = hosts[:1]
		return uopts, redisTopologySingle, nil
	}
}

// normalizeRedisHosts 把"host+port"与"hosts 列表"两种填法归一成 host:port 列表。
//
// 为什么两种都收: 表单上是 host/port 两个格子(单机最顺手), 而 cluster/sentinel 需要
// 一串地址 —— 硬要求用户去填一串字符串才能用集群, 不如两者都支持。
func normalizeRedisHosts(config connection.ConnectionConfig) []string {
	var out []string
	for _, h := range config.Hosts {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		// 没带端口的补默认端口(用户常只填主机名)
		if !strings.Contains(h, ":") {
			h = net.JoinHostPort(h, "6379")
		}
		out = append(out, h)
	}
	if len(out) == 0 {
		host := strings.TrimSpace(config.Host)
		if host == "" {
			return nil
		}
		port := config.Port
		if port <= 0 {
			port = 6379
		}
		out = append(out, net.JoinHostPort(host, strconv.Itoa(port)))
	}
	return out
}

func (e *RedisDB) Close() error {
	e.clientMu.Lock()
	defer e.clientMu.Unlock()
	var first error
	for db, c := range e.clients {
		if err := c.Close(); err != nil && first == nil {
			first = err
		}
		delete(e.clients, db)
	}
	return first
}

func (e *RedisDB) Ping() error {
	c, err := e.client(e.dbIndex)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), e.timeout+5*time.Second)
	defer cancel()
	return c.Ping(ctx).Err()
}

// ---------------------------------------------------------------- 元数据

// GetDatabases 返回 db0..db(N-1)。N 优先问 CONFIG GET databases(某些托管实例禁 CONFIG,
// 拿不到就退回常见的 16)。
func (e *RedisDB) GetDatabases() ([]string, error) {
	n := 16
	c, err := e.client(e.dbIndex)
	if err == nil {
		ctx, cancel := e.ctx()
		defer cancel()
		if v, cerr := c.ConfigGet(ctx, "databases").Result(); cerr == nil {
			if s, ok := v["databases"]; ok {
				if parsed, perr := strconv.Atoi(fmt.Sprint(s)); perr == nil && parsed > 0 && parsed <= 1024 {
					n = parsed
				}
			}
		}
	}
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, "db"+strconv.Itoa(i))
	}
	return out, nil
}

// GetTables 用 SCAN 列键(不用 KEYS, 见文件头取舍 2)。返回 partial 与否由调用方看条数推断,
// 这里在最后一项里塞一个提示键太脏, 所以走 note 通道: 界面上的"键数"来自 DBSIZE。
func (e *RedisDB) GetTables(dbName string) ([]string, error) {
	db, err := redisParseDB(dbName)
	if err != nil {
		return nil, err
	}
	return e.scanKeys(db, "*", redisMaxKeysListed)
}

func (e *RedisDB) scanKeys(db int, match string, capN int) ([]string, error) {
	if e.isClusterTopology() {
		// Cluster 的 SCAN 是**单节点**语义: 只扫到一个节点上就会漏键, 且漏得看不出来。
		return e.scanKeysAllNodes(db, match, capN)
	}
	c, err := e.client(db)
	if err != nil {
		return nil, err
	}
	ctx, cancel := e.ctx()
	defer cancel()
	var (
		cursor uint64
		out    = make([]string, 0, 256)
	)
	for {
		keys, next, serr := c.Scan(ctx, cursor, match, int64(redisDefaultScanCount)).Result()
		if serr != nil {
			return nil, fmt.Errorf("redis: SCAN 失败: %w", serr)
		}
		out = append(out, keys...)
		cursor = next
		if cursor == 0 || len(out) >= capN {
			break
		}
	}
	if len(out) > capN {
		out = out[:capN]
	}
	sort.Strings(out)
	return out, nil
}

// GetCreateStatement 给一个人类可读的键摘要(不是 DDL, Redis 没有 DDL)。
func (e *RedisDB) GetCreateStatement(dbName, tableName string) (string, error) {
	db, err := redisParseDB(dbName)
	if err != nil {
		return "", err
	}
	ctx, cancel := e.ctx()
	defer cancel()
	c, err := e.client(db)
	if err != nil {
		return "", err
	}
	typ, err := c.Type(ctx, tableName).Result()
	if err != nil {
		return "", fmt.Errorf("redis: TYPE %s 失败: %w", tableName, err)
	}
	ttl := c.TTL(ctx, tableName).Val()
	size := redisSizeOf(ctx, c, typ, tableName)
	return fmt.Sprintf("KEY %s  type=%s  ttl=%s  items=%d", redisQuoteKey(tableName), typ,
		redisTTLText(ttl), size), nil
}

// GetColumns 按类型给"列", 让公共网格能直接渲染(字符串=1 列; 哈希=field/value ...)。
func (e *RedisDB) GetColumns(dbName, tableName string) ([]connection.ColumnDefinition, error) {
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
	typ, err := c.Type(ctx, tableName).Result()
	if err != nil {
		return nil, err
	}
	return redisColumnsFor(typ), nil
}

func (e *RedisDB) GetAllColumns(dbName string) ([]connection.ColumnDefinitionWithTable, error) {
	return nil, nil // Redis 无 schema: 交给逐键的 GetColumns
}

func (e *RedisDB) GetIndexes(dbName, tableName string) ([]connection.IndexDefinition, error) {
	return nil, nil
}

func (e *RedisDB) GetForeignKeys(dbName, tableName string) ([]connection.ForeignKeyDefinition, error) {
	return nil, nil
}

func (e *RedisDB) GetTriggers(dbName, tableName string) ([]connection.TriggerDefinition, error) {
	return nil, nil
}

// ---------------------------------------------------------------- 查询

var (
	redisSelectRE   = regexp.MustCompile(`(?is)^\s*SELECT\s+(.+?)\s+FROM\s+(` + "`" + `?[^` + "`" + `\s]+` + "`" + `?)(\s+LIMIT\s+(\d+))?\s*$`)
	redisScanRE     = regexp.MustCompile(`(?is)^\s*SCAN(\s+(\d+))?(\s+MATCH\s+(\S+))?(\s+COUNT\s+(\d+))?\s*$`)
	redisWordsRE    = regexp.MustCompile(`(?is)^\s*([A-Za-z]+)`)
	redisCommentRE  = regexp.MustCompile(`(?s)#[^\n]*|/\*.*?\*/`)
	redisSingleNums = map[string]bool{"DBSIZE": true, "EXISTS": true, "TTL": true, "PTTL": true, "TYPE": true}
)

// Query 执行只读命令。允许的命令见 redisReadCommands; 不在表里的直接拒, 不转手发给服务端。
func (e *RedisDB) Query(query string) ([]map[string]interface{}, []string, error) {
	text := strings.TrimSpace(redisCommentRE.ReplaceAllString(query, " "))
	if text == "" {
		return nil, nil, errors.New("redis: 空语句")
	}
	up := strings.ToUpper(text)

	// 界面(公共数据网格)发的是 SELECT * FROM <key> [LIMIT n] —— 解释成"读这个键"
	if m := redisSelectRE.FindStringSubmatch(text); m != nil {
		db := e.dbIndex
		key := strings.Trim(m[2], "`")
		limit := redisMaxValueItems
		if m[4] != "" {
			if n, err := strconv.Atoi(m[4]); err == nil && n > 0 && n < limit {
				limit = n
			}
		}
		if cols := strings.TrimSpace(m[1]); cols != "*" && !strings.EqualFold(cols, "key") {
			return nil, nil, fmt.Errorf("redis: 只支持 SELECT * 或 SELECT <列>, 不支持 %q", cols)
		}
		return e.readKey(db, key, limit)
	}

	switch {
	case strings.HasPrefix(up, "INFO"):
		return e.rawQuery(text)
	case strings.HasPrefix(up, "DBSIZE"):
		if e.isClusterTopology() {
			return e.clusterDbsizeRows()
		}
		return e.rawQuery(text)
	case strings.HasPrefix(up, "KEYS"):
		return nil, nil, errors.New("redis: 不用 KEYS(服务端 O(N) 且阻塞), 请改用 SCAN 或界面上的键列表")
	case redisScanRE.MatchString(text):
		return e.rawQuery(text)
	case strings.HasPrefix(up, "TTL "), strings.HasPrefix(up, "PTTL "), strings.HasPrefix(up, "TYPE "),
		strings.HasPrefix(up, "EXISTS "), strings.HasPrefix(up, "MEMORY USAGE "):
		return e.rawQuery(text)
	case strings.HasPrefix(up, "HGETALL "), strings.HasPrefix(up, "SMEMBERS "), strings.HasPrefix(up, "LRANGE "),
		strings.HasPrefix(up, "ZRANGE "), strings.HasPrefix(up, "XRANGE "), strings.HasPrefix(up, "GET "):
		return e.rawQuery(text)
	case strings.HasPrefix(up, "PUBSUB "), strings.HasPrefix(up, "PUBSUB"):
		// PUBSUB 的子命令都是只读; 具体子命令的合法性交给 redis 自己判(它会回明确错误)
		return e.rawQuery(text)
	}
	return nil, nil, fmt.Errorf("redis: 不支持的只读命令: %s (可用: INFO/DBSIZE/SCAN/TTL/TYPE/EXISTS/MEMORY USAGE/HGETALL/SMEMBERS/LRANGE/ZRANGE/XRANGE/GET/SELECT * FROM <key>)",
		firstRedisWord(text))
}

func (e *RedisDB) rawQuery(text string) ([]map[string]interface{}, []string, error) {
	db := e.dbIndex
	if m := regexp.MustCompile(`(?is)^\s*SELECT\s+(\d+)\s*$`).FindStringSubmatch(text); m != nil {
		n, _ := strconv.Atoi(m[1])
		db = n
	}
	c, err := e.client(db)
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := e.ctx()
	defer cancel()
	args := redisSplitArgs(text)
	if len(args) == 0 {
		return nil, nil, errors.New("redis: 空语句")
	}
	// 这里只放**读**命令白名单, 所以直接 Do 是安全的
	if !redisIsReadArgv(args) {
		return nil, nil, fmt.Errorf("redis: %s 不是只读命令, 请走执行(写)通道", strings.ToUpper(args[0]))
	}
	res, err := c.Do(ctx, redisArgs(args)...).Result()
	if err != nil {
		return nil, nil, fmt.Errorf("redis: %s", err.Error())
	}
	return redisRowsOf(args[0], res)
}

// readKey 按类型读一个键的值, 渲染成网格能显示的行。
// 与 /data 走的是同一个 BrowseKey —— 查询编辑器里的 `SELECT * FROM <key>` 和
// 树里单击键打开的网格必须给出同一种形状, 否则"同一个键两种显示"就是下一个 bug。
func (e *RedisDB) readKey(db int, key string, limit int) ([]map[string]interface{}, []string, error) {
	rows, cols, _, err := e.BrowseKey("db"+strconv.Itoa(db), key, 0, limit)
	return rows, cols, err
}

// BrowseKey 实现 KeyValueBrowser: 取一个键的第 [offset, offset+limit) 段, 并给出总条目数。
//
// 分页口径按类型定:
//   - string 恒为 1 条(offset>0 返回空页);
//   - list/zset/stream 有服务端区间读(LRANGE/ZRANGE/XRANGE), 直接按 offset 取, 不把整个键拉进来;
//   - hash/set 没有区间读命令, 只能取全量再排序切片 —— 顺序是排序后的, 所以翻页稳定(不会重/漏),
//     代价是大 hash 每次翻页都是 O(N)。这是刻意的取舍: HSCAN 的游标顺序既不稳定也不可排序,
//     用它翻页会真的漏条目。上限见 redisMaxValueItems。
func (e *RedisDB) BrowseKey(dbName, keyName string, offset, limit int) ([]map[string]interface{}, []string, int, error) {
	db, err := redisParseDB(dbName)
	if err != nil {
		return nil, nil, 0, err
	}
	if strings.TrimSpace(keyName) == "" {
		return nil, nil, 0, errors.New("redis: 键名不能为空")
	}
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 || limit > redisMaxValueItems {
		limit = redisMaxValueItems
	}
	c, err := e.client(db)
	if err != nil {
		return nil, nil, 0, err
	}
	ctx, cancel := e.ctx()
	defer cancel()
	typ, err := c.Type(ctx, keyName).Result()
	if err != nil {
		return nil, nil, 0, fmt.Errorf("redis: TYPE %s 失败: %w", keyName, err)
	}
	if typ == "none" {
		// 键不存在: 空页 + 该类型的列名, 让网格能画出表头而不是报错
		return []map[string]interface{}{}, redisColumnNames(redisColumnsFor(typ)), 0, nil
	}
	ttl := c.TTL(ctx, keyName).Val()
	rows := make([]map[string]interface{}, 0, 16)

	switch typ {
	case "string":
		if offset > 0 {
			return rows, redisColumnNames(redisColumnsFor(typ)), 1, nil
		}
		v, gerr := c.Get(ctx, keyName).Result()
		if gerr != nil && !errors.Is(gerr, redis.Nil) {
			return nil, nil, 0, gerr
		}
		rows = append(rows, map[string]interface{}{"key": keyName, "type": typ, "ttl": redisTTLText(ttl),
			"value": redisInlineValue(v)})
		return rows, redisColumnNames(redisColumnsFor(typ)), 1, nil

	case "hash":
		hs, gerr := c.HGetAll(ctx, keyName).Result()
		if gerr != nil {
			return nil, nil, 0, gerr
		}
		fields := make([]string, 0, len(hs))
		for f := range hs {
			fields = append(fields, f)
		}
		sort.Strings(fields)
		total := len(fields)
		for i := offset; i < total && i < offset+limit; i++ {
			rows = append(rows, map[string]interface{}{"field": fields[i], "value": redisInlineValue(hs[fields[i]])})
		}
		return rows, redisColumnNames(redisColumnsFor(typ)), total, nil

	case "list":
		total := int(c.LLen(ctx, keyName).Val())
		if offset < total {
			vs, gerr := c.LRange(ctx, keyName, int64(offset), int64(offset+limit-1)).Result()
			if gerr != nil {
				return nil, nil, 0, gerr
			}
			for i, v := range vs {
				rows = append(rows, map[string]interface{}{"index": offset + i, "value": redisInlineValue(v)})
			}
		}
		return rows, redisColumnNames(redisColumnsFor(typ)), total, nil

	case "set":
		vs, gerr := c.SMembers(ctx, keyName).Result()
		if gerr != nil {
			return nil, nil, 0, gerr
		}
		sort.Strings(vs)
		total := len(vs)
		for i := offset; i < total && i < offset+limit; i++ {
			rows = append(rows, map[string]interface{}{"value": redisInlineValue(vs[i])})
		}
		return rows, redisColumnNames(redisColumnsFor(typ)), total, nil

	case "zset":
		total := int(c.ZCard(ctx, keyName).Val())
		if offset < total {
			zs, gerr := c.ZRangeWithScores(ctx, keyName, int64(offset), int64(offset+limit-1)).Result()
			if gerr != nil {
				return nil, nil, 0, gerr
			}
			for _, z := range zs {
				rows = append(rows, map[string]interface{}{"member": redisInlineValue(fmt.Sprint(z.Member)),
					"score": z.Score})
			}
		}
		return rows, redisColumnNames(redisColumnsFor(typ)), total, nil

	case "stream":
		total := int(c.XLen(ctx, keyName).Val())
		if offset < total {
			// XRANGE 只能从头数, 所以读到 offset+limit 再切掉前 offset 条
			xs, gerr := c.XRangeN(ctx, keyName, "-", "+", int64(offset+limit)).Result()
			if gerr != nil {
				return nil, nil, 0, gerr
			}
			for i := offset; i < len(xs) && i < offset+limit; i++ {
				// 条目 ID 在 XMessage.ID 上, **不在** Values 里 —— 曾经写成 Values["__id__"],
				// 于是 stream 的 id 列全是 null(值却对)。go-redis 的 XMessage 就是 {ID, Values} 两个字段。
				rows = append(rows, map[string]interface{}{"id": xs[i].ID,
					"fields": redisMapOf(xs[i].Values)})
			}
		}
		return rows, redisColumnNames(redisColumnsFor(typ)), total, nil
	}
	return nil, nil, 0, fmt.Errorf("redis: 暂不支持在网格里展示 %s 类型的值(可以用 TYPE/MEMORY USAGE 看元信息)", typ)
}

// ---------------------------------------------------------------- 类型化写入(KeyValueWriter)

// SetKey 写 string 键。已有键不是 string 就拒绝 —— 用 SET 盖掉别人的 hash/list 是不可逆的数据损失,
// 而"值类型不符"这件事在界面上本来就能看出来, 报错比静默覆盖诚实。
func (e *RedisDB) SetKey(dbName, keyName string, value []byte, ttlSeconds int) error {
	_, c, err := e.writeTarget(dbName, keyName)
	if err != nil {
		return err
	}
	ctx, cancel := e.ctx()
	defer cancel()
	if typ, terr := c.Type(ctx, keyName).Result(); terr == nil && typ != "none" && typ != "string" {
		return fmt.Errorf("redis: 键 %s 现在是 %s 类型, 不能用 SET 覆盖(请先用 DEL 删掉, 或改用对应的写命令)", keyName, typ)
	}
	return c.Set(ctx, keyName, value, time.Duration(ttlSeconds)*time.Second).Err()
}

// ExpireKey 设置/清除过期。ttlSeconds <= 0 = 清除过期(PERSIST), 而不是"立刻过期"。
func (e *RedisDB) ExpireKey(dbName, keyName string, ttlSeconds int) error {
	_, c, err := e.writeTarget(dbName, keyName)
	if err != nil {
		return err
	}
	ctx, cancel := e.ctx()
	defer cancel()
	if ttlSeconds <= 0 {
		// go-redis v9 的 Persist 返回 bool: 真的清掉了才算成功(false = 键不存在或本来就没有过期)
		ok, perr := c.Persist(ctx, keyName).Result()
		if perr != nil {
			return perr
		}
		if !ok {
			return fmt.Errorf("redis: 键 %s 不存在或本来就没有过期时间", keyName)
		}
		return nil
	}
	ok, err := c.Expire(ctx, keyName, time.Duration(ttlSeconds)*time.Second).Result()
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("redis: 键 %s 不存在", keyName)
	}
	return nil
}

// DeleteKey 删键。返回真正删掉的个数。
func (e *RedisDB) DeleteKey(dbName, keyName string) (int64, error) {
	_, c, err := e.writeTarget(dbName, keyName)
	if err != nil {
		return 0, err
	}
	ctx, cancel := e.ctx()
	defer cancel()
	return c.Del(ctx, keyName).Result()
}

// RenameKey 改名。目标已存在时拒绝 —— RENAME 会静默覆盖目标, 那是数据损失。
func (e *RedisDB) RenameKey(dbName, fromKey, toKey string) error {
	if strings.TrimSpace(toKey) == "" {
		return errors.New("redis: 新键名不能为空")
	}
	if fromKey == toKey {
		return nil
	}
	_, c, err := e.writeTarget(dbName, fromKey)
	if err != nil {
		return err
	}
	ctx, cancel := e.ctx()
	defer cancel()
	if n, eerr := c.Exists(ctx, fromKey).Result(); eerr != nil {
		return eerr
	} else if n == 0 {
		return fmt.Errorf("redis: 源键 %s 不存在", fromKey)
	}
	if n, eerr := c.Exists(ctx, toKey).Result(); eerr != nil {
		return eerr
	} else if n > 0 {
		return fmt.Errorf("redis: 目标键 %s 已存在, 改名会覆盖它 —— 本产品不做静默覆盖", toKey)
	}
	return c.Rename(ctx, fromKey, toKey).Err()
}

// writeTarget 解析库名与键名并取出该库的 client(三个写操作共用的前置)。
func (e *RedisDB) writeTarget(dbName, keyName string) (int, redis.UniversalClient, error) {
	db, err := redisParseDB(dbName)
	if err != nil {
		return 0, nil, err
	}
	if strings.TrimSpace(keyName) == "" {
		return 0, nil, errors.New("redis: 键名不能为空")
	}
	c, err := e.client(db)
	if err != nil {
		return 0, nil, err
	}
	return db, c, nil
}

// redisColumnNames 取类型对应的列名(空结果也要给列, 否则网格画不出表头)。
func redisColumnNames(defs []connection.ColumnDefinition) []string {
	names := make([]string, 0, len(defs))
	for _, c := range defs {
		names = append(names, c.Name)
	}
	return names
}

// clusterDbsizeRows 汇总各主节点的 DBSIZE。
// 单机版这个数字在集群里只是 1/N, 直接回给用户等于给一个看起来正常的错数 —— 所以既汇总,
// 也在同一行里写明是几个节点汇总的(口径见 redis_cluster.go 头部)。
func (e *RedisDB) clusterDbsizeRows() ([]map[string]interface{}, []string, error) {
	nodes, err := e.clusterPrimaryClients(e.dbIndex)
	if err != nil {
		return nil, nil, err
	}
	total, err := e.clusterDBSize(e.dbIndex)
	if err != nil {
		return nil, nil, err
	}
	return []map[string]interface{}{{
		"dbsize":        total,
		"primary_nodes": len(nodes),
		"note":          clusterScanNote(len(nodes)),
	}}, []string{"dbsize", "primary_nodes", "note"}, nil
}

// ---------------------------------------------------------------- 写入

// redisWriteCommands 是写侧白名单。之所以要白名单而不是"原样转发": 界面里能拼出任意文本,
// 而 Redis 的 CONFIG SET / DEBUG / SHUTDOWN / FLUSHALL 一旦能被随手发出去, 一个只读锁拦不住
// 把整台实例拖走的操作。FLUSHDB/FLUSHALL/CONFIG SET 这里**刻意不实现**, 要清空请在 Redis 自己的
// 客户端里做 —— 本产品不做没有回滚可言的全库清空。
var redisWriteCommands = map[string]bool{
	"SET": true, "SETEX": true, "PSETEX": true, "SETNX": true, "APPEND": true, "INCR": true, "DECR": true,
	"INCRBY": true, "DECRBY": true, "INCRBYFLOAT": true,
	"DEL": true, "UNLINK": true, "EXPIRE": true, "PEXPIRE": true, "EXPIREAT": true, "PERSIST": true,
	"RENAME": true, "RENAMENX": true,
	"HSET": true, "HMSET": true, "HDEL": true, "HINCRBY": true,
	"LPUSH": true, "RPUSH": true, "LPOP": true, "RPOP": true, "LSET": true, "LREM": true, "LTRIM": true,
	"SADD": true, "SREM": true,
	"ZADD": true, "ZREM": true, "ZINCRBY": true,
	"XADD": true, "XDEL": true,
}

// Exec 执行写命令, 返回受影响键数(删除类返回真正删掉的个数, 其余 1/0)。
func (e *RedisDB) Exec(query string) (int64, error) {
	text := strings.TrimSpace(query)
	args := redisSplitArgs(text)
	if len(args) == 0 {
		return 0, errors.New("redis: 空语句")
	}
	cmd := strings.ToUpper(args[0])
	if !redisWriteCommands[cmd] {
		if blocked := redisBlockedWhy(cmd); blocked != "" {
			return 0, errors.New(blocked)
		}
		return 0, fmt.Errorf("redis: 不支持的写命令 %s (白名单见引擎: 字符串/哈希/列表/集合/有序集合/流的增删改 + 过期 + 改名)", cmd)
	}
	if strings.EqualFold(cmd, "KEYS") || strings.EqualFold(cmd, "FLUSHDB") || strings.EqualFold(cmd, "FLUSHALL") {
		return 0, errors.New("redis: 该命令不在写侧白名单里")
	}
	c, err := e.client(e.dbIndex)
	if err != nil {
		return 0, err
	}
	ctx, cancel := e.ctx()
	defer cancel()
	res, err := c.Do(ctx, redisArgs(args)...).Result()
	if err != nil {
		return 0, fmt.Errorf("redis: %s", err.Error())
	}
	switch cmd {
	case "DEL", "UNLINK":
		if n, ok := res.(int64); ok {
			return n, nil
		}
		return 0, nil
	case "EXISTS":
		return 0, nil
	default:
		return redisAffects(res), nil
	}
}

func redisBlockedWhy(cmd string) string {
	switch cmd {
	case "FLUSHDB", "FLUSHALL":
		return "redis: 本产品不提供清库命令(没有回滚可言, 且会影响整库其它键)"
	case "CONFIG", "DEBUG", "SHUTDOWN", "SAVE", "BGSAVE", "CLUSTER", "SCRIPT", "FUNCTION", "MIGRATE", "RESTORE", "SORT":
		return "redis: " + cmd + " 属于服务端管理命令, 不在本产品可发的命令里(要调服务端请在 Redis 自己的工具里做)"
	case "KEYS":
		return "redis: 不用 KEYS(服务端 O(N) 且阻塞)"
	}
	return ""
}

// ---------------------------------------------------------------- 小工具

func firstRedisWord(text string) string {
	m := redisWordsRE.FindStringSubmatch(text)
	if m == nil {
		return "?"
	}
	return m[1]
}

func redisIsReadArgv(args []string) bool {
	switch strings.ToUpper(args[0]) {
	case "GET", "MGET", "HGET", "HMGET", "HGETALL", "HKEYS", "HVALS", "HLEN", "HEXISTS",
		"KEYS", "SCAN", "TYPE", "TTL", "PTTL", "EXISTS", "DBSIZE", "INFO", "LRANGE", "LLEN",
		"SMEMBERS", "SCARD", "ZRANGE", "ZRANGEWITHSCORES", "ZCARD", "XLEN", "XRANGE", "MEMORY",
		"OBJECT", "SELENCT", "SELECT", "STRLEN", "GETRANGE", "LINDEX", "SISMEMBER", "ZSCORE", "HSCAN", "SSCAN", "ZSCAN":
		return true
	}
	return false
}

func redisArgs(args []string) []interface{} {
	out := make([]interface{}, len(args))
	for i := range args {
		out[i] = args[i]
	}
	return out
}

// redisSplitArgs 按空格切命令, 支持单/双引号包裹(键名里可能有空格)。
func redisSplitArgs(text string) []string {
	var out []string
	var cur strings.Builder
	var quote rune
	for _, r := range strings.TrimSpace(text) {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote = r
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

func redisParseDB(name string) (int, error) {
	n := strings.TrimSpace(name)
	if n == "" {
		return 0, nil
	}
	if strings.HasPrefix(strings.ToLower(n), "db") {
		n = n[2:]
	}
	i, err := strconv.Atoi(n)
	if err != nil || i < 0 || i > 1023 {
		return 0, fmt.Errorf("redis: 非法库名 %q(应为 db0..dbN)", name)
	}
	return i, nil
}

func redisPickDB(config connection.ConnectionConfig) int {
	if strings.HasPrefix(strings.TrimSpace(config.Database), "db") {
		if n, err := redisParseDB(config.Database); err == nil {
			return n
		}
	}
	if config.RedisDB > 0 {
		return config.RedisDB
	}
	return 0
}

func redisDialTimeout(config connection.ConnectionConfig) time.Duration {
	if config.Timeout > 0 {
		return time.Duration(config.Timeout) * time.Second
	}
	return 15 * time.Second
}

func redisQueryTimeout(config connection.ConnectionConfig) time.Duration {
	if config.QueryTimeout > 0 {
		return time.Duration(config.QueryTimeout) * time.Second
	}
	return 30 * time.Second
}

func redisTTLText(ttl time.Duration) string {
	switch {
	case ttl < 0:
		return "无过期"
	default:
		return strconv.FormatFloat(ttl.Seconds(), 'f', -1, 64) + "s"
	}
}

// redisInlineValue: 能当文本就文本, 否则 base64 并说明 —— 不把二进制硬塞进界面。
func redisInlineValue(raw string) interface{} {
	if raw == "" {
		return ""
	}
	if len(raw) > redisMaxInlineValueSize {
		raw = raw[:redisMaxInlineValueSize] + "…(截断)"
	}
	if !redisLooksTextual(raw) {
		return map[string]interface{}{"encoding": "base64", "size": len(raw),
			"body": base64.StdEncoding.EncodeToString([]byte(raw))}
	}
	return raw
}

// redisLooksTextual: 有控制字符(制表/换行之外)或非 UTF-8 就不当文本 —— 二进制值原样进界面
// 会显示成乱码, 用户分不清是数据坏了还是本来就这样。
func redisLooksTextual(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r < 0x20 && r != 9 && r != 10 && r != 13 { // 制表/换行/回车之外不当作文本
			return false
		}
		if r == 0xFFFD {
			return false
		}
	}
	return true
}

func redisMapOf(v map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(v))
	for k, x := range v {
		out[k] = redisInlineValue(fmt.Sprint(x))
	}
	return out
}

func redisSizeOf(ctx context.Context, c redis.UniversalClient, typ string, key string) int64 {
	switch typ {
	case "string":
		if n, err := c.StrLen(ctx, key).Result(); err == nil {
			return int64(n)
		}
	case "hash":
		if n, err := c.HLen(ctx, key).Result(); err == nil {
			return n
		}
	case "list":
		if n, err := c.LLen(ctx, key).Result(); err == nil {
			return n
		}
	case "set":
		if n, err := c.SCard(ctx, key).Result(); err == nil {
			return n
		}
	case "zset":
		if n, err := c.ZCard(ctx, key).Result(); err == nil {
			return n
		}
	case "stream":
		if n, err := c.XLen(ctx, key).Result(); err == nil {
			return n
		}
	}
	return 0
}

func redisColumnsFor(typ string) []connection.ColumnDefinition {
	col := func(name, ctype string) connection.ColumnDefinition {
		return connection.ColumnDefinition{Name: name, Type: ctype}
	}
	switch typ {
	case "hash":
		return []connection.ColumnDefinition{col("field", "string"), col("value", "string")}
	case "list":
		return []connection.ColumnDefinition{col("index", "bigint"), col("value", "string")}
	case "set":
		return []connection.ColumnDefinition{col("value", "string")}
	case "zset":
		return []connection.ColumnDefinition{col("member", "string"), col("score", "double")}
	case "stream":
		return []connection.ColumnDefinition{col("id", "string"), col("fields", "json")}
	default:
		return []connection.ColumnDefinition{col("key", "string"), col("type", "string"),
			col("ttl", "string"), col("value", "string")}
	}
}

// redisRowsOf 把 Do() 的多态返回值变成行。INFO 是文本协议单独处理。
func redisRowsOf(cmd string, res interface{}) ([]map[string]interface{}, []string, error) {
	name := strings.ToUpper(cmd)
	if name == "INFO" {
		text, _ := res.(string)
		rows := []map[string]interface{}{}
		for _, line := range strings.Split(text, "\n") {
			line = strings.TrimRight(line, "\r")
			if line == "" || strings.HasPrefix(line, "#") {
				if strings.HasPrefix(line, "#") {
					rows = append(rows, map[string]interface{}{"section": strings.TrimSpace(line[1:])})
				}
				continue
			}
			k, v, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			rows = append(rows, map[string]interface{}{k: redisInlineValue(v)})
		}
		return rows, []string{"section"}, nil
	}
	if name == "SCAN" {
		arr, ok := res.([]interface{})
		if !ok || len(arr) != 2 {
			return nil, nil, fmt.Errorf("redis: SCAN 返回结构不符合预期")
		}
		next, _ := arr[0].(string)
		keys := []string{}
		if list, ok := arr[1].([]interface{}); ok {
			for _, k := range list {
				keys = append(keys, fmt.Sprint(k))
			}
		}
		rows := make([]map[string]interface{}, 0, len(keys))
		for _, k := range keys {
			rows = append(rows, map[string]interface{}{"cursor": next, "key": k})
		}
		return rows, []string{"cursor", "key"}, nil
	}
	if redisSingleNums[name] {
		return []map[string]interface{}{{strings.ToLower(name): redisInlineValue(fmt.Sprint(res))}},
			[]string{strings.ToLower(name)}, nil
	}
	list, ok := res.([]interface{})
	if !ok {
		return []map[string]interface{}{{"result": redisInlineValue(fmt.Sprint(res))}}, []string{"result"}, nil
	}
	rows := make([]map[string]interface{}, 0, len(list))
	switch name {
	case "HGETALL":
		for i := 0; i+1 < len(list); i += 2 {
			rows = append(rows, map[string]interface{}{"field": fmt.Sprint(list[i]),
				"value": redisInlineValue(fmt.Sprint(list[i+1]))})
		}
		return rows, []string{"field", "value"}, nil
	case "KEYS", "SMEMBERS", "LRANGE", "ZRANGE", "MGET":
		for i, v := range list {
			if i >= redisMaxValueItems {
				break
			}
			rows = append(rows, map[string]interface{}{"index": i, "value": redisInlineValue(fmt.Sprint(v))})
		}
		return rows, []string{"index", "value"}, nil
	}
	for _, v := range list {
		rows = append(rows, map[string]interface{}{"value": redisInlineValue(fmt.Sprint(v))})
	}
	return rows, []string{"value"}, nil
}

func redisAffects(res interface{}) int64 {
	switch v := res.(type) {
	case int64:
		if v > 1 {
			return 1
		}
		return v
	case string:
		if v == "OK" {
			return 1
		}
	}
	return 1
}

func redisQuoteKey(k string) string { return `"` + strings.ReplaceAll(k, `"`, `\"`) + `"` }
