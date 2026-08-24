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
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"

	"opscore/internal/hostkey"
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
		HostKeyCallback: hostkey.Callback,
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

// ParseSections 解析 SnapshotScript 的哨兵分段输出。
// 段格式: "__OPSCORE_<KEY>__" 标记其后内容为该段; 某些命令(如 printf 无换行尾)
// 会让标记粘在上一段内容之后, 因此按"行内查找标记"解析, 标记前缀归属上一段。
func ParseSections(out string) map[string]string {
	const marker = "__OPSCORE_"
	sections := map[string]string{}
	var key, b strings.Builder
	flush := func() {
		if key.String() != "" {
			sections[key.String()] = strings.TrimSpace(b.String())
			key.Reset()
			b.Reset()
		}
	}
	for _, line := range strings.Split(out, "\n") {
		idx := strings.Index(line, marker)
		if idx < 0 {
			if key.Len() > 0 {
				b.WriteString(line)
				b.WriteByte('\n')
			}
			continue
		}
		// 标记前的残余内容属于上一段
		if idx > 0 && key.Len() > 0 {
			b.WriteString(line[:idx])
		}
		flush()
		rest := line[idx+len(marker):]
		end := strings.Index(rest, "__")
		if end < 0 {
			continue // 异常行, 忽略
		}
		key.WriteString(rest[:end])
		b.WriteString(rest[end+2:])
		b.WriteByte('\n')
	}
	flush()
	return sections
}

// ExecScript 在单条 SSH 会话中执行合并脚本(一次网络往返), 返回按哨兵分段的 Result。
// 与逐条 Exec 相比: N 次 session 往返 -> 1 次。传输层失败时返回错误;
// 单段命令自身失败只体现为该段输出为空, 不影响其它段。
//
// 池化长连接可能因网络抖动/对端静默断开而半死(表现为会话"成功"但零输出),
// 此时强制丢弃池内连接并用新拨号重试一次(快照脚本只读, 重试无副作用)。
func (p *Pool) ExecScript(h Host, script string) (map[string]Result, error) {
	out, err := p.execScriptOnce(h, script)
	if err != nil {
		p.dropConn(h)
		out, err = p.execScriptOnce(h, script)
	}
	return out, err
}

func (p *Pool) execScriptOnce(h Host, script string) (map[string]Result, error) {
	entry, err := p.acquireEntry(h)
	if err != nil {
		return nil, fmt.Errorf("SSH 连接失败: %w", err)
	}
	defer p.release(entry)
	session, err := entry.client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("创建 session 失败: %w", err)
	}
	defer session.Close()

	var buf bytes.Buffer
	session.Stdout = &buf
	session.Stderr = &buf
	if err := session.Run(script); err != nil && buf.Len() == 0 {
		return nil, fmt.Errorf("执行失败: %w", err)
	}

	parsed := ParseSections(buf.String())
	if len(parsed) == 0 {
		// 会话成功但没有可识别分段: 带上原始输出片段便于定位(banner/报错/截断等)
		head := buf.String()
		if len(head) > 200 {
			head = head[:200]
		}
		return nil, fmt.Errorf("输出无可识别分段, 原始输出: %q", head)
	}
	out := make(map[string]Result, len(parsed))
	for k, v := range parsed {
		out[k] = Result{Output: v}
		if v == "" {
			out[k] = Result{Error: "无输出"}
		}
	}
	return out, nil
}

// ExecLine 在单条 SSH 会话中执行单行命令, 返回合并输出与远端退出码。
// 与 ExecScript 的区别: 不使用哨兵分段, 直接返回整段输出;
// 传输层故障(空输出+错误)时自动丢弃池内连接重试一次。
// 远端命令自身失败(rc!=0)不是传输错误, 由调用方按 rc 判定。
func (p *Pool) ExecLine(h Host, line string) (string, int, error) {
	out, rc, err := p.execLineOnce(h, line)
	// rc==-1 表示对端没有回传 __EXIT__ 标记: 会话被静默掐断/半死连接,
	// 这类传输层异常必须丢弃池内连接并用新拨号重试一次。
	if rc == -1 {
		p.dropConn(h)
		o2, r2, e2 := p.execLineOnce(h, line)
		if r2 != -1 {
			return o2, r2, e2
		}
		if err == nil {
			if e2 != nil {
				err = e2
			} else {
				err = fmt.Errorf("SSH 会话异常: 重试后仍未收到退出码标记")
			}
		}
		return o2, r2, err
	}
	return out, rc, err
}

func (p *Pool) execLineOnce(h Host, line string) (string, int, error) {
	entry, err := p.acquireEntry(h)
	if err != nil {
		return "", -1, fmt.Errorf("SSH 连接失败: %w", err)
	}
	defer p.release(entry)
	session, err := entry.client.NewSession()
	if err != nil {
		return "", -1, fmt.Errorf("创建 session 失败: %w", err)
	}
	defer session.Close()

	var buf bytes.Buffer
	session.Stdout = &buf
	session.Stderr = &buf
	// 尾部追加退出码标记; 命令自身失败不影响标记输出
	script := line + "\n" + `printf "__EXIT__%d" "$?"`
	if err := session.Run(script); err != nil && buf.Len() == 0 {
		return "", -1, fmt.Errorf("执行失败: %w", err)
	}
	out := buf.String()
	idx := strings.LastIndex(out, "__EXIT__")
	if idx < 0 {
		// 对端未回传标记(老 shell/异常), 按未知退出码处理但保留输出
		return out, -1, nil
	}
	rc := 0
	fmt.Sscanf(strings.TrimSpace(out[idx+len("__EXIT__"):]), "%d", &rc)
	out = strings.TrimSuffix(out[:idx], "\n")
	return out, rc, nil
}

// ExecOnHost 在单台主机上执行自定义命令集。
func ExecOnHost(h Host, cmds map[string]string) map[string]Result {
	return runCommands(h, cmds)
}

// Pool 管理持久 SSH 连接池，自动复用和重连。
//
// 并发安全模型:
//   - 每条连接带在途计数(use), 使用者通过 acquireEntry/release 包裹会话;
//   - Remove/keepalive 只"标记失效"(dead=1)并移出池, 真正 Close 延迟到
//     最后一个在途使用者退出时 —— 避免一个请求的重连把其它并发请求
//     正在使用的连接关掉("use of closed network connection")。
type Pool struct {
	mu    sync.Mutex
	conns map[string]*poolEntry
}

type poolEntry struct {
	host   Host
	client *ssh.Client
	use    int32 // atomic: 在途使用者数
	dead   int32 // atomic: 已标记失效
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

// acquireEntry 取出连接并计入在途。已标记失效的连接视为不存在(触发重拨)。
func (p *Pool) acquireEntry(h Host) (*poolEntry, error) {
	for {
		p.mu.Lock()
		e := p.conns[h.ID]
		if e != nil {
			if atomic.LoadInt32(&e.dead) == 1 {
				delete(p.conns, h.ID)
				p.mu.Unlock()
				p.closeIfIdle(e)
				continue // 重查/重拨
			}
			atomic.AddInt32(&e.use, 1)
			p.mu.Unlock()
			return e, nil
		}
		p.mu.Unlock()

		client, err := dialHost(h)
		if err != nil {
			return nil, err
		}
		p.mu.Lock()
		// double-check: 并发 miss 时可能已有人先拨号成功, 保留先到的连接
		if e2 := p.conns[h.ID]; e2 != nil && atomic.LoadInt32(&e2.dead) == 0 {
			atomic.AddInt32(&e2.use, 1)
			p.mu.Unlock()
			client.Close()
			return e2, nil
		}
		ne := &poolEntry{host: h, client: client, use: 1}
		p.conns[h.ID] = ne
		p.mu.Unlock()
		return ne, nil
	}
}

func (p *Pool) release(e *poolEntry) {
	if atomic.AddInt32(&e.use, -1) <= 0 && atomic.LoadInt32(&e.dead) == 1 {
		e.client.Close()
	}
}

func (p *Pool) closeIfIdle(e *poolEntry) {
	if atomic.LoadInt32(&e.use) <= 0 {
		e.client.Close()
	}
}

// markDead 标记失效并移出池; 最后一个使用者 release 时才真正关闭。
func (p *Pool) markDead(e *poolEntry) {
	if !atomic.CompareAndSwapInt32(&e.dead, 0, 1) {
		return
	}
	p.mu.Lock()
	if cur := p.conns[e.host.ID]; cur == e {
		delete(p.conns, e.host.ID)
	}
	p.mu.Unlock()
	if atomic.LoadInt32(&e.use) <= 0 {
		e.client.Close()
	}
}

// dropConn 供调用方在传输层故障后强制弃用某主机当前连接(下次获取将重拨)。
func (p *Pool) dropConn(h Host) {
	p.mu.Lock()
	e := p.conns[h.ID]
	p.mu.Unlock()
	if e != nil {
		p.markDead(e)
	}
}

func (p *Pool) KeepaliveAll() {
	p.mu.Lock()
	entries := make([]*poolEntry, 0, len(p.conns))
	for _, e := range p.conns {
		entries = append(entries, e)
	}
	p.mu.Unlock()
	for _, e := range entries {
		if atomic.LoadInt32(&e.dead) == 1 {
			continue
		}
		if _, _, err := e.client.SendRequest("keepalive@openssh.com", true, nil); err != nil {
			p.markDead(e)
		}
	}
}

func (p *Pool) Exec(h Host, cmds map[string]string) map[string]Result {
	entry, err := p.acquireEntry(h)
	if err != nil {
		errMsg := fmt.Sprintf("SSH 连接失败: %v", err)
		out := make(map[string]Result, len(cmds))
		for k := range cmds {
			out[k] = Result{Error: errMsg}
		}
		return out
	}
	defer p.release(entry)
	client := entry.client

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
	entry, err := p.acquireEntry(h)
	if err != nil {
		return Result{Error: fmt.Sprintf("SSH 连接失败: %v", err)}
	}
	defer p.release(entry)

	session, err := entry.client.NewSession()
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

// Remove 标记失效指定主机连接; 在途会话结束后才关闭(并发安全)。
func (p *Pool) Remove(hostID string) {
	p.mu.Lock()
	e := p.conns[hostID]
	p.mu.Unlock()
	if e != nil {
		p.markDead(e)
	}
}

func (p *Pool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, entry := range p.conns {
		atomic.StoreInt32(&entry.dead, 1)
		entry.client.Close()
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
