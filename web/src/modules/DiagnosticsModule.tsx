// ── 系统诊断模块: 网络诊断 / 登录审计 / 系统更新 ──

import { useEffect, useState } from 'react'
import { getJSON, postJSON } from '../api/client'
import Card from '../components/Card'

type Permission = 'root' | 'user'

type DiagnosticInfo = {
  permission: Permission
  features: { id: string; name: string; available: boolean }[]
}

type DiagResult = { output?: string; error?: string; permission: Permission }
type LoginAudit = { last: string; lastb: string; sshd_logs: string; permission: Permission }
type Updates = { updates: string; needs_restart: boolean; restart_detail: string; error?: string; permission: Permission }

const NET_TOOLS = [
  { id: 'ping', label: 'Ping', needsTarget: true },
  { id: 'traceroute', label: '路由追踪', needsTarget: true },
  { id: 'mtr', label: 'MTR 路径', needsTarget: true },
  { id: 'port', label: '端口检测', needsTarget: true },
  { id: 'dns', label: 'DNS 查询', needsTarget: true },
  { id: 'dns-detail', label: 'DNS 详情', needsTarget: true },
  { id: 'http', label: 'HTTP 探测', needsTarget: true },
  { id: 'route', label: '路由表', needsTarget: false },
  { id: 'arp', label: 'ARP 邻居', needsTarget: false },
]

export default function DiagnosticsModule() {
  const [info, setInfo] = useState<DiagnosticInfo | null>(null)
  const [tab, setTab] = useState('network')

  useEffect(() => {
    getJSON<DiagnosticInfo>('/api/core/diagnostics').then(setInfo).catch(() => {})
  }, [])

  if (!info) return <div className="loading">加载中…</div>

  const tabs = [
    { id: 'network', label: '网络诊断', avail: true },
    { id: 'login', label: '登录审计', avail: true },
    { id: 'updates', label: '系统更新', avail: info.features.find(f => f.id === 'updates')?.available ?? false },
  ]

  return (
    <div className="module">
      <div className="module-head">
        <h2>系统诊断</h2>
        <span className="pill">{info.permission === 'root' ? 'root 权限' : '受限模式'}</span>
      </div>

      <div className="tabs">
        {tabs.filter(t => t.avail).map(t => (
          <button key={t.id} className={`tab ${tab === t.id ? 'tab-on' : ''}`} onClick={() => setTab(t.id)}>{t.label}</button>
        ))}
      </div>

      {tab === 'network' && <NetworkSection />}
      {tab === 'login' && <LoginSection />}
      {tab === 'updates' && <UpdatesSection />}
    </div>
  )
}

// ── 网络诊断子组件: ping / traceroute / mtr / 端口 / DNS / HTTP / 路由 / ARP ──

function NetworkSection() {
  const [tool, setTool] = useState('ping')
  const [target, setTarget] = useState('')
  const [port, setPort] = useState(80)
  const [count, setCount] = useState(4)
  const [loading, setLoading] = useState(false)
  const [result, setResult] = useState<DiagResult | null>(null)
  const [history, setHistory] = useState<{tool:string;target:string;time:string;result:DiagResult}[]>([])
  const [elapsed, setElapsed] = useState(0)

  const cur = NET_TOOLS.find(t => t.id === tool) || NET_TOOLS[0]

  const run = async () => {
    if (cur.needsTarget && !target.trim()) return
    setLoading(true); setResult(null); setElapsed(0)
    const start = Date.now()
    const timer = setInterval(() => setElapsed(Math.floor((Date.now() - start) / 1000)), 500)
    try {
      const body: any = { tool }
      if (cur.needsTarget) body.target = target.trim()
      if (tool === 'ping') body.count = count
      if (tool === 'port') body.port = port
      const res = await postJSON<DiagResult>('/api/core/diagnostics/network', body)
      setResult(res)
      setHistory(p => [{tool, target: target.trim(), time: new Date().toLocaleTimeString(), result: res}, ...p].slice(0, 20))
    } catch { const res = { error: '请求失败' } as DiagResult; setResult(res); setHistory(p => [{tool, target: target.trim(), time: new Date().toLocaleTimeString(), result: res}, ...p].slice(0, 20)) }
    clearInterval(timer)
    setLoading(false)
  }

  const copyResult = (text: string) => navigator.clipboard?.writeText(text)

  return (
    <Card title="网络诊断" subtitle="多工具诊断">
      <div className="tabs" style={{ marginBottom: 12 }}>
        {NET_TOOLS.map(t => (
          <button key={t.id} className={`tab ${tool === t.id ? 'tab-on' : ''}`} onClick={() => setTool(t.id)}>{t.label}</button>
        ))}
      </div>

      <div className="form-inline" style={{ marginBottom: 14 }}>
        {cur.needsTarget && tool !== 'http' && (
          <input className="input" placeholder="目标地址 (IP 或域名)" value={target} onChange={e => setTarget(e.target.value)} onKeyDown={e => e.key === 'Enter' && run()} />
        )}
        {tool === 'port' && (
          <>
            <span className="field-label" style={{ margin: '0 0 0 0.5rem' }}>端口</span>
            <input className="input" type="number" min={1} max={65535} style={{ width: 90 }} value={port} onChange={e => setPort(Number(e.target.value))} onKeyDown={e => e.key === 'Enter' && run()} />
          </>
        )}
        {tool === 'http' && (
          <input className="input" placeholder="URL (如 https://example.com)" value={target} onChange={e => setTarget(e.target.value)} onKeyDown={e => e.key === 'Enter' && run()} />
        )}
        {tool === 'ping' && (
          <select className="sel" value={count} onChange={e => setCount(Number(e.target.value))}>
            <option value={2}>2 次</option>
            <option value={4}>4 次</option>
            <option value={6}>6 次</option>
            <option value={10}>10 次</option>
          </select>
        )}
        <button className="btn btn-accent" onClick={run} disabled={loading || (cur.needsTarget && !target.trim())}>
          {loading ? `诊断中… ${elapsed}s` : '执行'}
        </button>
      </div>

      {result && (
        <div className="code-block" style={{ whiteSpace: 'pre-wrap', fontFamily: 'ui-monospace,monospace', fontSize:'0.7812rem', position:'relative' }}>
          <button className="btn btn-sm" style={{position:'absolute',top:'0.25rem',right:'0.25rem',fontSize:'0.6875rem',padding:'0.125rem 0.5rem'}} onClick={() => copyResult(result.output || result.error || '')}>复制</button>
          {result.error && <div className="banner banner-err">{result.error}</div>}
          {result.output}
        </div>
      )}

      {history.length > 1 && (
        <div style={{marginTop:16}}>
          <div style={{fontSize:'0.8125rem',fontWeight:600,marginBottom:6}}>诊断历史 ({history.length})</div>
          <div style={{maxHeight:'12.5rem',overflow:'auto'}}>
            {history.slice(1).map((h, i) => (
              <div key={i} style={{display:'flex',gap:'0.5rem',padding:'0.1875rem 0',fontSize:'0.75rem',borderBottom:'1px solid var(--border)',cursor:'pointer'}}
                onClick={() => { setTool(h.tool); setResult(h.result); setTarget(h.target) }}>
                <span className="dim">{h.time}</span>
                <span className="badge badge-info" style={{fontSize:10}}>{h.tool}</span>
                <span className="mono dim">{h.target}</span>
                <span className={`badge ${h.result.error ? 'badge-danger' : 'badge-ok'}`} style={{fontSize:10}}>{h.result.error ? '失败' : '成功'}</span>
              </div>
            ))}
          </div>
        </div>
      )}
    </Card>
  )
}

// ── 登录审计子组件: last / lastb / sshd 日志 ──

function LoginSection() {
  const [data, setData] = useState<LoginAudit | null>(null)
  useEffect(() => { getJSON<LoginAudit>('/api/core/diagnostics/login-audit').then(setData).catch(() => {}) }, [])
  if (!data) return <div className="loading">加载中…</div>

  return (
    <>
      <Card title="最近登录" subtitle="last -F -n 30">
        <div className="code-block" style={{ whiteSpace: 'pre-wrap', fontSize: 12.5 }}>{data.last || '（无记录）'}</div>
      </Card>
      {data.lastb && (
        <Card title="失败登录尝试" subtitle="lastb">
          <div className="code-block" style={{ whiteSpace: 'pre-wrap', fontSize: 12.5 }}>{data.lastb}</div>
        </Card>
      )}
      {data.sshd_logs && (
        <Card title="SSHD 日志" subtitle="journalctl -u sshd (7天)">
          <div className="code-block" style={{ whiteSpace: 'pre-wrap', fontSize: 12.5 }}>{data.sshd_logs}</div>
        </Card>
      )}
    </>
  )
}

// ── 系统更新子组件: 安全更新列表 + 重启状态 ──

function UpdatesSection() {
  const [data, setData] = useState<Updates | null>(null)
  const [installing, setInstalling] = useState(false)
  const [installResult, setInstallResult] = useState<string | null>(null)

  const load = () => getJSON<Updates>('/api/core/diagnostics/updates').then(setData).catch(() => {})
  useEffect(() => { load() }, [])

  const installUpdates = async () => {
    if (!confirm('确定安装所有安全更新？此操作可能耗时较长。')) return
    setInstalling(true); setInstallResult(null)
    try {
      const d = await postJSON<any>('/api/core/diagnostics/updates/install', {})
      if (d.ok) setInstallResult('✓ 更新安装完成')
      else setInstallResult(`✗ ${d.error || '安装失败'}`)
      if (d.output) setInstallResult((p: string | null) => (p || '') + '\n\n' + d.output)
    } catch (e: any) {
      setInstallResult(`✗ ${e?.message || '请求失败'}`)
    }
    setInstalling(false); load()
  }

  if (!data) return <div className="loading">加载中…</div>
  if (data.error) return <div className="banner banner-err">{data.error}</div>

  return (
    <>
      <Card title="安全更新" subtitle="dnf check-update --security">
        <div className="flex-between" style={{marginBottom:8}}>
          <span>{data.updates ? '以下更新可用' : '无待安装安全更新'}</span>
          {data.updates && <button className="btn btn-accent" onClick={installUpdates} disabled={installing}>{installing ? '安装中…' : '安装安全更新'}</button>}
        </div>
        {installResult && <div className={`banner ${installResult.startsWith('✓') ? 'banner-ok' : 'banner-err'}`} style={{whiteSpace:'pre-wrap'}}>{installResult}</div>}
        <div className="code-block" style={{ whiteSpace: 'pre-wrap', fontSize: 12.5 }}>{data.updates || '（无）'}</div>
      </Card>
      <Card title="重启状态" subtitle="needs-restarting">
        <div className="banner" style={{ background: data.needs_restart ? '#ef44441f' : '#22c55e1f', borderColor: data.needs_restart ? '#ef44444d' : '#22c55e4d' }}>
          {data.needs_restart ? '⚠ 系统需要重启以应用更新' : '✓ 系统不需要重启'}
        </div>
        <div className="code-block" style={{ whiteSpace: 'pre-wrap', fontSize:'0.7812rem', marginTop: 8 }}>{data.restart_detail}</div>
      </Card>
    </>
  )
}
