// 树状对象侧栏（对�?GoNavi Sidebar / dbx sidebar�?
//   连接(引擎图标+状态点+hover 操作�? �?�?�?[�?N)/视图(N)] �?对象(行数徽标)
// 懒加�? 点开才请求。顶部搜索框。表节点: 单击打开数据浏览, 右键菜单�?
import React from 'react'
import { useEffect, useMemo, useState } from 'react'
import {
  type ConnectionInfo, listConnections, listDatabases, listTables, testConnection, deleteConnection, describeTable, fetchTableDDL, fetchTableInserts,
} from './api'
import { EngineIcon, NodeIcon, ActionIcon } from './DbIcons'
import ContextMenu, { type ContextMenuItem } from './ContextMenu'

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
  onSyncDb, onOpenStatus, onOpenExplain, onNewQueryWithSQL, onExportTable,
  onRefresh,
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
  onSyncDb: (conn: ConnectionInfo, db: string) => void
  onOpenStatus: (conn: ConnectionInfo, db: string, table: string) => void
  onOpenExplain: (conn: ConnectionInfo, db: string, table: string) => void
  onNewQueryWithSQL: (conn: ConnectionInfo, db: string, sql: string) => void
  onExportTable: (conn: ConnectionInfo, db: string, table: string, format: 'csv' | 'xlsx') => void
  onRefresh: () => void
}) {
  const [filter, setFilter] = useState('')
  const [expanded, setExpanded] = useState<Set<string>>(new Set())
  const [dbCache, setDbCache] = useState<Record<string, string[]>>({})
  const [tablesCache, setTablesCache] = useState<Record<string, { tables: string[]; views: string[] }>>({})
  const [menu, setMenu] = useState<{ x: number; y: number; node: TreeNode } | null>(null)
  const [testing, setTesting] = useState<string | null>(null)
  const [activeKey, setActiveKey] = useState<string | null>(null)

  // 置顶表(localStorage)
  const [pins, setPins] = useState<string[]>(() => {
    try { return JSON.parse(localStorage.getItem('dbmanager:pinned') || '[]') } catch { return [] }
  })
  const pinKey = (connId: string, db: string, table: string) => `${connId}|${db}|${table}`
  const isPinned = (connId: string, db: string, table: string) => pins.includes(pinKey(connId, db, table))
  const togglePin = (connId: string, db: string, table: string) => {
    setPins(prev => {
      const k = pinKey(connId, db, table)
      const next = prev.includes(k) ? prev.filter(x => x !== k) : [k, ...prev]
      try { localStorage.setItem('dbmanager:pinned', JSON.stringify(next)) } catch { /* ignore */ }
      return next
    })
  }

  const isSystemDb = (db: string) => {
    return db === 'information_schema' || db === 'performance_schema' || db === 'mysql' || db === 'sys' || db.startsWith('pg_')
  }

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
    setActiveKey(node.key)
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

  // dbx TreeItem 原版行类: group flex items-center gap-2 min-h-7 py-1 px-2 relative
  const renderRow = (node: TreeNode, depth: number, children: React.ReactNode) => {
    const selected = activeKey === node.key
    const canExpand = !node.leaf
    const isOpen = expanded.has(node.key)
    return (
      <div
        className={`group flex cursor-default items-center gap-2 min-h-7 py-1 px-2 relative outline-none rounded-[0.25rem] hover:bg-accent${selected ? ' bg-black/[0.08]' : ''}`}
        style={{ paddingLeft: `${8 + depth * 16}px`, contain: 'layout style' }}
        onClick={() => onNodeClick(node)}
        onContextMenu={e => {
          e.preventDefault()
          const menuItems = buildMenuItems(node)
          if (menuItems.length > 0) setMenu({ x: e.clientX, y: e.clientY, node })
        }}
        title={node.label}
      >
        {canExpand ? (
          <button
            className="-m-0.5 flex h-4 w-4 shrink-0 items-center justify-center rounded-sm text-muted-foreground hover:bg-muted hover:text-foreground"
            onClick={e => { e.stopPropagation(); onNodeClick(node) }}
          >
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" style={{ transform: isOpen ? 'rotate(90deg)' : 'none', transition: 'transform .12s' }}>
              <path d="m9 18 6-6-6-6" />
            </svg>
          </button>
        ) : (
          <span className="w-3.5 h-3.5 shrink-0" />
        )}
        {children}
      </div>
    )
  }

  const buildMenuItems = (node: TreeNode): ContextMenuItem[] => {
    if (node.level === 'conn' && node.conn) {
      return [
        { label: '新建查询', icon: <ActionIcon kind="query" />, onClick: () => onNewQuery(node.conn!, node.db || '') },
        { label: '测试连接', icon: <ActionIcon kind="test" />, onClick: () => quickTest(node.conn!) },
        { divider: true },
        { label: '编辑连接', icon: <ActionIcon kind="edit" />, onClick: () => onEditConn(node.conn!) },
        { label: '删除连接', icon: <ActionIcon kind="delete" />, danger: true, onClick: () => remove(node.conn!) },
      ]
    }
    if (node.level === 'db' && node.conn && node.db) {
      const sys = isSystemDb(node.db)
      return [
        { label: '新建查询', icon: <ActionIcon kind="query" />, onClick: () => onNewQuery(node.conn!, node.db!) },
        { label: '刷新列表', icon: <ActionIcon kind="refresh" />, onClick: () => { loadTables(node.conn!.id, node.db!) } },
        { divider: 'heavy' },
        { label: '跨库同步此库', icon: <ActionIcon kind="transfer" />, onClick: () => onSyncDb(node.conn!, node.db!) },
        { divider: 'heavy' },
        { label: '复制库名', icon: <ActionIcon kind="copy" />, onClick: () => { navigator.clipboard?.writeText(node.db!); notify(true, `已复制 ${node.db}`) } },
        { divider: 'light' },
        { label: sys ? '系统库 (不可删除)' : '删除数据库', icon: <ActionIcon kind="delete" />, danger: true, disabled: sys, onClick: () => {} },
      ]
    }
    if ((node.level === 'table' || node.level === 'view') && node.conn && node.db && node.table) {
      const pinned = isPinned(node.conn.id, node.db, node.table)
      const isTable = node.level === 'table'
      // ── 数据 ──
      const dataItems: ContextMenuItem[] = [
        { label: '查看数据', icon: <ActionIcon kind="chart" />, onClick: () => onOpenTable(node.conn!, node.db!, node.table!, node.level === 'view') },
        { label: '表统计 / 状态', icon: <ActionIcon kind="gear" />, onClick: () => onOpenStatus(node.conn!, node.db!, node.table!) },
      ]
      // ── SQL 与结构 ──
      const sqlItems: ContextMenuItem[] = [
        { label: '新建查询 (FROM)', icon: <ActionIcon kind="query" />, onClick: () => onNewQuery(node.conn!, node.db!) },
        ...(isTable ? [{ label: '生成 SELECT 模板', icon: <ActionIcon kind="query" />, onClick: () => onNewQueryWithSQL(node.conn!, node.db!, `SELECT * FROM ${node.table} LIMIT 100`) }] : []),
        { label: '查看结构 / DDL', icon: <ActionIcon kind="doc" />, onClick: () => onOpenDoc(node.conn!, node.db!, node.table!) },
        ...(isTable ? [{ label: '执行计划 (EXPLAIN)', icon: <ActionIcon kind="search" />, onClick: () => onOpenExplain(node.conn!, node.db!, node.table!) }] : []),
      ]
      // ── 复制与导出 ──
      const copyItems: ContextMenuItem[] = [
        { label: '复制表名', icon: <ActionIcon kind="copy" />, onClick: () => { navigator.clipboard?.writeText(node.table!); notify(true, `已复制 ${node.table}`) } },
        { label: '复制建表 DDL', icon: <ActionIcon kind="copy" />, onClick: async () => {
            try {
              const ddl = await fetchTableDDL(node.conn!.id, node.db!, node.table!)
              await navigator.clipboard?.writeText(ddl)
              notify(true, `已复制 ${node.table} 建表 DDL`)
            } catch (e: any) { notify(false, '复制 DDL 失败: ' + e.message) }
          } },
      ]
      if (isTable) {
        copyItems.push({ label: '复制全表 INSERT', icon: <ActionIcon kind="copy" />, onClick: async () => {
            try {
              const r = await fetchTableInserts(node.conn!.id, node.db!, node.table!, 500)
              await navigator.clipboard?.writeText(r.text)
              notify(true, `已复制 ${node.table} 全表 INSERT (${r.rows} 行${r.truncated ? ', 截断至 500' : ''})`)
            } catch (e: any) { notify(false, '复制 INSERT 失败: ' + e.message) }
          } })
        copyItems.push({ divider: 'light' })
        copyItems.push({ label: '导出 CSV', icon: <ActionIcon kind="upload" />, onClick: () => onExportTable(node.conn!, node.db!, node.table!, 'csv') })
        copyItems.push({ label: '导出 XLSX', icon: <ActionIcon kind="upload" />, onClick: () => onExportTable(node.conn!, node.db!, node.table!, 'xlsx') })
      }
      // ── 维护 ──
      const maintainItems: ContextMenuItem[] = [
        { label: pinned ? '取消置顶' : '置顶表', icon: <ActionIcon kind="pin" />, onClick: () => togglePin(node.conn!.id, node.db!, node.table!) },
        { label: '刷新行数统计', icon: <ActionIcon kind="refresh" />, onClick: () => notify(true, `${node.table}: 统计已刷新`) },
      ]
      return [
        ...dataItems,
        { divider: 'heavy' },
        ...sqlItems,
        { divider: 'heavy' },
        ...copyItems,
        ...(isTable ? [{ divider: 'heavy' as const }] : [{ divider: 'heavy' as const }]),
        ...maintainItems,
      ]
    }
    return []
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

  const refreshAll = () => {
    setDbCache({}); setTablesCache({}); setExpanded(new Set())
    notify(true, '已刷新, 重新展开连接加载')
  }

  const onExportConns = () => {
    const payload = conns.map(c => ({ name: c.name, engine: c.engine, config: { ...c.config, password: undefined } }))
    const blob = new Blob([JSON.stringify(payload, null, 2)], { type: 'application/json' })
    const a = document.createElement('a')
    a.href = URL.createObjectURL(blob)
    a.download = `opscore-connections-${new Date().toISOString().slice(0, 10)}.json`
    a.click()
    URL.revokeObjectURL(a.href)
    notify(true, `已导出 ${payload.length} 个连接(不含密码)`)
  }

  const onImportConns = async (file: File) => {
    try {
      const list = JSON.parse(await file.text()) as Array<{ name: string; engine: string; config: any }>
      let ok = 0
      for (const item of list) {
        if (!item?.name || !item?.engine) continue
        try {
          const token = localStorage.getItem('opscore-token')
          await fetch('/api/dbmanager/connections', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json', ...(token ? { Authorization: `Bearer ${token}` } : {}) },
            body: JSON.stringify({ name: item.name + '(导入)', engine: item.engine, config: item.config || {}, password: '' }),
          })
          ok++
        } catch { /* 单条失败继续 */ }
      }
      notify(true, `导入完成: ${ok} 个连接(密码需重新填写)`)
      onConnsChange(await listConnections())
    } catch (e: any) {
      notify(false, '导入失败: ' + e.message)
    }
  }

  // 组装可见行
  const rows: TreeNode[] = []
  for (const c of visibleConns) {
    const ckey = `conn:${c.id}`
    const isConnOpen = expanded.has(ckey)
    rows.push({ key: ckey, level: 'conn', label: c.name, conn: c })
    if (!isConnOpen) continue
    const dbs = dbCache[c.id] || []
    for (const db of dbs) {
      const dkey = `${ckey}|db:${db}`
      if (f && !db.toLowerCase().includes(f) && !c.name.toLowerCase().includes(f)) continue
      const isDbOpen = expanded.has(dkey)
      rows.push({ key: dkey, level: 'db', label: db, conn: c, db })
      if (!isDbOpen) continue
      const ck2 = `${c.id}|${db}`
      const objs = tablesCache[ck2]
      if (!objs) continue
      // �?视图不再作为树节�? 改为平铺卡片(�?db 节点下方连续渲染)
      const tables = objs.tables.filter(t => !f || t.toLowerCase().includes(f))
      const views = objs.views.filter(t => !f || t.toLowerCase().includes(f))
      // 用特殊的 group 节点标记, 渲染时展开为卡片
      if (tables.length > 0) {
        rows.push({ key: `${dkey}|group:table`, level: 'group', label: `表 (${tables.length})`, conn: c, db, count: tables.length })
      }
      if (views.length > 0) {
        rows.push({ key: `${dkey}|group:view`, level: 'group', label: `视图 (${views.length})`, conn: c, db, count: views.length })
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
    <div className="db-tree" style={{ fontSize: '14px' }}>
      <div className="db-tree-header">
        <input
          className="db-tree-search w-full h-6 rounded border border-border bg-background px-2 text-xs focus:outline-none focus:ring-1"
          placeholder="搜索连接 / 库 / 表..."
          value={filter}
          onChange={e => setFilter(e.target.value)}
        />
        <div className="db-tree-header-actions">
          <button className="db-tree-tool" onClick={() => { refreshAll(); onRefresh() }} title="刷新全部缓存">⟳</button>
          <button className="db-tree-tool" onClick={onExportConns} title="导出连接配置 (JSON)">↑</button>
          <label className="db-tree-tool" title="导入连接配置 (JSON)">
            ↓
            <input type="file" accept=".json" style={{ display: 'none' }} onChange={e => {
              const f = e.target.files?.[0]
              if (f) onImportConns(f)
              e.target.value = ''
            }} />
          </label>
          <button className="db-tree-add" onClick={onNewConn} title="新建连接">+</button>
        </div>
      </div>
      <div className="db-tree-body">
        {rows.length === 0 && <div className="db-empty-sm">{conns.length === 0 ? '暂无连接, 点右上 + 新建' : '无匹配对象'}</div>}
        {rows.map(node => {
          const depth = node.key.split('|').length - 1
          const isObj = node.level === 'table' || node.level === 'view'
          const isConn = node.level === 'conn'
          const isDb = node.level === 'db'
          const isGroup = node.level === 'group'
          const selected = activeKey === node.key

          // group 节点(表/视图分组) + 展开后的表行(dbx: 表就是普通行)
          if (isGroup && node.conn && node.db) {
            const ck2 = `${node.conn.id}|${node.db}`
            const objs = tablesCache[ck2]
            const tables = objs ? objs.tables.filter(t => !f || t.toLowerCase().includes(f)) : []
            const views = objs ? objs.views.filter(t => !f || t.toLowerCase().includes(f)) : []
            const isTableGroup = node.label.startsWith('表')
            const allItems = isTableGroup ? tables : views
            const items = [...allItems].sort((a, b) => {
              const pa = isPinned(node.conn!.id, node.db!, a) ? 0 : 1
              const pb = isPinned(node.conn!.id, node.db!, b) ? 0 : 1
              return pa - pb
            })
            const itemLevel = isTableGroup ? 'table' : 'view'

            return (
              <React.Fragment key={node.key}>
                {renderRow(node, depth, (
                  <>
                    <NodeIcon level={node.level} />
                    <span className="truncate">{node.label}</span>
                    {node.count !== undefined && (
                      <span className="ml-0.5 inline-flex h-4 items-center rounded bg-muted px-1.5 text-[10px] text-muted-foreground">{node.count}</span>
                    )}
                  </>
                ))}
                {expanded.has(node.key) && items.map(t => {
                  const tblKey = `${node.key}|${t}`
                  const tblNode: TreeNode = { key: tblKey, level: itemLevel, label: t, conn: node.conn, db: node.db, table: t, leaf: true }
                  return renderRow(tblNode, depth + 1, (
                    <>
                      <span className="relative flex h-3.5 w-3.5 shrink-0">
                        {itemLevel === 'view' ? <NodeIcon level="view" /> : <NodeIcon level="table" />}
                      </span>
                      <span className="truncate">
                        {t}{isPinned(node.conn!.id, node.db!, t) ? ' 📌' : ''}
                      </span>
                      <span className="ml-auto shrink-0 text-xs text-muted-foreground opacity-0 group-hover:opacity-100">@{node.db}</span>
                    </>
                  ))
                })}
              </React.Fragment>
            )
          }

          return renderRow(node, depth, (
            <>
              {isConn && node.conn ? <EngineIcon engine={node.conn.engine} size={14} /> : <NodeIcon level={node.level} />}
              <span className="truncate">{node.label}</span>
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
            </>
          ))
        })}
      </div>
      {menu && menu.node.conn && (
        <ContextMenu
          x={menu.x}
          y={menu.y}
          items={buildMenuItems(menu.node)}
          onClose={() => setMenu(null)}
        />
      )}
    </div>
  )
}
