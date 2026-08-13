package handlers

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"opscore/internal/ansible"

	"github.com/shirou/gopsutil/v4/process"
)

// ServiceInfo 统一描述一个"服务"条目。
// 在 Linux 上来自 systemd 单元;在其它平台降级为进程列表(demo 占位)。
type ServiceInfo struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Status      string  `json:"status"`     // active / inactive / running / sleep ...
	SubStatus   string  `json:"subStatus"`  // systemd 子状态
	Description string  `json:"description"`
	UnitFile    string  `json:"unitFile"`   // 单元文件位置
	LogHint     string  `json:"logHint"`    // 查看日志的命令
	IsProcess   bool    `json:"isProcess"`  // true = 进程降级列表
	PID         int32   `json:"pid,omitempty"`
	CPUPercent  float64 `json:"cpuPercent,omitempty"`
	MemPercent  float32 `json:"memPercent,omitempty"`
	// 常见服务识别(按单元名/进程名,属事实来源,安全标注)
	Recognized string   `json:"recognized,omitempty"`
	Category   string   `json:"category,omitempty"`
	Icon       string   `json:"icon,omitempty"`
	// 日志来源
	LogSource  string   `json:"logSource"`  // "journalctl" | "file" | "both" | ""
	LogPaths   []string `json:"logPaths"`   // 日志文件路径列表
	LogCommand string   `json:"logCommand"` // 可执行的日志查看命令
}

// ServicesList 返回服务列表与运行平台信息。
func ServicesList(w http.ResponseWriter, r *http.Request) {
	if hostID := r.URL.Query().Get("host"); hostID != "" {
		if agentHub != nil {
			if snap, ok := agentHub.GetSnapshot(hostID); ok && len(snap.Services) > 0 {
				// agent 只回传原始数据,识别/日志命令等展示字段在 server 端对齐(深拷贝,不改动缓存快照)
				svcs := make([]ServiceInfo, 0, len(snap.Services))
				for _, s := range snap.Services {
					si := ServiceInfo{
						ID: s.ID, Name: s.Name, Status: s.Status, SubStatus: s.SubStatus,
						Description: s.Description, UnitFile: s.UnitFile,
						PID: s.PID, CPUPercent: s.CPUPercent, MemPercent: s.MemPercent,
						LogHint: "journalctl -u " + s.Name, LogCommand: "journalctl -u " + s.Name, LogSource: "journalctl",
					}
					if meta, ok := recognizeProc(s.Name); ok {
						si.Recognized = meta.Label
						si.Category = meta.Category
						si.Icon = meta.Icon
					}
					svcs = append(svcs, si)
				}
				WriteJSON(w, map[string]any{"os": snap.Host.Platform, "managed": true, "services": svcs})
				return
			}
		}
		// Agent 无数据 → 异步推送新 Agent（替换旧版）
		TryUpdateAgent(hostID)
		// SSH 回退
		svcs, err := remoteServicesList(hostID)
		if err != nil {
			if agentHub != nil {
				agentHub.SetAlert(hostID, "SSH 无法连接: "+err.Error())
			}
			WriteJSON(w, map[string]any{"os": "linux", "managed": true, "services": []ServiceInfo{}, "error": err.Error()})
			return
		}
		WriteJSON(w, map[string]any{"os": "linux", "managed": true, "services": svcs})
		return
	}

	managed := hasSystemctl()
	if runtime.GOOS == "linux" && managed {
		WriteJSON(w, map[string]any{"os": "linux", "managed": true, "services": listSystemd()})
		return
	}
	WriteJSON(w, map[string]any{
		"os":       runtime.GOOS,
		"managed":  false,
		"services": listProcesses(),
		"note":     "Docker 环境：仅显示进程列表（PID/CPU/内存），无法管理系统服务",
	})
}
func remoteServicesList(hostID string) ([]ServiceInfo, error) {
	hosts := ansibleMgr.ListHosts()
	var h *ansible.Host
	for i := range hosts {
		if hosts[i].ID == hostID || hosts[i].Alias == hostID {
			h = &hosts[i]
			break
		}
	}
	if h == nil {
		return nil, fmt.Errorf("主机 %s 未找到", hostID)
	}

	rmHost := resolveRemoteHost(*h)

	cmds := map[string]string{"systemctl": `systemctl list-units --type=service --no-legend --no-pager 2>/dev/null`}
	res := remotePool.Exec(rmHost, cmds)
	if res["systemctl"].Error != "" {
		return nil, fmt.Errorf("%s", res["systemctl"].Error)
	}

	cpuMap, memMap := map[int32]float64{}, map[int32]float32{}
	psRes := remotePool.Exec(rmHost, map[string]string{"ps": `ps -eo pid,%cpu,%mem --no-headers 2>/dev/null`})
	if psRes["ps"].Error == "" {
		for _, line := range strings.Split(strings.TrimSpace(psRes["ps"].Output), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) < 3 {
				continue
			}
			pid, perr := strconv.ParseInt(fields[0], 10, 32)
			cpu, cerr := strconv.ParseFloat(fields[1], 64)
			mem, merr := strconv.ParseFloat(fields[2], 64)
			if perr == nil && cerr == nil {
				cpuMap[int32(pid)] = round2(cpu)
			}
			if perr == nil && merr == nil {
				memMap[int32(pid)] = float32(round2(mem))
			}
		}
	}

	var svcs []ServiceInfo
	units := []string{}
	for _, line := range strings.Split(strings.TrimSpace(res["systemctl"].Output), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		units = append(units, fields[0])
	}

	// 一次批量 show 取所有 unit 的 Id/FragmentPath/MainPID。
	// 兼容旧 systemd(如 CentOS 7 的 219): 批量输出按 unit 分组但无 unit 名头, 故用 -p Id 分组归属
	props := map[string]struct{ fp, pid string }{}
	if len(units) > 0 {
		showRes := remotePool.Exec(rmHost, map[string]string{"show": `systemctl show -p Id -p FragmentPath -p MainPID ` + strings.Join(units, " ") + ` 2>/dev/null`})
		if showRes["show"].Error == "" {
			for _, block := range strings.Split(showRes["show"].Output, "\n\n") {
				var id, fp, pid string
				for _, l2 := range strings.Split(block, "\n") {
					if v, ok := strings.CutPrefix(l2, "Id="); ok {
						id = strings.TrimSpace(v)
					} else if v, ok := strings.CutPrefix(l2, "FragmentPath="); ok {
						fp = strings.TrimSpace(v)
					} else if v, ok := strings.CutPrefix(l2, "MainPID="); ok {
						pid = strings.TrimSpace(v)
					}
				}
				if id != "" {
					props[id] = struct{ fp, pid string }{fp: fp, pid: pid}
				}
			}
		}
	}

	for _, line := range strings.Split(strings.TrimSpace(res["systemctl"].Output), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		unit := fields[0]
		si := ServiceInfo{ID: unit, Name: unit, Status: fields[2], SubStatus: fields[3], Description: strings.Join(fields[4:], " ")}

		if p, ok := props[unit]; ok {
			si.UnitFile = p.fp
			if n, perr := strconv.ParseInt(strings.TrimSpace(p.pid), 10, 32); perr == nil {
				si.PID = int32(n)
			}
		}

		if si.SubStatus == "running" && si.PID > 0 {
			if cpu, ok := cpuMap[si.PID]; ok {
				si.CPUPercent = cpu
			}
			if mem, ok := memMap[si.PID]; ok {
				si.MemPercent = mem
			}
		}
		si.LogHint = "journalctl -u " + unit
		if meta, ok := recognizeProc(unit); ok {
			si.Recognized = meta.Label
			si.Category = meta.Category
			si.Icon = meta.Icon
		}
		si.LogCommand = "journalctl -u " + unit
		si.LogSource = "journalctl"
		svcs = append(svcs, si)
	}
	return svcs, nil
}

func hasSystemctl() bool {
	_, err := exec.LookPath("systemctl")
	return err == nil
}

// fetchPsStats 通过一次 `ps -eo pid,%cpu,%mem` 批量取所有进程的 CPU/MEM 占比。
// 返回两个 map：pid -> cpuPct(保留2位小数), pid -> memPct(保留2位小数)。
func fetchPsStats() (map[int32]float64, map[int32]float32) {
	cpuMap := make(map[int32]float64)
	memMap := make(map[int32]float32)
	out, err := exec.Command("ps", "-eo", "pid,%cpu,%mem", "--no-headers").Output()
	if err != nil {
		return cpuMap, memMap
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pid, perr := strconv.ParseInt(fields[0], 10, 32)
		cpu, cerr := strconv.ParseFloat(fields[1], 64)
		mem, merr := strconv.ParseFloat(fields[2], 64)
		if perr == nil && cerr == nil {
			cpuMap[int32(pid)] = round2(cpu)
		}
		if perr == nil && merr == nil {
			memMap[int32(pid)] = float32(round2(mem))
		}
	}
	return cpuMap, memMap
}

// listSystemd 解析 `systemctl list-units --type=service`,把 Linux 命令变成结构化数据。
// 若 systemctl 不可用(如容器环境),降级为进程列表。
func listSystemd() []ServiceInfo {
	out, err := exec.Command("systemctl", "list-units", "--type=service", "--no-legend", "--no-pager").Output()
	if err != nil {
		return listProcesses()
	}
	cpuMap, memMap := fetchPsStats()
	var res []ServiceInfo
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		unit := fields[0]
		active := fields[2]
		sub := fields[3]
		desc := strings.Join(fields[4:], " ")
		si := ServiceInfo{ID: unit, Name: unit, Status: active, SubStatus: sub, Description: desc}
		// 一次 exec 取 FragmentPath + MainPID(兼容 systemd 219,它不支持 --value)
		if out, e := exec.Command("systemctl", "show", "-p", "FragmentPath", "-p", "MainPID", unit).Output(); e == nil {
			for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
				if v, ok := strings.CutPrefix(line, "FragmentPath="); ok {
					si.UnitFile = strings.TrimSpace(v)
				} else if v, ok := strings.CutPrefix(line, "MainPID="); ok {
					if n, perr := strconv.ParseInt(strings.TrimSpace(v), 10, 32); perr == nil {
						si.PID = int32(n)
					}
				}
			}
		}
		// 运行中且有主进程:从 ps 批量结果查 CPU/内存占用（保留 2 位小数）
		if si.SubStatus == "running" && si.PID > 0 {
			if cpu, ok := cpuMap[si.PID]; ok {
				si.CPUPercent = cpu
			}
			if mem, ok := memMap[si.PID]; ok {
				si.MemPercent = mem
			}
		}
		si.LogHint = "journalctl -u " + unit
		if meta, ok := recognizeProc(unit); ok {
			si.Recognized = meta.Label
			si.Category = meta.Category
			si.Icon = meta.Icon
		}
		si.LogCommand = "journalctl -u " + unit
		si.LogSource = "journalctl"
		if paths := detectLogPaths(unit); len(paths) > 0 {
			si.LogPaths = paths
			si.LogSource = "both"
		}
		res = append(res, si)
	}
	return res
}

// listProcesses 非 Linux 平台的降级:用 gopsutil 列进程,但 CPU/MEM 用 ps 批量查。
func listProcesses() []ServiceInfo {
	procs, err := process.Processes()
	if err != nil {
		return []ServiceInfo{}
	}
	cpuMap, memMap := fetchPsStats()
	var res []ServiceInfo
	for _, p := range procs {
		name, _ := p.Name()
		status, _ := p.Status()
		var si ServiceInfo
		if cpu, ok := cpuMap[p.Pid]; ok {
			si.CPUPercent = cpu
		}
		if mem, ok := memMap[p.Pid]; ok {
			si.MemPercent = mem
		}
		res = append(res, ServiceInfo{
			ID:          strconv.Itoa(int(p.Pid)),
			Name:        name,
			Status:      strings.ToLower(strings.Join(status, ",")),
			Description: "进程(demo 降级)",
			IsProcess:   true,
			PID:         p.Pid,
			CPUPercent:  si.CPUPercent,
			MemPercent:  si.MemPercent,
		})
		if meta, ok := recognizeProc(name); ok {
			res[len(res)-1].Recognized = meta.Label
			res[len(res)-1].Category = meta.Category
			res[len(res)-1].Icon = meta.Icon
		}
		if paths := detectLogPaths(name); len(paths) > 0 {
			res[len(res)-1].LogPaths = paths
			res[len(res)-1].LogSource = "file"
			if len(paths) > 0 {
				res[len(res)-1].LogCommand = "tail -n 100 " + paths[0]
			}
		}
	}
	// 按 CPU 占用降序,取前 50,避免列表过长
	top := res
	if len(top) > 50 {
		top = top[:50]
	}
	return top
}

// ServiceAction 对指定单元执行 start/stop/restart —— 把运维命令变成可视化按钮。
func ServiceAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		ID     string `json:"id"`
		Action string `json:"action"` // start | stop | restart
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ID == "" {
		WriteJSON(w, map[string]any{"ok": false, "error": "invalid body"})
		return
	}
	if runtime.GOOS != "linux" {
		WriteJSON(w, map[string]any{"ok": false, "error": "服务启停仅支持 Linux / systemd(当前 " + runtime.GOOS + ")"})
		return
	}
	switch body.Action {
	case "start", "stop", "restart":
	default:
		WriteJSON(w, map[string]any{"ok": false, "error": "action 必须是 start/stop/restart"})
		return
	}
	cmd := exec.Command("systemctl", body.Action, body.ID)
	out, err := cmd.CombinedOutput()
	if err != nil {
		WriteJSON(w, map[string]any{"ok": false, "error": strings.TrimSpace(string(out))})
		return
	}
	WriteJSON(w, map[string]any{"ok": true, "action": body.Action, "id": body.ID})
}

// round2 将浮点数保留 2 位小数（四舍五入）。
func round2(v float64) float64 {
	return math.Round(v*100) / 100
}
