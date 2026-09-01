// 跨库同步面板: MySQL 族 ↔ PostgreSQL 族。
// 流程: 选源/目标连接+库 → 选表+模式 → plan 预览(类型映射/DDL/增量策略) → run 后台执行 → 进度轮询。

import { useEffect, useMemo, useState } from 'react'
import { useToast } from '../Toast'
import {
  type ConnectionInfo,
  listConnections,
  listDatabases,
  listTables,
} from './api'

type SyncMode = 'schema_only' | 'schema_full' | 'schema_full_incr' | 'truncate_full' | 'incr_only' | 'verify'

const MODE_LABELS: Record<SyncMode, { text: string; desc: string }> = {
  schema_only:      { text: '仅表结构',   desc: '迁移建表 DDL + 索引, 不搬数据' },
  schema_full:      { text: '结构+全量',  desc: '建表并复制全部数据' },
  schema_full_incr: { text: '结构+全量+增量', desc: '建表 + 全量, 并记录水位供增量续传' },
  truncate_full:    { text: '清空+全量',  desc: '目标表已存在: 先 TRUNCATE 再全量(不迁移结构)' },
  incr_only:        { text: '仅增量',     desc: '按目标水位(自增主键/时间戳列)拉取新增' },
  verify:           { text: '行数校验',   desc: '对比源/目标行数' },
}

interface ColumnMapping { name: string; source: string; target: string; note?: string; isPk: boolean }
interface TablePlan {
  source: string; target: string; createDdl?: string; columns?: ColumnMapping[]
  indexDdl?: string[]; incrStrategy?: string; incrColumn?: string
  notes?: string[]; skipped?: boolean; skipReason?: string
}
interface SyncPlan { sourceDialect: string; targetDialect: string; mode: SyncMode; tables: TablePlan[]; unsupported?: string[] }
interface TableProgress { table: string; status: string; rowsCopied: number; err?: string }
interface Job {
  id: string; request: SyncRequest; plan?: SyncPlan; status: string
  tables: TableProgress[]; totalRows: number; err?: string
  startedAt: string; finishedAt?: string
}
interface SyncRequest {
  sourceId: string; sourceDb: string; targetId: string; targetDb: string
  tables?: string[]; mode: SyncMode; incrementalColumn?: string
  options?: { batchRows?: number; maxRows?: number; truncate?: boolean; whereClause?: string }
}

async function post<T = any>(url: string, body: any): Promise<T> {
  const token = localStorage.getItem('opscore-token')
  const r = await fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', ...(token ? { Authorization: `Bearer ${token}` } : {}) },
    body: JSON.stringify(body),
  })
  const data = await r.json().catch(() => ({}))
  if (!r.ok) throw new Error(data.error || `HTTP ${r.status}`)
  return data
}

export default function SyncPanel({ conns, activeConnId }: { conns: ConnectionInfo[]; activeConnId?: string }) {
  const toast = useToast()
  const [sourceId, setSourceId] = useState('')
  const [sourceDb, setSourceDb] = useState('')
  const [targetId, setTargetId] = useState('')
  const [targetDb, setTargetDb] = useState('')
  const [srcDbs, setSrcDbs] = useState<string[]>([])
  const [dstDbs, setDstDbs] = useState<string[]>([])
  const [srcTables, setSrcTables] = useState<string[]>([])
  const [pickedTables, setPickedTables] = useState<Set<string>>(new Set())
  const [mode, setMode] = useState<SyncMode>('schema_full')
  const [plan, setPlan] = useState<SyncPlan | null>(null)
  const [job, setJob] = useState<Job | null>(null)
  const [busy, setBusy] = useState(false)
  const [expanded, setExpanded] = useState<string | null>(null)

  const dbOptions = useMemo(() => conns.map(c => ({ id: c.id, name: c.name, engine: c.engine })), [conns])

  useEffect(() => {
    if (!sourceId && activeConnId) setSourceId(activeConnId)
  }, [activeConnId])

  // 源/目标库列表
  useEffect(() => {
    if (!sourceId) { setSrcDbs([]); setSrcTables([]); return }
    listDatabases(sourceId).then(dbs => setSrcDbs(dbs || [])).catch(() => setSrcDbs([]))
  }, [sourceId])
  useEffect(() => {
    if (!targetId) { setDstDbs([]); return }
    listDatabases(targetId).then(dbs => setDstDbs(dbs || [])).catch(() => setDstDbs([]))
  }, [targetId])

  // 源表列表
  useEffect(() => {
    setSrcTables([]); setPickedTables(new Set()); setPlan(null)
    if (!sourceId || !sourceDb) return
    listTables(sourceId, sourceDb).then(ts => setSrcTables(ts.map(t => t.name))).catch(() => setSrcTables([]))
  }, [sourceId, sourceDb])

  const toggleTable = (t: string) => {
    setPickedTables(prev => {
      const next = new Set(prev)
      if (next.has(t)) next.delete(t); else next.add(t)
      return next
    })
    setPlan(null)
  }

  const doPlan = async () => {
    if (!sourceId || !targetId || !sourceDb || !targetDb) { toast.error('请选择源/目标连接与库'); return }
    setBusy(true)
    try {
      const req: SyncRequest = { sourceId, sourceDb, targetId, targetDb, mode, tables: [...pickedTables] }
      const r = await post<{ plan: SyncPlan }>('/api/dbmanager/sync/plan', req)
      setPlan(r.plan)
      if (r.plan.unsupported?.length) toast.error(r.plan.unsupported.join('; '))
    } catch (e: any) {
      toast.error('生成计划失败: ' + e.message)
    } finally {
      setBusy(false)
    }
  }

  const doRun = async () => {
    if (!sourceId || !targetId || !sourceDb || !targetDb) { toast.error('请选择源/目标连接与库'); return }
    setBusy(true)
    try {
      const req: SyncRequest = { sourceId, sourceDb, targetId, targetDb, mode, tables: [...pickedTables] }
      const r = await post<{ jobId: string }>('/api/dbmanager/sync/run', req)
      toast.success(`任务已启动: ${r.jobId}`)
      poll(r.jobId)
    } catch (e: any) {
      toast.error('启动失败: ' + e.message)
    } finally {
      setBusy(false)
    }
  }

  const poll = (jobId: string) => {
    const token = localStorage.getItem('opscore-token')
    const tick = () => {
      fetch(`/api/dbmanager/sync/status?id=${jobId}`, { headers: token ? { Authorization: `Bearer ${token}` } : {} })
        .then(r => r.json()).then(d => {
          setJob(d.job)
          if (d.job?.status === 'running') setTimeout(tick, 1200)
          else if (d.job?.status === 'done') toast.success('同步完成')
          else if (d.job?.status === 'failed') toast.error('同步失败: ' + (d.job.err || ''))
        }).catch(() => {})
    }
    tick()
  }

  const visiblePlan = plan?.tables?.filter(t => !t.skipped) || []
  const skipped = plan?.tables?.filter(t => t.skipped) || []

  return (
    <div className="db-doc">
      <div className="db-conn-list-head">
        <span>跨库同步 <span className="dim" style={{ fontWeight: 400, fontSize: '0.6875rem' }}>MySQL 族 ↔ PostgreSQL 族 · 结构/全量/增量</span></span>
      </div>

      <div className="db-form">
        <div className="db-form-row">
          <label className="db-form-grow">
            源连接
            <select className="input" value={sourceId} onChange={e => { setSourceId(e.target.value); setSourceDb('') }}>
              <option value="">(选择连接)</option>
              {dbOptions.map(c => <option key={c.id} value={c.id}>{c.name} ({c.engine})</option>)}
            </select>
          </label>
          <label className="db-form-grow">
            源库
            <select className="input" value={sourceDb} onChange={e => setSourceDb(e.target.value)}>
              <option value="">(选择库)</option>
              {srcDbs.map(d => <option key={d} value={d}>{d}</option>)}
            </select>
          </label>
          <label className="db-form-grow">
            目标连接
            <select className="input" value={targetId} onChange={e => { setTargetId(e.target.value); setTargetDb('') }}>
              <option value="">(选择连接)</option>
              {dbOptions.filter(c => c.id !== sourceId).map(c => <option key={c.id} value={c.id}>{c.name} ({c.engine})</option>)}
            </select>
          </label>
          <label className="db-form-grow">
            目标库
            <select className="input" value={targetDb} onChange={e => setTargetDb(e.target.value)}>
              <option value="">(选择库)</option>
              {dstDbs.map(d => <option key={d} value={d}>{d}</option>)}
            </select>
          </label>
        </div>

        <label>
          同步模式
          <select className="input" value={mode} onChange={e => { setMode(e.target.value as SyncMode); setPlan(null) }}>
            {(Object.keys(MODE_LABELS) as SyncMode[]).map(m => (
              <option key={m} value={m}>{MODE_LABELS[m].text}</option>
            ))}
          </select>
          <span className="dim" style={{ fontSize: '0.6875rem' }}>{MODE_LABELS[mode].desc}</span>
        </label>

        {srcTables.length > 0 && (
          <div>
            <div className="db-cascade-label">
              选择表
              <button className="btn-glass-soft btn-glass-soft-sm" style={{ marginLeft: 8 }}
                onClick={() => setPickedTables(pickedTables.size === srcTables.length ? new Set() : new Set(srcTables))}>
                {pickedTables.size === srcTables.length ? '全不选' : '全选'}
              </button>
            </div>
            <div style={{ display: 'flex', flexWrap: 'wrap', gap: '0.3rem', maxHeight: '8rem', overflowY: 'auto' }}>
              {srcTables.map(t => (
                <label key={t} className="db-advanced-toggle">
                  <input type="checkbox" checked={pickedTables.has(t)} onChange={() => toggleTable(t)} />
                  {t}
                </label>
              ))}
            </div>
          </div>
        )}

        <div className="db-form-actions" style={{ justifyContent: 'flex-start' }}>
          <button className="btn-glass-soft btn-glass-soft-sm" onClick={doPlan} disabled={busy}>生成计划预览</button>
          <button className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-accent" onClick={doRun} disabled={busy}>启动同步</button>
        </div>
      </div>

      {/* 任务进度 */}
      {job && (
        <div className="db-advanced-block">
          <div className="db-conn-editor-title">
            任务 {job.id} <span className={`pill ${job.status === 'done' ? 'pill-ok' : job.status === 'failed' ? 'pill-err' : 'pill-warn'}`} style={{ marginLeft: 8 }}>
              {job.status === 'running' ? '执行中' : job.status === 'done' ? '完成' : job.status === 'failed' ? '失败' : job.status}
            </span>
          </div>
          {job.err && <div className="banner banner-err">{job.err}</div>}
          {job.tables.map(t => (
            <div key={t.table} style={{ display: 'flex', gap: '0.5rem', fontSize: '0.75rem', alignItems: 'center' }}>
              <span className={`pill ${t.status === 'done' ? 'pill-ok' : t.status === 'failed' ? 'pill-err' : t.status === 'skipped' ? '' : 'pill-warn'}`}>
                {t.status === 'copying' ? '复制中' : t.status === 'creating' ? '建表中' : t.status === 'done' ? '完成' : t.status === 'failed' ? '失败' : t.status}
              </span>
              <span style={{ minWidth: '8rem', overflow: 'hidden', textOverflow: 'ellipsis' }}>{t.table}</span>
              <span className="dim">{t.rowsCopied} 行</span>
              {t.err && <span style={{ color: 'var(--danger)', wordBreak: 'break-all' }}>{t.err}</span>}
            </div>
          ))}
        </div>
      )}

      {/* 计划预览 */}
      {plan && (
        <div className="db-advanced-block">
          <div className="db-conn-editor-title">
            迁移计划 {plan.sourceDialect} → {plan.targetDialect}
            <span className="dim" style={{ marginLeft: 8, fontSize: '0.6875rem' }}>{visiblePlan.length} 表可迁移{skipped.length ? `, ${skipped.length} 表跳过` : ''}</span>
          </div>
          {visiblePlan.map(tp => (
            <div key={tp.source} style={{ borderBottom: '1px solid var(--border)', paddingBottom: '0.4rem' }}>
              <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', cursor: 'pointer' }}
                onClick={() => setExpanded(expanded === tp.source ? null : tp.source)}>
                <span className="db-engine-badge">{tp.source}</span>
                {tp.incrStrategy && tp.incrStrategy !== 'none' && (
                  <span className="pill pill-ok" style={{ fontSize: '0.625rem' }}>增量: {tp.incrColumn} ({tp.incrStrategy === 'auto_increment' ? '自增' : '时间戳'})</span>
                )}
                {tp.notes?.length ? <span className="dim" style={{ fontSize: '0.625rem' }}>{tp.notes.length} 条映射备注</span> : null}
                <span className="dim" style={{ marginLeft: 'auto', fontSize: '0.625rem' }}>{expanded === tp.source ? '收起' : '展开 DDL'}</span>
              </div>
              {expanded === tp.source && (
                <div>
                  <pre className="db-audit-sql-full" style={{ maxHeight: '12rem', overflowY: 'auto' }}>{tp.createDdl}</pre>
                  {tp.columns?.some(c => c.note) && (
                    <div style={{ fontSize: '0.6875rem', color: 'var(--text-dim)' }}>
                      {tp.columns.filter(c => c.note).map(c => (
                        <div key={c.name}>· {c.name}: {c.source} → {c.target} ({c.note})</div>
                      ))}
                    </div>
                  )}
                </div>
              )}
            </div>
          ))}
          {skipped.map(t => (
            <div key={t.source} style={{ fontSize: '0.75rem', color: 'var(--text-dim)' }}>⚠ {t.source}: {t.skipReason}</div>
          ))}
        </div>
      )}
    </div>
  )
}
