package cicd

// CI/CD 流水线引擎: 定义/运行/日志/触发器的领域层, 不依赖 net/http。
// HTTP 层见 internal/handlers/cicd.go; 命令执行经 ExecFunc 回调注入
// (main.go 绑定 handlers.CicdExec → 本机 exec / 远程 SSH, 见构建文档)。

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"opscore/internal/store"
)

// 运行状态常量
const (
	StatusQueued   = "queued"
	StatusRunning  = "running"
	StatusSuccess  = "success"
	StatusFailed   = "failed"
	StatusCanceled = "canceled"
	StatusPending  = "pending"
	StatusSkipped  = "skipped"
)

// TriggerManual / TriggerWebhook / TriggerCron 运行触发来源
const (
	TriggerManual  = "manual"
	TriggerWebhook = "webhook"
	TriggerCron    = "cron"
)

// Var 流水线环境变量; Secret=true 时日志逐行掩码且列表接口脱敏
type Var struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Secret bool   `json:"secret"`
}

// Trigger 触发器: 手动 / Webhook / cron 三选可叠加
type Trigger struct {
	Manual  bool   `json:"manual"`
	Webhook bool   `json:"webhook"`
	Secret  string `json:"secret"` // webhook 凭证
	Cron    string `json:"cron"`   // 5 字段 cron, 空=禁用
}

// Step 阶段内的最小执行单元
type Step struct {
	Name           string `json:"name"`
	Command        string `json:"command"`
	ContinueOnFail bool   `json:"continueOnFail"`
	TimeoutMin     int    `json:"timeoutMin"`
}

// Stage 顺序执行的阶段, 回答"在哪台主机上做什么"
type Stage struct {
	Name      string `json:"name"`
	Host      string `json:"host"`      // 目标主机 ID, 空=本机
	Workspace string `json:"workspace"` // 工作目录, 空=默认
	Steps     []Step `json:"steps"`
}

// Source 代码源: 配置后引擎在首阶段自动注入"拉取代码"步骤
type Source struct {
	RepoID string `json:"repoId"` // 空=不自动拉取
	Branch string `json:"branch"` // 空=仓库默认分支
}

// Pipeline 流水线定义
type Pipeline struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Env         []Var     `json:"env"`
	Trigger     Trigger   `json:"trigger"`
	Stages      []Stage   `json:"stages"`
	Source      Source    `json:"source"`               // 代码源(可选)
	RegistryID  string    `json:"registryId,omitempty"` // 镜像仓库 → 注入 REGISTRY/REGISTRY_USER/REGISTRY_PASS
	KubeCredID  string    `json:"kubeCredId,omitempty"` // kubeconfig 凭据 → 注入 KUBECONFIG
	TimeoutMin  int       `json:"timeoutMin"`
	MaxRuns     int       `json:"maxRuns"`
	NotifyURL   string    `json:"notifyURL"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// StepRun / StageRun / Run 运行实例(含定义快照)
type StepRun struct {
	Name       string    `json:"name"`
	Command    string    `json:"command"`
	Status     string    `json:"status"`
	ExitCode   int       `json:"exitCode"`
	StartedAt  time.Time `json:"startedAt,omitempty"`
	FinishedAt time.Time `json:"finishedAt,omitempty"`
	DurationMs int64     `json:"durationMs"`
}

type StageRun struct {
	Name      string    `json:"name"`
	Host      string    `json:"host"`
	Workspace string    `json:"workspace"`
	Status    string    `json:"status"`
	Steps     []StepRun `json:"steps"`
}

type Run struct {
	ID         string     `json:"id"`
	PipelineID string     `json:"pipelineId"`
	Pipeline   string     `json:"pipeline"`
	Trigger    string     `json:"trigger"`
	Status     string     `json:"status"`
	Canceling  bool       `json:"canceling,omitempty"`
	Progress   int        `json:"progress"` // 完成步骤占比(读取时计算)
	Stages     []StageRun `json:"stages"`
	StartedAt  time.Time  `json:"startedAt,omitempty"`
	FinishedAt time.Time  `json:"finishedAt,omitempty"`
	DurationMs int64      `json:"durationMs"`
	Error      string     `json:"error,omitempty"`
}

// runProgress 已进入终态的步骤占比(0-100)
func runProgress(r *Run) int {
	total, done := 0, 0
	for _, st := range r.Stages {
		for _, sp := range st.Steps {
			total++
			switch sp.Status {
			case StatusSuccess, StatusFailed, StatusSkipped, StatusCanceled:
				done++
			}
		}
	}
	if total == 0 {
		return 0
	}
	return done * 100 / total
}

// ExecFunc 在目标主机上执行一条 shell 命令; onLine 逐行回传输出(远程步骤可能整块一次回传)。
// 返回退出码; ctx 取消时应尽快中断(本机 kill, 远程放弃等待)。
type ExecFunc func(ctx context.Context, hostID, workspace, command string, env []Var, onLine func(string)) (int, error)

// 默认并发与队列参数
const (
	DefaultMaxParallel = 2
	DefaultQueueSize   = 64
	DefaultMaxRuns     = 50
)

type runRequest struct {
	run      *Run
	pipeline *Pipeline
	rt       *runtimeCtx
}

// runtimeCtx 触发时解析的运行时资源(不落盘): 注入环境/克隆步骤/kubeconfig
type runtimeCtx struct {
	env      []Var // 内置变量 + 凭据注入 + 用户变量(用户可覆盖)
	clone    *Step // 首阶段自动插入的拉取代码步骤(nil=无)
	kubeB64  string
	kubePath string // 目标主机上的 kubeconfig 临时路径
}

// resolveRuntime 触发时解析代码源/镜像仓库/kubeconfig 凭据为运行时资源
func (e *Engine) resolveRuntime(p *Pipeline, buildNumber int) (*runtimeCtx, error) {
	// 安全护栏: 代码源要求首阶段显式工作目录 —— 防止 git 操作落到服务器进程 cwd
	if p.Source.RepoID != "" {
		if len(p.Stages) == 0 || strings.TrimSpace(p.Stages[0].Workspace) == "" {
			return nil, errors.New("启用代码源后, 首阶段必须设置工作目录(防误伤服务器目录)")
		}
	}
	rt := &runtimeCtx{}
	// 1. 内置变量
	env := []Var{
		{Name: "CICD_RUN_ID", Value: ""},
		{Name: "CICD_PIPELINE_ID", Value: p.ID},
		{Name: "CICD_PIPELINE_NAME", Value: shq(p.Name)},
		{Name: "CICD_TRIGGER", Value: ""},
		{Name: "CICD_BUILD_NUMBER", Value: fmt.Sprintf("%d", buildNumber)},
		{Name: "BUILD_NUMBER", Value: fmt.Sprintf("%d", buildNumber)}, // 简写别名
	}
	// 2. 代码源: 凭据注入 + 克隆命令
	if p.Source.RepoID != "" {
		if repo, ok := e.getRepo(p.Source.RepoID); ok {
			cred := e.credFor(repo.CredID)
			if cred != nil && cred.Type == CredGit {
				env = append(env,
					Var{Name: "GIT_REPO_USER", Value: cred.Username},
					Var{Name: "GIT_REPO_TOKEN", Value: cred.Data, Secret: true},
				)
			}
			branch := p.Source.Branch
			if branch == "" {
				branch = repo.DefaultBranch
			}
			env = append(env,
				Var{Name: "CICD_BRANCH", Value: branch},
				Var{Name: "CICD_REPO_URL", Value: repo.URL},
			)
			rt.clone = &Step{
				Name:    fmt.Sprintf("拉取代码 %s@%s", repo.Name, branch),
				Command: cloneCommand(repo.URL, branch, cred),
			}
		} else {
			log.Printf("[cicd] 流水线 %s 引用的代码仓库不存在: %s", p.Name, p.Source.RepoID)
		}
	}
	// 3. 镜像仓库凭据注入
	if p.RegistryID != "" {
		if reg, ok := e.getRegistry(p.RegistryID); ok {
			env = append(env, Var{Name: "REGISTRY", Value: reg.Server})
			if cred := e.credFor(reg.CredID); cred != nil && cred.Type == CredRegistry {
				env = append(env,
					Var{Name: "REGISTRY_USER", Value: cred.Username},
					Var{Name: "REGISTRY_PASS", Value: cred.Data, Secret: true},
				)
			}
		} else {
			log.Printf("[cicd] 流水线 %s 引用的镜像仓库不存在: %s", p.Name, p.RegistryID)
		}
	}
	// 4. kubeconfig: base64 编码待写入目标主机
	if p.KubeCredID != "" {
		if cred := e.credFor(p.KubeCredID); cred != nil && cred.Type == CredKubeconfig && cred.Data != "" {
			rt.kubeB64 = base64.StdEncoding.EncodeToString([]byte(cred.Data))
			rt.kubePath = "/tmp/.opscore-kubeconfig-" + p.ID + ".yaml"
		} else {
			log.Printf("[cicd] 流水线 %s 引用的 kubeconfig 凭据不存在或类型不符: %s", p.Name, p.KubeCredID)
		}
	}
	// 5. 用户变量最后追加(可覆盖内置变量)
	env = append(env, p.Env...)
	rt.env = env
	return rt, nil
}

// cloneCommand 首阶段自动拉取代码: 已有仓库则重置到远端分支, 否则浅克隆。
// 安全护栏: 仅当目录内 .git 的远端与目标仓库同名(按仓库名比对, 忽略协议差异)时
// 才允许 fetch/reset/clean; 否则报错退出 —— 杜绝在无关目录(如服务器工作目录)里重置。
func cloneCommand(url, branch string, cred *Credential) string {
	auth := gitAuthURL(url, cred)
	name := repoName(url)
	return fmt.Sprintf(
		"if [ -d .git ]; then R=$(git remote get-url origin 2>/dev/null | sed 's#.*/##; s#\\.git$##'); "+
			"if [ \"$R\" != %s ]; then echo \"工作目录是其他仓库($R), 拒绝重置\"; exit 64; fi; "+
			"git fetch origin %s && git reset --hard origin/%s && git clean -fd; "+
			"else git clone --depth 1 -b %s %s .; fi",
		shq(name), shq(branch), shq(branch), shq(branch), shq(auth),
	)
}

// repoName 从仓库地址提取仓库名(忽略协议与 .git 后缀)
func repoName(raw string) string {
	s := strings.TrimSuffix(strings.TrimPrefix(raw, "ssh://"), ".git")
	if i := strings.LastIndexAny(s, "/:"); i >= 0 {
		s = s[i+1:]
	}
	return strings.TrimSuffix(s, ".git")
}

// Engine CI/CD 引擎
type Engine struct {
	pipelinesFile  *store.JSONFile
	runsFile       *store.JSONFile
	logDir         string
	credsFile      *store.JSONFile
	reposFile      *store.JSONFile
	registriesFile *store.JSONFile
	scriptsFile    *store.JSONFile

	Exec ExecFunc // main.go 注入

	mu         sync.RWMutex
	pipes      []*Pipeline
	runs       []*Run
	creds      []*Credential
	repos      []*Repo
	registries []*Registry
	scripts    []*Script
	cancelFn   map[string]context.CancelFunc // runID → 取消函数(running 时有效)

	sem   chan struct{}
	queue chan runRequest
	stop  chan struct{}

	crons    map[string]*CronSpec // pipelineID → 预解析的 cron
	lastFire map[string]int64     // pipelineID → 上次 cron 触发的 unix 分钟
	stopOnce sync.Once
}

// NewEngine 初始化引擎并恢复持久化状态
func NewEngine(dataDir string) (*Engine, error) {
	dir := filepath.Join(dataDir, "cicd")
	pf, err := store.New(dir, "pipelines.json")
	if err != nil {
		return nil, err
	}
	rf, err := store.New(dir, "runs.json")
	if err != nil {
		return nil, err
	}
	cf, err := store.New(dir, "credentials.json")
	if err != nil {
		return nil, err
	}
	rpf, err := store.New(dir, "repos.json")
	if err != nil {
		return nil, err
	}
	rgf, err := store.New(dir, "registries.json")
	if err != nil {
		return nil, err
	}
	sf, err := store.New(dir, "scripts.json")
	if err != nil {
		return nil, err
	}
	logDir := filepath.Join(dir, "logs")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return nil, err
	}
	e := &Engine{
		pipelinesFile:  pf,
		runsFile:       rf,
		credsFile:      cf,
		reposFile:      rpf,
		registriesFile: rgf,
		scriptsFile:    sf,
		logDir:         logDir,
		cancelFn:       map[string]context.CancelFunc{},
		sem:            make(chan struct{}, maxParallelFromEnv()),
		queue:          make(chan runRequest, DefaultQueueSize),
		stop:           make(chan struct{}),
		crons:          map[string]*CronSpec{},
		lastFire:       map[string]int64{},
	}
	if err := pf.Read(&e.pipes); err != nil {
		log.Printf("[cicd] 加载流水线失败: %v", err)
	}
	if err := rf.Read(&e.runs); err != nil {
		log.Printf("[cicd] 加载运行历史失败: %v", err)
	}
	if err := cf.Read(&e.creds); err != nil {
		log.Printf("[cicd] 加载凭据失败: %v", err)
	}
	if err := rpf.Read(&e.repos); err != nil {
		log.Printf("[cicd] 加载仓库失败: %v", err)
	}
	if err := rgf.Read(&e.registries); err != nil {
		log.Printf("[cicd] 加载镜像仓库失败: %v", err)
	}
	if err := sf.Read(&e.scripts); err != nil {
		log.Printf("[cicd] 加载脚本库失败: %v", err)
	}
	e.recoverOrphans()
	e.rebuildCrons()
	go e.worker()
	e.startCronLoop()
	log.Printf("[cicd] 引擎就绪: %d 条流水线, %d 条运行历史, %d 凭据, %d 仓库, %d 镜像仓库, %d 脚本",
		len(e.pipes), len(e.runs), len(e.creds), len(e.repos), len(e.registries), len(e.scripts))
	return e, nil
}

// Stop 停止 cron 循环(不中断在跑任务, 交由优雅退出)
func (e *Engine) Stop() {
	e.stopOnce.Do(func() { close(e.stop) })
}

func maxParallelFromEnv() int {
	n := DefaultMaxParallel
	if v := strings.TrimSpace(os.Getenv("OPCORE_CICD_MAXRUNS")); v != "" {
		if x := parseInt(v); x > 0 && x <= 16 {
			n = x
		}
	}
	return n
}

func parseInt(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// recoverOrphans 服务重启时把非终态运行标记为 failed(日志停在最后写入处)
func (e *Engine) recoverOrphans() {
	changed := false
	for _, r := range e.runs {
		if r.Status == StatusQueued || r.Status == StatusRunning {
			r.Status = StatusFailed
			r.Error = "服务重启中断"
			r.FinishedAt = time.Now()
			if !r.StartedAt.IsZero() {
				r.DurationMs = time.Since(r.StartedAt).Milliseconds()
			}
			changed = true
		}
	}
	if changed {
		e.persistRunsLocked()
	}
}

func (e *Engine) rebuildCrons() {
	for _, p := range e.pipes {
		if p.Trigger.Cron != "" {
			if spec, err := ParseCron(p.Trigger.Cron); err == nil {
				e.crons[p.ID] = spec
			}
		}
	}
}

// ── 流水线 CRUD ──────────────────────────────────────────────

// ListPipelines 返回全部流水线(secret 脱敏)
func (e *Engine) ListPipelines() []Pipeline {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]Pipeline, 0, len(e.pipes))
	for _, p := range e.pipes {
		out = append(out, maskPipeline(*p))
	}
	return out
}

// GetPipeline 返回单条流水线(编辑用, 不脱敏)
func (e *Engine) GetPipeline(id string) (Pipeline, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	for _, p := range e.pipes {
		if p.ID == id {
			return *p, true
		}
	}
	return Pipeline{}, false
}

// SavePipeline 新建或更新(id 为空则新建)。校验名称唯一与 cron 语法。
func (e *Engine) SavePipeline(p *Pipeline) error {
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		return errors.New("流水线名称不能为空")
	}
	p.Trigger.Cron = strings.TrimSpace(p.Trigger.Cron)
	var spec *CronSpec
	if p.Trigger.Cron != "" {
		s, err := ParseCron(p.Trigger.Cron)
		if err != nil {
			return err
		}
		spec = s
	}
	if p.MaxRuns == 0 {
		p.MaxRuns = DefaultMaxRuns
	}
	if p.Trigger.Webhook && p.Trigger.Secret == "" {
		p.Trigger.Secret = NewSecret()
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if p.ID == "" {
		for _, q := range e.pipes {
			if q.Name == p.Name {
				return fmt.Errorf("流水线 %q 已存在", p.Name)
			}
		}
		p.ID = "pl-" + randHex(3)
		p.CreatedAt = time.Now()
		p.UpdatedAt = p.CreatedAt
		e.pipes = append(e.pipes, p)
	} else {
		found := false
		for _, q := range e.pipes {
			if q.ID == p.ID {
				if q.Name != p.Name {
					for _, o := range e.pipes {
						if o.ID != p.ID && o.Name == p.Name {
							return fmt.Errorf("流水线 %q 已存在", p.Name)
						}
					}
				}
				p.CreatedAt = q.CreatedAt
				p.UpdatedAt = time.Now()
				*q = *p
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("流水线不存在: %s", p.ID)
		}
	}
	if spec != nil {
		e.crons[p.ID] = spec
	} else {
		delete(e.crons, p.ID)
	}
	e.persistPipesLocked()
	return nil
}

// DeletePipeline 删除流水线及其全部运行历史与日志; 运行中拒绝。
func (e *Engine) DeletePipeline(id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, r := range e.runs {
		if r.PipelineID == id && (r.Status == StatusQueued || r.Status == StatusRunning) {
			return errors.New("流水线存在运行中/排队的任务, 请先取消后再删除")
		}
	}
	idx := -1
	for i, p := range e.pipes {
		if p.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("流水线不存在: %s", id)
	}
	e.pipes = append(e.pipes[:idx], e.pipes[idx+1:]...)
	rest := e.runs[:0]
	for _, r := range e.runs {
		if r.PipelineID == id {
			os.Remove(e.logPath(r.ID))
			continue
		}
		rest = append(rest, r)
	}
	e.runs = rest
	delete(e.crons, id)
	delete(e.lastFire, id)
	e.persistPipesLocked()
	e.persistRunsLocked()
	return nil
}

// maskPipeline 返回脱敏副本(列表/历史接口用)
func maskPipeline(p Pipeline) Pipeline {
	for i, v := range p.Env {
		if v.Secret {
			p.Env[i].Value = "******"
		}
	}
	if p.Trigger.Secret != "" {
		p.Trigger.Secret = "" // 列表不回 secret; 完整值走 GetPipeline
	}
	return p
}

// ── 触发与取消 ───────────────────────────────────────────────

// Trigger 按 ID 触发一次运行(手动/webhook/cron 共用)
func (e *Engine) Trigger(pipelineID, trigger string) (*Run, error) {
	e.mu.RLock()
	var pipe *Pipeline
	for _, p := range e.pipes {
		if p.ID == pipelineID {
			pipe = p
			break
		}
	}
	if pipe == nil {
		e.mu.RUnlock()
		return nil, fmt.Errorf("流水线不存在: %s", pipelineID)
	}
	buildNumber := 1
	for _, r := range e.runs {
		if r.PipelineID == pipelineID {
			buildNumber++
			if r.Status == StatusQueued || r.Status == StatusRunning {
				e.mu.RUnlock()
				return nil, errors.New("该流水线已有运行中/排队的任务")
			}
		}
	}
	snap := *pipe // 定义快照, 后续编辑不影响在跑任务
	e.mu.RUnlock()

	rt, err := e.resolveRuntime(&snap, buildNumber)
	if err != nil {
		return nil, err
	}
	run := newRun(&snap, trigger, rt)
	run.Progress = 0
	for i := range rt.env {
		switch rt.env[i].Name {
		case "CICD_RUN_ID":
			rt.env[i].Value = run.ID
		case "CICD_TRIGGER":
			rt.env[i].Value = trigger
		}
	}
	e.mu.Lock()
	e.runs = append(e.runs, run)
	e.mu.Unlock()
	e.persistRuns()

	select {
	case e.queue <- runRequest{run: run, pipeline: &snap, rt: rt}:
	default:
		e.finalize(run, StatusFailed, "队列已满")
		return nil, errors.New("执行队列已满, 请稍后重试")
	}
	return run, nil
}

// Cancel 取消运行: 排队中直接置 canceled; 运行中通知执行 goroutine
func (e *Engine) Cancel(runID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, r := range e.runs {
		if r.ID != runID {
			continue
		}
		switch r.Status {
		case StatusQueued:
			r.Status = StatusCanceled
			r.FinishedAt = time.Now()
			e.persistRunsLocked()
			return nil
		case StatusRunning:
			r.Canceling = true
			if fn, ok := e.cancelFn[runID]; ok && fn != nil {
				fn()
			}
			return nil
		default:
			return fmt.Errorf("运行已结束, 无法取消")
		}
	}
	return fmt.Errorf("运行不存在: %s", runID)
}

// worker 从队列取请求, 经信号量限流后执行
func (e *Engine) worker() {
	for {
		select {
		case req := <-e.queue:
			go func(r runRequest) {
				e.sem <- struct{}{}
				defer func() { <-e.sem }()
				e.execute(r)
			}(req)
		case <-e.stop:
			return
		}
	}
}

func newRun(p *Pipeline, trigger string, rt *runtimeCtx) *Run {
	run := &Run{
		ID:         fmt.Sprintf("run-%d-%s", time.Now().UnixMilli(), randHex(2)),
		PipelineID: p.ID,
		Pipeline:   p.Name,
		Trigger:    trigger,
		Status:     StatusQueued,
	}
	for i, st := range p.Stages {
		sr := StageRun{Name: st.Name, Host: st.Host, Workspace: st.Workspace, Status: StatusPending}
		for _, sp := range st.Steps {
			sr.Steps = append(sr.Steps, StepRun{Name: sp.Name, Command: sp.Command, Status: StatusPending})
		}
		// 代码源配置时, 首阶段自动插入拉取代码步骤(置于最前)
		if i == 0 && rt != nil && rt.clone != nil {
			sr.Steps = append([]StepRun{{
				Name: rt.clone.Name, Command: rt.clone.Command, Status: StatusPending,
			}}, sr.Steps...)
		}
		run.Stages = append(run.Stages, sr)
	}
	return run
}

// execute 执行一次运行: 顺序阶段 → 顺序步骤, 状态实时落盘, 日志写文件
func (e *Engine) execute(req runRequest) {
	p := req.pipeline
	run := req.run
	rt := req.rt
	if rt == nil {
		rt = &runtimeCtx{env: append([]Var{}, p.Env...)}
	}
	e.mu.Lock()
	if run.Status != StatusQueued { // 排队期间被取消
		e.mu.Unlock()
		return
	}
	run.Status = StatusRunning
	run.StartedAt = time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	if p.TimeoutMin > 0 {
		ctx, cancel = context.WithTimeout(context.Background(), time.Duration(p.TimeoutMin)*time.Minute)
	}
	e.cancelFn[run.ID] = cancel
	e.mu.Unlock()
	defer cancel()

	secrets := collectSecretsFromEnv(rt.env)
	logFile, err := os.OpenFile(e.logPath(run.ID), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		e.finalize(run, StatusFailed, "创建日志文件失败: "+err.Error())
		return
	}
	defer logFile.Close()
	var logMu sync.Mutex
	writeLine := func(line string) {
		logMu.Lock()
		defer logMu.Unlock()
		line = maskLine(line, secrets)
		logFile.WriteString(time.Now().Format("[15:04:05.000] ") + line + "\n")
	}

	// kubeconfig: 已写入文件的主机集合(每主机写一次), 运行结束统一清理
	kubeDone := map[string]bool{}
	kubeEnv := func(host string) []Var {
		if rt.kubeB64 == "" || kubeDone[host] {
			return nil
		}
		kubeDone[host] = true
		cmd := fmt.Sprintf("echo %s | base64 -d > %s && chmod 600 %s", rt.kubeB64, rt.kubePath, rt.kubePath)
		writeLine(fmt.Sprintf("──── [准备] 写入 kubeconfig → %s @ %s ────", rt.kubePath, displayHost(host)))
		if _, err := e.execCall(ctx, host, "", cmd, nil, writeLine); err != nil {
			writeLine("[error] kubeconfig 写入失败: " + err.Error())
		}
		return []Var{{Name: "KUBECONFIG", Value: rt.kubePath}}
	}

	runStatus := StatusSuccess
	runErr := ""
overall:
	for i := range p.Stages {
		stage := &p.Stages[i]
		sr := &run.Stages[i]
		e.mu.Lock()
		sr.Status = StatusRunning
		e.mu.Unlock()
		e.persistRuns()
		writeLine(fmt.Sprintf("════ [阶段 %d/%d] %s @ %s ════", i+1, len(p.Stages), stage.Name, displayHost(stage.Host)))
		stageEnv := append(append([]Var{}, rt.env...), kubeEnv(stage.Host)...)

		// 运行步骤视图与定义步骤对齐: 首阶段可能自动插入了"拉取代码"步骤(newRun)
		defs := stage.Steps
		if i == 0 && rt.clone != nil {
			defs = make([]Step, 0, len(stage.Steps)+1)
			defs = append(defs, *rt.clone)
			defs = append(defs, stage.Steps...)
		}

		stepStatus := StatusSuccess
	stepLoop:
		for j := range defs {
			step := &defs[j]
			spr := &sr.Steps[j]
			if err := ctx.Err(); err != nil {
				e.mu.Lock()
				spr.Status = StatusCanceled
				e.mu.Unlock()
				continue
			}
			e.mu.Lock()
			spr.Status = StatusRunning
			spr.StartedAt = time.Now()
			e.mu.Unlock()
			e.persistRuns()
			writeLine(fmt.Sprintf("──── [步骤 %d/%d] %s ────", j+1, len(defs), step.Name))

			stepCtx, stepCancel := context.WithCancel(ctx)
			if step.TimeoutMin > 0 {
				stepCtx, stepCancel = context.WithTimeout(ctx, time.Duration(step.TimeoutMin)*time.Minute)
			}
			exit, execErr := e.execCall(stepCtx, stage.Host, stage.Workspace, step.Command, stageEnv, writeLine)
			stepCancel()
			e.mu.Lock()
			spr.FinishedAt = time.Now()
			spr.DurationMs = spr.FinishedAt.Sub(spr.StartedAt).Milliseconds()
			spr.ExitCode = exit
			switch {
			case ctx.Err() != nil:
				spr.Status = StatusCanceled
			case execErr != nil || exit != 0:
				spr.Status = StatusFailed
			default:
				spr.Status = StatusSuccess
			}
			failed := spr.Status == StatusFailed
			e.mu.Unlock()
			if execErr != nil {
				writeLine("[error] " + execErr.Error())
			}
			writeLine(fmt.Sprintf("──── [步骤 %d/%d] %s → %s (exit %d, %s) ────",
				j+1, len(defs), step.Name, spr.Status, exit, time.Duration(spr.DurationMs).Round(time.Millisecond)))

			if failed && !step.ContinueOnFail {
				stepStatus = StatusFailed
				// 同阶段后续步骤全部跳过
				for k := j + 1; k < len(sr.Steps); k++ {
					e.mu.Lock()
					sr.Steps[k].Status = StatusSkipped
					e.mu.Unlock()
				}
				break stepLoop
			}
			// continueOnFail: 步骤标记 failed 但不阻断阶段与流水线(GitLab allow_failure 语义)
		}

		e.mu.Lock()
		if ctx.Err() != nil {
			sr.Status = StatusCanceled
		} else {
			sr.Status = stepStatus
		}
		e.mu.Unlock()
		e.persistRuns()
		writeLine(fmt.Sprintf("════ [阶段 %d/%d] %s → %s ════", i+1, len(p.Stages), stage.Name, sr.Status))

		if sr.Status != StatusSuccess {
			runStatus = sr.Status
			if sr.Status == StatusFailed {
				runErr = fmt.Sprintf("阶段 %q 失败", stage.Name)
			}
			// 后续阶段跳过
			for k := i + 1; k < len(p.Stages); k++ {
				e.mu.Lock()
				run.Stages[k].Status = StatusSkipped
				e.mu.Unlock()
			}
			break overall
		}
	}

	// kubeconfig 临时文件清理(尽力而为, 失败不影响结果)
	if len(kubeDone) > 0 {
		cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 30*time.Second)
		for host := range kubeDone {
			_, _ = e.execCall(cleanCtx, host, "", "rm -f "+shq(rt.kubePath), nil, writeLine)
		}
		cleanCancel()
	}
	e.finalize(run, runStatus, runErr)
}

// finalize 收尾: 终态落盘 + 清理 + 通知
func (e *Engine) finalize(run *Run, status, errMsg string) {
	e.mu.Lock()
	run.Status = status
	run.Error = errMsg
	run.Canceling = false
	run.FinishedAt = time.Now()
	if !run.StartedAt.IsZero() {
		run.DurationMs = run.FinishedAt.Sub(run.StartedAt).Milliseconds()
	}
	delete(e.cancelFn, run.ID)
	e.persistRunsLocked()
	e.mu.Unlock()

	if status == StatusFailed {
		log.Printf("[cicd] 运行 %s (%s) 失败: %s", run.ID, run.Pipeline, errMsg)
	} else {
		log.Printf("[cicd] 运行 %s (%s) %s, 耗时 %s", run.ID, run.Pipeline, status, time.Duration(run.DurationMs).Round(time.Millisecond))
	}
	e.pruneRuns(run.PipelineID)
	e.notify(run)
}

// pruneRuns 按 MaxRuns 滚动裁剪历史并删除对应日志
func (e *Engine) pruneRuns(pipelineID string) {
	e.mu.Lock()
	var keep []*Run
	var mine []*Run
	for _, r := range e.runs {
		if r.PipelineID == pipelineID {
			mine = append(mine, r)
		} else {
			keep = append(keep, r)
		}
	}
	maxN := DefaultMaxRuns
	for _, p := range e.pipes {
		if p.ID == pipelineID && p.MaxRuns > 0 {
			maxN = p.MaxRuns
		}
	}
	if len(mine) > maxN {
		// mine 按 append 顺序即时间顺序, 裁掉最旧
		for _, old := range mine[:len(mine)-maxN] {
			os.Remove(e.logPath(old.ID))
		}
		mine = mine[len(mine)-maxN:]
	}
	e.runs = append(keep, mine...)
	e.persistRunsLocked()
	e.mu.Unlock()
}

// notify 终态通知(notifyURL, 10s 超时, 失败不重试)
func (e *Engine) notify(run *Run) {
	e.mu.RLock()
	var url string
	for _, p := range e.pipes {
		if p.ID == run.PipelineID {
			url = p.NotifyURL
			break
		}
	}
	e.mu.RUnlock()
	if url == "" {
		return
	}
	go func() {
		body, _ := json.Marshal(map[string]any{
			"runId": run.ID, "pipelineId": run.PipelineID, "pipeline": run.Pipeline,
			"status": run.Status, "durationMs": run.DurationMs, "trigger": run.Trigger,
			"finishedAt": run.FinishedAt, "error": run.Error,
		})
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			log.Printf("[cicd] 通知失败 %s: %v", url, err)
			return
		}
		resp.Body.Close()
	}()
}

// ── 查询接口(HTTP 层用) ─────────────────────────────────────

// ListRuns 运行历史(倒序); pipelineID 为空=全部
func (e *Engine) ListRuns(pipelineID string, limit int) []Run {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if limit <= 0 {
		limit = 50
	}
	out := make([]Run, 0, limit)
	for i := len(e.runs) - 1; i >= 0 && len(out) < limit; i-- {
		r := *e.runs[i]
		if pipelineID == "" || r.PipelineID == pipelineID {
			r.Progress = runProgress(&r)
			out = append(out, r)
		}
	}
	return out
}

// GetRun 运行详情(含终态与取消中标记)
func (e *Engine) GetRun(runID string) (Run, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	for _, r := range e.runs {
		if r.ID == runID {
			c := *r
			c.Progress = runProgress(&c)
			return c, true
		}
	}
	return Run{}, false
}

// Overview 概览统计
func (e *Engine) Overview() map[string]any {
	e.mu.RLock()
	defer e.mu.RUnlock()
	running, queued := 0, 0
	ok24, fail24 := 0, 0
	day := time.Now().Add(-24 * time.Hour)
	for _, r := range e.runs {
		switch r.Status {
		case StatusRunning:
			running++
		case StatusQueued:
			queued++
		case StatusSuccess:
			if r.FinishedAt.After(day) {
				ok24++
			}
		case StatusFailed:
			if r.FinishedAt.After(day) {
				fail24++
			}
		}
	}
	recent := make([]Run, 0, 10)
	for i := len(e.runs) - 1; i >= 0 && len(recent) < 10; i-- {
		r := *e.runs[i]
		r.Progress = runProgress(&r)
		recent = append(recent, r)
	}
	return map[string]any{
		"pipelines":  len(e.pipes),
		"running":    running,
		"queued":     queued,
		"success24h": ok24,
		"failed24h":  fail24,
		"recentRuns": recent,
	}
}

// ReadLog 从偏移读取日志, 返回内容与新偏移
func (e *Engine) ReadLog(runID string, offset int64) (string, int64, error) {
	path := e.logPath(runID)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", 0, nil
		}
		return "", 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", 0, err
	}
	if offset < 0 || offset > st.Size() {
		offset = 0
	}
	if offset == st.Size() {
		return "", offset, nil
	}
	buf := make([]byte, st.Size()-offset)
	if _, err := f.ReadAt(buf, offset); err != nil {
		return "", offset, err
	}
	return string(buf), st.Size(), nil
}

func (e *Engine) logPath(runID string) string {
	return filepath.Join(e.logDir, runID+".log")
}

// ── 内部工具 ────────────────────────────────────────────────

func (e *Engine) persistPipesLocked() {
	if err := e.pipelinesFile.Write(e.pipes); err != nil {
		log.Printf("[cicd] 持久化流水线失败: %v", err)
	}
}

func (e *Engine) persistRunsLocked() {
	if err := e.runsFile.Write(e.runs); err != nil {
		log.Printf("[cicd] 持久化运行失败: %v", err)
	}
}

func (e *Engine) persistRuns() {
	e.mu.RLock()
	defer e.mu.RUnlock()
	e.persistRunsLocked()
}

// collectSecrets 收集需要掩码的值(Secret 且长度≥4)
func collectSecrets(env []Var) []string {
	var out []string
	for _, v := range env {
		if v.Secret && len(v.Value) >= 4 {
			out = append(out, v.Value)
		}
	}
	return out
}

// collectSecretsFromEnv 同 collectSecrets(显式命名, 引擎运行时用)
func collectSecretsFromEnv(env []Var) []string { return collectSecrets(env) }

// maskLine 逐行替换敏感值
func maskLine(line string, secrets []string) string {
	for _, s := range secrets {
		line = strings.ReplaceAll(line, s, "******")
	}
	return line
}

func displayHost(hostID string) string {
	if hostID == "" {
		return "本机"
	}
	return hostID
}

func randHex(nBytes int) string {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano()%0xffffff)
	}
	return strings.ToUpper(hex.EncodeToString(b))
}

// NewSecret 生成 32 位 webhook 凭证
func NewSecret() string { return randHex(16) }

// execCall Exec 回调包装: 未注入时返回明确错误(单测场景)
func (e *Engine) execCall(ctx context.Context, hostID, workspace, command string, env []Var, onLine func(string)) (int, error) {
	if e.Exec == nil {
		return -1, fmt.Errorf("执行回调未初始化")
	}
	return e.Exec(ctx, hostID, workspace, command, env, onLine)
}
