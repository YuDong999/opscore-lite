// 数据对比视图: 选两侧 → 选表 → 起任务(可取消/有进度) → 三分类逐行勾选 → 执行 → 自动重比。
//
// 与「结构对比」是配套的两步: 结构先把表对齐, 数据再把行对齐。所以这里一行 SQL 都不发出去 ——
// 勾选的只是**键**, 语句由后端按那一刻从两侧读到的行现生成。
//
// 三个刻意的口径:
//  1) 「仅目标侧存在」(DELETE)**默认不勾**: 补行/改值是"让目标更接近基准", 删行是真丢数据。
//  2) 结果不完整时状态写"未扫完", 不写"一致": 把没扫完当成比平了, 比报错危险得多。
//  3) 执行后自动重比: 只有再比为 0 差异才算收敛, 残差原样列出来。

import { useEffect, useMemo, useRef, useState } from 'react'
import { useToast } from '../../components/Toast'
import { useConfirm } from '../../lib/hooks/useConfirm'
import { SqlPreviewBody } from '../../components/common/SqlPreview'
import { SidePickers } from './SchemaDiffPanel'
import {
  type ConnectionInfo, type DataDiffEnd, type DataDiffJob, type DataDiffOptions, type DataRowDiff, type DataTableDiff,
  dataDiffApply, dataDiffCancel, dataDiffStart, fetchDataDiffJob, listDatabases, listSchemas, listTables,
} from './api'

const KIND_META: Record<string, { title: string; hint: string; badge: string }> = {
  insert: { title: '目标侧缺这些行', hint: '按基准侧补进去 (INSERT)', badge: 'badge-warn' },
  update: { title: '两边都有但值不同', hint: '用基准侧的值覆盖目标 (UPDATE)', badge: 'badge-warn' },
  delete: { title: '仅目标侧存在', hint: '删掉是真丢数据, 默认不勾', badge: 'badge-danger' },
}

const STATUS_META: Record<string, { text: string; cls: string }> = {
  same: { text: '一致', cls: 'badge-ok' },
  different: { text: '有差异', cls: 'badge-warn' },
  partial: { text: '未扫完', cls: 'badge-warn' },
  error: { text: '比不了', cls: 'badge-danger' },
}

const rowKey = (table: string, r: DataRowDiff) => `${table}::${r.id}`

export default function DataDiffPanel({ conns, activeConnId, presetDb, presetTable }: {
  conns: ConnectionInfo[]; activeConnId?: string; presetDb?: string; presetTable?: string
}) {
  const toast = useToast()
  const { confirm, confirmEl } = useConfirm()

  const [srcId, setSrcId] = useState(activeConnId || '')
  const [srcDb, setSrcDb] = useState(presetDb || '')
  const [srcSchema, setSrcSchema] = useState('')
  const [dstId, setDstId] = useState(activeConnId || '')
  const [dstDb, setDstDb] = useState(presetDb || '')
  const [dstSchema, setDstSchema] = useState('')
  const [srcDbs, setSrcDbs] = useState<string[]>([])
  const [dstDbs, setDstDbs] = useState<string[]>([])
  const [srcSchemas, setSrcSchemas] = useState<string[]>([])
  const [dstSchemas, setDstSchemas] = useState<string[]>([])
  const [srcTables, setSrcTables] = useState<string[]>([])
  const [dstTables, setDstTables] = useState<string[]>([])

  const [picked, setPicked] = useState<string[]>(presetTable ? [presetTable] : [])
  const [filter, setFilter] = useState('')
  const [keyText, setKeyText] = useState('')
  const [ignoreText, setIgnoreText] = useState('')
  const [opts, setOpts] = useState<DataDiffOptions>({ batchRows: 1000, maxRows: 200000, maxDiffs: 2000 })

  const [jobId, setJobId] = useState('')
  const [job, setJob] = useState<DataDiffJob | null>(null)
  const [running, setRunning] = useState(false)
  const [sel, setSel] = useState<Set<string>>(new Set())
  const [openRow, setOpenRow] = useState('')
  const [busy, setBusy] = useState(false)
  const [applyMsg, setApplyMsg] = useState('')
  const lastAutoSel = useRef('')

  useEffect(() => {
    if (!srcId) { setSrcDbs([]); setSrcSchemas([]); return }
    listDatabases(srcId).then(setSrcDbs).catch(() => setSrcDbs([]))
    listSchemas(srcId).then(setSrcSchemas).catch(() => setSrcSchemas([]))
  }, [srcId])
  useEffect(() => {
    if (!dstId) { setDstDbs([]); setDstSchemas([]); return }
    listDatabases(dstId).then(setDstDbs).catch(() => setDstDbs([]))
    listSchemas(dstId).then(setDstSchemas).catch(() => setDstSchemas([]))
  }, [dstId])
  useEffect(() => {
    if (!srcId || !srcDb) { setSrcTables([]); return }
    listTables(srcId, srcDb).then(ts => setSrcTables(ts.map(t => t.name).sort())).catch(() => setSrcTables([]))
  }, [srcId, srcDb])
  useEffect(() => {
    if (!dstId || !dstDb) { setDstTables([]); return }
    listTables(dstId, dstDb).then(ts => setDstTables(ts.map(t => t.name).sort())).catch(() => setDstTables([]))
  }, [dstId, dstDb])

  const ends = useMemo((): [DataDiffEnd, DataDiffEnd] => [
    { id: srcId, database: srcDb, ...(srcSchema ? { schema: srcSchema } : {}) },
    { id: dstId, database: dstDb, ...(dstSchema ? { schema: dstSchema } : {}) },
  ], [srcId, srcDb, srcSchema, dstId, dstDb, dstSchema])

  const results = job?.results || []
  const totals = useMemo(() => {
    let inserts = 0, updates = 0, deletes = 0, diffRows = 0, errs = 0
    for (const t of results) {
      inserts += t.summary?.inserts || 0
      updates += t.summary?.updates || 0
      deletes += t.summary?.deletes || 0
      diffRows += t.rows?.length || 0
      if (t.status === 'error') errs++
    }
    return { inserts, updates, deletes, diffRows, errs, tables: results.length }
  }, [results])

  const selectedRows = useMemo(() => {
    const out: { table: string; row: DataRowDiff }[] = []
    for (const t of results) for (const r of t.rows || []) {
      if (sel.has(rowKey(t.table, r))) out.push({ table: t.table, row: r })
    }
    return out
  }, [results, sel])

  // 轮询: 任务制是为了能取消、能报进度, 也能在超时前就把"扫到哪了"讲清楚
  useEffect(() => {
    if (!jobId) return
    let stop = false
    let timer = 0
    const tick = async () => {
      try {
        const r = await fetchDataDiffJob(jobId)
        if (stop) return
        setJob(r)
        if (!r.ok) {
          setRunning(false)
          toast.error(r.error || '比对任务不见了')
          return
        }
        if (r.status === 'running') {
          timer = window.setTimeout(tick, 700)
          return
        }
        setRunning(false)
        // 任务结束后给一次默认勾选: 补行/改值默认选中, 删行默认不选
        if (lastAutoSel.current !== jobId) {
          lastAutoSel.current = jobId
          const next = new Set<string>()
          for (const t of r.results || []) for (const row of t.rows || []) {
            if (row.kind !== 'delete') next.add(rowKey(t.table, row))
          }
          setSel(next)
        }
      } catch (e: any) {
        if (stop) return
        setRunning(false)
        toast.error(String(e?.message || e))
      }
    }
    timer = window.setTimeout(tick, 0)
    return () => { stop = true; window.clearTimeout(timer) }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [jobId])

  const startCompare = async (silent = false) => {
    if (!srcId || !dstId || !srcDb || !dstDb) { toast.error('两侧都要选好连接和库'); return }
    if (!picked.length) { toast.error('请至少选择一张表'); return }
    const keys = keyText.split(',').map(s => s.trim()).filter(Boolean)
    if (keys.length && picked.length > 1) { toast.error('手填键列只支持单表; 多表请留空按各表主键判定'); return }
    setJob(null); setSel(new Set()); lastAutoSel.current = ''
    if (!silent) setApplyMsg('')
    setRunning(true)
    try {
      const r = await dataDiffStart(ends[0], ends[1], picked, keys,
        ignoreText.split(',').map(s => s.trim()).filter(Boolean), opts)
      if (!r.ok || !r.jobId) {
        setRunning(false)
        toast.error(r.error || '起任务失败')
        return
      }
      setJobId(r.jobId)
    } catch (e: any) {
      setRunning(false)
      toast.error(String(e?.message || e))
    }
  }

  const cancel = async () => {
    if (!jobId) return
    const r = await dataDiffCancel(jobId)
    if (!r.ok) toast.info(r.error || '任务已经结束了')
  }

  const swap = () => {
    setSrcId(dstId); setSrcDb(dstDb); setSrcSchema(dstSchema)
    setDstId(srcId); setDstDb(srcDb); setDstSchema(srcSchema)
    setJob(null); setJobId(''); setSel(new Set())
  }

  const apply = async () => {
    if (!selectedRows.length) { toast.error('没有勾选任何行'); return }
    const hasDelete = selectedRows.some(x => x.row.kind === 'delete')
    const preview = selectedRows.flatMap(x => x.row.sqls)
    const ok = await confirm(`执行 ${selectedRows.length} 行的数据同步?`, {
      desc: hasDelete
        ? '勾选里含「仅目标侧存在」的行 —— 执行是真删除, 数据丢了不可撤销。'
        : '语句由后端按此刻从两侧读到的行生成(不是比对时的快照), 已经没有差异的行会被跳过。',
      content: <SqlPreviewBody sqls={preview} caption={`将执行的 ${preview.length} 条语句(按目标方言生成, 同一事务提交)`} />,
      okText: '执行', danger: hasDelete, maxWidth: 700,
    })
    if (!ok) return
    setBusy(true)
    try {
      const byTable = new Map<string, DataRowDiff[]>()
      for (const x of selectedRows) {
        const arr = byTable.get(x.table) || []
        arr.push(x.row)
        byTable.set(x.table, arr)
      }
      let stmts = 0, skipped = 0
      const errs: string[] = []
      for (const [table, rows] of byTable) {
        const td = results.find(t => t.table === table)
        const r = await dataDiffApply(ends[0], ends[1], table, td?.keyColumns || [],
          rows.map(row => ({ key: row.key.map(k => ({ column: k.column, value: k.src })) })))
        if (!r.ok) { errs.push(`${table}: ${r.error}`); continue }
        stmts += r.count || 0
        skipped += r.stale || 0
      }
      if (errs.length) {
        setApplyMsg(errs.join(' ; '))
        toast.error(errs[0])
      } else if (stmts === 0) {
        setApplyMsg(`勾选的 ${selectedRows.length} 行已经没有差异(可能刚被别人改过), 未执行任何语句`)
        toast.info('未执行任何语句')
      } else {
        setApplyMsg(`已执行 ${stmts} 条语句${skipped ? `, ${skipped} 行已无差异被跳过` : ''}, 正在重新比对…`)
        toast.success(`已执行 ${stmts} 条语句, 正在重新比对确认…`)
      }
      await startCompare(true)   // 自动重比: 收敛才算完
    } catch (e: any) {
      setApplyMsg(String(e?.message || e))
      toast.error(String(e?.message || e))
    } finally {
      setBusy(false)
    }
  }

  const tableChoices = useMemo(() => {
    const arr = [...new Set<string>([...srcTables, ...dstTables])].sort()
    return filter ? arr.filter(n => n.toLowerCase().includes(filter.toLowerCase())) : arr
  }, [srcTables, dstTables, filter])

  return (
    <div className="db-doc">
      <div className="db-sync-layout">
        <SidePickers label="源(基准侧 · 目标要改成和它一致)" conns={conns} connId={srcId} setConnId={setSrcId}
          db={srcDb} setDb={setSrcDb} schema={srcSchema} setSchema={setSrcSchema} dbs={srcDbs} schemas={srcSchemas} />
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
          <button className="btn-glass-soft btn-glass-soft-sm" title="交换两侧" onClick={swap}
            style={{ transform: 'rotate(90deg)' }}>⇅</button>
        </div>
        <SidePickers label="目标(被改的一侧)" conns={conns} connId={dstId} setConnId={setDstId}
          db={dstDb} setDb={setDstDb} schema={dstSchema} setSchema={setDstSchema} dbs={dstDbs} schemas={dstSchemas} />
      </div>

      <div className="db-advanced-block" style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
        <div className="btn-row" style={{ gap: 10, alignItems: 'center', flexWrap: 'wrap' }}>
          <span className="dim small">表范围: {picked.length ? `已指定 ${picked.length} 张` : '未选(至少选一张)'}</span>
          <label className="dim small" style={{ display: 'flex', gap: 4, alignItems: 'center' }}>
            每批
            <input className="input" type="number" min={50} max={5000} style={{ width: 70 }} value={opts.batchRows}
              onChange={e => setOpts({ ...opts, batchRows: Number(e.target.value) })} />
          </label>
          <label className="dim small" style={{ display: 'flex', gap: 4, alignItems: 'center' }}>
            单侧行数上限
            <input className="input" type="number" min={1000} step={10000} style={{ width: 100 }} value={opts.maxRows}
              onChange={e => setOpts({ ...opts, maxRows: Number(e.target.value) })} />
          </label>
          <label className="dim small" style={{ display: 'flex', gap: 4, alignItems: 'center' }}>
            差异上限
            <input className="input" type="number" min={10} style={{ width: 80 }} value={opts.maxDiffs}
              onChange={e => setOpts({ ...opts, maxDiffs: Number(e.target.value) })} />
          </label>
          <span style={{ marginLeft: 'auto' }}>
            {running
              ? <button className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-danger" onClick={cancel}>取消比对</button>
              : <button className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-accent" disabled={busy} onClick={() => startCompare()}>
                开始比对
              </button>}
          </span>
        </div>
        <div className="btn-row" style={{ gap: 6, alignItems: 'center', flexWrap: 'wrap' }}>
          <input className="input" placeholder="键列(留空=自动取主键; 仅单表可填, 逗号分隔)" value={keyText}
            onChange={e => setKeyText(e.target.value)} style={{ flex: '1 1 16rem', minWidth: 0 }} />
          <input className="input" placeholder="排除的列(如 updated_at, 逗号分隔)" value={ignoreText}
            onChange={e => setIgnoreText(e.target.value)} style={{ flex: '1 1 14rem', minWidth: 0 }} />
        </div>
        {tableChoices.length > 0 && (
          <>
            <input className="input" placeholder="过滤表名" value={filter} onChange={e => setFilter(e.target.value)} />
            <div style={{ maxHeight: '9rem', overflow: 'auto', border: '1px solid var(--border)', borderRadius: 'var(--radius-sm)', padding: 6 }}>
              {tableChoices.map(n => (
                <label key={n} className="small" style={{ display: 'inline-flex', gap: 4, alignItems: 'center', marginRight: 12 }}>
                  <input type="checkbox" checked={picked.includes(n)}
                    onChange={e => setPicked(prev => e.target.checked ? [...prev, n] : prev.filter(x => x !== n))} />
                  <span className="mono">{n}</span>
                </label>
              ))}
            </div>
            {picked.length > 0 && (
              <button className="btn-glass-soft btn-glass-soft-sm" style={{ alignSelf: 'flex-start' }} onClick={() => setPicked([])}>
                清空表选择
              </button>
            )}
          </>
        )}
      </div>

      {running && (
        <div className="banner banner-info">
          正在比 {job?.current || '…'}（{job?.doneTables || 0}/{job?.tables || picked.length}）·
          已扫 基准 {(job?.scannedSrc || 0).toLocaleString()} 行 / 目标 {(job?.scannedDst || 0).toLocaleString()} 行 ·
          已发现 {job?.diffs || 0} 行差异
        </div>
      )}
      {applyMsg && <div className={totals.diffRows ? 'banner banner-warn' : 'banner banner-ok'}>{applyMsg}</div>}

      {!running && results.length > 0 && (
        <div className="btn-row" style={{ gap: 10, alignItems: 'center', flexWrap: 'wrap' }}>
          <b className="small">
            {totals.tables} 张表 · 补 {totals.inserts} 行 / 改 {totals.updates} 行 / 删 {totals.deletes} 行
            {totals.errs ? ` · ${totals.errs} 张比不了` : ''}
            {results.some(t => t.partial) ? ' · 有表未扫完' : ''}
          </b>
          <span style={{ marginLeft: 'auto' }} className="dim small">已选 {selectedRows.length} 行</span>
          <button className="btn-glass-soft btn-glass-soft-sm"
            onClick={() => setSel(new Set(results.flatMap(t => (t.rows || []).map(r => rowKey(t.table, r)))))}>
            全选({totals.diffRows})
          </button>
          <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => setSel(new Set())}>清空</button>
          <button className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-accent" disabled={busy || !selectedRows.length} onClick={apply}>
            执行选中({selectedRows.length} 行)
          </button>
        </div>
      )}

      {!running && results.length === 0 && job?.ok && (
        <div className="banner banner-ok">没有比对结果 —— 请确认已选表并点「开始比对」。</div>
      )}

      {results.map(td => <TableBlock key={td.table} td={td} sel={sel} setSel={setSel} openRow={openRow} setOpenRow={setOpenRow} />)}
      {confirmEl}
    </div>
  )
}

function TableBlock({ td, sel, setSel, openRow, setOpenRow }: {
  td: DataTableDiff
  sel: Set<string>
  setSel: (fn: (prev: Set<string>) => Set<string>) => void
  openRow: string
  setOpenRow: (v: string) => void
}) {
  const st = STATUS_META[td.status] || { text: td.status, cls: 'badge' }
  const toggle = (id: string, on: boolean) => setSel(prev => {
    const next = new Set(prev)
    if (on) next.add(id); else next.delete(id)
    return next
  })
  const groupByKind = (kind: string) => (td.rows || []).filter(r => r.kind === kind)
  const selectKind = (kind: string, on: boolean) => {
    const ids = groupByKind(kind).map(r => rowKey(td.table, r))
    setSel(prev => {
      const next = new Set(prev)
      for (const id of ids) { if (on) next.add(id); else next.delete(id) }
      return next
    })
  }

  return (
    <div className="db-advanced-block" style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
      <div className="btn-row" style={{ gap: 8, alignItems: 'baseline', flexWrap: 'wrap' }}>
        <b className="mono small">{td.table}</b>
        <span className={`badge ${st.cls}`}>{st.text}</span>
        <span className="dim small">
          键: <span className="mono">{(td.keyColumns || []).join(', ')}</span>
          {td.keySource ? `(${td.keySource})` : ''} · 行数 基准 {td.srcRows.toLocaleString()} / 目标 {td.dstRows.toLocaleString()}
        </span>
      </div>

      {td.status === 'error' && <div className="banner banner-err">{td.error}</div>}
      {td.partial && !!td.reason && <div className="banner banner-warn">{td.reason}</div>}
      {(td.skipped || []).length > 0 && (
        <div className="dim small">未参与比较: {td.skipped.join(' ; ')}</div>
      )}
      {(td.notes || []).length > 0 && (
        <div className="dim small" style={{ lineHeight: 1.7 }}>{td.notes.map((n, i) => <div key={i}>· {n}</div>)}</div>
      )}

      {['insert', 'update', 'delete'].map(kind => {
        const rows = groupByKind(kind)
        if (!rows.length) return null
        const meta = KIND_META[kind]
        const allOn = rows.every(r => sel.has(rowKey(td.table, r)))
        return (
          <div key={kind} style={{ marginTop: 4 }}>
            <div className="btn-row" style={{ gap: 8, alignItems: 'baseline' }}>
              <span className={`badge ${meta.badge}`}>{meta.title}({rows.length})</span>
              <span className="dim small">{meta.hint}</span>
              <button className="btn-glass-soft btn-glass-soft-sm" style={{ marginLeft: 'auto' }}
                onClick={() => selectKind(kind, !allOn)}>{allOn ? '取消本组' : '本组全选'}</button>
            </div>
            {rows.map(r => {
              const id = rowKey(td.table, r)
              const isOpen = openRow === id
              const changed = (r.cells || []).filter(c => c.differs)
              return (
                <div key={id} className="db-dd-row">
                  <div className="btn-row" style={{ gap: 6, alignItems: 'flex-start' }}>
                    <input type="checkbox" checked={sel.has(id)} style={{ marginTop: 3 }}
                      onChange={e => toggle(id, e.target.checked)} />
                    <span className="mono small" style={{ flex: '0 0 auto' }}>
                      {r.key.map(k => `${k.column}=${k.src}`).join(' , ')}
                    </span>
                    {kind === 'update' && (
                      <button className="btn-glass-soft btn-glass-soft-bare small" onClick={() => setOpenRow(isOpen ? '' : id)}>
                        {changed.length ? changed.map(c => `${c.column}: ${c.dst} → ${c.src}`).join(' , ') : '（无列变化）'}
                      </button>
                    )}
                    {kind !== 'update' && (
                      <button className="btn-glass-soft btn-glass-soft-bare small dim" onClick={() => setOpenRow(isOpen ? '' : id)}>
                        {r.cells.length} 列 · 看明细
                      </button>
                    )}
                  </div>
                  {isOpen && (
                    <div style={{ marginLeft: 22 }}>
                      <table className="db-dd-cells">
                        <thead><tr><th>列</th><th>基准侧</th><th>目标侧</th></tr></thead>
                        <tbody>
                          {(r.cells || []).map((c, i) => (
                            <tr key={i} className={c.differs ? 'diff' : ''}>
                              <td className="mono">{c.column}</td>
                              <td className="mono">{c.src}</td>
                              <td className="mono">{c.dst}</td>
                            </tr>
                          ))}
                        </tbody>
                      </table>
                      <pre className="code-block mono small" style={{ maxHeight: '7rem', overflow: 'auto', fontSize: '0.68rem' }}>
                        {(r.sqls || []).join('\n')}
                      </pre>
                    </div>
                  )}
                </div>
              )
            })}
          </div>
        )
      })}
    </div>
  )
}
