// 表结构/索引/DDL 文档面板 + 结构编辑器(结构变更单模式: 编辑后生成"列变更意图", 后端拼 ALTER 并预览, 确认执行)。
// 范围裁剪: 仅列级变更(加/删/改列); 索引/约束变更提示走 SQL。
// 方言分支/标识符引用/默认值与注释转义都在后端(apply-alter), 这里不再手搓 SQL 字符串。

import { useEffect, useMemo, useState } from 'react'
import { describeTable, applyAlter, type ColumnInfo, type IndexInfo, type AlterCol } from './api'
import { useConfirm } from '../../lib/hooks/useConfirm'
import { useToast } from '../../components/Toast'
import { SqlPreviewBody } from '../../components/common/SqlPreview'
import { humanizeDbError } from './dbErrors'

// 编辑态列(原始列 + 编辑字段; 新列标记 __new)
interface EditCol extends ColumnInfo {
  __new?: boolean
  __drop?: boolean
  __origName?: string
}

export default function DocPanel({
  connId,
  database,
  table,
  engine = '',
  onStructureChanged,
}: {
  connId: string
  database: string
  table: string
  engine?: string
  onStructureChanged?: () => void
}) {
  const [cols, setCols] = useState<ColumnInfo[]>([])
  const [idxs, setIdxs] = useState<IndexInfo[]>([])
  const [ddl, setDdl] = useState('')
  const [loading, setLoading] = useState(false)
  const [err, setErr] = useState('')
  const [tab, setTab] = useState<'cols' | 'idx' | 'ddl'>('cols')

  // 结构编辑态: null=只读浏览; 非空=编辑中(副本)
  const [editing, setEditing] = useState<EditCol[] | null>(null)
  const [applying, setApplying] = useState(false)
  // 结构变更确认走公共弹窗(带 ALTER 预览), 结果走 toast —— 原先是原生 confirm/alert, 弹出的东西跟全站不是一个体系
  const { confirm, confirmEl } = useConfirm()
  const toast = useToast()

  const fullReload = () => {
    setEditing(null)
    setLoading(true)
    setErr('')
    describeTable(connId, database, table)
      .then(d => {
        setCols(d.columns || [])
        setIdxs(d.indexes || [])
        setDdl(d.ddl || '')
      })
      .catch(e => setErr(e.message || '加载表结构失败'))
      .finally(() => setLoading(false))
  }

  useEffect(() => {
    if (!connId || !database || !table) {
      setCols([])
      setIdxs([])
      setDdl('')
      setEditing(null)
      return
    }
    fullReload()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [connId, database, table])

  // ── 编辑操作 ──
  const startEdit = () => setEditing(cols.map(c => ({ ...c, __origName: c.name })))
  const upd = (i: number, patch: Partial<EditCol>) =>
    setEditing(prev => prev ? prev.map((c, k) => (k === i ? { ...c, ...patch } : c)) : prev)
  const addCol = () =>
    setEditing(prev => prev ? [...prev, {
      name: 'new_column', type: 'VARCHAR(255)', nullable: true, key: '', default: null,
      comment: '', __new: true,
    } as EditCol] : prev)
  const markDrop = (i: number) =>
    setEditing(prev => prev ? prev.map((c, k) => (k === i ? { ...c, __drop: !c.__drop } : c)) : prev)

  // ── 列变更意图: 对比编辑态 vs 原始列, 只描述"哪一列变成什么样" ──
  // 不在这里拼 SQL: 方言差异(MySQL MODIFY/CHANGE vs 标准 ALTER COLUMN 拆多条)、标识符引用、
  // 默认值/注释转义都由后端 apply-alter 负责, 预览也是找它要的(confirm=false 不落库)。
  const colChanges = useMemo<AlterCol[]>(() => {
    if (!editing) return []
    const orig = new Map(cols.map(c => [c.name, c]))
    const out: AlterCol[] = []
    const want = (c: EditCol) => ({
      name: c.name, type: c.type, nullable: !!c.nullable,
      default: c.default == null ? '' : String(c.default), comment: c.comment || '',
    })
    for (const c of editing) if (c.__new && !c.__drop && c.name.trim()) out.push({ kind: 'add', ...want(c) })
    for (const c of editing) if (!c.__new && c.__drop) out.push({ kind: 'drop', name: c.name, nullable: !!c.nullable })
    for (const c of editing) {
      if (c.__new || c.__drop) continue
      const o = orig.get(c.__origName || c.name)
      if (!o) continue
      const changed = c.name !== o.name || c.type !== o.type || c.nullable !== o.nullable
        || (c.default ?? '') !== (o.default ?? '') || (c.comment ?? '') !== (o.comment ?? '')
      if (changed) out.push({ kind: 'modify', ...want(c), origName: o.name })
    }
    return out
  }, [editing, cols])

  const commitStructure = async () => {
    if (!colChanges.length) return
    setApplying(true)
    try {
      const pv = await applyAlter(connId, database, table, colChanges, false)
      if (!pv.ok) { toast.error(pv.error || '生成预览失败'); return }
      const sqls = pv.sqls || []
      const ok = await confirm(`确认执行 ${sqls.length} 条结构变更?`, {
        desc: '涉及删列时数据将丢失, 不可撤销。',
        content: <SqlPreviewBody sqls={sqls} caption="结构变更" />,
        okText: '执行',
        danger: true,
        maxWidth: 620,
      })
      if (!ok) return
      const r = await applyAlter(connId, database, table, colChanges, true)
      if (!r.ok) { toast.error(humanizeDbError(r.error || '执行失败')); return }
      toast.success(`结构变更完成（${r.count || sqls.length} 条）`)
      fullReload()
      onStructureChanged?.()
    } catch (e: any) {
      // 写锁/高危拦截这类后端拒绝走的是 403, 文案已经在 error 里, 别再加一层"执行失败"
      toast.error(String(e?.message || e))
    } finally {
      setApplying(false)
    }
  }

  if (!connId || !database || !table) {
    return <div className="db-empty">选择数据库和表后查看表结构</div>
  }
  if (loading) return <div className="log-loading">加载中...</div>
  if (err) return <div className="banner banner-err">{err}</div>

  const editCols = editing || cols.map(c => ({ ...c })) as EditCol[]

  return (
    <div className="db-doc">
      <div className="db-doc-tabs">
        <button className={tab === 'cols' ? 'active' : ''} onClick={() => setTab('cols')}>
          列 ({cols.length})
        </button>
        <button className={tab === 'idx' ? 'active' : ''} onClick={() => setTab('idx')}>
          索引 ({idxs.length})
        </button>
        <button className={tab === 'ddl' ? 'active' : ''} onClick={() => setTab('ddl')}>DDL</button>
        <span style={{ marginLeft: 'auto', display: 'flex', gap: 6 }}>
          {tab === 'cols' && !editing && (
            <button className="btn-glass-soft btn-glass-soft-sm" onClick={startEdit}>编辑表结构</button>
          )}
          {tab === 'cols' && editing && (
            <>
              <button className="btn-glass-soft btn-glass-soft-sm" onClick={addCol}>+ 加列</button>
              <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => setEditing(null)}>放弃编辑</button>
            </>
          )}
        </span>
      </div>

      {tab === 'cols' && (
        <>
          <div className="table-wrap">
            <table className="db-table">
              <thead>
                <tr>
                  <th>列名</th>
                  <th>类型</th>
                  <th>主键</th>
                  <th>可空</th>
                  <th>默认值</th>
                  <th>注释</th>
                  {editing && <th style={{ width: 60 }}>操作</th>}
                </tr>
              </thead>
              <tbody>
                {editCols.map((c, i) => {
                  const drop = !!c.__drop
                  return (
                    <tr key={`${c.__origName || c.name}-${i}`} style={drop ? { opacity: 0.45, textDecoration: 'line-through' } : undefined}>
                      <td>
                        {editing && !c.key?.includes('PRI') ? (
                          <input className="input input-sm" style={{ width: '9rem' }} value={c.name} onChange={e => upd(i, { name: e.target.value })} />
                        ) : (
                          <code>{c.name}{c.__new ? ' (新)' : ''}</code>
                        )}
                      </td>
                      <td>
                        {editing ? (
                          <input className="input input-sm" style={{ width: '9rem' }} value={c.type} onChange={e => upd(i, { type: e.target.value })} />
                        ) : (
                          <>{c.type}</>
                        )}
                      </td>
                      <td>{c.key === 'PRI' ? <span className="pill pill-ok">PRI</span> : c.key || ''}</td>
                      <td>
                        {editing ? (
                          <input type="checkbox" checked={c.nullable} onChange={e => upd(i, { nullable: e.target.checked })} />
                        ) : (
                          c.nullable ? 'YES' : <span style={{ color: 'var(--warn)' }}>NO</span>
                        )}
                      </td>
                      <td>
                        {editing ? (
                          <input className="input input-sm" style={{ width: '6rem' }} value={c.default ?? ''} onChange={e => upd(i, { default: e.target.value })} placeholder="(无)" />
                        ) : (
                          <code style={{ fontSize: '0.75rem' }}>{c.default || ''}</code>
                        )}
                      </td>
                      <td>
                        {editing ? (
                          <input className="input input-sm" style={{ width: '10rem' }} value={c.comment ?? ''} onChange={e => upd(i, { comment: e.target.value })} placeholder="(无)" />
                        ) : (
                          <span className="dim">{c.comment || ''}</span>
                        )}
                      </td>
                      {editing && (
                        <td>
                          {!c.key?.includes('PRI') && (
                            <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => markDrop(i)} title={drop ? '取消删除' : '标记删除此列'}>
                              {drop ? '恢复' : '删列'}
                            </button>
                          )}
                        </td>
                      )}
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>

          {editing && (
            <div className="db-doc-alter" style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
              <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                <b style={{ fontSize: '0.78rem' }}>待提交的结构变更</b>
                <span className="dim" style={{ fontSize: '0.6875rem' }}>
                  {colChanges.length} 处变更 · 点执行会先给出后端拼好的 ALTER 语句(走写锁护栏)
                </span>
                <button
                  className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-accent"
                  style={{ marginLeft: 'auto' }}
                  disabled={!colChanges.length || applying}
                  onClick={commitStructure}
                >
                  {applying ? '执行中...' : `执行 ${colChanges.length} 处变更`}
                </button>
              </div>
              {/* 实时列出的是"改哪一列成什么样"。具体 SQL 要按方言拼, 放在执行前的确认弹窗里 ——
                  不为这块预览每次敲键都去问一次库。 */}
              <pre className="code-block" style={{ margin: 0, maxHeight: '10rem', overflow: 'auto', fontSize: '0.72rem' }}>
                {colChanges.length
                  ? colChanges.map(c => c.kind === 'drop'
                    ? `删除列 ${c.name}`
                    : c.kind === 'add'
                      ? `新增列 ${c.name} ${c.type}${c.nullable ? '' : ' NOT NULL'}${c.default ? ` 默认 ${c.default}` : ''}`
                      : `改列 ${c.origName === c.name ? c.name : `${c.origName} → ${c.name}`} ${c.type}${c.nullable ? '' : ' NOT NULL'}${c.default ? ` 默认 ${c.default}` : ''}${c.comment ? ` 注释 ${c.comment}` : ''}`
                  ).join('\n')
                  : '— 无变更 —'}
              </pre>
            </div>
          )}
        </>
      )}

      {tab === 'idx' && (
        <div className="table-wrap">
          <table className="db-table">
            <thead>
              <tr>
                <th>索引名</th>
                <th>类型</th>
                <th>列</th>
              </tr>
            </thead>
            <tbody>
              {idxs.length === 0 ? (
                <tr><td colSpan={3} className="dim" style={{ textAlign: 'center', padding: 12 }}>无索引</td></tr>
              ) : idxs.map(i => (
                <tr key={i.name}>
                  <td><code>{i.name}</code></td>
                  <td>
                    {i.primary ? <span className="pill pill-ok">PRIMARY</span>
                      : i.unique ? <span className="pill pill-warn">UNIQUE</span>
                      : <span className="pill">INDEX</span>}
                  </td>
                  <td>{i.columns.map(c => <code key={c} style={{ marginRight: 6 }}>{c}</code>)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {tab === 'ddl' && (
        <pre className="code-block db-doc-ddl">{ddl || '— DDL 不可用 —'}</pre>
      )}
      {confirmEl}
    </div>
  )
}
