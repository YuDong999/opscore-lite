package logmonitor

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestParseLogTimeTimezone 带时区时间戳必须换算为正确 epoch, 而非把本地时间当 UTC。
func TestParseLogTimeTimezone(t *testing.T) {
	// 2026-09-09T01:27:54+08:00 == 2026-09-08T17:27:54Z
	want := int64(1788888474000)
	cases := []string{
		"2026-09-09T01:27:54+08:00",
		"2026-09-08T17:27:54Z",
		"2026-09-09T01:27:54.608525861+08:00",
	}
	for _, s := range cases {
		got, err := parseLogTime(s)
		if err != nil {
			t.Errorf("parseLogTime(%q): %v", s, err)
			continue
		}
		if got != want && got != want+608 {
			t.Errorf("parseLogTime(%q) = %d, want %d 或 %d", s, got, want, want+608)
		}
	}
}

func TestNormalizeSvcName(t *testing.T) {
	cases := map[string]string{
		"nginx-6d664c6d47-s7ljj":                 "nginx",
		"halo-66859784d5-fvh9r":                  "halo",
		"smart-alert-aggregator-b6ccff87d-scsfm": "smart-alert-aggregator",
		"nginx-exporter-59fcccc856-lrqpk":        "nginx-exporter",
		"halo-0":                                  "halo",
		"halo":                                    "halo",
		"order-api":                               "order-api",
		"fluentd":                                 "fluentd",
		"mysql":                                   "mysql",
		"":                                        "",
	}
	for in, want := range cases {
		if got := NormalizeSvcName(in); got != want {
			t.Errorf("NormalizeSvcName(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestValidateCursors 未来时间戳游标应在启动自检中被重置为当前时间。
func TestValidateCursors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.db")
	st, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer st.Close()
	svc := NewService(st, nil)
	now := time.Now().UnixMilli()

	futureTs := now + cursorFuzzMs + 3600*1000 // 未来 3h, 远超容忍度
	offTs := now + cursorFuzzMs*3
	cases := []*LogSource{
		{ID: "ok", Name: "ok", Type: "k8s", Service: "s", Enabled: true, Follow: true, LastTs: now - 10000},
		{ID: "future", Name: "future", Type: "k8s", Service: "s", Enabled: true, Follow: true, LastTs: futureTs},
		{ID: "off", Name: "off", Type: "k8s", Service: "s", Enabled: false, Follow: true, LastTs: offTs},
	}
	for _, c := range cases {
		if err := st.SaveSource(c); err != nil {
			t.Fatalf("SaveSource: %v", err)
		}
	}
	if got := svc.ValidateCursors(); got != 1 {
		t.Fatalf("ValidateCursors fixed = %d, want 1", got)
	}
	merged, err := st.ListSources()
	if err != nil {
		t.Fatalf("ListSources: %v", err)
	}
	var future, off *LogSource
	for _, s := range merged {
		if s.ID == "future" {
			future = s
		}
		if s.ID == "off" {
			off = s
		}
	}
	if future == nil || off == nil {
		t.Fatalf("missing sources")
	}
	if future.LastTs > now+cursorFuzzMs || future.LastTs <= now-cursorFuzzMs {
		t.Fatalf("future source last_ts = %d, want ~now(%d)", future.LastTs, now)
	}
	if off.LastTs != offTs {
		t.Fatalf("disabled source should be untouched: got %d want %d", off.LastTs, offTs)
	}
}

// TestSalvageCorruptedDB 损坏库在启动时应自动 salvage 恢复（含损坏页附近行丢失）。
func TestSalvageCorruptedDB(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.db")
	st, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	entries := make([]*LogEntry, 5)
	for i := range entries {
		entries[i] = &LogEntry{Ts: time.Now().UnixMilli() - int64(5-i)*1000, Level: "INFO",
			Service: "t", Source: "test", FilePath: "f", Summary: "line"}
	}
	if _, err := st.InsertBatch(entries); err != nil {
		t.Fatalf("InsertBatch: %v", err)
	}
	st.Close()

	// 模拟损坏: 覆盖文件中间若干字节
	data, _ := os.ReadFile(path)
	mid := len(data) / 2
	for i := mid; i < mid+128 && i < len(data); i++ {
		data[i] = 0xFF
	}
	os.WriteFile(path, data, 0644)

	st2, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore after corruption: %v", err)
	}
	defer st2.Close()
	var cnt int
	if err := st2.db.QueryRow("SELECT COUNT(*) FROM log_meta").Scan(&cnt); err != nil {
		t.Fatalf("Count: %v", err)
	}
	t.Logf("salvaged rows=%d", cnt)
}