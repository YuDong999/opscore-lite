import { useEffect, useState, useCallback, useRef } from 'react'
import { getJSON, postJSON } from '../api/client'
import { useHost } from '../components/HostContext'

interface Host { id: string; addr: string; port: number; user: string; alias: string; sshKey?: string; password?: string; groups?: string[]; platform?: string; isLocal?: boolean; hostname?: string }
interface Group { name: string; parent?: string; vars?: Record<string,string>; children?: string[] }
interface HostEntry { id: string; addr: string; port: number; user: string; alias: string; sshKey?: string; password?: string; groups: string[]; vars?: Record<string,string> }
interface Inventory { id: string; name: string; description: string; groups: Record<string,Group>; hosts: Record<string,HostEntry> }
interface Playbook { id: string; name: string; description: string; content: string; path?: string }
interface Template { id: string; name: string; description: string; category: string; content: string }
interface AnsibleResult { host: string; success: boolean; output: string; stdout: string; stderr: string; changed: boolean }
interface SSEEvent { type: string; payload: any }
interface RunContext { hosts?: string[]; inventoryId?: string; module?: string; args?: string; checkMode?: boolean; tags?: string; extraVars?: string; limit?: string; forks?: number; playbookId?: string }
interface ExecRecord { id: string; time: string; type: string; target: string; results: AnsibleResult[]; success: boolean; duration: string; run?: RunContext }
interface SSHKeyPair { name: string; fingerprint: string; publicKey: string; createdAt: string }

function useSSEExec() {
  const [lines, setLines] = useState<string[]>([])
  const [results, setResults] = useState<AnsibleResult[] | null>(null)
  const [running, setRunning] = useState(false)
  const [err, setErr] = useState('')
  const aborter = useRef<AbortController | null>(null)

  const exec = async (url: string, body: any) => {
    setRunning(true); setLines([]); setResults(null); setErr('')
    const ctrl = new AbortController(); aborter.current = ctrl
    try {
      const resp = await fetch(url, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
        signal: ctrl.signal,
      })
      const reader = resp.body?.getReader()
      if (!reader) { setErr('无法读取响应流'); setRunning(false); return }
      const decoder = new TextDecoder()
      let buf = ''
      while (true) {
        const { done, value } = await reader.read()
        if (done) break
        buf += decoder.decode(value, { stream: true })
        const parts = buf.split('\n\n')
        buf = parts.pop() || ''
        for (const part of parts) {
          const m = part.match(/^data:\s*(.*)/m)
          if (!m) continue
          try {
            const evt: SSEEvent = JSON.parse(m[1])
            if (evt.type === 'line') setLines(p => [...p, evt.payload])
            else if (evt.type === 'result') setResults(evt.payload as AnsibleResult[])
            else if (evt.type === 'error') setErr(evt.payload as string)
          } catch {}
        }
      }
    } catch (e: any) {
      if (e.name !== 'AbortError') setErr(e.message || '请求失败')
    }
    setRunning(false)
  }

  const cancel = () => { aborter.current?.abort(); setRunning(false) }

  return { lines, results, running, err, exec, cancel, setErr }
}

type Tab = 'dashboard' | 'hosts' | 'groups' | 'inventories' | 'playbooks' | 'adhoc' | 'history' | 'ssh'

// 标准全选 Checkbox: 全选/半选(indeterminate)/未选三态
function CheckboxAll({ checked, indeterminate, onChange }: { checked: boolean; indeterminate: boolean; onChange: (v: boolean) => void }) {
  const ref = useRef<HTMLInputElement>(null)
  useEffect(() => {
    if (ref.current) ref.current.indeterminate = indeterminate && !checked
  }, [indeterminate, checked])
  return <input ref={ref} type="checkbox" checked={checked} onChange={e => onChange(e.target.checked)} />
}

export default function AnsibleModule() {
  const [tab, setTab] = useState<Tab>('dashboard')
  const tabs: { id: Tab; label: string }[] = [
    { id: 'dashboard', label: '总览' },
    { id: 'hosts', label: '主机管理' },
    { id: 'groups', label: '主机组' },
    { id: 'inventories', label: '库存清单' },
    { id: 'playbooks', label: 'Playbook' },
    { id: 'adhoc', label: 'Ad-hoc' },
    { id: 'history', label: '执行历史' },
    { id: 'ssh', label: 'SSH 免密' },
  ]
  return (
    <div className="module-card">
      <div className="module-header"><h2>Ansible 多机管理</h2></div>
      <div className="tabs">
        {tabs.map(t => (
          <button key={t.id} className={`tab ${tab === t.id ? 'tab-on' : ''}`} onClick={() => setTab(t.id)}>{t.label}</button>
        ))}
      </div>
      {tab === 'dashboard' && <Dashboard />}
      {tab === 'hosts' && <HostsPanel />}
      {tab === 'groups' && <GroupsPanel />}
      {tab === 'inventories' && <InventoriesPanel />}
      {tab === 'playbooks' && <PlaybooksPanel />}
      {tab === 'adhoc' && <AdhocPanel />}
      {tab === 'history' && <HistoryPanel />}
      {tab === 'ssh' && <SSHPanel />}
    </div>
  )
}

function Dashboard() {
  const [hosts, setHosts] = useState<Host[]>([])
  const [inv, setInv] = useState<Inventory[]>([])
  const [pb, setPb] = useState<Playbook[]>([])
  const [hist, setHist] = useState<ExecRecord[]>([])
  const [lastResult, setLastResult] = useState<AnsibleResult[] | null>(null)
  const [loading, setLoading] = useState(false)
  const [err, setErr] = useState('')

  useEffect(() => {
    getJSON<Host[]>('/api/ansible/hosts').then(setHosts).catch(() => {})
    getJSON<Inventory[]>('/api/ansible/inventories').then(setInv).catch(() => {})
    getJSON<Playbook[]>('/api/ansible/playbooks').then(d => setPb(d || [])).catch(() => {})
    getJSON<ExecRecord[]>('/api/ansible/history').then(h => setHist(h.slice(0,5))).catch(() => {})
  }, [])

  const pingAll = async () => {
    setLoading(true); setErr('')
    const d = await postJSON<AnsibleResult[]>('/api/ansible/ping', {}).catch(e => { setErr(e.message); return null })
    if (d) setLastResult(d)
    setLoading(false)
  }

  return (
    <div>
      <div className="grid grid-4">
        <div className="card"><div className="card-sub">主机</div><div style={{fontSize:'2rem',fontWeight:300,marginTop:4}}>{hosts.length}</div></div>
        <div className="card"><div className="card-sub">库存清单</div><div style={{fontSize:'2rem',fontWeight:300,marginTop:4}}>{inv.length}</div></div>
        <div className="card"><div className="card-sub">Playbook</div><div style={{fontSize:'2rem',fontWeight:300,marginTop:4}}>{pb.length}</div></div>
        <div className="card"><div className="card-sub">历史执行</div><div style={{fontSize:'2rem',fontWeight:300,marginTop:4}}>{hist.length}</div></div>
      </div>
      <div className="section">
        <h3>快速操作</h3>
        <div className="btn-row">
          <button className="btn" onClick={pingAll} disabled={loading}>Ping 全部主机</button>
        </div>
      </div>
      {err && <div className="error-box">{err}</div>}
      {lastResult && <ResultsCard results={lastResult} />}
      {hist.length > 0 && (
        <div className="section">
          <h3>最近执行</h3>
          {hist.map(r => (
            <div key={r.id} className="result-card" style={{borderLeftColor: r.success ? 'var(--ok)' : 'var(--danger)'}}>
              <div className="result-header">
                <span><strong>{r.type}</strong> {r.target} <span className="dim">({r.duration})</span></span>
                <span className={`badge ${r.success ? 'badge-on' : 'badge-off'}`}>{r.success ? '成功' : '失败'}</span>
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

function HostsPanel() {
  const { refreshHosts } = useHost()
  const [hosts, setHosts] = useState<Host[]>([])
  const [form, setForm] = useState({ id: '', addr: '', port: 22, user: 'root', alias: '', sshKey: '', password: '', platform: 'linux' })
  const [batchIp, setBatchIp] = useState('')
  const [batchUser, setBatchUser] = useState('root')
  const [batchPort, setBatchPort] = useState(22)
  const [batchPrefix, setBatchPrefix] = useState('')
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [err, setErr] = useState('')
  const [msg, setMsg] = useState('')
  const [testResult, setTestResult] = useState<{ok:boolean;error?:string} | null>(null)
  const [groupNames, setGroupNames] = useState<string[]>([])
  const [groupModal, setGroupModal] = useState<null | { group: string }>(null)
  const [status, setStatus] = useState<Record<string, boolean>>({})

  const load = useCallback(() => {
    getJSON<Host[]>('/api/ansible/hosts').then(setHosts).catch(() => {})
    getJSON<{groups: {name: string}[]}>('/api/ansible/host-groups').then(d => setGroupNames((d.groups || []).map(g => g.name))).catch(() => {})
  }, [])

  const loadStatus = useCallback(() => {
    getJSON<Record<string, { online: boolean }>>('/api/ansible/hosts/status').then(d => {
      const m: Record<string, boolean> = {}
      for (const [k, v] of Object.entries(d)) m[k] = v.online
      setStatus(m)
    }).catch(() => {})
  }, [])

  useEffect(() => {
    load(); loadStatus()
    const t = setInterval(() => { load(); loadStatus() }, 30000)
    return () => clearInterval(t)
  }, [load, loadStatus])

  const testHost = async () => {
    setTestResult(null)
    const d = await postJSON('/api/ansible/hosts/test', form).catch(e => { setTestResult({ok:false,error:e.message}); return null })
    if (d) setTestResult(d)
  }

  const addHost = async () => {
    setErr('')
    const d = await postJSON('/api/ansible/hosts/add', form).catch(e => { setErr(e.message); return null })
    if (d) { load(); setForm({ id: '', addr: '', port: 22, user: 'root', alias: '', sshKey: '', password: '', platform: 'linux' }); refreshHosts() }
  }

  const batchAdd = async () => {
    setErr(''); setMsg('')
    const d = await postJSON('/api/ansible/hosts/batch-add', { ipRange: batchIp, user: batchUser, port: batchPort, prefix: batchPrefix }).catch(e => { setErr(e.message); return null })
    if (d) { setMsg(`成功添加 ${d.added}/${d.total} 台主机`); load(); refreshHosts() }
  }

  const selectAll = () => setSelected(new Set(hosts.filter(h => h.id !== '').map(h => h.id)))
  const invertSelect = () => setSelected(new Set(hosts.filter(h => h.id !== '' && !selected.has(h.id)).map(h => h.id)))
  const clearSelect = () => setSelected(new Set())

  const addToGroup = async () => {
    if (!groupModal || selected.size === 0) return
    setErr(''); setMsg('')
    const g = groupModal.group.trim()
    if (!g) { setErr('请输入组名'); return }
    const d = await postJSON('/api/ansible/host-groups', { op: 'add', group: g, hostIds: Array.from(selected) }).catch(e => { setErr(e.message); return null })
    if (d) {
      setMsg(`已将 ${selected.size} 台主机加入组 ${g}`)
      setGroupModal(null); setSelected(new Set()); load()
    }
  }

  const removeSelected = async () => {
    if (selected.size === 0) return
    setErr('')
    const ids = Array.from(selected)
    const d = await postJSON('/api/ansible/hosts/remove', { ids }).catch(e => { setErr(e.message); return null })
    if (d) { setSelected(new Set()); load(); refreshHosts() }
  }

  const removeAll = async () => {
    setErr('')
    const d = await postJSON('/api/ansible/hosts/remove', { all: true }).catch(e => { setErr(e.message); return null })
    if (d) { setSelected(new Set()); load(); refreshHosts() }
  }

  const [editHost, setEditHost] = useState<Host | null>(null)

  const toggleSelect = (id: string) => {
    setSelected(p => { const next = new Set(p); if (next.has(id)) next.delete(id); else next.add(id); return next })
  }

  // 主机列表变化后, 清理已不存在主机的残留选中
  useEffect(() => {
    setSelected(p => {
      const ids = new Set(hosts.map(h => h.id))
      const next = new Set([...p].filter(id => ids.has(id)))
      return next.size === p.size ? p : next
    })
  }, [hosts])

  const selectableIds = hosts.filter(h => h.id !== '').map(h => h.id)
  const allChecked = selectableIds.length > 0 && selectableIds.every(id => selected.has(id))
  const allIndeterminate = selected.size > 0 && !allChecked

  const saveEdit = async () => {
    if (!editHost) return
    setErr('')
    const d = await postJSON('/api/ansible/hosts/update', editHost).catch(e => { setErr(e.message); return null })
    if (d) { setEditHost(null); load(); refreshHosts() }
  }

  return (
    <div>
      <div className="section">
        <h3>添加主机</h3>
        <div className="form-row">
          <input placeholder="ID" value={form.id} onChange={e => setForm(p => ({...p, id: e.target.value}))} style={{width:90}} />
          <input placeholder="IP 地址" value={form.addr} onChange={e => setForm(p => ({...p, addr: e.target.value}))} style={{width:120}} />
          <input type="number" placeholder="端口" value={form.port} onChange={e => setForm(p => ({...p, port: +e.target.value}))} style={{width:65}} />
          <select value={form.platform} onChange={e => { const pf = e.target.value; setForm(p => ({...p, platform: pf, port: pf === 'win' ? 5985 : 22})) }} style={{width:70}}>
            <option value="linux">Linux</option>
            <option value="win">Windows</option>
          </select>
          <input placeholder="用户" value={form.user} onChange={e => setForm(p => ({...p, user: e.target.value}))} style={{width:70}} />
          <input placeholder="别名" value={form.alias} onChange={e => setForm(p => ({...p, alias: e.target.value}))} style={{width:70}} />
          <input type="password" placeholder="密码" value={form.password} onChange={e => setForm(p => ({...p, password: e.target.value}))} style={{width:100}} />
          <input placeholder="SSH 私钥路径" value={form.sshKey} onChange={e => setForm(p => ({...p, sshKey: e.target.value}))} style={{width:120}} />
          <button className="btn" onClick={testHost} style={{fontSize:12}}>测试连接</button>
          <button className="btn" onClick={addHost}>添加</button>
        </div>
        {testResult !== null && (
          <div className={`banner ${testResult.ok ? 'banner-ok' : 'banner-err'}`} style={{marginTop:8}}>
            {testResult.ok ? '✓ 连接成功' : `✗ ${testResult.error}`}
          </div>
        )}
      </div>

      <div className="section">
        <h3>批量添加主机</h3>
        <div className="card" style={{marginBottom:12}}>
          <div style={{fontSize:'0.75rem',color:'var(--text-dim)',marginBottom:8}}>
            支持格式: IP 范围 (192.168.94.22-30) / CIDR 网段 (192.168.94.0/24) / 后缀列表 (192.168.94.22/23/24) / 逗号分隔 (10.2.22.1,10.2.22.2)
          </div>
          <div className="form-row">
            <input placeholder="IP 范围" value={batchIp} onChange={e => setBatchIp(e.target.value)} className="flex-1" />
            <input placeholder="用户名" value={batchUser} onChange={e => setBatchUser(e.target.value)} style={{width:120}} />
            <input type="number" placeholder="端口" value={batchPort} onChange={e => setBatchPort(+e.target.value)} style={{width:80}} />
            <input placeholder="ID 前缀(可选)" value={batchPrefix} onChange={e => setBatchPrefix(e.target.value)} style={{width:120}} />
            <button className="btn" onClick={batchAdd}>批量导入</button>
          </div>
        </div>
      </div>

      {msg && <div className="banner banner-ok">{msg}</div>}
      {err && <div className="error-box">{err}</div>}

      <div className="section">
        <div className="flex-between">
          <h3>主机列表 ({hosts.length})</h3>
          <div className="btn-row">
            {selected.size > 0 && <button className="btn btn-sm" onClick={() => setGroupModal({ group: '' })}>加入主机组 ({selected.size})</button>}
            {selected.size > 0 && <button className="btn btn-sm btn-danger" onClick={removeSelected}>删除选中 ({selected.size})</button>}
            {hosts.length > 0 && <button className="btn btn-sm" onClick={selectAll}>全选</button>}
            {hosts.length > 0 && <button className="btn btn-sm" onClick={invertSelect}>反选</button>}
            {selected.size > 0 && <button className="btn btn-sm" onClick={clearSelect}>清空选择</button>}
            {hosts.length > 0 && <button className="btn btn-sm" onClick={removeAll}>清空全部</button>}
          </div>
        </div>
        <div className="table-wrap">
          <table className="data-table" style={{tableLayout:'auto'}}>
            <thead>
              <tr>
                <th style={{width:30}}><CheckboxAll checked={allChecked} indeterminate={allIndeterminate} onChange={v => setSelected(v ? new Set(selectableIds) : new Set())} /></th>
                <th>ID</th><th>别名</th><th>地址</th><th>状态</th><th>平台</th><th>主机组</th><th>端口</th><th>主机名 / 用户</th><th>SSH 密钥</th><th></th>
              </tr>
            </thead>
            <tbody>
              {hosts.map(h => {
                const gs = h.groups || []
                const isLocal = h.isLocal || h.id === ''
                return (
                <tr key={h.id} className={selected.has(h.id) ? 'row-selected' : ''} style={{cursor:'pointer', background: isLocal ? 'rgba(100,120,255,0.06)' : undefined}} onClick={() => { if (!isLocal) toggleSelect(h.id) }}>
                  <td>{isLocal ? <span style={{fontSize:'0.6875rem',color:'var(--text-dim)'}}>—</span> : <input type="checkbox" checked={selected.has(h.id)} onChange={() => toggleSelect(h.id)} onClick={e => e.stopPropagation()} />}</td>
                  <td className="mono">{isLocal ? <span className="badge badge-info" style={{fontSize:10}}>本机</span> : h.id}</td>
                  <td>{isLocal ? (h.alias || '本机') : h.alias}</td>
                  <td className="mono">{h.addr}</td>
                  <td>
                    {isLocal
                      ? <span className="status-dot online" title="本机" />
                      : status[h.id] === undefined
                        ? <span className="status-dot pending" title="检测中" />
                        : <span className={`status-dot ${status[h.id] ? 'online' : 'offline'}`} title={status[h.id] ? '在线' : '离线'} />}
                  </td>
                  <td>{isLocal ? <span className="badge" style={{fontSize:10}}>{h.platform === 'win' ? 'Windows' : 'Linux'}</span> : <span className="badge badge-ghost" style={{fontSize:10}}>{h.platform === 'win' ? 'Windows' : 'Linux'}</span>}</td>
                  <td>
                    {gs.length === 0 ? <span style={{fontSize:'0.75rem',color:'var(--text-dim)'}}>-</span> : (
                      <span>
                        {gs.map((g, i) => (
                          <span key={i} className="badge badge-info" style={{fontSize:'0.625rem',marginRight:4}}>{g}</span>
                        ))}
                      </span>
                    )}
                  </td>
                  <td>{isLocal ? '-' : h.port}</td>
                  <td title={h.user || '本机无需用户'}>{h.hostname || h.user || '-'}</td>
                  <td style={{fontSize:'0.75rem',color:'var(--text-dim)'}}>{h.sshKey || h.password ? '*' : '-'}</td>
                  <td>{!isLocal && <span className="btn btn-sm btn-ghost" style={{fontSize:'0.6875rem',padding:'0.125rem 0.5rem'}} onClick={e => { e.stopPropagation(); setEditHost({...h}) }}>编辑</span>}</td>
                </tr>
                )
              })}
              {hosts.length === 0 && <tr><td colSpan={10} style={{textAlign:'center',padding:24,color:'var(--text-dim)'}}>暂无主机，请先添加</td></tr>}
            </tbody>
          </table>
        </div>
      </div>

      {groupModal && (
        <div className="modal-overlay" onClick={() => setGroupModal(null)}>
          <div className="modal" onClick={e => e.stopPropagation()}>
            <h3>将 {selected.size} 台主机加入主机组</h3>
            <div className="form-row">
              <label>组名</label>
              <input className="input" value={groupModal.group} onChange={e => setGroupModal({...groupModal, group: e.target.value})} placeholder="输入组名，不存在时自动创建" style={{flex:1}} list="host-group-list" />
              <datalist id="host-group-list">
                {groupNames.map(g => <option key={g} value={g} />)}
              </datalist>
            </div>
            <div className="btn-row" style={{marginTop:12}}>
              <button className="btn" onClick={addToGroup} disabled={!groupModal.group.trim()}>加入</button>
              <button className="btn" onClick={() => setGroupModal(null)}>取消</button>
            </div>
          </div>
        </div>
      )}

      {editHost && (
        <div className="modal-overlay" onClick={() => setEditHost(null)}>
          <div className="modal" onClick={e => e.stopPropagation()}>
            <h3>编辑主机 {editHost.id}</h3>
            <div className="form-row">
              <label>别名</label>
              <input className="input" value={editHost.alias} onChange={e => setEditHost({...editHost, alias: e.target.value})} />
            </div>
            <div className="form-row">
              <label>地址</label>
              <input className="input" value={editHost.addr} onChange={e => setEditHost({...editHost, addr: e.target.value})} />
            </div>
            <div className="form-row">
              <label>端口</label>
              <input className="input" type="number" value={editHost.port} onChange={e => setEditHost({...editHost, port: +e.target.value})} />
            </div>
            <div className="form-row">
              <label>平台</label>
              <select className="input" value={editHost.platform === 'win' ? 'win' : 'linux'} onChange={e => setEditHost({...editHost, platform: e.target.value})}>
                <option value="linux">Linux</option>
                <option value="win">Windows</option>
              </select>
            </div>
            <div className="form-row">
              <label>用户</label>
              <input className="input" value={editHost.user} onChange={e => setEditHost({...editHost, user: e.target.value})} />
            </div>
            <div className="form-row">
              <label>SSH 私钥路径</label>
              <input className="input" value={editHost.sshKey || ''} placeholder="如 ~/.ssh/id_rsa" onChange={e => setEditHost({...editHost, sshKey: e.target.value})} />
            </div>
            <div className="form-row">
              <label>密码</label>
              <input className="input" type="password" value={editHost.password || ''} placeholder="私钥为空时使用密码" onChange={e => setEditHost({...editHost, password: e.target.value})} />
            </div>
            <div className="btn-row" style={{marginTop:12}}>
              <button className="btn" onClick={saveEdit}>保存</button>
              <button className="btn" onClick={() => setEditHost(null)}>取消</button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}

function InventoriesPanel() {
  const [inventories, setInventories] = useState<Inventory[]>([])
  const [selected, setSelected] = useState<Inventory | null>(null)
  const [form, setForm] = useState({ id: '', name: '', description: '' })
  const [iniContent, setIniContent] = useState('')
  const [err, setErr] = useState('')
  const [msg, setMsg] = useState('')
  const [newGroup, setNewGroup] = useState('')
  const [newGroupParent, setNewGroupParent] = useState('')
  const [newGroupVars, setNewGroupVars] = useState('')
  const [allHosts, setAllHosts] = useState<Host[]>([])

  const load = useCallback(() => {
    getJSON<Inventory[]>('/api/ansible/inventories').then(setInventories).catch(() => {})
    getJSON<Host[]>('/api/ansible/hosts').then(setAllHosts).catch(() => {})
  }, [])

  useEffect(() => { load() }, [load])

  const create = async () => {
    setErr('')
    const d = await postJSON('/api/ansible/inventory/create', form).catch(e => { setErr(e.message); return null })
    if (d) { load(); setForm({ id: '', name: '', description: '' }) }
  }

  const view = async (id: string) => {
    setErr('')
    const d = await getJSON<Inventory>(`/api/ansible/inventory/get?id=${id}`).catch(e => { setErr(e.message); return null })
    if (d) {
      setSelected(d)
      const ini = await getJSON<{ini:string}>(`/api/ansible/inventory/render?id=${id}`).catch(() => null)
      if (ini) setIniContent(ini.ini)
    }
  }

  const del = async (id: string) => {
    setErr('')
    await postJSON('/api/ansible/inventory/delete', { id }).catch(e => { setErr(e.message); return null })
    setSelected(null); load()
  }

  const addGroup = async () => {
    if (!selected || !newGroup) return
    selected.groups[newGroup] = { name: newGroup, parent: newGroupParent, vars: {} }
    if (newGroupVars) {
      try { selected.groups[newGroup].vars = JSON.parse(newGroupVars) } catch {}
    }
    setSelected({...selected})
    setNewGroup(''); setNewGroupParent(''); setNewGroupVars('')
  }

  const importHosts = async () => {
    if (!selected) return; setErr(''); setMsg('')
    const entries: HostEntry[] = allHosts.filter(h => !selected.hosts[h.id]).map(h => ({
      id: h.id, addr: h.addr, port: h.port, user: h.user, alias: h.alias, sshKey: h.sshKey,
      groups: ['all'], vars: {},
    }))
    if (entries.length === 0) { setMsg('所有主机已在清单中'); return }
    const d = await postJSON('/api/ansible/inventory/import-hosts', { inventoryId: selected.id, hosts: entries }).catch(e => { setErr(e.message); return null })
    if (d) { setMsg(`成功导入 ${d.added} 台主机`); view(selected.id) }
  }

  const addInvHost = async (hostId: string) => {
    if (!selected) return; setErr(''); setMsg('')
    const d = await postJSON('/api/ansible/inventory/host-add', { inventoryId: selected.id, hostId, groups: ['all'] }).catch(e => { setErr(e.message); return null })
    if (d) { setMsg(`已添加 ${hostId}`); view(selected.id) }
  }

  const removeInvHost = async (hostId: string) => {
    if (!selected) return; setErr(''); setMsg('')
    const d = await postJSON('/api/ansible/inventory/host-remove', { inventoryId: selected.id, hostId }).catch(e => { setErr(e.message); return null })
    if (d) { setMsg(`已移除 ${hostId}`); view(selected.id) }
  }

  const saveInventory = async () => {
    if (!selected) return; setErr(''); setMsg('')
    const d = await postJSON('/api/ansible/inventory/save', selected).catch(e => { setErr(e.message); return null })
    if (d) { setMsg('清单已保存'); load() }
  }

  return (
    <div>
      <div className="section">
        <h3>创建库存清单</h3>
        <div className="form-row">
          <input placeholder="ID" value={form.id} onChange={e => setForm(p => ({...p, id: e.target.value}))} />
          <input placeholder="名称" value={form.name} onChange={e => setForm(p => ({...p, name: e.target.value}))} />
          <input placeholder="描述" value={form.description} onChange={e => setForm(p => ({...p, description: e.target.value}))} />
          <button className="btn" onClick={create}>创建</button>
        </div>
      </div>

      {err && <div className="error-box">{err}</div>}

      <div className="section">
        <h3>库存清单 ({inventories.length})</h3>
        {inventories.map(inv => (
          <div key={inv.id} className="plugin-row" style={{cursor:'pointer'}} onClick={() => view(inv.id)}>
            <div className="plugin-info">
              <span className="plugin-name">{inv.name || inv.id}</span>
              <span className="plugin-desc">{inv.description}</span>
            </div>
            <div className="btn-row">
              <span className="badge badge-info">{Object.keys(inv.groups).length} 组</span>
              <span className="badge badge-on">{Object.keys(inv.hosts).length} 主机</span>
              <button className="btn btn-sm btn-danger" onClick={e => { e.stopPropagation(); del(inv.id) }}>删除</button>
            </div>
          </div>
        ))}
        {inventories.length === 0 && <div className="loading">暂无库存清单</div>}
      </div>

      {selected && (
        <div className="section">
          <div className="flex-between">
            <h3>{selected.name || selected.id}</h3>
            <div className="btn-row">
              <button className="btn btn-sm" onClick={importHosts}>导入全部主机</button>
              <button className="btn btn-sm btn-accent" onClick={saveInventory}>保存修改</button>
            </div>
          </div>

          {err && <div className="alert-err">{err}</div>}
          {msg && <div className="alert-ok">{msg}</div>}

          <div className="card" style={{marginBottom:12}}>
            <div style={{fontSize:'0.8125rem',fontWeight:600,marginBottom:8}}>添加分组</div>
            <div className="form-row">
              <input placeholder="组名" value={newGroup} onChange={e => setNewGroup(e.target.value)} />
              <input placeholder="父组(可选)" value={newGroupParent} onChange={e => setNewGroupParent(e.target.value)} />
              <input placeholder='变量 JSON (可选) {"key":"val"}' value={newGroupVars} onChange={e => setNewGroupVars(e.target.value)} style={{flex:1}} />
              <button className="btn btn-sm" onClick={addGroup}>添加</button>
            </div>
          </div>

          <div style={{display:'grid',gridTemplateColumns:'1fr 1fr',gap:'1rem',marginBottom:16}}>
            <div className="card">
              <div style={{fontSize:'0.8125rem',fontWeight:600,marginBottom:8}}>分组</div>
              {Object.values(selected.groups).map(g => (
                <div key={g.name} style={{display:'flex',justifyContent:'space-between',padding:'0.25rem 0',fontSize:'0.8125rem',borderBottom:'1px solid var(--border)'}}>
                  <span><strong>{g.name}</strong>{g.parent ? <span className="dim"> → {g.parent}</span> : ''}</span>
                  {g.vars && Object.keys(g.vars).length > 0 && <span className="dim" style={{fontSize:11}}>{JSON.stringify(g.vars)}</span>}
                </div>
              ))}
            </div>
            <div className="card">
              <div style={{fontSize:'0.8125rem',fontWeight:600,marginBottom:8}}>主机 ({Object.keys(selected.hosts).length})</div>
              {Object.values(selected.hosts).map(h => (
                <div key={h.id} style={{display:'flex',justifyContent:'space-between',alignItems:'center',padding:'0.25rem 0',fontSize:'0.8125rem',borderBottom:'1px solid var(--border)'}}>
                  <div>
                    <span className="mono">{h.alias || h.id}</span>
                    <span className="dim" style={{marginLeft:8}}>{h.addr}:{h.port}</span>
                    <span style={{marginLeft:'0.5rem',fontSize:'0.6875rem',color:'var(--accent)'}}>{h.groups?.join(', ')}</span>
                  </div>
                  <button className="btn btn-sm btn-danger" style={{fontSize:'0.6875rem',padding:'0.125rem 0.5rem'}} onClick={() => { if (confirm(`从清单移除 ${h.id}？`)) removeInvHost(h.id) }}>移除</button>
                </div>
              ))}
              {Object.keys(selected.hosts).length === 0 && <div className="dim" style={{fontSize:'0.75rem',padding:'0.5rem 0'}}>暂无主机</div>}
            </div>
          </div>

          <div className="card" style={{marginBottom:12}}>
            <div style={{fontSize:'0.8125rem',fontWeight:600,marginBottom:8}}>从全局主机添加</div>
            <div className="table-wrap" style={{maxHeight:'12.5rem',overflow:'auto'}}>
              <table className="data-table" style={{tableLayout:'auto',fontSize:12}}>
                <thead>
                  <tr><th>ID</th><th>别名</th><th>地址</th><th style={{width:60}}></th></tr>
                </thead>
                <tbody>
                  {allHosts.filter(h => !selected.hosts[h.id]).map(h => (
                    <tr key={h.id}>
                      <td className="mono">{h.id}</td>
                      <td>{h.alias}</td>
                      <td className="mono">{h.addr}</td>
                      <td><button className="btn btn-sm" style={{fontSize:'0.6875rem',padding:'0.125rem 0.5rem'}} onClick={() => addInvHost(h.id)}>添加</button></td>
                    </tr>
                  ))}
                  {allHosts.filter(h => !selected.hosts[h.id]).length === 0 && <tr><td colSpan={4} style={{textAlign:'center',padding:12,color:'var(--text-dim)'}}>所有主机已在清单中</td></tr>}
                </tbody>
              </table>
            </div>
          </div>

          <div className="section">
            <h3>Inventory INI 预览</h3>
            <pre className="code-block" style={{maxHeight:'18.75rem',fontSize:11}}>{iniContent}</pre>
          </div>
        </div>
      )}
    </div>
  )
}

interface GlobalGroup { name: string; parent?: string; children?: string[]; members: Host[] }
interface HostGroupData { groups: GlobalGroup[]; ungrouped: Host[]; total: number }

function GroupsPanel() {
  const [allHosts, setAllHosts] = useState<Host[]>([])
  const [data, setData] = useState<HostGroupData>({ groups: [], ungrouped: [], total: 0 })
  const [selGroup, setSelGroup] = useState('')
  const [err, setErr] = useState('')
  const [msg, setMsg] = useState('')
  const [newGroup, setNewGroup] = useState('')
  const [newGroupParent, setNewGroupParent] = useState('')
  const [newHosts, setNewHosts] = useState<Set<string>>(new Set())

  const reload = useCallback(() => {
    getJSON<HostGroupData>('/api/ansible/host-groups').then(setData).catch(() => {})
    getJSON<Host[]>('/api/ansible/hosts').then(setAllHosts).catch(() => {})
  }, [])
  useEffect(() => { reload() }, [reload])

  const sel = data.groups.find(g => g.name === selGroup)
  const members = sel ? sel.members : []

  const addGroup = async () => {
    const name = newGroup.trim()
    if (!name) { setErr('请填写组名'); return }
    setErr(''); setMsg('')
    const d = await postJSON('/api/ansible/host-groups', { op: 'create', group: name, parent: newGroupParent.trim() }).catch(e => { setErr(e.message); return null })
    if (d) { setMsg(`已创建组 ${name}`); setNewGroup(''); setNewGroupParent(''); reload() }
  }

  const deleteGroup = async () => {
    if (!selGroup || !confirm(`删除组 ${selGroup}？组内主机将变为未分组`)) return
    setErr(''); setMsg('')
    const d = await postJSON('/api/ansible/host-groups', { op: 'remove', group: selGroup }).catch(e => { setErr(e.message); return null })
    if (d) { setMsg(`已删除组 ${selGroup}`); setSelGroup(''); reload() }
  }

  const addMembers = async () => {
    if (!selGroup || newHosts.size === 0) return
    setErr(''); setMsg('')
    const d = await postJSON('/api/ansible/host-groups', { op: 'add', group: selGroup, hostIds: Array.from(newHosts) }).catch(e => { setErr(e.message); return null })
    if (d) { setMsg(`已加入 ${newHosts.size} 台主机到 ${selGroup}`); setNewHosts(new Set()); reload() }
  }

  const removeMember = async (hostID: string) => {
    if (!selGroup) return
    setErr(''); setMsg('')
    const d = await postJSON('/api/ansible/host-groups', { op: 'del', group: selGroup, hostIds: [hostID] }).catch(e => { setErr(e.message); return null })
    if (d) { setMsg(`已从 ${selGroup} 组移除 ${hostID}`); reload() }
  }

  const toggleNewHost = (id: string) => {
    setNewHosts(p => { const n = new Set(p); if (n.has(id)) n.delete(id); else n.add(id); return n })
  }

  return (
    <div>
      {msg && <div className="banner banner-ok">{msg}</div>}
      {err && <div className="error-box">{err}</div>}

      <div className="section">
        <h3>主机组 ({data.groups.length} 组 · 共 {data.total} 台主机 · 未分组 {data.ungrouped.length} 台)</h3>
        <div className="table-wrap" style={{maxHeight:'20rem',overflow:'auto'}}>
          <table className="data-table" style={{tableLayout:'auto',fontSize:13}}>
            <thead>
              <tr><th>组名</th><th>父组</th><th>成员数</th><th style={{width:60}}></th></tr>
            </thead>
            <tbody>
              <tr className="row-selected" style={{opacity:0.8}}>
                <td><strong>all</strong> <span className="dim" style={{fontSize:11}}>(隐式根组)</span></td>
                <td>-</td>
                <td>{data.total}</td>
                <td></td>
              </tr>
              {data.groups.map(g => (
                <tr key={g.name} className={selGroup === g.name ? 'row-selected' : ''} style={{cursor:'pointer'}} onClick={() => setSelGroup(g.name)}>
                  <td><strong>{g.name}</strong>{g.children && g.children.length > 0 ? <span className="dim" style={{fontSize:11}}> (子组: {g.children.join(', ')})</span> : ''}</td>
                  <td>{g.parent || '-'}</td>
                  <td>{g.members.length}</td>
                  <td><span className="btn btn-sm btn-danger" style={{fontSize:'0.6875rem',padding:'0.125rem 0.5rem'}} onClick={e => { e.stopPropagation(); deleteGroup() }}>删除</span></td>
                </tr>
              ))}
              {data.groups.length === 0 && <tr><td colSpan={4} style={{textAlign:'center',padding:24,color:'var(--text-dim)'}}>暂无主机组，先在下方创建</td></tr>}
            </tbody>
          </table>
        </div>
      </div>

      <div className="section">
        <h3>新建主机组</h3>
        <div className="form-row">
          <input placeholder="组名" value={newGroup} onChange={e => setNewGroup(e.target.value)} style={{width:140}} />
          <select className="input" style={{width:160}} value={newGroupParent} onChange={e => setNewGroupParent(e.target.value)}>
            <option value="">父组: all (根)</option>
            {data.groups.map(g => <option key={g.name} value={g.name}>父组: {g.name}</option>)}
          </select>
          <button className="btn" onClick={addGroup}>创建</button>
        </div>
      </div>

      {sel && (
        <div className="section">
          <div className="flex-between">
            <h3>{sel.name} — 成员 ({members.length})</h3>
            <div className="btn-row">
              {newHosts.size > 0 && <button className="btn btn-sm" onClick={addMembers}>添加选中 ({newHosts.size})</button>}
              <button className="btn btn-sm" onClick={() => setNewHosts(new Set())}>清空勾选</button>
            </div>
          </div>

          <div className="card" style={{marginBottom:12}}>
            <div style={{fontSize:'0.8125rem',fontWeight:600,marginBottom:8}}>从全部主机添加</div>
            <div className="table-wrap" style={{maxHeight:'12.5rem',overflow:'auto'}}>
              <table className="data-table" style={{tableLayout:'auto',fontSize:12}}>
                <thead><tr><th style={{width:30}}></th><th>ID</th><th>别名</th><th>地址</th><th>当前组</th></tr></thead>
                <tbody>
                  {allHosts.map(h => {
                    const inGroup = members.some(m => m.id === h.id)
                    return (
                      <tr key={h.id} className={inGroup ? 'row-selected' : ''} style={{cursor:'pointer'}} onClick={() => !inGroup && toggleNewHost(h.id)}>
                        <td><input type="checkbox" disabled={inGroup} checked={inGroup || newHosts.has(h.id)} onChange={() => !inGroup && toggleNewHost(h.id)} onClick={e => e.stopPropagation()} /></td>
                        <td className="mono">{h.id}</td>
                        <td>{h.alias}</td>
                        <td className="mono">{h.addr}</td>
                        <td>{inGroup ? <span className="badge badge-on">已在组内</span> : <span className="dim" style={{fontSize:11}}>{(h.groups || []).join(', ') || '未分组'}</span>}</td>
                      </tr>
                    )
                  })}
                </tbody>
              </table>
            </div>
          </div>

          <div className="card">
            <div style={{fontSize:'0.8125rem',fontWeight:600,marginBottom:8}}>成员列表</div>
            {members.length === 0 && <div className="dim" style={{fontSize:'0.75rem',padding:'0.5rem 0'}}>暂无成员</div>}
            {members.map(m => (
              <div key={m.id} style={{display:'flex',justifyContent:'space-between',alignItems:'center',padding:'0.25rem 0',fontSize:'0.8125rem',borderBottom:'1px solid var(--border)'}}>
                <div>
                  <span className="mono">{m.alias || m.id}</span>
                  <span className="dim" style={{marginLeft:8}}>{m.addr}:{m.port}</span>
                </div>
                <button className="btn btn-sm btn-danger" style={{fontSize:'0.6875rem',padding:'0.125rem 0.5rem'}} onClick={() => { if (confirm(`从 ${selGroup} 组移除 ${m.id}？`)) removeMember(m.id) }}>移除</button>
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  )
}

function PlaybooksPanel() {
  const [playbooks, setPlaybooks] = useState<Playbook[]>([])
  const [editing, setEditing] = useState<Playbook | null>(null)
  const [err, setErr] = useState('')
  const [inventories, setInventories] = useState<Inventory[]>([])
  const [templates, setTemplates] = useState<Template[]>([])
  const [showTmpl, setShowTmpl] = useState(false)
  const sse = useSSEExec()
  const [runInv, setRunInv] = useState('')
  const [runCheck, setRunCheck] = useState(false)
  const [runTags, setRunTags] = useState('')
  const [runExtraVars, setRunExtraVars] = useState('')
  const [runLimit, setRunLimit] = useState('')
  const [runForks, setRunForks] = useState(0)

  const load = useCallback(() => {
    getJSON<Playbook[]>('/api/ansible/playbooks').then(setPlaybooks).catch(() => {})
    getJSON<Inventory[]>('/api/ansible/inventories').then(setInventories).catch(() => {})
  }, [])

  useEffect(() => { load() }, [load])
  useEffect(() => { getJSON<Template[]>('/api/ansible/templates').then(setTemplates).catch(() => {}) }, [])

  const openNew = () => {
    const defaultContent = `---
# ──────────────────────────────────────────────
# Ansible Playbook 默认模板
# 使用方式: 删掉行首的 # 即可启用该功能
# 标注为 <必填> 的项请填入实际值
# ──────────────────────────────────────────────

- name: 初始化配置 · 通用初始化流程
  hosts: all                                     # <必填> 目标主机组, 如 web-servers / db-servers
  # remote_user: root                            # SSH 远程用户 (默认当前用户)
  # become: yes                                  # 是否提权执行
  # become_user: root                            # 提权到的用户
  # become_method: sudo                          # 提权方式 sudo / su / pbrun
  # gather_facts: no                             # 是否收集 facts (加速用)
  # serial: 10                                   # 分批执行, 每批 10 台
  # max_fail_percentage: 20                      # 允许最大失败比例

  vars:
    # ── 通用变量 ──
    app_user: admin                              # 应用用户名
    app_group: admin                             # 应用用户组
    app_dir: /opt/myapp                          # 应用安装目录
    data_dir: /data                              # 数据目录
    log_dir: /var/log/myapp                      # 日志目录
    # ntp_server: ntp.aliyun.com                 # NTP 服务器
    # dns_servers: [114.114.114.114, 8.8.8.8]   # DNS 服务器列表
    # http_proxy: http://proxy.example.com:8080  # HTTP 代理
    # no_proxy: localhost,127.0.0.1,.local       # 不走代理的地址

  tasks:
    # ── 1. 系统初始化 ──
    - name: 1.1 设置主机名
      # ansible.builtin.hostname:
      #   name: "{{ inventory_hostname }}"
      tags: [init, hostname]

    # - name: 1.2 配置 hosts 文件
    #   ansible.builtin.lineinfile:
    #     path: /etc/hosts
    #     line: "10.0.0.10 {{ ansible_hostname }}"
    #     create: yes
    #   tags: [init, hosts]

    - name: 1.3 设置时区为 Asia/Shanghai
      ansible.builtin.timezone:
        name: Asia/Shanghai
      tags: [init, timezone]

    # - name: 1.4 配置 NTP 同步
    #   ansible.builtin.ntp:
    #     server: "{{ ntp_server }}"
    #     enabled: yes
    #   tags: [init, ntp]

    # - name: 1.5 配置 DNS 服务器
    #   ansible.builtin.copy:
    #     dest: /etc/resolv.conf
    #     content: |
    #       nameserver {{ dns_servers[0] }}
    #       nameserver {{ dns_servers[1] }}
    #   tags: [init, dns]

    # ── 2. 系统调优 ──
    - name: 2.1 关闭 SELinux
      ansible.posix.selinux:
        state: disabled
      when: ansible_os_family == "RedHat"
      tags: [tuning, selinux]

    # - name: 2.2 调优内核参数
    #   ansible.posix.sysctl:
    #     name: "{{ item.key }}"
    #     value: "{{ item.value }}"
    #     sysctl_set: yes
    #     reload: yes
    #   loop:
    #     - { key: net.core.somaxconn, value: "65535" }
    #     - { key: net.ipv4.tcp_max_syn_backlog, value: "65535" }
    #     - { key: vm.swappiness, value: "10" }
    #     - { key: fs.file-max, value: "1000000" }
    #   tags: [tuning, sysctl]

    # - name: 2.3 调高文件描述符限制
    #   ansible.builtin.lineinfile:
    #     path: /etc/security/limits.conf
    #     line: "{{ item }}"
    #     create: yes
    #   loop:
    #     - "* soft nofile 1048576"
    #     - "* hard nofile 1048576"
    #   tags: [tuning, limits]

    # ── 3. 用户和组 ──
    - name: 3.1 创建用户组
      ansible.builtin.group:
        name: "{{ app_group }}"
        state: present
      tags: [user]

    - name: 3.2 创建用户
      ansible.builtin.user:
        name: "{{ app_user }}"
        group: "{{ app_group }}"
        shell: /bin/bash
        create_home: yes
      tags: [user]

    # - name: 3.3 配置 SSH 免密登录
    #   ansible.posix.authorized_key:
    #     user: "{{ app_user }}"
    #     key: "{{ lookup('file', '~/.ssh/id_rsa.pub') }}"
    #   tags: [user, ssh]

    # ── 4. 目录结构 ──
    - name: 4.1 创建应用目录
      ansible.builtin.file:
        path: "{{ item }}"
        state: directory
        owner: "{{ app_user }}"
        group: "{{ app_group }}"
        mode: "0755"
      loop:
        - "{{ app_dir }}"
        - "{{ data_dir }}"
        - "{{ log_dir }}"
      tags: [dirs]

    # ── 5. 安装系统包 ──
    # - name: 5.1 安装基础工具 (RedHat)
    #   ansible.builtin.yum:
    #     name:
    #       - vim
    #       - git
    #       - curl
    #       - wget
    #       - htop
    #       - iotop
    #       - net-tools
    #       - bind-utils
    #       - lsof
    #       - jq
    #     state: present
    #   when: ansible_os_family == "RedHat"
    #   tags: [packages]

    # - name: 5.2 安装基础工具 (Debian)
    #   ansible.builtin.apt:
    #     name:
    #       - vim
    #       - git
    #       - curl
    #       - wget
    #       - htop
    #       - net-tools
    #       - dnsutils
    #       - lsof
    #       - jq
    #     state: present
    #   when: ansible_os_family == "Debian"
    #   tags: [packages]

    # ── 6. 防火墙配置 ──
    # - name: 6.1 开放应用端口 (firewalld)
    #   ansible.posix.firewalld:
    #     port: "{{ item }}"
    #     permanent: yes
    #     state: enabled
    #     immediate: yes
    #   loop:
    #     - 80/tcp    # HTTP
    #     - 443/tcp   # HTTPS
    #     - 8080/tcp  # 应用端口
    #   when: ansible_os_family == "RedHat"
    #   tags: [firewall]

    # ── 7. 应用部署 ──
    # - name: 7.1 拉取代码
    #   ansible.builtin.git:
    #     repo: https://github.com/example/myapp.git
    #     dest: "{{ app_dir }}"
    #     version: main
    #     force: yes
    #   tags: [deploy]

    # - name: 7.2 同步配置文件
    #   ansible.builtin.template:
    #     src: ./templates/app.conf.j2
    #     dest: "{{ app_dir }}/app.conf"
    #     owner: "{{ app_user }}"
    #     group: "{{ app_group }}"
    #     mode: "0644"
    #   notify: restart app
    #   tags: [deploy, config]

    # - name: 7.3 复制二进制文件
    #   ansible.builtin.copy:
    #     src: ./files/myapp
    #     dest: "{{ app_dir }}/myapp"
    #     owner: "{{ app_user }}"
    #     group: "{{ app_group }}"
    #     mode: "0755"
    #   notify: restart app
    #   tags: [deploy]

    # - name: 7.4 安装 Docker 容器
    #   community.docker.docker_container:
    #     name: myapp
    #     image: myapp:latest
    #     state: started
    #     restart_policy: always
    #     ports:
    #       - "8080:8080"
    #     env:
    #       APP_ENV: production
    #       DB_HOST: "{{ db_host }}"
    #   tags: [deploy, docker]

    # ── 8. 启动服务 ──
    # - name: 8.1 配置 systemd 服务
    #   ansible.builtin.template:
    #     src: ./templates/myapp.service.j2
    #     dest: /etc/systemd/system/myapp.service
    #     mode: "0644"
    #   notify: restart app
    #   tags: [service]

    # - name: 8.2 启动并启用服务
    #   ansible.builtin.systemd:
    #     name: myapp
    #     state: started
    #     enabled: yes
    #     daemon_reload: yes
    #   tags: [service]

    # ── 9. 文件操作 ──
    # - name: 9.1 下载文件
    #   ansible.builtin.get_url:
    #     url: https://example.com/package.tar.gz
    #     dest: /tmp/package.tar.gz
    #     mode: "0644"
    #   tags: [files]

    # - name: 9.2 解压文件
    #   ansible.builtin.unarchive:
    #     src: /tmp/package.tar.gz
    #     dest: /opt/
    #     remote_src: yes
    #   tags: [files]

    # ── 10. 健康检查 ──
    # - name: 10.1 等待端口可用
    #   ansible.builtin.wait_for:
    #     port: 8080
    #     host: 127.0.0.1
    #     delay: 5
    #     timeout: 60
    #   tags: [healthcheck]

    # - name: 10.2 HTTP 健康检查
    #   ansible.builtin.uri:
    #     url: http://127.0.0.1:8080/health
    #     return_content: yes
    #     status_code: 200
    #   register: health_result
    #   failed_when: '"ok" not in health_result.content'
    #   tags: [healthcheck]

  # ── handlers: 被 notify 触发 ──
  handlers:
    # - name: restart app
    #   ansible.builtin.systemd:
    #     name: myapp
    #     state: restarted
    #     daemon_reload: yes

    # - name: reload nginx
    #   ansible.builtin.service:
    #     name: nginx
    #     state: reloaded
`
    setEditing({ id: '', name: '', description: '', content: defaultContent })
  }

  const openEdit = (p: Playbook) => {
    getJSON<Playbook>(`/api/ansible/playbook/get?id=${p.id}`).then(setEditing).catch(() => {})
  }

  const save = async () => {
    if (!editing) return
    setErr('')
    const d = await postJSON('/api/ansible/playbook/save', editing).catch(e => { setErr(e.message); return null })
    if (d) { setEditing(null); load() }
  }

  const run = async (id: string) => {
    sse.exec('/api/ansible/sse/playbook', {
      playbookId: id, inventoryId: runInv, checkMode: runCheck, tags: runTags,
      extraVars: runExtraVars, limit: runLimit, forks: runForks,
    })
  }

  const del = async (id: string) => {
    setErr('')
    await postJSON('/api/ansible/playbook/delete', { id }).catch(e => { setErr(e.message); return null })
    load()
  }

  const createFromTemplate = async (tmpl: Template) => {
    const name = tmpl.id
    const d = await postJSON<Playbook>('/api/ansible/template/create', { templateId: tmpl.id, newId: name }).catch(e => { setErr(e.message); return null })
    if (d) { setShowTmpl(false); setEditing(d); load() }
  }

  return (
    <div>
      {showTmpl && (
        <div className="section">
          <div className="flex-between"><h3>Playbook 模板库</h3><button className="btn btn-sm" onClick={() => setShowTmpl(false)}>关闭</button></div>
          <div style={{display:'flex',flexWrap:'wrap',gap:'0.5rem',marginTop:8}}>
            {templates.map(t => (
              <div key={t.id} className="card" style={{flex:'1 1 16.25rem',cursor:'pointer'}} onClick={() => createFromTemplate(t)}>
                <div style={{fontSize:'0.625rem',color:'var(--theme)',marginBottom:2}}>{t.category}</div>
                <div style={{fontWeight:600}}>{t.name}</div>
                <div className="dim" style={{fontSize:'0.6875rem',marginTop:4}}>{t.description}</div>
              </div>
            ))}
          </div>
        </div>
      )}
      <div className="section">
        <div className="flex-between">
          <h3>Playbook ({playbooks.length})</h3>
          <div className="btn-row">
            <button className="btn" onClick={() => setShowTmpl(true)}>从模板新建</button>
            <button className="btn" onClick={openNew}>新建</button>
          </div>
        </div>
        {playbooks.map(p => (
          <div key={p.id} className="plugin-row" style={{flexDirection:'column',alignItems:'stretch',gap:6}}>
            <div style={{display:'flex',justifyContent:'space-between',alignItems:'center'}}>
              <span className="plugin-name">{p.name}</span>
              <div className="btn-row">
                <button className="btn btn-sm btn-accent" onClick={() => run(p.id)} disabled={sse.running}>{sse.running ? '执行中...' : '执行'}</button>
                <button className="btn btn-sm" onClick={() => openEdit(p)}>编辑</button>
                <button className="btn btn-sm btn-danger" onClick={() => del(p.id)}>删除</button>
              </div>
            </div>
            <div className="form-row" style={{fontSize:'0.75rem',gap:6}}>
              <select value={runInv} onChange={e => setRunInv(e.target.value)} className="sel" style={{minWidth:'6.25rem',fontSize:12}}>
                <option value="">默认清单</option>
                {inventories.map(i => <option key={i.id} value={i.id}>{i.name || i.id}</option>)}
              </select>
              <label className="chk" style={{fontSize:12}}><input type="checkbox" checked={runCheck} onChange={e => setRunCheck(e.target.checked)} /> check</label>
              <input placeholder="tags" title="标签过滤" value={runTags} onChange={e => setRunTags(e.target.value)} style={{width:'5rem',fontSize:12}} />
              <input placeholder="limit" title="目标限制 (host pattern)" value={runLimit} onChange={e => setRunLimit(e.target.value)} style={{width:'5rem',fontSize:12}} />
              <input type="number" placeholder="forks" title="并行进程数" value={runForks || ''} onChange={e => setRunForks(+e.target.value)} style={{width:'3.75rem',fontSize:12}} />
              <input placeholder="extra-vars" title='额外变量 JSON ({"key":"val"})' value={runExtraVars} onChange={e => setRunExtraVars(e.target.value)} style={{flex:1,fontSize:'0.75rem',minWidth:120}} />
            </div>
          </div>
        ))}
        {playbooks.length === 0 && <div className="loading">暂无 Playbook</div>}
      </div>

      {sse.err && <div className="error-box">{sse.err}</div>}
      {sse.running && <div className="loading"><button className="btn btn-sm btn-danger" style={{marginLeft:8}} onClick={sse.cancel}>取消</button></div>}
      {(sse.lines.length > 0 || sse.results) && (
        <div className="section">
          <div className="flex-between"><h3>执行输出</h3>{sse.running && <button className="btn btn-sm" onClick={sse.cancel}>取消</button>}</div>
          <div className="code-block" style={{whiteSpace:'pre-wrap',fontSize:'0.75rem',maxHeight:'25rem',overflow:'auto'}}>
            {sse.lines.join('\n')}
          </div>
          {sse.results && <ResultsCard results={sse.results} />}
        </div>
      )}

      {editing && (
        <div className="section">
          <div className="flex-between">
            <h3>{editing.id ? '编辑' : '新建'} Playbook</h3>
            <div className="btn-row">
              <button className="btn btn-sm" onClick={() => setEditing(null)}>取消</button>
              <button className="btn btn-sm btn-accent" onClick={save}>保存</button>
            </div>
          </div>
          <div className="form-row" style={{marginBottom:8}}>
            <input placeholder="名称" value={editing.name} onChange={e => setEditing(p => p ? {...p, name: e.target.value} : null)} style={{flex:1}} />
          </div>
          <textarea
            value={editing.content}
            onChange={e => setEditing(p => p ? {...p, content: e.target.value} : null)}
            className="editor-textarea"
          />
        </div>
      )}

    </div>
  )
}

function AdhocPanel() {
  const [module, setModule] = useState('shell')
  const [args, setArgs] = useState('uptime')
  const [hosts, setHosts] = useState<Host[]>([])
  const [selectedHosts, setSelectedHosts] = useState<Set<string>>(new Set())
  const [invId, setInvId] = useState('')
  const [inventories, setInventories] = useState<Inventory[]>([])
  const sse = useSSEExec()

  const load = useCallback(() => {
    getJSON<Host[]>('/api/ansible/hosts').then(setHosts).catch(() => {})
    getJSON<Inventory[]>('/api/ansible/inventories').then(setInventories).catch(() => {})
  }, [])

  useEffect(() => { load() }, [load])

  const modules = ['shell','command','setup','ping','copy','file','yum','apt','systemd','service','docker_container','synchronize','get_url','git','template','lineinfile','replace','user','group','mount','cron','debug','wait_for','stat','fetch','pip']

  const run = () => {
    sse.exec('/api/ansible/sse/adhoc', {
      module, args,
      hosts: selectedHosts.size > 0 ? Array.from(selectedHosts) : [],
      inventoryId: invId,
    })
  }

  const toggleHost = (id: string) => {
    setSelectedHosts(p => { const n = new Set(p); if (n.has(id)) n.delete(id); else n.add(id); return n })
  }

  const adhocAllChecked = hosts.length > 0 && hosts.every(h => selectedHosts.has(h.id))
  const adhocAllIndeterminate = selectedHosts.size > 0 && !adhocAllChecked

  return (
    <div>
      <div className="section">
        <h3>Ad-hoc 命令</h3>
        <div className="section">
          <h4>目标选择</h4>
          <div className="form-row" style={{marginBottom:8}}>
            <select value={invId} onChange={e => setInvId(e.target.value)} className="sel" style={{minWidth:140}}>
              <option value="">动态清单 (选主机)</option>
              {inventories.map(i => <option key={i.id} value={i.id}>{i.name || i.id}</option>)}
            </select>
            {!invId && (
              <span className="dim" style={{fontSize:12}}>已选 {selectedHosts.size} 台</span>
            )}
          </div>
          {!invId && (
            <div className="table-wrap" style={{maxHeight:'11.25rem',overflow:'auto',marginBottom:8}}>
              <table className="data-table" style={{tableLayout:'auto',fontSize:12}}>
                <thead>
                  <tr>
                    <th style={{width:28}}><CheckboxAll checked={adhocAllChecked} indeterminate={adhocAllIndeterminate} onChange={v => setSelectedHosts(v ? new Set(hosts.map(h => h.id)) : new Set())} /></th>
                    <th>ID</th><th>别名</th><th>地址</th>
                  </tr>
                </thead>
                <tbody>
                  {hosts.map(h => (
                    <tr key={h.id} style={{cursor:'pointer'}} onClick={() => toggleHost(h.id)}>
                      <td><input type="checkbox" checked={selectedHosts.has(h.id)} readOnly /></td>
                      <td className="mono">{h.id}</td>
                      <td>{h.alias}</td>
                      <td className="mono">{h.addr}:{h.port}</td>
                    </tr>
                  ))}
                  {hosts.length === 0 && <tr><td colSpan={4} style={{textAlign:'center',padding:16,color:'var(--text-dim)'}}>暂无主机</td></tr>}
                </tbody>
              </table>
            </div>
          )}
        </div>
        <div className="form-row">
          <select value={module} onChange={e => setModule(e.target.value)} className="sel" style={{minWidth:140}}>
            {modules.map(m => <option key={m} value={m}>{m}</option>)}
          </select>
          <input placeholder="参数 (如: uptime)" value={args} onChange={e => setArgs(e.target.value)} className="flex-1" />
          <button className="btn btn-accent" onClick={run} disabled={sse.running}>{sse.running ? '执行中...' : '执行'}</button>
        </div>
      </div>
      {sse.err && <div className="error-box">{sse.err}</div>}
      {sse.running && <div className="loading"><button className="btn btn-sm btn-danger" onClick={sse.cancel}>取消</button></div>}
      {(sse.lines.length > 0 || sse.results) && (
        <div className="section">
          <h3>执行输出</h3>
          <div className="code-block" style={{whiteSpace:'pre-wrap',fontSize:'0.75rem',maxHeight:'25rem',overflow:'auto'}}>
            {sse.lines.join('\n')}
          </div>
          {sse.results && <ResultsCard results={sse.results} />}
        </div>
      )}
    </div>
  )
}

function HistoryPanel() {
  const [history, setHistory] = useState<ExecRecord[]>([])
  const [expanded, setExpanded] = useState<string | null>(null)
  const sse = useSSEExec()

  const load = useCallback(() => {
    getJSON<ExecRecord[]>('/api/ansible/history').then(setHistory).catch(() => {})
  }, [])

  useEffect(() => { load() }, [load])

  const clear = async () => {
    await postJSON('/api/ansible/history/clear', {})
    setHistory([])
  }

  const rerun = async (id: string) => {
    const d = await postJSON<{type:string; run: RunContext}>('/api/ansible/history/rerun', { id }).catch(() => null)
    if (!d || !d.run) return
    if (d.type === 'playbook' && d.run.playbookId) {
      sse.exec('/api/ansible/sse/playbook', d.run)
    } else {
      sse.exec('/api/ansible/sse/adhoc', d.run)
    }
  }

  return (
    <div>
      <div className="flex-between">
        <h3>执行历史 ({history.length})</h3>
        {history.length > 0 && <button className="btn btn-sm" onClick={clear}>清空</button>}
      </div>
      {sse.err && <div className="error-box">{sse.err}</div>}
      {(sse.lines.length > 0 || sse.results) && (
        <div className="section">
          <div className="flex-between"><h3>重跑输出</h3>{sse.running && <button className="btn btn-sm" onClick={sse.cancel}>取消</button>}</div>
          <div className="code-block" style={{whiteSpace:'pre-wrap',fontSize:'0.75rem',maxHeight:'18.75rem',overflow:'auto'}}>{sse.lines.join('\n')}</div>
          {sse.results && <ResultsCard results={sse.results} />}
        </div>
      )}
      {history.map(r => (
        <div key={r.id} className="result-card" style={{marginBottom:8}}>
          <div className="result-header" style={{cursor:'pointer'}} onClick={() => setExpanded(expanded === r.id ? null : r.id)}>
            <span>
              <strong>{r.type.toUpperCase()}</strong>
              {r.target && <span className="mono" style={{marginLeft:8}}>{r.target}</span>}
              <span className="dim" style={{marginLeft:12}}>{new Date(r.time).toLocaleString()}</span>
              <span className="dim" style={{marginLeft:8}}>({r.duration})</span>
            </span>
            <div className="btn-row" style={{gap:4}}>
              {r.run && <button className="btn btn-sm" style={{fontSize:'0.625rem',padding:'0.125rem 0.5rem'}} onClick={e => { e.stopPropagation(); rerun(r.id) }}>重跑</button>}
              <span className={`badge ${r.success ? 'badge-on' : 'badge-off'}`}>{r.success ? '成功' : '失败'}</span>
            </div>
          </div>
          {expanded === r.id && (
            <div style={{padding:'0.5rem 0.875rem'}}>
              {r.results.map(res => (
                <div key={res.host} style={{marginBottom:'0.375rem',padding:8,background:'rgba(0,0,0,0.15)',borderRadius:'0.5rem',fontSize:12}}>
                  <div style={{display:'flex',alignItems:'center',gap:'0.5rem',marginBottom:4}}>
                    <strong>{res.host}</strong>
                    <span className={`badge ${res.success ? 'badge-on' : 'badge-off'}`} style={{fontSize:10}}>{res.success ? 'ok' : 'fail'}</span>
                    {res.changed && <span className="badge badge-warn" style={{fontSize:10}}>changed</span>}
                  </div>
                  <pre className="mono" style={{margin:0,fontSize:'0.6875rem',color:'var(--text-dim)',whiteSpace:'pre-wrap',wordBreak:'break-all',maxHeight:'12.5rem',overflow:'auto'}}>{res.output}</pre>
                </div>
              ))}
            </div>
          )}
        </div>
      ))}
      {history.length === 0 && <div className="loading">暂无执行记录</div>}
    </div>
  )
}

function SSHPanel() {
  const [keys, setKeys] = useState<SSHKeyPair[]>([])
  const [hosts, setHosts] = useState<Host[]>([])
  const [err, setErr] = useState('')
  const [msg, setMsg] = useState('')
  const [genName, setGenName] = useState('')
  const [deployModal, setDeployModal] = useState<{keyName:string;hostId:string;password:string} | null>(null)
  const [testing, setTesting] = useState<string | null>(null)

  const load = useCallback(() => {
    getJSON<SSHKeyPair[]>('/api/ansible/ssh/keys').then(setKeys).catch(() => {})
    getJSON<Host[]>('/api/ansible/hosts').then(setHosts).catch(() => {})
  }, [])

  useEffect(() => { load() }, [load])

  const generate = async () => {
    if (!genName.trim()) return
    setErr(''); setMsg('')
    const d = await postJSON('/api/ansible/ssh/generate', { name: genName.trim() }).catch(e => { setErr(e.message); return null })
    if (d) { setMsg('密钥生成成功'); setGenName(''); load() }
  }

  const delKey = async (name: string) => {
    setErr(''); setMsg('')
    await postJSON('/api/ansible/ssh/delete', { name }).catch(e => { setErr(e.message); return null })
    load()
  }

  const hostLookup = (id: string) => hosts.find(h => h.id === id)

  const deploy = async () => {
    if (!deployModal) return
    setErr(''); setMsg('')
    const h = hostLookup(deployModal.hostId)
    if (!h) { setErr('未找到主机'); return }
    const d = await postJSON('/api/ansible/ssh/deploy', {
      keyName: deployModal.keyName,
      host: h.addr,
      port: h.port,
      user: h.user,
      password: deployModal.password,
    }).catch(e => { setErr(e.message); return null })
    if (d) { setMsg('公钥部署成功'); setDeployModal(null); load() }
  }

  const testConn = async (name: string, hostId: string) => {
    const h = hostLookup(hostId)
    if (!h) { setErr('未找到主机'); return }
    setTesting(`${name}:${hostId}`); setErr(''); setMsg('')
    const d = await postJSON<{ok:boolean;message?:string}>('/api/ansible/ssh/test', {
      keyName: name,
      host: h.addr,
      port: String(h.port),
      user: h.user,
    }).catch(e => { setErr(e.message); return null })
    if (d) { setMsg(d.ok ? 'SSH 连接成功' : 'SSH 连接失败') }
    setTesting(null)
  }

  const bindKey = async (name: string, hostId: string) => {
    setErr(''); setMsg('')
    const d = await postJSON('/api/ansible/ssh/bind', { keyName: name, hostId }).catch(e => { setErr(e.message); return null })
    if (d) { setMsg('密钥绑定成功'); load() }
  }

  return (
    <div>
      <div className="flex-between"><h3>SSH 密钥管理</h3></div>
      {err && <div className="alert alert-err">{err}</div>}
      {msg && <div className="alert alert-ok">{msg}</div>}

      <div className="section">
        <h4>生成密钥对</h4>
        <div className="flex-between" style={{gap:8}}>
          <input className="input" placeholder="密钥名称（如：ansible-key）" value={genName} onChange={e => setGenName(e.target.value)}
            onKeyDown={e => e.key === 'Enter' && generate()} style={{flex:1}} />
          <button className="btn" onClick={generate}>生成 Ed25519 密钥</button>
        </div>
      </div>

      <div className="section">
        <h4>已生成密钥 ({keys.length})</h4>
        <div className="table-wrap">
          <table className="data-table">
            <thead>
              <tr>
                <th>名称</th><th>指纹</th><th>公钥</th><th>创建时间</th><th>操作</th>
              </tr>
            </thead>
            <tbody>
              {keys.map(k => (
                <tr key={k.name}>
                  <td><strong>{k.name}</strong></td>
                  <td className="mono" style={{fontSize:12}}>{k.fingerprint}</td>
                  <td className="mono" style={{fontSize:'0.6875rem',maxWidth:'18.75rem',overflow:'hidden',textOverflow:'ellipsis'}}>{k.publicKey}</td>
                  <td style={{fontSize:12}}>{new Date(k.createdAt).toLocaleString()}</td>
                  <td>
                    <div className="btn-row" style={{gap:'0.25rem',flexWrap:'wrap'}}>
                      <button className="btn btn-sm" onClick={() => setDeployModal({keyName:k.name,hostId:'',password:''})}>部署</button>
                      <button className="btn btn-sm" onClick={() => {
                        const hid = prompt('输入目标主机 ID：')
                        if (hid) testConn(k.name, hid.trim())
                      }} disabled={testing !== null}>测试连接</button>
                      <button className="btn btn-sm" onClick={() => {
                        const hid = prompt('输入目标主机 ID：')
                        if (hid) bindKey(k.name, hid.trim())
                      }}>绑定</button>
                      <button className="btn btn-sm btn-danger" onClick={() => { if (confirm('确定删除密钥 '+k.name+'？')) delKey(k.name) }}>删除</button>
                    </div>
                  </td>
                </tr>
              ))}
              {keys.length === 0 && <tr><td colSpan={5} style={{textAlign:'center',padding:24,color:'var(--text-dim)'}}>暂无密钥，请先生成</td></tr>}
            </tbody>
          </table>
        </div>
      </div>

      {deployModal && (
        <div className="modal-overlay" onClick={() => setDeployModal(null)}>
          <div className="modal" onClick={e => e.stopPropagation()}>
            <h3>部署公钥到主机</h3>
            <div className="form-row">
              <label>密钥：<strong>{deployModal.keyName}</strong></label>
            </div>
            <div className="form-row">
              <label>目标主机</label>
              <select className="input" value={deployModal.hostId} onChange={e => setDeployModal({...deployModal,hostId:e.target.value})}>
                <option value="">-- 选择主机 --</option>
                {hosts.map(h => <option key={h.id} value={h.id}>{h.alias || h.addr} ({h.id})</option>)}
              </select>
            </div>
            <div className="form-row">
              <label>SSH 密码</label>
              <input className="input" type="password" placeholder="目标主机的 SSH 密码" value={deployModal.password}
                onChange={e => setDeployModal({...deployModal,password:e.target.value})} />
            </div>
            <div className="btn-row" style={{marginTop:12}}>
              <button className="btn" onClick={deploy} disabled={!deployModal.hostId || !deployModal.password}>部署</button>
              <button className="btn" onClick={() => setDeployModal(null)}>取消</button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}

function ResultsCard({ results }: { results: AnsibleResult[] }) {
  return (
    <div className="section">
      <h3>执行结果</h3>
      {results.map(r => (
        <div key={r.host} className={`result-card ${r.success ? 'result-ok' : 'result-fail'}`}>
          <div className="result-header">
            <span><strong>{r.host}</strong></span>
            <span className={`badge ${r.success ? 'badge-on' : 'badge-off'}`}>{r.success ? '成功' : '失败'}</span>
          </div>
          <pre className="result-output">{r.output}</pre>
        </div>
      ))}
    </div>
  )
}
