// SQL 编辑器 + 结果表格。
// 不引入 Monaco(保持轻量), 使用 textarea + 行号 + Tab 缩进 + Ctrl+Enter 执行。
// 写操作经 ADR-003 拦截链: confirm_required 弹确认重发, write_locked 引导解锁, blocked 直接报错。
// 新增: 编辑功能支持 (Phase 2 M1)

import { useEffect, useRef, useState } from 'react'
import { runQueryRaw, type QueryResult, type InterceptionBody } from './api'

const SAMPLE_QUERIES = [
  { label: '当前用户', sql: 'SELECT CURRENT_USER() AS user, VERSION() AS version' },
  { label: '所有库', sql: '-- 切换到元数据类型 tab 查看库表' },
  { label: '前 100 行', sql: 'SELECT * FROM my_table LIMIT 100' },
]

const emptyResult = (): QueryResult => ({
  columns: [],
  rows: [],
  rowCount: 0,
  affected: 0,
  durationMs: 0,
  truncated: false,
})

export default function QueryEditor({
  connId,
  defaultSQL = '',
  onResult,
  onWriteLocked,
  onExecuted,
}: {
  connId: string
  defaultSQL?: string
  onResult?: (r: QueryResult) => void
  onWriteLocked?: (msg: string) => void
  onExecuted?: (sql: string) => void
}) {
  const [sql, setSql] = useState(defaultSQL)
  const [busy, setBusy] = useState(false)
  const taRef = useRef<HTMLTextAreaElement>(null)

  useEffect(() => {
    if (defaultSQL) setSql(defaultSQL)
  }, [defaultSQL])

  const run = async (confirm = false) => {
    if (!connId) return
    if (!sql.trim()) return
    setBusy(true)
    try {
      const { status, data } = await runQueryRaw(connId, sql, 5000, confirm)
      onExecuted?.(sql)
      await handleResponse(status, data as QueryResult & InterceptionBody, confirm)
    } catch (e: any) {
      onResult?.({ ...emptyResult(), error: e.message || '执行失败' })
    } finally {
      setBusy(false)
    }
  }

  const handleResponse = async (status: number, data: QueryResult & InterceptionBody, confirm: boolean) => {
    if (status === 200) {
      // 正常结果: 简化编辑判定(Phase 1: 假设 SELECT 都可编辑)
      if (data.columns?.length && data.rows?.length) {
        const clean = sql.toUpperCase().trim()
        // 只允许简单 SELECT 语句编辑
        const isEditable = clean.startsWith('SELECT') && 
                          !clean.includes(' JOIN ') && 
                          !clean.includes(' GROUP BY ') && 
                          !clean.includes(' ORDER BY ') &&
                          !clean.includes(' UNION ') &&
                          !clean.includes(' INTERSECT ') &&
                          !clean.includes(' EXCEPT ')
        data.isEditable = isEditable
      }
      onResult?.(data)
    } else if (status === 403) {
      // 拦截响应
      if (data.code === 'confirm_required') {
        if (confirm) {
          // 二次确认后重发
          run(true)
        } else {
          onResult?.(data)
        }
      } else if (data.code === 'write_locked') {
        onWriteLocked?.(data.error || '写操作被拦截: 连接默认只读')
        onResult?.(data)
      } else {
        onResult?.(data)
      }
    } else {
      onResult?.(data)
    }
  }

  return (
    <div className="db-query-editor">
      <div className="db-query-header">
        <div className="db-query-controls">
          <button onClick={() => run()} disabled={busy} className="btn btn-primary">
            {busy ? '执行中...' : '执行'}
          </button>
          <button onClick={() => run(true)} disabled={busy} className="btn btn-secondary">
            确认执行
          </button>
          <button onClick={() => setSql('')} className="btn btn-secondary">
            清空
          </button>
        </div>
        <div className="db-query-samples">
          {SAMPLE_QUERIES.map((q, i) => (
            <button key={i} onClick={() => setSql(q.sql)} className="btn btn-secondary">
              {q.label}
            </button>
          ))}
        </div>
      </div>
      
      <textarea
        ref={taRef}
        value={sql}
        onChange={(e) => setSql(e.target.value)}
        onKeyPress={(e) => {
          if (e.key === 'Enter' && e.ctrlKey) {
            e.preventDefault()
            run()
          }
        }}
        className="db-query-textarea"
        placeholder="输入 SQL 语句..."
      />
    </div>
  )
}