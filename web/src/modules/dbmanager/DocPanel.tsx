// 表结构/索引/DDL 文档面板 + 结构编辑器(结构变更单模式: 编辑后 diff 生成 ALTER 预览, 确认执行)。
// 范围裁剪: 仅列级变更(加/删/改列); 索引/约束变更提示走 SQL。ALTER 方言按引擎分支(MySQL MODIFY / 标准 ALTER)。

import { useEffect, useMemo, useState } from 'react'
import { describeTable, runQueryRaw, type ColumnInfo, type IndexInfo } from './api'

// 编辑态列(原始列 + 编辑字段; 新列标记 __new)
interface EditCol extends ColumnInfo {
  __new?: boolean
  __drop?: boolean
  __origName?: string
}

const QUOTE_ID = (engine: string, name: string) =>
  engine === 'mysql' || engine === 'mariadb' || engine === 'goldendb'
    ? '`' + name.replace(/`/g, '``') + '`'
    : '"' + name.replace(/"/g, '""') + '"'

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

  const qTable = QUOTE_ID(engine || 'mysql', table.includes('.') ? table : table)
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

  // ── ALTER 生成: 对比编辑态 vs 原始列 ──
  const alterStatements = useMemo(() => {
    if (!editing) return []
    const orig = new Map(cols.map(c => [c.name, c]))
    const out: string[] = []
    const t = qTable
    const mysql = engine === 'mysql' || engine === 'mariadb' || engine === 'goldendb'
    const colDef = (c: EditCol) => {
      const base = `${QUOTE_ID(engine, c.name)} ${c.type || 'TEXT'}${c.nullable ? '' : ' NOT NULL'}${c.default != null && c.default !== '' ? ` DEFAULT ${c.default}` : ''}${c.comment ? (mysql ? ` COMMENT '${c.comment.replace(/'/g, "''")}'` : '') : ''}`
      return base
    }
    // 1) 新增列
    for (const c of editing) {
      if (c.__new && !c.__drop && c.name.trim()) out.push(`ALTER TABLE ${t} ADD COLUMN ${colDef(c)};`)
    }
    // 2) 删除列
    for (const c of editing) {
      if (!c.__new && c.__drop) out.push(`ALTER TABLE ${t} DROP COLUMN ${QUOTE_ID(engine, c.name)};`)
    }
    // 3) 修改列(原名仍存在且未删): 改名/类型/可空/默认/注释 有任一变化即生成
    for (const c of editing) {
      if (c.__new || c.__drop) continue
      const o = orig.get(c.__origName || c.name)
      if (!o) continue
      const changed = c.name !== o.name || c.type !== o.type || c.nullable !== o.nullable
        || (c.default ?? '') !== (o.default ?? '') || (c.comment ?? '') !== (o.comment ?? '')
      if (!changed) continue
      if (mysql) {
        out.push(`ALTER TABLE ${t} MODIFY COLUMN ${colDef(c)};`)
      } else {
        // 标准/PG: 类型与改名分开; 简化: 改名列名不变走 ALTER COLUMN, 改名单独 RENAME
        if (c.name !== o.name) out.push(`ALTER TABLE ${t} RENAME COLUMN ${QUOTE_ID(engine, o.name)} TO ${QUOTE_ID(engine, c.name)};`)
        if (c.type !== o.type || c.nullable !== o.nullable || (c.default ?? '') !== (o.default ?? '')) {
          out.push(`ALTER TABLE ${t} ALTER COLUMN ${QUOTE_ID(engine, c.name)} TYPE ${c.type || 'TEXT'};`)
          out.push(`ALTER TABLE ${t} ALTER COLUMN ${QUOTE_ID(engine, c.name)} ${c.nullable ? 'DROP NOT NULL' : 'SET NOT NULL'};`)
          if ((c.default ?? '') !== (o.default ?? '')) {
            out.push(c.default != null && c.default !== ''
              ? `ALTER TABLE ${t} ALTER COLUMN ${QUOTE_ID(engine, c.name)} SET DEFAULT ${c.default};`
              : `ALTER TABLE ${t} ALTER COLUMN ${QUOTE_ID(engine, c.name)} DROP DEFAULT;`)
          }
        }
        if (c.comment !== (o.comment ?? '')) {
          out.push(`COMMENT ON COLUMN ${t}.${QUOTE_ID(engine, c.name)} IS '${(c.comment || '').replace(/'/g, "''")}';`)
        }
      }
    }
    return out
  }, [editing, cols, engine])

  const applyAlter = async () => {
    if (!alterStatements.length) return
    if (!confirm(`确认执行 ${alterStatements.length} 条结构变更? 涉及删列时数据将丢失。`)) return
    setApplying(true)
    try {
      const r = await runQueryRaw(connId, alterStatements.join('\n'))
      if (r.data.code === 'write_locked') {
        alert('写操作被拦截: 请先解锁写模式')
        return
      }
      alert('结构变更完成')
      fullReload()
      onStructureChanged?.()
    } catch (e: any) {
      alert('执行失败: ' + (e.message || e))
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
                <b style={{ fontSize: '0.78rem' }}>ALTER 预览</b>
                <span className="dim" style={{ fontSize: '0.6875rem' }}>{alterStatements.length} 条语句 · 确认后执行(走写锁护栏)</span>
                <button
                  className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-accent"
                  style={{ marginLeft: 'auto' }}
                  disabled={!alterStatements.length || applying}
                  onClick={applyAlter}
                >
                  {applying ? '执行中...' : `执行 ${alterStatements.length} 条变更`}
                </button>
              </div>
              <pre className="code-block" style={{ margin: 0, maxHeight: '10rem', overflow: 'auto', fontSize: '0.72rem' }}>
                {alterStatements.length ? alterStatements.join('\n') : '— 无变更 —'}
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
    </div>
  )
}
