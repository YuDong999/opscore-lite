// 审计日志 (ADR-003): 所有写操作(放行/拦截/失败)全量记录。
// 内存环形缓冲 + 最近 100 条持久化到 central meta KV, 服务重启不丢审计。

package dbmanager

import (
	"encoding/json"
	"sync"
	"time"

	"opscore/internal/central"
)

const (
	auditKey           = "dbmanager:audit"
	auditMemMax        = 200 // 内存环形缓冲上限
	auditPersistMax    = 100 // 持久化条数上限
	auditSQLExcerptMax = 500 // SQL 摘录长度上限
)

// AuditEntry 单条审计记录。
type AuditEntry struct {
	Time     int64  `json:"time"`
	ConnID   string `json:"connId"`
	ConnName string `json:"connName"`
	Engine   string `json:"engine"`
	SQL      string `json:"sql"`
	Risk     string `json:"risk"`
	Decision string `json:"decision"` // executed / denied / failed
	Detail   string `json:"detail,omitempty"`
}

// AuditLog 审计日志。
type AuditLog struct {
	mu    sync.Mutex
	buf   []AuditEntry
	store func() central.CentralStore
}

// NewAuditLog 创建审计日志; storeFn 复用延迟注入模式(可为 nil, 退化为纯内存)。
func NewAuditLog(storeFn func() central.CentralStore) *AuditLog {
	return &AuditLog{store: storeFn}
}

// Append 追加一条审计记录(SQL 截断到摘录上限)。
func (a *AuditLog) Append(e AuditEntry) {
	if e.Time == 0 {
		e.Time = time.Now().Unix()
	}
	if len(e.SQL) > auditSQLExcerptMax {
		e.SQL = e.SQL[:auditSQLExcerptMax] + "..."
	}
	a.mu.Lock()
	a.buf = append(a.buf, e)
	if len(a.buf) > auditMemMax {
		a.buf = a.buf[len(a.buf)-auditMemMax:]
	}
	a.mu.Unlock()
	a.persist()
}

// List 返回审计记录(可选按连接过滤), 新的在前。
func (a *AuditLog) List(connID string) []AuditEntry {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]AuditEntry, 0, len(a.buf))
	for i := len(a.buf) - 1; i >= 0; i-- {
		if connID == "" || a.buf[i].ConnID == connID {
			out = append(out, a.buf[i])
		}
	}
	return out
}

// persist 把最近 auditPersistMax 条写入 central meta(失败静默, 不影响主流程)。
func (a *AuditLog) persist() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.store == nil {
		return
	}
	st := a.store()
	if st == nil {
		return
	}
	n := len(a.buf)
	if n > auditPersistMax {
		n = auditPersistMax
	}
	b, err := json.Marshal(a.buf[len(a.buf)-n:])
	if err != nil {
		return
	}
	_ = central.SetMetaString(st, auditKey, string(b))
}

// loadFromDisk 启动时恢复持久化的审计记录(尽力而为)。
func (a *AuditLog) loadFromDisk() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.store == nil {
		return
	}
	st := a.store()
	if st == nil {
		return
	}
	raw, err := central.GetMetaString(st, auditKey)
	if err != nil || raw == "" {
		return
	}
	var disk []AuditEntry
	if json.Unmarshal([]byte(raw), &disk) != nil {
		return
	}
	a.buf = append(disk, a.buf...)
	if len(a.buf) > auditMemMax {
		a.buf = a.buf[len(a.buf)-auditMemMax:]
	}
}
