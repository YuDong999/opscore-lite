// 备份用的"在目标机上执行"封装。
//
// **为什么是接口注入而不是直接调 handlers**: dbmanager 是模块层, handlers 是 HTTP 层 ——
// 让模块去 import HTTP 层是层次倒挂(而且 handlers 将来若要用 dbmanager 就直接成环)。
// 所以这里只声明"我需要能在目标机上跑命令"这个能力, 由 main.go 把 handlers 的实现注进来
// (SetHostExecutor)。没注入时备份命令执行会明确报错, 而不是静默用本机顶替 —— 那会让
// "远程库的备份落在服务端机器上", 是个很隐蔽的错。
package dbmanager

import (
	"fmt"
	"strings"
)

// HostExecutor 是"在指定主机上执行命令"的最小能力(由 main.go 注入 handlers 的实现)。
type HostExecutor interface {
	// RunOnHost 在 hostID 指定的主机上执行 argv, 返回合并输出(stdout+stderr)。
	// hostID 为空/local 视为本机。
	RunOnHost(hostID string, argv []string) (string, error)
	// RunOnHostWithEnv 同 RunOnHost, 但带上环境变量(备份工具用它传密码, 不进 argv)。
	RunOnHostWithEnv(hostID string, env map[string]string, argv []string) (string, error)
	// IsLocal 判定 hostID 是否指向本机。
	IsLocal(hostID string) bool
	// ExecWithInput 把 stdin 喂给远端命令(用于 `cat > 文件` 这类写入)。
	// 本机实现应把命令当 argv 执行并写 stdin。
	ExecWithInput(hostID string, cmd string, input []byte) error
}

var hostExec HostExecutor

// SetHostExecutor 注入目标机执行器(在 main 里调一次)。
func SetHostExecutor(e HostExecutor) { hostExec = e }

// requireExecutor 取执行器, 没注入就直接报错 —— 不静默退化成"只支持本机"。
func requireExecutor() (HostExecutor, error) {
	if hostExec == nil {
		return nil, fmt.Errorf("备份需要目标机执行器, 但未注入(SetHostExecutor)")
	}
	return hostExec, nil
}

// Shq 把参数安全转义为单个 shell 词(单引号包裹)。远程执行是把 argv 拼成单行命令,
// 所以拼之前必须转义 —— 这是备份路径上唯一的注入面(库名/表名/路径都会进命令)。
func Shq(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// LookPathOnTarget 判断目标机上有没有某个命令。
// 用 `command -v`(POSIX 标准)而不是 which —— which 在精简镜像里常常没有。
func LookPathOnTarget(hostID, cmd string) (string, error) {
	ex, err := requireExecutor()
	if err != nil {
		return "", err
	}
	out, err := ex.RunOnHost(hostID, []string{"sh", "-c", "command -v " + Shq(cmd)})
	if err != nil {
		return "", err
	}
	p := strings.TrimSpace(out)
	if p == "" {
		return "", fmt.Errorf("%s 不存在", cmd)
	}
	return p, nil
}

// RunOnTargetQuiet 在目标机执行 argv 并返回合并输出。
// "Quiet"=不打印日志: 备份路径下探测失败是常态(没装工具), 不该刷日志。
func RunOnTargetQuiet(hostID string, argv []string) (string, error) {
	ex, err := requireExecutor()
	if err != nil {
		return "", err
	}
	return ex.RunOnHost(hostID, argv)
}

// RunOnTargetWithEnv 带环境变量在目标机执行(密码走这里, 不进命令行)。
func RunOnTargetWithEnv(hostID string, env map[string]string, argv []string) (string, error) {
	ex, err := requireExecutor()
	if err != nil {
		return "", err
	}
	return ex.RunOnHostWithEnv(hostID, env, argv)
}

// execWithInput 把数据喂给目标机上的命令(见 HostExecutor.ExecWithInput)。
func execWithInput(hostID, cmd string, data []byte) error {
	ex, err := requireExecutor()
	if err != nil {
		return err
	}
	return ex.ExecWithInput(hostID, cmd, data)
}

// buildBackupShellCommand 把"工具 argv + gzip 压缩 + 写入文件"拼成一条 shell 命令。
//
// 为什么用 shell 管道而不是 Go 侧读流再写: 原生工具的输出可能几十 GB, 走 Go 中转等于
// 在服务端多占一份内存/磁盘; 管道让 gzip 在**目标机上**完成, 我们只等结果。
//
// 转义规则: 每个 argv 元素经 Shq(单引号包裹), 参数里带空格/引号/分号都安全。
func buildBackupShellCommand(argv []string, filePath string) string {
	parts := make([]string, 0, len(argv))
	for _, a := range argv {
		parts = append(parts, Shq(a))
	}
	// set -o pipefail: 管道里**任何一段**失败都要算失败 —— 否则 mysqldump 挂了但 gzip 正常,
	// 我们会得到一个"看起来成功"的空备份。这是最容易骗过验证的一种失败。
	return "set -o pipefail; mkdir -p " + Shq(dirOf(filePath)) + " && " +
		strings.Join(parts, " ") + " | gzip -c > " + Shq(filePath)
}

// dirOf 取路径的目录部分(纯字符串操作, 目标机是 POSIX)。
func dirOf(p string) string {
	i := strings.LastIndexByte(p, '/')
	if i <= 0 {
		return "/"
	}
	return p[:i]
}

// fileSizeOnTarget 取目标机上文件的大小(字节)。取不到返回 -1 —— 不假装是 0。
func fileSizeOnTarget(hostID, filePath string) int64 {
	out, err := RunOnTargetQuiet(hostID, []string{"sh", "-c", "stat -c %s " + Shq(filePath)})
	if err != nil {
		return -1
	}
	var n int64
	if _, err := fmt.Sscanf(strings.TrimSpace(out), "%d", &n); err != nil {
		return -1
	}
	return n
}

// removeOnTarget 删目标机上的文件(保留策略清理旧备份用)。
func removeOnTarget(hostID, filePath string) error {
	_, err := RunOnTargetQuiet(hostID, []string{"sh", "-c", "rm -f " + Shq(filePath)})
	return err
}

// verifyBackupIsUsable 校验结果文件"不像个坏备份"。
//
// 只看退出码不够 —— `mysqldump` 某些失败(如权限不足只报 warning)会退出 0, 而 gzip
// 把错误信息压进去, 我们得到一个几 KB 的"备份"。所以两个判据:
//  1. 文件存在且**非空**(太小基本是错误输出)
//  2. 解压出来的头部能看出是 SQL(而不是一段报错文字)
func verifyBackupIsUsable(hostID, filePath string) error {
	size := fileSizeOnTarget(hostID, filePath)
	if size < 0 {
		return fmt.Errorf("备份文件不存在或取不到大小: %s", filePath)
	}
	if size < 64 {
		return fmt.Errorf("备份文件只有 %d 字节, 明显不是有效备份(工具可能报了错但退出码为 0)", size)
	}
	head, err := RunOnTargetQuiet(hostID, []string{"sh", "-c",
		"gzip -dc " + Shq(filePath) + " 2>/dev/null | head -c 400"})
	if err != nil {
		return fmt.Errorf("备份文件解不开(gzip 失败): %w", err)
	}
	low := strings.ToLower(head)
	if strings.Contains(low, "error") || strings.Contains(low, "denied") || strings.Contains(low, "fatal") {
		return fmt.Errorf("备份内容里出现错误信息, 不是有效 dump: %s", strings.TrimSpace(firstLine(head)))
	}
	return nil
}
