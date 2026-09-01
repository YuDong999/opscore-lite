// 查询结果表格: 渲染 columns/rows, 支持溢出截断、null/对象友好显示、单元格编辑。
// 编辑功能：单单元格编辑 + 批量编辑 + 发送编辑请求到后端生成 SQL。
// 导出: CSV / JSON / XLSX, 由后端流式返回文件, 浏览器直接下载。

import { useState, useEffect, useCallback } from 'react'
import { type QueryResult, type ColumnInfo, exportQuery, type ExportFormat } from './api'

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

export default function DataGrid({ result, onEdit, connId, sql }: {
  result: QueryResult | null
  onEdit?: (changes: Array<{ row: number, col: number, newValue: any, oldValue: any }>) => void
  connId?: string
  sql?: string
}) {
  const [editingCell, setEditingCell] = useState<EditableCell | null>(null)
  const [editedRows, setEditedRows] = useState<any[][]>([])
  const [isEditable, setIsEditable] = useState(false)

  useEffect(() => {
    if (!result || !result.columns?.length || !result.rows?.length) {
      setIsEditable(false)
      return
    }
    setIsEditable(result.isEditable || false) // 使用后端返回的 isEditable 标志
    setEditedRows(result.rows.map(row => [...row]))
  }, [result])

  const handleCellClick = useCallback((row: number, col: number) => {
    if (!isEditable) return
    setEditingCell({ row, col, value: editedRows[row][col] })
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
    
    // 收集所有变更的单元格
    const changes: Array<{ row: number, col: number, newValue: any, oldValue: any }> = []
    
    result.rows.forEach((origRow, i) => {
      editedRows[i].forEach((editedValue, j) => {
        if (origRow[j] !== editedValue) {
          changes.push({
            row: i,
            col: j,
            newValue: editedValue,
            oldValue: origRow[j]
          })
        }
      })
    })
    
    if (changes.length > 0) {
      onEdit(changes)
      // 重置为原始数据
      setEditedRows(result.rows.map(row => [...row]))
    }
  }, [isEditable, result, editedRows, onEdit])

  const handleCancel = useCallback(() => {
    setEditingCell(null)
    if (result?.rows) {
      setEditedRows(result.rows.map(row => [...row]))
    }
  }, [result])

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
    return <div className="db-empty">执行查询后查看结果</div>
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
        </span>
        {result.truncated && <span className="pill pill-warn">结果已截断 (max 5000)</span>}
        {exportErr && <span className="dim" style={{ fontSize: '0.6875rem' }}>{exportErr}</span>}
        {canExport && (
          <div className="db-export-controls" style={{ marginLeft: 'auto', display: 'flex', gap: 4 }}>
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
        {isEditable && (
          <div className="db-edit-controls">
            <button onClick={handleSave} className="btn btn-primary" style={{ marginRight: 8 }}>
              保存修改
            </button>
            <button onClick={handleCancel} className="btn btn-secondary">
              取消
            </button>
          </div>
        )}
      </div>
      <div className="table-wrap">
        <table className="db-table db-table-result">
          <thead>
            <tr>
              <th className="db-col-num">#</th>
              {result.columns.map(c => (
                <th key={c}>{c}</th>
              ))}
            </tr>
          </thead>
          <tbody>
            {editedRows.map((row, i) => (
              <tr key={i}>
                <td className="db-col-num dim">{i + 1}</td>
                {row.map((cell, j) => (
                  <td 
                    key={j} 
                    title={typeof cell === 'string' ? cell : undefined}
                    onClick={() => handleCellClick(i, j)}
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
    </div>
  )
}