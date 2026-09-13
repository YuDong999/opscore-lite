import { useEffect, useMemo, useRef, useState } from 'react'
import { getJSON, postJSON } from '../api/client'
import { useTheme } from '../theme'
import { useHost } from '../components/HostContext'
import HostSelector from '../components/HostSelector'
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

interface ParserRule {
  name: string
  match: { sources?: string[]; services?: string[]; files?: string[] }
  time: { regex?: string; formats?: string[]; timezone?: string }
  level: { regex?: string; default?: string }
  service: { regex?: string; normalize?: boolean }
  summary?: { maxLen?: number }
}

interface ParserTestResult {
  line: string
  ts: number
  level: string
  service: string
  summary: string
  error?: string
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

// ── 告警(字段对齐 internal/logmonitor/model.go) ──
interface AlertRuleRow {
  id: string
  name: string
  enabled: boolean
  condition: string   // level=ERROR / service=order-api
  countThresh: number
  windowMs: number
  cooldownMs: number
  channels: string[]
  state?: string      // firing/ok
  lastFired?: number
}

interface AlertChannelRow {
  id: string
  name: string
  type: string
  url: string
  method: string
  headers: string
  enabled: boolean
}

interface AlertEventRow {
  id: string
  ruleId: string
  ruleName: string
  level: string
  service: string
  count: number
  firedAt: number
  resolvedAt?: number
  status: string      // firing/resolved
}

interface RuleDraft {
  id: string
  name: string
  enabled: boolean
  condField: 'level' | 'service'
  condValue: string
  countThresh: number
  windowMs: number
  cooldownMs: number
  channels: string[]
}

interface ChanDraft {
  id: string
  name: string
  type: string
  url: string
  method: string
  headers: string
  enabled: boolean
}

const ILM_STAGES: (keyof IlmPolicy)[] = ['hot', 'warm', 'cold', 'delete']
const LEVELS = ['ERROR', 'WARN', 'INFO', 'DEBUG', 'FATAL']

// 级别/阶段色定义在各主题的 --lvl-* 中(浅色加深/暗色提亮, 文字对底色均≥4.5:1)
const LEVEL_VAR: Record<string, string> = {
  ERROR: '--lvl-error',
  FATAL: '--lvl-fatal',
  WARN: '--lvl-warn',
  INFO: '--lvl-info',
  DEBUG: '--lvl-debug',
}

const cssVar = (name: string) => getComputedStyle(document.documentElement).getPropertyValue(name).trim()
const levelColor = (lvl: string) => cssVar(LEVEL_VAR[lvl] || '--lvl-debug')
const STAGE_VAR: Record<string, string> = { hot: '--lvl-error', warm: '--lvl-warn', cold: '--lvl-info', delete: '--lvl-debug' }
const stageColor = (sg: string) => cssVar(STAGE_VAR[sg] || '--lvl-debug')

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
  const { theme } = useTheme()
  const [tab, setTab] = useState<'search' | 'stats' | 'sources' | 'indexes' | 'alerts'>('search')

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
  const { selected: discHost } = useHost()
  const [discLoading, setDiscLoading] = useState(false)
const [selCluster, setSelCluster] = useState('1')
  const [selNamespace, setSelNamespace] = useState('')
  const [podSearch, setPodSearch] = useState('')
  const [selTargetIdx, setSelTargetIdx] = useState('')
  const [selContainers, setSelContainers] = useState<Set<string>>(new Set())
  // 目标主机/集群切换时, 发现面板开着就自动重新发现(跟随全局主机上下文)
  useEffect(() => {
    if (!discOpen) return
    setDiscLoading(true)
    Promise.all([loadDiscoverContainers(), loadDiscoverClusters(), selCluster && loadDiscoverK8s(selCluster), loadSources()])
      .finally(() => setDiscLoading(false))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [discHost?.id, selCluster])
  const [selPods, setSelPods] = useState<Set<string>>(new Set())
  const [ingesting, setIngesting] = useState(false)

  // 解析规则(parsers.json) 可视化编辑 + 测试
  const [parserOpen, setParserOpen] = useState(false)
  const [parserBuiltin, setParserBuiltin] = useState(true)
  const [parserErr, setParserErr] = useState('')
  const [parserJson, setParserJson] = useState('')
  const [parserSample, setParserSample] = useState('')
  const [parserTestIdx, setParserTestIdx] = useState(0)
  const [parserResults, setParserResults] = useState<ParserTestResult[] | null>(null)
  const [parserBusy, setParserBusy] = useState(false)

  // 分片存储(shards.json)
  const [shardCfg, setShardCfg] = useState<{ shardBy: string; hotShards: number; defaultRetentionDays: number } | null>(null)
  const [shards, setShards] = useState<Array<{ shard: string; indexId: string; startTs: number; endTs: number; rows: number; dropAllowed: boolean }>>([])
  // 告警: 规则/通道/事件 + 编辑草稿
  const [alertRules, setAlertRules] = useState<AlertRuleRow[]>([])
  const [alertChannels, setAlertChannels] = useState<AlertChannelRow[]>([])
  const [alertEvents, setAlertEvents] = useState<AlertEventRow[]>([])
  const [ruleDraft, setRuleDraft] = useState<RuleDraft | null>(null)
  const [chanDraft, setChanDraft] = useState<ChanDraft | null>(null)

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

  // ── 解析规则(parsers.json) 编辑器 ──
  async function openParserEditor() {
    setParserOpen(true)
    setParserResults(null)
    try {
      const r = await getJSON<{ rules: ParserRule[]; builtin: boolean; error: string }>('/api/logmonitor/parsers')
      setParserBuiltin(r.builtin)
      setParserErr(r.error || '')
      setParserJson(JSON.stringify({ rules: r.rules }, null, 2))
      setParserTestIdx(0)
    } catch (e: any) {
      setParserErr(e?.message || '加载解析规则失败')
    }
  }

  // 从编辑文本解析规则列表(非法 JSON 返回 null)
  function parserRulesFromText(): ParserRule[] | null {
    try {
      const parsed = JSON.parse(parserJson)
      if (!parsed || !Array.isArray(parsed.rules) || parsed.rules.length === 0) return null
      return parsed.rules as ParserRule[]
    } catch {
      return null
    }
  }

  async function saveParsers() {
    const rules = parserRulesFromText()
    if (!rules) {
      pushToast('err', '规则 JSON 格式错误, 无法保存')
      return
    }
    setParserBusy(true)
    try {
      await postJSON('/api/logmonitor/parsers/save', { rules })
      pushToast('ok', '解析规则已保存并热载生效')
      setParserErr('')
      setParserBuiltin(false)
      const r = await getJSON<{ rules: ParserRule[] }>('/api/logmonitor/parsers')
      setParserJson(JSON.stringify({ rules: r.rules }, null, 2))
    } catch (e: any) {
      pushToast('err', '保存失败: ' + (e?.message || ''))
    } finally {
      setParserBusy(false)
    }
  }

  async function testParser() {
    const rules = parserRulesFromText()
    if (!rules) {
      pushToast('err', '规则 JSON 格式错误, 无法测试')
      return
    }
    const idx = Math.max(0, Math.min(parserTestIdx, rules.length - 1))
    const lines = parserSample.split('\n').map((s) => s.replace(/\r$/, '')).filter((s) => s.trim() !== '')
    if (lines.length === 0) {
      pushToast('err', '请先粘贴几行样例日志')
      return
    }
    setParserBusy(true)
    setParserResults(null)
    try {
      const r = await postJSON<{ results: ParserTestResult[] }>('/api/logmonitor/parsers/test', { rule: rules[idx], lines })
      setParserResults(r.results)
    } catch (e: any) {
      pushToast('err', '测试失败: ' + (e?.message || ''))
    } finally {
      setParserBusy(false)
    }
  }

  function fillDefaultRules() {
    setParserJson(JSON.stringify({
      rules: [
        {
          name: 'default',
          match: {},
          time: { regex: '(\\d{4}-\\d{2}-\\d{2}[T ]\\d{2}:\\d{2}:\\d{2}(?:\\.\\d+)?(?:Z|[+-]\\d{2}:?\\d{2})?)' },
          level: { regex: '\\b(ERROR|WARN|INFO|DEBUG|FATAL)\\b', default: 'INFO' },
          service: { regex: '\\[([a-zA-Z0-9\\-_\\.]+)\\]|service[=:]\\s*([a-zA-Z0-9\\-_\\.]+)', normalize: true },
          summary: { maxLen: 200 },
        },
      ],
    }, null, 2))
    setParserTestIdx(0)
    setParserResults(null)
    pushToast('ok', '已填入内置默认规则(未保存, 点保存生效)')
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
      const r = await getJSON<{ containers?: ContainerItem[] }>(`/api/logmonitor/discover/containers?host=${encodeURIComponent(discHost?.id || '')}`)
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
      const r = await getJSON<{ pods?: K8sPodItem[] }>(`/api/logmonitor/discover/k8s?cluster=${encodeURIComponent(cluster)}&host=${encodeURIComponent(discHost?.id || '')}`)
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
          await postJSON('/api/logmonitor/scan', { path: c, source: 'container', service: '', indexId: selTargetIdx, host: discHost?.id || '' })
          ok++
        } catch { fail.push(c) }
      }
      for (const key of selPods) {
        const p = discK8sPods.find((x) => `${x.namespace}/${x.name}` === key)
        if (!p) continue
        try {
          await postJSON('/api/logmonitor/scan', { path: p.name, source: 'k8s', namespace: p.namespace, service: podSvcName(p) || '', cluster: p.clusterID, indexId: selTargetIdx, host: discHost?.id || '' })
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
      loadShards()
    } catch (e: any) {
      pushToast('err', 'ILM 执行失败: ' + (e?.message || ''))
    }
  }

  // ── 分片存储(shards.json) ──
  async function loadShards() {
    try {
      const r = await getJSON<{ config: typeof shardCfg; shards: typeof shards }>('/api/logmonitor/shards')
      setShardCfg(r.config)
      setShards(r.shards)
    } catch (e: any) {
      pushToast('err', '加载分片状态失败: ' + (e?.message || ''))
    }
  }

  async function saveShardCfg() {
    if (!shardCfg) return
    if (shardCfg.defaultRetentionDays < 1) {
      pushToast('err', '全局保留天数必须 ≥ 1')
      return
    }
    try {
      const r = await postJSON('/api/logmonitor/shards/save', { config: shardCfg })
      pushToast('ok', `分片配置已保存: 按${r.config.shardBy === 'day' ? '天' : r.config.shardBy === 'week' ? '周' : '月'}分片, 全局保留 ${r.config.defaultRetentionDays} 天`)
      setShardCfg(r.config)
      loadShards()
    } catch (e: any) {
      pushToast('err', '保存分片配置失败: ' + (e?.message || ''))
    }
  }

  async function delShard(key: string) {
    try {
      const r = await postJSON('/api/logmonitor/shards/delete', { shard: key })
      pushToast('ok', `分片 ${key} 已删除 ${r.deleted?.toLocaleString() ?? ''} 行`)
      loadShards()
    } catch (e: any) {
      pushToast('err', '删除分片失败: ' + (e?.message || ''))
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
    if (tab === 'indexes') { loadIndexes(); loadShards() }
    if (tab === 'alerts') loadAlerts()
  }, [tab])

  // Live: stats/sources/indexes 每 3s 轮询（避免手动刷新才看到新采集数据）
  useEffect(() => {
    if (tab !== 'stats' && tab !== 'sources' && tab !== 'indexes' && tab !== 'alerts') return
    // 统计总览聚合重(百万行级 GROUP BY), 轮询降到 15s; 源列表/索引列表保持 3s 轻量刷新; 告警跟随 15s
    const intervalMs = tab === 'stats' || tab === 'alerts' ? 15000 : 3000
    const t = setInterval(() => {
      if (tab === 'stats') loadStats(statsService)
      if (tab === 'sources') loadSources()
if (tab === 'indexes') { loadIndexes(); loadShards() }
      if (tab === 'alerts') loadAlerts()
    }, intervalMs)
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

  // ── 告警: 规则/通道/事件 ──
  function loadAlerts() {
    getJSON('/api/logmonitor/alerts/events?limit=50').then((d) => setAlertEvents(d.events || [])).catch(() => {})
    getJSON('/api/logmonitor/alerts/rules').then((d) => setAlertRules(d.rules || [])).catch(() => {})
    getJSON('/api/logmonitor/alerts/channels').then((d) => setAlertChannels(d.channels || [])).catch(() => {})
  }

  const fmtDur = (ms: number) => (!ms || ms <= 0 ? '-' : ms % 3600000 === 0 ? `${ms / 3600000} 小时` : ms % 60000 === 0 ? `${ms / 60000} 分钟` : `${Math.round(ms / 1000)} 秒`)

  function openRuleEdit(r?: AlertRuleRow) {
    setRuleDraft(r ? {
      id: r.id, name: r.name, enabled: r.enabled,
      condField: r.condition.startsWith('service=') ? 'service' : 'level',
      condValue: r.condition.replace(/^(level|service)=/, ''),
      countThresh: r.countThresh || 1, windowMs: r.windowMs || 60000, cooldownMs: r.cooldownMs || 300000,
      channels: r.channels || [],
    } : { id: '', name: '', enabled: true, condField: 'level', condValue: 'ERROR', countThresh: 1, windowMs: 60000, cooldownMs: 300000, channels: [] })
  }

  function openChanEdit(c?: AlertChannelRow) {
    setChanDraft(c ? { id: c.id, name: c.name, type: c.type || 'webhook', url: c.url, method: c.method || 'POST', headers: c.headers || '', enabled: c.enabled }
      : { id: '', name: '', type: 'webhook', url: '', method: 'POST', headers: '', enabled: true })
  }

  async function saveRule() {
    if (!ruleDraft) return
    if (!ruleDraft.name.trim()) { pushToast('err', '请填写规则名称'); return }
    if (!ruleDraft.condValue.trim()) { pushToast('err', '请填写条件匹配值'); return }
    try {
      await postJSON('/api/logmonitor/alerts/rules/save', {
        id: ruleDraft.id, name: ruleDraft.name.trim(), enabled: ruleDraft.enabled,
        condition: `${ruleDraft.condField}=${ruleDraft.condValue.trim()}`,
        countThresh: ruleDraft.countThresh > 0 ? ruleDraft.countThresh : 1,
        windowMs: ruleDraft.windowMs, cooldownMs: ruleDraft.cooldownMs, channels: ruleDraft.channels,
      })
      pushToast('ok', '告警规则已保存')
      setRuleDraft(null)
      loadAlerts()
    } catch (e: any) { pushToast('err', '保存规则失败: ' + (e?.message || e)) }
  }

  async function saveChan() {
    if (!chanDraft) return
    if (!chanDraft.name.trim()) { pushToast('err', '请填写通道名称'); return }
    if (!chanDraft.url.trim()) { pushToast('err', '请填写 Webhook URL'); return }
    try {
      await postJSON('/api/logmonitor/alerts/channels/save', { ...chanDraft, name: chanDraft.name.trim(), url: chanDraft.url.trim() })
      pushToast('ok', '通知通道已保存')
      setChanDraft(null)
      loadAlerts()
    } catch (e: any) { pushToast('err', '保存通道失败: ' + (e?.message || e)) }
  }

  async function toggleRule(r: AlertRuleRow) {
    try { await postJSON('/api/logmonitor/alerts/rules/save', { ...r, enabled: !r.enabled }); loadAlerts() } catch (e: any) { pushToast('err', '切换失败: ' + (e?.message || e)) }
  }

  async function toggleChan(c: AlertChannelRow) {
    try { await postJSON('/api/logmonitor/alerts/channels/save', { ...c, enabled: !c.enabled }); loadAlerts() } catch (e: any) { pushToast('err', '切换失败: ' + (e?.message || e)) }
  }

  async function delRule(id: string, name: string) {
    try {
      await postJSON(`/api/logmonitor/alerts/rules/delete?id=${encodeURIComponent(id)}`, {})
      pushToast('ok', `规则 ${name} 已删除`)
      loadAlerts()
    } catch (e: any) { pushToast('err', '删除失败: ' + (e?.message || e)) }
  }

  async function delChan(id: string, name: string) {
    try {
      await postJSON(`/api/logmonitor/alerts/channels/delete?id=${encodeURIComponent(id)}`, {})
      pushToast('ok', `通道 ${name} 已删除`)
      loadAlerts()
    } catch (e: any) { pushToast('err', '删除失败: ' + (e?.message || e)) }
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
        textStyle: { color: cssVar('--text-dim'), fontSize: 11 },
        itemWidth: 12,
        itemHeight: 8,
      },
      grid: { left: 8, right: 8, top: 28, bottom: 4, containLabel: true },
      xAxis: {
        type: 'category',
        data: sorted.map((b) => b.ts),
        axisLine: { lineStyle: { color: cssVar('--border'), type: 'dashed' } },
        axisLabel: { color: cssVar('--text-dim'), fontSize: 10, hideOverlap: true, formatter: (v: number) => histAxisLabel(Number(v), bucketMs) },
        splitLine: { show: false },
      },
      yAxis: {
        type: 'value',
        axisLabel: { color: cssVar('--text-dim'), fontSize: 10 },
        splitLine: { lineStyle: { color: cssVar('--border'), type: 'dashed' } },
      },
      series: LEVELS.map((lvl) => ({
        name: lvl,
        type: 'bar',
        stack: 'total',
        itemStyle: { color: levelColor(lvl) },
        data: sorted.map((b) => ({ value: b.count[lvl] || 0, itemStyle: { color: levelColor(lvl) } })),
        barWidth: '55%',
        animation: false,
        emphasis: { itemStyle: { shadowBlur: 4, shadowColor: 'rgba(0,0,0,0.4)' } },
      })),
    }
  }, [hist, bucketMs, theme])

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
        <button className={`tab ${tab === 'alerts' ? 'tab-on' : ''}`} onClick={() => setTab('alerts')}>告警</button>
        <span style={{ flex: 1 }} />
        <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => setScanOpen(true)}>扫描文件入库</button>
        <button className="btn-glass-soft btn-glass-soft-sm" onClick={runIlm}>执行 ILM 清理</button>
      </div>

      {/* Toasts */}
      <div className="kib-toast-wrap" aria-live="polite">
        {toasts.map((t) => (
          <div key={t.id} className={`kib-toast kib-toast-${t.kind}`} role="status" title="点击关闭" onClick={() => setToasts((x) => x.filter((y) => y.id !== t.id))}>
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
                     <select value={indexView} onChange={(e) => onIndexViewChange(e.target.value)} title="数据视图 / 索引" aria-label="数据视图 / 索引">
                       <option value="">所有数据</option>
                       {allIndexes.map((ix) => (
                         <option key={ix.id} value={ix.id}>{ix.name || ix.id}</option>
                       ))}
                     </select>
                   </div>

                   {absStart && absEnd ? (
                     <span className="kib-topbar-soon">自定义区间</span>
                   ) : (
                     <select className="kib-time" value={relativeHours} onChange={(e) => onTimeChange(Number(e.target.value))} aria-label="时间范围">
                       <option value={1}>最近 1 小时</option>
                       <option value={6}>最近 6 小时</option>
                       <option value={24}>最近 24 小时</option>
                       <option value={72}>最近 3 天</option>
<option value={168}>最近 7 天</option>
                        <option value={720}>最近 30 天</option>
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
                       aria-label="KQL 查询"
                       onChange={(e) => { setKql(e.target.value); setSuggestIdx(0); setSuggestOpen(true) }}
                       onFocus={() => setSuggestOpen(true)}
                       onBlur={() => setTimeout(() => setSuggestOpen(false), 150)}
                       onKeyDown={(e) => {
                         const hints = suggestOpen ? buildHints() : []
                         if (e.key === 'ArrowDown' && hints.length) { e.preventDefault(); setSuggestIdx((i) => Math.min(i + 1, hints.length - 1)) }
                         else if (e.key === 'ArrowUp' && hints.length) { e.preventDefault(); setSuggestIdx((i) => Math.max(i - 1, 0)) }
                         else if (e.key === 'Enter') {
                           const h = hints[suggestIdx]
                           if (h) { setKql(h.field + h.op + h.val); applyKql(h.field + h.op + h.val) } else applyKql()
                         }
                         if (e.key === 'Escape') setSuggestOpen(false)
                       }}
                     />
{suggestOpen && (
                        <div className="kib-suggest" role="listbox" aria-label="字段建议">
                          {buildHints().map((h, i) => (
                            <div key={i} className={`kib-suggest-item ${i === suggestIdx ? 'sel' : ''}`} onMouseEnter={() => setSuggestIdx(i)} onMouseDown={() => { setKql(h.field + h.op + h.val); applyKql(h.field + h.op + h.val) }}>
                              <span className="kib-suggest-opt">{h.field}{h.op}{h.val}</span>
                              <span style={{ color: 'var(--text-dim)' }}>{h.label}</span>
                            </div>
                          ))}
                        </div>
                      )}
                   </div>
                   <button className="kib-btn kib-btn-primary" onClick={() => applyKql()}>查询</button>
                   <button className="kib-btn kib-btn-bare" onClick={clearFilters} title="清空所有过滤">重置</button>
                 </div>

                 {/* 已应用过滤 chips */}
                {(service || level || source || keyword || indexFilter) && (
                  <div className="kib-chips">
                    {indexFilter && <span className="kib-chip" role="button" tabIndex={0} aria-label={`移除过滤 index: ${allIndexes.find((ix) => ix.id === indexFilter)?.name || indexFilter}`} onClick={() => { setIndexFilter(''); setIndexView(''); setPage(1) }} onKeyDown={(e) => (e.key === 'Enter' || e.key === ' ') && (setIndexFilter(''), setIndexView(''), setPage(1))}><span className="kib-chip-key">index:</span>{allIndexes.find((ix) => ix.id === indexFilter)?.name || indexFilter} ✕</span>}
                    {service && <span className="kib-chip" role="button" tabIndex={0} aria-label={`移除过滤 service: ${service}`} onClick={() => setService('')} onKeyDown={(e) => (e.key === 'Enter' || e.key === ' ') && setService('')}><span className="kib-chip-key">service:</span>{service} ✕</span>}
                    {level && <span className="kib-chip" role="button" tabIndex={0} aria-label={`移除过滤 level: ${level}`} onClick={() => setLevel('')} onKeyDown={(e) => (e.key === 'Enter' || e.key === ' ') && setLevel('')}><span className="kib-chip-key">level:</span>{level} ✕</span>}
                    {source && <span className="kib-chip" role="button" tabIndex={0} aria-label={`移除过滤 source: ${source}`} onClick={() => setSource('')} onKeyDown={(e) => (e.key === 'Enter' || e.key === ' ') && setSource('')}><span className="kib-chip-key">source:</span>{source} ✕</span>}
                    {keyword && <span className="kib-chip" role="button" tabIndex={0} aria-label={`移除过滤 message: ${keyword}`} onClick={() => setKeyword('')} onKeyDown={(e) => (e.key === 'Enter' || e.key === ' ') && setKeyword('')}><span className="kib-chip-key">message:</span>{keyword} ✕</span>}
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
                              <tr key={e.id} className={detail?.id === e.id ? 'row-on' : ''} tabIndex={0} onClick={() => viewDetail(e.id)} onKeyDown={(ev) => ev.key === 'Enter' && viewDetail(e.id)}>
                                <td className="kib-ts">{fmtTime(e.ts)}</td>
                                <td>
                                  <span className="kib-lvl" style={{ color: levelColor(e.level), background: levelColor(e.level) + '22' }}>{e.level || '-'}</span>
                                </td>
                                <td className="kib-docs-sum" title={e.summary}>
                                  {highlight(e.summary, highlightWords()) || e.summary}
                                </td>
                                <td>
                                  {e.indexId ? (
                                    <span className="kib-idx" role="button" tabIndex={0} title={`索引 ${e.indexId}`} onClick={(ev) => { ev.stopPropagation(); addFieldFilter('indexId', e.indexId) }} onKeyDown={(ev) => { if (ev.key === 'Enter') { ev.stopPropagation(); addFieldFilter('indexId', e.indexId) } }}>
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
                      <span className="kib-lvl" style={{ color: levelColor(detail.level), background: levelColor(detail.level) + '22' }}>{detail.level || '-'}</span>
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
            <button className="btn-glass btn-sm" onClick={openParserEditor}>解析规则</button>
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
                        role="switch"
                        aria-checked={s.enabled}
                        aria-label={`${s.enabled ? '停用' : '启用'}采集: ${s.name}`}
                        tabIndex={0}
                        title={s.enabled ? '停用采集(暂停接收日志)' : '启用采集'}
                        onClick={() => toggleSourceEnabled(s)}
                        onKeyDown={(e) => (e.key === 'Enter' || e.key === ' ') && toggleSourceEnabled(s)}
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
              <span style={{ marginLeft: 10, color: 'var(--text-dim)', fontSize: 12 }}>选择接入目标主机容器 / 已连接 K8S 集群的日志</span>
            </div>
          ) : (
            <div className="kib-discover-panel" style={{ marginTop: 16 }}>
              <div className="log-filter-row">
                <div style={{ flex: 1 }}>
                  <span style={{ color: 'var(--text)', fontWeight: 600, fontSize: 14 }}>从已连接资源接入日志</span>
                  <span style={{ marginLeft: 10 }}><HostSelector /></span>
                  {sources.length > 0 && <span className="kib-badge" style={{ marginLeft: 8 }}>{sources.length} 个日志源</span>}
                  <div style={{ color: 'var(--text-dim)', fontSize: 12, marginTop: 3 }}>勾选下方容器 / Pod, 点击"接入"即可扫其 stdout 日志入库; 选择归档索引可双写。目标主机切换后自动重新发现。</div>
                  {sources.length === 0 && !discLoading && <div style={{ color: 'var(--lvl-error)', fontSize: 12, marginTop: 4 }}>⚠ 日志源列表加载失败/为空, 下方√ 状态不可用, 请检查服务端 /api/logmonitor/sources</div>}
                </div>
                <button className="btn-glass btn-sm" onClick={toggleDiscoverPanel} disabled={discLoading}>收起</button>
              </div>
              <div className="kib-form-row" style={{ marginTop: 10 }}>
                <label htmlFor="disc-target-idx">归属索引(可选, 双写到归档)</label>
                <select id="disc-target-idx" value={selTargetIdx} onChange={(e) => setSelTargetIdx(e.target.value)}>
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
      <select value={selCluster} onChange={(e) => { setSelCluster(e.target.value); loadDiscoverK8s(e.target.value); setSelPods(new Set()); setSelNamespace(''); setPodSearch(''); }} aria-label="集群">
        <option value="">选择集群…</option>
        {discClusters.map((c) => <option key={c} value={c}>集群 {c}</option>)}
      </select>
      <select value={selNamespace} onChange={(e) => setSelNamespace(e.target.value)} aria-label="命名空间">
        <option value="">所有命名空间</option>
        {[...new Set(discK8sPods.map((p) => p.namespace))].sort().map((ns) => (
          <option key={ns} value={ns}>{ns}</option>
        ))}
      </select>
      <input
        placeholder="搜索 Pod 名 / 命名空间"
        aria-label="搜索 Pod 名 / 命名空间"
        value={podSearch}
        onChange={(e) => setPodSearch(e.target.value)}
        style={{ flex: 1, minWidth: 180, padding: '4px 8px', fontSize: 12, background: 'var(--bg-soft)', border: '1px solid var(--border)', borderRadius: 8 }}
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

      {tab === 'alerts' && (
        <div className="glass log-card">
          <div className="log-filter-row">
            <span style={{ color: 'var(--text)', fontWeight: 600, fontSize: 14 }}>告警</span>
            <span className="kib-badge kib-tint-danger">{alertEvents.filter((e) => e.status === 'firing').length} 个触发中</span>
            <span style={{ flex: 1 }} />
            <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => openChanEdit()}>+ 通知通道</button>
            <button className="btn-glass btn-sm" onClick={() => openRuleEdit()}>+ 告警规则</button>
          </div>

          <div className="kib-docs-head">告警规则 (评估器每 30s 扫描一次)</div>
          <table className="log-table">
            <thead><tr><th>名称</th><th>条件</th><th>阈值</th><th>窗口</th><th>冷却</th><th>通知通道</th><th>状态</th><th style={{ width: 170 }}>操作</th></tr></thead>
            <tbody>
              {alertRules.map((r) => (
                <tr key={r.id}>
                  <td><strong>{r.name}</strong></td>
                  <td className="log-mono">{r.condition}</td>
                  <td>≥ {r.countThresh} 条</td>
                  <td>{fmtDur(r.windowMs)}</td>
                  <td>{fmtDur(r.cooldownMs)}</td>
                  <td>{(r.channels || []).length === 0 ? <span style={{ color: 'var(--text-dim)' }}>未配置</span> : r.channels.map((cid) => alertChannels.find((c) => c.id === cid)?.name || cid).join(', ')}</td>
                  <td>
                    <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
                      {r.state === 'firing' ? <span className="kib-badge kib-tint-danger">firing</span> : <span className="kib-badge kib-tint-ok">ok</span>}
                      <span
                        className={`kib-switch ${r.enabled ? 'on' : ''}`}
                        role="switch"
                        aria-checked={r.enabled}
                        aria-label={`${r.enabled ? '停用' : '启用'}规则: ${r.name}`}
                        tabIndex={0}
                        title={r.enabled ? '停用规则' : '启用规则'}
                        onClick={() => toggleRule(r)}
                        onKeyDown={(e) => (e.key === 'Enter' || e.key === ' ') && toggleRule(r)}
                      ><i /></span>
                    </span>
                  </td>
                  <td>
                    <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => openRuleEdit(r)}>编辑</button>
                    <button className="btn-glass-soft btn-glass-soft-danger btn-glass-soft-sm" onClick={() => delRule(r.id, r.name)}>删除</button>
                  </td>
                </tr>
              ))}
              {alertRules.length === 0 && <tr><td colSpan={8} style={{ color: 'var(--text-dim)' }}>暂无规则 — 点右上角"新增告警规则"开始</td></tr>}
            </tbody>
          </table>

          <div className="kib-docs-head">触发事件 (最近 50 条)</div>
          <table className="log-table">
            <thead><tr><th>时间</th><th>规则</th><th>级别</th><th>服务</th><th>计数</th><th>状态</th></tr></thead>
            <tbody>
              {alertEvents.map((ev) => (
                <tr key={ev.id}>
                  <td className="log-mono">{fmtTime(ev.firedAt)}</td>
                  <td>{ev.ruleName}</td>
                  <td><span className="log-level" style={{ color: levelColor(ev.level) }}>{ev.level}</span></td>
                  <td>{ev.service || '-'}</td>
                  <td>{ev.count}</td>
                  <td>{ev.status === 'firing' ? <span className="kib-badge kib-tint-danger">触发中</span> : <span className="kib-badge kib-tint-ok">已恢复</span>}</td>
                </tr>
              ))}
              {alertEvents.length === 0 && <tr><td colSpan={6} style={{ color: 'var(--text-dim)' }}>暂无触发事件</td></tr>}
            </tbody>
          </table>

          <div className="kib-docs-head">通知通道</div>
          <table className="log-table">
            <thead><tr><th>名称</th><th>类型</th><th>地址</th><th>方法</th><th>状态</th><th style={{ width: 170 }}>操作</th></tr></thead>
            <tbody>
              {alertChannels.map((c) => (
                <tr key={c.id}>
                  <td><strong>{c.name}</strong></td>
                  <td>{c.type}</td>
                  <td className="log-mono" style={{ maxWidth: 320, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{c.url}</td>
                  <td>{c.method || 'POST'}</td>
                  <td>{c.enabled ? <span className="kib-badge kib-tint-ok">启用</span> : <span className="kib-badge kib-tint-danger">停用</span>}</td>
                  <td>
                    <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => openChanEdit(c)}>编辑</button>
                    <button className="btn-glass-soft btn-glass-soft-danger btn-glass-soft-sm" onClick={() => delChan(c.id, c.name)}>删除</button>
                  </td>
                </tr>
              ))}
              {alertChannels.length === 0 && <tr><td colSpan={6} style={{ color: 'var(--text-dim)' }}>暂无通道 — 告警以 Webhook POST 推送</td></tr>}
            </tbody>
          </table>
        </div>
      )}

      {tab === 'stats' && (
        <div className="glass log-card">
          <div className="log-filter-row">
            <span style={{ marginRight: 8 }}>服务过滤:</span>
            <input
              className="input log-input log-svc-filter"
              placeholder="输入服务名过滤"
              aria-label="按服务名过滤统计"
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
                      <span className="log-level-bar-lbl" style={{ color: levelColor(lvl) }}>{lvl}</span>
                      <div className="log-level-bar-track">
                        <div className="log-level-bar-fill" style={{ width: (cnt / st.totalCount) * 100 + '%', backgroundColor: levelColor(lvl) }} />
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
                            <td key={l} style={{ color: (s.levels || {})[l] ? levelColor(l) : undefined }}>
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
                      <td><span className="log-level" style={{ color: stageColor(st?.storageStage || 'hot') }}>{st?.storageStage || 'hot'}</span></td>
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

      {tab === 'indexes' && !editing && shardCfg && (
        <div className="glass log-card" style={{ marginTop: 14 }}>
          <div className="log-filter-row">
            <span style={{ color: 'var(--text)', fontWeight: 600, fontSize: 14 }}>分片存储 (shards.json)</span>
            <span className="kib-badge kib-tint-accent">按月分片 · 自动归档</span>
          </div>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap', fontSize: 12, color: 'var(--text-dim)', marginBottom: 8 }}>
            <label>分片粒度</label>
            <select aria-label="分片粒度" value={shardCfg.shardBy} onChange={(e) => setShardCfg({ ...shardCfg, shardBy: e.target.value })} style={{ width: 110 }}>
              <option value="month">月</option>
              <option value="week">周</option>
              <option value="day">天</option>
            </select>
            <label>热片数</label>
            <input type="number" min={0} aria-label="热分片数量" value={shardCfg.hotShards} onChange={(e) => setShardCfg({ ...shardCfg, hotShards: Number(e.target.value) })} style={{ width: 55 }} />
            <label>全局保留(天)</label>
            <input type="number" min={1} aria-label="全局保留天数" value={shardCfg.defaultRetentionDays} onChange={(e) => setShardCfg({ ...shardCfg, defaultRetentionDays: Number(e.target.value) })} style={{ width: 55 }} />
            <button className="btn-glass btn-sm" onClick={saveShardCfg}>保存配置</button>
            <span style={{ flex: 1 }} />
            <span className={`kib-badge${shards.some((x) => x.dropAllowed) ? ' kib-tint-danger' : ''}`}>
              {shards.filter((x) => x.dropAllowed).length} 片可清理
            </span>
            <span>过期分片整体删除(秒级), 保存即写 shards.json</span>
          </div>
          <table className="log-table">
            <thead>
              <tr>
                <th>分片</th>
                <th>归属索引</th>
                <th>起止时间</th>
                <th>行数</th>
                <th>状态</th>
                <th style={{ width: 180 }}>操作</th>
              </tr>
            </thead>
            <tbody>
              {shards.map((s) => {
                const idxName = s.indexId ? (indexes.find((ix) => ix.id === s.indexId)?.name || s.indexId) : '未归属'
                return (
                <tr key={s.shard}>
                  <td className="log-mono"><strong>{s.shard}</strong></td>
                  <td>{idxName}</td>
                  <td className="log-mono">
                    {s.startTs ? `${new Date(s.startTs).toLocaleDateString('zh-CN')} ~ ${new Date(s.endTs).toLocaleDateString('zh-CN')}` : '—'}
                  </td>
                  <td>{s.rows.toLocaleString()}</td>
                  <td>
                    {s.dropAllowed ? <span className="kib-badge kib-tint-danger">可清理</span> : <span className="kib-badge kib-tint-ok">保留中</span>}
                  </td>
                  <td>
                    <button className="btn-glass-soft btn-glass-soft-danger btn-glass-soft-sm" disabled={!s.dropAllowed} onClick={() => delShard(s.shard)} title={s.dropAllowed ? '删除此分片与数据' : '分片仍在保留期内, 不可删除'}>
                      删除分片
                    </button>
                  </td>
                </tr>
                )
              })}
            </tbody>
          </table>
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
            <input className="input log-input" placeholder="索引名称 *" aria-label="索引名称(必填)" value={editing.name} onChange={(e) => setEditing({ ...editing, name: e.target.value })} />
            <select className="input log-input" aria-label="来源类型" value={editing.source} onChange={(e) => setEditing({ ...editing, source: e.target.value })}>
              <option value="file">file</option>
              <option value="syslog">syslog</option>
              <option value="journal">journal</option>
              <option value="http">http</option>
              <option value="loki">loki</option>
              <option value="fluentbit">fluentbit</option>
              <option value="es">es</option>
            </select>
            <input className="input log-input" placeholder="采集路径/地址" aria-label="采集路径/地址" value={editing.sourcePath} onChange={(e) => setEditing({ ...editing, sourcePath: e.target.value })} />
            <input className="input log-input" placeholder="归属服务" aria-label="归属服务" value={editing.service} onChange={(e) => setEditing({ ...editing, service: e.target.value })} />
            <input className="input log-input log-hours" type="number" placeholder="总保留(天)" aria-label="总保留天数" value={editing.deleteAfter} onChange={(e) => setEditing({ ...editing, deleteAfter: Number(e.target.value) })} />
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
                  <td><span className="log-level" style={{ color: stageColor(sg) }}>{sg.toUpperCase()}</span></td>
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

      {/* 通用表单: 告警规则 */}
      {ruleDraft && (
        <div className="kib-modal-mask" onClick={() => setRuleDraft(null)}>
          <div className="kib-modal" onClick={(e) => e.stopPropagation()}>
            <h3>{ruleDraft.id ? '编辑告警规则' : '新增告警规则'}</h3>
            <div className="kib-inline-form">
              <div className="kib-form-row">
                <label htmlFor="rule-name">名称 *</label>
                <input id="rule-name" value={ruleDraft.name} onChange={(e) => setRuleDraft({ ...ruleDraft, name: e.target.value })} placeholder="如 生产错误激增" />
              </div>
              <div className="kib-form-row">
                <label htmlFor="rule-cond-field">匹配条件</label>
                <div style={{ display: 'flex', gap: 8 }}>
                  <select id="rule-cond-field" value={ruleDraft.condField} onChange={(e) => setRuleDraft({ ...ruleDraft, condField: e.target.value as 'level' | 'service' })} style={{ width: 110 }}>
                    <option value="level">级别为</option>
                    <option value="service">服务为</option>
                  </select>
                  <input id="rule-cond-value" aria-label="条件匹配值" value={ruleDraft.condValue} onChange={(e) => setRuleDraft({ ...ruleDraft, condValue: e.target.value })} placeholder={ruleDraft.condField === 'level' ? 'ERROR' : 'order-api'} style={{ flex: 1 }} />
                </div>
              </div>
              <div className="kib-form-row">
                <label htmlFor="rule-thresh">触发阈值(窗口内匹配条数)</label>
                <input id="rule-thresh" type="number" min={1} value={ruleDraft.countThresh} onChange={(e) => setRuleDraft({ ...ruleDraft, countThresh: Number(e.target.value) })} />
              </div>
              <div className="kib-form-row">
                <label htmlFor="rule-window">统计窗口</label>
                <select id="rule-window" value={ruleDraft.windowMs} onChange={(e) => setRuleDraft({ ...ruleDraft, windowMs: Number(e.target.value) })}>
                  <option value={60000}>最近 1 分钟</option>
                  <option value={300000}>最近 5 分钟</option>
                  <option value={900000}>最近 15 分钟</option>
                  <option value={3600000}>最近 1 小时</option>
                </select>
              </div>
              <div className="kib-form-row">
                <label htmlFor="rule-cooldown">告警冷却(触发后多久内不重复)</label>
                <select id="rule-cooldown" value={ruleDraft.cooldownMs} onChange={(e) => setRuleDraft({ ...ruleDraft, cooldownMs: Number(e.target.value) })}>
                  <option value={300000}>5 分钟</option>
                  <option value={1800000}>30 分钟</option>
                  <option value={3600000}>1 小时</option>
                  <option value={21600000}>6 小时</option>
                </select>
              </div>
              <div className="kib-form-row">
                <label>通知通道</label>
                <div style={{ display: 'flex', flexWrap: 'wrap', gap: 10 }}>
                  {alertChannels.length === 0 && <span style={{ color: 'var(--text-dim)', fontSize: 12 }}>尚无通道, 可先"新增通知通道"</span>}
                  {alertChannels.map((c) => (
                    <label key={c.id} style={{ display: 'inline-flex', alignItems: 'center', gap: 4, fontSize: 13 }}>
                      <input type="checkbox" checked={ruleDraft.channels.includes(c.id)} onChange={(e) => setRuleDraft({ ...ruleDraft, channels: e.target.checked ? [...ruleDraft.channels, c.id] : ruleDraft.channels.filter((x) => x !== c.id) })} />
                      {c.name}
                    </label>
                  ))}
                </div>
              </div>
              <div className="kib-form-row">
                <label style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
                  <input type="checkbox" checked={ruleDraft.enabled} onChange={(e) => setRuleDraft({ ...ruleDraft, enabled: e.target.checked })} />
                  启用该规则
                </label>
              </div>
            </div>
            <div className="kib-modal-actions">
              <button className="kib-btn kib-btn-bare" onClick={() => setRuleDraft(null)}>取消</button>
              <button className="kib-btn kib-btn-primary" onClick={saveRule}>保存</button>
            </div>
          </div>
        </div>
      )}

      {/* 通用表单: 通知通道 */}
      {chanDraft && (
        <div className="kib-modal-mask" onClick={() => setChanDraft(null)}>
          <div className="kib-modal" onClick={(e) => e.stopPropagation()}>
            <h3>{chanDraft.id ? '编辑通知通道' : '新增通知通道'}</h3>
            <div className="kib-inline-form">
              <div className="kib-form-row">
                <label htmlFor="chan-name">名称 *</label>
                <input id="chan-name" value={chanDraft.name} onChange={(e) => setChanDraft({ ...chanDraft, name: e.target.value })} placeholder="如 运维值班群机器人" />
              </div>
              <div className="kib-form-row">
                <label htmlFor="chan-url">Webhook URL *</label>
                <input id="chan-url" value={chanDraft.url} onChange={(e) => setChanDraft({ ...chanDraft, url: e.target.value })} placeholder="https://example.com/hook" />
              </div>
              <div className="kib-form-row">
                <label htmlFor="chan-method">请求方法</label>
                <select id="chan-method" value={chanDraft.method} onChange={(e) => setChanDraft({ ...chanDraft, method: e.target.value })}>
                  <option value="POST">POST</option>
                  <option value="GET">GET</option>
                </select>
              </div>
              <div className="kib-form-row">
                <label htmlFor="chan-headers">额外请求头(可选, JSON 对象)</label>
                <textarea
                  id="chan-headers"
                  value={chanDraft.headers}
                  onChange={(e) => setChanDraft({ ...chanDraft, headers: e.target.value })}
                  placeholder='{"Authorization": "Bearer xxx"}'
                  spellCheck={false}
                  style={{ minHeight: 60, fontFamily: 'var(--mono)', fontSize: 12, background: 'var(--bg-soft)', border: '1px solid var(--border)', borderRadius: 8, padding: 6, color: 'var(--text)', boxSizing: 'border-box' }}
                />
              </div>
              <div className="kib-form-row">
                <label style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
                  <input type="checkbox" checked={chanDraft.enabled} onChange={(e) => setChanDraft({ ...chanDraft, enabled: e.target.checked })} />
                  启用该通道
                </label>
              </div>
            </div>
            <div className="kib-modal-actions">
              <button className="kib-btn kib-btn-bare" onClick={() => setChanDraft(null)}>取消</button>
              <button className="kib-btn kib-btn-primary" onClick={saveChan}>保存</button>
            </div>
          </div>
        </div>
      )}

      {/* 通用表单: 扫描入库 (替换 window.prompt) */}
      {scanOpen && (
        <div className="kib-modal-mask" onClick={() => setScanOpen(false)}>
          <div className="kib-modal" onClick={(e) => e.stopPropagation()}>
            <h3>扫描文件入库</h3>
            <div className="kib-inline-form">
              <div className="kib-form-row">
                <label htmlFor="scan-path">日志文件绝对路径 *</label>
                <input id="scan-path" value={scanPath} onChange={(e) => setScanPath(e.target.value)} placeholder="/var/log/syslog" />
              </div>
              <div className="kib-form-row">
                <label htmlFor="scan-svc">归属服务</label>
                <input id="scan-svc" value={scanSvc} onChange={(e) => setScanSvc(e.target.value)} placeholder="留空自动提取" />
              </div>
              <div className="kib-form-row">
                <label htmlFor="scan-idx">归属索引(可选, 双写到归档)</label>
                <select id="scan-idx" value={scanIdx} onChange={(e) => setScanIdx(e.target.value)}>
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
                <label htmlFor="src-name">名称 *</label>
                <input id="src-name" value={srcDraft.name} onChange={(e) => setSrcDraft({ ...srcDraft, name: e.target.value })} placeholder="如 order-api" />
              </div>
              <div className="kib-form-row">
                <label htmlFor="src-type">类型</label>
                <select id="src-type" value={srcDraft.type} onChange={(e) => setSrcDraft({ ...srcDraft, type: e.target.value })}>
                  <option value="file">file</option>
                  <option value="syslog">syslog</option>
                  <option value="journal">journal</option>
                  <option value="http">http</option>
                  <option value="container">container</option>
                </select>
              </div>
              <div className="kib-form-row">
                <label htmlFor="src-path">文件路径 / 容器名 / URL *</label>
                <input id="src-path" value={srcDraft.path} onChange={(e) => setSrcDraft({ ...srcDraft, path: e.target.value })} />
              </div>
              <div className="kib-form-row">
                <label htmlFor="src-svc">所属服务</label>
                <input id="src-svc" value={srcDraft.service} onChange={(e) => setSrcDraft({ ...srcDraft, service: e.target.value })} />
              </div>
            </div>
            <div className="kib-modal-actions">
              <button className="kib-btn kib-btn-bare" onClick={() => setSrcOpen(false)}>取消</button>
              <button className="kib-btn kib-btn-primary" onClick={addSource}>保存</button>
            </div>
          </div>
        </div>
      )}

      {/* 解析规则编辑器 (parsers.json): 可视化编辑 + 测试窗口 */}
      {parserOpen && (
        <div className="kib-modal-mask" onClick={() => setParserOpen(false)}>
          <div className="kib-modal kib-modal-wide" onClick={(e) => e.stopPropagation()}>
            <h3 style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
              解析规则 (parsers.json)
              {parserBuiltin ? (
                <span className="kib-badge kib-tint-info">内置默认</span>
              ) : (
                <span className="kib-badge kib-tint-ok">自定义</span>
              )}
            </h3>
            <div style={{ fontSize: 12, color: 'var(--text-dim)', marginBottom: 8 }}>
              按 source/服务/文件 匹配日志行, 用正则提取 时间/级别/服务。保存即写底层文件并热加载生效（支持自动热重载）。
            </div>
            {parserErr && <div style={{ color: 'var(--lvl-error)', fontSize: 12, marginBottom: 6 }}>⚠ {parserErr}</div>}

            <div className="kib-inline-form">
              <div className="kib-form-row">
                <label htmlFor="parser-json">规则 JSON *（写错会标红, 保存被拒）</label>
                <textarea
                  id="parser-json"
                  value={parserJson}
                  onChange={(e) => { setParserJson(e.target.value); setParserResults(null) }}
                  spellCheck={false}
                  style={{ width: '100%', minHeight: 180, fontFamily: 'var(--mono)', fontSize: 12, background: 'var(--bg-soft)', border: '1px solid var(--border)', borderRadius: 8, padding: 8, boxSizing: 'border-box' }}
                />
              </div>
              <div className="kib-form-row">
                <label htmlFor="parser-test-idx">用什么规则测试（来自上方 JSON）</label>
                <select id="parser-test-idx" value={parserTestIdx} onChange={(e) => { setParserTestIdx(Number(e.target.value)); setParserResults(null) }}>
                  {(() => { const rs = parserRulesFromText(); return rs
                    ? rs.map((r, i) => <option key={i} value={i}>{i} · {r.name || '(未命名)'}</option>)
                    : <option value={0}>JSON 格式错误</option> })()}
                </select>
              </div>
              <div className="kib-form-row">
                <label htmlFor="parser-sample">样例日志（每行一条, 粘贴真实日志行）</label>
                <textarea
                  id="parser-sample"
                  value={parserSample}
                  onChange={(e) => setParserSample(e.target.value)}
                  placeholder={'2026-09-09 10:00:01.123 INFO  [order-api] 订单创建成功 id=123\n2026-09-09 10:00:02.456 WARN  [order-api] 重试第 2 次'}
                  spellCheck={false}
                  style={{ width: '100%', minHeight: 90, fontFamily: 'var(--mono)', fontSize: 12, background: 'var(--bg-soft)', border: '1px solid var(--border)', borderRadius: 8, padding: 8, boxSizing: 'border-box' }}
                />
              </div>

              {parserResults && (
                <table className="log-table" style={{ marginTop: 6 }}>
                  <thead>
                    <tr>
                      <th style={{ width: 160 }}>解析时间</th>
                      <th style={{ width: 70 }}>级别</th>
                      <th style={{ width: 120 }}>服务</th>
                      <th>摘要</th>
                    </tr>
                  </thead>
                  <tbody>
                    {parserResults.map((res, i) => (
                      <tr key={i}>
                        {res.error ? (
                          <td colSpan={4} style={{ color: 'var(--lvl-error)' }}>✗ {res.error}</td>
                        ) : (
                          <>
                            <td className="log-mono">{new Date(res.ts).toLocaleString('zh-CN', { hour12: false })}</td>
                            <td><span className={`dot ${res.level === 'ERROR' || res.level === 'FATAL' ? 'dot-err' : res.level === 'WARN' ? 'dot-warn' : 'dot-ok'}`} />{res.level}</td>
                            <td className="log-mono">{res.service || '—'}</td>
                            <td className="log-mono" style={{ overflow: 'hidden', textOverflow: 'ellipsis', maxWidth: 380, whiteSpace: 'nowrap' }}>{res.summary}</td>
                          </>
                        )}
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
              {parserResults === null && !parserErr && (
                <div style={{ fontSize: 12, color: 'var(--text-dim)', marginTop: 4 }}>
                  点「测试解析」后, 这里会显示按规则提取出的 时间/级别/服务 — 写对了出正常结果, 正则错了会红字提示或提取不到。
                </div>
              )}
            </div>

            <div className="kib-modal-actions">
              <button className="kib-btn kib-btn-bare" onClick={() => setParserOpen(false)}>关闭</button>
              <button className="kib-btn kib-btn-bare" onClick={fillDefaultRules}>填入内置默认</button>
              <button className="kib-btn" onClick={testParser} disabled={parserBusy}>测试解析</button>
              <button className="kib-btn kib-btn-primary" onClick={saveParsers} disabled={parserBusy}>保存并生效</button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}