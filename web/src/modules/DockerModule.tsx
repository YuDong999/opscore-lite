// ── Docker 管理视图: 内嵌侧栏布局 ──
// 容器 / 镜像(含 pull/rmi) / 镜像源(daemon.json) / 构建镜像(Dockerfile) / Compose / Swarm(只读)
// 全部操作走既有 RunOnTarget 分发体系, 写操作带回读验证/审计

import { useEffect, useMemo, useState } from 'react'
import Card from '../components/Card'
import { useHost } from '../components/HostContext'
import { getJSON, postJSON } from '../api/client'

// 侧栏图标 — 正式图标库混搭, 均为各库官方 path (16px 与 K8s 侧栏一致)
type Section = 'containers' | 'images' | 'registries' | 'build' | 'compose' | 'swarm' | 'volumes' | 'networks'

const SECTIONS: { key: Section; title: string; desc: string }[] = [
  { key: 'containers', title: '容器', desc: '启停 / 删除 / 日志 / 详情' },
  { key: 'images', title: '镜像', desc: '列表 / 拉取 / 删除' },
  { key: 'volumes', title: 'Volumes', desc: '卷列表 / 创建 / 删除' },
  { key: 'networks', title: 'Networks', desc: '网络列表 / 创建 / 删除' },
  { key: 'registries', title: '镜像源', desc: '加速地址 / insecure' },
  { key: 'build', title: '构建镜像', desc: 'Dockerfile 在线构建' },
  { key: 'compose', title: 'Compose', desc: '项目编排 up/down/ps' },
  { key: 'swarm', title: 'Swarm', desc: '集群管理' },
]

const SectionIcon = ({ name }: { name: string }) => {
  const base = (sw: number) => ({ width: 16, height: 16, viewBox: '0 0 24 24', fill: 'none', stroke: 'currentColor', strokeWidth: sw, strokeLinecap: 'round' as const, strokeLinejoin: 'round' as const })
  switch (name) {
    // lucide hard-drive — Volumes
    case 'volumes': return <svg {...base(2)}><path d="M10 16h.01m-7.798-4.423a2 2 0 0 0-.212.896V18a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2v-5.527a2 2 0 0 0-.212-.896L18.55 5.11A2 2 0 0 0 16.76 4H7.24a2 2 0 0 0-1.79 1.11zm19.734.436H2.054M6 16h.01" /></svg>
    // lucide network — Networks
    case 'networks': return <svg {...base(2)}><rect width="6" height="6" x="16" y="16" rx="1" /><rect width="6" height="6" x="2" y="16" rx="1" /><rect width="6" height="6" x="9" y="2" rx="1" /><path d="M5 16v-3a1 1 0 0 1 1-1h12a1 1 0 0 1 1 1v3m-7-4V8" /></svg>
    // hugeicons boxes — 容器
    case 'containers': return <svg {...base(1.5)}><path strokeLinecap="round" d="M6 12V8c0-.943 0-1.414.293-1.707S7.057 6 8 6h4c.943 0 1.414 0 1.707.293S14 7.057 14 8v4c0 .943 0 1.414-.293 1.707S12.943 14 12 14H8c-.943 0-1.414 0-1.707-.293S6 12.943 6 12m-4 8v-4c0-.943 0-1.414.293-1.707S3.057 14 4 14h4c.943 0 1.414 0 1.707.293S10 15.057 10 16v4c0 .943 0 1.414-.293 1.707S8.943 22 8 22H4c-.943 0-1.414 0-1.707-.293S2 20.943 2 20m8 0v-4c0-.943 0-1.414.293-1.707S11.057 14 12 14h4c.943 0 1.414 0 1.707.293S18 15.057 18 16v4c0 .943 0 1.414-.293 1.707S16.943 22 16 22h-4c-.943 0-1.414 0-1.707-.293S10 20.943 10 20m8 1.5l3.414-3.414c.29-.29.434-.434.51-.617c.076-.184.076-.389.076-.797V12c0-.943 0-1.414-.293-1.707S20.943 10 20 10h-2"/><path strokeLinecap="round" d="m6 10l-3.414 3.414c-.29.29-.434.434-.51.617C2 14.215 2 14.42 2 14.829V16.5m12-3l3.317-2.902c.336-.295.504-.442.594-.639S18 9.54 18 9.092V4c0-.943 0-1.414-.293-1.707S16.943 2 16 2h-4.74c-.376 0-.563 0-.735.065c-.172.066-.312.19-.593.44l-3.26 2.898c-.331.294-.496.441-.584.636C6 6.235 6 6.456 6 6.9V9"/><path d="m14 6l3.5-3.5M18 14l3.5-3.5" /></svg>
    // lucide archive — 镜像(只读分层打包制品)
    case 'images': return <svg {...base(2)}><rect width="20" height="5" x="2" y="3" rx="1" /><path d="M4 8v11a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8m-10 4h4" /></svg>
    // hugeicons database — 镜像源
    case 'registries': return <svg {...base(1.5)}><ellipse cx="12" cy="5" rx="8" ry="3" /><path strokeLinecap="round" d="M7 10.842c.602.18 1.274.33 2 .44" /><path d="M20 12c0 1.657-3.582 3-8 3s-8-1.343-8-3" /><path strokeLinecap="round" d="M7 17.842c.602.18 1.274.33 2 .44" /><path d="M20 5v14c0 1.657-3.582 3-8 3s-8-1.343-8-3V5" /></svg>
    // tabler hammer — 构建镜像
    case 'build': return <svg {...base(2)}><path d="m11.414 10l-7.383 7.418a2.09 2.09 0 0 0 0 2.967a2.11 2.11 0 0 0 2.976 0L14.414 13m3.707 2.293l2.586-2.586a1 1 0 0 0 0-1.414l-7.586-7.586a1 1 0 0 0-1.414 0L9.121 6.293a1 1 0 0 0 0 1.414l7.586 7.586a1 1 0 0 0 1.414 0" /></svg>
    // mingcute layers — Compose
    case 'compose': return <svg {...base(2)}><path d="M7 14.4v1.77a1.5 1.5 0 0 0 1.794 1.471L10 17.4m-3-3V9.64a2 2 0 0 1 1.608-1.962l6.598-1.32A1.5 1.5 0 0 1 17 7.83V9.6M7 14.4l-1.206.241A1.5 1.5 0 0 1 4 13.171V6.64a2 2 0 0 1 1.608-1.962l6.598-1.32A1.5 1.5 0 0 1 14 4.83V6.5m3 3.1l-5.392 1.078A2 2 0 0 0 10 12.64v4.76m7-7.8l1.206-.241A1.5 1.5 0 0 1 20 10.829v6.531a2 2 0 0 1-1.608 1.962l-6.598 1.32A1.5 1.5 0 0 1 10 19.17V17.4" /></svg>
    // heroicons square-2-stack — Swarm
    default: return <svg {...base(1.5)}><path d="M16.5 8.25V6a2.25 2.25 0 0 0-2.25-2.25H6A2.25 2.25 0 0 0 3.75 6v8.25A2.25 2.25 0 0 0 6 16.5h2.25m8.25-8.25H18a2.25 2.25 0 0 1 2.25 2.25V18A2.25 2.25 0 0 1 18 20.25h-7.5A2.25 2.25 0 0 1 8.25 18v-1.5m8.25-8.25h-6a2.25 2.25 0 0 0-2.25 2.25v6" /></svg>
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
      <section className="k8s-main" style={{ minWidth: 0, flex: 1, height: '100%', display: 'flex', flexDirection: 'column' }}>
        {section === 'containers' && <ContainersPanel onMsg={onMsg} />}
        {section === 'images' && <ImagesPanel onMsg={onMsg} />}
        {section === 'volumes' && <VolumesPanel onMsg={onMsg} />}
        {section === 'networks' && <NetworksPanel onMsg={onMsg} />}
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
  const [statsView, setStatsView] = useState<{ name: string } | null>(null)
  const [statsData, setStatsData] = useState<any>(null)
  const [envModal, setEnvModal] = useState<{ name: string } | null>(null)
  const [cpModal, setCpModal] = useState<{ name: string } | null>(null)
  const [topModal, setTopModal] = useState<{ name: string } | null>(null)
  const [dfOpen, setDfOpen] = useState(false)
  const [pruneOpen, setPruneOpen] = useState(false)
  const [pauseBusy, setPauseBusy] = useState('')

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

  const openStats = (name: string) => {
    setStatsView({ name })
    refreshStats(name)
  }

  const refreshStats = (name: string) => {
    getJSON<any>(`/api/plugins/containers/docker/container/stats?${hostQ.replace('&', '')}&name=${encodeURIComponent(name)}`)
      .then((d) => setStatsData(d.stats || null))
      .catch(() => setStatsData(null))
  }

  // docker 直连操作(经 tool/action 端点): 暂停/继续 + 清理
  const pauseToggle = (c: AppContainer) => {
    const pause = c.state === 'paused'
    setPauseBusy(c.name)
    dockerTool(selected?.id || '', { scope: 'container', action: pause ? 'unpause' : 'pause', name: c.name })
      .then((d: any) => onMsg?.(d.ok ? `✓ ${pause ? '继续运行' : '已暂停'} ${c.name}` : '✗ ' + (d.error || '操作失败')))
      .catch((e) => onMsg?.('✗ ' + String(e)))
      .finally(() => { setPauseBusy(''); setTimeout(load, 400); setTimeout(load, 2500) })
  }

  const pruneContainers = () => {
    setPruneOpen(false); setBusy(true)
    dockerTool(selected?.id || '', { scope: 'container', action: 'prune' })
      .then((d: any) => onMsg?.(d.ok ? '✓ 已清理停止容器' : '✗ ' + (d.error || '失败')))
      .catch((e) => onMsg?.('✗ ' + String(e)))
      .finally(() => { setBusy(false); setTimeout(load, 400); setTimeout(load, 2500) })
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
          <span className="dim" style={{ marginLeft: 'auto' }} />
          <button className="btn-glass-soft btn-glass-soft-sm btn-ghost" disabled={busy} title="清理所有已停止容器 (docker container prune -f)" onClick={() => setPruneOpen(true)}>清理停止容器</button>
          <button className="btn-glass-soft btn-glass-soft-sm btn-ghost" title="磁盘占用总览 (docker system df)" onClick={() => setDfOpen(true)}>磁盘占用</button>
        </div>
        {list?.note && <div className="banner banner-warn">{list.note}</div>}
        <div className="table-wrap">
          <table className="data-table ctable">
            <thead><tr>
              <th><input type="checkbox" checked={allChecked} onChange={toggleAll} /></th>
              <th>名称</th>
              <th>镜像</th>
              <th>状态</th>
              <th>端口</th>
              <th>操作</th>
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
                  <td className="row-ops" onClick={(e) => e.stopPropagation()}>
                    <div className="btn-row k8s-row-actions">
                      <button className="btn-glass-soft btn-glass-soft-sm" disabled={busy || !canWrite || c.state !== 'exited'} onClick={() => runAction(c.name, 'start')}>启动</button>
                      <button className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-danger" disabled={busy || !canWrite || c.state !== 'running'} onClick={() => runAction(c.name, 'stop')}>停止</button>
                      <button className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-accent" disabled={busy || !canWrite} onClick={() => runAction(c.name, 'restart')}>重启</button>
                      <button className="btn-glass-soft btn-glass-soft-sm" disabled={!canWrite} title="编辑配置并重建(端口/卷/环境变量)" onClick={() => openEdit(c)}>编辑</button>
                      <button className="btn-glass-soft btn-glass-soft-sm" disabled={!canWrite} title="实时资源监控(CPU/内存/网络)" onClick={() => openStats(c.name)}>监控</button>
                      <button className="btn-glass-soft btn-glass-soft-sm" disabled={!canWrite} onClick={() => setExecView({ name: c.name })}>命令</button>
                      <button className="btn-glass-soft btn-glass-soft-sm btn-ghost" disabled={!canWrite} onClick={() => openLogs(c)}>日志</button>
                      <button className="btn-glass-soft btn-glass-soft-sm btn-ghost" disabled={!canWrite || pauseBusy === c.name} title="docker pause / unpause" onClick={() => pauseToggle(c)}>
                        {pauseBusy === c.name ? '…' : (c.state === 'paused' ? '继续' : '暂停')}
                      </button>
                      <button className="btn-glass-soft btn-glass-soft-sm btn-ghost" disabled={!canWrite} title="进程列表 (docker top)" onClick={() => setTopModal({ name: c.name })}>进程</button>
                      <button className="btn-glass-soft btn-glass-soft-sm btn-ghost" title="在线修改环境变量 (docker update)" onClick={() => setEnvModal({ name: c.name })}>改Env</button>
                      <button className="btn-glass-soft btn-glass-soft-sm btn-ghost" disabled={!canWrite || c.state !== 'running'} title="文件互拷 (docker cp)" onClick={() => setCpModal({ name: c.name })}>互拷</button>
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

      {statsView && (
        <div className="modal-overlay" onClick={() => setStatsView(null)}>
          <div className="modal stats-modal" onClick={(e) => e.stopPropagation()}>
            <div className="modal-head">
              <div className="modal-title">
                监控: {statsView.name}
                <button className="btn-glass-soft btn-glass-soft-sm" style={{ marginLeft: 8 }} onClick={() => refreshStats(statsView.name)}>刷新</button>
              </div>
              <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => setStatsView(null)}>关闭</button>
            </div>
            {statsData ? (
              <div className="stats-grid">
                <div className="metric-cell">
                  <span className="metric-label">CPU 占用</span>
                  <span className="metric-val">{statsData.cpuPct ?? '—'}</span>
                  <span className="metric-sub dim">单次快照</span>
                </div>
                <div className="metric-cell">
                  <span className="metric-label">内存</span>
                  <span className="metric-val">{statsData.memUsage ?? '—'}</span>
                  <span className="metric-sub dim">上限 {statsData.memLimit ?? '—'} · 占比 {statsData.memPerc ?? '—'}</span>
                </div>
                <div className="metric-cell">
                  <span className="metric-label">网络 IO (RX / TX)</span>
                  <span className="metric-val">{statsData.netIO ?? '—'}</span>
                </div>
                <div className="metric-cell">
                  <span className="metric-label">块 IO (读 / 写)</span>
                  <span className="metric-val">{statsData.blockIO ?? '—'}</span>
                </div>
                <div className="metric-cell">
                  <span className="metric-label">进程数 PIDs</span>
                  <span className="metric-val">{statsData.pids ?? '—'}</span>
                </div>
                <div className="metric-cell">
                  <span className="metric-label">容器 ID</span>
                  <span className="metric-val mono" style={{ fontSize: '0.75rem' }}>{statsData.id || '—'}</span>
                </div>
              </div>
            ) : (
              <p className="dim" style={{ padding: '2rem', textAlign: 'center' }}>无数据(容器可能已停止或运行时不支持)</p>
            )}
            <p className="dim" style={{ fontSize: '0.6875rem', marginTop: 8 }}>一次性快照 · docker stats --no-stream · 兼容 docker/podman · 点“刷新”查看最新数值</p>
          </div>
        </div>
      )}

      {runModal && (
        <ContainerRunModal
          initial={runModal}
          host={selected?.id || ''}
          onClose={() => setRunModal(null)}
          onDone={(ok, m) => { setRunModal(null); onMsg?.(m); if (ok) setTimeout(load, 500) }}
        />
      )}

      {envModal && <EnvEditModal host={selected?.id || ''} name={envModal.name} onClose={() => setEnvModal(null)} onMsg={onMsg} />}
      {cpModal && <DockerCpModal host={selected?.id || ''} name={cpModal.name} onClose={() => setCpModal(null)} onMsg={onMsg} />}
      {topModal && <ContainerTopModal host={selected?.id || ''} name={topModal.name} onClose={() => setTopModal(null)} onMsg={onMsg} />}
      {dfOpen && <SystemDfModal host={selected?.id || ''} onClose={() => setDfOpen(false)} />}
      {pruneOpen && (
        <ConfirmModal title="清理停止容器" desc="将删除所有已停止的容器 (docker container prune -f)。运行中的容器不受影响。" danger
          onCancel={() => setPruneOpen(false)} onOk={pruneContainers} okLabel="清理" />
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

  // ── Run 完整参数(Docker Desktop 对标) ──
  const [memLimit, setMemLimit] = useState(initial.memLimit || '')
  const [user, setUser] = useState(initial.user || '')
  const [hostname, setHostname] = useState(initial.hostname || '')
  const [workdir, setWorkdir] = useState(initial.workdir || '')
  const [privileged, setPrivileged] = useState(!!initial.privileged)
  const [autoHealth, setAutoHealth] = useState(!!(initial as any).autoHealth || !!(initial as any).healthcheck)
  const [restartDelay, setRestartDelay] = useState('')

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
      memLimit: memLimit.trim(),
      user: user.trim(),
      hostname: hostname.trim(),
      workdir: workdir.trim(),
      privileged,
      autoHealth,
      restartDelay: restartDelay.trim() ? parseInt(restartDelay.trim()) : 0,
    })
      .then((d: any) => onDone(d.ok, d.ok ? `✓ ${isRecreate ? '重建' : '创建'} ${name} 成功` : '✗ ' + (d.error || '失败')))
      .catch((e) => onDone(false, '✗ ' + String(e)))
  }

  const rowStyle = { display: 'grid', gridTemplateColumns: 'minmax(0,1fr) minmax(0,1fr) 4.5rem auto', gap: '0.375rem', alignItems: 'center' } as const

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
            <div key={i} className="form-grid-row" style={{ gridTemplateColumns: 'minmax(0,1.4fr) minmax(0,1fr) auto auto' }}>
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
            <div key={i} className="form-grid-row" style={{ gridTemplateColumns: 'minmax(0,1fr) minmax(0,2fr) auto' }}>
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

          <div className="dim" style={{ fontSize: '0.6875rem', fontWeight: 700, letterSpacing: '0.04em' }}>资源与高级(可选)</div>
          <div className="form-row2">
            <label className="form-field"><span>内存限制</span>
              <input className="input mono" placeholder="512m / 1g (留空不限)" value={memLimit} onChange={(e) => setMemLimit(e.target.value)} />
            </label>
            <label className="form-field"><span>运行用户</span>
              <input className="input mono" placeholder="1000:1000 或 root" value={user} onChange={(e) => setUser(e.target.value)} />
            </label>
            <label className="form-field"><span>主机名</span>
              <input className="input mono" placeholder="my-host (留空用容器名)" value={hostname} onChange={(e) => setHostname(e.target.value)} />
            </label>
            <label className="form-field"><span>工作目录</span>
              <input className="input mono" placeholder="/app (留空用镜像默认)" value={workdir} onChange={(e) => setWorkdir(e.target.value)} />
            </label>
          </div>
          <div className="form-row2" style={{ alignItems: 'center' }}>
            <label style={{ display: 'flex', gap: 6, alignItems: 'center', fontSize: '0.75rem', cursor: 'pointer' }}>
              <input type="checkbox" checked={privileged} onChange={(e) => setPrivileged(e.target.checked)} style={{ accentColor: 'var(--accent,#7c6cf6)' }} />
              特权模式 <span className="dim">(--privileged)</span>
            </label>
            <label style={{ display: 'flex', gap: 6, alignItems: 'center', fontSize: '0.75rem', cursor: 'pointer' }}>
              <input type="checkbox" checked={autoHealth} onChange={(e) => setAutoHealth(e.target.checked)} style={{ accentColor: 'var(--accent,#7c6cf6)' }} />
              简易健康检查 <span className="dim">(curl 127.0.0.1)</span>
            </label>
            {restart === 'on-failure' && (
              <label className="form-field" style={{ flex: 1 }}><span>on-failure 重启上限</span>
                <input className="input" type="number" min={1} max={99} placeholder="如 5 (留空=无限)" value={restartDelay} onChange={(e) => setRestartDelay(e.target.value)} />
              </label>
            )}
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
  rt?: string
}

function fmtBytes(n: number): string {
  if (!isFinite(n) || n < 0) return '—'
  if (n < 1024) return `${n} B`
  const units = ['KiB', 'MiB', 'GiB', 'TiB', 'PiB']
  let v = n
  let i = -1
  do { v /= 1024; i++ } while (v >= 1024 && i < units.length - 1)
  return `${v.toFixed(1)} ${units[i]}`
}

function ImagesPanel({ onMsg }: { onMsg?: (m: string) => void }) {
  const { selected } = useHost()
  const [images, setImages] = useState<{ repo: string; tag: string; id: string; size: string; key?: string }[]>([])
  const [pullImage, setPullImage] = useState('')
  const [busy, setBusy] = useState(false)
  const [out, setOut] = useState('')
  const [sel, setSel] = useState<Set<string>>(new Set())
  const [job, setJob] = useState<PullJobState | null>(null)
  const [tagModal, setTagModal] = useState<string | null>(null)
  const [histModal, setHistModal] = useState<string | null>(null)
  const [tagInput, setTagInput] = useState('')
  const [hist, setHist] = useState<any[] | null>(null)
  const [regFilter, setRegFilter] = useState('')
  const [pullRT, setPullRT] = useState('') // 目标运行时: '' 自动 | docker | podman | crictl | ctr
  const [migrateModal, setMigrateModal] = useState('') // 跨运行时迁移的镜像名
  const [migrateImage, setMigrateImage] = useState('')
  const [saveModal, setSaveModal] = useState<string | null>(null) // 导出镜像(默认名)
  const [loadOpen, setLoadOpen] = useState(false)
  const [pruneOpen, setPruneOpen] = useState(false)

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

  // ── 仓库来源筛选: 按镜像名前缀提取仓库域名 ──
  const regOf = (repo: string): string => {
    const r = (repo || '').trim()
    if (!r || r === '<none>') return '悬挂镜像(<none>)'
    const first = r.split('/')[0]
    // 第一段含 . 或 : 视为仓库地址(域名/端口), 否则为官方/本地无前缀
    return first.includes('.') || first.includes(':') ? first : '无前缀(官方/本地)'
  }
  const registries = Array.from(new Set(images.map((im) => regOf(im.repo)))).sort((a, b) => a.localeCompare(b))
  const visible = regFilter ? images.filter((im) => regOf(im.repo) === regFilter) : images

  const startPull = (rt?: string) => {
    const img = pullImage.trim()
    const target = rt ?? pullRT
    if (!img) return
    setBusy(true); setOut(''); setSel(new Set())
    postJSON('/api/plugins/containers/docker/pull/async', { host: selected?.id || '', image: img, rt: target })
      .then((d: any) => {
        if (d.ok) {
          setJob({ id: d.jobId, done: false, err: '', image: img, lines: [], secs: 0, layersDone: 0, layersTotal: 0, rt: target })
        } else {
          onMsg?.('✗ ' + (d.error || '发起失败'))
        }
      })
      .catch((e) => onMsg?.('✗ ' + String(e)))
      .finally(() => setBusy(false))
  }

  const startMigrate = () => {
    const image = migrateImage.trim()
    if (!image) return
    setMigrateModal('')
    setPullRT('ctr')           // 迁移路径固定以 ctr(k8s.io) 为目标通道
    setPullImage(image)
    startPull('ctr')
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

  const doTag = (image: string) => {
    const t = tagInput.trim()
    if (!t) return
    setBusy(true)
    postJSON('/api/plugins/containers/docker/image/tag', { host: selected?.id || '', image, tag: t })
      .then((d: any) => {
        if (d.ok) { onMsg?.(`✓ 已打标签 ${image} → ${t}`); setTagModal(null); setTagInput(''); setTimeout(load, 500) }
        else onMsg?.('✗ ' + (d.error || '失败'))
      })
      .catch((e) => onMsg?.('✗ ' + String(e))).finally(() => setBusy(false))
  }

  const pruneImages = () => {
    setPruneOpen(false); setBusy(true)
    dockerTool(selected?.id || '', { scope: 'image', action: 'prune' })
      .then((d: any) => onMsg?.(d.ok ? '✓ 已清理悬挂镜像' : '✗ ' + (d.error || '失败')))
      .catch((e) => onMsg?.('✗ ' + String(e)))
      .finally(() => { setBusy(false); setTimeout(load, 500) })
  }

  const doPush = (image: string) => {
    setBusy(true); setOut('')
    postJSON('/api/plugins/containers/docker/image/push', { host: selected?.id || '', image })
      .then((d: any) => {
        if (d.ok) { onMsg?.(`✓ 推送完成: ${image}`); setOut(d.output || '') }
        else { onMsg?.('✗ ' + (d.error || '推送失败(镜像名需含仓库前缀)')); setOut(d.output || d.error || '') }
      })
      .catch((e) => { onMsg?.('✗ ' + String(e)); setOut(String(e)) })
      .finally(() => setBusy(false))
  }

  const openHistory = (image: string) => {
    setHistModal(image); setHist(null)
    getJSON<{ ok?: boolean; history?: any[] }>(`/api/plugins/containers/docker/image/history?${hostQ.replace('&', '')}&image=${encodeURIComponent(image)}`)
      .then((d) => setHist(d.history || []))
      .catch((e) => onMsg?.('✗ ' + String(e)))
  }

  return (
    <>
      <Card title="拉取镜像" subtitle="异步拉取 · 实时显示层进度">
        <div className="btn-row" style={{ alignItems: 'center' }}>
          <input className="input" style={{ flex: 1, minWidth: 200 }} value={pullImage} onChange={(e) => setPullImage(e.target.value)}
            placeholder="镜像名, 如 nginx:1.27-alpine 或 registry.example.com/app:v1"
            onKeyDown={(e) => e.key === 'Enter' && !busy && startPull()} disabled={!!job && !job.done} />
          <select className="input" style={{ width: 130, height: 'auto', flexShrink: 0 }}
            value={pullRT} onChange={(e) => setPullRT(e.target.value)} title="拉取到哪个运行时 · crictl/ctr 将走迁移链(docker pull→save→import)">
            <option value="">自动(探测)</option>
            <option value="docker">docker</option>
            <option value="podman">podman</option>
            <option value="crictl">crictl(K8s)</option>
            <option value="ctr">ctr→k8s.io</option>
          </select>
          <button className="btn-glass-soft btn-glass-soft-accent" disabled={busy || !pullImage.trim() || (!!job && !job.done)} onClick={() => startPull()}>拉取</button>
        </div>
        {job && (
          <div style={{ marginTop: '0.625rem' }}>
            <div className="btn-row" style={{ justifyContent: 'space-between', fontSize: '0.6875rem' }}>
              <span>
                {job.done ? (job.err ? `✗ ${job.image} 失败` : `✓ ${job.image} 完成`) : `拉取中: ${job.image}`}
                {job.rt === 'crictl' || job.rt === 'ctr'
                  ? <span className="pill pill-sub" style={{ marginLeft: 6 }}>迁移链: docker→.{job.rt}</span>
                  : <span className="pill pill-sub" style={{ marginLeft: 6 }}>→ {job.rt || '自动'}</span>}
              </span>
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
            {job.done && job.err && job.rt !== 'crictl' && job.rt !== 'ctr' && (
              <div className="btn-row" style={{ marginTop: '0.5rem', gap: 8 }}>
                <span className="dim" style={{ fontSize: '0.75rem' }}>直接拉取失败? 常见原因是目标机无 docker/podman 或网络与镜像源不通</span>
                <button className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-accent"
                  onClick={() => { setMigrateModal(job.image); setTimeout(() => setJob(null), 0) }}>
                  改用跨运行时迁移(dock→ctr)
                </button>
              </div>
            )}
          </div>
        )}
        {out && <pre className="code-block" style={{ maxHeight: 140, overflow: 'auto', marginTop: '0.5rem', fontSize: '0.6875rem' }}>{out}</pre>}
      </Card>

      {migrateModal && (
        <div className="modal-overlay" onClick={() => setMigrateModal('')}>
          <div className="modal" onClick={(e) => e.stopPropagation()} style={{ maxWidth: 540 }}>
            <h3>跨运行时迁移: {migrateModal}</h3>
            <p className="dim" style={{ marginTop: '0.5rem', lineHeight: 1.7 }}>
              目标机自动使用 docker(兼容别名亦可)拉取; 若直连失败将改走迁移链:<br />
              <span className="mono" style={{ fontSize: '0.75rem' }}>① docker pull → ② docker save 成 tar → ③ ctr -n k8s.io images import</span>
              <br />常用于 containerd(K8s 托管)无法直连镜像源、或知名代理被墙时。
            </p>
            <div className="btn-row" style={{ marginTop: '1rem' }}>
              <input className="input" style={{ flex: 1 }} defaultValue={migrateModal} placeholder="可修改镜像名" disabled={busy}
                onChange={(e) => setMigrateImage(e.target.value)} />
              <button className="btn-glass-soft" onClick={() => setMigrateModal('')}>取消</button>
              <button className="btn-glass-soft btn-glass-soft-accent" disabled={busy} onClick={startMigrate}>开始迁移</button>
            </div>
          </div>
        </div>
      )}

      <Card className="images-card" title={`镜像列表 (${images.length})`} subtitle={`已选 ${sel.size} · 支持全选/反选/批量删除`}>
        <div className="toolbar-strip">
          <button className="btn-glass-soft btn-glass-soft-sm" onClick={toggleAll}>{allChecked ? '取消全选' : '全选'}</button>
          <button className="btn-glass-soft btn-glass-soft-sm" onClick={toggleInvert}>反选</button>
          <span className="dim">已选 {sel.size}</span>
          <select className="input" style={{ width: 210, height: 'auto', marginLeft: 'auto' }}
            value={regFilter} onChange={(e) => setRegFilter(e.target.value)}
            title="按仓库来源筛选镜像">
            <option value="">全部仓库 ({images.length})</option>
            {registries.map((r) => (
              <option key={r} value={r}>{r} ({images.filter((im) => regOf(im.repo) === r).length})</option>
            ))}
          </select>
          <button className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-danger" disabled={busy || !sel.size} onClick={batchRemove}>删除选中</button>
          <span className="dim" style={{ marginLeft: 'auto' }} />
          <button className="btn-glass-soft btn-glass-soft-sm btn-ghost" disabled={busy} title="导出为 tar 下载 (docker save)" onClick={() => setSaveModal(null)}>导出镜像</button>
          <button className="btn-glass-soft btn-glass-soft-sm btn-ghost" disabled={busy} title="从 tar 导入 (docker load)" onClick={() => setLoadOpen(true)}>导入镜像</button>
          <button className="btn-glass-soft btn-glass-soft-sm btn-ghost" disabled={busy} title="清理无引用镜像 (docker image prune -f)" onClick={() => setPruneOpen(true)}>清理悬挂镜像</button>
        </div>
        <div className="table-wrap">
          <table className="data-table">
            <thead><tr>
              <th style={{ width: '4%' }}><input type="checkbox" checked={allChecked} onChange={toggleAll} /></th>
              <th style={{ width: '30%' }}>仓库</th><th style={{ width: '12%' }}>标签</th>
              <th style={{ width: '14%' }}>ID</th><th style={{ width: '12%' }}>大小</th><th style={{ width: '28%' }}>操作</th>
            </tr></thead>
            <tbody>
              {visible.length === 0 && <tr><td colSpan={6} className="dim">{images.length === 0 ? '（无镜像）' : '（该仓库下无镜像）'}</td></tr>}
              {visible.map((im, i) => (
                <tr key={i}>
                  <td><input type="checkbox" checked={sel.has(im.key)} onChange={() => toggleOne(im.key)} /></td>
                  <td className="mono">{im.repo}</td>
                  <td className="mono">{im.tag}</td>
                  <td className="mono dim">{im.id}</td>
                  <td>{im.size}</td>
                  <td><div className="btn-row k8s-row-actions">
                    <button className="btn-glass-soft btn-glass-soft-sm" disabled={busy} title="镜像历史层" onClick={() => openHistory(im.key)}>历史</button>
                    <button className="btn-glass-soft btn-glass-soft-sm" disabled={busy} title="打标签" onClick={() => { setTagModal(im.key); setTagInput('') }}>tag</button>
                    <button className="btn-glass-soft btn-glass-soft-sm" disabled={busy} title="推送到仓库(镜像名需含仓库前缀)" onClick={() => doPush(im.key)}>push</button>
                    <button className="btn-glass-soft btn-glass-soft-sm" disabled={busy} title="导出此镜像为 tar 下载" onClick={() => setSaveModal(im.key)}>导出</button>
                    <button className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-danger" disabled={busy} onClick={() => removeOne(im.key)}>删除</button>
                  </div></td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </Card>

      {tagModal && (
        <div className="modal-overlay" onClick={() => setTagModal(null)}>
          <div className="modal" onClick={(e) => e.stopPropagation()} style={{ maxWidth: 560 }}>
            <h3>打标签: <span className="mono" style={{ fontWeight: 600 }}>{tagModal}</span></h3>
            <p className="dim" style={{ marginTop: '0.5rem' }}>新标签可含仓库前缀以准备推送, 如 <span className="mono">registry.example.com/app:v2</span></p>
            <div className="btn-row" style={{ alignItems: 'center', marginTop: '0.625rem' }}>
              <input className="input" style={{ flex: 1, minWidth: 0 }} value={tagInput}
                onChange={(e) => setTagInput(e.target.value)} placeholder="新标签, 如 my-app:v2"
                onKeyDown={(e) => e.key === 'Enter' && !busy && doTag(tagModal)} autoFocus />
              <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => setTagModal(null)}>取消</button>
              <button className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-accent" disabled={busy || !tagInput.trim()} onClick={() => doTag(tagModal)}>确认</button>
            </div>
          </div>
        </div>
      )}

      {histModal && (
        <div className="modal-overlay" onClick={() => setHistModal(null)}>
          <div className="modal log-modal" onClick={(e) => e.stopPropagation()} style={{ maxWidth: 860 }}>
            <div className="modal-head">
              <div className="modal-title">镜像历史层: {histModal}</div>
              <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => setHistModal(null)}>关闭</button>
            </div>
            <div className="table-wrap" style={{ maxHeight: 420, border: 'none' }}>
              <table className="data-table">
                <thead><tr>
                  <th style={{ width: '14%' }}>ID</th><th style={{ width: '10%' }}>创建时间</th>
                  <th style={{ width: '12%' }}>大小</th><th style={{ width: '64%' }}>指令</th>
                </tr></thead>
                <tbody>
                  {!hist && <tr><td colSpan={4} className="dim">加载中…</td></tr>}
                  {hist && hist.length === 0 && <tr><td colSpan={4} className="dim">（无层信息）</td></tr>}
                  {hist?.map((h, i) => (
                    <tr key={i}>
                      <td className="mono dim">{String(h.ID || '').slice(0, 12) || '<missing>'}</td>
                      <td className="dim">{new Date(h.CreatedAt || Date.now()).toLocaleString()}</td>
                      <td className="dim">{Number(h.SizeBytes || 0) < 1 ? (h.SizeBytes === undefined ? h.Size ?? '0B' : '0B') : fmtBytes(Number(h.SizeBytes))}</td>
                      <td className="mono dim" style={{ fontSize: '0.6875rem' }}>{h.CreatedBy}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </div>
        </div>
      )}

      {saveModal !== null && saveModal !== undefined && (
        <ImageSaveModal host={selected?.id || ''} def={saveModal || ''} onClose={() => setSaveModal(null)} />
      )}
      {loadOpen && <ImageLoadModal host={selected?.id || ''} onClose={() => setLoadOpen(false)} onMsg={onMsg} />}
      {pruneOpen && (
        <ConfirmModal title="清理悬挂镜像" desc="将删除所有未被容器引用的镜像 (docker image prune -f)。正在使用的镜像不受影响。" danger
          onCancel={() => setPruneOpen(false)} onOk={pruneImages} okLabel="清理" />
      )}
    </>
  )
}

// ── Volumes 卷管理 ──

function VolumesPanel({ onMsg }: { onMsg?: (m: string) => void }) {
  const { selected } = useHost()
  const [vols, setVols] = useState<any[]>([])
  const [busy, setBusy] = useState(false)
  const [createName, setCreateName] = useState('')
  const [confirmDel, setConfirmDel] = useState<string | null>(null)

  const hostQ = selected?.id ? `&host=${encodeURIComponent(selected.id)}` : ''
  const load = () => getJSON<{ ok?: boolean; volumes?: any[] }>(`/api/plugins/containers/docker/volumes?_=${Date.now()}${hostQ}`)
    .then((d) => setVols(d.volumes || [])).catch(() => setVols([]))
  useEffect(() => { load() }, [selected?.id])

  const doCreate = () => {
    const n = createName.trim()
    if (!n) return
    setBusy(true)
    postJSON('/api/plugins/containers/docker/volumes/action', { host: selected?.id || '', name: n, action: 'create' })
      .then((d: any) => { onMsg?.(d.ok ? `✓ 卷 ${n} 创建成功` : '✗ ' + (d.error || '失败')); if (d.ok) setCreateName(''); setTimeout(load, 400) })
      .catch((e) => onMsg?.('✗ ' + String(e))).finally(() => setBusy(false))
  }

  const doRemove = (n: string) => {
    setConfirmDel(null); setBusy(true)
    postJSON('/api/plugins/containers/docker/volumes/action', { host: selected?.id || '', name: n, action: 'remove' })
      .then((d: any) => { onMsg?.(d.ok ? `✓ 卷 ${n} 已删除` : '✗ ' + (d.error || '失败')); setTimeout(load, 400) })
      .catch((e) => onMsg?.('✗ ' + String(e))).finally(() => setBusy(false))
  }

  return (
    <>
      <Card title="创建卷" subtitle="Docker named volume · 挂载到容器时可用卷名代替宿主路径">
        <div className="btn-row" style={{ alignItems: 'center' }}>
          <input className="input" style={{ flex: 1, minWidth: 240 }} value={createName} onChange={(e) => setCreateName(e.target.value)}
            placeholder="卷名, 如 app-data" onKeyDown={(e) => e.key === 'Enter' && !busy && doCreate()} />
          <button className="btn-glass-soft btn-glass-soft-accent" disabled={busy || !createName.trim()} onClick={doCreate}>创建</button>
        </div>
      </Card>
      <Card title={`Volumes (${vols.length})`} subtitle="Docker 数据卷列表 · 删除前需先解绑占用容器">
        <div className="table-wrap">
          <table className="data-table">
            <thead><tr>
              <th style={{ width: '30%' }}>名称</th><th style={{ width: '14%' }}>Driver</th>
              <th style={{ width: '36%' }}>挂载点</th><th style={{ width: '20%' }}>操作</th>
            </tr></thead>
            <tbody>
              {vols.length === 0 && <tr><td colSpan={4} className="dim">（无卷）</td></tr>}
              {vols.map((v, i) => (
                <tr key={i}>
                  <td className="mono">{v.Name}</td>
                  <td className="dim">{v.Driver || 'local'}</td>
                  <td className="mono dim" style={{ fontSize: '0.6875rem' }}>{v.Mountpoint || '—'}</td>
                  <td><div className="btn-row k8s-row-actions">
                    <button className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-danger" disabled={busy} onClick={() => setConfirmDel(v.Name)}>删除</button>
                  </div></td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </Card>

      {confirmDel && (
        <div className="modal-overlay" onClick={() => setConfirmDel(null)}>
          <div className="modal" onClick={(e) => e.stopPropagation()} style={{ maxWidth: 420 }}>
            <h3>删除卷: {confirmDel}</h3>
            <p className="dim">目标主机 <b>{selected?.label || '本机'}</b> · 若被容器占用将报错</p>
            <div className="modal-actions">
              <button className="btn-glass-soft" onClick={() => setConfirmDel(null)}>取消</button>
              <button className="btn btn-danger" onClick={() => doRemove(confirmDel)}>确认删除</button>
            </div>
          </div>
        </div>
      )}
    </>
  )
}

// ── Networks 网络管理 ──

function NetworksPanel({ onMsg }: { onMsg?: (m: string) => void }) {
  const { selected } = useHost()
  const [nets, setNets] = useState<any[]>([])
  const [busy, setBusy] = useState(false)
  const [createName, setCreateName] = useState('')
  const [createDriver, setCreateDriver] = useState('bridge')
  const [confirmDel, setConfirmDel] = useState<string | null>(null)
  const [netModal, setNetModal] = useState<{ action: 'connect' | 'disconnect'; net: string } | null>(null)
  const [containers, setContainers] = useState<{ name: string }[]>([])

  const openNetModal = (action: 'connect' | 'disconnect', net: string) => {
    setNetModal({ action, net })
    getJSON<any>(`/api/plugins/containers/list?_=${Date.now()}${hostQ}`)
      .then((d) => setContainers((d?.containers || []).map((c: any) => ({ name: c.name || '' })).filter((c: any) => c.name)))
      .catch(() => setContainers([]))
  }

  const hostQ = selected?.id ? `&host=${encodeURIComponent(selected.id)}` : ''
  const load = () => getJSON<{ ok?: boolean; networks?: any[] }>(`/api/plugins/containers/docker/networks?_=${Date.now()}${hostQ}`)
    .then((d) => setNets(d.networks || [])).catch(() => setNets([]))
  useEffect(() => { load() }, [selected?.id])

  const doCreate = () => {
    const n = createName.trim()
    if (!n) return
    setBusy(true)
    postJSON('/api/plugins/containers/docker/networks/action', { host: selected?.id || '', name: n, driver: createDriver, action: 'create' })
      .then((d: any) => { onMsg?.(d.ok ? `✓ 网络 ${n} 创建成功` : '✗ ' + (d.error || '失败')); if (d.ok) setCreateName(''); setTimeout(load, 400) })
      .catch((e) => onMsg?.('✗ ' + String(e))).finally(() => setBusy(false))
  }

  const doRemove = (n: string) => {
    setConfirmDel(null); setBusy(true)
    postJSON('/api/plugins/containers/docker/networks/action', { host: selected?.id || '', name: n, action: 'remove' })
      .then((d: any) => { onMsg?.(d.ok ? `✓ 网络 ${n} 已删除` : '✗ ' + (d.error || '失败')); setTimeout(load, 400) })
      .catch((e) => onMsg?.('✗ ' + String(e))).finally(() => setBusy(false))
  }

  const isDefault = (n: any) => ['bridge', 'host', 'none'].includes(n.Name)

  return (
    <>
      <Card title="创建网络" subtitle="Docker Network · bridge 用于容器互联, overlay 用于 Swarm">
        <div className="btn-row" style={{ alignItems: 'center' }}>
          <select className="input" style={{ width: 120 }} value={createDriver} onChange={(e) => setCreateDriver(e.target.value)}>
            <option value="bridge">bridge</option><option value="host">host</option>
            <option value="overlay">overlay</option><option value="macvlan">macvlan</option>
            <option value="none">none</option>
          </select>
          <input className="input" style={{ flex: 1, minWidth: 240 }} value={createName} onChange={(e) => setCreateName(e.target.value)}
            placeholder="网络名, 如 app-net" onKeyDown={(e) => e.key === 'Enter' && !busy && doCreate()} />
          <button className="btn-glass-soft btn-glass-soft-accent" disabled={busy || !createName.trim()} onClick={doCreate}>创建</button>
        </div>
      </Card>
      <Card title={`Networks (${nets.length})`} subtitle="内建 bridge/host/none 不可删除">
        <div className="table-wrap">
          <table className="data-table">
            <thead><tr>
              <th style={{ width: '20%' }}>名称</th><th style={{ width: '16%' }}>Driver</th>
              <th style={{ width: '12%' }}>ID</th><th style={{ width: '28%' }}>子网</th><th style={{ width: '24%' }}>操作</th>
            </tr></thead>
            <tbody>
              {nets.length === 0 && <tr><td colSpan={5} className="dim">（无网络）</td></tr>}
              {nets.map((n, i) => (
                <tr key={i}>
                  <td className="mono">{n.Name}</td>
                  <td className="dim">{n.Driver}</td>
                  <td className="mono dim">{n.ID}</td>
                  <td className="mono dim" style={{ fontSize: '0.6875rem' }}>{n.Subnet ? `${n.Subnet}${n.Gateway ? ' · gw ' + n.Gateway : ''}` : '—'}</td>
                  <td><div className="btn-row k8s-row-actions">
                    <button className="btn-glass-soft btn-glass-soft-sm" disabled={busy} title="连接一个容器到该网络 (docker network connect)"
                      onClick={() => openNetModal('connect', n.Name)}>连接容器</button>
                    <button className="btn-glass-soft btn-glass-soft-sm btn-ghost" disabled={busy} title="将容器从该网络断开 (docker network disconnect)"
                      onClick={() => openNetModal('disconnect', n.Name)}>断开容器</button>
                    <button className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-danger" disabled={busy || isDefault(n)} title={isDefault(n) ? '内建网络不可删除' : ''}
                      onClick={() => setConfirmDel(n.Name)}>删除</button>
                  </div></td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </Card>

      {confirmDel && (
        <div className="modal-overlay" onClick={() => setConfirmDel(null)}>
          <div className="modal" onClick={(e) => e.stopPropagation()} style={{ maxWidth: 420 }}>
            <h3>删除网络: {confirmDel}</h3>
            <p className="dim">目标主机 <b>{selected?.label || '本机'}</b> · 删除失败时可能仍有容器连接</p>
            <div className="modal-actions">
              <button className="btn-glass-soft" onClick={() => setConfirmDel(null)}>取消</button>
              <button className="btn btn-danger" onClick={() => doRemove(confirmDel)}>确认删除</button>
            </div>
          </div>
        </div>
      )}

      {netModal && (
        <NetConnectModal host={selected?.id || ''} net={netModal.net} action={netModal.action} containers={containers}
          onClose={() => setNetModal(null)} onMsg={onMsg} />
      )}
    </>
  )
}

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

// ── Docker 补齐 (tool/action 单端点): 磁盘占用 / 进程 top / 改 Env / 互拷 / 镜像导出导入 / 网络连接 ──

function dockerTool(host: string, body: Record<string, any>): Promise<any> {
  return postJSON('/api/plugins/containers/docker/tool/action', { host: host || '', ...body })
}

function b64Download(b64: string, fileName: string) {
  const bin = atob(b64)
  const bytes = new Uint8Array(bin.length)
  for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i)
  const blob = new Blob([bytes])
  const a = document.createElement('a')
  a.href = URL.createObjectURL(blob)
  a.download = fileName
  a.click()
  setTimeout(() => URL.revokeObjectURL(a.href), 5000)
}

function readFileB64(file: File, cb: (b64: string) => void, err: (m: string) => void) {
  const rd = new FileReader()
  rd.onload = () => {
    const s = String(rd.result || '')
    const i = s.indexOf(',')
    cb(i >= 0 ? s.slice(i + 1) : s)
  }
  rd.onerror = () => err('读取文件失败')
  rd.readAsDataURL(file)
}

function ToolOutModal({ title, body, onClose }: { title: string; body: string; onClose: () => void }) {
  return (
    <div className="modal-overlay" onClick={onClose}>
      <div className="modal log-modal" onClick={(e) => e.stopPropagation()} style={{ maxWidth: 760 }}>
        <div className="modal-head">
          <div className="modal-title">{title}</div>
          <button className="btn-glass-soft btn-glass-soft-sm" onClick={onClose}>关闭</button>
        </div>
        <pre className="code-block" style={{ margin: 0, maxHeight: 480, overflow: 'auto', fontSize: '0.6875rem', whiteSpace: 'pre-wrap' }}>{body}</pre>
      </div>
    </div>
  )
}

function ConfirmModal({ title, desc, danger, busy, onCancel, onOk, okLabel }: {
  title: string; desc: string; danger?: boolean; busy?: boolean
  onCancel: () => void; onOk: () => void; okLabel?: string
}) {
  return (
    <div className="modal-overlay" onClick={onCancel}>
      <div className="modal" onClick={(e) => e.stopPropagation()} style={{ maxWidth: 420 }}>
        <h3>{title}</h3>
        <p className="dim">{desc}</p>
        <div className="modal-actions">
          <button className="btn-glass-soft" onClick={onCancel} disabled={busy}>取消</button>
          <button className={`btn ${danger ? 'btn-danger' : 'btn-accent'}`} disabled={busy} onClick={onOk}>{busy ? '执行中…' : (okLabel || '确认')}</button>
        </div>
      </div>
    </div>
  )
}

// 容器环境变量在线修改 (docker update --env-add/--env-rm)
function EnvEditModal({ host, name, onClose, onMsg }: { host: string; name: string; onClose: () => void; onMsg?: (m: string) => void }) {
  const [addKeys, setAddKeys] = useState('')
  const [rmKeys, setRmKeys] = useState('')
  const [busy, setBusy] = useState(false)
  const parseKV = (s: string) => {
    const env: Record<string, string> = {}
    for (const kv of s.split(',')) {
      const t = kv.trim(); if (!t) continue
      const i = t.indexOf('='); if (i < 0) continue
      env[t.slice(0, i).trim()] = t.slice(i + 1).trim()
    }
    return env
  }
  const apply = () => {
    setBusy(true)
    dockerTool(host, { scope: 'container', action: 'env-update', name, env: parseKV(addKeys), removeEnv: rmKeys.split(',').map((k) => k.trim()).filter(Boolean) })
      .then((d: any) => { onMsg?.(d.ok ? `✓ 已更新环境变量 ${name}` : '✗ ' + (d.error || '失败')); if (d.ok) onClose() })
      .catch((e) => onMsg?.('✗ ' + String(e))).finally(() => setBusy(false))
  }
  return (
    <div className="modal-overlay" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()} style={{ maxWidth: 560 }}>
        <h3>修改环境变量 — {name}</h3>
        <p className="dim" style={{ fontSize: '0.75rem', marginTop: '0.25rem' }}>docker update 仅影响重启后新进程的环境; 运行中的进程需容器外改。</p>
        <div style={{ marginTop: '0.75rem', display: 'grid', gap: '0.5rem' }}>
          <input className="input mono" placeholder="新增/覆盖 env, 逗号分隔 K=V, 如 A=1,B=2" value={addKeys} onChange={(e) => setAddKeys(e.target.value)} disabled={busy} />
          <input className="input mono" placeholder="删除 env 键, 逗号分隔, 如 OLD_KEY" value={rmKeys} onChange={(e) => setRmKeys(e.target.value)} disabled={busy} />
        </div>
        <div className="modal-actions">
          <button className="btn-glass-soft" onClick={onClose} disabled={busy}>取消</button>
          <button className="btn btn-accent" disabled={busy || (!addKeys.trim() && !rmKeys.trim())} onClick={apply}>应用</button>
        </div>
      </div>
    </div>
  )
}

// 容器文件互拷 (docker cp, 经 base64 传输, 单文件 ≤100MB)
function DockerCpModal({ host, name, onClose, onMsg }: { host: string; name: string; onClose: () => void; onMsg?: (m: string) => void }) {
  const [direction, setDirection] = useState<'from-pod' | 'to-pod'>('from-pod')
  const [path, setPath] = useState('')
  const [file, setFile] = useState<File | null>(null)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const doOut = () => {
    setBusy(true); setErr('')
    dockerTool(host, { scope: 'container', action: 'cp-out', name, path: path.trim() })
      .then((d: any) => {
        if (d.ok) { b64Download(d.archive, d.base ? `${name}-${d.base}.tar` : `${name}.tar`); onMsg?.(`✓ 已下载 ${d.base} (${(d.size / 1048576).toFixed(2)} MiB)`) }
        else setErr(d.error || '失败')
      })
      .catch((e) => setErr(String(e))).finally(() => setBusy(false))
  }
  const doIn = () => {
    if (!file) return
    setBusy(true); setErr('')
    readFileB64(file, (b64) => {
      dockerTool(host, { scope: 'container', action: 'cp-in', name, dir: path.trim(), archiveB64: b64 })
        .then((d: any) => { if (d.ok) { onMsg?.(`✓ 已上传 ${file.name} → ${path.trim()}`); onClose() } else setErr(d.error || '失败') })
        .catch((e) => setErr(String(e))).finally(() => setBusy(false))
    }, setErr)
  }
  return (
    <div className="modal-overlay" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()} style={{ maxWidth: 620 }}>
        <h3>文件互拷 — {name}</h3>
        <div className="btn-row" style={{ marginTop: '0.5rem' }}>
          <button className={`btn-glass-soft btn-glass-soft-sm ${direction === 'from-pod' ? 'btn-glass-soft-accent' : ''}`} onClick={() => setDirection('from-pod')}>从容器下载</button>
          <button className={`btn-glass-soft btn-glass-soft-sm ${direction === 'to-pod' ? 'btn-glass-soft-accent' : ''}`} onClick={() => setDirection('to-pod')}>上传到容器</button>
        </div>
        <div style={{ marginTop: '0.75rem', display: 'grid', gap: '0.5rem' }}>
          <input className="input mono" style={{ width: '100%' }} placeholder={direction === 'from-pod' ? '容器内路径, 如 /app/app.log' : '容器内目标目录, 如 /data'}
            value={path} onChange={(e) => setPath(e.target.value)} disabled={busy} />
          {direction === 'to-pod' && <input type="file" onChange={(e) => setFile(e.target.files?.[0] || null)} disabled={busy} />}
        </div>
        {err && <div style={{ marginTop: '0.5rem', color: 'var(--danger)' }}>{err}</div>}
        <div className="modal-actions">
          <button className="btn-glass-soft" onClick={onClose} disabled={busy}>关闭</button>
          <button className="btn btn-accent" disabled={busy || !path.trim() || (direction === 'to-pod' && !file)} onClick={direction === 'from-pod' ? doOut : doIn}>
            {busy ? '处理中…' : (direction === 'from-pod' ? '下载' : '上传')}
          </button>
        </div>
      </div>
    </div>
  )
}

// 容器进程视图 (docker top)
function ContainerTopModal({ host, name, onClose, onMsg }: { host: string; name: string; onClose: () => void; onMsg?: (m: string) => void }) {
  const [out, setOut] = useState('')
  useEffect(() => {
    dockerTool(host, { scope: 'container', action: 'top', name })
      .then((d: any) => setOut(d.ok ? d.output : '✗ ' + (d.error || '失败')))
      .catch((e) => setOut('✗ ' + String(e)))
  }, [])
  return <ToolOutModal title={`进程列表: ${name}`} body={out || '查询中…'} onClose={onClose} />
}

// 磁盘占用总览 (docker system df)
function SystemDfModal({ host, onClose }: { host: string; onClose: () => void }) {
  const [out, setOut] = useState('')
  useEffect(() => {
    dockerTool(host, { scope: 'system', action: 'df' })
      .then((d: any) => {
        if (!d.ok) { setOut('✗ ' + (d.error || '失败')); return }
        const rows = (d.rows || []) as any[]
        setOut([
          `TYPE\ttotal\tactive\tsize\treclaimable`,
          ...rows.map((r: any) => `${r['Type'] ?? r.type ?? '-'}\t${r['Total'] ?? '-'}\t${r['Active'] ?? '-'}\t${r['Size'] ?? '-'}\t${r['Reclaimable'] ?? '-'}`),
        ].join('\n'))
      })
      .catch((e) => setOut('✗ ' + String(e)))
  }, [])
  return <ToolOutModal title="磁盘占用 (docker system df)" body={out || '查询中…'} onClose={onClose} />
}

// 网络连接容器 / 断开容器
function NetConnectModal({ host, net, action, containers, onClose, onMsg }: {
  host: string; net: string; action: 'connect' | 'disconnect'
  containers: { name: string }[]; onClose: () => void; onMsg?: (m: string) => void
}) {
  const [container, setContainer] = useState(containers[0]?.name || '')
  const [alias, setAlias] = useState('')
  const [force, setForce] = useState(false)
  const [busy, setBusy] = useState(false)
  const titleText = action === 'connect' ? `连接实例到网络: ${net}` : `从网络断开: ${net}`
  const doIt = () => {
    if (!container) return
    setBusy(true)
    dockerTool(host, { scope: 'network', action, network: net, container, alias: alias.trim(), force })
      .then((d: any) => { onMsg?.(d.ok ? `✓ ${action} ${container} ${net}` : '✗ ' + (d.error || '失败')); if (d.ok) onClose() })
      .catch((e) => onMsg?.('✗ ' + String(e))).finally(() => setBusy(false))
  }
  return (
    <div className="modal-overlay" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()} style={{ maxWidth: 520 }}>
        <h3>{titleText}</h3>
        <div style={{ marginTop: '0.75rem', display: 'grid', gap: '0.5rem' }}>
          <select className="input" value={container} onChange={(e) => setContainer(e.target.value)} disabled={busy || !containers.length}>
            {containers.length === 0 && <option value="">（无容器可连接）</option>}
            {containers.map((c) => <option key={c.name} value={c.name}>{c.name}</option>)}
          </select>
          {action === 'connect' && (
            <input className="input mono" placeholder="网络别名(可选)" value={alias} onChange={(e) => setAlias(e.target.value)} disabled={busy} />
          )}
          {action === 'disconnect' && (
            <label className="k8s-check" style={{ display: 'inline-flex', alignItems: 'center', gap: 6, fontSize: '0.8125rem' }}>
              <input type="checkbox" checked={force} onChange={(e) => setForce(e.target.checked)} disabled={busy} /> 强制断开 (--force)
            </label>
          )}
        </div>
        <div className="modal-actions">
          <button className="btn-glass-soft" onClick={onClose} disabled={busy}>取消</button>
          <button className="btn btn-accent" disabled={busy || !container} onClick={doIt}>{busy ? '执行中…' : (action === 'connect' ? '连接' : '断开')}</button>
        </div>
      </div>
    </div>
  )
}

// 导出镜像 (docker save → 下载 tar)
function ImageSaveModal({ host, def, onClose }: { host: string; def: string; onClose: () => void }) {
  const [image, setImage] = useState(def)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const doSave = () => {
    if (!image.trim()) return
    setBusy(true); setErr('')
    dockerTool(host, { scope: 'image', action: 'save', image: image.trim() })
      .then((d: any) => {
        if (d.ok) {
          b64Download(d.archive, d.fileName)
          onClose()
        } else setErr(d.error || '失败')
      })
      .catch((e) => setErr(String(e))).finally(() => setBusy(false))
  }
  return (
    <div className="modal-overlay" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()} style={{ maxWidth: 520 }}>
        <h3>导出镜像 (docker save)</h3>
        <p className="dim" style={{ fontSize: '0.75rem', marginTop: '0.25rem' }}>经 base64 传输, 单次上限 100MB, 大镜像请直接在宿主机 docker save -o。</p>
        <div className="btn-row" style={{ marginTop: '0.75rem', alignItems: 'center' }}>
          <input className="input mono" style={{ flex: 1 }} value={image} onChange={(e) => setImage(e.target.value)} placeholder="镜像名, 如 nginx:1.27-alpine" disabled={busy} />
          <button className="btn btn-accent" disabled={busy || !image.trim()} onClick={doSave}>{busy ? '导出中…' : '导出下载'}</button>
        </div>
        {err && <div style={{ marginTop: '0.5rem', color: 'var(--danger)' }}>{err}</div>}
      </div>
    </div>
  )
}

// 导入镜像 (docker load: 上传或宿主机已有路径)
function ImageLoadModal({ host, onClose, onMsg }: { host: string; onClose: () => void; onMsg?: (m: string) => void }) {
  const [mode, setMode] = useState<'upload' | 'path'>('upload')
  const [file, setFile] = useState<File | null>(null)
  const [path, setPath] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const doLoad = () => {
    setBusy(true); setErr('')
    const finish = (b64: string, p: string) => {
      dockerTool(host, { scope: 'image', action: 'load', archiveB64: b64, path: p })
        .then((d: any) => { if (d.ok) { onMsg?.('✓ 镜像导入完成'); onClose() } else setErr(d.error || '失败') })
        .catch((e) => setErr(String(e))).finally(() => setBusy(false))
    }
    if (mode === 'path') {
      if (!path.trim()) { setErr('请输入宿主机镜像 tar 路径'); setBusy(false); return }
      finish('', path.trim())
    } else {
      if (!file) { setErr('请选择镜像 tar 文件'); setBusy(false); return }
      readFileB64(file, (b64) => finish(b64, ''), setErr)
    }
  }
  return (
    <div className="modal-overlay" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()} style={{ maxWidth: 540 }}>
        <h3>导入镜像 (docker load)</h3>
        <div className="btn-row" style={{ marginTop: '0.5rem' }}>
          <button className={`btn-glass-soft btn-glass-soft-sm ${mode === 'upload' ? 'btn-glass-soft-accent' : ''}`} onClick={() => setMode('upload')}>本地上传</button>
          <button className={`btn-glass-soft btn-glass-soft-sm ${mode === 'path' ? 'btn-glass-soft-accent' : ''}`} onClick={() => setMode('path')}>宿主机已有文件</button>
        </div>
        <div style={{ marginTop: '0.75rem', display: 'grid', gap: '0.5rem' }}>
          {mode === 'upload'
            ? <input type="file" onChange={(e) => setFile(e.target.files?.[0] || null)} disabled={busy} />
            : <input className="input mono" style={{ width: '100%' }} placeholder="宿主机镜像 tar 绝对路径, 如 /opt/images/app-v1.tar" value={path} onChange={(e) => setPath(e.target.value)} disabled={busy} />}
        </div>
        {err && <div style={{ marginTop: '0.5rem', color: 'var(--danger)' }}>{err}</div>}
        <div className="modal-actions">
          <button className="btn-glass-soft" onClick={onClose} disabled={busy}>取消</button>
          <button className="btn btn-accent" disabled={busy} onClick={doLoad}>{busy ? '导入中…' : '导入'}</button>
        </div>
      </div>
    </div>
  )
}
