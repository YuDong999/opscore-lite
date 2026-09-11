// 服务器仪表盘(dbx 同款): SHOW GLOBAL STATUS/VARIABLES 两次采样算速率, 指标卡 + 自动刷新 + 状态变量表。
// PG 走 pg_stat_database/pg_stat_activity 简版。纯 SQL 实现, 零后端改动。

import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { runQueryRaw } from './api'

type StatusMap = Record<string, string>

interface Sample {
  at: number
  status: StatusMap
}

const fmtNum = (n: number): string => n.toLocaleString('en-US', { maximumFractionDigits: 1 })
const fmtBytes = (n: number): string => {
  if (n < 1024) return `${n} B`
  if (n < 1048576) return `${(n / 1024).toFixed(1)} KB`
  if (n < 1073741824) return `${(n / 1048576).toFixed(1)} MB`
  if (n < 1073741824 * 1024) return `${(n / 1073741824).toFixed(2)} GB`
  return `${(n / 1073741824 / 1024).toFixed(2)} TB`
}
const fmtRate = (n: number): string => `${fmtNum(n)}/s`
const fmtUptime = (s: number): string => {
  const d = Math.floor(s / 86400)
  const h = Math.floor((s % 86400) / 3600)
  const m = Math.floor((s % 3600) / 60)
  return d > 0 ? `${d}天 ${h}时 ${m}分` : h > 0 ? `${h}时 ${m}分` : `${m}分 ${Math.floor(s % 60)}秒`
}

// 两列结果 → Map: QueryResult.rows 是二维数组(与列名数组配对), 列名大小写兼容
function parseTwoCol(rows: any[][], columns: string[]): StatusMap {
  const out: StatusMap = {}
  const ki = columns.findIndex(c => /variable_name/i.test(c))
  const vi = columns.findIndex(c => /^value$/i.test(c))
  if (ki < 0 || vi < 0) return out
  for (const row of rows) {
    const k = String(row[ki] ?? '').trim()
    if (k) out[k] = String(row[vi] ?? '')
  }
  return out
}

export default function ServerDashboardPanel({ connId, engine }: { connId: string; engine: string }) {
  const isMysql = ['mysql', 'mariadb', 'goldendb'].includes(engine)
  const isPg = ['postgres', 'opengauss', 'kingbase', 'highgo', 'vastbase', 'gaussdb'].includes(engine)
  const [err, setErr] = useState('')
  const [loading, setLoading] = useState(true)
  const [variables, setVariables] = useState<StatusMap>({})
  const [samples, setSamples] = useState<{ prev: Sample | null; curr: Sample | null }>({ prev: null, curr: null })
  const prev = samples.prev
  const curr = samples.curr
  const [auto, setAuto] = useState(true)
  const [statusFilter, setStatusFilter] = useState('')
  const timer = useRef<ReturnType<typeof setInterval> | null>(null)

  const sample = useCallback(async (): Promise<Sample> => {
    if (isMysql) {
      const r = await runQueryRaw(connId, 'SHOW GLOBAL STATUS')
      return { at: Date.now(), status: parseTwoCol(r.data.rows || [], r.data.columns || []) }
    }
    // PG: pg_stat_database 汇总行转 StatusMap
    const r = await runQueryRaw(connId, `SELECT
      sum(numbackends) AS numbackends,
      sum(xact_commit) AS xact_commit,
      sum(xact_rollback) AS xact_rollback,
      sum(blks_read) AS blks_read,
      sum(blks_hit) AS blks_hit,
      sum(tup_inserted) AS tup_inserted,
      sum(tup_updated) AS tup_updated,
      sum(deadlocks) AS deadlocks,
      sum(temp_files) AS temp_files
      FROM pg_stat_database`)
    const row = (r.data.rows || [])[0] || {}
    const status: StatusMap = {}
    for (const [k, v] of Object.entries(row)) status[k] = String(v ?? '0')
    return { at: Date.now(), status }
  }, [connId, isMysql])

  const load = useCallback(async () => {
    try {
      const s = await sample()
      setSamples(({ curr: prevSample }) => ({ prev: prevSample, curr: s }))
      setErr('')
      if (isMysql) {
        const v = await runQueryRaw(connId, 'SHOW GLOBAL VARIABLES')
        setVariables(parseTwoCol(v.data.rows || [], v.data.columns || []))
      }
    } catch (e: any) {
      setErr(e.message || '加载失败')
    } finally {
      setLoading(false)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [connId, sample])

  useEffect(() => {
    load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [connId])

  useEffect(() => {
    if (timer.current) clearInterval(timer.current)
    if (auto) timer.current = setInterval(load, 10000)
    return () => { if (timer.current) clearInterval(timer.current) }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [auto, load])

  // ── 速率计算(两次采样差 / 时间差) ──
  const rate = useCallback((key: string): number | null => {
    if (!prev || !curr) return null
    const dt = (curr.at - prev.at) / 1000
    if (dt <= 0) return null
    const a = Number(prev.status[key] ?? 0)
    const b = Number(curr.status[key] ?? 0)
    if (Number.isNaN(a) || Number.isNaN(b)) return null
    return Math.max(0, (b - a) / dt)
  }, [prev, curr])
  const val = useCallback((key: string): number => Number(curr?.status[key] ?? 0) || 0, [curr])

  const cards = useMemo(() => {
    if (!curr) return []
    if (isMysql) {
      const qps = rate('Queries') ?? rate('Questions')
      const uptime = val('Uptime')
      const reads = val('Innodb_buffer_pool_reads')
      const requests = val('Innodb_buffer_pool_read_requests')
      const hit = requests > 0 ? ((1 - reads / requests) * 100).toFixed(2) + '%' : '—'
      return [
        { label: '运行时长', value: fmtUptime(uptime) },
        { label: '版本', value: variables.version || '—' },
        { label: '连接数', value: fmtNum(val('Threads_connected')) },
        { label: '运行线程', value: fmtNum(val('Threads_running')) },
        { label: 'QPS', value: qps != null ? fmtRate(qps) : '采样中…' },
        { label: 'TPS', value: (() => { const a = rate('Com_commit'), b = rate('Com_rollback'); return a != null && b != null ? fmtRate(a + b) : '采样中…' })() },
        { label: '入流量', value: rate('Bytes_received') != null ? fmtBytes(Math.round(rate('Bytes_received')!)) + '/s' : '采样中…' },
        { label: '出流量', value: rate('Bytes_sent') != null ? fmtBytes(Math.round(rate('Bytes_sent')!)) + '/s' : '采样中…' },
        { label: '慢查询(累计)', value: fmtNum(val('Slow_queries')) },
        { label: 'InnoDB 命中率', value: hit },
      ]
    }
    if (isPg) {
      const tps = (() => { const a = rate('xact_commit'), b = rate('xact_rollback'); return a != null && b != null ? fmtRate(a + b) : '采样中…' })()
      const hr = (() => { const r2 = val('blks_read'), h = val('blks_hit'); return h + r2 > 0 ? ((h / (h + r2)) * 100).toFixed(2) + '%' : '—' })()
      return [
        { label: '连接数', value: fmtNum(val('numbackends')) },
        { label: 'TPS', value: tps },
        { label: '缓存命中率', value: hr },
        { label: '死锁(累计)', value: fmtNum(val('deadlocks')) },
        { label: '临时文件(累计)', value: fmtNum(val('temp_files')) },
        { label: '元组写入(累计)', value: fmtNum(val('tup_inserted')) },
        { label: '元组更新(累计)', value: fmtNum(val('tup_updated')) },
      ]
    }
    return []
  }, [curr, prev, isMysql, isPg, variables, rate, val])

  const statusRows = useMemo(() => {
    if (!isMysql) return []
    const q = statusFilter.trim().toLowerCase()
    return Object.entries(curr?.status || {}).filter(([k]) => !q || k.toLowerCase().includes(q))
  }, [curr, statusFilter, isMysql])

  return (
    <div className="db-dash" style={{ display: 'flex', flexDirection: 'column', gap: 10, height: '100%', minHeight: 0, overflowY: 'auto' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
        <span className="db-engine-badge">服务器仪表盘</span>
        <label style={{ display: 'flex', alignItems: 'center', gap: 4, fontSize: '0.72rem', cursor: 'pointer' }}>
          <input type="checkbox" checked={auto} onChange={e => setAuto(e.target.checked)} /> 自动刷新(10s)
        </label>
        <button className="btn-glass-soft btn-glass-soft-sm" onClick={load} disabled={loading}>{loading ? '刷新中...' : '手动刷新'}</button>
        <span className="dim" style={{ fontSize: '0.6875rem' }}>{curr ? `采样 ${new Date(curr.at).toLocaleTimeString()}` : ''}</span>
      </div>
      {err && <div className="banner banner-err">{err}</div>}

      <div className="db-dash-cards">
        {cards.map(c => (
          <div key={c.label} className="db-dash-card">
            <div className="db-dash-card-label">{c.label}</div>
            <div className="db-dash-card-value" title={c.value}>{c.value}</div>
          </div>
        ))}
      </div>

      {isMysql && (
        <div className="db-dash-status">
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 6 }}>
            <b style={{ fontSize: '0.78rem' }}>状态变量 (SHOW GLOBAL STATUS)</b>
            <input className="input input-sm" style={{ width: '12rem', marginLeft: 'auto' }} placeholder="筛选变量名..."
              value={statusFilter} onChange={e => setStatusFilter(e.target.value)} />
          </div>
          <div className="table-wrap" style={{ maxHeight: '18rem' }}>
            <table className="db-table">
              <thead><tr><th>变量</th><th>值</th></tr></thead>
              <tbody>
                {statusRows.map(([k, v]) => (
                  <tr key={k}><td><code>{k}</code></td><td>{fmtBytes(Number(v) || 0) !== '0 B' && /^Bytes|^Data_|^Innodb_data|^Innodb_written/.test(k) ? `${v} (${fmtBytes(Number(v))})` : v}</td></tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}
    </div>
  )
}
