package logmonitor

import (
	"fmt"
	"log"
	"sync"
	"time"
)

// pollErrLimiter: 采集失败必须留痕, 但"同一个故障"不能每 10 秒刷一行。
// 起因(2026-09-24 实测): 本机实例配了 12 个 k8s pod 源(集群无 kubeconfig) + 2 个容器源(无 docker),
// 每轮 14 行、一小时约五千行, 把控制台淹了 —— 留痕的初衷是"故障可见", 不是"故障刷屏"。
//
// 规则: 首次立即打; 故障内容变了立即打; 同一条故障每 pollErrCooldownMs 打一次并带上被压掉的轮数;
// 采集恢复成功后清账, 下次再坏仍会立即打。
type pollErrLimiter struct {
	mu   sync.Mutex
	last map[string]*pollErrEntry
}

type pollErrEntry struct {
	msg        string
	lastLogMs  int64
	suppressed int
}

const pollErrCooldownMs = 10 * 60 * 1000

// decide 返回本次该打的日志正文, 空串表示这一轮压掉不打。nowMs 由调用方传入, 便于测试。
func (l *pollErrLimiter) decide(key, msg string, nowMs int64) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.last == nil {
		l.last = map[string]*pollErrEntry{}
	}
	ent := l.last[key]
	if ent == nil || ent.msg != msg {
		l.last[key] = &pollErrEntry{msg: msg, lastLogMs: nowMs}
		return fmt.Sprintf("poll %s 失败: %s", key, msg)
	}
	if nowMs-ent.lastLogMs >= pollErrCooldownMs {
		line := fmt.Sprintf("poll %s 失败(已持续 %d 轮): %s", key, ent.suppressed+1, msg)
		ent.lastLogMs = nowMs
		ent.suppressed = 0
		return line
	}
	ent.suppressed++
	return ""
}

// recover 该源这一轮成功了: 清账, 下次再坏重新立即报
func (l *pollErrLimiter) recover(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.last, key)
}

func (s *Service) pollFail(key, msg string) {
	if line := s.pollErrs.decide(key, msg, time.Now().UnixMilli()); line != "" {
		log.Printf("[logmonitor] %s", line)
	}
}
