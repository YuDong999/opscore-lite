package logmonitor

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"
)

// Store 元数据索引存储（SQLite）
type Store struct {
	db       *sql.DB
	dbPath   string
	mu       sync.RWMutex
	agg      *aggCache // 聚合结果缓存(Stats/Histogram/Terms), 3s 轮询直接命中
	shardCfg ShardConfig
	// 分钟级物化桶表状态(直方图/统计卡片查询走它, 与数据量解耦)
	histReady atomic.Bool
	// 存量混合片是否已拆入索引分片 (迁移期间查询附带旧表)
	migrated atomic.Bool
}

// aggCache 简单的 TTL 缓存, 泛型 getOrBuild(带单飞去重), 供高频聚合查询使用。
type aggCache struct {
	mu       sync.Mutex
	items    map[string]aggCacheItem
	inflight map[string]bool // 单飞: 同 key 同时只允许一个 builder
}

type aggCacheItem struct {
	val     interface{}
	expires int64
}

func newAggCache() *aggCache {
	return &aggCache{items: map[string]aggCacheItem{}, inflight: map[string]bool{}}
}

const aggCacheTTL = 10 * time.Second

// aggGetOrBuild 泛型只读缓存: 命中直接返回, 未命中构建并缓存。
// 单飞: 同一 key 并发请求时, 只有一个 goroutine 实际构建, 其余等待其结果。
func aggGetOrBuild[T any](c *aggCache, key string, ttl time.Duration, build func() (T, error)) (T, error) {
	now := time.Now().UnixMilli()
	c.mu.Lock()
	if it, ok := c.items[key]; ok && it.expires > now {
		v := it.val.(T)
		c.mu.Unlock()
		return v, nil
	}
	if c.inflight[key] {
		// 已有其他请求在构建: 短暂自旋等待(聚合通常 <1s, 避免 3s 轮询摧毁缓存价值)
		c.mu.Unlock()
		for i := 0; i < 50; i++ {
			time.Sleep(20 * time.Millisecond)
			now2 := time.Now().UnixMilli()
			c.mu.Lock()
			it, ok := c.items[key]
			if ok && it.expires > now2 {
				v := it.val.(T)
				c.mu.Unlock()
				return v, nil
			}
			c.mu.Unlock()
		}
		c.mu.Lock()
	}
	c.inflight[key] = true
	c.mu.Unlock()

	v, err := build()
	c.mu.Lock()
	delete(c.inflight, key)
	if err == nil {
		c.items[key] = aggCacheItem{val: v, expires: time.Now().UnixMilli() + ttl.Milliseconds()}
		if len(c.items) > 256 {
			for k, it := range c.items {
				if it.expires <= now {
					delete(c.items, k)
				}
			}
		}
	}
	c.mu.Unlock()
	return v, err
}

// clear 数据写入后调用, 避免读到过期聚合
func (c *aggCache) clear() {
	c.mu.Lock()
	c.items = map[string]aggCacheItem{}
	c.mu.Unlock()
}

func NewStore(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_cache_size=-64000&_pragma=busy_timeout(30000)")
	if err != nil {
		return nil, fmt.Errorf("open logmeta db: %w", err)
	}
	db.SetMaxOpenConns(8) // WAL 下读并发安全; 写仍由 s.mu 串行, 单连接会令所有读在聚合时排队

	s := &Store{db: db, dbPath: dbPath, agg: newAggCache()}
	if err := s.Check(); err != nil {
		// 库损坏且 salvage 失败：拒绝启动，避免带病写入扩大损坏
		db.Close()
		return nil, err
	}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	if err := s.shardsInit(); err != nil {
		db.Close()
		return nil, err
	}
	go s.EnsureHistBackfill() // 后台回填存量分钟桶(不阻塞启动)
	return s, nil
}

// shardsInit 读分片配置 + 建路由/物化表 + 老库兼容
func (s *Store) shardsInit() error {
	s.shardCfg = loadShardConfig(filepath.Join(filepath.Dir(s.dbPath), "shards.json"))
	// 预热 索引id→显示名 映射, 保证写入/迁移全程用可读 slug(而非 hash)
	if idxs, err := s.ListIndexes(); err == nil {
		_ = idxs
	}
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS log_shards (
		shard    TEXT PRIMARY KEY,
		index_id TEXT NOT NULL DEFAULT '',
		start_ts INTEGER NOT NULL DEFAULT 0,
		end_ts   INTEGER NOT NULL DEFAULT 0
	)`); err != nil {
		return fmt.Errorf("create log_shards: %w", err)
	}
	if err := s.ensureColumn("log_shards", "index_id", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	// 物化表结构升级: 旧结构(无 shard 列) → DROP, 由下方 CREATE 一次性重建(数据由启动回填重灌)
	// 注意: 用 PRAGMA table_info 直接查询(参数化 pragma 在 modernc 下不可靠)
	for _, tbl := range []string{"log_meta_minute", "log_meta_minute_svc"} {
		rows, err := s.db.Query("PRAGMA table_info(" + tbl + ")")
		if err != nil {
			continue // 表不存在: 由下方 CREATE IF NOT EXISTS 建表
		}
		hasShard := false
		found := false
		for rows.Next() {
			var cid int
			var name, ctype string
			var notnull, pk int
			var dflt interface{}
			rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk)
			found = true
			if name == "shard" {
				hasShard = true
			}
		}
		rows.Close()
		if !found {
			continue // 表不存在(PRAGMA 对缺失表返回空集)
		}
		if !hasShard {
			if _, err := s.db.Exec("DROP TABLE IF EXISTS " + tbl); err != nil {
				return err
			}
			log.Printf("[logmonitor] 物化表 %s 结构升级: 重建并按片重灌", tbl)
		}
	}
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS log_meta_minute (
		minute INTEGER NOT NULL,
		level  TEXT    NOT NULL DEFAULT 'INFO',
		shard  TEXT    NOT NULL DEFAULT '',
		cnt    INTEGER NOT NULL DEFAULT 0,
		bytes  INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (minute, level, shard)
	);
	CREATE INDEX IF NOT EXISTS idx_minute_ts ON log_meta_minute(minute);
	CREATE TABLE IF NOT EXISTS log_meta_minute_svc (
		minute  INTEGER NOT NULL,
		service TEXT    NOT NULL DEFAULT '',
		shard   TEXT    NOT NULL DEFAULT '',
		cnt     INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (minute, service, shard)
	);
	CREATE INDEX IF NOT EXISTS idx_minute_svc_ts ON log_meta_minute_svc(minute);`); err != nil {
		return fmt.Errorf("migrate shard tables: %w", err)
	}
	if err := s.ensureColumn("log_meta_minute", "bytes", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	go s.EnsureIndexShardMigration()
	// 重建后的空物化表由后台回填; histsReady 会在回填完成后置位
	return nil
}

// EnsureIndexShardMigration 存量混合片(log_meta + 旧纯时间片)后台拆入 (索引,期) 新片, 完成后关掉旧表兼容。
func (s *Store) EnsureIndexShardMigration() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[logmonitor] 分片迁移 panic: %v", r)
		}
	}()

	// 已见新格式记录(任意 index_id 非空片)则跳过
	var n int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM log_shards WHERE index_id != ''").Scan(&n); err == nil && n > 0 {
		// 幂等: 主机已在跑新格式, 但旧片可能仍残留 → 检查旧表
	}
	legacy := []string{"log_meta"}
	rows, err := s.db.Query("SELECT shard FROM log_shards WHERE index_id = '' AND shard NOT LIKE '%\\_%' ESCAPE '\\'")
	if err == nil {
		for rows.Next() {
			var k string
			rows.Scan(&k)
			legacy = append(legacy, shardTableName(k))
		}
		rows.Close()
	}
	// 孤儿表兜底: 存在于磁盘但已无路由记录的 log_meta_* 表(迁移中断残留) 也纳入拆片
	orphanRows, err := s.db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name LIKE 'log\_meta\_%' ESCAPE '\'
		AND name NOT LIKE 'log\_meta\_minute%' ESCAPE '\'
		AND name NOT IN (SELECT 'log_meta_' || shard FROM log_shards)
		AND name NOT LIKE 'log\_meta\_idx\_%' AND name NOT LIKE '%\_\_10e2b3'`)
	if err == nil {
		for orphanRows.Next() {
			var nm string
			orphanRows.Scan(&nm)
			legacy = append(legacy, nm)
		}
		orphanRows.Close()
	}
	moved := int64(0)
	for _, tbl := range legacy {
		var cnt int64
		if err := s.db.QueryRow("SELECT COUNT(*) FROM " + tbl).Scan(&cnt); err != nil || cnt == 0 {
			continue
		}
		// 按 (index_id) 分组搬移
		idxRows, err := s.db.Query("SELECT DISTINCT index_id FROM " + tbl)
		if err != nil {
			continue
		}
		var ids []string
		for idxRows.Next() {
			var id string
			idxRows.Scan(&id)
			ids = append(ids, id)
		}
		idxRows.Close()

		var mn, mx int64
		s.db.QueryRow("SELECT MIN(ts), MAX(ts) FROM " + tbl).Scan(&mn, &mx)
		for _, id := range ids {
			slug := indexSlug(id)
			period := shardKeyOf(mn, s.shardCfg.ShardBy)
			if mx > 0 {
				period = shardKeyOf((mn+mx)/2, s.shardCfg.ShardBy)
			}
			key := slug + "_" + period
			name := shardTableName(key)
			var exists int
			s.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", name).Scan(&exists)
			if exists == 0 {
				if err := s.ensureShardTable(id, key); err != nil {
					log.Printf("[logmonitor] 迁移建片失败 %s: %v", key, err)
					continue
				}
			} else if id != "" {
				// 目标片已存在(可能迁过一轮): 用 INSERT OR IGNORE 吸取剩余
			}
			if _, err := s.db.Exec(`INSERT OR IGNORE INTO `+name+` (ts, level, service, source, file_path, offset, size, summary, index_id)
				SELECT ts, level, service, source, file_path, offset, size, summary, index_id FROM `+tbl+` WHERE index_id = ?`, id); err != nil {
				log.Printf("[logmonitor] 迁移搬移失败 %s→%s: %v", tbl, key, err)
				continue
			}
			// 幂等去重: 重复搬移或 id 冲突导致的重复行清理(GROUP BY 百万行级, 一次性)
			if _, err := s.db.Exec(`DELETE FROM `+name+` WHERE rowid NOT IN (
				SELECT MIN(rowid) FROM `+name+` GROUP BY ts, offset, source, file_path)`); err != nil {
				log.Printf("[logmonitor] 迁移去重失败(%s): %v", name, err)
			}
			moved++
			// 记录/更新路由并刷新边界
			s.db.Exec("INSERT OR REPLACE INTO log_shards (shard, index_id, start_ts, end_ts) VALUES (?, ?, ?, ?) ",
				key, id,
				func() int64 {
					if id == "" {
						return 0
					}
					var m0, m1 int64
					s.db.QueryRow("SELECT MIN(ts), MAX(ts) FROM "+name).Scan(&m0, &m1)
					return m0
				}(),
				func() int64 {
					var m1 int64
					s.db.QueryRow("SELECT COALESCE(MAX(ts),0) FROM "+name).Scan(&m1)
					return m1
				}())
			s.updateShardBounds(key)
		}
		// 旧表拆空后删除
		var remain int64
		s.db.QueryRow("SELECT COUNT(*) FROM " + tbl).Scan(&remain)
		if remain == 0 {
			if tbl != "log_meta" {
				s.db.Exec("DROP TABLE " + tbl)
			}
		}
	}
	// 旧纯时间片记录清理
	s.db.Exec("DELETE FROM log_shards WHERE index_id = '' AND shard NOT LIKE '%\\_%' ESCAPE '\\'")
	// 阶段2: hash slug 片 → 可读名字 slug 片(幂等改名; 目标已存在则跳过, 查询路由两者都会返回)
	hashRows, err := s.db.Query("SELECT shard, index_id FROM log_shards WHERE shard LIKE 'idx\\_%' ESCAPE '\\'")
	if err == nil {
		var renames []struct{ old, new string }
		for hashRows.Next() {
			var k, idxID string
			hashRows.Scan(&k, &idxID)
			sep := strings.LastIndexByte(k, '_')
			if sep < 0 {
				continue
			}
			want := indexSlug(idxID) + k[sep:]
			if want != k {
				renames = append(renames, struct{ old, new string }{k, want})
			}
		}
		hashRows.Close()
		for _, r := range renames {
			var exists int
			s.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", shardTableName(r.new)).Scan(&exists)
			if exists > 0 {
				continue // 名字片已存在, 保留两片(路由都覆盖), 不冒险合并
			}
			if _, err := s.db.Exec("ALTER TABLE " + shardTableName(r.old) + " RENAME TO " + shardTableName(r.new)); err == nil {
				s.db.Exec("UPDATE log_shards SET shard=? WHERE shard=?", r.new, r.old)
			}
		}
		if len(renames) > 0 {
			log.Printf("[logmonitor] 分片 hash→名字 slug 改名 %d 个", len(renames))
		}
	}
	if moved > 0 {
		log.Printf("[logmonitor] 分片迁移完成: %d 组索引数据已拆入新片", moved)
	}
	// 孤儿/新拆片补齐物化(INSERT OR IGNORE 幂等)
	if moved > 0 || !s.histReady.Load() {
		s.EnsureHistBackfill()
	}
	s.migrated.Store(true)
}

// Check 启动自检：PRAGMA integrity_check。损坏时尝试 salvage 重建（搬运转可读行到新库）
// 并替换原库；已自动恢复返回 nil，salvage 也失败才报错（拒绝启动, 避免带病写入）。
func (s *Store) Check() error {
	result := "ok"
	if err := s.db.QueryRow("PRAGMA integrity_check").Scan(&result); err != nil {
		return s.salvage()
	}
	if result == "ok" {
		return nil
	}
	log.Printf("[logmonitor] 数据库完整性异常: %s, 尝试自动恢复...", result)
	return s.salvage()
}

// sqliteWalDSN 拼接 DSN：老库自动补列依赖写路径, 新库/替换库统一走 WAL。
const writeDSN = "_journal_mode=WAL&_cache_size=-64000"

// salvage 损坏库救援：坏库原文件改为 .corrupt-<ts> 备份, 可读行逐表搬进新库, 原子换回原路径。
// 复刻 2026-09-09 人工恢复流程（逐行读取, 坏行跳过）。
func (s *Store) salvage() error {
	s.db.Close()

	badPath := s.dbPath + ".corrupt-" + time.Now().Format("20060102-150405")
	if err := os.Rename(s.dbPath, badPath); err != nil {
		return fmt.Errorf("salvage: 备份损坏库: %w", err)
	}

	src, err := sql.Open("sqlite", badPath+"?mode=ro")
	if err != nil {
		return fmt.Errorf("salvage: 打开损坏库: %w", err)
	}
	defer src.Close()

	if _, err := src.Exec("PRAGMA busy_timeout=5000"); err != nil {
		return fmt.Errorf("salvage: priset busy: %w", err)
	}

	var tables []string
	rows, err := src.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return fmt.Errorf("salvage: 枚举表: %w", err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return fmt.Errorf("salvage: 读表名: %w", err)
		}
		tables = append(tables, name)
	}
	rows.Close()

	dst, err := sql.Open("sqlite", s.dbPath+"?"+writeDSN)
	if err != nil {
		return fmt.Errorf("salvage: 打开恢复库: %w", err)
	}

	recovered := false
	for _, t := range tables {
		n, err := copyTable(src, dst, t)
		if err != nil {
			log.Printf("[logmonitor] salvage 表 %s 恢复失败: %v (可后续手工处理 %s)", t, err, badPath)
			continue
		}
		log.Printf("[logmonitor] salvage 表 %s: 搬回 %d 行", t, n)
		recovered = true
	}
	if !recovered {
		dst.Close()
		// 一无所获: 原路径恢复为损坏库的备份, 交给人工
		_ = os.Rename(badPath, s.dbPath)
		return fmt.Errorf("salvage: 未恢复任何数据, 请人工处理 %s", badPath)
	}
	// 恢复库就绪, 换上正式连接供后续 migrate 使用
	dst.Close()
	db, err := sql.Open("sqlite", s.dbPath+"?"+writeDSN)
	if err != nil {
		return fmt.Errorf("salvage: 重开恢复库: %w", err)
	}
	db.SetMaxOpenConns(1)
	s.db = db
	log.Printf("[logmonitor] 已自动恢复数据库, 损坏库备份于 %s", badPath)
	return nil
}

// copyTable 从损坏库搬一张表：复制原 DDL 建表后逐行搬, 坏行跳过。
// 先用定界查询拿列数拼 INSERT 占位符, 再全表搬。返回搬回行数。
func copyTable(src, dst *sql.DB, table string) (int, error) {
	var ddl string
	if err := src.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&ddl); err != nil {
		return 0, fmt.Errorf("读表 %s 结构: %w", table, err)
	}
	if _, err := dst.Exec(ddl); err != nil {
		return 0, fmt.Errorf("建表 %s: %w", table, err)
	}

	probe, err := src.Query("SELECT * FROM " + table)
	if err != nil {
		return 0, fmt.Errorf("读表 %s 列: %w", table, err)
	}
	cols, err := probe.Columns()
	probe.Close()
	if err != nil {
		return 0, fmt.Errorf("读表 %s 列名: %w", table, err)
	}
	holders := make([]string, len(cols))
	for i := range holders {
		holders[i] = "?"
	}
	stmt, err := dst.Prepare(fmt.Sprintf("INSERT INTO %q VALUES (%s)", table, strings.Join(holders, ",")))
	if err != nil {
		return 0, fmt.Errorf("prepare %s: %w", table, err)
	}
	defer stmt.Close()

	rows, err := src.Query("SELECT * FROM " + table)
	if err != nil {
		return 0, nil // 表不可读: 骨架表保底, 不视为致命
	}
	defer rows.Close()
	scanVals := make([]interface{}, len(cols))
	ptrs := make([]interface{}, len(cols))
	for i := range ptrs {
		ptrs[i] = &scanVals[i]
	}
	ok := 0
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			continue
		}
		out := make([]interface{}, len(cols))
		copy(out, scanVals)
		if _, err := stmt.Exec(out...); err != nil {
			continue
		}
		ok++
	}
	return ok, nil
}

func (s *Store) migrate() error {
	ddl := `
	CREATE TABLE IF NOT EXISTS log_meta (
		id        INTEGER PRIMARY KEY AUTOINCREMENT,
		ts        INTEGER NOT NULL,
		level     TEXT    NOT NULL DEFAULT 'INFO',
		service   TEXT    NOT NULL DEFAULT '',
		source    TEXT    NOT NULL DEFAULT '',
		file_path TEXT    NOT NULL,
		offset    INTEGER NOT NULL DEFAULT 0,
		size      INTEGER NOT NULL DEFAULT 0,
		summary   TEXT    NOT NULL DEFAULT '',
		index_id  TEXT    NOT NULL DEFAULT ''
	);
	CREATE INDEX IF NOT EXISTS idx_meta_ts       ON log_meta(ts);
	CREATE INDEX IF NOT EXISTS idx_meta_svc      ON log_meta(service);
	CREATE INDEX IF NOT EXISTS idx_meta_lvl      ON log_meta(level);
	CREATE INDEX IF NOT EXISTS idx_meta_src      ON log_meta(source);
	CREATE INDEX IF NOT EXISTS idx_meta_svc_lvl  ON log_meta(service, level, ts);
	CREATE INDEX IF NOT EXISTS idx_meta_svc_ts   ON log_meta(service, ts);

	CREATE TABLE IF NOT EXISTS log_sources (
		id      TEXT PRIMARY KEY,
		name    TEXT NOT NULL,
		type    TEXT NOT NULL DEFAULT 'file',
		path    TEXT NOT NULL DEFAULT '',
		service TEXT NOT NULL DEFAULT '',
		enabled INTEGER NOT NULL DEFAULT 1,
		follow  INTEGER NOT NULL DEFAULT 0
	);

	CREATE TABLE IF NOT EXISTS log_indexes (
		id          TEXT PRIMARY KEY,
		name        TEXT NOT NULL,
		source      TEXT NOT NULL DEFAULT '',
		source_path TEXT NOT NULL DEFAULT '',
		service     TEXT NOT NULL DEFAULT '',
		fields      TEXT NOT NULL DEFAULT '[]',
		ilm         TEXT NOT NULL DEFAULT '{}',
		delete_after INTEGER NOT NULL DEFAULT 0,
		created_at   INTEGER NOT NULL DEFAULT 0,
		updated_at   INTEGER NOT NULL DEFAULT 0
	);

	CREATE TABLE IF NOT EXISTS alert_rules (
		id          TEXT PRIMARY KEY,
		name        TEXT NOT NULL,
		enabled     INTEGER NOT NULL DEFAULT 1,
		condition   TEXT NOT NULL DEFAULT '',
		count_thresh INTEGER NOT NULL DEFAULT 0,
		window_ms   INTEGER NOT NULL DEFAULT 60000,
		cooldown_ms INTEGER NOT NULL DEFAULT 300000,
		channels    TEXT NOT NULL DEFAULT '[]',
		created_at  INTEGER NOT NULL DEFAULT 0,
		updated_at  INTEGER NOT NULL DEFAULT 0,
		state       TEXT NOT NULL DEFAULT 'ok',
		last_fired  INTEGER NOT NULL DEFAULT 0
	);

	CREATE TABLE IF NOT EXISTS alert_channels (
		id         TEXT PRIMARY KEY,
		name       TEXT NOT NULL,
		type       TEXT NOT NULL DEFAULT 'webhook',
		url        TEXT NOT NULL DEFAULT '',
		method     TEXT NOT NULL DEFAULT 'POST',
		headers    TEXT NOT NULL DEFAULT '{}',
		enabled    INTEGER NOT NULL DEFAULT 1,
		created_at INTEGER NOT NULL DEFAULT 0
	);

	CREATE TABLE IF NOT EXISTS alert_events (
		id          TEXT PRIMARY KEY,
		rule_id     TEXT NOT NULL DEFAULT '',
		rule_name   TEXT NOT NULL DEFAULT '',
		level       TEXT NOT NULL DEFAULT '',
		service     TEXT NOT NULL DEFAULT '',
		count       INTEGER NOT NULL DEFAULT 0,
		fired_at    INTEGER NOT NULL DEFAULT 0,
		resolved_at INTEGER NOT NULL DEFAULT 0,
		status      TEXT NOT NULL DEFAULT 'firing'
	);
	CREATE INDEX IF NOT EXISTS idx_alert_events_fired ON alert_events(fired_at DESC);
	CREATE INDEX IF NOT EXISTS idx_alert_events_rule  ON alert_events(rule_id);
	`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("migrate log_meta: %w", err)
	}

	// 老库兼容：确保 log_meta.index_id 列存在（SQLite ALTER ADD COLUMN）
	// 必须在建 idx_meta_index_id 索引之前, 否则老库会因列不存在而建索引失败
	if err := s.ensureColumn("log_meta", "index_id", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if _, err := s.db.Exec("CREATE INDEX IF NOT EXISTS idx_meta_index_id ON log_meta(index_id)"); err != nil {
		return fmt.Errorf("migrate index_id index: %w", err)
	}

	// log_sources 持续采集所需列（老库幂等补列）
	for _, col := range []struct{ name, typ string }{
		{"index_id", "TEXT NOT NULL DEFAULT ''"},
		{"namespace", "TEXT NOT NULL DEFAULT ''"},
		{"cluster", "TEXT NOT NULL DEFAULT ''"},
		{"last_ts", "INTEGER NOT NULL DEFAULT 0"},
	} {
		if err := s.ensureColumn("log_sources", col.name, col.typ); err != nil {
			return err
		}
	}

	// 自动创建数据目录
	log.Println("[logmonitor] store migrated OK")
	return nil
}

// ensureColumn 为已存在的表安全添加列（幂等）
func (s *Store) ensureColumn(table, col, typ string) error {
	rows, err := s.db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notnull, pk int
		var name, ctype string
		var dflt interface{}
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return err
		}
		if name == col {
			return nil // 已存在
		}
	}
	_, err = s.db.Exec("ALTER TABLE " + table + " ADD COLUMN " + col + " " + typ)
	return err
}

// InsertBatch 批量写入元数据索引
func (s *Store) InsertBatch(entries []*LogEntry) ([]int64, error) {
	if len(entries) == 0 {
		return []int64{}, nil
	}
	// 不主动清聚合缓存: TTL(10s) 自然过期即携带新数据, 避免 poller 每次写入令统计页全量重算
	s.mu.Lock()
	defer s.mu.Unlock()

	// 双层分片: (索引, 期) 落片
	type shardBatch struct {
		key, indexID string
		entries      []*LogEntry
	}
	groups := map[string]*shardBatch{}
	for _, e := range entries {
		key := s.shardKeyFor(e.IndexID, e.Ts)
		if g, ok := groups[key]; ok {
			g.entries = append(g.entries, e)
		} else {
			groups[key] = &shardBatch{key: key, indexID: e.IndexID, entries: []*LogEntry{e}}
		}
	}

	ids := make([]int64, 0, len(entries))
	for _, g := range groups {
		if err := s.ensureShardTable(g.indexID, g.key); err != nil {
			return nil, err
		}
		tx, err := s.db.Begin()
		if err != nil {
			return nil, err
		}
		stmt, err := tx.Prepare(`INSERT INTO ` + shardTableName(g.key) + ` (ts, level, service, source, file_path, offset, size, summary, index_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
		if err != nil {
			tx.Rollback()
			return nil, err
		}
		for _, e := range g.entries {
			res, err := stmt.Exec(e.Ts, e.Level, e.Service, e.Source, e.FilePath, e.Offset, e.Size, e.Summary, e.IndexID)
			if err != nil {
				stmt.Close()
				tx.Rollback()
				return nil, fmt.Errorf("insert %s: %w", g.key, err)
			}
			id, _ := res.LastInsertId()
			e.ID = id
			ids = append(ids, id)
		}
		stmt.Close()
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		s.updateShardBounds(g.key)
	}
	// 分钟物化桶(直方图/统计卡片加速): 失败仅记日志, 不阻断主写入; 回填可补齐
	if err := s.upsertMinute(entries); err != nil {
		log.Printf("[logmonitor] 物化分钟桶写入失败(可回填): %v", err)
	}
	return ids, nil
}

// upsertMinute 把批次日志增量累加进物化表(分钟,级别,字节,服务 × 片); 调用方需已持 s.mu 写锁
func (s *Store) upsertMinute(entries []*LogEntry) error {
	type acc struct {
		cnt, bytes int64
	}
	m := map[string]acc{}   // "minute|level|slug"
	svc := map[string]acc{} // "minute|service|slug"
	for _, e := range entries {
		slug := indexSlug(e.IndexID)
		min := e.Ts / 60000
		k := strconv.FormatInt(min, 10) + "|" + e.Level + "|" + slug
		v := m[k]
		m[k] = acc{v.cnt + 1, v.bytes + int64(e.Size)}
		k2 := strconv.FormatInt(min, 10) + "|" + e.Service + "|" + slug
		v2 := svc[k2]
		svc[k2] = acc{v2.cnt + 1, 0}
	}
	if len(m) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	commit := func() error {
		if err := tx.Commit(); err != nil {
			return err
		}
		s.histReady.CompareAndSwap(false, true)
		return nil
	}
	for k, v := range m {
		p := strings.SplitN(k, "|", 3)
		if len(p) != 3 {
			continue
		}
		if _, err := tx.Exec(`INSERT INTO log_meta_minute (minute, level, shard, cnt, bytes) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(minute, level, shard) DO UPDATE SET cnt = cnt + excluded.cnt, bytes = bytes + excluded.bytes`,
			p[0], p[1], p[2], v.cnt, v.bytes); err != nil {
			tx.Rollback()
			return err
		}
	}
	for k, v := range svc {
		p := strings.SplitN(k, "|", 3)
		if len(p) != 3 {
			continue
		}
		if _, err := tx.Exec(`INSERT INTO log_meta_minute_svc (minute, service, shard, cnt) VALUES (?, ?, ?, ?)
			ON CONFLICT(minute, service, shard) DO UPDATE SET cnt = cnt + excluded.cnt`,
			p[0], p[1], p[2], v.cnt); err != nil {
			tx.Rollback()
			return err
		}
	}
	return commit()
}

// EnsureHistBackfill 后台一次性回填物化表(存量, 幂等); 完成后 histReady=true
func (s *Store) EnsureHistBackfill() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[logmonitor] 分钟物化回填 panic: %v", r)
		}
	}()
	var n int64
	if err := s.db.QueryRow("SELECT COUNT(*) FROM log_meta_minute").Scan(&n); err == nil && n > 0 {
		s.histReady.Store(true) // 已有数据(重启场景): 先行启用, 回填继续补全
	}
	for _, t := range s.allDataTables() {
		if t == "log_meta" && s.migrated.Load() {
			continue
		}
		// 从表名推导片 slug(表名 log_meta_<slug>_<period>), 物化按片独立行 → 幂等且多源不互相吞
		slug := strings.TrimPrefix(t, "log_meta_")
		if i := strings.LastIndexByte(slug, '_'); i > 0 {
			slug = slug[:i]
		}
		if _, err := s.db.Exec(`INSERT OR IGNORE INTO log_meta_minute (minute, level, shard, cnt, bytes)
			SELECT ts/60000, level, ?, COUNT(*), COALESCE(SUM(size),0) FROM `+t+` GROUP BY 1, 2`, slug); err != nil {
			log.Printf("[logmonitor] 分钟物化回填失败(%s): %v", t, err)
		}
		if _, err := s.db.Exec(`INSERT OR IGNORE INTO log_meta_minute_svc (minute, service, shard, cnt)
			SELECT ts/60000, service, ?, COUNT(*) FROM `+t+` GROUP BY 1, 2`, slug); err != nil {
			log.Printf("[logmonitor] 服务物化回填失败(%s): %v", t, err)
		}
	}
	s.histReady.Store(true)
}
// inClause 把逗号分隔的多值拼成 IN (...), 单值退化为 =?
func inClause(col, csv string, args *[]interface{}) string {
	vals := strings.Split(csv, ",")
	if len(vals) == 1 {
		*args = append(*args, strings.TrimSpace(vals[0]))
		return col + " = ?"
	}
	phs := make([]string, len(vals))
	for i, v := range vals {
		phs[i] = "?"
		*args = append(*args, strings.TrimSpace(v))
	}
	return col + " IN (" + strings.Join(phs, ",") + ")"
}

func (s *Store) Query(q *LogQuery) (*LogQueryResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	start := time.Now()

	where := []string{}
	args := []interface{}{}

	if q.Service != "" {
		where = append(where, inClause("service", q.Service, &args))
	}
	if q.Level != "" {
		where = append(where, inClause("level", q.Level, &args))
	}
	if q.Source != "" {
		where = append(where, inClause("source", q.Source, &args))
	}
	if q.StartTs > 0 {
		where = append(where, "ts >= ?")
		args = append(args, q.StartTs)
	}
	if q.EndTs > 0 {
		where = append(where, "ts <= ?")
		args = append(args, q.EndTs)
	}
	if q.Keyword != "" {
		where = append(where, "summary LIKE ?")
		args = append(args, "%"+q.Keyword+"%")
	}
	if q.IndexID != "" {
		where = append(where, inClause("index_id", q.IndexID, &args))
	}

	whereClause := ""
	if len(where) > 0 {
		whereClause = "WHERE " + strings.Join(where, " AND ")
	}

	// 分页参数
	page := q.Page
	if page < 1 {
		page = 1
	}
	pageSize := q.PageSize
	if pageSize < 1 || pageSize > 500 {
		pageSize = 100
	}
	offset := int64((page - 1) * pageSize)
	need := offset + int64(pageSize)

	// 分片路由: 时间窗命中片集(历史片+热表)
	tables := s.tablesForRange(q.StartTs, q.EndTs, q.IndexID)
	var total int64
	var all []*LogEntry
	for _, t := range tables {
		var c int64
		if err := s.db.QueryRow("SELECT COUNT(*) FROM "+t+" "+whereClause, args...).Scan(&c); err != nil {
			return nil, err
		}
		total += c

		querySQL := `SELECT id, ts, level, service, source, file_path, offset, size, summary, index_id
			FROM ` + t + ` ` + whereClause + `
			ORDER BY ts DESC
			LIMIT ?`
		rows, err := s.db.Query(querySQL, append(append([]interface{}{}, args...), need)...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			e := &LogEntry{}
			if err := rows.Scan(&e.ID, &e.Ts, &e.Level, &e.Service, &e.Source, &e.FilePath, &e.Offset, &e.Size, &e.Summary, &e.IndexID); err != nil {
				rows.Close()
				return nil, err
			}
			all = append(all, e)
		}
		rows.Close()
	}

	// 跨片合并: 时间倒序(同 ts 时热表优先=id 反序兜底)
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Ts != all[j].Ts {
			return all[i].Ts > all[j].Ts
		}
		return all[i].ID > all[j].ID
	})
	items := all
	if int64(len(items)) > need {
		items = items[:need]
	}
	if int64(len(items)) > offset {
		items = items[offset:]
	} else {
		items = []*LogEntry{}
	}

	took := float64(time.Since(start).Microseconds()) / 1000.0
	return &LogQueryResult{Total: total, Items: items, TookMs: took}, nil
}

// Stats 获取统计信息
func (s *Store) Stats(q *LogStatsQuery) (*LogStats, error) {
	return aggGetOrBuild(s.agg, "stats|"+statsKey(q)+"|0", aggCacheTTL, func() (*LogStats, error) {
		return s.statsUncached(q)
	})
}

// statsFromMaterialized 从分钟物化表出全部统计(总数/字节/级别/服务Top/最早最晚)
func (s *Store) statsFromMaterialized(q *LogStatsQuery) (*LogStats, error) {
	where := ""
	args := []interface{}{}
	if q != nil && q.StartTs > 0 {
		where = " WHERE minute >= ?"
		args = append(args, q.StartTs/60000)
	}
	if q != nil && q.EndTs > 0 {
		if where == "" {
			where = " WHERE minute <= ?"
		} else {
			where += " AND minute <= ?"
		}
		args = append(args, q.EndTs/60000)
	}

	stats := &LogStats{LevelCounts: make(map[string]int64)}
	if err := s.db.QueryRow("SELECT COALESCE(SUM(cnt),0), COALESCE(SUM(bytes),0) FROM log_meta_minute"+where, args...).
		Scan(&stats.TotalCount, &stats.TotalBytes); err != nil {
		return nil, err
	}

	rows, err := s.db.Query("SELECT level, SUM(cnt) FROM log_meta_minute"+where+" GROUP BY level", args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var lvl string
		var c int64
		rows.Scan(&lvl, &c)
		stats.LevelCounts[lvl] = c
	}
	rows.Close()

	rows2, err := s.db.Query("SELECT service, SUM(cnt) FROM log_meta_minute_svc"+where+" GROUP BY service ORDER BY 2 DESC LIMIT 50", args...)
	if err != nil {
		return nil, err
	}
	for rows2.Next() {
		var svc string
		var c int64
		rows2.Scan(&svc, &c)
		stats.Services = append(stats.Services, ServiceStat{Service: svc, Count: c})
	}
	rows2.Close()
	if stats.Services == nil {
		stats.Services = []ServiceStat{}
	}

	var mn, mx int64
	if err := s.db.QueryRow("SELECT COALESCE(MIN(minute),0), COALESCE(MAX(minute),0) FROM log_meta_minute"+where, args...).Scan(&mn, &mx); err != nil {
		return nil, err
	}
	if mn > 0 {
		minTs := mn * 60000
		maxTs := mx * 60000
		stats.Oldest = &minTs
		stats.Newest = &maxTs
	}
	return stats, nil
}

func (s *Store) statsUncached(q *LogStatsQuery) (*LogStats, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// ── 物化路径: 无服务过滤且物化可用 → 全部统计毫秒级(与数据量解耦) ──
	if (q == nil || q.Service == "") && s.histReady.Load() {
		if st, err := s.statsFromMaterialized(q); err == nil {
			return st, nil
		}
	}

	where := []string{}
	args := []interface{}{}
	if q != nil {
		if q.Service != "" {
			where = append(where, inClause("service", q.Service, &args))
		}
		if q.StartTs > 0 {
			where = append(where, "ts >= ?")
			args = append(args, q.StartTs)
		}
		if q.EndTs > 0 {
			where = append(where, "ts <= ?")
			args = append(args, q.EndTs)
		}
	}
	wc := ""
	if len(where) > 0 {
		wc = "WHERE " + strings.Join(where, " AND ")
	}

	stats := &LogStats{LevelCounts: make(map[string]int64)}
	if q != nil {
		stats.TotalCount = 0
	}
	svcAgg := map[string]int64{}
	var startTs, endTs int64
	var minTs, maxTs int64
	var aggMu sync.Mutex
	if q != nil {
		startTs, endTs = q.StartTs, q.EndTs
	}

	tables := s.tablesForRange(startTs, endTs, "")

	// 分片路由: 4 组查询并行执行(读并发, 墙钟 = 最慢组), 各组内按片累加
	var gErr error
	var wg sync.WaitGroup
	wg.Add(4)
	go func() { // 组1: 总数+字节
		defer wg.Done()
		for _, t := range tables {
			var cnt, bytes int64
			if err := s.db.QueryRow("SELECT COUNT(*), COALESCE(SUM(size),0) FROM "+t+" "+wc, args...).Scan(&cnt, &bytes); err != nil {
				gErr = err
				return
			}
			aggMu.Lock()
			stats.TotalCount += cnt
			stats.TotalBytes += bytes
			aggMu.Unlock()
		}
	}()
	go func() { // 组2: 级别计数
		defer wg.Done()
		levelMap := map[string]int64{}
		for _, t := range tables {
			rows, err := s.db.Query("SELECT level, COUNT(*) FROM "+t+" "+wc+" GROUP BY level", args...)
			if err != nil {
				gErr = err
				return
			}
			for rows.Next() {
				var lvl string
				var c int64
				rows.Scan(&lvl, &c)
				levelMap[lvl] += c
			}
			rows.Close()
		}
		aggMu.Lock()
		for l, c := range levelMap {
			stats.LevelCounts[l] += c
		}
		aggMu.Unlock()
	}()
	go func() { // 组3: 服务 Top50
		defer wg.Done()
		svc := map[string]int64{}
		for _, t := range tables {
			rows, err := s.db.Query("SELECT service, COUNT(*) FROM "+t+" "+wc+" GROUP BY service", args...)
			if err != nil {
				gErr = err
				return
			}
			for rows.Next() {
				var sv string
				var c int64
				rows.Scan(&sv, &c)
				svc[sv] += c
			}
			rows.Close()
		}
		aggMu.Lock()
		for sv, c := range svc {
			svcAgg[sv] += c
		}
		aggMu.Unlock()
	}()
	go func() { // 组4: 最早/最晚
		defer wg.Done()
		for _, t := range tables {
			var mn, mx int64
			if err := s.db.QueryRow("SELECT COALESCE(MIN(ts),0), COALESCE(MAX(ts),0) FROM "+t+" "+wc, args...).Scan(&mn, &mx); err != nil {
				gErr = err
				return
			}
			aggMu.Lock()
			if mn > 0 && (minTs == 0 || mn < minTs) {
				minTs = mn
			}
			if mx > maxTs {
				maxTs = mx
			}
			aggMu.Unlock()
		}
	}()
	wg.Wait()
	if gErr != nil {
		return nil, gErr
	}
	if minTs > 0 {
		stats.Oldest = &minTs
		stats.Newest = &maxTs
	}

	// 服务聚合跨片合并后取 Top50
	type svcCnt struct {
		svc string
		cnt int64
	}
	svcs := make([]svcCnt, 0, len(svcAgg))
	for svc, c := range svcAgg {
		svcs = append(svcs, svcCnt{svc, c})
	}
	sort.Slice(svcs, func(i, j int) bool { return svcs[i].cnt > svcs[j].cnt })
	if len(svcs) > 50 {
		svcs = svcs[:50]
	}
	for _, s2 := range svcs {
		stats.Services = append(stats.Services, ServiceStat{Service: s2.svc, Count: s2.cnt})
	}
	if stats.Services == nil {
		stats.Services = []ServiceStat{}
	}

	return stats, nil
}

// Histogram 按时间桶聚合（用于前端图表）
func (s *Store) Histogram(q *LogStatsQuery, bucketMs int64) ([]HistogramBucket, error) {
	return aggGetOrBuild(s.agg, "hist|"+statsKey(q)+"|"+strconv.FormatInt(bucketMs, 10), aggCacheTTL, func() ([]HistogramBucket, error) {
		return s.histogramUncached(q, bucketMs)
	})
}

func (s *Store) histogramUncached(q *LogStatsQuery, bucketMs int64) ([]HistogramBucket, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if bucketMs <= 0 {
		bucketMs = 60000 // 默认 1 分钟
	}
	// 自适应桶宽: 桶数超过 MaxHistogramBuckets 时自动放大到分钟/5分钟/15分钟/小时/6小时/天,
	// 避免大时间窗下聚合万级桶拖垮性能(对齐 Kibana 自动 date_histogram 间隔)。
	if q != nil && q.EndTs > q.StartTs && q.StartTs > 0 {
		const maxBuckets = 600
		span := q.EndTs - q.StartTs
		if span/bucketMs > maxBuckets {
			bucketMs = autoBucketFor(span, maxBuckets)
		}
	}

	where := []string{}
	args := []interface{}{}
	if q != nil {
		if q.Service != "" {
			where = append(where, "service = ?")
			args = append(args, q.Service)
		}
		if q.StartTs > 0 {
			where = append(where, "ts >= ?")
			args = append(args, q.StartTs)
		}
		if q.EndTs > 0 {
			where = append(where, "ts <= ?")
			args = append(args, q.EndTs)
		}
	}
	wc := ""
	if len(where) > 0 {
		wc = "WHERE " + strings.Join(where, " AND ")
	}

	// 分片路由: 各片按桶聚合后合并
	bucketMap := make(map[int64]map[string]int64)
	var bucketOrder []int64
	var rStart, rEnd int64
	if q != nil {
		rStart, rEnd = q.StartTs, q.EndTs
	}

	// ── 物化路径: 无服务过滤且分钟桶可用 → 直查物化表(与数据量解耦, 毫秒级) ──
	if (q == nil || q.Service == "") && s.histReady.Load() {
		if result, err := s.histFromMinute(rStart, rEnd, bucketMs, q); err == nil {
			return result, nil
		}
	}

	// ── 常规路径: 无其他过滤时按时间对半拆段并行(读并发), 否则串行 ──
	simple := q == nil || q.Service == ""
	var wg sync.WaitGroup
	var gErr error
	var gMu sync.Mutex
	tasks := []struct {
		t    string
		s, e int64
	}{}
	if simple {
		for _, t := range s.tablesForRange(rStart, rEnd, "") {
			if rStart > 0 && rEnd > rStart+1 {
				mid := (rStart + rEnd) / 2
				tasks = append(tasks, struct {
					t    string
					s, e int64
				}{t, rStart, mid}, struct {
					t    string
					s, e int64
				}{t, mid + 1, rEnd})
			} else {
				tasks = append(tasks, struct {
					t    string
					s, e int64
				}{t, rStart, rEnd})
			}
		}
	} else {
		for _, t := range s.tablesForRange(rStart, rEnd, "") {
			tasks = append(tasks, struct {
				t    string
				s, e int64
			}{t, rStart, rEnd})
		}
	}
	runTask := func(tt struct {
		t    string
		s, e int64
	}) {
		defer wg.Done()
		where := wc
		targs := args
		if simple && rStart > 0 {
			// 拆段任务: 用任务自身 ts 范围构建 where
			where = "WHERE ts >= ? AND ts <= ?"
			targs = []interface{}{tt.s, tt.e}
		}
		histoQuery := fmt.Sprintf(`
			SELECT (ts / %d) * %d AS bucket, level, COUNT(*)
			FROM %s %s
			GROUP BY bucket, level
			ORDER BY bucket
		`, bucketMs, bucketMs, tt.t, where)

		rows, err := s.db.Query(histoQuery, targs...)
		if err != nil {
			gErr = err
			return
		}
		gMu.Lock()
		defer gMu.Unlock()
		for rows.Next() {
			var bucket int64
			var lvl string
			var cnt int64
			rows.Scan(&bucket, &lvl, &cnt)
			if bucketMap[bucket] == nil {
				bucketMap[bucket] = make(map[string]int64)
				bucketOrder = append(bucketOrder, bucket)
			}
			bucketMap[bucket][lvl] += cnt
		}
		rows.Close()
	}
	wg.Add(len(tasks))
	for _, tt := range tasks {
		go runTask(tt)
	}
	wg.Wait()
	if gErr != nil {
		return nil, gErr
	}

	var result []HistogramBucket
	for _, b := range bucketOrder {
		result = append(result, HistogramBucket{Ts: b, Count: bucketMap[b]})
	}

	// 若指定了时间范围, 把窗口内所有空桶补齐, 使 x 轴连续覆盖整个选中区间(对齐 Kibana 行为)。
	result = fillEmptyBuckets(result, q, bucketMs)

	return result, nil
}

// histFromMinute 从分钟物化表聚合直方图(桶宽 = bucketMs 的整数分钟倍数)
func (s *Store) histFromMinute(startTs, endTs, bucketMs int64, q *LogStatsQuery) ([]HistogramBucket, error) {
	div := bucketMs / 60000
	if div < 1 {
		div = 1
	}
	where := ""
	args := []interface{}{}
	if startTs > 0 {
		where = " WHERE minute >= ?"
		args = append(args, startTs/60000)
	}
	if endTs > 0 {
		if where == "" {
			where = " WHERE minute <= ?"
		} else {
			where += " AND minute <= ?"
		}
		args = append(args, endTs/60000)
	}
	sqlStr := "SELECT (minute/" + strconv.FormatInt(div, 10) + ")*" + strconv.FormatInt(div, 10) +
		" AS bm, level, SUM(cnt) FROM log_meta_minute" + where + " GROUP BY bm, level ORDER BY bm"

	rows, err := s.db.Query(sqlStr, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	bucketMap := make(map[int64]map[string]int64)
	var bucketOrder []int64
	for rows.Next() {
		var bm int64
		var lvl string
		var c int64
		if err := rows.Scan(&bm, &lvl, &c); err != nil {
			return nil, err
		}
		bucket := bm * 60000
		if bucketMap[bucket] == nil {
			bucketMap[bucket] = make(map[string]int64)
			bucketOrder = append(bucketOrder, bucket)
		}
		bucketMap[bucket][lvl] += c
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var result []HistogramBucket
	for _, b := range bucketOrder {
		result = append(result, HistogramBucket{Ts: b, Count: bucketMap[b]})
	}
	result = fillEmptyBuckets(result, q, bucketMs)
	return result, nil
}

// Terms 字段聚合（对标 ES terms aggregation）
func (s *Store) Terms(q *LogTermsQuery) (*TermsResult, error) {
	return aggGetOrBuild(s.agg, "terms|"+termsKey(q), aggCacheTTL, func() (*TermsResult, error) {
		return s.termsUncached(q)
	})
}

// termsKey 构造 Terms 缓存键（含全部过滤条件）
func termsKey(q *LogTermsQuery) string {
	if q == nil {
		return ""
	}
	return strings.Join([]string{
		q.Field, q.Service, q.Level, q.Source, q.Keyword, q.IndexID,
		strconv.FormatInt(q.StartTs, 10), strconv.FormatInt(q.EndTs, 10),
		strconv.Itoa(q.Size),
	}, "|")
}

func (s *Store) termsUncached(q *LogTermsQuery) (*TermsResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()


	// 验证聚合字段
	field := q.Field
	if field == "" {
		field = "service" // 默认按 service 聚合
	}
	// 只允许安全的字段
	switch field {
	case "service", "level", "source", "indexId":
	default:
		return nil, fmt.Errorf("unsupported terms field: %s", field)
	}

	where := []string{}
	args := []interface{}{}
	if q != nil {
		if q.Service != "" {
			where = append(where, inClause("service", q.Service, &args))
		}
		if q.Level != "" {
			where = append(where, inClause("level", q.Level, &args))
		}
		if q.Source != "" {
			where = append(where, inClause("source", q.Source, &args))
		}
		if q.Keyword != "" {
			where = append(where, "summary LIKE ?")
			args = append(args, "%"+q.Keyword+"%")
		}
		if q.StartTs > 0 {
			where = append(where, "ts >= ?")
			args = append(args, q.StartTs)
		}
		if q.EndTs > 0 {
			where = append(where, "ts <= ?")
			args = append(args, q.EndTs)
		}
		if q.IndexID != "" {
			where = append(where, inClause("index_id", q.IndexID, &args))
		}
	}
	wc := ""
	if len(where) > 0 {
		wc = "WHERE " + strings.Join(where, " AND ")
	}

	// 执行聚合: 分片路由, 每片取全量 top 1000 后跨片合并取 topN
	size := q.Size
	if size <= 0 {
		size = 10 // 默认 top 10
	}
	if size > 100 {
		size = 100 // 最大 100
	}

	start := time.Now()
	agg := map[string]int64{}
	var rStart, rEnd int64
	idxID := ""
	if q != nil {
		rStart, rEnd = q.StartTs, q.EndTs
		idxID = q.IndexID
	}
	for _, t := range s.tablesForRange(rStart, rEnd, idxID) {
		query := fmt.Sprintf(`
			SELECT %s, COUNT(*) as cnt
			FROM %s %s
			GROUP BY %s
			ORDER BY cnt DESC
			LIMIT 1000`, field, t, wc, field)

		rows, err := s.db.Query(query, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var key string
			var count int64
			if err := rows.Scan(&key, &count); err != nil {
				rows.Close()
				return nil, err
			}
			agg[key] += count
		}
		rows.Close()
	}

	buckets := make([]TermsBucket, 0, len(agg))
	for k, c := range agg {
		buckets = append(buckets, TermsBucket{Key: k, Count: c})
	}
	sort.Slice(buckets, func(i, j int) bool { return buckets[i].Count > buckets[j].Count })
	if len(buckets) > size {
		buckets = buckets[:size]
	}

	took := float64(time.Since(start).Microseconds()) / 1000.0
	return &TermsResult{
		Field:   field,
		Buckets: buckets,
		TookMs:  took,
	}, nil
}

// fillEmptyBuckets 沿 [StartTs, EndTs] 以 bucketMs 为步长补齐缺失桶(零计数)。
// 桶数过多时截断, 避免超大数组; x 轴随时间选择联动。
// statsKey 构造 Stats/Histogram 缓存键（服务 + 时间窗）
func statsKey(q *LogStatsQuery) string {
	if q == nil {
		return ""
	}
	return q.Service + "|" + strconv.FormatInt(q.StartTs, 10) + "|" + strconv.FormatInt(q.EndTs, 10)
}

// autoBucketFor 返回使桶数不超过 maxBuckets 的最小候选桶宽(毫秒)。
// 候选: 1m / 5m / 15m / 1h / 6h / 1d
func autoBucketFor(spanMs int64, maxBuckets int64) int64 {
	candidates := []int64{60000, 300000, 900000, 3600000, 21600000, 86400000}
	for _, b := range candidates {
		if spanMs/b <= maxBuckets {
			return b
		}
	}
	return 86400000
}

func fillEmptyBuckets(buckets []HistogramBucket, q *LogStatsQuery, bucketMs int64) []HistogramBucket {
	if q == nil || (q.StartTs <= 0 && q.EndTs <= 0) || bucketMs <= 0 {
		return buckets
	}
	start := q.StartTs
	if start <= 0 {
		// 只有 EndTs 时, 向前探 N 个桶
		if len(buckets) > 0 {
			start = buckets[0].Ts - bucketMs*30
		} else {
			return buckets
		}
	}
	end := q.EndTs
	if end <= 0 {
		if len(buckets) > 0 {
			end = buckets[len(buckets)-1].Ts + bucketMs
		} else {
			return buckets
		}
	}

	first := floorDiv(start, bucketMs)
	last := floorDiv(end, bucketMs)
	if last < first {
		return buckets
	}
	// 上限保护: 超过 maxBuckets 个桶则按比例降采样到 maxBuckets (对齐 Kibana histogram:maxBars)
	const maxBuckets = 100
	n := last - first + 1
	if n > maxBuckets {
		// 按步长采样
		step := (n + maxBuckets - 1) / maxBuckets
		byTs := make(map[int64]map[string]int64, len(buckets))
		for _, b := range buckets {
			byTs[b.Ts] = b.Count
		}
		out := make([]HistogramBucket, 0, maxBuckets)
		for b := first; b <= last; b += step {
			bt := b * bucketMs
			if _, ok := byTs[bt]; ok {
				out = append(out, HistogramBucket{Ts: bt, Count: byTs[bt]})
			} else {
				out = append(out, HistogramBucket{Ts: bt, Count: map[string]int64{}})
			}
		}
		return out
	}

	byTs := make(map[int64]map[string]int64, len(buckets))
	for _, b := range buckets {
		byTs[b.Ts] = b.Count
	}
	out := make([]HistogramBucket, 0, n)
	for b := first; b <= last; b++ {
		bt := b * bucketMs
		if c, ok := byTs[bt]; ok {
			out = append(out, HistogramBucket{Ts: bt, Count: c})
		} else {
			out = append(out, HistogramBucket{Ts: bt, Count: map[string]int64{}})
		}
	}
	return out
}

// floorDiv 向下取整除法(适配负数)。
func floorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

// ReadRaw 根据 id 读取原始日志内容(分片表无法定位 id 所在表, 按热表→历史片倒序遍历)
func (s *Store) ReadRaw(id int64) (*LogEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	scanFrom := func(t string) (*LogEntry, bool) {
		e := &LogEntry{}
		err := s.db.QueryRow(`SELECT id, ts, level, service, source, file_path, offset, size, summary
			FROM `+t+` WHERE id = ?`, id).Scan(
			&e.ID, &e.Ts, &e.Level, &e.Service, &e.Source, &e.FilePath, &e.Offset, &e.Size, &e.Summary)
		if err != nil {
			return nil, false
		}
		return e, true
	}
	if e, ok := scanFrom("log_meta"); ok {
		return e, nil
	}
	for _, t := range s.allDataTables() {
		if t == "log_meta" {
			continue
		}
		if e, ok := scanFrom(t); ok {
			return e, nil
		}
	}
	return nil, sql.ErrNoRows
}

// BulkDelete 批量删除元数据(逐片表执行)
func (s *Store) BulkDelete(ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	s.agg.clear()
	s.mu.Lock()
	defer s.mu.Unlock()

	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	for _, t := range s.allDataTables() {
		if _, err := s.db.Exec("DELETE FROM "+t+" WHERE id IN ("+strings.Join(placeholders, ",")+")", args...); err != nil {
			return err
		}
	}
	return nil
}

// Source CRUD
func (s *Store) ListSources() ([]*LogSource, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query("SELECT id, name, type, path, service, enabled, follow, index_id, namespace, cluster, last_ts FROM log_sources ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	sources := []*LogSource{}
	for rows.Next() {
		src := &LogSource{}
		rows.Scan(&src.ID, &src.Name, &src.Type, &src.Path, &src.Service, &src.Enabled, &src.Follow, &src.IndexID, &src.Namespace, &src.Cluster, &src.LastTs)
		sources = append(sources, src)
	}
	return sources, nil
}

func (s *Store) SaveSource(src *LogSource) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`INSERT OR REPLACE INTO log_sources (id, name, type, path, service, enabled, follow, index_id, namespace, cluster, last_ts)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		src.ID, src.Name, src.Type, src.Path, src.Service, src.Enabled, src.Follow, src.IndexID, src.Namespace, src.Cluster, src.LastTs)
	return err
}

func (s *Store) DeleteSource(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec("DELETE FROM log_sources WHERE id = ?", id)
	return err
}

// AdvanceSourceCursor 推进持续采集游标（实现幂等，不改变其它字段）
func (s *Store) AdvanceSourceCursor(id string, lastTs int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec("UPDATE log_sources SET last_ts = ? WHERE id = ?", lastTs, id)
	return err
}

// SetSourceEnabled 启停来源采集。仅翻 enabled, 不动 last_ts/其它字段, 避免 SaveSource 的 REPLACE 把游标归零导致全量重采。
func (s *Store) SetSourceEnabled(id string, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec("UPDATE log_sources SET enabled = ? WHERE id = ?", enabled, id)
	return err
}

func (s *Store) Close() {
	if s.db != nil {
		s.db.Close()
	}
}

// ---------- Index (Kibana Data View + ILM) CRUD ----------

func (s *Store) ListIndexes() ([]*LogIndex, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query("SELECT id, name, source, source_path, service, fields, ilm, delete_after, created_at, updated_at FROM log_indexes ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	indexes := []*LogIndex{}
	slugNames := map[string]string{} // 刷新 索引id→显示名 映射(分片 slug 用)
	for rows.Next() {
		idx := &LogIndex{}
		var fields, ilm string
		if err := rows.Scan(&idx.ID, &idx.Name, &idx.Source, &idx.SourcePath, &idx.Service, &fields, &ilm, &idx.DeleteAfter, &idx.CreatedAt, &idx.UpdatedAt); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(fields), &idx.Fields)
		json.Unmarshal([]byte(ilm), &idx.Ilm)
		slugNames[idx.ID] = idx.Name
		indexes = append(indexes, idx)
	}
	setSlugNames(slugNames)
	if indexes == nil {
		indexes = []*LogIndex{}
	}
	return indexes, nil
}

func (s *Store) GetIndex(id string) (*LogIndex, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	idx := &LogIndex{}
	var fields, ilm string
	err := s.db.QueryRow("SELECT id, name, source, source_path, service, fields, ilm, delete_after, created_at, updated_at FROM log_indexes WHERE id = ?", id).
		Scan(&idx.ID, &idx.Name, &idx.Source, &idx.SourcePath, &idx.Service, &fields, &ilm, &idx.DeleteAfter, &idx.CreatedAt, &idx.UpdatedAt)
	if err != nil {
		return nil, err
	}
	json.Unmarshal([]byte(fields), &idx.Fields)
	json.Unmarshal([]byte(ilm), &idx.Ilm)
	return idx, nil
}

func (s *Store) SaveIndex(idx *LogIndex) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if idx.ID == "" {
		idx.ID = "idx_" + strconv.FormatInt(time.Now().UnixMilli(), 10)
	}
	if idx.Fields == nil {
		idx.Fields = []FieldMap{}
	}
	// ILM 兜底默认：全部阶段保留 0 时采用默认策略，避免仅传 deleteAfter 的调用丢失策略
	idx.Ilm = withDefaultIlm(idx.Ilm)
	now := time.Now().UnixMilli()
	if idx.CreatedAt == 0 {
		idx.CreatedAt = now
	}
	idx.UpdatedAt = now

	fields, err := json.Marshal(idx.Fields)
	if err != nil {
		return err
	}
	ilmB, err := json.Marshal(idx.Ilm)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT OR REPLACE INTO log_indexes (id, name, source, source_path, service, fields, ilm, delete_after, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		idx.ID, idx.Name, idx.Source, idx.SourcePath, idx.Service, string(fields), string(ilmB), idx.DeleteAfter, idx.CreatedAt, idx.UpdatedAt)
	return err
}

func (s *Store) DeleteIndex(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec("DELETE FROM log_indexes WHERE id = ?", id)
	return err
}

// IndexStatsFor 返回指定索引的统计 + 当前存储阶段(跨分片累加)
func (s *Store) IndexStatsFor(id string) (*IndexStats, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	st := &IndexStats{}
	var minTs, maxTs int64
	for _, t := range s.tablesForRange(0, 0, id) {
		var c, b, mn, mx int64
		if err := s.db.QueryRow("SELECT COUNT(*), COALESCE(SUM(size),0), COALESCE(MIN(ts),0), COALESCE(MAX(ts),0) FROM "+t+" WHERE index_id = ?", id).
			Scan(&c, &b, &mn, &mx); err != nil {
			return nil, err
		}
		st.DocCount += c
		st.Bytes += b
		if mn > 0 && (minTs == 0 || mn < minTs) {
			minTs = mn
		}
		if mx > maxTs {
			maxTs = mx
		}
	}
	if minTs > 0 {
		st.Oldest = &minTs
		st.Newest = &maxTs
	}
	if st.Oldest == nil {
		return nil, fmt.Errorf("索引 %s 无数据", id)
	}
	st.StorageStage = "hot"
	return st, nil
}

// IlmCleanup 描述一次 ILM 淘汰的清理动作
type IlmCleanup struct {
	IndexID   string `json:"indexId"`
	CutoffDate string `json:"cutoffDate"` // 删除早于此日(含)的归档文件与元数据
	Rows      int64  `json:"rows"`        // 被删除的元数据行数
}

// ApplyIlm 执行 ILM 淘汰 —— 按索引整片 DROP(秒级), 取代逐行 DELETE:
//   1) 按各索引保留期(delete_after/ilm 配置)算出截止时间
//   2) 该索引所有 end_ts < 截止的片 → DROP TABLE + 删路由 + 清理物化
//   3) 返回 (indexID, cutoffDate, rows) 计划, 由上层联动删除归档文件
func (s *Store) ApplyIlm() ([]IlmCleanup, int64, error) {
	s.mu.Lock()
	indexes, err := s.ListIndexes()
	s.mu.Unlock()
	if err != nil {
		return nil, 0, err
	}

	beyondNow := time.Now().Add(-24 * time.Hour).UnixMilli()
	cleanups := []IlmCleanup{}
	totalRows := int64(0)
	dropShard := func(key, idx string) int64 {
		var cnt int64
		s.db.QueryRow("SELECT COUNT(*) FROM " + shardTableName(key)).Scan(&cnt)
		var st, et int64
		s.db.QueryRow("SELECT start_ts, end_ts FROM log_shards WHERE shard=?", key).Scan(&st, &et)
		if _, err := s.db.Exec("DROP TABLE " + shardTableName(key)); err != nil {
			return 0
		}
		s.db.Exec("DELETE FROM log_shards WHERE shard=?", key)
		s.db.Exec("DELETE FROM log_meta_minute WHERE minute >= ? AND minute <= ?", st/60000, et/60000)
		s.db.Exec("DELETE FROM log_meta_minute_svc WHERE minute >= ? AND minute <= ?", st/60000, et/60000)
		return cnt
	}
	for _, idx := range indexes {
		days := idx.DeleteAfter
		if days <= 0 {
			if idx.Ilm.Delete.RetentionDays > 0 {
				days = idx.Ilm.Delete.RetentionDays
			} else if idx.Ilm.Cold.RetentionDays > 0 {
				days = idx.Ilm.Cold.RetentionDays
			}
		}
		if days <= 0 {
			continue
		}
		cutoff := beyondNow - int64(days)*24*3600*1000
		slug := indexSlug(idx.ID)
		rows, err := s.db.Query("SELECT shard, end_ts FROM log_shards WHERE index_id = ?", idx.ID)
		if err != nil {
			return cleanups, totalRows, err
		}
		perIndex := int64(0)
		cutDate := ""
		for rows.Next() {
			var k string
			var et int64
			rows.Scan(&k, &et)
			if et <= 0 || et >= cutoff {
				continue
			}
			perIndex += dropShard(k, idx.ID)
		}
		rows.Close()
		// 兜底: 残留逐行清(片边界缺失/样本外的行)
		if perIndex > 0 || slug != "" {
			for _, t := range s.tablesForRange(0, 0, idx.ID) {
				res, err := s.db.Exec("DELETE FROM "+t+" WHERE index_id = ? AND ts < ?", idx.ID, cutoff)
				if err != nil {
					return cleanups, totalRows, err
				}
				n, _ := res.RowsAffected()
				perIndex += n
			}
		}
		if perIndex > 0 {
			totalRows += perIndex
			cutDate = time.UnixMilli(cutoff).Format("2006-01-02")
			cleanups = append(cleanups, IlmCleanup{IndexID: idx.ID, CutoffDate: cutDate, Rows: perIndex})
		}
	}
	if totalRows > 0 {
		s.agg.clear()
	}
	return cleanups, totalRows, nil
}

// ApplyIlmAll 无索引归属(unassigned)日志按全局保留清理 —— 整片 DROP 优先, 残留逐行兜底
func (s *Store) ApplyIlmAll() (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg := s.shardCfg
	if cfg.DefaultRetentionDays <= 0 {
		cfg.DefaultRetentionDays = 7
	}
	cutoff := time.Now().Add(-time.Duration(cfg.DefaultRetentionDays) * 24 * time.Hour).UnixMilli()
	dropped := int64(0)

	// unassigned 整片删除
	rows, err := s.db.Query("SELECT shard, start_ts, end_ts FROM log_shards WHERE index_id = ''")
	if err != nil {
		return 0, err
	}
	var dropKeys []struct{ k string; st, et int64 }
	for rows.Next() {
		var k string
		var st, et int64
		rows.Scan(&k, &st, &et)
		if et > 0 && et < cutoff {
			dropKeys = append(dropKeys, struct {
				k  string
				st, et int64
			}{k, st, et})
		}
	}
	rows.Close()
	for _, dk := range dropKeys {
		var n int64
		s.db.QueryRow("SELECT COUNT(*) FROM " + shardTableName(dk.k)).Scan(&n)
		if _, err := s.db.Exec("DROP TABLE " + shardTableName(dk.k)); err == nil {
			s.db.Exec("DELETE FROM log_shards WHERE shard=?", dk.k)
			s.db.Exec("DELETE FROM log_meta_minute WHERE minute >= ? AND minute <= ?", dk.st/60000, dk.et/60000)
			s.db.Exec("DELETE FROM log_meta_minute_svc WHERE minute >= ? AND minute <= ?", dk.st/60000, dk.et/60000)
			dropped += n
		}
	}
	// 热表/未删片逐行清理 unassigned
	for _, t := range s.tablesForRange(0, 0, "") {
		res, err := s.db.Exec("DELETE FROM "+t+" WHERE ts < ? AND index_id = ''", cutoff)
		if err != nil {
			return dropped, err
		}
		n, _ := res.RowsAffected()
		dropped += n
	}
	// 物化表联动清理
	if _, err := s.db.Exec("DELETE FROM log_meta_minute WHERE minute < ?", cutoff/60000); err != nil {
		return dropped, err
	}
	if _, err := s.db.Exec("DELETE FROM log_meta_minute_svc WHERE minute < ?", cutoff/60000); err != nil {
		return dropped, err
	}
	if dropped > 0 {
		s.agg.clear()
	}
	return dropped, nil
}

// withDefaultIlm 填充 ILM 默认策略（当传入各阶段保留全为 0 时）
func withDefaultIlm(p IlmPolicy) IlmPolicy {
	if p.Hot.RetentionDays == 0 && p.Warm.RetentionDays == 0 && p.Cold.RetentionDays == 0 && p.Delete.RetentionDays == 0 && p.Hot.Priority == 0 {
		return IlmPolicy{
			Hot:    IlmStage{RetentionDays: 7, Priority: 100},
			Warm:   IlmStage{RetentionDays: 30, Readonly: true, Compress: true, Priority: 50},
			Cold:   IlmStage{RetentionDays: 90, Readonly: true, Compress: true, Freeze: true, Priority: 10},
			Delete: IlmStage{RetentionDays: 180, Priority: 0},
		}
	}
	return p
}

// ---------- Alert Rules CRUD ----------

func (s *Store) ListAlertRules() ([]*AlertRule, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query("SELECT id, name, enabled, condition, count_thresh, window_ms, cooldown_ms, channels, created_at, updated_at, state, last_fired FROM alert_rules ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	rules := []*AlertRule{}
	for rows.Next() {
		r := &AlertRule{}
		var channels string
		if err := rows.Scan(&r.ID, &r.Name, &r.Enabled, &r.Condition, &r.CountThresh, &r.WindowMs, &r.CooldownMs, &channels, &r.CreatedAt, &r.UpdatedAt, &r.State, &r.LastFired); err != nil {
			return nil, err
		}
		if channels != "" {
			json.Unmarshal([]byte(channels), &r.Channels)
		}
		if r.Channels == nil { r.Channels = []string{} }
		rules = append(rules, r)
	}
	return rules, nil
}

func (s *Store) SaveAlertRule(r *AlertRule) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if r.ID == "" { r.ID = "ar_" + strconv.FormatInt(time.Now().UnixMilli(), 10) }
	channelsB, _ := json.Marshal(r.Channels)
	_, err := s.db.Exec(`INSERT OR REPLACE INTO alert_rules (id, name, enabled, condition, count_thresh, window_ms, cooldown_ms, channels, created_at, updated_at, state, last_fired)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.Name, r.Enabled, r.Condition, r.CountThresh, r.WindowMs, r.CooldownMs, string(channelsB), r.CreatedAt, time.Now().UnixMilli(), r.State, r.LastFired)
	return err
}

func (s *Store) DeleteAlertRule(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec("DELETE FROM alert_rules WHERE id = ?", id)
	return err
}

// ---------- Alert Channels CRUD ----------

func (s *Store) ListAlertChannels() ([]*AlertChannel, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query("SELECT id, name, type, url, method, headers, enabled, created_at FROM alert_channels ORDER BY name")
	if err != nil { return nil, err }
	defer rows.Close()

	chans := []*AlertChannel{}
	for rows.Next() {
		c := &AlertChannel{}
		var headers string
		if err := rows.Scan(&c.ID, &c.Name, &c.Type, &c.URL, &c.Method, &headers, &c.Enabled, &c.CreatedAt); err != nil { return nil, err }
		if headers != "" { json.Unmarshal([]byte(headers), &c.Headers) }
		chans = append(chans, c)
	}
	return chans, nil
}

func (s *Store) SaveAlertChannel(c *AlertChannel) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c.ID == "" { c.ID = "ac_" + strconv.FormatInt(time.Now().UnixMilli(), 10) }
	hB, _ := json.Marshal(c.Headers)
	_, err := s.db.Exec(`INSERT OR REPLACE INTO alert_channels (id, name, type, url, method, headers, enabled, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.Name, c.Type, c.URL, c.Method, string(hB), c.Enabled, c.CreatedAt)
	return err
}

func (s *Store) DeleteAlertChannel(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec("DELETE FROM alert_channels WHERE id = ?", id)
	return err
}

// ---------- Alert Events ----------

func (s *Store) ListAlertEvents(limit int) ([]*AlertEvent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 { limit = 100 }
	rows, err := s.db.Query("SELECT id, rule_id, rule_name, level, service, count, fired_at, resolved_at, status FROM alert_events ORDER BY fired_at DESC LIMIT ?", limit)
	if err != nil { return nil, err }
	defer rows.Close()
	events := []*AlertEvent{}
	for rows.Next() {
		e := &AlertEvent{}
		if err := rows.Scan(&e.ID, &e.RuleID, &e.RuleName, &e.Level, &e.Service, &e.Count, &e.FiredAt, &e.ResolvedAt, &e.Status); err != nil { return nil, err }
		events = append(events, e)
	}
	return events, nil
}
