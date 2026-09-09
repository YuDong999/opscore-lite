package logmonitor

import (
	"encoding/json"
	"fmt"
	"os"

	"sort"
	"time"
)

// ── 分片存储 (仿 ES Data Stream / logstash 按天索引) ──────────
// 热表 log_meta 持续写入; 新周期首次写入时把上一周期数据 RENAME 归档为
// log_meta_<key> 并重建热表; 查询按时间窗路由到命中片表逐片聚合再合并。
// log_shards 记录各片起止与状态, 过期片整体 DROP(秒级), 取代逐行 DELETE。

type ShardConfig struct {
	ShardBy              string `json:"shardBy"`              // month | day | week
	HotShards            int    `json:"hotShards"`            // 热片数(当前片+最近N片不删)
	DefaultRetentionDays int    `json:"defaultRetentionDays"` // 全局默认保留天数(未设 delete_after 的索引/unassigned)
}

func defaultShardConfig() ShardConfig {
	return ShardConfig{ShardBy: "month", HotShards: 1, DefaultRetentionDays: 7}
}

// loadShardConfig 读取 shards.json; 缺失/损坏回退内置默认(文件不存在, 前端编辑保存时落盘)
func loadShardConfig(path string) ShardConfig {
	cfg := defaultShardConfig()
	b, err := os.ReadFile(path)
	if err != nil {
		return cfg
	}
	var c ShardConfig
	if err := json.Unmarshal(b, &c); err != nil {
		return cfg
	}
	switch c.ShardBy {
	case "month", "day", "week":
	default:
		c.ShardBy = cfg.ShardBy
	}
	if c.HotShards < 0 {
		c.HotShards = 0
	}
	if c.DefaultRetentionDays <= 0 {
		c.DefaultRetentionDays = cfg.DefaultRetentionDays
	}
	return c
}

func saveShardConfig(path string, cfg ShardConfig) error {
	b, _ := json.MarshalIndent(cfg, "", "  ")
	return os.WriteFile(path, b, 0644)
}

// shardKeyOf 时间 → 分片键 (month: 200601, day: 20060102, week: 周一起始日20060102)
func shardKeyOf(ts int64, by string) string {
	t := time.UnixMilli(ts)
	switch by {
	case "day":
		return t.Format("20060102")
	case "week":
		wd := (int(t.Weekday()) + 6) % 7 // 周一=0
		return t.AddDate(0, 0, -wd).Format("20060102")
	default: // month
		return t.Format("200601")
	}
}

func shardTableName(key string) string { return "log_meta_" + key }

var shardIndexes = []string{
	"idx_m_ts", "idx_m_svc", "idx_m_lvl", "idx_m_src",
	"idx_m_svc_lvl", "idx_m_svc_ts", "idx_m_index_id",
}

// ensureShardTable 建历史片表(结构=热表)并记录 log_shards。
// 注意: 调用方(InsertBatch)必须已持有 s.mu 写锁。
func (s *Store) ensureShardTable(key string) error {
	name := shardTableName(key)
	var n int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", name).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		if _, err := s.db.Exec(`CREATE TABLE ` + name + ` (
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
		)`); err != nil {
			return fmt.Errorf("create shard %s: %w", name, err)
		}
		for _, ix := range shardIndexes {
			s.db.Exec("CREATE INDEX IF NOT EXISTS " + ix + "_" + key + " ON " + name + indexedCols(ix))
		}
	}
	_, err := s.db.Exec(`INSERT OR IGNORE INTO log_shards (shard, start_ts, end_ts) VALUES (?, 0, 0)`, key)
	return err
}

func indexedCols(ix string) string {
	switch ix {
	case "idx_m_ts":
		return "(ts)"
	case "idx_m_svc":
		return "(service)"
	case "idx_m_lvl":
		return "(level)"
	case "idx_m_src":
		return "(source)"
	case "idx_m_svc_lvl":
		return "(service, level, ts)"
	case "idx_m_svc_ts":
		return "(service, ts)"
	default:
		return "(index_id)"
	}
}

// RefreshShardBounds 更新片上界(统计/删除后调用, 保证路由准确)
func (s *Store) RefreshShardBounds() {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query("SELECT shard FROM log_shards")
	if err != nil {
		return
	}
	var keys []string
	for rows.Next() {
		var k string
		rows.Scan(&k)
		keys = append(keys, k)
	}
	rows.Close()
	for _, k := range keys {
		// 热表或历史片, 更新起止
		s.db.Exec("UPDATE log_shards SET start_ts=(SELECT COALESCE(MIN(ts),0) FROM "+shardTableName(k)+"), end_ts=(SELECT COALESCE(MAX(ts),0) FROM "+shardTableName(k)+") WHERE shard=?", k)
	}
	s.db.Exec("UPDATE log_shards SET start_ts=(SELECT COALESCE(MIN(ts),0) FROM log_meta), end_ts=(SELECT COALESCE(MAX(ts),0) FROM log_meta) WHERE shard='__hot__'")
}

// rollShard 把热表数据归档为历史片并重建热表; 调用方必须已持 s.mu.Lock
func (s *Store) rollShardLocked(oldKey string) error {
	name := shardTableName(oldKey)
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	// 热表索引随表走, 重建热表时同名索引会冲突 → 先删热表索引
	for _, ix := range []string{"idx_meta_ts", "idx_meta_svc", "idx_meta_lvl", "idx_meta_src", "idx_meta_svc_lvl", "idx_meta_svc_ts", "idx_meta_index_id"} {
		if _, err := tx.Exec("DROP INDEX IF EXISTS " + ix); err != nil {
			tx.Rollback()
			return err
		}
	}
	if _, err := tx.Exec("ALTER TABLE log_meta RENAME TO " + name); err != nil {
		tx.Rollback()
		return err
	}
	// 重建热表 + 索引
	ddl := `CREATE TABLE log_meta (
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
	CREATE INDEX idx_meta_ts      ON log_meta(ts);
	CREATE INDEX idx_meta_svc     ON log_meta(service);
	CREATE INDEX idx_meta_lvl     ON log_meta(level);
	CREATE INDEX idx_meta_src     ON log_meta(source);
	CREATE INDEX idx_meta_svc_lvl ON log_meta(service, level, ts);
	CREATE INDEX idx_meta_svc_ts  ON log_meta(service, ts);
	CREATE INDEX idx_meta_index_id ON log_meta(index_id);`
	if _, err := tx.Exec(ddl); err != nil {
		tx.Rollback()
		return err
	}
	// 历史片索引
	for _, ix := range shardIndexes {
		if _, err := tx.Exec("CREATE INDEX IF NOT EXISTS " + ix + "_" + oldKey + " ON " + name + indexedCols(ix)); err != nil {
			tx.Rollback()
			return err
		}
	}
	if _, err := tx.Exec(`INSERT OR REPLACE INTO log_shards (shard, start_ts, end_ts)
		VALUES (?, (SELECT COALESCE(MIN(ts),0) FROM `+name+`), (SELECT COALESCE(MAX(ts),0) FROM `+name+`))`, oldKey); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// tablesForRange 时间窗命中的表集合(含热表); start/end<=0 时返回全部。
// 注意: 调用方必须已持有 s.mu 读锁或写锁(RWMutex 不可重入)。
func (s *Store) tablesForRange(startTs, endTs int64) []string {
	var tables []string
	rows, err := s.db.Query("SELECT shard, start_ts, end_ts FROM log_shards")
	if err != nil {
		return []string{"log_meta"}
	}
	defer rows.Close()
	any := false
	for rows.Next() {
		var k string
		var st, et int64
		rows.Scan(&k, &st, &et)
		any = true
		if startTs > 0 && et > 0 && et < startTs {
			continue
		}
		if endTs > 0 && st > 0 && st > endTs {
			continue
		}
		tables = append(tables, shardTableName(k))
	}
	if !any {
		return []string{"log_meta"}
	}
	// 热表恒参与(可能含跨片少量数据)
	tables = append(tables, "log_meta")
	return tables
}

// allDataTables 全部数据表(热表 + 历史片)
func (s *Store) allDataTables() []string {
	return s.tablesForRange(0, 0)
}

// ListShards 分片状态(前端展示); cfg 用于保留期计算
func (s *Store) ListShards(cfg ShardConfig) []map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cutoff := time.Now().AddDate(0, 0, -cfg.DefaultRetentionDays).UnixMilli()
	out := []map[string]interface{}{}
	rows, err := s.db.Query("SELECT shard, start_ts, end_ts FROM log_shards ORDER BY shard DESC")
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var st, et int64
		rows.Scan(&k, &st, &et)
		var cnt int64
		s.db.QueryRow("SELECT COUNT(*) FROM " + shardTableName(k)).Scan(&cnt)
		out = append(out, map[string]interface{}{
			"shard": k, "startTs": st, "endTs": et, "rows": cnt,
			"dropAllowed": et > 0 && et < cutoff,
		})
	}
	// 热表
	var cnt int64
	s.db.QueryRow("SELECT COUNT(*) FROM log_meta").Scan(&cnt)
	out = append(out, map[string]interface{}{"shard": "__hot__", "startTs": 0, "endTs": 0, "rows": cnt, "dropAllowed": false})
	return out
}

// DropShard 手动删除过期分片(误删保护: 片 endTs 必须早于保留线)
func (s *Store) DropShard(key string, cfg ShardConfig) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var st, et int64
	cutoff := time.Now().AddDate(0, 0, -cfg.DefaultRetentionDays).UnixMilli()
	if err := s.db.QueryRow("SELECT start_ts, end_ts FROM log_shards WHERE shard=?", key).Scan(&st, &et); err != nil {
		return 0, fmt.Errorf("分片不存在: %v", err)
	}
	if et <= 0 || et >= cutoff {
		return 0, fmt.Errorf("分片 %s 仍在保留期内(end=%d, cutoff=%d), 拒绝删除", key, et, cutoff)
	}
	n := int64(0)
	s.db.QueryRow("SELECT COUNT(*) FROM " + shardTableName(key)).Scan(&n)
	if _, err := s.db.Exec("DROP TABLE " + shardTableName(key)); err != nil {
		return 0, err
	}
	s.db.Exec("DELETE FROM log_shards WHERE shard=?", key)
	return n, nil
}

// SortShardKeys 稳定排序(无果)
func sortShardKeys(keys []string) { sort.Strings(keys) }
