package cicd

import (
	"context"
	"strings"
	"testing"
)

// TestParamizedRun 参数化触发: 必填校验/默认值回填/环境注入/运行记录
func TestParamizedRun(t *testing.T) {
	e := newTestEngine(t)
	ws := t.TempDir()
	var gotEnv []string
	e.Exec = func(ctx context.Context, hostID, workspace, command string, env []Var, onLine func(string)) (int, error) {
		for _, v := range env {
			if v.Name == "TARGET_ENV" {
				gotEnv = append(gotEnv, v.Value)
			}
		}
		return 0, nil
	}

	p := &Pipeline{Name: "param-flow", Trigger: Trigger{Manual: true},
		Params: []ParamDef{
			{Name: "TARGET_ENV", Label: "目标环境", Type: "text", Default: "staging", Required: true},
			{Name: "EXTRA", Label: "附加", Type: "text"},
		},
		Stages: []Stage{{Name: "发布", Host: "", Workspace: ws, Steps: []Step{
			{Name: "部署", Command: "echo deploy to $TARGET_ENV"},
		}}},
		MaxRuns: 10}
	e.SavePipeline(p)

	// 空 params → 默认值回填 staging(有默认值, 不拒绝)
	// 无默认值的必填参数 → 拒绝
	p2 := &Pipeline{Name: "param-required", Trigger: Trigger{Manual: true},
		Params: []ParamDef{{Name: "DEPLOY_HOST", Label: "发布主机", Type: "text", Required: true}},
		Stages: []Stage{{Name: "发布", Host: "", Workspace: ws, Steps: []Step{
			{Name: "部署", Command: "echo to $DEPLOY_HOST"},
		}}},
		MaxRuns: 10}
	e.SavePipeline(p2)
	if _, err := e.TriggerBranch(p2.ID, TriggerManual, "", nil); err == nil {
		t.Fatal("无默认值的必填参数缺失应拒绝触发")
	} else if !strings.Contains(err.Error(), "DEPLOY_HOST") {
		t.Errorf("错误应指出缺失参数名, 实际: %v", err)
	}

	// 填参 → 注入环境变量 + 运行记录
	r, err := e.TriggerBranch(p.ID, TriggerManual, "", map[string]string{"TARGET_ENV": "prod"})
	if err != nil {
		t.Fatalf("TriggerBranch: %v", err)
	}
	waitCond(t, "成功", func() bool {
		r, _ := e.GetRun(r.ID)
		return r.Status == StatusSuccess
	})
	if len(gotEnv) != 1 || gotEnv[0] != "prod" {
		t.Errorf("TARGET_ENV 应为 prod, 实际 %v", gotEnv)
	}
	r2, _ := e.GetRun(r.ID)
	if r2.RunParams["TARGET_ENV"] != "prod" {
		t.Errorf("运行记录应含参数快照, 实际 %v", r2.RunParams)
	}

	// 不填参数 → 回退默认值 staging
	r3, _ := e.Trigger(p.ID, TriggerManual)
	waitCond(t, "r3 成功", func() bool {
		r, _ := e.GetRun(r3.ID)
		return r.Status == StatusSuccess
	})
	if len(gotEnv) != 2 || gotEnv[1] != "staging" {
		t.Errorf("默认值应回填 staging, 实际 %v", gotEnv)
	}
}
