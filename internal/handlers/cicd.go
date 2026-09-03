package handlers

// CI/CD 流水线模块 HTTP 层: 引擎见 internal/cicd, 本文件只做
// 参数校验(白名单正则)/分发/响应封装, 与其他模块写操作安全管线一致。

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"opscore/internal/cicd"
)

var cicdEngine *cicd.Engine

// InitCicd 注入引擎(main.go 构造后调用)
func InitCicd(e *cicd.Engine) { cicdEngine = e }

// ── 校验白名单 ──────────────────────────────────────────────

var (
	reCicdName   = regexp.MustCompile(`^[^<>{}\r\n]{1,64}$`)
	reCicdID     = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	reCicdNotify = regexp.MustCompile(`^$|^https?://\S+$`)
)

func cicdValidatePipeline(p *cicd.Pipeline) string {
	if !reCicdName.MatchString(strings.TrimSpace(p.Name)) {
		return "流水线名称无效(1-64 字符, 不含 <>{})"
	}
	if len(p.Description) > 256 {
		return "描述过长(≤256 字符)"
	}
	if p.TimeoutMin < 0 || p.TimeoutMin > 1440 {
		return "整体超时无效(0-1440 分钟)"
	}
	if p.MaxRuns != 0 && (p.MaxRuns < 5 || p.MaxRuns > 500) {
		return "历史保留条数无效(5-500)"
	}
	if !reCicdNotify.MatchString(p.NotifyURL) {
		return "通知 URL 无效(需 http/https 或留空)"
	}
	if p.Source.RepoID != "" && !reCicdID.MatchString(p.Source.RepoID) {
		return "代码仓库引用无效"
	}
	if len(p.Source.Branch) > 64 {
		return "分支名过长(≤64 字符)"
	}
	if p.RegistryID != "" && !reCicdID.MatchString(p.RegistryID) {
		return "镜像仓库引用无效"
	}
	if p.KubeCredID != "" && !reCicdID.MatchString(p.KubeCredID) {
		return "kubeconfig 凭据引用无效"
	}
	for _, v := range p.Env {
		if v.Name == "" || strings.ContainsAny(v.Name, " \t\r\n") || len(v.Name) > 64 {
			return "环境变量名无效(非空, 无空白, ≤64 字符)"
		}
		if len(v.Value) > 4096 {
			return fmt.Sprintf("环境变量 %s 值过长(≤4096)", v.Name)
		}
	}
	if len(p.Stages) == 0 || len(p.Stages) > 20 {
		return "阶段数量无效(1-20)"
	}
	for i, st := range p.Stages {
		if !reCicdName.MatchString(strings.TrimSpace(st.Name)) {
			return fmt.Sprintf("阶段名无效: %q", st.Name)
		}
		if st.Host != "" && !reCicdID.MatchString(st.Host) {
			return fmt.Sprintf("阶段 %q 目标主机 ID 无效", st.Name)
		}
		if len(st.Workspace) > 256 {
			return fmt.Sprintf("阶段 %q 工作目录过长", st.Name)
		}
		// 安全护栏: 启用代码源后首阶段必须显式工作目录(防 git 操作落到服务器进程 cwd)
		if i == 0 && p.Source.RepoID != "" && strings.TrimSpace(st.Workspace) == "" {
			return "启用代码源后, 首阶段必须设置工作目录"
		}
		if len(st.Steps) == 0 || len(st.Steps) > 50 {
			return fmt.Sprintf("阶段 %q 步骤数量无效(1-50)", st.Name)
		}
		for _, sp := range st.Steps {
			if !reCicdName.MatchString(strings.TrimSpace(sp.Name)) {
				return fmt.Sprintf("阶段 %q 存在无效步骤名", st.Name)
			}
			if strings.TrimSpace(sp.Command) == "" {
				return fmt.Sprintf("步骤 %q 命令不能为空", sp.Name)
			}
			if len(sp.Command) > 8192 {
				return fmt.Sprintf("步骤 %q 命令过长(≤8KB)", sp.Name)
			}
			if sp.TimeoutMin < 0 || sp.TimeoutMin > 1440 {
				return fmt.Sprintf("步骤 %q 超时无效", sp.Name)
			}
		}
	}
	return ""
}

// ── 执行回调(main.go 注入 engine.Exec) ─────────────────────

// CicdExec 在目标主机上执行步骤命令: 本机逐行流式; 远程 SSH 单会话(完成后整块回传)。
// 命令语义为 POSIX shell(sh -c); 远程参数经 Shq 单引号转义防注入。
func CicdExec(ctx context.Context, hostID, workspace, command string, env []cicd.Var, onLine func(string)) (int, error) {
	if IsLocalTarget(hostID) {
		return cicdExecLocal(ctx, workspace, command, env, onLine)
	}
	return cicdExecRemote(ctx, hostID, workspace, command, env, onLine)
}

func cicdExecLocal(ctx context.Context, workspace, command string, env []cicd.Var, onLine func(string)) (int, error) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		return -1, errors.New("本机未找到 sh(Windows 需 Git Bash, 或将阶段目标改为远程主机)")
	}
	cmd := exec.CommandContext(ctx, sh, "-c", command)
	if workspace != "" {
		cmd.Dir = workspace
	}
	if len(env) > 0 {
		e := os.Environ()
		for _, v := range env {
			if v.Name != "" {
				e = append(e, v.Name+"="+v.Value)
			}
		}
		cmd.Env = e
	}
	stdout, perr := cmd.StdoutPipe()
	if perr != nil {
		return -1, perr
	}
	stderr, perr := cmd.StderrPipe()
	if perr != nil {
		return -1, perr
	}
	if err := cmd.Start(); err != nil {
		return -1, err
	}
	var wg sync.WaitGroup
	scan := func(rd interface{ Read([]byte) (int, error) }) {
		defer wg.Done()
		sc := bufio.NewScanner(rd)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			onLine(sc.Text())
		}
	}
	wg.Add(2)
	go scan(stdout)
	go scan(stderr)
	waitErr := cmd.Wait()
	wg.Wait()
	if ctx.Err() != nil {
		return -1, ctx.Err()
	}
	if waitErr == nil {
		return 0, nil
	}
	var ee *exec.ExitError
	if errors.As(waitErr, &ee) {
		return ee.ExitCode(), nil
	}
	return -1, waitErr
}

func cicdExecRemote(ctx context.Context, hostID, workspace, command string, env []cicd.Var, onLine func(string)) (int, error) {
	h := resolveAnsibleHost(hostID)
	if h == nil {
		return -1, fmt.Errorf("目标主机不存在: %s", hostID)
	}
	if remotePool == nil {
		return -1, errors.New("远程执行池未初始化")
	}
	rm := resolveRemoteHost(*h)
	script := cicdRemoteScript(workspace, command, env)
	line := ArgsToLine([]string{"sh", "-c", script})
	type execResult struct {
		out string
		rc  int
		err error
	}
	ch := make(chan execResult, 1)
	go func() {
		out, rc, err := remotePool.ExecLine(rm, line)
		ch <- execResult{out, rc, err}
	}()
	var res execResult
	select {
	case res = <-ch:
	case <-ctx.Done():
		// SSH 会话无法安全终止, 放弃等待; 远端命令自行结束后会话释放
		return -1, ctx.Err()
	}
	if res.out != "" {
		for _, l := range strings.Split(strings.TrimRight(res.out, "\n"), "\n") {
			onLine(l)
		}
	}
	if res.err != nil {
		onLine("[error] " + res.err.Error())
		return -1, res.err
	}
	return res.rc, nil
}

// cicdRemoteScript 拼装远程脚本: env export(转义) + cd 工作目录 + 命令本体
func cicdRemoteScript(workspace, command string, env []cicd.Var) string {
	var b strings.Builder
	for _, v := range env {
		if v.Name != "" {
			b.WriteString("export " + v.Name + "=" + Shq(v.Value) + "; ")
		}
	}
	if workspace != "" {
		b.WriteString("cd " + Shq(workspace) + " || exit 64; ")
	}
	b.WriteString(command)
	return b.String()
}

// ── 流水线 CRUD ────────────────────────────────────────────

// CicdPipelines 流水线列表(含最近一次运行摘要)
func CicdPipelines(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	pipes := cicdEngine.ListPipelines()
	runs := cicdEngine.ListRuns("", 0)
	lastByPipe := map[string]cicd.Run{}
	for _, run := range runs {
		if _, ok := lastByPipe[run.PipelineID]; !ok {
			lastByPipe[run.PipelineID] = run
		}
	}
	type pipeView struct {
		cicd.Pipeline
		StageCount int       `json:"stageCount"`
		LastRun    *cicd.Run `json:"lastRun,omitempty"`
	}
	out := make([]pipeView, 0, len(pipes))
	for _, p := range pipes {
		v := pipeView{Pipeline: p, StageCount: len(p.Stages)}
		if lr, ok := lastByPipe[p.ID]; ok {
			v.LastRun = &lr
		}
		out = append(out, v)
	}
	WriteJSON(w, out)
}

// CicdPipelineGet 流水线详情(编辑用, 含真实 secret)
func CicdPipelineGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := r.URL.Query().Get("id")
	if !reCicdID.MatchString(id) {
		writeErr(w, "无效的流水线 ID", http.StatusBadRequest)
		return
	}
	p, ok := cicdEngine.GetPipeline(id)
	if !ok {
		writeErr(w, "流水线不存在", http.StatusNotFound)
		return
	}
	WriteJSON(w, p)
}

// CicdPipelineSave 新建/更新流水线
func CicdPipelineSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var p cicd.Pipeline
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&p); err != nil {
		writeErr(w, "请求格式错误", http.StatusBadRequest)
		return
	}
	if msg := cicdValidatePipeline(&p); msg != "" {
		writeErr(w, msg, http.StatusBadRequest)
		return
	}
	if err := cicdEngine.SavePipeline(&p); err != nil {
		writeErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	WriteJSON(w, map[string]any{"ok": true, "id": p.ID})
}

// CicdPipelineDelete 删除流水线(运行中拒绝)
func CicdPipelineDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, "请求格式错误", http.StatusBadRequest)
		return
	}
	if !reCicdID.MatchString(body.ID) {
		writeErr(w, "无效的流水线 ID", http.StatusBadRequest)
		return
	}
	if err := cicdEngine.DeletePipeline(body.ID); err != nil {
		writeErr(w, err.Error(), http.StatusConflict)
		return
	}
	WriteJSON(w, map[string]any{"ok": true})
}

// ── 触发/取消 ──────────────────────────────────────────────

// CicdPipelineRun 手动触发
func CicdPipelineRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, "请求格式错误", http.StatusBadRequest)
		return
	}
	if !reCicdID.MatchString(body.ID) {
		writeErr(w, "无效的流水线 ID", http.StatusBadRequest)
		return
	}
	p, ok := cicdEngine.GetPipeline(body.ID)
	if !ok {
		writeErr(w, "流水线不存在", http.StatusNotFound)
		return
	}
	if !p.Trigger.Manual {
		writeErr(w, "该流水线未启用手动触发", http.StatusForbidden)
		return
	}
	run, err := cicdEngine.Trigger(body.ID, cicd.TriggerManual)
	if err != nil {
		writeErr(w, err.Error(), http.StatusConflict)
		return
	}
	WriteJSON(w, map[string]any{"ok": true, "run": *run})
}

// CicdRunCancel 取消运行
func CicdRunCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		RunID string `json:"runId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, "请求格式错误", http.StatusBadRequest)
		return
	}
	if err := cicdEngine.Cancel(body.RunID); err != nil {
		writeErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	WriteJSON(w, map[string]any{"ok": true})
}

// CicdRunApprove 审批等待中的阶段(approve=true 放行, false 拒绝)
func CicdRunApprove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		RunID   string `json:"runId"`
		Approve bool   `json:"approve"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, "请求格式错误", http.StatusBadRequest)
		return
	}
	if err := cicdEngine.Approve(body.RunID, body.Approve); err != nil {
		writeErr(w, err.Error(), http.StatusConflict)
		return
	}
	WriteJSON(w, map[string]any{"ok": true})
}

// ── 运行历史/详情/日志 ─────────────────────────────────────

// CicdRuns 运行历史
func CicdRuns(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		fmt.Sscanf(v, "%d", &limit)
		if limit <= 0 || limit > 500 {
			limit = 50
		}
	}
	WriteJSON(w, cicdEngine.ListRuns(r.URL.Query().Get("pipeline"), limit))
}

// CicdRunGet 运行详情
func CicdRunGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	run, ok := cicdEngine.GetRun(r.URL.Query().Get("id"))
	if !ok {
		writeErr(w, "运行记录不存在", http.StatusNotFound)
		return
	}
	WriteJSON(w, run)
}

// CicdRunLog 全量/增量日志回填(SSE 断线恢复用)
func CicdRunLog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := r.URL.Query().Get("id")
	var offset int64
	if v := r.URL.Query().Get("offset"); v != "" {
		fmt.Sscanf(v, "%d", &offset)
	}
	content, newOffset, err := cicdEngine.ReadLog(id, offset)
	if err != nil {
		writeErr(w, "读取日志失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	WriteJSON(w, map[string]any{"content": content, "offset": newOffset})
}

// CicdRunStream SSE: 日志增量 + 状态推送, 终态后补发一帧并结束
func CicdRunStream(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RunID string `json:"runId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.RunID == "" {
		writeErr(w, "请求格式错误", http.StatusBadRequest)
		return
	}
	if _, ok := cicdEngine.GetRun(body.RunID); !ok {
		writeErr(w, "运行记录不存在", http.StatusNotFound)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, "SSE not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	emit := func(evtType string, payload any) {
		data, _ := json.Marshal(map[string]any{"type": evtType, "payload": payload})
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}

	var offset int64
	var lastStatus string
	ticker := time.NewTicker(800 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			run, ok := cicdEngine.GetRun(body.RunID)
			if !ok {
				emit("error", "运行记录不存在")
				emit("done", nil)
				return
			}
			if content, newOffset, err := cicdEngine.ReadLog(body.RunID, offset); err == nil && content != "" {
				offset = newOffset
				for _, line := range strings.Split(strings.TrimRight(content, "\n"), "\n") {
					emit("log", line)
				}
			}
			if sj, _ := json.Marshal(run); string(sj) != lastStatus {
				lastStatus = string(sj)
				emit("status", run)
			}
			switch run.Status {
			case cicd.StatusSuccess, cicd.StatusFailed, cicd.StatusCanceled:
				emit("done", nil)
				return
			}
		}
	}
}

// ── Webhook(唯一无 Bearer 端点, 自身凭证保障) ─────────────

// CicdWebhook POST /api/cicd/webhook/{pipelineId}
// 凭证: header X-Opscore-Token / ?token= / body.secret 任一; 兼容任意 JSON body。
func CicdWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/cicd/webhook/")
	if !reCicdID.MatchString(id) {
		writeErr(w, "无效的流水线 ID", http.StatusNotFound)
		return
	}
	p, ok := cicdEngine.GetPipeline(id)
	if !ok {
		writeErr(w, "流水线不存在", http.StatusNotFound)
		return
	}
	if !p.Trigger.Webhook || p.Trigger.Secret == "" {
		writeErr(w, "流水线未启用 Webhook", http.StatusForbidden)
		return
	}
	token := r.Header.Get("X-Opscore-Token")
	if token == "" {
		token = r.URL.Query().Get("token")
	}
	if token == "" {
		var body struct {
			Secret string `json:"secret"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err == nil {
			token = body.Secret
		}
	}
	if token == "" {
		writeErr(w, "缺少 Webhook 凭证", http.StatusUnauthorized)
		return
	}
	if subtle.ConstantTimeCompare([]byte(token), []byte(p.Trigger.Secret)) != 1 {
		writeErr(w, "Webhook 凭证错误", http.StatusForbidden)
		return
	}
	run, err := cicdEngine.Trigger(id, cicd.TriggerWebhook)
	if err != nil {
		writeErr(w, err.Error(), http.StatusConflict)
		return
	}
	WriteJSON(w, map[string]any{"ok": true, "runId": run.ID})
}

// ── 概览 ──────────────────────────────────────────────────

// CicdOverview 概览统计
func CicdOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	WriteJSON(w, cicdEngine.Overview())
}

// ── 凭据中心 ───────────────────────────────────────────────

// CicdCredentials 凭据列表(密文永不回传, 仅 hasData 标记)
func CicdCredentials(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	WriteJSON(w, cicdEngine.ListCredentials())
}

// CicdCredentialSave 新建/更新凭据(编辑时 data 留空=保持原值)
func CicdCredentialSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var c cicd.Credential
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512<<10)).Decode(&c); err != nil {
		writeErr(w, "请求格式错误", http.StatusBadRequest)
		return
	}
	if err := cicdEngine.SaveCredential(&c); err != nil {
		writeErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	WriteJSON(w, map[string]any{"ok": true, "id": c.ID})
}

// CicdCredentialDelete 删除凭据
func CicdCredentialDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, "请求格式错误", http.StatusBadRequest)
		return
	}
	if err := cicdEngine.DeleteCredential(body.ID); err != nil {
		writeErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	WriteJSON(w, map[string]any{"ok": true})
}

// ── 代码仓库 ───────────────────────────────────────────────

// CicdRepos 代码仓库列表
func CicdRepos(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	WriteJSON(w, cicdEngine.ListRepos())
}

// CicdRepoSave 新建/更新代码仓库
func CicdRepoSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var v cicd.Repo
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&v); err != nil {
		writeErr(w, "请求格式错误", http.StatusBadRequest)
		return
	}
	if err := cicdEngine.SaveRepo(&v); err != nil {
		writeErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	WriteJSON(w, map[string]any{"ok": true, "id": v.ID})
}

// CicdRepoDelete 删除代码仓库
func CicdRepoDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, "请求格式错误", http.StatusBadRequest)
		return
	}
	if err := cicdEngine.DeleteRepo(body.ID); err != nil {
		writeErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	WriteJSON(w, map[string]any{"ok": true})
}

// CicdRepoTest 连通性测试(服务端 git ls-remote)
func CicdRepoTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, "请求格式错误", http.StatusBadRequest)
		return
	}
	output, err := cicdEngine.TestRepo(body.ID)
	if err != nil {
		WriteJSON(w, map[string]any{"ok": false, "error": err.Error(), "output": output})
		return
	}
	WriteJSON(w, map[string]any{"ok": true, "output": output})
}

// ── 镜像仓库 ───────────────────────────────────────────────

// CicdRegistries 镜像仓库列表
func CicdRegistries(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	WriteJSON(w, cicdEngine.ListRegistries())
}

// CicdRegistrySave 新建/更新镜像仓库
func CicdRegistrySave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var v cicd.Registry
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&v); err != nil {
		writeErr(w, "请求格式错误", http.StatusBadRequest)
		return
	}
	if err := cicdEngine.SaveRegistry(&v); err != nil {
		writeErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	WriteJSON(w, map[string]any{"ok": true, "id": v.ID})
}

// CicdRegistryDelete 删除镜像仓库
func CicdRegistryDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, "请求格式错误", http.StatusBadRequest)
		return
	}
	if err := cicdEngine.DeleteRegistry(body.ID); err != nil {
		writeErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	WriteJSON(w, map[string]any{"ok": true})
}

// CicdRegistryTest 探活(GET /v2/, 200/401 均视为存活)
func CicdRegistryTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, "请求格式错误", http.StatusBadRequest)
		return
	}
	output, err := cicdEngine.TestRegistry(body.ID)
	if err != nil {
		WriteJSON(w, map[string]any{"ok": false, "error": err.Error(), "output": output})
		return
	}
	WriteJSON(w, map[string]any{"ok": true, "output": output})
}

// ── 脚本库 ─────────────────────────────────────────────────

// CicdScripts 脚本库列表
func CicdScripts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	WriteJSON(w, cicdEngine.ListScripts())
}

// CicdScriptSave 新建/更新脚本
func CicdScriptSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var s cicd.Script
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10)).Decode(&s); err != nil {
		writeErr(w, "请求格式错误", http.StatusBadRequest)
		return
	}
	if err := cicdEngine.SaveScript(&s); err != nil {
		writeErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	WriteJSON(w, map[string]any{"ok": true, "id": s.ID})
}

// CicdScriptDelete 删除脚本
func CicdScriptDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, "请求格式错误", http.StatusBadRequest)
		return
	}
	if err := cicdEngine.DeleteScript(body.ID); err != nil {
		writeErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	WriteJSON(w, map[string]any{"ok": true})
}
