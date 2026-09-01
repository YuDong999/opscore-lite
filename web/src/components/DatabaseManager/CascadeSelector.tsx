// 库/表级联选择器: 选库 → 加载表 → 选表 → 触发回调。

import { useEffect, useState } from 'react'
import { listDatabases, listTables, type TableInfo } from './api'

export default function CascadeSelector({
  connId,
  database,
  table,
  onDatabaseChange,
  onTableChange,
}: {
  connId: string
  database: string
  table: string
  onDatabaseChange: (db: string) => void
  onTableChange: (t: string) => void
}) {
  const [databases, setDatabases] = useState<string[]>([])
  const [tables, setTables] = useState<TableInfo[]>([])
  const [loadingDb, setLoadingDb] = useState(false)
  const [loadingTbl, setLoadingTbl] = useState(false)
  const [dbError, setDbError] = useState('')
  const [tableFilter, setTableFilter] = useState('')

  useEffect(() => {
    if (!connId) {
      setDatabases([])
      return
    }
    setLoadingDb(true)
    setDbError('')
    listDatabases(connId)
      .then(dbs => {
        setDatabases(dbs)
        // 如果当前选中的库不在列表里, 自动清空
        if (database && !dbs.includes(database)) {
          onDatabaseChange('')
        } else if (!database && dbs.length > 0) {
          onDatabaseChange(dbs[0])
        }
      })
      .catch(e => setDbError(e.message || '加载库失败'))
      .finally(() => setLoadingDb(false))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [connId])

  useEffect(() => {
    if (!connId || !database) {
      setTables([])
      return
    }
    setLoadingTbl(true)
    listTables(connId, database)
      .then(ts => {
        setTables(ts)
        if (table && !ts.find(t => t.name === table)) onTableChange('')
      })
      .catch(() => setTables([]))
      .finally(() => setLoadingTbl(false))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [connId, database])

  const filteredTables = tables.filter(t =>
    !tableFilter || t.name.toLowerCase().includes(tableFilter.toLowerCase()),
  )

  return (
    <div className="db-cascade">
      <div className="db-cascade-section">
        <div className="db-cascade-label">数据库</div>
        {loadingDb ? (
          <div className="log-loading">加载中...</div>
        ) : dbError ? (
          <div className="banner banner-err">{dbError}</div>
        ) : databases.length === 0 ? (
          <div className="db-empty-sm">无可访问的库</div>
        ) : (
          <ul className="db-cascade-list">
            {databases.map(d => (
              <li
                key={d}
                className={`db-cascade-item ${database === d ? 'active' : ''}`}
                onClick={() => onDatabaseChange(d)}
                title={d}
              >
                {d}
              </li>
            ))}
          </ul>
        )}
      </div>

      <div className="db-cascade-section db-cascade-grow">
        <div className="db-cascade-label-row">
          <span className="db-cascade-label">表/视图 ({tables.length})</span>
          <input
            className="input db-cascade-search"
            placeholder="过滤..."
            value={tableFilter}
            onChange={e => setTableFilter(e.target.value)}
          />
        </div>
        {!database ? (
          <div className="db-empty-sm">请先选择数据库</div>
        ) : loadingTbl ? (
          <div className="log-loading">加载中...</div>
        ) : filteredTables.length === 0 ? (
          <div className="db-empty-sm">{tableFilter ? '没有匹配的表' : '库下没有表'}</div>
        ) : (
          <ul className="db-cascade-list">
            {filteredTables.map(t => (
              <li
                key={t.name}
                className={`db-cascade-item ${table === t.name ? 'active' : ''}`}
                onClick={() => onTableChange(t.name)}
                title={t.comment || `${t.type}: ${t.name}`}
              >
                <span className={`db-tbl-type db-tbl-${t.type.replace(/\s+/g, '_')}`}>
                  {t.type === 'VIEW' ? 'V' : 'T'}
                </span>
                <span className="db-cascade-item-name">{t.name}</span>
                {t.comment && <span className="db-cascade-item-comment">{t.comment}</span>}
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  )
}
