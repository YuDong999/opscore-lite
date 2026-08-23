// ── 网络模块: 接口列表 / 监听端口 / 防火墙 / 网络配置 ──

import { useEffect, useState } from 'react'
import { getJSON, postJSON } from '../api/client'
import { useHost } from '../components/HostContext'
import HostSelector from '../components/HostSelector'
import Card from '../components/Card'
import FirewallModule from './FirewallModule'
import TopologyPanel from './TopologyPanel'

interface NetIface {
  name: string; mtu: number; flags: string[]; addrs: string[]; rxBytes?: number; txBytes?: number
}
interface NetData {
  interfaces: NetIface[]
  ifaceError?: string
  listenError?: string
  listeners: {
    protocol: string
    local: string
    port: number
    pid: number
    process: string
    service: string
    category: string
    icon: string
    knownAs: string
    verified: boolean
  }[]
}

type NetTab = 'topology' | 'network' | 'firewall' | 'config' | 'nmcli' | 'lldp'

export default function NetworkModule() {
  const { selected } = useHost()
  const [data, setData] = useState<NetData | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [tab, setTab] = useState<NetTab>('firewall')
  const [ifaceFilter, setIfaceFilter] = useState('')
  const [showAllListeners, setShowAllListeners] = useState(false)

  useEffect(() => {
    if (tab === 'firewall' || tab === 'topology') return
    const url = selected?.id ? `/api/core/network?host=${selected.id}` : '/api/core/network'
    const load = () =>
      getJSON<NetData>(url)
        .then((d) => {
          setData(d)
          setError(null)
        })
        .catch((err) => {
          setError(err instanceof Error ? err.message : String(err))
        })
    load()
    const t = setInterval(load, 5000)
    return () => clearInterval(t)
  }, [tab, selected])

  return (
    <div className="module">
      <div className="module-head">
        <div className="module-head-row"><h2>防火墙和网络</h2><span className="pill">网络 · 防火墙</span></div>
        <div className="module-head-row"><HostSelector /></div>
      </div>

      <div className="tabs" style={{flexWrap:'wrap'}}>
        <button className={`tab ${tab === 'topology' ? 'tab-on' : ''}`} onClick={() => setTab('topology')}>拓扑</button>
        <button className={`tab ${tab === 'network' ? 'tab-on' : ''}`} onClick={() => setTab('network')}>网络</button>
        <button className={`tab ${tab === 'firewall' ? 'tab-on' : ''}`} onClick={() => setTab('firewall')}>防火墙</button>
        <button className={`tab ${tab === 'config' ? 'tab-on' : ''}`} onClick={() => setTab('config')}>网络配置</button>
        <button className={`tab ${tab === 'nmcli' ? 'tab-on' : ''}`} onClick={() => setTab('nmcli')}>NM 连接</button>
        <button className={`tab ${tab === 'lldp' ? 'tab-on' : ''}`} onClick={() => setTab('lldp')}>LLDP</button>
      </div>

      {tab === 'firewall' && <FirewallModule />}
      {tab === 'topology' && <TopologyPanel />}

      {tab === 'config' && <NetConfigSection />}
      {tab === 'nmcli' && <NmcliSection />}
      {tab === 'lldp' && <LldpSection />}

      {tab === 'network' &&
        (error ? (
          <div className="banner banner-error">请求失败: {error}</div>
        ) : data ? (
          <div className="grid grid-2">
            {(data.ifaceError || data.listenError) && (
              <div className="banner banner-warn small" style={{ gridColumn: '1 / -1' }}>
                后端采集出现错误(已尽量返回其余数据):
                {data.ifaceError && <div>· 网络接口: {data.ifaceError}</div>}
                {data.listenError && <div>· 监听端口: {data.listenError}</div>}
              </div>
            )}
            <Card title="网络接口" subtitle="interface / MTU / 流量 / 地址">
              <input className="input" placeholder="搜索接口..." value={ifaceFilter}
                onChange={e => setIfaceFilter(e.target.value)} style={{marginBottom:'0.5rem',fontSize:13}} />
              <div className="table-wrap">
                <table className="data-table">
                  <thead>
                    <tr><th>接口</th><th>MTU</th><th>↓ 接收</th><th>↑ 发送</th><th>地址</th></tr>
                  </thead>
                  <tbody>
                    {data.interfaces.filter(i => !ifaceFilter || i.name.includes(ifaceFilter) || (i.addrs ?? []).some(a => a.includes(ifaceFilter))).map((i) => {
                      const rx = i.rxBytes ? (i.rxBytes / 1024 / 1024).toFixed(1) : '—'
                      const tx = i.txBytes ? (i.txBytes / 1024 / 1024).toFixed(1) : '—'
                      return (
                        <tr key={i.name}>
                          <td className="mono">{i.name}</td>
                          <td className="dim">{i.mtu}</td>
                          <td className="mono small" style={{color:'var(--ok)'}}>{rx} MB</td>
                          <td className="mono small" style={{color:'var(--accent)'}}>{tx} MB</td>
                          <td className="mono small">{(i.addrs ?? []).join(' , ') || '—'}</td>
                        </tr>
                      )
                    })}
                  </tbody>
                </table>
              </div>
            </Card>

            <Card title="监听端口" subtitle="身份以真实进程为准 · 非端口假设">
              <div className="banner banner-info small">
                端口常见服务仅作提示；真实身份 = 占用该端口的进程(PID→进程名)。二者一致才标「已确认」。
              </div>
              <div className="table-wrap">
                <table className="data-table net-port-table">
                  <thead>
                    <tr><th>协议</th><th>本地地址</th><th>识别服务</th><th>真实进程 / PID</th><th>端口提示</th></tr>
                  </thead>
                  <tbody>
                    {(showAllListeners ? data.listeners : data.listeners.slice(0, 40)).map((l, idx) => (
                      <tr key={idx}>
                        <td><span className="badge badge-ok">{l.protocol}</span></td>
                        <td className="mono">{l.local}</td>
                        <td>
                          {l.service
                            ? <span className="svc-badge">{l.icon} {l.service}{l.verified && <span className="verified" title="端口提示与进程身份一致">✓</span>}</span>
                            : <span className="dim">未知</span>}
                        </td>
                        <td className="mono small">
                          {l.process || '—'}
                          <span className="dim"> · PID {l.pid || '?'}</span>
                        </td>
                        <td className="dim small">
                          {l.knownAs
                            ? (l.verified ? l.knownAs : `${l.knownAs}(进程不符)`)
                            : '—'}
                        </td>
                      </tr>
                    ))}
                    {data.listeners.length === 0 && (
                      <tr><td colSpan={5} className="dim">无监听端口或权限不足</td></tr>
                    )}
                  </tbody>
                </table>
              </div>
              {data.listeners.length > 40 && (
                <button className="btn btn-sm" style={{marginTop:8}} onClick={() => setShowAllListeners(!showAllListeners)}>
                  {showAllListeners ? '收起' : `显示全部 (${data.listeners.length})`}
                </button>
              )}
            </Card>
          </div>
        ) : (
          <div className="loading">加载网络信息中…</div>
        )      )}
    </div>
  )
}

type NetConfig = {
  interfaces: string
  routes: string
  dns: string
  nm: string
  permission: 'root' | 'user'
}

type ConfigResult = {
  ok?: boolean
  error?: string
  note?: string
  output?: string
  permission: 'root' | 'user'
}

// ── LLDP 子组件 ──
function LldpSection() {
  const [data, setData] = useState<{ installed: boolean; running: boolean; neighbors: any[] } | null>(null)
  const [msg, setMsg] = useState('')
  const load = () => getJSON('/api/core/lldp').then(setData).catch(() => {})
  useEffect(() => { load(); const t = setInterval(load, 10000); return () => clearInterval(t) }, [])
  const act = async (action: string) => {
    try { const r = await postJSON<any>('/api/core/lldp', { action }); setMsg(r.ok ? '✓ '+action : '✗ '+r.error) } catch { setMsg('✗ 请求失败') }
    setTimeout(() => setMsg(''), 5000)
    load()
  }
  return (
    <>
      {msg && <div className={`banner ${msg.startsWith('✓') ? 'banner-ok' : 'banner-err'}`}>{msg}</div>}
      <Card title="LLDP 邻居发现" subtitle="lldpd / lldpctl">
        <div className="status-row">
          <span className={`badge ${data?.installed ? 'badge-ok' : 'badge-off'}`}>{data?.installed ? 'lldpd 已安装' : '未安装'}</span>
          <span className={`badge ${data?.running ? 'badge-ok' : 'badge-danger'}`}>{data?.running ? '运行中' : '已停止'}</span>
          {(!data?.installed) && <button className="btn btn-sm btn-accent" onClick={() => act('install')}>安装 lldpd</button>}
          {data?.installed && <>
            <button className="btn btn-sm" onClick={() => act('start')}>启动</button>
            <button className="btn btn-sm btn-danger" onClick={() => act('stop')}>停止</button>
            <button className="btn btn-sm" onClick={() => act('restart')}>重启</button>
          </>}
        </div>
      </Card>
      <Card title="LLDP 邻居" subtitle="直连交换机/路由器信息">
        <div className="table-wrap">
          <table className="data-table">
            <thead><tr><th>本机接口</th><th>对端设备</th><th>Chassis ID</th><th>端口 ID</th><th>VLAN</th><th>描述</th></tr></thead>
            <tbody>
              {(data?.neighbors ?? []).map((n, i) => (
                <tr key={i}>
                  <td className="mono small">{n.interface}</td>
                  <td><span className="badge badge-info">{n.sysName || '?'}</span></td>
                  <td className="mono small">{n.chassisId || '—'}</td>
                  <td className="mono small">{n.portId || '—'}</td>
                  <td className="mono small">{n.vlan || '—'}</td>
                  <td className="dim small">{n.sysDesc || '—'}</td>
                </tr>
              ))}
              {(!data?.neighbors || data.neighbors.length === 0) && <tr><td colSpan={6} className="dim">未发现 LLDP 邻居或 lldpd 未运行</td></tr>}
            </tbody>
          </table>
        </div>
      </Card>
    </>
  )
}

// ── NMCLI 子组件 ──
function NmcliSection() {
  const { selected } = useHost()
  const nc = selected?.id ? `?host=${selected.id}` : ''
  const [data, setData] = useState<{ connections: string; wifi: string } | null>(null)
  const [msg, setMsg] = useState('')
  const [actName, setActName] = useState('')
  const [ssid, setSsid] = useState('')
  const [psk, setPsk] = useState('')
  const [wifiDev, setWifiDev] = useState('')
  const [showWifi, setShowWifi] = useState(false)
  const load = () => getJSON('/api/core/netconfig/connections'+nc).then(setData).catch(() => {})
  useEffect(() => { load() }, [selected])
  const act = async (action: string, extra?: Record<string, any>) => {
    try {
      const body: any = { action, name: actName, ssid, psk, device: wifiDev, ...extra }
      if (selected?.id) body.host = selected.id
      const r = await postJSON<any>('/api/core/netconfig/connection', body)
      setMsg(r.ok ? '✓ '+action : '✗ '+(r.error||'失败'))
    } catch { setMsg('✗ 请求失败') }
    setTimeout(() => setMsg(''), 5000)
    load()
  }
  return (
    <>
      {msg && <div className={`banner ${msg.startsWith('✓') ? 'banner-ok' : 'banner-err'}`}>{msg}</div>}
      <Card title="NetworkManager 连接" subtitle="nmcli con show">
        <div className="status-row">
          <input className="input" placeholder="连接名" value={actName} onChange={e => setActName(e.target.value)} style={{width:180}} />
          <button className="btn btn-sm btn-accent" disabled={!actName} onClick={() => act('up')}>启用</button>
          <button className="btn btn-sm" disabled={!actName} onClick={() => act('down')}>停用</button>
          <button className="btn btn-sm btn-danger" disabled={!actName} onClick={() => { if (confirm('确认删除连接 '+actName+'？')) act('delete') }}>删除</button>
        </div>
        <div className="code-block" style={{fontSize:'0.7812rem',whiteSpace:'pre-wrap',maxHeight:'25rem',overflowY:'auto',marginTop:8}}>{data?.connections || '(无 nmcli 或加载中)'}</div>
      </Card>
      <Card title="WiFi" subtitle="nmcli dev wifi">
        <div className="form-inline">
          <input className="input" placeholder="SSID" value={ssid} onChange={e => setSsid(e.target.value)} />
          <input className="input" placeholder="密码(可选)" value={psk} onChange={e => setPsk(e.target.value)} type="password" />
          <input className="input" placeholder="网卡(可选)" value={wifiDev} onChange={e => setWifiDev(e.target.value)} style={{width:120}} />
          <button className="btn btn-accent" disabled={!ssid} onClick={() => act('wifi-connect')}>连接</button>
          <button className="btn btn-sm" onClick={() => setShowWifi(!showWifi)}>{showWifi ? '隐藏' : '扫描 WiFi'}</button>
        </div>
        {showWifi && <div className="code-block" style={{fontSize:'0.7812rem',whiteSpace:'pre-wrap',maxHeight:'18.75rem',overflowY:'auto',marginTop:8}}>{data?.wifi || '扫描中…'}</div>}
      </Card>
    </>
  )
}

// ── 网络配置子组件: 查看/修改 IP, 路由, DNS ──

function NetConfigSection() {
  const { selected } = useHost()
  const nc = selected?.id ? `?host=${selected.id}` : ''
  const [data, setData] = useState<NetConfig | null>(null)
  const [loading, setLoading] = useState(true)
  const [actionMsg, setActionMsg] = useState('')
  const load = async () => {
    setLoading(true)
    try { const d = await getJSON<NetConfig>('/api/core/network/config'+nc); setData(d) } catch { /* ignore */ }
    setLoading(false)
  }
  useEffect(() => { load() }, [selected])
  const runAction = async (action: string, device: string, extra?: Record<string, any>) => {
    try {
      const body: any = { action, device, ...extra }
      if (selected?.id) body.host = selected.id
      const res = await postJSON<ConfigResult>('/api/core/network/config', body)
      if (res.ok) {
        setActionMsg(`✓ ${action} 成功${res.note ? ' · ' + res.note : ''}`)
        load()
      } else setActionMsg(`✗ ${res.error || '操作失败'}`)
    } catch { setActionMsg('✗ 请求失败') }
    setTimeout(() => setActionMsg(''), 5000)
  }
  const [restartDev, setRestartDev] = useState('')
  const [editIP, setEditIP] = useState('')
  const [editMask, setEditMask] = useState(24)
  const [editIPDev, setEditIPDev] = useState('')
  const [editDNS, setEditDNS] = useState('')
  const [editDNSDev, setEditDNSDev] = useState('')
  if (loading && !data) return <div className="loading">加载网络配置中…</div>
  if (!data) return <div className="banner banner-err">加载失败</div>
  const isRoot = data.permission === 'root'
  return (
    <>
      {actionMsg && <div className={`banner ${actionMsg.startsWith('✓') ? 'banner-ok' : 'banner-err'}`}>{actionMsg}</div>}
      <div className="grid grid-2">
        <Card title="网络接口" subtitle="ip addr show">
          <div className="code-block" style={{ fontSize:'0.7812rem', whiteSpace: 'pre-wrap', maxHeight:'21.25rem', overflowY: 'auto' }}>{data.interfaces}</div>
        </Card>
        <Card title="路由表" subtitle="ip route show">
          <div className="code-block" style={{ fontSize:'0.7812rem', whiteSpace: 'pre-wrap', maxHeight:'21.25rem', overflowY: 'auto' }}>{data.routes}</div>
        </Card>
        <Card title="DNS 配置" subtitle={data.permission === 'root' ? '' : '只读'}>
          <div className="code-block" style={{ fontSize:'0.7812rem', whiteSpace: 'pre-wrap', maxHeight:'15rem', overflowY: 'auto' }}>{data.dns}</div>
        </Card>
        <Card title="NetworkManager" subtitle="nmcli dev status">
          <div className="code-block" style={{ fontSize:'0.7812rem', whiteSpace: 'pre-wrap', maxHeight:'15rem', overflowY: 'auto' }}>{data.nm}</div>
        </Card>
      </div>
      {isRoot && (
        <Card title="操作" subtitle="root 权限">
          <div className="grid" style={{ gridTemplateColumns: '1fr 1fr', gap: 16 }}>
            <div>
              <div className="field-label">网卡重启 / DHCP</div>
              <div className="form-inline">
                <input className="input" placeholder="设备名 (如 ens160)" value={restartDev} onChange={e => setRestartDev(e.target.value)} style={{width:120}} />
                <button className="btn btn-danger" disabled={!restartDev} onClick={() => { if (confirm('确认重启 ' + restartDev + '？连接将断开')) runAction('restart', restartDev); setRestartDev('') }}>重启</button>
                <button className="btn btn-accent" disabled={!restartDev} onClick={() => { runAction('dhcp', restartDev); setRestartDev('') }}>DHCP 续租</button>
              </div>
            </div>
            <div>
              <div className="field-label">修改 IP</div>
              <div className="form-inline">
                <input className="input" placeholder="设备" value={editIPDev} onChange={e => setEditIPDev(e.target.value)} style={{ width: 100 }} />
                <input className="input" placeholder="IP" value={editIP} onChange={e => setEditIP(e.target.value)} />
                <span className="dim">/</span>
                <input className="input" type="number" min={1} max={32} value={editMask} onChange={e => setEditMask(Number(e.target.value))} style={{ width: 60 }} />
                <button className="btn btn-accent" disabled={!editIPDev || !editIP} onClick={() => { runAction('set-ip', editIPDev, { ip: editIP, mask: editMask }); setEditIPDev(''); setEditIP(''); setEditMask(24) }}>设置</button>
              </div>
            </div>
            <div style={{ gridColumn: '1 / -1' }}>
              <div className="field-label">修改 DNS</div>
              <div className="form-inline">
                <input className="input" placeholder="设备" value={editDNSDev} onChange={e => setEditDNSDev(e.target.value)} style={{ width: 100 }} />
                <input className="input" placeholder="DNS 服务器 (空格分隔多个)" value={editDNS} onChange={e => setEditDNS(e.target.value)} />
                <button className="btn btn-accent" disabled={!editDNSDev || !editDNS} onClick={() => { runAction('set-dns', editDNSDev, { dns: editDNS }); setEditDNSDev(''); setEditDNS('') }}>设置</button>
              </div>
            </div>
          </div>
        </Card>
      )}
    </>
  )
}
