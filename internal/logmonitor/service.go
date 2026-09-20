package logmonitor

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
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
	parsers  *ParserRuleSet
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
	// 解析规则集: <base>/parsers.json (dataDir=<base>/logs), 缺失时用内置默认规则
	s.parsers = NewParserRuleSet(filepath.Join(filepath.Dir(dataDir), "parsers.json"))
	s.ValidateCursors()
	go s.pollLoop(10 * time.Second)
	go s.ilmAutoLoop()
}

// ilmAutoLoop 每小时自动执行一次 ILM 淘汰(按各索引 delete_after + 全局保留), 定时清数据
func (s *Service) ilmAutoLoop() {
	ticker := time.NewTicker(time.Hour)
	for {
		select {
		case <-s.cancel:
			return
		case <-ticker.C:
			if _, _, err := s.store.ApplyIlm(); err != nil {
				log.Printf("[logmonitor] 自动 ILM 失败: %v", err)
			}
			if _, err := s.store.ApplyIlmAll(); err != nil {
				log.Printf("[logmonitor] 自动 ILM(unassigned) 失败: %v", err)
			}
		}
	}
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
		if src.Type == "file" {
			continue // 文件源游标是字节偏移, 与墙钟无关, 不参与"未来时间戳"自检
		}
		if src.LastTs > now+cursorFuzzMs {
			_ = s.store.AdvanceTimeCursor(src.ID, now-180000) // 落后3分钟, 避免与真实日志ts相等被去重吞掉
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
		log.Printf("[logmonitor] poll 列出日志源失败: %v", err)
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
		case "file":
			s.pollFile(src)
		}
	}
}

// pollFile 文件源增量采集: last_ts 是字节偏移游标, file_ino 是该文件的身份(dev:ino)。
// 轮转判据以文件身份为准 —— 身份变即"换了文件"(rename/create), 旧游标对新文件无意义, 归零从头采;
// 身份不变而 size < 游标才是原地截断(copytruncate), 同样归零。
// 单轮最多读 2MB 防爆内存; 末尾不足一行的残段既不入库也不推进游标(留到下一轮补齐)。
// 不走 ingestIncremental —— 那条链路用时间戳去重(e.Ts<=lastTs 跳过), 与字节偏移游标语义冲突。
func (s *Service) pollFile(src *LogSource) {
	f, err := os.Open(src.Path)
	if err != nil {
		return // 文件暂不存在(轮转间隙), 静默等下一轮
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return
	}
	size := fi.Size()
	ino := fileIdentity(fi)
	cursor := src.LastTs
	rotated := false
	switch {
	case ino != "" && src.FileIno != "" && ino != src.FileIno:
		cursor, rotated = 0, true // 轮转: 同名但已是另一个文件
	case ino == "" || src.FileIno == "":
		if cursor > size {
			cursor = 0 // 无身份可比(Windows/特殊文件系统或首采)时退回 size 猜测
		}
	}
	if size <= cursor {
		if rotated || ino != src.FileIno {
			_ = s.store.SetFileCursor(src.ID, cursor, ino) // 没有新行也要把身份落下来, 否则每轮都误判轮转
		}
		return
	}
	const maxRead = int64(2 << 20)
	start := cursor
	catchUp := false
	if remain := size - cursor; remain > maxRead {
		start = size - maxRead // 追平保护: 只取最近 2MB
		catchUp = true
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return
	}
	reader := bufio.NewReader(io.LimitReader(f, size-start))
	var entries []*LogEntry
	off := start
	// 追平起点可能落在行中间, 那第一块是缺了行头的残段: 跨过它但不入库
	if catchUp && start > 0 && !atLineStart(f, start) {
		line, rerr := reader.ReadString('\n')
		off += int64(len(line))
		if rerr != nil {
			_ = s.store.SetFileCursor(src.ID, off, ino)
			return
		}
	}
	for {
		line, rerr := reader.ReadString('\n')
		if !strings.HasSuffix(line, "\n") {
			break // 末尾半行: 游标停在最后一个完整行尾
		}
		if e := s.ParseLine(strings.TrimRight(line, "\r\n"), src.Path, off, src.Service, src.Type, src.IndexID); e != nil {
			entries = append(entries, e)
		}
		off += int64(len(line))
		if rerr != nil {
			break
		}
	}
	s.insertAndArchive(src, entries)
	if off > cursor || ino != src.FileIno {
		_ = s.store.SetFileCursor(src.ID, off, ino)
	}
}

// atLineStart 判断 off 是否恰好在行首(前一个字节是换行)。用 ReadAt 取字节, 不影响顺序读位置。
func atLineStart(f *os.File, off int64) bool {
	b := make([]byte, 1)
	if _, err := f.ReadAt(b, off-1); err != nil {
		return false
	}
	return b[0] == '\n'
}

// insertAndArchive 先入库再归档。归档只送真正落库的行: 文件幂等索引吸收掉的重复行没有 id,
// 连着送会把原文映射到别的行号上, raw 回看就串台。
func (s *Service) insertAndArchive(src *LogSource, entries []*LogEntry) {
	if len(entries) == 0 {
		return
	}
	if _, err := s.store.InsertBatch(entries); err != nil {
		log.Printf("[logmonitor] 源 %s 入库失败: %v", src.ID, err)
	}
	if src.IndexID == "" || s.archiver == nil {
		return
	}
	fresh := make([]*LogEntry, 0, len(entries))
	for _, e := range entries {
		if e.ID > 0 {
			fresh = append(fresh, e)
		}
	}
	if len(fresh) > 0 {
		_ = s.archiver.appendBatch(fresh)
	}
}

func (s *Service) pollContainer(src *LogSource) {
	lines, err := CollectDockerLogsSince(src.Path, src.LastTs)
	if err != nil {
		// 采集命令已带 15s 硬超时, 不会永久挂起; 失败必须留痕(此前静默 return, 故障不可见)
		log.Printf("[logmonitor] poll 容器 %s 失败: %v", src.Path, err)
		return
	}
	s.ingestIncremental(src, lines, "container")
}

func (s *Service) pollK8s(src *LogSource) {
	kc := kubeconfigPathFor(s.dataDir, src.Cluster)
	if kc == "" {
		log.Printf("[logmonitor] poll pod %s/%s 失败: 集群 %s 无 kubeconfig", src.Namespace, src.Path, src.Cluster)
		return
	}
	// 首采(lastTs=0)不带 since-time，取尾巴后续增量; 已有游标则增量
	lines, err := CollectK8sPodLogsSince(kc, src.Namespace, src.Path, src.LastTs)
	if err != nil {
		log.Printf("[logmonitor] poll pod %s/%s 失败: %v", src.Namespace, src.Path, err)
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
	s.insertAndArchive(src, entries)
	if maxTs > src.LastTs {
		_ = s.store.AdvanceTimeCursor(src.ID, maxTs)
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

// ParseLine 解析一行日志为元数据（委托可配置规则集; 规则集未就绪时回退内置默认）
func (s *Service) ParseLine(line string, filePath string, offset int64, defaultService string, defaultSource string, indexID string) *LogEntry {
	if s.parsers != nil {
		return s.parsers.Parse(line, filePath, offset, defaultService, defaultSource, indexID)
	}
	tmp, _ := compileRules(defaultParserRules())
	return parseOneRule(tmp[0], line, filePath, offset, defaultService, defaultSource, indexID)
}

// ── 解析规则管理(前端可视化编辑/测试) ──

// ParserInfo 当前规则状态
func (s *Service) ParserInfo() (rules []ParserRule, builtin bool, errStr string) {
	if s.parsers == nil {
		return defaultParserRules(), true, ""
	}
	return s.parsers.LoadedRules(), s.parsers.IsBuiltin(), s.parsers.Err
}

// ParserSave 保存规则文件并热载
func (s *Service) ParserSave(rules []ParserRule) error {
	if s.parsers == nil {
		return fmt.Errorf("规则集未初始化")
	}
	return s.parsers.Save(rules)
}

// ParserTestResult 单行测试结果
type ParserTestResult struct {
	Line    string `json:"line"`
	Ts      int64  `json:"ts"`
	Level   string `json:"level"`
	Service string `json:"service"`
	Summary string `json:"summary"`
	Error   string `json:"error,omitempty"`
}

// ParserTest 用给定单条规则测试解析(不改全局规则)
func (s *Service) ParserTest(rule ParserRule, lines []string) []ParserTestResult {
	compiled, err := compileRules([]ParserRule{rule})
	out := make([]ParserTestResult, 0, len(lines))
	for i, line := range lines {
		if err != nil {
			out = append(out, ParserTestResult{Line: line, Error: err.Error()})
			continue
		}
		e := parseOneRule(compiled[0], line, "parser-test", int64(i), "", "test", "")
		out = append(out, ParserTestResult{Line: line, Ts: e.Ts, Level: e.Level, Service: e.Service, Summary: e.Summary})
	}
	return out
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
