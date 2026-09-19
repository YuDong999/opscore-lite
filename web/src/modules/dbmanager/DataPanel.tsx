// 表数据浏览面板: 点表即看数据。分页 + 总行数 + 多视图(表格/JSON/文本) + 字段筛选。
// 表格视图复用 DataGrid, JSON/文本视图展示原始数据。

import { useCallback, useEffect, useState, useMemo } from 'react'
import { type ConnectionInfo, fetchData, describeTable, getTableMeta, applyCellEdit, type TableData, type ColumnInfo, type TableMeta, importTableCsv, exportQuery, runQueryRaw } from './api'
import { useToast } from '../../components/Toast'
import DataGrid, { PAGE_SIZES } from '../../components/common/DataGrid'
import { FilterWorkbench } from './FilterWorkbench'

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
  // 列名 → 类型映射(用于判断数值列是否加引号); 类型取不到时按字符串(加引号)处理
  const colTypeByName = useMemo(() => {
    const m = new Map<string, string | undefined>()
    ;(colMeta ?? []).forEach(c => m.set(c.name, c.type))
    return m
  }, [colMeta])

  // 数值类型判定: 命中任一关键字即视为数值(不加引号)。
  // 覆盖: integer/int/bigint/smallint/tinyint/mediumint + int2/int4/int8 变体;
  // serial 系列; numeric/decimal/number; real/double/float; money; 以及 'xxx unsigned' 写法。
  // 类型取不到(undefined)返回 false → 保持现状(加引号), 不猜测。
  const isNumericType = useCallback((type?: string): boolean => {
    if (!type) return false
    // 去掉 (精度) 与 unsigned 修饰, 取首词判定 —— 兼容 'int unsigned' / 'unsigned int' 两种写法
    const base = type.toLowerCase().replace(/\(.*\)/, '').replace(/\bunsigned\b/g, '').trim().split(/\s+/)[0]
    const NUMERIC = new Set([
      'int', 'integer', 'smallint', 'bigint', 'tinyint', 'mediumint',
      'int2', 'int4', 'int8',
      'serial', 'bigserial', 'smallserial',
      'numeric', 'decimal', 'number',
      'real', 'double', 'float', 'money',
    ])
    return NUMERIC.has(base)
  }, [])

  // 筛选状态条 chip 文案: 复用 isNumericType / colTypeByName 同一套引号判定, 与 where 生成保持一致
  // (数值列无引号、其余加引号、NULL 类不吃 value、类型取不到按字符串加引号), 避免"显示 vs 实际 SQL"不一致
  const filterChipLabel = useCallback((f: { col: string; op: string; value: string }): string => {
    if (f.op === 'IS NULL') return `${f.col} IS NULL`
    if (f.op === 'IS NOT NULL') return `${f.col} IS NOT NULL`
    const num = isNumericType(colTypeByName.get(f.col))
    // LIKE 操作数恒为字符串字面量, 不受"数值列不加引号"影响(数值列也加引号, 否则 MySQL 语法错)
    if (f.op === 'LIKE') return `${f.col} LIKE '%${f.value}%'`
    return `${f.col} ${f.op} ${num ? f.value : `'${f.value}'`}`
  }, [isNumericType, colTypeByName])

  // 输入防抖: 筛选条件(filters)即时更新 UI, 但只有停手 350ms 后才提交到 appliedFilters
  // 并真正参与 where / 触发查询 —— 避免值输入框每敲一个字符都打一次数据库。
  const [appliedFilters, setAppliedFilters] = useState<Array<{ col: string; op: string; value: string }>>(filters)
  useEffect(() => {
    const t = setTimeout(() => setAppliedFilters(filters), 350)
    return () => clearTimeout(t)
  }, [filters])

  const where = useMemo(() => appliedFilters.map(f => {
    const col = f.col
    const op = f.op
    // NULL 类操作不吃 value, 始终保留(即使 value 为空也照常生成)
    if (op === 'IS NULL') return `${col} IS NULL`
    if (op === 'IS NOT NULL') return `${col} IS NOT NULL`
    // 空值/纯空白条件不进 WHERE(新条件默认 value:'' 即被过滤掉)
    if (!f.value || !f.value.trim()) return ''
    const v = f.value.replace(/'/g, "''")
    const num = isNumericType(colTypeByName.get(col))
    const q = (s: string) => (num ? s : `'${s}'`)
    switch (op) {
      case '=': return `${col} = ${q(v)}`
      case '!=': return `${col} != ${q(v)}`
      // LIKE 始终字符串字面量(数值列也加引号, 避免 MySQL Error 1064); 比较运算符才走数值去引号
      case 'LIKE': return `${col} LIKE '%${v}%'`
      case '>': return `${col} > ${q(v)}`
      case '<': return `${col} < ${q(v)}`
      case '>=': return `${col} >= ${q(v)}`
      case '<=': return `${col} <= ${q(v)}`
      default: return ''
    }
  }).filter(Boolean).join(` ${filterJoiner} `), [appliedFilters, filterJoiner, colTypeByName, isNumericType])

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
            { key: 'json', label: 'JSON' },
            { key: 'text', label: '文本' },
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

        {/* "每页行数"控件唯一入口在底部分页器(唯一外层 pager 的既定设计); 工具栏原重复控件已删除
            —— 两控件同绑 pageSize 但选项集不同, 选 10/20/1000 时工具栏显示值会背离真实值 */}
        <button className="btn-glass-soft btn-glass-soft-sm" onClick={load} disabled={busy} title="刷新">⟳</button>
      </div>

      {/* 常驻筛选状态条: 只要存在"生效中"的筛选条件就显示(不受 showFilterRow 影响), 空结果时也可见, 作为“清除筛选”逃生通道。
          生效判定与 where 生成(:91)完全一致 —— 空值/纯空白的非 NULL 条件视为未生效, 既不进 where 也不在 chip 展示,
          故 chip 里出现的每条, where 里必然也有; IS NULL / IS NOT NULL 不吃 value, 一加即生效, 始终展示。 */}
      {filters.some(f => f.op === 'IS NULL' || f.op === 'IS NOT NULL' || !!(f.value && f.value.trim())) && (
        <div className="db-filter-bar">
          <span className="dim db-filter-bar-label">筛选</span>
          {filters.map((f, fi) => {
            // 与 where 同源的生效判定; 未生效的条件(空值)直接不渲染, 避免"显示但不生效"
            const eff = f.op === 'IS NULL' || f.op === 'IS NOT NULL' || !!(f.value && f.value.trim())
            if (!eff) return null
            return (
              <span key={fi} className="db-filter-chip">
                <span className="db-filter-chip-text">{filterChipLabel(f)}</span>
                <button className="db-filter-chip-x" title="移除该条件" onClick={() => setFilters(fs => fs.filter((_, i) => i !== fi))}>✕</button>
              </span>
            )
          })}
          <button className="btn-glass-soft btn-glass-soft-sm db-filter-clear" onClick={() => { setFilters([]); setOrderBy('') }}>清除全部筛选</button>
        </div>
      )}

      {/* 行过滤栏(编辑 UI 抽取至 FilterWorkbench; filters/joiner 状态与 where 执行判定仍在本组件) */}
      {showFilterRow && data && (
        <FilterWorkbench
          columns={data.columns}
          filters={filters}
          onChange={setFilters}
          joiner={filterJoiner}
          onJoinerToggle={() => setFilterJoiner(j => (j === 'AND' ? 'OR' : 'AND'))}
          onClearAll={() => setFilters([])}
          hint={`多条件 ${filterJoiner} 组合 · 筛选后翻页统计随之变化`}
        />
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
              // 单表浏览天然可编辑; 有主键才激活(后端 apply-edit 对无主键表同样拒绝), 视图不可编辑
              isEditable: !isView && (colMeta ?? []).some(c => c.key === 'PRI'),
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
            hidePager
            backend={{
              onExport: (sql, format) => exportQuery(conn.id, sql, format),
              runWrite: sql => runQueryRaw(conn.id, sql),
            }}
            emptyState={
              filters.length > 0
                ? { hint: '当前筛选无匹配数据', actionLabel: '清除筛选', onAction: () => { setFilters([]); setOrderBy('') } }
                : { hint: '该表暂无数据' }
            }
          />
          <div className="db-data-pager">
            <span className="dim">共 {total} 行</span>
            <select className="input db-page-size" title="每页行数" value={pageSize}
              onChange={e => { setPageSize(Number(e.target.value)); setPage(1) }}>
              {PAGE_SIZES.map(n => <option key={n} value={n}>{n} 行/页</option>)}
            </select>
            <button className="btn-glass-soft btn-glass-soft-sm" disabled={page <= 1 || busy} onClick={() => setPage(1)} title="首页"><svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round"><path d="m15 18-6-6 6-6" /></svg></button>
            <button className="btn-glass-soft btn-glass-soft-sm" disabled={page <= 1 || busy} onClick={() => setPage(p => p - 1)} title="上一页"><svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round"><path d="m15 18-6-6 6-6" /></svg></button>
            <span>{page} / {totalPages}</span>
            <button className="btn-glass-soft btn-glass-soft-sm" disabled={page >= totalPages || busy} onClick={() => setPage(p => p + 1)} title="下一页"><svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round"><path d="m9 18 6-6-6-6" /></svg></button>
            <button className="btn-glass-soft btn-glass-soft-sm" disabled={page >= totalPages || busy} onClick={() => setPage(totalPages)} title="末页"><svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round"><path d="m9 18 6-6-6-6" /></svg></button>
          </div>
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
