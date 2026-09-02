// 数据库管理模块主入口（P0 改造：标签页工作台）。
// 布局: 左侧 = ConnectionTree(连接→库→表/视图 懒加载树) ; 右侧 = 多标签工作台
//   标签类型: data(表数据浏览) / query(查询) / doc(表结构) / sync(跨库同步) / audit(审计)
// 右上角不再放重复的「新建连接」(入口在连接面板与概览页)。

import { useState, useEffect, useMemo } from 'react'
import { useToast } from '../components/Toast'
import {
  type ConnectionInfo, type QueryResult, type InterceptionBody,
  listConnections, getUnlockState, lockWrite, unlockWrite,
} from '../components/DatabaseManager/api'
import ConnectionPanel from '../components/DatabaseManager/ConnectionPanel'
import ConnectionTree from '../components/DatabaseManager/ConnectionTree'
import DocPanel from '../components/DatabaseManager/DocPanel'
import QueryEditor from '../components/DatabaseManager/QueryEditor'
import DataGrid from '../components/DatabaseManager/DataGrid'
import DataPanel from '../components/DatabaseManager/DataPanel'
import OverviewPanel from '../components/DatabaseManager/OverviewPanel'
import SyncPanel from '../components/DatabaseManager/SyncPanel'
import AuditPanel from '../components/DatabaseManager/AuditPanel'

interface WorkTab {
  key: string          // data:cid.db.table / query:cid / doc:cid.db.table / sync / audit
  kind: 'data' | 'query' | 'doc' | 'sync' | 'audit'
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

  useEffect(() => {
    listConnections().then(setConns).catch(() => setConns([]))
  }, [conn?.id])

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
  const handleSelectConn = (c: ConnectionInfo) => { setConn(c) }

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
        {activeConn && (
          <div className="db-head-info">
            <span className="pill">
              <span className={`db-engine-badge db-engine-${activeConn.engine}`}>{activeConn.engine}</span>
              {activeConn.name}
            </span>
            {isProd && <span className="pill pill-err">生产</span>}
            {unlockState.unlocked ? (
              <span className="pill pill-ok" title="写操作已解锁">🔓 写 {formatRemaining(unlockState.remainingSec)}</span>
            ) : (
              <span className="pill pill-warn" title="默认只读, 写操作前需解锁">🔒 只读</span>
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

      <div className="db-layout">
        <aside className="db-side">
          <ConnectionPanel
            selected={conn}
            onSelect={handleSelectConn}
            onConnsChange={setConns}
          />
          <ConnectionTree
            conns={conns}
            selectedConnId={conn?.id}
            onOpenTable={handleOpenTable}
            onNewQuery={handleNewQuery}
            onOpenDoc={handleOpenDoc}
            onSelectConn={handleSelectConn}
            onEditConn={(c) => window.dispatchEvent(new CustomEvent('dbmanager:edit-conn', { detail: c }))}
            onNewConn={() => window.dispatchEvent(new CustomEvent('dbmanager:new-conn'))}
            onConnsChange={setConns}
            notify={(ok, msg) => { ok ? toast.success(msg) : toast.error(msg) }}
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
                    <span className="db-worktab-kind">{t.kind === 'data' ? '▦' : t.kind === 'query' ? 'SQL' : t.kind === 'doc' ? '≡' : t.kind === 'sync' ? '⇄' : '✦'}</span>
                    <span className="db-worktab-label">{t.label}</span>
                    <span className="db-worktab-close" onClick={e => { e.stopPropagation(); closeTab(t.key) }}>×</span>
                  </div>
                ))}
                {conn && (
                  <div className="db-worktabs-fixed">
                    <button className="btn-glass-soft btn-glass-soft-sm" title="跨库同步"
                      onClick={() => openTab({ key: `sync:${conn.id}`, kind: 'sync', connId: conn.id, label: '跨库同步' })}>⇄ 同步</button>
                    <button className="btn-glass-soft btn-glass-soft-sm" title="审计日志"
                      onClick={() => openTab({ key: `audit:${conn.id}`, kind: 'audit', connId: conn.id, label: '审计' })}>✦ 审计</button>
                  </div>
                )}
              </div>
              {tabs.map(t => {
                if (t.key !== activeTab) return null
                const c = connById.get(t.connId)
                if (!c) return <div className="db-empty">连接不存在, 请关闭此标签</div>
                switch (t.kind) {
                  case 'data':
                    return <DataPanel key={t.key} conn={c} database={t.db!} table={t.table!} isView={t.isView} />
                  case 'query':
                    return (
                      <div className="db-query-section" key={t.key}>
                        <QueryEditor
                          connId={c.id}
                          engine={c.engine}
                          onResult={handleResult}
                          onWriteLocked={() => setShowUnlock(true)}
                          onExecuted={setLastSQL}
                        />
                        {result && <DataGrid result={result} connId={c.id} sql={lastSQL} />}
                      </div>
                    )
                  case 'doc':
                    return <DocPanel key={t.key} connId={c.id} database={t.db!} table={t.table!} />
                  case 'sync':
                    return <div className="db-doc-section" key={t.key}><SyncPanel conns={conns} activeConnId={c.id} /></div>
                  case 'audit':
                    return <div className="db-audit-section" key={t.key}><AuditPanel conns={conns} /></div>
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
