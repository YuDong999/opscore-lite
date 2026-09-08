package logmonitor

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
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
	store    *Store
	archiver *Archiver
	dataDir  string
	mu       sync.Mutex
	cancel   chan struct{}
	// 采集状态
	inFlight atomic.Int64
}

func NewService(store *Store, archiver *Archiver) *Service {
	return &Service{store: store, archiver: archiver, cancel: make(chan struct{})}
}

// Start 启动后台持续采集：每 10s 对 log_sources 中 enabled+follow 的源做增量采集。
func (s *Service) Start(dataDir string) {
	s.dataDir = dataDir
	s.ValidateCursors()
	go s.pollLoop(10 * time.Second)
}

// cursorFuzzMs 游标漂移容忍度: 超过「当前时间+该值」的 last_ts 视为时钟错乱(集群/宿主机曾快进
// 留下的未来时间戳), 会导致 poller 用 --since-time 永远抓不到新日志。启动时自动重置。
const cursorFuzzMs = 2 * 60 * 60 * 1000 // 2h

// ValidateCursors 启动自检: 将 all enabled+follow 源里明显未来的 last_ts 重置为当前时间。
// 返回修正的源个数。
func (s *Service) ValidateCursors() int {
	sources, err := s.store.ListSources()
	if err != nil {
		log.Printf("[logmonitor] ValidateCursors 读取源失败: %v", err)
		return 0
	}
	now := time.Now().UnixMilli()
	fixed := 0
	for _, src := range sources {
		if !src.Enabled || !src.Follow {
			continue
		}
		if src.LastTs > now+cursorFuzzMs {
			_ = s.store.AdvanceSourceCursor(src.ID, now-180000) // 落后3分钟, 避免与真实日志ts相等被去重吞掉
			log.Printf("[logmonitor] 游标自检: %s last_ts=%d 超未来值, 重置为当前时间", src.ID, src.LastTs)
			fixed++
		}
	}
	if fixed > 0 {
		log.Printf("[logmonitor] 游标自检完成: 修正 %d 个源", fixed)
	}
	return fixed
}

func (s *Service) Stop() {
	select {
	case <-s.cancel:
	default:
		close(s.cancel)
	}
}

func (s *Service) pollLoop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-s.cancel:
			return
		case <-ticker.C:
			s.poll()
		}
	}
}

// poll 对每个 follow 源增量采集。已归档（有 last_ts 游标）的源按游标增量，首采全量 tail。
func (s *Service) poll() {
	if s.inFlight.Load() > 0 {
		return // 上一轮未完成，跳过防并发攒批
	}
	s.inFlight.Add(1)
	defer s.inFlight.Add(-1)

	sources, err := s.store.ListSources()
	if err != nil {
		return
	}
	for _, src := range sources {
		if !src.Enabled || !src.Follow {
			continue
		}
		switch src.Type {
		case "container":
			s.pollContainer(src)
		case "k8s", "k8spod":
			s.pollK8s(src)
		}
	}
}

func (s *Service) pollContainer(src *LogSource) {
	lines, err := CollectDockerLogsSince(src.Path, src.LastTs)
	if err != nil {
		return
	}
	s.ingestIncremental(src, lines, "container")
}

func (s *Service) pollK8s(src *LogSource) {
	kc := kubeconfigPathFor(s.dataDir, src.Cluster)
	if kc == "" {
		return
	}
	// 首采(lastTs=0)不带 since-time，取尾巴后续增量; 已有游标则增量
	lines, err := CollectK8sPodLogsSince(kc, src.Namespace, src.Path, src.LastTs)
	if err != nil {
		return
	}
	s.ingestIncremental(src, lines, "k8s")
}

// ingestIncremental 把抓到的行入库，只保留 ts>lastTs 的新行，并推进游标。
func (s *Service) ingestIncremental(src *LogSource, lines []string, source string) {
	entries := make([]*LogEntry, 0, len(lines))
	var maxTs int64 = src.LastTs
	for i, line := range lines {
		e := s.ParseLine(line, "http-ingest", int64(i), src.Service, source, src.IndexID)
		if source == "k8s" && src.Service != "" {
			e.Service = src.Service
		}
		if e.Ts <= src.LastTs {
			continue // 去重：跳过游标前已入库的行
		}
		entries = append(entries, e)
		if e.Ts > maxTs {
			maxTs = e.Ts
		}
	}
	if len(entries) == 0 {
		return
	}
	if src.IndexID != "" && s.archiver != nil {
		_, _ = s.store.InsertBatch(entries)
		_ = s.archiver.appendBatch(entries)
	} else {
		_, _ = s.store.InsertBatch(entries)
	}
	if maxTs > src.LastTs {
		_ = s.store.AdvanceSourceCursor(src.ID, maxTs)
	}
}

// NormalizeSvcName 将 K8S pod 名规整为逻辑服务名，如：
// nginx-6d664c6d47-s7ljj        → nginx
// halo-66859784d5-fvh9r         → halo
// smart-alert-aggregator-b6ccff87d-scsfm → smart-alert-aggregator
// halo-0 (statefulset)          → halo
func NormalizeSvcName(s string) string {
	if s == "" {
		return s
	}
	if m := rePodHash.FindString(s); m != "" {
		return strings.TrimSuffix(s, m)
	}
	if idx := strings.LastIndexByte(s, '-'); idx > 0 {
		if tail, err := strconv.Atoi(s[idx+1:]); err == nil && tail >= 0 {
			return s[:idx]
		}
	}
	return s
}

var (
	// reService 提取服务名：如 [order-api] 或 service=order-api
	reService = regexp.MustCompile(`\[([a-zA-Z0-9\-_\.]+)\]|service[=:]\s*([a-zA-Z0-9\-_\.]+)`)
	// rePodHash 匹配 K8S 生成的 pod 名后缀：deployment pod: <name>-<rs-hash>-<pod-hash>；statefulset pod: <name>-<序号>
	rePodHash = regexp.MustCompile(`-[a-z0-9]{5,10}-[a-z0-9]{4,6}$`)
	// reLevel 提取级别
	reLevel = regexp.MustCompile(`\b(ERROR|WARN|INFO|DEBUG|FATAL)\b`)
	// reTimestamp 时间戳匹配（多种格式）：末尾时区可选, 匹配到 +08:00 时 parseLogTime 会用带时区格式正确换算, 避免把本地时间当 UTC
	reTimestamp = regexp.MustCompile(`(\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:?\d{2})?)`)
)

// ParseLine 解析一行日志为元数据
func (s *Service) ParseLine(line string, filePath string, offset int64, defaultService string, defaultSource string, indexID string) *LogEntry {
	e := &LogEntry{
		Ts:       nowMs(),
		Level:    LevelINFO,
		Service:  defaultService,
		Source:   defaultSource,
		FilePath: filePath,
		Offset:   offset,
		Size:     len(line),
		IndexID:  indexID,
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
	e.Service = NormalizeSvcName(e.Service)

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

// Ingest 单条日志写入（供 HTTP API 使用），若 indexID 非空则同时原文入库归档
func (s *Service) Ingest(line, service, source, indexID string) (*LogEntry, error) {
	e := s.ParseLine(line, "http-ingest", 0, service, source, indexID)
	var err error
	if e.IndexID != "" {
		// 双写：先插入元数据拿 id, 再写归档
		ids, ierr := s.store.InsertBatch([]*LogEntry{e})
		if ierr != nil {
			return e, ierr
		}
		e.ID = ids[0]
		if s.archiver != nil {
			_ = s.archiver.appendLine(e) // 归档失败不阻断(容错)
		}
		return e, nil
	}
	_, err = s.store.InsertBatch([]*LogEntry{e})
	return e, err
}

// IngestBatch 批量写入，indexID 非空则原文入归档
func (s *Service) IngestBatch(lines []string, service, source, indexID string) (int, error) {
	entries := make([]*LogEntry, 0, len(lines))
	for i, line := range lines {
		e := s.ParseLine(line, "http-ingest", int64(i), service, source, indexID)
		// k8s 来源: service 由调用方指定(pod 容器名=服务名), 不被日志行内 [xxx] 提取结果覆盖
		if source == "k8s" && service != "" {
			e.Service = service
		}
		entries = append(entries, e)
	}
	if indexID != "" {
		ids, err := s.store.InsertBatch(entries)
		if err != nil {
			return len(entries), err
		}
		_ = ids
		if s.archiver != nil {
			if err := s.archiver.appendBatch(entries); err != nil {
				// 容错
			}
		}
		return len(entries), nil
	}
	_, err := s.store.InsertBatch(entries)
	return len(entries), err
}

// ScanFile 扫描一个文件（全量或尾部），indexID 非空则原文入归档
func (s *Service) ScanFile(path, defaultService, defaultSource string, tailOnly bool, indexID string) (int, error) {
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
			e = s.ParseLine(strings.TrimRight(line, "\r\n"), path, offset, defaultService, defaultSource, indexID)
			entries = append(entries, e)
		}
		offset += int64(len(line))
	}
	if len(entries) > 0 {
		if indexID != "" {
			if _, err := s.store.InsertBatch(entries); err != nil {
				return 0, err
			}
			if s.archiver != nil {
				if err := s.archiver.appendBatch(entries); err != nil {
					// 容错
				}
			}
		} else {
			if _, err := s.store.InsertBatch(entries); err != nil {
				return 0, err
			}
		}
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
