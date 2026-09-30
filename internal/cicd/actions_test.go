package cicd

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestCompileAction 命令合成与必填校验
func TestCompileAction(t *testing.T) {
	cmd, err := CompileAction("docker.build", map[string]string{"IMAGE": "reg.io/team/app"})
	if err != nil {
		t.Fatalf("docker.build: %v", err)
	}
	for _, want := range []string{"docker build", "reg.io/team/app", "${BUILD_NUMBER}", "-f 'Dockerfile'"} {
		if !strings.Contains(cmd, want) {
			t.Errorf("命令缺少 %q: %s", want, cmd)
		}
	}
	if _, err := CompileAction("docker.build", nil); err == nil {
		t.Error("缺 IMAGE 应报错")
	}
	// 默认值兜底
	cmd, err = CompileAction("k8s.rollout", map[string]string{"DEPLOYMENT": "api"})
	if err != nil {
		t.Fatalf("k8s.rollout: %v", err)
	}
	if !strings.Contains(cmd, "--timeout=180s") {
		t.Errorf("默认超时应为 180s: %s", cmd)
	}
}

// TestValidateActionParams 保存校验
func TestValidateActionParams(t *testing.T) {
	if err := ValidateActionParams("no.such", nil); err == nil {
		t.Error("未知动作应拒绝")
	}
	if err := ValidateActionParams("health.http", nil); err == nil {
		t.Error("缺 URL 应拒绝")
	}
	if err := ValidateActionParams("health.http", map[string]string{"URL": "http://x/healthz"}); err != nil {
		t.Errorf("合法参数不应报错: %v", err)
	}
}

// TestActionStepExecution 动作步骤运行: 参数注入环境 + 命令编译 + 定义不被污染
func TestActionStepExecution(t *testing.T) {
	e := newTestEngine(t)
	ws := t.TempDir()
	var seenEnv []string
	var seenCmd []string
	e.Exec = func(ctx context.Context, hostID, workspace, command string, env []Var, onLine func(string)) (int, error) {
		var img string
		for _, v := range env {
			if v.Name == "IMAGE" {
				img = v.Value
			}
		}
		seenEnv = append(seenEnv, img)
		seenCmd = append(seenCmd, command)
		return 0, nil
	}

	p := &Pipeline{Name: "action-flow", Trigger: Trigger{Manual: true},
		Stages: []Stage{{Name: "构建", Host: "", Workspace: ws, Steps: []Step{
			{Name: "构建镜像", Command: "", Action: "docker.build",
				Params: map[string]string{"IMAGE": "reg.io/team/app"}},
		}}},
		MaxRuns: 10}
	if err := e.SavePipeline(p); err != nil {
		t.Fatalf("SavePipeline: %v", err)
	}
	r, _ := e.Trigger(p.ID, TriggerManual)
	waitCond(t, "成功", func() bool {
		r, _ := e.GetRun(r.ID)
		return r.Status == StatusSuccess
	})

	if len(seenEnv) != 1 || seenEnv[0] != "reg.io/team/app" {
		t.Errorf("IMAGE 应注入环境, 实际 %v", seenEnv)
	}
	if !strings.Contains(seenCmd[0], "docker build") || !strings.Contains(seenCmd[0], "${BUILD_NUMBER}") {
		t.Errorf("命令应含 docker build 与 tag 引用: %s", seenCmd[0])
	}
	// 流水线定义不被编译产物污染
	saved, _ := e.GetPipeline(p.ID)
	if saved.Stages[0].Steps[0].Command != "" {
		t.Errorf("定义中的 Command 应保持为空, 实际 %q", saved.Stages[0].Steps[0].Command)
	}
	// 运行视图展示合成命令
	run, _ := e.GetRun(r.ID)
	if run.Stages[0].Steps[0].Command == "" {
		t.Error("运行视图应展示合成命令")
	}
}

// Actions() 的结果必须能过 json.Marshal —— 2026-10-01 真机: ActionSpec.Build 是 func 字段
// 且没有 json tag, json.Marshal 直接报 "unsupported type: func(...)", 而 WriteJSON 当时吞掉
// 错误, /api/cicd/actions 回 200 + 空 body, 前端动作下拉只剩"Shell 命令"(8 个动作全丢)。
func TestActionsCatalogIsJSONSerializable(t *testing.T) {
	acts := Actions()
	if len(acts) == 0 {
		t.Fatal("动作库不应为空")
	}
	b, err := json.Marshal(acts)
	if err != nil {
		t.Fatalf("动作库必须能序列化(检查 func 字段的 json tag): %v", err)
	}
	// 序列化后不能再出现 func 字段名(Build), 且关键字段都在
	s := string(b)
	if strings.Contains(s, "Build") {
		t.Errorf("Build 是编译期函数, 不该出现在 JSON 里: %s", s[:200])
	}
	for _, want := range []string{"\"type\"", "\"title\"", "\"category\"", "\"fields\"", "nginx.bgswitch", "nginx.canary", "docker.build", "k8s.apply"} {
		if !strings.Contains(s, want) {
			t.Errorf("动作库 JSON 缺少 %s", want)
		}
	}
}
