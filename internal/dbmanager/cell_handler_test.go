// 单元格分片读 / 下载的拼句与边界测试(P1-13)。
//
// 这里能真验的是"我们自己这一层": 方言函数名、offset 的 0/1 基换算、标识符与主键校验、
// 文件名清洗。真正"驱动是否照做"要靠真机(Google 已验: 见提交信息)。
package dbmanager

import (
	"strings"
	"testing"

	syncpkg "opscore/internal/dbmanager/sync"
)

// MySQL 用 SUBSTRING 且下标从 1 开始, 所以 offset=0 必须translate 成 1。
func TestChunkExprMySQLIsOneBased(t *testing.T) {
	got := chunkExprFor(syncpkg.DialectMySQL, "`col`", 0, 100)
	if got != "SUBSTRING(`col`, 1, 100)" {
		t.Errorf("offset=0 应对应起始 1: %q", got)
	}
	got = chunkExprFor(syncpkg.DialectMySQL, "`col`", 4096, 128)
	if got != "SUBSTRING(`col`, 4097, 128)" {
		t.Errorf("offset=4096 应对应起始 4097: %q", got)
	}
}

func TestChunkExprPostgres(t *testing.T) {
	got := chunkExprFor(syncpkg.DialectPostgres, `"col"`, 0, 64)
	if got != `substr("col", 1, 64)` {
		t.Errorf("PG 应走 substr: %q", got)
	}
}

// 长度函数: PG 必须用 octet_length(按**字节**), 否则分片按字节切而长度按字符算, 对不上。
func TestLengthExprPerDialect(t *testing.T) {
	if got := lengthExprFor(syncpkg.DialectPostgres, "c"); got != "octet_length(c)" {
		t.Errorf("PG 长度应按字节: %q", got)
	}
	if got := lengthExprFor(syncpkg.DialectMySQL, "c"); got != "LENGTH(c)" {
		t.Errorf("MySQL 长度: %q", got)
	}
}

func TestSafeDownloadName(t *testing.T) {
	cases := map[string]string{
		"payload":        "payload.bin",
		"a/b":            "a_b.bin",     // 路径分隔要去掉
		"a\\b":          "a_b.bin",
		"a b":            "a b.bin",     // 空格保留
		"":               "cell.bin",    // 兜底
		"   ":            "cell.bin",
		"a:b*c?d\"e<f>g|h": "a_b_c_d_e_f_g_h.bin",
	}
	for in, want := range cases {
		if got := safeDownloadName(in); got != want {
			t.Errorf("safeDownloadName(%q) = %q, 期望 %q", in, got, want)
		}
	}
	// 换行等控制字符必须被清掉(否则会破坏 Content-Disposition 头)
	if got := safeDownloadName("a\nb"); strings.ContainsAny(got, "\n\r") {
		t.Errorf("控制字符未清洗: %q", got)
	}
	// 超长要截断
	long := strings.Repeat("x", 200)
	if got := safeDownloadName(long); len(got) > 90 {
		t.Errorf("超长名字应截断: len=%d", len(got))
	}
}

// 长度换算: 驱动可能给各种数值形态, 取不到必须返回 -1(而不是 0 —— 0 会被当成"空值")
func TestToInt64Loose(t *testing.T) {
	if got := toInt64Loose(nil); got != -1 {
		t.Errorf("nil 应为 -1(未知), 得到 %d", got)
	}
	if got := toInt64Loose("not-a-number"); got != -1 {
		t.Errorf("非数字应为 -1, 得到 %d", got)
	}
	if got := toInt64Loose(int64(42)); got != 42 {
		t.Errorf("int64: %d", got)
	}
	if got := toInt64Loose([]byte("12345")); got != 12345 {
		t.Errorf("[]byte: %d", got)
	}
	if got := toInt64Loose("6789"); got != 6789 {
		t.Errorf("string: %d", got)
	}
}
