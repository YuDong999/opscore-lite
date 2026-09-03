package cicd

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// newTestEngine 建一个 Exec 桩引擎: 所有步骤瞬间成功
func newTestEngine(t *testing.T) *Engine {
	t.Helper()
	e, err := NewEngine(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	e.Exec = func(ctx context.Context, hostID, workspace, command string, env []Var, onLine func(string)) (int, error) {
		return 0, nil
	}
	t.Cleanup(e.Stop)
	return e
}

// waitCond 轮询等待条件成立(最长 5s)
func waitCond(t *testing.T, desc string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("等待超时: %s", desc)
}

func approvalPipeline(e *Engine, name string, stages []Stage) *Pipeline {
	p := &Pipeline{
		Name: name,
		Trigger: Trigger{Manual: true},
		Stages: stages,
		MaxRuns: 10,
	}
	if err := e.SavePipeline(p); err != nil {
		panic(err)
	}
	return p
}

func TestApprovalGate_Approve(t *testing.T) {
	e := newTestEngine(t)
	p := approvalPipeline(e, "approve-flow", []Stage{
		{Name: "构建", Steps: []Step{{Name: "build", Command: "echo build"}}},
		{Name: "发布", Approval: true, Steps: []Step{{Name: "deploy", Command: "echo deploy"}}},
	})
	run, err := e.Trigger(p.ID, TriggerManual)
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	// 等第二阶段进入等待审批
	waitCond(t, "阶段2进入 waiting", func() bool {
		r, ok := e.GetRun(run.ID)
		return ok && len(r.Stages) == 2 && r.Stages[1].Status == StatusWaiting
	})
	// 批准
	if err := e.Approve(run.ID, true); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	waitCond(t, "运行成功", func() bool {
		r, _ := e.GetRun(run.ID)
		return r.Status == StatusSuccess
	})
	r, _ := e.GetRun(run.ID)
	if r.Stages[1].Status != StatusSuccess {
		t.Errorf("批准后阶段2应为 success, 实际 %s", r.Stages[1].Status)
	}
	// 审批通道应已清理
	if err := e.Approve(run.ID, true); err == nil {
		t.Error("终态后 Approve 应报错")
	}
}

func TestApprovalGate_Reject(t *testing.T) {
	e := newTestEngine(t)
	p := approvalPipeline(e, "reject-flow", []Stage{
		{Name: "构建", Steps: []Step{{Name: "build", Command: "echo build"}}},
		{Name: "发布", Approval: true, Steps: []Step{{Name: "deploy", Command: "echo deploy"}}},
		{Name: "收尾", Steps: []Step{{Name: "cleanup", Command: "echo cleanup"}}},
	})
	run, _ := e.Trigger(p.ID, TriggerManual)
	waitCond(t, "阶段2进入 waiting", func() bool {
		r, ok := e.GetRun(run.ID)
		return ok && len(r.Stages) == 3 && r.Stages[1].Status == StatusWaiting
	})
	if err := e.Approve(run.ID, false); err != nil {
		t.Fatalf("Approve(reject): %v", err)
	}
	waitCond(t, "运行取消", func() bool {
		r, _ := e.GetRun(run.ID)
		return r.Status == StatusCanceled
	})
	r, _ := e.GetRun(run.ID)
	if r.Stages[1].Status != StatusCanceled {
		t.Errorf("拒绝后阶段2应为 canceled, 实际 %s", r.Stages[1].Status)
	}
	if r.Stages[2].Status != StatusSkipped {
		t.Errorf("拒绝后阶段3应为 skipped, 实际 %s", r.Stages[2].Status)
	}
}

func TestApprovalGate_CancelWhileWaiting(t *testing.T) {
	e := newTestEngine(t)
	p := approvalPipeline(e, "cancel-flow", []Stage{
		{Name: "发布", Approval: true, Steps: []Step{{Name: "deploy", Command: "echo deploy"}}},
	})
	run, _ := e.Trigger(p.ID, TriggerManual)
	waitCond(t, "阶段1进入 waiting", func() bool {
		r, ok := e.GetRun(run.ID)
		return ok && r.Stages[0].Status == StatusWaiting
	})
	// 等待期间取消 → ctx 取消 → waitApproval 视为拒绝
	if err := e.Cancel(run.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	waitCond(t, "运行取消", func() bool {
		r, _ := e.GetRun(run.ID)
		return r.Status == StatusCanceled
	})
}

func TestApprovalGate_NoApprovalPending(t *testing.T) {
	e := newTestEngine(t)
	p := approvalPipeline(e, "no-gate", []Stage{
		{Name: "构建", Steps: []Step{{Name: "build", Command: "echo build"}}},
	})
	run, _ := e.Trigger(p.ID, TriggerManual)
	waitCond(t, "运行成功", func() bool {
		r, _ := e.GetRun(run.ID)
		return r.Status == StatusSuccess
	})
	if err := e.Approve(run.ID, true); err == nil {
		t.Error("无等待审批时 Approve 应报错")
	}
}
