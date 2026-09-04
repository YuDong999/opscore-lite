package cicd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"os/exec"
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

// TestParseCommitMarker commit 标记行解析
func TestParseCommitMarker(t *testing.T) {
	if c := parseCommitMarker("@@CICD_COMMIT@@a1b2c3d 修复登录页"); c != "a1b2c3d 修复登录页" {
		t.Errorf("标记行应提取 commit, 实际 %q", c)
	}
	if c := parseCommitMarker("普通日志输出"); c != "" {
		t.Errorf("非标记行应返回空, 实际 %q", c)
	}
	if c := parseCommitMarker("@@CICD_COMMIT@@  "); c != "" {
		t.Errorf("空 commit 应清理为空, 实际 %q", c)
	}
}

// TestCloneCommandCommitMarker clone 命令必须包含 commit 标记输出
func TestCloneCommandCommitMarker(t *testing.T) {
	cmd := cloneCommand("https://git.example.com/team/app.git", "main", nil)
	if !strings.Contains(cmd, commitMarkerPrefix) {
		t.Error("clone 命令应包含 commit 标记输出")
	}
	if !strings.Contains(cmd, "git log -1 --format='%h %s'") {
		t.Errorf("clone 命令应含 git log 格式化: %s", cmd)
	}
}

// TestCommitCapture 真实执行: 本机 git 仓库拉取后 Run.Commit 应被捕获
func TestCommitCapture(t *testing.T) {
	e := newTestEngine(t)
	// 建一个真实 git 仓库作为拉取目标
	ws := t.TempDir()
	gitDir := t.TempDir()
	run := func(args ...string) {
		c := exec.Command("git", args...)
		c.Dir = gitDir
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	// 文件名用仓库名 app(护栏按远端名比对)
	ws = filepath.Join(ws, "app")
	os.MkdirAll(ws, 0755)
	run("init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(ws, "f.txt"), []byte("v1"), 0644)
	run("add", ".")
	run("commit", "-q", "-m", "初始提交")
	run("remote", "add", "origin", "https://git.example.com/team/app.git")

	e.Exec = func(ctx context.Context, hostID, workspace, command string, env []Var, onLine func(string)) (int, error) {
		sh, _ := exec.LookPath("sh")
		cmd := exec.CommandContext(ctx, sh, "-c", command)
		cmd.Dir = workspace
		out, _ := cmd.CombinedOutput()
		for _, l := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
			onLine(l)
		}
		return 0, nil
	}

	p := &Pipeline{Name: "commit-capture", Trigger: Trigger{Manual: true},
		Source: Source{RepoID: "repo-x", Branch: "main"},
		Stages: []Stage{{Name: "构建", Host: "", Workspace: ws, Steps: []Step{{Name: "拉取", Command: "true"}}}},
		MaxRuns: 10}
	if err := e.SavePipeline(p); err != nil {
		t.Fatalf("SavePipeline: %v", err)
	}
	e.repos = append(e.repos, &Repo{ID: "repo-x", Name: "app", URL: "https://git.example.com/team/app.git", DefaultBranch: "main"})

	r, err := e.Trigger(p.ID, TriggerManual)
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	waitCond(t, "运行成功", func() bool {
		r2, _ := e.GetRun(r.ID)
		return r2.Status == StatusSuccess
	})
	r2, _ := e.GetRun(r.ID)
	if r2.Commit == "" {
		t.Error("运行成功后 Run.Commit 应被捕获")
	} else if !strings.Contains(r2.Commit, "初始提交") {
		t.Errorf("Commit 应包含提交标题, 实际 %q", r2.Commit)
	}
}
