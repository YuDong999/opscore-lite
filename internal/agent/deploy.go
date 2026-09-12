package agent

import (
	"crypto/sha256"
	"encoding/hex"
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

	// 不做无差别 killall —— scpAndStart 的 clean 步骤只清自己的产物,
	// 避免误杀同主机上其他 opscore 服务端管理的 agent(曾引发两台服务端唤醒互杀战争)。
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
		id := agentArtifactID(AgentServerAddr)
		pool.Exec(rmHost, map[string]string{
			"stop": fmt.Sprintf(
				`systemctl stop opscore-agent-%s 2>/dev/null; systemctl disable opscore-agent-%s 2>/dev/null; pkill -9 -f "opscore-agent-%s" 2>/dev/null; rm -f /tmp/opscore-agent-%s /tmp/opscore-agent-%s.log /tmp/opscore-agent /tmp/opscore-agent.log`,
				id, id, id, id, id),
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

// agentArtifactID 由服务端 WS 地址派生的 6 位短 id: 决定二进制路径/systemd unit 名/进程名,
// 使多台 opscore 服务端可以共存管理同一批主机 —— 各服务端只 kill/重启自己的 agent,
// 不会误杀别的服务端(或其他版本)部署的同名 agent(旧实现 killall opscore-agent 会全杀, 引发两台服务端的唤醒互杀战争)。
func agentArtifactID(serverAddr string) string {
	sum := sha256.Sum256([]byte(serverAddr))
	return hex.EncodeToString(sum[:])[:6]
}

func scpAndStart(pool *remote.Pool, h remote.Host, binary []byte, serverAddr string) error {
	id := agentArtifactID(serverAddr)
	binPath := fmt.Sprintf("/tmp/opscore-agent-%s", id)
	unitName := fmt.Sprintf("opscore-agent-%s", id)

	// 只清理"自己的"产物 + 一次性清理历史遗留的旧全局单元(旧实现共享 /tmp/opscore-agent 与 opscore-agent.service)
	pool.Exec(h, map[string]string{
		"clean": fmt.Sprintf(
			`systemctl stop opscore-agent 2>/dev/null; systemctl disable opscore-agent 2>/dev/null; killall -9 opscore-agent 2>/dev/null; systemctl stop %s 2>/dev/null; pkill -9 -f "opscore-agent-%s" 2>/dev/null; true`, unitName, id),
	})

	// 写入二进制(按服务端隔离, 不再互相覆盖)
	res := pool.ExecWithInput(h, fmt.Sprintf(`cat > %s && chmod +x %s`, binPath, binPath), binary)
	if res.Error != "" {
		return fmt.Errorf("scp 失败: %s", res.Error)
	}

	// 启动 agent 的辅助函数
	fallbackCmd := fmt.Sprintf(
		`nohup %s --server %s --host-id %s > /tmp/opscore-agent-%s.log 2>&1 &`,
		binPath, serverAddr, h.ID, id)
	tryStartAgent := func() {
		pool.Exec(h, map[string]string{"start": fallbackCmd})
	}

	// systemd unit 按服务端隔离
	serviceContent := fmt.Sprintf(`[Unit]
Description=OpsCore Agent (%s) for %s

[Service]
ExecStart=%s --server %s --host-id %s
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
`, id, h.ID, binPath, serverAddr, h.ID)

	res2 := pool.ExecWithInput(h, fmt.Sprintf(`cat > /etc/systemd/system/%s.service`, unitName), []byte(serviceContent))
	if res2.Error != "" {
		log.Printf("[agent] %s: 写入 service 文件失败: %s, 回退到 nohup", h.ID, res2.Error)
		tryStartAgent()
		time.Sleep(2 * time.Second)
		return nil
	}

	res3 := pool.Exec(h, map[string]string{"start": fmt.Sprintf(
		`systemctl daemon-reload && systemctl enable %s && systemctl restart %s`, unitName, unitName)})
	if res3["start"].Error != "" {
		log.Printf("[agent] %s: systemctl 启动失败: %s, 回退到 nohup", h.ID, res3["start"].Error)
		tryStartAgent()
		time.Sleep(2 * time.Second)
		return nil
	}

	log.Printf("[agent] %s: 已通过 systemd 启动 agent (%s)", h.ID, unitName)
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
