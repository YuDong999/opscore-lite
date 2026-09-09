import { useEffect, useMemo, useRef, useState } from 'react'
import { getJSON, postJSON } from '../api/client'
import EChart from '../charts/EChart'
import './logmonitor-kibana.css'

interface LogEntry {
  id: number
  ts: number
  level: string
  service: string
  source: string
  filePath: string
  offset: number
  size: number
  summary: string
  raw?: string
  indexId?: string
}

interface LogQueryResult {
  total: number
  items: LogEntry[]
  tookMs: number
}

interface ServiceStat {
  service: string
  count: number
  levels: Record<string, number>
}

interface LogStats {
  totalCount: number
  levelCounts: Record<string, number>
  services: ServiceStat[]
  oldest: number | null
  newest: number | null
  totalBytes: number
}

interface HistogramBucket {
  ts: number
  count: Record<string, number>
}

interface LogStatsResult {
  stats: LogStats
  histogram: HistogramBucket[]
}

interface LogSource {
  id: string
  name: string
  type: string
  path: string
  service: string
  enabled: boolean
  follow: boolean
  namespace?: string
  cluster?: string
}

interface ContainerItem {
  name: string
  image: string
  state: string
}

interface TermsBucket {
  key: string
  count: number
}

interface TermsResult {
  field: string
  buckets: TermsBucket[]
  tookMs: number
}

interface K8sPodItem {
  name: string
  namespace: string
  clusterID: string
  containers: string[]
  any?: boolean
}

interface FieldMap {
  name: string
  type: string
  indexed: boolean
}

interface IlmStage {
  retentionDays: number
  readonly: boolean
  compress: boolean
  freeze: boolean
  priority: number
}

interface IlmPolicy {
  hot: IlmStage
  warm: IlmStage
  cold: IlmStage
  delete: IlmStage
}

interface LogIndex {
  id: string
  name: string
  source: string
  sourcePath: string
  service: string
  fields: FieldMap[]
  ilm: IlmPolicy
  deleteAfter: number
  createdAt?: number
  updatedAt?: number
}

interface IndexStats {
  docCount: number
  bytes: number
  oldest?: number | null
  newest?: number | null
  storageStage: string
}

const EMPTY_ILM: IlmPolicy = {
  hot: { retentionDays: 7, readonly: false, compress: false, freeze: false, priority: 100 },
  warm: { retentionDays: 30, readonly: true, compress: true, freeze: false, priority: 50 },
  cold: { retentionDays: 90, readonly: true, compress: true, freeze: true, priority: 10 },
  delete: { retentionDays: 180, readonly: false, compress: false, freeze: false, priority: 0 },
}

const ILM_STAGES: (keyof IlmPolicy)[] = ['hot', 'warm', 'cold', 'delete']
const LEVELS = ['ERROR', 'WARN', 'INFO', 'DEBUG', 'FATAL']

const LEVEL_COLOR: Record<string, string> = {
  ERROR: '#ff4d4f',
  FATAL: '#a8071a',
  WARN: '#faad14',
  INFO: '#1677ff',
  DEBUG: '#8c8c8c',
}

function fmtTime(ts: number): string {
  if (!ts) return '-'
  const d = new Date(ts)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

function fmtBytes(n: number): string {
  if (n < 1024) return n + ' B'
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KB'
  if (n < 1024 * 1024 * 1024) return (n / 1024 / 1024).toFixed(1) + ' MB'
  return (n / 1024 / 1024 / 1024).toFixed(2) + ' GB'
}

// podSvcName 取 pod 的主容器名作为服务名(单容器通常即服务名), 供接入与展示共用
function podSvcName(p: { name: string; containers?: string[] }): string {
  const containers = Array.isArray(p.containers) ? p.containers : []
  return (containers[0] || p.name).trim()
}

// —— 字段词法: 简单 KQL 提示 ——
const FIELD_HINTS = [
  { field: 'level', op: ':', val: 'ERROR', label: 'log.level:ERROR 精确匹配错误级别' },
  { field: 'service', op: ':', val: 'order-api', label: 'service:order-api 指定服务' },
  { field: 'source', op: ':', val: 'file', label: 'source:file 按来源' },
  { field: 'message', op: ':', val: 'timeout', label: 'message:* 全文(关键字)' },
]

const FIELD_TREE = [
  { field: 'level', children: ['ERROR', 'WARN', 'INFO', 'DEBUG'] },
  { field: 'source', children: ['k8s', 'container', 'app', 'file'] },
  { field: 'service', children: null },
  { field: 'indexId', children: null },
  { field: 'message', children: null },
]

const FIELD_INFO: [string, string][] = [
  ['level', 'keyword'],
  ['service', 'keyword'],
  ['source', 'keyword'],
  ['indexId', 'keyword'],
  ['message', 'text'],
  ['timestamp', 'date'],
]

function parseKql(kql: string): { service?: string; level?: string; source?: string; indexId?: string; keyword?: string } {
  const out: { service?: string; level?: string; source?: string; indexId?: string; keyword?: string } = {}
  if (!kql) return out
  const rest: string[] = []
  for (const tok of kql.split(/\s+(and\s+)?/i)) {
    const m = /^([a-zA-Z_][a-zA-Z0-9_.]*)\s*(:|=)\s*"?([^"]*)"?$/.exec(tok.trim())
    if (m) {
      const f = m[1].toLowerCase()
      const v = m[3]
      if (f === 'service') out.service = v
      else if (f === 'level') out.level = v.toUpperCase()
      else if (f === 'source') out.source = v
      else if (f === 'indexid') out.indexId = v
      else if (f === 'message') out.keyword = v  // message 也放 keyword，用于 highlight
      else rest.push(v)
    } else if (tok.trim() && tok.trim().toLowerCase() !== 'and') {
      rest.push(tok.trim())
    }
  }
  if (rest.length) out.keyword = rest.join(' ')
  return out
}

type ToastKind = 'ok' | 'err' | 'info'
interface Toast { id: number; kind: ToastKind; text: string }

export default function LogMonitorModule() {
  const [tab, setTab] = useState<'search' | 'stats' | 'sources' | 'indexes'>('search')

  // 顶栏
  const [indexView, setIndexView] = useState('') // 数据视图/索引
  const [allIndexes, setAllIndexes] = useState<LogIndex[]>([])
  const [live, setLive] = useState(false)
  const [relativeHours, setRelativeHours] = useState(24)
  const [absStart, setAbsStart] = useState('')
  const [absEnd, setAbsEnd] = useState('')

  // 查询
  const [kql, setKql] = useState('')
  const [keyword, setKeyword] = useState('')
  const [service, setService] = useState('')
  const [level, setLevel] = useState('')
  const [source, setSource] = useState('')
  const [indexFilter, setIndexFilter] = useState('')
  const [suggestOpen, setSuggestOpen] = useState(false)
  const [suggestIdx, setSuggestIdx] = useState(0)

  // 多选字段匹配（类似 Kibana）
  const [selectedLevelValues, setSelectedLevelValues] = useState<Set<string>>(new Set())
  const [fieldTreeOpen, setFieldTreeOpen] = useState<Set<string>>(new Set()) // 展开哪些父节点
  const [terms, setTerms] = useState<Record<string, TermsBucket[]>>({}) // 真实字段聚合: field -> top buckets
  const pullingRef = useRef(0) // terms 请求批次, 防止旧响应覆盖新响应
  const [podFilter, setPodFilter] = useState('') // K8S Pod 列表命名空间过滤
  const toggleFieldTree = (f: string) => {
    setFieldTreeOpen(prev => {
      const next = new Set(prev)
      if (next.has(f)) next.delete(f)
      else next.add(f)
      return next
    })
  }
  const toggleLevelValue = (v: string) => {
    setSelectedLevelValues(prev => {
      const next = new Set(prev)
      if (next.has(v)) next.delete(v)
      else next.add(v)
      return next
    })
  }

  const [page, setPage] = useState(1)
  const pageSize = 100
  const [result, setResult] = useState<LogQueryResult | null>(null)
  const [loading, setLoading] = useState(false)
  const [err, setErr] = useState('')

  // 直方图
  const [hist, setHist] = useState<HistogramBucket[]>([])
  const [bucketMs, setBucketMs] = useState(60000) // 当前直方图分桶粒度

  // 详情抽屉
  const [detail, setDetail] = useState<LogEntry | null>(null)
  const [detailLoading, setDetailLoading] = useState(false)

  // Toasts
  const [toasts, setToasts] = useState<Toast[]>([])
  const toastId = useRef(0)

  // 统计
  const [stats, setStats] = useState<LogStatsResult | null>(null)
  const [statsService, setStatsService] = useState('')

  // 源管理
  const [sources, setSources] = useState<LogSource[]>([])

  // 索引管理
  const [indexes, setIndexes] = useState<LogIndex[]>([])
  const [idxStats, setIdxStats] = useState<Record<string, IndexStats>>({})
  const [editing, setEditing] = useState<LogIndex | null>(null)

  // 通用 modal 表单(替换 window.prompt/alert)
  const [scanOpen, setScanOpen] = useState(false)
  const [scanPath, setScanPath] = useState('')
  const [scanSvc, setScanSvc] = useState('')
  const [scanIdx, setScanIdx] = useState('')
  const [srcOpen, setSrcOpen] = useState(false)
  const [srcDraft, setSrcDraft] = useState<LogSource>({ id: '', name: '', type: 'file', path: '', service: '', enabled: true, follow: false })

  // 从已连接资源添加(容器/K8S): 选择权交给用户
  const [discContainers, setDiscContainers] = useState<ContainerItem[]>([])
  const [discClusters, setDiscClusters] = useState<string[]>([])
  const [discK8sPods, setDiscK8sPods] = useState<K8sPodItem[]>([])
  const [discOpen, setDiscOpen] = useState(false)
  const [discLoading, setDiscLoading] = useState(false)
const [selCluster, setSelCluster] = useState('1')
  const [selNamespace, setSelNamespace] = useState('')
  const [podSearch, setPodSearch] = useState('')
  const [selTargetIdx, setSelTargetIdx] = useState('')
  const [selContainers, setSelContainers] = useState<Set<string>>(new Set())
  const [selPods, setSelPods] = useState<Set<string>>(new Set())
  const [ingesting, setIngesting] = useState(false)

  const latestHist = useRef<HistogramBucket[]>([])

  function pushToast(kind: ToastKind, text: string) {
    const id = ++toastId.current
    setToasts((t) => [...t, { id, kind, text }])
    setTimeout(() => setToasts((t) => t.filter((x) => x.id !== id)), 3500)
  }

  // 计算时间范围
  function timeRange(): { startTs: number; endTs: number } {
    const now = Date.now()
    if (absStart || absEnd) {
      const startTs = absStart ? new Date(absStart).getTime() : 0
      const endTs = absEnd ? new Date(absEnd).getTime() : now
      return { startTs: isNaN(startTs) ? 0 : startTs, endTs }
    }
    return { startTs: relativeHours > 0 ? now - relativeHours * 3600 * 1000 : 0, endTs: now }
  }

  // —— 直方图时间轴 (对标 Kibana: barTarget=50, maxBars=100, 粒度阶梯取整) ——
  // boundsDescending: [下界ms, 该档粒度ms], 下界由大到小
  const BUCKET_BOUNDS: Array<[number, number]> = [
    [Infinity, 31536000000],        // >1年: 按 1 年
    [31536000000, 2592000000],      // >30天: 按 1 月
    [1814400000, 604800000],        // >3周: 按 1 周
    [604800000, 86400000],          // >1周: 按 1 天
    [86400000, 43200000],           // >24h: 按 12 小时
    [21600000, 10800000],           // >6h: 按 3 小时
    [7200000, 3600000],             // >2h: 按 1 小时
    [2700000, 1800000],             // >45m: 按 30 分钟
    [1200000, 600000],              // >20m: 按 10 分钟
    [540000, 300000],               // >9m: 按 5 分钟
    [180000, 60000],                // >3m: 按 1 分钟
    [45000, 30000],                 // >45s: 按 30 秒
    [15000, 10000],                 // >15s: 按 10 秒
    [7500, 5000],                   // >7.5s: 按 5 秒
    [5000, 1000],                   // >5s: 按 1 秒
    [500, 100],                     // 其余: 按 100ms
  ]

  // calcAutoIntervalNear: 目标 ~targetBucket 个桶, 取最接近的阶梯粒度
  function autoIntervalNear(targetBucket: number, duration: number): number {
    const per = duration / targetBucket
    for (let i = 0; i < BUCKET_BOUNDS.length; i++) {
      if (BUCKET_BOUNDS[i][0] <= per) return BUCKET_BOUNDS[Math.max(i - 1, 0)][1]
    }
    return Math.max(1, Math.floor(per))
  }

  // calcAutoIntervalLessThan: 粒度细到超出 maxBars 时按比例取整(对标 ES date_histogram 缩放)
  function autoIntervalLessThan(maxBars: number, duration: number): number {
    const per = duration / maxBars
    for (const [, iv] of BUCKET_BOUNDS) {
      if (iv <= per) return iv
    }
    return Math.max(1, Math.floor(per))
  }

  // 依据时间窗口选择直方图分桶粒度: 目标 50 桶, 桶数超过 100 时放大粒度
  function pickBucketMs(rangeMs: number): number {
    if (rangeMs <= 0) return 60000
    const barTarget = 50
    const maxBars = 100
    let iv = autoIntervalNear(barTarget, rangeMs)
    if (rangeMs / iv > maxBars) iv = autoIntervalLessThan(maxBars, rangeMs)
    return iv
  }

  // 直方图桶粒度的人类可读标签
  function bucketLabel(ms: number): string {
    if (ms % (86400 * 1000) === 0) return `每 ${ms / (86400 * 1000)} 天`
    if (ms % (3600 * 1000) === 0) return `每 ${ms / (3600 * 1000)} 小时`
    if (ms % (60 * 1000) === 0) return `每 ${ms / (60 * 1000)} 分钟`
    return `每 ${ms / 1000} 秒`
  }

  // dateFormat:scaled 分级 (对标 Kibana), 按粒度长度选时间格式
  const SCALED_FORMATS: Array<[number, string]> = [
    [31536000000, 'YYYY'],
    [86400000, 'YYYY-MM-DD'],
    [3600000, 'YYYY-MM-DD HH:mm'],
    [60000, 'HH:mm'],
    [1000, 'HH:mm:ss'],
    [0, 'HH:mm:ss.SSS'],
  ]

  // 依据粒度选择时间刻度格式 (对标 Kibana dateFormat:scaled)
  function histAxisLabel(ts: number, ms: number): string {
    const d = new Date(ts)
    const p = (n: number) => String(n).padStart(2, '0')
    const Y = String(d.getFullYear())
    const M = p(d.getMonth() + 1)
    const D = p(d.getDate())
    const H = p(d.getHours())
    const m = p(d.getMinutes())
    const s = p(d.getSeconds())
    const ms3 = String(d.getMilliseconds()).padStart(3, '0')
    const fmt = SCALED_FORMATS.find(([minMs]) => ms >= minMs)![1]
    return fmt
      .replace('YYYY', Y).replace('MM', M).replace('DD', D)
      .replace('HH', H).replace('mm', m).replace('ss', s).replace('SSS', ms3)
  }

  async function loadHistogram(params: URLSearchParams) {
    try {
      const r = await getJSON<LogStatsResult>('/api/logmonitor/stats?' + params.toString())
      latestHist.current = r.histogram || []
      setHist(r.histogram || [])
    } catch {
      /* 直方图失败不阻塞 */
    }
  }

  // 拉取真实字段聚合 (service/source/indexId/level), 供字段树叶子与建议下拉使用
  // 仅按时间窗口聚合, 不继承过滤条件, 避免点选后其他选项消失 (类似 Kibana 字段浏览)
  async function loadFieldTerms(params: URLSearchParams) {
    const batch = ++pullingRef.current
    const fields: string[] = ['service', 'source', 'level', 'indexId']
    const out: Record<string, TermsBucket[]> = {}
    await Promise.all(fields.map(async (f) => {
      try {
        const p = new URLSearchParams()
        if (params.get('startTs')) p.set('startTs', params.get('startTs')!)
        if (params.get('endTs')) p.set('endTs', params.get('endTs')!)
        p.set('field', f)
        p.set('size', '10')
        const r = await getJSON<TermsResult>('/api/logmonitor/stats/terms?' + p.toString())
        out[f] = r.buckets
      } catch { out[f] = [] }
    }))
    if (pullingRef.current === batch) setTerms(out)
  }

  async function runSearch() {
    setLoading(true)
    setErr('')
    try {
      const { startTs, endTs } = timeRange()
      const params = new URLSearchParams()
      if (service) params.set('service', service)
      if (level) params.set('level', level)
      if (source) params.set('source', source)
      if (keyword) params.set('keyword', keyword)
      if (indexFilter) params.set('indexId', indexFilter)
      if (startTs > 0) params.set('startTs', String(startTs))
      params.set('endTs', String(endTs))
      params.set('page', String(page))
      params.set('pageSize', String(pageSize))
      const q = new URLSearchParams(params.toString())
      q.delete('page')
      q.delete('pageSize')
      const bm = pickBucketMs(endTs - startTs)
      setBucketMs(bm)
      q.set('bucketMs', String(bm)) // 分桶粒度随时间窗口联动
      loadHistogram(q)
      const r = await getJSON<LogQueryResult>('/api/logmonitor/query?' + params.toString())
      setResult(r)
      loadFieldTerms(params)
    } catch (e: any) {
      setErr(e?.message || '查询失败')
    } finally {
      setLoading(false)
    }
  }

  async function loadStats(svc: string) {
    const params = new URLSearchParams()
    if (svc) params.set('service', svc)
    params.set('bucketMs', String(60000 * 5))
    try {
      const r = await getJSON<LogStatsResult>('/api/logmonitor/stats?' + params.toString())
      setStats(r)
      latestHist.current = r.histogram
    } catch (e: any) {
      setErr(e?.message || '加载统计失败')
    }
  }

  async function loadSources() {
    try {
      const r = await getJSON<LogSource[]>('/api/logmonitor/sources')
      setSources(r)
    } catch (e: any) {
      setErr(e?.message || '加载源失败')
    }
  }

  async function loadIndexList() {
    try {
      const r = await getJSON<LogIndex[]>('/api/logmonitor/indexes')
      setAllIndexes(r)
    } catch {
      /* noop */
    }
  }

  async   function toggleDiscoverPanel() {
    const next = !discOpen
    setDiscOpen(next)
    // Only load data when opening the panel (not when closing)
    if (next) {
      setDiscLoading(true)
      try {
        await Promise.all([loadDiscoverContainers(), loadDiscoverClusters(), selCluster && loadDiscoverK8s(selCluster), loadSources()])
      } finally {
        setDiscLoading(false)
      }
    }
  }

  async function loadDiscoverContainers() {
    try {
      const r = await getJSON<{ containers?: ContainerItem[] }>('/api/logmonitor/discover/containers')
      setDiscContainers(r.containers || [])
    } catch (e: any) {
      pushToast('err', e?.message || '发现容器失败')
    }
  }

  async function loadDiscoverClusters() {
    try {
      const r = await getJSON<{ clusters?: string[] }>('/api/logmonitor/discover/clusters')
      setDiscClusters(r.clusters || [])
    } catch (e: any) {
      pushToast('err', e?.message || '发现集群失败')
    }
  }

  async function loadDiscoverK8s(cluster: string) {
    if (!cluster) return
    setDiscLoading(true)
    try {
      const r = await getJSON<{ pods?: K8sPodItem[] }>(`/api/logmonitor/discover/k8s?cluster=${encodeURIComponent(cluster)}`)
      setDiscK8sPods(r.pods || [])
    } catch (e: any) {
      pushToast('err', e?.message || '发现 K8S pod 失败')
      setDiscK8sPods([])
    }
    setDiscLoading(false)
  }

  function toggleContainers(name: string) {
    const next = new Set(selContainers)
    next.has(name) ? next.delete(name) : next.add(name)
    setSelContainers(next)
  }

  function togglePods(key: string) {
    const next = new Set(selPods)
    next.has(key) ? next.delete(key) : next.add(key)
    setSelPods(next)
  }

  async function ingestSelected() {
    if (selContainers.size === 0 && selPods.size === 0) {
      pushToast('err', '请先勾选要接入的容器或 Pod')
      return
    }
    setIngesting(true)
    let ok = 0
    const fail: string[] = []
    try {
      for (const c of selContainers) {
        try {
          await postJSON('/api/logmonitor/scan', { path: c, source: 'container', service: '', indexId: selTargetIdx })
          ok++
        } catch { fail.push(c) }
      }
      for (const key of selPods) {
        const p = discK8sPods.find((x) => `${x.namespace}/${x.name}` === key)
        if (!p) continue
        try {
          await postJSON('/api/logmonitor/scan', { path: p.name, source: 'k8s', namespace: p.namespace, service: podSvcName(p) || '', cluster: p.clusterID, indexId: selTargetIdx })
          ok++
        } catch { fail.push(`${p.namespace}/${p.name}`) }
      }
      if (ok > 0) pushToast('ok', `已接入 ${ok} 个来源的日志`)
      if (fail.length > 0) pushToast('err', `${fail.length} 个接入失败: ${fail.join(', ')}`)
      setSelContainers(new Set())
      setSelPods(new Set())
      loadDiscoverContainers()
      loadDiscoverK8s(selCluster)
      loadSources()
      loadIndexList()
    } catch (e: any) {
      pushToast('err', e?.message || '接入失败')
    }
    setIngesting(false)
  }

  async function doScan() {
    if (!scanPath.trim()) {
      pushToast('err', '请输入要扫描的文件路径')
      return
    }
    try {
      const r = await postJSON('/api/logmonitor/scan', { path: scanPath, service: scanSvc, source: 'file', indexId: scanIdx })
      pushToast('ok', `扫描完成, 共入库 ${r.scanned} 条日志`)
      setScanOpen(false)
      setScanPath('')
      runSearch()
    } catch (e: any) {
      pushToast('err', '扫描失败: ' + (e?.message || ''))
    }
  }

  async function viewDetail(id: number) {
    setDetailLoading(true)
    try {
      const r = await getJSON<LogEntry>('/api/logmonitor/raw?id=' + id)
      setDetail(r)
    } catch (e: any) {
      setDetail({ id, ts: 0, level: '', service: '', source: '', filePath: '', offset: 0, size: 0, summary: '读取失败: ' + (e?.message || ''), raw: '' } as LogEntry)
    } finally {
      setDetailLoading(false)
    }
  }

  async function addSource() {
    if (!srcDraft.name || !srcDraft.path) {
      pushToast('err', '名称与路径/标识不能为空')
      return
    }
    try {
      await postJSON('/api/logmonitor/sources/save', { ...srcDraft, id: '' })
      pushToast('ok', `已新增日志源「${srcDraft.name}」`)
      setSrcOpen(false)
      setSrcDraft({ id: '', name: '', type: 'file', path: '', service: '', enabled: true, follow: false })
      loadSources()
    } catch (e: any) {
      pushToast('err', '添加失败: ' + (e?.message || ''))
    }
  }

  async function delSource(id: string, name: string) {
    try {
      await postJSON('/api/logmonitor/sources/delete', { id })
      pushToast('ok', `已删除日志源「${name}」`)
      loadSources()
    } catch (e: any) {
      pushToast('err', '删除失败: ' + (e?.message || ''))
    }
  }

  async function toggleSourceEnabled(s: LogSource) {
    try {
      await postJSON('/api/logmonitor/sources/enabled', { id: s.id, enabled: !s.enabled })
      setSources((prev) => prev.map((x) => (x.id === s.id ? { ...x, enabled: !x.enabled } : x)))
    } catch (e: any) {
      pushToast('err', '切换失败: ' + (e?.message || ''))
    }
  }

  async function loadIndexes() {
    try {
      const list = await getJSON<LogIndex[]>('/api/logmonitor/indexes')
      setIndexes(list)
      const st: Record<string, IndexStats> = {}
      await Promise.all(
        list.map(async (ix) => {
          try {
            const r = await getJSON<IndexStats>('/api/logmonitor/indexes/stats?id=' + ix.id)
            st[ix.id] = r
          } catch {
            /* noop */
          }
        })
      )
      setIdxStats(st)
    } catch (e: any) {
      setErr(e?.message || '加载索引失败')
    }
  }

  function newIndexDraft(): LogIndex {
    return {
      id: '', name: '', source: 'file', sourcePath: '', service: '', fields: [],
      ilm: {
        hot: { ...EMPTY_ILM.hot },
        warm: { ...EMPTY_ILM.warm },
        cold: { ...EMPTY_ILM.cold },
        delete: { ...EMPTY_ILM.delete },
      },
      deleteAfter: 180,
    }
  }

  function openEdit(ix?: LogIndex) {
    if (ix) {
      setEditing({
        ...ix,
        ilm: {
          hot: { ...EMPTY_ILM.hot, ...(ix.ilm?.hot || {}) },
          warm: { ...EMPTY_ILM.warm, ...(ix.ilm?.warm || {}) },
          cold: { ...EMPTY_ILM.cold, ...(ix.ilm?.cold || {}) },
          delete: { ...EMPTY_ILM.delete, ...(ix.ilm?.delete || {}) },
        },
      })
    } else {
      setEditing(newIndexDraft())
    }
  }

  async function saveIndex() {
    if (!editing) return
    if (!editing.name.trim()) {
      pushToast('err', '请填写索引名称')
      return
    }
    try {
      await postJSON('/api/logmonitor/indexes/save', editing)
      setEditing(null)
      pushToast('ok', editing.id ? '索引已更新' : '索引已创建')
      loadIndexes()
      loadIndexList()
    } catch (e: any) {
      pushToast('err', '保存失败: ' + (e?.message || ''))
    }
  }

  async function delIndex(id: string, name: string) {
    try {
      await postJSON('/api/logmonitor/indexes/delete', { id })
      pushToast('ok', `已删除索引定义「${name}」`)
      loadIndexes()
      loadIndexList()
    } catch (e: any) {
      pushToast('err', '删除失败: ' + (e?.message || ''))
    }
  }

  async function runIlm() {
    try {
      const r = await postJSON('/api/logmonitor/ilm/run', {})
      pushToast('ok', `ILM 清理完成: ${r.deleted} 条(归档索引) / ${r.deletedUnassigned} 条(未归属)`)
      runSearch()
      loadIndexes()
    } catch (e: any) {
      pushToast('err', 'ILM 执行失败: ' + (e?.message || ''))
    }
  }

  function setStageField(stage: keyof IlmPolicy, field: string, val: number | boolean) {
    if (!editing) return
    setEditing({
      ...editing,
      ilm: { ...editing.ilm, [stage]: { ...editing.ilm[stage], [field]: val } },
    })
  }

  // 数据视图切换 → 应用索引过滤
  function onIndexViewChange(v: string) {
    setIndexView(v)
    setIndexFilter(v)
    setPage(1)
  }
  // 相对时间
  function onTimeChange(v: number) {
    setRelativeHours(v)
    setPage(1)
  }

  // 同步执行查询(相对时间/索引/级别/服务/关键字变化时)
  useEffect(() => {
    if (tab === 'search') runSearch()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [indexFilter, relativeHours, level, service, source, keyword, tab])

  useEffect(() => {
    if (tab === 'search') runSearch()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [page])

  useEffect(() => {
    loadIndexList()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  useEffect(() => {
    if (tab === 'stats') loadStats('')
    if (tab === 'sources') loadSources()
    if (tab === 'indexes') loadIndexes()
  }, [tab])

  // Live: stats/sources/indexes 每 3s 轮询（避免手动刷新才看到新采集数据）
  useEffect(() => {
    if (tab !== 'stats' && tab !== 'sources' && tab !== 'indexes') return
    const t = setInterval(() => {
      if (tab === 'stats') loadStats(statsService)
      if (tab === 'sources') loadSources()
      if (tab === 'indexes') loadIndexes()
    }, 3000)
    return () => clearInterval(t)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [tab, statsService])

  // Live: 每 3s 轮询
  useEffect(() => {
    if (!live || tab !== 'search') return
    const t = setInterval(() => runSearch(), 3000)
    return () => clearInterval(t)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [live, tab, indexFilter, relativeHours, level, service, source])

  // KQL → 解析应用到筛选, 并同步到简化字段
  function applyKql(kqlOverride?: string) {
    const p = parseKql(kqlOverride ?? kql)
    setService(p.service || '')
    setLevel(p.level || '')
    setSource(p.source || '')
    setKeyword(p.keyword || '')
    if (p.indexId) {
      setIndexFilter(p.indexId)
      setIndexView(p.indexId)
    }
    setPage(1)
    setSuggestOpen(false)
  }

  // 点击字段 → 快捷过滤
  function addFieldFilter(field: string, value: string) {
    if (field === 'level') { setLevel(value); setKql(`level:${value}`) }
    else if (field === 'service') { setService(value); setKql(`service:"${value}"`) }
    else if (field === 'source') { setSource(value) }
    else if (field === 'indexId') { setIndexFilter(value); setIndexView(value) }
    setPage(1)
  }
function clearFilters() {
    setService(''); setLevel(''); setSource(''); setKeyword(''); setKql('')
    setIndexFilter(''); setIndexView('')
    setSelectedLevelValues(new Set())
    setFieldTreeOpen(new Set())
    setPage(1)
  }

  // 字段树叶子点击 → 按字段多选切换（全部字段均可多选）
  function toggleFieldTreeValue(field: string, value: string) {
    setPage(1)
    const fieldState: Record<string, string> = { level, service, source, indexId: indexFilter }
    const cur = fieldState[field] ? fieldState[field].split(',').map((x: string) => x.trim()).filter(Boolean) : []
    const hit = cur.findIndex((x: string) => x.toUpperCase() === value.toUpperCase())
    const next = hit >= 0 ? cur.filter((_, i) => i !== hit) : [...cur, value]
    const joined = next.join(',')
    if (field === 'level') setLevel(joined)
    else if (field === 'service') setService(joined)
    else if (field === 'source') setSource(joined)
    else if (field === 'indexId') { setIndexFilter(joined); setIndexView(joined) }
  }

  const treeChildren = (field: string): TermsBucket[] => {
    if (terms[field] && terms[field].length) return terms[field]
    const staticChildren: Record<string, string[]> = {
      level: [...LEVELS],
      source: ['k8s', 'container', 'app', 'file'],
    }
    return (staticChildren[field] || []).map((k) => ({ key: k, count: 0 }))
  }

  const treeChildActive = (field: string, value: string): boolean => {
    const cur = (field === 'level' ? level : field === 'service' ? service : field === 'source' ? source : field === 'indexId' ? indexFilter : '') || ''
    return cur.split(',').map((l: string) => l.trim()).some((l: string) => l.toUpperCase() === value.toUpperCase())
  }

  // 建议下拉: 真实高频 service/source 候选 + 静态辅助项
  const buildHints = () => {
    const hs: { field: string; op: string; val: string; label: string }[] = []
    const known = new Set<string>()
    for (const f of ['service', 'source']) {
      const buckets = terms[f] || []
      for (const b of buckets.slice(0, 3)) {
        if (known.has(f + ':' + b.key)) continue
        known.add(f + ':' + b.key)
        hs.push({ field: f, op: ':', val: b.key, label: `${f === 'service' ? '服务' : '来源'} 高频值 (${b.count}条)` })
      }
    }
    hs.push({ field: 'level', op: ':', val: 'ERROR', label: 'log.level:ERROR 精确匹配错误级别' })
    hs.push({ field: 'message', op: ':', val: 'timeout', label: 'message:* 全文(关键字)' })
    return hs
  }

  // 命中高亮: 支持多词, 覆盖文本中所有出现, 大小写不敏感
  function highlight(text: string, words: string[]) {
    const kws = (words || []).map(w => w.trim()).filter(w => w.length > 0)
    if (!kws.length || !text) return null
    const lower = text.toLowerCase()
    const ranges: [number, number][] = []
    for (const w of kws) {
      const kwl = w.toLowerCase()
      let from = 0
      let idx = lower.indexOf(kwl, from)
      while (idx >= 0) {
        ranges.push([idx, idx + w.length])
        from = idx + w.length
        idx = lower.indexOf(kwl, from)
      }
    }
    if (!ranges.length) return null
    ranges.sort((a, b) => a[0] - b[0])
    const merged: [number, number][] = []
    for (const r of ranges) {
      const last = merged[merged.length - 1]
      if (last && r[0] <= last[1]) last[1] = Math.max(last[1], r[1])
      else merged.push(r)
    }
    const parts: JSX.Element[] = []
    let pos = 0
    merged.forEach(([s, e], i) => {
      if (s > pos) parts.push(<span key={i + 'p'}>{text.slice(pos, s)}</span>)
      parts.push(<span key={i} className="kib-hl">{text.slice(s, e)}</span>)
      pos = e
    })
    if (pos < text.length) parts.push(<span key="t">{text.slice(pos)}</span>)
    return <>{parts}</>
  }

  // 高亮词集: 全部生效过滤词
  const highlightWords = () => {
    const ws: string[] = []
    if (keyword) ws.push(keyword)
    ;(service || '').split(',').forEach(s => s.trim() && ws.push(s))
    ;(level || '').split(',').forEach(s => s.trim() && ws.push(s))
    ;(source || '').split(',').forEach(s => s.trim() && ws.push(s))
    return ws
  }

  // ECharts 直方图配置
  const histOption = useMemo(() => {
    if (!hist.length) return null
    const sorted = [...hist].sort((a, b) => a.ts - b.ts)
    return {
      tooltip: {
        trigger: 'axis',
        confine: true,
        backgroundColor: 'rgba(30,30,40,0.9)',
        borderColor: 'rgba(255,255,255,0.15)',
        textStyle: { color: '#eee' },
        axisPointer: { type: 'shadow' },
        formatter: (ps: any) => {
          if (!Array.isArray(ps) || !ps.length) return ''
          const t = Number(ps[0].axisValue)
          const rows = ps.map((p: any) => `${p.marker}${p.seriesName}: ${Number(p.value) || 0}`).join('<br/>')
          return `${fmtTime(t)}<br/>${rows}`
        },
      },
      legend: {
        top: 0,
        textStyle: { color: 'var(--text-dim)', fontSize: 11 },
        itemWidth: 12,
        itemHeight: 8,
      },
      grid: { left: 8, right: 8, top: 28, bottom: 4, containLabel: true },
      xAxis: {
        type: 'category',
        data: sorted.map((b) => b.ts),
        axisLine: { lineStyle: { color: 'var(--border)', type: 'dashed' } },
        axisLabel: { color: 'var(--text-dim)', fontSize: 10, hideOverlap: true, formatter: (v: number) => histAxisLabel(Number(v), bucketMs) },
        splitLine: { show: false },
      },
      yAxis: {
        type: 'value',
        axisLabel: { color: 'var(--text-dim)', fontSize: 10 },
        splitLine: { lineStyle: { color: 'var(--border)', type: 'dashed' } },
      },
      series: LEVELS.map((lvl) => ({
        name: lvl,
        type: 'bar',
        stack: 'total',
        itemStyle: { color: LEVEL_COLOR[lvl] },
        data: sorted.map((b) => ({ value: b.count[lvl] || 0, itemStyle: { color: LEVEL_COLOR[lvl] } })),
        barWidth: '55%',
        animation: false,
        emphasis: { itemStyle: { shadowBlur: 4, shadowColor: 'rgba(0,0,0,0.4)' } },
      })),
    }
  }, [hist, bucketMs])

  const st = stats?.stats
  const activeFields = useMemo(() => {
    const f = new Set<string>()
    if (service) f.add('service')
    if (level) f.add('level')
    if (source) f.add('source')
    if (indexFilter) f.add('indexId')
    return f
  }, [service, level, source, indexFilter])

  const searchPlaceholder = '字段过滤如 level:ERROR service:*  或直接输入关键字，/ 显示语法提示'

  return (
    <div className="module kib-page">
      <div className="module-header">
        <h2>日志监控</h2>
      </div>

      <div className="tabs compact-tabs">
        <button className={`tab ${tab === 'search' ? 'tab-on' : ''}`} onClick={() => setTab('search')}>Discover 检索</button>
        <button className={`tab ${tab === 'stats' ? 'tab-on' : ''}`} onClick={() => setTab('stats')}>统计总览</button>
        <button className={`tab ${tab === 'sources' ? 'tab-on' : ''}`} onClick={() => setTab('sources')}>日志源管理</button>
        <button className={`tab ${tab === 'indexes' ? 'tab-on' : ''}`} onClick={() => setTab('indexes')}>索引与ILM</button>
        <span style={{ flex: 1 }} />
        <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => setScanOpen(true)}>扫描文件入库</button>
        <button className="btn-glass-soft btn-glass-soft-sm" onClick={runIlm}>执行 ILM 清理</button>
      </div>

      {/* Toasts */}
      <div className="kib-toast-wrap">
        {toasts.map((t) => (
          <div key={t.id} className={`kib-toast kib-toast-${t.kind}`} onClick={() => setToasts((x) => x.filter((y) => y.id !== t.id))}>
            {t.text}
          </div>
        ))}
      </div>

      {tab === 'search' && (
        <div className="kib">
            {/* 错误横幅 */}
            {err && <div className="banner banner-err kib-err">{err}</div>}

            {/* 三段式主体 */}
            <div className={`kib-body ${detail ? 'with-drawer' : ''}`}>
              {/* 左: 字段层级面板（类似 Kibana 字段浏览器） */}
              <aside className="kib-fields">
                <div className="kib-fields-head">
                  <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><path d="M4 7h16M4 12h16M4 17h10" /></svg>
                  可用字段
                </div>
                <div className="kib-fields-list">
                  {FIELD_TREE.map((ft) => {
                    const kids = treeChildren(ft.field)
                    const hasChildren = kids.length > 0
                    const isExpanded = fieldTreeOpen.has(ft.field)
                    return (
                      <div key={ft.field} className="kib-field-tree">
                        <div
                          className={`kib-field ${activeFields.has(ft.field) ? 'active' : ''}`}
                          onClick={() => {
                            setFieldTreeOpen(prev => {
                              const next = new Set(prev)
                              if (next.has(ft.field)) next.delete(ft.field)
                              else next.add(ft.field)
                              return next
                            })
                            if (ft.field === 'service') { setService(''); setKql('') }
                            else if (ft.field === 'indexId') { setIndexView(''); setIndexFilter('') }
                            else if (ft.field === 'message') pushToast('info', '点击文档行查看 message 全文')
                          }}
                          onKeyDown={(e) => {
                            if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); e.currentTarget.click() }
                          }}
                        >
                          <span className="kib-field-name">{ft.field}</span>
                          {hasChildren && (
                            <span
                              className="kib-field-toggle"
                              onClick={(e) => { e.stopPropagation(); toggleFieldTree(ft.field) }}
                            >
                              {isExpanded ? '▾' : '▸'}
                            </span>
                          )}
                        </div>
{hasChildren && isExpanded && (
  <div className="kib-field-children">
    {kids.map((b) => (
      <div
        key={b.key}
        className={`kib-field-child ${treeChildActive(ft.field, b.key) ? 'active' : ''}`}
        onClick={(e) => {
          e.stopPropagation()
          toggleFieldTreeValue(ft.field, b.key)
        }}
      >
        <span className="kib-field-child-check">
          {treeChildActive(ft.field, b.key) ? '✓' : '·'}
        </span>
        <span className="kib-field-child-val">{b.key}</span>
        {b.count > 0 && <span className="kib-field-child-count">{b.count}</span>}
      </div>
    ))}
  </div>
)}
                      </div>
                    )
                  })}
                </div>
                <div className="kib-fields-tip">
                  点字段名切换过滤，点子级值多选匹配（如 level:ERROR + level:WARN）
                </div>
              </aside>

{/* 中: 时间线 + 文档列表 */}
               <main className="kib-main">
                 {/* 工具栏: 数据视图 + 时间范围 + Live + 刷新 + 搜条件 */}
                 <div className="kib-topbar">
                   <div className="kib-dataview">
                     <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><ellipse cx="12" cy="5" rx="8" ry="3" /><path d="M4 5v14c0 1.7 3.6 3 8 3s8-1.3 8-3V5" /><path d="M4 12c0 1.7 3.6 3 8 3s8-1.3 8-3" /></svg>
                     <select value={indexView} onChange={(e) => onIndexViewChange(e.target.value)} title="数据视图 / 索引">
                       <option value="">所有数据</option>
                       {allIndexes.map((ix) => (
                         <option key={ix.id} value={ix.id}>{ix.name || ix.id}</option>
                       ))}
                     </select>
                   </div>

                   {absStart && absEnd ? (
                     <span className="kib-topbar-soon">自定义区间</span>
                   ) : (
                     <select className="kib-time" value={relativeHours} onChange={(e) => onTimeChange(Number(e.target.value))}>
                       <option value={1}>最近 1 小时</option>
                       <option value={6}>最近 6 小时</option>
                       <option value={24}>最近 24 小时</option>
                       <option value={72}>最近 3 天</option>
                       <option value={168}>最近 7 天</option>
                       <option value={0}>全部时间</option>
                     </select>
                   )}

                   <button className={`kib-live ${live ? 'on' : ''}`} onClick={() => setLive(!live)} title={live ? 'Live tail: 每 3 秒刷新' : '开启实时跟随'}>
                     <span className="kib-live-dot" />
                     {live ? 'LIVE' : 'Live'}
                   </button>

                   <button className="kib-btn" onClick={runSearch} disabled={loading}>
                     {loading ? '查询中…' : '刷新'}
                   </button>

                   <div className="kib-search">
                     <input
                       value={kql}
                       placeholder={searchPlaceholder}
                       onChange={(e) => { setKql(e.target.value); setSuggestOpen(true) }}
                       onFocus={() => setSuggestOpen(true)}
                       onBlur={() => setTimeout(() => setSuggestOpen(false), 150)}
                       onKeyDown={(e) => {
                         if (e.key === 'Enter') applyKql()
                         if (e.key === 'Escape') setSuggestOpen(false)
                       }}
                     />
{suggestOpen && (
                        <div className="kib-suggest">
                          {buildHints().map((h, i) => (
                            <div key={i} className={`kib-suggest-item ${i === suggestIdx ? 'sel' : ''}`} onMouseDown={() => { setKql(h.field + h.op + h.val); applyKql(h.field + h.op + h.val) }}>
                              <span className="kib-suggest-opt">{h.field}{h.op}{h.val}</span>
                              <span style={{ color: 'var(--text-dim)' }}>{h.label}</span>
                            </div>
                          ))}
                        </div>
                      )}
                   </div>
                   <button className="kib-btn kib-btn-primary" onClick={applyKql}>查询</button>
                   <button className="kib-btn kib-btn-bare" onClick={clearFilters} title="清空所有过滤">重置</button>
                 </div>

                 {/* 已应用过滤 chips */}
                {(service || level || source || keyword || indexFilter) && (
                  <div className="kib-chips">
                    {indexFilter && <span className="kib-chip" onClick={() => { setIndexFilter(''); setIndexView(''); setPage(1) }}><span className="kib-chip-key">index:</span>{allIndexes.find((ix) => ix.id === indexFilter)?.name || indexFilter} ✕</span>}
                    {service && <span className="kib-chip" onClick={() => setService('')}><span className="kib-chip-key">service:</span>{service} ✕</span>}
                    {level && <span className="kib-chip" onClick={() => setLevel('')}><span className="kib-chip-key">level:</span>{level} ✕</span>}
                    {source && <span className="kib-chip" onClick={() => setSource('')}><span className="kib-chip-key">source:</span>{source} ✕</span>}
                    {keyword && <span className="kib-chip" onClick={() => setKeyword('')}><span className="kib-chip-key">message:</span>{keyword} ✕</span>}
                  </div>
                )}

                {/* 时间线柱状图 */}
                <div className="kib-hist-card">
                  <div className="kib-hist-head">
                    <span>日志量趋势 <span className="kib-hist-total">{result ? result.total.toLocaleString() : 0}</span> 条命中 · {bucketLabel(bucketMs)}</span>
                    <span style={{ fontSize: 10 }}>级别着色</span>
                  </div>
                  {histOption ? (
                    <EChart option={histOption} height={170} />
                  ) : (
                    <div className="kib-empty">暂无直方图数据</div>
                  )}
                </div>

                {/* 文档列表 */}
                <div className="kib-docs">
                  <div className="kib-docs-head">
                    <span>命中文档列表</span>
                    {result && <span style={{ marginLeft: 'auto', fontSize: 11 }}>耗时 {result.tookMs.toFixed(1)} ms</span>}
                  </div>
                  {!result ? (
                    <div className="kib-empty">{loading ? '加载中…' : '输入条件后点击查询'}</div>
                  ) : result.items.length === 0 ? (
                    <div className="kib-empty">没有匹配的日志，试试放宽时间范围或关键字</div>
                  ) : (
                    <>
                      <div className="kib-docs-scroll">
                        <table className="kib-docs-table">
                          <colgroup>
                            <col style={{ width: '15%' }} />
                            <col style={{ width: '4%' }} />
                            <col style={{ width: '42%' }} />
                            <col style={{ width: '12%' }} />
                            <col style={{ width: '13%' }} />
                            <col style={{ width: '14%' }} />
                          </colgroup>
                          <thead>
                            <tr>
                              <th>时间</th>
                              <th>级别</th>
                              <th>日志内容</th>
                              <th>索引</th>
                              <th>来源</th>
                              <th>服务</th>
                            </tr>
                          </thead>
                          <tbody>
                            {result.items.map((e) => (
                              <tr key={e.id} className={detail?.id === e.id ? 'row-on' : ''} onClick={() => viewDetail(e.id)}>
                                <td className="kib-ts">{fmtTime(e.ts)}</td>
                                <td>
                                  <span className="kib-lvl" style={{ color: LEVEL_COLOR[e.level] || '#888', background: (LEVEL_COLOR[e.level] || '#888') + '22' }}>{e.level || '-'}</span>
                                </td>
                                <td className="kib-docs-sum" title={e.summary}>
                                  {highlight(e.summary, highlightWords()) || e.summary}
                                </td>
                                <td>
                                  {e.indexId ? (
                                    <span className="kib-idx" title={`索引 ${e.indexId}`} onClick={(ev) => { ev.stopPropagation(); addFieldFilter('indexId', e.indexId) }}>
                                      {allIndexes.find((ix) => ix.id === e.indexId)?.name || e.indexId}
                                    </span>
                                  ) : (
                                    <span style={{ color: 'var(--text-dim)' }}>未归档</span>
                                  )}
                                </td>
                                <td className="kib-docs-src">{e.source || '-'}</td>
                                <td className="kib-docs-svc" title={e.service || ''} onClick={(ev) => e.service && (ev.stopPropagation(), addFieldFilter('service', e.service))}>{e.service || '-'}</td>
                              </tr>
                            ))}
                          </tbody>
                        </table>
                      </div>
                      {result.total > pageSize && (
                        <div className="kib-pager">
                          <button className="kib-btn kib-btn-bare" disabled={page <= 1} onClick={() => setPage(page - 1)}>← 上一页</button>
                          <span>第 {page} / {Math.ceil(result.total / pageSize)} 页 · 共 {result.total.toLocaleString()} 条</span>
                          <button className="kib-btn kib-btn-bare" disabled={page * pageSize >= result.total} onClick={() => setPage(page + 1)}>下一页 →</button>
                        </div>
                      )}
                    </>
                  )}
                </div>
              </main>

              {/* 右: 详情抽屉(点行浮出) */}
              {detail && (
                <div className="kib-drawer-mask" onClick={() => setDetail(null)}>
                  <div className="kib-drawer" onClick={(e) => e.stopPropagation()}>
                    <div className="kib-drawer-head">
                      <span className="kib-drawer-title">日志 #{detail.id}</span>
                      <span className="kib-lvl" style={{ color: LEVEL_COLOR[detail.level] || '#888', background: (LEVEL_COLOR[detail.level] || '#888') + '22' }}>{detail.level || '-'}</span>
                      <span style={{ flex: 1 }} />
                      <button className="kib-btn kib-btn-bare" onClick={() => setDetail(null)} title="关闭">
                        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><path d="M18 6L6 18M6 6l12 12" /></svg>
                      </button>
                    </div>
                    <div className="kib-drawer-body">
                      {detailLoading && <div className="kib-empty">加载中…</div>}
                      {!detailLoading && detail.ts > 0 && (
                        <div className="kib-field-card">
                          <div className="kib-field-card-row"><span className="kib-fc-name">timestamp</span><span className="kib-fc-val">{fmtTime(detail.ts)}</span></div>
                          <div className="kib-field-card-row"><span className="kib-fc-name">level</span><span className="kib-fc-val">{detail.level}</span></div>
                          <div className="kib-field-card-row"><span className="kib-fc-name">service</span><span className="kib-fc-val">{detail.service || '-'}</span></div>
                          <div className="kib-field-card-row"><span className="kib-fc-name">source</span><span className="kib-fc-val">{detail.source || '-'}</span></div>
                          <div className="kib-field-card-row"><span className="kib-fc-name">filePath</span><span className="kib-fc-val">{detail.filePath || '-'}</span></div>
                          <div className="kib-field-card-row"><span className="kib-fc-name">indexId</span><span className="kib-fc-val">{detail.indexId || '(未归档)'}</span></div>
                          <div className="kib-field-card-row"><span className="kib-fc-name">size</span><span className="kib-fc-val">{fmtBytes(detail.size)}</span></div>
                        </div>
                      )}
                      <div className="kib-drawer-actions">
                        {(detail.filePath === 'http-ingest' || detail.ts > 0) && (
                          <button className="kib-chip" onClick={() => detail.indexId && addFieldFilter('indexId', detail.indexId)}><span className="kib-chip-key">过滤:</span> 同索引</button>
                        )}
                      </div>
                      <div style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 12, color: 'var(--text-dim)' }}>
                        原文
                        {detail.indexId && <span className="kib-idx" title="来自 zstd 归档">archive</span>}
                      </div>
                      <pre className="kib-raw-pre">{highlight(detail.raw || detail.summary || '', highlightWords()) || detail.raw || detail.summary || '无内容'}</pre>
                    </div>
                  </div>
                </div>
              )}
          </div>
        </div>
      )}

      {tab === 'sources' && (
        <div className="glass log-card">
          <div className="log-filter-row">
            <button className="btn-glass btn-sm" onClick={() => setSrcOpen(true)}>新增日志源</button>
          </div>
          {sources.length === 0 ? (
            <div className="log-empty">暂无日志源。可添加文件/容器/syslog 源, 或在检索页直接"扫描文件入库"</div>
          ) : (
            <table className="log-table">
              <thead>
                <tr>
                  <th>名称</th>
                  <th>类型</th>
                  <th>路径/标识</th>
                  <th>服务</th>
                  <th>状态</th>
                  <th style={{ width: 170 }}>操作</th>
                </tr>
              </thead>
              <tbody>
                {sources.map((s) => (
                  <tr key={s.id}>
                    <td>{s.name}</td>
                    <td>{s.type}</td>
                    <td className="log-mono">{s.path || '-'}</td>
                    <td>{s.service || '-'}</td>
                    <td>
                      <span className={`dot ${s.enabled ? 'dot-ok' : 'dot-off'}`} /> {s.enabled ? '启用' : '停用'}
                    </td>
                    <td>
                      <span
                        className={`kib-switch ${s.enabled ? 'on' : ''}`}
                        title={s.enabled ? '停用采集(暂停接收日志)' : '启用采集'}
                        onClick={() => toggleSourceEnabled(s)}
                      >
                        <i />
                      </span>
                      <button className="btn-glass-soft btn-glass-soft-danger btn-glass-soft-sm" onClick={() => delSource(s.id, s.name)}>删除</button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}

          {/* 从已连接资源添加: 选择权交给用户 */}
          {!discOpen ? (
            <div className="log-filter-row" style={{ marginTop: 12 }}>
              <button
              className="btn-glass btn-sm"
              onClick={toggleDiscoverPanel}
              style={{ minWidth: '120px', padding: '6px 16px' }} // 调整宽度和内边距
              title={discOpen ? '隐藏已连接资源面板' : '从已连接资源添加'}
            >
              从已连接资源添加
            </button>
              <span style={{ marginLeft: 10, color: 'var(--text-dim)', fontSize: 12 }}>选择接入本机容器 / 已连接 K8S 集群的日志</span>
            </div>
          ) : (
            <div className="kib-discover-panel" style={{ marginTop: 16 }}>
              <div className="log-filter-row">
                <div style={{ flex: 1 }}>
                  <span style={{ color: 'var(--text)', fontWeight: 600, fontSize: 14 }}>从已连接资源接入日志</span>
                  {sources.length > 0 && <span className="kib-badge" style={{ marginLeft: 8 }}>{sources.length} 个日志源</span>}
                  <div style={{ color: 'var(--text-dim)', fontSize: 12, marginTop: 3 }}>勾选下方容器 / Pod, 点击"接入"即可扫其 stdout 日志入库; 选择归档索引可双写。</div>
                  {sources.length === 0 && !discLoading && <div style={{ color: '#e5484d', fontSize: 12, marginTop: 4 }}>⚠ 日志源列表加载失败/为空, 下方√ 状态不可用, 请检查服务端 /api/logmonitor/sources</div>}
                </div>
                <button className="btn-glass btn-sm" onClick={toggleDiscoverPanel} disabled={discLoading}>收起</button>
              </div>
              <div className="kib-form-row" style={{ marginTop: 10 }}>
                <label>归属索引(可选, 双写到归档)</label>
                <select value={selTargetIdx} onChange={(e) => setSelTargetIdx(e.target.value)}>
                  <option value="">未归属</option>
                  {allIndexes.map((ix) => (
                    <option key={ix.id} value={ix.id}>{ix.name || ix.id}</option>
                  ))}
                </select>
              </div>
              {discLoading && <div className="log-empty">正在发现已连接资源…</div>}
              <div className="kib-form-row" style={{ marginTop: 8 }}>
                <h4 style={{ margin: 0, color: 'var(--text)' }}>本机容器 (docker/podman)</h4>
                {discContainers.length > 0 && <span className="kib-badge">{discContainers.length} 个</span>}
                {discContainers.length > 0 && <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => setSelContainers(new Set(discContainers.map((c) => c.name)))}>全选</button>}
              </div>
              {(() => {
                const ghosts = sources.filter((s) => s.type === 'container' && !discContainers.some((c) => c.name === s.path))
                  .map((s) => ({ name: s.path, image: '已停止 / 不在当前 docker', state: 'ghost' as string }))
                const all = [...discContainers, ...ghosts]
                const maxName = Math.max(...all.map((c) => c.name.length))
                const maxImage = Math.max(...all.map((c) => (c.image || '—').length))
                return (discContainers.length === 0 && ghosts.length === 0) ? (
                  <div className="log-empty">未发现本机容器(需 docker/podman 运行在同机)</div>
                ) : (
                <div className="kib-check-list">
                  {all.map((c) => {
                    const joined = sources.some((s) => s.type === 'container' && s.path === c.name && s.enabled)
                    const ghost = c.state === 'ghost'
                    return (
                      <label key={c.name} className={`kib-check-item${ghost ? ' kib-ghost' : ''}`} style={{ display: 'grid', gridTemplateColumns: `18px ${maxName}ch minmax(0,${maxImage}ch) 78px 20px`, alignItems: 'center', gap: 6 }}>
                        <input type="checkbox" disabled={ghost} checked={!ghost && selContainers.has(c.name)} onChange={() => toggleContainers(c.name)} />
                        <code style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{c.name}</code>
                        <span className="log-mono" style={{ color: 'var(--text-dim)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{c.image || '—'}</span>
                        <span style={{ whiteSpace: 'nowrap' }}><span className={`dot ${ghost ? 'dot-off' : c.state === 'running' ? 'dot-ok' : 'dot-off'}`} />{ghost ? '已停止' : c.state}</span>
                        {joined && <span className="kib-joined" title="已接入">✓</span>}
                      </label>
                    )
                  })}
                </div>
                )
              })()}
<div className="kib-form-row" style={{ marginTop: 12 }}>
  <h4 style={{ margin: 0, color: 'var(--text)' }}>K8S Pod</h4>
  {discClusters.length > 0 && (
    <div style={{ display: 'flex', alignItems: 'center', gap: 4, flexWrap: 'nowrap' }}>
      <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => {
        const filtered = discK8sPods.filter((p) => {
          if (!podSearch && !selNamespace) return true
          const hay = `${p.namespace}/${p.name}`.toLowerCase()
          const q = podSearch.toLowerCase()
          const matchSearch = !podSearch || hay.includes(q) || p.namespace.toLowerCase().includes(q) || p.name.toLowerCase().includes(q)
          const matchNs = !selNamespace || p.namespace === selNamespace
          return matchNs && matchSearch
        })
        setSelPods(new Set(filtered.map((p) => `${p.namespace}/${p.name}`)))
      }}>全选</button>
      <select value={selCluster} onChange={(e) => { setSelCluster(e.target.value); loadDiscoverK8s(e.target.value); setSelPods(new Set()); setSelNamespace(''); setPodSearch(''); }}>
        <option value="">选择集群…</option>
        {discClusters.map((c) => <option key={c} value={c}>集群 {c}</option>)}
      </select>
      <select value={selNamespace} onChange={(e) => setSelNamespace(e.target.value)}>
        <option value="">所有命名空间</option>
        {[...new Set(discK8sPods.map((p) => p.namespace))].sort().map((ns) => (
          <option key={ns} value={ns}>{ns}</option>
        ))}
      </select>
      <input
        placeholder="搜索 Pod 名 / 命名空间"
        value={podSearch}
        onChange={(e) => setPodSearch(e.target.value)}
        style={{ flex: 1, minWidth: 180, padding: '4px 8px', fontSize: 12, background: 'var(--bg-soft)', border: '1px solid var(--border)', borderRadius: 6 }}
      />
    </div>
  )}
</div>
{(() => {
                const shown = discK8sPods.filter((p) => {
                    if (!podSearch && !selNamespace) return true
                    const hay = `${p.namespace}/${p.name}`.toLowerCase()
                    const q = podSearch.toLowerCase()
                    const matchSearch = !podSearch || hay.includes(q) || p.namespace.toLowerCase().includes(q) || p.name.toLowerCase().includes(q)
                    const matchNs = !selNamespace || p.namespace === selNamespace
                    return matchNs && matchSearch
                  })
                function contStr(p: { containers?: unknown }): string {
                  const a = Array.isArray(p.containers) ? p.containers : (typeof p.containers === 'string' ? (p.containers as string).split(',').map((s) => s.trim()).filter(Boolean) : [])
                  return a.join(', ')
                }
                const maxName = Math.max(...shown.map((p) => (p.namespace + '/' + p.name).length))
                const maxCont = Math.max(...shown.map((p) => contStr(p).length + (Array.isArray(p.containers) && (p.containers as unknown[]).length > 1 ? 3 : 0)))
                return discClusters.length === 0 ? (
                  <div className="log-empty">无已连接集群</div>
                ) : shown.length === 0 ? (
                  <div className="log-empty">该集群未发现 pod</div>
                ) : (
                  <div className="kib-check-list">
                    {shown.map((p) => {
                      const key = `${p.namespace}/${p.name}`
                      const containers = Array.isArray(p.containers) ? p.containers : (typeof p.containers === 'string' ? (p.containers as unknown as string).split(',').map((s) => s.trim()).filter(Boolean) : [])
                      const cls = containers.length > 1 ? 'kib-multi' : ''
                      const joined = sources.some((s) => s.type === 'k8s' && s.path === p.name && s.namespace === p.namespace && s.enabled)
                      return (
                        <label key={key} className="kib-check-item" style={{ display: 'grid', gridTemplateColumns: `18px ${maxName}ch minmax(0,${maxCont}ch) 20px`, alignItems: 'center', gap: 6 }}>
                          <input type="checkbox" checked={selPods.has(key)} onChange={() => togglePods(key)} />
                          <code style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{p.namespace}/{p.name}</code>
                          <span className="log-mono" style={{ color: 'var(--text-dim)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                            {containers.length > 1 && <span className={`kib-badge ${cls}`}>{containers.length}</span>}
                            {containers.join(', ') || '—'}
                          </span>
                          {joined && <span className="kib-joined" title="已接入">✓</span>}
                        </label>
                      )
                    })}
                  </div>
                )
              })()}
<div className="log-filter-row" style={{ marginTop: 14 }}>
  <span style={{ color: 'var(--text-dim)', fontSize: 12 }}>
    已勾选 {selContainers.size} 容器 / {selPods.size} Pod
  </span>
  <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => { setSelContainers(new Set()); setSelPods(new Set()) }}>清空勾选</button>
  <button className="btn-glass btn-sm" onClick={ingestSelected} disabled={ingesting || (selContainers.size === 0 && selPods.size === 0)}>
    {ingesting ? '接入中…' : '接入勾选日志'}
  </button>
</div>
            </div>
          )}
        </div>
      )}

      {tab === 'stats' && (
        <div className="glass log-card">
          <div className="log-filter-row">
            <span style={{ marginRight: 8 }}>服务过滤:</span>
            <input
              className="input log-input log-svc-filter"
              placeholder="输入服务名过滤"
              value={statsService}
              onChange={(e) => setStatsService(e.target.value)}
            />
            <button className="btn-glass btn-sm" onClick={() => loadStats(statsService)} disabled={loading}>
              {loading ? '加载中...' : '重新统计'}
            </button>
          </div>

          {st && (
            <>
              <div className="log-stat-grid">
                <div className="log-stat-box">
                  <div className="log-stat-num">{st.totalCount.toLocaleString()}</div>
                  <div className="log-stat-label">日志总数</div>
                </div>
                <div className="log-stat-box">
                  <div className="log-stat-num">{fmtBytes(st.totalBytes)}</div>
                  <div className="log-stat-label">索引字节</div>
                </div>
                <div className="log-stat-box">
                  <div className="log-stat-num">{st.oldest ? fmtTime(st.oldest) : '-'}</div>
                  <div className="log-stat-label">最早时间</div>
                </div>
                <div className="log-stat-box">
                  <div className="log-stat-num">{st.newest ? fmtTime(st.newest) : '-'}</div>
                  <div className="log-stat-label">最新时间</div>
                </div>
              </div>

              {Object.keys(st.levelCounts).length > 0 && (
                <div className="log-level-bars">
                  {Object.entries(st.levelCounts).map(([lvl, cnt]) => (
                    <div key={lvl} className="log-level-bar-row">
                      <span className="log-level-bar-lbl" style={{ color: LEVEL_COLOR[lvl] || '#888' }}>{lvl}</span>
                      <div className="log-level-bar-track">
                        <div className="log-level-bar-fill" style={{ width: (cnt / st.totalCount) * 100 + '%', backgroundColor: LEVEL_COLOR[lvl] || '#888' }} />
                      </div>
                      <span className="log-level-bar-num">{cnt.toLocaleString()}</span>
                    </div>
                  ))}
                </div>
              )}

              <div className="log-hist-wrap">
                <div className="log-hist-title">日志量趋势 (每 5 分钟)</div>
                <div className="kib-hist-card">
                  {histOption ? (
                    <EChart option={{ ...histOption, xAxis: { ...histOption.xAxis, axisLabel: { ...histOption.xAxis.axisLabel, formatter: (v: number) => fmtTime(Number(v)).slice(5, 16) } } }} height={160} />
                  ) : (
                    <div className="kib-empty">暂无数据</div>
                  )}
                </div>
              </div>

              {st.services.length > 0 && (
                <div className="log-services">
                  <div className="log-section-title">各服务日志量 Top</div>
                  <table className="log-table log-table-sm">
                    <thead>
                      <tr>
                        <th>服务</th>
                        <th>总条数</th>
                        {LEVELS.map((l) => (
                          <th key={l}>{l}</th>
                        ))}
                      </tr>
                    </thead>
                    <tbody>
                      {st.services.slice(0, 10).map((s) => (
                        <tr key={s.service}>
                          <td className="log-svc">{s.service || '(未标注)'}</td>
                          <td>{s.count.toLocaleString()}</td>
                          {LEVELS.map((l) => (
                            <td key={l} style={{ color: (s.levels || {})[l] ? LEVEL_COLOR[l] : undefined }}>
                              {((s.levels || {})[l] || 0).toLocaleString()}
                            </td>
                          ))}
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </>
          )}
        </div>
      )}

      {tab === 'indexes' && !editing && (
        <div className="glass log-card">
          <div className="log-filter-row">
            <button className="btn-glass btn-sm" onClick={() => openEdit()}>新增索引</button>
            <span style={{ marginLeft: 12, color: 'var(--text-dim)', fontSize: 12 }}>
              索引定义纳入 ILM 冷热归档策略, 每次"执行 ILM 清理"按保留期淘汰到期日志
            </span>
          </div>
          {indexes.length === 0 ? (
            <div className="log-empty">暂无索引定义。创建索引以启用 ILM 冷热归档策略。</div>
          ) : (
            <table className="log-table">
              <thead>
                <tr>
                  <th>名称</th>
                  <th>来源</th>
                  <th>服务</th>
                  <th>文档数</th>
                  <th>字节</th>
                  <th>阶段</th>
                  <th>保留(天)</th>
                  <th style={{ width: 160, whiteSpace: 'nowrap' }}>操作</th>
                </tr>
              </thead>
              <tbody>
                {indexes.map((ix) => {
                  const st = idxStats[ix.id]
                  return (
                    <tr key={ix.id}>
                      <td><strong>{ix.name}</strong></td>
                      <td>{ix.source}{ix.sourcePath ? `(${ix.sourcePath})` : ''}</td>
                      <td>{ix.service || '-'}</td>
                      <td>{st?.docCount?.toLocaleString() ?? '-'}</td>
                      <td>{st?.bytes ? fmtBytes(st.bytes) : '-'}</td>
                      <td><span className="log-level" style={{ color: '#1677ff' }}>{st?.storageStage || 'hot'}</span></td>
                      <td>{ix.deleteAfter || ix.ilm?.delete?.retentionDays || '-'}</td>
                      <td style={{ display: 'flex', gap: 6, alignItems: 'center' }}>
                        <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => openEdit(ix)}>编辑</button>
                        <button className="btn-glass-soft btn-glass-soft-danger btn-glass-soft-sm" onClick={() => delIndex(ix.id, ix.name)}>删除</button>
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          )}
        </div>
      )}

      {tab === 'indexes' && editing && (
        <div className="glass log-card">
          <div className="log-filter-row">
            <strong style={{ marginRight: 8 }}>{editing.id ? '编辑索引' : '新增索引'}</strong>
            {editing.id && <span style={{ color: 'var(--text-dim)', fontSize: 12 }}>{editing.id}</span>}
            <span style={{ flex: 1 }} />
            <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => setEditing(null)}>取消</button>
            <button className="btn-glass btn-sm" onClick={saveIndex}>保存</button>
          </div>

          <div className="log-filter-row" style={{ flexWrap: 'wrap', gap: 8 }}>
            <input className="input log-input" placeholder="索引名称 *" value={editing.name} onChange={(e) => setEditing({ ...editing, name: e.target.value })} />
            <select className="input log-input" value={editing.source} onChange={(e) => setEditing({ ...editing, source: e.target.value })}>
              <option value="file">file</option>
              <option value="syslog">syslog</option>
              <option value="journal">journal</option>
              <option value="http">http</option>
              <option value="loki">loki</option>
              <option value="fluentbit">fluentbit</option>
              <option value="es">es</option>
            </select>
            <input className="input log-input" placeholder="采集路径/地址" value={editing.sourcePath} onChange={(e) => setEditing({ ...editing, sourcePath: e.target.value })} />
            <input className="input log-input" placeholder="归属服务" value={editing.service} onChange={(e) => setEditing({ ...editing, service: e.target.value })} />
            <input className="input log-input log-hours" type="number" placeholder="总保留(天)" value={editing.deleteAfter} onChange={(e) => setEditing({ ...editing, deleteAfter: Number(e.target.value) })} />
          </div>

          <div className="log-section-title" style={{ margin: '12px 0 8px' }}>ILM 冷热归档策略</div>
          <table className="log-table log-table-sm">
            <thead>
              <tr>
                <th>阶段</th>
                <th>保留(天)</th>
                <th>只读</th>
                <th>压缩</th>
                <th>冻结</th>
                <th>优先级</th>
              </tr>
            </thead>
            <tbody>
              {ILM_STAGES.map((sg) => (
                <tr key={sg}>
                  <td><span className="log-level" style={{ color: sg === 'hot' ? '#ff4d4f' : sg === 'warm' ? '#faad14' : sg === 'cold' ? '#1677ff' : '#8c8c8c' }}>{sg.toUpperCase()}</span></td>
                  <td>
                    <input className="input log-input" type="number" value={editing.ilm[sg].retentionDays} onChange={(e) => setStageField(sg, 'retentionDays', Number(e.target.value))} />
                  </td>
                  <td><input type="checkbox" checked={editing.ilm[sg].readonly} onChange={(e) => setStageField(sg, 'readonly', e.target.checked)} /></td>
                  <td><input type="checkbox" checked={editing.ilm[sg].compress} onChange={(e) => setStageField(sg, 'compress', e.target.checked)} /></td>
                  <td><input type="checkbox" checked={editing.ilm[sg].freeze} onChange={(e) => setStageField(sg, 'freeze', e.target.checked)} /></td>
                  <td><input className="input log-input" type="number" value={editing.ilm[sg].priority} onChange={(e) => setStageField(sg, 'priority', Number(e.target.value))} /></td>
                </tr>
              ))}
            </tbody>
          </table>

          <div className="log-section-title" style={{ margin: '12px 0 8px' }}>字段映射</div>
          <div className="log-filter-row" style={{ flexWrap: 'wrap', gap: 8 }}>
            {editing.fields.map((f, i) => (
              <div key={i} style={{ display: 'flex', gap: 6, alignItems: 'center', padding: 4 }}>
                <input className="input log-input" style={{ width: 130 }} placeholder="字段名" value={f.name} onChange={(e) => {
                  const fields = [...editing.fields]
                  fields[i] = { ...f, name: e.target.value }
                  setEditing({ ...editing, fields })
                }} />
                <select className="input log-input" style={{ width: 110 }} value={f.type} onChange={(e) => {
                  const fields = [...editing.fields]
                  fields[i] = { ...f, type: e.target.value }
                  setEditing({ ...editing, fields })
                }}>
                  <option value="text">text</option>
                  <option value="keyword">keyword</option>
                  <option value="date">date</option>
                  <option value="integer">integer</option>
                  <option value="float">float</option>
                  <option value="boolean">boolean</option>
                </select>
                <label style={{ fontSize: 12 }}><input type="checkbox" checked={f.indexed} onChange={(e) => {
                  const fields = [...editing.fields]
                  fields[i] = { ...f, indexed: e.target.checked }
                  setEditing({ ...editing, fields })
                }} /> 索引</label>
                <button className="btn-glass-soft btn-glass-soft-danger btn-glass-soft-sm" onClick={() => setEditing({ ...editing, fields: editing.fields.filter((_, j) => j !== i) })}>删</button>
              </div>
            ))}
          </div>
          <button className="btn-glass-soft btn-glass-soft-sm" style={{ marginTop: 8 }} onClick={() => setEditing({ ...editing, fields: [...editing.fields, { name: '', type: 'text', indexed: true }] })}>+ 添加字段</button>
        </div>
      )}

      {/* 通用表单: 扫描入库 (替换 window.prompt) */}
      {scanOpen && (
        <div className="kib-modal-mask" onClick={() => setScanOpen(false)}>
          <div className="kib-modal" onClick={(e) => e.stopPropagation()}>
            <h3>扫描文件入库</h3>
            <div className="kib-inline-form">
              <div className="kib-form-row">
                <label>日志文件绝对路径 *</label>
                <input value={scanPath} onChange={(e) => setScanPath(e.target.value)} placeholder="/var/log/syslog" />
              </div>
              <div className="kib-form-row">
                <label>归属服务</label>
                <input value={scanSvc} onChange={(e) => setScanSvc(e.target.value)} placeholder="留空自动提取" />
              </div>
              <div className="kib-form-row">
                <label>归属索引(可选, 双写到归档)</label>
                <select value={scanIdx} onChange={(e) => setScanIdx(e.target.value)}>
                  <option value="">未归属</option>
                  {allIndexes.map((ix) => (
                    <option key={ix.id} value={ix.id}>{ix.name || ix.id}</option>
                  ))}
                </select>
              </div>
            </div>
            <div className="kib-modal-actions">
              <button className="kib-btn kib-btn-bare" onClick={() => setScanOpen(false)}>取消</button>
              <button className="kib-btn kib-btn-primary" onClick={doScan}>开始扫描</button>
            </div>
          </div>
        </div>
      )}

      {/* 通用表单: 新增日志源 */}
      {srcOpen && (
        <div className="kib-modal-mask" onClick={() => setSrcOpen(false)}>
          <div className="kib-modal" onClick={(e) => e.stopPropagation()}>
            <h3>新增日志源</h3>
            <div className="kib-inline-form">
              <div className="kib-form-row">
                <label>名称 *</label>
                <input value={srcDraft.name} onChange={(e) => setSrcDraft({ ...srcDraft, name: e.target.value })} placeholder="如 order-api" />
              </div>
              <div className="kib-form-row">
                <label>类型</label>
                <select value={srcDraft.type} onChange={(e) => setSrcDraft({ ...srcDraft, type: e.target.value })}>
                  <option value="file">file</option>
                  <option value="syslog">syslog</option>
                  <option value="journal">journal</option>
                  <option value="http">http</option>
                  <option value="container">container</option>
                </select>
              </div>
              <div className="kib-form-row">
                <label>文件路径 / 容器名 / URL *</label>
                <input value={srcDraft.path} onChange={(e) => setSrcDraft({ ...srcDraft, path: e.target.value })} />
              </div>
              <div className="kib-form-row">
                <label>所属服务</label>
                <input value={srcDraft.service} onChange={(e) => setSrcDraft({ ...srcDraft, service: e.target.value })} />
              </div>
            </div>
            <div className="kib-modal-actions">
              <button className="kib-btn kib-btn-bare" onClick={() => setSrcOpen(false)}>取消</button>
              <button className="kib-btn kib-btn-primary" onClick={addSource}>保存</button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}