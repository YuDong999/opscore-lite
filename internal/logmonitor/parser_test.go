package logmonitor

import (
	"testing"
	"time"
)

// Go 标准库 log 的默认布局是 "2026/09/24 12:27:06"(斜杠日期), 曾经内置规则只认短横线:
// 这类行时间解析失败, ts 退化成"入库时刻" —— 实测采自己 19,204 行时全部落到同一时刻。
// 这里钉住两件事: 行内时间要解析出来; 方括号里的服务名要提出来。
func TestDefaultParserRulesParseGoLogTimestamp(t *testing.T) {
	compiled, err := compileRules(defaultParserRules())
	if err != nil {
		t.Fatalf("编译内置规则失败: %v", err)
	}
	line := `2026/09/24 12:27:06 [logmonitor] poll 容器 dbx 失败: exec: "docker": executable file not found in %PATH%`
	e := parseOneRule(compiled[0], line, "data/opscore.log", 12345, "opscore", "file", "")

	// 裸时间按本机时区解析(日志时间 = 写日志那台机器的本地时间), 不是 UTC
	want := time.Date(2026, 9, 24, 12, 27, 6, 0, time.Local).UnixMilli()
	if e.Ts != want {
		t.Fatalf("行内时间没被解析: got %d(%s), want %d(%s)", e.Ts, time.UnixMilli(e.Ts).UTC(), want, time.UnixMilli(want).UTC())
	}
	if e.Service != "logmonitor" {
		t.Fatalf("方括号服务名没被提取: got %q", e.Service)
	}
	if e.Summary != line {
		t.Fatalf("summary 应为整行: got %q", e.Summary)
	}
}
