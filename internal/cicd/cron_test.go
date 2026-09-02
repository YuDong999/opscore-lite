package cicd

import (
	"strings"
	"testing"
	"time"
)

func TestParseCronValid(t *testing.T) {
	cases := []string{
		"* * * * *",
		"0 3 * * *",
		"*/5 * * * *",
		"30 2 1,15 * *",
		"0 9-18 * * 1-5",
		"15,45 */2 * * 0",
		"10-30/5 * * * *",
	}
	for _, expr := range cases {
		if _, err := ParseCron(expr); err != nil {
			t.Errorf("ParseCron(%q) 应成功, 实际: %v", expr, err)
		}
	}
}

func TestParseCronInvalid(t *testing.T) {
	cases := []string{
		"",
		"* * * *",      // 字段不足
		"* * * * * *",  // 字段过多
		"60 * * * *",   // 分钟越界
		"* 24 * * *",   // 小时越界
		"a * * * *",    // 非数字
		"*/0 * * * *",  // 步进为 0
		"5-1 * * * *",  // 范围倒置
		"1,,2 * * * *", // 空子项
	}
	for _, expr := range cases {
		if _, err := ParseCron(expr); err == nil {
			t.Errorf("ParseCron(%q) 应失败", expr)
		}
	}
}

func TestCronMatch(t *testing.T) {
	// 2026-09-03 是周四
	mustParse := func(expr string) *CronSpec {
		s, err := ParseCron(expr)
		if err != nil {
			t.Fatalf("ParseCron(%q): %v", expr, err)
		}
		return s
	}
	// 每天 03:00
	s := mustParse("0 3 * * *")
	if !s.Match(time.Date(2026, 9, 3, 3, 0, 0, 0, time.Local)) {
		t.Error("0 3 * * * 应命中 03:00")
	}
	if s.Match(time.Date(2026, 9, 3, 3, 1, 0, 0, time.Local)) {
		t.Error("0 3 * * * 不应命中 03:01")
	}
	// 每 5 分钟
	s = mustParse("*/5 * * * *")
	if !s.Match(time.Date(2026, 9, 3, 10, 55, 0, 0, time.Local)) {
		t.Error("*/5 应命中 :55")
	}
	if s.Match(time.Date(2026, 9, 3, 10, 53, 0, 0, time.Local)) {
		t.Error("*/5 不应命中 :53")
	}
	// 工作日(周一到周五)
	s = mustParse("0 9 * * 1-5")
	if !s.Match(time.Date(2026, 9, 3, 9, 0, 0, 0, time.Local)) {
		t.Error("1-5 应命中周四")
	}
	if s.Match(time.Date(2026, 9, 6, 9, 0, 0, 0, time.Local)) {
		t.Error("1-5 不应命中周日")
	}
	// 0 与 7 都是周日
	s = mustParse("0 9 * * 7")
	if !s.Match(time.Date(2026, 9, 6, 9, 0, 0, 0, time.Local)) {
		t.Error("dow=7 应命中周日")
	}
	// 日+周同时受限 → 或语义(9月1日是周二)
	s = mustParse("0 3 1 * 1")
	if !s.Match(time.Date(2026, 9, 1, 3, 0, 0, 0, time.Local)) {
		t.Error("日=1 周=1 应命中 9/1(周二, 周命中)")
	}
	if !s.Match(time.Date(2026, 9, 7, 3, 0, 0, 0, time.Local)) {
		t.Error("日=1 周=1 应命中 9/7(周一, 周命中)")
	}
	if s.Match(time.Date(2026, 9, 2, 3, 0, 0, 0, time.Local)) {
		t.Error("日=1 周=1 不应命中 9/2")
	}
}

func TestMaskLine(t *testing.T) {
	// ≥4 字符的 secret 才进入掩码列表(过滤发生在 collectSecrets)
	secrets := []string{"super-secret-token"}
	got := maskLine("echo super-secret-token && echo abc def", secrets)
	if got != "echo ****** && echo abc def" {
		t.Errorf("掩码结果不符合预期: %q", got)
	}
	env := []Var{
		{Name: "A", Value: "abc", Secret: true},
		{Name: "B", Value: "long-secret", Secret: true},
		{Name: "C", Value: "not-masked", Secret: false},
	}
	if s := collectSecrets(env); len(s) != 1 || s[0] != "long-secret" {
		t.Errorf("collectSecrets 应只保留 Secret 且长度≥4 的值: %v", s)
	}
}

func TestCronDescribe(t *testing.T) {
	s, _ := ParseCron("0 3 * * *")
	if d := s.Describe(); d != "每天 03:00" {
		t.Errorf("Describe(0 3 * * *) = %q, 期望 每天 03:00", d)
	}
}

func TestRepoName(t *testing.T) {
	cases := map[string]string{
		"https://git.example.com/team/app.git": "app",
		"https://github.com/user/repo":         "repo",
		"git@github.com:user/repo.git":         "repo",
		"ssh://git@host:2222/a/b/thing.git":    "thing",
	}
	for url, want := range cases {
		if got := repoName(url); got != want {
			t.Errorf("repoName(%q) = %q, 期望 %q", url, got, want)
		}
	}
}

func TestCloneCommandSafetyGuard(t *testing.T) {
	cmd := cloneCommand("https://git.example.com/team/app.git", "main", nil)
	if !strings.Contains(cmd, "拒绝重置") {
		t.Error("clone 命令必须包含远端同名校验护栏")
	}
	if !strings.Contains(cmd, "git clone --depth 1 -b 'main'") {
		t.Errorf("克隆分支应被单引号包裹: %s", cmd)
	}
}
