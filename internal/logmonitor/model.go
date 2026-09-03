package logmonitor

import "time"

// LogEntry 元数据索引条目（存 SQLite）
type LogEntry struct {
	ID       int64  `json:"id"`
	Ts       int64  `json:"ts"`       // 毫秒时间戳
	Level    string `json:"level"`    // ERROR/WARN/INFO/DEBUG
	Service  string `json:"service"`  // 服务名
	Source   string `json:"source"`   // 来源：file/container/syslog/http
	FilePath string `json:"filePath"` // 实际日志文件路径
	Offset   int64  `json:"offset"`   // 文件内偏移
	Size     int    `json:"size"`     // 该条日志字节数
	Summary  string `json:"summary"`  // 前 200 字符摘要
	Raw      string `json:"raw,omitempty"` // 实际内容（按需填充）
}

// LogQuery 查询条件
type LogQuery struct {
	Service  string `json:"service"`
	Level    string `json:"level"`    // 逗号分隔：ERROR,WARN
	Source   string `json:"source"`
	Keyword  string `json:"keyword"`  // 摘要/原文模糊搜索
	StartTs  int64  `json:"startTs"`  // 毫秒
	EndTs    int64  `json:"endTs"`    // 毫秒
	Page     int    `json:"page"`     // 从 1 开始
	PageSize int    `json:"pageSize"` // 默认 100
}

// LogQueryResult 查询结果
type LogQueryResult struct {
	Total  int64      `json:"total"`
	Items  []*LogEntry `json:"items"`
	TookMs float64    `json:"tookMs"` // 查询耗时 ms
}

// LogStats 统计信息
type LogStats struct {
	TotalCount  int64            `json:"totalCount"`
	LevelCounts map[string]int64 `json:"levelCounts"`
	Services    []ServiceStat    `json:"services"`
	Oldest      *int64           `json:"oldest,omitempty"` // 最早日志时间戳
	Newest      *int64           `json:"newest,omitempty"` // 最新日志时间戳
	TotalBytes  int64            `json:"totalBytes"`
}

type ServiceStat struct {
	Service string `json:"service"`
	Count   int64  `json:"count"`
	Levels  map[string]int64 `json:"levels"`
}

// LogSource 日志源配置
type LogSource struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`    // file/syslog/http/container
	Path    string `json:"path"`    // 文件路径 / 容器名 / URL
	Service string `json:"service"` // 标签：属于哪个服务
	Enabled bool   `json:"enabled"`
	// file 类型特有
	Follow bool `json:"follow"` // 是否持续跟踪（tail -f 模式）
}

// LogStatsQuery 统计查询参数
type LogStatsQuery struct {
	Service string `json:"service"`
	StartTs int64  `json:"startTs"`
	EndTs   int64  `json:"endTs"`
}

// BulkDeleteRequest 批量删除
type BulkDeleteRequest struct {
	IDs []int64 `json:"ids"`
}

// LogStatsResult 包含 Histogram
type LogStatsResult struct {
	Stats      *LogStats       `json:"stats"`
	Histogram  []HistogramBucket `json:"histogram"`
}

type HistogramBucket struct {
	Ts    int64            `json:"ts"`    // 桶起始时间
	Count map[string]int64 `json:"count"` // 各级别计数
}

func nowMs() int64 {
	return time.Now().UnixNano() / int64(time.Millisecond)
}
