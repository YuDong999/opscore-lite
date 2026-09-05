package sync

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// JobRegistry 内存任务注册表(重启即失; Phase 1 同步任务生命周期=进程生命周期)。
type JobRegistry struct {
	mu   sync.Mutex
	jobs map[string]*Job
}

func NewJobRegistry() *JobRegistry {
	return &JobRegistry{jobs: map[string]*Job{}}
}

func (g *JobRegistry) Create(req SyncRequest, tables []TableProgress) *Job {
	id, _ := newJobID()
	j := &Job{
		ID:        id,
		Request:   req,
		Status:    "running",
		StartedAt: time.Now(),
		Tables:    tables,
	}
	g.mu.Lock()
	g.jobs[id] = j
	g.mu.Unlock()
	return j
}

func (g *JobRegistry) Get(id string) *Job {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.jobs[id]
}

// List 返回全部任务(新→旧)。
func (g *JobRegistry) List() []*Job {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]*Job, 0, len(g.jobs))
	for _, j := range g.jobs {
		out = append(out, j)
	}
	// 新的在前
	for i := 0; i < len(out); i++ {
		for k := i + 1; k < len(out); k++ {
			if out[k].StartedAt.After(out[i].StartedAt) {
				out[i], out[k] = out[k], out[i]
			}
		}
	}
	return out
}

// Start 注册任务并起后台 goroutine 执行。
func (r *Runner) Start(req SyncRequest, tables []TableProgress) *Job {
	job := r.jobs.Create(req, tables)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				r.fail(job, fmt.Sprintf("同步任务内部异常: %v", p))
			}
		}()
		r.Run(job)
	}()
	return job
}

// Cancel 取消运行中的任务。
func (g *JobRegistry) Cancel(id string) bool {
	j := g.Get(id)
	if j == nil {
		return false
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.Status == "running" && j.cancel != nil {
		j.cancel()
		now := time.Now()
		j.Status, j.Err, j.FinishedAt = "canceled", "已取消", &now
		return true
	}
	return false
}

func newJobID() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return fmt.Sprintf("sync_%d_%s", time.Now().Unix(), hex.EncodeToString(b)), nil
}

