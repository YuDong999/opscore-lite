package logmonitor

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// Store 元数据索引存储（SQLite）
type Store struct {
	db     *sql.DB
	dbPath string
	mu     sync.RWMutex
}

func NewStore(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_cache_size=-64000")
	if err != nil {
		return nil, fmt.Errorf("open logmeta db: %w", err)
	}
	db.SetMaxOpenConns(1) // SQLite 单写

	s := &Store{db: db, dbPath: dbPath}
	if err := s.Check(); err != nil {
		// 库损坏且 salvage 失败：拒绝启动，避免带病写入扩大损坏
		db.Close()
		return nil, err
	}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
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
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	stmt, err := tx.Prepare(`INSERT INTO log_meta (ts, level, service, source, file_path, offset, size, summary, index_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		tx.Rollback()
		return nil, err
	}
	defer stmt.Close()

	ids := make([]int64, 0, len(entries))
	for _, e := range entries {
		res, err := stmt.Exec(e.Ts, e.Level, e.Service, e.Source, e.FilePath, e.Offset, e.Size, e.Summary, e.IndexID)
		if err != nil {
			tx.Rollback()
			return nil, fmt.Errorf("insert log_meta: %w", err)
		}
		id, _ := res.LastInsertId()
		e.ID = id
		ids = append(ids, id)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return ids, nil
}

// Query 多条件查询（走复合索引）
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

	// 查总数
	var total int64
	countSQL := "SELECT COUNT(*) FROM log_meta " + whereClause
	if err := s.db.QueryRow(countSQL, args...).Scan(&total); err != nil {
		return nil, err
	}

	// 分页查询
	page := q.Page
	if page < 1 {
		page = 1
	}
	pageSize := q.PageSize
	if pageSize < 1 || pageSize > 500 {
		pageSize = 100
	}
	offset := int64((page - 1) * pageSize)

	querySQL := `SELECT id, ts, level, service, source, file_path, offset, size, summary, index_id
		FROM log_meta ` + whereClause + `
		ORDER BY ts DESC
		LIMIT ? OFFSET ?`

	args = append(args, pageSize, offset)
	rows, err := s.db.Query(querySQL, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []*LogEntry
	for rows.Next() {
		e := &LogEntry{}
		if err := rows.Scan(&e.ID, &e.Ts, &e.Level, &e.Service, &e.Source, &e.FilePath, &e.Offset, &e.Size, &e.Summary, &e.IndexID); err != nil {
			return nil, err
		}
		items = append(items, e)
	}
	if items == nil {
		items = []*LogEntry{}
	}

	took := float64(time.Since(start).Microseconds()) / 1000.0
	return &LogQueryResult{Total: total, Items: items, TookMs: took}, nil
}

// Stats 获取统计信息
func (s *Store) Stats(q *LogStatsQuery) (*LogStats, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

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

	// 总数 + 总字节
	s.db.QueryRow("SELECT COUNT(*), COALESCE(SUM(size),0) FROM log_meta "+wc, args...).Scan(&stats.TotalCount, &stats.TotalBytes)

	// 各级别计数
	rows, err := s.db.Query("SELECT level, COUNT(*) FROM log_meta "+wc+" GROUP BY level", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var lvl string
		var cnt int64
		rows.Scan(&lvl, &cnt)
		stats.LevelCounts[lvl] = cnt
	}

	// 各服务统计
	rows2, err := s.db.Query("SELECT service, COUNT(*) FROM log_meta "+wc+" GROUP BY service ORDER BY COUNT(*) DESC LIMIT 50", args...)
	if err != nil {
		return nil, err
	}
	defer rows2.Close()
	for rows2.Next() {
		var svc string
		var cnt int64
		rows2.Scan(&svc, &cnt)
		stats.Services = append(stats.Services, ServiceStat{Service: svc, Count: cnt})
	}
	if stats.Services == nil {
		stats.Services = []ServiceStat{}
	}

	// 时间范围
	s.db.QueryRow("SELECT MIN(ts), MAX(ts) FROM log_meta "+wc, args...).Scan(&stats.Oldest, &stats.Newest)

	return stats, nil
}

// Histogram 按时间桶聚合（用于前端图表）
func (s *Store) Histogram(q *LogStatsQuery, bucketMs int64) ([]HistogramBucket, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if bucketMs <= 0 {
		bucketMs = 60000 // 默认 1 分钟
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

	// 按桶聚合
	histoQuery := fmt.Sprintf(`
		SELECT (ts / %d) * %d AS bucket, level, COUNT(*)
		FROM log_meta %s
		GROUP BY bucket, level
		ORDER BY bucket
	`, bucketMs, bucketMs, wc)

	rows, err := s.db.Query(histoQuery, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	bucketMap := make(map[int64]map[string]int64)
	var bucketOrder []int64

	for rows.Next() {
		var bucket int64
		var lvl string
		var cnt int64
		rows.Scan(&bucket, &lvl, &cnt)
		if bucketMap[bucket] == nil {
			bucketMap[bucket] = make(map[string]int64)
			bucketOrder = append(bucketOrder, bucket)
		}
		bucketMap[bucket][lvl] = cnt
	}

	var result []HistogramBucket
	for _, b := range bucketOrder {
		result = append(result, HistogramBucket{Ts: b, Count: bucketMap[b]})
	}

	// 若指定了时间范围, 把窗口内所有空桶补齐, 使 x 轴连续覆盖整个选中区间(对齐 Kibana 行为)。
	result = fillEmptyBuckets(result, q, bucketMs)

	return result, nil
}

// Terms 字段聚合（对标 ES terms aggregation）
func (s *Store) Terms(q *LogTermsQuery) (*TermsResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	start := time.Now()

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

	// 执行聚合查询
	size := q.Size
	if size <= 0 {
		size = 10 // 默认 top 10
	}
	if size > 100 {
		size = 100 // 最大 100
	}

	query := fmt.Sprintf(`
		SELECT %s, COUNT(*) as cnt
		FROM log_meta %s
		GROUP BY %s
		ORDER BY cnt DESC
		LIMIT ?`, field, wc, field)

	args = append(args, size)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var buckets []TermsBucket
	for rows.Next() {
		var key string
		var count int64
		if err := rows.Scan(&key, &count); err != nil {
			return nil, err
		}
		buckets = append(buckets, TermsBucket{Key: key, Count: count})
	}
	if err := rows.Err(); err != nil {
		return nil, err
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

// ReadRaw 根据 id 读取原始日志内容
func (s *Store) ReadRaw(id int64) (*LogEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	e := &LogEntry{}
	err := s.db.QueryRow(`SELECT id, ts, level, service, source, file_path, offset, size, summary
		FROM log_meta WHERE id = ?`, id).Scan(
		&e.ID, &e.Ts, &e.Level, &e.Service, &e.Source, &e.FilePath, &e.Offset, &e.Size, &e.Summary)
	if err != nil {
		return nil, err
	}
	return e, nil
}

// BulkDelete 批量删除元数据
func (s *Store) BulkDelete(ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	_, err := s.db.Exec("DELETE FROM log_meta WHERE id IN ("+strings.Join(placeholders, ",")+")", args...)
	return err
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
	for rows.Next() {
		idx := &LogIndex{}
		var fields, ilm string
		if err := rows.Scan(&idx.ID, &idx.Name, &idx.Source, &idx.SourcePath, &idx.Service, &fields, &ilm, &idx.DeleteAfter, &idx.CreatedAt, &idx.UpdatedAt); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(fields), &idx.Fields)
		json.Unmarshal([]byte(ilm), &idx.Ilm)
		indexes = append(indexes, idx)
	}
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

// IndexStatsFor 返回指定索引的统计 + 当前存储阶段
func (s *Store) IndexStatsFor(id string) (*IndexStats, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	st := &IndexStats{}
	err := s.db.QueryRow("SELECT COUNT(*), COALESCE(SUM(size),0), MIN(ts), MAX(ts) FROM log_meta WHERE index_id = ?", id).
		Scan(&st.DocCount, &st.Bytes, &st.Oldest, &st.Newest)
	if err != nil {
		return nil, err
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

// ApplyIlm 执行 ILM 冷热归档淘汰：
//   1) 按各索引保留期算出截止日期
//   2) 删除该日及之前的 SQLite 元数据行
//   3) 返回每个索引的 (indexID, cutoffDate, rows) 计划, 由上层联动删除归档文件
func (s *Store) ApplyIlm() ([]IlmCleanup, int64, error) {
	s.mu.RLock()
	indexes, err := s.ListIndexes()
	s.mu.RUnlock()
	if err != nil {
		return nil, 0, err
	}

	beyondNow := time.Now().Add(-24 * time.Hour).UnixMilli()
	cleanups := []IlmCleanup{}
	totalRows := int64(0)
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
		beyond := beyondNow - int64(days)*24*3600*1000
		res, err := s.db.Exec("DELETE FROM log_meta WHERE index_id = ? AND ts < ?", idx.ID, beyond)
		if err != nil {
			return cleanups, totalRows, err
		}
		n, _ := res.RowsAffected()
		if n > 0 {
			totalRows += n
			cutoff := time.UnixMilli(beyond).Format("2006-01-02")
			cleanups = append(cleanups, IlmCleanup{IndexID: idx.ID, CutoffDate: cutoff, Rows: n})
		}
	}
	return cleanups, totalRows, nil
}

// ApplyIlmAll 无索引归属的日志按全局保留期清理（default 索引）
func (s *Store) ApplyIlmAll() (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cutoff := time.Now().Add(-24 * time.Hour).UnixMilli()
	res, err := s.db.Exec("DELETE FROM log_meta WHERE ts < ? AND index_id = ''", cutoff)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
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
