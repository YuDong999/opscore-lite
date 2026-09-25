//go:build linux

package metrics

import (
	"os"
	"strconv"
	"strings"
)

// CollectLimits 采内核层的容量红线 —— 全部是 /proc 里现成的计数: 不装任何组件、不起额外进程。
// 这几项平时都是个位数百分比, 不进 CPU/内存/磁盘任何一块面板, 但撞顶的后果是
// "直接丢新连接 / 起不了新进程 / 写不进文件", 属于出事才被发现的资源。
// 导出是因为两条采集路径都要用: 服务端本地采集(internal/metrics.tick) 与 agent(cmd/agent)。
func CollectLimits() *LimitInfo {
	li := &LimitInfo{}

	// 线程/进程总数: /proc/loadavg 第 4 段形如 "1/531"(运行中/总数)
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		if f := strings.Fields(string(b)); len(f) >= 4 {
			if _, total, ok := strings.Cut(f[3], "/"); ok {
				li.Threads = parseInt64(total)
			}
		}
	}
	li.ThreadsMax = readProcInt("/proc/sys/kernel/pid_max")

	li.Conntrack = readProcInt("/proc/sys/net/netfilter/nf_conntrack_count")
	li.ConntrackMax = readProcInt("/proc/sys/net/netfilter/nf_conntrack_max")

	// /proc/sys/fs/file-nr: 三列 = 已分配 / 空闲 / 上限(较新内核第三列为 0, 上限在 file-max)
	if b, err := os.ReadFile("/proc/sys/fs/file-nr"); err == nil {
		f := strings.Fields(string(b))
		if len(f) >= 1 {
			li.FileHandles = parseInt64(f[0])
		}
		if len(f) >= 3 {
			li.FileHandlesMax = parseInt64(f[2])
		}
	}
	if li.FileHandlesMax <= 0 {
		li.FileHandlesMax = readProcInt("/proc/sys/fs/file-max")
	}

	li.ThreadsPct = pctOf(li.Threads, li.ThreadsMax)
	li.ConntrackPct = pctOf(li.Conntrack, li.ConntrackMax)
	li.FileHandlesPct = pctOf(li.FileHandles, li.FileHandlesMax)

	// 一个上限都读不到(没有 /proc 的容器等) → 整块不显示, 而不是显示一排 0 误导人
	if li.ThreadsMax <= 0 && li.ConntrackMax <= 0 && li.FileHandlesMax <= 0 {
		return nil
	}
	return li
}

func readProcInt(path string) int64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	return parseInt64(strings.TrimSpace(string(b)))
}

func parseInt64(s string) int64 {
	v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0
	}
	return v
}

// pctOf 使用率(百分比, 两位小数); 上限未知时返回 0 而不是 NaN/Inf
func pctOf(cur, max int64) float64 {
	if max <= 0 || cur <= 0 {
		return 0
	}
	return round2(float64(cur) / float64(max) * 100)
}
