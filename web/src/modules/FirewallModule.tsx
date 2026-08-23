// ── 防火墙模块: 状态 / 端口规则 / IP 黑白名单 / 确认弹窗 / 审计链 ──

import { useCallback, useEffect, useState } from 'react'
import { getJSON, postJSON } from '../api/client'
import Card from '../components/Card'
import { useHost } from '../components/HostContext'
import HostSelector from '../components/HostSelector'

interface FWStatus {
  os: string
  backend: string
  running: boolean
  manageable: boolean
  message: string
}
interface FWRule {
  name: string
  direction: string
  action: string
  protocol: string
  localPort: string
  remoteIP: string
}
interface AuditEntry {
  ts: string
  actor: string
  role: string
  credential: string
  action: string
  params: string
  result: string
  dryRun: boolean
}

type Tab = 'port' | 'ip' | 'rules' | 'zones' | 'rich' | 'forward'

export default function FirewallModule({ embedded = false }: { embedded?: boolean }) {
  const { selected } = useHost()
  const [status, setStatus] = useState<FWStatus | null>(null)
  const [rules, setRules] = useState<FWRule[]>([])
  const [audit, setAudit] = useState<AuditEntry[]>([])
  const [tab, setTab] = useState<Tab>('port')
  const [msg, setMsg] = useState<string>('')
  const [loadErr, setLoadErr] = useState('')

  // zone / rich-rule / forward 数据
  const [zones, setZones] = useState<{ all: string[]; default: string; active: string } | null>(null)
  const [richRules, setRichRules] = useState<string[]>([])
  const [forwardPorts, setForwardPorts] = useState<string[]>([])

  // 表单
  const [port, setPort] = useState('')
  const [proto, setProto] = useState('tcp')
  const [portAct, setPortAct] = useState<'allow' | 'deny'>('allow')
  const [cidr, setCidr] = useState('')
  const [ipAct, setIpAct] = useState<'allow' | 'deny'>('allow')
  const [richInput, setRichInput] = useState('')
  const [zoneSelect, setZoneSelect] = useState('')
  const [fwdSrcPort, setFwdSrcPort] = useState('')
  const [fwdDest, setFwdDest] = useState('')
  const [fwdProto, setFwdProto] = useState('tcp')

  // 确认弹窗(二次确认 + 防锁死警告)
  const [confirm, setConfirm] = useState<{ open: boolean; payload: any; command: string; lockoutRisk: boolean }>({
    open: false,
    payload: null,
    command: '',
    lockoutRisk: false,
  })
  const [reason, setReason] = useState('')
  const [busy, setBusy] = useState(false)

  const h = selected?.id ? `?host=${selected.id}` : ''
  const load = useCallback(() => {
    setLoadErr('')
    getJSON<FWStatus>('/api/core/firewall'+h).then(setStatus).catch(e => setLoadErr('状态加载失败: '+e.message))
    getJSON<{ rules: FWRule[] }>('/api/core/firewall/rules'+h).then((d) => setRules(d.rules || [])).catch(() => {})
    getJSON<{ entries: AuditEntry[] }>('/api/core/firewall/audit'+h).then((d) => setAudit(d.entries || [])).catch(() => {})
    getJSON<{ all: string[]; default: string; active: string }>('/api/core/firewall/zones'+h).then(setZones).catch(() => {})
    getJSON<{ rules: string[] }>('/api/core/firewall/rich-rules'+h).then((d) => setRichRules(d.rules || [])).catch(() => {})
    getJSON<{ ports: string[] }>('/api/core/firewall/forward-ports'+h).then((d) => setForwardPorts(d.ports || [])).catch(() => {})
  }, [selected])

  useEffect(() => {
    load()
    const t = setInterval(load, 8000)
    return () => clearInterval(t)
  }, [load])

  const openConfirm = (payload: any) => {
    const body = selected?.id ? { ...payload, host: selected.id, dryRun: true, reason: 'preview' } : { ...payload, dryRun: true, reason: 'preview' }
    postJSON<{ command: string; lockoutRisk: boolean }>('/api/core/firewall/action', body)
      .then((r) => {
        setConfirm({ open: true, payload, command: r.command || '', lockoutRisk: !!r.lockoutRisk })
        setReason('')
      })
      .catch(() => setMsg('预览失败'))
  }

  const doConfirm = async () => {
    if (!reason.trim()) {
      setMsg('✗ 必须填写操作原因(审计要求)')
      return
    }
    setBusy(true)
    const actionPayload = selected?.id ? { ...confirm.payload, host: selected.id, reason: reason.trim() } : { ...confirm.payload, reason: reason.trim() }
    const res = await postJSON<any>('/api/core/firewall/action', actionPayload)
    setBusy(false)
    setConfirm({ ...confirm, open: false })
    if (res.dryRun) {
      setMsg(`⚠ 只读演示:未真正执行。预览命令 → ${res.command}`)
    } else if (res.ok) {
      setMsg(`✓ 已执行:${res.command}`)
    } else {
      setMsg(`✗ ${res.error || res.message || '操作失败'}`)
    }
    load()
  }

  if (!status) return <div className="loading">{loadErr || '加载防火墙状态中…'}</div>

  const body = (
    <>
      {loadErr && <div className="banner banner-err">{loadErr}</div>}
      {!status.manageable && (
        <div className="banner banner-warn">
          ⚠ {status.message}
        </div>
      )}
      {msg && <div className={`banner ${msg.startsWith('✗') ? 'banner-err' : msg.startsWith('⚠') ? 'banner-warn' : 'banner-ok'}`}>{msg}</div>}

      <Card title="防火墙状态" subtitle="高危操作 · 需二次确认 + 审计">
        <div className="status-row">
          <span className={`badge ${status.running ? 'badge-ok' : 'badge-danger'}`}>
            防火墙 {status.running ? '已开启' : '已关闭'}
          </span>
          <span className="badge badge-info">后端 {status.backend}</span>
          <span className={`badge ${status.manageable ? 'badge-ok' : 'badge-off'}`}>
            {status.manageable ? '可写入' : '只读演示'}
          </span>
          <div className="btn-row" style={{ marginLeft: 'auto' }}>
            <button className="btn btn-sm" disabled={busy} onClick={() => openConfirm({ action: 'start' })}>启动</button>
            <button className="btn btn-sm" disabled={busy} onClick={() => openConfirm({ action: 'stop' })}>停止</button>
            <button className="btn btn-sm btn-accent" disabled={busy} onClick={() => openConfirm({ action: 'restart' })}>重启</button>
          </div>
        </div>
      </Card>

      <div className="tabs" style={{flexWrap:'wrap'}}>
        <button className={`tab ${tab === 'port' ? 'tab-on' : ''}`} onClick={() => setTab('port')}>端口规则</button>
        <button className={`tab ${tab === 'ip' ? 'tab-on' : ''}`} onClick={() => setTab('ip')}>IP 黑白名单</button>
        <button className={`tab ${tab === 'rules' ? 'tab-on' : ''}`} onClick={() => setTab('rules')}>现有规则 ({rules.length})</button>
        <button className={`tab ${tab === 'zones' ? 'tab-on' : ''}`} onClick={() => setTab('zones')}>区域管理</button>
        <button className={`tab ${tab === 'rich' ? 'tab-on' : ''}`} onClick={() => setTab('rich')}>Rich-Rule</button>
        <button className={`tab ${tab === 'forward' ? 'tab-on' : ''}`} onClick={() => setTab('forward')}>端口转发</button>
      </div>

      {tab === 'port' && (
        <Card title="端口开关" subtitle="允许 / 拒绝 某端口+协议">
          <div className="form-inline">
            <input className="input" placeholder="端口,如 3306" value={port}
              onChange={(e) => { const v = e.target.value.replace(/[^0-9]/g, ''); setPort(v) }}
              onBlur={() => { const n = +port; if (port && (n < 1 || n > 65535)) setMsg('✗ 端口范围 1-65535') }} />
            <select className="input" value={proto} onChange={(e) => setProto(e.target.value)}>
              <option value="tcp">TCP</option>
              <option value="udp">UDP</option>
            </select>
            <select className="input" value={portAct} onChange={(e) => setPortAct(e.target.value as any)}>
              <option value="allow">允许</option>
              <option value="deny">拒绝</option>
            </select>
            <button className="btn btn-accent" disabled={!port}
              onClick={() => openConfirm({ action: portAct + '-port', port, proto })}>
              {portAct === 'allow' ? '开放端口' : '关闭端口'}
            </button>
          </div>
        </Card>
      )}

      {tab === 'ip' && (
        <Card title="IP 黑白名单" subtitle="按来源 IP / CIDR 放行或封禁">
          <div className="form-inline">
            <input className="input" placeholder="CIDR,如 10.0.0.0/24" value={cidr}
              onChange={(e) => setCidr(e.target.value)} />
            <select className="input" value={ipAct} onChange={(e) => setIpAct(e.target.value as any)}>
              <option value="allow">白名单(放行)</option>
              <option value="deny">黑名单(封禁)</option>
            </select>
            <button className="btn btn-accent" disabled={!cidr}
              onClick={() => openConfirm({ action: ipAct + '-ip', cidr })}>
              {ipAct === 'allow' ? '加入白名单' : '加入黑名单'}
            </button>
          </div>
        </Card>
      )}

      {tab === 'rules' && (
        <Card title="现有规则" subtitle="真实读取自后端(netsh / ufw)">
          <div className="table-wrap">
            <table className="data-table">
              <thead>
                <tr><th>名称</th><th>方向</th><th>动作</th><th>协议</th><th>本地端口</th><th>远端 IP</th><th style={{width:60}}></th></tr>
              </thead>
              <tbody>
                {rules.map((r, i) => (
                  <tr key={i}>
                    <td className="mono small">{r.name}</td>
                    <td className="dim">{r.direction || '—'}</td>
                    <td>
                      <span className={`badge ${/allow/i.test(r.action) ? 'badge-ok' : 'badge-danger'}`}>
                        {r.action || '—'}
                      </span>
                    </td>
                    <td className="dim">{r.protocol || '—'}</td>
                    <td className="mono small">{r.localPort || '—'}</td>
                    <td className="mono small">{r.remoteIP || '—'}</td>
                    <td><button className="btn btn-sm btn-danger" style={{fontSize:'0.6875rem',padding:'0.125rem 0.375rem'}} onClick={() => openConfirm({ action: 'delete-rule', source: r.name })}>删除</button></td>
                  </tr>
                ))}
                {rules.length === 0 && <tr><td colSpan={7} className="dim">无规则或当前环境不支持读取</td></tr>}
              </tbody>
            </table>
          </div>
        </Card>
      )}

      {tab === 'zones' && (
        <Card title="firewalld 区域管理" subtitle="查看/切换默认区域">
          {zones ? (<>
            <div className="status-row">
              <span className="badge badge-info">默认区域: {zones.default}</span>
              <span className="badge badge-ok">可用区域: {zones.all.length}</span>
            </div>
            <div className="field-label">当前活跃区域</div>
            <pre className="code-block">{zones.active || '(无)'}</pre>
            <div className="form-inline" style={{marginTop:8}}>
              <select className="input" value={zoneSelect} onChange={e => setZoneSelect(e.target.value)}>
                <option value="">选择区域…</option>
                {zones.all.map(z => <option key={z} value={z}>{z}</option>)}
              </select>
              <button className="btn btn-accent" disabled={!zoneSelect}
                onClick={() => openConfirm({ action: 'set-default-zone', zone: zoneSelect })}>
                设为默认区域
              </button>
            </div>
          </>) : (
            <div className="dim">区域信息需要 firewalld 环境</div>
          )}
        </Card>
      )}

      {tab === 'rich' && (
        <Card title="Rich-Rule 管理" subtitle="firewalld 高级规则(add/list/remove)">
          <div className="form-inline">
            <input className="input" placeholder="rule family=ipv4 source address=10.0.0.0/24 port port=8080 protocol=tcp reject" value={richInput}
              onChange={e => setRichInput(e.target.value)} />
            <button className="btn btn-accent" disabled={!richInput}
              onClick={() => openConfirm({ action: 'add-rich-rule', richRule: richInput })}>
              添加
            </button>
          </div>
          <div className="table-wrap" style={{marginTop:8}}>
            <table className="data-table">
              <thead><tr><th>Rich-Rule</th><th style={{width:60}}></th></tr></thead>
              <tbody>
                {richRules.map((r, i) => (
                  <tr key={i}>
                    <td className="mono small" style={{wordBreak:'break-all'}}>{r}</td>
                    <td><button className="btn btn-sm btn-danger" onClick={() => openConfirm({ action: 'remove-rich-rule', richRule: r })}>删除</button></td>
                  </tr>
                ))}
                {richRules.length === 0 && <tr><td colSpan={2} className="dim">无 rich-rule 或非 firewalld 环境</td></tr>}
              </tbody>
            </table>
          </div>
        </Card>
      )}

      {tab === 'forward' && (
        <Card title="端口转发" subtitle="firewalld 端口转发 add/list/remove">
          <div className="form-inline">
            <input className="input" placeholder="源端口" value={fwdSrcPort}
              onChange={e => setFwdSrcPort(e.target.value.replace(/[^0-9]/g,''))} />
            <span className="dim">→</span>
            <input className="input" placeholder="目标 10.0.0.2:80" value={fwdDest}
              onChange={e => setFwdDest(e.target.value)} />
            <select className="input" value={fwdProto} onChange={e => setFwdProto(e.target.value)}>
              <option value="tcp">TCP</option><option value="udp">UDP</option>
            </select>
            <button className="btn btn-accent" disabled={!fwdSrcPort || !fwdDest}
              onClick={() => openConfirm({ action: 'add-forward-port', fwdSrcPort, fwdDest, proto: fwdProto })}>
              添加转发
            </button>
          </div>
          <div className="table-wrap" style={{marginTop:8}}>
            <table className="data-table">
              <thead><tr><th>转发规则</th><th style={{width:60}}></th></tr></thead>
              <tbody>
                {forwardPorts.map((p, i) => (
                  <tr key={i}>
                    <td className="mono small" style={{wordBreak:'break-all'}}>{p}</td>
                    <td><button className="btn btn-sm btn-danger" onClick={() => {
                      // 从现有转发规则反向解析参数
                      const m = p.match(/port=(\d+):proto=(\w+):toaddr=([\w.]+):toport=(\d+)/)
                      if (m) openConfirm({ action: 'remove-forward-port', fwdSrcPort: m[1], proto: m[2], fwdDest: m[3]+':'+m[4] })
                      else setMsg('✗ 无法解析该转发规则以删除')
                    }}>删除</button></td>
                  </tr>
                ))}
                {forwardPorts.length === 0 && <tr><td colSpan={2} className="dim">无转发规则或非 firewalld 环境</td></tr>}
              </tbody>
            </table>
          </div>
        </Card>
      )}

      <Card title="审计链" subtitle="ADR-002:actor · role · action · params · result">
        <div className="table-wrap">
          <table className="data-table">
            <thead><tr><th>时间</th><th>操作人</th><th>角色</th><th>动作</th><th>命令/参数</th><th>结果</th></tr></thead>
            <tbody>
              {audit.slice().reverse().map((a, i) => (
                <tr key={i}>
                  <td className="mono small dim">{a.ts}</td>
                  <td className="small">{a.actor || '—'}</td>
                  <td className="small">{a.role || '—'}</td>
                  <td><span className={`badge ${a.dryRun ? 'badge-off' : 'badge-ok'}`}>{a.action}{a.dryRun ? ' · 预览' : ''}</span></td>
                  <td className="mono small" style={{maxWidth:'18.75rem',overflow:'hidden',textOverflow:'ellipsis'}}>{a.params}</td>
                  <td className="small">{a.result}</td>
                </tr>
              ))}
              {audit.length === 0 && <tr><td colSpan={6} className="dim">暂无审计记录</td></tr>}
            </tbody>
          </table>
        </div>
      </Card>

      {confirm.open && (
        <div className="modal-overlay" onClick={() => !busy && setConfirm({ ...confirm, open: false })}>
          <div className="modal" onClick={(e) => e.stopPropagation()}>
            <h3>确认防火墙操作</h3>
            {confirm.lockoutRisk && (
              <div className="lockout-warn">
                🔴 高危:此操作可能把自己锁死(关闭 SSH / RDP / 当前端口,或封禁全网)。请确认你有其他接入方式!
              </div>
            )}
            <div className="field-label">将执行的命令</div>
            <pre className="code-block">{confirm.command || '(无法预览)'}</pre>
            <div className="field-label">操作原因(必填,记入审计)</div>
            <input className="input" value={reason} onChange={(e) => setReason(e.target.value)}
              placeholder="例如:为应用 A 开放 3306 端口"
              onKeyDown={e => e.key === 'Enter' && !busy && doConfirm()} />
            <div className="modal-actions">
              <button className="btn" disabled={busy} onClick={() => setConfirm({ ...confirm, open: false })}>取消</button>
              <button className="btn btn-danger" disabled={busy} onClick={doConfirm}>
                {busy ? '执行中…' : '确认执行'}
              </button>
            </div>
            {!status.manageable && <div className="dim small">本环境为只读演示,确认后仅记录审计、不真正改网络。</div>}
          </div>
        </div>
      )}
    </>
  )

  if (embedded) return body

  return (
    <div className="module">
      <div className="module-head">
        <div className="module-head-row"><h2>防火墙</h2><HostSelector /></div>
        <span className="pill">{status.backend} · {status.os}</span>
      </div>
      {body}
    </div>
  )
}
