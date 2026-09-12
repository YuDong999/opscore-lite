package logmonitor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// ── 可配置解析规则 (parsers.json) ──────────────────────────────
// 仿 fluentd <parse>: 按 source/service/file 匹配, 用正则提取 时间/级别/服务。
// 文件缺失或规则为空时使用内置默认规则(与历史硬编码行为完全一致)。
//
// 示例 parsers.json:
//   {
//     "rules": [
//       {
//         "name": "k8s-json-time",
//         "match": { "sources": ["k8s"] },
//         "time": { "regex": "(\\d{4}-\\d{2}-\\d{2}T\\d{2}:\\d{2}:\\d{2}(?:\\.\\d+)?Z?)",
//                   "formats": ["2006-01-02T15:04:05Z07:00", "2006-01-02T15:04:05.999999999Z07:00"] },
//         "level": { "regex": "\\b(ERROR|WARN|INFO|DEBUG|FATAL)\\b", "default": "INFO" },
//         "service": { "normalize": true },
//         "summary": { "maxLen": 200 }
//       }
//     ]
//   }

type RuleMatch struct {
	Sources  []string `json:"sources"`  // 匹配的 source 值 (container/k8s/file/...), 空=任意
	Services []string `json:"services"` // 匹配的默认服务名, 空=任意
	Files    []string `json:"files"`    // 文件路径子串匹配, 空=任意
}

type TimeRule struct {
	Regex    string   `json:"regex"`             // 提取时间文本, 要求第 1 个捕获组为时间串
	Formats  []string `json:"formats"`           // Go 布局列表; 空=内置通用列表(含逗号小数秒兜底)
	Timezone string   `json:"timezone,omitempty"` // 空=按 UTC(与历史一致); 如 "Asia/Shanghai"
}

type LevelRule struct {
	Regex   string `json:"regex"`   // 提取级别; 要求第 1 捕获组
	Default string `json:"default"` // 未匹配时的默认级别
}

type ServiceRule struct {
	Regex     string `json:"regex"`     // 提取服务名; 空=不提取(沿用默认服务名)
	Normalize bool   `json:"normalize"` // 去掉 pod 随机后缀 (nginx-xx-abc → nginx)
}

type SummaryRule struct {
	MaxLen int `json:"maxLen"` // 摘要截断长度, <=0 用 200
}

type ParserRule struct {
	Name    string       `json:"name"`
	Match   RuleMatch    `json:"match"`
	Time    TimeRule     `json:"time"`
	Level   LevelRule    `json:"level"`
	Service ServiceRule  `json:"service"`
	Summary SummaryRule  `json:"summary"`
}

type parserFile struct {
	Rules []ParserRule `json:"rules"`
}

type compiledRule struct {
	src             ParserRule
	timeRe, lvlRe   *regexp.Regexp
	svcRe           *regexp.Regexp
	timeFormats     []string
}

// ParserRuleSet 规则集: 启动加载 + reload 热载; 失败时回退内置默认并在 Err 中暴露原因。
type ParserRuleSet struct {
	path string
	mu   sync.RWMutex
	rules []compiledRule
	builtin []compiledRule
	Err  string // 最近一次加载错误(空=正常)
}

// defaultParserRules 内置默认规则 —— 等价历史硬编码行为
func defaultParserRules() []ParserRule {
	return []ParserRule{
		{
			Name:  "default",
			Match: RuleMatch{},
			Time: TimeRule{
				Regex: `(\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:?\d{2})?)`,
			},
			Level: LevelRule{
				Regex:   `\b(ERROR|WARN|INFO|DEBUG|FATAL)\b`,
				Default: "INFO",
			},
			Service: ServiceRule{
				Regex:     `\[([a-zA-Z0-9\-_\.]+)\]|service[=:]\s*([a-zA-Z0-9\-_\.]+)`,
				Normalize: true,
			},
			Summary: SummaryRule{MaxLen: 200},
		},
	}
}

func compileRules(src []ParserRule) ([]compiledRule, error) {
	out := make([]compiledRule, 0, len(src))
	for _, r := range src {
		c := compiledRule{src: r}
		if r.Time.Regex != "" {
			re, err := regexp.Compile(r.Time.Regex)
			if err != nil {
				return nil, fmt.Errorf("规则 %q 时间正则错误: %v", r.Name, err)
			}
			c.timeRe = re
		}
		if r.Level.Regex != "" {
			re, err := regexp.Compile(r.Level.Regex)
			if err != nil {
				return nil, fmt.Errorf("规则 %q 级别正则错误: %v", r.Name, err)
			}
			c.lvlRe = re
		}
		if r.Service.Regex != "" {
			re, err := regexp.Compile(r.Service.Regex)
			if err != nil {
				return nil, fmt.Errorf("规则 %q 服务正则错误: %v", r.Name, err)
			}
			c.svcRe = re
		}
		c.timeFormats = r.Time.Formats
		if len(c.timeFormats) == 0 {
			c.timeFormats = builtinTimeFormats
		}
		out = append(out, c)
	}
	return out, nil
}

var builtinTimeFormats = []string{
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04:05",
	"2006-01-02T15:04:05.999999999Z07:00",
	"2006-01-02T15:04:05Z07:00",
}

func NewParserRuleSet(path string) *ParserRuleSet {
	builtin, _ := compileRules(defaultParserRules())
	ps := &ParserRuleSet{path: path, builtin: builtin}
	ps.rules = builtin
	ps.reload()
	go ps.watch()
	return ps
}

// watch 后台每 15s 检查规则文件 mtime, 有变化则热重载
func (ps *ParserRuleSet) watch() {
	var last time.Time
	for {
		time.Sleep(15 * time.Second)
		fi, err := os.Stat(ps.path)
		if err != nil {
			continue
		}
		if !fi.ModTime().After(last) {
			continue
		}
		last = fi.ModTime()
		ps.reload()
	}
}

// reload 重读规则文件; 失败时保留旧规则并记录 Err
func (ps *ParserRuleSet) reload() {
	b, err := os.ReadFile(ps.path)
	if err != nil {
		ps.mu.Lock()
		ps.Err = ""
		ps.rules = ps.builtin
		ps.mu.Unlock()
		return
	}
	var pf parserFile
	if err := json.Unmarshal(b, &pf); err != nil {
		ps.mu.Lock()
		ps.Err = "parsers.json 解析失败: " + err.Error()
		ps.mu.Unlock()
		return
	}
	if len(pf.Rules) == 0 {
		// 空规则列表 = 用内置默认(便于用户留空文件回退)
		ps.mu.Lock()
		ps.Err = ""
		ps.rules = ps.builtin
		ps.mu.Unlock()
		return
	}
	compiled, err := compileRules(pf.Rules)
	if err != nil {
		ps.mu.Lock()
		ps.Err = err.Error()
		ps.mu.Unlock()
		return
	}
	ps.mu.Lock()
	ps.rules = compiled
	ps.Err = ""
	ps.mu.Unlock()
}

// LoadedRules 返回当前生效规则(用于前端展示/编辑)
func (ps *ParserRuleSet) LoadedRules() []ParserRule {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	out := make([]ParserRule, 0, len(ps.rules))
	for _, c := range ps.rules {
		out = append(out, c.src)
	}
	return out
}

// IsBuiltin 当前使用内置默认(false=用户自定义文件已生效)
func (ps *ParserRuleSet) IsBuiltin() bool {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	if len(ps.rules) != len(ps.builtin) {
		return false
	}
	for i := range ps.rules {
		if ps.rules[i].src.Name != ps.builtin[i].src.Name {
			return false
		}
	}
	return true
}

func (ps *ParserRuleSet) save(rules []ParserRule) error {
	b, _ := json.MarshalIndent(parserFile{Rules: rules}, "", "  ")
	if err := os.MkdirAll(filepath.Dir(ps.path), 0755); err != nil {
		return err
	}
	if err := os.WriteFile(ps.path, b, 0644); err != nil {
		return err
	}
	ps.reload()
	return nil
}

// Save 写规则文件并立即热载(供前端"保存"使用)
func (ps *ParserRuleSet) Save(rules []ParserRule) error {
	return ps.save(rules)
}

func ruleMatches(r ParserRule, source, service, filePath string) bool {
	if m := r.Match.Sources; len(m) > 0 {
		ok := false
		for _, x := range m {
			if strings.EqualFold(x, source) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if m := r.Match.Services; len(m) > 0 {
		ok := false
		for _, x := range m {
			if x == service {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if m := r.Match.Files; len(m) > 0 {
		ok := false
		for _, x := range m {
			if strings.Contains(filePath, x) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// matchRule 返回首个匹配规则(未命中时回退内置默认)
func (ps *ParserRuleSet) matchRule(source, service, filePath string) compiledRule {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	for _, c := range ps.rules {
		if ruleMatches(c.src, source, service, filePath) {
			return c
		}
	}
	// 用户规则无一命中时回退内置默认, 保证总能解析
	for _, c := range ps.builtin {
		if ruleMatches(c.src, source, service, filePath) {
			return c
		}
	}
	return ps.builtin[len(ps.builtin)-1]
}

// Parse 用规则集解析一行日志(替代原硬编码 ParseLine)
func (ps *ParserRuleSet) Parse(line, filePath string, offset int64, defaultService, defaultSource, indexID string) *LogEntry {
	rule := ps.matchRule(defaultSource, defaultService, filePath)
	return parseOneRule(rule, line, filePath, offset, defaultService, defaultSource, indexID)
}

// parseOneRule 用单条已编译规则解析(测试接口复用)
func parseOneRule(rule compiledRule, line, filePath string, offset int64, defaultService, defaultSource, indexID string) *LogEntry {
	e := &LogEntry{
		Ts:       time.Now().UnixMilli(),
		Level:    "INFO",
		Service:  defaultService,
		Source:   defaultSource,
		FilePath: filePath,
		Offset:   offset,
		Size:     len(line),
		IndexID:  indexID,
	}

	if rule.timeRe != nil {
		if m := rule.timeRe.FindStringSubmatch(line); m != nil {
			if t, err := parseLogTimeWith(m[1], rule.timeFormats, rule.src.Time.Timezone); err == nil {
				e.Ts = t
			}
		}
	}
	if rule.lvlRe != nil {
		if m := rule.lvlRe.FindStringSubmatch(line); m != nil {
			e.Level = strings.ToUpper(m[1])
		} else if d := rule.src.Level.Default; d != "" {
			e.Level = strings.ToUpper(d)
		}
	}
	if rule.svcRe != nil {
		if m := rule.svcRe.FindStringSubmatch(line); m != nil {
			for _, g := range m[1:] {
				if g != "" {
					e.Service = g
					break
				}
			}
		}
	}
	if rule.src.Service.Normalize {
		e.Service = NormalizeSvcName(e.Service)
	}

	maxLen := rule.src.Summary.MaxLen
	if maxLen <= 0 {
		maxLen = 200
	}
	summary := strings.TrimSpace(line)
	if len(summary) > maxLen {
		summary = summary[:maxLen]
	}
	e.Summary = summary

	return e
}

// parseLogTimeWith 按指定布局与时区解析; 布局空用内置列表(含逗号小数秒兜底)
func parseLogTimeWith(s string, formats []string, tz string) (int64, error) {
	loc := time.UTC
	if tz != "" {
		if l, err := time.LoadLocation(tz); err == nil {
			loc = l
		}
	}
	parse := func(f string, v ...string) (time.Time, error) {
		val := s
		if len(v) > 0 {
			val = v[0]
		}
		if loc == time.UTC {
			return time.Parse(f, val)
		}
		return time.ParseInLocation(f, val, loc)
	}
	for _, f := range formats {
		if t, err := parse(f); err == nil {
			return t.UnixMilli(), nil
		}
	}
	commaS := strings.Replace(s, ",", ".", 1)
	if len(s) > 19 && commaS != s {
		if t, err := parse("2006-01-02 15:04:05.999999999", commaS); err == nil {
			return t.UnixMilli(), nil
		}
	}
	return 0, fmt.Errorf("cannot parse time: %s", s)
}