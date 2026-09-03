package logmonitor

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"
)

// ArchiveDirRel 归档文件根目录（相对 dataDir 的 logs/archive）
const ArchiveDirRel = "logs/archive"

// Archiver 原文归档管理器（zstd 压缩，按 index+日期 追加写）
// 存储布局:
//   data/logs/archive/<indexID>/<YYYY-MM-DD>.log.zst
//   每行: <id> <tsMs> <level> <content>    (空格分隔, content 其余为原文, 已去换行)
//   id 即 SQLite log_meta.id, 用于随机定位单条
//   level 用于级别着色回显
//
// 设计原则：
//   - 每命中一条日志双写：原文追加进归档 + 元数据进 SQLite。
//   - 归档按 index+日期分文件，跨日自动开新文件。
//   - ILM delete 阶段按保留期删除整个 index 目录(联动)。
//   - 每次追加采用"打开→写→关闭"，简单可靠(避免句柄跨日/进位泄漏)。
type Archiver struct {
	root string
	mu   sync.Mutex
}

func NewArchiver(dataDir string) (*Archiver, error) {
	root := filepath.Join(dataDir, ArchiveDirRel)
	if err := os.MkdirAll(root, 0755); err != nil {
		return nil, fmt.Errorf("create archive dir: %w", err)
	}
	return &Archiver{root: root}, nil
}

func dayStr(ts int64) string {
	return time.UnixMilli(ts).Format("2006-01-02")
}

func (a *Archiver) pathFor(indexID, date string) string {
	return filepath.Join(a.root, indexID, date+".log.zst")
}

// appendLine 追加一条日志原文到 <indexID>/<date>.log.zst
// 每行格式: <id> <ts> <level> <content>
func (a *Archiver) appendLine(entry *LogEntry) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	date := dayStr(entry.Ts)
	dir := filepath.Join(a.root, entry.IndexID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	p := a.pathFor(entry.IndexID, date)

	content := strings.ReplaceAll(entry.Summary, "\n", " ")
	line := fmt.Sprintf("%d %d %s %s\n", entry.ID, entry.Ts, entry.Level, content)

	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	wr, err := zstd.NewWriter(f, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		return err
	}
	if _, err := wr.Write([]byte(line)); err != nil {
		wr.Close()
		return err
	}
	return wr.Close()
}

// appendBatch 批量追加（一次开文件写多条，减少 IO）
func (a *Archiver) appendBatch(entries []*LogEntry) error {
	if len(entries) == 0 {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	// 按 (indexID,date) 分组
	groups := map[string][]*LogEntry{}
	var order []string
	for _, e := range entries {
		date := dayStr(e.Ts)
		key := e.IndexID + "|" + date
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], e)
	}

	for _, key := range order {
		idxDate := strings.SplitN(key, "|", 2)
		indexID, date := idxDate[0], idxDate[1]
		dir := filepath.Join(a.root, indexID)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
		p := a.pathFor(indexID, date)
		f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return err
		}
		wr, werr := zstd.NewWriter(f, zstd.WithEncoderLevel(zstd.SpeedDefault))
		if werr != nil {
			f.Close()
			return werr
		}
		for _, e := range groups[key] {
			content := strings.ReplaceAll(e.Summary, "\n", " ")
			wr.Write([]byte(fmt.Sprintf("%d %d %s %s\n", e.ID, e.Ts, e.Level, content)))
		}
		err = wr.Close()
		f.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// readByID 按 id 读取归档中的原文；找不到返回空串
// 需要知道该条目属于哪个日期文件——通过 ts 推断日期
func (a *Archiver) readByID(indexID string, ts int64, id int64) string {
	p := a.pathFor(indexID, dayStr(ts))
	f, err := os.Open(p)
	if err != nil {
		return ""
	}
	defer f.Close()

	dc, err := zstd.NewReader(f, zstd.WithDecoderConcurrency(1))
	if err != nil {
		return ""
	}
	defer dc.Close()

	sc := bufio.NewScanner(dc)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	prefix := fmt.Sprintf("%d ", id)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, prefix) {
			// 去掉 "<id> <ts> <level> " 前缀
			parts := strings.SplitN(line, " ", 4)
			if len(parts) == 4 {
				return parts[3]
			}
			return ""
		}
	}
	return ""
}

// deleteIndex 删除某索引所有归档文件（ILM delete 联动，整删场景）
func (a *Archiver) deleteIndex(indexID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return os.RemoveAll(filepath.Join(a.root, indexID))
}

// deleteBeforeDate 删除某索引中早于(含)保留截止日期的归档文件，返回删除的文件数
// cutoffDate 形如 "2026-08-01"：删除所有 < cutoffDate 或 == cutoffDate 的 .log.zst
func (a *Archiver) deleteBeforeDate(indexID string, cutoffDate string) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	dir := filepath.Join(a.root, indexID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	var deleted int
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".log.zst") {
			continue
		}
		date := strings.TrimSuffix(e.Name(), ".log.zst")
		if date <= cutoffDate {
			if err := os.Remove(filepath.Join(dir, e.Name())); err == nil {
				deleted++
			}
		}
	}
	return deleted, nil
}

// listIndexFiles 列出某索引的归档文件（按日期）
func (a *Archiver) listIndexFiles(indexID string) ([]string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	dir := filepath.Join(a.root, indexID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".log.zst") {
			files = append(files, e.Name())
		}
	}
	return files, nil
}

// Close 释放编解码器（保留已写文件）
func (a *Archiver) Close() {}