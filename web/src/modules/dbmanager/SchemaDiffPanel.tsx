// 结构对比视图: 选两侧(连接/库/模式) → 选表 → 看差异 → 勾选执行。
//
// 与"跨库同步"是两回事: 同步是"把数据搬过去"(整表建新), 这里回答的是"两边哪里不一样、
// 要发哪几条 DDL 才能对齐"。所以比对口径与生成语句全部在后端(sync 包), 这里只发
// "比哪两侧 + 勾选了哪几条变更的 key" —— 一次 SQL 文本都不发出去。
//
// 三个刻意的取舍:
//  1) 预览默认**不可编辑**, 要改才切到编辑态: 改过的文本会原样发到目标库(仍走写锁/风险链/审计),
//     但"按 key 重算"这条默认路径不因此松动 —— 见下面 editOpen 的注释。
//  2) 破坏性项(删列/删索引/删表)**默认不勾**, 勾上才进批次。
//  3) 执行后自动重比一次: 只有"再比 = 0 差异"才算真收敛, 残差会原样列出来(不报"成功")。

import { useEffect, useMemo, useState } from 'react'
import { useToast } from '../../components/Toast'
import { useConfirm } from '../../lib/hooks/useConfirm'
import { SqlPreviewBody } from '../../components/common/SqlPreview'
import {
  type ConnectionInfo, type SchemaDiffEnd, type SchemaDiffOptions, type SchemaTableDiff,
  listDatabases, listSchemas, listTables, schemaDiff, schemaDiffApply,
} from './api'

const KIND_LABELS: Record<string, string> = {
  table_create: '新建表', table_drop: '删除表',
  column_add: '加列', column_drop: '删列', column_modify: '改列',
  index_add: '建索引', index_drop: '删索引',
  fk_add: '建外键', fk_drop: '删外键',
  pk_modify: '主键差异', autoinc_differs: '自增差异',
}

const ACTION_GROUPS: Array<{ key: string; title: string; hint: string }> = [
  { key: 'create', title: '目标侧缺失', hint: '将在目标建出来' },
  { key: 'modify', title: '两边都有但不同', hint: '按勾选发 ALTER' },
  { key: 'delete', title: '仅目标侧存在', hint: '删除会丢数据, 默认不勾' },
]

// 导出给「数据对比」复用: 两端选择器的语义完全一样(连接/库/模式, 模式列表为空=两级引擎),
// 两处各写一遍迟早会漂成两套口径。
export function SidePickers({ label, conns, connId, setConnId, db, setDb, schema, setSchema, dbs, schemas }: {
  label: string; conns: ConnectionInfo[]
  connId: string; setConnId: (v: string) => void
  db: string; setDb: (v: string) => void
  schema: string; setSchema: (v: string) => void
  dbs: string[]; schemas: string[]
}) {
  return (
    <div className="db-sync-field" style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
      <span className="dim small">{label}</span>
      <div style={{ display: 'flex', gap: 4, flexWrap: 'wrap' }}>
        <select className="input" value={connId} onChange={e => setConnId(e.target.value)} style={{ minWidth: 0, flex: '1 1 8rem' }}>
          <option value="">选择连接…</option>
          {conns.map(c => <option key={c.id} value={c.id}>{c.name} · {c.engine}</option>)}
        </select>
        <select className="input" value={db} onChange={e => setDb(e.target.value)} style={{ minWidth: 0, flex: '1 1 7rem' }}>
          <option value="">选择库…</option>
          {dbs.map(d => <option key={d} value={d}>{d}</option>)}
        </select>
        {schemas.length > 0 && (
          <select className="input" value={schema} onChange={e => setSchema(e.target.value)} style={{ minWidth: 0, flex: '0 1 7rem' }}
            title="该引擎有模式层级; 留空 = 用连接的默认模式">
            <option value="">模式(默认)</option>
            {schemas.map(s => <option key={s} value={s}>{s}</option>)}
          </select>
        )}
      </div>
    </div>
  )
}

export default function SchemaDiffPanel({ conns, activeConnId, presetDb, presetTable }: {
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

  const [picked, setPicked] = useState<string[]>(presetTable ? [presetTable] : [])   // 空 = 两侧表名并集
  const [filter, setFilter] = useState('')
  const [opts, setOpts] = useState<SchemaDiffOptions>({ indexes: true, foreignKeys: true, comments: true })
  const [diffs, setDiffs] = useState<SchemaTableDiff[] | null>(null)
  const [notes, setNotes] = useState<string[]>([])
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [busy, setBusy] = useState(false)
  const [applyMsg, setApplyMsg] = useState('')
  // 预览可编辑: 改过的文本会原样发到目标库(仍走写锁/风险链/审计)。
  // 这不是新增的信任面 —— 本模块的查询页本来就能执行任意 SQL; 省掉检查才是问题, 所以后端没为它开第二条路。
  const [editOpen, setEditOpen] = useState(false)
  const [editText, setEditText] = useState('')

  // 连接变化时拉它的库/模式能力(与同步页同一口径: 模式列表为空 = 两级引擎)
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

  const ends = useMemo((): [SchemaDiffEnd, SchemaDiffEnd] => [
    { id: srcId, database: srcDb, ...(srcSchema ? { schema: srcSchema } : {}) },
    { id: dstId, database: dstDb, ...(dstSchema ? { schema: dstSchema } : {}) },
  ], [srcId, srcDb, srcSchema, dstId, dstDb, dstSchema])

  const allKeys = useMemo(() => (diffs || []).flatMap(td => td.changes.map(ch => ch.key)), [diffs])
  const destructiveKeys = useMemo(
    () => new Set((diffs || []).flatMap(td => td.changes.filter(ch => ch.destructive).map(ch => ch.key))),
    [diffs])
  const summary = useMemo(() => {
    let changes = 0, destructive = 0, sqls = 0
    for (const td of diffs || []) {
      changes += td.changes.length
      destructive += td.changes.filter(c => c.destructive).length
      sqls += td.sqls.length
    }
    return { tables: (diffs || []).length, changes, destructive, sqls }
  }, [diffs])

  const swap = () => {
    setSrcId(dstId); setSrcDb(dstDb); setSrcSchema(dstSchema)
    setDstId(srcId); setDstDb(srcDb); setDstSchema(srcSchema)
    setDiffs(null); setSelected(new Set()); setApplyMsg('')
  }

  const compare = async () => {
    if (!srcId || !dstId || !srcDb || !dstDb) { toast.error('两侧都要选好连接和库'); return }
    setBusy(true); setApplyMsg('')
    try {
      const r = await schemaDiff(ends[0], ends[1], picked, opts)
      if (!r.ok) { toast.error(r.error || '比对失败'); setDiffs(null); return }
      const ds = r.tables || []
      setDiffs(ds)
      setNotes(r.notes || [])
      // 默认勾"非破坏性"的那几项: 删列/删表这类要人明确勾选才进批次
      setSelected(new Set(ds.flatMap(td => td.changes.filter(c => !c.destructive).map(c => c.key))))
      if (!ds.length) toast.success('两侧结构一致, 没有差异')
    } catch (e: any) {
      toast.error(String(e?.message || e))
    } finally {
      setBusy(false)
    }
  }

  const selectedSqls = useMemo(() => {
    const out: string[] = []
    for (const td of diffs || []) for (const ch of td.changes) if (selected.has(ch.key)) out.push(...ch.sqls)
    return out
  }, [diffs, selected])

  // 语句之间用**空行**分隔: 生成的 CREATE TABLE 自带换行, 按分号切会把 DEFAULT 'a;b' 切坏。
  const parsedEdit = useMemo(() => editText
    .split(/\n[ 	]*\n/).map(x => x.trim().replace(/;$/, '')).filter(Boolean), [editText])
  const openEditor = () => { setEditText(selectedSqls.join('\n\n')); setEditOpen(true) }
  const resetEdit = () => setEditText(selectedSqls.join('\n\n'))
  const batch = editOpen ? parsedEdit : selectedSqls

  const apply = async () => {
    if (!batch.length) { toast.error(editOpen ? '编辑框里没有可执行的语句' : '没有勾选任何变更'); return }
    const destructive = editOpen ? /DROP\s+(TABLE|COLUMN|INDEX|CONSTRAINT|FOREIGN)/i.test(batch.join(' '))
      : [...selected].some(k => destructiveKeys.has(k))
    const ok = await confirm(`在目标侧执行 ${batch.length} 条结构变更?`, {
      desc: editOpen
        ? `你选了「编辑 SQL」这 ${batch.length} 条按编辑框里的文本原样执行 —— 不再与比对结果对齐, 但仍要过写锁与风险确认。${destructive ? '其中含 DROP, 数据丢失不可撤销。' : ''}`
        : destructive
          ? '勾选里包含破坏性变更(删列/删索引/删表), 数据丢失不可撤销。'
          : '语句由后端按目标方言生成, 在一个事务里执行; MySQL 系 DDL 会隐式提交, 中途失败前面已生效的撤不回。',
      content: <SqlPreviewBody sqls={batch} caption={editOpen ? '编辑后的 SQL' : '比对生成的语句'} />,
      okText: '执行', danger: destructive, maxWidth: 680,
    })
    if (!ok) return
    setBusy(true)
    try {
      const r = await schemaDiffApply(ends[0], ends[1], picked, opts, editOpen ? [] : [...selected], editOpen ? batch : undefined)
      if (!r.ok) {
        setApplyMsg(r.error || '执行失败')
        toast.error(r.error || '执行失败')
        return
      }
      if (r.noop) { toast.info(r.message || '选中项已无差异, 未执行'); }
      else toast.success(`已执行 ${r.count} 条语句, 正在重新比对确认…`)
      // 执行后自动重比: 收敛才算完, 残差原样列出来
      const again = await schemaDiff(ends[0], ends[1], picked, opts)
      if (again.ok) {
        const ds = again.tables || []
        setDiffs(ds)
        setNotes(again.notes || [])
        setSelected(new Set(ds.flatMap(td => td.changes.filter(c => !c.destructive).map(c => c.key))))
        setApplyMsg(ds.length ? `执行完成, 但仍有 ${summary.changes} 项差异未处理` : '执行完成, 两侧结构已一致')
      }
    } catch (e: any) {
      setApplyMsg(String(e?.message || e))
      toast.error(String(e?.message || e))
    } finally {
      setBusy(false)
    }
  }

  const tableChoices = useMemo(() => {
    const set = new Set<string>([...srcTables, ...dstTables])
    const arr = [...set].sort()
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
        <div className="btn-row" style={{ gap: 10, alignItems: 'center' }}>
          <label className="dim small" style={{ display: 'flex', gap: 4, alignItems: 'center' }}>
            <input type="checkbox" checked={!!opts.indexes} onChange={e => setOpts({ ...opts, indexes: e.target.checked })} />索引
          </label>
          <label className="dim small" style={{ display: 'flex', gap: 4, alignItems: 'center' }}>
            <input type="checkbox" checked={!!opts.foreignKeys} onChange={e => setOpts({ ...opts, foreignKeys: e.target.checked })} />外键
          </label>
          <label className="dim small" style={{ display: 'flex', gap: 4, alignItems: 'center' }}>
            <input type="checkbox" checked={!!opts.comments} onChange={e => setOpts({ ...opts, comments: e.target.checked })} />注释
          </label>
          <span className="dim small" style={{ marginLeft: 8 }}>
            表范围: {picked.length ? `已指定 ${picked.length} 张` : '两侧全部表(按表名配对)'}
          </span>
          <button className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-accent" style={{ marginLeft: 'auto' }}
            disabled={busy} onClick={compare}>{busy ? '处理中…' : '开始比对'}</button>
        </div>
        {tableChoices.length > 0 && (
          <>
            <input className="input" placeholder="过滤表名(不勾任何项 = 比全部)" value={filter} onChange={e => setFilter(e.target.value)} />
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
              <button className="btn-glass-soft btn-glass-soft-sm" style={{ alignSelf: 'flex-start' }} onClick={() => setPicked([])}>清空表选择(改回全库)</button>
            )}
          </>
        )}
      </div>

      {applyMsg && <div className={diffs && diffs.length ? 'banner banner-warn' : 'banner banner-ok'}>{applyMsg}</div>}

      {diffs && (
        <>
          <div className="btn-row" style={{ gap: 10, alignItems: 'center' }}>
            <b className="small">
              {summary.tables} 张表有差异 · {summary.changes} 项 · {summary.sqls} 条语句
              {summary.destructive ? ` · ${summary.destructive} 项破坏性(默认不勾)` : ''}
            </b>
            <span style={{ marginLeft: 'auto' }} className="dim small">已选 {[...selected].length} 项</span>
            <button className="btn-glass-soft btn-glass-soft-sm"
              onClick={() => setSelected(new Set(allKeys))}>全选({allKeys.length})</button>
            <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => setSelected(new Set())}>清空</button>
            <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => (editOpen ? setEditOpen(false) : openEditor())}>
              {editOpen ? '收起 SQL 编辑' : '编辑 SQL'}
            </button>
            <button className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-accent" disabled={busy || !batch.length} onClick={apply}>
              执行{editOpen ? '' : '选中'}({batch.length} 条)
            </button>
          </div>

          {editOpen && (
            <div className="db-advanced-block" style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
              <div className="btn-row" style={{ alignItems: 'baseline', gap: 8 }}>
                <span className="dim small">
                  编辑框里的文本就是将要执行的语句，<b>语句之间空一行分隔</b>（按分号切会切坏 <span className="mono">DEFAULT 'a;b'</span> 这类值）
                </span>
                <button className="btn-glass-soft btn-glass-soft-sm" style={{ marginLeft: 'auto' }} onClick={resetEdit}>
                  恢复成后端生成的 {selectedSqls.length} 条
                </button>
              </div>
              <textarea className="input mono" rows={10} spellCheck={false} value={editText}
                style={{ width: '100%', resize: 'vertical', fontSize: '0.72rem', lineHeight: 1.6 }}
                onChange={e => setEditText(e.target.value)} />
              <span className="dim small">解析出 {parsedEdit.length} 条 · 与生成结果{parsedEdit.join('\n') === selectedSqls.join('\n') ? '一致' : '已不同(将按文本执行)'}</span>
            </div>
          )}

          {!summary.tables && !applyMsg && <div className="banner banner-ok">两侧结构一致, 没有需要执行的变更。</div>}

          {notes.length > 0 && (
            <div className="dim small" style={{ lineHeight: 1.7 }}>
              {notes.map((n, i) => <div key={i}>· {n}</div>)}
            </div>
          )}

          {ACTION_GROUPS.map(g => {
            const rows = (diffs || []).filter(td => td.action === g.key)
            if (!rows.length) return null
            return (
              <div key={g.key} className="db-advanced-block">
                <div className="btn-row" style={{ alignItems: 'baseline', gap: 8 }}>
                  <b className="small">{g.title}({rows.length})</b>
                  <span className="dim small">{g.hint}</span>
                </div>
                {rows.map(td => (
                  <div key={td.table} style={{ marginTop: 4 }}>
                    <span className="mono small"><b>{td.table}</b></span>
                    {td.changes.map(ch => (
                      <div key={ch.key} style={{ display: 'flex', gap: 6, alignItems: 'flex-start', padding: '2px 0 2px 10px' }}>
                        <input type="checkbox" checked={selected.has(ch.key)} style={{ marginTop: 2 }}
                          onChange={e => setSelected(prev => {
                            const next = new Set(prev)
                            if (e.target.checked) next.add(ch.key); else next.delete(ch.key)
                            return next
                          })} />
                        <span className={`badge ${ch.destructive ? 'badge-danger' : 'badge-warn'}`} style={{ minWidth: '4.6rem', textAlign: 'center' }}>
                          {KIND_LABELS[ch.kind] || ch.kind}
                        </span>
                        <span className="mono small" style={{ minWidth: '9rem' }}>{ch.object}</span>
                        <span className="dim small" style={{ flex: 1 }}>{ch.detail.join(' ; ')}</span>
                      </div>
                    ))}
                    {td.sqls.length > 0 && (
                      <pre className="code-block mono small" style={{ margin: '2px 0 0 16px', maxHeight: '7rem', overflow: 'auto', fontSize: '0.68rem' }}>
                        {td.sqls.join('\n')}
                      </pre>
                    )}
                  </div>
                ))}
              </div>
            )
          })}
        </>
      )}
      {confirmEl}
    </div>
  )
}
