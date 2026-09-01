// 限时写解锁 (dbx write_unlock 机制, ADR-003):
// 连接默认只读; 执行写操作前需显式解锁, 解锁仅在时间窗内有效, 到期自动回落只读。
// 写权限从"配置常量"变为"按需临时授予", 误操作窗口被压缩到分钟级。
// 状态仅存内存 —— 服务重启后回落只读, 是安全方向的默认。

package dbmanager

import (
	"sync"
	"time"
)

// WriteUnlockManager 管理每个连接的写解锁时间窗。
type WriteUnlockManager struct {
	mu      sync.Mutex
	until   map[string]time.Time
	maxMins int
}

// NewWriteUnlockManager 创建管理器; maxMinutes 为单次解锁时长上限(默认 30)。
func NewWriteUnlockManager(maxMinutes int) *WriteUnlockManager {
	if maxMinutes <= 0 {
		maxMinutes = 30
	}
	return &WriteUnlockManager{
		until:   map[string]time.Time{},
		maxMins: maxMinutes,
	}
}

// Unlock 给连接解锁 minutes 分钟, 返回到期时间。超过上限按上限截断。
func (w *WriteUnlockManager) Unlock(connID string, minutes int) time.Time {
	if minutes <= 0 {
		minutes = 5
	}
	if minutes > w.maxMins {
		minutes = w.maxMins
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	t := time.Now().Add(time.Duration(minutes) * time.Minute)
	w.until[connID] = t
	return t
}

// Lock 立即收回写权限。
func (w *WriteUnlockManager) Lock(connID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.until, connID)
}

// Remaining 返回剩余有效秒数; 0 表示未解锁或已过期(惰性清理)。
func (w *WriteUnlockManager) Remaining(connID string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	t, ok := w.until[connID]
	if !ok {
		return 0
	}
	rem := int(time.Until(t).Seconds())
	if rem <= 0 {
		delete(w.until, connID)
		return 0
	}
	return rem
}

// MaxMinutes 返回单次解锁时长上限。
func (w *WriteUnlockManager) MaxMinutes() int {
	return w.maxMins
}
