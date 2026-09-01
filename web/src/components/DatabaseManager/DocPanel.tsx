// 表结构/索引/DDL 文档面板。

import { useEffect, useState } from 'react'
import { describeTable, type ColumnInfo, type IndexInfo } from './api'

export default function DocPanel({
  connId,
  database,
  table,
}: {
  connId: string
  database: string
  table: string
}) {
  const [cols, setCols] = useState<ColumnInfo[]>([])
  const [idxs, setIdxs] = useState<IndexInfo[]>([])
  const [ddl, setDdl] = useState('')
  const [loading, setLoading] = useState(false)
  const [err, setErr] = useState('')
  const [tab, setTab] = useState<'cols' | 'idx' | 'ddl'>('cols')

  useEffect(() => {
    if (!connId || !database || !table) {
      setCols([])
      setIdxs([])
      setDdl('')
      return
    }
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
  }, [connId, database, table])

  if (!connId || !database || !table) {
    return <div className="db-empty">选择数据库和表后查看表结构</div>
  }
  if (loading) return <div className="log-loading">加载中...</div>
  if (err) return <div className="banner banner-err">{err}</div>

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
      </div>

      {tab === 'cols' && (
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
              </tr>
            </thead>
            <tbody>
              {cols.map(c => (
                <tr key={c.name}>
                  <td><code>{c.name}</code></td>
                  <td>{c.type}</td>
                  <td>{c.key === 'PRI' ? <span className="pill pill-ok">PRI</span> : c.key || ''}</td>
                  <td>{c.nullable ? 'YES' : <span style={{ color: 'var(--warn)' }}>NO</span>}</td>
                  <td><code style={{ fontSize: '0.75rem' }}>{c.default || ''}</code></td>
                  <td className="dim">{c.comment || ''}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
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
