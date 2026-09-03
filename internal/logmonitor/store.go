package logmonitor

import (
	"database/sql"
	"fmt"
	"log"
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
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
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
		summary   TEXT    NOT NULL DEFAULT ''
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
	`
	_, err := s.db.Exec(ddl)
	if err != nil {
		return fmt.Errorf("migrate log_meta: %w", err)
	}

	// 自动创建数据目录
	log.Println("[logmonitor] store migrated OK")
	return nil
}

// InsertBatch 批量写入元数据索引
func (s *Store) InsertBatch(entries []*LogEntry) error {
	if len(entries) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(`INSERT INTO log_meta (ts, level, service, source, file_path, offset, size, summary)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer stmt.Close()

	for _, e := range entries {
		_, err := stmt.Exec(e.Ts, e.Level, e.Service, e.Source, e.FilePath, e.Offset, e.Size, e.Summary)
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("insert log_meta: %w", err)
		}
	}
	return tx.Commit()
}

// Query 多条件查询（走复合索引）
func (s *Store) Query(q *LogQuery) (*LogQueryResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	start := time.Now()

	where := []string{}
	args := []interface{}{}

	if q.Service != "" {
		where = append(where, "service = ?")
		args = append(args, q.Service)
	}
	if q.Level != "" {
		levels := strings.Split(q.Level, ",")
		placeholders := make([]string, len(levels))
		for i, l := range levels {
			placeholders[i] = "?"
			args = append(args, strings.TrimSpace(l))
		}
		where = append(where, "level IN ("+strings.Join(placeholders, ",")+")")
	}
	if q.Source != "" {
		where = append(where, "source = ?")
		args = append(args, q.Source)
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

	querySQL := `SELECT id, ts, level, service, source, file_path, offset, size, summary
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
		if err := rows.Scan(&e.ID, &e.Ts, &e.Level, &e.Service, &e.Source, &e.FilePath, &e.Offset, &e.Size, &e.Summary); err != nil {
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
	query := fmt.Sprintf(`
		SELECT (ts / %d) * %d AS bucket, level, COUNT(*)
		FROM log_meta %s
		GROUP BY bucket, level
		ORDER BY bucket
	`, bucketMs, bucketMs, wc)

	rows, err := s.db.Query(query, args...)
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
	return result, nil
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

	rows, err := s.db.Query("SELECT id, name, type, path, service, enabled, follow FROM log_sources ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	sources := []*LogSource{}
	for rows.Next() {
		src := &LogSource{}
		rows.Scan(&src.ID, &src.Name, &src.Type, &src.Path, &src.Service, &src.Enabled, &src.Follow)
		sources = append(sources, src)
	}
	return sources, nil
}

func (s *Store) SaveSource(src *LogSource) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`INSERT OR REPLACE INTO log_sources (id, name, type, path, service, enabled, follow)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		src.ID, src.Name, src.Type, src.Path, src.Service, src.Enabled, src.Follow)
	return err
}

func (s *Store) DeleteSource(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec("DELETE FROM log_sources WHERE id = ?", id)
	return err
}

func (s *Store) Close() {
	if s.db != nil {
		s.db.Close()
	}
}
