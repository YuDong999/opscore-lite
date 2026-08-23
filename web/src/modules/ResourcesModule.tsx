import { Fragment, useEffect, useRef, useState } from 'react'
import { getJSON } from '../api/client'
import { useTheme } from '../theme'
import { useHost } from '../components/HostContext'
import HostSelector from '../components/HostSelector'
import MultiOverview from '../components/MultiOverview'
import Card from '../components/Card'
import EChart from '../charts/EChart'

interface DiskChild {
  name: string
  path: string
  size: number
  isDir: boolean
}
interface DiskChildrenResp {
  root: string
  total: number
  used: number
  usedPercent: number
  children: DiskChild[]
  partial: boolean
}

interface DrillState {
  loading: boolean
  error?: string
  data?: DiskChildrenResp
}

interface Snapshot {
  timestamp: number
  host: { hostname: string; os: string; platform: string; uptime: number }
  cpu: { percent: number; perCore: number[]; cores: number; model: string }
  memory: {
    total: number
    used: number
    usedPercent: number
    free: number
    swapTotal: number
    swapUsed: number
    swapPercent: number
  }
  load?: { load1: number; load5: number; load15: number }
  disks: { mountpoint: string; total: number; used: number; usedPercent: number; fstype: string }[]
  net: { byNic: { name: string; rxRate: number; txRate: number; rxTotal: number; txTotal: number }[] }
}

type TrendMetric = 'combined' | 'cpu' | 'mem' | 'swap' | 'net'
type TrendWin = 5 | 15 | 60

const fmtBytes = (b: number) => {
  if (!b) return '0 B'
  const u = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.min(u.length - 1, Math.floor(Math.log(b) / Math.log(1024)))
  return `${(b / Math.pow(1024, i)).toFixed(i ? 1 : 0)} ${u[i]}`
}

const fmtUptime = (sec: number) => {
  if (!sec) return '—'
  const d = Math.floor(sec / 86400)
  const h = Math.floor((sec % 86400) / 3600)
  const m = Math.floor((sec % 3600) / 60)
  if (d > 0) return `${d} 天 ${h} 小时`
  if (h > 0) return `${h} 小时 ${m} 分`
  return `${m} 分`
}

type ColorStop = { pct: number; r: number; g: number; b: number }

const MEM_COLOR_STOPS: ColorStop[] = [
  { pct: 0,   r: 34,  g: 197, b: 94  },
  { pct: 20,  r: 6,   g: 182, b: 212 },
  { pct: 40,  r: 20,  g: 184, b: 166 },
  { pct: 60,  r: 249, g: 115, b: 22  },
  { pct: 80,  r: 245, g: 158, b: 11  },
  { pct: 100, r: 239, g: 68,  b: 68  },
]

function interpolateColor(pct: number): string {
  const clamped = Math.max(0, Math.min(100, pct))
  let lower = MEM_COLOR_STOPS[0]
  let upper = MEM_COLOR_STOPS[MEM_COLOR_STOPS.length - 1]
  for (let i = 0; i < MEM_COLOR_STOPS.length - 1; i++) {
    if (clamped >= MEM_COLOR_STOPS[i].pct && clamped <= MEM_COLOR_STOPS[i + 1].pct) {
      lower = MEM_COLOR_STOPS[i]
      upper = MEM_COLOR_STOPS[i + 1]
      break
    }
  }
  const range = upper.pct - lower.pct
  const t = range === 0 ? 0 : (clamped - lower.pct) / range
  const r = Math.round(lower.r + (upper.r - lower.r) * t)
  const g = Math.round(lower.g + (upper.g - lower.g) * t)
  const b = Math.round(lower.b + (upper.b - lower.b) * t)
  return `rgb(${r}, ${g}, ${b})`
}

const WIN_POINTS: Record<TrendWin, number> = { 5: 150, 15: 450, 60: 1800 }

export default function ResourcesModule() {
  const { dark } = useTheme()
  const { selected } = useHost()
  const [tab, setTab] = useState<'live' | 'multi'>('live')
  const [snap, setSnap] = useState<Snapshot | null>(null)
  const [msg, setMsg] = useState('')
  const [expanded, setExpanded] = useState<string | null>(null)
  const [drill, setDrill] = useState<Record<string, DrillState>>({})
  const [metric, setMetric] = useState<TrendMetric>('combined')
  const [win, setWin] = useState<TrendWin>(5)
  const history = useRef<{ cpu: number[]; mem: number[]; swap: number[]; rx: number[]; tx: number[] }>({
    cpu: [], mem: [], swap: [], rx: [], tx: [],
  })

  const toggleDisk = (mp: string) => {
    if (expanded === mp) {
      setExpanded(null)
      return
    }
    setExpanded(mp)
    if (!drill[mp]) {
      setDrill((d) => ({ ...d, [mp]: { loading: true } }))
      getJSON<DiskChildrenResp>(`/api/core/disk/children?path=${encodeURIComponent(mp)}${selected?.id ? `&host=${selected.id}` : ''}`)
        .then((data) => setDrill((d) => ({ ...d, [mp]: { loading: false, data } })))
        .catch((e) => setDrill((d) => ({ ...d, [mp]: { loading: false, error: String(e) } })))
    }
  }

  useEffect(() => {
    let alive = true
    const load = () => {
      const url = selected?.id ? `/api/core/resources?host=${selected.id}` : '/api/core/resources'
      getJSON<Snapshot>(url)
        .then((s) => {
          if (!alive) return
          if (!s || typeof s.cpu?.percent !== 'number' || typeof s.memory?.usedPercent !== 'number') {
            setMsg((s as any)?.error || '采集系统指标中…')
            return
          }
          setSnap(s)
          setMsg('')
          const h = history.current
          h.cpu.push(s.cpu.percent)
          h.mem.push(s.memory.usedPercent)
          h.swap.push(s.memory.swapPercent)
          const byNic = s.net.byNic || []
          const totalRx = byNic.reduce((a, n) => a + n.rxRate, 0)
          const totalTx = byNic.reduce((a, n) => a + n.txRate, 0)
          h.rx.push(totalRx)
          h.tx.push(totalTx)
          if (h.cpu.length > 1800) {
            h.cpu.shift(); h.mem.shift(); h.swap.shift(); h.rx.shift(); h.tx.shift()
          }
        })
        .catch((e) => setMsg((e as Error)?.message || '⚠ 远程主机不可达'))
    }
    load()
    const t = setInterval(load, 2000)
    return () => { alive = false; clearInterval(t) }
  }, [selected])

  if (!snap) return <div className="loading">采集系统指标中…</div>

  const txt = dark ? '#e2e8f0' : '#0f172a'
  const dim = dark ? '#94a8b8' : '#64748b'
  const axis = dark ? 'rgba(255,255,255,0.12)' : 'rgba(15,23,42,0.10)'

  const memPct = snap.memory.usedPercent
  const memColor = interpolateColor(memPct)
  const memOption = {
    series: [
      {
        type: 'liquidFill',
        radius: '88%',
        center: ['50%', '50%'],
        data: [
          Math.max(0, memPct / 100),
          Math.max(0, memPct / 100 - 0.07),
          Math.max(0, memPct / 100 - 0.14),
        ],
        color: [memColor],
        backgroundStyle: { color: dark ? 'rgba(255,255,255,0.04)' : 'rgba(15,23,42,0.04)' },
        outline: { borderColor: memColor, borderWidth: 2, borderDistance: 4 },
        amplitude: 14,
        waveAnimation: true,
        label: {
          formatter: () => `${memPct.toFixed(2)}%`,
          fontSize: 30,
          fontWeight: 700,
          color: txt,
        },
      },
    ],
  }

  const cpuOption = {
    series: [
      {
        type: 'gauge',
        startAngle: 210,
        endAngle: -30,
        center: ['50%', '50%'],
        min: 0,
        max: 100,
        progress: { show: true, width: 16, itemStyle: { color: '#6366f1' } },
        axisLine: { lineStyle: { width: 16, color: [[1, axis]] } },
        axisTick: { show: false },
        splitLine: { show: false },
        axisLabel: { show: false },
        pointer: { show: false },
        detail: { valueAnimation: true, fontSize: 22, color: txt, offsetCenter: [0, 0], formatter: (value: number) => value.toFixed(2) + '%' },
        data: [{ value: snap.cpu.percent }],
      },
    ],
  }

  const totalUsed = snap.disks.reduce((a, d) => a + d.used, 0)
  const totalFree = snap.disks.reduce((a, d) => a + (d.total - d.used), 0)
  const diskOption = {
    tooltip: { trigger: 'item', formatter: '{b}: {d}%' },
    legend: { bottom: 0, textStyle: { color: dim } },
    series: [
      {
        type: 'pie',
        radius: ['42%', '68%'],
        center: ['50%', '40%'],
        avoidLabelOverlap: true,
        itemStyle: { borderColor: dark ? '#0b1020' : '#fff', borderWidth: 2 },
        label: { color: txt },
        data: [
          { name: '已用', value: totalUsed, itemStyle: { color: '#6366f1' } },
          { name: '空闲', value: totalFree, itemStyle: { color: '#22c55e' } },
        ],
      },
    ],
  }

  const perCore = snap.cpu.perCore || []
  const coreOption = {
    grid: { left: 30, right: 10, top: 10, bottom: 20 },
    xAxis: { type: 'category', data: perCore.map((_, i) => `C${i}`), axisLabel: { color: dim }, axisLine: { lineStyle: { color: axis } } },
    yAxis: { max: 100, axisLabel: { color: dim }, splitLine: { lineStyle: { color: axis } } },
    series: [
      {
        type: 'bar',
        data: perCore.map((v) => ({ value: v, itemStyle: { color: v > 80 ? '#ef4444' : '#06b6d4' } })),
        barWidth: '55%',
        itemStyle: { borderRadius: [4, 4, 0, 0] },
      },
    ],
  }

  const h = history.current
  const winPts = WIN_POINTS[win]
  const tail = (arr: number[]) => arr.slice(Math.max(0, arr.length - winPts))
  const xData = tail(h.cpu).map((_, i) => i)
  let liveOption: any
  if (metric === 'combined') {
    liveOption = {
      grid: { left: 60, right: 70, top: 22, bottom: 22 },
      legend: { top: 0, textStyle: { color: dim }, data: ['CPU%', '下行', '上行'] },
      tooltip: { trigger: 'axis' },
      xAxis: { type: 'category', data: xData, axisLabel: { show: false }, axisLine: { lineStyle: { color: axis } } },
      yAxis: [
        { type: 'value', max: 100, axisLabel: { color: dim }, splitLine: { lineStyle: { color: axis } } },
        { type: 'value', axisLabel: { color: dim, formatter: (v: number) => fmtBytes(v) }, splitLine: { show: false } },
      ],
      series: [
        { name: 'CPU%', type: 'line', smooth: true, showSymbol: false, data: tail(h.cpu), lineStyle: { width: 2, color: '#6366f1' }, areaStyle: { color: 'rgba(99,102,241,0.15)' } },
        { name: '下行', type: 'line', yAxisIndex: 1, smooth: true, showSymbol: false, data: tail(h.rx), lineStyle: { width: 2, color: '#06b6d4' } },
        { name: '上行', type: 'line', yAxisIndex: 1, smooth: true, showSymbol: false, data: tail(h.tx), lineStyle: { width: 2, color: '#f59e0b' } },
      ],
    }
  } else if (metric === 'net') {
    liveOption = {
      grid: { left: 60, right: 12, top: 28, bottom: 22 }, 
      legend: { top: 0, textStyle: { color: dim }, data: ['下行', '上行'] },
      tooltip: { trigger: 'axis' },
      xAxis: { type: 'category', data: xData, axisLabel: { show: false }, axisLine: { lineStyle: { color: axis } } },
      yAxis: { type: 'value', axisLabel: { color: dim, formatter: (v: number) => fmtBytes(v) }, splitLine: { lineStyle: { color: axis } } },
      series: [
        { name: '下行', type: 'line', smooth: true, showSymbol: false, data: tail(h.rx), lineStyle: { width: 2, color: '#06b6d4' }, areaStyle: { color: 'rgba(6,182,212,0.12)' } },
        { name: '上行', type: 'line', smooth: true, showSymbol: false, data: tail(h.tx), lineStyle: { width: 2, color: '#f59e0b' } },
      ],
    }
  } else {
    const m = {
      cpu: { name: 'CPU%', data: h.cpu, color: '#6366f1' },
      mem: { name: '内存%', data: h.mem, color: '#06b6d4' },
      swap: { name: 'Swap%', data: h.swap, color: '#f59e0b' },
    }[metric]
    liveOption = {
      grid: { left: 60, right: 12, top: 28, bottom: 22 }, 
      legend: { top: 0, textStyle: { color: dim }, data: [m.name] },
      tooltip: { trigger: 'axis' },
      xAxis: { type: 'category', data: xData, axisLabel: { show: false }, axisLine: { lineStyle: { color: axis } } },
      yAxis: { type: 'value', max: 100, axisLabel: { color: dim, formatter: '{value}%' }, splitLine: { lineStyle: { color: axis } } },
      series: [
        { name: m.name, type: 'line', smooth: true, showSymbol: false, data: tail(m.data), lineStyle: { width: 2, color: m.color }, areaStyle: { color: m.color + '22' } },
      ],
    }
  }

  const swapSpark = {
    grid: { left: 0, right: 0, top: 4, bottom: 4 },
    xAxis: { type: 'category', show: false, data: h.swap.slice(Math.max(0, h.swap.length - 150)).map((_, i) => i) },
    yAxis: { type: 'value', max: 100, show: false },
    series: [
      { type: 'line', smooth: true, showSymbol: false, data: h.swap.slice(Math.max(0, h.swap.length - 150)), lineStyle: { width: 2, color: '#f59e0b' }, areaStyle: { color: 'rgba(245,158,11,0.15)' } },
    ],
  }

  const byNic = snap.net.byNic || []
  const totalRxRate = byNic.reduce((a, n) => a + n.rxRate, 0)
  const totalTxRate = byNic.reduce((a, n) => a + n.txRate, 0)

return (
    <div className="module">
      <div className="module-head">
        <div className="module-head-row">
          <h2>系统资源</h2>
          <span className="pill">{snap.host.hostname} · {snap.host.platform}</span>
        </div>
        <div className="module-head-row">
          <div className="tabs" style={{ margin: 0 }}>
            <button className={`tab ${tab === 'live' ? 'tab-on' : ''}`} onClick={() => setTab('live')}>实时</button>
            <button className={`tab ${tab === 'multi' ? 'tab-on' : ''}`} onClick={() => setTab('multi')}>大屏</button>
          </div>
          {tab === 'live' && <HostSelector />}
          {tab === 'live' && msg && <span className="host-err">{msg}</span>}
        </div>
      </div>

      {tab === 'multi' ? (
        <MultiOverview />
      ) : (
        <>
          <div className="grid grid-5">
            <Card title="系统信息" subtitle="主机 / 版本 / 规格">
              <div className="sysinfo">
                <div className="sysinfo-item"><span className="sysinfo-k">主机名</span><span className="sysinfo-v">{snap.host.hostname || '—'}</span></div>
                <div className="sysinfo-item"><span className="sysinfo-k">系统</span><span className="sysinfo-v">{snap.host.platform || '—'} {snap.host.os || ''}</span></div>
                <div className="sysinfo-item"><span className="sysinfo-k">运行时长</span><span className="sysinfo-v">{fmtUptime(snap.host.uptime)}</span></div>
                <div className="sysinfo-item"><span className="sysinfo-k">CPU</span><span className="sysinfo-v">{snap.cpu.cores} 核{snap.cpu.model ? ` · ${snap.cpu.model}` : ''}</span></div>
                <div className="sysinfo-item"><span className="sysinfo-k">内存</span><span className="sysinfo-v">{fmtBytes(snap.memory.total)}{snap.memory.swapTotal > 0 ? ` · Swap ${fmtBytes(snap.memory.swapTotal)}` : ''}</span></div>
                <div className="sysinfo-item"><span className="sysinfo-k">磁盘</span><span className="sysinfo-v">{fmtBytes(snap.disks.reduce((a, d) => a + d.total, 0))} · {snap.disks.length} 个挂载点</span></div>
              </div>
            </Card>

            <Card title="内存占用" subtitle="波浪图">
              <EChart option={memOption} height={240} />
              <div className="stat-row">
                <span>{fmtBytes(snap.memory.used)}</span>
                <span className="dim">/ {fmtBytes(snap.memory.total)}</span>
              </div>
            </Card>

            <Card title="CPU 使用率" subtitle="仪表盘">
              <EChart option={cpuOption} height={240} />
              <div className="stat-row" style={{ display: 'block' }}>
                <span style={{ fontSize:'0.8125rem', fontWeight: 700 }}>{snap.cpu.cores} 核</span>{' '}
                <span className="dim" style={{ fontSize: 11.5 }}>{snap.cpu.model || '—'}</span>
              </div>
            </Card>

            <Card title="磁盘空间" subtitle="饼图">
              <EChart option={diskOption} height={240} />
              <div className="stat-row">
                <span>{fmtBytes(totalUsed)}</span>
                <span className="dim">/ {fmtBytes(totalUsed + totalFree)}</span>
              </div>
            </Card>

            <Card title="系统负载" subtitle="load average">
              <div className="load-box">
                <div className="load-item"><b>{snap.load ? snap.load.load1.toFixed(2) : '—'}</b><span>1 min</span></div>
                <div className="load-item"><b>{snap.load ? snap.load.load5.toFixed(2) : '—'}</b><span>5 min</span></div>
                <div className="load-item"><b>{snap.load ? snap.load.load15.toFixed(2) : '—'}</b><span>15 min</span></div>
              </div>
              <div className="stat-row">
                <span>Swap</span>
                <span className="dim">{snap.memory.swapPercent.toFixed(1)}% ({fmtBytes(snap.memory.swapUsed)})</span>
              </div>
              <EChart option={swapSpark} height={56} />
            </Card>
          </div>

          <div className="grid grid-2">
            <Card title="实时趋势" subtitle="默认 CPU+网络 · 可切换指标/时间窗">
              <div className="trend-controls">
                <div className="tabs">
                  <button className={`tab ${metric === 'combined' ? 'tab-on' : ''}`} onClick={() => setMetric('combined')}>综合</button>
                  <button className={`tab ${metric === 'cpu' ? 'tab-on' : ''}`} onClick={() => setMetric('cpu')}>CPU</button>
                  <button className={`tab ${metric === 'mem' ? 'tab-on' : ''}`} onClick={() => setMetric('mem')}>内存</button>
                  <button className={`tab ${metric === 'swap' ? 'tab-on' : ''}`} onClick={() => setMetric('swap')}>Swap</button>
                  <button className={`tab ${metric === 'net' ? 'tab-on' : ''}`} onClick={() => setMetric('net')}>网络</button>
                </div>
                <div className="tabs">
                  <button className={`tab ${win === 5 ? 'tab-on' : ''}`} onClick={() => setWin(5)}>5 分钟</button>
                  <button className={`tab ${win === 15 ? 'tab-on' : ''}`} onClick={() => setWin(15)}>15 分钟</button>
                  <button className={`tab ${win === 60 ? 'tab-on' : ''}`} onClick={() => setWin(60)}>1 小时</button>
                </div>
              </div>
              <EChart option={liveOption} height={190} />
            </Card>
            <Card title="每核占用" subtitle="bar">
              <EChart option={coreOption} height={260} />
            </Card>
          </div>

          <div className="grid grid-2">
            <Card title="网络吞吐" subtitle="当前速率">
              <div className="net-stats">
                <div className="net-stat">
                  <span className="dim">下行</span>
                  <b>{fmtBytes(totalRxRate)}/s</b>
                </div>
                <div className="net-stat">
                  <span className="dim">上行</span>
                  <b>{fmtBytes(totalTxRate)}/s</b>
                </div>
              </div>
              <table className="mini-table">
                <thead><tr><th>网卡</th><th>下行</th><th>上行</th></tr></thead>
                <tbody>
                  {byNic.slice(0, 6).map((n) => (
                    <tr key={n.name}>
                      <td>{n.name}</td>
                      <td>{fmtBytes(n.rxRate)}/s</td>
                      <td>{fmtBytes(n.txRate)}/s</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </Card>

            <Card title="磁盘挂载点" subtitle={`点击展开目录 · ${snap.disks.length} 个`}>
              <table className="mini-table">
                <thead><tr><th>挂载点</th><th>类型</th><th>容量</th><th>已用 / 总</th></tr></thead>
                <tbody>
                  {snap.disks.map((d) => {
                    const open = expanded === d.mountpoint
                    const st = drill[d.mountpoint]
                    const pct = d.total > 0 ? (d.used / d.total) * 100 : d.usedPercent
                    return (
                      <Fragment key={d.mountpoint}>
                        <tr
                          className={`clickable ${open ? 'row-open' : ''}`}
                          onClick={() => toggleDisk(d.mountpoint)}
                        >
                          <td>
                            <span className="caret">{open ? '▾' : '▸'}</span>
                            <span className="mono">{d.mountpoint}</span>
                          </td>
                          <td className="dim">{d.fstype}</td>
                          <td className="mono">{fmtBytes(d.total)}</td>
                          <td>
                            <div className="usage-cell">
                              <div className="usage-bar">
                                <span
                                  className={`usage-fill ${pct > 85 ? 'bg-danger' : pct > 65 ? 'bg-warn' : 'bg-ok'}`}
                                  style={{ width: `${Math.min(100, pct)}%` }}
                                />
                              </div>
                              <span className={`badge ${pct > 85 ? 'badge-danger' : pct > 65 ? 'badge-warn' : 'badge-ok'}`}>
                                 {pct.toFixed(2)}%
                              </span>
                              <span className="dim small"> {fmtBytes(d.used)} / {fmtBytes(d.total)}</span>
                            </div>
                          </td>
                        </tr>
                        {open && (
                          <tr key={d.mountpoint + '::drill'} className="drill-row">
                            <td colSpan={4}>
                              {st?.loading && <div className="loading small">计算目录大小中…(大目录可能需要数秒)</div>}
                              {st?.error && <div className="banner banner-err small">{st.error}</div>}
                              {st?.data && (
                                <div className="drill">
                                  <div className="drill-head dim small">
                                    顶层占用 · 按大小排序{st.data.partial ? ' · 部分目录因超时/权限未扫全' : ''} · Docker 部署下子目录大小为逻辑遍历结果，可能与已用总量存在差异
                                  </div>
                                  <div className="drill-list">
                                    {st.data.children.map((c) => {
                                      const cp = st.data!.total > 0 ? (c.size / st.data!.total) * 100 : 0
                                      return (
                                        <div className="drill-item" key={c.path}>
                                          <span className={`drill-icon ${c.isDir ? 'dir' : 'file'}`}>{c.isDir ? '📁' : '📄'}</span>
                                          <span className="drill-name" title={c.path}>{c.name}</span>
                                          <div className="usage-bar sm">
                                            <span
                                              className={`usage-fill ${cp > 85 ? 'bg-danger' : cp > 65 ? 'bg-warn' : 'bg-ok'}`}
                                              style={{ width: `${Math.min(100, cp)}%` }}
                                            />
                                          </div>
                                           <span className="drill-pct dim small">{cp.toFixed(2)}%</span>
                                          <span className="drill-size mono small">{fmtBytes(c.size)}</span>
                                        </div>
                                      )
                                    })}
                                    {st.data.children.length === 0 && <div className="dim small">无子项或不可访问</div>}
                                  </div>
                                </div>
                              )}
                            </td>
                          </tr>
                        )}
                      </Fragment>
                    )
                  })}
                </tbody>
              </table>
            </Card>
          </div>
        </>
      )}
    </div>
  )
}
