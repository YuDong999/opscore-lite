package main

import (
	"math"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"
	"opscore/internal/cmds"
	"opscore/internal/metrics"
)

type collector struct {
	prevNet     map[string]net.IOCountersStat
	lastCrontab time.Time
	// 重数据节流: services/network/processes 每 30s 重采一次, 其余 tick 复用缓存
	lastHeavy time.Time
	svcCache  []metrics.ServiceInfo
	netCache  metrics.NetworkDetail
	procCache []metrics.ProcessInfo
}

func newCollector() *collector {
	cpu.Percent(200*time.Millisecond, false)
	return &collector{}
}

func (c *collector) run(push func(metrics.Snapshot)) {
	for {
		snap := c.tick()
		push(snap)
		time.Sleep(2 * time.Second)
	}
}

func (c *collector) tick() metrics.Snapshot {
	s := metrics.Snapshot{Timestamp: time.Now().Unix(), AgentVersion: metrics.AgentVersion}

	if h, err := host.Info(); err == nil {
		s.Host = metrics.HostInfo{Hostname: h.Hostname, OS: h.OS, Platform: h.Platform, Uptime: h.Uptime}
	}
	if cpuPct, err := cpu.Percent(time.Second, false); err == nil && len(cpuPct) > 0 {
		s.CPU.Percent = round2(cpuPct[0])
	}
	if pc, err := cpu.Percent(0, true); err == nil {
		s.CPU.PerCore = pc
	}
	if n, err := cpu.Counts(true); err == nil {
		s.CPU.Cores = n
	}
	if info, err := cpu.Info(); err == nil && len(info) > 0 {
		s.CPU.Model = info[0].ModelName
	}
	if v, err := mem.VirtualMemory(); err == nil {
		s.Memory = metrics.MemoryInfo{Total: v.Total, Used: v.Used, UsedPercent: round2(v.UsedPercent), Free: v.Free}
	}
	if sw, err := mem.SwapMemory(); err == nil {
		s.Memory.SwapTotal = sw.Total
		s.Memory.SwapUsed = sw.Used
		s.Memory.SwapPercent = round2(sw.UsedPercent)
	}
	if la, err := load.Avg(); err == nil {
		s.Load = la
	}

	if parts, err := disk.Partitions(false); err == nil {
		for _, p := range parts {
			if shouldSkipDisk(p) {
				continue
			}
			if u, err := disk.Usage(p.Mountpoint); err == nil && u.Total > 0 && u.UsedPercent < 1000 {
				s.Disks = append(s.Disks, metrics.DiskInfo{
					Mountpoint:  p.Mountpoint,
					Total:       u.Total,
					Used:        u.Used,
					UsedPercent: round2(u.UsedPercent),
					Fstype:      p.Fstype,
				})
			}
		}
	}

	if counters, err := net.IOCounters(true); err == nil {
		prev := c.prevNet
		cur := map[string]net.IOCountersStat{}
		for _, cnt := range counters {
			cur[cnt.Name] = cnt
			nic := metrics.NicIO{Name: cnt.Name, RxTotal: cnt.BytesRecv, TxTotal: cnt.BytesSent}
			if p, ok := prev[cnt.Name]; ok {
				nic.RxRate = sub(cnt.BytesRecv, p.BytesRecv)
				nic.TxRate = sub(cnt.BytesSent, p.BytesSent)
			}
			s.Net.ByNic = append(s.Net.ByNic, nic)
		}
		c.prevNet = cur
	}

	c.collectNodeData(&s)
	c.collectCrontab(&s)

	return s
}

// collectCrontab 按注册表命令慢周期采集 crontab (Linux), 避免高频执行
func (c *collector) collectCrontab(s *metrics.Snapshot) {
	if runtime.GOOS != "linux" {
		return
	}
	if time.Since(c.lastCrontab) < 5*time.Minute {
		return
	}
	cmd := cmds.Find("tasks.crontab.list")
	if cmd == nil {
		return
	}
	args := cmds.Expand(cmd, nil)
	if len(args) == 0 {
		return
	}
	out, err := exec.Command(args[0], args[1:]...).Output()
	if err != nil {
		return
	}
	s.Crontab = &metrics.CrontabInfo{
		Content:     string(out),
		User:        os.Getenv("USER"),
		CollectedAt: time.Now().Unix(),
	}
	c.lastCrontab = time.Now()
}

func (c *collector) collectNodeData(s *metrics.Snapshot) {
	now := time.Now()
	if c.lastHeavy.IsZero() || now.Sub(c.lastHeavy) >= 30*time.Second {
		c.svcCache = collectServices()
		c.procCache = collectProcesses()
		c.netCache = collectNetwork()
		c.lastHeavy = now
	}
	s.Services = c.svcCache
	s.Processes = c.procCache
	s.Network = c.netCache
}

func collectServices() []metrics.ServiceInfo {
	out, err := exec.Command("systemctl", "list-units", "--type=service", "--no-legend", "--no-pager").Output()
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 0 {
		return nil
	}

	// Build a map of unit -> properties from a single systemctl show call
	units := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 1 {
			continue
		}
		units = append(units, fields[0])
	}

	pidMap := map[int32]float64{}
	memMap := map[int32]float32{}
	psOut, psErr := exec.Command("ps", "-eo", "pid,%cpu,%mem", "--no-headers").Output()
	if psErr == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(psOut)), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) < 3 {
				continue
			}
			pid, perr := strconv.ParseInt(fields[0], 10, 32)
			cpu, _ := strconv.ParseFloat(fields[1], 64)
			mem, merr := strconv.ParseFloat(fields[2], 64)
			if perr == nil {
				pidMap[int32(pid)] = round2(cpu)
			}
			if merr == nil {
				memMap[int32(pid)] = float32(round2(mem))
			}
		}
	}

	// 逐个 unit 并发取 FragmentPath + MainPID。
	// 注意: 旧 systemd(如 CentOS 7 的 219)批量 show 不输出 unit 名头, 无法区分归属, 故逐 unit 执行
	type unitProps struct {
		mainPID      string
		fragmentPath string
	}
	var (
		mu      sync.Mutex
		props   = map[string]unitProps{}
		wg      sync.WaitGroup
		sem     = make(chan struct{}, 8)
		showCmd = func(u string) (string, string) {
			out, err := exec.Command("systemctl", "show", "-p", "FragmentPath", "-p", "MainPID", u).Output()
			if err != nil {
				return "", ""
			}
			var fp, mp string
			for _, line := range strings.Split(string(out), "\n") {
				if v, ok := strings.CutPrefix(line, "FragmentPath="); ok {
					fp = strings.TrimSpace(v)
				} else if v, ok := strings.CutPrefix(line, "MainPID="); ok {
					mp = strings.TrimSpace(v)
				}
			}
			return fp, mp
		}
	)
	for _, unit := range units {
		wg.Add(1)
		sem <- struct{}{}
		go func(u string) {
			defer wg.Done()
			fp, mp := showCmd(u)
			mu.Lock()
			props[u] = unitProps{mainPID: mp, fragmentPath: fp}
			mu.Unlock()
			<-sem
		}(unit)
	}
	wg.Wait()

	var svcs []metrics.ServiceInfo
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		unit := fields[0]
		si := metrics.ServiceInfo{
			ID:          unit,
			Name:        unit,
			Status:      fields[2],
			SubStatus:   fields[3],
			Description: strings.Join(fields[4:], " "),
		}
		if p, ok := props[unit]; ok {
			if n, perr := strconv.ParseInt(strings.TrimSpace(p.mainPID), 10, 32); perr == nil && n > 0 {
				si.PID = int32(n)
			}
			si.UnitFile = p.fragmentPath
		}
		if si.SubStatus == "running" && si.PID > 0 {
			if cpu, ok := pidMap[si.PID]; ok {
				si.CPUPercent = cpu
			}
			if mem, ok := memMap[si.PID]; ok {
				si.MemPercent = mem
			}
		}
		svcs = append(svcs, si)
	}
	return svcs
}

func collectProcesses() []metrics.ProcessInfo {
	procs, err := process.Processes()
	if err != nil {
		return nil
	}
	pidMap := map[int32]float64{}
	memMap := map[int32]float32{}
	psOut, psErr := exec.Command("ps", "-eo", "pid,%cpu,%mem", "--no-headers").Output()
	if psErr == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(psOut)), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) < 3 {
				continue
			}
			pid, perr := strconv.ParseInt(fields[0], 10, 32)
			cpu, _ := strconv.ParseFloat(fields[1], 64)
			mem, merr := strconv.ParseFloat(fields[2], 64)
			if perr == nil {
				pidMap[int32(pid)] = round2(cpu)
			}
			if merr == nil {
				memMap[int32(pid)] = float32(round2(mem))
			}
		}
	}
	var res []metrics.ProcessInfo
	for _, p := range procs {
		name, _ := p.Name()
		statuses, _ := p.Status()
		pi := metrics.ProcessInfo{PID: p.Pid, Name: name, Status: strings.Join(statuses, ",")}
		if cpu, ok := pidMap[p.Pid]; ok {
			pi.CPU = cpu
		}
		if mem, ok := memMap[p.Pid]; ok {
			pi.Memory = mem
		}
		res = append(res, pi)
	}
	// Sort by CPU descending, keep top 50
	for i := 0; i < len(res); i++ {
		for j := i + 1; j < len(res); j++ {
			if res[j].CPU > res[i].CPU {
				res[i], res[j] = res[j], res[i]
			}
		}
	}
	if len(res) > 50 {
		res = res[:50]
	}
	return res
}

func collectNetwork() metrics.NetworkDetail {
	nd := metrics.NetworkDetail{}

	// 每接口收发字节计数
	ioMap := map[string]net.IOCountersStat{}
	if counters, err := net.IOCounters(true); err == nil {
		for _, c := range counters {
			ioMap[c.Name] = c
		}
	}

	// Interfaces
	ipOut, ipErr := exec.Command("ip", "-o", "addr", "show").Output()
	if ipErr == nil {
		ifaceMap := map[string]*metrics.NetInterface{}
		for _, line := range strings.Split(strings.TrimSpace(string(ipOut)), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			parts := strings.Fields(line)
			if len(parts) < 4 {
				continue
			}
			ifaceName := strings.TrimRight(parts[1], ":")
			if ifaceMap[ifaceName] == nil {
				ifaceMap[ifaceName] = &metrics.NetInterface{Name: ifaceName}
			}
			if parts[2] == "inet" || parts[2] == "inet6" {
				addr := strings.Split(parts[3], "/")[0]
				ifaceMap[ifaceName].Addrs = append(ifaceMap[ifaceName].Addrs, addr)
			}
		}
		names := make([]string, 0, len(ifaceMap))
		for name, v := range ifaceMap {
			if io, ok := ioMap[name]; ok {
				v.RxBytes = io.BytesRecv
				v.TxBytes = io.BytesSent
			}
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			nd.Interfaces = append(nd.Interfaces, *ifaceMap[name])
		}
	}

	// Listeners
	for _, proto := range []struct {
		cmd    string
		pFlag  string
	}{
		{"ss", "-tlnp"},
		{"ss", "-ulnp"},
	} {
		out, err := exec.Command(proto.cmd, proto.pFlag).Output()
		if err != nil {
			continue
		}
		pType := "TCP"
		if strings.Contains(proto.pFlag, "u") {
			pType = "UDP"
		}
		nd.Listeners = append(nd.Listeners, metrics.ParseSS(string(out), pType)...)
	}

	return nd
}

func sub(a, b uint64) uint64 {
	if a > b {
		return a - b
	}
	return 0
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}

func shouldSkipDisk(p disk.PartitionStat) bool {
	fstype := p.Fstype
	switch fstype {
	case "", "proc", "procfs", "procfs2", "subfs", "sysfs", "sysfs2",
		"tmpfs", "devtmpfs", "devpts", "ramfs", "overlay", "aufs",
		"squashfs", "cgroup", "cgroup2", "securityfs", "debugfs",
		"pstore", "bpf", "fusectl", "hugetlbfs", "mqueue", "configfs",
		"tracefs", "rpc_pipefs", "nfsd", "fuse.gvfsd-fuse", "binfmt_misc",
		"efivarfs":
		return true
	}
	mp := strings.ToLower(p.Mountpoint)
	virtualRoots := []string{
		"/proc", "/sys", "/dev", "/run", "/boot/efi",
		"/var/lib/docker/", "/var/lib/containers/",
	}
	for _, root := range virtualRoots {
		if mp == root || strings.HasPrefix(mp, root+"/") {
			return true
		}
	}
	return false
}
