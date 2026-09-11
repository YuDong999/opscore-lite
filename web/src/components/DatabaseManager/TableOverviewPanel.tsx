// 库级表概览(GoNavi 同款): 行数/数据大小/索引大小/引擎/注释/建表·更新时间
// 卡片/列表双视图 + 排序 + 点击表名跳数据页。数据源 /table-overview(信息库估算)。

import { useEffect, useMemo, useState } from 'react'
import { getTableOverview, type TableOverviewRow } from './api'

type SortKey = 'name' | 'rows' | 'dataSize' | 'indexSize' | 'updatedAt'

const fmtBytes = (n: number): string => {
  if (n < 1024) return `${n} B`
  if (n < 1048576) return `${(n / 1024).toFixed(1)} KB`
  if (n < 1073741824) return `${(n / 1048576).toFixed(1)} MB`
  return `${(n / 1073741824).toFixed(2)} GB`
}
const fmtRows = (n: number): string => {
  if (n < 1000) return String(n)
  if (n < 1000000) return `${(n / 1000).toFixed(1)}K`
  return `${(n / 1000000).toFixed(1)}M`
}

export default function TableOverviewPanel({
  connId,
  database,
  onOpenTable,
}: {
  connId: string
  database: string
  onOpenTable?: (table: string) => void
}) {
  const [rows, setRows] = useState<TableOverviewRow[]>([])
  const [loading, setLoading] = useState(true)
  const [err, setErr] = useState('')
  const [view, setView] = useState<'cards' | 'list'>('cards')
  const [sortKey, setSortKey] = useState<SortKey>('rows')
  const [desc, setDesc] = useState(true)
  const [filter, setFilter] = useState('')

  useEffect(() => {
    setLoading(true)
    setErr('')
    getTableOverview(connId, database)
      .then(setRows)
      .catch(e => setErr(e.message || '加载表概览失败'))
      .finally(() => setLoading(false))
  }, [connId, database])

  const sorted = useMemo(() => {
    const q = filter.trim().toLowerCase()
    const list = rows.filter(r => !q || r.name.toLowerCase().includes(q) || (r.comment || '').toLowerCase().includes(q))
    const dir = desc ? -1 : 1
    return [...list].sort((a, b) => {
      if (sortKey === 'name') return a.name.localeCompare(b.name) * dir
      if (sortKey === 'updatedAt') return String(a.updatedAt).localeCompare(String(b.updatedAt)) * dir
      return ((a[sortKey] as number) - (b[sortKey] as number)) * dir
    })
  }, [rows, sortKey, desc, filter])

  const totals = useMemo(() => ({
    data: rows.reduce((a, r) => a + r.dataSize, 0),
    index: rows.reduce((a, r) => a + r.indexSize, 0),
    rows: rows.reduce((a, r) => a + Math.max(r.rows, 0), 0),
  }), [rows])

  if (loading) return <div className="log-loading">加载表概览中...</div>
  if (err) return <div className="banner banner-err">{err}</div>

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 8, height: '100%', minHeight: 0 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'nowrap' }}>
        <span className="db-engine-badge">表概览 · {database}</span>
        <span className="dim" style={{ fontSize: '0.72rem' }}>
          {rows.length} 表 · 约 {fmtRows(totals.rows)} 行 · 数据 {fmtBytes(totals.data)} · 索引 {fmtBytes(totals.index)}
        </span>
        <input className="input input-sm" style={{ width: '10rem', marginLeft: 'auto' }}
          placeholder="过滤表名/注释..." value={filter} onChange={e => setFilter(e.target.value)} />
        <select className="input db-page-size" value={sortKey} onChange={e => setSortKey(e.target.value as SortKey)} title="排序">
          <option value="rows">按行数</option>
          <option value="dataSize">按数据大小</option>
          <option value="indexSize">按索引大小</option>
          <option value="name">按名称</option>
          <option value="updatedAt">按更新时间</option>
        </select>
        <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => setDesc(d => !d)} title="切换升降序">
          {desc ? '↓ 降' : '↑ 升'}
        </button>
        <div className="db-view-toggle">
          {([['cards', '卡片'], ['list', '列表']] as const).map(([k, label]) => (
            <button key={k} className={`btn-glass-soft btn-glass-soft-sm ${view === k ? 'active' : ''}`} onClick={() => setView(k)}>{label}</button>
          ))}
        </div>
      </div>

      {sorted.length === 0 && <div className="db-empty">无匹配表</div>}

      {view === 'cards' ? (
        <div style={{ flex: 1, minHeight: 0, overflowY: 'auto', display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(240px, 1fr))', gap: 8, alignContent: 'start' }}>
          {sorted.map(r => (
            <div key={r.name} className="db-drv-card" style={{ cursor: onOpenTable ? 'pointer' : 'default' }} onClick={() => onOpenTable?.(r.name)}
              title={onOpenTable ? '点击打开表数据' : undefined}>
              <div className="db-drv-card-head">
                <span className="db-engine-dot" style={{ background: '#16a34a' }} />
                <span className="db-drv-name" style={{ overflow: 'hidden', textOverflow: 'ellipsis' }}>{r.name}</span>
                {r.rows > 0 && <span className="pill">{fmtRows(r.rows)} 行</span>}
              </div>
              <div className="db-drv-meta dim">
                <span>数据 {fmtBytes(r.dataSize)}</span>
                <span>·</span>
                <span>索引 {fmtBytes(r.indexSize)}</span>
                {r.engine && <span>·</span>}
                {r.engine && <span>{r.engine}</span>}
              </div>
              {(r.comment || r.updatedAt) && (
                <div className="dim" style={{ fontSize: '0.6875rem', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                  {r.comment || r.updatedAt}
                </div>
              )}
            </div>
          ))}
        </div>
      ) : (
        <div className="table-wrap">
          <table className="db-table">
            <thead>
              <tr>
                <th>表名</th><th>行数(约)</th><th>数据大小</th><th>索引大小</th><th>引擎</th><th>注释</th><th>更新时间</th>
              </tr>
            </thead>
            <tbody>
              {sorted.map(r => (
                <tr key={r.name} style={{ cursor: onOpenTable ? 'pointer' : 'default' }} onClick={() => onOpenTable?.(r.name)}>
                  <td><code>{r.name}</code>{r.comment && <span className="dim" style={{ marginLeft: 6, fontSize: '0.6875rem' }}>{r.comment}</span>}</td>
                  <td>{fmtRows(r.rows)}</td>
                  <td>{fmtBytes(r.dataSize)}</td>
                  <td>{fmtBytes(r.indexSize)}</td>
                  <td>{r.engine || '-'}</td>
                  <td className="dim">{r.comment || ''}</td>
                  <td className="dim">{r.updatedAt || '-'}</td>
                </tr>
              ))}
              {sorted.length === 0 && <tr><td colSpan={7} className="dim" style={{ textAlign: 'center', padding: 12 }}>无匹配表</td></tr>}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}
