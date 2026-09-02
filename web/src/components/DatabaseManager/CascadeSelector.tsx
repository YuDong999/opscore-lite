// 库/表级联选择器: 选库 → 加载表 → 选表 → 触发回调。
// v2: 系统库分组折叠 + 窗口式表列表 + 库名前缀显示。

import { useEffect, useState, useMemo } from 'react'
import { listDatabases, listTables, type TableInfo } from './api'

// 系统库判定规则（通用 + 引擎特例）
const SYSTEM_SCHEMA_RULES: Array<(db: string) => boolean> = [
  d => d === 'information_schema',
  d => d === 'performance_schema',
  d => d === 'mysql',
  d => d === 'sys',
  d => d.startsWith('pg_'),
  d => d.startsWith('__'),
]

function isSystemSchema(db: string): boolean {
  return SYSTEM_SCHEMA_RULES.some(rule => rule(db))
}

// 表类型图标
function tableIcon(type: string): string {
  if (type === 'VIEW') return '◱'
  if (type === 'SYSTEM VIEW') return '◱'
  return '▦'
}

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
  const [showSystem, setShowSystem] = useState(false)
  const [systemOpen, setSystemOpen] = useState(false)

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

  const filteredTables = useMemo(() => {
    const q = tableFilter.trim().toLowerCase()
    return tables.filter(t =>
      !q || t.name.toLowerCase().includes(q) || (t.comment && t.comment.toLowerCase().includes(q))
    )
  }, [tables, tableFilter])

  // 按系统/用户分组
  const { systemDbs, userDbs } = useMemo(() => {
    const sys: string[] = []
    const usr: string[] = []
    databases.forEach(d => (isSystemSchema(d) ? sys : usr).push(d))
    return { systemDbs: sys, userDbs: usr }
  }, [databases])

  const renderDbItem = (d: string) => {
    const active = database === d
    return (
      <li
        key={d}
        className={`db-cascade-item ${active ? 'active' : ''}`}
        onClick={() => onDatabaseChange(d)}
        title={d}
      >
        <span className="db-cascade-db-icon">◆</span>
        <span className="db-cascade-db-name">{d}</span>
      </li>
    )
  }

  return (
    <div className="db-cascade">
      {/* 数据库列表 */}
      <div className="db-cascade-section db-cascade-db-section">
        <div className="db-cascade-label-row">
          <span className="db-cascade-label">数据库</span>
          <span className="db-cascade-db-count">
            {userDbs.length} 个库
            {systemDbs.length > 0 && ` · ${systemDbs.length} 个系统库`}
          </span>
        </div>

        {loadingDb ? (
          <div className="log-loading">加载中...</div>
        ) : dbError ? (
          <div className="banner banner-err">{dbError}</div>
        ) : databases.length === 0 ? (
          <div className="db-empty-sm">无可访问的库</div>
        ) : (
          <div className="db-cascade-db-list">
            {/* 用户库 */}
            <ul className="db-cascade-list db-cascade-list-compact">
              {userDbs.map(renderDbItem)}
            </ul>

            {/* 系统库折叠 */}
            {systemDbs.length > 0 && (
              <div className="db-system-group">
                <button
                  className="db-system-toggle"
                  onClick={() => setSystemOpen(!systemOpen)}
                >
                  <span className="db-system-arrow">{systemOpen ? '▾' : '▸'}</span>
                  <span className="db-system-label">系统库</span>
                  <span className="db-system-count">{systemDbs.length}</span>
                </button>
                {systemOpen && (
                  <ul className="db-cascade-list db-cascade-list-compact db-system-list">
                    {systemDbs.map(renderDbItem)}
                  </ul>
                )}
              </div>
            )}
          </div>
        )}
      </div>

      {/* 表列表 — 窗口式 */}
      <div className="db-cascade-section db-cascade-grow db-cascade-tbl-section">
        <div className="db-cascade-label-row">
          <span className="db-cascade-label">
            {database ? (
              <>表/视图 <span className="db-cascade-tbl-count">{filteredTables.length}</span></>
            ) : (
              '表/视图'
            )}
          </span>
          {database && (
            <div className="db-cascade-tbl-actions">
              <label className="db-cascade-show-system">
                <input
                  type="checkbox"
                  checked={showSystem}
                  onChange={e => setShowSystem(e.target.checked)}
                />
                <span>系统表</span>
              </label>
              <input
                className="input db-cascade-search"
                placeholder="过滤..."
                value={tableFilter}
                onChange={e => setTableFilter(e.target.value)}
              />
            </div>
          )}
        </div>

        {!database ? (
          <div className="db-empty-sm">请先选择数据库</div>
        ) : loadingTbl ? (
          <div className="log-loading">加载中...</div>
        ) : filteredTables.length === 0 ? (
          <div className="db-empty-sm">{tableFilter ? '没有匹配的表' : '库下没有表'}</div>
        ) : (
          <div className="db-tbl-window">
            {filteredTables.map(t => {
              const active = table === t.name
              return (
                <div
                  key={t.name}
                  className={`db-tbl-card ${active ? 'db-tbl-card-active' : ''}`}
                  onClick={() => onTableChange(t.name)}
                  title={t.comment || `${t.type}: ${t.name}`}
                >
                  <span className="db-tbl-card-icon">{tableIcon(t.type)}</span>
                  <span className="db-tbl-card-name">{t.name}</span>
                  <span className="db-tbl-card-db">{database}</span>
                  {t.comment && <span className="db-tbl-card-comment">{t.comment}</span>}
                  <button
                    className="db-tbl-card-close"
                    onClick={e => { e.stopPropagation(); onTableChange('') }}
                    title="关闭"
                  >
                    ×
                  </button>
                </div>
              )
            })}
          </div>
        )}
      </div>
    </div>
  )
}
