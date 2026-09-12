// 表数据浏览面板: 点表即看数据。分页 + 总行数 + 多视图(表格/JSON/文本) + 字段筛选。
// 表格视图复用 DataGrid, JSON/文本视图展示原始数据。

import { useCallback, useEffect, useState, useMemo } from 'react'
import { type ConnectionInfo, fetchData, describeTable, getTableMeta, applyCellEdit, type TableData, type ColumnInfo, type TableMeta, importTableCsv } from './api'
import { useToast } from '../Toast'
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
  const toast = useToast()
  const [data, setData] = useState<TableData | null>(null)
  const [colTypes, setColTypes] = useState<(string | undefined)[] | undefined>(undefined)
  const [colMeta, setColMeta] = useState<ColumnInfo[] | undefined>(undefined)
  const [meta, setMeta] = useState<TableMeta | null>(null)
  const [showMeta, setShowMeta] = useState(false)
  const [metaTab, setMetaTab] = useState<'indexes' | 'fks' | 'triggers'>('indexes')
  const [showImport, setShowImport] = useState(false)
  const [importCsv, setImportCsv] = useState('')
  const [importMsg, setImportMsg] = useState('')
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
  const [filterJoiner, setFilterJoiner] = useState<'AND' | 'OR'>('AND')
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
  }).filter(Boolean).join(` ${filterJoiner} `), [filters, filterJoiner])

  const load = useCallback(async () => {
    setBusy(true); setErr('')
    try {
      const d = await fetchData(conn.id, database, table, page, pageSize, orderBy, orderDir, where)
      const normalized = { ...d, columns: d.columns || [], rows: d.rows || [] }
      setData(normalized)
      if (normalized.columns.length > 0 && visibleCols.size === 0) {
        setVisibleCols(new Set(normalized.columns.map((_, i) => i)))
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
        setColTypes(d?.columns?.map(c => c?.type) ?? undefined)
        setColMeta(d?.columns)
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
    if (!data || !data.columns) return []
    return data.columns.filter((_, i) => visibleCols.has(i))
  }, [data, visibleCols])

  // 单元格编辑落库: 变更 → 后端按方言拼 UPDATE(主键定位) → 预览确认 → 执行 → 回读。
  // 无主键的表直接拒绝(后端同样拒绝)。行索引与 data.rows 对齐(DataGrid 保证), 列名取可见列投影。
  const handleEditChanges = useCallback(async (changes: Array<{ row: number, col: number, newValue: any, oldValue: any }>) => {
    if (!data?.rows || !data.columns) return
    const pkCols = (colMeta ?? []).filter(c => c.key === 'PRI').map(c => c.name)
    if (pkCols.length === 0) { toast.error('该表无主键, 无法安全定位行, 已拒绝编辑'); return }
    const rowObjAt = (rowIdx: number) => {
      const obj: Record<string, any> = {}
      data.columns.forEach((name, i) => { obj[name] = data.rows![rowIdx]?.[i] })
      return obj
    }
    try {
      // 第一刀: confirm=false 拿到后端生成的 SQL 预览, 展示后再执行(透明原则)
      const previews = await Promise.all(changes.map(ch =>
        applyCellEdit(conn.id, database, table, pkCols, rowObjAt(ch.row), visibleColumns[ch.col], ch.newValue, false)
      ))
      const sqls = previews.map(p => p.sql).filter(Boolean)
      if (sqls.length && !window.confirm('将执行以下语句:\n\n' + sqls.join('\n\n'))) return
      let done = 0
      for (const ch of changes) {
        const r = await applyCellEdit(conn.id, database, table, pkCols, rowObjAt(ch.row), visibleColumns[ch.col], ch.newValue, true)
        if (!r.ok) throw new Error(r.error || '写入失败')
        done += r.affected ?? 0
      }
      toast.success(`已更新 ${changes.length} 个单元格 (影响 ${done} 行)`)
      load()
    } catch (e: any) {
      toast.error('更新失败: ' + (e.message || e))
    }
  }, [data, colMeta, visibleColumns, conn.id, database, table, load, toast])

  // JSON 视图
  const jsonRows = useMemo(() => {
    if (!data || viewMode !== 'json' || !data.rows) return []
    return data.rows.map(row => {
      const obj: Record<string, any> = {}
      data.columns.forEach((col, i) => { if (visibleCols.has(i)) obj[col] = row[i] })
      return obj
    })
  }, [data, viewMode, visibleCols])

  // 文本视图
  const textRows = useMemo(() => {
    if (!data || viewMode !== 'text' || !data.columns || !data.rows) return []
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

        <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => { setShowImport(!showImport); setImportMsg('') }} title="粘贴或选择 CSV(首行为列名)导入本表">
          {showImport ? '隐藏导入' : '导入数据'}
        </button>

        {showImport && (
          <div className="db-import-panel" style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
              <b style={{ fontSize: '0.78rem' }}>导入 CSV 到 {database}.{table}</b>
              <span className="dim" style={{ fontSize: '0.6875rem' }}>首行为列名 · NULL 关键字置空 · 事务内整批写入</span>
              <label className="btn-glass-soft btn-glass-soft-sm" style={{ marginLeft: 'auto' }}>
                选择文件
                <input type="file" accept=".csv,.txt" style={{ display: 'none' }} onChange={e => {
                  const f = e.target.files?.[0]
                  if (f) { const rd = new FileReader(); rd.onload = () => setImportCsv(String(rd.result || '')); rd.readAsText(f) }
                  e.target.value = ''
                }} />
              </label>
            </div>
            <textarea className="input" style={{ minHeight: '7rem', fontFamily: 'ui-monospace, Consolas, monospace', fontSize: '0.72rem' }}
              placeholder={'id,name,score\n9001,zhang,90\n9002,NULL,85'}
              value={importCsv} onChange={e => setImportCsv(e.target.value)} />
            <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
              <button className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-accent"
                disabled={!importCsv.trim()}
                onClick={() => {
                  setImportMsg('导入中...')
                  importTableCsv(conn.id, database, table, importCsv)
                    .then(r => { setImportMsg(`导入成功: ${r.imported} 行`); load() })
                    .catch(e => setImportMsg('失败: ' + (e.message || e)))
                }}>
                开始导入
              </button>
              {importMsg && <span style={{ fontSize: '0.75rem' }}>{importMsg}</span>}
            </div>
          </div>
        )}

        {/* 表信息抽屉 */}
        <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => {
          if (!meta) getTableMeta(conn.id, database, table).then(setMeta).catch(e => setMeta(null))
          setShowMeta(!showMeta)
        }} title="表信息: 索引 / 外键 / 触发器">
          {showMeta ? '隐藏表信息' : '表信息'}
        </button>

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
            {filters.length > 1 && (
              <button className="btn-glass-soft btn-glass-soft-sm" title="切换条件间组合方式"
                onClick={() => setFilterJoiner(j => (j === 'AND' ? 'OR' : 'AND'))}>
                组合: {filterJoiner}
              </button>
            )}
            {filters.length > 0 && <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => setFilters([])}>清除全部</button>}
            <span className="dim" style={{ fontSize: '0.625rem' }}>多条件 {filterJoiner} 组合 · 筛选后翻页统计随之变化</span>
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
               rows: (data.rows || []).map(row => visibleColumns.map((_, i) => {
                const origIdx = data.columns.indexOf(visibleColumns[i])
                return origIdx >= 0 ? row[origIdx] : null
              })),
              rowCount: data.rows?.length ?? 0,
              affected: 0,
              durationMs: data.durationMs || 0,
              truncated: false,
            }}
            connId={conn.id}
            sql={`SELECT * FROM ${database}.${table}`}
            exportSql={`SELECT * FROM ${database}.${table} LIMIT ${pageSize} OFFSET ${(page - 1) *pageSize}`}
            onEdit={handleEditChanges}
            columnTypes={colTypes?.filter((_, i) => visibleCols.has(i))}
            columnMeta={colMeta?.filter((_, i) => visibleCols.has(i))}
            onFilter={(col, op, value) => { setFilters([{ col, op, value }]); setPage(1) }}
            onClearFilters={() => { setFilters([]); setOrderBy('') }}
            onSortDatabase={(col, dir) => { setOrderBy(col); setOrderDir(dir === 'desc' ? 'DESC' : 'ASC'); setPage(1) }}
          />
          {showMeta && (
            <div className="db-table-meta-drawer">
              <div className="db-table-meta-tabs">
                {([['indexes', `索引 (${meta?.indexes?.length ?? 0})`], ['fks', `外键 (${meta?.foreignKeys?.length ?? 0})`], ['triggers', `触发器 (${meta?.triggers?.length ?? 0})`]] as const).map(([k, label]) => (
                  <button key={k} className={`btn-glass-soft btn-glass-soft-sm ${metaTab === k ? 'active' : ''}`} onClick={() => setMetaTab(k)}>{label}</button>
                ))}
              </div>
              {!meta ? <div className="db-empty-sm">加载表信息失败(可能不含该元数据)</div> : metaTab === 'indexes' ? (
                <table className="db-table db-table-meta-mini">
                  <thead><tr><th>索引名</th><th>列</th><th>唯一</th><th>主键</th></tr></thead>
                  <tbody>
                    {(!meta?.indexes?.length) && <tr><td colSpan={4} className="dim">无索引</td></tr>}
                    {(meta?.indexes || []).map((ix, i) => (
                      <tr key={i}>
                        <td>{ix.name || '-'}</td>
                        <td>{(ix.columns || []).join(', ')}</td>
                        <td>{ix.unique ? '是' : '否'}</td>
                        <td>{ix.primary ? '是' : '否'}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              ) : metaTab === 'fks' ? (
                <table className="db-table db-table-meta-mini">
                  <thead><tr><th>约束名</th><th>本表列</th><th>引用表</th><th>引用列</th></tr></thead>
                  <tbody>
                    {(!meta?.foreignKeys?.length) && <tr><td colSpan={4} className="dim">无外键</td></tr>}
                    {(meta?.foreignKeys || []).map((fk, i) => (
                      <tr key={i}>
                        <td>{fk.constraint || fk.name || '-'}</td>
                        <td>{fk.column}</td>
                        <td>{fk.refTable}</td>
                        <td>{fk.refColumn}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              ) : (
                <table className="db-table db-table-meta-mini">
                  <thead><tr><th>触发器</th><th>时机</th><th>事件</th></tr></thead>
                  <tbody>
                    {(!meta?.triggers?.length) && <tr><td colSpan={3} className="dim">无触发器</td></tr>}
                    {(meta?.triggers || []).map((tr, i) => (
                      <tr key={i}>
                        <td title={tr.statement}>{tr.name}</td>
                        <td>{tr.timing}</td>
                        <td>{tr.event}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
            </div>
          )}
          <div className="db-data-pager">
            <span className="dim">共 {total} 行</span>
            <select className="input db-page-size" title="每页行数" value={pageSize}
              onChange={e => { setPageSize(Number(e.target.value)); setPage(1) }}>
              {[10, 20, 50, 100, 200, 500, 1000].map(n => <option key={n} value={n}>{n} 行/页</option>)}
            </select>
            <button className="btn-glass-soft btn-glass-soft-sm" disabled={page <= 1 || busy} onClick={() => setPage(1)} title="首页"><svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round"><path d="m15 18-6-6 6-6" /></svg></button>
            <button className="btn-glass-soft btn-glass-soft-sm" disabled={page <= 1 || busy} onClick={() => setPage(p => p - 1)} title="上一页"><svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round"><path d="m15 18-6-6 6-6" /></svg></button>
            <span>{page} / {totalPages}</span>
            <button className="btn-glass-soft btn-glass-soft-sm" disabled={page >= totalPages || busy} onClick={() => setPage(p => p + 1)} title="下一页"><svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round"><path d="m9 18 6-6-6-6" /></svg></button>
            <button className="btn-glass-soft btn-glass-soft-sm" disabled={page >= totalPages || busy} onClick={() => setPage(totalPages)} title="末页"><svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round"><path d="m9 18 6-6-6-6" /></svg></button>
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
