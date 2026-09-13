// 查询结果表格: 渲染 columns/rows, 支持溢出截断、null/对象友好显示、单元格编辑。
// 增强: 表头点击排序(本地)、单元格点击复制、导出 CSV/JSON/XLSX(后端流式下载)。
// 编辑功能：单单元格编辑 + 批量编辑 + 发送编辑请求到后端生成 SQL。

import { useState, useEffect, useCallback, useMemo } from 'react'
import { createPortal } from 'react-dom'
import { type QueryResult, type ColumnInfo, exportQuery, runQueryRaw, type ExportFormat } from './api'
import ContextMenu, { type ContextMenuItem } from './ContextMenu'
import { ActionIcon } from './DbIcons'

function renderCell(v: any): string {
  if (v === null || v === undefined) return ''
  if (typeof v === 'object') {
    try { return JSON.stringify(v) } catch { return String(v) }
  }
  if (typeof v === 'string' && v.length > 200) return v.substring(0, 200) + '...'
  return String(v)
}

interface EditableCell {
  row: number
  col: number
  value: any
}

export default function DataGrid({ result, onEdit, connId, sql, exportSql, columnTypes, columnMeta, onFilter, onClearFilters, onSortDatabase, onAfterWrite, hidePager }: {
  result: QueryResult | null
  onEdit?: (changes: Array<{ row: number, col: number, newValue: any, oldValue: any }>) => void
  connId?: string
  sql?: string
  columnTypes?: (string | undefined)[]  // 列类型(数据浏览模式展示在列头第二行)
  columnMeta?: ColumnInfo[]             // 完整列元数据(describe; 注释/可空/键)
  onFilter?: (col: string, op: string, value: string) => void
  onClearFilters?: () => void
  onSortDatabase?: (col: string, dir: 'asc' | 'desc') => void
  onAfterWrite?: () => void        // 写操作(置NULL等)成功后的刷新回调
  exportSql?: string                 // 导出用 SQL(数据页=当前页 LIMIT/OFFSET; 缺省用 sql)
  hidePager?: boolean                // 不渲染内部分页脚(数据页由外层 pager 负责)
}) {
  const [editingCell, setEditingCell] = useState<EditableCell | null>(null)
  const [editedRows, setEditedRows] = useState<any[][]>([])
  const [isEditable, setIsEditable] = useState(false)
  const [sortCol, setSortCol] = useState<number | null>(null)
  const [sortAsc, setSortAsc] = useState(true)
  const [copied, setCopied] = useState('')
  const [detail, setDetail] = useState<{ r: number; c: number } | null>(null)
  const [rowDetail, setRowDetail] = useState<number | null>(null)
  const [colDetail, setColDetail] = useState<number | null>(null)
  const [fieldFilter, setFieldFilter] = useState('')
  const [transpose, setTranspose] = useState<{ r: number } | null>(null)
  const [ctxMenu, setCtxMenu] = useState<{ row: number; col: number; x: number; y: number } | null>(null)
  const [rowCtxMenu, setRowCtxMenu] = useState<{ row: number; x: number; y: number } | null>(null)

  useEffect(() => {
    setSortCol(null); setSortAsc(true)
    if (!result || !result.columns?.length || !result.rows?.length) {
      setIsEditable(false)
      setEditedRows([])
      return
    }
    setIsEditable(!!(result as any).isEditable)
    setEditedRows(result.rows.map(row => [...row]))
  }, [result])

  const handleCellClick = useCallback((row: number, col: number) => {
    if (!isEditable) return
    setEditingCell({ row, col, value: editedRows[row]?.[col] })
  }, [isEditable, editedRows])

  const handleCellChange = useCallback((value: any) => {
    if (!editingCell || !result) return
    const newRows = [...editedRows]
    newRows[editingCell.row][editingCell.col] = value
    setEditedRows(newRows)
    setEditingCell(null)
  }, [editingCell, editedRows, result])

  const handleSave = useCallback(() => {
    if (!isEditable || !result || !result.columns || !onEdit) return
    const changes: Array<{ row: number, col: number, newValue: any, oldValue: any }> = []
    result.rows.forEach((origRow, i) => {
      editedRows[i]?.forEach((editedValue, j) => {
        if (origRow[j] !== editedValue) {
          changes.push({ row: i, col: j, newValue: editedValue, oldValue: origRow[j] })
        }
      })
    })
    if (changes.length > 0) {
      onEdit(changes)
      setEditedRows(result.rows.map(row => [...row]))
    }
  }, [isEditable, result, editedRows, onEdit])

  const handleCancel = useCallback(() => {
    setEditingCell(null)
    if (result?.rows) {
      setEditedRows(result.rows.map(row => [...row]))
    }
  }, [result])

  // ── 结果分页(dbx 同款): 默认 100 行/页 + 底部翻页栏; 行数据始终全量在内存(客户端分页) ──
  const [page, setPage] = useState(1)
  const [pageSize, setPageSize] = useState(100)
  useEffect(() => { setPage(1) }, [result])

  // 本地排序视图(不影响 editedRows 的原始行号映射)
  const viewRows = useMemo(() => {
    if (sortCol === null || !editedRows.length) return editedRows.map((_, i) => i)  // 统一语义: viewRows=原行索引数组
    const idx = editedRows.map((_, i) => i)
    idx.sort((a, b) => {
      const va = editedRows[a]?.[sortCol], vb = editedRows[b]?.[sortCol]
      if (va === null || va === undefined) return 1
      if (vb === null || vb === undefined) return -1
      const na = Number(va), nb = Number(vb)
      let cmp: number
      if (!Number.isNaN(na) && !Number.isNaN(nb) && String(va).trim() !== '' && String(vb).trim() !== '') {
        cmp = na - nb
      } else {
        cmp = String(va).localeCompare(String(vb))
      }
      return sortAsc ? cmp : -cmp
    })
    return idx
  }, [editedRows, sortCol, sortAsc])

  // 当前页的原行索引切片(排序感知; 右键/详情/编辑条全部用原索引, 排序不再错位)
  const pageStart = (page - 1) * pageSize
  const pageIdx = viewRows.slice(pageStart, pageStart + pageSize)



  // ── 详情(单/行/列三种聚合共用一个原子构建器, dbx dataGridDetail 同构) ──
  const metaByName = useMemo(() => new Map((columnMeta || []).map(m => [m.name, m])), [columnMeta])
  const buildCellInfo = useCallback((r: number, c: number) => {
    if (!result || !result.columns) return null
    const column = result.columns[c]
    if (column === undefined) return null
    const value = result.rows[r]?.[c] ?? null
    const meta = metaByName.get(column)
    return {
      column, rowNumber: r + 1, value,
      type: columnMeta?.[c]?.type || columnTypes?.[c] || '',
      comment: meta?.comment || '',
      nullable: meta?.nullable,
      key: meta?.key || '',
      length: value === null ? 0 : String(value).length,
    }
  }, [result, metaByName, columnMeta, columnTypes])

  const exportEffective = exportSql?.trim() || sql   // 导出用 SQL(数据页=当前页 LIMIT/OFFSET; 缺省用 sql)

  // 写操作辅助: 从 sql 解析目标表(SELECT * FROM x)、主键列、值转义
  const tableFromSql = useMemo(() => {
    const m = /FROM\s+([`\"\[\]\w.]+)/i.exec(sql || '')
    return m ? m[1] : null
  }, [sql])
  const pkCols = useMemo(() => (columnMeta || []).filter(c => c.key === 'PRI').map(c => c.name), [columnMeta])
  const escVal = useCallback((v: any) => {
    if (v === null) return 'NULL'
    if (typeof v === 'number') return String(v)
    return `'${String(v).replace(/'/g, "''")}'`
  }, [])

  const copyCell = useCallback((v: any) => {
    const text = renderCell(v)
    navigator.clipboard?.writeText(text).then(() => {
      setCopied('已复制')
      setTimeout(() => setCopied(''), 1200)
    }).catch(() => {})
  }, [])

  // dbx 式: 筛选子菜单真实现(走 DataPanel filters → 后端 where)
  const buildCtxMenu = useCallback((row: number, col: number, x: number, y: number): ContextMenuItem[] => {
    if (!result || !result.columns) return []
    const colName = result.columns[col]
    const cellValue = result.rows[row]?.[col]
    const rowData = result.rows[row] || []
    const fv = String(cellValue ?? '').slice(0, 40)
    const filterItems: (ContextMenuItem & { divider?: boolean | 'light' | 'heavy' })[] = onFilter ? [
      { label: `筛选 = '${fv}'`, icon: <ActionIcon kind="search" />, onClick: () => onFilter(colName, '=', String(cellValue ?? '')) },
      { label: `筛选 != '${fv}'`, icon: <ActionIcon kind="search" />, onClick: () => onFilter(colName, '!=', String(cellValue ?? '')) },
      { label: `筛选 LIKE '%${fv}%'`, icon: <ActionIcon kind="search" />, onClick: () => onFilter(colName, 'LIKE', String(cellValue ?? '')) },
      { divider: 'light' },
      { label: '筛选 IS NULL', icon: <ActionIcon kind="search" />, onClick: () => onFilter(colName, 'IS NULL', '') },
      { label: '筛选 IS NOT NULL', icon: <ActionIcon kind="search" />, onClick: () => onFilter(colName, 'IS NOT NULL', '') },
      { divider: 'light' },
      { label: '清除全部筛选', disabled: !onClearFilters, icon: <ActionIcon kind="refresh" />, onClick: () => onClearFilters?.() },
    ] : []
    const items: ContextMenuItem[] = [
      // ── 复制 ──
      { label: '复制值', icon: <ActionIcon kind="copy" />, onClick: () => copyCell(cellValue) },
      { label: '复制整行', icon: <ActionIcon kind="copy" />, onClick: () => navigator.clipboard?.writeText(rowData.map(v => renderCell(v)).join('\t')) },
      { label: '复制列名', icon: <ActionIcon kind="copy" />, onClick: () => navigator.clipboard?.writeText(colName) },
      { divider: 'heavy' },
      // ── 详情 ──
      { label: '单元格详情', icon: <ActionIcon kind="doc" />, onClick: () => setDetail({ r: row, c: col }) },
      { label: '行详情', icon: <ActionIcon kind="doc" />, onClick: () => { setFieldFilter(''); setRowDetail(row) } },
      { label: '列详情', icon: <ActionIcon kind="doc" />, onClick: () => { setFieldFilter(''); setColDetail(col) } },
      { label: '转置显示此行', icon: <ActionIcon kind="doc" />, onClick: () => setTranspose({ r: row }) },
      ...(tableFromSql && pkCols.length && !pkCols.includes(colName) ? [{
        label: '置为 NULL',
        icon: <ActionIcon kind="edit" />,
        onClick: () => {
          const rowData = result.rows[row] || []
          const where = pkCols.map(pk => {
            const idx = result.columns.indexOf(pk)
            return `${pk} = ${escVal(rowData[idx])}`
          }).join(' AND ')
          if (!confirm(`确认将 ${colName} 置为 NULL?
${tableFromSql} WHERE ${where}`)) return
          runQueryRaw(connId!, `UPDATE ${tableFromSql} SET ${colName} = NULL WHERE ${where}`)
            .then(r => {
              if (r.data.code === 'write_locked') { alert('写操作被拦截: 请先解锁写模式'); return }
              setCopied('已置 NULL'); setTimeout(() => setCopied(''), 1200)
              onAfterWrite?.()
            })
            .catch((e: any) => alert('置 NULL 失败: ' + (e.message || e)))
        },
      }] : []),
      ...(tableFromSql && pkCols.length ? [{
        label: '删除行',
        icon: <ActionIcon kind="delete" />,
        danger: true,
        onClick: () => {
          const rowData = result.rows[row] || []
          const where = pkCols.map(pk => {
            const idx = result.columns.indexOf(pk)
            return `${pk} = ${escVal(rowData[idx])}`
          }).join(' AND ')
          if (!confirm(`确认删除该行?
${tableFromSql} WHERE ${where}
不可撤销。`)) return
          runQueryRaw(connId!, `DELETE FROM ${tableFromSql} WHERE ${where}`)
            .then(r => {
              if (r.data.code === 'write_locked') { alert('写操作被拦截: 请先解锁写模式'); return }
              setCopied('已删除'); setTimeout(() => setCopied(''), 1200)
              onAfterWrite?.()
            })
            .catch((e: any) => alert('删除失败: ' + (e.message || e)))
        },
      }] : []),
      { divider: 'heavy' },
      // ── 排序(dbx 双模式: 数据库排序=后端 ORDER BY, 当前页排序=本地) ──
      ...(onSortDatabase ? [
        { label: '数据库升序排序', icon: <ActionIcon kind="refresh" />, onClick: () => onSortDatabase(colName, 'asc') },
        { label: '数据库降序排序', icon: <ActionIcon kind="refresh" />, onClick: () => onSortDatabase(colName, 'desc') },
        { divider: 'light' as const },
      ] : []),
      { label: '当前页升序排序', icon: <ActionIcon kind="refresh" />, onClick: () => { setSortCol(col); setSortAsc(true) } },
      { label: '当前页降序排序', icon: <ActionIcon kind="refresh" />, onClick: () => { setSortCol(col); setSortAsc(false) } },
      ...(sortCol !== null ? [{ label: '清除排序', icon: <ActionIcon kind="close" />, onClick: () => setSortCol(null) }] : []),
      ...(filterItems.length ? [{ divider: 'heavy' as const }] : []),
      // ── 筛选 ──
      ...filterItems,
    ]
    return items
  }, [result, copyCell, onFilter, onClearFilters, sortCol])

  const buildRowCtxMenu = useCallback((row: number, x: number, y: number): ContextMenuItem[] => {
    if (!result || !result.columns) return []
    const rowData = result.rows[row] || []
    const rowObj: Record<string, any> = {}
    result.columns.forEach((c, i) => { rowObj[c] = rowData[i] })
    const items: ContextMenuItem[] = [
      { label: '复制整行 (TAB)', icon: <ActionIcon kind="copy" />, onClick: () => navigator.clipboard?.writeText(rowData.map(v => renderCell(v)).join('\t')) },
      { label: '复制整行 (JSON)', icon: <ActionIcon kind="copy" />, onClick: () => navigator.clipboard?.writeText(JSON.stringify(rowObj, null, 2)) },
      { divider: true },
    ]
    items.push({ label: '导出当前页 (CSV)', icon: <ActionIcon kind="upload" />, disabled: !connId || !exportEffective, onClick: () => { if (connId && exportEffective) exportQuery(connId, exportEffective, 'csv').catch(() => {}) } })
    if (tableFromSql) {
      items.push({ divider: true })
      items.push({
        label: '复制行为新行 (INSERT)', icon: <ActionIcon kind="copy" />, disabled: !pkCols.length && !result.columns.length,
        onClick: () => {
          const cols = result.columns.filter(c => !pkCols.includes(c))
          const vals = cols.map(c => escVal(rowData[result.columns.indexOf(c)]))
          navigator.clipboard?.writeText(`INSERT INTO ${tableFromSql} (${cols.join(', ')}) VALUES (${vals.join(', ')})`)
        },
        title: '生成排除主键的 INSERT 语句到剪贴板',
      })
    }
    return items
  }, [result, connId, exportEffective, tableFromSql, pkCols])

  const [exporting, setExporting] = useState<ExportFormat | null>(null)
  const [exportErr, setExportErr] = useState('')

  const doExport = useCallback(async (format: ExportFormat) => {
    if (!connId || !exportEffective?.trim()) return
    setExporting(format)
    setExportErr('')
    try {
      const { fileName } = await exportQuery(connId, exportEffective, format)
      setExportErr(`已导出 ${fileName}`)
    } catch (e: any) {
      setExportErr(e.message || '导出失败')
    } finally {
      setExporting(null)
    }
  }, [connId, exportEffective])

  if (!result) {
    return <div className="db-empty">执行查询后查看结果 · Ctrl+Enter 快速执行</div>
  }
  if (result.error) {
    return <div className="banner banner-err" style={{ margin: 12 }}>{result.error}</div>
  }
  if (!result.columns?.length) {
    return <div className="db-empty">查询成功, 无返回列</div>
  }

  const canExport = !!connId && !!sql?.trim() && !result.error

  return (
    <div className="db-result">
      <div className="db-result-head">
        <span>
          {result.rowCount} 行
          {result.affected > 0 && ` · 影响 ${result.affected} 行`}
          {' · '}{result.durationMs}ms
          {sortCol !== null && ` · 已按 ${result.columns[sortCol]} ${sortAsc ? '↑' : '↓'} 排序`}
          {copied && <span style={{ color: 'var(--ok)' }}> {copied}</span>}
        </span>
        {result.truncated && <span className="pill pill-warn">结果已截断</span>}
        {exportErr && <span style={{ fontSize: '0.6875rem' }}>{exportErr}</span>}
        {canExport && (
          <div className="db-export-controls">
            {(['csv', 'json', 'xlsx'] as ExportFormat[]).map(fmt => (
              <button
                key={fmt}
                onClick={() => doExport(fmt)}
                disabled={!!exporting}
                className="btn-glass-soft btn-glass-soft-sm"
                title={`导出为 ${fmt.toUpperCase()}`}
              >
                {exporting === fmt ? '...' : fmt.toUpperCase()}
              </button>
            ))}
          </div>
        )}
      </div>

      {/* 多语句执行摘要 */}
      {result.statements && result.statements.length > 1 && (
        <div className="db-stmts-summary">
          <div className="db-stmts-title">执行摘要 · {result.statements.length} 条语句</div>
          <div className="db-stmts-list">
            {result.statements.map((s, i) => (
              <div key={i} className={`db-stmt-item db-stmt-${s.type.toLowerCase()}`}>
                <span className="db-stmt-num">{i + 1}</span>
                <span className={`pill db-stmt-type-${s.type.toLowerCase()}`}>{s.type}</span>
                <code className="db-stmt-sql" title={s.sql}>{s.sql.length > 80 ? s.sql.slice(0, 80) + '...' : s.sql}</code>
                {s.error ? (
                  <span className="pill pill-err" title={s.error}>失败</span>
                ) : (
                  <>
                    <span className="dim">{s.durationMs}ms</span>
                    {s.rows > 0 && <span className="dim">{s.rows} 行</span>}
                    {s.affected > 0 && <span className="dim">{s.affected} 行</span>}
                  </>
                )}
              </div>
            ))}
          </div>
        </div>
      )}

      <div className="db-table-grid">
        <table className="db-table-result">
          <thead>
            <tr>
              <th className="db-col-num">#</th>
              {result.columns.map((c, j) => (
                <th key={c} title="点击排序" onClick={() => {
                  if (sortCol === j) { setSortAsc(!sortAsc) } else { setSortCol(j); setSortAsc(true) }
                }}>
                  {columnTypes ? (
                    <span className="db-th-two-line">
                      <span className="db-th-name">{c}{sortCol === j ? (sortAsc ? ' ↑' : ' ↓') : ''}</span>
                      <span className="db-th-type">{columnTypes[j] || ''}</span>
                    </span>
                  ) : (
                    <>{c}{sortCol === j ? (sortAsc ? ' ↑' : ' ↓') : ''}</>
                  )}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {pageIdx.map((i, j) => {
              const row = editedRows[i] || []
              return (
              <tr
                key={i}
                onContextMenu={e => {
                  e.preventDefault()
                  // 只在行空白处(非单元格)弹行菜单; 打开前行菜单先关单元格菜单
                  if (e.target === e.currentTarget) {
                    setCtxMenu(null)
                    setRowCtxMenu({ row: i, x: e.clientX, y: e.clientY })
                  }
                }}
              >
                <td className="db-col-num">{i + 1}</td>
                {row.map((cell, j) => (
                   <td
                    key={j}
                    title={typeof cell === 'string' ? cell : undefined}
                    onContextMenu={e => {
                      e.preventDefault()
                      e.stopPropagation() // 防冒泡到 tr 造成双菜单叠加
                      setRowCtxMenu(null)
                      setCtxMenu({ row: i, col: j, x: e.clientX, y: e.clientY })
                    }}
                    onDoubleClick={() => setDetail({ r: i, c: j })}
                    onClick={() => {
                      if (editingCell?.row === i && editingCell?.col === j) return
                      if (isEditable) handleCellClick(i, j)
                    }}
                    className={editingCell?.row === i && editingCell?.col === j ? 'editing' : ''}
                  >
                    {editingCell?.row === i && editingCell?.col === j ? (
                      <input
                        type="text"
                        value={String(cell)}
                        onChange={(e) => handleCellChange(e.target.value)}
                        onBlur={handleCancel}
                        autoFocus
                        className="db-edit-input"
                      />
                    ) : (
                      cell === null ? <span className="dim">NULL</span> : renderCell(cell)
                    )}
                  </td>
                ))}
              </tr>
              )
            })}
          </tbody>
        </table>
      </div>
      {(!hidePager || isEditable) && (
      <div className="db-result-footer">
        {!hidePager && (<>
        <span className="dim">共 {viewRows.length} 行{result.truncated ? ' · 已截断' : ''}</span>
        <select className="input db-page-size" title="每页行数" value={pageSize}
          onChange={e => { setPageSize(Number(e.target.value)); setPage(1) }}>
          {[100, 200, 500, 1000].map(n => <option key={n} value={n}>{n} 行/页</option>)}
        </select>
        <button className="btn-glass-soft btn-glass-soft-sm" disabled={page <= 1} onClick={() => setPage(1)} title="首页" aria-label="首页">«</button>
        <button className="btn-glass-soft btn-glass-soft-sm" disabled={page <= 1} onClick={() => setPage(p => p - 1)} title="上一页" aria-label="上一页">‹</button>
        <span className="dim">{page} / {Math.max(1, Math.ceil(viewRows.length / pageSize))}</span>
        <button className="btn-glass-soft btn-glass-soft-sm" disabled={page >= Math.ceil(viewRows.length / pageSize)} onClick={() => setPage(p => p + 1)} title="下一页" aria-label="下一页">›</button>
        <button className="btn-glass-soft btn-glass-soft-sm" disabled={page >= Math.ceil(viewRows.length / pageSize)} onClick={() => setPage(Math.ceil(viewRows.length / pageSize))} title="末页" aria-label="末页">»</button>
        </>)}
        {isEditable && (
          <span style={{ marginLeft: 'auto', display: 'flex', gap: '0.3rem' }}>
            <button onClick={handleSave} className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-accent">保存修改</button>
            <button onClick={handleCancel} className="btn-glass-soft btn-glass-soft-sm">取消</button>
          </span>
        )}
      </div>
      )}
      {ctxMenu && result && result.columns?.length && ctxMenu.col < result.columns.length && (
        <ContextMenu
          x={ctxMenu.x}
          y={ctxMenu.y}
          items={buildCtxMenu(ctxMenu.row, ctxMenu.col, ctxMenu.x, ctxMenu.y)}
          onClose={() => setCtxMenu(null)}
        />
      )}
      {rowCtxMenu && result && result.rows?.length && rowCtxMenu.row < result.rows.length && (
        <ContextMenu
          x={rowCtxMenu.x}
          y={rowCtxMenu.y}
          items={buildRowCtxMenu(rowCtxMenu.row, rowCtxMenu.x, rowCtxMenu.y)}
          onClose={() => setRowCtxMenu(null)}
        />
      )}
      {/* 单元格详情: 元数据网格 + 注释 + 值(dbx CellDetailDialog 同构) */}
      {detail && (() => {
        const info = buildCellInfo(detail.r, detail.c)
        if (!info) return null
        return createPortal(
          <div className="qo-overlay" onClick={() => setDetail(null)}>
            <div className="db-cell-detail" onClick={e => e.stopPropagation()}>
              <div className="db-cell-detail-head">
                <span className="db-cell-detail-col">单元格详情</span>
                <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => setDetail(null)}>✕</button>
              </div>
              <div className="db-cell-detail-body">
                <div className="db-cell-meta">
                  <div><span className="dim">列名</span><b>{info.column}</b></div>
                  <div><span className="dim">行号</span>{info.rowNumber}</div>
                  <div><span className="dim">类型</span>{info.type || '-'}</div>
                  <div><span className="dim">长度</span>{info.length}</div>
                  {columnMeta && <div><span className="dim">可空</span>{info.nullable ? 'YES' : 'NO'}</div>}
                  {columnMeta && <div><span className="dim">键</span>{info.key || '-'}</div>}
                </div>
                <div className="db-cell-meta-comment">
                  <span className="dim">注释</span>
                  <span>{info.comment || '暂无注释'}</span>
                </div>
                <div className="db-cell-meta-value">
                  <div className="db-cell-meta-value-head">
                    <span className="dim">值</span>
                    <span style={{ marginLeft: 'auto', display: 'flex', gap: 4 }}>
                      <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => { navigator.clipboard?.writeText(info.value === null ? '' : renderCell(info.value)); setCopied('已复制'); setTimeout(() => setCopied(''), 1200) }}>复制值</button>
                      <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => { navigator.clipboard?.writeText(info.column); setCopied('已复制'); setTimeout(() => setCopied(''), 1200) }}>复制列名</button>
                    </span>
                  </div>
                  <pre>{info.value === null ? <i className="dim">NULL</i> : typeof info.value === 'object' ? JSON.stringify(info.value, null, 2) : String(info.value)}</pre>
                </div>
              </div>
            </div>
          </div>,
          document.body,
        )
      })()}
      {/* 行详情: 字段列表(列名+值, 可过滤) + 复制 JSON/TSV */}
      {rowDetail !== null && (() => {
        if (!result || !result.columns) return null
        const fields = result.columns
          .map((column, c) => buildCellInfo(rowDetail, c))
          .filter(Boolean) as NonNullable<ReturnType<typeof buildCellInfo>>[]
        const kw = fieldFilter.trim().toLowerCase()
        const shown = fields.filter(f => !kw || f.column.toLowerCase().includes(kw) || String(f.value ?? '').toLowerCase().includes(kw))
        const json = JSON.stringify(Object.fromEntries(fields.map(f => [f.column, f.value])), null, 2)
        const tsv = fields.map(f => renderCell(f.value)).join('	')
        return createPortal(
          <div className="qo-overlay" onClick={() => setRowDetail(null)}>
            <div className="db-cell-detail" onClick={e => e.stopPropagation()}>
              <div className="db-cell-detail-head">
                <span className="db-cell-detail-col">行详情 · 第 {rowDetail + 1} 行 <span className="dim">{fields.length} 列</span></span>
                <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => { navigator.clipboard?.writeText(json); setCopied('已复制 JSON'); setTimeout(() => setCopied(''), 1200) }}>复制 JSON</button>
                <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => { navigator.clipboard?.writeText(tsv); setCopied('已复制 TSV'); setTimeout(() => setCopied(''), 1200) }}>复制 TSV</button>
                <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => { const NL = String.fromCharCode(10); const md = '| ' + fields.map(f => f.column).join(' | ') + ' |' + NL + '| ' + fields.map(() => '---').join(' | ') + ' |' + NL + fields.map(f => '| ' + (f.value === null ? 'NULL' : String(f.value)) + ' |').join(NL); navigator.clipboard?.writeText(md); setCopied('已复制 Markdown'); setTimeout(() => setCopied(''), 1200) }}>复制 Markdown</button>
                <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => setRowDetail(null)}>✕</button>
              </div>
              <div className="db-cell-detail-body">
                <input className="input input-sm" style={{ marginBottom: 6 }} placeholder="过滤列名 / 值..." value={fieldFilter} onChange={e => setFieldFilter(e.target.value)} />
                <div className="db-detail-fields">
                  {shown.map(f => (
                    <div key={f.column} className="db-detail-field-row">
                      <span className="db-detail-field-name" title={f.column}>{f.column}</span>
                      <span className="db-detail-field-val">{f.value === null ? <i className="dim">NULL</i> : renderCell(f.value)}</span>
                    </div>
                  ))}
                  {shown.length === 0 && <div className="db-empty-sm">无匹配字段</div>}
                </div>
              </div>
            </div>
          </div>,
          document.body,
        )
      })()}
      {/* 转置显示: 单行按字段名×值两列铺开 */}
      {transpose !== null && (() => {
        if (!result || !result.columns) return null
        const fields = result.columns
          .map((column, c) => buildCellInfo(transpose.r, c))
          .filter(Boolean) as NonNullable<ReturnType<typeof buildCellInfo>>[]
        return createPortal(
          <div className="qo-overlay" onClick={() => setTranspose(null)}>
            <div className="db-cell-detail" onClick={e => e.stopPropagation()}>
              <div className="db-cell-detail-head">
                <span className="db-cell-detail-col">转置 · 第 {transpose.r + 1} 行</span>
                <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => setTranspose(null)}>✕</button>
              </div>
              <div className="db-cell-detail-body">
                <div className="db-detail-fields">
                  {fields.map(f => (
                    <div key={f.column} className="db-detail-field-row">
                      <span className="db-detail-field-name" title={f.column}>{f.column}</span>
                      <span className="db-detail-field-val">{f.value === null ? <i className="dim">NULL</i> : renderCell(f.value)}</span>
                    </div>
                  ))}
                </div>
              </div>
            </div>
          </div>,
          document.body,
        )
      })()}
      {/* 列详情: 列元数据头 + 本列逐行值(可过滤) */}
      {colDetail !== null && (() => {
        if (!result || !result.columns) return null
        const meta = buildCellInfo(0, colDetail)
        if (!meta) return null
        const kw = fieldFilter.trim().toLowerCase()
        const rows = result.rows
          .map((row, r) => ({ rowNumber: r + 1, value: row[colDetail] }))
          .filter(x => !kw || String(x.value ?? '').toLowerCase().includes(kw) || String(x.rowNumber).includes(kw))
        const tsv = result.rows.map(row => renderCell(row[colDetail])).join('\n')
        const json = JSON.stringify(result.rows.map((row, r) => ({ row: r + 1, value: row[colDetail] })), null, 2)
        return createPortal(
          <div className="qo-overlay" onClick={() => setColDetail(null)}>
            <div className="db-cell-detail" onClick={e => e.stopPropagation()}>
              <div className="db-cell-detail-head">
                <span className="db-cell-detail-col">列详情 · {meta.column}</span>
                <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => { navigator.clipboard?.writeText(json); setCopied('已复制 JSON'); setTimeout(() => setCopied(''), 1200) }}>复制 JSON</button>
                <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => { navigator.clipboard?.writeText(tsv); setCopied('已复制 全列值'); setTimeout(() => setCopied(''), 1200) }}>复制全列值</button>
                <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => setColDetail(null)}>✕</button>
              </div>
              <div className="db-cell-detail-body">
                <div className="db-cell-meta">
                  <div><span className="dim">类型</span>{meta.type || '-'}</div>
                  {columnMeta && <div><span className="dim">可空</span>{meta.nullable ? 'YES' : 'NO'}</div>}
                  {columnMeta && <div><span className="dim">键</span>{meta.key || '-'}</div>}
                  <div><span className="dim">行数</span>{result.rows.length}</div>
                </div>
                <div className="db-cell-meta-comment">
                  <span className="dim">注释</span>
                  <span>{meta.comment || '暂无注释'}</span>
                </div>
                <input className="input input-sm" style={{ margin: '6px 0' }} placeholder="过滤值 / 行号..." value={fieldFilter} onChange={e => setFieldFilter(e.target.value)} />
                <div className="db-detail-fields">
                  {rows.map(x => (
                    <div key={x.rowNumber} className="db-detail-field-row">
                      <span className="db-detail-field-name">{x.rowNumber}</span>
                      <span className="db-detail-field-val">{x.value === null ? <i className="dim">NULL</i> : renderCell(x.value)}</span>
                    </div>
                  ))}
                  {rows.length === 0 && <div className="db-empty-sm">无匹配行</div>}
                </div>
              </div>
            </div>
          </div>,
          document.body,
        )
      })()}
    </div>
  )
}
