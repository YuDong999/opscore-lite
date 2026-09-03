// ── Docker 管理视图: 内嵌侧栏布局 ──
// 容器 / 镜像(含 pull/rmi) / 镜像源(daemon.json) / 构建镜像(Dockerfile) / Compose / Swarm(只读)
// 全部操作走既有 RunOnTarget 分发体系, 写操作带回读验证/审计

import { useEffect, useMemo, useState } from 'react'
import Card from '../components/Card'
import { useHost } from '../components/HostContext'
import { getJSON, postJSON } from '../api/client'

// 迷你线性图标(16px, stroke 继承 currentColor)
type Section = 'containers' | 'images' | 'registries' | 'build' | 'compose' | 'swarm'

const SECTIONS: { key: Section; title: string; desc: string }[] = [
  { key: 'containers', title: '容器', desc: '启停 / 删除 / 日志 / 详情' },
  { key: 'images', title: '镜像', desc: '列表 / 拉取 / 删除' },
  { key: 'registries', title: '镜像源', desc: '加速地址 / insecure' },
  { key: 'build', title: '构建镜像', desc: 'Dockerfile 在线构建' },
  { key: 'compose', title: 'Compose', desc: '项目编排 up/down/ps' },
  { key: 'swarm', title: 'Swarm', desc: '集群管理' },
]

const SectionIcon = ({ name }: { name: string }) => {
  const common = { width: 15, height: 15, viewBox: '0 0 24 24', fill: 'none', stroke: 'currentColor', strokeWidth: 2, strokeLinecap: 'round' as const, strokeLinejoin: 'round' as const }
  switch (name) {
    case 'containers': return <svg {...common}><path d="M3 9h18M9 3v18M4.5 3h15A1.5 1.5 0 0 1 21 4.5v15a1.5 1.5 0 0 1-1.5 1.5h-15A1.5 1.5 0 0 1 3 19.5v-15A1.5 1.5 0 0 1 4.5 3z" /></svg>
    case 'images': return <svg {...common}><path d="M12 2l10 5-10 5L2 7l10-5z" /><path d="M2 17l10 5 10-5" /><path d="M2 12l10 5 10-5" /></svg>
    case 'registries': return <svg {...common}><circle cx="12" cy="12" r="10" /><path d="M2 12h20" /><path d="M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z" /></svg>
    case 'build': return <svg {...common}><path d="M14.7 6.3a1 1 0 0 0 0 1.4l1.6 1.6a1 1 0 0 0 1.4 0l3.77-3.77a6 6 0 0 1-7.94 7.94l-6.91 6.91a2.12 2.12 0 0 1-3-3l6.91-6.91a6 6 0 0 1 7.94-7.94l-3.76 3.76z" /></svg>
    case 'compose': return <svg {...common}><rect x="3" y="3" width="7" height="7" rx="1" /><rect x="14" y="3" width="7" height="7" rx="1" /><rect x="3" y="14" width="7" height="7" rx="1" /><rect x="14" y="14" width="7" height="7" rx="1" /></svg>
    default: return <svg {...common}><path d="M12 2l7 4v6c0 5-3.5 8.5-7 10-3.5-1.5-7-5-7-10V6l7-4z" /><path d="M12 8v4M12 16h.01" /></svg>
  }
}

interface AppContainer {
  id?: string; name: string; image: string; state: string
  runtime: string; ports?: string[]
}

export default function DockerModule({ onMsg }: { onMsg?: (m: string) => void }) {
  const [section, setSection] = useState<Section>('containers')
  return (
    <div className="k8s-shell">
      <aside className="k8s-side">
        <nav className="k8s-side-nav" style={{ paddingTop: 4 }}>
          {SECTIONS.map((sec) => (
            <div key={sec.key} className={`k8s-side-item ${sec.key === section ? 'active' : ''}`}
              onClick={() => setSection(sec.key)} style={{ gap: '0.5rem' }}>
              <span className="side-ico"><SectionIcon name={sec.key} /></span>
              <span style={{ display: 'flex', flexDirection: 'column', minWidth: 0 }}>
                <span style={{ fontWeight: sec.key === section ? 700 : 500 }}>{sec.title}</span>
                <span style={{ fontSize: '0.5625rem', opacity: 0.55 }}>{sec.desc}</span>
              </span>
            </div>
          ))}
        </nav>
      </aside>
      <section className="k8s-main" style={{ minWidth: 0, flex: 1, height: '100%', overflow: 'hidden', display: 'flex', flexDirection: 'column' }}>
        {section === 'containers' && <ContainersPanel onMsg={onMsg} />}
        {section === 'images' && <ImagesPanel onMsg={onMsg} />}
        {section === 'registries' && <RegistriesPanel onMsg={onMsg} />}
        {section === 'build' && <BuildPanel onMsg={onMsg} />}
        {section === 'compose' && <ComposePanel onMsg={onMsg} />}
        {section === 'swarm' && <SwarmPanel />}
      </section>
    </div>
  )
}

// ── 容器面板: 批量选择(全选/反选) + 启停删 + 创建/重建(端口/卷/env) ──

function ContainersPanel({ onMsg }: { onMsg?: (m: string) => void }) {
  const { selected } = useHost()
  const [list, setList] = useState<{ runtime: string; containers: AppContainer[] | null; note?: string } | null>(null)
  const [busy, setBusy] = useState(false)
  const [sel, setSel] = useState<Set<string>>(new Set())
  const [confirmAct, setConfirmAct] = useState<{ name: string; action: string } | null>(null)
  const [logView, setLogView] = useState<{ name: string; logs: string; target: string } | null>(null)
  const [runModal, setRunModal] = useState<{ recreateOf?: string } | null>(null)
  const [execView, setExecView] = useState<{ name: string } | null>(null)

  const hostQ = selected?.id ? `&host=${encodeURIComponent(selected.id)}` : ''
  const load = () => getJSON<any>(`/api/plugins/containers/list?_=${Date.now()}${hostQ}`).then(setList).catch(() => setList(null))
  useEffect(() => { load(); setSel(new Set()) }, [selected?.id])

  const rt = list?.runtime || ''
  const canWrite = rt === 'docker' || rt === 'podman'
  const containers = list?.containers || []
  const allChecked = containers.length > 0 && containers.every((c) => sel.has(c.name))

  const toggleAll = () => {
    if (allChecked) setSel(new Set())
    else setSel(new Set(containers.map((c) => c.name)))
  }
  const toggleInvert = () => {
    const next = new Set<string>()
    containers.forEach((c) => { if (!sel.has(c.name)) next.add(c.name) })
    setSel(next)
  }
  const toggleOne = (name: string) => {
    const next = new Set(sel)
    next.has(name) ? next.delete(name) : next.add(name)
    setSel(next)
  }

  const runAction = (name: string, action: string, after?: () => void) => {
    setConfirmAct(null); setBusy(true)
    postJSON('/api/plugins/containers/action', { host: selected?.id || '', name, runtime: rt, action })
      .then((d: any) => onMsg?.(d.ok
        ? `✓ ${action} ${name}${d.verified ? ' · 回读验证通过' : ' · ⚠ 回读验证未通过'}`
        : '✗ ' + (d.error || '操作失败')))
      .catch((e) => onMsg?.('✗ ' + String(e)))
      .finally(() => { setBusy(false); after?.(); setTimeout(load, 400); setTimeout(load, 2500) })
  }

  // 批量操作: 顺序执行避免并发冲突
  const batchAction = async (action: string) => {
    const names = [...sel]
    if (!names.length) return
    if (!confirm(`对 ${names.length} 个容器执行「${action}」?`)) return
    setBusy(true)
    let okN = 0, failN = 0
    for (const n of names) {
      try {
        const d = await postJSON('/api/plugins/containers/action', { host: selected?.id || '', name: n, runtime: rt, action })
        d.ok ? okN++ : failN++
      } catch { failN++ }
    }
    setBusy(false); setSel(new Set())
    onMsg?.(`✓ 批量${action}完成: 成功 ${okN} / 失败 ${failN}`)
    setTimeout(load, 400); setTimeout(load, 2500)
  }

  const openLogs = (c: AppContainer) => {
    getJSON<{ ok: boolean; logs: string; target: string }>(
      `/api/plugins/containers/logs?${hostQ.replace('&', '')}&runtime=${rt}&name=${encodeURIComponent(c.name)}&tail=300`)
      .then((d) => setLogView({ name: c.name, logs: d.logs || '(空)', target: d.target }))
      .catch((e) => onMsg?.('✗ ' + String(e)))
  }

  const openEdit = async (c: AppContainer) => {
    try {
      const d = await getJSON<{ ok: boolean; config: any; error?: string }>(
        `/api/plugins/containers/docker/container/config?${hostQ.replace('&', '')}&name=${encodeURIComponent(c.name)}`)
      if (!d.ok) throw new Error(d.error || '读取配置失败')
      setRunModal({ ...d.config, recreateOf: c.name })
    } catch (e: any) {
      onMsg?.('✗ ' + String(e.message || e))
    }
  }

  return (
    <>
      <Card className="containers-card" title={`容器列表 (${containers.length})`}
        subtitle={canWrite ? `${rt} · 支持批量操作与创建/重建(端口/卷挂载/环境变量)` : '当前运行时只读(K8s 托管或未检测到 docker/podman)'}>
        <div className="toolbar-strip">
          <button className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-accent" disabled={!canWrite}
            onClick={() => setRunModal({})}>+ 创建容器</button>
          <span className="dim">已选 {sel.size}</span>
          <button className="btn-glass-soft btn-glass-soft-sm" onClick={toggleAll}>{allChecked ? '取消全选' : '全选'}</button>
          <button className="btn-glass-soft btn-glass-soft-sm" onClick={toggleInvert}>反选</button>
          <button className="btn-glass-soft btn-glass-soft-sm" disabled={busy || !sel.size} onClick={() => batchAction('start')}>批量启动</button>
          <button className="btn-glass-soft btn-glass-soft-sm" disabled={busy || !sel.size} onClick={() => batchAction('stop')}>批量停止</button>
          <button className="btn-glass-soft btn-glass-soft-sm" disabled={busy || !sel.size} onClick={() => batchAction('restart')}>批量重启</button>
          <button className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-danger" disabled={busy || !sel.size} onClick={() => batchAction('remove')}>批量删除</button>
        </div>
        {list?.note && <div className="banner banner-warn">{list.note}</div>}
        <div className="table-wrap">
          <table className="data-table">
            <thead><tr>
              <th style={{ width: '4%' }}><input type="checkbox" checked={allChecked} onChange={toggleAll} /></th>
              <th style={{ width: '19%' }}>名称</th>
              <th style={{ width: '24%' }}>镜像</th>
              <th style={{ width: '9%' }}>状态</th>
              <th style={{ width: '16%' }}>端口</th>
              <th style={{ width: '28%' }}>操作</th>
            </tr></thead>
            <tbody>
              {containers.length === 0 && (
                <tr><td colSpan={6} className="dim">{list ? '（未检测到容器）' : '加载中…'}</td></tr>
              )}
              {containers.map((c) => (
                <tr key={c.name} style={{ cursor: 'pointer' }}
                  title="双击编辑配置并重建"
                  onDoubleClick={() => canWrite && openEdit(c)}>
                  <td onClick={(e) => e.stopPropagation()}><input type="checkbox" checked={sel.has(c.name)} onChange={() => toggleOne(c.name)} /></td>
                  <td className="mono">{c.name}</td>
                  <td className="mono dim">{c.image}</td>
                  <td><span className={`badge ${c.state === 'running' || c.state === 'CONTAINER_RUNNING' ? 'badge-ok' : c.state === 'exited' || c.state === 'CONTAINER_EXITED' ? 'badge-off' : 'badge-warn'}`}>{c.state}</span></td>
                  <td className="mono dim">{(c.ports || []).join(', ') || '—'}</td>
                  <td onClick={(e) => e.stopPropagation()}>
                    <div className="btn-row k8s-row-actions">
                      <button className="btn-glass-soft btn-glass-soft-sm" disabled={busy || !canWrite || c.state !== 'exited'} onClick={() => runAction(c.name, 'start')}>启动</button>
                      <button className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-danger" disabled={busy || !canWrite || c.state !== 'running'} onClick={() => runAction(c.name, 'stop')}>停止</button>
                      <button className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-accent" disabled={busy || !canWrite} onClick={() => runAction(c.name, 'restart')}>重启</button>
                      <button className="btn-glass-soft btn-glass-soft-sm" disabled={!canWrite} title="编辑配置并重建(端口/卷/环境变量)" onClick={() => openEdit(c)}>编辑</button>
                      <button className="btn-glass-soft btn-glass-soft-sm" disabled={!canWrite} onClick={() => setExecView({ name: c.name })}>命令</button>
                      <button className="btn-glass-soft btn-glass-soft-sm btn-ghost" disabled={!canWrite} onClick={() => openLogs(c)}>日志</button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </Card>

      {confirmAct && (
        <div className="modal-overlay" onClick={() => setConfirmAct(null)}>
          <div className="modal" onClick={(e) => e.stopPropagation()} style={{ maxWidth: 420 }}>
            <h3>确认执行: {confirmAct.action === 'remove' ? '删除容器(高危)' : confirmAct.action}</h3>
            <p>目标主机 <b>{selected?.label || '本机'}</b>, 容器 <b className="mono">{confirmAct.name}</b></p>
            <div className="modal-actions">
              <button className="btn-glass-soft" onClick={() => setConfirmAct(null)}>取消</button>
              <button className={`btn ${confirmAct.action === 'remove' || confirmAct.action === 'stop' ? 'btn-danger' : 'btn-accent'}`} disabled={busy}
                onClick={() => runAction(confirmAct.name, confirmAct.action)}>确认{confirmAct.action}</button>
            </div>
          </div>
        </div>
      )}

      {logView && (
        <div className="modal-overlay" onClick={() => setLogView(null)}>
          <div className="modal log-modal" onClick={(e) => e.stopPropagation()} style={{ maxWidth: 860 }}>
            <div className="modal-head">
              <div className="modal-title">日志: {logView.name} <span className="pill pill-sub">@ {logView.target} · 最近300行</span></div>
              <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => setLogView(null)}>关闭</button>
            </div>
            <pre className="code-block" style={{ margin: 0, maxHeight: 480, overflow: 'auto', whiteSpace: 'pre-wrap', fontSize: '0.6875rem' }}>{logView.logs}</pre>
          </div>
        </div>
      )}

      {execView && (
        <ExecModal name={execView.name} host={selected?.id || ''}
          onClose={() => setExecView(null)} />
      )}

      {runModal && (
        <ContainerRunModal
          initial={runModal}
          host={selected?.id || ''}
          onClose={() => setRunModal(null)}
          onDone={(ok, m) => { setRunModal(null); onMsg?.(m); if (ok) setTimeout(load, 500) }}
        />
      )}
    </>
  )
}

// ── 创建/重建容器弹窗: 端口映射 / 卷挂载(含单文件) / 环境变量 ──

interface PortRow { hostPort: string; ctrlPort: string; proto: 'tcp' | 'udp' }
interface VolRow { hostPath: string; ctrlPath: string; readOnly: boolean }
interface EnvRow { key: string; value: string }

function ContainerRunModal({ initial, host, onClose, onDone }: {
  initial: any
  host: string
  onClose: () => void
  onDone: (ok: boolean, msg: string) => void
}) {
  const isRecreate = !!initial.recreateOf
  const [name, setName] = useState<string>(initial.recreateOf || '')
  const [image, setImage] = useState(initial.image || '')
  const [restart, setRestart] = useState(initial.restart === '' ? 'no' : (initial.restart || 'no'))
  const [network, setNetwork] = useState(initial.network === 'bridge' || initial.network === 'host' || initial.network === 'none' ? initial.network : '')
  const [ports, setPorts] = useState<PortRow[]>(
    (initial.ports || []).map((p: any) => ({ hostPort: String(p.hostPort || p.HostPort || ''), ctrlPort: String(p.ctrlPort || p.CtrlPort || ''), proto: (p.proto || p.Proto || 'tcp') })))
  const [vols, setVols] = useState<VolRow[]>(
    (initial.volumes || []).map((v: any) => ({ hostPath: v.hostPath || v.HostPath || '', ctrlPath: v.ctrlPath || v.CtrlPath || '', readOnly: !!(v.readOnly ?? v.ReadOnly) })))
  const [envs, setEnvs] = useState<EnvRow[]>(
    (initial.envs || []).filter((e: any) => e.key).map((e: any) => ({ key: e.key, value: e.value })))
  const [busy, setBusy] = useState(false)

  const submit = () => {
    setBusy(true)
    postJSON('/api/plugins/containers/docker/container/run', {
      host,
      name: name.trim(),
      image: image.trim(),
      restart: restart === 'no' ? '' : restart,
      network,
      ports: ports.filter((p) => p.hostPort && p.ctrlPort),
      volumes: vols.filter((v) => v.hostPath && v.ctrlPath),
      envs: envs.filter((e) => e.key),
      recreateOf: isRecreate ? initial.recreateOf : '',
    })
      .then((d: any) => onDone(d.ok, d.ok ? `✓ ${isRecreate ? '重建' : '创建'} ${name} 成功` : '✗ ' + (d.error || '失败')))
      .catch((e) => onDone(false, '✗ ' + String(e)))
  }

  const rowStyle = { display: 'grid', gridTemplateColumns: '1fr 1fr 4.5rem auto', gap: '0.375rem', alignItems: 'center' } as const

  return (
    <div className="modal-overlay" onClick={onClose}>
      <div className="modal log-modal" onClick={(e) => e.stopPropagation()} style={{ maxWidth: 720 }}>
        <div className="modal-head">
          <div className="modal-title">
            {isRecreate ? `重建容器: ${initial.recreateOf}` : '创建容器'}
            {isRecreate && <span className="pill pill-sub">将先删除旧容器再按新配置运行</span>}
          </div>
          <button className="btn-glass-soft btn-glass-soft-sm" onClick={onClose}>关闭</button>
        </div>
        <div style={{ padding: '1rem 1.25rem', display: 'grid', gap: '0.875rem', maxHeight: '70vh', overflowY: 'auto' }}>
          <div className="form-row2">
            <label className="form-field"><span>容器名</span>
              <input className="input" value={name} onChange={(e) => setName(e.target.value)} disabled={isRecreate} placeholder="my-app" />
            </label>
            <label className="form-field"><span>镜像</span>
              <input className="input mono" value={image} onChange={(e) => setImage(e.target.value)} placeholder="nginx:1.27-alpine" style={{ fontSize: '0.75rem' }} />
            </label>
          </div>

          <div className="dim" style={{ fontSize: '0.6875rem', fontWeight: 700, letterSpacing: '0.04em' }}>端口映射</div>
          {ports.map((p, i) => (
            <div key={i} className="form-grid-row" style={{ gridTemplateColumns: rowStyle.gridTemplateColumns }}>
              <input className="input" placeholder="宿主端口" value={p.hostPort} onChange={(e) => setPorts(ports.map((x, j) => j === i ? { ...x, hostPort: e.target.value } : x))} />
              <input className="input" placeholder="容器端口" value={p.ctrlPort} onChange={(e) => setPorts(ports.map((x, j) => j === i ? { ...x, ctrlPort: e.target.value } : x))} />
              <select className="input" value={p.proto} onChange={(e) => setPorts(ports.map((x, j) => j === i ? { ...x, proto: e.target.value as 'tcp' | 'udp' } : x))} style={{ width: 76 }}>
                <option value="tcp">tcp</option><option value="udp">udp</option>
              </select>
              <button className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-danger" onClick={() => setPorts(ports.filter((_, j) => j !== i))}>×</button>
            </div>
          ))}
          <button className="btn-glass-soft btn-glass-soft-sm" style={{ justifySelf: 'start' }} onClick={() => setPorts([...ports, { hostPort: '', ctrlPort: '', proto: 'tcp' }])}>+ 端口</button>

          <div className="dim" style={{ fontSize: '0.6875rem', fontWeight: 700, letterSpacing: '0.04em' }}>卷挂载 / 文件挂载(宿主绝对路径)</div>
          {vols.map((v, i) => (
            <div key={i} className="form-grid-row" style={{ gridTemplateColumns: '1.4fr 1fr auto auto' }}>
              <input className="input mono" placeholder="/data/app 或 /etc/nginx/nginx.conf" value={v.hostPath} onChange={(e) => setVols(vols.map((x, j) => j === i ? { ...x, hostPath: e.target.value } : x))} />
              <input className="input mono" placeholder="/usr/share/nginx/html" value={v.ctrlPath} onChange={(e) => setVols(vols.map((x, j) => j === i ? { ...x, ctrlPath: e.target.value } : x))} />
              <label style={{ fontSize: '0.6875rem', whiteSpace: 'nowrap', display: 'flex', gap: 4, alignItems: 'center' }}>
                <input type="checkbox" checked={v.readOnly} onChange={(e) => setVols(vols.map((x, j) => j === i ? { ...x, readOnly: e.target.checked } : x))} />只读
              </label>
              <button className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-danger" onClick={() => setVols(vols.filter((_, j) => j !== i))}>×</button>
            </div>
          ))}
          <button className="btn-glass-soft btn-glass-soft-sm" style={{ justifySelf: 'start' }} onClick={() => setVols([...vols, { hostPath: '', ctrlPath: '', readOnly: false }])}>+ 挂载</button>

          <div className="dim" style={{ fontSize: '0.6875rem', fontWeight: 700, letterSpacing: '0.04em' }}>环境变量</div>
          {envs.map((en, i) => (
            <div key={i} className="form-grid-row" style={{ gridTemplateColumns: '1fr 2fr auto' }}>
              <input className="input mono" placeholder="KEY" value={en.key} onChange={(e) => setEnvs(envs.map((x, j) => j === i ? { ...x, key: e.target.value } : x))} />
              <input className="input mono" placeholder="value" value={en.value} onChange={(e) => setEnvs(envs.map((x, j) => j === i ? { ...x, value: e.target.value } : x))} />
              <button className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-danger" onClick={() => setEnvs(envs.filter((_, j) => j !== i))}>×</button>
            </div>
          ))}
          <button className="btn-glass-soft btn-glass-soft-sm" style={{ justifySelf: 'start' }} onClick={() => setEnvs([...envs, { key: '', value: '' }])}>+ 环境变量</button>

          <div className="form-row2">
            <label className="form-field"><span>重启策略</span>
              <select className="input" value={restart} onChange={(e) => setRestart(e.target.value)}>
                <option value="no">no(不自动重启)</option><option value="on-failure">on-failure</option>
                <option value="always">always</option><option value="unless-stopped">unless-stopped</option>
              </select>
            </label>
            <label className="form-field"><span>网络模式</span>
              <select className="input" value={network} onChange={(e) => setNetwork(e.target.value)}>
                <option value="">默认(bridge)</option><option value="host">host</option><option value="none">none</option>
              </select>
            </label>
          </div>

          <div className="modal-actions">
            <button className="btn-glass-soft" onClick={onClose}>取消</button>
            <button className="btn-glass-soft btn-glass-soft-accent" disabled={busy || !name.trim() || !image.trim()}
              onClick={submit}>{busy ? '执行中…' : isRecreate ? '确认重建' : '创建'}</button>
          </div>
        </div>
      </div>
    </div>
  )
}

// ── 镜像面板(列表 + 全选/反选 + 批量删除 + 拉取) ──

interface PullJobState {
  id: string; done: boolean; err: string; image: string
  lines: string[]; secs: number; layersDone: number; layersTotal: number
}

function ImagesPanel({ onMsg }: { onMsg?: (m: string) => void }) {
  const { selected } = useHost()
  const [images, setImages] = useState<{ repo: string; tag: string; id: string; size: string; key?: string }[]>([])
  const [pullImage, setPullImage] = useState('')
  const [busy, setBusy] = useState(false)
  const [out, setOut] = useState('')
  const [sel, setSel] = useState<Set<string>>(new Set())
  const [job, setJob] = useState<PullJobState | null>(null)

  // 轮询拉取进度
  useEffect(() => {
    if (!job || job.done) return
    const t = setInterval(() => {
      getJSON<PullJobState & { ok: boolean }>(`/api/plugins/containers/docker/pull/progress?id=${job.id}&_=${Date.now()}`)
        .then((d) => {
          if (!d.ok) { setJob(null); return }
          setJob({ ...d })
          if (d.done) {
            clearInterval(t)
            if (d.err) { onMsg?.('✗ 拉取失败: ' + d.err) } else { onMsg?.(`✓ 拉取完成: ${d.image} · ${d.secs}s`) }
            setTimeout(load, 400)
            setTimeout(() => setJob(null), 6000)
          }
        })
        .catch(() => {})
    }, 700)
    return () => clearInterval(t)
  }, [job?.id, job?.done])

  const hostQ = selected?.id ? `&host=${encodeURIComponent(selected.id)}` : ''
  const load = () => getJSON<{ images?: any[] }>(`/api/plugins/containers/images?_=${Date.now()}${hostQ}`)
    .then((d) => {
      const list = (d.images || []).map((im: any) => ({ ...im, key: `${im.repo}:${im.tag}`.replace(/:$/, '') }))
      setImages(list)
      setSel((prev) => new Set([...prev].filter((k) => list.some((x: any) => x.key === k))))
    }).catch(() => setImages([]))
  useEffect(() => { load(); setSel(new Set()) }, [selected?.id])

  const allChecked = images.length > 0 && images.every((im) => sel.has(im.key))
  const toggleAll = () => setSel(allChecked ? new Set() : new Set(images.map((im) => im.key)))
  const toggleInvert = () => setSel(new Set(images.filter((im) => !sel.has(im.key)).map((im) => im.key)))
  const toggleOne = (k: string) => {
    const next = new Set(sel)
    next.has(k) ? next.delete(k) : next.add(k)
    setSel(next)
  }

  const startPull = () => {
    const img = pullImage.trim()
    if (!img) return
    setBusy(true); setOut(''); setSel(new Set())
    postJSON('/api/plugins/containers/docker/pull/async', { host: selected?.id || '', image: img })
      .then((d: any) => {
        if (d.ok) {
          setJob({ id: d.jobId, done: false, err: '', image: img, lines: [], secs: 0, layersDone: 0, layersTotal: 0 })
        } else {
          onMsg?.('✗ ' + (d.error || '发起失败'))
        }
      })
      .catch((e) => onMsg?.('✗ ' + String(e)))
      .finally(() => setBusy(false))
  }

  const removeOne = (image: string) => {
    setBusy(true); setOut('')
    postJSON('/api/plugins/containers/docker/image/action', { host: selected?.id || '', image, action: 'remove' })
      .then((d: any) => {
        if (d.ok) { onMsg?.(`✓ 删除完成: ${image}`); setTimeout(load, 500) }
        else { onMsg?.('✗ ' + (d.error || '失败')); setOut(d.error || '') }
      })
      .catch((e) => { onMsg?.('✗ ' + String(e)); setOut(String(e)) })
      .finally(() => setBusy(false))
  }

  const batchRemove = async () => {
    const keys = [...sel]
    if (!keys.length || !confirm(`删除 ${keys.length} 个镜像?`)) return
    setBusy(true)
    let okN = 0, failN = 0
    for (const k of keys) {
      try {
        const d = await postJSON('/api/plugins/containers/docker/image/action', { host: selected?.id || '', image: k, action: 'remove' })
        d.ok ? okN++ : failN++
      } catch { failN++ }
    }
    setBusy(false); setSel(new Set())
    onMsg?.(`✓ 批量删除完成: 成功 ${okN} / 失败 ${failN}`)
    setTimeout(load, 500)
  }

  return (
    <>
      <Card title="拉取镜像" subtitle="异步拉取 · 实时显示层进度">
        <div className="btn-row" style={{ alignItems: 'center' }}>
          <input className="input" style={{ flex: 1, minWidth: 260 }} value={pullImage} onChange={(e) => setPullImage(e.target.value)}
            placeholder="镜像名, 如 nginx:1.27-alpine 或 registry.example.com/app:v1"
            onKeyDown={(e) => e.key === 'Enter' && !busy && startPull()} disabled={!!job && !job.done} />
          <button className="btn-glass-soft btn-glass-soft-accent" disabled={busy || !pullImage.trim() || (!!job && !job.done)} onClick={startPull}>拉取</button>
        </div>
        {job && (
          <div style={{ marginTop: '0.625rem' }}>
            <div className="btn-row" style={{ justifyContent: 'space-between', fontSize: '0.6875rem' }}>
              <span>{job.done ? (job.err ? `✗ ${job.image} 失败` : `✓ ${job.image} 完成`) : `拉取中: ${job.image}`}</span>
              <span className="dim">
                {job.layersTotal > 0 ? `${Math.min(job.layersDone, job.layersTotal)}/${job.layersTotal} 层` : '准备中…'} · {job.secs}s
              </span>
            </div>
            <div className={`pull-progress ${!job.done && job.layersTotal === 0 ? 'indeterminate' : ''}`}>
              <div className="pull-progress-bar" style={{
                width: job.done ? '100%' : `${job.layersTotal ? Math.round(Math.min(job.layersDone / job.layersTotal, 1) * 100) : 10}%`,
                background: job.err ? '#ef4444' : undefined,
              }} />
            </div>
            {job.lines.length > 0 && (
              <pre className="code-block" style={{ maxHeight: 120, overflow: 'auto', marginTop: '0.5rem', fontSize: '0.6875rem' }}>
                {job.lines.slice(-4).join('\n')}
              </pre>
            )}
          </div>
        )}
        {out && <pre className="code-block" style={{ maxHeight: 140, overflow: 'auto', marginTop: '0.5rem', fontSize: '0.6875rem' }}>{out}</pre>}
      </Card>

      <Card className="images-card" title={`镜像列表 (${images.length})`} subtitle={`已选 ${sel.size} · 支持全选/反选/批量删除`}>
        <div className="toolbar-strip">
          <button className="btn-glass-soft btn-glass-soft-sm" onClick={toggleAll}>{allChecked ? '取消全选' : '全选'}</button>
          <button className="btn-glass-soft btn-glass-soft-sm" onClick={toggleInvert}>反选</button>
          <span className="dim">已选 {sel.size}</span>
          <button className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-danger" disabled={busy || !sel.size} onClick={batchRemove}>删除选中</button>
        </div>
        <div className="table-wrap">
          <table className="data-table">
            <thead><tr>
              <th style={{ width: '4%' }}><input type="checkbox" checked={allChecked} onChange={toggleAll} /></th>
              <th style={{ width: '30%' }}>仓库</th><th style={{ width: '12%' }}>标签</th>
              <th style={{ width: '14%' }}>ID</th><th style={{ width: '12%' }}>大小</th><th style={{ width: '28%' }}>操作</th>
            </tr></thead>
            <tbody>
              {images.length === 0 && <tr><td colSpan={6} className="dim">（无镜像）</td></tr>}
              {images.map((im, i) => (
                <tr key={i}>
                  <td><input type="checkbox" checked={sel.has(im.key)} onChange={() => toggleOne(im.key)} /></td>
                  <td className="mono">{im.repo}</td>
                  <td className="mono">{im.tag}</td>
                  <td className="mono dim">{im.id}</td>
                  <td>{im.size}</td>
                  <td><div className="btn-row k8s-row-actions">
                    <button className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-danger" disabled={busy} onClick={() => removeOne(im.key)}>删除</button>
                  </div></td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </Card>
    </>
  )
}

// ── 镜像源面板(daemon.json registry-mirrors / insecure-registries) ──

function RegistriesPanel({ onMsg }: { onMsg?: (m: string) => void }) {
  const { selected } = useHost()
  const [mirrors, setMirrors] = useState('')
  const [insecure, setInsecure] = useState('')
  const [info, setInfo] = useState('')
  const [raw, setRaw] = useState('')
  const [restart, setRestart] = useState(false)
  const [busy, setBusy] = useState(false)

  const hostQ = selected?.id ? `&host=${encodeURIComponent(selected.id)}` : ''
  const load = () => getJSON<any>(`/api/plugins/containers/docker/registries?_=${Date.now()}${hostQ}`)
    .then((d) => {
      setMirrors((d['registry-mirrors'] || []).join('\n'))
      setInsecure((d['insecure-registries'] || []).join('\n'))
      setInfo(d.info || ''); setRaw(d.raw || '')
    }).catch((e) => onMsg?.('✗ ' + String(e)))
  useEffect(() => { load() }, [selected?.id])

  const save = () => {
    setBusy(true)
    postJSON('/api/plugins/containers/docker/registries', {
      host: selected?.id || '',
      'registry-mirrors': mirrors.split('\n').map((s) => s.trim()).filter(Boolean),
      'insecure-registries': insecure.split('\n').map((s) => s.trim()).filter(Boolean),
      restart,
    })
      .then((d: any) => onMsg?.(d.ok ? `✓ ${d.note}` : '✗ ' + (d.error || '失败')))
      .catch((e) => onMsg?.('✗ ' + String(e)))
      .finally(() => setBusy(false))
  }

  return (
    <>
      <Card title="镜像加速源" subtitle="/etc/docker/daemon.json · 每行一个地址">
        <label className="dim" style={{ fontSize: '0.75rem' }}>registry-mirrors</label>
        <textarea className="input" rows={4} value={mirrors} onChange={(e) => setMirrors(e.target.value)}
          placeholder={'https://docker.m.daocloud.io\nhttps://mirror.ccs.tencentyun.com'}
          style={{ width: '100%', fontFamily: 'monospace', fontSize: '0.75rem' }} />
        <label className="dim" style={{ fontSize: '0.75rem', display: 'block', marginTop: '0.5rem' }}>insecure-registries(私有仓库)</label>
        <textarea className="input" rows={3} value={insecure} onChange={(e) => setInsecure(e.target.value)}
          placeholder={'192.168.94.20:5000\nregistry.local:32000'}
          style={{ width: '100%', fontFamily: 'monospace', fontSize: '0.75rem' }} />
        <div className="btn-row" style={{ alignItems: 'center', marginTop: '0.625rem' }}>
          <label style={{ fontSize: '0.8125rem', display: 'flex', alignItems: 'center', gap: 6 }}>
            <input type="checkbox" checked={restart} onChange={(e) => setRestart(e.target.checked)} />
            保存后立即重启 docker（会中断该主机容器）
          </label>
          <button className="btn-glass-soft btn-glass-soft-accent" disabled={busy} onClick={save}>{busy ? '写入中…' : '保存配置'}</button>
        </div>
        {info && <div className="banner banner-ok" style={{ whiteSpace: 'pre-wrap', fontFamily: 'monospace', fontSize: '0.6875rem' }}>{info}</div>}
      </Card>
      {raw && (
        <Card title="当前 daemon.json 原文" subtitle="只读">
          <pre className="code-block" style={{ maxHeight: 220, overflow: 'auto', fontSize: '0.6875rem' }}>{raw}</pre>
        </Card>
      )}
    </>
  )
}

// ── Dockerfile 构建面板 ──

const SAMPLE_DOCKERFILE = `FROM nginx:1.27-alpine
COPY dist/ /usr/share/nginx/html/
EXPOSE 80
CMD ["nginx", "-g", "daemon off;"]`

const BASIC_TEMPLATE = `# 基础 Dockerfile 模板
FROM <image>:<tag>
WORKDIR /app

# 复制依赖并安装
COPY package*.json ./
RUN npm ci --only=production

# 复制源码
COPY . .

# 暴露端口
EXPOSE 3000

# 启动命令
CMD ["node", "server.js"]`

// ── Dockerfile 生成 + 构建面板 ──
type Kv = { k: string; v: string }
type CopyItem = { src: string; dest: string }

type DfModel = {
  from: string; maintainer: string; workdir: string; user: string
  args: Kv[]; envs: Kv[]; copies: CopyItem[]; adds: CopyItem[]
  exposes: string[]; volumes: string[]; runs: string[]
  cmd: string; entrypoint: string
  healthcheck: { cmd: string; interval: string; timeout: string; retries: string } | null
}

const blank = (): DfModel => ({
  from: '', maintainer: '', workdir: '', user: '',
  args: [], envs: [], copies: [], adds: [],
  exposes: [], volumes: [], runs: [],
  cmd: '', entrypoint: '', healthcheck: null,
})

function buildDockerfile(m: DfModel): string {
  if (!m.from.trim()) return ''
  const lines: string[] = []
  lines.push(`FROM ${m.from.trim()}`)
  if (m.maintainer.trim()) lines.push(`LABEL maintainer="${m.maintainer.trim()}"`)
  m.args.forEach((a) => { if (a.k.trim()) lines.push(`ARG ${a.k}${a.v.trim() ? `=${a.v.trim()}` : ''}`) })
  if (m.user.trim()) lines.push(`USER ${m.user.trim()}`)
  if (m.workdir.trim()) lines.push(`WORKDIR ${m.workdir.trim()}`)
  m.envs.forEach((e) => { if (e.k.trim()) lines.push(`ENV ${e.k}=${e.v}`) })
  m.copies.forEach((c) => { if (c.src.trim() && c.dest.trim()) lines.push(`COPY ${c.src} ${c.dest}`) })
  m.adds.forEach((a) => { if (a.src.trim() && a.dest.trim()) lines.push(`ADD ${a.src} ${a.dest}`) })
  m.runs.forEach((r) => { if (r.trim()) lines.push(`RUN ${r.trim()}`) })
  m.exposes.forEach((p) => { if (p.trim()) lines.push(`EXPOSE ${p.trim()}`) })
  m.volumes.forEach((v) => { if (v.trim()) lines.push(`VOLUME ${v.trim()}`) })
  if (m.healthcheck) {
    const hc = m.healthcheck
    if (hc.cmd.trim()) {
      const parts = [`CMD=${hc.cmd.trim()}`]
      if (hc.interval.trim()) parts.push(`--interval=${hc.interval.trim()}`)
      if (hc.timeout.trim()) parts.push(`--timeout=${hc.timeout.trim()}`)
      if (hc.retries.trim()) parts.push(`--retries=${hc.retries.trim()}`)
      lines.push(`HEALTHCHECK ${parts.join(' ')}`)
    }
  }
  if (m.cmd.trim()) lines.push(`CMD ${m.cmd.trim()}`)
  if (m.entrypoint.trim()) lines.push(`ENTRYPOINT ${m.entrypoint.trim()}`)
  return lines.join('\n') + '\n'
}

const IN = 'input'

function F({ label, children, hint, required, wide }: { label: string; children: any; hint?: string; required?: boolean; wide?: boolean }) {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 4, fontSize: '0.75rem', color: 'var(--text-dim)', minWidth: 0, ...(wide ? { gridColumn: '1 / -1' } : {}) }}>
      <span>{label}{required && <span style={{ color: '#ef4444', marginLeft: 2 }}>*</span>}{hint && <i style={{ fontStyle: 'normal', opacity: 0.7 }}> · {hint}</i>}</span>
      {children}
    </div>
  )
}

function Grid({ cols = 2, children }: { cols?: number; children: any }) {
  return <div style={{ display: 'grid', gridTemplateColumns: `repeat(${cols}, minmax(0, 1fr))`, gap: '0.5rem' }}>{children}</div>
}

function Section({ title, children, defaultOpen, badge }: { title: string; children: any; defaultOpen?: boolean; badge?: string }) {
  const [open, setOpen] = useState(!!defaultOpen)
  return (
    <div style={{ border: '1px solid var(--border)', borderRadius: 'var(--radius-sm)', padding: '0.5rem 0.75rem' }}>
      <div onClick={() => setOpen(!open)} style={{ cursor: 'pointer', display: 'flex', justifyContent: 'space-between', fontSize: '0.8125rem', fontWeight: 600, userSelect: 'none' }}>
        <span>{open ? '▾' : '▸'} {title}</span>
        {badge && <span className="dim" style={{ fontSize: '0.6875rem' }}>{badge}</span>}
      </div>
      {open && <div style={{ display: 'flex', flexDirection: 'column', gap: '0.5rem', marginTop: '0.5rem' }}>{children}</div>}
    </div>
  )
}

function KvRows({ items, onChange, kHint, vHint, addLabel }: { items: Kv[]; onChange: (x: Kv[]) => void; kHint: string; vHint: string; addLabel?: string }) {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
      {items.map((it, i) => (
        <div key={i} style={{ display: 'flex', gap: 4, alignItems: 'center' }}>
          <input className={IN} value={it.k} placeholder={kHint} onChange={(e) => { const n = [...items]; n[i] = { ...it, k: e.target.value }; onChange(n) }} style={{ flex: 1, minWidth: 0 }} />
          <input className={IN} value={it.v} placeholder={vHint} onChange={(e) => { const n = [...items]; n[i] = { ...it, v: e.target.value }; onChange(n) }} style={{ flex: 1, minWidth: 0 }} />
          <button type="button" className="btn-glass-soft btn-glass-soft-sm" style={{ width: '1.75rem', height: '1.75rem', opacity: 1, flexShrink: 0 }} onClick={() => onChange(items.filter((_, j) => j !== i))} title="删除">✕</button>
        </div>
      ))}
      <button type="button" className="btn-glass-soft btn-glass-soft-sm" style={{ alignSelf: 'flex-start' }} onClick={() => onChange([...items, { k: '', v: '' }])}>{addLabel || '+ 添加'}</button>
    </div>
  )
}

function RowList({ items, onChange, placeholder, addLabel }: { items: string[]; onChange: (x: string[]) => void; placeholder: string; addLabel?: string }) {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
      {items.map((it, i) => (
        <div key={i} style={{ display: 'flex', gap: 4, alignItems: 'center' }}>
          <input className={IN} value={it} placeholder={placeholder} onChange={(e) => { const n = [...items]; n[i] = e.target.value; onChange(n) }} style={{ flex: 1, minWidth: 0 }} />
          <button type="button" className="btn-glass-soft btn-glass-soft-sm" style={{ width: '1.75rem', height: '1.75rem', opacity: 1, flexShrink: 0 }} onClick={() => onChange(items.filter((_, j) => j !== i))} title="删除">✕</button>
        </div>
      ))}
      <button type="button" className="btn-glass-soft btn-glass-soft-sm" style={{ alignSelf: 'flex-start' }} onClick={() => onChange([...items, ''])}>{addLabel || '+ 添加'}</button>
    </div>
  )
}

function BuildPanel({ onMsg }: { onMsg?: (m: string) => void }) {
  const { selected } = useHost()
  const [tag, setTag] = useState('')
  const [busy, setBusy] = useState(false)
  const [out, setOut] = useState('')
  const [mode, setMode] = useState<'form' | 'raw'>('form')

  const [m, setM] = useState<DfModel>(blank())
  const [copied, setCopied] = useState(false)
  const [rawDf, setRawDf] = useState(SAMPLE_DOCKERFILE)

  const generated = useMemo(() => buildDockerfile(m), [m])
  const canGenerate = m.from.trim().length > 0
  const set = (patch: Partial<DfModel>) => setM((x) => ({ ...x, ...patch }))

  const copy = async () => {
    if (!generated) return
    try {
      await navigator.clipboard.writeText(generated)
      setCopied(true)
      onMsg?.('✓ 已复制到剪贴板')
      setTimeout(() => setCopied(false), 1500)
    } catch {
      onMsg?.('✗ 复制失败')
    }
  }

  const build = (dockerfile: string) => {
    if (!tag.trim() || !dockerfile.trim()) return
    setBusy(true); setOut('构建中…')
    postJSON('/api/plugins/containers/docker/build', { host: selected?.id || '', tag: tag.trim(), dockerfile })
      .then((d: any) => {
        setOut(d.output || d.error || '')
        onMsg?.(d.ok ? `✓ 构建成功: ${tag}` : '✗ 构建失败')
      })
      .catch((e) => { setOut(String(e)); onMsg?.('✗ ' + String(e)) })
      .finally(() => setBusy(false))
  }

  return (
    <>
      <div className="btn-row" style={{ alignItems: 'center', flexWrap: 'wrap', marginBottom: '0.75rem' }}>
        <span className="dim" style={{ fontSize: '0.8125rem' }}>镜像 Tag:</span>
        <input className="input" style={{ width: 280 }} value={tag} onChange={(e) => setTag(e.target.value)}
          placeholder="如 myapp:v1" />
        <button className="btn-glass-soft btn-glass-soft-accent" disabled={busy || !tag.trim()} onClick={() => build(mode === 'form' ? generated : rawDf)}>
          {busy ? '构建中…' : '开始构建'}
        </button>
        <div style={{ display: 'flex', gap: '0.375rem', marginLeft: 'auto' }}>
          <button type="button" className={`btn-glass-soft btn-glass-soft-sm ${mode === 'form' ? 'btn-accent' : ''}`} onClick={() => setMode('form')}>可视化表单</button>
          <button type="button" className={`btn-glass-soft btn-glass-soft-sm ${mode === 'raw' ? 'btn-accent' : ''}`} onClick={() => setMode('raw')}>手动编辑</button>
        </div>
      </div>

      {mode === 'form' ? (
        <div style={{ display: 'grid', gridTemplateColumns: 'minmax(0, 1fr) minmax(0, 1fr)', gap: '1rem', alignItems: 'start' }}>
          {/* 左栏: 基础 + 可选指令 */}
          <div style={{ display: 'flex', flexDirection: 'column', gap: '0.75rem' }}>
            <div className="card" style={{ minWidth: 0 }}>
              <div className="card-head"><h3>基础指令</h3><span className="card-sub">必填项已标红</span></div>
              <div style={{ display: 'flex', flexDirection: 'column', gap: '0.625rem' }}>
                <Grid cols={2}>
                  <F label="FROM (基础镜像)" required hint="如 nginx:1.27-alpine">
                    <input className={IN} value={m.from} placeholder="nginx:1.27-alpine" onChange={(e) => set({ from: e.target.value })} />
                  </F>
                  <F label="MAINTAINER (维护者)">
                    <input className={IN} value={m.maintainer} placeholder="可选" onChange={(e) => set({ maintainer: e.target.value })} />
                  </F>
                </Grid>
                <Grid cols={2}>
                  <F label="WORKDIR (工作目录)" hint="如 /app">
                    <input className={IN} value={m.workdir} placeholder="/app" onChange={(e) => set({ workdir: e.target.value })} />
                  </F>
                  <F label="USER (运行用户)" hint="如 node">
                    <input className={IN} value={m.user} placeholder="root" onChange={(e) => set({ user: e.target.value })} />
                  </F>
                </Grid>
              </div>
            </div>

            <div className="card" style={{ minWidth: 0 }}>
              <div className="card-head"><h3>可选指令</h3><span className="card-sub">按需添加</span></div>
              <div style={{ display: 'flex', flexDirection: 'column', gap: '0.5rem' }}>
                <Section title="ARG (构建参数)" badge="变量替换">
                  <KvRows items={m.args} onChange={(x) => set({ args: x })} kHint="KEY" vHint="默认值" addLabel="+ ARG" />
                </Section>
                <Section title="ENV (环境变量)">
                  <KvRows items={m.envs} onChange={(x) => set({ envs: x })} kHint="KEY" vHint="value" addLabel="+ ENV" />
                </Section>
                <Section title="COPY (复制文件)">
                  <KvRows items={m.copies.map(c => ({ k: c.src, v: c.dest }))} onChange={(x) => set({ copies: x.map(i => ({ src: i.k, dest: i.v })) })} kHint="源路径" vHint="目标路径" addLabel="+ COPY" />
                </Section>
                <Section title="ADD (添加文件/URL)">
                  <KvRows items={m.adds.map(c => ({ k: c.src, v: c.dest }))} onChange={(x) => set({ adds: x.map(i => ({ src: i.k, dest: i.v })) })} kHint="源路径/URL" vHint="目标路径" addLabel="+ ADD" />
                </Section>
                <Section title="RUN (执行命令)">
                  <RowList items={m.runs} onChange={(x) => set({ runs: x })} placeholder="如 apt-get update && apt-get install -y curl" addLabel="+ RUN" />
                </Section>
                <Section title="EXPOSE (暴露端口)">
                  <RowList items={m.exposes} onChange={(x) => set({ exposes: x })} placeholder="如 8080" addLabel="+ EXPOSE" />
                </Section>
                <Section title="VOLUME (数据卷)">
                  <RowList items={m.volumes} onChange={(x) => set({ volumes: x })} placeholder="如 /data" addLabel="+ VOLUME" />
                </Section>
                <Section title="CMD (默认命令)">
                  <F label="CMD" hint="容器启动时执行的命令">
                    <input className={IN} value={m.cmd} placeholder='如 ["nginx", "-g", "daemon off;"] 或 nginx -g daemon off;' onChange={(e) => set({ cmd: e.target.value })} />
                  </F>
                </Section>
                <Section title="ENTRYPOINT (入口点)">
                  <F label="ENTRYPOINT" hint="容器的固定入口">
                    <input className={IN} value={m.entrypoint} placeholder='如 ["nginx"]' onChange={(e) => set({ entrypoint: e.target.value })} />
                  </F>
                </Section>
                <Section title="HEALTHCHECK (健康检查)">
                  {!m.healthcheck ? (
                    <button type="button" className="btn-glass-soft btn-glass-soft-sm" onClick={() => set({ healthcheck: { cmd: 'curl -f http://localhost/ || exit 1', interval: '30s', timeout: '10s', retries: '3' } })}>
                      + 添加健康检查
                    </button>
                  ) : (
                    <>
                      <F label="检查命令" hint="如 curl -f http://localhost/ || exit 1">
                        <input className={IN} value={m.healthcheck.cmd} onChange={(e) => set({ healthcheck: m.healthcheck ? { ...m.healthcheck, cmd: e.target.value } : null })} />
                      </F>
                      <Grid cols={3}>
                        <F label="间隔"><input className={IN} value={m.healthcheck.interval} onChange={(e) => set({ healthcheck: m.healthcheck ? { ...m.healthcheck, interval: e.target.value } : null })} /></F>
                        <F label="超时"><input className={IN} value={m.healthcheck.timeout} onChange={(e) => set({ healthcheck: m.healthcheck ? { ...m.healthcheck, timeout: e.target.value } : null })} /></F>
                        <F label="重试次数"><input className={IN} value={m.healthcheck.retries} onChange={(e) => set({ healthcheck: m.healthcheck ? { ...m.healthcheck, retries: e.target.value } : null })} /></F>
                      </Grid>
                      <button type="button" className="btn-glass-soft btn-glass-soft-sm" style={{ color: '#ef4444' }} onClick={() => set({ healthcheck: null })}>移除健康检查</button>
                    </>
                  )}
                </Section>
              </div>
            </div>
          </div>

          {/* 右栏: 预览 */}
          <div className="card" style={{ minWidth: 0, display: 'flex', flexDirection: 'column' }}>
            <div className="card-head">
              <h3>Dockerfile 预览</h3>
              <button type="button" className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-accent" disabled={!canGenerate || copied} onClick={copy}>
                {copied ? '✓ 已复制' : '复制'}
              </button>
            </div>
            <div className="card-body" style={{ flex: 1, display: 'flex', flexDirection: 'column', minHeight: 0 }}>
              <pre className="code-block mono" style={{ flex: 1, margin: 0, whiteSpace: 'pre-wrap', wordBreak: 'break-all', fontSize: '0.6875rem', lineHeight: 1.6, minHeight: 400 }}>
                {generated || BASIC_TEMPLATE}
              </pre>
            </div>
          </div>
        </div>
      ) : (
        <div className="card" style={{ minWidth: 0 }}>
          <div className="card-head"><h3>Dockerfile 编辑</h3><span className="card-sub">手动编写</span></div>
          <textarea className="input" rows={14} value={rawDf} onChange={(e) => setRawDf(e.target.value)}
            style={{ width: '100%', fontFamily: 'ui-monospace, monospace', fontSize: '0.75rem' }} />
        </div>
      )}

      {out && <pre className="code-block" style={{ maxHeight: 260, overflow: 'auto', marginTop: '0.625rem', fontSize: '0.6875rem', whiteSpace: 'pre-wrap' }}>{out}</pre>}
    </>
  )
}

// ── Compose 面板 ──

const SAMPLE_COMPOSE = `services:
  web:
    image: nginx:1.27-alpine
    ports:
      - "8088:80"
  redis:
    image: redis:7-alpine`

// ── Compose 表单模型 ──
type ComposeService = {
  name: string; image: string
  ports: string[]; environment: { key: string; value: string }[]
  volumes: string[]; command: string; restart: string
}
type ComposeModel = {
  project: string
  services: ComposeService[]
  activeIdx: number
}

const blankSvc = (): ComposeService => ({
  name: '', image: '',
  ports: [], environment: [], volumes: [],
  command: '', restart: 'no',
})

function buildComposeYaml(m: ComposeModel): string {
  if (!m.project.trim()) return ''
  const lines: string[] = [`services:`]
  m.services.forEach((svc) => {
    if (!svc.name.trim() || !svc.image.trim()) return
    lines.push(`  ${svc.name.trim()}:`)
    lines.push(`    image: ${svc.image.trim()}`)
    if (svc.ports.length) {
      const validPorts = svc.ports.filter((p) => p.trim())
      if (validPorts.length) lines.push(`    ports:\n${validPorts.map((p) => `      - "${p.trim()}"`).join('\n')}`)
    }
    const validEnv = svc.environment.filter((e) => e.key.trim())
    if (validEnv.length) {
      lines.push(`    environment:`)
      validEnv.forEach((e) => { lines.push(`      ${e.key.trim()}: ${e.value.trim()}`) })
    }
    if (svc.volumes.length) {
      const validVols = svc.volumes.filter((v) => v.trim())
      if (validVols.length) lines.push(`    volumes:\n${validVols.map((v) => `      - ${v.trim()}`).join('\n')}`)
    }
    if (svc.command.trim()) lines.push(`    command: ${svc.command.trim()}`)
    if (svc.restart && svc.restart !== 'no') lines.push(`    restart: ${svc.restart}`)
  })
  return lines.join('\n') + '\n'
}

// ── Compose 可视化面板 ──
function ComposePanel({ onMsg }: { onMsg?: (m: string) => void }) {
  const { selected } = useHost()
  const [project, setProject] = useState('')
  const [mode, setMode] = useState<'form' | 'yaml'>('form')
  const [m, setM] = useState<ComposeModel>({ project: '', services: [blankSvc()], activeIdx: 0 })
  const [rawYaml, setRawYaml] = useState(SAMPLE_COMPOSE)
  const [busy, setBusy] = useState(false)
  const [out, setOut] = useState('')
  const [projHint, setProjHint] = useState('')
  const [copied, setCopied] = useState(false)

  const generated = useMemo(() => buildComposeYaml(m), [m])
  const active = m.services[m.activeIdx] || blankSvc()
  const set = (patch: Partial<ComposeModel>) => setM((x) => ({ ...x, ...patch }))
  const setActive = (patch: Partial<ComposeService>) => {
    const services = [...m.services]
    services[m.activeIdx] = { ...services[m.activeIdx], ...patch }
    set({ services })
  }

  const addService = () => {
    const services = [...m.services, blankSvc()]
    set({ services, activeIdx: services.length - 1 })
  }
  const removeService = (idx: number) => {
    if (m.services.length <= 1) return
    const services = m.services.filter((_, i) => i !== idx)
    const activeIdx = idx === m.activeIdx ? 0 : (idx < m.activeIdx ? m.activeIdx - 1 : m.activeIdx)
    set({ services, activeIdx })
  }

  const copy = async () => {
    const text = mode === 'form' ? generated : rawYaml
    if (!text.trim()) return
    try {
      await navigator.clipboard.writeText(text)
      setCopied(true); onMsg?.('✓ 已复制到剪贴板'); setTimeout(() => setCopied(false), 1500)
    } catch { onMsg?.('✗ 复制失败') }
  }

  const act = (action: 'up' | 'down' | 'restart' | 'ps') => {
    if (!project.trim()) { setProjHint('请先填写项目名'); return }
    const compose = mode === 'form' ? generated : rawYaml
    if (action === 'up' && !compose.trim()) return
    setBusy(true); setOut(action.toUpperCase() + ' …')
    postJSON('/api/plugins/containers/docker/compose', {
      host: selected?.id || '', project: project.trim(), action,
      compose: action === 'up' ? compose : undefined,
    })
      .then((d: any) => {
        setOut(typeof d.output === 'string' ? prettyCompose(d.output) : JSON.stringify(d.output, null, 2))
        onMsg?.(d.ok ? `✓ ${project} ${action} 完成` : '✗ ' + (d.error || '失败'))
      })
      .catch((e) => { setOut(String(e)); onMsg?.('✗ ' + String(e)) })
      .finally(() => setBusy(false))
  }

  return (
    <Card title="Compose 项目编排" subtitle="配置保存在目标主机 /tmp/.opscore-compose/<project>/ · 后续操作无需重复粘贴">
      <div className="btn-row" style={{ alignItems: 'center', flexWrap: 'wrap', marginBottom: '0.75rem' }}>
        <label style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: '0.8125rem' }}>
          <span className="dim">项目名:</span>
          <input className="input" style={{ width: 200 }} value={project}
            onChange={(e) => { setProject(e.target.value); if (e.target.value.trim()) setProjHint('') }}
            placeholder="小写字母/数字/_-" />
          {projHint && <span style={{ color: '#ef4444', fontSize: '0.6875rem' }}>{projHint}</span>}
        </label>
        <button className="btn-glass-soft btn-glass-soft-accent" disabled={busy} onClick={() => act('up')}>up -d 部署</button>
        <button className="btn-glass-soft" disabled={busy} onClick={() => act('ps')}>查看状态</button>
        <button className="btn-glass-soft" disabled={busy} onClick={() => act('restart')}>重启</button>
        <button className="btn-glass-soft btn-glass-soft-danger" disabled={busy} onClick={() => act('down')}>down 销毁</button>
        <div style={{ display: 'flex', gap: '0.375rem', marginLeft: 'auto' }}>
          <button type="button" className={`btn-glass-soft btn-glass-soft-sm ${mode === 'form' ? 'btn-accent' : ''}`} onClick={() => setMode('form')}>可视化表单</button>
          <button type="button" className={`btn-glass-soft btn-glass-soft-sm ${mode === 'yaml' ? 'btn-accent' : ''}`} onClick={() => setMode('yaml')}>手动编辑</button>
        </div>
      </div>

      {mode === 'form' ? (
        <div style={{ display: 'grid', gridTemplateColumns: 'minmax(0, 1fr) minmax(0, 1fr)', gap: '1rem', alignItems: 'start' }}>
          {/* 左栏: 服务列表 + 编辑 */}
          <div style={{ display: 'flex', flexDirection: 'column', gap: '0.75rem' }}>
            <div className="card" style={{ minWidth: 0 }}>
              <div className="card-head"><h3>服务列表</h3><span className="card-sub">{m.services.length} 个服务</span></div>
              <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
                {m.services.map((svc, i) => (
                  <div key={i} style={{ display: 'flex', gap: 6, alignItems: 'center' }}>
                    <button type="button" className={`btn-glass-soft btn-glass-soft-sm ${i === m.activeIdx ? 'btn-accent' : ''}`}
                      style={{ flex: 1, justifyContent: 'flex-start' }}
                      onClick={() => set({ activeIdx: i })}>
                      <span className="mono">{svc.name.trim() || '(未命名)'}</span>
                      <span className="dim" style={{ marginLeft: 6, fontSize: '0.6875rem' }}>{svc.image.trim() || '无镜像'}</span>
                    </button>
                    <button type="button" className="btn-glass-soft btn-glass-soft-sm" style={{ color: '#ef4444' }} onClick={() => removeService(i)} title="删除服务">✕</button>
                  </div>
                ))}
                <button type="button" className="btn-glass-soft btn-glass-soft-sm" onClick={addService}>+ 添加服务</button>
              </div>
            </div>

            {active && (
              <div className="card" style={{ minWidth: 0 }}>
                <div className="card-head"><h3>编辑服务</h3><span className="card-sub">{active.name.trim() || '新服务'}</span></div>
                <div style={{ display: 'flex', flexDirection: 'column', gap: '0.625rem' }}>
                  <div style={{ display: 'grid', gridTemplateColumns: 'repeat(2, minmax(0, 1fr))', gap: '0.5rem' }}>
                    <F label="服务名 *" required hint="如 web">
                      <input className={IN} value={active.name} placeholder="web" onChange={(e) => setActive({ name: e.target.value })} />
                    </F>
                    <F label="镜像 *" required hint="如 nginx:1.27-alpine">
                      <input className={IN} value={active.image} placeholder="nginx:1.27-alpine" onChange={(e) => setActive({ image: e.target.value })} />
                    </F>
                  </div>
                  <F label="端口映射" hint="如 8080:80">
                    <RowList items={active.ports} onChange={(x) => setActive({ ports: x })} placeholder="8080:80" addLabel="+ 端口" />
                  </F>
                  <F label="环境变量">
                    <KvRows items={active.environment} onChange={(x) => setActive({ environment: x })} kHint="KEY" vHint="value" addLabel="+ ENV" />
                  </F>
                  <F label="卷挂载" hint="如 ./data:/app/data">
                    <RowList items={active.volumes} onChange={(x) => setActive({ volumes: x })} placeholder="./data:/app/data" addLabel="+ 卷" />
                  </F>
                  <div style={{ display: 'grid', gridTemplateColumns: 'repeat(2, minmax(0, 1fr))', gap: '0.5rem' }}>
                    <F label="启动命令" hint="如 npm start">
                      <input className={IN} value={active.command} placeholder="npm start" onChange={(e) => setActive({ command: e.target.value })} />
                    </F>
                    <F label="重启策略">
                      <select className={`${IN} sel`} value={active.restart} onChange={(e) => setActive({ restart: e.target.value })}>
                        {['no', 'on-failure', 'always', 'unless-stopped'].map((r) => <option key={r} value={r}>{r}</option>)}
                      </select>
                    </F>
                  </div>
                </div>
              </div>
            )}
          </div>

          {/* 右栏: YAML 预览 */}
          <div className="card" style={{ minWidth: 0, display: 'flex', flexDirection: 'column' }}>
            <div className="card-head">
              <h3>Compose YAML</h3>
              <button type="button" className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-accent" disabled={!generated || copied} onClick={copy}>
                {copied ? '✓ 已复制' : '复制'}
              </button>
            </div>
            <div className="card-body" style={{ flex: 1, display: 'flex', flexDirection: 'column', minHeight: 0 }}>
              <pre className="code-block mono" style={{ flex: 1, margin: 0, whiteSpace: 'pre-wrap', wordBreak: 'break-all', fontSize: '0.6875rem', lineHeight: 1.6, minHeight: 400 }}>
                {generated || '# 请填写项目名和服务信息'}
              </pre>
            </div>
          </div>
        </div>
      ) : (
        <div className="card" style={{ minWidth: 0 }}>
          <div className="card-head"><h3>Compose YAML 编辑</h3><span className="card-sub">手动编写</span></div>
          <textarea className="input" rows={14} value={rawYaml} onChange={(e) => setRawYaml(e.target.value)}
            style={{ width: '100%', fontFamily: 'ui-monospace, monospace', fontSize: '0.75rem' }} />
        </div>
      )}

      {out && <pre className="code-block" style={{ maxHeight: 300, overflow: 'auto', marginTop: '0.625rem', fontSize: '0.6875rem', whiteSpace: 'pre-wrap' }}>{out}</pre>}
    </Card>
  )
}

function prettyCompose(s: string): string {
  // compose ps --format json 输出为 NDJSON, 美化为可读行
  if (!s.startsWith('{') && !s.startsWith('[')) return s
  try {
    const lines = s.split('\n').filter(Boolean).map((l) => JSON.parse(l))
    return lines.map((o) => Object.entries(o).map(([k, v]) => `${k}=${v}`).join('  ')).join('\n')
  } catch { return s }
}

// ── Swarm 管理面板(状态 + init/join-token/leave/scale) ──

function SwarmPanel() {
  const { selected } = useHost()
  const [data, setData] = useState<any>(null)
  const [advIP, setAdvIP] = useState('')
  const [busy, setBusy] = useState(false)
  const hostQ = selected?.id ? `&host=${encodeURIComponent(selected.id)}` : ''
  const load = () => getJSON<any>(`/api/plugins/containers/docker/swarm?_=${Date.now()}${hostQ}`).then(setData).catch(() => setData(null))
  useEffect(() => { load() }, [selected?.id])

  const swarmAct = (body: any, confirmMsg?: string) => {
    if (confirmMsg && !confirm(confirmMsg)) return
    setBusy(true)
    postJSON('/api/plugins/containers/docker/swarm/action', { host: selected?.id || '', ...body })
      .then((d: any) => {
        if (d.ok) {
          onSwarmMsg(d.token ? `✓ ${body.role} join-token:\n${d.token}` : `✓ ${body.action} 完成${d.output ? ': ' + d.output : ''}`)
          load()
        } else {
          onSwarmMsg('✗ ' + (d.error || '失败'))
        }
      })
      .catch((e) => onSwarmMsg('✗ ' + String(e)))
      .finally(() => setBusy(false))
  }

  const [swarmMsgState, setSwarmMsgState] = useState('')
  const onSwarmMsg = (m: string) => {
    setSwarmMsgState(m)
    if (m.startsWith('\n') || m.includes('join-token')) setTimeout(() => setSwarmMsgState(''), 30000)
  }

  const scaleService = async (svc: string) => {
    const input = prompt(`调整服务 ${svc} 副本数:`)
    if (input === null) return
    const n = parseInt(input, 10)
    if (isNaN(n) || n < 0) return
    setBusy(true)
    postJSON('/api/plugins/containers/docker/swarm/action', { host: selected?.id || '', action: 'scale', service: svc, replicas: n })
      .then((d: any) => { onSwarmMsg(d.ok ? `✓ ${svc} → ${n} 副本` : '✗ ' + (d.error || '失败')); setTimeout(load, 800) })
      .catch((e) => onSwarmMsg('✗ ' + String(e)))
      .finally(() => setBusy(false))
  }

  const swarm = data?.swarm
  const active = swarm?.LocalNodeState === 'active'
  const isManager = swarm?.ControlAvailable === true
  return (
    <>
      {swarmMsgState && (
        <div className="banner banner-ok" style={{ whiteSpace: 'pre-wrap', fontFamily: 'monospace', fontSize: '0.75rem' }}>{swarmMsgState}</div>
      )}
      <Card title="Swarm 集群" subtitle={active ? `已初始化 · ${isManager ? 'Manager 节点' : 'Worker 节点'}` : '未初始化'}>
        {!data ? <div className="loading">加载中…</div> : (
          <>
            <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill,minmax(190px,1fr))', gap: '0.625rem', marginBottom: '0.75rem' }}>
              <KV k="LocalNodeState" v={String(swarm?.LocalNodeState ?? '-')} />
              <KV k="ControlAvailable" v={String(swarm?.ControlAvailable ?? false)} />
              <KV k="ClusterID" v={String(swarm?.ClusterID || '(未初始化)')} />
              <KV k="Nodes / Managers" v={`${swarm?.Nodes ?? '-'} / ${swarm?.Managers ?? '-'}`} />
            </div>
            <div className="btn-row k8s-row-actions">
              {!active ? (
                <>
                  <input className="input" style={{ width: 170 }} value={advIP} onChange={(e) => setAdvIP(e.target.value)} placeholder="advertise-ip(可选)" />
                  <button className="btn-glass-soft btn-glass-soft-accent btn-sm" disabled={busy}
                    onClick={() => swarmAct({ action: 'init', advertiseIp: advIP.trim() }, '在该主机初始化 Swarm 集群?')}>swarm init</button>
                </>
              ) : (
                <>
                  <button className="btn-glass-soft btn-glass-soft-sm" disabled={busy} onClick={() => swarmAct({ action: 'token', role: 'worker' })}>查看 worker token</button>
                  <button className="btn-glass-soft btn-glass-soft-sm" disabled={busy} onClick={() => swarmAct({ action: 'token', role: 'manager' })}>查看 manager token</button>
                  <button className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-danger" disabled={busy}
                    onClick={() => swarmAct({ action: 'leave', force: true }, '强制脱离 Swarm(高危, 会中断该节点上的服务)?')}>脱离集群</button>
                </>
              )}
              <button className="btn-glass-soft btn-glass-soft-sm" onClick={load}>刷新</button>
            </div>
            {!active && (
              <p className="dim" style={{ fontSize: '0.6875rem', marginTop: '0.5rem' }}>
                在管理节点执行 init 后, 用「查看 worker/manager token」获取加入命令, 在其他节点手动 docker swarm join。
              </p>
            )}
          </>
        )}
      </Card>

      {isManager && Array.isArray(data.nodes) && data.nodes.length > 0 && (
        <Card title={`Swarm Nodes (${data.nodes.length})`}>
          <div className="table-wrap"><table className="data-table">
            <thead><tr><th>主机名</th><th>状态</th><th>可用性</th><th>角色</th><th>TLS</th></tr></thead>
            <tbody>{data.nodes.map((n: any, i: number) => (
              <tr key={i}>
                <td className="mono">{n.Hostname}</td>
                <td><span className={`badge ${n.Status === 'Ready' ? 'badge-ok' : 'badge-warn'}`}>{n.Status}</span></td>
                <td>{n.Availability}</td>
                <td>{n.ManagerStatus ? 'Manager' : 'Worker'}</td>
                <td className="dim mono">{n.TLSStatus}</td>
              </tr>
            ))}</tbody>
          </table></div>
        </Card>
      )}

      {isManager && Array.isArray(data.services) && data.services.length > 0 && (
        <Card title={`Swarm Services (${data.services.length})`} subtitle="支持在线调整副本数(scale)">
          <div className="table-wrap"><table className="data-table">
            <thead><tr><th>ID</th><th>名称</th><th>模式</th><th>副本</th><th>镜像</th><th>操作</th></tr></thead>
            <tbody>{data.services.map((sv: any, i: number) => (
              <tr key={i}>
                <td className="mono dim">{sv.ID}</td>
                <td className="mono">{sv.Name}</td>
                <td>{sv.Mode}</td>
                <td className="mono">{sv.Replicas}</td>
                <td className="mono dim">{sv.Image}</td>
                <td><div className="btn-row k8s-row-actions">
                  {sv.Mode !== 'global' && (
                    <button className="btn-glass-soft btn-glass-soft-sm" disabled={busy} onClick={() => scaleService(sv.Name)}>scale</button>
                  )}
                </div></td>
              </tr>
            ))}</tbody>
          </table></div>
        </Card>
      )}
    </>
  )
}

function KV({ k, v }: { k: string; v: string }) {
  return (
    <div style={{ background: 'var(--bg-card, rgba(127,127,127,0.05))', border: '1px solid var(--border)', borderRadius: '0.5rem', padding: '0.5rem 0.75rem' }}>
      <div className="dim" style={{ fontSize: '0.6875rem' }}>{k}</div>
      <div className="mono" style={{ fontSize: '0.8125rem', wordBreak: 'break-all' }}>{v}</div>
    </div>
  )
}

// ── 容器命令执行弹窗(一次性 exec) ──

const COMMON_CMDS = ['env', 'ps aux', 'df -h', 'cat /etc/os-release', 'netstat -tlnp 2>/dev/null || ss -tlnp', 'uptime']

function ExecModal({ name, host, onClose }: { name: string; host: string; onClose: () => void }) {
  const [cmd, setCmd] = useState('ps aux')
  const [out, setOut] = useState('')
  const [busy, setBusy] = useState(false)
  const [history, setHistory] = useState<string[]>([])

  const run = () => {
    if (!cmd.trim()) return
    setBusy(true)
    postJSON('/api/plugins/containers/docker/exec', { host, name, cmd: cmd.trim() })
      .then((d: any) => {
        setOut(`$ ${cmd}\n${d.ok ? d.out : '✗ ' + d.error}`)
        setHistory((h) => [cmd.trim(), ...h.filter((x) => x !== cmd.trim())].slice(0, 6))
      })
      .catch((e) => setOut('$ ' + cmd + '\n✗ ' + String(e)))
      .finally(() => setBusy(false))
  }

  return (
    <div className="modal-overlay" onClick={onClose}>
      <div className="modal log-modal" onClick={(e) => e.stopPropagation()} style={{ maxWidth: 780 }}>
        <div className="modal-head">
          <div className="modal-title">容器内执行: <span className="mono">{name}</span> <span className="pill pill-sub">docker exec · sh -c</span></div>
          <button className="btn-glass-soft btn-glass-soft-sm" onClick={onClose}>关闭</button>
        </div>
        <div style={{ padding: '1rem 1.25rem', display: 'grid', gap: '0.625rem' }}>
          <div className="btn-row" style={{ alignItems: 'center', flexWrap: 'wrap' }}>
            <input className="input mono" style={{ flex: 1, minWidth: 280 }} value={cmd}
              onChange={(e) => setCmd(e.target.value)}
              onKeyDown={(e) => e.key === 'Enter' && !busy && run()}
              placeholder="如 ps aux / env / df -h / cat /path/file" />
            <button className="btn-glass-soft btn-glass-soft-accent" disabled={busy || !cmd.trim()} onClick={run}>{busy ? '执行中…' : '执行'}</button>
          </div>
          {history.length > 0 && (
            <div className="btn-row k8s-row-actions">
              {history.map((h, i) => (
                <button key={i} className="btn-glass-soft btn-glass-soft-sm btn-ghost mono" style={{ fontSize: '0.6875rem' }}
                  onClick={() => setCmd(h)}>{h}</button>
              ))}
            </div>
          )}
          <pre className="code-block" style={{ maxHeight: 360, overflow: 'auto', fontSize: '0.6875rem', whiteSpace: 'pre-wrap', minHeight: 120 }}>
            {out || `常用: ${COMMON_CMDS.slice(0, 4).join(' · ')}`}
          </pre>
        </div>
      </div>
    </div>
  )
}
