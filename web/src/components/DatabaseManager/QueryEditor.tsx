// SQL 编辑器 + 结果表格。
// 不引入 Monaco(保持轻量), 使用 textarea + Tab 缩进 + Ctrl+Enter 执行。
// 工具: 格式化(sql-formatter, 按引擎方言) / 执行历史(本地回填) / 示例。
// 写操作经 ADR-003 拦截链: confirm_required 弹确认重发, write_locked 引导解锁, blocked 直接报错。

import { useEffect, useRef, useState } from 'react'
import { runQueryRaw, type QueryResult, type InterceptionBody } from './api'
import { formatSQL } from './sqlFormat'

const SAMPLE_QUERIES = [
  { label: '当前用户', sql: "SELECT CURRENT_USER() AS user, VERSION() AS version" },
  { label: '前 100 行', sql: 'SELECT * FROM my_table LIMIT 100' },
]

const HISTORY_KEY = 'dbmanager:sql-history'
const HISTORY_MAX = 30

function loadHistory(): string[] {
  try {
    const raw = localStorage.getItem(HISTORY_KEY)
    const arr = raw ? JSON.parse(raw) : []
    return Array.isArray(arr) ? arr.slice(0, HISTORY_MAX) : []
  } catch {
    return []
  }
}

function pushHistory(sqlText: string): string[] {
  const t = sqlText.trim()
  if (!t) return loadHistory()
  const next = [t, ...loadHistory().filter(s => s !== t)].slice(0, HISTORY_MAX)
  try { localStorage.setItem(HISTORY_KEY, JSON.stringify(next)) } catch { /* 空间不足忽略 */ }
  return next
}

export default function QueryEditor({
  connId,
  engine,
  defaultSQL = '',
  onResult,
  onWriteLocked,
  onExecuted,
}: {
  connId: string
  engine?: string
  defaultSQL?: string
  onResult?: (r: QueryResult) => void
  onWriteLocked?: (msg: string) => void
  onExecuted?: (sql: string) => void
}) {
  const [sql, setSql] = useState(defaultSQL)
  const [busy, setBusy] = useState(false)
  const [history, setHistory] = useState<string[]>(loadHistory)
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
      setHistory(pushHistory(sql))
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
      // 只允许简单 SELECT 进入编辑模式(无 JOIN/GROUP/UNION 等)
      if (data.columns?.length && data.rows?.length) {
        const clean = sql.toUpperCase().trim()
        data.isEditable = clean.startsWith('SELECT') &&
          !clean.includes(' JOIN ') &&
          !clean.includes(' GROUP BY ') &&
          !clean.includes(' ORDER BY ') &&
          !clean.includes(' UNION ') &&
          !clean.includes(' INTERSECT ') &&
          !clean.includes(' EXCEPT ')
      }
      onResult?.(data)
    } else if (status === 403) {
      if (data.code === 'confirm_required') {
        if (confirm) {
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

  const doFormat = () => setSql(formatSQL(sql, engine))

  const onKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) {
      e.preventDefault()
      run()
      return
    }
    // Tab 缩进
    if (e.key === 'Tab') {
      e.preventDefault()
      const ta = e.currentTarget
      const start = ta.selectionStart, end = ta.selectionEnd
      const next = sql.slice(0, start) + '  ' + sql.slice(end)
      setSql(next)
      requestAnimationFrame(() => { ta.selectionStart = ta.selectionEnd = start + 2 })
    }
  }

  return (
    <div className="db-query-editor">
      <div className="db-query-header">
        <div className="db-query-controls">
          <button onClick={() => run()} disabled={busy} className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-accent">
            {busy ? '执行中...' : '▶ 执行'}
          </button>
          <button onClick={() => run(true)} disabled={busy} className="btn-glass-soft btn-glass-soft-sm" title="高危语句二次确认执行">
            确认执行
          </button>
          <button onClick={doFormat} className="btn-glass-soft btn-glass-soft-sm" title="按当前引擎方言格式化 SQL">格式化</button>
          <button onClick={() => setSql('')} className="btn-glass-soft btn-glass-soft-sm">清空</button>
          {history.length > 0 && (
            <select
              className="input btn-glass-soft-sm"
              style={{ maxWidth: '12rem', fontSize: '0.75rem' }}
              value=""
              onChange={e => { if (e.target.value) setSql(e.target.value) }}
              title="执行历史(本地保留最近 30 条)"
            >
              <option value="">🕘 历史 ({history.length})</option>
              {history.map((h, i) => (
                <option key={i} value={h}>{h.slice(0, 60).replace(/\s+/g, ' ')}{h.length > 60 ? '…' : ''}</option>
              ))}
            </select>
          )}
        </div>
        <div className="db-query-samples">
          {SAMPLE_QUERIES.map((q, i) => (
            <button key={i} onClick={() => setSql(q.sql)} className="btn-glass-soft btn-glass-soft-sm">
              {q.label}
            </button>
          ))}
        </div>
      </div>

      <textarea
        ref={taRef}
        value={sql}
        onChange={(e) => setSql(e.target.value)}
        onKeyDown={onKeyDown}
        className="db-query-textarea"
        placeholder={'输入 SQL 语句...\nCtrl+Enter 执行 · Tab 缩进 · 写操作默认锁定, 需先解锁'}
        spellCheck={false}
      />
    </div>
  )
}

const emptyResult = (): QueryResult => ({
  columns: [],
  rows: [],
  rowCount: 0,
  affected: 0,
  durationMs: 0,
  truncated: false,
})
