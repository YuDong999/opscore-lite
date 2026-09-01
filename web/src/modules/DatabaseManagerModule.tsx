// 数据库管理模块主入口。
// 整体布局:
//   - module-head: 标题 + 当前连接 + 环境标记 + 解锁倒计时 + 新建按钮
//   - 主体两栏: 左侧连接树(连接列表 + 级联), 右侧内容区(概览/工作台/审计)
//   - 右侧默认展示概览页(未选连接时); 选连接后默认进工作台

import { useState, useEffect, useCallback, useMemo } from 'react'
import { useToast } from '../components/Toast'
import {
  type ConnectionInfo, type QueryResult, type InterceptionBody,
  listConnections, getUnlockState, lockWrite, unlockWrite,
} from '../components/DatabaseManager/api'
import ConnectionPanel from '../components/DatabaseManager/ConnectionPanel'
import CascadeSelector from '../components/DatabaseManager/CascadeSelector'
import DocPanel from '../components/DatabaseManager/DocPanel'
import QueryEditor from '../components/DatabaseManager/QueryEditor'
import DataGrid from '../components/DatabaseManager/DataGrid'
import OverviewPanel from '../components/DatabaseManager/OverviewPanel'
import SyncPanel from '../components/DatabaseManager/SyncPanel'
import AuditPanel from '../components/DatabaseManager/AuditPanel'

type Tab = 'query' | 'doc' | 'audit' | 'sync' | 'overview'

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
  const [database, setDatabase] = useState('')
  const [table, setTable] = useState('')
  const [tab, setTab] = useState<Tab>('overview')
  const [result, setResult] = useState<QueryResult | null>(null)
  const [lastSQL, setLastSQL] = useState('')
  const [unlockState, setUnlockState] = useState<{ unlocked: boolean; remainingSec: number; maxMinutes: number }>({ unlocked: false, remainingSec: 0, maxMinutes: 30 })
  const [showUnlock, setShowUnlock] = useState(false)

  // 加载连接列表
  useEffect(() => {
    listConnections().then(setConns).catch(() => setConns([]))
  }, [conn])

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

  const onSelectConn = (c: ConnectionInfo) => {
    setConn(c)
    setDatabase('')
    setTable('')
    setResult(null)
    setLastSQL('')
    setTab('query')
  }

  const handleStartNew = () => {
    // 触发 ConnectionPanel 内的 startNew: 简化做法是把 ConnectionPanel 暴露一个 ref
    // 这里通过一个全局事件触发, 实际 ConnectionPanel 还没接, 用一个临时变通
    setNewConnTrigger(t => t + 1)
  }

  // 通过自定义事件通知 ConnectionPanel 启动新建流程
  const [newConnTrigger, setNewConnTrigger] = useState(0)
  useEffect(() => {
    if (newConnTrigger > 0) {
      window.dispatchEvent(new CustomEvent('dbmanager:new-conn'))
    }
  }, [newConnTrigger])

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
        {conn && (
          <div className="db-head-info">
            <span className="pill">
              <span className={`db-engine-badge db-engine-${conn.engine}`}>{conn.engine}</span>
              {conn.name}
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
        <div className="db-head-actions">
          <button className="btn-glass-soft btn-glass-soft-sm" onClick={handleStartNew}>+ 新建连接</button>
        </div>
      </div>

      {showUnlock && conn && (
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
            onSelect={onSelectConn}
            onConnsChange={setConns}
            newConnTrigger={newConnTrigger}
          />
          {conn && (
            <CascadeSelector
              connId={conn.id}
              database={database}
              table={table}
              onDatabaseChange={(d) => { setDatabase(d); setTable('') }}
              onTableChange={setTable}
            />
          )}
        </aside>

        <main className="db-main">
          {!conn ? (
            <OverviewPanel
              conns={conns}
              onNewConn={handleStartNew}
              onPickConn={onSelectConn}
            />
          ) : (
            <>
              <div className="db-main-tabs">
                <button className={tab === 'query' ? 'active' : ''} onClick={() => setTab('query')}>
                  查询
                </button>
                <button className={tab === 'doc' ? 'active' : ''} onClick={() => setTab('doc')} disabled={!table}>
                  表结构{table && `: ${table}`}
                </button>
                <button className={tab === 'sync' ? 'active' : ''} onClick={() => setTab('sync')}>
                  同步
                </button>
                <button className={tab === 'audit' ? 'active' : ''} onClick={() => setTab('audit')}>
                  审计
                </button>
              </div>

              {tab === 'query' && (
                <div className="db-query-section">
                  <QueryEditor
                    connId={conn.id}
                    engine={conn.engine}
                    onResult={handleResult}
                    onWriteLocked={() => setShowUnlock(true)}
                    onExecuted={setLastSQL}
                  />
                  {result && <DataGrid result={result} connId={conn.id} sql={lastSQL} />}
                </div>
              )}

              {tab === 'sync' && (
                <div className="db-doc-section">
                  <SyncPanel conns={conns} activeConnId={conn.id} />
                </div>
              )}

              {tab === 'doc' && table && (
                <div className="db-doc-section">
                  <DocPanel connId={conn.id} database={database} table={table} />
                </div>
              )}

              {tab === 'audit' && (
                <div className="db-audit-section">
                  <AuditPanel conns={conns} />
                </div>
              )}
            </>
          )}
        </main>
      </div>
    </div>
  )
}
