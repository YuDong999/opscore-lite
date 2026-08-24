package handlers

import (
	"fmt"
	"net/http"
	"os/exec"
	"strings"

	"opscore/internal/remote"
)

// 目标主机执行器: 切换主机后的所有操作(读/写)必须落在所选主机上,
// 而不是 server 所在机器。本机 → 进程内 exec; 远程 → 连接池单会话 SSH。
//
// 契约: 前端在 query(?host=) 与 JSON body.host 中传目标主机 ID;
// 后端所有 handler 必须经 RunOnTarget / TargetBackend 等入口分发,
// 禁止对"可切换目标"的操作直接 exec.Command(CI 有 grep 护栏, 见 docs)。

// HostIDFromRequest 从 query(?host=) 提取目标主机; body 里的 host 由各 handler 解码后传入。
func HostIDFromRequest(r *http.Request) string {
	return strings.TrimSpace(r.URL.Query().Get("host"))
}

// IsLocalTarget 判定是否本机。空/local/内置本机ID 视为本机。
func IsLocalTarget(hostID string) bool {
	return hostID == "" || hostID == "local" || hostID == localHostID
}

// Shq 把参数安全转义为单个 shell 词(单引号包裹), 用于 argv→SSH 单行命令。
func Shq(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ArgsToLine 将参数数组拼为可安全经远端 shell 执行的单行命令。
func ArgsToLine(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		parts[i] = Shq(a)
	}
	return strings.Join(parts, " ")
}

// RunOnTarget 在指定主机上执行命令(argv 形态, 本机不经 shell, 远程经转义单行)。
// 返回合并输出(stdout+stderr); 远端非零退出码转为 error("exit status N")。
// 远程传输层故障时自动丢弃池内连接并以新拨号重试一次(命令需幂等/只读方可调用)。
func RunOnTarget(hostID string, argv []string) (string, error) {
	if len(argv) == 0 {
		return "", fmt.Errorf("空命令")
	}
	if IsLocalTarget(hostID) {
		out, err := exec.Command(argv[0], argv[1:]...).CombinedOutput()
		return string(out), err
	}
	rm, err := remoteHostByID(hostID)
	if err != nil {
		return "", err
	}
	if remotePool == nil {
		return "", fmt.Errorf("远程执行池未初始化")
	}
	line := ArgsToLine(argv)
	out, rc, err := remotePool.ExecLine(rm, line)
	if err != nil {
		// 传输层故障: 换新连接重试一次; 仍失败才向上抛
		p2, e2 := remoteHostByID(hostID)
		if e2 != nil {
			return out, err
		}
		out, rc, err = remotePool.ExecLine(p2, line)
		if err != nil {
			return out, err
		}
	}
	if rc != 0 {
		return out, fmt.Errorf("exit status %d", rc)
	}
	return out, nil
}

// ProbeTargetBackend 探测目标主机的防火墙后端(ufw/firewalld/none)。
func ProbeTargetBackend(hostID string) string {
	out, err := RunOnTarget(hostID, []string{"sh", "-c",
		`if command -v ufw >/dev/null 2>&1; then echo ufw; elif command -v firewall-cmd >/dev/null 2>&1; then echo firewalld; else echo none; fi`})
	b := strings.TrimSpace(out)
	if err != nil || b == "" {
		return "unknown"
	}
	return b
}

// remoteHostByID 按 ID/别名/主机名解析清单主机为 remote.Host。
func remoteHostByID(hostID string) (remote.Host, error) {
	h := resolveAnsibleHost(hostID)
	if h == nil {
		return remote.Host{}, fmt.Errorf("未找到目标主机: %s", hostID)
	}
	return resolveRemoteHost(*h), nil
}
