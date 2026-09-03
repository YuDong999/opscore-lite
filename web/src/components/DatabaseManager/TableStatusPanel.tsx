// 表状态面板: 调用后端 /api/dbmanager/tables/{id}/status 查看表状态。

import { useEffect, useState } from 'react'
import { getTableStatus } from './api'

interface StatusData {
  engine: string
  columns: string[]
  rows: any[]
  note?: string
}

export default function TableStatusPanel({ connId, database, table }: { connId: string; database: string; table: string }) {
  const [data, setData] = useState<StatusData | null>(null)
  const [loading, setLoading] = useState(false)

  useEffect(() => {
    if (!connId || !database || !table) { setData(null); return }
    setLoading(true)
    getTableStatus(connId, database, table)
      .then(setData)
      .catch(() => setData(null))
      .finally(() => setLoading(false))
  }, [connId, database, table])

  if (!connId || !database || !table) return <div className="db-empty">选择数据库和表后查看状态</div>
  if (loading) return <div className="log-loading">加载表状态中...</div>
  if (!data || !data.columns?.length) return <div className="db-empty">暂无表状态信息</div>

  return (
    <div className="db-status">
      <div className="db-status-head dim">{data.engine} · {data.note || '表状态'}</div>
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
