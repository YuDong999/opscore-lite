package logmonitor

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// ── 双层分片存储 (对标 ES: 先按索引模板分业务序列, 再在序列内按时间切) ──
// 表名: log_meta_<索引slug>_<期>, 如 log_meta_k8s_logs_202608; 无索引数据落 unassigned 片。
// 写入按 (index_id, ts) 直接落片; 查询按 (时间窗 × 索引) 路由; 保留策略按索引整片 DROP。

type ShardConfig struct {
	ShardBy              string `json:"shardBy"`              // month | day | week
	HotShards            int    `json:"hotShards"`            // 保留的最近片数(不参与保留期删除)
	DefaultRetentionDays int    `json:"defaultRetentionDays"` // 全局默认保留天数(未设 delete_after 的索引/unassigned)
}

func defaultShardConfig() ShardConfig {
	return ShardConfig{ShardBy: "month", HotShards: 1, DefaultRetentionDays: 7}
}

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

// shardKeyOf 时间 → 期键 (month: 200601, day: 20060102, week: 周一起始日20060102)
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

var reSlugBad = regexp.MustCompile(`[^a-z0-9_]`)

// indexSlug 索引 id → 分片 slug (可读、幂等、冲突后缀 hash)
func indexSlug(indexID string) string {
	if indexID == "" {
		return "unassigned"
	}
	if strings.HasPrefix(indexID, "idx_") {
		if n := slugNameOf(indexID); n != "" {
			slug := strings.ToLower(reSlugBad.ReplaceAllString(n, "_"))
			slug = strings.Trim(slug, "_")
			if len(slug) > 24 {
				slug = slug[:24]
			}
			if slug != "" {
				return slug
			}
		}
		return "idx_" + fixedHash(indexID)
	}
	slug := strings.ToLower(reSlugBad.ReplaceAllString(indexID, "_"))
	slug = strings.Trim(slug, "_")
	if len(slug) > 24 {
		slug = slug[:24]
	}
	return slug
}

// slugNameCache index_id → 索引显示名 (由 ListIndexes 刷新)
var slugNameCache = struct {
	sync.RWMutex
	m map[string]string
}{m: map[string]string{}}

func setSlugNames(byID map[string]string) {
	slugNameCache.Lock()
	slugNameCache.m = byID
	slugNameCache.Unlock()
}

func slugNameOf(indexID string) string {
	slugNameCache.RLock()
	defer slugNameCache.RUnlock()
	return slugNameCache.m[indexID]
}

func fixedHash(s string) string {
	h := uint32(7)
	for i := 0; i < len(s); i++ {
		h = h*31 + uint32(s[i])
	}
	return fmt.Sprintf("%x", h&0xffff)
}

// shardKeyFor (indexID, ts) → 完整片键; hot 表语义取消, 一律落 (索引, 期) 片
func (s *Store) shardKeyFor(indexID string, ts int64) string {
	return indexSlug(indexID) + "_" + shardKeyOf(ts, s.shardCfg.ShardBy)
}

// ensureShardTable 建 (索引,期) 片表并记录 log_shards; 调用方需已持 s.mu 写锁
func (s *Store) ensureShardTable(indexID, key string) error {
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
	_, err := s.db.Exec(`INSERT OR IGNORE INTO log_shards (shard, index_id, start_ts, end_ts) VALUES (?, ?, 0, 0)`, key, indexID)
	return err
}

// updateShardBounds 缩容时用实际 min/max 回写片边界
func (s *Store) updateShardBounds(key string) {
	// 片已删则跳过
	var n int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", shardTableName(key)).Scan(&n); err != nil || n == 0 {
		return
	}
	s.db.Exec("UPDATE log_shards SET start_ts=(SELECT COALESCE(MIN(ts),0) FROM "+shardTableName(key)+"), end_ts=(SELECT COALESCE(MAX(ts),0) FROM "+shardTableName(key)+") WHERE shard=?", key)
}

// tablesForRange 时间窗 × 索引 命中的表集合。
// indexID=="" 返回全部(全局统计/检索); 否则仅该索引的片。
// 注意: 调用方必须已持有 s.mu 读锁或写锁(RWMutex 不可重入)。
func (s *Store) tablesForRange(startTs, endTs int64, indexID string) []string {
	var tables []string
	where := ""
	args := []interface{}{}
	if indexID != "" {
		where = " WHERE index_id = ?"
		args = append(args, indexID)
	}
	rows, err := s.db.Query("SELECT shard, index_id, start_ts, end_ts FROM log_shards"+where, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	any := false
	for rows.Next() {
		var k, idx string
		var st, et int64
		rows.Scan(&k, &idx, &st, &et)
		if indexID == "" && idx != "" {
			any = true
		} else if indexID != "" {
			any = true
		}
		if startTs > 0 && et > 0 && et < startTs {
			continue
		}
		if endTs > 0 && st > 0 && st > endTs {
			continue
		}
		tables = append(tables, shardTableName(k))
	}
	if !any {
		// 无分片记录(全新库/迁移前): 回到传统单表
		return []string{"log_meta"}
	}
	// 迁移未完成时附加遗留旧表(兼容窗口)
	tables = append(tables, s.legacyTables()...)
	return tables
}

// legacyTables 迁移完成前附带的旧表(热表 log_meta + 旧纯时间片)
func (s *Store) legacyTables() []string {
	if s.migrated.Load() {
		return nil
	}
	t := []string{"log_meta"}
	rows, err := s.db.Query("SELECT shard FROM log_shards WHERE index_id = '' AND shard NOT LIKE '%\\_%' ESCAPE '\\'")
	if err == nil {
		for rows.Next() {
			var k string
			rows.Scan(&k)
			t = append(t, shardTableName(k))
		}
		rows.Close()
	}
	return t
}

// allDataTables 全部数据表
func (s *Store) allDataTables() []string {
	return s.tablesForRange(0, 0, "")
}

// ListShards 分片状态(前端展示); cfg 用于保留期与热片计算
func (s *Store) ListShards(cfg ShardConfig) []map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cutoff := time.Now().AddDate(0, 0, -cfg.DefaultRetentionDays).UnixMilli()
	out := []map[string]interface{}{}
	rows, err := s.db.Query("SELECT shard, index_id, start_ts, end_ts FROM log_shards ORDER BY shard DESC")
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var k, idx string
		var st, et int64
		rows.Scan(&k, &idx, &st, &et)
		var cnt int64
		s.db.QueryRow("SELECT COUNT(*) FROM " + shardTableName(k)).Scan(&cnt)
		out = append(out, map[string]interface{}{
			"shard": k, "indexId": idx, "startTs": st, "endTs": et, "rows": cnt,
			"dropAllowed": et > 0 && et < cutoff,
		})
	}
	return out
}

// DropShard 手动删除过期分片(误删保护: 片 end_ts 必须早于保留线)
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
	// 物化分钟桶联动清理(该片时间范围)
	s.db.Exec("DELETE FROM log_meta_minute WHERE minute >= ? AND minute <= ?", st/60000, et/60000)
	if _, err := s.db.Exec("DELETE FROM log_meta_minute_svc WHERE minute >= ? AND minute <= ?", st/60000, et/60000); err != nil {
		return 0, err
	}
	return n, nil
}

// migrated 存量混合片是否已拆入新格式(查询兼容开关)
// (字段放 Store; 这里声明避免误用)