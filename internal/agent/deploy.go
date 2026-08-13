package agent

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"opscore/internal/ansible"
	"opscore/internal/remote"
)

// AgentServerAddr 是 Agent 用来连接 OpsCore Server 的 WebSocket 地址（由 server 端启动时设置）
// 如果未通过环境变量 OPCORE_AGENT_SERVER 手动指定，会在第一次 deploy 时通过 SSH 自动检测
var AgentServerAddr string

// AgentServerAddrExplicit 标记地址是否由用户显式配置（环境变量或 --agent-addr 标记）
var AgentServerAddrExplicit bool

// AgentServerPort 是 Agent WebSocket 服务端口（由 server 端启动时设置，用于 SSH_CLIENT 检测拼接）
var AgentServerPort = "8089"

// detectedServerAddr 缓存通过 SSH 检测到的服务端地址（最多一次 SSH 调用）
var detectedServerAddr string

// detectServerAddr 通过 SSH 登录远程主机，读取 $SSH_CLIENT 获取宿主机 IP
// 结果会缓存，多次部署只 SSH 一次
func detectServerAddr(pool *remote.Pool, h remote.Host) string {
	if detectedServerAddr != "" {
		return detectedServerAddr
	}
	res := pool.Exec(h, map[string]string{
		"detect": `echo $SSH_CLIENT | awk '{print $1}'`,
	})
	if res["detect"].Output != "" {
		ip := res["detect"].Output
		addr := fmt.Sprintf("ws://%s:%s/ws/agent", ip, AgentServerPort)
		log.Printf("[agent] 通过 SSH 检测到服务端地址: %s", addr)
		detectedServerAddr = addr
		return addr
	}
	log.Printf("[agent] SSH_CLIENT 检测失败 (%s), 使用默认地址", res["detect"].Error)
	return AgentServerAddr
}

func DeployAgent(pool *remote.Pool, host ansible.Host) {
	go doDeploy(pool, host)
}

func doDeploy(pool *remote.Pool, host ansible.Host) {
	rmHost := resolveHost(host)

	binary, err := pickAgentBinary()
	if err != nil {
		log.Printf("[agent] %s: 找不到合适的 agent 二进制: %v", host.ID, err)
		return
	}

	addr := AgentServerAddr
	if !AgentServerAddrExplicit {
		addr = detectServerAddr(pool, rmHost)
	}
	if addr == "" {
		log.Printf("[agent] %s: 服务端地址未配置, 跳过部署", host.ID)
		return
	}

	log.Printf("[agent] %s: 开始推送 agent (%d bytes, server=%s)...", host.ID, len(binary), addr)

	if err := scpAndStart(pool, rmHost, binary, addr); err != nil {
		log.Printf("[agent] %s: 部署失败: %v", host.ID, err)
		return
	}

	log.Printf("[agent] %s: 部署成功, agent 已启动", host.ID)
}

func TryWakeAgent(pool *remote.Pool, host ansible.Host) error {
	rmHost := resolveHost(host)

	binary, err := pickAgentBinary()
	if err != nil {
		return err
	}

	log.Printf("[agent] %s: 尝试唤醒 agent...", host.ID)

	addr := AgentServerAddr
	if !AgentServerAddrExplicit {
		addr = detectServerAddr(pool, rmHost)
	}

	res := pool.Exec(rmHost, map[string]string{
		"kill": "killall -9 opscore-agent 2>/dev/null; rm -f /tmp/opscore-agent",
	})
	if res["kill"].Error != "" && res["kill"].Error != "exit status 1" {
		log.Printf("[agent] %s: kill 旧 agent 警告: %v", host.ID, res["kill"].Error)
	}

	if err := scpAndStart(pool, rmHost, binary, addr); err != nil {
		return err
	}

	log.Printf("[agent] %s: 唤醒成功", host.ID)
	return nil
}

// DeployToAll 对 ansible 中所有已有主机部署 agent（用于 server 启动时）
func DeployToAll(pool *remote.Pool, hosts []ansible.Host) {
	for _, h := range hosts {
		DeployAgent(pool, h)
	}
}

// StartWakeLoop 开启定期扫描：对 agent 掉线的主机通过 SSH 重新部署
// hostsFn 返回当前所有主机列表（由 caller 在闭包中捕获 ansibleMgr.ListHosts）
func StartWakeLoop(hub *AgentHub, pool *remote.Pool, hostsFn func() []ansible.Host) {
	go func() {
		for {
			time.Sleep(60 * time.Second)
			hosts := hostsFn()
			for _, h := range hosts {
				if hub.IsOnline(h.ID) {
					continue
				}
				log.Printf("[agent] %s: agent 掉线, 尝试唤醒...", h.ID)
				if err := TryWakeAgent(pool, h); err != nil {
					log.Printf("[agent] %s: 唤醒失败: %v", h.ID, err)
				}
			}
		}
	}()
	log.Println("[agent] 健康检查循环已启动 (间隔 60s)")
}

func CleanAgent(pool *remote.Pool, hostID string, hosts []ansible.Host) {
	for _, h := range hosts {
		if h.ID != hostID {
			continue
		}
		rmHost := resolveHost(h)
		pool.Exec(rmHost, map[string]string{
			"stop": "killall -9 opscore-agent 2>/dev/null; rm -f /tmp/opscore-agent /tmp/opscore-agent.log",
		})
		return
	}
}

func pickAgentBinary() ([]byte, error) {
	name := "agent-linux-amd64"

	exe, _ := os.Executable()
	dir := filepath.Dir(exe)

	paths := []string{
		filepath.Join(dir, name),
		filepath.Join(dir, "bin", name),
		filepath.Join("bin", name),
	}

	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err == nil {
			return data, nil
		}
	}

	return nil, fmt.Errorf("未找到 agent 二进制: bin/%s (请先编译)", name)
}

func scpAndStart(pool *remote.Pool, h remote.Host, binary []byte, serverAddr string) error {
	// kill 旧进程
	pool.Exec(h, map[string]string{
		"clean": `killall -9 opscore-agent 2>/dev/null; systemctl stop opscore-agent 2>/dev/null; rm -f /tmp/opscore-agent /tmp/opscore-agent.log`,
	})

	// 写入二进制
	res := pool.ExecWithInput(h, `cat > /tmp/opscore-agent && chmod +x /tmp/opscore-agent`, binary)
	if res.Error != "" {
		return fmt.Errorf("scp 失败: %s", res.Error)
	}

	// 启动 agent 的辅助函数
	tryStartAgent := func() {
		fallbackCmd := fmt.Sprintf(
			`nohup /tmp/opscore-agent --server %s --host-id %s > /tmp/opscore-agent.log 2>&1 &`,
			serverAddr, h.ID,
		)
		pool.Exec(h, map[string]string{"start": fallbackCmd})
	}

	// 优先使用 systemd 启动 agent
	serviceContent := fmt.Sprintf(`[Unit]
Description=OpsCore Agent for %s

[Service]
ExecStart=/tmp/opscore-agent --server %s --host-id %s
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
`, h.ID, serverAddr, h.ID)

	res2 := pool.ExecWithInput(h, `cat > /etc/systemd/system/opscore-agent.service`, []byte(serviceContent))
	if res2.Error != "" {
		log.Printf("[agent] %s: 写入 service 文件失败: %s, 回退到 nohup", h.ID, res2.Error)
		tryStartAgent()
		time.Sleep(2 * time.Second)
		return nil
	}

	res3 := pool.Exec(h, map[string]string{"start": `systemctl daemon-reload && systemctl enable opscore-agent && systemctl restart opscore-agent`})
	if res3["start"].Error != "" {
		log.Printf("[agent] %s: systemctl 启动失败: %s, 回退到 nohup", h.ID, res3["start"].Error)
		tryStartAgent()
		time.Sleep(2 * time.Second)
		return nil
	}

	log.Printf("[agent] %s: 已通过 systemd 启动 agent", h.ID)
	time.Sleep(2 * time.Second)
	return nil
}

func resolveHost(h ansible.Host) remote.Host {
	port := h.Port
	if port == 0 {
		port = 22
	}
	user := h.User
	if user == "" {
		user = "root"
	}
	return remote.Host{
		ID:       h.ID,
		Addr:     h.Addr,
		Port:     port,
		User:     user,
		Alias:    h.Alias,
		SSHKey:   h.SSHKey,
		Password: h.Password,
	}
}
