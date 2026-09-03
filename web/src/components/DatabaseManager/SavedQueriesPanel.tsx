// 已保存查询面板: 列表 + 删除。

import { useEffect, useState } from 'react'
import { listSavedQueries, deleteSavedQuery, type SavedQuery, type ConnectionInfo } from './api'

export default function SavedQueriesPanel({
  conns,
  activeConn,
}: {
  conns: ConnectionInfo[]
  activeConn: ConnectionInfo | null
}) {
  const [queries, setQueries] = useState<SavedQuery[]>([])
  const [loading, setLoading] = useState(false)
  const [expanded, setExpanded] = useState<string | null>(null)

  const reload = async () => {
    setLoading(true)
    try {
      setQueries(await listSavedQueries())
    } catch {
      setQueries([])
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    reload()
  }, [])

  const remove = async (id: string) => {
    try {
      await deleteSavedQuery(id)
      setQueries(qs => qs.filter(q => q.id !== id))
    } catch {
      /* ignore */
    }
  }

  const connName = (id?: string) => {
    if (!id) return null
    const c = conns.find(x => x.id === id)
    return c ? c.name : id.slice(0, 8)
  }

  return (
    <div className="db-queries">
      <div className="db-queries-toolbar">
        <span className="dim">当前连接: {activeConn ? `${activeConn.name} (${activeConn.engine})` : '未选择'}</span>
        <span className="dim">共 {queries.length} 条</span>
        <button className="btn-glass-soft btn-glass-soft-sm" onClick={reload} disabled={loading}>
          {loading ? '刷新中...' : '刷新'}
        </button>
      </div>

      {queries.length === 0 ? (
        <div className="db-empty">{loading ? '加载中...' : '暂无已保存查询'}</div>
      ) : (
        <div className="db-queries-list">
          {queries.map(q => {
            const isOpen = expanded === q.id
            return (
              <div key={q.id} className="db-query-item">
                <div className="db-query-item-head" onClick={() => setExpanded(isOpen ? null : q.id)}>
                  <span className="db-query-name">{q.name}</span>
                  <span className="dim db-query-meta">
                    {connName(q.connId) && <>{connName(q.connId)} · </>}
                    {q.engine || '—'}
                  </span>
                  <button
                    className="btn-glass-soft btn-glass-soft-sm"
                    onClick={e => { e.stopPropagation(); remove(q.id) }}
                  >
                    删除
                  </button>
                </div>
                {isOpen && (
                  <pre className="code-block db-query-sql">{q.sql}</pre>
                )}
              </div>
            )
          })}
        </div>
      )}
    </div>
  )
}
