package db

import (
	"runtime"
	"runtime/debug"
	"sync/atomic"

	"opscore/internal/dbmanager/gonavi/logger"
)

// 本文件实现 driver-agent 进程的 GOMEMLIMIT 自适应策略。
//
// 背景：driver-agent 是独立子进程，主进程无法控制其内存。
// 静态 limit（如固定 2GB）在大结果集场景下会触发 GC 硬模式，导出速度降 15-25%；
// 而 limit 设太大又失去约束意义。
//
// 策略：起步保守（2GB），运行时监控 HeapAlloc，逼近当前 limit 时按 1GB 步长抬升，
// 上限 8GB 防止无限制增长。配合 SetGCPercent(50) + 周期 GC，正常场景下稳态堆
// 仅几百 MB，limit 不会被触发；只有 GC 真的跟不上时才抬升。

const (
	// MemorySoftLimitInitialBytes 是进程启动时的默认 soft limit。
	// 2GB 覆盖绝大多数导出场景的稳态堆需求。
	MemorySoftLimitInitialBytes int64 = 2 * 1024 * 1024 * 1024

	// MemorySoftLimitMaxBytes 是自适应抬升的绝对上限。
	// 8GB 防止失控；用户机器内存 < 16GB 时也留有余量给主进程和系统。
	MemorySoftLimitMaxBytes int64 = 8 * 1024 * 1024 * 1024

	// MemorySoftLimitStepBytes 是每次抬升的步长。
	// 1GB 粒度足够平滑（不会一次跳太多），又不会频繁触发（HeapAlloc 1GB 量级才需要再抬）。
	MemorySoftLimitStepBytes int64 = 1 * 1024 * 1024 * 1024

	// MemoryAutoscaleTriggerPercent 控制 HeapAlloc 达到当前 limit 的多少百分比时触发抬升。
	// 80% 留出 20% 缓冲，避免 GC 噪声导致频繁抖动抬升。
	MemoryAutoscaleTriggerPercent = 80
)

// currentMemorySoftLimit 记录当前已应用的 soft limit。
// atomic 以便 MaybeGrowMemoryLimit 在并发流式查询中安全调用。
var currentMemorySoftLimit atomic.Int64

// InitMemorySoftLimit 在进程启动时调用，应用初始 soft limit。
// 重复调用安全：以最后一次为准。
func InitMemorySoftLimit(initial int64) {
	if initial <= 0 {
		initial = MemorySoftLimitInitialBytes
	}
	if initial > MemorySoftLimitMaxBytes {
		initial = MemorySoftLimitMaxBytes
	}
	debug.SetMemoryLimit(initial)
	currentMemorySoftLimit.Store(initial)
}

// CurrentMemorySoftLimit 返回当前已应用的 soft limit，主要供测试和监控使用。
func CurrentMemorySoftLimit() int64 {
	return currentMemorySoftLimit.Load()
}

// MaybeGrowMemoryLimit 在大结果集流式处理时周期性调用（建议与周期 GC 同节奏），
// 当堆用量达到当前 limit 的 MemoryAutoscaleTriggerPercent 时按步长抬升。
//
// 设计要点：
//   - 仅对调用过 InitMemorySoftLimit 的进程生效（driver-agent）；主进程未初始化时 currentMemorySoftLimit=0，
//     本函数直接返回，不影响主进程的 GC 行为
//   - 读 HeapAlloc 用 runtime.ReadMemStats（短暂 STW，每 5W 行一次可忽略）
//   - 抬升通过 debug.SetMemoryLimit 应用，原子记录新值
//   - 达到 MemorySoftLimitMaxBytes 后不再抬升，让 GC 硬模式接管
//   - 不做"降级"：进程 long-running，下次任务可能同样需要；soft limit 大不浪费内存
//
// 返回 true 表示触发了抬升（用于日志观测）。
func MaybeGrowMemoryLimit() bool {
	current := currentMemorySoftLimit.Load()
	if current <= 0 {
		// 进程未启用 soft limit（如主进程），跳过自适应
		return false
	}

	grown, next := shouldGrowMemoryLimit(current, readHeapAlloc())
	if !grown {
		return false
	}

	currentHeap := readHeapAlloc()
	debug.SetMemoryLimit(next)
	currentMemorySoftLimit.Store(next)
	logger.Infof("内存 soft limit 自适应抬升：%dMB → %dMB（HeapAlloc=%dMB）",
		current/1024/1024, next/1024/1024, currentHeap/1024/1024)
	return true
}

// shouldGrowMemoryLimit 是 MaybeGrowMemoryLimit 的纯逻辑核心，便于单元测试。
// 输入：当前 limit、当前 HeapAlloc；输出：是否抬升、抬升后的新 limit。
func shouldGrowMemoryLimit(currentLimit, heapAlloc int64) (bool, int64) {
	if currentLimit >= MemorySoftLimitMaxBytes {
		return false, currentLimit
	}
	if heapAlloc < currentLimit*MemoryAutoscaleTriggerPercent/100 {
		return false, currentLimit
	}
	next := currentLimit + MemorySoftLimitStepBytes
	if next > MemorySoftLimitMaxBytes {
		next = MemorySoftLimitMaxBytes
	}
	if next == currentLimit {
		return false, currentLimit
	}
	return true, next
}

// readHeapAlloc 封装 runtime.ReadMemStats，便于测试 mock。
func readHeapAlloc() int64 {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return int64(m.HeapAlloc)
}
