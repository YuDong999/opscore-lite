package handlers

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/disk"
)

// DirEntry 描述挂载点下的一个顶层条目(目录或文件)。
type DirEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	Size  uint64 `json:"size"`
	IsDir bool   `json:"isDir"`
}

// DiskChildrenResp 是某个磁盘挂载点下"可下钻"的返回结构。
type DiskChildrenResp struct {
	Root        string     `json:"root"`
	Total       uint64     `json:"total"`
	Used        uint64     `json:"used"`
	UsedPercent float64    `json:"usedPercent"`
	Children    []DirEntry `json:"children"`
	Partial     bool       `json:"partial"` // 是否因超时/权限未扫全
}

// virtualDirs 排除虚拟/特殊文件系统, 避免把 /proc、/sys 等当成真实磁盘占用。
var virtualDirs = map[string]bool{
	"proc": true, "sys": true, "dev": true, "run": true,
	"snap": true, "boot": true, "mnt": true, "media": true,
}

// DiskChildren 处理 GET /api/core/disk/children?path=<挂载点>&host=<主机ID>
// 返回该挂载点的总容量 + 顶层子目录/文件大小,供前端点击下钻。
// 目标主机按 target.go 的契约经 RunOnTarget 分发: 本机直接读盘, 远程一条 SSH 会话。
func DiskChildren(w http.ResponseWriter, r *http.Request) {
	hostID := HostIDFromRequest(r)
	root := strings.TrimSpace(r.URL.Query().Get("path"))
	if root == "" {
		WriteJSON(w, map[string]any{"error": "缺少路径参数"})
		return
	}
	// 禁止下钻虚拟文件系统：这些路径不是真实磁盘，扫出来会得到天文数字。
	if isVirtualMount(root) {
		WriteJSON(w, map[string]any{"error": "虚拟文件系统不可下钻", "root": root})
		return
	}
	if !IsLocalTarget(hostID) {
		remoteDiskChildren(w, hostID, root)
		return
	}
	// Windows 盘符形如 `C:` 会被 Go 解释为"该盘的当前工作目录",
	// 需补成 `C:\` 才能读到盘根(否则下钻内容其实是程序 CWD)。
	if len(root) == 2 && root[1] == ':' {
		root = root + `\`
	}

	dc := DiskChildrenResp{Root: root}
	if u, err := disk.Usage(root); err == nil {
		dc.Total = u.Total
		dc.Used = u.Used
		dc.UsedPercent = u.UsedPercent
	}

	// 用上下文超时管控遍历,避免大目录(如 Windows 的 C:\Windows)卡死请求。
	ctx, cancel := context.WithTimeout(r.Context(), 300*time.Second)
	defer cancel()

	entries, err := os.ReadDir(root)
	if err != nil {
		log.Printf("[disk] 本机读取 %s 失败: %v", root, err)
		WriteJSON(w, map[string]any{"error": "读不到 " + root + "（不存在或没有权限）", "root": root})
		return
	}

	// Docker 环境下 du 遍历结果与 statvfs 口径不一致，标记为部分扫描
	if _, err := os.Stat("/.dockerenv"); err == nil {
		dc.Partial = true
	}

	var collected []DirEntry
	for _, e := range entries {
		if e.IsDir() && virtualDirs[e.Name()] {
			continue
		}
		p := filepath.Join(root, e.Name())
		if e.IsDir() {
			size, err := duSize(ctx, p)
			if err != nil {
				dc.Partial = true
				continue
			}
			collected = append(collected, DirEntry{Name: e.Name(), Path: p, Size: size, IsDir: true})
		} else if fi, ferr := e.Info(); ferr == nil {
			collected = append(collected, DirEntry{Name: e.Name(), Path: p, Size: uint64(fi.Size()), IsDir: false})
		}
	}

	sort.Slice(collected, func(i, j int) bool { return collected[i].Size > collected[j].Size })
	if len(collected) > 60 {
		collected = collected[:60]
	}
	dc.Children = collected
	WriteJSON(w, dc)
}

// remoteDiskScript 在目标主机上一条会话里取: 挂载点容量(df) + 顶层占用(du) + 哪些是目录(find)。
// 路径经 argv 传成 $1, 不参与 shell 解析; 各段以哨兵开头, 由 remoteDiskChildren 解析。
// 注意 du 的 -s 与 --max-depth 互斥(GNU 会报 "summarizing conflicts"), 这里只留后者:
// 一次遍历就拿到全部顶层大小, 比逐个子目录各起一个 du 快一个量级。
// 远程下钻脚本。三点要紧的:
//  1. du 用 -x(不跨文件系统): 目录树里挂着别的盘(NFS/CIFS 卡住时尤其)会让扫描无限期挂住,
//     而且跨盘的数字对"这个盘被谁吃了"没有意义 —— df 也是按盘报的, 两边口径要一致。
//  2. du 外面包一层 timeout(有就用): 远端命令的硬上限是 60s(remote.exec), 不自我设限的话
//     超时是把整段脚本连 df 一起掐掉, 用户只看到"连不上"; 自我设限则能留下 df + 部分 du,
//     再用 __OPSCORE_DU_PARTIAL__ 告诉前端"这层没扫全"(前端的 partial 文案已有, 不用改)。
//  3. 脚本经 RunOnTarget 投递会被包进单引号 —— **不能出现单引号/反斜杠**, 见 target.go。
const remoteDiskScript = `echo __OPSCORE_DF__
df -Pk "$1" 2>/dev/null | tail -n +2
echo __OPSCORE_DU__
if command -v timeout >/dev/null 2>&1; then
  timeout -k 5 45 du -kx --max-depth=1 "$1" 2>/dev/null
  if [ $? -eq 124 ]; then echo __OPSCORE_DU_PARTIAL__; fi
else
  du -kx --max-depth=1 "$1" 2>/dev/null
fi
echo __OPSCORE_DIRS__
find "$1" -xdev -maxdepth 1 -mindepth 1 -type d 2>/dev/null
echo __OPSCORE_END__`

// remoteDiskChildren 在远程主机上列挂载点的顶层占用, 返回结构与本机版本一致。
func remoteDiskChildren(w http.ResponseWriter, hostID, root string) {
	target := displayTarget(hostID)
	out, err := RunOnTarget(hostID, []string{"sh", "-c", remoteDiskScript, "opscore-disk", root})
	if err != nil {
		log.Printf("[disk] 远程读取 %s@%s 失败: %v out=%q", root, target, err, truncateForLog(out))
		// 超时与"连不上"是两件事, 混成一句会让人去查凭据
		msg := "连不上 " + target + "（检查主机是否在线、凭据是否正确）"
		if strings.Contains(err.Error(), "超时") {
			msg = "读取 " + root + " 超时（目录太大或这台机器的磁盘很慢），换个目录试试"
		}
		WriteJSON(w, map[string]any{"error": msg, "root": root})
		return
	}

	dc, sizes, dirs := parseRemoteDiskOutput(out, root)
	if dc.Total > 0 {
		dc.UsedPercent = float64(dc.Used) / float64(dc.Total) * 100
	}
	if len(sizes) == 0 {
		log.Printf("[disk] 远程读取 %s@%s 无结果(目录不存在或无权限)", root, target)
		WriteJSON(w, map[string]any{"error": "读不到 " + target + " 上的 " + root + "（目录不存在或没有权限）", "root": root})
		return
	}

	collected := make([]DirEntry, 0, len(sizes))
	for p, size := range sizes {
		name := path.Base(p)
		if dirs[p] && virtualDirs[name] {
			continue
		}
		collected = append(collected, DirEntry{Name: name, Path: p, Size: size, IsDir: dirs[p]})
	}
	sort.Slice(collected, func(i, j int) bool { return collected[i].Size > collected[j].Size })
	if len(collected) > 60 {
		collected = collected[:60]
	}
	dc.Children = collected
	WriteJSON(w, dc)
}

// truncateForLog 截断远端输出, 避免把整屏报错灌进日志。
func truncateForLog(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}

// duSize 调用 du -s 获取单个目录的总大小（字节），比 WalkDir 快。
// 若 du 因权限/环境失败，回退到 filepath.WalkDir（兼容非 root 用户）。
func duSize(ctx context.Context, dir string) (uint64, error) {
	cmd := exec.CommandContext(ctx, "du", "-s", dir)
	out, err := cmd.Output()
	if err == nil {
		parts := strings.Fields(string(out))
		if len(parts) >= 2 {
			return parseDuSize(parts[0]), nil
		}
	}
	// du 失败（权限不足、命令不存在等），回退到 Go 遍历
	return dirSizeWalk(ctx, dir)
}

// dirSizeWalk 用 filepath.WalkDir 计算目录总大小（回退方案）。
func dirSizeWalk(ctx context.Context, root string) (uint64, error) {
	var total uint64
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // 跳过无权访问的子树
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			return nil
		}
		if fi, e := d.Info(); e == nil {
			total += uint64(fi.Size())
		}
		return nil
	})
	return total, nil
}

// parseDuSize 解析 du 输出的文件大小（支持 K/M/G/T 等单位，默认 1K 块）。
func parseDuSize(s string) uint64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	mult := uint64(1024) // du 默认单位是 1K 块
	if last := s[len(s)-1]; last < '0' || last > '9' {
		switch strings.ToLower(string(last)) {
		case "k":
			mult = 1024
		case "m":
			mult = 1024 * 1024
		case "g":
			mult = 1024 * 1024 * 1024
		case "t":
			mult = 1024 * 1024 * 1024 * 1024
		default:
			return 0
		}
		s = s[:len(s)-1]
	}
	var size uint64
	for _, c := range s {
		if c >= '0' && c <= '9' {
			size = size*10 + uint64(c-'0')
		}
	}
	return size * mult // du 默认 1K 块，mult 已含 1024，直接得字节
}

// isVirtualMount 判断给定路径是否属于 Linux 虚拟/伪文件系统，不应被下钻扫描。
func isVirtualMount(path string) bool {
	// 统一小写并处理 Windows 路径分隔符，便于前缀匹配。
	p := strings.ToLower(filepath.ToSlash(path))
	virtualRoots := []string{
		"/proc", "/sys", "/dev", "/run", "/boot/efi",
	}
	for _, root := range virtualRoots {
		if p == root || strings.HasPrefix(p, root+"/") {
			return true
		}
	}
	return false
}

// parseRemoteDiskOutput 把远程探测脚本的分段输出拆成 (响应骨架, 每个路径的字节数, 目录集合)。
// 单独抽出来是为了能测这几条容易写错的地方: 段名切换、du 连"目录自身"那一行也报出来、
// 以及 du 被自我设限掐掉时的 partial 标记 —— 前端据此提示"没扫全", 而不是让人把局部当全部。
func parseRemoteDiskOutput(out, root string) (DiskChildrenResp, map[string]uint64, map[string]bool) {
	dc := DiskChildrenResp{Root: root}
	sizes := map[string]uint64{}
	dirs := map[string]bool{}
	section := ""
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimRight(raw, "\r")
		switch line {
		case "__OPSCORE_DF__":
			section = "df"
			continue
		case "__OPSCORE_DU__":
			section = "du"
			continue
		case "__OPSCORE_DU_PARTIAL__":
			section = "" // 它是标记, 不是尺寸行
			dc.Partial = true
			continue
		case "__OPSCORE_DIRS__":
			section = "dirs"
			continue
		case "__OPSCORE_END__":
			section = ""
			continue
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		switch section {
		case "df":
			// Filesystem 1024-blocks Used Available Capacity Mounted-on
			f := strings.Fields(line)
			if len(f) >= 3 {
				dc.Total = parseUint64(f[1]) * 1024
				dc.Used = parseUint64(f[2]) * 1024
			}
		case "du":
			// "<KB>\t<path>"; 其中一行是挂载点自身, 不算子项
			i := strings.IndexByte(line, '\t')
			if i <= 0 {
				continue
			}
			p := line[i+1:]
			if strings.TrimRight(p, "/") == strings.TrimRight(root, "/") {
				continue
			}
			sizes[p] = parseUint64(line[:i]) * 1024
		case "dirs":
			dirs[line] = true
		}
	}
	return dc, sizes, dirs
}
