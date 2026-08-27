// ── Kubernetes 多集群管理 (client-go 直连, 只读) ──
// 布局对齐 kubevision: 左侧固定侧栏(集群树 + 可折叠资源分组) + 右侧主内容区
// 分类: 概览 / 工作负载 / 网络 / 配置 / 存储 / 集群 / 策略

import { useEffect, useMemo, useRef, useState } from 'react'
import { getJSON, postJSON } from '../api/client'
import CreateResource from './CreateResource'
import Card from '../components/Card'
import EChart from '../charts/EChart'
import { useTheme } from '../theme'
import jsYaml from 'js-yaml'

interface K8sCluster {
  id: string
  name: string
  apiServer: string
  version: string
  status: string
}

type K8sRes =
  | 'overview'
  | 'pods' | 'deployments' | 'statefulsets' | 'daemonsets' | 'jobs' | 'cronjobs'
  | 'services' | 'ingresses'
  | 'configmaps' | 'secrets'
  | 'persistentvolumes' | 'persistentvolumeclaims' | 'storageclasses'
  | 'nodes' | 'namespaces' | 'events'
  | 'networkpolicies' | 'resourcequotas'

// 资源分组(kubevision 信息架构), 折叠状态持久化在 localStorage
const RES_GROUPS: { key: string; label: string; defaultOpen: boolean; items: { res: K8sRes; title: string }[] }[] = [
  { key: 'workloads', label: '工作负载', defaultOpen: true, items: [
    { res: 'pods', title: 'Pods' },
    { res: 'deployments', title: 'Deployments' },
    { res: 'statefulsets', title: 'StatefulSets' },
    { res: 'daemonsets', title: 'DaemonSets' },
    { res: 'jobs', title: 'Jobs' },
    { res: 'cronjobs', title: 'CronJobs' },
  ]},
  { key: 'network', label: '网络', defaultOpen: true, items: [
    { res: 'services', title: 'Services' },
    { res: 'ingresses', title: 'Ingresses' },
  ]},
  { key: 'config', label: '配置', defaultOpen: false, items: [
    { res: 'configmaps', title: 'ConfigMaps' },
    { res: 'secrets', title: 'Secrets' },
  ]},
  { key: 'storage', label: '存储', defaultOpen: false, items: [
    { res: 'persistentvolumes', title: 'PersistentVolumes' },
    { res: 'persistentvolumeclaims', title: 'PersistentVolumeClaims' },
    { res: 'storageclasses', title: 'StorageClasses' },
  ]},
  { key: 'cluster', label: '集群', defaultOpen: true, items: [
    { res: 'nodes', title: 'Nodes' },
    { res: 'namespaces', title: 'Namespaces' },
    { res: 'events', title: 'Events' },
  ]},
  { key: 'policy', label: '策略', defaultOpen: false, items: [
    { res: 'networkpolicies', title: 'NetworkPolicies' },
    { res: 'resourcequotas', title: 'ResourceQuotas' },
  ]},
]

const NSLESS = new Set<K8sRes>(['nodes', 'namespaces', 'events', 'persistentvolumes', 'storageclasses'])

type Col = [string, string, number, ('mono' | 'dim' | 'status')?]
const COLS: Partial<Record<K8sRes, Col[]>> = {
  pods: [
    ['name', '名称', 24, 'mono'], ['namespace', '命名空间', 13, 'dim'],
    ['status', '状态', 11, 'status'], ['restarts', '重启', 8],
    ['node', '节点', 15, 'dim'], ['ip', 'IP', 13, 'mono'], ['age', '年龄', 9, 'dim'],
  ],
  deployments: [
    ['name', '名称', 26, 'mono'], ['namespace', '命名空间', 18, 'dim'],
    ['ready', '就绪', 14, 'mono'], ['updated', '已更新', 12], ['available', '可用', 12], ['age', '年龄', 14, 'dim'],
  ],
  statefulsets: [
    ['name', '名称', 30, 'mono'], ['namespace', '命名空间', 22, 'dim'],
    ['ready', '就绪', 16, 'mono'], ['age', '年龄', 20, 'dim'],
  ],
  daemonsets: [
    ['name', '名称', 28, 'mono'], ['namespace', '命名空间', 20, 'dim'],
    ['ready', '就绪', 14, 'mono'], ['available', '可用', 12], ['age', '年龄', 14, 'dim'],
  ],
  jobs: [
    ['name', '名称', 28, 'mono'], ['namespace', '命名空间', 18, 'dim'],
    ['status', '状态', 14, 'status'], ['succeeded', '完成', 14, 'mono'], ['age', '年龄', 14, 'dim'],
  ],
  cronjobs: [
    ['name', '名称', 22, 'mono'], ['namespace', '命名空间', 14, 'dim'],
    ['schedule', '计划', 20, 'mono'], ['active', '活跃', 10],
    ['lastSchedule', '上次调度', 14, 'dim'], ['age', '年龄', 12, 'dim'],
  ],
  services: [
    ['name', '名称', 21, 'mono'], ['namespace', '命名空间', 14, 'dim'],
    ['type', '类型', 12], ['clusterIP', 'ClusterIP', 17, 'mono'],
    ['ports', '端口', 20, 'mono'], ['age', '年龄', 11, 'dim'],
  ],
  ingresses: [
    ['name', '名称', 24, 'mono'], ['namespace', '命名空间', 18, 'dim'],
    ['class', 'Class', 14, 'dim'], ['host', 'Host', 26, 'mono'], ['age', '年龄', 12, 'dim'],
  ],
  configmaps: [
    ['name', '名称', 34, 'mono'], ['namespace', '命名空间', 24, 'dim'],
    ['dataCount', '数据项', 20], ['age', '年龄', 22, 'dim'],
  ],
  secrets: [
    ['name', '名称', 30, 'mono'], ['namespace', '命名空间', 18, 'dim'],
    ['type', '类型', 24, 'dim'], ['dataCount', '数据项', 14], ['age', '年龄', 14, 'dim'],
  ],
  persistentvolumes: [
    ['name', '名称', 20, 'mono'], ['capacity', '容量', 10],
    ['accessModes', '访问模式', 18, 'dim'], ['reclaim', '回收策略', 12, 'dim'],
    ['status', '状态', 10, 'status'], ['claim', '绑定 PVC', 20, 'mono'], ['age', '年龄', 10, 'dim'],
  ],
  persistentvolumeclaims: [
    ['name', '名称', 24, 'mono'], ['namespace', '命名空间', 16, 'dim'],
    ['status', '状态', 12, 'status'], ['volume', 'PV', 20, 'mono'],
    ['capacity', '容量', 12], ['age', '年龄', 12, 'dim'],
  ],
  storageclasses: [
    ['name', '名称', 24, 'mono'], ['provisioner', 'Provider', 30, 'dim'],
    ['reclaim', '回收策略', 12, 'dim'], ['bindingMode', '绑定模式', 16, 'dim'],
    ['default', '默认', 8], ['age', '年龄', 10, 'dim'],
  ],
  nodes: [
    ['name', '名称', 16, 'mono'], ['status', '状态', 11, 'status'],
    ['roles', '角色', 16, 'dim'], ['version', '版本', 11, 'mono'],
    ['internalIP', 'IP', 15, 'mono'], ['osImage', '系统', 23, 'dim'], ['age', '年龄', 8, 'dim'],
  ],
  namespaces: [
    ['name', '名称', 40, 'mono'], ['status', '状态', 20, 'status'], ['age', '年龄', 40, 'dim'],
  ],
  events: [
    ['lastSeen', '时间', 9, 'dim'], ['type', '级别', 9, 'status'],
    ['reason', '原因', 13, 'mono'], ['object', '对象', 21, 'mono'],
    ['message', '消息', 48, 'dim'],
  ],
  networkpolicies: [
    ['name', '名称', 45, 'mono'], ['namespace', '命名空间', 30, 'dim'], ['age', '年龄', 25, 'dim'],
  ],
  resourcequotas: [
    ['name', '名称', 24, 'mono'], ['namespace', '命名空间', 18, 'dim'],
    ['cpu', 'CPU(used/hard)', 25], ['memory', '内存(used/hard)', 25], ['age', '年龄', 12, 'dim'],
  ],
}

const FOLD_KEY = 'k8s-side-fold'

export default function K8sModule({ onMsg }: { onMsg?: (m: string) => void }) {
  const [clusters, setClusters] = useState<K8sCluster[] | null>(null)
  const [clusterID, setClusterID] = useState('')
  const [res, setRes] = useState<string>('overview')
  const [sortKey, setSortKey] = useState('')
  const [sortDir, setSortDir] = useState<'desc' | 'asc'>('desc')
  const [ns, setNs] = useState('all')
  const [namespaces, setNamespaces] = useState<string[]>([])
  const [rows, setRows] = useState<any[]>([])
  const [note, setNote] = useState('')
  const [loading, setLoading] = useState(false)
  const [showReg, setShowReg] = useState(false)
  const [createKind, setCreateKind] = useState('Deployment')
  const [folded, setFolded] = useState<Record<string, boolean>>(() => {
    try { return JSON.parse(localStorage.getItem(FOLD_KEY) || '{}') } catch { return {} }
  })
  // 资源弹层: 双击行或行内操作打开
  const [modal, setModal] = useState<{ kind: 'pod' | 'workload' | 'yaml' | 'node'; res: K8sRes; ns: string; name: string } | null>(null)

  const openModal = (kind: 'pod' | 'workload' | 'yaml' | 'node', r: any) => {
    if (!clusterID || !r?.name) return
    // 命名空间作用域解析: 集群级资源忽略 ns; 列表为"全部命名空间"时用行内自带的 namespace
    const effNs = NSLESS.has(res as K8sRes)
      ? ''
      : ns === 'all' ? String(r.namespace || '') : ns
    setModal({ kind, res: res as K8sRes, ns: effNs, name: String(r.name) })
  }
  // 表头排序: 点新列=降序, 再点=升序, 三点=取消(借鉴 kubevision 交互)
  const toggleSort = (k: string) => {
    if (sortKey !== k) { setSortKey(k); setSortDir('desc') }
    else if (sortDir === 'desc') setSortDir('asc')
    else { setSortKey(''); setSortDir('desc') }
  }
  const sortedRows = useMemo(() => {
    if (!sortKey) return rows
    if (!(COLS[res as K8sRes] || []).some((c) => c[0] === sortKey)) return rows
    const dir = sortDir === 'desc' ? -1 : 1
    const ageVal = (s: any): number => {
      const parts = String(s ?? '').match(/(\d+)([smhd])/g)
      if (!parts) return -1
      const unit: any = { s: 1, m: 60, h: 3600, d: 86400 }
      return parts.reduce((acc: number, p: string) => acc + parseInt(p) * (unit[p[p.length - 1]] || 1), 0)
    }
    return [...rows].sort((a, b) => {
      const av = sortKey === 'age' ? ageVal(a.age) : a[sortKey]
      const bv = sortKey === 'age' ? ageVal(b.age) : b[sortKey]
      if (typeof av === 'number' && typeof bv === 'number') return (av - bv) * dir
      return String(av ?? '').localeCompare(String(bv ?? '')) * dir
    })
  }, [rows, sortKey, sortDir, res])

  const dblRow = (r: any) => {
    if (res === 'pods') openModal('pod', r)
    else if (res === 'deployments' || res === 'statefulsets') openModal('workload', r)
    else if (res === 'nodes') openModal('node', r)
    else if (res !== 'overview') openModal('yaml', r)
  }

  const act = (body: Record<string, any>, confirmMsg?: string) => {
    if (confirmMsg && !confirm(confirmMsg)) return
    postJSON('/api/plugins/containers/k8s/resource/action', { cluster: clusterID, ...body })
      .then((d: any) => {
        onMsg?.(d.ok ? `✓ ${body.action} ${body.name} 完成` : '✗ ' + (d.error || '失败'))
        if (d.ok) { setModal(null); setTimeout(loadRows, 600) }
      })
      .catch((e) => onMsg?.('✗ ' + String(e)))
  }

  const toggleFold = (key: string) => {
    setFolded((f) => {
      const next = { ...f, [key]: !f[key] }
      localStorage.setItem(FOLD_KEY, JSON.stringify(next))
      return next
    })
  }

  const loadClusters = () => {
    getJSON<{ clusters: K8sCluster[] }>('/api/plugins/containers/k8s/clusters')
      .then((d) => {
        const cs = d.clusters || []
        setClusters(cs)
        if (!cs.find((c) => c.id === clusterID)) setClusterID(cs[0]?.id || '')
      })
      .catch((e) => onMsg?.('✗ 加载集群失败: ' + String(e)))
  }
  useEffect(loadClusters, [])

  // 概览页 TOP 榜点击 → 打开 Pod 详情(跨组件事件)
  useEffect(() => {
    const h = (e: Event) => {
      const d = (e as CustomEvent).detail || {}
      if (!clusterID || !d.name) return
      setModal({ kind: 'pod', res: 'pods', ns: d.ns === 'all' ? '' : d.ns || '', name: String(d.name) })
    }
    window.addEventListener('k8s-open-pod', h)
    return () => window.removeEventListener('k8s-open-pod', h)
  }, [clusterID])

  // 命名空间列表: 与当前资源页无关, 进入即拉; 失败自动重试(偶发请求失败会导致永远为空)
  const loadNsList = (retry = 0) => {
    if (!clusterID) return
    getJSON<{ rows: any[] }>(`/api/plugins/containers/k8s/resources?cluster=${clusterID}&res=namespaces&_=${Date.now()}`)
      .then((d) => setNamespaces((d.rows || []).map((r) => r.name)))
      .catch(() => { if (retry < 3) setTimeout(() => loadNsList(retry + 1), 1200) })
  }
  useEffect(loadNsList, [clusterID])

  const loadRows = () => {
    if (!clusterID || res === 'overview') return
    setLoading(true)
    const nsQ = NSLESS.has(res as K8sRes) ? '' : `&ns=${encodeURIComponent(ns)}`
    getJSON<{ rows: any[]; note?: string }>(
      `/api/plugins/containers/k8s/resources?cluster=${clusterID}&res=${res}${nsQ}&_=${Date.now()}`
    )
      .then((d) => { setRows(d.rows || []); setNote(d.note || '') })
      .catch((e) => { setRows([]); setNote(String(e)) })
      .finally(() => setLoading(false))
  }
  useEffect(loadRows, [clusterID, res, ns])

  const cluster = clusters?.find((c) => c.id === clusterID)

  return (
    <div className="k8s-shell">
      {/* ── 内嵌侧栏(固定高度独立滚动, 不随右侧内容移动) ── */}
      <aside className="k8s-side">
        <div className="k8s-side-label">集群</div>
        {(clusters || []).map((c) => (
          <div key={c.id} className={`k8s-side-item ${c.id === clusterID ? 'active' : ''}`}
            onClick={() => setClusterID(c.id)} title={c.apiServer}>
            <span className={`k8s-dot ${c.status === 'ready' ? 'k8s-dot-ok' : 'k8s-dot-bad'}`} />
            <span style={{ flex: 1, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{c.name}</span>
            <span style={{ fontSize: '0.625rem', opacity: 0.7, marginRight: 4 }}>{c.status === 'ready' ? c.version.replace(/^v/, '') : '离线'}</span>
            <button className="k8s-del-cluster"
              title="移除集群"
              onClick={(e) => {
                e.stopPropagation()
                if (!confirm(`移除集群「${c.name}」? (仅删除本地注册, 不影响集群本身)`)) return
                postJSON('/api/plugins/containers/k8s/cluster/action', { id: c.id, action: 'delete' })
                  .then((d: any) => {
                    if (d.ok) { loadClusters(); if (clusterID === c.id) setClusterID('') }
                    else onMsg?.('✗ ' + (d.error || '删除失败'))
                  })
                  .catch((err) => onMsg?.('✗ ' + String(err)))
              }}>✕</button>
          </div>
        ))}
        <button className="btn btn-sm k8s-add-cluster" onClick={() => setShowReg(true)}>+ 注册集群</button>
        <div className="k8s-side-divider" />

        <nav className="k8s-side-nav">
          <div className={`k8s-side-item ${res === 'create' ? 'active' : ''}`} onClick={() => setRes('create' as any)}>
            <span>➕ 创建资源</span>
          </div>
          {/* 概览 */}
          <div className={`k8s-side-item ${res === 'overview' ? 'active' : ''}`} onClick={() => setRes('overview')}>
            <span>概览</span>
          </div>
          {RES_GROUPS.map((g) => (
            <div key={g.key}>
              <div className="k8s-side-group" onClick={() => toggleFold(g.key)}>
                <span>{g.label}</span>
                <span className={`k8s-fold-arrow ${isFolded(folded, g.key, g.defaultOpen) ? 'folded' : ''}`}>▾</span>
              </div>
              {!isFolded(folded, g.key, g.defaultOpen) && g.items.map((it) => (
                <div key={it.res} className={`k8s-side-item ${it.res === res ? 'active' : ''}`}
                  onClick={() => setRes(it.res)}>
                  <span>{it.title}</span>
                </div>
              ))}
            </div>
          ))}
        </nav>
      </aside>

      {/* ── 主内容区 ── */}
      <section className="k8s-main" style={{ minWidth: 0, flex: 1 }}>
        {!cluster ? (
          <div className="card" style={{ padding: '3rem', textAlign: 'center' }}>
            <p className="dim">尚未注册集群或未选择</p>
            <button className="btn-glass is-accent" onClick={() => setShowReg(true)}>+ 注册第一个集群</button>
          </div>
        ) : res === 'create' ? (
          <CreateResource
            cluster={clusterID}
            namespaces={namespaces}
            initialKind={createKind}
            onMsg={onMsg!}
            onCreated={(r) => { setRes(r as any); setTimeout(loadRows, 700) }}
          />
        ) : res === 'overview' ? (
          <K8sOverview clusterID={clusterID} clusterName={cluster.name} />
        ) : (
          <div className="card">
            <div className="card-head" style={{ flexWrap: 'wrap', gap: '0.5rem' }}>
              <span style={{ fontWeight: 700 }}>{titleOf(res)}</span>
              <span className="dim" style={{ fontSize: '0.625rem' }}>双击行打开详情/操作</span>
              {!NSLESS.has(res as K8sRes) && (
                <select className="input sel" value={ns} onChange={(e) => setNs(e.target.value)} style={{ width: 200 }}>
                  <option value="all">全部命名空间</option>
                  {namespaces.map((n) => <option key={n} value={n}>{n}</option>)}
                </select>
              )}
              <span className="pill pill-sub">{loading ? '加载中…' : `${rows.length} 条`}</span>
              <button className="btn btn-sm" style={{ marginLeft: 'auto' }} onClick={loadRows}>刷新</button>
              {CREATE_KIND_OF[res] && (
                <button className="btn-glass is-accent btn-sm" onClick={() => { setCreateKind(CREATE_KIND_OF[res]); setRes('create' as any) }}>+ 创建</button>
              )}
            </div>
            {note && <div className="banner banner-warn">{note}</div>}
            <div className="table-wrap">
              <table className="data-table">
                <thead>
                  <tr>{(COLS[res as K8sRes] || []).map(([k, t, w]) => (
                    <th key={k} style={{ width: `${w}%`, cursor: 'pointer', userSelect: 'none' }} onClick={() => toggleSort(k)} title="点击排序">
                      {t}
                      <span style={{ opacity: sortKey === k ? 1 : 0.3, marginLeft: 3, fontSize: '0.5rem' }}>
                        {sortKey === k ? (sortDir === 'desc' ? '▼' : '▲') : '↕'}
                      </span>
                    </th>
                  ))}<th style={{ width: 130, minWidth: 110, textAlign: 'right' }}>操作</th></tr>
                </thead>
                <tbody>
                  {rows.length === 0 && (
                    <tr><td colSpan={(COLS[res as K8sRes] || []).length + 1} className="dim">{loading ? '加载中…' : '（无数据）'}</td></tr>
                  )}
                  {sortedRows.map((r, i) => (
                    <tr key={i} style={{ cursor: 'pointer' }} onDoubleClick={() => dblRow(r)} title="双击查看详情/操作">
                      {(COLS[res as K8sRes] || []).map(([k, , , typ]) => (
                        <td key={k} className={`${typ === 'dim' ? 'dim' : typ === 'mono' ? 'mono' : ''}`}
                          style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                          {typ === 'status' ? (
                            <span className={`badge ${badgeOf(String(r[k]))}`}>{String(r[k])}</span>
                          ) : String(r[k] ?? '—')}
                        </td>
                      ))}
                      <td style={{ textAlign: 'right', whiteSpace: 'nowrap' }}>
                        <div style={{ display: 'inline-flex', gap: 4, justifyContent: 'flex-end' }}>
                          {res === 'pods' && (
                            <button className="btn btn-sm" title="优雅删除(30s, SIGTERM), 卡住时可在详情里强制删除"
                              onClick={(e) => { e.stopPropagation(); act({ res: 'pods', ns: r.namespace || ns, name: r.name, action: 'delete' }, `优雅删除 Pod ${r.name}? (30s 优雅期)`) }}>删除</button>
                          )}
                          {(res === 'deployments' || res === 'statefulsets' || res === 'daemonsets') && (
                            <>
                              <button className="btn btn-sm" title="回滚到上一版本"
                                onClick={(e) => { e.stopPropagation(); act({ res, ns: r.namespace || ns, name: r.name, action: 'rollback', revision: 0 }, `回滚 ${r.name} 到上一版本?`) }}>回滚</button>
                              <button className="btn btn-sm btn-danger" title="优雅删除"
                                onClick={(e) => { e.stopPropagation(); act({ res, ns: r.namespace || ns, name: r.name, action: 'delete' }, `删除 ${res} ${r.name}?`) }}>删除</button>
                            </>
                          )}
                          {(res === 'jobs' || res === 'cronjobs' || res === 'services' || res === 'ingresses' || res === 'configmaps' || res === 'secrets' || res === 'persistentvolumeclaims') && (
                            <button className="btn btn-sm btn-danger"
                              onClick={(e) => { e.stopPropagation(); const prompt = res === 'jobs' ? `重跑 Job ${r.name}?` : res === 'cronjobs' ? `立即触发 ${r.name}?` : `删除 ${res} ${r.name}?`; const action = res === 'cronjobs' ? 'trigger' : res === 'jobs' ? 'rerun' : 'delete'; act({ res, ns: r.namespace || ns, name: r.name, action }, prompt) }}>
                              {res === 'jobs' ? '重跑' : res === 'cronjobs' ? '触发' : '删除'}
                            </button>
                          )}
                          {res === 'nodes' && (
                            <button className="btn btn-sm" onClick={(e) => { e.stopPropagation(); dblRow(r) }}>管理</button>
                          )}
                          {!['pods','deployments','statefulsets','daemonsets','jobs','cronjobs','services','ingresses','configmaps','secrets','persistentvolumeclaims','nodes'].includes(res) && (
                            <button className="btn btn-sm btn-danger" onClick={(e) => { e.stopPropagation(); act({ res, ns: r.namespace || ns, name: r.name, action: 'delete' }, `删除 ${r.name}?`) }}>删除</button>
                          )}
                        </div>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </div>
        )}
      </section>

      {/* 资源详情 / 工作负载管理 / YAML 弹层 */}
      {modal && cluster && (
        <ResourceModal
          info={{ ...modal, cluster: clusterID }}
          onClose={() => setModal(null)}
          act={act}
          onMsg={onMsg}
        />
      )}

      {showReg && (
        <RegisterModal
          onClose={() => setShowReg(false)}
          onDone={(ok, msg) => { setShowReg(false); onMsg?.(msg); if (ok) loadClusters() }}
        />
      )}

    </div>
  )
}

const CREATE_KIND_OF: Record<string, string> = {
  deployments: 'Deployment', statefulsets: 'StatefulSet', daemonsets: 'DaemonSet',
  services: 'Service', ingresses: 'Ingress', configmaps: 'ConfigMap',
  secrets: 'Secret', cronjobs: 'CronJob', persistentvolumeclaims: 'PVC',
}

function isFolded(folded: Record<string, boolean>, key: string, defaultOpen: boolean): boolean {
  const v = folded[key]
  if (v === undefined) return !defaultOpen
  return v
}

function titleOf(res: string): string {
  for (const g of RES_GROUPS) {
    const it = g.items.find((i) => i.res === res)
    if (it) return it.title
  }
  return res
}

function badgeOf(v: string): string {
  const s = v.toLowerCase()
  if (s.includes('fail') || s.includes('warn') || s.includes('notready') || s.includes('pending') || s.includes('released') || s.includes('terminating')) return 'badge-warn'
  return 'badge-ok'
}

// hex → rgba: 主题色的面积填充保持低透明度
function fade(hex: string, a: number): string {
  const m = hex.replace('#', '')
  const f = m.length === 3 ? m.split('').map((c) => c + c).join('') : m
  if (!/^[0-9a-fA-F]{6}$/.test(f)) return hex
  const n = parseInt(f, 16)
  return `rgba(${(n >> 16) & 255}, ${(n >> 8) & 255}, ${n & 255}, ${a})`
}

// ── 概览仪表盘: metrics.k8s.io 实时用量 + 服务端采样历史(跨重启) ──

const WIN_LABELS: Record<string, string> = { '15m': '15分钟', '1h': '1小时', '6h': '6小时' }

function K8sOverview({ clusterID, clusterName }: { clusterID: string; clusterName: string }) {
  useTheme() // 仅订阅主题状态: 切换主题时重渲染, 图表重新读取 CSS 变量配色
  const [ov, setOv] = useState<Record<string, any> | null>(null)
  const [nodeMetrics, setNodeMetrics] = useState<any[]>([])
  const [topPods, setTopPods] = useState<any[]>([])
  const [win, setWin] = useState<'15m' | '1h' | '6h'>('1h')
  const [hist, setHist] = useState<{ ts: number; cpu: number; memMiB: number }[]>([])
  const [err, setErr] = useState('')
  const openPodRef = useRef<(r: any) => void>(() => {})

  // 暴露双击跳转: TOP 表点击 → 打开 pod 详情弹层(复用列表页 modal 逻辑)
  useEffect(() => {
    openPodRef.current = (r: any) => {
      window.dispatchEvent(new CustomEvent('k8s-open-pod', { detail: { ns: r.namespace, name: r.name } }))
    }
  }, [])

  const loadAll = () => {
    getJSON<Record<string, any>>(`/api/plugins/containers/k8s/overview?cluster=${clusterID}&_=${Date.now()}`)
      .then(setOv).catch((e) => setErr(String(e)))
    getJSON<{ ok: boolean; nodes: any[] }>(`/api/plugins/containers/k8s/metrics/nodes?cluster=${clusterID}&_=${Date.now()}`)
      .then((d) => d.ok && setNodeMetrics(d.nodes || [])).catch(() => {})
    getJSON<{ ok: boolean; pods: any[] }>(`/api/plugins/containers/k8s/metrics/pods?cluster=${clusterID}&ns=all&top=10&_=${Date.now()}`)
      .then((d) => d.ok && setTopPods(d.pods || [])).catch(() => {})
    getJSON<{ ok: boolean; points: any[] }>(`/api/plugins/containers/k8s/metrics/history?cluster=${clusterID}&window=${win}&_=${Date.now()}`)
      .then((d) => d.ok && setHist(d.points || [])).catch(() => {})
  }
  useEffect(loadAll, [clusterID, win])
  // 30s 自动刷新实时区
  useEffect(() => {
    const t = setInterval(() => {
      getJSON<{ ok: boolean; nodes: any[] }>(`/api/plugins/containers/k8s/metrics/nodes?cluster=${clusterID}&_=${Date.now()}`)
        .then((d) => d.ok && setNodeMetrics(d.nodes || [])).catch(() => {})
      getJSON<{ ok: boolean; pods: any[] }>(`/api/plugins/containers/k8s/metrics/pods?cluster=${clusterID}&ns=all&top=10&_=${Date.now()}`)
        .then((d) => d.ok && setTopPods(d.pods || [])).catch(() => {})
    }, 30000)
    return () => clearInterval(t)
  }, [clusterID])

  if (err) return <div className="banner banner-err">{err}</div>
  if (!ov) return <div className="log-loading">加载概览中…</div>
  if (ov.note) return <div className="banner banner-warn">{String(ov.note)}</div>

  // 图表配色跟随当前主题(CSS 变量), 换主题即联动, 不再硬编码模板色
  const cv = (n: string, fb: string) => {
    const s = getComputedStyle(document.documentElement).getPropertyValue(n).trim()
    return s || fb
  }
  const txt = cv('--text', '#e5e7eb')
  const dim = cv('--text-dim', '#8a7ea8')
  const axis = cv('--border', 'rgba(127,127,127,0.18)')
  const accent = cv('--accent', '#a78bfa')
  const accent2 = cv('--accent-2', '#f472b6')
  const okC = cv('--ok', '#34d399')
  const warnC = cv('--warn', '#fbbf24')
  const dangerC = cv('--danger', '#fb7185')
  const surf = cv('--surface-solid', '#15131e')
  const num = (v: any) => (typeof v === 'number' ? v : 0)

  // ── 趋势图(CPU毫核 + 内存MiB 双序列) ──
  const trendOption = {
    grid: { left: 56, right: 60, top: 34, bottom: 26 },
    tooltip: { trigger: 'axis' },
    legend: { top: 2, left: 0, textStyle: { color: dim, fontSize: 10 }, itemWidth: 12 },
    xAxis: {
      type: 'category',
      data: hist.map((p) => new Date(p.ts * 1000).toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' })),
      axisLabel: { color: dim, fontSize: 10, interval: Math.max(0, Math.ceil(hist.length / 7) - 1), hideOverlap: true },
      axisLine: { lineStyle: { color: axis } },
    },
    yAxis: [
      { type: 'value',
        axisLabel: { color: dim, fontSize: 10, formatter: (v: number) => (v / 1000).toFixed(1) },
        splitLine: { lineStyle: { color: axis } } },
      { type: 'value',
        axisLabel: { color: dim, fontSize: 10, formatter: (v: number) => (v / 1024).toFixed(0) },
        splitLine: { show: false } },
    ],
    series: [
      { name: 'CPU', type: 'line', data: hist.map((p) => p.cpu), smooth: true, showSymbol: false,
        lineStyle: { width: 2, color: accent }, itemStyle: { color: accent },
        areaStyle: { color: fade(accent, 0.08) } },
      { name: '内存', type: 'line', yAxisIndex: 1, data: hist.map((p) => p.memMiB), smooth: true, showSymbol: false,
        lineStyle: { width: 2, color: okC }, itemStyle: { color: okC },
        areaStyle: { color: fade(okC, 0.07) } },
    ],
  }

  const gaugeOpt = (value: number, color: string) => ({
    series: [{
      type: 'gauge', startAngle: 210, endAngle: -30, min: 0, max: 100,
      progress: { show: true, width: 14, itemStyle: { color } },
      axisLine: { lineStyle: { width: 14, color: [[1, axis]] } },
      axisTick: { show: false }, splitLine: { show: false }, axisLabel: { show: false },
      pointer: { show: false },
      detail: { valueAnimation: true, fontSize: 20, color: txt, offsetCenter: [0, 0], formatter: (v: number) => v.toFixed(1) + '%' },
      data: [{ value }],
    }],
  })

  const podPie = {
    tooltip: { trigger: 'item', formatter: '{b}: {c} ({d}%)' },
    legend: { bottom: 0, textStyle: { color: dim, fontSize: 10 }, itemWidth: 10, itemHeight: 10 },
    series: [{
      type: 'pie', radius: ['42%', '68%'], center: ['50%', '42%'],
      itemStyle: { borderColor: surf, borderWidth: 2 },
      label: { show: false },
      data: [
        { name: 'Running', value: num(ov.podsRunning), itemStyle: { color: okC } },
        { name: 'Pending', value: num(ov.podsPending), itemStyle: { color: warnC } },
        { name: 'Succeeded', value: num(ov.podsSucceeded), itemStyle: { color: accent2 } },
        { name: 'Failed', value: num(ov.podsFailed), itemStyle: { color: dangerC } },
      ].filter((d) => d.value > 0),
    }],
  }

  return (
    <div className="k8s-overview">
      <div className="module-head">
        <h2 style={{ marginRight: 0 }}>{clusterName}</h2>
        {!!ov.version && <span className="pill pill-sub">{String(ov.version)}</span>}
        <span className="pill pill-sub">{num(ov.nodesReady)}/{num(ov.nodesTotal)} 节点在线</span>
        <span className="pill pill-sub">{num(ov.podsTotal)} pods</span>
        <button className="btn btn-sm" style={{ marginLeft: 'auto' }} onClick={loadAll}>刷新</button>
      </div>

      {/* 趋势 + 节点真实用量 */}
      <div className="grid grid-2">
        <Card title="集群资源趋势" subtitle="CPU / 内存 · 历史采样持久化">
          <div className="btn-row" style={{ marginBottom: '0.375rem' }}>
            {(Object.keys(WIN_LABELS)).map((w) => (
              <button key={w} className={`btn btn-sm ${win === w ? 'btn-accent' : ''}`} onClick={() => setWin(w as any)}>{WIN_LABELS[w]}</button>
            ))}
            <span className="dim" style={{ fontSize: '0.6875rem', marginLeft: 'auto' }}>
              {hist.length ? `共 ${hist.length} 个采样点` : '采样积累中…'}
            </span>
          </div>
          <EChart option={trendOption} height={230} />
        </Card>
        <Card title={`节点实时用量 (${nodeMetrics.length})`} subtitle="CPU / 内存 · 相对 allocatable">
          {nodeMetrics.length === 0 ? <div className="loading">读取中…</div> : nodeMetrics.map((n) => (
            <div key={n.name} className="k8s-node-meter">
              <div className="k8s-meter-head">
                <b className="mono">{n.name}</b>
                <span className="dim mono">{n.cpuMilli}m ({n.cpuPct.toFixed(0)}%) · {Math.round(n.memMiB)}MiB ({n.memPct.toFixed(0)}%)</span>
              </div>
              {[
                { v: n.cpuPct, mem: false },
                { v: n.memPct, mem: true },
              ].map((row, i) => (
                <div key={i} className="usage-bar">
                  <span className={`usage-fill ${row.v > 80 ? 'bg-danger' : row.mem ? 'bg-ok' : 'bg-accent'}`}
                    style={{ width: `${Math.min(row.v, 100)}%` }} />
                </div>
              ))}
            </div>
          ))}
        </Card>      </div>

      {/* Pod TOP 榜 + 状态分布 */}
      <div className="grid grid-2">
        <Card title="Pod 用量 TOP 10" subtitle="按 CPU 排序 · 点击行打开 Pod 详情">
          <div className="table-wrap"><table className="data-table">
            <thead><tr><th style={{ width: '42%' }}>Pod</th><th style={{ width: '20%' }}>命名空间</th><th style={{ width: '14%' }}>CPU(m)</th><th style={{ width: '24%' }}>内存(MiB)</th></tr></thead>
            <tbody>
              {topPods.length === 0 && <tr><td colSpan={4} className="dim">（暂无数据）</td></tr>}
              {topPods.map((p, i) => (
                <tr key={i} style={{ cursor: 'pointer' }}
                  onClick={() => openPodRef.current(p)}
                  title="点击打开 Pod 详情">
                  <td className="mono">{p.name}</td>
                  <td className="dim">{p.namespace}</td>
                  <td className="mono">{p.cpuMilli}</td>
                  <td className="mono">{Math.round(p.memMiB)}</td>
                </tr>
              ))}
            </tbody>
          </table></div>
        </Card>
        <Card title="Pod 状态分布">
          <EChart option={podPie} height={210} />
          <div className="stat-row">
            <span>{num(ov.podsTotal)} pods</span>
            <span className="dim">
              {num(ov.deployments)} deploy · {num(ov.statefulsets)} sts · {num(ov.daemonsets)} ds · {num(ov.services)} svc
            </span>
          </div>
        </Card>
      </div>

      {/* 就绪率 gauges */}
      <div className="grid grid-2">
        <Card title="节点 Ready 率">
          <EChart option={gaugeOpt(ov.nodesTotal ? (num(ov.nodesReady) / num(ov.nodesTotal)) * 100 : 0, accent)} height={150} />
          <div className="stat-row"><span>{num(ov.nodesReady)}</span><span className="dim">/ {num(ov.nodesTotal)} 台</span></div>
        </Card>
        <Card title="告警事件">
          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', height: 150 }}>
            <span style={{ fontSize: '2.5rem', fontWeight: 800, fontVariantNumeric: 'tabular-nums',
              color: num(ov.warningEvents) > 0 ? dangerC : okC }}>{num(ov.warningEvents)}</span>
          </div>
          <div className="stat-row"><span className="dim">Warning 事件数(全命名空间)</span></div>
        </Card>
      </div>
    </div>
  )
}

function RegisterModal({ onClose, onDone }: { onClose: () => void; onDone: (ok: boolean, msg: string) => void }) {
  const [name, setName] = useState('')
  const [kubeconfig, setKubeconfig] = useState('')
  const [busy, setBusy] = useState(false)

  const pickFile = async (f: File | null) => {
    if (f) setKubeconfig(await f.text())
  }

  const submit = () => {
    if (!name.trim() || !kubeconfig.trim()) return
    setBusy(true)
    postJSON('/api/plugins/containers/k8s/clusters', { name: name.trim(), kubeconfig })
      .then((d: any) => {
        if (d.ok) {
          const c = d.cluster || {}
          onDone(true, `✓ 集群 ${c.name || name} 已注册${c.status === 'ready' ? ` · 连接正常 (${c.version})` : ' · ⚠ 已注册但暂不可达'}`)
        } else {
          onDone(false, '✗ ' + (d.error || '注册失败'))
        }
      })
      .catch((e) => onDone(false, '✗ ' + String(e)))
  }

  return (
    <div className="modal-overlay" onClick={onClose}>
      <div className="modal log-modal" onClick={(e) => e.stopPropagation()} style={{ maxWidth: 640 }}>
        <div className="modal-head">
          <div className="modal-title">注册集群</div>
          <button className="btn btn-sm" onClick={onClose}>关闭</button>
        </div>
        <div style={{ padding: '1rem 1.25rem', display: 'grid', gap: '0.75rem' }}>
          <label style={{ fontSize: '0.8125rem' }}>
            集群名称
            <input className="input" value={name} onChange={(e) => setName(e.target.value)}
              placeholder="如 prod-1 / staging" style={{ width: '100%', marginTop: 4 }} />
          </label>
          <label style={{ fontSize: '0.8125rem' }}>
            kubeconfig
            <input type="file" accept=".yaml,.yml,.conf,.txt" onChange={(e) => pickFile(e.target.files?.[0] || null)}
              style={{ display: 'block', margin: '4px 0', fontSize: '0.75rem' }} />
            <textarea className="input" value={kubeconfig} onChange={(e) => setKubeconfig(e.target.value)}
              placeholder="粘贴 kubeconfig YAML(凭据仅保存在服务端 data/kubeconfigs/, 权限 0600)"
              rows={10} style={{ width: '100%', fontFamily: 'monospace', fontSize: '0.6875rem' }} />
          </label>
          <div className="modal-actions">
            <button className="btn" onClick={onClose}>取消</button>
            <button className="btn btn-accent" disabled={busy || !name.trim() || !kubeconfig.trim()} onClick={submit}>
              {busy ? '探测中…' : '注册并探测'}
            </button>
          </div>
        </div>
      </div>
    </div>
  )
}

// ── 资源弹层: Pod 详情 · 工作负载管理(scale/回滚/暂停/重启) · 节点管理 · 通用 YAML 编辑 ──

function ResourceModal({ info, onClose, act, onMsg }: {
  info: { kind: 'pod' | 'workload' | 'yaml' | 'node'; res: K8sRes; ns: string; name: string; cluster: string }
  onClose: () => void
  act: (body: Record<string, any>, confirmMsg?: string) => void
  onMsg?: (m: string) => void
}) {
  const [tab, setTab] = useState<'detail' | 'yaml'>('detail')
  const [yaml, setYaml] = useState('')
  const [yamlEditing, setYamlEditing] = useState(false)
  const [detail, setDetail] = useState<any>(null)
  const [err, setErr] = useState('')
  const [replicas, setReplicas] = useState<number | ''>('')
  const [imageDraft, setImageDraft] = useState('')
  const [revs, setRevs] = useState<any[] | null>(null)
  const [rollTo, setRollTo] = useState<number>(0)
  const [expandTo, setExpandTo] = useState('')
  const yamlReadonly = info.res === 'secrets'
  const parsed = useMemo(() => {
    if (!yaml) return null
    try { return jsYaml.load(yaml) as any } catch { return null }
  }, [yaml])
  const [related, setRelated] = useState<any>(null)
  const [logOut, setLogOut] = useState('')
  const logRef = useRef<HTMLPreElement>(null)
  useEffect(() => { if (logRef.current) logRef.current.scrollTop = logRef.current.scrollHeight }, [logOut])
  const [logBusy, setLogBusy] = useState(false)
  const [logContainer, setLogContainer] = useState('')
  const [execCmd, setExecCmd] = useState('env | sort')
  const [execOut, setExecOut] = useState('')
  const [execBusy, setExecBusy] = useState(false)

  const fetchRelated = () => {
    if (info.kind !== 'pod') return
    getJSON<{ ok: boolean; related: any }>(`/api/plugins/containers/k8s/pod/related?cluster=${info.cluster}&ns=${info.ns}&name=${info.name}`)
      .then((d) => d.ok && setRelated(d.related)).catch(() => {})
  }
  const fetchLog = () => {
    setLogBusy(true); setLogOut('')
    const c = logContainer || detail?.containers?.[0]?.name || ''
    getJSON<{ ok: boolean; log: string; error?: string }>(`/api/plugins/containers/k8s/pod/log?cluster=${info.cluster}&ns=${info.ns}&name=${info.name}&container=${encodeURIComponent(c)}&tail=200`)
      .then((d) => d.ok ? setLogOut(d.log || '(空)') : setLogOut('✗ ' + (d.error || '失败')))
      .catch((e) => setLogOut('✗ ' + String(e))).finally(() => setLogBusy(false))
  }
  const runExec = () => {
    if (!execCmd.trim()) return
    setExecBusy(true); setExecOut('')
    const c = logContainer || detail?.containers?.[0]?.name || ''
    const parts = execCmd.trim().split(/\s+/)
    postJSON('/api/plugins/containers/k8s/pod/exec', { cluster: info.cluster, ns: info.ns, name: info.name, container: c, command: parts })
      .then((d: any) => setExecOut(d.ok ? (d.stdout || '(无输出)') + (d.stderr ? '\n[stderr]\n' + d.stderr : '') : '✗ ' + (d.error || '失败')))
      .catch((e) => setExecOut('✗ ' + String(e))).finally(() => setExecBusy(false))
  }

  useEffect(() => {
    if (info.kind === 'pod') {
      getJSON<{ ok: boolean; detail: any; error?: string }>(`/api/plugins/containers/k8s/pod/detail?cluster=${info.cluster}&res=pods&ns=${info.ns}&name=${info.name}`)
        .then((d) => d.ok ? setDetail(d.detail) : setErr(d.error || '加载失败'))
        .catch((e) => setErr(String(e)))
      fetchRelated()
    }
    if (info.kind === 'workload') {
      getJSON<{ ok: boolean; replicas: number }>(`/api/plugins/containers/k8s/replicas?cluster=${info.cluster}&res=${info.res}&ns=${info.ns}&name=${info.name}`)
        .then((d) => d.ok && setReplicas(d.replicas))
        .catch(() => {})
      getJSON<{ ok: boolean; revisions: any[] }>(`/api/plugins/containers/k8s/rollout/history?cluster=${info.cluster}&res=${info.res}&ns=${info.ns}&name=${info.name}`)
        .then((d) => d.ok && setRevs(d.revisions || []))
        .catch(() => {})
    }
    getJSON<{ ok: boolean; yaml: string; error?: string }>(`/api/plugins/containers/k8s/yaml?cluster=${info.cluster}&res=${info.res}&ns=${info.ns}&name=${info.name}`)
      .then((d) => d.ok ? setYaml(d.yaml) : setErr(d.error || 'YAML 加载失败'))
      .catch((e) => setErr(String(e)))
  }, [info])

  const saveYaml = () => {
    postJSON('/api/plugins/containers/k8s/yaml/save',
      { cluster: info.cluster, res: info.res, ns: info.ns, name: info.name, yaml })
      .then((d: any) => {
        if (d.ok) { onMsg?.(`✓ ${info.name} YAML 已保存`); setYamlEditing(false) }
        else onMsg?.('✗ ' + (d.error || '保存失败'))
      })
      .catch((e) => onMsg?.('✗ ' + String(e)))
  }

  return (
    <div className="modal-overlay" onClick={onClose}>
      <div className="modal log-modal" onClick={(e) => e.stopPropagation()} style={{ maxWidth: 880 }}>
        <div className="modal-head">
          <div className="modal-title">
            {titleOf(info.res)}: <span className="mono">{info.name}</span>
            {info.ns && <span className="pill pill-sub">{info.ns}</span>}
            <span className="dim" style={{ fontSize: '0.625rem', marginLeft: 6 }}>双击行打开本页</span>
          </div>
          <button className="btn btn-sm" onClick={onClose}>关闭</button>
        </div>

        <div style={{ padding: '0.5rem 1.25rem', borderBottom: '1px solid var(--border)', display: 'flex', gap: '0.375rem', flexWrap: 'wrap' }}>
          {(info.kind === 'pod') && (
            <>
              <button className={`btn btn-sm ${tab === 'detail' ? 'btn-accent' : ''}`} onClick={() => setTab('detail')}>概览</button>
              <button className={`btn btn-sm ${tab === 'yaml' ? 'btn-accent' : ''}`} onClick={() => setTab('yaml')}>YAML</button>
              <span style={{ marginLeft: 'auto' }} />
              <button type="button" className="btn btn-sm" onClick={fetchLog} disabled={logBusy}>{logBusy ? '日志加载中…' : '日志'}</button>
              <button type="button" className="btn btn-sm" onClick={(e) => { e.preventDefault(); document.getElementById('cr-exec-anchor')?.scrollIntoView({ behavior: 'smooth', block: 'center' }) }}>执行</button>
              <button className="btn btn-sm btn-danger"
                onClick={() => act({ res: 'pods', ns: info.ns, name: info.name, action: 'delete' },
                  `优雅删除 Pod ${info.name}? (30s 优雅期, SIGTERM)`)}>删除</button>
              <button className="btn btn-sm" title="卡住 Terminating 时使用(grace=0 立即)"
                onClick={() => act({ res: 'pods', ns: info.ns, name: info.name, action: 'delete', force: true },
                  `强制删除 Pod ${info.name}? (立即, 不等待)`)}>强制</button>
            </>
          )}
          {info.kind === 'workload' && (
            <>
              <span className="pill pill-sub">scale / 回滚 / 暂停发布 / 滚动重启 / YAML 编辑</span>
              <span style={{ marginLeft: 'auto' }} />
              {info.res === 'deployments' && (
                <button className="btn btn-sm" disabled={!!detail}
                  onClick={() => act({ res: info.res, ns: info.ns, name: info.name, action: 'pause' },
                    `暂停 ${info.name} 的滚动更新流程?`)}>暂停发布</button>
              )}
              {info.res === 'deployments' && (
                <button className="btn btn-sm" disabled={!!detail}
                  onClick={() => act({ res: info.res, ns: info.ns, name: info.name, action: 'resume' })}>恢复发布</button>
              )}
              <button className="btn btn-sm btn-danger" disabled={!!detail}
                onClick={() => act({ res: info.res, ns: info.ns, name: info.name, action: 'delete' },
                  `删除 ${titleOf(info.res)} ${info.name}? 其 Pod 将一并删除!`)}>删除该负载</button>
            </>
          )}
          {info.kind === 'node' && (
            <>
              <span className="pill pill-sub">节点管理</span>
              <span style={{ marginLeft: 'auto' }} />
              <button className="btn btn-sm" disabled={!!detail}
                onClick={() => act({ res: 'nodes', name: info.name, action: 'cordon' },
                  `封锁节点 ${info.name}(不再调度新 Pod)?`)}>cordon 封锁</button>
              <button className="btn btn-sm" disabled={!!detail}
                onClick={() => act({ res: 'nodes', name: info.name, action: 'uncordon' })}>uncordon 解除</button>
              <button className="btn btn-sm btn-danger" disabled={!!detail}
                onClick={() => {
                  if (!confirm(`排空节点 ${info.name}?\n将驱逐其上所有业务 Pod(自动跳过 DaemonSet/static pod)。`)) return
                  if (!confirm('再次确认: 这是高危操作, 业务会短暂中断。继续?')) return
                  act({ res: 'nodes', name: info.name, action: 'drain' })
                }}>drain 排空</button>
            </>
          )}
          {info.kind === 'yaml' && <span className="pill pill-sub">{info.res === 'secrets' ? '只读(Secret 已脱敏)' : '双击行进入 · 支持编辑保存'}</span>}
        </div>

        <div style={{ padding: '1rem 1.25rem', maxHeight: '62vh', overflowY: 'auto' }}>
          {err && <div className="banner banner-err">{err}</div>}

          {/* Pod 详情 */}
          {info.kind === 'pod' && tab === 'detail' && (!detail ? !err && <div className="loading">加载中…</div> : (
            <>
              <div className="toolbar-strip">
                <span className="dim">状态</span><span className={`badge ${badgeOf(detail.phase)}`}>{detail.phase}</span>
                <span className="dim">节点</span><span className="mono" style={{ fontSize: '0.6875rem' }}>{detail.node || '—'}</span>
                <span className="dim">IP</span><span className="mono" style={{ fontSize: '0.6875rem' }}>{detail.ip || '—'}</span>
                <span className="dim">QoS</span><span>{detail.qos || '—'}</span>
                <span className="dim">创建</span><span>{detail.createdAt}</span>
              </div>
              <div className="table-wrap"><table className="data-table">
                <thead><tr><th>容器</th><th>镜像</th><th>就绪</th><th>重启</th><th>状态</th><th>端口</th></tr></thead>
                <tbody>{(detail.containers || []).map((c: any, i: number) => (
                  <tr key={i}>
                    <td className="mono">{c.name}</td>
                    <td className="mono dim">{c.image}</td>
                    <td><span className={`badge ${c.ready ? 'badge-ok' : 'badge-warn'}`}>{c.ready ? 'Ready' : 'No'}</span></td>
                    <td>{c.restarts}</td>
                    <td title={c.stateDetail}><span className={`badge ${badgeOf(c.state)}`}>{c.state}</span></td>
                    <td className="mono dim">{c.ports || '—'}</td>
                  </tr>
                ))}</tbody>
              </table></div>
              {(detail.events || []).length > 0 && (
                <>
                  <div className="dim" style={{ fontSize: '0.6875rem', fontWeight: 700, margin: '0.625rem 0 0.375rem' }}>相关事件</div>
                  <div className="table-wrap"><table className="data-table">
                    <thead><tr><th style={{ width: '12%' }}>级别</th><th style={{ width: '16%' }}>原因</th><th style={{ width: '58%' }}>消息</th><th style={{ width: '14%' }}>次数/时间</th></tr></thead>
                    <tbody>{detail.events.map((e: any, i: number) => (
                      <tr key={i}>
                        <td><span className={`badge ${e.type === 'Warning' ? 'badge-warn' : 'badge-ok'}`}>{e.type}</span></td>
                        <td className="mono dim">{e.reason}</td>
                        <td className="dim">{e.message}</td>
                        <td className="dim">{e.count}次 · {e.lastSeen}</td>
                      </tr>
                    ))}</tbody>
                  </table></div>
                </>
              )}
              {(detail.volumes?.length || (detail.nodeSelector && Object.keys(detail.nodeSelector).length) || detail.tolerations?.length || detail.affinitySummary !== '—' || detail.serviceAccount || detail.hostNetwork) && (
                <>
                  <div className="dim" style={{ fontSize: '0.6875rem', fontWeight: 700, margin: '0.75rem 0 0.375rem' }}>调度与卷</div>
                  <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit,minmax(220px,1fr))', gap: '0.5rem' }}>
                    {detail.volumes?.length > 0 && (
                      <div className="card" style={{ padding: '0.5rem' }}>
                        <div className="dim" style={{ fontSize: '0.6875rem', fontWeight: 600 }}>挂载卷</div>
                        {detail.volumes.map((v: any) => <div key={v.name} className="mono" style={{ fontSize: '0.6875rem' }}>{v.name}: {v.source}</div>)}
                      </div>
                    )}
                    <div className="card" style={{ padding: '0.5rem' }}>
                      <div className="dim" style={{ fontSize: '0.6875rem', fontWeight: 600 }}>调度</div>
                      <div className="dim mono" style={{ fontSize: '0.6875rem', lineHeight: 1.5 }}>
                        nodeSelector: {detail.nodeSelector && Object.keys(detail.nodeSelector).length ? JSON.stringify(detail.nodeSelector) : '—'}<br />
                        affinity: {detail.affinitySummary || '—'}<br />
                        tolerations: {(detail.tolerations || []).join(', ') || '—'}
                      </div>
                    </div>
                    <div className="card" style={{ padding: '0.5rem' }}>
                      <div className="dim" style={{ fontSize: '0.6875rem', fontWeight: 600 }}>运行时</div>
                      <div className="dim" style={{ fontSize: '0.6875rem', lineHeight: 1.5 }}>
                        SA: {detail.serviceAccount || '—'} · hostNet: {String(detail.hostNetwork)} · restart: {detail.restartPolicy}<br />
                        hostIP: {detail.hostIP || '—'}
                      </div>
                    </div>
                  </div>
                </>
              )}
              {detail.containers?.length > 0 && (
                <>
                  <div className="dim" style={{ fontSize: '0.6875rem', fontWeight: 700, margin: '0.75rem 0 0.375rem' }}>容器扩展信息</div>
                  {detail.containers.map((c: any) => (
                    <div key={c.name} className="card" style={{ padding: '0.5rem', marginBottom: '0.375rem' }}>
                      <div style={{ fontSize: '0.75rem', fontWeight: 600 }}>{c.name} <span className="dim" style={{ fontWeight:400 }}>· {c.image}</span></div>
                      <div className="dim mono" style={{ fontSize: '0.6875rem', marginTop: 2, lineHeight: 1.4 }}>
                        资源: {c.resources || '—'} · 探针: 活跃:{c.liveness} 就绪:{c.readiness}<br />
                        env: {(c.env || []).join(', ') || '—'}<br />
                        挂载: {(c.mounts || []).join(', ') || '—'}
                      </div>
                    </div>
                  ))}
                </>
              )}
              {related && (
                <>
                  <div className="dim" style={{ fontSize: '0.6875rem', fontWeight: 700, margin: '0.75rem 0 0.375rem' }}>关联资源</div>
                  <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit,minmax(260px,1fr))', gap: '0.5rem' }}>
                    <div className="card" style={{ padding: '0.5rem' }}>
                      <div className="dim" style={{ fontSize: '0.6875rem', fontWeight: 600 }}>网络策略命中 ({related.networkPolicies?.length || 0})</div>
                      {(related.networkPolicies || []).length ? related.networkPolicies.map((np: any) => (
                        <div key={np.name} className="mono" style={{ fontSize: '0.6875rem' }}>{np.namespace}/{np.name} (in:{np.ingress} e:{np.egress})</div>
                      )) : <span className="dim" style={{ fontSize: '0.6875rem' }}>无策略命中</span>}
                    </div>
                    <div className="card" style={{ padding: '0.5rem' }}>
                      <div className="dim" style={{ fontSize: '0.6875rem', fontWeight: 600 }}>存储链路 (PVC→PV→SC)</div>
                      {(related.pvcs || []).length ? related.pvcs.map((p: any) => (
                        <div key={p.name} className="mono" style={{ fontSize: '0.6875rem' }}>{p.name}: {p.status} {p.capacity} → {p.volumeName || '—'}({p.pvStatus || '—'}) SC:{p.storageClass} {p.error ? ` err:${p.error}` : ''}</div>
                      )) : <span className="dim" style={{ fontSize: '0.6875rem' }}>无 PVC 卷</span>}
                    </div>
                  </div>
                </>
              )}
              <div style={{ border: '1px solid var(--border)', borderRadius: 'var(--radius-sm)', padding: '0.5rem 0.75rem', marginTop: '0.75rem' }}>
                <div style={{ display: 'flex', alignItems: 'center', gap: 6, marginBottom: 6, fontSize: '0.8125rem', fontWeight: 600 }}>日志 <span className="dim" style={{ fontWeight: 400, fontSize: '0.6875rem' }}>近200行</span>
                  <select className="input" value={logContainer} onChange={(e) => setLogContainer(e.target.value)} style={{ marginLeft: 'auto', width: 140 }}>
                    <option value="">(默认容器)</option>
                    {(detail.containers || []).map((c: any) => <option key={c.name} value={c.name}>{c.name}</option>)}
                  </select>
                  <button type="button" className="btn btn-sm" onClick={fetchLog} disabled={logBusy}>{logBusy ? '加载中' : '拉取'}</button>
                </div>
                {logOut && <pre ref={logRef} className="code-block" style={{ maxHeight: 240, overflow: 'auto', fontSize: '0.6875rem', whiteSpace: 'pre-wrap' }}>{logOut}</pre>}
              </div>
              <div id="cr-exec-anchor" style={{ border: '1px solid var(--border)', borderRadius: 'var(--radius-sm)', padding: '0.5rem 0.75rem', marginTop: '0.5rem' }}>
                <div style={{ display: 'flex', alignItems: 'center', gap: 6, marginBottom: 6, fontSize: '0.8125rem', fontWeight: 600 }}>单次执行 <span className="dim" style={{ fontWeight: 400, fontSize: '0.6875rem' }}>非交互</span></div>
                <div style={{ display: 'flex', gap: 4 }}>
                  <select className="input" value={logContainer} onChange={(e) => setLogContainer(e.target.value)} style={{ width: 120 }}>
                    <option value="">(默认容器)</option>
                    {(detail.containers || []).map((c: any) => <option key={c.name} value={c.name}>{c.name}</option>)}
                  </select>
                  <input className="input" value={execCmd} onChange={(e) => setExecCmd(e.target.value)} placeholder="如 env | sort 或 cat /etc/hosts" style={{ flex: 1 }} />
                  <button type="button" className="btn btn-sm btn-accent" onClick={runExec} disabled={execBusy}>{execBusy ? '执行中' : '执行'}</button>
                </div>
                {execOut && <pre className="code-block" style={{ maxHeight: 240, overflow: 'auto', fontSize: '0.6875rem', whiteSpace: 'pre-wrap', marginTop: 6 }}>{execOut}</pre>}
              </div>
            </>
          ))}

          {/* Pod YAML(切到 YAML tab 时显示) */}
          {info.kind === 'pod' && tab === 'yaml' && yaml && (
            <>
              <div className="dim" style={{ fontSize: '0.6875rem', fontWeight: 700, margin: '0.625rem 0 0.375rem', display: 'flex', alignItems: 'center', gap: 8 }}>
                YAML
                {!yamlEditing && <button className="btn btn-sm" onClick={() => setYamlEditing(true)}>编辑</button>}
                {yamlEditing && <button className="btn btn-sm btn-accent" onClick={saveYaml}>保存修改</button>}
                {yamlEditing && <button className="btn btn-sm" onClick={() => setYamlEditing(false)}>取消</button>}
                {yamlEditing && <span style={{ color: 'var(--warn)', fontSize: '0.625rem' }}>保存即下发到集群(API Server 校验)</span>}
              </div>
              <textarea className="input mono" rows={16} value={yaml} readOnly={!yamlEditing}
                onChange={(e) => setYaml(e.target.value)}
                style={{ width: '100%', fontSize: '0.6875rem', lineHeight: 1.55 }} />
            </>
          )}

          {/* 工作负载管理 */}
          {info.kind === 'workload' && (
            <>
              <div className="dim" style={{ fontSize: '0.6875rem', fontWeight: 700, marginBottom: '0.375rem' }}>扩缩容</div>
              <div className="toolbar-strip">
                <input className="input" type="number" min={0} max={1000} value={replicas}
                  onChange={(e) => setReplicas(e.target.value === '' ? '' : Number(e.target.value))} style={{ width: 100 }} />
                <span className="dim">副本</span>
                <button className="btn btn-sm btn-accent" disabled={replicas === ''}
                  onClick={() => act({ res: info.res, ns: info.ns, name: info.name, action: 'scale', replicas })}>应用副本数</button>
                <span style={{ width: 12 }} />
                <button className="btn btn-sm"
                  onClick={() => act({ res: info.res, ns: info.ns, name: info.name, action: 'restart' },
                    `滚动重启 ${info.name}?`)}>滚动重启</button>
              </div>
              <div className="dim" style={{ fontSize: '0.6875rem', fontWeight: 700, margin: '0.625rem 0 0.375rem' }}>镜像更新</div>
              <div className="toolbar-strip">
                <input className="input" value={imageDraft} onChange={(e) => setImageDraft(e.target.value)} placeholder="如 nginx:1.27-alpine" style={{ flex: 1 }} />
                <button className="btn btn-sm btn-accent" disabled={!imageDraft.trim()}
                  onClick={() => act({ res: info.res, ns: info.ns, name: info.name, action: 'setImage', image: imageDraft.trim() },
                    `将 ${info.name} 镜像更新为 ${imageDraft.trim()}?`)}>更新镜像</button>
              </div>
              {parsed?.spec?.template?.spec?.containers && (
                <>
                  <div className="dim" style={{ fontSize: '0.6875rem', fontWeight: 700, margin: '0.625rem 0 0.375rem' }}>容器</div>
                  <div className="table-wrap"><table className="data-table"><thead><tr><th>容器</th><th>镜像</th><th>端口</th><th>资源</th></tr></thead><tbody>
                    {(parsed.spec.template.spec.containers || []).map((c: any) => (
                      <tr key={c.name}><td className="mono">{c.name}</td><td className="mono dim">{c.image}</td><td className="mono dim">{(c.ports || []).map((p: any) => p.containerPort).join(',') || '—'}</td><td className="dim">{c.resources ? `${c.resources.requests?.cpu || ''} ${c.resources.requests?.memory || ''} → ${c.resources.limits?.cpu || ''} ${c.resources.limits?.memory || ''}`.trim() || '—' : '—'}</td></tr>
                    ))}
                  </tbody></table></div>
                </>
              )}

              {(info.res === 'deployments' || info.res === 'statefulsets') && (
                <>
                  <div className="dim" style={{ fontSize: '0.6875rem', fontWeight: 700, margin: '0.625rem 0 0.375rem' }}>版本回滚</div>
                  {!revs ? <div className="loading">加载历史中…</div> : revs.length === 0 ? (
                    <div className="dim" style={{ fontSize: '0.75rem' }}>(无历史版本)</div>
                  ) : (
                    <div className="toolbar-strip">
                      <select className="input" value={rollTo} onChange={(e) => setRollTo(Number(e.target.value))} style={{ width: 320 }}>
                        <option value={0}>上一版本</option>
                        {revs.map((rv: any) => (
                          <option key={rv.revision} value={rv.revision}>
                            rev {rv.revision}{rv.current ? '(当前)' : ''} · {rv.age}{rv.changeCause ? ` · ${rv.changeCause}` : ''}
                          </option>
                        ))}
                      </select>
                      <button className="btn btn-sm btn-accent"
                        onClick={() => act({ res: info.res, ns: info.ns, name: info.name, action: 'rollback', revision: rollTo },
                          rollTo === 0 ? `回滚 ${info.name} 到上一版本?` : `回滚 ${info.name} 到 rev ${rollTo}?`)}>执行回滚</button>
                    </div>
                  )}
                </>
              )}

              {yaml && (
                <>
                  <div className="dim" style={{ fontSize: '0.6875rem', fontWeight: 700, margin: '0.625rem 0 0.375rem', display: 'flex', alignItems: 'center', gap: 8 }}>
                    YAML
                    {!yamlEditing && <button className="btn btn-sm" onClick={() => setYamlEditing(true)}>编辑</button>}
                    {yamlEditing && <button className="btn btn-sm btn-accent" onClick={saveYaml}>保存修改</button>}
                    {yamlEditing && <button className="btn btn-sm" onClick={() => setYamlEditing(false)}>取消</button>}
                    {yamlEditing && <span style={{ color: 'var(--warn)', fontSize: '0.625rem' }}>保存即下发到集群(API Server 校验)</span>}
                  </div>
                  <textarea className="input mono" rows={14} value={yaml} readOnly={!yamlEditing}
                    onChange={(e) => setYaml(e.target.value)}
                    style={{ width: '100%', fontSize: '0.6875rem', lineHeight: 1.55 }} />
                </>
              )}
            </>
          )}

          {/* 节点管理 */}
          {info.kind === 'node' && (
            <>
              <NodePanel cluster={info.cluster} name={info.name} />
              {yaml && (
                <>
                  <div className="dim" style={{ fontSize: '0.6875rem', fontWeight: 700, margin: '0.625rem 0 0.375rem' }}>YAML</div>
                  <pre className="code-block" style={{ maxHeight: 220, overflow: 'auto', fontSize: '0.6875rem' }}>{yaml}</pre>
                </>
              )}
            </>
          )}

          {/* CronJob / Job / PVC 快捷操作 + 通用 YAML */}
          {info.kind === 'yaml' && (
            <>
              {info.res === 'cronjobs' && (
                <div className="toolbar-strip">
                  <span className="dim">CronJob 操作:</span>
                  <button className="btn btn-sm btn-accent"
                    onClick={() => act({ res: 'cronjobs', ns: info.ns, name: info.name, action: 'trigger' },
                      `立即触发一次 ${info.name}?`)}>立即触发一次</button>
                  <button className="btn btn-sm" onClick={() => act({ res: 'cronjobs', ns: info.ns, name: info.name, action: 'suspend', suspend: true })}>suspend 挂起</button>
                  <button className="btn btn-sm" onClick={() => act({ res: 'cronjobs', ns: info.ns, name: info.name, action: 'suspend', suspend: false })}>resume 恢复</button>
                </div>
              )}
              {info.res === 'jobs' && (
                <div className="toolbar-strip">
                  <span className="dim">Job 操作:</span>
                  <button className="btn btn-sm btn-accent"
                    onClick={() => act({ res: 'jobs', ns: info.ns, name: info.name, action: 'rerun' },
                      `按当前配置重跑 Job ${info.name}(创建新 Job)?`)}>重跑(新 Job)</button>
                </div>
              )}
              {info.res === 'persistentvolumeclaims' && (
                <div className="toolbar-strip">
                  <span className="dim">PVC 扩容:</span>
                  <input className="input" style={{ width: 130 }} value={expandTo} onChange={(e) => setExpandTo(e.target.value)} placeholder="如 20Gi" />
                  <button className="btn btn-sm btn-accent" disabled={!expandTo.trim()}
                    onClick={() => act({ res: 'persistentvolumeclaims', ns: info.ns, name: info.name, action: 'expand', storage: expandTo.trim() },
                      `将 ${info.name} 扩容到 ${expandTo}? (只能扩大不能缩小)`)}>应用容量</button>
                </div>
              )}
              {parsed && info.res === 'configmaps' && parsed.data && (
                <>
                  <div className="dim" style={{ fontSize: '0.6875rem', fontWeight: 700, margin: '0.5rem 0 0.375rem' }}>数据键 ({Object.keys(parsed.data).length})</div>
                  <div className="table-wrap"><table className="data-table"><thead><tr><th>键</th><th>值预览</th></tr></thead><tbody>
                    {Object.entries(parsed.data as Record<string,string>).slice(0, 12).map(([k,v])=> <tr key={k}><td className="mono">{k}</td><td className="dim mono" style={{ maxWidth: 360, overflow: 'hidden', textOverflow: 'ellipsis' }}>{String(v).slice(0, 80)}</td></tr>)}
                  </tbody></table></div>
                </>
              )}
              {parsed && info.res === 'secrets' && (
                <div className="dim" style={{ fontSize: '0.6875rem', margin: '0.5rem 0', lineHeight: 1.4 }}>
                  类型: <span className="mono">{parsed.type || 'Opaque'}</span> · 键数: {parsed.data ? Object.keys(parsed.data as any).length : parsed.stringData ? Object.keys(parsed.stringData as any).length : 0} · 已脱敏仅显示长度
                </div>
              )}
              {parsed && info.res === 'services' && parsed.spec && (
                <>
                  <div className="dim" style={{ fontSize: '0.6875rem', fontWeight: 700, margin: '0.5rem 0 0.375rem' }}>服务概览</div>
                  <div className="table-wrap"><table className="data-table"><tbody>
                    <tr><td className="dim">类型</td><td className="mono">{parsed.spec.type || 'ClusterIP'}</td><td className="dim">ClusterIP</td><td className="mono">{parsed.spec.clusterIP || '—'}</td></tr>
                    <tr><td className="dim">Selector</td><td colSpan={3} className="mono dim">{parsed.spec.selector ? JSON.stringify(parsed.spec.selector) : '—'}</td></tr>
                    <tr><td className="dim">端口</td><td colSpan={3} className="mono dim">{(parsed.spec.ports || []).map((p:any)=> `${p.port}:${p.targetPort}/${p.protocol}${p.nodePort?`→${p.nodePort}`:''}`).join(', ') || '—'}</td></tr>
                  </tbody></table></div>
                </>
              )}
              {parsed && info.res === 'persistentvolumeclaims' && parsed.spec && (
                <div className="dim" style={{ fontSize: '0.6875rem', margin: '0.5rem 0', lineHeight: 1.5 }}>
                  容量: <span className="mono">{parsed.spec.resources?.requests?.storage || '—'}</span> · 模式: {(parsed.spec.accessModes || []).join(',') || '—'} · 类: {parsed.spec.storageClassName || '(默认)'} · 状态由 PVC 对象决定
                </div>
              )}
              {parsed && info.res === 'ingresses' && parsed.spec?.rules && (
                <>
                  <div className="dim" style={{ fontSize: '0.6875rem', fontWeight: 700, margin: '0.5rem 0 0.375rem' }}>路由规则</div>
                  <div className="table-wrap"><table className="data-table"><thead><tr><th>Host</th><th>Path</th><th>后端</th></tr></thead><tbody>
                    {(parsed.spec.rules || []).flatMap((r:any)=> (r.http?.paths||[]).map((p:any)=> <tr key={r.host+p.path}><td className="mono">{r.host||'*'}</td><td className="mono">{p.path}({p.pathType})</td><td className="mono dim">{p.backend?.service?.name}:{p.backend?.service?.port?.number || p.backend?.service?.port?.name}</td></tr>))}
                  </tbody></table></div>
                </>
              )}
              <div className="dim" style={{ fontSize: '0.6875rem', fontWeight: 700, margin: '0.5rem 0 0.375rem', display: 'flex', alignItems: 'center', gap: 8 }}>
                YAML
                {!yamlReadonly && !yamlEditing && <button className="btn btn-sm" onClick={() => setYamlEditing(true)}>编辑</button>}
                {!yamlReadonly && yamlEditing && <button className="btn btn-sm btn-accent" onClick={saveYaml}>保存修改</button>}
                {!yamlReadonly && yamlEditing && <button className="btn btn-sm" onClick={() => setYamlEditing(false)}>取消</button>}
                {yamlReadonly && <span style={{ color: 'var(--warn)' }}>数据已脱敏, 禁止在线编辑</span>}
              </div>
              <textarea className="input mono" rows={16} value={yaml} readOnly={!yamlEditing || yamlReadonly}
                onChange={(e) => setYaml(e.target.value)}
                style={{ width: '100%', fontSize: '0.6875rem', lineHeight: 1.55 }} />
            </>
          )}
        </div>
      </div>
    </div>
  )
}

// 节点实时用量小面板(metrics.k8s.io)
function NodePanel({ cluster, name }: { cluster: string; name: string }) {
  const [m, setM] = useState<any>(null)
  useEffect(() => {
    getJSON<{ ok: boolean; nodes: any[] }>(`/api/plugins/containers/k8s/metrics/nodes?cluster=${cluster}&_=${Date.now()}`)
      .then((d) => d.ok && setM((d.nodes || []).find((n: any) => n.name === name)))
      .catch(() => {})
  }, [cluster, name])
  if (!m) return <div className="loading">读取节点用量中…</div>
  return (
    <div className="toolbar-strip">
      <span className="dim">CPU</span>
      <b style={{ fontVariantNumeric: 'tabular-nums' }}>{m.cpuMilli}m</b>
      <span className="usage-bar"><span className={`usage-fill ${m.cpuPct > 80 ? 'bg-danger' : 'bg-accent'}`} style={{ width: `${Math.min(m.cpuPct, 100)}%` }} /></span>
      <span className="dim">{m.cpuPct.toFixed(1)}%</span>
      <span className="dim" style={{ marginLeft: 10 }}>内存</span>
      <b style={{ fontVariantNumeric: 'tabular-nums' }}>{Math.round(m.memMiB)}MiB</b>
      <span className="usage-bar"><span className={`usage-fill ${m.memPct > 80 ? 'bg-danger' : 'bg-ok'}`} style={{ width: `${Math.min(m.memPct, 100)}%` }} /></span>
      <span className="dim">{m.memPct.toFixed(1)}%</span>
    </div>
  )
}
