package logmonitor

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
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
	if q.IndexID != "" {
		where = append(where, "index_id = ?")
		args = append(args, q.IndexID)
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

	// 若指定了时间范围, 把窗口内所有空桶补齐, 使 x 轴连续覆盖整个选中区间(对齐 Kibana 行为)。
	result = fillEmptyBuckets(result, q, bucketMs)

	return result, nil
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
	// 上限保护: 超过 maxBuckets 个桶则按比例降采样到 maxBuckets
	const maxBuckets = 1000
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
