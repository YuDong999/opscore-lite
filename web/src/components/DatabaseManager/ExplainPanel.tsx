// 执行计划面板: 调用后端 /api/dbmanager/explain 解析 SQL 执行计划。

import { useEffect, useState } from 'react'
import { explainSQL } from './api'

interface ExplainData {
  engine: string
  sql: string
  format: string
  columns: string[]
  rows: any[]
}

export default function ExplainPanel({ connId, sql }: { connId: string; sql: string }) {
  const [data, setData] = useState<ExplainData | null>(null)
  const [loading, setLoading] = useState(false)

  useEffect(() => {
    if (!connId || !sql) { setData(null); return }
    setLoading(true)
    explainSQL(connId, sql, 'json')
      .then(setData)
      .catch(() => setData(null))
      .finally(() => setLoading(false))
  }, [connId, sql])

  if (!connId || !sql) return <div className="db-empty">执行过 SQL 后可查看执行计划</div>
  if (loading) return <div className="log-loading">解析执行计划中...</div>
  if (!data || !data.columns?.length) return <div className="db-empty">无法解析执行计划</div>

  return (
    <div className="db-explain">
      <div className="db-explain-head dim">
        {data.engine} · {data.format.toUpperCase()}
      </div>
      <pre className="code-block db-explain-sql">{data.sql}</pre>
      <div className="table-wrap">
        <table className="db-table">
          <thead>
            <tr>{data.columns.map(c => <th key={c}>{c}</th>)}</tr>
          </thead>
          <tbody>
            {data.rows.map((r, i) => (
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
