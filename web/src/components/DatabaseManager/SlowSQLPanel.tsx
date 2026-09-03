// 慢 SQL 面板: 调用后端 /api/dbmanager/slow 查看慢查询。

import { useEffect, useState, useCallback } from 'react'
import { getSlowSQL } from './api'

interface SlowData {
  engine: string
  columns: string[]
  rows: any[]
  note?: string
}

export default function SlowSQLPanel({ connId }: { connId: string }) {
  const [data, setData] = useState<SlowData | null>(null)
  const [loading, setLoading] = useState(false)

  const reload = useCallback(async () => {
    if (!connId) { setData(null); return }
    setLoading(true)
    try {
      setData(await getSlowSQL(connId, 20))
    } catch {
      setData(null)
    } finally {
      setLoading(false)
    }
  }, [connId])

  useEffect(() => {
    reload()
    const t = setInterval(reload, 30000)
    return () => clearInterval(t)
  }, [reload])

  if (!connId) return <div className="db-empty">选择一个连接后查看慢 SQL</div>
  if (loading && !data) return <div className="log-loading">加载慢 SQL 中...</div>
  if (!data || !data.columns?.length) return <div className="db-empty">暂无慢 SQL 记录</div>

  return (
    <div className="db-slow">
      <div className="db-slow-toolbar">
        <span className="dim">{data.engine} · {data.note || `${data.rows.length} 条慢查询`}</span>
        <button className="btn-glass-soft btn-glass-soft-sm" onClick={reload} disabled={loading}>
          {loading ? '刷新中...' : '刷新'}
        </button>
      </div>
      <div className="table-wrap">
        <table className="db-table">
          <thead>
            <tr>{data.columns.map(c => <th key={c}>{c}</th>)}</tr>
          </thead>
          <tbody>
            {data.rows.length === 0 ? (
              <tr><td colSpan={data.columns.length} className="dim" style={{ textAlign: 'center', padding: 12 }}>无记录</td></tr>
            ) : data.rows.map((r, i) => (
              <tr key={i}>
                {data.columns.map(c => <td key={c}><Cell v={r[c]} /></td>)}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  )
}

function Cell({ v }: { v: any }) {
  if (v === null || v === undefined) return <span className="dim">NULL</span>
  if (typeof v === 'object') return <code>{JSON.stringify(v)}</code>
  return <span>{String(v)}</span>
}
