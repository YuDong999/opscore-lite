// ── 容器管理插件: 一级目录 → Docker 管理 / Kubernetes 管理(二级) → 页内 tab(三级) ──
// Docker: 启停/重启/删除/日志/镜像/连接走向/重启策略修改 + 点击行查看 inspect 详情
// K8s:    crictl/ctr 只读巡检

import { useEffect, useMemo, useState } from 'react'
import { useLocation } from 'react-router-dom'
import Card from '../components/Card'
import { useHost } from '../components/HostContext'
import { getJSON, postJSON } from '../api/client'
import EChart from '../charts/EChart'

interface AppContainer {
  id?: string
  name: string
  image: string
  state: string
  status?: string
  runtime: string
  ports?: string[]
  createdAt?: string
  restartCount?: number
  restartPolicy?: string
  // 详情字段
  mounts?: { type: string; source: string; destination: string; readOnly: boolean }[]
  env?: string[]
  networks?: string[]
  memoryLimit?: number
  cpuLimit?: number
  startedAt?: string
  exitCode?: number
  pid?: number
  labels?: Record<string, string>
}

interface ListResp {
  runtime: string
  containers: AppContainer[] | null
  note?: string
}

interface ImageRow {
  repo: string
  tag: string
  id: string
  size: string
}

interface FlowsResp {
  nodes: { id: string; name: string; type: string }[]
  edges: { source: string; target: string; proto: string; count: number }[]
  note?: string
}

type Tab = 'containers' | 'images' | 'flows'

const writable = (rt: string) => rt === 'docker' || rt === 'podman'

export default function ContainersModule({ scope }: { scope: 'docker' | 'k8s' }) {
  const { selected } = useHost()
  const location = useLocation()
  const isK8s = scope === 'k8s' || location.pathname.endsWith('/k8s')
  const [tab, setTab] = useState<Tab>('containers')
  const [list, setList] = useState<ListResp | null>(null)
  const [images, setImages] = useState<ImageRow[] | null>(null)
  const [flows, setFlows] = useState<FlowsResp | null>(null)
  const [msg, setMsg] = useState('')
  const [busy, setBusy] = useState(false)
  const [confirmAct, setConfirmAct] = useState<{ name: string; action: string } | null>(null)
  const [logView, setLogView] = useState<{ name: string; logs: string; target: string } | null>(null)
  const [detail, setDetail] = useState<{ c: AppContainer; loading: boolean } | null>(null)

  // 切换二级页(Docker/K8s)时重置状态
  useEffect(() => {
    setList(null); setImages(null); setFlows(null); setTab('containers'); setDetail(null); setLogView(null)
  }, [isK8s])

  const hostQ = selected?.id ? `&host=${encodeURIComponent(selected.id)}` : ''
  // Docker 页: 首选运行时(docker/podman); K8s 页: 强制 crictl
  const rtParam = isK8s ? '&rt=crictl' : ''

  const loadList = () => {
    getJSON<ListResp>(`/api/plugins/containers/list?_=${Date.now()}${rtParam}${hostQ}`)
      .then(setList)
      .catch((e) => setMsg('✗ ' + String(e)))
  }

  useEffect(() => {
    loadList()
    if (tab === 'images') {
      getJSON<{ images: ImageRow[] }>(`/api/plugins/containers/images?_=${Date.now()}${hostQ}`)
        .then((d) => setImages(d.images || []))
        .catch(() => setImages([]))
    }
    if (tab === 'flows') {
      getJSON<FlowsResp>(`/api/plugins/containers/flows?_=${Date.now()}${hostQ}`)
        .then(setFlows)
        .catch(() => setFlows({ nodes: [], edges: [], note: '采集失败' }))
    }
  }, [selected?.id, tab, isK8s])

  const rt = list?.runtime || ''

  const runAction = (name: string, action: string, policy?: string) => {
    setConfirmAct(null)
    setBusy(true)
    postJSON('/api/plugins/containers/action', {
      host: selected?.id || '',
      name,
      runtime: rt,
      action,
      policy,
    })
      .then((d: any) => {
        if (d.ok) {
          setMsg(`✓ ${action}${policy ? `(${policy})` : ''} ${name} @ ${d.target}${d.verified ? ' · 回读验证通过' : ' · ⚠ 回读验证未通过'}`)
        } else {
          setMsg('✗ ' + (d.error || '操作失败'))
        }
      })
      .catch((e) => setMsg('✗ ' + String(e)))
      .finally(() => {
        setBusy(false)
        setTimeout(loadList, 300)
        setTimeout(loadList, 2500)
      })
  }

  const openLogs = (c: AppContainer) => {
    getJSON<{ ok: boolean; logs: string; target: string }>(
      `/api/plugins/containers/logs?${hostQ.replace('&', '')}&runtime=${rt}&name=${encodeURIComponent(c.name)}&tail=300`
    )
      .then((d) => setLogView({ name: c.name, logs: d.logs || '(空)', target: d.target }))
      .catch((e) => setMsg('✗ ' + String(e)))
  }

  const openDetail = (c: AppContainer) => {
    setDetail({ c, loading: true })
    getJSON<{ ok: boolean; container: AppContainer; error?: string }>(
      `/api/plugins/containers/detail?${hostQ.replace('&', '')}&runtime=${rt}&id=${encodeURIComponent(c.id || c.name)}`
    )
      .then((d) => setDetail({ c: d.container || c, loading: false }))
      .catch((e) => { setMsg('✗ ' + String(e)); setDetail(null) })
  }

  const flowsOption = useMemo(() => {
    if (!flows) return {}
    const colorOf = (t: string) =>
      t === 'container' ? '#3b82f6' : t === 'host' ? '#a855f7' : t === 'internal' ? '#22c55e' : '#ef4444'
    return {
      tooltip: {},
      series: [
        {
          type: 'graph',
          layout: 'force',
          roam: true,
          label: { show: true, fontSize: 11 },
          force: { repulsion: 260, edgeLength: 120, gravity: 0.1 },
          data: flows.nodes.map((n) => ({
            id: n.id,
            name: n.name,
            itemStyle: { color: colorOf(n.type) },
            symbolSize: n.type === 'container' ? 34 : 20,
          })),
          links: flows.edges.map((e) => ({
            source: e.source,
            target: e.target,
            lineStyle: { width: Math.min(1 + e.count / 4, 5), opacity: 0.6 },
            label: { show: true, formatter: `${e.proto} ×${e.count}`, fontSize: 9 },
          })),
        },
      ],
    }
  }, [flows])

  const canWrite = !isK8s && writable(rt)

  return (
    <div className="module">
      <div className="module-head" style={{ flexWrap: 'wrap', gap: '0.5rem 1rem' }}>
        <h2 style={{ marginRight: 0 }}>{isK8s ? 'Kubernetes 管理' : 'Docker 管理'}</h2>
        <span className="pill pill-sub">{list ? `运行时: ${rt || '未检测到'}` : '加载中…'}{selected && ` · ${selected.label}`}</span>
      </div>

      {/* 三级: 页内 tab */}
      {!isK8s && (
        <div className="btn-row" style={{ marginBottom: '1rem', gap: '0.375rem' }}>
          <button className={`btn btn-sm ${tab === 'containers' ? 'btn-accent' : ''}`} onClick={() => setTab('containers')}>容器 ({list?.containers?.length ?? '-'})</button>
          <button className={`btn btn-sm ${tab === 'images' ? 'btn-accent' : ''}`} onClick={() => setTab('images')}>镜像</button>
          <button className={`btn btn-sm ${tab === 'flows' ? 'btn-accent' : ''}`} onClick={() => setTab('flows')}>连接走向</button>
        </div>
      )}

      {msg && <div className={`banner ${msg.startsWith('✓') ? 'banner-ok' : 'banner-err'}`}>{msg}</div>}
      {list?.note && <div className="banner banner-warn">{list.note}</div>}

      {(isK8s || tab === 'containers') && (
        <Card title={isK8s ? 'Pod 容器巡检 (只读)' : '容器列表'} subtitle={isK8s ? '点击行查看详情 · 写操作已禁用(K8s 托管)' : '点击行查看详情 · 操作带回读验证'}>
          <div className="table-wrap">
            <table className="data-table">
              <thead>
                <tr>
                  <th style={{ width: '16%' }}>名称</th>
                  <th style={{ width: '26%' }}>镜像</th>
                  <th style={{ width: '9%' }}>状态</th>
                  <th style={{ width: '21%' }}>端口</th>
                  {!isK8s && <th style={{ width: '28%' }}>操作</th>}
                </tr>
              </thead>
              <tbody>
                {(list?.containers || []).length === 0 && (
                  <tr><td colSpan={isK8s ? 4 : 5} className="dim">{list ? '（未检测到容器）' : '加载中…'}</td></tr>
                )}
                {(list?.containers || []).map((c) => (
                  <tr key={c.name} style={{ cursor: 'pointer' }} onClick={() => openDetail(c)} title="点击查看详情">
                    <td className="mono">{c.name}</td>
                    <td className="mono dim" style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{c.image}</td>
                    <td>
                      <span className={`badge ${c.state === 'running' || c.state === 'CONTAINER_RUNNING' ? 'badge-ok' : c.state === 'exited' || c.state === 'CONTAINER_EXITED' ? 'badge-off' : 'badge-warn'}`}>
                        {c.state}
                      </span>
                    </td>
                    <td className="mono dim" style={{ fontSize: '0.6875rem', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                      {(c.ports || []).join(', ') || '—'}
                    </td>
                    {!isK8s && (
                      <td onClick={(e) => e.stopPropagation()}>
                        <div className="btn-row" style={{ flexWrap: 'nowrap', gap: '0.25rem' }}>
                          <button className="btn btn-sm" disabled={busy || !canWrite || c.state !== 'exited'}
                            onClick={() => runAction(c.name, 'start')}>启动</button>
                          <button className="btn btn-sm btn-danger" disabled={busy || !canWrite || c.state !== 'running'}
                            onClick={() => runAction(c.name, 'stop')}>停止</button>
                          <button className="btn btn-sm btn-accent" disabled={busy || !canWrite}
                            onClick={() => runAction(c.name, 'restart')}>重启</button>
                          <button className="btn btn-sm btn-danger" disabled={busy || !canWrite}
                            onClick={() => setConfirmAct({ name: c.name, action: 'remove' })}>删除</button>
                          <button className="btn btn-sm btn-ghost" disabled={!canWrite} onClick={() => openLogs(c)}>日志</button>
                        </div>
                      </td>
                    )}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </Card>
      )}

      {!isK8s && tab === 'images' && (
        <Card title="镜像列表" subtitle="只读">
          <div className="table-wrap">
            <table className="data-table">
              <thead><tr><th>仓库</th><th>标签</th><th>ID</th><th>大小</th></tr></thead>
              <tbody>
                {(images || []).length === 0 && <tr><td colSpan={4} className="dim">（无镜像）</td></tr>}
                {(images || []).map((im, i) => (
                  <tr key={i}>
                    <td className="mono">{im.repo}</td>
                    <td className="mono">{im.tag}</td>
                    <td className="mono dim">{im.id}</td>
                    <td>{im.size}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </Card>
      )}

      {!isK8s && tab === 'flows' && (
        <Card title="连接走向" subtitle="基于 conntrack 会话表(无侵入采集 · 未来可升级 eBPF)">
          {flows?.note && <div className="banner banner-warn">{flows.note}</div>}
          {(flows?.nodes?.length ?? 0) > 0 ? (
            <EChart option={flowsOption} height={480} />
          ) : (
            !flows?.note && <div className="loading">当前主机暂无连接数据(需 Linux 主机 + conntrack 可读)</div>
          )}
          <p className="dim" style={{ fontSize: '0.75rem', marginTop: '0.75rem' }}>
            节点颜色: <span style={{ color: '#3b82f6' }}>■ 容器</span> ·{' '}
            <span style={{ color: '#22c55e' }}>■ 内网</span> ·{' '}
            <span style={{ color: '#ef4444' }}>■ 外网</span> ·{' '}
            <span style={{ color: '#a855f7' }}>■ 本机回环</span>
          </p>
        </Card>
      )}

      {/* 操作确认 */}
      {confirmAct && (
        <div className="modal-overlay" onClick={() => setConfirmAct(null)}>
          <div className="modal" onClick={(e) => e.stopPropagation()} style={{ maxWidth: 420 }}>
            <h3>确认执行: {confirmAct.action === 'remove' ? '删除容器(高危)' : confirmAct.action}</h3>
            <p>
              目标主机 <b>{selected?.label || '本机'}</b>, 容器 <b className="mono">{confirmAct.name}</b>
              {confirmAct.action === 'remove' && ' — 运行中的容器将被强制删除(-f), 不可恢复!'}
            </p>
            <div className="modal-actions">
              <button className="btn" onClick={() => setConfirmAct(null)}>取消</button>
              <button className={`btn ${confirmAct.action === 'remove' || confirmAct.action === 'stop' ? 'btn-danger' : 'btn-accent'}`}
                disabled={busy}
                onClick={() => runAction(confirmAct.name, confirmAct.action)}>
                确认{confirmAct.action}
              </button>
            </div>
          </div>
        </div>
      )}

      {/* 日志弹层 */}
      {logView && (
        <div className="modal-overlay" onClick={() => setLogView(null)}>
          <div className="modal log-modal" onClick={(e) => e.stopPropagation()} style={{ maxWidth: 860 }}>
            <div className="modal-head">
              <div className="modal-title">日志: {logView.name} <span className="pill pill-sub">@ {logView.target} · 最近300行</span></div>
              <button className="btn btn-sm" onClick={() => setLogView(null)}>关闭</button>
            </div>
            <pre className="code-block" style={{ margin: 0, maxHeight: 480, overflow: 'auto', whiteSpace: 'pre-wrap', fontSize: '0.6875rem' }}>
              {logView.logs}
            </pre>
          </div>
        </div>
      )}

      {/* 详情弹层: 点击行展开 */}
      {detail && (
        <div className="modal-overlay" onClick={() => setDetail(null)}>
          <div className="modal log-modal" onClick={(e) => e.stopPropagation()} style={{ maxWidth: 780 }}>
            <div className="modal-head">
              <div className="modal-title">
                容器详情: {detail.c.name || detail.c.id}
                <span className="pill pill-sub">{detail.c.runtime}{detail.c.state ? ` · ${detail.c.state}` : ''}</span>
              </div>
              <button className="btn btn-sm" onClick={() => setDetail(null)}>关闭</button>
            </div>
            <div style={{ padding: '1rem 1.25rem', overflowY: 'auto', maxHeight: '65vh' }}>
              {detail.loading ? (
                <div className="loading">采集 inspect 信息中…</div>
              ) : (
                <>
                  <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(210px, 1fr))', gap: '0.75rem', marginBottom: '1rem' }}>
                    <InfoCell k="镜像" v={detail.c.image} />
                    <InfoCell k="创建时间" v={detail.c.createdAt || '—'} />
                    <InfoCell k="启动时间" v={detail.c.startedAt || '—'} />
                    <InfoCell k="重启次数" v={String(detail.c.restartCount ?? 0)} />
                    <InfoCell k="PID" v={detail.c.pid ? String(detail.c.pid) : '—'} />
                    <InfoCell k="退出码" v={detail.c.exitCode ? String(detail.c.exitCode) : '—'} />
                    <InfoCell k="网络" v={(detail.c.networks || []).join(', ') || '—'} />
                    <InfoCell k="内存限额" v={fmtBytes(detail.c.memoryLimit)} />
                    <InfoCell k="CPU 限额" v={detail.c.cpuLimit ? `${(detail.c.cpuLimit / 1e9).toFixed(2)} 核` : '不限'} />
                  </div>

                  {detail.c.mounts && detail.c.mounts.length > 0 && (
                    <Card title={`挂载 (${detail.c.mounts.length})`} >
                      <div className="table-wrap">
                        <table className="data-table">
                          <thead><tr><th>类型</th><th>宿主机路径</th><th>容器路径</th><th>读写</th></tr></thead>
                          <tbody>
                            {detail.c.mounts.map((m, i) => (
                              <tr key={i}>
                                <td>{m.type}</td>
                                <td className="mono" style={{ wordBreak: 'break-all' }}>{m.source}</td>
                                <td className="mono" style={{ wordBreak: 'break-all' }}>{m.destination}</td>
                                <td>{m.readOnly ? '只读' : '读写'}</td>
                              </tr>
                            ))}
                          </tbody>
                        </table>
                      </div>
                    </Card>
                  )}

                  {canWrite && (
                    <Card title="修改重启策略">
                      <PolicyEditor current={detail.c.restartPolicy || 'no'} busy={busy}
                        onSave={(p) => runAction(detail.c.name, 'update-policy', p)} />
                    </Card>
                  )}

                  {detail.c.env && detail.c.env.length > 0 && (
                    <details style={{ marginTop: '0.75rem' }}>
                      <summary className="dim" style={{ cursor: 'pointer' }}>环境变量 ({detail.c.env.length})</summary>
                      <pre className="code-block" style={{ maxHeight: 200, overflow: 'auto', fontSize: '0.6875rem' }}>{detail.c.env.join('\n')}</pre>
                    </details>
                  )}
                  {detail.c.labels && Object.keys(detail.c.labels).length > 0 && (
                    <details style={{ marginTop: '0.5rem' }}>
                      <summary className="dim" style={{ cursor: 'pointer' }}>标签 ({Object.keys(detail.c.labels).length})</summary>
                      <pre className="code-block" style={{ maxHeight: 200, overflow: 'auto', fontSize: '0.6875rem' }}>
                        {Object.entries(detail.c.labels).map(([k, v]) => `${k}=${v}`).join('\n')}
                      </pre>
                    </details>
                  )}
                </>
              )}
            </div>
          </div>
        </div>
      )}
    </div>
  )
}

function InfoCell({ k, v }: { k: string; v: string }) {
  return (
    <div style={{ background: 'var(--bg-card, rgba(255,255,255,0.03))', border: '1px solid var(--border)', borderRadius: '0.5rem', padding: '0.5rem 0.75rem', minWidth: 0 }}>
      <div className="dim" style={{ fontSize: '0.6875rem', marginBottom: '0.25rem' }}>{k}</div>
      <div className="mono" style={{ fontSize: '0.75rem', wordBreak: 'break-all' }}>{v}</div>
    </div>
  )
}

function PolicyEditor({ current, busy, onSave }: { current: string; busy: boolean; onSave: (p: string) => void }) {
  const [policy, setPolicy] = useState(current)
  const changed = policy !== current
  return (
    <div className="btn-row" style={{ alignItems: 'center' }}>
      <select className="input" value={policy} onChange={(e) => setPolicy(e.target.value)} style={{ width: 180 }}>
        <option value="no">no (不自动重启)</option>
        <option value="on-failure">on-failure (异常退出重启)</option>
        <option value="always">always (总是重启)</option>
        <option value="unless-stopped">unless-stopped (除非手动停止)</option>
      </select>
      <button className="btn btn-sm btn-accent" disabled={busy || !changed} onClick={() => onSave(policy)}>应用</button>
      <span className="dim" style={{ fontSize: '0.75rem' }}>当前: {current}</span>
    </div>
  )
}

function fmtBytes(b?: number): string {
  if (!b) return '不限'
  const units = ['B', 'KB', 'MB', 'GB']
  let v = b, i = 0
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++ }
  return `${v.toFixed(v >= 100 || i === 0 ? 0 : 1)} ${units[i]}`
}
