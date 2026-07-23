package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// CrontabEntry 表示 cron 条目
type CronEntry struct {
	ID      string `json:"id"`
	Schedule string `json:"schedule"`
	Command  string `json:"command"`
	Comment  string `json:"comment"`
	Enabled  bool   `json:"enabled"`
}

// stableID 生成基于输入字符串的确定性ID
func stableID(s string) string {
	// 简单的哈希函数生成一致的ID
	hash := 0
	for i := 0; i < len(s); i++ {
		hash = 31*hash + int(s[i])
		hash &= 0x7fffffff
	}
	return strconv.Itoa(hash)
}

// ParseCrontabEntry 解析单行 crontab，支持注释
func ParseCrontabEntry(line string) (*CronEntry, error) {
	// 移除前后空格
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return nil, nil
	}

	// 标准 crontab 行格式: [分钟] [小时] [日] [月] [周] [命令] [# 注释]
	parts := strings.Fields(line)
	if len(parts) < 6 {
		return nil, fmt.Errorf("无效的 crontab 行: %s", line)
	}

	// 前5个是时间字段
	schedule := strings.Join(parts[0:5], " ")
	
	// 剩余部分可能包含命令和注释
	rest := strings.Join(parts[5:], " ")
	
	// 查找注释部分（# 开头的部分）
	commentIdx := strings.Index(rest, "#")
	var command, comment string
	if commentIdx != -1 {
		command = strings.TrimSpace(rest[:commentIdx])
		comment = strings.TrimSpace(rest[commentIdx+1:])
	} else {
		command = strings.TrimSpace(rest)
		comment = ""
	}

	return &CronEntry{
		ID:       stableID(schedule + command), // 基于调度和命令生成稳定ID
		Schedule: schedule,
		Command:  command,
		Comment:  comment,
	}, nil
}

// CrontabHandler 处理 crontab 相关的 API 请求
func CrontabHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		user := r.URL.Query().Get("user")
		if user == "" {
			user = "root"
		}
		if !isRoot() {
			u := os.Getenv("USER")
			if user != u {
				WriteJSON(w, map[string]any{"error": "非 root 只能查看自己的 crontab", "permission": "user"})
				return
			}
			user = u
		}
		cmd := exec.Command("crontab", "-l", "-u", user)
		out, _ := cmd.CombinedOutput()
		WriteJSON(w, map[string]any{"content": string(out), "permission": permLabel()})

	case "POST":
		if !isRoot() {
			WriteJSON(w, map[string]any{"error": "需要 root 权限修改 crontab", "permission": "user"})
			return
		}
		var body struct {
			User    string `json:"user"`
			Content string `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			WriteJSON(w, map[string]any{"error": "请求格式错误", "permission": "root"})
			return
		}
		if body.User == "" {
			body.User = "root"
		}
		cmd := exec.Command("crontab", "-u", body.User, "-")
		cmd.Stdin = strings.NewReader(body.Content)
		out, err := cmd.CombinedOutput()
		resp := map[string]any{"permission": "root"}
		if err != nil {
			resp["error"] = err.Error()
			resp["output"] = string(out)
		} else {
			resp["ok"] = true
		}
		WriteJSON(w, resp)

	default:
		http.Error(w, "method not allowed", 405)
	}
}

// DisksHandler 处理磁盘信息请求
func DisksHandler(w http.ResponseWriter, r *http.Request) {
	lsblk := runCapture("lsblk", "-o", "NAME,SIZE,TYPE,FSTYPE,MOUNTPOINT,MODEL")
	mounts := runCapture("mount")
	df := runCapture("df", "-h")
	WriteJSON(w, map[string]any{
		"lsblk":      lsblk,
		"mounts":     mounts,
		"df":         df,
		"permission": permLabel(),
	})
}

// DiskActionHandler 处理磁盘操作请求（挂载/卸载/SMART）
func DiskActionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	if !isRoot() {
		WriteJSON(w, map[string]any{"error": "需要 root 权限", "permission": "user"})
		return
	}
	var body struct {
		Action     string `json:"action"`
		Device     string `json:"device"`
		Mountpoint string `json:"mountpoint"`
		Fstype     string `json:"fstype"`
		Options    string `json:"options"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteJSON(w, map[string]any{"error": "请求格式错误", "permission": "root"})
		return
	}

	var cmd *exec.Cmd
	switch body.Action {
	case "mount":
		args := []string{body.Device, body.Mountpoint}
		if body.Fstype != "" {
			args = append([]string{"-t", body.Fstype}, args...)
		}
		if body.Options != "" {
			args = append([]string{"-o", body.Options}, args...)
		}
		cmd = exec.Command("mount", args...)
	case "umount":
		target := body.Mountpoint
		if target == "" {
			target = body.Device
		}
		cmd = exec.Command("umount", target)
	case "smart":
		dev := body.Device
		if dev == "" {
			WriteJSON(w, map[string]any{"error": "缺少 device", "permission": "user"})
			return
		}
		if !strings.HasPrefix(dev, "/dev/") {
			dev = "/dev/" + dev
		}
		if _, err := os.Stat(dev); os.IsNotExist(err) {
			WriteJSON(w, map[string]any{"error": "设备不存在 " + dev, "permission": "user"})
			return
		}
		out := runCapture("smartctl", "-a", dev)
		WriteJSON(w, map[string]any{"output": out, "permission": "root"})
		return
	default:
		WriteJSON(w, map[string]any{"error": "未知操作: " + body.Action, "permission": "user"})
		return
	}

	out, err := cmd.CombinedOutput()
	resp := map[string]any{"permission": "root"}
	if err != nil {
		resp["error"] = err.Error()
		resp["output"] = string(out)
	} else {
		resp["ok"] = true
		resp["output"] = string(out)
	}
	WriteJSON(w, resp)
}

// runCapture 执行命令并捕获输出
func runCapture(name string, args ...string) string {
	path, err := exec.LookPath(name)
	if err != nil {
		return "(" + name + " not found)"
	}
	cmd := exec.Command(path, args...)
	out, _ := cmd.CombinedOutput()
	return string(out)
}