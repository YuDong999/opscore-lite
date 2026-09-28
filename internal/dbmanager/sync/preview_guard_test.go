package sync

import "testing"

// P1-13 回归防护: **产出数据的路径不能吃预览截断值**。
//
// 背景(2026-09-28 实测踩到): 扫码层对超阈值的大对象给预览是**对的**(界面不能被几 MB 的
// 二进制拖死), 但"复制为 INSERT"和"导出 CSV"拿的是同一份数据 —— 于是 5000 字节的 BLOB
// 被写成 "[BLOB preview: 4096/5000 bytes] ZZZ...", 导出即损坏。
//
// 这一组断言钉住两件事:
//  1. 预览标记能被认出来(严格前缀);
//  2. 兜底检查会在命中时报错, 而不是放行。
func TestLooksLikeTruncatedPreview(t *testing.T) {
	truncated := []any{
		"[BLOB preview: 4096/5000 bytes] ZZZ",
		"[CLOB preview: 4096/9000 bytes] 内容",
		"[BLOB preview: 1/2 bytes] x",
	}
	for _, v := range truncated {
		if !LooksLikeTruncatedPreview(v) {
			t.Errorf("应认出这是预览截断值: %#v", v)
		}
	}

	notTruncated := []any{
		nil,
		123,
		[]byte("raw"),
		"普通文本",
		// **只有前缀才算**: 真实数据里中间出现这串文字不该被误判(误判比漏判更烦人)
		"前缀里提到 [BLOB preview: 1/2 bytes] 但不在开头",
		// 短值本来就不会被截断
		"ZZZZ",
	}
	for _, v := range notTruncated {
		if LooksLikeTruncatedPreview(v) {
			t.Errorf("不该判为预览截断: %#v", v)
		}
	}
}

func TestCheckNoPreviewTruncationCatchesAndNamesColumn(t *testing.T) {
	rows := []map[string]any{
		{"id": 1, "doc": "ok"},
		{"id": 2, "doc": "[BLOB preview: 4096/5000 bytes] ZZZ"},
	}
	err := CheckNoPreviewTruncation(rows, []string{"id", "doc"})
	if err == nil {
		t.Fatal("含预览截断值时应报错 —— 否则会导出一份坏数据")
	}
	// 错误信息要点名是哪一列, 否则用户不知道该去哪取完整值
	if !contains(err.Error(), "doc") {
		t.Errorf("错误信息应点名列名: %v", err)
	}
}

func TestCheckNoPreviewTruncationPassesCleanData(t *testing.T) {
	rows := []map[string]any{
		{"id": 1, "doc": "完整内容"},
		{"id": 2, "doc": nil},
		{"id": 3, "blob": []byte{1, 2, 3}},
	}
	if err := CheckNoPreviewTruncation(rows, []string{"id", "doc", "blob"}); err != nil {
		t.Errorf("干净数据不该报错: %v", err)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
