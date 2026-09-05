package cicd

// ── CI/CD 操作审计链(参照 ADR-002 风格: 内存环形日志, 触发/审批/取消/删除/回滚/维护全记录) ──
// 与运行历史互补: run 记录"构建发生了什么", 审计记录"谁在何时做了什么管理动作"。

import (
	"fmt"
	"sync"
	"time"
)

const TriggerRollback = "rollback"

// CicdAuditEntry 单条审计记录
type CicdAuditEntry struct {
	TS     string `json:"ts"`     // 时间
	Actor  string `json:"actor"`  // 操作者(单租户阶段固定 admin/webhook/cron)
	Action string `json:"action"` // 动作: trigger/approve/reject/cancel/delete_run/delete_pipeline/rollback/maintenance
	Target string `json:"target"` // 落点: 流水线名或运行 ID
	Detail string `json:"detail"` // 补充信息(分支/commit/参数名等; 不落任何密钥)
}

type cicdAuditStore struct {
	mu   sync.Mutex
	log  []CicdAuditEntry
	next int // 环形写指针
}

const cicdAuditCap = 200

var cicdAudits cicdAuditStore

// recordAudit 引擎侧统一入口(环形覆盖, 线程安全)
func recordAudit(action, target, detail string) {
	cicdAudits.mu.Lock()
	defer cicdAudits.mu.Unlock()
	e := CicdAuditEntry{
		TS:     time.Now().Format("2006-01-02 15:04:05"),
		Actor:  actorFor(action),
		Action: action,
		Target: target,
		Detail: detail,
	}
	if len(cicdAudits.log) < cicdAuditCap {
		cicdAudits.log = append(cicdAudits.log, e)
	} else {
		cicdAudits.log[cicdAudits.next] = e
	}
	cicdAudits.next = (cicdAudits.next + 1) % cicdAuditCap
}

// actorFor 按动作推断操作者(单租户: 人工动作=admin, 自动动作为来源)
func actorFor(action string) string {
	switch action {
	case "trigger(webhook)":
		return "webhook"
	case "trigger(cron)":
		return "cron"
	default:
		return "admin"
	}
}

// ListAudit 返回审计记录(新→旧)
func (e *Engine) ListAudit() []CicdAuditEntry {
	cicdAudits.mu.Lock()
	defer cicdAudits.mu.Unlock()
	out := make([]CicdAuditEntry, 0, len(cicdAudits.log))
	for i := len(cicdAudits.log) - 1; i >= 0; i-- {
		out = append(out, cicdAudits.log[i])
	}
	return out
}

// auditTarget 流水线的审计落点名
func auditTarget(p *Pipeline) string {
	if p == nil {
		return "?"
	}
	return fmt.Sprintf("%s(%s)", p.Name, p.ID)
}
