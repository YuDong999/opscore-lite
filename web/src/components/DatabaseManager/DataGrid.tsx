// 查询结果表格: 渲染 columns/rows, 支持溢出截断、null/对象友好显示、单元格编辑。
// 增强: 表头点击排序(本地)、单元格点击复制、导出 CSV/JSON/XLSX(后端流式下载)。
// 编辑功能：单单元格编辑 + 批量编辑 + 发送编辑请求到后端生成 SQL。

import { useState, useEffect, useCallback, useMemo } from 'react'
import { createPortal } from 'react-dom'
import { type QueryResult, type ColumnInfo, exportQuery, type ExportFormat } from './api'
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

export default function DataGrid({ result, onEdit, connId, sql, columnTypes, onFilter, onClearFilters, onSortDatabase }: {
  result: QueryResult | null
  onEdit?: (changes: Array<{ row: number, col: number, newValue: any, oldValue: any }>) => void
  connId?: string
  sql?: string
  columnTypes?: (string | undefined)[]  // 列类型(数据浏览模式展示在列头第二行)
  onFilter?: (col: string, op: string, value: string) => void
  onClearFilters?: () => void
  onSortDatabase?: (col: string, dir: 'asc' | 'desc') => void
}) {
  const [editingCell, setEditingCell] = useState<EditableCell | null>(null)
  const [editedRows, setEditedRows] = useState<any[][]>([])
  const [isEditable, setIsEditable] = useState(false)
  const [sortCol, setSortCol] = useState<number | null>(null)
  const [sortAsc, setSortAsc] = useState(true)
  const [copied, setCopied] = useState('')
  const [detail, setDetail] = useState<{ v: any; col: string } | null>(null)
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

  // 本地排序视图(不影响 editedRows 的原始行号映射)
  const viewRows = useMemo(() => {
    if (sortCol === null || !editedRows.length) return editedRows
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
    return idx.map(i => editedRows[i])
  }, [editedRows, sortCol, sortAsc])

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
      { label: '单元格详情', icon: <ActionIcon kind="doc" />, onClick: () => setDetail({ v: cellValue, col: colName }) },
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
    items.push({ label: '导出当前页 (CSV)', icon: <ActionIcon kind="upload" />, disabled: !connId || !sql, onClick: () => { if (connId && sql) exportQuery(connId, sql, 'csv').catch(() => {}) } })
    return items
  }, [result, connId, sql])

  const [exporting, setExporting] = useState<ExportFormat | null>(null)
  const [exportErr, setExportErr] = useState('')

  const doExport = useCallback(async (format: ExportFormat) => {
    if (!connId || !sql?.trim()) return
    setExporting(format)
    setExportErr('')
    try {
      const { fileName } = await exportQuery(connId, sql, format)
      setExportErr(`已导出 ${fileName}`)
    } catch (e: any) {
      setExportErr(e.message || '导出失败')
    } finally {
      setExporting(null)
    }
  }, [connId, sql])

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

      <div className="table-wrap">
        <table className="db-table db-table-result">
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
            {viewRows.map((row, i) => (
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
                    onDoubleClick={() => setDetail({ v: cell, col: result.columns[j] })}
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
            ))}
          </tbody>
        </table>
      </div>
      {isEditable && (
        <div className="db-edit-controls">
          <button onClick={handleSave} className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-accent">保存修改</button>
          <button onClick={handleCancel} className="btn-glass-soft btn-glass-soft-sm">取消</button>
        </div>
      )}
      {ctxMenu && result && result.columns && (
        <ContextMenu
          x={ctxMenu.x}
          y={ctxMenu.y}
          items={buildCtxMenu(ctxMenu.row, ctxMenu.col, ctxMenu.x, ctxMenu.y)}
          onClose={() => setCtxMenu(null)}
        />
      )}
      {rowCtxMenu && result && result.columns && (
        <ContextMenu
          x={rowCtxMenu.x}
          y={rowCtxMenu.y}
          items={buildRowCtxMenu(rowCtxMenu.row, rowCtxMenu.x, rowCtxMenu.y)}
          onClose={() => setRowCtxMenu(null)}
        />
      )}
      {detail && createPortal(
        <div className="qo-overlay" onClick={() => setDetail(null)}>
          <div className="db-cell-detail" onClick={e => e.stopPropagation()}>
            <div className="db-cell-detail-head">
              <span className="db-cell-detail-col">{detail.col}</span>
              <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => { navigator.clipboard?.writeText(renderCell(detail.v)); setCopied('已复制'); setTimeout(() => setCopied(''), 1200) }}>复制</button>
              <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => setDetail(null)}>✕</button>
            </div>
            <pre className="db-cell-detail-body">{detail.v === null ? 'NULL' : typeof detail.v === 'object' ? JSON.stringify(detail.v, null, 2) : String(detail.v)}</pre>
          </div>
        </div>,
        document.body,
      )}
    </div>
  )
}
