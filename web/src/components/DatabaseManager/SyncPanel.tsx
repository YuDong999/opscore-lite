// 跨库同步视图: MySQL 族 ↔ PostgreSQL 族。
// 两栏对象选择(组合表设计): 范围(两级引擎=库 / 三级引擎=模式) + 表; 库在三级引擎下为连接库(dim 上下文, 不可选)。
// 入口层级(库/模式/表右键)决定预置与固定: 表级=该表锁定可追加; 模式级=锁定模式; 库级=整库全部模式。
// 更换源连接即清除入口预设, 所有选择恢复可选 —— 不存在"选完就永久固定"。
// 流程: 选源/目标 → 选表 → plan 预览 → run 后台执行 → 进度轮询。

import { useEffect, useMemo, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { useToast } from '../Toast'
import {
  type ConnectionInfo,
  listDatabases,
  listSchemas,
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
  targetSchema?: string
  tables?: string[]
  tableMaps?: Array<{ source: string; target: string }>
  mode: SyncMode; incrementalColumn?: string
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

// 下拉箭头(SVG, 避免 ▸ 字形缺字渲染成月牙)
const Chevron = ({ open }: { open: boolean }) => (
  <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"
    style={{ transform: open ? 'rotate(180deg)' : 'none', transition: 'transform .12s', flexShrink: 0 }}>
    <path d="m6 9 6 6 6-6" />
  </svg>
)

export default function SyncPanel({ conns, activeConnId, presetDb, presetSchema, presetTable }: {
  conns: ConnectionInfo[]; activeConnId?: string
  presetDb?: string; presetSchema?: string; presetTable?: string
}) {
  const toast = useToast()
  const [sourceId, setSourceId] = useState('')
  const [sourceDb, setSourceDb] = useState('')
  const [targetId, setTargetId] = useState('')
  const [targetDb, setTargetDb] = useState('')
  const [srcDbs, setSrcDbs] = useState<string[]>([])
  const [srcTables, setSrcTables] = useState<string[]>([])
  const [srcSchemas, setSrcSchemas] = useState<string[]>([])   // 能力探测: 空=两级引擎(库→表); 非空=三级(库→模式→表)
  const [dstSchemas, setDstSchemas] = useState<string[]>([])
  const [dstDbs, setDstDbs] = useState<string[]>([])
  const [srcSchema, setSrcSchema] = useState('')               // 三级引擎的范围选择(''=全部模式)
  const [targetSchema, setTargetSchema] = useState('')
  const [pickedTables, setPickedTables] = useState<Set<string>>(new Set())
  const [targetNames, setTargetNames] = useState<Record<string, string>>({})
  const [syncEngines, setSyncEngines] = useState<string[] | null>(null)
  const [tblOpen, setTblOpen] = useState(false)
  const [tblFilter, setTblFilter] = useState('')
  const [tblPos, setTblPos] = useState<{ left: number; top: number; bottom: number; up: boolean; maxH: number; width: number }>({ left: 0, top: 0, bottom: 0, up: false, maxH: 380, width: 0 })
  const tblAnchorRef = useRef<HTMLDivElement>(null)
  const [mode, setMode] = useState<SyncMode>('schema_full')
  const [plan, setPlan] = useState<SyncPlan | null>(null)
  const [job, setJob] = useState<Job | null>(null)
  const [busy, setBusy] = useState(false)
  const [expanded, setExpanded] = useState<string | null>(null)
  const presetApplied = useRef(false)

  // 生效的入口预设(库/模式/表右键进入); 顶部按钮/面板进入为无预设
  const eff = { db: presetDb || '', schema: presetSchema || '', table: presetTable || '' }
  const srcThree = srcSchemas.length > 0
  const dstThree = dstSchemas.length > 0
  // 源范围栏固定条件: 表级/模式级入口(两级/三级都固定); 两级引擎的库级入口也固定库
  const srcScopeFixed = !!eff.table || !!eff.schema || (!!eff.db && !srcThree)
  const shortName = (t: string) => { const i = t.indexOf('.'); return i > 0 ? t.slice(i + 1) : t }

  const resetDownstream = () => { setPickedTables(new Set()); setTargetNames({}); setPlan(null); setTblOpen(false) }

  // 打开时按触发器定位(portal 到 body): 下方空间足则向下, 否则向上翻转
  const openPicker = () => {
    if (tblOpen) { setTblOpen(false); return }
    const el = tblAnchorRef.current
    if (!el) { setTblOpen(true); return }
    const r = el.getBoundingClientRect()
    const below = window.innerHeight - r.bottom - 8
    const up = below < 260 && r.top > below
    setTblPos({
      left: r.left, width: Math.max(r.width, 480),
      top: r.bottom + 2, bottom: window.innerHeight - r.top + 2, up,
      maxH: Math.max(180, up ? r.top - 16 : below),
    })
    setTblFilter('')
    setTblOpen(true)
  }

  // 锚点随视图滚动/窗口变化时关闭(面板为 fixed, 锚点会移位)
  useEffect(() => {
    if (!tblOpen) return
    const close = () => setTblOpen(false)
    window.addEventListener('scroll', close, true)
    window.addEventListener('resize', close)
    return () => { window.removeEventListener('scroll', close, true); window.removeEventListener('resize', close) }
  }, [tblOpen])

  const dbOptions = useMemo(() => conns.map(c => ({ id: c.id, name: c.name, engine: c.engine })), [conns])

  // 源表名兜底派生模式(驱动未返回模式列表时, 从 schema.table 前缀取)
  const nameSchemas = useMemo(() => {
    const set = new Set<string>()
    for (const t of srcTables) { const i = t.indexOf('.'); if (i > 0) set.add(t.slice(0, i)) }
    return [...set].sort()
  }, [srcTables])
  const srcSchemaOptions = srcSchemas.length ? srcSchemas : nameSchemas
  const visibleTables = useMemo(
    () => srcSchema ? srcTables.filter(t => t.startsWith(srcSchema + '.')) : srcTables,
    [srcTables, srcSchema],
  )

  useEffect(() => {
    fetch('/api/dbmanager/sync/engines', { headers: localStorage.getItem('opscore-token') ? { Authorization: `Bearer ${localStorage.getItem('opscore-token')}` } : {} })
      .then(r => r.json()).then(d => setSyncEngines(d.engines || [])).catch(() => setSyncEngines(null))
  }, [])
  useEffect(() => {
    if (!sourceId && activeConnId) setSourceId(activeConnId)
  }, [activeConnId])
  // 入口预设变化(重新从树进入) → 允许重新应用
  useEffect(() => { presetApplied.current = false }, [presetDb, presetSchema, presetTable])
  useEffect(() => {
    if (eff.db && !entryCleared) setSourceDb(eff.db)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [eff.db])

  // 源侧探测: 库列表 + 模式能力; 三级引擎库=连接库(自动带出, 栏位上只作 dim 上下文)
  useEffect(() => {
    if (!sourceId) { setSrcDbs([]); setSrcSchemas([]); setSrcTables([]); return }
    listDatabases(sourceId).then(dbs => setSrcDbs(dbs || [])).catch(() => setSrcDbs([]))
    listSchemas(sourceId).then(ss => {
      setSrcSchemas(ss || [])
      if (ss?.length) {
        const db = conns.find(c => c.id === sourceId)?.config.database
        if (db) setSourceDb(db)
      }
    }).catch(() => setSrcSchemas([]))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sourceId])
  // 目标侧探测
  useEffect(() => {
    if (!targetId) { setDstSchemas([]); setTargetSchema(''); setTargetDb(''); setDstDbs([]); return }
    listDatabases(targetId).then(dbs => setDstDbs(dbs || [])).catch(() => setDstDbs([]))
    listSchemas(targetId).then(ss => {
      setDstSchemas(ss || [])
      setTargetSchema(ss?.includes('public') ? 'public' : (ss?.[0] || ''))
      if (ss?.length) {
        const db = conns.find(c => c.id === targetId)?.config.database
        if (db) setTargetDb(db)
      }
    }).catch(() => { setDstSchemas([]); setTargetSchema('') })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [targetId])

  // 源表列表: 级联下游重置, 然后按入口层级应用预设
  useEffect(() => {
    setSrcTables([]); resetDownstream(); setSrcSchema(''); presetApplied.current = false
    if (!sourceId || !sourceDb) return
    listTables(sourceId, sourceDb).then(ts => {
      const names = ts.map(t => t.name)
      setSrcTables(names)
      if (presetApplied.current) return
      presetApplied.current = true
      const schemas = [...new Set(names.map(n => { const i = n.indexOf('.'); return i > 0 ? n.slice(0, i) : '' }).filter(Boolean))].sort()
      const pool = srcSchemas.length ? srcSchemas : schemas
      if (eff.table) {
        // 表级入口: 该表锁定预选, 范围固定为其所属层级
        if (names.includes(eff.table)) setPickedTables(new Set([eff.table]))
        const i = eff.table.indexOf('.')
        const pfx = i > 0 ? eff.table.slice(0, i) : ''
        if (pfx && pool.includes(pfx)) setSrcSchema(pfx)
      } else if (eff.schema) {
        // 模式级入口: 锁定该模式
        setSrcSchema(pool.includes(eff.schema) ? eff.schema : '')
      } else {
        // 库级入口(及无预设进入): 全部模式/整库 —— 不做擅自收窄
        setSrcSchema('')
      }
      // eslint-disable-next-line react-hooks/exhaustive-deps
    }).catch(() => setSrcTables([]))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sourceId, sourceDb])

  const toggleTable = (t: string) => {
    if (eff.table && t === eff.table) return // 表级入口的该表锁定
    setPickedTables(prev => {
      const next = new Set(prev)
      if (next.has(t)) next.delete(t); else next.add(t)
      return next
    })
    setPlan(null)
  }

  const buildRequest = (): SyncRequest | null => {
    if (!sourceId || !targetId) { toast.error('请选择源/目标连接'); return null }
    if (!sourceDb) { toast.error('请选择源范围'); return null }
    if (dstThree && !targetSchema) { toast.error('请选择目标模式'); return null }
    if (!dstThree && !targetDb) { toast.error('请选择目标库'); return null }
    if (pickedTables.size === 0) { toast.error('请至少选择一张表'); return null }
    return {
      sourceId, sourceDb, targetId, targetDb,
      targetSchema: dstThree ? targetSchema : undefined,
      mode,
      tableMaps: [...pickedTables].map(t => ({ source: t, target: targetNames[t]?.trim() || t })),
    }
  }

  const doPlan = async () => {
    const req = buildRequest(); if (!req) return
    setBusy(true)
    try {
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
    const req = buildRequest(); if (!req) return
    setBusy(true)
    try {
      const r = await post<{ jobId: string }>('/api/dbmanager/sync/run', req)
      toast.success(`任务已启动: ${r.jobId}`)
      setTblOpen(false)
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
  const pickedSummary = [...pickedTables].slice(0, 3).map(shortName).join(', ') + (pickedTables.size > 3 ? ` 等 ${pickedTables.size} 张` : '')
  const srcConn = conns.find(c => c.id === sourceId)
  const dstConn = conns.find(c => c.id === targetId)

  return (
    <div className="db-doc">
      <div className="db-conn-list-head">
        <span>跨库同步 <span className="dim" style={{ fontWeight: 400, fontSize: '0.6875rem' }}>
          MySQL 族 ↔ PostgreSQL 族 · 范围+表两栏级联 · 结构/全量/增量
        </span></span>
      </div>

      {/* 入口上下文横幅: 三种入口可见区分; 更换源连接后消失(预设失效) */}
      {eff.db && (
        <div style={{ padding: '0.35rem 0.6rem', background: 'var(--surface-tint)', border: '1px solid var(--border)', borderRadius: 6, fontSize: '0.75rem', display: 'flex', alignItems: 'center', gap: 6 }}>
          <span className="db-engine-badge">同步范围</span>
          <b>{eff.table ? `表 ${shortName(eff.table)}` : eff.schema ? `模式 ${eff.schema}` : `库 ${eff.db}`}</b>
          <span className="dim">{eff.table ? '该表已锁定, 可追加其他表' : '固定项不可改'} · 更换源连接可解除</span>
        </div>
      )}

      <div className="db-form">
        {/* 第一行: 源连接 → 源范围(库|模式) → 表 */}
        <div className="db-form-row">
          {eff.db ? (
            <div className="db-form-grow">
              源连接 · 固定
              <div className="input" style={{ display: 'flex', alignItems: 'center', gap: 6, opacity: 0.75 }} title="已由入口(库/模式/表右键)固定; 同步其他连接请从对应节点进入或用顶部同步按钮">
                <b>{srcConn?.name || eff.db}</b>
                <span className="dim">{srcConn?.engine}</span>
              </div>
            </div>
          ) : (
            <label className="db-form-grow">
              源连接
              <select className="input" value={sourceId}
                onChange={e => { setSourceId(e.target.value); setSourceDb(''); setSrcSchema(''); resetDownstream() }}>
                <option value="">(选择连接)</option>
                {dbOptions.map(c => {
                  const ok = !syncEngines || syncEngines.includes(c.engine)
                  return <option key={c.id} value={c.id} disabled={!ok}>{c.name} ({c.engine}){ok ? '' : ' · 不支持同步'}</option>
                })}
              </select>
            </label>
          )}
          <label className="db-form-grow">
            源范围{srcThree ? ' (模式)' : ' (库)'}{srcScopeFixed ? ' · 固定' : ''}
            {srcThree ? (
              <>
                <select className="input" value={srcSchema} disabled={srcScopeFixed}
                  title={srcScopeFixed ? '已由入口固定' : srcSchema ? undefined : '全部模式 = 整库范围'}
                  onChange={e => { setSrcSchema(e.target.value); resetDownstream() }}
                  style={srcScopeFixed ? { opacity: 0.65 } : undefined}>
                  <option value="">全部模式</option>
                  {srcSchemaOptions.map(sc => <option key={sc} value={sc}>{sc}</option>)}
                </select>
                <span className="dim" style={{ fontSize: '0.6875rem' }}>库: {srcConn?.config.database || '(连接默认库)'} · 三级命名, 库即连接库</span>
              </>
            ) : (
              <select className="input" value={sourceDb} disabled={srcScopeFixed}
                title={srcScopeFixed ? '已由入口固定' : undefined}
                onChange={e => { setSourceDb(e.target.value); setSrcSchema('') }}
                style={srcScopeFixed ? { opacity: 0.65 } : undefined}>
                <option value="">(选择库)</option>
                {srcDbs.map(d => <option key={d} value={d}>{d}</option>)}
              </select>
            )}
          </label>
          {/* 表选择: 下拉选择框(多选/反选/搜索); portal 到 body 上下翻转 */}
          <div className="db-form-grow" style={{ position: 'relative' }} ref={tblAnchorRef}>
            表 (可多选)
            <button type="button" className="input" disabled={visibleTables.length === 0}
              style={{ width: '100%', textAlign: 'left', display: 'flex', alignItems: 'center', gap: 8, cursor: 'pointer' }}
              onClick={openPicker}>
              <span style={{ flex: 1, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                {pickedTables.size === 0
                  ? (visibleTables.length ? '选择表' : '请先选择源连接与范围')
                  : `已选 ${pickedTables.size} 张: ${pickedSummary}`}
              </span>
              <Chevron open={tblOpen} />
            </button>
          </div>
        </div>

        {/* 第二行: 目标连接 → 目标范围(库|模式) → 同步模式 */}
        <div className="db-form-row">
          <label className="db-form-grow">
            目标连接
            <select className="input" value={targetId} onChange={e => { setTargetId(e.target.value); setTargetDb(''); setTargetSchema(''); setPlan(null) }}>
              <option value="">(选择连接)</option>
              {dbOptions.filter(c => c.id !== sourceId).map(c => {
                const ok = !syncEngines || syncEngines.includes(c.engine)
                return <option key={c.id} value={c.id} disabled={!ok}>{c.name} ({c.engine}){ok ? '' : ' · 不支持同步'}</option>
              })}
            </select>
          </label>
          <label className="db-form-grow">
            目标范围{dstThree ? ' (模式)' : ' (库)'}
            {dstThree ? (
              <>
                <select className="input" value={targetDb} disabled title="三级命名引擎: 库即连接库(驱动按连接定库), 实际写入位置由下方模式决定" style={{ opacity: 0.65 }}>
                  <option value="">{dstConn?.config.database || '(连接默认库)'}</option>
                </select>
                <select className="input" value={targetSchema} onChange={e => { setTargetSchema(e.target.value); setPlan(null) }}>
                  {dstSchemas.map(sc => <option key={sc} value={sc}>{sc}</option>)}
                </select>
                <span className="dim" style={{ fontSize: '0.6875rem' }}>表将建在所选模式下</span>
              </>
            ) : (
              <select className="input" value={targetDb} onChange={e => { setTargetDb(e.target.value); setPlan(null) }}>
                <option value="">(选择库)</option>
                {dstDbs.map(d => <option key={d} value={d}>{d}</option>)}
              </select>
            )}
          </label>
          <label className="db-form-grow">
            同步模式
            <select className="input" value={mode} onChange={e => { setMode(e.target.value as SyncMode); setPlan(null) }}>
              {(Object.keys(MODE_LABELS) as SyncMode[]).map(m => (
                <option key={m} value={m}>{MODE_LABELS[m].text}</option>
              ))}
            </select>
            <span className="dim" style={{ fontSize: '0.6875rem' }}>{MODE_LABELS[mode].desc}</span>
          </label>
        </div>

        <div className="db-form-actions" style={{ justifyContent: 'flex-start' }}>
          <button className="btn-glass-soft btn-glass-soft-sm" onClick={doPlan} disabled={busy}>生成计划预览</button>
          <button className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-accent" onClick={doRun} disabled={busy}>启动同步</button>
        </div>
      </div>

      {/* 表选择面板: portal 到 body, 不被任何容器裁剪 */}
      {tblOpen && createPortal(
        <>
          <div style={{ position: 'fixed', inset: 0, zIndex: 2999 }} onClick={() => setTblOpen(false)} />
          <div style={{ position: 'fixed', zIndex: 3000, left: tblPos.left, width: tblPos.width,
            top: tblPos.up ? undefined : tblPos.top, bottom: tblPos.up ? tblPos.bottom : undefined,
            background: 'var(--surface-solid)', border: '1px solid var(--border)', borderRadius: 6,
            boxShadow: 'var(--shadow, 0 8px 24px rgba(0,0,0,.18))', maxHeight: tblPos.maxH, overflowY: 'auto', padding: '0.3rem' }}>
            <div style={{ position: 'sticky', top: 0, background: 'var(--surface-solid)', zIndex: 1 }}>
              <div style={{ display: 'flex', alignItems: 'center', gap: 6, padding: '0.2rem 0.4rem' }}>
                <span className="dim" style={{ fontSize: '0.6875rem', flexShrink: 0 }}>
                  {srcSchema ? `模式 ${srcSchema} · ` : ''}{visibleTables.length} 张表
                </span>
                <span style={{ marginLeft: 'auto', display: 'flex', gap: 4 }}>
                  <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => setPickedTables(new Set(visibleTables))}>全选</button>
                  <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => setPickedTables(prev => new Set(visibleTables.filter(t => prev.has(t) ? t === eff.table : true)))}>反选</button>
                  <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => { setPickedTables(new Set(eff.table ? [eff.table] : [])); setTargetNames({}) }}>清空</button>
                </span>
              </div>
              <div style={{ padding: '0.15rem 0.4rem 0.25rem' }}>
                <input className="input input-sm" style={{ width: '100%' }} placeholder="搜索表名..." value={tblFilter} onChange={e => setTblFilter(e.target.value)} />
              </div>
            </div>
            {visibleTables.filter(t => !tblFilter.trim() || shortName(t).toLowerCase().includes(tblFilter.trim().toLowerCase()) || t.toLowerCase().includes(tblFilter.trim().toLowerCase())).map(t => {
              const lockedIn = !!eff.table && t === eff.table
              return (
                <div key={t} style={{ display: 'flex', alignItems: 'center', gap: '0.4rem', padding: '0.15rem 0.4rem' }}>
                  <label className="db-advanced-toggle" style={{ flexShrink: 0, minWidth: '9rem', maxWidth: '14rem', overflow: 'hidden', textOverflow: 'ellipsis' }}>
                    <input type="checkbox" checked={pickedTables.has(t)} disabled={lockedIn} onChange={() => toggleTable(t)} />
                    <span title={t}>{shortName(t)}{lockedIn ? ' (入口表)' : ''}</span>
                  </label>
                  {pickedTables.has(t) ? (
                    <>
                      <span className="dim">→</span>
                      <input
                        className="input input-sm"
                        style={{ flex: 1, minWidth: 0 }}
                        value={targetNames[t] ?? ''}
                        onChange={e => setTargetNames(prev => ({ ...prev, [t]: e.target.value }))}
                        placeholder="目标表名(留空=同名, 自定义则自动建表)"
                      />
                    </>
                  ) : (
                    <span className="dim" style={{ fontSize: '0.6875rem' }}>勾选后可自定义目标表名</span>
                  )}
                </div>
              )
            })}
          </div>
        </>,
        document.body,
      )}

      {/* 任务进度 */}
      {job && (
        <div className="db-advanced-block">
          <div className="db-conn-editor-title">
            任务 {job.id} <span className={`pill ${job.status === 'done' ? 'pill-ok' : job.status === 'failed' ? 'pill-err' : 'pill-warn'}`} style={{ marginLeft: 8 }}>
              {job.status === 'running' ? '执行中' : job.status === 'done' ? '完成' : job.status === 'failed' ? '失败' : job.status}
            </span>
          </div>
          {job.err && <div className="banner banner-err">{job.err}</div>}
          {(job.tables || []).map(t => (
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
