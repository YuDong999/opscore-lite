// 数据库管理模块主入口（P0 改造：标签页工作台）。
// 布局: 左侧 = ConnectionTree(连接→库→表/视图 懒加载树) ; 右侧 = 多标签工作台
//   标签类型: data(表数据浏览) / query(查询) / doc(表结构) / sync(跨库同步) / audit(审计)
// 右上角不再放重复的「新建连接」(入口在连接面板与概览页)。

import { useState, useEffect, useMemo, useRef, useCallback } from 'react'
import { useToast } from '../../components/Toast'
import {
  type ConnectionInfo, type QueryResult, type InterceptionBody,
  listConnections, getUnlockState, lockWrite, unlockWrite, exportQuery, runQueryRaw,
  listTables, fetchTableDDL, describeTable, applyCellEdit,
} from './api'
import ConnectionPanel from './ConnectionPanel'
import ConnectionTree from './ConnectionTree'
import { ActionIcon } from './DbIcons'
import DocPanel from './DocPanel'
import QueryEditor from './QueryEditor'
import DataGrid from '../../components/common/DataGrid'
import DataPanel from './DataPanel'
import OverviewPanel from './OverviewPanel'
import QuickOpen from './QuickOpen'
import SyncPanel from './SyncPanel'
import ErGraphPanel from './ErGraphPanel'
import TableOverviewPanel from './TableOverviewPanel'
import ServerDashboardPanel from './ServerDashboardPanel'
import AuditPanel from './AuditPanel'
import DriverManagement from './DriverManagement'
import SlowSQLPanel from './SlowSQLPanel'
import TableStatusPanel from './TableStatusPanel'
import ExplainPanel from './ExplainPanel'
import SavedQueriesPanel from './SavedQueriesPanel'

interface WorkTab {
  key: string          // data:cid.db.table / query:cid / doc:cid.db.table / sync / audit / drivers / slow / status / explain / queries
  kind: 'data' | 'query' | 'doc' | 'sync' | 'audit' | 'drivers' | 'slow' | 'status' | 'explain' | 'queries' | 'er' | 'overview' | 'dash'
  connId: string
  db?: string
  table?: string
  schema?: string
  isView?: boolean
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
  const handleOpenTable = (c: ConnectionInfo, db: string, table: string, isView?: boolean) => {
    setConn(c)
    openTab({ key: `data:${c.id}.${db}.${table}`, kind: 'data', connId: c.id, db, table, isView, label: tabLabel(c, db, table) })
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

  const handleOpenDash = (c: ConnectionInfo) => {
    setConn(c)
    openTab({ key: `dash:${c.id}`, kind: 'dash', connId: c.id, label: '服务器仪表盘' })
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
      if (sqls.length && !window.confirm('将执行以下语句:\n\n' + sqls.join('\n\n'))) return
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

  return (
    <div className="module db-module">
      <div className="module-head db-module-head">
        <h2>数据库管理</h2>
        <div className="db-head-global-actions">
          <button className="btn-glass-soft btn-glass-soft-sm" title="驱动管理" onClick={() => openTab({ key: 'drivers', kind: 'drivers', connId: '', label: '驱动管理' })}>驱动</button>
          <button className="btn-glass-soft btn-glass-soft-sm" title="保存的查询" onClick={() => openTab({ key: 'queries', kind: 'queries', connId: '', label: '保存的查询' })}>查询</button>
          {conn && (
            <button className="btn-glass-soft btn-glass-soft-sm" title="慢 SQL" onClick={() => openTab({ key: `slow:${conn.id}`, kind: 'slow', connId: conn.id, label: '慢 SQL' })}>慢 SQL</button>
          )}
          {conn && (
            <button className="btn-glass-soft btn-glass-soft-sm" title="执行计划" onClick={() => openTab({ key: `explain:${conn.id}`, kind: 'explain', connId: conn.id, label: '执行计划' })}>执行计划</button>
          )}
          <button className="btn-glass-soft btn-glass-soft-sm" title="审计日志" onClick={() => openTab({ key: `audit:${conn?.id || 'all'}`, kind: 'audit', connId: conn?.id || '', label: '全局审计' })}>审计</button>
          {conn && (
            <>
              <button className="btn-glass-soft btn-glass-soft-sm" title="跨库同步" onClick={() => openTab({ key: `sync:${conn.id}`, kind: 'sync', connId: conn.id, label: '跨库同步' })}>同步</button>
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
              onOpenEr={handleOpenEr}
              onOpenDash={handleOpenDash}
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
                    <span className="db-worktab-kind">{t.kind === 'data' ? '表' : t.kind === 'query' ? 'SQL' : t.kind === 'doc' ? 'DDL' : t.kind === 'sync' ? '同步' : t.kind === 'audit' ? '审' : t.kind === 'drivers' ? '驱' : '查'}</span>
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
                    return <DataPanel key={t.key} conn={c!} database={t.db!} table={t.table!} isView={t.isView} />
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
                        {qs.result && <DataGrid result={qs.result} connId={c!.id} sql={qs.lastSQL} onEdit={(changes) => handleQueryEditChanges(changes, c!, t.db, t.key)} backend={{ onExport: (sql, format) => exportQuery(c!.id, sql, format), runWrite: sql => runQueryRaw(c!.id, sql) }} emptyState={{ hint: '查询返回 0 行 —— 检查 WHERE 条件' }} />}
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
                  case 'audit':
                    return <div className="db-audit-section" key={t.key}><AuditPanel conns={conns} /></div>
                  case 'drivers':
                    return <div className="db-driver-section" key={t.key}><DriverManagement /></div>
                  case 'slow':
                    return <div className="db-slow-section" key={t.key}><SlowSQLPanel connId={t.connId} /></div>
                  case 'status':
                    return <div className="db-status-section" key={t.key}><TableStatusPanel connId={t.connId} database={t.db!} table={t.table!} /></div>
                  case 'explain':
                    return <div className="db-explain-section" key={t.key}><ExplainPanel connId={t.connId} sql={explainSqlRef.current || lastSQLRef.current} /></div>
                  case 'queries':
                    return <div className="db-queries-section" key={t.key}><SavedQueriesPanel conns={conns} activeConn={activeConn} /></div>
                  default:
                    return null
                }
              })}
            </>
          )}
        </main>
      </div>
    </div>
  )
}
