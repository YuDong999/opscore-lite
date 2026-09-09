// ── Kubernetes 多集群管理 (client-go 直连, 只读) ──
// 布局对齐 kubevision: 左侧固定侧栏(集群树 + 可折叠资源分组) + 右侧主内容区
// 分类: 概览 / 工作负载 / 网络 / 配置 / 存储 / 集群 / 策略

import { useEffect, useMemo, useRef, useState } from 'react'
import { getJSON, postJSON } from '../api/client'
import CreateResource from './CreateResource'
import Card from '../components/Card'
import EChart from '../charts/EChart'
import K8sActionPanel from '../components/K8sActionPanel'
import ExecTerminalModal from '../components/ExecTerminalModal'
import K8sCertsModal from '../components/K8sCertsModal'
import LogStreamModal from '../components/LogStreamModal'
import PortForwardModal from '../components/PortForwardModal'
import CpModal from '../components/CpModal'
import { useTheme } from '../theme'
import jsYaml from 'js-yaml'

// 侧栏分组图标 — 7 家混搭, 均为各库官方 path
function SideIcon({ paths, sw = 2 }: { paths: string[]; sw?: number }) {
  return (
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor"
      strokeWidth={sw} strokeLinecap="round" strokeLinejoin="round">
      {paths.map((d, i) => <path key={i} d={d} />)}
    </svg>
  )
}
// lucide square-stack (工作负载)
const ICON_WORKLOADS = ['M4 10c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h4c1.1 0 2 .9 2 2m0 12c-1.1 0-2-.9-2-2v-4c0-1.1.9-2 2-2h4c1.1 0 2 .9 2 2', 'M16 14h4a2 2 0 0 1 2 2v4a2 2 0 0 1-2 2h-4a2 2 0 0 1-2-2v-4a2 2 0 0 1 2-2z']
// mingcute package-line (应用管理)
const ICON_APPS = ['M5 10h11M5 10v8a1 1 0 0 0 1 1h10M5 10L3 5h11l2 5m0 0v9m0-9h4m-4 0l1-5h4l-1 5m-4 9h3a1 1 0 0 0 1-1v-8m-8 6h1']
// mingcute earth-line (网络)
const ICON_NETWORK = ['M21 12a9 9 0 0 1-9 9m9-9a9 9 0 0 0-9-9m9 9c0 1.657-4.03 3-9 3s-9-1.343-9-3m18 0c0-1.657-4.03-3-9-3s-9 1.343-9 3m9 9a9 9 0 0 1-9-9m9 9c-1.657 0-3-4.03-3-9s1.343-9 3-9m0 18c1.657 0 3-4.03 3-9s-1.343-9-3-9m-9 9a9 9 0 0 1 9-9']
// mingcute settings-1-line (配置)
const ICON_CONFIG = ['M15 12a3 3 0 1 1-6 0a3 3 0 0 1 6 0ZM10.5 3.628a3 3 0 0 1 3 0l5 2.887A3 3 0 0 1 20 9.113v5.773a3 3 0 0 1-1.5 2.598l-5 2.887a3 3 0 0 1-3 0l-5-2.887A3 3 0 0 1 4 14.886V9.113a3 3 0 0 1 1.5-2.598z']
// mingcute storage-line (存储)
const ICON_STORAGE = ['M8 17h2M8 8h2M5 20h14a1 1 0 0 0 1-1v-4a1 1 0 0 0-1-1H5a1 1 0 0 0-1 1v4a1 1 0 0 0 1 1Zm0-9h14a1 1 0 0 0 1-1V6a1 1 0 0 0-1-1H5a1 1 0 0 0-1 1v4a1 1 0 0 0 1 1Z']
// heroicons server (集群)
const ICON_CLUSTER = ['M21.75 17.25v-.228a4.5 4.5 0 0 0-.12-1.03l-2.268-9.64a3.375 3.375 0 0 0-3.285-2.602H7.923a3.375 3.375 0 0 0-3.285 2.602l-2.268 9.64a4.5 4.5 0 0 0-.12 1.03v.228m19.5 0a3 3 0 0 1-3 3H5.25a3 3 0 0 1-3-3m19.5 0a3 3 0 0 0-3-3H5.25a3 3 0 0 0-3 3m16.5 0h.008v.008h-.008zm-3 0h.008v.008h-.008z']
// heroicons shield-check (策略)
const ICON_POLICY = ['M9 12.75L11.25 15L15 9.75m-3-7.036A11.96 11.96 0 0 1 3.598 6A12 12 0 0 0 3 9.749c0 5.592 3.824 10.29 9 11.623c5.176-1.332 9-6.03 9-11.622c0-1.31-.21-2.571-.598-3.751h-.152c-3.196 0-6.1-1.248-8.25-3.285']
// heroicons key (权限与账号)
const ICON_RBAC = ['M15.75 5.25a3 3 0 0 1 3 3m3 0a6 6 0 0 1-7.029 5.912c-.563-.097-1.159.026-1.563.43L10.5 17.25H8.25v2.25H6v2.25H2.25v-2.818c0-.597.237-1.17.659-1.591l6.499-6.499c.404-.404.527-1 .43-1.563A6 6 0 1 1 21.75 8.25']

interface K8sCluster {
  id: string
  name: string
  apiServer: string
  version: string
  status: string
}

type K8sRes =
  | 'overview'
  | 'helm'
  | 'pods' | 'deployments' | 'statefulsets' | 'daemonsets' | 'replicasets' | 'jobs' | 'cronjobs'
  | 'services' | 'ingresses' | 'ingressclasses'
  | 'configmaps' | 'secrets'
  | 'persistentvolumes' | 'persistentvolumeclaims' | 'storageclasses'
  | 'nodes' | 'namespaces' | 'events'
  | 'networkpolicies' | 'resourcequotas' | 'horizontalpodautoscalers' | 'poddisruptionbudgets' | 'limitranges' | 'priorityclasses'
  | 'serviceaccounts' | 'roles' | 'rolebindings' | 'clusterroles' | 'clusterrolebindings'

// 资源分组(kubevision 信息架构), 折叠状态持久化在 localStorage
const RES_GROUPS: { key: string; label: string; icon: React.ReactNode; defaultOpen: boolean; items: { res: K8sRes; title: string }[] }[] = [
  { key: 'workloads', label: '工作负载', icon: <SideIcon paths={ICON_WORKLOADS} />, defaultOpen: true, items: [
    { res: 'pods', title: 'Pods' },
    { res: 'deployments', title: 'Deployments' },
    { res: 'statefulsets', title: 'StatefulSets' },
    { res: 'daemonsets', title: 'DaemonSets' },
    { res: 'replicasets', title: 'ReplicaSets' },
    { res: 'jobs', title: 'Jobs' },
    { res: 'cronjobs', title: 'CronJobs' },
  ]},
  { key: 'apps', label: '应用管理', icon: <SideIcon paths={ICON_APPS} />, defaultOpen: false, items: [
    { res: 'helm', title: 'Helm Releases' },
  ]},
  { key: 'network', label: '网络', icon: <SideIcon paths={ICON_NETWORK} />, defaultOpen: true, items: [
    { res: 'services', title: 'Services' },
    { res: 'ingresses', title: 'Ingresses' },
    { res: 'ingressclasses', title: 'IngressClasses' },
  ]},
  { key: 'config', label: '配置', icon: <SideIcon paths={ICON_CONFIG} />, defaultOpen: false, items: [
    { res: 'configmaps', title: 'ConfigMaps' },
    { res: 'secrets', title: 'Secrets' },
  ]},
  { key: 'storage', label: '存储', icon: <SideIcon paths={ICON_STORAGE} />, defaultOpen: false, items: [
    { res: 'persistentvolumes', title: 'PersistentVolumes' },
    { res: 'persistentvolumeclaims', title: 'PersistentVolumeClaims' },
    { res: 'storageclasses', title: 'StorageClasses' },
  ]},
  { key: 'cluster', label: '集群', icon: <SideIcon paths={ICON_CLUSTER} sw={1.5} />, defaultOpen: true, items: [
    { res: 'nodes', title: 'Nodes' },
    { res: 'namespaces', title: 'Namespaces' },
    { res: 'priorityclasses', title: 'PriorityClasses' },
    { res: 'events', title: 'Events' },
  ]},
  { key: 'policy', label: '策略与扩缩容', icon: <SideIcon paths={ICON_POLICY} sw={1.5} />, defaultOpen: false, items: [
    { res: 'horizontalpodautoscalers', title: 'HPA 自动扩缩' },
    { res: 'poddisruptionbudgets', title: 'PDB 扰动预算' },
    { res: 'networkpolicies', title: 'NetworkPolicies' },
    { res: 'resourcequotas', title: 'ResourceQuotas' },
    { res: 'limitranges', title: 'LimitRanges' },
  ]},
  { key: 'rbac', label: '权限与账号', icon: <SideIcon paths={ICON_RBAC} sw={1.5} />, defaultOpen: true, items: [
    { res: 'serviceaccounts', title: 'ServiceAccounts' },
    { res: 'roles', title: 'Roles' },
    { res: 'rolebindings', title: 'RoleBindings' },
    { res: 'clusterroles', title: 'ClusterRoles' },
    { res: 'clusterrolebindings', title: 'ClusterRoleBindings' },
  ]},
]

const NSLESS = new Set<K8sRes>(['nodes', 'namespaces', 'events', 'persistentvolumes', 'storageclasses', 'clusterroles', 'clusterrolebindings', 'priorityclasses', 'ingressclasses'])

// events 聚合行 object:"Kind/Name" → 关联对象定位(kind→详情类型 / 资源类型)
const EVENT_MODAL: Record<string, 'pod' | 'workload' | 'yaml' | 'node'> = {
  Pod: 'pod', Deployment: 'workload', StatefulSet: 'workload', Node: 'node',
}
const EVENT_RES: Record<string, string> = {
  Pod: 'pods', Deployment: 'deployments', StatefulSet: 'statefulsets', DaemonSet: 'daemonsets',
  ReplicaSet: 'replicasets', Job: 'jobs', CronJob: 'cronjobs', Service: 'services',
  Ingress: 'ingresses', ConfigMap: 'configmaps', Secret: 'secrets',
  PersistentVolumeClaim: 'persistentvolumeclaims', PersistentVolume: 'persistentvolumes',
  StorageClass: 'storageclasses', Node: 'nodes', Namespace: 'namespaces',
  HorizontalPodAutoscaler: 'horizontalpodautoscalers',
}

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
    ['type', '级别', 9, 'status'], ['reason', '原因', 14, 'mono'],
    ['object', '对象', 18, 'mono'], ['namespace', '命名空间', 16, 'dim'],
    ['count', '次数', 8], ['lastSeen', '最近', 12, 'dim'],
    ['message', '消息', 23, 'dim'],
  ],
  networkpolicies: [
    ['name', '名称', 45, 'mono'], ['namespace', '命名空间', 30, 'dim'], ['age', '年龄', 25, 'dim'],
  ],
  horizontalpodautoscalers: [
    ['name', '名称', 30, 'mono'], ['namespace', '命名空间', 20, 'dim'],
    ['target', '目标', 24, 'mono'], ['min/max', '最小/最大', 14, 'mono'], ['current', '当前/期望', 14, 'mono'], ['age', '年龄', 12, 'dim'],
  ],
  poddisruptionbudgets: [
    ['name', '名称', 30, 'mono'], ['namespace', '命名空间', 20, 'dim'],
    ['minAvailable', '最小可用', 14, 'mono'], ['healthy', '健康/期望', 16, 'mono'], ['allowed', '允许中断', 10], ['age', '年龄', 12, 'dim'],
  ],
  resourcequotas: [
    ['name', '名称', 24, 'mono'], ['namespace', '命名空间', 18, 'dim'],
    ['cpu', 'CPU(used/hard)', 25], ['memory', '内存(used/hard)', 25], ['age', '年龄', 12, 'dim'],
  ],
  limitranges: [
    ['name', '名称', 30, 'mono'], ['namespace', '命名空间', 20, 'dim'],
    ['limits', '限制类型', 26, 'dim'], ['count', '数量', 10], ['age', '年龄', 14, 'dim'],
  ],
  priorityclasses: [
    ['name', '名称', 34, 'mono'],
    ['value', '值', 12, 'mono'], ['globalDefault', '默认', 14], ['age', '年龄', 16, 'dim'],
  ],
  serviceaccounts: [
    ['name', '名称', 26, 'mono'], ['namespace', '命名空间', 20, 'dim'],
    ['secrets', 'Secrets', 26, 'dim'], ['age', '年龄', 14, 'dim'],
  ],
  roles: [
    ['name', '名称', 30, 'mono'], ['namespace', '命名空间', 20, 'dim'],
    ['rules', '规则数', 10], ['age', '年龄', 14, 'dim'],
  ],
  clusterroles: [
    ['name', '名称', 30, 'mono'],
    ['rules', '规则数', 10], ['age', '年龄', 14, 'dim'],
  ],
  rolebindings: [
    ['name', '名称', 24, 'mono'], ['namespace', '命名空间', 18, 'dim'],
    ['role', '绑定角色', 22, 'mono'], ['subjects', '主体', 30, 'dim'], ['age', '年龄', 10, 'dim'],
  ],
  clusterrolebindings: [
    ['name', '名称', 24, 'mono'],
    ['role', '绑定角色', 22, 'mono'], ['subjects', '主体', 30, 'dim'], ['age', '年龄', 10, 'dim'],
  ],
}

// 通用列推导: CRD 短名查不到内置 COLS, 从首行 row 动态生成列.
// 优先列: name / namespace / age / status; 剩余按字母序. 数值列宽 12, 字符串 18.
function crdCols(rows: any[]): Col[] {
  if (!rows || rows.length === 0) {
    return [['name', '名称', 30, 'mono'], ['namespace', '命名空间', 20, 'dim'], ['age', '年龄', 12, 'dim']]
  }
  const sample = rows[0] || {}
  const allKeys = Object.keys(sample)
  // 已知优先字段
  const priority: Record<string, [string, number, ('mono' | 'dim' | 'status')?]> = {
    name: ['名称', 26, 'mono'],
    namespace: ['命名空间', 16, 'dim'],
    status: ['状态', 12, 'status'],
    phase: ['阶段', 12, 'status'],
    age: ['年龄', 10, 'dim'],
    labels: ['标签数', 8],
  }
  const cols: Col[] = []
  for (const [k, v] of Object.entries(priority)) {
    if (allKeys.includes(k)) cols.push([k, v[0], v[1], v[2]])
  }
  // status_* / spec.* / 剩余
  const used = new Set(cols.map((c) => c[0]))
  const others = allKeys.filter((k) => !used.has(k)).sort()
  for (const k of others) {
    const v = sample[k]
    let label = k
    if (k.startsWith('status_')) label = '状态·' + k.slice(7)
    else if (k === 'replicas') label = '副本'
    else if (k === 'image') label = '镜像'
    else if (k === 'host') label = 'Host'
    else if (k === 'schedule') label = '计划'
    // 列宽: 短字段 14, 字段名长 18
    const w = k.length > 12 || (typeof v === 'string' && (v as string).length > 20) ? 18 : 14
    cols.push([k, label, w, typeof v === 'number' ? undefined : 'dim'])
  }
  return cols
}

// 取列定义: 内置 res 走 COLS, CRD 走动态 crdCols
function colsForRes(res: string, rows: any[]): Col[] {
  const builtin = COLS[res as K8sRes]
  if (builtin) return builtin
  return crdCols(rows)
}

const FOLD_KEY = 'k8s-side-fold'

// legacy 动作名 → catalog 动作名 (写轨统一后同名即同义)
const legacyActionName = (a: string): string => (a === 'setImage' ? 'set-image' : a)

export default function K8sModule({ onMsg }: { onMsg?: (m: string) => void }) {
  const [clusters, setClusters] = useState<K8sCluster[] | null>(null)
  const [clusterID, setClusterID] = useState('')
  const [res, setRes] = useState<string>('overview')
  const [crds, setCrds] = useState<{ shortName: string; kind: string; group: string; scope: string; version: string }[]>([])
  const [sortKey, setSortKey] = useState('')
  const [sortDir, setSortDir] = useState<'desc' | 'asc'>('desc')
  const [ns, setNs] = useState('all')
  const [namespaces, setNamespaces] = useState<string[]>([])
  const [rows, setRows] = useState<any[]>([])
  const [note, setNote] = useState('')
  const [loading, setLoading] = useState(false)
  const [showReg, setShowReg] = useState(false)
  const [certModalOpen, setCertModalOpen] = useState(false)
  const [createKind, setCreateKind] = useState('Deployment')
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [ctxMenu, setCtxMenu] = useState<{ x: number; y: number; row: any; res?: string } | null>(null)
  const [clusterMenu, setClusterMenu] = useState<{ x: number; y: number; cluster: any } | null>(null)
  const [folded, setFolded] = useState<Record<string, boolean>>(() => {
    try { return JSON.parse(localStorage.getItem(FOLD_KEY) || '{}') } catch { return {} }
  })
  // 资源弹层: 双击行或行内操作打开
  const [modal, setModal] = useState<{ kind: 'pod' | 'workload' | 'yaml' | 'node' | 'describe'; res: K8sRes; ns: string; name: string } | null>(null)
  const [actionPanel, setActionPanel] = useState<{ res: string; name: string; ns: string; autoOpen?: string } | null>(null)
  // 右键菜单流式操作: exec / logs / port-forward / cp (K8sModule 级 state, 供 onStream 与渲染)
  const [podTools, setPodTools] = useState<{ kind: string; cluster: string; ns: string; pod: string; containers: string[] } | null>(null)
  // 加入节点弹层: 生成 kubeadm join 命令
  const [joinNode, setJoinNode] = useState(false)

  // events 聚合行 → 关联对象(双击/右键/操作按钮共用); object:"Kind/Name"
  const eventTarget = (r: any): { res: string; modal: 'pod' | 'workload' | 'yaml' | 'node'; name: string; ns: string } | null => {
    const [kind, name] = String(r?.object || '').split('/')
    if (!kind || !name) return null
    const targetRes = EVENT_RES[kind] || kind.toLowerCase()
    return {
      res: targetRes,
      modal: EVENT_MODAL[kind] || 'yaml',
      name,
      ns: NSLESS.has(targetRes as K8sRes) ? '' : r.namespace || '',
    }
  }

  const openModal = (kind: 'pod' | 'workload' | 'yaml' | 'node' | 'describe', r: any, resOf?: string) => {
    if (!clusterID || !r?.name) return
    // 命名空间作用域解析: 集群级资源忽略 ns; 列表为"全部命名空间"时用行内自带的 namespace
    const effRes = resOf || (res as string)
    const effNs = NSLESS.has(effRes as K8sRes)
      ? ''
      : resOf ? String(r.namespace || '')
      : ns === 'all' ? String(r.namespace || '') : ns
    setModal({ kind, res: effRes as K8sRes, ns: effNs, name: String(r.name) })
  }
  // 表头排序: 点新列=降序, 再点=升序, 三点=取消(借鉴 kubevision 交互)
  const toggleSort = (k: string) => {
    if (sortKey !== k) { setSortKey(k); setSortDir('desc') }
    else if (sortDir === 'desc') setSortDir('asc')
    else { setSortKey(''); setSortDir('desc') }
  }
  const sortedRows = useMemo(() => {
    if (!sortKey) return rows
    if (!(colsForRes(res, rows) || []).some((c) => c[0] === sortKey)) return rows
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

  const rowKey = (r: any, i: number) => `${r.namespace || ''}/${r.name || i}`

  const isAllSelected = rows.length > 0 && rows.every((r, i) => selected.has(rowKey(r, i)))
  const toggleSelectAll = () => {
    if (isAllSelected) {
      setSelected(new Set())
    } else {
      setSelected(new Set(rows.map((r, i) => rowKey(r, i))))
    }
  }

  const batchAct = (action: string, confirmMsg: string) => {
    if (selected.size === 0) return
    if (!confirm(confirmMsg.replace('{count}', String(selected.size)))) return
    const targets = Array.from(selected).map((k) => {
      const [nsPart, namePart] = k.split('/')
      return { name: namePart, ...(NSLESS.has(res as K8sRes) ? {} : { ns: res === 'all' ? nsPart : ns }) }
    })
    // 批量也走 catalog 执行 (后端 /resources/action 已是 catalog 薄代理)
    postJSON('/api/plugins/containers/k8s/resources/action', { cluster: clusterID, res, action: legacyActionName(action), targets })
      .then((d: any) => {
        onMsg?.(d.ok ? `✓ 批量 ${action} ${selected.size} 个资源完成` : '✗ ' + (d.error || '失败'))
        if (d.ok) { setSelected(new Set()); setTimeout(loadRows, 600) }
      })
      .catch((e) => onMsg?.('✗ ' + String(e)))
  }

  const dblRow = (r: any) => {
    if (res === 'events') {
      const t = eventTarget(r)
      if (t) openModal(t.modal, { name: t.name, namespace: t.ns }, t.res)
      return
    }
    if (res === 'pods') openModal('pod', r)
    else if (res === 'deployments' || res === 'statefulsets') openModal('workload', r)
    else if (res === 'nodes') openModal('node', r)
    else if (res !== 'overview') openModal('yaml', r)
  }

  const act = (body: Record<string, any>, confirmMsg?: string) => {
    if (confirmMsg && !confirm(confirmMsg)) return
    // 统一写轨: 一律走 /k8s/action (catalog); legacy 顶层字段名与 catalog 参数名对齐
    const { action, res, ns, name, ...params } = body
    const mapped = legacyActionName(action as string)
    postJSON('/api/plugins/containers/k8s/action', { cluster: clusterID, res, ns, name, action: mapped, params })
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

  // 30s 自动刷新集群状态（离线恢复后自动变绿）
  useEffect(() => {
    const t = setInterval(loadClusters, 30000)
    return () => clearInterval(t)
  }, [loadClusters])

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

  const loadCrds = () => {
    if (!clusterID) { setCrds([]); return }
    getJSON<{ ok: boolean; crds: any[]; error?: string }>(`/api/plugins/containers/k8s/crds?cluster=${clusterID}&_=${Date.now()}`)
      .then((d) => {
        if (d.ok) {
          // 过滤掉 customresourcedefinitions 自身, 只展示真正的 CRD 实例
          setCrds((d.crds || []).filter((c) => c.shortName !== 'customresourcedefinitions.apiextensions.k8s.io'))
        }
      })
      .catch(() => { if (!clusterID) return; setTimeout(loadCrds, 1500) })
  }
  useEffect(loadCrds, [clusterID])

  const loadRows = () => {
    if (!clusterID || res === 'overview' || res === 'helm') return
    setLoading(true)
    if (res === 'events') {
      getJSON<{ ok: boolean; rows: any[]; error?: string }>(`/api/plugins/containers/k8s/events/aggregate?cluster=${clusterID}&_=${Date.now()}`)
        .then((d) => {
          if (!d.ok) { setRows([]); setNote(d.error || ''); return }
          const rows = (d.rows || []).slice()
          rows.sort((a, b) => {
            const pa = a.type === 'Warning' ? 1 : 0
            const pb = b.type === 'Warning' ? 1 : 0
            if (pa !== pb) return pb - pa
            return (Number(b.count) || 0) - (Number(a.count) || 0)
          })
          setRows(rows); setNote('')
        })
        .catch((e) => { setRows([]); setNote(String(e)) })
        .finally(() => setLoading(false))
      return
    }
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

  const onRowContext = (e: React.MouseEvent, r: any) => {
    e.preventDefault()
    e.stopPropagation()
    if (res === 'events') {
      const t = eventTarget(r)
      if (!t) return
      setCtxMenu({ x: e.clientX, y: e.clientY, row: { ...r, name: t.name, namespace: t.ns }, res: t.res })
      return
    }
    setCtxMenu({ x: e.clientX, y: e.clientY, row: r })
  }

  useEffect(() => {
    if (ctxMenu) {
      const h = (e: MouseEvent) => { if (!(e.target as HTMLElement)?.closest('.k8s-ctxmenu')) setCtxMenu(null) }
      document.addEventListener('mousedown', h)
      return () => document.removeEventListener('mousedown', h)
    }
    if (clusterMenu) {
      const h = (e: MouseEvent) => { if (!(e.target as HTMLElement)?.closest('.k8s-cluster-ctxmenu')) setClusterMenu(null) }
      document.addEventListener('mousedown', h)
      return () => document.removeEventListener('mousedown', h)
    }
  }, [ctxMenu, clusterMenu])

  const closeCtx = () => setCtxMenu(null)
  const openClusterMenu = (c: any, x: number, y: number) => setClusterMenu({ x, y, cluster: c })
  const closeClusterMenu = () => setClusterMenu(null)
  const handleClusterAction = (action: string) => {
    if (!clusterMenu) return
    const c = clusterMenu.cluster
    if (action === 'set-active') { setClusterID(c.id) }
    else if (action === 'remove') {
      if (!confirm(`移除集群「${c.name}」? (仅删除本地注册, 不影响集群本身)`)) return
      postJSON('/api/plugins/containers/k8s/cluster/action', { id: c.id, action: 'delete' })
        .then((d: any) => {
          if (d.ok) { loadClusters(); if (clusterID === c.id) setClusterID('') }
          else onMsg?.('✗ ' + (d.error || '删除失败'))
        })
        .catch((err) => onMsg?.('✗ ' + String(err)))
    }
    setClusterMenu(null)
  }

  return (
    <div className="k8s-shell" onClick={() => { closeCtx(); closeClusterMenu() }}>
      {/* ── 内嵌侧栏(固定高度独立滚动, 不随右侧内容移动) ── */}
      <aside className="k8s-side">
        <div className="k8s-side-clusters">
          <div className="k8s-side-section-label">集群</div>
          {(clusters || []).map((c) => (
            <div key={c.id}
              className={`k8s-side-item k8s-cluster-item ${c.id === clusterID ? 'active' : ''}`}
              onClick={() => setClusterID(c.id)}
              onContextMenu={(e) => {
                e.preventDefault()
                openClusterMenu(c, e.clientX, e.clientY)
              }}
              title={c.apiServer}>
              <span className={`k8s-dot ${c.status === 'ready' ? 'k8s-dot-ok' : 'k8s-dot-bad'}`} />
              <span className="k8s-cluster-name" title={c.name}>{c.name}</span>
              <span className={`k8s-cluster-version ${c.status === 'offline' ? 'offline' : ''}`}>
                v{c.version.replace(/^v/, '')}
              </span>
            </div>
          ))}
          <button className="btn-glass-soft btn-glass-soft-sm k8s-add-cluster"
            onClick={() => setShowReg(true)}>+ 注册集群</button>
          <div className="k8s-side-divider" />
        </div>
        <nav className="k8s-side-nav">
          <div className="k8s-side-item k8s-top-item" onClick={() => setRes('create' as any)} data-res="create">
            <span>创建资源</span>
          </div>
          <div className="k8s-side-item k8s-top-item" onClick={() => setRes('overview' as any)} data-res="overview">
            <span>概览</span>
          </div>
          <div className="k8s-side-item k8s-top-item" onClick={() => setCertModalOpen(true)} data-res="certs">
            <span>证书</span>
          </div>
          {RES_GROUPS.map((g) => (
            <div key={g.key} className="k8s-nav-group">
              <div className="k8s-side-group" onClick={() => toggleFold(g.key)}>
                <span className="k8s-group-icon">{g.icon}</span>
                <span className="k8s-group-label">{g.label}</span>
                <span className={`k8s-fold-arrow ${isFolded(folded, g.key, g.defaultOpen) ? 'folded' : ''}`}>▾</span>
              </div>
              {!isFolded(folded, g.key, g.defaultOpen) && g.items.map((it) => (
                <div key={it.res}
                  className={`k8s-side-item k8s-nav-item ${it.res === res ? 'active' : ''}`}
                  data-res={it.res}
                  onClick={() => setRes(it.res)}>
                  <span>{it.title}</span>
                </div>
              ))}
            </div>
          ))}
          {/* 动态 Custom Resources 分组: 来自集群已发现 CRD */}
          {crds.length > 0 && (
            <div key="crds" className="k8s-nav-group">
              <div className="k8s-side-group" onClick={() => toggleFold('crds')}>
                <span className="k8s-group-label">Custom Resources ({crds.length})</span>
                <span className={`k8s-fold-arrow ${isFolded(folded, 'crds', false) ? 'folded' : ''}`}>▾</span>
              </div>
              {!isFolded(folded, 'crds', false) && crds.map((c) => (
                <div key={c.shortName}
                  className={`k8s-side-item k8s-nav-item ${c.shortName === res ? 'active' : ''}`}
                  data-res={c.shortName}
                  onClick={() => setRes(c.shortName)}>
                  <span className="mono">{c.kind}</span>
                  <span className="dim" style={{ marginLeft: 6, fontSize: 10 }}>{c.group}</span>
                </div>
              ))}
            </div>
          )}
        </nav>
      </aside>

      {/* ── 主内容区 ── */}
      <section className="k8s-main" style={{ minWidth: 0, flex: 1, height: '100%', display: 'flex', flexDirection: 'column' }}>
        {!cluster ? (
          <div className="card" style={{ padding: '3rem', textAlign: 'center' }}>
            <p className="dim">尚未注册集群或未选择</p>
            <button className="btn-glass-soft btn-glass-soft-accent" onClick={() => setShowReg(true)}>+ 注册第一个集群</button>
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
        ) : res === 'helm' ? (
          <HelmPanel clusterID={clusterID} onMsg={onMsg!} />
        ) : (
            <div className="card k8s-table-card">
              <div className="card-head k8s-card-head">
                <div className="k8s-head-left">
                  <span className="k8s-res-title">{titleOf(res)}</span>
                  <span className="dim k8s-res-hint">双击行打开详情/操作</span>
                  <span className="pill pill-sub">{loading ? '加载中…' : `${rows.length} 条`}</span>
                </div>
                {!NSLESS.has(res as K8sRes) && (
                  <select className="input sel" value={ns} onChange={(e) => setNs(e.target.value)} style={{ width: 200 }}>
                    <option value="all">全部命名空间</option>
                    {namespaces.map((n) => <option key={n} value={n}>{n}</option>)}
                  </select>
                )}
                <div className="k8s-head-actions">
                  <button className="btn-glass-soft btn-glass-soft-sm" onClick={loadRows}>刷新</button>
                  {res === 'nodes' && (
                    <button className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-accent" onClick={() => setJoinNode(true)}>+ 加入节点</button>
                  )}
                  {CREATE_KIND_OF[res] && (
                    <button className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-accent" onClick={() => { setCreateKind(CREATE_KIND_OF[res]); setRes('create' as any) }}>+ 创建</button>
                  )}
                </div>
              </div>
            {note && <div className="banner banner-warn">{note}</div>}
            {/* 批量操作工具条 — 始终在 DOM 中, 避免选择状态切换时布局跳动 */}
            <div className={`k8s-batch-bar${selected.size === 0 ? ' k8s-batch-bar-hidden' : ''}`}>
              <span className="mono" style={{ fontSize: '0.75rem' }}>已选 {selected.size} 个</span>
              <div style={{ display: 'inline-flex', gap: 4, marginLeft: 'auto' }}>
                {res === 'pods' && (
                  <button className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-danger"
                    onClick={() => batchAct('delete', '批量删除 {count} 个 Pod?')}>批量删除</button>
                )}
                {res === 'deployments' && (
                  <button className="btn-glass-soft btn-glass-soft-sm"
                    onClick={() => batchAct('restart', '滚动重启 {count} 个 Deployment?')}>批量重启</button>
                )}
                {res !== 'pods' && res !== 'deployments' && (
                  <button className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-danger"
                    onClick={() => batchAct('delete', '批量删除 {count} 个资源?')}>批量删除</button>
                )}
                <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => setSelected(new Set())}>取消选择</button>
              </div>
            </div>
            <div className="table-wrap">
              <table className="data-table">
                <thead>
                  <tr>
                    <th style={{ width: 36, padding: '0.5rem 0.375rem', textAlign: 'center' }}>
                      <input type="checkbox" style={{ cursor: 'pointer' }}
                        checked={isAllSelected}
                        onChange={toggleSelectAll}
                        disabled={rows.length === 0} />
                    </th>
                    {(colsForRes(res, rows) || []).map(([k, t, w]) => (
                      <th key={k} style={{ width: `${w}%`, cursor: 'pointer', userSelect: 'none' }} onClick={() => toggleSort(k)} title="点击排序">
                        {t}
                        <span style={{ opacity: sortKey === k ? 1 : 0.3, marginLeft: 3, fontSize: '0.5rem' }}>
                          {sortKey === k ? (sortDir === 'desc' ? '▼' : '▲') : '↕'}
                        </span>
                      </th>
                     ))}<th style={{ width: 180, minWidth: 160, textAlign: 'right' }}>操作</th>
                   </tr>
                 </thead>
                <tbody>
                  {rows.length === 0 && (
                    <tr><td colSpan={(colsForRes(res, rows) || []).length + 2} className="dim">{loading ? '加载中…' : '（无数据）'}</td></tr>
                  )}
                  {sortedRows.map((r, i) => {
                    const rk = rowKey(r, i)
                    const checked = selected.has(rk)
                    const warnRow = res === 'events' && r.type === 'Warning'
                    return (
                    <tr key={rk} style={{ cursor: 'pointer', background: warnRow ? 'rgba(239, 68, 68, 0.08)' : undefined }} onDoubleClick={() => dblRow(r)}
                      onContextMenu={(e) => onRowContext(e, r)}
                      title="双击查看详情/操作 · 右键快速操作">
                      <td style={{ padding: '0.5rem 0.375rem', textAlign: 'center', whiteSpace: 'nowrap' }}>
                        <input type="checkbox" style={{ cursor: 'pointer' }}
                          checked={checked}
                          onChange={(e) => {
                            e.stopPropagation()
                            const next = new Set(selected)
                            if (e.target.checked) next.add(rk)
                            else next.delete(rk)
                            setSelected(next)
                          }} />
                      </td>
                      {(colsForRes(res, rows) || []).map(([k, , , typ]) => (
                        <td key={k} className={`${typ === 'dim' ? 'dim' : typ === 'mono' ? 'mono' : ''}`}
                          style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                          {typ === 'status' ? (
                            <span className={`badge ${badgeOf(String(r[k]))}`}>{String(r[k])}</span>
                          ) : String(r[k] ?? '—')}
                        </td>
                      ))}
<td className="row-ops" style={{ textAlign: 'right', whiteSpace: 'nowrap' }}>
                        <div className="k8s-row-actions" style={{ display: 'inline-flex', gap: 4, justifyContent: 'flex-end' }}>
                          <button className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-accent"
                            title="基于资源类型动态展示可用操作 (kubectl 等价)"
                            onClick={(e) => {
                              e.stopPropagation()
                              if (res === 'events') {
                                const t = eventTarget(r)
                                if (!t) return
                                setActionPanel({ res: t.res, name: t.name, ns: t.ns })
                                return
                              }
                              setActionPanel({ res, name: r.name, ns: r.namespace || ns })
                            }}>操作</button>
                          {res === 'pods' && (
                            <>
                            <button className="btn-glass-soft btn-glass-soft-sm" title="优雅删除(30s, SIGTERM), 卡住时可在详情里强制删除"
                              onClick={(e) => { e.stopPropagation(); act({ res: 'pods', ns: r.namespace || ns, name: r.name, action: 'delete' }, `优雅删除 Pod ${r.name}? (30s 优雅期)`) }}>删除</button>
                            <button className="btn-glass-soft btn-glass-soft-sm" title="滚动重启该 Pod(删除后由 Deployment 重新创建)"
                              onClick={(e) => { e.stopPropagation(); act({ res: 'pods', ns: r.namespace || ns, name: r.name, action: 'delete', grace: 0 }, `重启 Pod ${r.name}? (立即删除, Deployment 将自动重建)`) }}>重启</button>
                            </>
                          )}
                          {(res === 'deployments' || res === 'statefulsets' || res === 'daemonsets') && (
                            <>
                              <button className="btn-glass-soft btn-glass-soft-sm" title="回滚到上一版本"
                                onClick={(e) => { e.stopPropagation(); act({ res, ns: r.namespace || ns, name: r.name, action: 'rollback', revision: 0 }, `回滚 ${r.name} 到上一版本?`) }}>回滚</button>
                              <button className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-danger" title="优雅删除"
                                onClick={(e) => { e.stopPropagation(); act({ res, ns: r.namespace || ns, name: r.name, action: 'delete' }, `删除 ${res} ${r.name}?`) }}>删除</button>
                            </>
                          )}
                          {(res === 'jobs' || res === 'cronjobs' || res === 'services' || res === 'ingresses' || res === 'configmaps' || res === 'secrets' || res === 'persistentvolumeclaims') && (
                            <button className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-danger"
                              onClick={(e) => { e.stopPropagation(); const prompt = res === 'jobs' ? `重跑 Job ${r.name}?` : res === 'cronjobs' ? `立即触发 ${r.name}?` : `删除 ${res} ${r.name}?`; const action = res === 'cronjobs' ? 'trigger' : res === 'jobs' ? 'rerun' : 'delete'; act({ res, ns: r.namespace || ns, name: r.name, action }, prompt) }}>
                              {res === 'jobs' ? '重跑' : res === 'cronjobs' ? '触发' : '删除'}
                            </button>
                          )}
                          {res === 'nodes' && (
                            <button className="btn-glass-soft btn-glass-soft-sm" onClick={(e) => { e.stopPropagation(); dblRow(r) }}>管理</button>
                          )}
                          {!['pods','deployments','statefulsets','daemonsets','jobs','cronjobs','services','ingresses','configmaps','secrets','persistentvolumeclaims','nodes'].includes(res) && (
                            <button className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-danger" onClick={(e) => { e.stopPropagation(); act({ res, ns: r.namespace || ns, name: r.name, action: 'delete' }, `删除 ${r.name}?`) }}>删除</button>
                          )}
                        </div>
                      </td>
                    </tr>
                  )
                  })}
                </tbody>
              </table>
            </div>
          </div>
        )}
      </section>

      {/* ── 右键上下文菜单 ── */}
      {ctxMenu && (
        <K8sContextMenu
          x={ctxMenu.x}
          y={ctxMenu.y}
          res={(ctxMenu.res || res) as K8sRes}
          ns={ctxMenu.res ? ctxMenu.row.namespace || '' : ns === 'all' ? ctxMenu.row.namespace || '' : ns}
          name={ctxMenu.row.name}
          cluster={clusterID}
          onAction={(action, extra) => {
            const mres = ctxMenu.res || res
            postJSON('/api/plugins/containers/k8s/action', {
              cluster: clusterID,
              res: mres,
              ns: NSLESS.has(mres as K8sRes) ? '' : ctxMenu.res ? ctxMenu.row.namespace : ns === 'all' ? ctxMenu.row.namespace : ns,
              name: ctxMenu.row.name, action, params: extra || {},
            }).then((d: any) => {
              onMsg?.(d.ok ? `✓ ${action} ${ctxMenu.row.name} 完成` : '✗ ' + (d.error || '失败'))
              if (d.ok) setTimeout(loadRows, 600)
            }).catch((e) => onMsg?.('✗ ' + String(e)))
          }}
          onOpenForm={(action) => {
            const mres = ctxMenu.res || res
            setActionPanel({
              res: mres, ns: NSLESS.has(mres as K8sRes) ? '' : ctxMenu.res ? ctxMenu.row.namespace : ns === 'all' ? ctxMenu.row.namespace : ns,
              name: ctxMenu.row.name, autoOpen: action,
            })
            setCtxMenu(null)
          }}
          onViewDetail={() => {
            const mres = ctxMenu.res || res
            openModal(mres === 'pods' ? 'pod' : mres === 'deployments' || mres === 'statefulsets' ? 'workload' : mres === 'nodes' ? 'node' : 'yaml', ctxMenu.row, ctxMenu.res)
            setCtxMenu(null)
          }}
          onDescribe={() => {
            openModal('describe', ctxMenu.row, ctxMenu.res)
            setCtxMenu(null)
          }}
          onStream={(type) => {
            const mres = ctxMenu.res || res
            const tns = NSLESS.has(mres as K8sRes) ? '' : ctxMenu.res ? ctxMenu.row.namespace : ns === 'all' ? ctxMenu.row.namespace : ns
            const name = ctxMenu.row.name
            setCtxMenu(null)
            const open = (containers: string[]) => setPodTools({ kind: type, cluster: clusterID, ns: tns, pod: name, containers })
            if (type === 'exec' || type === 'logs') {
              getJSON<{ ok: boolean; detail: any }>(`/api/plugins/containers/k8s/pod/detail?cluster=${clusterID}&res=pods&ns=${encodeURIComponent(tns)}&name=${encodeURIComponent(name)}`)
                .then((d) => open(d.ok ? (d.detail?.containers || []).map((c: any) => c.name).filter(Boolean) : []))
                .catch(() => open([]))
            } else {
              open([])
            }
          }}
          onClose={() => setCtxMenu(null)}
        />
      )}

      {/* 资源操作面板 (action-catalog 动态填充) */}
      {actionPanel && clusterID && (
        <div className="modal-overlay" onClick={() => setActionPanel(null)}>
          <div className="modal" onClick={(e) => e.stopPropagation()} style={{ maxWidth: 720, width: '90vw' }}>
            <h3>资源操作 — {actionPanel.res}/{actionPanel.name}</h3>
            <p className="dim" style={{ marginTop: '0.25rem', fontSize: '0.8125rem' }}>
              按资源类型动态展示可用操作 (kubectl 子命令等价)。
              流式操作(进入终端/日志跟随)请用「详情」弹层里的对应按钮。
            </p>
            <div style={{ marginTop: '0.75rem' }}>
              <K8sActionPanel
                res={actionPanel.res} name={actionPanel.name}
                ns={actionPanel.ns} cluster={clusterID}
                onMsg={onMsg}
                openActionName={actionPanel.autoOpen}
                onClose={() => setActionPanel(null)} />
            </div>
            <div className="btn-row" style={{ marginTop: '1rem', justifyContent: 'flex-end' }}>
              <button className="btn-glass-soft" onClick={() => setActionPanel(null)}>关闭</button>
            </div>
          </div>
        </div>
      )}

      {joinNode && clusterID && (
        <NodeJoinModal cluster={clusterID} onClose={() => setJoinNode(false)} onMsg={onMsg || ((m: string) => window.alert(m))} />
      )}

      {/* 右键菜单流式操作: 终端 / 日志 / 端口转发 / 文件互拷 */}
      {podTools?.kind === 'exec' && (
        <ExecTerminalModal
          cluster={podTools.cluster} ns={podTools.ns} pod={podTools.pod}
          containers={podTools.containers}
          onClose={() => setPodTools(null)} />
      )}
      {podTools?.kind === 'logs' && (
        <LogStreamModal
          cluster={podTools.cluster} ns={podTools.ns} pod={podTools.pod}
          containers={podTools.containers}
          onClose={() => setPodTools(null)} />
      )}
      {podTools?.kind === 'port-forward' && (
        <PortForwardModal
          cluster={podTools.cluster} ns={podTools.ns} name={podTools.pod}
          onClose={() => setPodTools(null)} />
      )}
      {podTools?.kind === 'cp' && (
        <CpModal
          cluster={podTools.cluster} ns={podTools.ns} pod={podTools.pod}
          onClose={() => setPodTools(null)} />
      )}

      {/* 集群右键菜单 */}
      {clusterMenu && (
        <div className="k8s-cluster-ctxmenu" style={{ left: clusterMenu.x, top: clusterMenu.y }}>
          <div className="k8s-ctx-item" onClick={() => handleClusterAction('set-active')}>
            设为当前集群
          </div>
          <div className="k8s-ctx-divider" />
          <div className="k8s-ctx-item k8s-ctx-danger" onClick={() => handleClusterAction('remove')}>
            移除集群
          </div>
        </div>
      )}

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

      {certModalOpen && cluster && (
        <K8sCertsModal
          cluster={clusterID}
          clusterName={cluster.name}
          onClose={() => setCertModalOpen(false)}
          onMsg={onMsg}
        />
      )}
    </div>
  )
}

const CREATE_KIND_OF: Record<string, string> = {
  deployments: 'Deployment', statefulsets: 'StatefulSet', daemonsets: 'DaemonSet',
  services: 'Service', ingresses: 'Ingress', configmaps: 'ConfigMap',
  secrets: 'Secret', cronjobs: 'CronJob', persistentvolumeclaims: 'PVC',
  serviceaccounts: 'ServiceAccount', roles: 'Role', clusterroles: 'ClusterRole',
  rolebindings: 'RoleBinding', clusterrolebindings: 'ClusterRoleBinding',
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
        <button className="btn-glass-soft btn-glass-soft-sm" style={{ marginLeft: 'auto' }} onClick={loadAll}>刷新</button>
      </div>

      {/* 趋势 + 节点真实用量 */}
      <div className="grid grid-2">
        <Card title="集群资源趋势" subtitle="CPU / 内存 · 历史采样持久化">
          <div className="btn-row" style={{ marginBottom: '0.375rem' }}>
            {(Object.keys(WIN_LABELS)).map((w) => (
              <button key={w} className={`btn-glass-soft btn-glass-soft-sm ${win === w ? 'btn-accent' : ''}`} onClick={() => setWin(w as any)}>{WIN_LABELS[w]}</button>
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
  const [scanBusy, setScanBusy] = useState(false)
  const [scanInfo, setScanInfo] = useState('')

  const pickFile = async (f: File | null) => {
    if (f) setKubeconfig(await f.text())
  }

  const loadDefault = async () => {
    setScanBusy(true)
    setScanInfo('')
    try {
      const d: any = await getJSON('/api/plugins/containers/k8s/kubeconfig/default')
      if (!d || !d.found) {
        setScanInfo('未发现服务器默认 kubeconfig，请粘贴或上传')
        return
      }
      setKubeconfig(d.source || '')
      if (!name.trim() && d.current) setName(d.current)
      const n = (d.contexts || []).length
      const extra = n > 1 ? `（文件含 ${n} 个 context，已默认取 ${d.current || 'current-context'}）` : ''
      setScanInfo(`✓ 已读取 ${d.path}${extra}`)
    } catch (e) {
      setScanInfo('✗ 读取失败: ' + String(e))
    } finally {
      setScanBusy(false)
    }
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
    <div className="modal-overlay" onClick={onClose} onWheel={(e) => e.stopPropagation()}>
      <div className="modal log-modal" onClick={(e) => e.stopPropagation()} style={{ maxWidth: 640 }}>
        <div className="modal-head">
          <div className="modal-title">注册集群</div>
          <button className="btn-glass-soft btn-glass-soft-sm" onClick={onClose}>关闭</button>
        </div>
        <div style={{ padding: '1rem 1.25rem', display: 'grid', gap: '0.75rem' }}>
          <label style={{ fontSize: '0.8125rem' }}>
            集群名称
            <input className="input" value={name} onChange={(e) => setName(e.target.value)}
              placeholder="如 prod-1 / staging" style={{ width: '100%', marginTop: 4 }} />
          </label>
          <label style={{ fontSize: '0.8125rem' }}>
            kubeconfig
            <div style={{ display: 'flex', alignItems: 'center', gap: 8, margin: '4px 0' }}>
              <button className="btn-glass-soft" onClick={loadDefault} disabled={scanBusy}>
                {scanBusy ? '读取中…' : '读取服务器默认配置'}
              </button>
              <input type="file" accept=".yaml,.yml,.conf,.txt" onChange={(e) => pickFile(e.target.files?.[0] || null)}
                style={{ fontSize: '0.75rem' }} />
            </div>
            {scanInfo && <div style={{ fontSize: '0.75rem', marginBottom: 4 }}>{scanInfo}</div>}
            <textarea className="input" value={kubeconfig} onChange={(e) => setKubeconfig(e.target.value)}
              placeholder="留空可点上方「读取服务器默认配置」自动加载；或粘贴 kubeconfig YAML（凭据仅保存在服务端 ~/.kube/config, 权限0600）"
              rows={10} style={{ width: '100%', fontFamily: 'monospace', fontSize: '0.6875rem' }} />
          </label>
          <div className="modal-actions">
            <button className="btn-glass-soft" onClick={onClose}>取消</button>
            <button className="btn-glass-soft btn-glass-soft-accent" disabled={busy || !name.trim() || !kubeconfig.trim()} onClick={submit}>
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
  info: { kind: 'pod' | 'workload' | 'yaml' | 'node' | 'describe'; res: K8sRes; ns: string; name: string; cluster: string }
  onClose: () => void
  act: (body: Record<string, any>, confirmMsg?: string) => void
  onMsg?: (m: string) => void
}) {
  const [tab, setTab] = useState<'detail' | 'yaml'>('detail')
  const [yaml, setYaml] = useState('')
  const [yamlEditing, setYamlEditing] = useState(false)
  const [detail, setDetail] = useState<any>(null)
  const [err, setErr] = useState('')
  const [execOpen, setExecOpen] = useState(false)
  const [logStreamOpen, setLogStreamOpen] = useState(false)
  const [logModalOpen, setLogModalOpen] = useState(false)
  const [podTools, setPodTools] = useState<{ kind: string; cluster: string; ns: string; pod: string; containers: string[] } | null>(null)
  const [desc, setDesc] = useState('')
  const [descBusy, setDescBusy] = useState(false)
  const [replicas, setReplicas] = useState<number | ''>('')
  const [imageDraft, setImageDraft] = useState('')
  const [revs, setRevs] = useState<any[] | null>(null)
  const [rollTo, setRollTo] = useState<number>(0)
  // 节点操作: 打标签 / drain 驱逐选项 / 删除节点
  const [nodePanel, setNodePanel] = useState<'label' | 'drain' | 'delete' | null>(null)
  const [nLabelKey, setNLabelKey] = useState('')
  const [nLabelVal, setNLabelVal] = useState('')
  const [drainForce, setDrainForce] = useState(false)
  const [drainIgnoreDS, setDrainIgnoreDS] = useState(false)
  const [drainEmptyDir, setDrainEmptyDir] = useState(false)
  const [drainGrace, setDrainGrace] = useState('30')

  // 容器名列表(供 exec / log-stream 弹层使用)
  const logContainers = (): string[] => {
    if (Array.isArray(detail?.containers)) {
      return detail.containers.map((c: any) => c.name).filter(Boolean) as string[]
    }
    return []
  }
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
      .catch((e) => setLogOut('✗ ' + String(e)))
      .finally(() => setLogBusy(false))
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
    if (info.kind === 'describe') {
      setDescBusy(true)
      getJSON<{ ok: boolean; describe: string; error?: string }>(`/api/plugins/containers/k8s/describe?cluster=${info.cluster}&res=${info.res}&ns=${info.ns}&name=${info.name}`)
        .then((d) => d.ok ? setDesc(d.describe) : setErr(d.error || 'describe 失败'))
        .catch((e) => setErr(String(e)))
        .finally(() => setDescBusy(false))
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
    <div className="modal-overlay" onClick={onClose} onWheel={(e) => e.stopPropagation()}>
      <div className="modal log-modal" onClick={(e) => e.stopPropagation()} style={{ maxWidth: 880 }}>
        <div className="modal-head">
          <div className="modal-title">
            {titleOf(info.res)}: <span className="mono">{info.name}</span>
            {info.ns && <span className="pill pill-sub">{info.ns}</span>}
            <span className="dim" style={{ fontSize: '0.625rem', marginLeft: 6 }}>双击行打开本页</span>
          </div>
          <button className="btn-glass-soft btn-glass-soft-sm" onClick={onClose}>关闭</button>
        </div>

        <div style={{ padding: '0.5rem 1.25rem', borderBottom: '1px solid var(--border)', display: 'flex', gap: '0.375rem', flexWrap: 'wrap' }}>
          {(info.kind === 'pod') && (
            <>
              <button className={`btn-glass-soft btn-glass-soft-sm ${tab === 'detail' ? 'btn-accent' : ''}`} onClick={() => setTab('detail')}>概览</button>
              <button className={`btn-glass-soft btn-glass-soft-sm ${tab === 'yaml' ? 'btn-accent' : ''}`} onClick={() => setTab('yaml')}>YAML</button>
              <span style={{ marginLeft: 'auto' }} />
              <button type="button" className="btn-glass-soft btn-glass-soft-sm" onClick={() => { setLogModalOpen(true); fetchLog() }}>日志</button>
              <button type="button" className="btn-glass-soft btn-glass-soft-sm"
                onClick={() => setLogStreamOpen(true)}>日志跟随</button>
              <button type="button" className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-accent"
                onClick={() => setExecOpen(true)}>进入终端</button>
              <button type="button" className="btn-glass-soft btn-glass-soft-sm" onClick={(e) => { e.preventDefault(); document.getElementById('cr-exec-anchor')?.scrollIntoView({ behavior: 'smooth', block: 'center' }) }}>执行</button>
              <button className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-danger"
                onClick={() => act({ res: 'pods', ns: info.ns, name: info.name, action: 'delete' },
                  `优雅删除 Pod ${info.name}? (30s 优雅期, SIGTERM)`)}>删除</button>
              <button className="btn-glass-soft btn-glass-soft-sm" title="卡住 Terminating 时使用(grace=0 立即)"
                onClick={() => act({ res: 'pods', ns: info.ns, name: info.name, action: 'delete', force: true },
                  `强制删除 Pod ${info.name}? (立即, 不等待)`)}>强制</button>
            </>
          )}
          {info.kind === 'workload' && (
            <>
              <span className="pill pill-sub">scale / 回滚 / 暂停发布 / 滚动重启 / YAML 编辑</span>
              <span style={{ marginLeft: 'auto' }} />
              {info.res === 'deployments' && (
                <button className="btn-glass-soft btn-glass-soft-sm" disabled={!!detail}
                  onClick={() => act({ res: info.res, ns: info.ns, name: info.name, action: 'pause' },
                    `暂停 ${info.name} 的滚动更新流程?`)}>暂停发布</button>
              )}
              {info.res === 'deployments' && (
                <button className="btn-glass-soft btn-glass-soft-sm" disabled={!!detail}
                  onClick={() => act({ res: info.res, ns: info.ns, name: info.name, action: 'resume' })}>恢复发布</button>
              )}
              <button className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-danger" disabled={!!detail}
                onClick={() => act({ res: info.res, ns: info.ns, name: info.name, action: 'delete' },
                  `删除 ${titleOf(info.res)} ${info.name}? 其 Pod 将一并删除!`)}>删除该负载</button>
            </>
          )}
          {info.kind === 'node' && (
            <>
              <span className="pill pill-sub">节点管理</span>
              <span style={{ marginLeft: 'auto' }} />
              <button className="btn-glass-soft btn-glass-soft-sm" disabled={!!detail}
                onClick={() => act({ res: 'nodes', name: info.name, action: 'cordon' },
                  `封锁节点 ${info.name}(不再调度新 Pod)?`)}>cordon 封锁</button>
              <button className="btn-glass-soft btn-glass-soft-sm" disabled={!!detail}
                onClick={() => act({ res: 'nodes', name: info.name, action: 'uncordon' })}>uncordon 解除</button>
              <button className="btn-glass-soft btn-glass-soft-sm" disabled={!!detail}
                onClick={() => setNodePanel(nodePanel === 'label' ? null : 'label')}>打标签</button>
              <button className="btn-glass-soft btn-glass-soft-sm" disabled={!!detail}
                onClick={() => setNodePanel(nodePanel === 'drain' ? null : 'drain')}>drain 排空</button>
              <button className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-danger" disabled={!!detail}
                onClick={() => setNodePanel(nodePanel === 'delete' ? null : 'delete')}>删除节点</button>
            </>
          )}
          {info.kind === 'yaml' && <span className="pill pill-sub">{info.res === 'secrets' ? '只读(Secret 已脱敏)' : '双击行进入 · 支持编辑保存'}</span>}
        </div>

        <div style={{ padding: '1rem 1.25rem', maxHeight: '62vh', overflowY: 'auto' }}>
          {err && <div className="banner banner-err">{err}</div>}

          {/* 节点操作: 打标签 / drain(驱逐选项) / 删除节点 */}
          {info.kind === 'node' && nodePanel === 'label' && (
            <div className="card" style={{ padding: '0.75rem 1rem', marginBottom: '0.75rem' }}>
              <div className="dim" style={{ fontSize: '0.75rem', fontWeight: 700, marginBottom: '0.5rem' }}>打标签/移除标签</div>
              <div className="toolbar-strip">
                <input className="input" style={{ width: 200 }} placeholder="key 如 rack=b / kubernetes.io/role"
                  value={nLabelKey} onChange={(e) => setNLabelKey(e.target.value)} />
                <input className="input" style={{ width: 160 }} placeholder="value(留空=移除)"
                  value={nLabelVal} onChange={(e) => setNLabelVal(e.target.value)} />
                <button className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-accent" disabled={!nLabelKey.trim()}
                  onClick={() => {
                    if (!nLabelKey.trim()) return
                    const prompt = nLabelVal.trim() ? `给节点 ${info.name} 打标签 ${nLabelKey.trim()}=${nLabelVal.trim()}?`
                      : `移除节点 ${info.name} 的标签 ${nLabelKey.trim()}?`
                    act({ res: 'nodes', name: info.name, action: 'label', key: nLabelKey.trim(), value: nLabelVal.trim(), overwrite: true }, prompt)
                  }}>应用</button>
                <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => setNodePanel(null)}>收起</button>
              </div>
            </div>
          )}
          {info.kind === 'node' && nodePanel === 'drain' && (
            <div className="card" style={{ padding: '0.75rem 1rem', marginBottom: '0.75rem' }}>
              <div className="dim" style={{ fontSize: '0.75rem', fontWeight: 700, marginBottom: '0.5rem' }}>
                Drain 排空(通过 Eviction 子资源, 尊重 PDB; 将自动 xx 节点封锁)
              </div>
              <div className="toolbar-strip" style={{ flexWrap: 'wrap', gap: '0.75rem 1rem' }}>
                <label className="k8s-check" style={{ display: 'inline-flex', alignItems: 'center', gap: 6, fontSize: '0.8125rem' }}>
                  <input type="checkbox" checked={drainForce} onChange={(e) => setDrainForce(e.target.checked)} />
                  force 强制(跳过 PDB, grace=0)
                </label>
                <label className="k8s-check" style={{ display: 'inline-flex', alignItems: 'center', gap: 6, fontSize: '0.8125rem' }}>
                  <input type="checkbox" checked={drainIgnoreDS} onChange={(e) => setDrainIgnoreDS(e.target.checked)} />
                  ignore-daemonsets 忽略 DaemonSet
                </label>
                <label className="k8s-check" style={{ display: 'inline-flex', alignItems: 'center', gap: 6, fontSize: '0.8125rem' }}>
                  <input type="checkbox" checked={drainEmptyDir} onChange={(e) => setDrainEmptyDir(e.target.checked)} />
                  delete-emptydir-data
                </label>
                <span className="dim" style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
                  grace(秒)
                  <input className="input" type="number" style={{ width: 70 }} min={0} max={600} value={drainGrace}
                    onChange={(e) => setDrainGrace(e.target.value)} />
                </span>
              </div>
              <div className="toolbar-strip" style={{ marginTop: '0.625rem' }}>
                <button className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-danger"
                  onClick={() => {
                    if (!confirm(`排空节点 ${info.name}?\n将驱逐其上所有业务 Pod(走 Eviction 子资源, 尊重 PDB)。`)) return
                    act({
                      res: 'nodes', name: info.name, action: 'drain',
                      force: drainForce, ignoreDaemonsets: drainIgnoreDS,
                      deleteEmptyDirData: drainEmptyDir, graceSeconds: Number(drainGrace) || 30,
                    })
                  }}>执行 drain</button>
                <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => setNodePanel(null)}>收起</button>
              </div>
            </div>
          )}
          {info.kind === 'node' && nodePanel === 'delete' && (
            <div className="card" style={{ padding: '0.75rem 1rem', marginBottom: '0.75rem', borderColor: 'var(--danger, #ef4444)' }}>
              <div className="dim" style={{ fontSize: '0.75rem', fontWeight: 700, marginBottom: '0.5rem', color: 'var(--danger, #ef4444)' }}>
                删除节点 {info.name}(高危: 先 drain 驱逐 Pod, 再从集群移除 Node 对象)
              </div>
              <div className="toolbar-strip" style={{ flexWrap: 'wrap', gap: '0.75rem 1rem' }}>
                <label className="k8s-check" style={{ display: 'inline-flex', alignItems: 'center', gap: 6, fontSize: '0.8125rem' }}>
                  <input type="checkbox" checked={drainForce} onChange={(e) => setDrainForce(e.target.checked)} />
                  force 强制驱逐
                </label>
                <label className="k8s-check" style={{ display: 'inline-flex', alignItems: 'center', gap: 6, fontSize: '0.8125rem' }}>
                  <input type="checkbox" checked={drainIgnoreDS} onChange={(e) => setDrainIgnoreDS(e.target.checked)} />
                  ignore-daemonsets
                </label>
                <label className="k8s-check" style={{ display: 'inline-flex', alignItems: 'center', gap: 6, fontSize: '0.8125rem' }}>
                  <input type="checkbox" checked={drainEmptyDir} onChange={(e) => setDrainEmptyDir(e.target.checked)} />
                  delete-emptydir-data
                </label>
              </div>
              <div className="toolbar-strip" style={{ marginTop: '0.625rem' }}>
                <button className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-danger"
                  onClick={() => {
                    if (!confirm(`删除节点 ${info.name}?\n将先排空(Drain)视觉上所有业务 Pod, 然后从集群删除该 Node。`)) return
                    if (!confirm('再次确认: 节点上未保护的临时数据可能丢失。若仍需清理该机器请在最终端上执行 kubeadm reset。继续?')) return
                    act({
                      res: 'nodes', name: info.name, action: 'delete-node',
                      force: drainForce, ignoreDaemonsets: drainIgnoreDS,
                      deleteEmptyDirData: drainEmptyDir, graceSeconds: Number(drainGrace) || 30,
                    })
                  }}>执行删除节点</button>
                <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => setNodePanel(null)}>收起</button>
              </div>
            </div>
          )}

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
              {(detail.conditions || []).length > 0 && (
                <>
                  <div className="dim" style={{ fontSize: '0.6875rem', fontWeight: 700, margin: '0.625rem 0 0.375rem' }}>状态条件</div>
                  <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit,minmax(190px,1fr))', gap: '0.375rem' }}>
                    {(detail.conditions as any[]).map((cond) => {
                      const ok = cond.status === 'True'
                      return (
                        <div key={cond.type} className="card" style={{ padding: '0.4rem 0.5rem', borderColor: ok ? 'var(--border)' : 'var(--danger, #ef4444)' }}>
                          <div style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: '0.75rem', fontWeight: 600 }}>
                            <span className={`k8s-dot ${ok ? 'k8s-dot-ok' : 'k8s-dot-bad'}`} />
                            {cond.type}
                            <span style={{ marginLeft: 'auto', fontSize: '0.625rem', fontWeight: 400 }} className="dim">{cond.age}</span>
                          </div>
                          {cond.reason && <div className="mono dim" style={{ fontSize: '0.6875rem', marginTop: 2, color: ok ? undefined : 'var(--danger, #ef4444)' }}>{cond.reason}</div>}
                        </div>
                      )
                    })}
                  </div>
                </>
              )}
              <div className="table-wrap"><table className="data-table">
                <thead><tr><th>容器</th><th>镜像</th><th>就绪</th><th>重启</th><th>状态</th><th>端口</th></tr></thead>
                <tbody>{(detail.containers || []).map((c: any, i: number) => (
                  <tr key={i}>
                    <td className="mono">{c.name}</td>
                    <td className="mono dim">{c.image}</td>
                    <td><span className={`badge ${c.ready ? 'badge-ok' : 'badge-warn'}`}>{c.ready ? 'Ready' : 'No'}</span></td>
                    <td>{c.restarts}</td>
                    <td>
                      <span className={`badge ${c.state === 'running' ? 'badge-ok' : c.state === 'terminated' ? 'badge-warn' : 'badge-warn'}`}>{c.state}</span>
                      {c.state === 'waiting' && c.stateDetail && (
                        <span className="mono" style={{ display: 'block', fontSize: '0.6875rem', marginTop: 2, color: 'var(--danger, #ef4444)' }} title={c.stateDetail}>{c.stateDetail}</span>
                      )}
                      {c.state === 'terminated' && c.stateDetail && (
                        <span className="mono dim" style={{ display: 'block', fontSize: '0.6875rem', marginTop: 2 }} title={c.stateDetail}>{c.stateDetail}</span>
                      )}
                    </td>
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
                      <tr key={i} style={e.type === 'Warning' ? { background: 'rgba(239, 68, 68, 0.08)' } : undefined}>
                        <td><span className={`badge ${e.type === 'Warning' ? 'badge-warn' : 'badge-ok'}`}>{e.type}</span></td>
                        <td className={`mono ${e.type === 'Warning' ? '' : 'dim'}`}>{e.reason}</td>
                        <td className={e.type === 'Warning' ? '' : 'dim'}>{e.message}</td>
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
                  <button type="button" className="btn-glass-soft btn-glass-soft-sm" onClick={fetchLog} disabled={logBusy}>{logBusy ? '加载中' : '拉取'}</button>
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
                  <button type="button" className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-accent" onClick={runExec} disabled={execBusy}>{execBusy ? '执行中' : '执行'}</button>
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
                {!yamlEditing && <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => setYamlEditing(true)}>编辑</button>}
                {yamlEditing && <button className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-accent" onClick={saveYaml}>保存修改</button>}
                {yamlEditing && <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => setYamlEditing(false)}>取消</button>}
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
                <button className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-accent" disabled={replicas === ''}
                  onClick={() => act({ res: info.res, ns: info.ns, name: info.name, action: 'scale', replicas })}>应用副本数</button>
                <span style={{ width: 12 }} />
                <button className="btn-glass-soft btn-glass-soft-sm"
                  onClick={() => act({ res: info.res, ns: info.ns, name: info.name, action: 'restart' },
                    `滚动重启 ${info.name}?`)}>滚动重启</button>
              </div>
              <div className="dim" style={{ fontSize: '0.6875rem', fontWeight: 700, margin: '0.625rem 0 0.375rem' }}>镜像更新</div>
              <div className="toolbar-strip">
                <input className="input" value={imageDraft} onChange={(e) => setImageDraft(e.target.value)} placeholder="如 nginx:1.27-alpine" style={{ flex: 1 }} />
                <button className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-accent" disabled={!imageDraft.trim()}
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
                      <button className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-accent"
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
                    {!yamlEditing && <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => setYamlEditing(true)}>编辑</button>}
                    {yamlEditing && <button className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-accent" onClick={saveYaml}>保存修改</button>}
                    {yamlEditing && <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => setYamlEditing(false)}>取消</button>}
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
                  <button className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-accent"
                    onClick={() => act({ res: 'cronjobs', ns: info.ns, name: info.name, action: 'trigger' },
                      `立即触发一次 ${info.name}?`)}>立即触发一次</button>
                  <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => act({ res: 'cronjobs', ns: info.ns, name: info.name, action: 'suspend', suspend: true })}>suspend 挂起</button>
                  <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => act({ res: 'cronjobs', ns: info.ns, name: info.name, action: 'suspend', suspend: false })}>resume 恢复</button>
                </div>
              )}
              {info.res === 'jobs' && (
                <div className="toolbar-strip">
                  <span className="dim">Job 操作:</span>
                  <button className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-accent"
                    onClick={() => act({ res: 'jobs', ns: info.ns, name: info.name, action: 'rerun' },
                      `按当前配置重跑 Job ${info.name}(创建新 Job)?`)}>重跑(新 Job)</button>
                </div>
              )}
              {info.res === 'persistentvolumeclaims' && (
                <div className="toolbar-strip">
                  <span className="dim">PVC 扩容:</span>
                  <input className="input" style={{ width: 130 }} value={expandTo} onChange={(e) => setExpandTo(e.target.value)} placeholder="如 20Gi" />
                  <button className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-accent" disabled={!expandTo.trim()}
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
                {!yamlReadonly && !yamlEditing && <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => setYamlEditing(true)}>编辑</button>}
                {!yamlReadonly && yamlEditing && <button className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-accent" onClick={saveYaml}>保存修改</button>}
                {!yamlReadonly && yamlEditing && <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => setYamlEditing(false)}>取消</button>}
                {yamlReadonly && <span style={{ color: 'var(--warn)' }}>数据已脱敏, 禁止在线编辑</span>}
              </div>
              <textarea className="input mono" rows={16} value={yaml} readOnly={!yamlEditing || yamlReadonly}
                onChange={(e) => setYaml(e.target.value)}
                style={{ width: '100%', fontSize: '0.6875rem', lineHeight: 1.55 }} />
            </>
          )}

          {/* Describe 描述 */}
          {info.kind === 'describe' && (
            <>
              <div className="dim" style={{ fontSize: '0.6875rem', fontWeight: 700, marginBottom: '0.375rem' }}>
                {titleOf(info.res)} {info.name} 描述 (kubectl describe 风格)
                {descBusy && <span className="dim" style={{ fontWeight: 400 }}> · 加载中…</span>}
              </div>
              {!descBusy && !desc && !err && <div className="loading">加载中…</div>}
              {desc && <pre className="code-block" style={{ maxHeight: '56vh', overflow: 'auto', fontSize: '0.75rem', whiteSpace: 'pre-wrap' }}>{desc}</pre>}
            </>
          )}
        </div>
      </div>

      {/* 交互式 exec (WS + xterm) */}
      {execOpen && (
        <ExecTerminalModal
          cluster={info.cluster} ns={info.ns} pod={info.name}
          containers={logContainers()}
          onClose={() => setExecOpen(false)} />
      )}

      {/* 流式日志跟随 */}
      {logStreamOpen && (
        <LogStreamModal
          cluster={info.cluster} ns={info.ns} pod={info.name}
          containers={logContainers()}
          onClose={() => setLogStreamOpen(false)} />
      )}

      {/* 右键菜单流式操作: 终端 / 日志 / 端口转发 / 文件互拷 */}
      {podTools?.kind === 'exec' && (
        <ExecTerminalModal
          cluster={podTools.cluster} ns={podTools.ns} pod={podTools.pod}
          containers={podTools.containers}
          onClose={() => setPodTools(null)} />
      )}
      {podTools?.kind === 'logs' && (
        <LogStreamModal
          cluster={podTools.cluster} ns={podTools.ns} pod={podTools.pod}
          containers={podTools.containers}
          onClose={() => setPodTools(null)} />
      )}
      {podTools?.kind === 'port-forward' && (
        <PortForwardModal
          cluster={podTools.cluster} ns={podTools.ns} name={podTools.pod}
          onClose={() => setPodTools(null)} />
      )}
      {podTools?.kind === 'cp' && (
        <CpModal
          cluster={podTools.cluster} ns={podTools.ns} pod={podTools.pod}
          onClose={() => setPodTools(null)} />
      )}

      {/* 日志(静态, 近200行)弹窗 */}
      {logModalOpen && (
        <div className="modal-overlay" onClick={() => setLogModalOpen(false)}>
          <div className="modal" onClick={(e) => e.stopPropagation()}
            style={{ maxWidth: 800, width: '90vw', maxHeight: '70vh', display: 'flex', flexDirection: 'column' }}>
            <h3>Pod 日志 — {info.ns ? `${info.ns}/` : ''}{info.name}{logContainer ? ` · ${logContainer}` : ''}</h3>
            <div className="btn-row" style={{ marginTop: '0.5rem' }}>
              <select className="input" value={logContainer} onChange={(e) => setLogContainer(e.target.value)} style={{ width: 160 }}>
                <option value="">(默认容器)</option>
                {(detail.containers || []).map((c: any) => <option key={c.name} value={c.name}>{c.name}</option>)}
              </select>
              <button className="btn-glass-soft btn-glass-soft-accent" onClick={fetchLog} disabled={logBusy}>{logBusy ? '加载中…' : '刷新'}</button>
              <span className="dim" style={{ fontSize: '0.75rem', flex: 1 }}>近200行</span>
              <button className="btn-glass-soft" onClick={() => setLogModalOpen(false)}>关闭</button>
            </div>
            {logOut ? (
              <pre ref={logRef} className="code-block" style={{ flex: 1, overflow: 'auto', fontSize: '0.6875rem', whiteSpace: 'pre-wrap', marginTop: '0.5rem', minHeight: 200 }}>{logOut}</pre>
            ) : (
              <div className="loading" style={{ padding: '1rem' }}>{logBusy ? '加载中…' : ''}</div>
            )}
          </div>
        </div>
      )}
    </div>
  )
}

// ── 右键上下文菜单 ──
// 按资源类型差异化: 资源专属快捷操作(form=打开可视化表单→命令预览→确认; direct=直接执行)
// label 由后端 /action-catalog 提供(权威), CTX_DEFS 只声明展示顺序/形式/确认语。
type ActionType = { name: string; label?: string; category?: string }
interface CtxAction { label: string; action: string; form?: boolean; direct?: boolean; danger?: boolean; confirm?: string; extra?: Record<string, any>; stream?: string }
type CtxItem = CtxAction | { sep: boolean }

const CTX_DEFS: Partial<Record<K8sRes, CtxItem[]>> = {
  pods: [
    { label: '进入终端', action: 'exec', stream: 'exec' },
    { label: '日志跟随', action: 'logs', stream: 'logs' },
    { label: '端口转发', action: 'port-forward', stream: 'port-forward' },
    { label: '文件互拷', action: 'cp', stream: 'cp' },
    { label: '等待条件就绪', action: 'wait', form: true },
  ],
  horizontalpodautoscalers: [
    { label: '编辑扩缩规则', action: 'update-hpa', form: true },
  ],
  deployments: [
    { label: '扩缩容', action: 'scale', form: true },
    { label: '更新镜像', action: 'set-image', form: true },
    { label: '设置环境变量', action: 'set-env', form: true },
    { label: '设置资源限制', action: 'set-resources', form: true },
    { label: '设置 ServiceAccount', action: 'set-sa', form: true },
    { label: '创建 HPA 自动扩缩', action: 'autoscale', form: true },
    { label: '暴露为 Service', action: 'expose', form: true },
    { label: '等待条件就绪', action: 'wait', form: true },
    { label: '滚动重启', action: 'restart', direct: true, confirm: '滚动重启 {name}?' },
    { label: '暂停发布', action: 'pause', direct: true, confirm: '暂停发布 {name}?' },
    { label: '恢复发布', action: 'resume', direct: true, confirm: '恢复发布 {name}?' },
    { label: '回滚', action: 'rollback', form: true },
  ],
  statefulsets: [
    { label: '扩缩容', action: 'scale', form: true },
    { label: '更新镜像', action: 'set-image', form: true },
    { label: '设置环境变量', action: 'set-env', form: true },
    { label: '设置资源限制', action: 'set-resources', form: true },
    { label: '设置 ServiceAccount', action: 'set-sa', form: true },
    { label: '创建 HPA 自动扩缩', action: 'autoscale', form: true },
    { label: '暴露为 Service', action: 'expose', form: true },
    { label: '等待条件就绪', action: 'wait', form: true },
    { label: '滚动重启', action: 'restart', direct: true, confirm: '滚动重启 {name}?' },
    { label: '回滚', action: 'rollback', form: true },
  ],
  daemonsets: [
    { label: '更新镜像', action: 'set-image', form: true },
    { label: '设置环境变量', action: 'set-env', form: true },
    { label: '设置资源限制', action: 'set-resources', form: true },
    { label: '设置 ServiceAccount', action: 'set-sa', form: true },
    { label: '等待条件就绪', action: 'wait', form: true },
    { label: '滚动重启', action: 'restart', direct: true, confirm: '滚动重启 {name}?' },
  ],
  jobs: [
    { label: '重跑任务', action: 'rerun', direct: true, confirm: '重跑任务 {name}?' },
    { label: '等待条件就绪', action: 'wait', form: true },
  ],
  cronjobs: [
    { label: '立即触发', action: 'trigger', direct: true, confirm: '立即触发 {name}?' },
    { label: '挂起 / 恢复', action: 'suspend', form: true },
  ],
  persistentvolumeclaims: [
    { label: '扩容存储', action: 'expand', form: true },
  ],
  nodes: [
    { label: '封锁调度 (Cordon)', action: 'cordon', direct: true, confirm: '封锁节点 {name}?' },
    { label: '解除封锁 (Uncordon)', action: 'uncordon', direct: true, confirm: '解除封锁节点 {name}?' },
    { label: 'Drain 排空', action: 'drain', form: true },
    { label: '添加污点', action: 'taint-add', form: true },
    { label: '移除污点', action: 'taint-remove', form: true },
    { label: '删除节点', action: 'delete-node', form: true, danger: true, confirm: '删除节点 {name}?' },
  ],
  namespaces: [
    { label: '创建 ResourceQuota', action: 'create-quota', form: true },
    { label: '创建 LimitRange', action: 'create-limitrange', form: true },
    { label: '创建 NetworkPolicy', action: 'create-networkpolicy', form: true },
  ],
}

function K8sContextMenu({ x, y, res, ns, name, cluster, onAction, onOpenForm, onViewDetail, onDescribe, onStream, onClose }: {
  x: number; y: number
  res: K8sRes; ns: string; name: string; cluster: string
  onAction: (action: string, extra?: Record<string, any>) => void
  onOpenForm: (action: string) => void
  onViewDetail: () => void
  onDescribe: () => void
  onStream: (type: string) => void
  onClose: () => void
}) {
  // catalog 驱动: 后端 action-label 为权威, CTX_DEFS 只声明展示顺序/行为
  const [catalogLabels, setCatalogLabels] = useState<Record<string, string>>({})
  useEffect(() => {
    const cur = res as string
    if (!cur) return
    getJSON<{ ok: boolean; actions: ActionType[]; error?: string }>(
      `/api/plugins/containers/k8s/action-catalog?res=${encodeURIComponent(cur)}&t=${Date.now()}`
    ).then((d) => {
      if (!d.ok) return
      const m: Record<string, string> = {}
      for (const a of d.actions || []) m[a.name] = a.label || a.name
      setCatalogLabels(m)
    }).catch(() => {})
  }, [res])

  const items: (CtxItem & { label?: string; action?: string; danger?: boolean; confirm?: string; extra?: Record<string, any> })[] = []
  const defs = CTX_DEFS[res] || []
  for (const it of defs) {
    if ('sep' in it) continue
    items.push({
      // 目录中已注册的 action 用后端 label, 未注册的降级为前端 label (并保留提示不隐藏)
      label: catalogLabels[it.action] || it.label, action: it.action, danger: it.danger, extra: it.extra,
      confirm: it.confirm ? it.confirm.replace('{name}', name).replace('{ns}', ns) : undefined,
      form: it.form, stream: (it as any).stream,
    })
  }
  if (items.length) items.push({ sep: true } as any)
  items.push({ label: '打标签', action: 'label', form: true })
  items.push({ label: '打注解', action: 'annotate', form: true })
  items.push({ label: '查看详情', action: 'view-detail' })
  items.push({ label: 'Describe', action: 'describe' })
  items.push({ sep: true } as any)
  items.push({ label: '删除资源', action: 'delete', danger: true, confirm: `删除 ${name}?` })
  return (
    <div className="k8s-ctxmenu" style={{ left: x, top: y }} onClick={onClose}>
      {items.map((it, i) =>
        (it as any).sep
          ? <div key={i} className="k8s-ctx-divider" />
          : <div key={i} className={`k8s-ctx-item ${it.danger ? 'k8s-ctx-danger' : ''}`}
              onClick={(e) => {
                e.stopPropagation()
                if (it.confirm && !confirm(it.confirm)) return
                if (it.action === 'view-detail') { onViewDetail() }
                else if (it.action === 'describe') { onDescribe() }
                else if (it.stream) { onStream(it.stream) }
                else if (it.form) { onOpenForm(it.action!) }
                else onAction(it.action!, it.extra)
              }}>
            {it.label}
          </div>
      )}
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

// ── 加入节点 (kubeadm join 命令) ──
function NodeJoinModal({ cluster, onClose, onMsg }: { cluster: string; onClose: () => void; onMsg: (m: string) => void }) {
  const [ttl, setTtl] = useState(1)
  const [role, setRole] = useState<'worker' | 'control-plane'>('worker')
  const [busy, setBusy] = useState(false)
  const [cmd, setCmd] = useState('')
  const [copied, setCopied] = useState(false)
  const load = () => {
    setBusy(true)
    setCmd('')
    getJSON<{ ok: boolean; command: string; error?: string }>(`/api/plugins/containers/k8s/node-join-command?cluster=${cluster}&ttl=${ttl}&role=${role}`)
      .then((d) => { if (d.ok) setCmd(d.command); else onMsg('✗ ' + (d.error || '生成失败')) })
      .catch((e) => onMsg('✗ ' + String(e)))
      .finally(() => setBusy(false))
  }
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(cmd)
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    } catch { onMsg('✗ 复制失败(请手动选择复制)') }
  }
  return (
    <div className="modal-overlay" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()} style={{ maxWidth: 640, width: '90vw' }}>
        <h3>加入节点 (kubeadm join)</h3>
        <p className="dim" style={{ marginTop: '0.25rem', fontSize: '0.8125rem' }}>
          在 control-plane 上生成 join 命令。前往目标新节点执行该命令即可入群。
          注意: 新节点需已安装 kubeadm/kubelet/容器运行时, 且控制面端口可达。
        </p>
        <div className="toolbar-strip" style={{ margin: '0.75rem 0', flexWrap: 'wrap', gap: '0.5rem 0.75rem' }}>
          <span className="pill pill-sub" style={{ marginRight: 4 }}>节点角色</span>
          <label className="k8s-check" style={{ display: 'inline-flex', alignItems: 'center', gap: 6, fontSize: '0.8125rem' }}>
            <input type="radio" name="join-role" checked={role === 'worker'}
              onChange={() => { setRole('worker'); setCmd('') }} /> Worker 工作节点
          </label>
          <label className="k8s-check" style={{ display: 'inline-flex', alignItems: 'center', gap: 6, fontSize: '0.8125rem' }}>
            <input type="radio" name="join-role" checked={role === 'control-plane'}
              onChange={() => { setRole('control-plane'); setCmd('') }} /> Control-plane 控制面(Master)
          </label>
          <span style={{ marginLeft: 'auto', display: 'inline-flex', alignItems: 'center', gap: 6 }}>
            <span className="dim">Token 有效期(小时)</span>
            <input className="input" type="number" style={{ width: 90 }} min={1} max={720} value={ttl}
              onChange={(e) => setTtl(Math.max(1, Number(e.target.value) || 1))} />
          </span>
        </div>
        <div className="toolbar-strip" style={{ margin: '0.75rem 0' }}>
          <button className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-accent" disabled={busy} onClick={load}>
            {busy ? '生成中…' : (cmd ? '重新生成' : '生成 join 命令')}
          </button>
          {role === 'control-plane' && (
            <span className="dim" style={{ fontSize: '0.75rem' }}>会更新 kube-system/kubeadm-certs Secret(含证书密钥), 用于控制面节点证书分发</span>
          )}
        </div>
        {cmd && (
          <pre className="code-block" style={{ fontSize: '0.75rem', overflow: 'auto', wordBreak: 'break-all', userSelect: 'text' }}>{cmd}</pre>
        )}
        <div className="btn-row" style={{ marginTop: '1rem', justifyContent: 'flex-end' }}>
          {cmd && <button className="btn-glass-soft btn-glass-soft-sm" onClick={copy}>{copied ? '✓ 已复制' : '复制命令'}</button>}
          <button className="btn-glass-soft" onClick={onClose}>关闭</button>
        </div>
      </div>
    </div>
  )
}

// ── Helm Releases 管理 ──
function HelmPanel({ clusterID, onMsg }: { clusterID: string; onMsg: (m: string) => void }) {
  const [releases, setReleases] = useState<any[]>([])
  const [busy, setBusy] = useState(false)
  const [showInstall, setShowInstall] = useState(false)
  const [detail, setDetail] = useState<any>(null)
  const [history, setHistory] = useState<any[]>([])
  const [values, setValues] = useState('')
  const [detailTab, setDetailTab] = useState<'history' | 'values'>('history')

  const load = () => {
    setBusy(true)
    getJSON<{ ok: boolean; releases: any[] }>(`/api/plugins/containers/k8s/helm/releases?cluster=${clusterID}&_=${Date.now()}`)
      .then((d) => d.ok ? setReleases(d.releases || []) : onMsg('✗ ' + (d.error || '加载失败')))
      .catch((e) => onMsg('✗ ' + String(e)))
      .finally(() => setBusy(false))
  }
  useEffect(load, [clusterID])

  useEffect(() => {
    if (!detail) return
    if (detailTab === 'history') {
      getJSON<{ ok: boolean; history: any[] }>(`/api/plugins/containers/k8s/helm/history?cluster=${clusterID}&name=${detail.name}&ns=${detail.namespace}`)
        .then((d) => d.ok && setHistory(d.history || []))
        .catch(() => setHistory([]))
    } else {
      getJSON<{ ok: boolean; values: string }>(`/api/plugins/containers/k8s/helm/values?cluster=${clusterID}&name=${detail.name}&ns=${detail.namespace}`)
        .then((d) => d.ok && setValues(d.values || ''))
        .catch(() => setValues(''))
    }
  }, [detail, detailTab, clusterID])

  const uninstall = (rel: any) => {
    if (!confirm(`卸载 Release ${rel.name} (ns ${rel.namespace})? 其管理的所有资源将一并删除!`)) return
    postJSON('/api/plugins/containers/k8s/helm/uninstall', { cluster: clusterID, name: rel.name, namespace: rel.namespace })
      .then((d: any) => { onMsg(d.ok ? `✓ 已卸载 ${rel.name}` : '✗ ' + (d.error || '卸载失败')); if (d.ok) { setDetail(null); load() } })
      .catch((e) => onMsg('✗ ' + String(e)))
  }

  const rollback = (rel: any, rev: number) => {
    if (!confirm(`回滚 ${rel.name} 到 revision ${rev}?`)) return
    postJSON('/api/plugins/containers/k8s/helm/rollback', { cluster: clusterID, name: rel.name, namespace: rel.namespace, revision: rev })
      .then((d: any) => { onMsg(d.ok ? `✓ 已回滚 ${rel.name} 到 rev ${rev}` : '✗ ' + (d.error || '回滚失败')); if (d.ok) load() })
      .catch((e) => onMsg('✗ ' + String(e)))
  }

  return (
    <div className="card k8s-table-card">
      <div className="card-head k8s-card-head">
        <div className="k8s-head-left">
          <span className="k8s-res-title">Helm Releases</span>
          <span className="pill pill-sub">{busy ? '加载中…' : `${releases.length} 个`}</span>
          <span className="dim k8s-res-hint">发布 · 升级 · 回滚 · 卸载</span>
        </div>
        <div className="k8s-head-actions">
          <button className="btn-glass-soft btn-glass-soft-sm" onClick={load}>刷新</button>
          <button className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-accent" onClick={() => setShowInstall(true)}>+ 安装</button>
        </div>
      </div>
      {releases.length === 0 && !busy && (
        <div className="dim" style={{ padding: '2rem', textAlign: 'center' }}>暂无 Release, 点击"+ 安装"发布应用</div>
      )}
      <div className="table-wrap"><table className="data-table">
        <thead><tr><th>名称</th><th>命名空间</th><th>版本</th><th>状态</th><th>Chart</th><th>App 版本</th><th>更新时间</th><th style={{ textAlign: 'right' }}>操作</th></tr></thead>
        <tbody>
          {releases.map((r) => (
            <tr key={r.name + r.namespace}>
              <td className="mono">{r.name}</td>
              <td className="mono dim">{r.namespace}</td>
              <td className="mono">v{r.revision}</td>
              <td><span className={`badge ${String(r.status).toLowerCase() === 'deployed' ? 'badge-ok' : 'badge-warn'}`}>{r.status}</span></td>
              <td className="mono dim">{r.chart}</td>
              <td className="mono dim">{r.app_version || '—'}</td>
              <td className="dim">{r.updated}</td>
              <td style={{ textAlign: 'right', whiteSpace: 'nowrap' }}>
                <div style={{ display: 'inline-flex', gap: 4 }}>
                  <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => setDetail(r)}>详情</button>
                  <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => { setDetail(r); setDetailTab('history'); }}>回滚</button>
                  <button className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-danger" onClick={() => uninstall(r)}>卸载</button>
                </div>
              </td>
            </tr>
          ))}
        </tbody>
      </table></div>

      {showInstall && <HelmInstallModal clusterID={clusterID} onClose={() => setShowInstall(false)} onDone={(ok, msg) => { onMsg(msg); setShowInstall(false); if (ok) load() }} />}

      {detail && (
        <div className="modal-overlay" onClick={() => setDetail(null)} onWheel={(e) => e.stopPropagation()}>
          <div className="modal log-modal" onClick={(e) => e.stopPropagation()} style={{ maxWidth: 760 }}>
            <div className="modal-head">
              <div className="modal-title">Release: <span className="mono">{detail.name}</span></div>
              <div style={{ display: 'flex', gap: 4 }}>
                <button className={`btn-glass-soft btn-glass-soft-sm ${detailTab === 'history' ? 'btn-accent' : ''}`} onClick={() => setDetailTab('history')}>历史</button>
                <button className={`btn-glass-soft btn-glass-soft-sm ${detailTab === 'values' ? 'btn-accent' : ''}`} onClick={() => setDetailTab('values')}>Values</button>
                <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => setDetail(null)}>关闭</button>
              </div>
            </div>
            <div style={{ padding: '1rem 1.25rem', maxHeight: '60vh', overflowY: 'auto' }}>
              {detailTab === 'history' && (
                history.length === 0 ? <div className="loading">加载历史…</div> :
                <table className="data-table"><thead><tr><th>版本</th><th>更新时间</th><th>状态</th><th>说明</th><th></th></tr></thead><tbody>
                  {history.map((h) => (
                    <tr key={h.revision}>
                      <td className="mono">v{h.revision}</td>
                      <td className="dim">{h.updated}</td>
                      <td><span className={`badge ${String(h.status).toLowerCase() === 'deployed' ? 'badge-ok' : 'badge-warn'}`}>{h.status}</span></td>
                      <td className="dim">{h.description}</td>
                      <td><button className="btn-glass-soft btn-glass-soft-sm" disabled={String(h.status).toLowerCase() === 'deployed'} onClick={() => rollback(detail, h.revision)}>回滚到此版本</button></td>
                    </tr>
                  ))}
                </tbody></table>
              )}
              {detailTab === 'values' && (
                <pre className="code-block" style={{ maxHeight: '48vh', overflow: 'auto', fontSize: '0.75rem', whiteSpace: 'pre-wrap' }}>{values || '(加载中或为空)'}</pre>
              )}
            </div>
          </div>
        </div>
      )}
    </div>
  )
}

function HelmInstallModal({ clusterID, onClose, onDone }: {
  clusterID: string
  onClose: () => void
  onDone: (ok: boolean, msg: string) => void
}) {
  const [f, setF] = useState({ name: '', namespace: 'default', chart: '', repoName: '', repo: '', version: '', createNs: false })
  const [values, setValues] = useState('')
  const [busy, setBusy] = useState(false)
  const [charts, setCharts] = useState<any[]>([])
  const [searching, setSearching] = useState(false)
  const [showChartHelp, setShowChartHelp] = useState(false)
  const [penKey, setPenKey] = useState('replicaCount')
  const [penVal, setPenVal] = useState('')
  const [valRows, setValRows] = useState<{ k: string; v: string }[]>([])
  const set = (k: string, v: string | boolean) => setF((p) => ({ ...p, [k]: v }))

  // 加载已注册 repo → 构建 chart 候选列表(通过 search " " 拉取全部)
  const loadCharts = (kw = '') => {
    setSearching(true)
    getJSON<{ ok: boolean; charts: any[]; error?: string }>(`/api/plugins/containers/k8s/helm/search?cluster=${clusterID}&q=${encodeURIComponent(kw || ' ')}`)
      .then((d) => { if (d.ok) setCharts(d.charts || []) })
      .catch(() => setCharts([]))
      .finally(() => setSearching(false))
  }
  useEffect(() => { loadCharts() }, [clusterID])

  const addValRow = () => {
    if (!penKey.trim()) return
    setValRows((x) => [...x, { k: penKey.trim(), v: penVal.trim() }])
  }

  const submit = () => {
    if (!f.name.trim() || !f.chart.trim()) { onDone(false, 'Release 名与 Chart 必填'); return }
    const parsed: Record<string, string> = {}
    for (const row of valRows) { if (row.k && row.v) parsed[row.k] = row.v }
    for (const line of values.split('\n')) {
      const t = line.trim()
      if (!t || t.startsWith('#')) continue
      const idx = t.indexOf('=')
      if (idx <= 0) continue
      const k = t.slice(0, idx).trim()
      const v = t.slice(idx + 1).trim().replace(/^["']|["']$/g, '')
      if (k) parsed[k] = v
    }
    setBusy(true)
    postJSON('/api/plugins/containers/k8s/helm/install', {
      cluster: clusterID, name: f.name.trim(), namespace: f.namespace.trim() || 'default',
      chart: f.chart.trim(), version: f.version.trim(), repoName: f.repoName.trim(), repo: f.repo.trim(),
      createNamespace: f.createNs, values: parsed, timeout: 300,
    })
      .then((d: any) => onDone(d.ok, d.ok ? `✓ 已发布 ${f.name}` : '✗ ' + (d.error || '发布失败')))
      .catch((e) => onDone(false, '✗ ' + String(e)))
      .finally(() => setBusy(false))
  }

  return (
    <div className="modal-overlay" onClick={onClose} onWheel={(e) => e.stopPropagation()}>
      <div className="modal" onClick={(e) => e.stopPropagation()} style={{ maxWidth: 640 }}>
        <div className="modal-head">
          <div className="modal-title">安装/升级 Helm Release</div>
          <button className="btn-glass-soft btn-glass-soft-sm" onClick={onClose}>关闭</button>
        </div>
        <div style={{ padding: '1rem 1.25rem', display: 'grid', gap: 10, maxHeight: '68vh', overflowY: 'auto' }}>
          <div className="form-row"><label>Release 名称 *</label><input className="input" value={f.name} onChange={(e) => set('name', e.target.value)} placeholder="如 my-app" /></div>
          <div className="form-row"><label>命名空间</label><input className="input" value={f.namespace} onChange={(e) => set('namespace', e.target.value)} /></div>

          <div className="form-row">
            <label>Chart * <button className="btn-glass-soft btn-glass-soft-sm" style={{ marginLeft: 8, fontSize: '0.7rem' }} onClick={() => setShowChartHelp(!showChartHelp)}>? 格式</button></label>
            <div style={{ display: 'flex', gap: 4 }}>
              <input className="input" list="helm-charts" value={f.chart} onChange={(e) => set('chart', e.target.value)} placeholder="如 bitnami/nginx" />
              <datalist id="helm-charts">{charts.map((c) => <option key={c.name} value={String(c.name || '')} />)}</datalist>
              {searching && <span className="dim" style={{ alignSelf: 'center', fontSize: '0.7rem' }}>搜索中…</span>}
            </div>
            {showChartHelp && (
              <div className="dim" style={{ fontSize: '0.7rem', marginTop: 4 }}>
                格式: <span className="mono">repo/chart</span> (来自已注册仓库) 或 <span className="mono">path/to/chart</span>。
                左侧下拉框自动列出已注册仓库的搜索候选。
              </div>
            )}
            {charts.length > 0 && (
              <div style={{ marginTop: 6, display: 'flex', flexWrap: 'wrap', gap: 4, maxHeight: 90, overflowY: 'auto' }}>
                {charts.slice(0, 16).map((c) => (
                  <button key={c.name} className="btn-glass-soft btn-glass-soft-sm"
                    title={`${c.description || ''} · v${c.chart_version || ''}`}
                    onClick={() => { set('chart', String(c.name)); if (c.chart_version) set('version', String(c.chart_version)) }}>
                    {c.name}
                  </button>
                ))}
              </div>
            )}
          </div>

          <div className="form-row"><label>Chart 版本</label><input className="input" value={f.version} onChange={(e) => set('version', e.target.value)} placeholder="留空=最新; 点选右侧候选自动填入" /></div>
          <div className="form-row"><label>新增 Repo(可选)</label>
            <div style={{ display: 'flex', gap: 4 }} className="mono">
              <input className="input" value={f.repoName} onChange={(e) => set('repoName', e.target.value)} placeholder="名称, 如 bitnami" style={{ width: '45%' }} />
              <input className="input" value={f.repo} onChange={(e) => set('repo', e.target.value)} placeholder="URL https://charts.bitnami.com/bitnami" />
            </div>
          </div>
          <label className="chk"><input type="checkbox" checked={f.createNs} onChange={(e) => set('createNs', e.target.checked)} /> 命名空间不存在时自动创建</label>

          <div style={{ borderTop: '1px solid var(--border)', paddingTop: 10 }}>
            <div className="dim" style={{ fontSize: '0.75rem', fontWeight: 700, marginBottom: 6 }}>Values 覆写</div>
            <div className="form-row" style={{ border: 'none' }}>
              <div style={{ display: 'flex', gap: 4, width: '100%' }}>
                <input className="input mono" style={{ flex: 1 }} value={penKey} placeholder="点分键, 如 service.type" onChange={(e) => setPenKey(e.target.value)} onKeyDown={(e) => { if (e.key === 'Enter') addValRow() }} />
                <input className="input mono" style={{ flex: 1 }} value={penVal} placeholder="值, 如 NodePort" onChange={(e) => setPenVal(e.target.value)} onKeyDown={(e) => { if (e.key === 'Enter') addValRow() }} />
                <button className="btn-glass-soft btn-glass-soft-sm" onClick={addValRow}>+ 添加</button>
              </div>
            </div>
            {valRows.length > 0 && (
              <div style={{ display: 'flex', flexDirection: 'column', gap: 4, marginTop: 6 }}>
                {valRows.map((row, i) => (
                  <div key={i} style={{ display: 'flex', alignItems: 'center', gap: 4 }}>
                    <input className="input mono" style={{ flex: 1 }} value={row.k}
                      onChange={(e) => setValRows(valRows.map((r, j) => j === i ? { ...r, k: e.target.value } : r))} />
                    <input className="input mono" style={{ flex: 1 }} value={row.v}
                      onChange={(e) => setValRows(valRows.map((r, j) => j === i ? { ...r, v: e.target.value } : r))} />
                    <button className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-danger" onClick={() => setValRows(valRows.filter((_, j) => j !== i))}>✕</button>
                  </div>
                ))}
              </div>
            )}
            <details style={{ marginTop: 8 }}>
              <summary className="dim" style={{ cursor: 'pointer', fontSize: '0.7rem' }}>高级: 原始 values.yaml 覆写(每行 key=value)</summary>
              <textarea className="input mono" rows={4} style={{ marginTop: 4, width: '100%', resize: 'vertical', boxSizing: 'border-box' }} value={values}
                onChange={(e) => setValues(e.target.value)} placeholder={'replicaCount=2\nservice.type=NodePort'} />
            </details>
          </div>

          <div className="modal-actions">
            <button className="btn-glass-soft" onClick={onClose} disabled={busy}>取消</button>
            <button className="btn-glass-soft btn-glass-soft-accent" onClick={submit} disabled={busy}>{busy ? '发布中…' : '发布'}</button>
          </div>
        </div>
      </div>
    </div>
  )
}
