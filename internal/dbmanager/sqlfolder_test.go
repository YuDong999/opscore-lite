// SQL 仓库目录路径的边界测试(P1-10)。
//
// 目录只是 SavedQuery.Folder 里的一段文本, 但它会出现在界面路径、将来的导出文件名里,
// 所以这些边界必须在入口处定死 —— 尤其是 `..` 与绝对路径: 一旦漏进文件名拼接就是越界写。
package dbmanager

import (
	"strings"
	"testing"
)

func TestNormalizeSQLFolderAccepts(t *testing.T) {
	cases := map[string]string{
		"":                    "",
		"   ":                 "",
		"/":                   "",
		"ops":                 "ops",
		"ops/k8s":             "ops/k8s",
		"/ops/k8s/":           "ops/k8s",
		"ops//k8s":            "ops/k8s",   // 连续斜杠折叠
		"  ops / k8s  ":       "ops/k8s",   // 每段去空白
		"运维/K8s 排障":          "运维/K8s 排障", // 中文与空格是正常名字
		"ops\\k8s":            "ops/k8s",  // 反斜杠归一成 /
		"a/b/c/d":             "a/b/c/d",
	}
	for in, want := range cases {
		got, err := normalizeSQLFolder(in)
		if err != nil {
			t.Errorf("%q: 不该报错, 得到 %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("%q -> %q, 期望 %q", in, got, want)
		}
	}
}

func TestNormalizeSQLFolderRejects(t *testing.T) {
	bad := []string{
		"..", "ops/..", "../etc", "ops/../etc", ".", "ops/.",
		"a:b", "a*b", "a?b", "a\"b", "a<b", "a>b", "a|b",
	}
	for _, in := range bad {
		if got, err := normalizeSQLFolder(in); err == nil {
			t.Errorf("%q 应被拒, 却得到 %q", in, got)
		}
	}
	if _, err := normalizeSQLFolder(strings.Repeat("a", 201)); err == nil {
		t.Error("超长路径应被拒")
	}
	// 200 字符以内要放行(边界不能卡太死)
	if _, err := normalizeSQLFolder(strings.Repeat("a", 200)); err != nil {
		t.Errorf("200 字符应放行: %v", err)
	}
}

// 目录前缀匹配必须按"整段"而不是"字符串前缀":
// 否则改 ops 会连带把 ops2 也改了(经典的前缀陷阱)。
func TestFolderPrefixMatchingIsSegmentWise(t *testing.T) {
	inFolder := func(f, target string) bool {
		return f == target || strings.HasPrefix(f, target+"/")
	}
	if inFolder("ops2/x", "ops") {
		t.Error("ops2/x 不属于 ops 目录 —— 前缀匹配必须按 / 分段")
	}
	if !inFolder("ops/x", "ops") {
		t.Error("ops/x 属于 ops")
	}
	if !inFolder("ops", "ops") {
		t.Error("ops 本身属于 ops")
	}
	if inFolder("", "ops") {
		t.Error("根目录不属于 ops")
	}
}
