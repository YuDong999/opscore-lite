// 树状对象侧栏（对标 GoNavi Sidebar / dbx sidebar）:
//   连接(引擎图标+状态点+hover 操作组) → 库 → [表(N)/视图(N)] → 对象(行数徽标)
// 懒加载: 点开才请求。顶部搜索框。表节点: 单击打开数据浏览, 右键菜单。

import { useEffect, useMemo, useState } from 'react'
import {
  type ConnectionInfo, listDatabases, listTables, testConnection, deleteConnection,
} from './api'
import { EngineIcon, NodeIcon, ActionIcon } from './DbIcons'

interface TreeNode {
  key: string
  level: 'conn' | 'db' | 'group' | 'table' | 'view'
  label: string
  conn?: ConnectionInfo
  db?: string
  table?: string
  count?: number
  leaf?: boolean
}

export default function ConnectionTree({
  conns, selectedConnId, onOpenTable, onNewQuery, onOpenDoc, onSelectConn, onEditConn, onNewConn, onConnsChange, notify,
}: {
  conns: ConnectionInfo[]
  selectedConnId?: string
  onOpenTable: (conn: ConnectionInfo, db: string, table: string, isView?: boolean) => void
  onNewQuery: (conn: ConnectionInfo, db: string) => void
  onOpenDoc: (conn: ConnectionInfo, db: string, table: string) => void
  onSelectConn: (conn: ConnectionInfo) => void
  onEditConn: (conn: ConnectionInfo) => void
  onNewConn: () => void
  onConnsChange: (list: ConnectionInfo[]) => void
  notify: (ok: boolean, msg: string) => void
}) {
  const [filter, setFilter] = useState('')
  const [expanded, setExpanded] = useState<Set<string>>(new Set())
  const [dbCache, setDbCache] = useState<Record<string, string[]>>({})
  const [tablesCache, setTablesCache] = useState<Record<string, { tables: string[]; views: string[] }>>({})
  const [menu, setMenu] = useState<{ x: number; y: number; node: TreeNode } | null>(null)
  const [testing, setTesting] = useState<string | null>(null)

  const f = filter.trim().toLowerCase()
  const visibleConns = useMemo(
    () => conns.filter(c => !f || c.name.toLowerCase().includes(f)),
    [conns, f],
  )

  const loadDbs = async (connId: string) => {
    if (dbCache[connId]) return
    try {
      const dbs = await listDatabases(connId)
      setDbCache(prev => ({ ...prev, [connId]: dbs || [] }))
    } catch { setDbCache(prev => ({ ...prev, [connId]: [] })) }
  }

  const loadTables = async (connId: string, db: string) => {
    const ck = `${connId}|${db}`
    if (tablesCache[ck]) return
    try {
      const ts = await listTables(connId, db)
      setTablesCache(prev => ({
        ...prev,
        [ck]: {
          tables: ts.filter(t => t.type !== 'VIEW').map(t => t.name),
          views: ts.filter(t => t.type === 'VIEW').map(t => t.name),
        },
      }))
    } catch { setTablesCache(prev => ({ ...prev, [ck]: { tables: [], views: [] } })) }
  }

  const toggle = (key: string) => {
    setExpanded(prev => {
      const next = new Set(prev)
      if (next.has(key)) next.delete(key); else next.add(key)
      return next
    })
  }

  const onNodeClick = (node: TreeNode) => {
    if (node.level === 'conn' && node.conn) {
      onSelectConn(node.conn)
      toggle(node.key)
      if (!expanded.has(node.key)) loadDbs(node.conn.id)
      return
    }
    if (node.level === 'db' || node.level === 'group') { toggle(node.key); return }
    if ((node.level === 'table' || node.level === 'view') && node.conn && node.db && node.table) {
      onOpenTable(node.conn, node.db, node.table, node.level === 'view')
    }
  }

  const quickTest = async (c: ConnectionInfo) => {
    setTesting(c.id)
    try {
      const r = await testConnection({ id: c.id })
      notify(r.ok, r.ok ? `${c.name}: ${r.version || '连接成功'}` : `${c.name}: ${r.error}`)
    } catch (e: any) {
      notify(false, `${c.name}: ${e.message}`)
    } finally { setTesting(null) }
  }

  const remove = async (c: ConnectionInfo) => {
    if (!confirm(`确认删除连接「${c.name}」?`)) return
    try {
      await deleteConnection(c.id)
      notify(true, `已删除 ${c.name}`)
      onConnsChange(conns.filter(x => x.id !== c.id))
    } catch (e: any) {
      notify(false, '删除失败: ' + e.message)
    }
  }

  // 组装可见行
  const rows: TreeNode[] = []
  for (const c of visibleConns) {
    const ckey = `conn:${c.id}`
    const isOpen = expanded.has(ckey)
    rows.push({ key: ckey, level: 'conn', label: c.name, conn: c })
    if (!isOpen) continue
    const dbs = dbCache[c.id] || []
    for (const db of dbs) {
      const dkey = `${ckey}|db:${db}`
      if (f && !db.toLowerCase().includes(f) && !c.name.toLowerCase().includes(f)) continue
      rows.push({ key: dkey, level: 'db', label: db, conn: c, db })
      if (!expanded.has(dkey)) continue
      const ck2 = `${c.id}|${db}`
      const objs = tablesCache[ck2]
      const groups: Array<{ label: string; level: 'table' | 'view'; items: string[] }> = objs ? [
        { label: '表', level: 'table', items: objs.tables },
        { label: '视图', level: 'view', items: objs.views },
      ] : []
      for (const g of groups) {
        const gkey = `${dkey}|${g.level}`
        const items = g.items.filter(t => !f || t.toLowerCase().includes(f))
        if (g.level === 'view' && items.length === 0 && objs) continue
        rows.push({ key: gkey, level: 'group', label: g.label, conn: c, db, count: items.length })
        if (!expanded.has(gkey)) continue
        for (const t of items) {
          rows.push({ key: `${gkey}|${t}`, level: g.level, label: t, conn: c, db, table: t, leaf: true })
        }
      }
    }
  }

  // 懒加载触发
  useEffect(() => {
    for (const key of expanded) {
      const [connPart, dbPart] = key.split('|')
      if (key.startsWith('conn:') && !dbPart) {
        const connId = key.slice(5)
        if (connId && !dbCache[connId]) loadDbs(connId)
      }
      if (dbPart && dbPart.startsWith('db:')) {
        const connId = connPart.slice(5)
        const db = dbPart.slice(3)
        if (connId && db && !tablesCache[`${connId}|${db}`]) loadTables(connId, db)
      }
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [expanded])

  useEffect(() => {
    if (!menu) return
    const close = () => setMenu(null)
    window.addEventListener('click', close)
    return () => window.removeEventListener('click', close)
  }, [menu])

  return (
    <div className="db-tree">
      <div className="db-tree-header">
        <input
          className="input db-tree-search"
          placeholder="搜索连接 / 库 / 表..."
          value={filter}
          onChange={e => setFilter(e.target.value)}
        />
        <button className="db-tree-add" onClick={onNewConn} title="新建连接">+</button>
      </div>
      <div className="db-tree-body">
        {rows.length === 0 && <div className="db-empty-sm">{conns.length === 0 ? '暂无连接, 点右上 + 新建' : '无匹配对象'}</div>}
        {rows.map(node => {
          const depth = node.key.split('|').length - 1
          const isObj = node.level === 'table' || node.level === 'view'
          const isConn = node.level === 'conn'
          const selected = selectedConnId === node.conn?.id && (isConn || isObj || node.level === 'db')
          return (
            <div
              key={node.key}
              className={`db-tree-node lv-${node.level}${selected ? ' selected' : ''}`}
              style={{ paddingLeft: `${0.3 + depth * 0.9}rem` }}
              onClick={() => onNodeClick(node)}
              onContextMenu={e => {
                if (!isObj) return
                e.preventDefault()
                setMenu({ x: e.clientX, y: e.clientY, node })
              }}
              title={node.label}
            >
              <span className={`db-tree-caret${expanded.has(node.key) && !node.leaf ? ' open' : ''}${node.leaf ? ' leaf' : ''}`} />
              {isConn && node.conn ? <EngineIcon engine={node.conn.engine} /> : <NodeIcon level={node.level} />}
              <span className="db-tree-label">{node.label}</span>
              {node.level === 'group' && node.count !== undefined && <span className="db-tree-count">{node.count}</span>}
              {isConn && node.conn && (
                <span className="db-tree-actions" onClick={e => e.stopPropagation()}>
                  <button title="测试连接" onClick={() => quickTest(node.conn!)}>
                    {testing === node.conn.id ? <span className="db-spin" /> : <ActionIcon kind="test" />}
                  </button>
                  <button title="编辑" onClick={() => onEditConn(node.conn!)}>
                    <ActionIcon kind="edit" />
                  </button>
                  <button title="删除" className="danger" onClick={() => remove(node.conn!)}>
                    <ActionIcon kind="delete" />
                  </button>
                </span>
              )}
            </div>
          )
        })}
      </div>
      {menu && menu.node.conn && menu.node.db && menu.node.table && (
        <div className="db-tree-menu" style={{ left: menu.x, top: menu.y }}>
          <div className="db-tree-menu-title">{menu.node.table}</div>
          <button onClick={() => { onOpenTable(menu.node.conn!, menu.node.db!, menu.node.table!, menu.node.level === 'view'); setMenu(null) }}>查看数据</button>
          <button onClick={() => { onNewQuery(menu.node.conn!, menu.node.db!); setMenu(null) }}>新建查询</button>
          <button onClick={() => { onOpenDoc(menu.node.conn!, menu.node.db!, menu.node.table!); setMenu(null) }}>查看结构 / DDL</button>
          <button onClick={() => { navigator.clipboard?.writeText(menu.node.table!); setMenu(null) }}>复制表名</button>
        </div>
      )}
    </div>
  )
}
