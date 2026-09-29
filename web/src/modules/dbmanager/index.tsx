// 数据库管理模块主入口（P0 改造：标签页工作台）。
// 布局: 左侧 = ConnectionTree(连接→库→表/视图 懒加载树) ; 右侧 = 多标签工作台
//   标签类型: data(表数据浏览) / query(查询) / doc(表结构) / sync(跨库同步) / audit(审计)
// 右上角不再放重复的「新建连接」(入口在连接面板与概览页)。

import { useState, useEffect, useMemo, useRef, useCallback } from 'react'
import { useToast } from '../../components/Toast'
import {
  type ConnectionInfo, type QueryResult, type InterceptionBody,
  listConnections, getUnlockState, lockWrite, unlockWrite, exportQuery,
  listTables, fetchTableDDL, describeTable, applyCellEdit, applyBatch, getEngineMeta,
} from './api'
import ConnectionPanel from './ConnectionPanel'
import ConnectionTree from './ConnectionTree'
import { ActionIcon } from './DbIcons'
import DocPanel from './DocPanel'
import QueryEditor from './QueryEditor'
import DataGrid from '../../components/common/DataGrid'
import DataPanel, { type TableFilter } from './DataPanel'
import OverviewPanel from './OverviewPanel'
import QuickOpen from './QuickOpen'
import { useConfirm } from '../../lib/hooks/useConfirm'
import { humanizeDbError } from './dbErrors'
import { precheckChanges } from './batchWrite'
import { SqlPreviewBody } from '../../components/common/SqlPreview'
import SyncPanel from './SyncPanel'
import SchemaDiffPanel from './SchemaDiffPanel'
import DataDiffPanel from './DataDiffPanel'
import MqPanel from './MqPanel'
import RedisPanel from './RedisPanel'
import ErGraphPanel from './ErGraphPanel'
import TableOverviewPanel from './TableOverviewPanel'
import ServerDashboardPanel from './ServerDashboardPanel'
import AuditPanel from './AuditPanel'
import DriverManagement from './DriverManagement'
import SlowSQLPanel from './SlowSQLPanel'
import ProcessListPanel from './ProcessListPanel'
import TableStatusPanel from './TableStatusPanel'
import ExplainPanel from './ExplainPanel'
import SavedQueriesPanel from './SavedQueriesPanel'
import BackupPanel from './BackupPanel'

interface WorkTab {
  key: string          // data:cid.db.table / query:cid / doc:cid.db.table / sync / audit / drivers / slow / status / explain / queries
  kind: 'data' | 'query' | 'doc' | 'sync' | 'schemadiff' | 'datadiff' | 'audit' | 'drivers' | 'slow' | 'status' | 'explain' | 'queries' | 'er' | 'overview' | 'dash' | 'procs' | 'mq' | 'redis' | 'backup'
  connId: string
  db?: string
  table?: string
  schema?: string
  isView?: boolean
  initialFilters?: TableFilter[]  // 外键跳转带入的过滤条件; 有值时 tab key 附条件指纹, 避免复用旧 tab 的过滤态
  label: string
}

function formatRemaining(sec: number): string {
  if (sec <= 0) return '已锁定'
  const m = Math.floor(sec / 60)
  const s = sec % 60
  return m > 0 ? `${m}m ${s}s` : `${s}s`
}

export default function DatabaseManagerModule() {
  const toast = useToast()
  const { confirm, confirmEl } = useConfirm()
  const [conns, setConns] = useState<ConnectionInfo[]>([])
  const [conn, setConn] = useState<ConnectionInfo | null>(null)
  const [tabs, setTabs] = useState<WorkTab[]>([])
  const [activeTab, setActiveTab] = useState('')
  // 标签带: 活动标签滚入视野 + 非被动滚轮转横向浏览(dbx 同款; 标签带已 overflow-x:auto)
  const tabStripRef = useRef<HTMLDivElement>(null)
  const tabRailRef = useRef<HTMLDivElement>(null)
  const tabThumbRef = useRef<HTMLDivElement>(null)
  const [stripScroll, setStripScroll] = useState({ scrollWidth: 1, clientWidth: 1, left: 0 })
  const updateStripScroll = useCallback(() => {
    const el = tabStripRef.current
    if (el) setStripScroll({ scrollWidth: el.scrollWidth, clientWidth: el.clientWidth, left: el.scrollLeft })
  }, [])
  useEffect(() => {
    const el = tabStripRef.current
    if (!el) return
    const on = () => updateStripScroll()
    el.addEventListener('scroll', on, { passive: true })
    const ro = new ResizeObserver(on)
    ro.observe(el)
    on()
    return () => { el.removeEventListener('scroll', on); ro.disconnect() }
  }, [updateStripScroll, tabs.length])
  useEffect(() => {
    tabStripRef.current?.querySelector('.db-worktab.active')?.scrollIntoView({ behavior: 'smooth', block: 'nearest', inline: 'center' })
    updateStripScroll()
  }, [activeTab, tabs.length, updateStripScroll])
  // 滑块拖拽: 指针位移 × (内容宽/轨宽) → 标签带滚动
  const thumbDrag = useRef<{ startX: number; startLeft: number; ratio: number } | null>(null)
  const onThumbDown = (e: React.PointerEvent<HTMLDivElement>) => {
    ;(window as any).__dbg = Object.assign({ down: 0, move: 0, assign: 0, val: -1 }, (window as any).__dbg)
    ;(window as any).__dbg.down++
    e.preventDefault()
    e.currentTarget.setPointerCapture(e.pointerId)
    const railW = tabRailRef.current?.clientWidth || 1
    thumbDrag.current = { startX: e.clientX, startLeft: stripScroll.left, ratio: stripScroll.scrollWidth / railW }
  }
  const onThumbMove = (e: React.PointerEvent<HTMLDivElement>) => {
    const dbg = (window as any).__dbg
    if (dbg) dbg.move++
    const d = thumbDrag.current
    const el = tabStripRef.current
    if (!d || !el) return
    el.scrollLeft = Math.max(0, Math.min(stripScroll.scrollWidth - stripScroll.clientWidth, d.startLeft + (e.clientX - d.startX) * d.ratio))
    if (dbg) { dbg.assign++; dbg.val = el.scrollLeft }
  }
  const onThumbUp = (e: React.PointerEvent<HTMLDivElement>) => {
    thumbDrag.current = null
    e.currentTarget.releasePointerCapture(e.pointerId)
  }
  useEffect(() => {
    const el = tabStripRef.current
    if (!el) return
    const onWheel = (e: WheelEvent) => {
      if (Math.abs(e.deltaY) > Math.abs(e.deltaX) && el.scrollWidth > el.clientWidth) {
        el.scrollLeft += e.deltaY
        e.preventDefault()
      }
    }
    el.addEventListener('wheel', onWheel, { passive: false })
    return () => el.removeEventListener('wheel', onWheel)
  }, [])
  // 每个查询标签独立持有自己的结果与 SQL —— 修复"跨标签串台"
  // （此前 result/lastSQL 是模块级单份 state：在 A 库标签执行，切到 B 库标签仍显示 A 的结果）
  const [queryState, setQueryState] = useState<Record<string, { result: QueryResult | null; lastSQL: string }>>({})
  const getQS = (key: string) => queryState[key] || { result: null as QueryResult | null, lastSQL: '' }
  const lastSQLRef = useRef('')
  const setQS = (key: string, patch: Partial<{ result: QueryResult | null; lastSQL: string }>) => {
    if (patch.lastSQL != null) lastSQLRef.current = patch.lastSQL
    setQueryState(prev => ({ ...prev, [key]: { result: null, lastSQL: '', ...prev[key], ...patch } }))
  }
  const [unlockState, setUnlockState] = useState<{ unlocked: boolean; remainingSec: number; maxMinutes: number }>({ unlocked: false, remainingSec: 0, maxMinutes: 30 })
  const [showUnlock, setShowUnlock] = useState(false)
  const [showQuickOpen, setShowQuickOpen] = useState(false)
  // 侧栏收纳状态(localStorage 记忆, 对齐 dbx/goNavi 的侧栏折叠)
  const [sideCollapsed, setSideCollapsed] = useState<boolean>(() => {
    try { return localStorage.getItem('dbmanager:side-collapsed') === '1' } catch { return false }
  })
  const toggleSide = () => setSideCollapsed(v => {
    const next = !v
    try { localStorage.setItem('dbmanager:side-collapsed', next ? '1' : '0') } catch { /* ignore */ }
    return next
  })

  useEffect(() => {
    listConnections().then(setConns).catch(() => setConns([]))
  }, [conn?.id])

  // Ctrl+K 快速打开
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault()
        setShowQuickOpen(true)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  // 解锁状态轮询
  useEffect(() => {
    if (!conn) return
    let alive = true
    const tick = () => {
      getUnlockState(conn.id).then(s => { if (alive) setUnlockState(s) }).catch(() => {})
    }
    tick()
    const t = setInterval(tick, 15000)
    return () => { alive = false; clearInterval(t) }
  }, [conn])

  const connById = useMemo(() => new Map(conns.map(c => [c.id, c])), [conns])
  const activeConn = conn ? connById.get(conn.id) ?? conn : null

  const openTab = (t: WorkTab) => {
    setTabs(prev => prev.some(x => x.key === t.key)
      ? prev.map(x => x.key === t.key ? { ...x, db: t.db, table: t.table, schema: t.schema } : x)
      : [...prev, t])
    setActiveTab(t.key)
  }

  const closeTab = (key: string) => {
    setTabs(prev => {
      const idx = prev.findIndex(t => t.key === key)
      const next = prev.filter(t => t.key !== key)
      if (activeTab === key && next.length > 0) {
        setActiveTab(next[Math.max(0, idx - 1)].key)
      } else if (next.length === 0) {
        setActiveTab('')
      }
      return next
    })
  }

  const tabLabel = (c: ConnectionInfo, db: string, table?: string) =>
    table ? `${table}@${db}` : db ? `查询@${db}` : c.name

  // 跨标签传递的种子数据(查询模板/执行计划 SQL/同步预填) — 记录目标标签 key, 精确投递到那一个标签
  const querySeedRef = useRef<{ key: string; sql: string } | null>(null)
  const explainSqlRef = useRef<string>('')

  // ── 树交互 ──
  const handleOpenTable = (c: ConnectionInfo, db: string, table: string, isView?: boolean, filters?: TableFilter[]) => {
    setConn(c)
    // 带条件时把条件编进 tab key: 同表不同键值 = 各自独立 tab(否则复用旧 tab 会串过滤条件),
    // 同键值重复跳 = 回到同一个 tab。等价于 dbx 的 forceNew + whereInput 落 tab。
    const suffix = filters?.length ? `?${filters.map(f => `${f.col}=${f.value}`).join('&')}` : ''
    openTab({ key: `data:${c.id}.${db}.${table}${suffix}`, kind: 'data', connId: c.id, db, table, isView, initialFilters: filters, label: tabLabel(c, db, table) })
  }
  const handleNewQuery = (c: ConnectionInfo, db: string) => {
    setConn(c)
    openTab({ key: `query:${c.id}.${db}`, kind: 'query', connId: c.id, db, label: tabLabel(c, db) })
  }
  const handleOpenDoc = (c: ConnectionInfo, db: string, table: string) => {
    setConn(c)
    openTab({ key: `doc:${c.id}.${db}.${table}`, kind: 'doc', connId: c.id, db, table, label: `${table} 结构` })
  }
  const handleOpenStatus = (c: ConnectionInfo, db: string, table: string) => {
    setConn(c)
    openTab({ key: `status:${c.id}.${db}.${table}`, kind: 'status', connId: c.id, db, table, label: `${table} 状态` })
  }
  const handleOpenExplain = (c: ConnectionInfo, db: string, table: string) => {
    setConn(c)
    explainSqlRef.current = `SELECT * FROM ${db}.${table} LIMIT 100`
    openTab({ key: `explain:${c.id}.${db}.${table}`, kind: 'explain', connId: c.id, db, table, label: `${table} 执行计划` })
  }
  const handleNewQueryWithSQL = (c: ConnectionInfo, db: string, sql: string) => {
    setConn(c)
    const key = `query:${c.id}.${db}.${Date.now()}`
    querySeedRef.current = { key, sql }
    openTab({ key, kind: 'query', connId: c.id, db, label: `${db} 查询` })
  }
  const handleExportTable = (c: ConnectionInfo, db: string, table: string, format: 'csv' | 'xlsx') => {
    exportQuery(c.id, `SELECT * FROM ${db}.${table}`, format)
      .then(({ fileName }) => toast.success(`已导出 ${fileName}`))
      .catch(e => toast.error('导出失败: ' + e.message))
  }
  const openSyncTab = (seed: { connId: string; db: string; table?: string; schema?: string }) => {
    const c = conns.find(x => x.id === seed.connId)
    if (c) setConn(c)
    openTab({ key: `sync:${seed.connId}`, kind: 'sync', connId: seed.connId, db: seed.db, table: seed.table, schema: seed.schema, label: '跨库同步' })
  }
  const handleSyncDb = (c: ConnectionInfo, db: string) => openSyncTab({ connId: c.id, db })
  const handleSyncTable = (c: ConnectionInfo, db: string, table: string) => openSyncTab({ connId: c.id, db, table })
  const handleSyncSchema = (c: ConnectionInfo, db: string, schema: string) => openSyncTab({ connId: c.id, db, schema })
  // 结构对比入口: 与同步一样按"入口层级"预置 —— 库级比整库, 表级把这张表带进范围
  const openDiffTab = (seed: { connId: string; db: string; table?: string }) => {
    const c = conns.find(x => x.id === seed.connId)
    if (c) setConn(c)
    openTab({
      key: `schemadiff:${seed.connId}:${seed.db}:${seed.table || ''}`, kind: 'schemadiff',
      connId: seed.connId, db: seed.db, table: seed.table, label: seed.table ? `结构对比 ${seed.table}` : '结构对比',
    })
  }
  const handleDiffDb = (c: ConnectionInfo, db: string) => openDiffTab({ connId: c.id, db })
  const handleDiffTable = (c: ConnectionInfo, db: string, table: string) => openDiffTab({ connId: c.id, db, table })
  // 数据对比入口: 与结构对比同一个层级口径(库级自己勾选表范围, 表级把这张表带进去)
  const openDataDiffTab = (seed: { connId: string; db: string; table?: string }) => {
    const c = conns.find(x => x.id === seed.connId)
    if (c) setConn(c)
    openTab({
      key: `datadiff:${seed.connId}:${seed.db}:${seed.table || ''}`, kind: 'datadiff',
      connId: seed.connId, db: seed.db, table: seed.table, label: seed.table ? `数据对比 ${seed.table}` : '数据对比',
    })
  }
  const handleDataDiffDb = (c: ConnectionInfo, db: string) => openDataDiffTab({ connId: c.id, db })
  const handleDataDiffTable = (c: ConnectionInfo, db: string, table: string) => openDataDiffTab({ connId: c.id, db, table })

  const handleOpenDash = (c: ConnectionInfo) => {
    setConn(c)
    openTab({ key: `dash:${c.id}`, kind: 'dash', connId: c.id, label: '服务器仪表盘' })
  }
  // MQ 面板一个连接一个页签; 从 topic 节点右键进来时带上要预选的名字
  const mqSeedRef = useRef<{ key: string; name: string } | null>(null)
  // Redis 面板: 一个连接一个页签(键列表 + 值 + 键级写操作)。从树里点键进来时预选那个键。
  const redisSeedRef = useRef<{ key: string; name: string } | null>(null)
  const handleOpenRedis = (c: ConnectionInfo, db: string, keyName?: string) => {
    setConn(c)
    const key = `redis:${c.id}.${db}`
    if (keyName) redisSeedRef.current = { key, name: keyName }
    openTab({ key, kind: 'redis', connId: c.id, db, label: keyName ? `${keyName}` : `Redis ${db}` })
  }
  // 备份面板: 一个连接一个页签。它是"对整库的操作", 不是对某个对象, 所以从连接级进入。
  const handleOpenBackup = (c: ConnectionInfo, db?: string) => {
    setConn(c)
    openTab({ key: `backup:${c.id}`, kind: 'backup', connId: c.id, db, label: '备份' })
  }
  const handleOpenMq = (c: ConnectionInfo, topic?: string) => {
    setConn(c)
    const key = `mq:${c.id}`
    if (topic) mqSeedRef.current = { key, name: topic }
    openTab({ key, kind: 'mq', connId: c.id, label: topic ? `${topic} 消息` : '消息队列' })
  }
  const handleOpenOverview = (c: ConnectionInfo, db: string) => {
    setConn(c)
    openTab({ key: `overview:${c.id}:${db}`, kind: 'overview', connId: c.id, db, label: `表概览 ${db}` })
  }
  const handleNewTable = (c: ConnectionInfo, db: string) => {
    const NL = String.fromCharCode(10)
    handleNewQueryWithSQL(c, db, `CREATE TABLE ${db}.new_table (${NL}  id INT PRIMARY KEY AUTO_INCREMENT,${NL}  name VARCHAR(255) NOT NULL,${NL}  created_at DATETIME DEFAULT CURRENT_TIMESTAMP${NL});`)
  }
  const handleExportSchema = (c: ConnectionInfo, db: string) => {
    setConn(c)
    toast.success(`正在导出 ${db} 全部表结构...`)
    ;(async () => {
      const ts = await listTables(c.id, db)
      const parts: string[] = [`-- ${db} 表结构导出
-- ${new Date().toLocaleString()}
`]
      for (const t of ts.filter(x => x.type !== 'VIEW')) {
        try {
          const ddl = await fetchTableDDL(c.id, db, t.name)
          parts.push(`
-- ── ${t.name} ──
${ddl};
`)
        } catch { /* 单表失败跳过 */ }
      }
      const blob = new Blob([parts.join('\n')], { type: 'text/sql' })
      const a = document.createElement('a')
      a.href = URL.createObjectURL(blob)
      a.download = `${db}-schema.sql`
      a.click()
      URL.revokeObjectURL(a.href)
      toast.success(`已导出 ${ts.filter(x => x.type !== 'VIEW').length} 张表结构`)
    })()
  }
  const handleOpenEr = (c: ConnectionInfo, db: string, table?: string) => {
    setConn(c)
    openTab({ key: `er:${c.id}:${db}`, kind: 'er', connId: c.id, db, table, label: table ? `ER ${table}` : 'ER 关系图' })
  }
  const handleSelectConn = (c: ConnectionInfo) => { setConn(c) }

  // 新建/编辑连接通过自定义事件交给 ConnectionPanel (其内部用 portal 渲染向导浮层)
  const handleNewConn = () => {
    window.dispatchEvent(new CustomEvent('dbmanager:new-conn'))
  }
  const handleEditConn = (c: ConnectionInfo) => {
    setConn(c)
    window.dispatchEvent(new CustomEvent('dbmanager:edit-conn', { detail: c }))
  }

  // 解锁/锁定
  const onUnlock = async (minutes: number) => {
    if (!conn) return
    try {
      const r = await unlockWrite(conn.id, minutes)
      setUnlockState(s => ({ ...s, unlocked: true, remainingSec: r.remainingSec }))
      toast.success(`已解锁 ${minutes} 分钟`)
      setShowUnlock(false)
    } catch (e: any) {
      toast.error('解锁失败: ' + e.message)
    }
  }
  const onLock = async () => {
    if (!conn) return
    try {
      await lockWrite(conn.id)
      setUnlockState(s => ({ ...s, unlocked: false, remainingSec: 0 }))
      toast.success('已锁定')
    } catch (e: any) {
      toast.error('锁定失败: ' + e.message)
    }
  }
  const handleResult = (r: QueryResult & InterceptionBody, tabKey: string) => {
    setQS(tabKey, { result: r })
    if (r.code === 'write_locked') {
      setUnlockState(s => ({ ...s, unlocked: false, remainingSec: 0 }))
      setShowUnlock(true)
    }
  }

  // ── 查询页行内编辑: 解析目标表 → describe 取主键 → 预览确认 → 执行 → 原位更新结果 ──
  // 仅支持单表 SELECT(QueryEditor 的 isEditable 判定已先行过滤); 无主键表后端同样拒绝。
  // 从查询结果里解析"改哪张表"(与下面单条编辑同一套规则: 只认单表 SELECT + 库前缀/默认库)
  const resolveQueryTarget = useCallback((qs: { lastSQL?: string }, c: ConnectionInfo, tabDb?: string) => {
    const m = /FROM\s+([`"\[\]\w.]+)/i.exec(qs.lastSQL || '')
    if (!m) return { error: '无法定位目标表: 行内编辑仅支持单表 SELECT' }
    const parts = m[1].replace(/[`"\[\]]/g, '').split('.').filter(Boolean)
    const database = parts.length >= 2 ? parts[0] : (tabDb || c.config?.database || '')
    const table = parts.length >= 2 ? parts[parts.length - 1] : parts[0]
    if (!database || !table) return { error: '无法定位目标库表(表名未带库前缀且连接未指定默认库)' }
    return { database, table }
  }, [])

  // 查询结果页也走"待提交变更"(与表数据页一致): 整批一个事务, 预览→确认→提交
  // 注意: 故意**不**用 useCallback —— 它要读 queryState(结果集), 记忆化会捕获到"查询之前"的空快照,
  // 表现为一点保存就报"结果集未就绪"(实测踩过)。与旁边的单条编辑同一写法。
  const handleQueryBatch = async (
    changes: import('../../components/common/DataGrid').GridChange[],
    c: ConnectionInfo, tabDb?: string, tabKey?: string,
  ): Promise<{ ok: boolean; cancelled?: boolean; error?: string; affected?: number; badCells?: Array<{ row: number; col: number }> }> => {
    const qs = getQS(tabKey || activeTab)
    if (!qs.result?.columns || !qs.result.rows) return { ok: false, error: '结果集未就绪' }
    const t = resolveQueryTarget(qs, c, tabDb)
    if (t.error) return { ok: false, error: t.error }
    const { database, table } = t as { database: string; table: string }
    try {
      const d = await describeTable(c.id, database, table)
      const meta = d.columns || []
      const pkCols = meta.filter(x => x.key === 'PRI').map(x => x.name)
      if (pkCols.length === 0 && changes.some(ch => ch.kind !== 'insert')) {
        return { ok: false, error: `表 ${database}.${table} 无主键, 无法安全定位行` }
      }
      const pre = precheckChanges(changes, qs.result.columns, meta)
      if (pre.error) return { ok: false, error: pre.error, badCells: pre.badCells }
      const rowObjAt = (ri: number) => {
        const obj: Record<string, any> = {}
        qs.result!.columns!.forEach((name, i) => { obj[name] = qs.result!.rows![ri]?.[i] ?? null })
        return obj
      }
      const ops = changes.map(ch => ch.kind === 'insert'
        ? { kind: 'insert' as const, values: ch.values || {} }
        : ch.kind === 'delete'
          ? { kind: 'delete' as const, row: rowObjAt(ch.row!) }
          : { kind: 'update' as const, row: rowObjAt(ch.row!), setCol: qs.result!.columns![ch.col!], setValue: ch.value })
      const preview = await applyBatch(c.id, database, table, pkCols, ops, false)
      if (!preview.ok) return { ok: false, error: preview.error || '生成预览失败' }
      if (!(await confirm(`将提交 ${ops.length} 处变更 · 同一事务 · ${database}.${table}`, {
        content: <SqlPreviewBody sqls={preview.sqls || []} caption={`${ops.length} 处变更`} />,
        okText: '提交', danger: true, maxWidth: 680,
      }))) return { ok: true, cancelled: true }
      const r = await applyBatch(c.id, database, table, pkCols, ops, true)
      if (!r.ok) {
        const bad = r.failedAt && changes[r.failedAt - 1]?.col !== undefined
          ? [{ row: changes[r.failedAt - 1].row!, col: changes[r.failedAt - 1].col! }]
          : undefined
        return { ok: false, error: humanizeDbError(r.error || '提交失败'), badCells: bad }
      }
      // 结果集原位刷新(与单条编辑同一目标): 改值/删行直接落到本地结果;
      // 新增行要看库生成的默认值/自增号, 只能重新执行查询 —— 提示一句, 不假装刷过
      setQueryState(prev => {
        const key = tabKey || activeTab
        const cur = prev[key]
        if (!cur?.result?.rows) return prev
        const upRow = new Map<number, Array<{ col: number; newValue: any }>>()
        const delRows = new Set<number>()
        changes.forEach(ch => {
          if (ch.kind === 'update') { const arr = upRow.get(ch.row!) || []; arr.push({ col: ch.col!, newValue: ch.value }); upRow.set(ch.row!, arr) }
          else if (ch.kind === 'delete') delRows.add(ch.row!)
        })
        const rows = cur.result.rows
          .map((row, ri) => { const chs = upRow.get(ri); if (!chs) return row; const next = [...row]; chs.forEach(x => { next[x.col] = x.newValue }); return next })
          .filter((_, ri) => !delRows.has(ri))
        return { ...prev, [key]: { ...cur, result: { ...cur.result, rows, rowCount: rows.length } } }
      })
      if (changes.some(ch => ch.kind === 'insert')) toast.success('新增行已提交 —— 重新执行查询即可看到(含库生成的默认值)')
      return { ok: true, affected: r.affected }
    } catch (e: any) {
      return { ok: false, error: String(e?.message || e) }
    }
  }

  const handleQueryEditChanges = async (
    changes: Array<{ row: number, col: number, newValue: any, oldValue: any }>,
    c: ConnectionInfo, tabDb?: string, tabKey?: string,
  ) => {
    const qs = getQS(tabKey || activeTab)
    if (!qs.result?.columns || !qs.result.rows) return
    const m = /FROM\s+([`"\[\]\w.]+)/i.exec(qs.lastSQL || '')
    if (!m) { toast.error('无法定位目标表: 行内编辑仅支持单表 SELECT'); return }
    const parts = m[1].replace(/[`"\[\]]/g, '').split('.').filter(Boolean)
    const database = parts.length >= 2 ? parts[0] : (tabDb || c.config?.database || '')
    const table = parts.length >= 2 ? parts[parts.length - 1] : parts[0]
    if (!database || !table) { toast.error('无法定位目标库表(表名未带库前缀且连接未指定默认库)'); return }
    try {
      const d = await describeTable(c.id, database, table)
      const pkCols = (d.columns || []).filter(x => x.key === 'PRI').map(x => x.name)
      if (pkCols.length === 0) { toast.error(`表 ${database}.${table} 无主键, 已拒绝编辑`); return }
      const rowObjAt = (ri: number) => {
        const obj: Record<string, any> = {}
        qs.result!.columns!.forEach((name, i) => { obj[name] = qs.result!.rows![ri]?.[i] ?? null })
        return obj
      }
      // 第一刀 confirm=false 拿后端生成的 UPDATE 预览(透明原则), 确认后再执行
      const previews = await Promise.all(changes.map(ch =>
        applyCellEdit(c.id, database, table, pkCols, rowObjAt(ch.row), qs.result!.columns![ch.col], ch.newValue, false)
      ))
      const sqls = previews.map(p => p.sql).filter(Boolean)
      if (sqls.length && !(await confirm(`将执行以下语句 · ${database}.${table}`, { content: <SqlPreviewBody sqls={sqls} />, okText: '执行', danger: true, maxWidth: 620 }))) return
      for (const ch of changes) {
        const r = await applyCellEdit(c.id, database, table, pkCols, rowObjAt(ch.row), qs.result!.columns![ch.col], ch.newValue, true)
        if (!r.ok) throw new Error(r.error || '写入失败')
      }
      toast.success(`已更新 ${changes.length} 个单元格 (结果网格已原位刷新)`)
      const byRow = new Map<number, Array<{ col: number; newValue: any }>>()
      changes.forEach(ch => {
        const arr = byRow.get(ch.row) || []
        arr.push({ col: ch.col, newValue: ch.newValue })
        byRow.set(ch.row, arr)
      })
      setQueryState(prev => {
        const cur = prev[tabKey || activeTab] || { result: null as QueryResult | null, lastSQL: '' }
        if (!cur.result?.rows) return prev
        const rows = cur.result.rows.map((row, ri) => {
          const chs = byRow.get(ri)
          if (!chs) return row
          const next = [...row]
          chs.forEach(ch => { next[ch.col] = ch.newValue })
          return next
        })
        return { ...prev, [tabKey || activeTab]: { ...cur, result: { ...cur.result, rows } } }
      })
    } catch (e: any) {
      toast.error('更新失败: ' + (e.message || e))
    }
  }

  const isProd = useMemo(() => {
    if (!conn) return false
    const hay = (conn.name + ' ' + (conn.config.host || '') + ' ' + (conn.config.database || '')).toLowerCase()
    return conn.config.envTag === 'prod' || ['prod', 'production', '生产', '线上'].some(k => hay.includes(k))
  }, [conn])

  // MQ 连接没有 SQL/表结构/ER 这些概念, 顶部那些按钮点了只会报错 —— 按引擎类别整组收起,
  // 换成「消息队列」。能力差异的具体口径由后端 mq/capabilities 说了算。
  // hasSql=false 的引擎(RabbitMQ 的"结构对比"、Redis 的"慢 SQL"…)点开只会拿到一个报错,
  // 所以整组按"这个引擎有没有 SQL"收起; MQ 额外给一个面板入口。
  const noSqlConn = !!conn && getEngineMeta(conn.engine)?.hasSql === false
  const isMqConn = !!conn && getEngineMeta(conn.engine)?.category === 'mq'
  // Redis 是 keyvalue 类别: 面板与 MQ 同为"专用 UI", 但入口分开(动作集不同)。
  const isRedisConn = !!conn && conn.engine === 'redis'

  return (
    <div className="module db-module">
      <div className="module-head db-module-head">
        <h2>数据库管理</h2>
        <div className="db-head-global-actions">
          <button className="btn-glass-soft btn-glass-soft-sm" title="驱动管理" onClick={() => openTab({ key: 'drivers', kind: 'drivers', connId: '', label: '驱动管理' })}>驱动</button>
          <button className="btn-glass-soft btn-glass-soft-sm" title="保存的查询" onClick={() => openTab({ key: 'queries', kind: 'queries', connId: '', label: '保存的查询' })}>查询</button>
          {conn && (
            <button className="btn-glass-soft btn-glass-soft-sm" title="数据库备份(原生工具 / 内置导出)"
              onClick={() => handleOpenBackup(conn, conn.config?.database || '')}>备份</button>
          )}
          {conn && isRedisConn && (
            <button className="btn-glass-soft btn-glass-soft-sm" title="Redis 键管理(浏览 + 键级写操作)"
              onClick={() => handleOpenRedis(conn, conn.config?.database || 'db0')}>Redis</button>
          )}
          {conn && isMqConn && (
            <button className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-accent" title="消息队列管理(topic/队列/消费组/收发)"
              onClick={() => handleOpenMq(conn)}>消息队列</button>
          )}
          {conn && !noSqlConn && (
            <button className="btn-glass-soft btn-glass-soft-sm" title="进程列表(当前会话)" onClick={() => openTab({ key: `procs:${conn.id}`, kind: 'procs', connId: conn.id, label: '进程列表' })}>进程</button>
          )}
          {conn && !noSqlConn && (
            <button className="btn-glass-soft btn-glass-soft-sm" title="慢 SQL" onClick={() => openTab({ key: `slow:${conn.id}`, kind: 'slow', connId: conn.id, label: '慢 SQL' })}>慢 SQL</button>
          )}
          {conn && !noSqlConn && (
            <button className="btn-glass-soft btn-glass-soft-sm" title="执行计划" onClick={() => openTab({ key: `explain:${conn.id}`, kind: 'explain', connId: conn.id, label: '执行计划' })}>执行计划</button>
          )}
          <button className="btn-glass-soft btn-glass-soft-sm" title="审计日志" onClick={() => openTab({ key: `audit:${conn?.id || 'all'}`, kind: 'audit', connId: conn?.id || '', label: '全局审计' })}>审计</button>
          {conn && !noSqlConn && (
            <>
              <button className="btn-glass-soft btn-glass-soft-sm" title="跨库同步" onClick={() => openTab({ key: `sync:${conn.id}`, kind: 'sync', connId: conn.id, label: '跨库同步' })}>同步</button>
              <button className="btn-glass-soft btn-glass-soft-sm" title="结构对比(两侧表结构差异 + 生成变更语句)" onClick={() => openTab({ key: `schemadiff:${conn.id}:${conn.config?.database || ''}`, kind: 'schemadiff', connId: conn.id, db: conn.config?.database || '', label: '结构对比' })}>结构对比</button>
              <button className="btn-glass-soft btn-glass-soft-sm" title="数据对比(两侧行级差异 + 补/改/删)" onClick={() => openTab({ key: `datadiff:${conn.id}:${conn.config?.database || ''}`, kind: 'datadiff', connId: conn.id, db: conn.config?.database || '', label: '数据对比' })}>数据对比</button>
              <button className="btn-glass-soft btn-glass-soft-sm" title="服务器仪表盘" onClick={() => handleOpenDash(conn)}>仪表盘</button>
              <button className="btn-glass-soft btn-glass-soft-sm" disabled={!conn.config?.database} title={conn.config?.database ? 'ER 关系图' : '该连接未指定默认库, 请从树中库节点右键进入'} onClick={() => openTab({ key: `er:${conn.id}:${conn.config?.database || ''}`, kind: 'er', connId: conn.id, db: conn.config?.database || '', label: 'ER 关系图' })}>关系图</button>
            </>
          )}
        </div>
        {activeConn && (
          <div className="db-head-info">
            <span className="pill" title={activeConn.engine}>
              {activeConn.name}
            </span>
            {isProd && <span className="pill pill-err">生产</span>}
            {unlockState.unlocked ? (
              <span className="pill pill-ok" title="写操作已解锁">写 {formatRemaining(unlockState.remainingSec)}</span>
            ) : (
              <span className="pill pill-warn" title="默认只读, 写操作前需解锁">只读</span>
            )}
            {unlockState.unlocked ? (
              <button className="btn-glass-soft btn-glass-soft-sm" onClick={onLock}>立即锁定</button>
            ) : (
              <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => setShowUnlock(true)}>解锁写</button>
            )}
          </div>
        )}
      </div>

      {showUnlock && activeConn && (
        <div className="banner banner-warn" style={{ margin: '0.5rem 0' }}>
          <span style={{ flex: 1 }}>
            执行写操作前需解锁。解锁后, 写权限仅在时间窗内有效, 到期自动回落只读。
          </span>
          {[5, 10, 30].filter(m => m <= unlockState.maxMinutes).map(m => (
            <button key={m} className="btn-glass-soft btn-glass-soft-sm" onClick={() => onUnlock(m)} style={{ marginLeft: 6 }}>
              解锁 {m}m
            </button>
          ))}
          <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => setShowUnlock(false)} style={{ marginLeft: 6 }}>取消</button>
        </div>
      )}

      <QuickOpen
        conns={conns}
        open={showQuickOpen}
        onClose={() => setShowQuickOpen(false)}
        onOpenTable={handleOpenTable}
        onNewQuery={handleNewQuery}
      />
      <div className="db-layout">
        <aside className={`db-side${sideCollapsed ? ' db-side-collapsed' : ''}`}>
          {sideCollapsed ? (
            <div className="db-side-rail" title="展开侧栏">
              <button className="db-side-rail-btn" onClick={toggleSide}><ActionIcon kind="panel" size={15} /></button>
            </div>
          ) : (<>
            <ConnectionTree
              conns={conns}
              selectedConnId={conn?.id}
              onOpenTable={handleOpenTable}
              onNewQuery={handleNewQuery}
              onOpenDoc={handleOpenDoc}
              onSelectConn={handleSelectConn}
              onEditConn={handleEditConn}
              onNewConn={handleNewConn}
              onConnsChange={setConns}
              notify={(ok, msg) => { ok ? toast.success(msg) : toast.error(msg) }}
              onSyncDb={handleSyncDb}
              onSyncTable={handleSyncTable}
              onSyncSchema={handleSyncSchema}
              onDiffDb={handleDiffDb}
              onDiffTable={handleDiffTable}
              onDataDiffDb={handleDataDiffDb}
              onDataDiffTable={handleDataDiffTable}
              onOpenEr={handleOpenEr}
              onOpenDash={handleOpenDash}
              onOpenMq={handleOpenMq}
              onOpenRedis={handleOpenRedis}
              onNewTable={handleNewTable}
              onExportSchema={handleExportSchema}
              onOpenOverview={handleOpenOverview}
              onOpenStatus={handleOpenStatus}
              onOpenExplain={handleOpenExplain}
              onNewQueryWithSQL={handleNewQueryWithSQL}
              onExportTable={handleExportTable}
              onRefresh={() => listConnections().then(setConns).catch(() => {})}
              onToggleSide={toggleSide}
            />
            <ConnectionPanel
              selected={null}
              onSelect={handleSelectConn}
              onConnsChange={setConns}
            />
          </>)}
        </aside>

        <main className="db-main">
          {tabs.length === 0 ? (
            !conn ? (
              <OverviewPanel
                conns={conns}
                onNewConn={() => window.dispatchEvent(new CustomEvent('dbmanager:new-conn'))}
                onPickConn={handleSelectConn}
              />
            ) : (
              <div className="db-empty" style={{ marginTop: '3rem' }}>
                从左侧树展开连接, 单击表查看数据 / 右键更多操作
              </div>
            )
          ) : (
            <>
              <div ref={tabStripRef} className="db-main-tabs db-worktabs" role="tablist" aria-label="工作区标签"
                onWheel={e => {
                  const el = tabStripRef.current
                  if (el && Math.abs(e.deltaY) > Math.abs(e.deltaX) && el.scrollWidth > el.clientWidth) el.scrollLeft += e.deltaY
                }}>
                {tabs.map((t, i) => (
                  <div key={t.key} role="tab" aria-selected={activeTab === t.key} tabIndex={0}
                    className={`db-worktab ${activeTab === t.key ? 'active' : ''}`}
                    onClick={() => setActiveTab(t.key)} title={t.label}
                    onKeyDown={e => {
                      if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); setActiveTab(t.key) }
                      if (e.key === 'ArrowRight' || e.key === 'ArrowLeft') {
                        e.preventDefault()
                        const dir = e.key === 'ArrowRight' ? 1 : -1
                        const next = tabs[(i + dir + tabs.length) % tabs.length]
                        if (next) setActiveTab(next.key)
                      }
                    }}>
                    <span className="db-worktab-kind">{t.kind === 'data' ? '表' : t.kind === 'query' ? 'SQL' : t.kind === 'doc' ? 'DDL' : t.kind === 'sync' ? '同步' : t.kind === 'schemadiff' ? '比对' : t.kind === 'datadiff' ? '比数' : t.kind === 'audit' ? '审' : t.kind === 'drivers' ? '驱' : t.kind === 'mq' ? 'MQ' : t.kind === 'redis' ? 'KV' : t.kind === 'backup' ? '备份' : '查'}</span>
                    <span className="db-worktab-label">{t.label}</span>
                    <button type="button" className="db-worktab-close" aria-label={`关闭 ${t.label}`}
                      onClick={e => { e.stopPropagation(); closeTab(t.key) }}>×</button>
                  </div>
                ))}
              </div>
              {stripScroll.scrollWidth > stripScroll.clientWidth && (
                <div ref={tabRailRef} className="db-tab-scrollbar" role="scrollbar" aria-orientation="horizontal"
                  aria-controls="db-worktabs" aria-valuenow={Math.round(stripScroll.left)}>
                  <div ref={tabThumbRef} className="db-tab-thumb"
                    style={{
                      width: `${Math.max(8, (stripScroll.clientWidth / stripScroll.scrollWidth) * 100)}%`,
                      marginLeft: `${Math.min(100 - Math.max(8, (stripScroll.clientWidth / stripScroll.scrollWidth) * 100), (stripScroll.left / stripScroll.scrollWidth) * 100)}%`,
                    }}
                    onPointerDown={onThumbDown} onPointerMove={onThumbMove} onPointerUp={onThumbUp} onPointerCancel={onThumbUp} />
                </div>
              )}
              {tabs.map(t => {
                if (t.key !== activeTab) return null
                const c = t.connId ? connById.get(t.connId) : null
                const needsConn = !['drivers', 'audit', 'queries'].includes(t.kind)
                if (needsConn && !c) return <div className="db-empty">连接不存在, 请关闭此标签</div>
                switch (t.kind) {
                  case 'data':
                    return <DataPanel key={t.key} conn={c!} database={t.db!} table={t.table!} isView={t.isView}
                      initialFilters={t.initialFilters}
                      onOpenTable={(refTable, conds) => handleOpenTable(c!, t.db!, refTable, false, conds)} />
                  case 'query': {
                    const seedTab = querySeedRef.current
                    const isSeedTab = seedTab != null && seedTab.key === t.key
                    const qs = getQS(t.key)
                    return (
                      <div className="db-query-section" key={t.key}>
                        <QueryEditor
                          connId={c!.id}
                          engine={c!.engine}
                          db={t.db}
                          defaultSQL={isSeedTab ? seedTab.sql : undefined}
                          onResult={r => handleResult(r, t.key)}
                          onWriteLocked={() => setShowUnlock(true)}
                          onExecuted={sql => setQS(t.key, { lastSQL: sql })}
                        />
                        {qs.result && <DataGrid result={qs.result} connId={c!.id} sql={qs.lastSQL} onEdit={(changes) => handleQueryEditChanges(changes, c!, t.db, t.key)} onCommitBatch={(changes) => handleQueryBatch(changes, c!, t.db, t.key)} backend={{ onExport: (sql, format) => exportQuery(c!.id, sql, format) }} emptyState={{ hint: '查询返回 0 行 —— 检查 WHERE 条件' }} />}
                      </div>
                    )
                  }
                  case 'doc':
                    return <DocPanel key={t.key} connId={c!.id} engine={c!.engine} database={t.db!} table={t.table!} onStructureChanged={() => { listConnections().then(setConns).catch(() => {}) }} />
                  case 'er':
                    return <div className="db-doc-section" style={{ flex: 1, minHeight: 0 }} key={t.key}><ErGraphPanel connId={c!.id} database={t.db!} focusTable={t.table} /></div>
                  case 'overview':
                    return <div className="db-doc-section" style={{ flex: 1, minHeight: 0, display: 'flex' }} key={t.key}>
                      <TableOverviewPanel connId={c!.id} database={t.db!} onOpenTable={table => handleOpenTable(c!, t.db!, table)} />
                    </div>
                  case 'dash':
                    return <div className="db-doc-section" style={{ flex: 1, minHeight: 0, display: 'flex' }} key={t.key}><ServerDashboardPanel connId={c!.id} engine={c!.engine} database={t.db || c!.config?.database} /></div>
                  case 'sync':
                    return <div className="db-doc-section" key={t.key}><SyncPanel conns={conns} activeConnId={c!.id} presetDb={t.db} presetSchema={t.schema} presetTable={t.table} /></div>
                  case 'schemadiff':
                    return <div className="db-doc-section" key={t.key} style={{ overflow: 'auto' }}><SchemaDiffPanel conns={conns} activeConnId={c!.id} presetDb={t.db} presetTable={t.table} /></div>
                  case 'datadiff':
                    return <div className="db-doc-section" key={t.key} style={{ overflow: 'auto' }}><DataDiffPanel conns={conns} activeConnId={c!.id} presetDb={t.db} presetTable={t.table} /></div>
                  case 'audit':
                    return <div className="db-audit-section" key={t.key}><AuditPanel conns={conns} /></div>
                  case 'drivers':
                    return <div className="db-driver-section" key={t.key}><DriverManagement /></div>
                  case 'slow':
                    return <div className="db-slow-section" key={t.key}><SlowSQLPanel connId={t.connId} /></div>
                  case 'procs':
                    return <div className="db-slow-section" key={t.key}><ProcessListPanel connId={c!.id} engine={c!.engine} database={t.db || c!.config?.database} /></div>
                  case 'redis':
                    return <div className="db-mq-section" key={t.key} style={{ overflow: 'auto' }}>
                      <RedisPanel connId={c!.id} database={t.db || c!.config?.database || 'db0'}
                        preset={redisSeedRef.current?.key === t.key ? redisSeedRef.current?.name : undefined}
                        onWriteLocked={() => setShowUnlock(true)} />
                    </div>
                  case 'mq':
                    return <div className="db-mq-section" key={t.key} style={{ overflow: 'auto' }}><MqPanel connId={c!.id} engine={c!.engine} preset={mqSeedRef.current?.key === t.key ? mqSeedRef.current?.name : undefined} onWriteLocked={() => setShowUnlock(true)} /></div>
                  case 'status':
                    return <div className="db-status-section" key={t.key}><TableStatusPanel connId={t.connId} database={t.db!} table={t.table!} /></div>
                  case 'explain':
                    return <div className="db-explain-section" key={t.key}><ExplainPanel connId={t.connId} sql={explainSqlRef.current || lastSQLRef.current} /></div>
                  case 'backup':
                    return <div className="db-backup-section" key={t.key}>
                      <BackupPanel conns={conns} activeConn={c!} />
                    </div>
                  case 'queries':
                    return <div className="db-queries-section" key={t.key}>
                      <SavedQueriesPanel conns={conns} activeConn={activeConn}
                        onOpenQuery={(sql, name) => {
                          // 在**当前活跃连接**上开一个新查询标签并预填 SQL(复用既有的 seed 机制)。
                          // 没选连接时不硬猜 —— 保存的查询可能属于别的连接, 猜错比不打开更糟。
                          const c = activeConn || conn
                          if (!c) { toast.error('先选一个连接, 再打开这条查询'); return }
                          handleNewQueryWithSQL(c, t.db || c.config?.database || '', sql)
                          toast.info(`已在新标签打开「${name}」`)
                        }} />
                    </div>
                  default:
                    return null
                }
              })}
            </>
          )}
        </main>
      </div>
      {confirmEl}
    </div>
  )
}
