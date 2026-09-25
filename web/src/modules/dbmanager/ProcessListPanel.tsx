// 进程/会话列表(dbx 同款): MySQL 走 information_schema.PROCESSLIST, PG 走 pg_stat_activity。
// 列表是纯 SQL(零后端); 「终止」是写操作 —— 前端只报"连接 + pid", 语句由后端 /kill-session 生成
// (与 apply-* 同一规矩), 并复用公共确认弹窗做二次确认(可看 SQL、可取消)。

import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { killSession, runQueryRaw } from './api'
import { useToast } from '../../components/Toast'
import { useConfirm } from '../../lib/hooks/useConfirm'
import { SqlPreviewBody } from '../../components/common/SqlPreview'

const MYSQL_ENGINES = ['mysql', 'mysql_agent', 'mariadb', 'goldendb']
const PG_ENGINES = ['postgres', 'opengauss', 'kingbase', 'highgo', 'vastbase', 'gaussdb']

// 长的慢会话阈值(秒): 越线才染色, 平时安静 —— 与隐形容量那套阈值思路一致
const WARN_SECS = 60
const DANGER_SECS = 600

function listSQL(engine: string): string | null {
  if (MYSQL_ENGINES.includes(engine)) {
    return 'SELECT ID, USER, HOST, DB, COMMAND, TIME, STATE, INFO FROM information_schema.PROCESSLIST WHERE ID <> CONNECTION_ID() ORDER BY TIME DESC'
  }
  if (PG_ENGINES.includes(engine)) {
    return "SELECT pid, usename, datname, client_addr, state, wait_event_type || coalesce(':' || wait_event, '') AS wait, round(extract(epoch FROM (now() - query_start))) AS secs, query FROM pg_stat_activity WHERE pid <> pg_backend_pid() ORDER BY query_start NULLS LAST"
  }
  return null
}

export default function ProcessListPanel({ connId, engine, database }: { connId: string; engine: string; database?: string }) {
  const sql = useMemo(() => listSQL(engine), [engine])
  const [columns, setColumns] = useState<string[]>([])
  const [rows, setRows] = useState<any[][]>([])
  const [err, setErr] = useState('')
  const [loading, setLoading] = useState(false)
  const [auto, setAuto] = useState(true)
  const [filter, setFilter] = useState('')
  const [killing, setKilling] = useState<number | null>(null)
  const timer = useRef<ReturnType<typeof setInterval> | null>(null)
  const toast = useToast()
  const { confirm, confirmEl } = useConfirm()

  const pidIdx = useMemo(() => columns.findIndex(c => /^(id|pid)$/i.test(c)), [columns])
  const timeIdx = useMemo(() => columns.findIndex(c => /^(time|secs)$/i.test(c)), [columns])
  const sqlIdx = useMemo(() => columns.findIndex(c => /^(info|query)$/i.test(c)), [columns])
  const cmdIdx = useMemo(() => columns.findIndex(c => /^command$/i.test(c)), [columns])
  const stateIdx = useMemo(() => columns.findIndex(c => /^state$/i.test(c)), [columns])

  // 后台/系统进程不给终止入口 —— 按引擎判, 别看列在不在:
  //   PG: 辅助进程(checkpointer/bgwriter 等) state 为空;
  //   MySQL: event_scheduler 的 COMMAND 是 Daemon(Sleep 会话的 STATE 也是空, 不能当判据)。
  const isSystemRow = (r: any[]) => {
    if (PG_ENGINES.includes(engine)) return !String(r[stateIdx] ?? '').trim()
    if (MYSQL_ENGINES.includes(engine)) return /^(daemon|binlog dump)$/i.test(String(r[cmdIdx] ?? '').trim())
    return false
  }

  const reload = useCallback(async () => {
    if (!connId || !sql) return
    setLoading(true)
    try {
      const { status, data } = await runQueryRaw(connId, sql, 500, false, database)
      if (status >= 400 || (data as any).error) {
        setErr((data as any).error || `查询失败 (HTTP ${status})`)
        setColumns([]); setRows([])
        return
      }
      setErr('')
      setColumns(data.columns || [])
      setRows(data.rows || [])
    } catch (e: any) {
      setErr(String(e?.message || e))
    } finally {
      setLoading(false)
    }
  }, [connId, sql, database])

  useEffect(() => { reload() }, [reload])
  useEffect(() => {
    if (timer.current) clearInterval(timer.current)
    if (auto) timer.current = setInterval(reload, 5000)
    return () => { if (timer.current) clearInterval(timer.current) }
  }, [auto, reload])

  const shown = useMemo(() => {
    const q = filter.trim().toLowerCase()
    if (!q) return rows
    return rows.filter(r => r.some(c => String(c ?? '').toLowerCase().includes(q)))
  }, [rows, filter])

  const kill = async (pid: number) => {
    setKilling(pid)
    try {
      // 第一刀拿后端生成的语句做预览, 确认后再真执行
      const preview = await killSession(connId, pid, false)
      if (!preview.ok) { toast.error(preview.error || '无法终止该会话'); return }
      if (!(await confirm('将终止该会话', {
        content: <SqlPreviewBody sqls={[preview.sql || '']} caption="终止会话" />,
        okText: '终止', danger: true, maxWidth: 560,
      }))) return
      const r = await killSession(connId, pid, true)
      if (!r.ok) { toast.error('终止失败: ' + (r.error || '')); return }
      toast.success(`已终止会话 ${pid}`)
      reload()
    } catch (e: any) {
      toast.error('终止失败: ' + (e?.message || e))
    } finally {
      setKilling(null)
    }
  }

  if (!sql) return <div className="db-empty">该引擎暂不支持进程列表（当前支持 MySQL 系与 PostgreSQL 系）</div>

  return (
    <div className="db-slow">
      <div className="db-slow-toolbar">
        <span className="dim">{shown.length} 个会话{rows.length !== shown.length ? ` / 共 ${rows.length}` : ''}</span>
        <input className="input log-input" style={{ maxWidth: 220 }} placeholder="过滤(用户/库/SQL)" value={filter}
          onChange={e => setFilter(e.target.value)} aria-label="过滤会话" />
        <label className="dim" style={{ display: 'inline-flex', alignItems: 'center', gap: 4, fontSize: '0.75rem' }}>
          <input type="checkbox" checked={auto} onChange={e => setAuto(e.target.checked)} /> 自动刷新(5s)
        </label>
        <button className="btn-glass-soft btn-glass-soft-sm" onClick={reload} disabled={loading}>
          {loading ? '刷新中...' : '刷新'}
        </button>
      </div>
      {err && <div className="banner banner-err small">{err}</div>}
      <div className="table-wrap">
        <table className="db-table">
          <thead>
            <tr>
              {columns.map(c => <th key={c}>{c}</th>)}
              <th style={{ width: 68 }}>操作</th>
            </tr>
          </thead>
          <tbody>
            {shown.length === 0 ? (
              <tr><td colSpan={columns.length + 1} className="dim" style={{ textAlign: 'center', padding: 12 }}>
                {loading ? '读取中…' : '没有其它会话'}
              </td></tr>
            ) : shown.map((r, i) => {
              const secs = timeIdx >= 0 ? Number(r[timeIdx]) : NaN
              const long = Number.isFinite(secs) && secs > WARN_SECS
              return (
                <tr key={i}>
                  {columns.map((c, j) => {
                    const v = r[j]
                    if (j === timeIdx && long) {
                      return <td key={c}>
                        <span className={`badge ${secs > DANGER_SECS ? 'badge-danger' : 'badge-warn'}`}>{String(v)}s</span>
                      </td>
                    }
                    if (j === sqlIdx && v != null) {
                      const s = String(v).replace(/\s+/g, ' ')
                      return <td key={c} title={String(v)}><span className="mono small">{s.length > 120 ? s.slice(0, 120) + '…' : s}</span></td>
                    }
                    return <td key={c}>{v === null || v === undefined ? <span className="dim">NULL</span> : String(v)}</td>
                  })}
                  <td>
                    {pidIdx >= 0 && (isSystemRow(r)
                      ? <span className="dim small" title="后台/系统进程, 不需要终止">—</span>
                      : <button className="btn-glass-soft btn-glass-soft-sm" disabled={killing === Number(r[pidIdx])}
                          title="终止该会话" onClick={() => kill(Number(r[pidIdx]))}>终止</button>)}
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>
      {confirmEl}
    </div>
  )
}
