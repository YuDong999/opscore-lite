// 数据库管理模块主入口（P0 改造：标签页工作台）。
// 布局: 左侧 = ConnectionTree(连接→库→表/视图 懒加载树) ; 右侧 = 多标签工作台
//   标签类型: data(表数据浏览) / query(查询) / doc(表结构) / sync(跨库同步) / audit(审计)
// 右上角不再放重复的「新建连接」(入口在连接面板与概览页)。

import { useState, useEffect, useMemo, useRef } from 'react'
import { useToast } from '../components/Toast'
import {
  type ConnectionInfo, type QueryResult, type InterceptionBody,
  listConnections, getUnlockState, lockWrite, unlockWrite, exportQuery,
  listTables, fetchTableDDL,
} from '../components/DatabaseManager/api'
import ConnectionPanel from '../components/DatabaseManager/ConnectionPanel'
import ConnectionTree from '../components/DatabaseManager/ConnectionTree'
import { ActionIcon } from '../components/DatabaseManager/DbIcons'
import DocPanel from '../components/DatabaseManager/DocPanel'
import QueryEditor from '../components/DatabaseManager/QueryEditor'
import DataGrid from '../components/DatabaseManager/DataGrid'
import DataPanel from '../components/DatabaseManager/DataPanel'
import OverviewPanel from '../components/DatabaseManager/OverviewPanel'
import QuickOpen from '../components/DatabaseManager/QuickOpen'
import SyncPanel from '../components/DatabaseManager/SyncPanel'
import ErGraphPanel from '../components/DatabaseManager/ErGraphPanel'
import TableOverviewPanel from '../components/DatabaseManager/TableOverviewPanel'
import ServerDashboardPanel from '../components/DatabaseManager/ServerDashboardPanel'
import AuditPanel from '../components/DatabaseManager/AuditPanel'
import DriverManagement from '../components/DatabaseManager/DriverManagement'
import SlowSQLPanel from '../components/DatabaseManager/SlowSQLPanel'
import TableStatusPanel from '../components/DatabaseManager/TableStatusPanel'
import ExplainPanel from '../components/DatabaseManager/ExplainPanel'
import SavedQueriesPanel from '../components/DatabaseManager/SavedQueriesPanel'

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
  const [lastSQL, setLastSQL] = useState('')
  const [result, setResult] = useState<QueryResult | null>(null)
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
  const handleResult = (r: QueryResult & InterceptionBody) => {
    setResult(r)
    if (r.code === 'write_locked') {
      setUnlockState(s => ({ ...s, unlocked: false, remainingSec: 0 }))
      setShowUnlock(true)
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
              <div className="db-main-tabs db-worktabs" role="tablist" aria-label="工作区标签">
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
                    return (
                      <div className="db-query-section" key={t.key}>
                        <QueryEditor
                          connId={c!.id}
                          engine={c!.engine}
                          db={t.db}
                          defaultSQL={isSeedTab ? seedTab.sql : undefined}
                          onResult={handleResult}
                          onWriteLocked={() => setShowUnlock(true)}
                          onExecuted={setLastSQL}
                        />
                        {result && <DataGrid result={result} connId={c!.id} sql={lastSQL} />}
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
                    return <div className="db-explain-section" key={t.key}><ExplainPanel connId={t.connId} sql={explainSqlRef.current || lastSQL} /></div>
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
