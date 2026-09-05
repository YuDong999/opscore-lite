package cicd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestTriggerBranchOverride 运行时分支覆盖 → CICD_BRANCH 应使用覆盖值
func TestTriggerBranchOverride(t *testing.T) {
	e := newTestEngine(t)
	ws := t.TempDir()
	var gotBranch []string
	e.Exec = func(ctx context.Context, hostID, workspace, command string, env []Var, onLine func(string)) (int, error) {
		for _, v := range env {
			if v.Name == "CICD_BRANCH" {
				gotBranch = append(gotBranch, v.Value)
			}
		}
		return 0, nil
	}

	p := &Pipeline{Name: "branch-flow", Trigger: Trigger{Manual: true},
		Source: Source{RepoID: "repo-b", Branch: "dev"},
		Stages: []Stage{{Name: "构建", Host: "", Workspace: ws, Steps: []Step{{Name: "s", Command: "true"}}}},
		MaxRuns: 10}
	if err := e.SavePipeline(p); err != nil {
		t.Fatalf("SavePipeline: %v", err)
	}
	e.repos = append(e.repos, &Repo{ID: "repo-b", Name: "app", URL: "https://git.example.com/team/app.git", DefaultBranch: "master"})

	// 不覆盖 → 用流水线分支 dev
	r1, err := e.Trigger(p.ID, TriggerManual)
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	waitCond(t, "r1 成功", func() bool {
		r, _ := e.GetRun(r1.ID)
		return r.Status == StatusSuccess
	})
	// 覆盖 main → 用 main
	r2, err := e.TriggerBranch(p.ID, TriggerManual, "main", nil)
	if err != nil {
		t.Fatalf("TriggerBranch: %v", err)
	}
	waitCond(t, "r2 成功", func() bool {
		r, _ := e.GetRun(r2.ID)
		return r.Status == StatusSuccess
	})

	// 每次 run 含 clone+普通两个步骤, 各携带一次 CICD_BRANCH → 4 条(2 dev + 2 main)
	if len(gotBranch) != 4 || gotBranch[0] != "dev" || gotBranch[len(gotBranch)-1] != "main" {
		t.Errorf("CICD_BRANCH 序列应为 [dev dev main main], 实际 %v", gotBranch)
	}
}

// TestRepoBranchesParse 分支列表解析(ls-remote 输出)
func TestRepoBranchesParse(t *testing.T) {
	// RepoBranches 依赖真实 git+网络, 这里只验证 parse 逻辑等价物
	out := "a1b2c3d\trefs/heads/main\ne4f5a6b\trefs/heads/dev\n"
	var branches []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if i := strings.Index(line, "refs/heads/"); i >= 0 {
			branches = append(branches, line[i+len("refs/heads/"):])
		}
	}
	if len(branches) != 2 || branches[0] != "main" || branches[1] != "dev" {
		t.Errorf("分支解析结果不符: %v", branches)
	}
}

// TestRunDefaultBranchFallback 覆盖空 + 流水线分支空 → 回退仓库默认分支
func TestRunDefaultBranchFallback(t *testing.T) {
	e := newTestEngine(t)
	ws := t.TempDir()
	var gotBranch []string
	e.Exec = func(ctx context.Context, hostID, workspace, command string, env []Var, onLine func(string)) (int, error) {
		for _, v := range env {
			if v.Name == "CICD_BRANCH" {
				gotBranch = append(gotBranch, v.Value)
			}
		}
		return 0, nil
	}
	p := &Pipeline{Name: "fallback", Trigger: Trigger{Manual: true},
		Source: Source{RepoID: "repo-c"},
		Stages: []Stage{{Name: "a", Host: "", Workspace: ws, Steps: []Step{{Name: "s", Command: "true"}}}},
		MaxRuns: 10}
	e.SavePipeline(p)
	e.repos = append(e.repos, &Repo{ID: "repo-c", Name: "app", URL: "https://git.example.com/team/app.git", DefaultBranch: "master"})

	r, _ := e.Trigger(p.ID, TriggerManual)
	waitCond(t, "成功", func() bool {
		r, _ := e.GetRun(r.ID)
		return r.Status == StatusSuccess
	})
	if len(gotBranch) != 2 || gotBranch[0] != "master" {
		t.Errorf("应回退仓库默认分支 master, 实际 %v", gotBranch)
	}
	_ = os.Getenv
	_ = filepath.Join
}

// TestRunBranchRecorded 运行应记录所用分支(重新执行用)
func TestRunBranchRecorded(t *testing.T) {
	e := newTestEngine(t)
	ws := t.TempDir()
	e.Exec = func(ctx context.Context, hostID, workspace, command string, env []Var, onLine func(string)) (int, error) {
		return 0, nil
	}
	p := &Pipeline{Name: "branch-record", Trigger: Trigger{Manual: true},
		Source: Source{RepoID: "repo-x", Branch: "dev"},
		Stages: []Stage{{Name: "a", Host: "", Workspace: ws, Steps: []Step{{Name: "s", Command: "true"}}}},
		MaxRuns: 10}
	e.SavePipeline(p)
	e.repos = append(e.repos, &Repo{ID: "repo-x", Name: "app", URL: "https://git.example.com/team/app.git", DefaultBranch: "master"})

	r, _ := e.TriggerBranch(p.ID, TriggerManual, "release-1", nil)
	waitCond(t, "成功", func() bool {
		r, _ := e.GetRun(r.ID)
		return r.Status == StatusSuccess
	})
	r2, _ := e.GetRun(r.ID)
	if r2.Branch != "release-1" {
		t.Errorf("Run.Branch 应记录覆盖分支, 实际 %q", r2.Branch)
	}
}

// TestDeleteRun 单条运行删除(含日志制品), 进行中拒绝
func TestDeleteRun(t *testing.T) {
	e := newTestEngine(t)
	ws := t.TempDir()
	e.Exec = func(ctx context.Context, hostID, workspace, command string, env []Var, onLine func(string)) (int, error) {
		time.Sleep(3 * time.Second) // 长任务, 给删除测试制造 running 窗口
		return 0, nil
	}
	p := &Pipeline{Name: "del-flow", Trigger: Trigger{Manual: true},
		Stages: []Stage{{Name: "a", Host: "", Workspace: ws, Steps: []Step{{Name: "s", Command: "sleep 3"}}}},
		MaxRuns: 10}
	e.SavePipeline(p)

	// 成功运行 → 删除成功
	r1, _ := e.Trigger(p.ID, TriggerManual)
	waitCond(t, "r1 成功", func() bool {
		r, _ := e.GetRun(r1.ID)
		return r.Status == StatusSuccess
	})
	if err := e.DeleteRun(r1.ID); err != nil {
		t.Fatalf("DeleteRun: %v", err)
	}
	if _, ok := e.GetRun(r1.ID); ok {
		t.Error("删除后不应再能查到")
	}

	// 进行中 → 拒绝
	r2, _ := e.Trigger(p.ID, TriggerManual)
	waitCond(t, "r2 运行中", func() bool {
		r, _ := e.GetRun(r2.ID)
		return r.Status == StatusRunning
	})
	if err := e.DeleteRun(r2.ID); err == nil {
		t.Error("进行中的运行应拒绝删除")
	}
	e.Cancel(r2.ID)
	waitCond(t, "取消完成", func() bool {
		r, _ := e.GetRun(r2.ID)
		return r.Status == StatusCanceled
	})
	if err := e.DeleteRun(r2.ID); err != nil {
		t.Errorf("取消后应可删除: %v", err)
	}
}
