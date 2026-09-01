package db

import (
	"testing"
)

func TestShouldGrowMemoryLimit_NoActionWhenBelowThreshold(t *testing.T) {
	current := int64(2 * 1024 * 1024 * 1024) // 2GB
	// HeapAlloc 仅占 50%，远低于 80% 阈值
	heapAlloc := current * 50 / 100

	grown, next := shouldGrowMemoryLimit(current, heapAlloc)
	if grown {
		t.Fatalf("HeapAlloc=%dB 低于 80%% 阈值，不应抬升", heapAlloc)
	}
	if next != current {
		t.Fatalf("未抬升时 next 应等于 current，got=%d want=%d", next, current)
	}
}

func TestShouldGrowMemoryLimit_NoActionAtExactThreshold(t *testing.T) {
	current := int64(2 * 1024 * 1024 * 1024)
	// HeapAlloc 正好等于 80% 阈值：heapAlloc < current*80/100 为假时才抬升
	// current*80/100 = 1.6GB；heapAlloc = 1.6GB 时 heapAlloc < 1.6GB 为假 → 抬升
	heapAlloc := current * MemoryAutoscaleTriggerPercent / 100

	grown, next := shouldGrowMemoryLimit(current, heapAlloc)
	if !grown {
		t.Fatalf("HeapAlloc=%dB 已达 80%% 阈值，应抬升", heapAlloc)
	}
	wantNext := current + MemorySoftLimitStepBytes
	if next != wantNext {
		t.Fatalf("抬升步长错误：got=%d want=%d", next, wantNext)
	}
}

func TestShouldGrowMemoryLimit_StepByGB(t *testing.T) {
	current := int64(2 * 1024 * 1024 * 1024) // 2GB
	heapAlloc := int64(3 * 1024 * 1024 * 1024) // 3GB > 2GB * 80% = 1.6GB

	grown, next := shouldGrowMemoryLimit(current, heapAlloc)
	if !grown {
		t.Fatalf("HeapAlloc=%dB 超过 80%% 阈值，应抬升", heapAlloc)
	}
	wantNext := int64(3 * 1024 * 1024 * 1024) // 2GB + 1GB step = 3GB
	if next != wantNext {
		t.Fatalf("抬升后 limit 应为 3GB，got=%d want=%d", next, wantNext)
	}
}

func TestShouldGrowMemoryLimit_CapAtMax(t *testing.T) {
	// 当前 limit 已等于上限
	current := MemorySoftLimitMaxBytes
	heapAlloc := current * 2 // 即使 HeapAlloc 远超 limit 也不再抬升

	grown, next := shouldGrowMemoryLimit(current, heapAlloc)
	if grown {
		t.Fatalf("已达上限 %dB，不应再抬升", MemorySoftLimitMaxBytes)
	}
	if next != current {
		t.Fatalf("已达上限时 next 应等于 current，got=%d want=%d", next, current)
	}
}

func TestShouldGrowMemoryLimit_CapWhenStepExceedsMax(t *testing.T) {
	// 当前 limit 距上限不足 1GB 步长：7.5GB
	current := MemorySoftLimitMaxBytes - 512*1024*1024 // 7.5GB
	heapAlloc := current + 1 // 超过 80% 阈值

	grown, next := shouldGrowMemoryLimit(current, heapAlloc)
	if !grown {
		t.Fatalf("HeapAlloc 已逼近 limit，应触发抬升（即便步长会触及上限）")
	}
	if next != MemorySoftLimitMaxBytes {
		t.Fatalf("抬升后应 cap 在 max，got=%d want=%d", next, MemorySoftLimitMaxBytes)
	}
}

func TestShouldGrowMemoryLimit_NoActionWhenCurrentExceedsMax(t *testing.T) {
	// 异常情况：current > max（理论不会发生，但应防御性处理）
	current := MemorySoftLimitMaxBytes + 1
	heapAlloc := current * 2

	grown, next := shouldGrowMemoryLimit(current, heapAlloc)
	if grown {
		t.Fatalf("current 已超过 max，不应再抬升")
	}
	if next != current {
		t.Fatalf("next 应等于 current，got=%d want=%d", next, current)
	}
}

func TestInitMemorySoftLimit_ClampToMax(t *testing.T) {
	// 初始化值超过 max 时应被截断
	overMax := MemorySoftLimitMaxBytes * 2
	InitMemorySoftLimit(overMax)
	if got := CurrentMemorySoftLimit(); got != MemorySoftLimitMaxBytes {
		t.Fatalf("初始化超过 max 应被截断：got=%d want=%d", got, MemorySoftLimitMaxBytes)
	}
	// 恢复默认值，避免污染其他测试
	InitMemorySoftLimit(MemorySoftLimitInitialBytes)
}

func TestInitMemorySoftLimit_DefaultWhenZeroOrNegative(t *testing.T) {
	InitMemorySoftLimit(0)
	if got := CurrentMemorySoftLimit(); got != MemorySoftLimitInitialBytes {
		t.Fatalf("initial=0 应使用默认值：got=%d want=%d", got, MemorySoftLimitInitialBytes)
	}
	InitMemorySoftLimit(-1)
	if got := CurrentMemorySoftLimit(); got != MemorySoftLimitInitialBytes {
		t.Fatalf("initial<0 应使用默认值：got=%d want=%d", got, MemorySoftLimitInitialBytes)
	}
}

func TestMaybeGrowMemoryLimit_NoOpWhenUninitialized(t *testing.T) {
	// 模拟主进程未初始化的场景：
	// 通过将 currentMemorySoftLimit 直接置零（绕过 InitMemorySoftLimit）来测试
	// 注意：这是一个破坏性测试，需在测试末尾恢复状态
	saved := currentMemorySoftLimit.Load()
	defer currentMemorySoftLimit.Store(saved)

	currentMemorySoftLimit.Store(0)
	if MaybeGrowMemoryLimit() {
		t.Fatalf("currentMemorySoftLimit=0 时应直接返回 false，不主动初始化")
	}
	if got := CurrentMemorySoftLimit(); got != 0 {
		t.Fatalf("未初始化时不应被 MaybeGrowMemoryLimit 改写，got=%d want=0", got)
	}
}
