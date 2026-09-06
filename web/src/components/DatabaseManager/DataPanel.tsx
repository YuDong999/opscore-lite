// 表数据浏览面板: 点表即看数据。分页 + 总行数 + 多视图(表格/JSON/文本) + 字段筛选。
// 表格视图复用 DataGrid, JSON/文本视图展示原始数据。

import { useCallback, useEffect, useState, useMemo } from 'react'
import { type ConnectionInfo, fetchData, describeTable, type TableData } from './api'
import DataGrid from './DataGrid'

type ViewMode = 'table' | 'json' | 'text'

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
  const [viewMode, setViewMode] = useState<ViewMode>('table')
  const [visibleCols, setVisibleCols] = useState<Set<number>>(new Set())
  const [showColFilter, setShowColFilter] = useState(false)
  const [showFilterRow, setShowFilterRow] = useState(false)
  const [orderBy, setOrderBy] = useState('')
  const [orderDir, setOrderDir] = useState<'ASC' | 'DESC'>('ASC')
  const [filters, setFilters] = useState<Array<{ col: string; op: string; value: string }>>([])
  const where = useMemo(() => filters.map(f => {
    const col = f.col
    const v = f.value.replace(/'/g, "''")
    switch (f.op) {
      case '=': return `${col} = '${v}'`
      case '!=': return `${col} != '${v}'`
      case 'LIKE': return `${col} LIKE '%${v}%'`
      case '>': return `${col} > '${v}'`
      case '<': return `${col} < '${v}'`
      case '>=': return `${col} >= '${v}'`
      case '<=': return `${col} <= '${v}'`
      case 'IS NULL': return `${col} IS NULL`
      case 'IS NOT NULL': return `${col} IS NOT NULL`
      default: return ''
    }
  }).filter(Boolean).join(' AND '), [filters])

  const load = useCallback(async () => {
    setBusy(true); setErr('')
    try {
      const d = await fetchData(conn.id, database, table, page, pageSize, orderBy, orderDir, where)
      setData(d)
      if (d.columns.length > 0 && visibleCols.size === 0) {
        setVisibleCols(new Set(d.columns.map((_, i) => i)))
      }
    } catch (e: any) {
      setErr(e.message || '加载失败')
      setData(null)
    } finally {
      setBusy(false)
    }
  }, [conn.id, database, table, page, pageSize, where, orderBy, orderDir])

  useEffect(() => {
    setColTypes(undefined)
    describeTable(conn.id, database, table)
      .then(d => {
        setColTypes(d.columns.map(c => c.type))
      })
      .catch(() => setColTypes(undefined))
  }, [conn.id, database, table])

  useEffect(() => { load() }, [load])
  useEffect(() => { setPage(1) }, [database, table, where, orderBy, orderDir])

  const total = data?.total ?? 0
  const totalPages = Math.max(1, Math.ceil(total / pageSize))

  const toggleCol = (idx: number) => {
    setVisibleCols(prev => {
      const next = new Set(prev)
      if (next.has(idx)) next.delete(idx); else next.add(idx)
      return next
    })
  }

  const visibleColumns = useMemo(() => {
    if (!data) return []
    return data.columns.filter((_, i) => visibleCols.has(i))
  }, [data, visibleCols])

  // JSON 视图
  const jsonRows = useMemo(() => {
    if (!data || viewMode !== 'json') return []
    return data.rows.map(row => {
      const obj: Record<string, any> = {}
      data.columns.forEach((col, i) => { if (visibleCols.has(i)) obj[col] = row[i] })
      return obj
    })
  }, [data, viewMode, visibleCols])

  // 文本视图
  const textRows = useMemo(() => {
    if (!data || viewMode !== 'text') return []
    const colWidths = data.columns.map((col, i) => {
      if (!visibleCols.has(i)) return 0
      return Math.max(col.length, ...data.rows.slice(0, 20).map(row => String(row[i] ?? '').length))
    })
    return data.rows.map(row => {
      const parts: string[] = []
      data.columns.forEach((col, i) => {
        if (!visibleCols.has(i)) return
        const w = colWidths[i]
        const val = String(row[i] ?? 'NULL').padEnd(w)
        parts.push(val)
      })
      return parts.join(' | ')
    })
  }, [data, viewMode, visibleCols])


  return (
    <div className="db-data-panel">
      <div className="db-data-toolbar">
        <span className={`db-engine-badge db-engine-${conn.engine}`}>{isView ? 'VIEW' : 'TABLE'}</span>
        <span className="db-data-title"><span className="db-crumb-db">{database}</span>.{table}</span>
        {total > 0 && <span className="db-toolbar-stat">{total} 行</span>}
        {busy && <span className="dim">加载中...</span>}
        {err && <span style={{ color: 'var(--danger)', fontSize: '0.75rem' }} title={err}>⚠ {err.slice(0, 40)}</span>}
        <span className="db-data-spacer" />

        {/* 视图切换 */}
        <div className="db-view-toggle">
          {([
            { key: 'table', label: '表格' },
            { key: 'json', label: '{ } JSON' },
            { key: 'text', label: 'T 文本' },
          ] as const).map(v => (
            <button
              key={v.key}
              className={`btn-glass-soft btn-glass-soft-sm ${viewMode === v.key ? 'active' : ''}`}
              onClick={() => setViewMode(v.key)}
              title={v.label}
            >
              {v.label}
            </button>
          ))}
        </div>

        {/* 字段筛选 */}
        <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => setShowColFilter(!showColFilter)} title="字段筛选">
          {showColFilter ? '隐藏字段' : '筛选字段'}
        </button>
        <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => setShowFilterRow(!showFilterRow)} title="按条件过滤行">
          {showFilterRow ? '隐藏过滤' : '过滤行'}
        </button>

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

      {/* 行过滤栏 */}
      {showFilterRow && data && (
        <div className="db-row-filter">
          {filters.map((f, fi) => (
            <div key={fi} className="db-row-filter-row">
              <select className="input" value={f.col} onChange={e => setFilters(fs => fs.map((x, i) => i === fi ? { ...x, col: e.target.value } : x))}>
                {data.columns.map(c => <option key={c} value={c}>{c}</option>)}
              </select>
              <select className="input" value={f.op} onChange={e => setFilters(fs => fs.map((x, i) => i === fi ? { ...x, op: e.target.value } : x))}>
                {['=', '!=', 'LIKE', '>', '<', '>=', '<=', 'IS NULL', 'IS NOT NULL'].map(op => <option key={op} value={op}>{op}</option>)}
              </select>
              <input className="input" placeholder="值" value={f.value} disabled={f.op.startsWith('IS')}
                onChange={e => setFilters(fs => fs.map((x, i) => i === fi ? { ...x, value: e.target.value } : x))} />
              <button className="btn-glass-soft btn-glass-soft-sm" title="移除" onClick={() => setFilters(fs => fs.filter((_, i) => i !== fi))}>✕</button>
            </div>
          ))}
          <div style={{ display: 'flex', gap: '0.4rem', alignItems: 'center' }}>
            <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => setFilters(fs => [...fs, { col: data.columns[0] || '', op: '=', value: '' }])}>+ 条件</button>
            {filters.length > 0 && <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => setFilters([])}>清除全部</button>}
            <span className="dim" style={{ fontSize: '0.625rem' }}>多条件 AND 组合 · 筛选后翻页统计随之变化</span>
          </div>
        </div>
      )}

      {/* 字段筛选栏 */}
      {showColFilter && data && (
        <div className="db-col-filter">
          {data.columns.map((col, i) => {
            const visible = visibleCols.has(i)
            return (
              <label key={col} className="db-col-filter-item">
                <input type="checkbox" checked={visible} onChange={() => toggleCol(i)} />
                <span className={visible ? '' : 'dim'} style={{ fontSize: '0.75rem' }}>{col}</span>
                {colTypes && colTypes[i] && <span className="dim" style={{ fontSize: '0.625rem' }}>{colTypes[i]}</span>}
              </label>
            )
          })}
        </div>
      )}

      {/* 内容区 */}
      {viewMode === 'table' && data && visibleCols.size > 0 ? (
        <>
          <DataGrid
            result={{
              columns: visibleColumns,
              rows: data.rows.map(row => visibleColumns.map((_, i) => {
                const origIdx = data.columns.indexOf(visibleColumns[i])
                return origIdx >= 0 ? row[origIdx] : null
              })),
              rowCount: data.rows.length,
              affected: 0,
              durationMs: data.durationMs || 0,
              truncated: false,
            }}
            connId={conn.id}
            sql={`SELECT * FROM ${database}.${table}`}
            columnTypes={colTypes?.filter((_, i) => visibleCols.has(i))}
            onFilter={(col, op, value) => { setFilters([{ col, op, value }]); setPage(1) }}
            onClearFilters={() => { setFilters([]); setOrderBy('') }}
            onSortDatabase={(col, dir) => { setOrderBy(col); setOrderDir(dir === 'desc' ? 'DESC' : 'ASC'); setPage(1) }}
          />
          <div className="db-data-pager">
            <span className="dim">共 {total} 行</span>
            <button className="btn-glass-soft btn-glass-soft-sm" disabled={page <= 1 || busy} onClick={() => setPage(1)}>⏮</button>
            <button className="btn-glass-soft btn-glass-soft-sm" disabled={page <= 1 || busy} onClick={() => setPage(p => p - 1)}>‹</button>
            <span>{page} / {totalPages}</span>
            <button className="btn-glass-soft btn-glass-soft-sm" disabled={page >= totalPages || busy} onClick={() => setPage(p => p + 1)}>›</button>
            <button className="btn-glass-soft btn-glass-soft-sm" disabled={page >= totalPages || busy} onClick={() => setPage(totalPages)}>⏭</button>
          </div>
        </>
      ) : viewMode === 'json' ? (
        <div className="db-json-view">
          <pre className="code-block">{JSON.stringify(jsonRows, null, 2)}</pre>
        </div>
      ) : viewMode === 'text' ? (
        <div className="db-text-view">
          <pre className="code-block">{textRows.join('\n')}</pre>
        </div>
      ) : (
        <div className="db-empty">选择字段后查看数据</div>
      )}
    </div>
  )
}
