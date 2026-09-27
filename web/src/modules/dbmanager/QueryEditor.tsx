// SQL 编辑器 + 结果表格。
// CodeMirror6: SQL 高亮+表名补全+括号匹配; Ctrl+Enter 执行。
// 工具: 格式化(sql-formatter, 按引擎方言) / 执行历史(本地回填) / 示例。
// 写操作经 ADR-003 拦截链: confirm_required 弹确认重发, write_locked 引导解锁, blocked 直接报错。

import { useEffect, useRef, useState } from 'react'
import CodeMirror from '@uiw/react-codemirror'
import { sql as sqlLang } from '@codemirror/lang-sql'
import { HighlightStyle, syntaxHighlighting } from '@codemirror/language'
import { tags as t } from '@lezer/highlight'
import { runQueryRaw, saveQuery, txBegin, txCommit, txExecute, txRollback, txStatus, type TxStatement, type QueryResult, type InterceptionBody } from './api'
import { useToast } from '../../components/Toast'
import { useConfirm } from '../../lib/hooks/useConfirm'
import { SqlPreviewBody } from '../../components/common/SqlPreview'
import { formatSQL } from './sqlFormat'

const SAMPLE_QUERIES = [
  { label: '当前用户', sql: "SELECT CURRENT_USER() AS user, VERSION() AS version" },
  { label: '前 100 行', sql: 'SELECT * FROM my_table LIMIT 100' },
]

const HISTORY_KEY = 'dbmanager:sql-history'

// 语法高亮：颜色全部走主题 token（--syn-*，每套主题按对比度 ≥4.5:1 挑选）。
// 修复：此前用 CM 默认浅色高亮 + theme="none"，深色主题下对比度只有 1.06–1.32（基本不可见）。
const sqlHighlight = HighlightStyle.define([
  { tag: [t.keyword, t.moduleKeyword, t.controlKeyword, t.operatorKeyword], color: 'var(--syn-keyword)', fontWeight: '600' },
  { tag: [t.number, t.integer, t.float], color: 'var(--syn-number)' },
  { tag: [t.string, t.special(t.string)], color: 'var(--syn-string)' },
  { tag: [t.comment, t.lineComment, t.blockComment], color: 'var(--syn-comment)', fontStyle: 'italic' },
  { tag: [t.operator, t.punctuation, t.bracket, t.separator], color: 'var(--text)' },
  { tag: [t.variableName, t.propertyName, t.definition(t.variableName)], color: 'var(--text)' },
  { tag: [t.typeName, t.function(t.variableName), t.standard(t.variableName)], color: 'var(--syn-keyword)' },
])
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
  db,
  defaultSQL = '',
  onResult,
  onWriteLocked,
  onExecuted,
}: {
  connId: string
  engine?: string
  db?: string
  defaultSQL?: string
  onResult?: (r: QueryResult) => void
  onWriteLocked?: (msg: string) => void
  onExecuted?: (sql: string) => void
}) {
  const toast = useToast()
  const [sql, setSql] = useState(defaultSQL)
  const [busy, setBusy] = useState(false)
  const [history, setHistory] = useState<string[]>(loadHistory)
  // 保存当前 SQL 到已保存查询(补全缺口: 后端 /queries/save + api.saveQuery 一直在, 缺保存入口)
  const [saveOpen, setSaveOpen] = useState(false)
  const [saveName, setSaveName] = useState('')
  const { confirm: askConfirm, confirmEl } = useConfirm()

  // ── 手动事务(事务模式) ──
  // begin 是懒的: 切开关只是打个标记, 第一条语句执行时才真开事务(与 dbx 一致)。
  const [txMode, setTxMode] = useState(false)
  const [txId, setTxId] = useState('')
  const [txStmts, setTxStmts] = useState<TxStatement[]>([])
  const [idleLeft, setIdleLeft] = useState(0)
  const resetTx = () => { setTxId(''); setTxStmts([]); setIdleLeft(0) }

  const syncTx = async (id: string) => {
    const s = await txStatus(id)
    if (!s.active) {
      // 服务端已经回滚掉了(空闲过期): 明确说, 不偷偷重开再跑一次。
      // dbx 是静默重新 begin + 重试, 用户以为"我点了三次执行", 但库上发生过什么他已不知道了。
      resetTx(); setTxMode(false)
      toast.error('事务已不在服务端(空闲过期或已结束), 里面的改动已回滚, 请重新执行')
      return
    }
    setTxStmts((s.statements as TxStatement[]) || [])
    setIdleLeft(s.idleSecLeft || 0)
  }
  useEffect(() => {
    if (!txId) return
    void syncTx(txId)
    const timer = window.setInterval(() => void syncTx(txId), 15000)
    return () => window.clearInterval(timer)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [txId])

  // 倒计时本地逐秒走(状态接口 15 秒才回一次, 中间数字不动会让人以为不会过期)
  useEffect(() => {
    if (!txId) return
    const timer = window.setInterval(() => setIdleLeft(v => (v > 1 ? v - 1 : 0)), 1000)
    return () => window.clearInterval(timer)
  }, [txId])

  // 换连接/换库/关页签: 挂着的事务必须回滚, 不能留一条 idle in transaction 挡住别人
  const txRef = useRef('')
  txRef.current = txId
  useEffect(() => () => { if (txRef.current) void txRollback(txRef.current) }, [connId, db])

  const doCommit = async () => {
    if (!txId) return
    const writes = txStmts.filter(s => s.type !== 'SELECT')
    const ok = await askConfirm(`提交这个事务的 ${txStmts.length} 条语句?`, {
      desc: `提交后不可撤销。其中 ${writes.length} 条是写入, 累计影响 ${txStmts.reduce((a, s) => a + s.affected, 0)} 行。`,
      content: <SqlPreviewBody sqls={txStmts.map(s => s.sql)} caption="事务内已执行的语句" />,
      okText: '提交', danger: writes.length > 0, maxWidth: 680,
    })
    if (!ok) return
    const r = await txCommit(txId)
    if (r.data.ok) {
      toast.success(r.data.noop ? '事务里没有语句, 已结束' : `已提交 ${r.data.count ?? 0} 条语句`)
      resetTx(); setTxMode(false)
      if (!r.data.noop) onExecuted?.('COMMIT')
    } else {
      toast.error(r.data.error || '提交失败')
      if (r.data.code === 'tx_gone') { resetTx(); setTxMode(false) }
    }
  }

  const doRollback = async () => {
    if (!txId) return
    const ok = await askConfirm('回滚整个事务?', {
      desc: `事务内的 ${txStmts.length} 条语句全部作废(尚未提交, 库里本来也看不到)。`,
      okText: '回滚', danger: txStmts.some(s => s.type !== 'SELECT'),
    })
    if (!ok) return
    const r = await txRollback(txId)
    toast.info(`已回滚, 丢弃 ${r.data.discarded ?? 0} 条语句`)
    resetTx(); setTxMode(false)
  }

  const doSave = async () => {
    const name = saveName.trim()
    if (!name) { toast.error('请填写查询名称'); return }
    if (!sql.trim()) { toast.error('当前 SQL 为空'); return }
    try {
      await saveQuery({ name, sql, engine })
      toast.success(`已保存查询「${name}」`)
      setSaveOpen(false)
      setSaveName('')
    } catch (e: any) {
      toast.error('保存失败: ' + (e.message || e))
    }
  }

  useEffect(() => {
    if (defaultSQL) setSql(defaultSQL)
  }, [defaultSQL])

  // 在事务里跑一条。返回响应体给上层判断 confirm_required / tx_gone。
  const runInTx = async (id: string, confirmed: boolean) => {
    const { status, data } = await txExecute(id, sql, confirmed)
    if (status === 200 && data.ok) {
      setTxStmts((data.statements as TxStatement[]) || [])
      setIdleLeft(data.idleSecLeft || 0)
      onResult?.({
        ...emptyResult(),
        columns: data.columns || [], rows: data.rows || [], rowCount: data.rowCount || 0,
        truncated: !!data.truncated, statements: data.batch,
      } as unknown as QueryResult)
      if (data.note) toast.info(data.note)
      return data
    }
    if (data.code === 'tx_gone' || data.rolledBack) {
      // 一条失败 = 整笔回滚(服务端已做), 这里同步把界面收干净
      resetTx(); setTxMode(false)
    }
    onResult?.(asErrorResult(data, status))
    return data
  }

  const run = async (confirmed = false) => {
    if (!connId) return
    if (!sql.trim()) return
    setBusy(true)
    try {
      if (txMode) {
        let id = txId
        if (!id) {
          const b = await txBegin(connId, db || '')
          if (!b.data.ok || !b.data.txId) {
            toast.error(b.data.error || '开启事务失败')
            onResult?.(asErrorResult(b.data, b.status))
            return
          }
          id = b.data.txId
          setTxId(id)
          setIdleLeft(b.data.idleTimeoutSec || 300)
        }
        setHistory(pushHistory(sql))
        onExecuted?.(sql)
        const data = await runInTx(id, confirmed)
        if (data.code === 'confirm_required') {
          const ok = await askConfirm('这条语句风险较高, 确认在事务内执行?', {
            desc: data.reason || data.error || '后端要求二次确认后才执行。',
            content: <SqlPreviewBody sqls={[sql]} caption="将在事务内执行的语句" />,
            okText: '确认执行', danger: true, maxWidth: 640,
          })
          if (ok) await runInTx(id, true)
        }
        return
      }
      const { status, data } = await runQueryRaw(connId, sql, 5000, confirmed, db)
      setHistory(pushHistory(sql))
      onExecuted?.(sql)
      await handleResponse(status, data as QueryResult & InterceptionBody, confirmed)
    } catch (e: any) {
      onResult?.({ ...emptyResult(), error: e.message || '执行失败' })
    } finally {
      setBusy(false)
    }
  }

  // 非 200 的响应体(拦截/报错)没有 columns/rows —— 直接丢给网格会让它去 join(null) 整模块崩。
  // 统一裹成"有形状的错误结果"(保留 code/reason 供拦截链判断)。
  const asErrorResult = (data: any, status: number): QueryResult & InterceptionBody => ({
    ...emptyResult(),
    error: data?.error || data?.reason || `执行失败 (HTTP ${status})`,
    code: data?.code,
    risk: data?.risk,
    reason: data?.reason,
  })

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
          onResult?.(asErrorResult(data, status))
        }
      } else if (data.code === 'write_locked') {
        onWriteLocked?.([data.error, data.reason].filter(Boolean).join(' —— ') || '写操作被拦截: 连接默认只读')
        onResult?.(asErrorResult(data, status))
      } else {
        onResult?.(asErrorResult(data, status))
      }
    } else {
      onResult?.(asErrorResult(data, status))
    }
  }

  const doFormat = () => setSql(formatSQL(sql, engine))

  const onKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) {
      e.preventDefault()
      run()
      return
    }
  }

  return (
    <div className="db-query-editor">
      <div className="db-query-header">
        <div className="db-query-controls">
          <button onClick={() => run()} disabled={busy} className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-accent">
            {busy ? '执行中...' : (
              <>
                <svg width="11" height="11" viewBox="0 0 24 24" fill="currentColor" style={{ verticalAlign: -1 }}><path d="M8 5v14l11-7z" /></svg>执行
              </>
            )}
          </button>
          <button onClick={() => run(true)} disabled={busy} className="btn-glass-soft btn-glass-soft-sm" title="高危语句二次确认执行">
            确认执行
          </button>
          {/* 事务模式开关: 切过去不立刻 begin, 第一条语句才开事务(与 dbx 同口径) */}
          <button
            onClick={() => {
              if (txId) { void doRollback(); return }
              setTxMode(v => !v)
              toast.info(txMode ? '已退出事务模式' : '事务模式已开启: 第一条语句执行时才 BEGIN, 结束前必须提交或回滚')
            }}
            disabled={busy}
            className={`btn-glass-soft btn-glass-soft-sm ${txMode ? 'btn-glass-soft-accent' : ''}`}
            title="手动事务: 语句先进事务, 由你决定提交还是回滚(MySQL 系 DDL 不允许进事务)"
          >
            {txMode ? '事务中' : '自动提交'}
          </button>
          {txId && (
            <>
              <button onClick={doCommit} disabled={busy || !txStmts.length} className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-accent"
                title="提交后不可撤销">提交({txStmts.length})</button>
              <button onClick={doRollback} disabled={busy} className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-danger"
                title="丢弃事务内的全部语句">回滚</button>
            </>
          )}
          <button onClick={doFormat} className="btn-glass-soft btn-glass-soft-sm" title="按当前引擎方言格式化 SQL">格式化</button>
          <button onClick={() => setSql('')} className="btn-glass-soft btn-glass-soft-sm">清空</button>
          <button onClick={() => setSaveOpen(v => !v)} disabled={busy || !sql.trim()} className="btn-glass-soft btn-glass-soft-sm" title="保存当前 SQL 到已保存查询" aria-label="保存当前 SQL">保存</button>
          {saveOpen && (
            <span className="db-save-inline">
              <input
                className="input btn-glass-soft-sm"
                style={{ maxWidth: '11rem', fontSize: '0.75rem' }}
                placeholder="查询名称..."
                value={saveName}
                autoFocus
                aria-label="查询名称"
                onChange={e => setSaveName(e.target.value)}
                onKeyDown={e => { if (e.key === 'Enter') doSave(); if (e.key === 'Escape') setSaveOpen(false) }}
              />
              <button onClick={doSave} className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-accent">确定</button>
            </span>
          )}
          {history.length > 0 && (
            <select
              className="input btn-glass-soft-sm"
              style={{ maxWidth: '12rem', fontSize: '0.75rem' }}
              value=""
              onChange={e => { if (e.target.value) setSql(e.target.value) }}
              title="执行历史(本地保留最近 30 条)"
            >
              <option value="">历史 ({history.length})</option>
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

      {(txMode || txId) && (
        <div className="db-tx-bar">
          {txId ? (
            <>
              <span className="db-tx-strong">未提交的事务</span>
              <span>{txStmts.length} 条语句</span>
              <span>累计影响 {txStmts.reduce((a, s) => a + s.affected, 0)} 行</span>
              <span title="超过这个时间没有操作, 服务端会自动回滚">空闲剩 {idleLeft}s</span>
              <span>点「提交」生效, 点「回滚」作废</span>
            </>
          ) : (
            <span>事务模式: 执行第一条语句时才开启事务</span>
          )}
          {/mysql|maria|tidb|oceanbase/i.test(engine || '') && (
            <span className="db-tx-warn">改表结构的语句不能放进事务(会直接报错)</span>
          )}
        </div>
      )}

      <div className="db-query-cm" onKeyDown={onKeyDown}>
        <CodeMirror
          value={sql}
          height="220px"
          theme="none"
          extensions={[sqlLang(), syntaxHighlighting(sqlHighlight)]}
          basicSetup={{
            lineNumbers: true,
            highlightActiveLine: true,
            autocompletion: true,
            bracketMatching: true,
            closeBrackets: true,
          }}
          onChange={setSql}
          placeholder="输入 SQL 语句... (Ctrl+Enter 执行)"
          spellCheck={false}
        />
      </div>
      {confirmEl}
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
