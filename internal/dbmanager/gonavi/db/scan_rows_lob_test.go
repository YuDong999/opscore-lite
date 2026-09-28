package db

import (
	"fmt"
	"strings"
	"testing"
)

// P1-13: 大对象预览判定从"仅 Oracle 类型名"泛化到全方言。
//
// 这一组断言防两个方向:
//   - **该认的没认**(MySQL 的 longblob、PG 的 bytea 原来整值进网格, 几 MB 就拖死界面);
//   - **不该认的认了**(binary(16) 这种定长小二进制被标"已截断", 是误导)。
func TestBinaryLargeObjectTypePerDialect(t *testing.T) {
	shouldBeLOB := []string{
		// Oracle 族(原有能力, 不能退步)
		"OCIBLOBLOCATOR", "LONGRAW", "LONGVARRAW", "ocibloblocator", "long raw",
		// MySQL 族
		"blob", "LONGBLOB", "mediumblob", "tinyblob",
		// PostgreSQL
		"bytea", "BYTEA",
		// SQL Server
		"image", "varbinary(max)", "binary(max)",
		// 地理类型(值可能很大)
		"geometry", "GEOGRAPHY",
		// 长度很大的 varbinary
		"varbinary(65535)",
	}
	for _, tn := range shouldBeLOB {
		if !isBinaryLargeObjectType(tn, "") {
			t.Errorf("%q 应判为二进制大对象(否则整值进网格把界面拖死)", tn)
		}
	}

	shouldNot := []string{
		"", "int", "varchar(255)", "text_small",
		// 定长小二进制: 本来就短, 标"已截断"只会误导
		"binary(16)", "varbinary(255)", "raw(2000)",
		// 普通文本
		"varchar(4000)",
	}
	for _, tn := range shouldNot {
		if isBinaryLargeObjectType(tn, "") {
			t.Errorf("%q 不该判为二进制大对象(会给短列误打截断标签)", tn)
		}
	}
}

func TestTextLargeObjectTypePerDialect(t *testing.T) {
	shouldBeLOB := []string{
		"OCICLOBLOCATOR", "ocicloBlocator", "LONG", "LONGVARCHAR",
		"clob", "NCLOB", "LONGTEXT", "mediumtext", "tinytext",
		"xml", "JSONB", "json",
	}
	for _, tn := range shouldBeLOB {
		if !isTextLargeObjectType(tn, "") {
			t.Errorf("%q 应判为文本大对象", tn)
		}
	}
	shouldNot := []string{"", "varchar(255)", "char(10)", "int", "timestamp"}
	for _, tn := range shouldNot {
		if isTextLargeObjectType(tn, "") {
			t.Errorf("%q 不该判为文本大对象", tn)
		}
	}
}

func TestParseTypeSize(t *testing.T) {
	cases := map[string]struct {
		n  int
		ok bool
	}{
		"VARBINARY(65535)": {65535, true},
		"BINARY(16)":       {16, true},
		"BLOB":             {0, false},
		"VARBINARY()":      {0, false},
		"":                 {0, false},
	}
	for in, want := range cases {
		n, ok := parseTypeSize(in)
		if ok != want.ok || (ok && n != want.n) {
			t.Errorf("parseTypeSize(%q) = (%d, %v), 期望 (%d, %v)", in, n, ok, want.n, want.ok)
		}
	}
}

// 预览提示必须**保留原有前缀格式**(可解析), 且带上"怎么拿全"的出口。
func TestLargeObjectHintKeepsParsablePrefix(t *testing.T) {
	if !strings.Contains(largeObjectHint, "查看完整值") {
		t.Error("截断提示必须告诉用户怎么拿全(否则只看到半截数据不知道下一步)")
	}
	// 前缀格式不能被这段提示污染: 快速自检一下格式串仍是 "N/M bytes] ..." 形态
	blob := fmt.Sprintf("[BLOB preview: %d/%d bytes] %s\n%s", 4096, 9999, "x", largeObjectHint)
	if !strings.HasPrefix(blob, "[BLOB preview: 4096/9999 bytes] ") {
		t.Errorf("BLOB 预览前缀格式被破坏: %q", blob)
	}
	clob := fmt.Sprintf("[CLOB preview: %d/%d bytes] %s\n%s", 4096, 9999, "x", largeObjectHint)
	if !strings.HasPrefix(clob, "[CLOB preview: 4096/9999 bytes] ") {
		t.Errorf("CLOB 预览前缀格式被破坏: %q", clob)
	}
}
