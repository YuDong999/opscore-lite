// 表数据浏览面板（P0）：点表即看数据。分页 + 总行数 + 每页行数 + 排序。
// 复用 DataGrid(排序/复制/导出)。

import { useCallback, useEffect, useState } from 'react'
import { type ConnectionInfo, fetchData, describeTable, type TableData } from './api'
import DataGrid from './DataGrid'

export default function DataPanel({
  conn, database, table, isView,
}: {
  conn: ConnectionInfo
  database: string
  table: string
  isView?: boolean
}) {
  const [data, setData] = useState<TableData | null>(null)
  const [colTypes, setColTypes] = useState<(string | undefined)[] | undefined>(undefined)
  const [page, setPage] = useState(1)
  const [pageSize, setPageSize] = useState(100)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')

  const load = useCallback(async () => {
    setBusy(true); setErr('')
    try {
      const d = await fetchData(conn.id, database, table, page, pageSize)
      setData(d)
    } catch (e: any) {
      setErr(e.message || '加载失败')
      setData(null)
    } finally {
      setBusy(false)
    }
  }, [conn.id, database, table, page, pageSize])

  // 列类型(列头第二行), 拉一次
  useEffect(() => {
    setColTypes(undefined)
    describeTable(conn.id, database, table)
      .then(d => setColTypes(d.columns.map(c => c.type)))
      .catch(() => setColTypes(undefined))
  }, [conn.id, database, table])

  useEffect(() => { load() }, [load])
  useEffect(() => { setPage(1) }, [database, table])

  const total = data?.total ?? 0
  const totalPages = Math.max(1, Math.ceil(total / pageSize))

  return (
    <div className="db-data-panel">
      <div className="db-data-toolbar">
        <span className={`db-engine-badge db-engine-${conn.engine}`}>{isView ? 'VIEW' : 'TABLE'}</span>
        <span className="db-data-title">{database}.{table}</span>
        {busy && <span className="dim">加载中...</span>}
        {err && <span style={{ color: 'var(--danger)', fontSize: '0.75rem' }}>{err}</span>}
        <span className="db-data-spacer" />
        <select
          className="input"
          style={{ width: 'auto', fontSize: '0.75rem', padding: '0.15rem 0.4rem' }}
          value={pageSize}
          onChange={e => { setPageSize(Number(e.target.value)); setPage(1) }}
          title="每页行数"
        >
          {[50, 100, 200, 500].map(n => <option key={n} value={n}>{n} 行/页</option>)}
        </select>
        <button className="btn-glass-soft btn-glass-soft-sm" onClick={load} disabled={busy} title="刷新">⟳</button>
      </div>
      <DataGrid
        result={data ? {
          columns: data.columns,
          rows: data.rows,
          rowCount: data.rows.length,
          affected: 0,
          durationMs: data.durationMs || 0,
          truncated: false,
        } : null}
        connId={conn.id}
        sql={`SELECT * FROM ${database}.${table}`}
        columnTypes={colTypes}
      />
      <div className="db-data-pager">
        <span className="dim">共 {total} 行</span>
        <button className="btn-glass-soft btn-glass-soft-sm" disabled={page <= 1 || busy} onClick={() => setPage(1)}>⏮</button>
        <button className="btn-glass-soft btn-glass-soft-sm" disabled={page <= 1 || busy} onClick={() => setPage(p => p - 1)}>‹</button>
        <span>{page} / {totalPages}</span>
        <button className="btn-glass-soft btn-glass-soft-sm" disabled={page >= totalPages || busy} onClick={() => setPage(p => p + 1)}>›</button>
        <button className="btn-glass-soft btn-glass-soft-sm" disabled={page >= totalPages || busy} onClick={() => setPage(totalPages)}>⏭</button>
      </div>
    </div>
  )
}
