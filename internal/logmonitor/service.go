package logmonitor

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// 日志级别
const (
	LevelDEBUG = "DEBUG"
	LevelINFO  = "INFO"
	LevelWARN  = "WARN"
	LevelERR   = "ERROR"
	LevelFATAL = "FATAL"
)

// Service 日志处理服务
type Service struct {
	store  *Store
	mu     sync.Mutex
	cancel chan struct{}
	// 采集状态
	inFlight atomic.Int64
}

func NewService(store *Store) *Service {
	return &Service{store: store}
}

var (
	// reService 提取服务名：如 [order-api] 或 service=order-api
	reService = regexp.MustCompile(`\[([a-zA-Z0-9\-_\.]+)\]|service[=:]\s*([a-zA-Z0-9\-_\.]+)`)
	// reLevel 提取级别
	reLevel = regexp.MustCompile(`\b(ERROR|WARN|INFO|DEBUG|FATAL)\b`)
	// reTimestamp 时间戳匹配（多种格式）
	reTimestamp = regexp.MustCompile(`(\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:\.\d+)?)`)
)

// ParseLine 解析一行日志为元数据
func (s *Service) ParseLine(line string, filePath string, offset int64, defaultService string, defaultSource string) *LogEntry {
	e := &LogEntry{
		Ts:       nowMs(),
		Level:    LevelINFO,
		Service:  defaultService,
		Source:   defaultSource,
		FilePath: filePath,
		Offset:   offset,
		Size:     len(line),
	}

	// 提取级别
	if m := reLevel.FindString(line); m != "" {
		e.Level = m
	}

	// 提取服务
	if m := reService.FindStringSubmatch(line); m != nil {
		if m[1] != "" {
			e.Service = m[1]
		} else if m[2] != "" {
			e.Service = m[2]
		}
	}

	// 提取时间戳
	if m := reTimestamp.FindStringSubmatch(line); m != nil {
		if t, err := parseLogTime(m[1]); err == nil {
			e.Ts = t
		}
	}

	// 摘要：去首行换行，限长
	summary := strings.TrimSpace(line)
	if len(summary) > 200 {
		summary = summary[:200]
	}
	e.Summary = summary

	return e
}

func parseLogTime(s string) (int64, error) {
	var t time.Time
	formats := []string{
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05.999999999Z07:00",
		"2006-01-02T15:04:05Z07:00",
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05",
	}
	var err error
	for _, f := range formats {
		if t, err = time.Parse(f, s); err == nil {
			return t.UnixMilli(), nil
		}
	}
	if len(s) > 19 {
		// 逗号小数秒
		if t, e := time.Parse("2006-01-02 15:04:05.999999999", strings.Replace(s, ",", ".", 1)); e == nil {
			return t.UnixMilli(), nil
		}
	}
	return 0, fmt.Errorf("cannot parse time: %s", s)
}

// Ingest 单条日志写入（供 HTTP API 使用）
func (s *Service) Ingest(line, service, source string) (*LogEntry, error) {
	e := s.ParseLine(line, "http-ingest", 0, service, source)
	err := s.store.InsertBatch([]*LogEntry{e})
	return e, err
}

// IngestBatch 批量写入
func (s *Service) IngestBatch(lines []string, service, source string) (int, error) {
	// 用户有没有指定 service？无则每行自行提取
	entries := make([]*LogEntry, 0, len(lines))
	for i, line := range lines {
		entries = append(entries, s.ParseLine(line, "http-ingest", int64(i), service, source))
	}
	err := s.store.InsertBatch(entries)
	return len(entries), err
}

// ScanFile 扫描一个文件（全量或尾部）
func (s *Service) ScanFile(path, defaultService, defaultSource string, tailOnly bool) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	// 获取文件大小
	fi, err := f.Stat()
	if err != nil {
		return 0, err
	}
	total := fi.Size()

	var startOffset int64 = 0
	if tailOnly {
		startOffset = total
	}

	reader := bufio.NewReader(f)
	var entries []*LogEntry
	var offset int64 = 0
	var count int
	lineNum := 0

	for {
		if offset >= total {
			break
		}
		line, err := reader.ReadString('\n')
		if len(line) == 0 && err != nil {
			break
		}
		lineNum++
		var e *LogEntry
		if offset >= startOffset {
			e = s.ParseLine(strings.TrimRight(line, "\r\n"), path, offset, defaultService, defaultSource)
			entries = append(entries, e)
		}
		offset += int64(len(line))
	}
	if len(entries) > 0 {
		s.store.InsertBatch(entries)
		count = len(entries)
	}
	return count, nil
}

// ToJSON 序列化
func ToJSON(v interface{}) ([]byte, error) {
	return json.Marshal(v)
}

// EscapeFile 规范化文件路径
func normalizePath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return abs
}
