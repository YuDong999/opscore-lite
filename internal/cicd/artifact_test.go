package cicd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// TestTarLocal 通配收集 + 归档内容验证
func TestTarLocal(t *testing.T) {
	base := t.TempDir()
	os.MkdirAll(filepath.Join(base, "dist"), 0755)
	os.WriteFile(filepath.Join(base, "dist", "app.js"), []byte("console.log(1)"), 0644)
	os.WriteFile(filepath.Join(base, "dist", "style.css"), []byte("body{}"), 0644)
	os.WriteFile(filepath.Join(base, "root.txt"), []byte("root"), 0644)
	os.MkdirAll(filepath.Join(base, "node_modules", "x"), 0755) // 目录应被忽略(只收集文件)

	data, err := tarLocal(base, []string{"dist/*", "root.txt"})
	if err != nil {
		t.Fatalf("tarLocal: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("归档不应为空")
	}
	got := readTarNames(t, data)
	want := map[string]bool{"dist/app.js": false, "dist/style.css": false, "root.txt": false}
	for _, n := range got {
		if _, ok := want[n]; ok {
			want[n] = true
		}
	}
	for n, seen := range want {
		if !seen {
			t.Errorf("归档缺少 %s, 实际内容: %v", n, got)
		}
	}
	// 目录不应被打包为条目
	for _, n := range got {
		if n == "node_modules" || n == "node_modules/x" {
			t.Errorf("目录不应被归档: %s", n)
		}
	}
}

// TestTarLocalNoMatch 未匹配返回 nil(引擎层记 warn)
func TestTarLocalNoMatch(t *testing.T) {
	base := t.TempDir()
	data, err := tarLocal(base, []string{"nothing/*"})
	if err != nil {
		t.Fatalf("tarLocal: %v", err)
	}
	if data != nil {
		t.Error("无匹配时应返回 nil")
	}
}

// TestArtifactPatternWhiteList 制品路径白名单(防 shell 注入边界)
func TestArtifactPatternWhiteList(t *testing.T) {
	valid := []string{"dist/*", "app.tar.gz", "build/bin/app", "out/*.log", "a?", "[abc].txt"}
	for _, v := range valid {
		if !reArtifactPattern.MatchString(v) {
			t.Errorf("%q 应为合法制品路径", v)
		}
	}
	invalid := []string{
		"a b",       // 空格
		"a;rm -rf /", // 命令分隔
		"a$(id)",    // 命令替换
		"`id`",      // 反引号
		"a|b",       // 管道
		"a>b",       // 重定向
		"a&b",       // 后台执行
		`a"b`,       // 引号
		"a'b",
		"a\\b", // 反斜杠
		"",     // 空
	}
	for _, v := range invalid {
		if reArtifactPattern.MatchString(v) {
			t.Errorf("%q 应被白名单拒绝", v)
		}
	}
}

// TestArtifactFilePathSafety 下载路径安全(runID 防穿越 + 文件名白名单)
func TestArtifactFilePathSafety(t *testing.T) {
	e := newTestEngine(t)
	// runID 含路径分隔/点 → 拒绝
	for _, runID := range []string{"../x", "a/b", ".", "..", "run-1.tar.gz"} {
		if _, err := e.ArtifactFile(runID, "s1-step1.tar.gz"); err == nil {
			t.Errorf("runID %q 应被拒绝", runID)
		}
	}
	// 文件名白名单外 → 拒绝
	for _, f := range []string{"../etc/passwd", "x.tar.gz", "s1-step1.zip", "s1-step1.tar.gz.bak", "sA-stepB.tar.gz"} {
		if _, err := e.ArtifactFile("run-1-AB", f); err == nil {
			t.Errorf("文件名 %q 应被拒绝", f)
		}
	}
	// 合法但不存在 → 明确报不存在
	if _, err := e.ArtifactFile("run-1-AB", "s1-step1.tar.gz"); err == nil || err.Error() != "制品不存在(可能已被历史清理)" {
		t.Errorf("不存在制品应报明确错误, 实际: %v", err)
	}
	// 真实文件 → 放行
	dir := filepath.Join(e.artDir, "run-1-AB")
	os.MkdirAll(dir, 0755)
	os.WriteFile(filepath.Join(dir, "s1-step1.tar.gz"), []byte("gz"), 0644)
	if p, err := e.ArtifactFile("run-1-AB", "s1-step1.tar.gz"); err != nil || !filepath.IsAbs(p) {
		t.Errorf("合法制品应放行, 实际: %q %v", p, err)
	}
}

func readTarNames(t *testing.T, data []byte) []string {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var out []string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar: %v", err)
		}
		out = append(out, hdr.Name)
	}
	return out
}
