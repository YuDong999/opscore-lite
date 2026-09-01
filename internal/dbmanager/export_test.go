package dbmanager

import (
	"bytes"
	"strings"
	"testing"
)

func testExportResult() *QueryResult {
	return &QueryResult{
		Columns: []string{"id", "name", "备注"},
		Rows: [][]any{
			{1, "alice", "正常"},
			{2, nil, "带,逗号"},
			{3, "bo\"b", `带"引号"`},
		},
		RowCount: 3,
	}
}

func TestExportToCSV(t *testing.T) {
	h := &Handlers{}
	var buf bytes.Buffer
	if err := h.exportToCSV(testExportResult(), &buf); err != nil {
		t.Fatalf("exportToCSV: %v", err)
	}
	out := buf.String()
	if !strings.HasPrefix(out, "\xEF\xBB\xBF") {
		t.Error("CSV 缺少 UTF-8 BOM")
	}
	for _, want := range []string{"id,name,备注", "1,alice,正常", `"bo""b"`, `"带""引号"""`} {
		if !strings.Contains(out, want) {
			t.Errorf("CSV 缺少 %q:\n%s", want, out)
		}
	}
	// nil 单元格应输出空串
	if !strings.Contains(out, ",,") {
		t.Errorf("nil 行应为空字段:\n%s", out)
	}
}

func TestExportToJSON(t *testing.T) {
	h := &Handlers{}
	var buf bytes.Buffer
	if err := h.exportToJSON(testExportResult(), &buf); err != nil {
		t.Fatalf("exportToJSON: %v", err)
	}
	out := buf.String()
	for _, want := range []string{`"columns"`, `"rows"`, `"count": 3`, "alice"} {
		if !strings.Contains(out, want) {
			t.Errorf("JSON 缺少 %q:\n%s", want, out)
		}
	}
}

func TestExportToXLSX(t *testing.T) {
	h := &Handlers{}
	var buf bytes.Buffer
	if err := h.exportToXLSX(testExportResult(), &buf); err != nil {
		t.Fatalf("exportToXLSX: %v", err)
	}
	// xlsx 是 zip: PK 魔数
	if !bytes.HasPrefix(buf.Bytes(), []byte("PK")) {
		t.Error("XLSX 输出不是合法 zip (缺 PK 魔数)")
	}
	if buf.Len() < 200 {
		t.Errorf("XLSX 输出过小: %d bytes", buf.Len())
	}
}

func TestExportErrorPropagation(t *testing.T) {
	h := &Handlers{}
	bad := &QueryResult{Error: "boom"}
	var buf bytes.Buffer
	if err := h.exportToCSV(bad, &buf); err == nil {
		t.Error("查询错误时应返回 error 而非空文件")
	}
	if err := h.exportToJSON(bad, &buf); err == nil {
		t.Error("JSON: 查询错误时应返回 error")
	}
	if err := h.exportToXLSX(bad, &buf); err == nil {
		t.Error("XLSX: 查询错误时应返回 error")
	}
}
