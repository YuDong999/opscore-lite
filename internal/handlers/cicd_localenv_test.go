package handlers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"opscore/internal/cicd"
)

// CI 本机执行环境必须带 HOME。
//
// 2026-10-01 真机: OpsCore 以 systemd 服务运行, 服务环境里**没有 HOME**,
// 于是模板流水线里 `go build` 直接报
// "GOCACHE is not defined and neither $XDG_CACHE_HOME nor $HOME are defined" ——
// 装好了 go 也用不了。
func TestCicdLocalEnvProvidesHome(t *testing.T) {
	env := cicdLocalEnv(nil)
	m := map[string]string{}
	for _, kv := range env {
		if i := strings.IndexByte(kv, '='); i > 0 {
			m[kv[:i]] = kv[i+1:]
		}
	}
	if m["HOME"] == "" {
		t.Fatalf("HOME 不能为空(go/npm 都靠它): %v", m)
	}
	if m["PATH"] == "" {
		t.Fatal("PATH 不能为空")
	}
}

// 用户级工具链目录: 存在才加(不造悬空 PATH 项), 不重复。
func TestCicdLocalEnvAppendsExistingToolDirs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	env := cicdLocalEnv(nil)
	var path string
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			path = strings.TrimPrefix(kv, "PATH=")
		}
	}
	if !strings.Contains(path, bin) {
		t.Errorf("PATH 应包含已存在的 %s: %s", bin, path)
	}
	if strings.Count(path, bin) != 1 {
		t.Errorf("PATH 不应重复添加: %s", path)
	}
	// 不存在的目录不进 PATH
	if strings.Contains(path, filepath.Join(home, "go", "bin")) {
		t.Errorf("不存在的目录不该进 PATH: %s", path)
	}
}

// 流水线自带的环境变量优先级最高(可覆盖 PATH/HOME)。
func TestCicdLocalEnvPipelineVarsWin(t *testing.T) {
	env := cicdLocalEnv([]cicd.Var{
		{Name: "HOME", Value: "/custom/home"},
		{Name: "PATH", Value: "/custom/path"},
		{Name: "REGISTRY", Value: "reg.example.com"},
	})
	m := map[string]string{}
	for _, kv := range env {
		if i := strings.IndexByte(kv, '='); i > 0 {
			m[kv[:i]] = kv[i+1:]
		}
	}
	if m["HOME"] != "/custom/home" {
		t.Errorf("HOME 应被流水线变量覆盖, 实际 %q", m["HOME"])
	}
	if m["PATH"] != "/custom/path" {
		t.Errorf("PATH 应被流水线变量覆盖, 实际 %q", m["PATH"])
	}
	if m["REGISTRY"] != "reg.example.com" {
		t.Errorf("REGISTRY 未注入: %q", m["REGISTRY"])
	}
}
