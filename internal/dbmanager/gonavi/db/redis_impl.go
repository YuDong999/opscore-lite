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

const (
	redisDefaultScanCount   = 500
	redisMaxKeysListed      = 2000 // 列键上限: 超了就标 partial, 不把大库整个拖进界面
	redisMaxValueItems      = 300  // 集合类一次最多渲染多少条
	redisMaxInlineValueSize = 256 << 10
)

// RedisDB 是引擎对外的句柄; 每个 db index 建一个 client(见文件头取舍 1)。
type RedisDB struct {
	clientMu sync.Mutex
	clients  map[int]*redis.Client
	opts     redis.Options
	dbIndex  int
	timeout  time.Duration
	queryTO  time.Duration
}

func newRedisDB() *RedisDB {
	return &RedisDB{clients: map[int]*redis.Client{}, dbIndex: 0}
}

func (e *RedisDB) ctx() (context.Context, context.CancelFunc) {
	if e.queryTO > 0 {
		return context.WithTimeout(context.Background(), e.queryTO)
	}
	return context.WithTimeout(context.Background(), 30*time.Second)
}

func (e *RedisDB) client(db int) (*redis.Client, error) {
	e.clientMu.Lock()
	defer e.clientMu.Unlock()
	if c, ok := e.clients[db]; ok {
		return c, nil
	}
	if e.clients == nil {
		// 工厂是按 &RedisDB{} 裸建的, 没走构造函数 —— 这里兜住, 否则写 nil map 直接 panic
		e.clients = map[int]*redis.Client{}
	}
	opt := e.opts
	opt.DB = db
	c := redis.NewClient(&opt)
	e.clients[db] = c
	return c, nil
}

// Connect 建立连接(只验证 db 0 可用, 其余库按需建)。
func (e *RedisDB) Connect(config connection.ConnectionConfig) error {
	host := strings.TrimSpace(config.Host)
	if host == "" {
		return errors.New("redis: 缺少地址")
	}
	port := config.Port
	if port <= 0 {
		port = 6379
	}
	e.timeout = redisDialTimeout(config)
	e.queryTO = redisQueryTimeout(config)
	e.opts = redis.Options{
		Addr:     net.JoinHostPort(host, strconv.Itoa(port)),
		Username: strings.TrimSpace(config.User),
		Password: config.Password,
	}
	if config.UseSSL || strings.EqualFold(config.SSLMode, "skip-verify") {
		e.opts.TLSConfig = &tls.Config{InsecureSkipVerify: strings.EqualFold(config.SSLMode, "skip-verify")}
	}
	e.dbIndex = redisPickDB(config)
	if e.dbIndex < 0 {
		return fmt.Errorf("redis: 非法的库号 %d", e.dbIndex)
	}
	c, err := e.client(e.dbIndex)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), e.timeout+5*time.Second)
	defer cancel()
	if err := c.Ping(ctx).Err(); err != nil {
		_ = c.Close()
		e.clients = map[int]*redis.Client{}
		return fmt.Errorf("redis: 连接失败: %w", err)
	}
	return nil
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
				rows = append(rows, map[string]interface{}{"id": xs[i].Values["__id__"],
					"fields": redisMapOf(xs[i].Values)})
			}
		}
		return rows, redisColumnNames(redisColumnsFor(typ)), total, nil
	}
	return nil, nil, 0, fmt.Errorf("redis: 暂不支持在网格里展示 %s 类型的值(可以用 TYPE/MEMORY USAGE 看元信息)", typ)
}

// redisColumnNames 取类型对应的列名(空结果也要给列, 否则网格画不出表头)。
func redisColumnNames(defs []connection.ColumnDefinition) []string {
	names := make([]string, 0, len(defs))
	for _, c := range defs {
		names = append(names, c.Name)
	}
	return names
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

func redisSizeOf(ctx context.Context, c *redis.Client, typ string, key string) int64 {
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
