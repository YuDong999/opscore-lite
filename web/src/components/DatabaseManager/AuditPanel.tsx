// 审计日志面板: 调用后端 /api/dbmanager/audit, 支持按连接/风险/决策筛选。

import { useEffect, useState, useCallback } from 'react'
import { type AuditEntry, getAudit, type ConnectionInfo, getEngineMeta } from './api'

const RISK_LABEL: Record<string, string> = {
  safe: '✓ 只读',
  medium: '⚡ 写操作',
  high: '⚠ 结构变更',
  critical: '✗ 高危',
}

const DECISION_LABEL: Record<string, { text: string; cls: string }> = {
  executed: { text: '已执行', cls: 'pill-ok' },
  denied: { text: '已拒绝', cls: 'pill-warn' },
  failed: { text: '已失败', cls: 'pill-err' },
}

function timeAgo(t: number): string {
  const s = Math.floor(Date.now() / 1000 - t)
  if (s < 60) return `${s}s 前`
  if (s < 3600) return `${Math.floor(s / 60)}m 前`
  if (s < 86400) return `${Math.floor(s / 3600)}h 前`
  return new Date(t * 1000).toLocaleString()
}

export default function AuditPanel({ conns }: { conns: ConnectionInfo[] }) {
  const [entries, setEntries] = useState<AuditEntry[]>([])
  const [loading, setLoading] = useState(false)
  const [filterConn, setFilterConn] = useState('')
  const [filterRisk, setFilterRisk] = useState('')
  const [filterDecision, setFilterDecision] = useState('')
  const [expanded, setExpanded] = useState<string | null>(null)

  const reload = useCallback(async () => {
    setLoading(true)
    try {
      const list = await getAudit(filterConn || undefined)
      setEntries(list)
    } catch (e) {
      setEntries([])
    } finally {
      setLoading(false)
    }
  }, [filterConn])

  useEffect(() => {
    reload()
    const t = setInterval(reload, 30000)
    return () => clearInterval(t)
  }, [reload])

  const filtered = entries.filter(e =>
    (!filterRisk || e.risk === filterRisk) &&
    (!filterDecision || e.decision === filterDecision),
  )

  return (
    <div className="db-audit">
      <div className="db-audit-toolbar">
        <div className="db-audit-filters">
          <select className="input db-audit-filter" value={filterConn} onChange={e => setFilterConn(e.target.value)}>
            <option value="">全部连接</option>
            {conns.map(c => <option key={c.id} value={c.id}>{c.name}</option>)}
          </select>
          <select className="input db-audit-filter" value={filterRisk} onChange={e => setFilterRisk(e.target.value)}>
            <option value="">全部风险</option>
            <option value="safe">✓ 只读</option>
            <option value="medium">⚡ 写操作</option>
            <option value="high">⚠ 结构变更</option>
            <option value="critical">✗ 高危</option>
          </select>
          <select className="input db-audit-filter" value={filterDecision} onChange={e => setFilterDecision(e.target.value)}>
            <option value="">全部决策</option>
            <option value="executed">已执行</option>
            <option value="denied">已拒绝</option>
            <option value="failed">已失败</option>
          </select>
        </div>
        <div className="db-audit-stats">
          <span className="dim">共 {filtered.length} 条</span>
          <button className="btn-glass-soft btn-glass-soft-sm" onClick={reload} disabled={loading}>
            {loading ? '刷新中...' : '刷新'}
          </button>
        </div>
      </div>

      {filtered.length === 0 ? (
        <div className="db-empty">
          {loading ? '加载中...' : '暂无审计记录, 执行 SQL 后会自动记录'}
        </div>
      ) : (
        <div className="table-wrap db-audit-table-wrap">
          <table className="db-table db-audit-table">
            <thead>
              <tr>
                <th style={{ width: 100 }}>时间</th>
                <th style={{ width: 140 }}>连接</th>
                <th style={{ width: 90 }}>风险</th>
                <th style={{ width: 80 }}>决策</th>
                <th>SQL</th>
                <th style={{ width: 100 }}>详情</th>
              </tr>
            </thead>
            <tbody>
              {filtered.map((e, i) => {
                const k = `${e.time}-${i}`
                const conn = conns.find(c => c.id === e.connId)
                const meta = conn ? getEngineMeta(conn.engine) : null
                const dec = DECISION_LABEL[e.decision] || { text: e.decision, cls: 'pill' }
                const isOpen = expanded === k
                return (
                  <>
                    <tr key={k} className={isOpen ? 'expanded' : ''} onClick={() => setExpanded(isOpen ? null : k)}>
                      <td className="dim">{timeAgo(e.time)}</td>
                      <td>
                        {conn && <span className={`db-engine-badge db-engine-${conn.engine}`} style={{ marginRight: 4 }}>{meta?.label.split(' ')[0] || conn.engine}</span>}
                        {e.connName || e.connId.slice(0, 8)}
                      </td>
                      <td>{RISK_LABEL[e.risk] || e.risk}</td>
                      <td><span className={`pill ${dec.cls}`}>{dec.text}</span></td>
                      <td className="db-audit-sql"><code>{e.sql}</code></td>
                      <td className="dim">{e.detail || (isOpen ? '收起' : '展开')}</td>
                    </tr>
                    {isOpen && (
                      <tr className="db-audit-detail">
                        <td colSpan={6}>
                          <div className="db-audit-detail-grid">
                            <div><span className="dim">连接 ID: </span><code>{e.connId}</code></div>
                            <div><span className="dim">引擎: </span>{e.engine}</div>
                            <div><span className="dim">完整时间: </span>{new Date(e.time * 1000).toLocaleString()}</div>
                            {e.detail && <div><span className="dim">说明: </span>{e.detail}</div>}
                            <div className="db-audit-sql-full">
                              <span className="dim">完整 SQL:</span>
                              <pre className="code-block">{e.sql}</pre>
                            </div>
                          </div>
                        </td>
                      </tr>
                    )}
                  </>
                )
              })}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}
