// 数据库管理模块主入口（P0 改造：标签页工作台）。
// 布局: 左侧 = ConnectionTree(连接→库→表/视图 懒加载树) ; 右侧 = 多标签工作台
//   标签类型: data(表数据浏览) / query(查询) / doc(表结构) / sync(跨库同步) / audit(审计)
// 右上角不再放重复的「新建连接」(入口在连接面板与概览页)。

import { useState, useEffect, useMemo, useRef } from 'react'
import { useToast } from '../components/Toast'
import {
  type ConnectionInfo, type QueryResult, type InterceptionBody,
  listConnections, getUnlockState, lockWrite, unlockWrite, exportQuery,
} from '../components/DatabaseManager/api'
import ConnectionPanel from '../components/DatabaseManager/ConnectionPanel'
import ConnectionTree from '../components/DatabaseManager/ConnectionTree'
import DocPanel from '../components/DatabaseManager/DocPanel'
import QueryEditor from '../components/DatabaseManager/QueryEditor'
import DataGrid from '../components/DatabaseManager/DataGrid'
import DataPanel from '../components/DatabaseManager/DataPanel'
import OverviewPanel from '../components/DatabaseManager/OverviewPanel'
import QuickOpen from '../components/DatabaseManager/QuickOpen'
import SyncPanel from '../components/DatabaseManager/SyncPanel'
import AuditPanel from '../components/DatabaseManager/AuditPanel'
import DriverManagement from '../components/DatabaseManager/DriverManagement'
import SlowSQLPanel from '../components/DatabaseManager/SlowSQLPanel'
import TableStatusPanel from '../components/DatabaseManager/TableStatusPanel'
import ExplainPanel from '../components/DatabaseManager/ExplainPanel'
import SavedQueriesPanel from '../components/DatabaseManager/SavedQueriesPanel'

interface WorkTab {
  key: string          // data:cid.db.table / query:cid / doc:cid.db.table / sync / audit / drivers / slow / status / explain / queries
  kind: 'data' | 'query' | 'doc' | 'sync' | 'audit' | 'drivers' | 'slow' | 'status' | 'explain' | 'queries'
  connId: string
  db?: string
  table?: string
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
    setTabs(prev => prev.some(x => x.key === t.key) ? prev : [...prev, t])
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

  // 跨标签传递的种子数据(查询模板/执行计划 SQL/同步预填)
  const querySeedRef = useRef<string>('')
  const explainSqlRef = useRef<string>('')
  const syncSeedRef = useRef<{ connId: string; db: string } | null>(null)

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
    querySeedRef.current = sql
    openTab({ key: `query:${c.id}.${db}.${Date.now()}`, kind: 'query', connId: c.id, db, label: `${db} 查询` })
  }
  const handleExportTable = (c: ConnectionInfo, db: string, table: string, format: 'csv' | 'xlsx') => {
    exportQuery(c.id, `SELECT * FROM ${db}.${table}`, format)
      .then(({ fileName }) => toast.success(`已导出 ${fileName}`))
      .catch(e => toast.error('导出失败: ' + e.message))
  }
  const handleSyncDb = (c: ConnectionInfo, db: string) => {
    setConn(c)
    syncSeedRef.current = { connId: c.id, db }
    openTab({ key: `sync:${c.id}`, kind: 'sync', connId: c.id, label: '跨库同步' })
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
            <button className="btn-glass-soft btn-glass-soft-sm" title="跨库同步" onClick={() => openTab({ key: `sync:${conn.id}`, kind: 'sync', connId: conn.id, label: '跨库同步' })}>同步</button>
          )}
        </div>
        {activeConn && (
          <div className="db-head-info">
            <span className="pill">
              <span className={`db-engine-badge db-engine-${activeConn.engine}`}>{activeConn.engine}</span>
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
        <aside className="db-side">
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
            onOpenStatus={handleOpenStatus}
            onOpenExplain={handleOpenExplain}
            onNewQueryWithSQL={handleNewQueryWithSQL}
            onExportTable={handleExportTable}
            onRefresh={() => listConnections().then(setConns).catch(() => {})}
          />
          <ConnectionPanel
            selected={null}
            onSelect={handleSelectConn}
            onConnsChange={setConns}
          />
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
              <div className="db-main-tabs db-worktabs">
                {tabs.map(t => (
                  <div key={t.key} className={`db-worktab ${activeTab === t.key ? 'active' : ''}`}
                    onClick={() => setActiveTab(t.key)} title={t.label}>
                    <span className="db-worktab-kind">{t.kind === 'data' ? '表' : t.kind === 'query' ? 'SQL' : t.kind === 'doc' ? 'DDL' : t.kind === 'sync' ? '同步' : t.kind === 'audit' ? '审' : t.kind === 'drivers' ? '驱' : '查'}</span>
                    <span className="db-worktab-label">{t.label}</span>
                    <span className="db-worktab-close" onClick={e => { e.stopPropagation(); closeTab(t.key) }}>×</span>
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
                    const seed = querySeedRef.current
                    const isSeedTab = !!seed && t.key.endsWith(String(seed.length)) === false || !!seed
                    return (
                      <div className="db-query-section" key={t.key}>
                        <QueryEditor
                          connId={c!.id}
                          engine={c!.engine}
                          defaultSQL={isSeedTab ? seed : undefined}
                          onResult={handleResult}
                          onWriteLocked={() => setShowUnlock(true)}
                          onExecuted={setLastSQL}
                        />
                        {result && <DataGrid result={result} connId={c!.id} sql={lastSQL} />}
                      </div>
                    )
                  }
                  case 'doc':
                    return <DocPanel key={t.key} connId={c!.id} database={t.db!} table={t.table!} />
                  case 'sync': {
                    const seed = syncSeedRef.current && syncSeedRef.current.connId === t.connId ? syncSeedRef.current : null
                    return <div className="db-doc-section" key={t.key}><SyncPanel conns={conns} activeConnId={c!.id} presetDb={seed?.db} /></div>
                  }
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
