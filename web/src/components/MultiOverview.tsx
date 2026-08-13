import { useEffect, useState } from 'react'
import { getJSON } from '../api/client'

interface OverviewHost {
  id: string
  alias: string
  addr: string
  online: boolean
  cpuPercent: number
  memTotal: number
  memUsed: number
  memPercent: number
  diskTotal: number
  diskUsed: number
  diskPercent: number
  netRx: number
  netTx: number
  uptime: number
  hostname: string
  os: string
  alert?: string
}

interface OverviewResp {
  hosts: OverviewHost[]
  updated: number
  message?: string
  alerts?: Record<string, string>
}

const fmtBytes = (b: number) => {
  if (!b) return '0 B'
  const u = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.min(u.length - 1, Math.floor(Math.log(b) / Math.log(1024)))
  return `${(b / Math.pow(1024, i)).toFixed(i ? 1 : 0)} ${u[i]}`
}

const fmtRate = (b: number) => fmtBytes(b) + '/s'

const fmtUptime = (sec: number) => {
  if (!sec) return '—'
  const d = Math.floor(sec / 86400)
  const h = Math.floor((sec % 86400) / 3600)
  if (d > 0) return `${d}d ${h}h`
  return `${h}h`
}

export default function MultiOverview() {
  const [data, setData] = useState<OverviewResp | null>(null)
  const [loading, setLoading] = useState(true)
  const [err, setErr] = useState('')

  useEffect(() => {
    let alive = true
    let timer: ReturnType<typeof setTimeout> | null = null
    const load = () =>
      getJSON<OverviewResp>('/api/core/resources/overview')
        .then((d) => { if (alive) { setData(d); setErr(''); setLoading(false) } })
        .catch((e: any) => { if (alive) { setErr((e as Error)?.message || '采集失败'); setLoading(false) } })
        .finally(() => { if (alive) timer = setTimeout(load, 5000) })
    load()
    return () => { alive = false; if (timer) clearTimeout(timer) }
  }, [])

  if (loading && !data) return <div className="loading">采集多机数据中…</div>
  if (!data || data.hosts.length === 0) return <div className="banner">暂无已管理主机，请先在 Ansible 中添加主机</div>

  return (
    <div>
      {err && <div className="banner banner-warn" style={{ marginBottom: 12 }}>{err}</div>}
      {data.message && <div className="banner banner-warn" style={{ marginBottom: 12 }}>{data.message}</div>}
      {data.alerts && Object.entries(data.alerts).map(([host, msg]) => (
        <div key={host} className="banner banner-warn" style={{ marginBottom: 8 }}>
          ⚠ [{host}] {msg}
        </div>
      ))}
      <div className="table-wrap">
        <table className="data-table multi-table">
          <thead>
            <tr>
              <th>主机</th>
              <th>地址</th>
              <th>状态</th>
              <th>CPU</th>
              <th>内存</th>
              <th>磁盘</th>
              <th>下行</th>
              <th>上行</th>
              <th>运行时间</th>
              <th>系统</th>
            </tr>
          </thead>
          <tbody>
            {data.hosts.map((h) => (
              <tr key={h.id} className={h.online ? '' : 'row-offline'}>
                <td><span className="mono">{h.alias || h.hostname || h.id}</span></td>
                <td className="dim mono">{h.addr}</td>
                <td>
                  <span className={`status-dot ${h.online ? 'online' : 'offline'}`} />
                  {h.online ? '在线' : '离线'}
                  {h.alert && <span className="alert-badge" title={h.alert}>⚠</span>}
                </td>
                <td><CpuBar pct={h.cpuPercent} /></td>
                <td><MemBar total={h.memTotal} used={h.memUsed} pct={h.memPercent} /></td>
                <td><DiskBar total={h.diskTotal} used={h.diskUsed} pct={h.diskPercent} /></td>
                <td className="mono">{h.online ? fmtRate(h.netRx) : '—'}</td>
                <td className="mono">{h.online ? fmtRate(h.netTx) : '—'}</td>
                <td className="mono">{fmtUptime(h.uptime)}</td>
                <td className="dim" style={{ maxWidth: 160, overflow: 'hidden', textOverflow: 'ellipsis' }}>{h.os}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <div className="dim small" style={{ marginTop: 8, textAlign: 'right' }}>
        {new Date(data.updated * 1000).toLocaleTimeString()} 更新 · 每 5 秒自动刷新
      </div>
    </div>
  )
}

function CpuBar({ pct }: { pct: number }) {
  return <Bar value={pct} color={pct > 80 ? 'var(--danger)' : pct > 60 ? 'var(--warn)' : 'var(--accent)'} unit="%" />
}

function MemBar({ total, used, pct }: { total: number; used: number; pct: number }) {
  return (
    <div>
      <Bar value={pct} color={pct > 80 ? 'var(--danger)' : pct > 60 ? 'var(--warn)' : 'var(--ok)'} unit="%" />
      <span className="dim small">{fmtBytes(used)} / {fmtBytes(total)}</span>
    </div>
  )
}

function DiskBar({ total, used, pct }: { total: number; used: number; pct: number }) {
  return (
    <div>
      <Bar value={pct} color={pct > 85 ? 'var(--danger)' : pct > 65 ? 'var(--warn)' : 'var(--ok)'} unit="%" />
      <span className="dim small">{fmtBytes(used)} / {fmtBytes(total)}</span>
    </div>
  )
}

function Bar({ value, color, unit }: { value: number; color: string; unit: string }) {
  const v = Math.min(100, Math.max(0, value))
  return (
    <div className="multi-bar-wrap">
      <div className="multi-bar" style={{ background: 'var(--border)' }}>
        <div className="multi-bar-fill" style={{ width: `${v}%`, background: color }} />
      </div>
      <span className="mono small">{v.toFixed(v < 10 ? 1 : 0)}{unit}</span>
    </div>
  )
}
