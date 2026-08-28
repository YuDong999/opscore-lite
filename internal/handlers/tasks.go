package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"opscore/internal/cmds"
)

// CrontabEntry 表示 cron 条目
type CronEntry struct {
	ID       string `json:"id"`
	Schedule string `json:"schedule"`
	Command  string `json:"command"`
	Comment  string `json:"comment"`
	Enabled  bool   `json:"enabled"`
}

// DeviceInfo 描述单个块设备信息 (children 表示层级从属, 如 LVM 位于分区之下)
type DeviceInfo struct {
	Name       string       `json:"name"`
	Size       string       `json:"size"`
	Type       string       `json:"type"`
	Fstype     string       `json:"fstype"`
	Mountpoint string       `json:"mountpoint"`
	Children   []DeviceInfo `json:"children,omitempty"`
}

// DiskActionResult 磁盘操作返回结构
type DiskActionResult struct {
	Ok           bool   `json:"ok"`
	Error        string `json:"error"`
	Output       string `json:"output"`
	Permission   string `json:"permission"`
	NewPartition string `json:"newPartition,omitempty"`
}

// freeSpace 记录 parted 输出的空闲区间
type freeSpace struct {
	start string
	end   string
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
		if hostID := r.URL.Query().Get("host"); hostID != "" {
			remoteCrontabList(w, hostID)
			return
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
		if hostID := r.URL.Query().Get("host"); hostID != "" {
			remoteCrontabWrite(w, hostID, body.Content)
			return
		}
		if !isRoot() {
			WriteJSON(w, map[string]any{"error": "需要 root 权限修改 crontab", "permission": "user"})
			return
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

// remoteCrontabList 远端 crontab 读取: agent 缓存优先, 无则推送新 Agent, SSH 兜底 (命令来自注册表)
func remoteCrontabList(w http.ResponseWriter, hostID string) {
	h := resolveAnsibleHost(hostID)
	if h == nil {
		writeErr(w, "未找到指定主机", http.StatusNotFound)
		return
	}
	if agentHub != nil {
		if snap, ok := agentHub.GetSnapshot(hostID); ok && snap.Crontab != nil {
			WriteJSON(w, map[string]any{
				"content":     snap.Crontab.Content,
				"permission":  "root",
				"managed":     true,
				"source":      "agent",
				"collectedAt": snap.Crontab.CollectedAt,
			})
			return
		}
	}
	// Agent 无数据 → 异步推送新 Agent（替换旧版, 补齐 crontab 采集）
	TryUpdateAgent(hostID)
	rmHost := resolveRemoteHost(*h)
	cmd := cmds.Find("tasks.crontab.list")
	if cmd == nil {
		writeErr(w, "命令注册表缺少 tasks.crontab.list", http.StatusInternalServerError)
		return
	}
	res := remotePool.Exec(rmHost, map[string]string{"crontab": cmds.RemoteWith(cmd, nil)})
	if res["crontab"].Error != "" {
		writeErr(w, "SSH 命令执行失败: "+res["crontab"].Error, http.StatusBadGateway)
		return
	}
	WriteJSON(w, map[string]any{"content": res["crontab"].Output, "permission": "root", "managed": true, "source": "ssh"})
}

// remoteCrontabWrite 远端 crontab 写入 (SSH 实时执行, 命令来自注册表)
func remoteCrontabWrite(w http.ResponseWriter, hostID, content string) {
	h := resolveAnsibleHost(hostID)
	if h == nil {
		writeErr(w, "未找到指定主机", http.StatusNotFound)
		return
	}
	rmHost := resolveRemoteHost(*h)
	cmd := cmds.Find("tasks.crontab.write")
	if cmd == nil {
		writeErr(w, "命令注册表缺少 tasks.crontab.write", http.StatusInternalServerError)
		return
	}
	// crontab 文件必须以换行结尾 (读取侧 TrimSpace 会去掉末尾换行, 写回前补回)
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	res := remotePool.ExecWithInput(rmHost, cmds.RemoteWith(cmd, nil), []byte(content))
	resp := map[string]any{"permission": "root", "managed": true}
	if res.Error != "" {
		resp["error"] = res.Error
		resp["output"] = res.Output
	} else {
		resp["ok"] = true
		TryUpdateAgent(hostID) // 异步刷新 agent 缓存
	}
	WriteJSON(w, resp)
}

// DisksHandler 处理磁盘信息请求
func DisksHandler(w http.ResponseWriter, r *http.Request) {
	if hostID := r.URL.Query().Get("host"); hostID != "" {
		remoteDisksHandler(w, hostID)
		return
	}
	lsblk := runCapture("lsblk", "-o", "NAME,SIZE,TYPE,FSTYPE,MOUNTPOINT,MODEL")
	mounts := runCapture("mount")
	df := runCapture("df", "-h")
	devicesOut := runCapture("lsblk", "-J", "-o", "NAME,SIZE,TYPE,FSTYPE,MOUNTPOINT")
	devices := parseDevices(devicesOut)
	if isCmdError(devicesOut) {
		devices = nil
	}
	resp := map[string]any{
		"lsblk":      lsblk,
		"mounts":     mounts,
		"df":         df,
		"devices":    devices,
		"permission": permLabel(),
	}
	if isCmdError(lsblk) || isCmdError(mounts) || isCmdError(df) {
		resp["error"] = "系统命令执行失败，可能缺少 /sys 或 /proc 访问权限"
	}
	WriteJSON(w, resp)
}

func remoteDisksHandler(w http.ResponseWriter, hostID string) {
	h := resolveAnsibleHost(hostID)
	if h == nil {
		writeErr(w, "未找到指定主机", http.StatusNotFound)
		return
	}
	rmHost := resolveRemoteHost(*h)
	cmds := map[string]string{
		"lsblk":   `lsblk -o NAME,SIZE,TYPE,FSTYPE,MOUNTPOINT,MODEL 2>/dev/null`,
		"mount":   `mount 2>/dev/null`,
		"df":      `df -h 2>/dev/null`,
		"devices": `lsblk -J -o NAME,SIZE,TYPE,FSTYPE,MOUNTPOINT 2>/dev/null`,
	}
	res := remotePool.Exec(rmHost, cmds)
	if res["lsblk"].Error != "" {
		writeErr(w, "SSH 命令执行失败: "+res["lsblk"].Error, http.StatusBadGateway)
		return
	}
	devices := parseDevices(res["devices"].Output)
	WriteJSON(w, map[string]any{
		"lsblk":      res["lsblk"].Output,
		"mounts":     res["mount"].Output,
		"df":         res["df"].Output,
		"devices":    devices,
		"permission": "root",
	})
}

// DiskActionHandler 处理磁盘操作请求（挂载/卸载/分区/格式化/SMART）
func DiskActionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	var body struct {
		Action     string `json:"action"`
		Device     string `json:"device"`
		Partition  string `json:"partition"`
		Mountpoint string `json:"mountpoint"`
		Fstype     string `json:"fstype"`
		Options    string `json:"options"`
		Start      string `json:"start"`
		End        string `json:"end"`
		Host       string `json:"host"` // 目标主机ID(空=本机)
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteJSON(w, map[string]any{"error": "请求格式错误", "permission": "root"})
		return
	}

	// 切换主机后的操作必须落在目标主机上(前端 body.host 此前被忽略)
	if !IsLocalTarget(body.Host) {
		diskActionRemote(w, body.Host, &body)
		return
	}
	if !isRoot() {
		WriteJSON(w, map[string]any{"error": "需要 root 权限", "permission": "user"})
		return
	}

	var cmd *exec.Cmd
	switch body.Action {
	case "mount":
		if body.Device == "" || body.Mountpoint == "" {
			WriteJSON(w, map[string]any{"error": "缺少 device 或 mountpoint", "permission": "root"})
			return
		}
		existing := strings.TrimSpace(runCapture("findmnt", "-n", "-o", "TARGET", body.Device))
		if existing != "" {
			WriteJSON(w, map[string]any{"error": fmt.Sprintf("设备 %s 已挂载到 %s，请先卸载", body.Device, existing), "permission": "root"})
			return
		}
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
		out, err := doUmount(target)
		WriteJSON(w, map[string]any{"output": string(out), "permission": "root", "ok": err == nil, "error": errMsg(err)})
		return
	case "info":
		dev, verr := wholeDiskDev(body.Device)
		if verr != "" {
			WriteJSON(w, map[string]any{"error": verr, "permission": "root"})
			return
		}
		out := runCapture("parted", "-s", dev, "print", "free")
		WriteJSON(w, map[string]any{"output": out, "permission": "root"})
		return
	case "delete":
		if body.Device == "" || body.Partition == "" {
			WriteJSON(w, map[string]any{"error": "缺少 device 或 partition", "permission": "root"})
			return
		}
		dev, verr := wholeDiskDev(body.Device)
		if verr != "" {
			WriteJSON(w, map[string]any{"error": verr, "permission": "root"})
			return
		}
		out, err := exec.Command("parted", "-s", dev, "rm", body.Partition).CombinedOutput()
		if err == nil {
			exec.Command("partprobe", dev).Run()
		}
		WriteJSON(w, map[string]any{"output": string(out), "permission": "root", "ok": err == nil, "error": errMsg(err)})
		return
	case "partition":
		if body.Device == "" {
			WriteJSON(w, map[string]any{"error": "缺少 device", "permission": "root"})
			return
		}
		dev, verr := wholeDiskDev(body.Device)
		if verr != "" {
			WriteJSON(w, map[string]any{"error": verr, "permission": "root"})
			return
		}
		start, end := body.Start, body.End
		if start == "" || end == "" {
			freeInfo := parseFreeSpace(runCapture("parted", "-s", dev, "print", "free"))
			if freeInfo.start == "" || freeInfo.end == "" {
				WriteJSON(w, map[string]any{"error": "未找到可用空闲空间", "permission": "root"})
				return
			}
			start, end = freeInfo.start, freeInfo.end
		}
		beforeDevices := getDeviceNames(runCapture("lsblk", "-l", "-n", "-o", "NAME"))
		out, err := exec.Command("parted", "-s", dev, "mkpart", "primary", "xfs", start, end).CombinedOutput()
		if err == nil {
			exec.Command("partprobe", dev).Run()
		}
		newPartition := ""
		afterDevices := getDeviceNames(runCapture("lsblk", "-l", "-n", "-o", "NAME"))
		for _, d := range afterDevices {
			if !contains(beforeDevices, d) && strings.HasPrefix(d, filepath.Base(dev)) {
				newPartition = "/dev/" + d
				break
			}
		}
		resp := map[string]any{"output": string(out), "permission": "root", "ok": err == nil, "error": errMsg(err)}
		if newPartition != "" {
			resp["newPartition"] = newPartition
		}
		WriteJSON(w, resp)
		return
	case "format":
		if body.Device == "" {
			WriteJSON(w, map[string]any{"error": "缺少 device", "permission": "root"})
			return
		}
		dev, verr := wholeDiskDev(body.Device)
		if verr != "" {
			WriteJSON(w, map[string]any{"error": verr, "permission": "root"})
			return
		}
		ft := body.Fstype
		if ft == "" {
			ft = "xfs"
		}
		var fc *exec.Cmd
		switch ft {
		case "xfs":
			fc = exec.Command("mkfs.xfs", "-f", dev)
		case "ext4":
			fc = exec.Command("mkfs.ext4", "-F", dev)
		default:
			WriteJSON(w, map[string]any{"error": "不支持的格式: " + ft, "permission": "root"})
			return
		}
		out, err := fc.CombinedOutput()
		WriteJSON(w, map[string]any{"output": string(out), "permission": "root", "ok": err == nil, "error": errMsg(err)})
		return
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

// diskActionRemote 磁盘操作的远程分发: 与本机分支语义一致, 经 RunOnTarget 落到目标主机。
// 只读类(smart/info)与写操作(mount/umount/delete/partition/format)统一处理;
// partition 的空闲空间探测与新分区识别同样经 SSH 完成。
func diskActionRemote(w http.ResponseWriter, hostID string, body *struct {
	Action     string `json:"action"`
	Device     string `json:"device"`
	Partition  string `json:"partition"`
	Mountpoint string `json:"mountpoint"`
	Fstype     string `json:"fstype"`
	Options    string `json:"options"`
	Start      string `json:"start"`
	End        string `json:"end"`
	Host       string `json:"host"`
}) {
	target := displayTarget(hostID)
	run := func(argv []string) (string, error) { return RunOnTarget(hostID, argv) }
	resp := map[string]any{"permission": "root", "target": target}

	switch body.Action {
	case "mount":
		if body.Device == "" || body.Mountpoint == "" {
			WriteJSON(w, map[string]any{"error": "缺少 device 或 mountpoint", "target": target})
			return
		}
		argv := []string{"mount"}
		if body.Fstype != "" {
			argv = append([]string{"-t", body.Fstype}, argv...)
		}
		if body.Options != "" {
			argv = append([]string{"-o", body.Options}, argv...)
		}
		argv = append(argv, body.Device, body.Mountpoint)
		out, err := run(argv)
		fillDiskResp(resp, out, err)

	case "umount":
		t := body.Mountpoint
		if t == "" {
			t = body.Device
		}
		out, err := run([]string{"umount", t})
		if err != nil && (strings.Contains(err.Error(), "exit status 32") || strings.Contains(out, "busy")) {
			out, err = run([]string{"umount", "-l", t})
		}
		fillDiskResp(resp, out, err)

	case "delete":
		dev, verr := wholeDiskDev(body.Device)
		if verr != "" {
			WriteJSON(w, map[string]any{"error": verr, "target": target})
			return
		}
		out, err := run([]string{"parted", "-s", dev, "rm", body.Partition})
		if err == nil {
			run([]string{"partprobe", dev})
		}
		fillDiskResp(resp, out, err)

	case "partition":
		dev, verr := wholeDiskDev(body.Device)
		if verr != "" {
			WriteJSON(w, map[string]any{"error": verr, "target": target})
			return
		}
		start, end := body.Start, body.End
		if start == "" || end == "" {
			freeOut, _ := run([]string{"parted", "-s", dev, "print", "free"})
			fs := parseFreeSpace(freeOut)
			if fs.start == "" || fs.end == "" {
				WriteJSON(w, map[string]any{"error": "未找到可用空闲空间", "target": target})
				return
			}
			start, end = fs.start, fs.end
		}
		beforeOut, _ := run([]string{"lsblk", "-l", "-n", "-o", "NAME"})
		before := getDeviceNames(beforeOut)
		out, err := run([]string{"parted", "-s", dev, "mkpart", "primary", "xfs", start, end})
		if err == nil {
			run([]string{"partprobe", dev})
		}
		newPartition := ""
		afterOut, _ := run([]string{"lsblk", "-l", "-n", "-o", "NAME"})
		for _, d := range getDeviceNames(afterOut) {
			if !contains(before, d) && strings.HasPrefix(d, filepath.Base(dev)) {
				newPartition = "/dev/" + d
				break
			}
		}
		fillDiskResp(resp, out, err)
		if newPartition != "" {
			resp["newPartition"] = newPartition
		}

	case "format":
		dev, verr := wholeDiskDev(body.Device)
		if verr != "" {
			WriteJSON(w, map[string]any{"error": verr, "target": target})
			return
		}
		ft := body.Fstype
		if ft == "" {
			ft = "xfs"
		}
		var argv []string
		switch ft {
		case "xfs":
			argv = []string{"mkfs.xfs", "-f", dev}
		case "ext4":
			argv = []string{"mkfs.ext4", "-F", dev}
		default:
			WriteJSON(w, map[string]any{"error": "不支持的格式: " + ft, "target": target})
			return
		}
		out, err := run(argv)
		fillDiskResp(resp, out, err)

	case "smart":
		dev := body.Device
		if dev == "" {
			WriteJSON(w, map[string]any{"error": "缺少 device", "target": target})
			return
		}
		if !strings.HasPrefix(dev, "/dev/") {
			dev = "/dev/" + dev
		}
		out, _ := run([]string{"smartctl", "-a", dev})
		resp["output"] = out

	default:
		WriteJSON(w, map[string]any{"error": "未知操作: " + body.Action, "target": target})
		return
	}
	WriteJSON(w, resp)
}

func fillDiskResp(resp map[string]any, out string, err error) {
	if err != nil {
		resp["error"] = err.Error()
		resp["output"] = out
		return
	}
	resp["ok"] = true
	resp["output"] = out
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

// doUmount 卸载目标; 普通失败时自动降级为 lazy unmount(-l), 处理 device busy 场景。
func doUmount(target string) (string, error) {
	out, err := exec.Command("umount", target).CombinedOutput()
	if err == nil {
		return string(out), nil
	}
	msg := string(out)
	if strings.Contains(msg, "busy") || strings.Contains(err.Error(), "exit status 32") {
		lo, le := exec.Command("umount", "-l", target).CombinedOutput()
		if le == nil {
			return string(lo), nil
		}
		return string(lo), fmt.Errorf("普通卸载失败(设备忙), 延迟卸载也失败: %v", le)
	}
	return string(out), err
}

// wholeDiskDev 规范化设备路径并要求为整盘(非分区);返回规范化路径与错误信息(错误时路径为空)
func wholeDiskDev(dev string) (string, string) {
	if dev == "" {
		return "", "缺少 device"
	}
	if !strings.HasPrefix(dev, "/dev/") {
		dev = "/dev/" + dev
	}
	if isPartitionDev(dev) {
		return "", fmt.Sprintf("设备 %s 是分区，请选择整盘设备（如 /dev/sda）", dev)
	}
	return dev, ""
}

// isPartitionDev 通过 sysfs 判断设备是否为分区(而非整盘)
func isPartitionDev(dev string) bool {
	base := strings.TrimPrefix(dev, "/dev/")
	if base == "" || strings.Contains(base, "/") {
		return false
	}
	_, err := os.Stat("/sys/class/block/" + base + "/partition")
	return err == nil
}

// parseDevices 解析 lsblk -J 输出为带层级的设备树; 兼容旧式 -ln 平铺输出作为回退
func parseDevices(output string) []DeviceInfo {
	out := strings.TrimSpace(output)
	if out == "" {
		return nil
	}
	var tree struct {
		Blockdevices []DeviceInfo `json:"blockdevices"`
	}
	if err := json.Unmarshal([]byte(out), &tree); err == nil && len(tree.Blockdevices) > 0 {
		return tree.Blockdevices
	}
	return parseDevicesFlat(out)
}

// parseDevicesFlat 解析 lsblk -ln 平铺输出 (回退兼容)
func parseDevicesFlat(output string) []DeviceInfo {
	lines := strings.Split(output, "\n")
	var devices []DeviceInfo
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		d := DeviceInfo{Name: fields[0], Size: fields[1], Type: fields[2]}
		if len(fields) >= 4 && fields[3] != "" {
			d.Fstype = fields[3]
		}
		if len(fields) >= 5 && fields[4] != "" {
			d.Mountpoint = fields[4]
		}
		devices = append(devices, d)
	}
	return devices
}

// parseSize 解析大小字符串（支持 K/M/G/T/KB/MB/GB/TB）
func parseSize(s string) uint64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	s = strings.ToUpper(s)
	mult := uint64(1)
	if strings.HasSuffix(s, "GB") {
		mult = 1024 * 1024 * 1024
		s = strings.TrimSuffix(s, "GB")
	} else if strings.HasSuffix(s, "MB") {
		mult = 1024 * 1024
		s = strings.TrimSuffix(s, "MB")
	} else if strings.HasSuffix(s, "KB") {
		mult = 1024
		s = strings.TrimSuffix(s, "KB")
	} else if strings.HasSuffix(s, "TB") {
		mult = 1024 * 1024 * 1024 * 1024
		s = strings.TrimSuffix(s, "TB")
	} else if strings.HasSuffix(s, "G") {
		mult = 1024 * 1024 * 1024
		s = strings.TrimSuffix(s, "G")
	} else if strings.HasSuffix(s, "M") {
		mult = 1024 * 1024
		s = strings.TrimSuffix(s, "M")
	} else if strings.HasSuffix(s, "K") {
		mult = 1024
		s = strings.TrimSuffix(s, "K")
	} else if strings.HasSuffix(s, "T") {
		mult = 1024 * 1024 * 1024 * 1024
		s = strings.TrimSuffix(s, "T")
	}
	var size uint64
	for _, c := range s {
		if c >= '0' && c <= '9' {
			size = size*10 + uint64(c-'0')
		}
	}
	return size * mult
}

// parseFreeSpace 解析 parted -s dev print free 的空闲区间
func parseFreeSpace(output string) freeSpace {
	lines := strings.Split(output, "\n")
	var best freeSpace
	maxSize := uint64(0)
	for _, line := range lines {
		if !strings.Contains(line, "Free Space") {
			continue
		}
		fields := strings.Fields(line)
		for i, f := range fields {
			if f == "Free" && i+1 < len(fields) && fields[i+1] == "Space" {
				if i >= 3 {
					start := fields[i-3]
					end := fields[i-2]
					sizeStr := fields[i-1]
					size := parseSize(sizeStr)
					if size > maxSize {
						maxSize = size
						best = freeSpace{start: start, end: end}
					}
				}
				break
			}
		}
	}
	return best
}

// getDeviceNames 从 lsblk 输出提取设备名列表
func getDeviceNames(output string) []string {
	lines := strings.Split(output, "\n")
	var devices []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			devices = append(devices, line)
		}
	}
	return devices
}

// errMsg 将 error 转为字符串
func errMsg(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// isCmdError 判断命令输出是否为错误信息(以命令名+冒号开头或含典型错误词)
func isCmdError(output string) bool {
	output = strings.TrimSpace(output)
	if output == "" {
		return false
	}
	if strings.HasPrefix(output, "lsblk:") || strings.HasPrefix(output, "mount:") || strings.HasPrefix(output, "df:") {
		return true
	}
	if strings.Contains(output, "failed to access") || strings.Contains(output, "No such file or directory") {
		return true
	}
	return false
}

// contains 检查字符串切片是否包含某元素
func contains(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}