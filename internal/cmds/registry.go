package cmds

import (
	"strings"
	"time"
)

// Command 定义一条可执行命令。
// server 的本地执行 / SSH fallback 与 agent 采集共用同一份注册表:
// agent 打包二进制即携带完整命令组, 新命令只在此登记即可, 不会遗漏。
type Command struct {
	ID       string        // 唯一标识, 如 tasks.crontab.list
	Module   string        // 模块分组: tasks / services / disks / network / lvm ...
	Platform string        // 平台组: linux / win
	Args     []string      // 本地 exec 参数数组 (支持 {{key}} 插值)
	Remote   string        // SSH fallback 单行 shell 模板 (支持 {{key}} 插值)
	Interval time.Duration // agent 自动采集周期; 0 = 不自动采集 (按需/写入类)
	Stdin    bool          // true = 通过 stdin 传入内容 (写入类命令)
}

var registry = []Command{
	{
		ID: "tasks.crontab.list", Module: "tasks", Platform: "linux",
		Args:     []string{"crontab", "-l"},
		Remote:   "crontab -l 2>/dev/null",
		Interval: 5 * time.Minute,
	},
	{
		ID: "tasks.crontab.write", Module: "tasks", Platform: "linux",
		Args:   []string{"crontab", "-"},
		Remote: "crontab -",
		Stdin:  true,
	},
	{
		ID: "tasks.schtasks.list", Module: "tasks", Platform: "win",
		Args:     []string{"schtasks", "/query", "/fo", "CSV", "/nh"},
		Remote:   "schtasks /query /fo CSV /nh",
		Interval: 5 * time.Minute,
	},
}

// All 返回完整命令组 (agent 打包即全部携带)
func All() []Command {
	out := make([]Command, len(registry))
	copy(out, registry)
	return out
}

// Find 按 ID 查找命令
func Find(id string) *Command {
	for i := range registry {
		if registry[i].ID == id {
			return &registry[i]
		}
	}
	return nil
}

// ByModule 返回某模块下指定平台的命令; platform 为空返回该模块全部
func ByModule(module, platform string) []Command {
	var out []Command
	for i := range registry {
		c := &registry[i]
		if c.Module != module {
			continue
		}
		if platform != "" && c.Platform != platform {
			continue
		}
		out = append(out, *c)
	}
	return out
}

// Expand 将 Args 中的 {{key}} 替换为 vars 中的值
func Expand(c *Command, vars map[string]string) []string {
	if c == nil {
		return nil
	}
	out := make([]string, 0, len(c.Args))
	for _, a := range c.Args {
		out = append(out, expandVars(a, vars))
	}
	return out
}

// RemoteWith 将 Remote 模板中的 {{key}} 替换为 vars 中的值
func RemoteWith(c *Command, vars map[string]string) string {
	if c == nil {
		return ""
	}
	return expandVars(c.Remote, vars)
}

func expandVars(s string, vars map[string]string) string {
	for k, v := range vars {
		s = strings.ReplaceAll(s, "{{"+k+"}}", v)
	}
	return s
}
