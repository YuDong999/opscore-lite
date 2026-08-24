// ── 应用与容器: 容器( docker/podman/containerd ) + Nginx 站点 + 健康状态 + 访问统计 ──
// 只读检测。容器/站点详情用弹层展示, 访问统计按需拉取。

import { useCallback, useEffect, useMemo, useState } from 'react'
import { getJSON } from '../api/client'
import { useHost } from '../components/HostContext'
import Card from '../components/Card'
import EChart from '../charts/EChart'
import { useTheme } from '../theme'

interface AppContainer {
  id: string
  name: string
  image: string
  state: string
  status: string
  runtime: string
  health: string
  healthNote: string
  createdAt?: string
  restartCount?: number
  restartPolicy?: string
  ports?: string[]
  labels?: Record<string, string>
  podSandbox?: boolean
  mounts?: ContainerMount[]
  memoryLimit?: number
  cpuLimit?: number
  env?: string[]
  startedAt?: string
  exitCode?: number
  pid?: number
  networks?: string[]
}

interface ContainerMount {
  type: string
  source: string
  destination: string
  readOnly: boolean
}

interface AppSite {
  id: string
  name: string
  serverNames: string[]
  listens: string[]
  type: string
  root?: string
  proxyPass?: string
  ssl: boolean
  accessLog: string
  configPath?: string
  nginxActive: boolean
  httpCode: number
  health: string
  healthNote: string
  proxyTarget?: string
  proxyCode?: number
  proxyNote?: string
}

interface HealthSummary {
  ok: number
  warn: number
  down: number
}

interface AppsData {
  nginx: { installed: boolean; version: string; active: boolean }
  containers: AppContainer[] | null
  sites: AppSite[]
  summary: HealthSummary
  runtime: string
  note?: string
  errors?: string[]
}

interface AppSiteStats {
  site: string
  win: string
  total: number
  inWindow: number
  topIPs: { ip: string; count: number }[] | null
  status: { code: string; count: number }[] | null
  series: { t: string; c: number }[] | null
  error?: string
}

type AppsTab = 'overview' | 'containers' | 'sites' | 'stats'

export default function AppsSection() {
  const { selected } = useHost()
  const [data, setData] = useState<AppsData | null>(null)
  const [loadErr, setLoadErr] = useState('')
  const [tab, setTab] = useState<AppsTab>('overview')
  const [detailTarget, setDetailTarget] = useState<AppContainer | null>(null)
  const [statsSite, setStatsSite] = useState<AppSite | null>(null)

  const load = useCallback(() => {
    const url = selected?.id ? `/api/core/apps?host=${selected.id}` : '/api/core/apps'
    getJSON<AppsData>(url).then(setData).catch((e) => setLoadErr(e instanceof Error ? e.message : String(e)))
  }, [selected])

  useEffect(() => {
    load()
    const t = setInterval(load, 30000)
    return () => clearInterval(t)
  }, [load])

  const containers = useMemo(() => data?.containers || [], [data])
  const sites = useMemo(() => data?.sites || [], [data])
  const summary = data?.summary

  const openDetail = useCallback(async (c: AppContainer) => {
    setDetailTarget(c)
    try {
      const q = [
        `id=${encodeURIComponent(c.id)}`,
        c.runtime ? `runtime=${encodeURIComponent(c.runtime)}` : '',
        selected?.id ? `host=${encodeURIComponent(selected.id)}` : '',
      ].filter(Boolean).join('&')
      const full = await getJSON<AppContainer>(`/api/core/apps/containers/detail?${q}`)
      setDetailTarget({ ...c, ...full, status: c.status || full.status, createdAt: c.createdAt || full.createdAt })
    } catch {
      setDetailTarget(c)
    }
  }, [selected])

  if (loadErr) return <div className="banner banner-err">加载失败: {loadErr}</div>
  if (!data) return <div className="loading">加载应用与容器…</div>

  return (
    <div>
      <div className="tabs" style={{ flexWrap: 'wrap' }}>
        <button className={`tab ${tab === 'overview' ? 'tab-on' : ''}`} onClick={() => setTab('overview')}>总览</button>
        <button className={`tab ${tab === 'containers' ? 'tab-on' : ''}`} onClick={() => setTab('containers')}>容器 ({containers.length})</button>
        <button className={`tab ${tab === 'sites' ? 'tab-on' : ''}`} onClick={() => setTab('sites')}>站点 ({sites.length})</button>
        <button className={`tab ${tab === 'stats' ? 'tab-on' : ''}`} onClick={() => setTab('stats')}>访问统计</button>
      </div>

      {data.note && <div className="banner banner-info">{data.note}</div>}
      {(data.errors || []).length > 0 && (
        <div className="banner banner-warn">{data.errors!.map((e, i) => <div key={i}>⚠ {e}</div>)}</div>
      )}

      {tab === 'overview' && (
        <OverviewCards
          runtime={data.runtime}
          nginx={data.nginx}
          containers={containers.length}
          sites={sites.length}
          summary={summary}
        />
      )}

      {tab === 'overview' && summary && (summary.warn > 0 || summary.down > 0) && (
        <div className="banner banner-err" style={{ gridColumn: '1 / -1', cursor: 'pointer' }}
             onClick={() => setTab(summary.down > 0 ? (sites.some(x => x.health === 'down') ? 'sites' : 'containers') : 'containers')}>
          异常 {summary.down} · 警告 {summary.warn}
          {(() => {
            const names = [
              ...containers.filter(c => c.health === 'down').map(c => `容器:${c.name}`),
              ...containers.filter(c => c.health === 'warn').map(c => `容器:${c.name}(警告)`),
              ...sites.filter(s => s.health === 'down').map(s => `站点:${s.name}${s.healthNote ? '(' + s.healthNote + ')' : ''}`),
              ...sites.filter(s => s.health === 'warn').map(s => `站点:${s.name}(警告)`),
            ]
            return <div style={{ marginTop: 4 }}>{"点击此处查看 → "}{names.join('、')}</div>
          })()}
        </div>
      )}

      {tab === 'containers' && (
        <Card title="容器列表" subtitle={`运行时: ${data.runtime || '未检测到'} · 点击行查看详情`}>
          <div className="table-wrap">
            <table className="data-table">
              <thead>
                <tr><th>名称</th><th>镜像</th><th>状态</th><th>健康</th><th>端口</th><th>创建时间</th></tr>
              </thead>
              <tbody>
                {containers.length === 0 && (
                  <tr><td colSpan={6} className="dim">（本主机未检测到容器）</td></tr>
                )}
                {containers.map((c) => (
                  <tr key={c.id} onClick={() => openDetail(c)} style={{ cursor: 'pointer' }}>
                    <td><b className="mono">{c.name}</b><div className="dim small">{shortID(c.id)}</div></td>
                    <td className="mono small">{c.image || '—'}</td>
                    <td><span className={`badge ${/running/i.test(c.state) ? 'badge-on' : 'badge-off'}`}>{c.state || '—'}</span></td>
                    <td>{healthBadge(c.health, c.healthNote)}</td>
                    <td className="mono small">{c.ports && c.ports.length > 0 ? c.ports.join(', ') : '—'}</td>
                    <td className="small dim">{c.createdAt || '—'}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </Card>
      )}

      {tab === 'sites' && (
        <Card title="Nginx 站点" subtitle={data.nginx.installed ? `nginx ${data.nginx.version}${data.nginx.active ? ' · 运行中' : ' · 未运行'}` : '未安装 nginx'}>
          <div className="table-wrap">
            <table className="data-table">
              <thead>
                <tr><th>名称</th><th>server_name</th><th>监听端口</th><th>类型</th><th>代理目标</th><th>健康</th><th>访问统计</th></tr>
              </thead>
              <tbody>
                {sites.length === 0 && (
                  <tr><td colSpan={7} className="dim">（未检测到 nginx 站点）</td></tr>
                )}
                {sites.map((s) => (
                  <tr key={s.id}>
                    <td><b className="mono">{s.name}</b></td>
                    <td className="mono small">{(s.serverNames || []).join(', ') || '—'}</td>
                    <td className="mono small">{(s.listens || []).join(', ')}</td>
                    <td><span className="tag">{typeLabel(s.type)}</span></td>
                    <td className="mono small">
                      {s.type === 'proxy' ? (
                        s.proxyTarget ? (
                          <span className="mono small">→ {s.proxyTarget} <span className="dim">({proxyStatus(s)})</span></span>
                        ) : (
                          <span className="mono small dim">{(s.proxyPass || '').replace(/^https?:\/\//, '') || '—'}</span>
                        )
                      ) : (
                        <span className="dim">—</span>
                      )}
                    </td>
                    <td>
                      <span className="mono small">HTTP {s.httpCode || '—'}</span>
                      {' '}
                      {healthBadge(s.health, s.healthNote)}
                    </td>
                    <td>
                      <button className="btn btn-sm btn-log" onClick={() => { setStatsSite(s); setTab('stats') }}>统计</button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </Card>
      )}

      {tab === 'stats' && <SiteStats site={statsSite} sites={sites} onSelect={setStatsSite} />}

      {detailTarget && <ContainerDetail container={detailTarget} onClose={() => setDetailTarget(null)} />}
    </div>
  )
}

// ── 总览 KPI 卡片 ──

function OverviewCards({ runtime, nginx, containers, sites, summary }: {
  runtime: string
  nginx: AppsData['nginx']
  containers: number
  sites: number
  summary?: HealthSummary
}) {
  const items: { label: string; value: string; sub?: string }[] = [
    { label: '容器运行时', value: runtime || '未检测', sub: 'docker / podman / containerd' },
    { label: 'Nginx', value: nginx.installed ? `v${nginx.version}` : '未安装', sub: nginx.active ? '运行中' : '未运行' },
    { label: '容器数', value: String(containers), sub: '含 已停止 容器' },
    { label: '站点数', value: String(sites), sub: 'nginx server 块' },
    {
      label: '健康状态',
      value: summary ? `${summary.ok} / ${summary.warn} / ${summary.down}` : '—',
      sub: '正常 / 警告 / 异常',
    },
  ]
  return (
    <div className="grid grid-5">
      {items.map((it) => (
        <Card key={it.label} title={it.label}>
          <div className="sysinfo">
            <div className="sysinfo-item"><span className="sysinfo-v" style={{ fontSize:'1.375rem', fontWeight: 700 }}>{it.value}</span></div>
            <div className="sysinfo-item"><span className="sysinfo-k">{it.sub}</span></div>
          </div>
        </Card>
      ))}

    </div>
  )
}

// ── 访问统计: 按需拉取 + ECharts 序列 ──

function SiteStats({ site, sites, onSelect }: {
  site: AppSite | null
  sites: AppSite[]
  onSelect: (s: AppSite) => void
}) {
  const { selected } = useHost()
  const [win, setWin] = useState('1h')
  const [stats, setStats] = useState<AppSiteStats | null>(null)
  const [busy, setBusy] = useState(false)
  const { dark } = useTheme()
  const dim = dark ? '#94a8b8' : '#64748b'
  const axis = dark ? 'rgba(255,255,255,0.12)' : 'rgba(15,23,42,0.10)'

  useEffect(() => {
    if (!site) {
      setStats(null)
      return
    }
    setBusy(true)
    const q = [
      site ? `site=${encodeURIComponent(site.name)}` : '',
      site?.accessLog ? `log=${encodeURIComponent(site.accessLog)}` : '',
      `win=${win}`,
      selected?.id ? `host=${selected.id}` : '',
    ].filter(Boolean).join('&')
    getJSON<AppSiteStats>(`/api/core/apps/sites/stats?${q}`)
      .then(setStats)
      .catch((e) => setStats({ site: site.name, win, total: 0, inWindow: 0, topIPs: null, status: null, series: null, error: e instanceof Error ? e.message : String(e) }))
      .finally(() => setBusy(false))
  }, [site, win, selected])

  const seriesOption = useMemo(() => {
    const s = stats?.series || []
    return {
      grid: { left: 48, right: 16, top: 28, bottom: 28 },
      legend: { top: 0, textStyle: { color: dim }, data: ['请求数/分钟'] },
      tooltip: { trigger: 'axis' },
      xAxis: { type: 'category', data: s.map((p) => p.t), axisLabel: { color: dim, fontSize: 10, rotate: 30 }, axisLine: { lineStyle: { color: axis } } },
      yAxis: { type: 'value', axisLabel: { color: dim }, splitLine: { lineStyle: { color: axis } } },
      series: [
        { name: '请求数/分钟', type: 'line', smooth: true, showSymbol: false, data: s.map((p) => p.c), lineStyle: { width: 2, color: '#6366f1' }, areaStyle: { color: 'rgba(99,102,241,0.15)' } },
      ],
    }
  }, [stats, dim, axis])

  return (
    <Card title="站点访问统计" subtitle="按需读取 access.log · 不轮询">
      <div className="trend-controls" style={{ display: 'flex', alignItems: 'center', gap:'0.625rem', flexWrap: 'wrap' }}>
        <select className="sel sel-sm" value={site?.name || ''} onChange={(e) => {
          const s = sites.find((x) => x.name === e.target.value)
          if (s) onSelect(s)
        }}>
          <option value="">选择站点…</option>
          {sites.map((s) => <option key={s.id} value={s.name}>{s.name}（{s.listens.join(',')}）</option>)}
        </select>
        <div className="tabs" style={{ margin: 0 }}>
          {(['1h', '6h', '24h'] as const).map((w) => (
            <button key={w} className={`tab ${win === w ? 'tab-on' : ''}`} onClick={() => setWin(w)}>{w}</button>
          ))}
        </div>
        {busy && <span className="spinner" />}
      </div>

      {!site && <div className="banner banner-info">请选择站点以查看访问统计（TOP IP / 状态码 / 时间序列）</div>}
      {site && stats?.error && <div className="banner banner-err">⚠ {stats.error}</div>}
      {site && stats && !stats.error && (
        <div className="grid grid-2" style={{ marginTop: 8 }}>
          <Card title={`请求时间序列（${win}）`} subtitle={`共 ${stats.total} 条 · 窗口内 ${stats.inWindow} 条`}>
            <EChart option={seriesOption} height={200} />
          </Card>
          <div>
            <Card title="TOP 10 IP" subtitle="访问来源">
              <table className="mini-table">
                <thead><tr><th>IP</th><th>次数</th></tr></thead>
                <tbody>
                  {(stats.topIPs || []).map((r) => (
                    <tr key={r.ip}><td className="mono">{r.ip}</td><td>{r.count}</td></tr>
                  ))}
                  {(!stats.topIPs || stats.topIPs.length === 0) && <tr><td colSpan={2} className="dim">（无数据）</td></tr>}
                </tbody>
              </table>
            </Card>
            <Card title="状态码分布" subtitle="HTTP 响应">
              <table className="mini-table">
                <thead><tr><th>状态码</th><th>次数</th></tr></thead>
                <tbody>
                  {(stats.status || []).map((r) => (
                    <tr key={r.code}><td className="mono">{r.code}</td><td>{r.count}</td></tr>
                  ))}
                  {(!stats.status || stats.status.length === 0) && <tr><td colSpan={2} className="dim">（无数据）</td></tr>}
                </tbody>
              </table>
            </Card>
          </div>
        </div>
      )}
    </Card>
  )
}

// ── 容器详情弹层 ──

function ContainerDetail({ container: c, onClose }: { container: AppContainer; onClose: () => void }) {
  return (
    <div className="modal-mask" onClick={onClose}>
      <div className="modal log-modal" onClick={(e) => e.stopPropagation()} style={{ maxWidth: 720 }}>
        <div className="modal-head">
          <div className="modal-title">
            <b className="mono">{c.name}</b>
            <span className="dim small"> · {shortID(c.id)} · {c.runtime}</span>
          </div>
          <button className="btn btn-sm btn-ghost" onClick={onClose}>✕ 关闭</button>
        </div>

        <div style={{ padding: 16, maxHeight: '70vh', overflow: 'auto' }}>
          <div className="detail-grid">
            <InfoCell k="状态" v={c.state || '—'} />
            <InfoCell k="健康" v={<>{healthBadge(c.health, c.healthNote)}</>} />
            <InfoCell k="镜像" v={c.image || '—'} mono />
            <InfoCell k="重启策略" v={c.restartPolicy || '—'} />
            <InfoCell k="重启次数" v={String(c.restartCount ?? 0)} />
            <InfoCell k="PID" v={c.pid ? String(c.pid) : '—'} />
            <InfoCell k="创建时间" v={c.createdAt || '—'} />
            <InfoCell k="启动时间" v={c.startedAt || '—'} />
            <InfoCell k="退出码" v={c.exitCode !== undefined ? String(c.exitCode) : '—'} />
          </div>

          <Card title="端口" subtitle="暴露端口">
            <div className="chips">{c.ports && c.ports.length ? c.ports.map((p) => <span key={p} className="tag">{p}</span>) : <span className="dim">（无）</span>}</div>
          </Card>

          <Card title="挂载" subtitle="Volume / Bind">
            <table className="mini-table">
              <thead><tr><th>类型</th><th>源</th><th>目标</th><th>只读</th></tr></thead>
              <tbody>
                {(c.mounts || []).map((m, i) => (
                  <tr key={i}>
                    <td className="mono small">{m.type}</td>
                    <td className="mono small">{m.source}</td>
                    <td className="mono small">{m.destination}</td>
                    <td>{m.readOnly ? '是' : '否'}</td>
                  </tr>
                ))}
                {(!c.mounts || c.mounts.length === 0) && <tr><td colSpan={4} className="dim">（无挂载）</td></tr>}
              </tbody>
            </table>
          </Card>

          <Card title="网络" subtitle="网络模式">
            <div className="chips">{(c.networks || []).map((n) => <span key={n} className="tag">{n}</span>)}</div>
          </Card>

          {(c.env && c.env.length > 0) && (
            <Card title="环境变量" subtitle={`${c.env.length} 项`}>
              <div className="env-list">
                {c.env.map((e, i) => <div key={i} className="mono small">{maskEnv(e)}</div>)}
              </div>
            </Card>
          )}

          {(c.labels && Object.keys(c.labels).length > 0) && (
            <Card title="标签" subtitle="Labels">
              <div className="chips">{Object.entries(c.labels).map(([k, v]) => <span key={k} className="tag">{k}={v}</span>)}</div>
            </Card>
          )}
        </div>
      </div>
    </div>
  )
}

function InfoCell({ k, v, mono }: { k: string; v: React.ReactNode; mono?: boolean }) {
  return (
    <div className="sysinfo-item">
      <span className="sysinfo-k">{k}</span>
      <span className={`sysinfo-v ${mono ? 'mono small' : ''}`}>{v}</span>
    </div>
  )
}

// ── 工具 ──

function healthBadge(h?: string, note?: string): React.ReactNode {
  const map: Record<string, { cls: string; label: string }> = {
    ok: { cls: 'badge-ok', label: '正常' },
    warn: { cls: 'badge-warn', label: '警告' },
    down: { cls: 'badge-danger', label: '异常' },
  }
  const m = map[h || ''] || { cls: 'badge-off', label: h || '—' }
  return (
    <span className={`badge ${m.cls}`} title={note}>
      {m.label}
      {note ? <span className="dim" style={{ marginLeft:'0.25rem', fontSize: 11 }}>· {note}</span> : null}
    </span>
  )
}

function typeLabel(t: string): string {
  return { proxy: '反向代理', static: '静态站点', unknown: '未知' }[t] || t
}

function proxyStatus(s: AppSite): string {
  if (s.proxyCode && s.proxyCode > 0) {
    return `HTTP ${s.proxyCode}`
  }
  if (s.proxyNote) {
    return '上游无响应'
  }
  return '—'
}

function shortID(id: string): string {
  return id.length > 12 ? id.slice(0, 12) : id
}

function maskEnv(env: string): string {
  const i = env.indexOf('=')
  if (i < 0) return env
  const k = env.slice(0, i)
  const v = env.slice(i + 1)
  if (/(token|secret|password|key|credential|auth)/i.test(k) && v) {
    return `${k}=******`
  }
  return env
}
