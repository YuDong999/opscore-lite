package remote

import (
	"bytes"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

type Host struct {
	ID       string
	Addr     string
	Port     int
	User     string
	Alias    string
	SSHKey   string
	Password string
}

type Result struct {
	Output string
	Error  string
}

type RunResult struct {
	HostID  string
	Host    Host
	Results map[string]Result
	Online  bool
}

func parseED25519Key(pemData []byte) (ssh.Signer, error) {
	block, _ := pem.Decode(pemData)
	if block == nil {
		return nil, fmt.Errorf("pem 解码失败")
	}
	var key interface{}
	var err error
	if block.Type == "OPENSSH PRIVATE KEY" {
		key, err = ssh.ParseRawPrivateKey(pemData)
	} else {
		key, err = x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			key, err = x509.ParsePKCS1PrivateKey(block.Bytes)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("解析私钥失败: %w", err)
	}
	switch k := key.(type) {
	case ed25519.PrivateKey:
		return ssh.NewSignerFromKey(k)
	case *ed25519.PrivateKey:
		return ssh.NewSignerFromKey(*k)
	default:
		return ssh.NewSignerFromKey(k)
	}
}

func signerFromFile(path string) (ssh.Signer, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取私钥文件失败: %w", err)
	}
	return parseED25519Key(data)
}

func dialHost(host Host) (*ssh.Client, error) {
	addr := net.JoinHostPort(host.Addr, fmt.Sprintf("%d", host.Port))

	var authMethods []ssh.AuthMethod

	if host.SSHKey != "" {
		signer, err := signerFromFile(host.SSHKey)
		if err == nil {
			authMethods = append(authMethods, ssh.PublicKeys(signer))
		}
	}
	if host.Password != "" {
		authMethods = append(authMethods, ssh.Password(host.Password))
	}
	if len(authMethods) == 0 {
		return nil, fmt.Errorf("未配置 SSH 私钥路径或密码")
	}

	config := &ssh.ClientConfig{
		User:            host.User,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}

	return ssh.Dial("tcp", addr, config)
}

func TestHost(host Host) error {
	client, err := dialHost(host)
	if err != nil {
		return err
	}
	client.Close()
	return nil
}

func runCommands(host Host, cmds map[string]string) map[string]Result {
	out := make(map[string]Result, len(cmds))

	client, err := dialHost(host)
	if err != nil {
		errMsg := fmt.Sprintf("SSH 连接失败: %v", err)
		for k := range cmds {
			out[k] = Result{Error: errMsg}
		}
		return out
	}
	defer client.Close()

	for key, cmd := range cmds {
		session, err := client.NewSession()
		if err != nil {
			out[key] = Result{Error: fmt.Sprintf("创建 session 失败: %v", err)}
			continue
		}
		output, err := session.CombinedOutput(cmd)
		session.Close()
		if err != nil {
			out[key] = Result{Error: fmt.Sprintf("命令执行失败: %v\n输出: %s", err, strings.TrimSpace(string(output)))}
			continue
		}
		out[key] = Result{Output: strings.TrimSpace(string(output))}
	}

	return out
}

// ExecOnHost 在单台主机上执行自定义命令集。
func ExecOnHost(h Host, cmds map[string]string) map[string]Result {
	return runCommands(h, cmds)
}

// Pool 管理持久 SSH 连接池，自动复用和重连。
type Pool struct {
	mu    sync.Mutex
	conns map[string]*poolEntry
}

type poolEntry struct {
	host   Host
	client *ssh.Client
}

func NewPool() *Pool {
	p := &Pool{conns: map[string]*poolEntry{}}
	go func() {
		for {
			time.Sleep(30 * time.Second)
			p.KeepaliveAll()
		}
	}()
	return p
}

func (p *Pool) getClient(h Host) (*ssh.Client, error) {
	p.mu.Lock()
	entry, ok := p.conns[h.ID]
	p.mu.Unlock()

	if ok && entry.client != nil {
		return entry.client, nil
	}

	client, err := dialHost(h)
	if err != nil {
		return nil, err
	}

	// double-check: 并发 miss 时可能已有人先拨号成功, 保留先到的连接, 关闭本线程刚拨的
	p.mu.Lock()
	if e, ok2 := p.conns[h.ID]; ok2 && e.client != nil {
		p.mu.Unlock()
		client.Close()
		return e.client, nil
	}
	p.conns[h.ID] = &poolEntry{host: h, client: client}
	p.mu.Unlock()

	return client, nil
}

func (p *Pool) KeepaliveAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, entry := range p.conns {
		_, _, err := entry.client.SendRequest("keepalive@openssh.com", true, nil)
		if err != nil {
			entry.client.Close()
			delete(p.conns, id)
		}
	}
}

func (p *Pool) Exec(h Host, cmds map[string]string) map[string]Result {
	client, err := p.getClient(h)
	if err != nil {
		errMsg := fmt.Sprintf("SSH 连接失败: %v", err)
		out := make(map[string]Result, len(cmds))
		for k := range cmds {
			out[k] = Result{Error: errMsg}
		}
		return out
	}

	out := make(map[string]Result, len(cmds))
	for key, cmd := range cmds {
		session, err := client.NewSession()
		if err != nil {
			out[key] = Result{Error: fmt.Sprintf("创建 session 失败: %v", err)}
			continue
		}
		output, err := session.CombinedOutput(cmd)
		session.Close()
		if err != nil {
			out[key] = Result{Error: fmt.Sprintf("命令执行失败: %v\n输出: %s", err, strings.TrimSpace(string(output)))}
			continue
		}
		out[key] = Result{Output: strings.TrimSpace(string(output))}
	}

	return out
}

// ExecWithInput 执行命令并通过 stdin 写入二进制数据（避免 base64 编码绕过命令行长度限制）
func (p *Pool) ExecWithInput(h Host, cmd string, input []byte) Result {
	client, err := p.getClient(h)
	if err != nil {
		return Result{Error: fmt.Sprintf("SSH 连接失败: %v", err)}
	}
	session, err := client.NewSession()
	if err != nil {
		return Result{Error: fmt.Sprintf("创建 session 失败: %v", err)}
	}
	defer session.Close()

	stdin, err := session.StdinPipe()
	if err != nil {
		return Result{Error: fmt.Sprintf("创建 stdin pipe 失败: %v", err)}
	}

	var buf bytes.Buffer
	session.Stdout = &buf
	session.Stderr = &buf

	if err := session.Start(cmd); err != nil {
		return Result{Error: fmt.Sprintf("启动命令失败: %v", err)}
	}

	stdin.Write(input)
	stdin.Close()

	if err := session.Wait(); err != nil {
		return Result{Error: fmt.Sprintf("命令执行失败: %v\n输出: %s", err, strings.TrimSpace(buf.String()))}
	}

	return Result{Output: strings.TrimSpace(buf.String())}
}

func (p *Pool) Remove(hostID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if entry, ok := p.conns[hostID]; ok {
		if entry.client != nil {
			entry.client.Close()
		}
		delete(p.conns, hostID)
	}
}

func (p *Pool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, entry := range p.conns {
		if entry.client != nil {
			entry.client.Close()
		}
	}
	p.conns = map[string]*poolEntry{}
}

func RunOnHosts(hosts []Host, cmds map[string]string) []RunResult {
	var mu sync.Mutex
	results := make([]RunResult, 0, len(hosts))
	var wg sync.WaitGroup

	for _, h := range hosts {
		wg.Add(1)
		go func(host Host) {
			defer wg.Done()
			out := runCommands(host, cmds)
			online := true
			for _, r := range out {
				if r.Error != "" {
					online = false
					break
				}
			}
			mu.Lock()
			results = append(results, RunResult{
				HostID:  host.ID,
				Host:    host,
				Results: out,
				Online:  online,
			})
			mu.Unlock()
		}(h)
	}

	wg.Wait()
	return results
}
