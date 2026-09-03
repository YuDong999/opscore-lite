package cicd

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestArtifactPull 阶段1本机收集制品 → 阶段2拉取到工作目录并注入 CICD_ARTIFACT
func TestArtifactPull(t *testing.T) {
	e := newTestEngine(t)
	ws1 := t.TempDir()
	ws2 := t.TempDir()
	os.WriteFile(filepath.Join(ws1, "app.bin"), []byte("payload-bytes"), 0644)

	var gotArtifactEnv []string
	e.Exec = func(ctx context.Context, hostID, workspace, command string, env []Var, onLine func(string)) (int, error) {
		for _, v := range env {
			if v.Name == "CICD_ARTIFACT" {
				gotArtifactEnv = append(gotArtifactEnv, v.Value)
			}
		}
		return 0, nil
	}
	// Push 桩: 模拟"推送到目标主机"——写到本地 shadow 目录以便断言内容
	pushShadow := t.TempDir()
	e.Push = func(ctx context.Context, hostID, destPath string, data []byte) error {
		return os.WriteFile(filepath.Join(pushShadow, filepath.Base(destPath)), data, 0644)
	}

	p := approvalPipeline(e, "pull-flow", []Stage{
		{Name: "构建", Host: "", Workspace: ws1, Steps: []Step{
			{Name: "打包", Command: "echo build", Artifacts: []string{"app.bin"}},
		}},
		{Name: "发布", Host: "", Workspace: ws2, Steps: []Step{
			{Name: "部署", Command: "tar xzf $CICD_ARTIFACT", PullArtifact: "s1-step1.tar.gz"},
		}},
	})
	run, err := e.Trigger(p.ID, TriggerManual)
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	waitCond(t, "运行成功", func() bool {
		r, _ := e.GetRun(run.ID)
		return r.Status == StatusSuccess
	})
	if len(gotArtifactEnv) != 1 || gotArtifactEnv[0] != "s1-step1.tar.gz" {
		t.Errorf("CICD_ARTIFACT 应注入一次且为制品文件名, 实际: %v", gotArtifactEnv)
	}
	// 拉取的制品应真实落在阶段2工作目录
	b, err := os.ReadFile(filepath.Join(ws2, "s1-step1.tar.gz"))
	if err != nil {
		t.Fatalf("制品应已复制到阶段2工作目录: %v", err)
	}
	if len(b) == 0 {
		t.Error("制品文件不应为空")
	}
}

// TestArtifactPullMissing 引用不存在的制品 → 步骤失败(非命令执行错误路径)
func TestArtifactPullMissing(t *testing.T) {
	e := newTestEngine(t)
	ws := t.TempDir()
	e.Exec = func(ctx context.Context, hostID, workspace, command string, env []Var, onLine func(string)) (int, error) {
		t.Error("制品缺失时不应执行步骤命令")
		return 0, nil
	}
	p := approvalPipeline(e, "pull-missing", []Stage{
		{Name: "发布", Host: "", Workspace: ws, Steps: []Step{
			{Name: "部署", Command: "echo x", PullArtifact: "s9-step9.tar.gz"},
		}},
	})
	run, _ := e.Trigger(p.ID, TriggerManual)
	waitCond(t, "运行失败", func() bool {
		r, _ := e.GetRun(run.ID)
		return r.Status == StatusFailed
	})
	r, _ := e.GetRun(run.ID)
	if r.Stages[0].Steps[0].Status != StatusFailed {
		t.Errorf("步骤应为 failed, 实际 %s", r.Stages[0].Steps[0].Status)
	}
	if r.Error == "" {
		t.Error("应有失败原因")
	}
}

// TestArtifactPullSaveValidation 保存时校验拉取制品文件名格式
func TestArtifactPullSaveValidation(t *testing.T) {
	e := newTestEngine(t)
	err := e.SavePipeline(&Pipeline{
		Name:    "bad-pull",
		Trigger: Trigger{Manual: true},
		Stages: []Stage{{Name: "a", Steps: []Step{
			{Name: "s", Command: "echo x", PullArtifact: "../../etc/passwd"},
		}}},
	})
	if err == nil {
		t.Error("非法制品文件名应被拒绝保存")
	}
}
