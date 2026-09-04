package cicd

// 凭据中心 / 代码仓库 / 镜像仓库 / 脚本库 —— 流水线的外部资源定义层。
// 与 engine.go 同域: 凭据永不回传明文(Data 字段仅写), 列表只带 hasData 标记;
// 连通性测试在服务端本机执行(git ls-remote / registry /v2/ 探活)。

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"opscore/internal/store"
)

// 凭据类型
const (
	CredGit        = "git"        // 代码库账号(token/密码)
	CredRegistry   = "registry"   // 镜像仓库账号
	CredKubeconfig = "kubeconfig" // K8s 集群配置
	CredGeneric    = "generic"    // 通用密文(自用)
)

var reCredName = regexp.MustCompile(`^[^<>{}\r\n]{1,64}$`)
var reRepoURL = regexp.MustCompile(`^(https?://[^\s]+|ssh://[^\s]+|git@[^\s:]+:.+)$`)

// Credential 凭据: Data 为密文(token/密码/kubeconfig 内容), 仅写不读
type Credential struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Type      string    `json:"type"`
	Username  string    `json:"username,omitempty"` // git/registry 用户名
	Server    string    `json:"server,omitempty"`   // 备注(如 registry 地址)
	Data      string    `json:"data"`               // 密文, 列表恒为空
	HasData   bool      `json:"hasData"`            // 列表输出用
	Note      string    `json:"note,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Repo 代码仓库
type Repo struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	URL           string    `json:"url"`
	CredID        string    `json:"credId"`
	DefaultBranch string    `json:"defaultBranch"`
	Note          string    `json:"note,omitempty"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// Registry 镜像仓库
type Registry struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Server    string    `json:"server"` // 如 registry.example.com / https://...
	CredID    string    `json:"credId"`
	Note      string    `json:"note,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Script 脚本库条目
type Script struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Content     string    `json:"content"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// maskCredential 列表输出: 密文替换为 hasData 标记
func maskCredential(c Credential) Credential {
	c.HasData = c.Data != ""
	c.Data = ""
	return c
}

func validCredType(t string) bool {
	return t == CredGit || t == CredRegistry || t == CredKubeconfig || t == CredGeneric
}

// ── 凭据 CRUD ───────────────────────────────────────────────

func (e *Engine) ListCredentials() []Credential {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]Credential, 0, len(e.creds))
	for _, c := range e.creds {
		out = append(out, maskCredential(*c))
	}
	return out
}

func (e *Engine) getCredential(id string) (Credential, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	for _, c := range e.creds {
		if c.ID == id {
			return *c, true
		}
	}
	return Credential{}, false
}

// SaveCredential 新建或更新; 编辑时 Data 为空表示保持原值
func (e *Engine) SaveCredential(c *Credential) error {
	c.Name = strings.TrimSpace(c.Name)
	if !reCredName.MatchString(c.Name) {
		return errors.New("凭据名称无效(1-64 字符)")
	}
	if !validCredType(c.Type) {
		return errors.New("凭据类型无效")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if c.ID == "" {
		if c.Data == "" {
			return errors.New("凭据内容不能为空")
		}
		for _, o := range e.creds {
			if o.Name == c.Name {
				return fmt.Errorf("凭据 %q 已存在", c.Name)
			}
		}
		c.ID = "cred-" + randHex(3)
		c.UpdatedAt = time.Now()
		e.creds = append(e.creds, c)
	} else {
		found := false
		for _, o := range e.creds {
			if o.ID == c.ID {
				if o.Name != c.Name {
					for _, p := range e.creds {
						if p.ID != c.ID && p.Name == c.Name {
							return fmt.Errorf("凭据 %q 已存在", c.Name)
						}
					}
				}
				if c.Data == "" {
					c.Data = o.Data // 保持原密文
				}
				c.UpdatedAt = time.Now()
				*o = *c
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("凭据不存在: %s", c.ID)
		}
	}
	e.persistCredsLocked()
	return nil
}

func (e *Engine) DeleteCredential(id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	idx := -1
	for i, c := range e.creds {
		if c.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("凭据不存在: %s", id)
	}
	e.creds = append(e.creds[:idx], e.creds[idx+1:]...)
	e.persistCredsLocked()
	return nil
}

// ── 代码仓库 CRUD + 连通性测试 ──────────────────────────────

func (e *Engine) ListRepos() []Repo {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]Repo, 0, len(e.repos))
	for _, r := range e.repos {
		out = append(out, *r)
	}
	return out
}

func (e *Engine) SaveRepo(r *Repo) error {
	r.Name = strings.TrimSpace(r.Name)
	r.URL = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(r.URL), "/"))
	if !reCredName.MatchString(r.Name) {
		return errors.New("仓库名称无效")
	}
	if !reRepoURL.MatchString(r.URL) {
		return errors.New("仓库地址无效(需 https:// 或 ssh 形态)")
	}
	if r.DefaultBranch == "" {
		r.DefaultBranch = "master"
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if r.ID == "" {
		for _, o := range e.repos {
			if o.Name == r.Name {
				return fmt.Errorf("仓库 %q 已存在", r.Name)
			}
		}
		r.ID = "repo-" + randHex(3)
		r.UpdatedAt = time.Now()
		e.repos = append(e.repos, r)
	} else {
		found := false
		for _, o := range e.repos {
			if o.ID == r.ID {
				if o.Name != r.Name {
					for _, p := range e.repos {
						if p.ID != r.ID && p.Name == r.Name {
							return fmt.Errorf("仓库 %q 已存在", r.Name)
						}
					}
				}
				r.UpdatedAt = time.Now()
				*o = *r
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("仓库不存在: %s", r.ID)
		}
	}
	e.persistReposLocked()
	return nil
}

func (e *Engine) DeleteRepo(id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	idx := -1
	for i, r := range e.repos {
		if r.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("仓库不存在: %s", id)
	}
	e.repos = append(e.repos[:idx], e.repos[idx+1:]...)
	e.persistReposLocked()
	return nil
}

// TestRepo 在服务端本机执行 git ls-remote 校验地址与凭据(git 需在 PATH)
func (e *Engine) TestRepo(id string) (string, error) {
	r, ok := e.getRepo(id)
	if !ok {
		return "", fmt.Errorf("仓库不存在: %s", id)
	}
	cred := e.credFor(r.CredID)
	url := gitAuthURL(r.URL, cred)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "ls-remote", "--heads", url).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git ls-remote 失败: %v", err)
	}
	if strings.TrimSpace(string(out)) == "" {
		return string(out), fmt.Errorf("远端没有返回任何分支, 请检查地址与分支")
	}
	return string(out), nil
}

func (e *Engine) getRepo(id string) (Repo, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	for _, r := range e.repos {
		if r.ID == id {
			return *r, true
		}
	}
	return Repo{}, false
}

// RepoBranches 列出代码仓库的远端分支(git ls-remote --heads, 按名排序)
func (e *Engine) RepoBranches(id string) ([]string, error) {
	r, ok := e.getRepo(id)
	if !ok {
		return nil, fmt.Errorf("仓库不存在: %s", id)
	}
	url := gitAuthURL(r.URL, e.credFor(r.CredID))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "ls-remote", "--heads", url).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git ls-remote 失败: %v", err)
	}
	var branches []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if i := strings.Index(line, "refs/heads/"); i >= 0 {
			branches = append(branches, line[i+len("refs/heads/"):])
		}
	}
	return branches, nil
}

// credFor 凭据查找(空 ID 返回 nil)
func (e *Engine) credFor(id string) *Credential {
	if id == "" {
		return nil
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	for _, c := range e.creds {
		if c.ID == id {
			return c
		}
	}
	return nil
}

// gitAuthURL 为 https 仓库地址注入 user:token; ssh 形态原样返回(依赖主机 ssh key)。
func gitAuthURL(raw string, cred *Credential) string {
	if cred == nil || (cred.Username == "" && cred.Data == "") {
		return raw
	}
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		return raw
	}
	// https://host/path → https://user:pass@host/path
	idx := strings.Index(raw, "://")
	head := raw[:idx+3]
	rest := raw[idx+3:]
	user := cred.Username
	if user == "" {
		user = "token" // github/gitea 等仅 token 时的占位用户名
	}
	return head + shq(user+":"+cred.Data) + "@" + rest
}

// ── 镜像仓库 CRUD + 探活 ───────────────────────────────────

func (e *Engine) ListRegistries() []Registry {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]Registry, 0, len(e.registries))
	for _, r := range e.registries {
		out = append(out, *r)
	}
	return out
}

func (e *Engine) SaveRegistry(r *Registry) error {
	r.Name = strings.TrimSpace(r.Name)
	r.Server = strings.TrimSuffix(strings.TrimSpace(r.Server), "/")
	r.Server = strings.TrimPrefix(r.Server, "https://")
	r.Server = strings.TrimPrefix(r.Server, "http://")
	if !reCredName.MatchString(r.Name) {
		return errors.New("镜像仓库名称无效")
	}
	if r.Server == "" || strings.Contains(r.Server, "/") {
		return errors.New("镜像仓库地址无效(如 registry.example.com:5000)")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if r.ID == "" {
		for _, o := range e.registries {
			if o.Name == r.Name {
				return fmt.Errorf("镜像仓库 %q 已存在", r.Name)
			}
		}
		r.ID = "reg-" + randHex(3)
		r.UpdatedAt = time.Now()
		e.registries = append(e.registries, r)
	} else {
		found := false
		for _, o := range e.registries {
			if o.ID == r.ID {
				if o.Name != r.Name {
					for _, p := range e.registries {
						if p.ID != r.ID && p.Name == r.Name {
							return fmt.Errorf("镜像仓库 %q 已存在", r.Name)
						}
					}
				}
				r.UpdatedAt = time.Now()
				*o = *r
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("镜像仓库不存在: %s", r.ID)
		}
	}
	e.persistRegistriesLocked()
	return nil
}

func (e *Engine) DeleteRegistry(id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	idx := -1
	for i, r := range e.registries {
		if r.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("镜像仓库不存在: %s", id)
	}
	e.registries = append(e.registries[:idx], e.registries[idx+1:]...)
	e.persistRegistriesLocked()
	return nil
}

// TestRegistry 探活: GET https://<server>/v2/ —— 200/401 均视为存活(401=需认证, 正常)
func (e *Engine) TestRegistry(id string) (string, error) {
	r, ok := e.getRegistry(id)
	if !ok {
		return "", fmt.Errorf("镜像仓库不存在: %s", id)
	}
	url := "https://" + r.Server + "/v2/"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	if cred := e.credFor(r.CredID); cred != nil && cred.Username != "" {
		req.SetBasicAuth(cred.Username, cred.Data)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("无法连接 %s: %v", url, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK:
		return fmt.Sprintf("%s → 200 (匿名可访问)", url), nil
	case resp.StatusCode == http.StatusUnauthorized:
		return fmt.Sprintf("%s → 401 (服务存活, 需认证)", url), nil
	default:
		return fmt.Sprintf("%s → %d", url, resp.StatusCode), fmt.Errorf("服务响应异常: HTTP %d", resp.StatusCode)
	}
}

func (e *Engine) getRegistry(id string) (Registry, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	for _, r := range e.registries {
		if r.ID == id {
			return *r, true
		}
	}
	return Registry{}, false
}

// ── 脚本库 CRUD ────────────────────────────────────────────

func (e *Engine) ListScripts() []Script {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]Script, 0, len(e.scripts))
	for _, s := range e.scripts {
		out = append(out, *s)
	}
	return out
}

func (e *Engine) SaveScript(s *Script) error {
	s.Name = strings.TrimSpace(s.Name)
	if !reCredName.MatchString(s.Name) {
		return errors.New("脚本名称无效(1-64 字符)")
	}
	if strings.TrimSpace(s.Content) == "" {
		return errors.New("脚本内容不能为空")
	}
	if len(s.Content) > 64*1024 {
		return errors.New("脚本内容过长(≤64KB)")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if s.ID == "" {
		for _, o := range e.scripts {
			if o.Name == s.Name {
				return fmt.Errorf("脚本 %q 已存在", s.Name)
			}
		}
		s.ID = "script-" + randHex(3)
		s.UpdatedAt = time.Now()
		e.scripts = append(e.scripts, s)
	} else {
		found := false
		for _, o := range e.scripts {
			if o.ID == s.ID {
				if o.Name != s.Name {
					for _, p := range e.scripts {
						if p.ID != s.ID && p.Name == s.Name {
							return fmt.Errorf("脚本 %q 已存在", s.Name)
						}
					}
				}
				s.UpdatedAt = time.Now()
				*o = *s
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("脚本不存在: %s", s.ID)
		}
	}
	e.persistScriptsLocked()
	return nil
}

func (e *Engine) DeleteScript(id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	idx := -1
	for i, s := range e.scripts {
		if s.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("脚本不存在: %s", id)
	}
	e.scripts = append(e.scripts[:idx], e.scripts[idx+1:]...)
	e.persistScriptsLocked()
	return nil
}

// ── 持久化 ─────────────────────────────────────────────────

func (e *Engine) persistCredsLocked()      { persistJSON(e.credsFile, e.creds, "凭据") }
func (e *Engine) persistReposLocked()      { persistJSON(e.reposFile, e.repos, "仓库") }
func (e *Engine) persistRegistriesLocked() { persistJSON(e.registriesFile, e.registries, "镜像仓库") }
func (e *Engine) persistScriptsLocked()    { persistJSON(e.scriptsFile, e.scripts, "脚本") }

func persistJSON(f *store.JSONFile, v any, label string) {
	if err := f.Write(v); err != nil {
		log.Printf("[cicd] 持久化%s失败: %v", label, err)
	}
}

// shq 单引号包裹(shell 安全), 与 handlers.Shq 同语义(引擎侧独立实现避免反向依赖)
func shq(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
