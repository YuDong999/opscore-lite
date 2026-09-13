// 树状对象侧栏(对齐 GoNavi Sidebar / dbx sidebar 的连接树形态)。
//   连接(引擎图标+状态点+hover 操作) -> 库 -> [模式(三级引擎)] -> 表/视图(行数徽标) 等对象节点。
// 懒加载: 点开才请求。顶部搜索框。表节点: 单击打开数据浏览, 右键菜单提供更多操作。
import React from 'react'
import { useEffect, useMemo, useState } from 'react'
import {
  type ConnectionInfo, type DbObject, listConnections, listDatabases, listSchemas, listTables, listObjects, getObjectDefinition, getTableCounts, testConnection, deleteConnection, updateConnection, describeTable, fetchTableDDL, fetchTableInserts, runQueryRaw,
} from './api'
import { EngineIcon, NodeIcon, ActionIcon } from './DbIcons'
import ContextMenu, { type ContextMenuItem } from './ContextMenu'

// 系统库/系统对象判定: 灰色置底便于识别
function isSysDbName(name: string): boolean {
  return name === 'information_schema' || name === 'performance_schema' || name === 'mysql' || name === 'sys'
    || name.startsWith('pg_') || name.startsWith('__')
}
function isSysObjName(name: string): boolean {
  const dot = name.indexOf('.')
  const sch = dot > 0 ? name.slice(0, dot) : ''
  const base = dot > 0 ? name.slice(dot + 1) : name
  return sch === 'information_schema' || sch === 'pg_catalog' || /^(pg_|sql_)/.test(base)
}
function fmtCount(n: number): string {
  if (n < 0) return '~'
  if (n < 1000) return String(n)
  if (n < 1000000) return (n / 1000).toFixed(1).replace(/\.0$/, '') + 'K'
  return (n / 1000000).toFixed(1).replace(/\.0$/, '') + 'M'
}

function escapeHtml(s: string): string {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
}

// ── 第二层导航页签(GoNavi 类别过滤): 全部/表/视图/序列/函数/存储包/事件 ──
type ObjCategory = 'all' | 'table' | 'view' | 'sequence' | 'function' | 'procedure' | 'event'
const OBJ_CATEGORY_ORDER: ObjCategory[] = ['all', 'table', 'view', 'sequence', 'function', 'procedure', 'event']
const OBJ_CATEGORY_LABEL: Record<ObjCategory, string> = {
  all: '全部', table: '表', view: '视图', sequence: '序列', function: '函数', procedure: '存储包', event: '事件',
}
const DB_KIND_PER_GROUP: Partial<Record<ObjCategory, DbObject['kind']>> = {
  view: 'VIEW', function: 'FUNCTION', procedure: 'PROCEDURE', event: 'EVENT', sequence: 'SEQUENCE',
}
const GROUP_ITEM_LEVEL: Record<Exclude<ObjCategory, 'all'>, TreeNodeLevel> = {
  table: 'table', view: 'view', sequence: 'sequence', function: 'function', procedure: 'procedure', event: 'event',
}
// 单击可查看 DDL 的对象级(单击打开 DDL 新窗口)
const OBJ_LEVEL_TO_KIND: Partial<Record<TreeNodeLevel, DbObject['kind']>> = {
  view: 'VIEW', function: 'FUNCTION', procedure: 'PROCEDURE', event: 'EVENT', trigger: 'TRIGGER', sequence: 'SEQUENCE',
}

type TreeNodeLevel = 'conn' | 'db' | 'group' | 'schema' | 'table' | 'view' | 'connGroup' | 'function' | 'procedure' | 'event' | 'trigger' | 'sequence'

interface DbObjectsCache {
  tables: string[]
  views: string[]
  objects: DbObject[]
}

// 按第二层类别页签过滤对象名(+行层级): 全部=表/视图/序列/函数/存储包/事件合并平铺
function itemsForCategory(objs: DbObjectsCache | undefined, cat: ObjCategory, pfx: string, f: string): Array<{ name: string; level: TreeNodeLevel }> {
  if (!objs) return []
  const out: Array<{ name: string; level: TreeNodeLevel }> = []
  const seen = new Set<string>()
  const push = (n: string, level: TreeNodeLevel) => {
    if (seen.has(n)) return
    const okName = (!pfx || n.startsWith(pfx)) && (!f || n.toLowerCase().includes(f))
    if (okName) { seen.add(n); out.push({ name: n, level }) }
  }
  if (cat === 'all' || cat === 'table') for (const n of objs.tables) push(n, 'table')
  if (cat === 'all' || cat === 'view') for (const n of objs.views) push(n, 'view')
  if (cat === 'all') {
    for (const o of objs.objects) push(o.name, objKindLevel(o.kind))
  } else {
    const k = DB_KIND_PER_GROUP[cat]
    if (k) for (const o of objs.objects) if (o.kind === k) push(o.name, GROUP_ITEM_LEVEL[cat])
  }
  return out
}
function objKindLevel(kind: string): TreeNodeLevel {
  const k = kind.toUpperCase()
  if (k === 'VIEW' || k === 'MATERIALIZED VIEW') return 'view'
  if (k === 'FUNCTION') return 'function'
  if (k === 'PROCEDURE') return 'procedure'
  if (k === 'EVENT') return 'event'
  if (k === 'TRIGGER') return 'trigger'
  if (k === 'SEQUENCE') return 'sequence'
  return 'table'
}

interface TreeNode {
  key: string
  level: TreeNodeLevel
  label: string
  conn?: ConnectionInfo
  db?: string
  table?: string
  count?: number
  schema?: string
  leaf?: boolean
  group?: string
  sys?: boolean
}

export default function ConnectionTree({
  conns, selectedConnId, onOpenTable, onNewQuery, onOpenDoc, onSelectConn, onEditConn, onNewConn, onConnsChange, notify,
  onSyncDb, onSyncTable, onSyncSchema, onOpenEr, onOpenOverview, onNewTable, onExportSchema, onOpenDash, onOpenStatus, onOpenExplain, onNewQueryWithSQL, onExportTable,
  onRefresh, onToggleSide,
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
  onSyncTable?: (conn: ConnectionInfo, db: string, table: string) => void
  onSyncSchema?: (conn: ConnectionInfo, db: string, schema: string) => void
  onOpenEr?: (conn: ConnectionInfo, db: string, table?: string) => void
  onNewTable?: (conn: ConnectionInfo, db: string) => void
  onOpenDash?: (conn: ConnectionInfo) => void
  onExportSchema?: (conn: ConnectionInfo, db: string) => void
  onOpenOverview?: (conn: ConnectionInfo, db: string) => void
  onOpenStatus: (conn: ConnectionInfo, db: string, table: string) => void
  onOpenExplain: (conn: ConnectionInfo, db: string, table: string) => void
  onNewQueryWithSQL: (conn: ConnectionInfo, db: string, sql: string) => void
  onExportTable: (conn: ConnectionInfo, db: string, table: string, format: 'csv' | 'xlsx') => void
  onRefresh: () => void
  onToggleSide?: () => void
}) {
  const [filter, setFilter] = useState('')
  const [expanded, setExpanded] = useState<Set<string>>(new Set())
  // 钻取模式(=类别页签非"全部")下默认展开、但允许手动收起: 记录被收起的节点
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set())
  const [dbCache, setDbCache] = useState<Record<string, string[]>>({})
  const [tablesCache, setTablesCache] = useState<Record<string, DbObjectsCache>>({})
  const [menu, setMenu] = useState<{ x: number; y: number; node: TreeNode } | null>(null)
  const [testing, setTesting] = useState<string | null>(null)
  const [activeKey, setActiveKey] = useState<string | null>(null)
  // GoNavi 第二层类别页签: 全部/表/视图/序列/函数/存储包/事件(过滤树显示)
  const [category, setCategory] = useState<ObjCategory>('all')
  // 非"全部"类别 = 自动钻取模式: 强制展开并只显示该类对象及其直系上级链, 无匹配的库/模式/连接整段隐藏(对齐 GoNavi filterV2ExplorerTreeByKind)
  const drill = category !== 'all'
  // dbx 式分组移动面板(替代 prompt): 右键"移动到分组…"时展开
  const [moveToGroupTarget, setMoveToGroupTarget] = useState<ConnectionInfo | null>(null)
  const [newGroupName, setNewGroupName] = useState('')

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
      testConnection({ id: connId }).then(r => setConnHealth(prev => ({ ...prev, [connId]: r.ok ? 'ok' : 'fail' }))).catch(() => setConnHealth(prev => ({ ...prev, [connId]: 'fail' })))
      setDbCache(prev => ({ ...prev, [connId]: dbs || [] }))
    } catch { setDbCache(prev => ({ ...prev, [connId]: [] })) }
  }

  const loadTables = async (connId: string, db: string) => {
    const ck = `${connId}|${db}`
    if (tablesCache[ck]) {
      // 已缓存: 行数统计(ANALYZE 后会变化)仍每次展开刷新
      getTableCounts(connId, db).then(counts => setRowCounts(prev => ({ ...prev, [ck]: counts }))).catch(() => {})
      return
    }
    try {
      const [ts, objs] = await Promise.all([
        listTables(connId, db),
        listObjects(connId, db).catch(() => [] as DbObject[]),
      ])
      const objects = Array.isArray(objs) ? objs : []
      setTablesCache(prev => ({
        ...prev,
        [ck]: {
          tables: ts.filter(t => t.type !== 'VIEW').map(t => t.name),
          views: Array.from(new Set([
            ...ts.filter(t => t.type === 'VIEW').map(t => t.name),
            ...objects.filter(o => o.kind === 'VIEW' || o.kind === 'MATERIALIZED VIEW').map(o => o.name),
          ])),
          objects,
        },
      }))
      getTableCounts(connId, db).then(counts => setRowCounts(prev => ({ ...prev, [ck]: counts }))).catch(() => {})
    } catch { setTablesCache(prev => ({ ...prev, [ck]: { tables: [], views: [], objects: [] } })) }
  }

  // 模式能力探测(dbx loadSchemas 同构): 列模式非空 → 库下渲染模式层级; 空/失败 → 平铺对象
  const [schemaCache, setSchemaCache] = useState<Record<string, string[]>>({})
  const [rowCounts, setRowCounts] = useState<Record<string, Record<string, number>>>({})
  const [connHealth, setConnHealth] = useState<Record<string, 'ok' | 'fail'>>({})
  const probeSchemas = (connId: string) => {
    listSchemas(connId)
      .then(ss => setSchemaCache(prev => ({ ...prev, [connId]: ss || [] })))
      .catch(() => setSchemaCache(prev => ({ ...prev, [connId]: [] })))
  }

  const toggle = (key: string) => {
    if (drill) {
      // 钻取模式: 默认展开, 点击切换进 collapsed 反向集合
      setCollapsed(prev => {
        const next = new Set(prev)
        if (next.has(key)) next.delete(key); else next.add(key)
        return next
      })
      return
    }
    setExpanded(prev => {
      const next = new Set(prev)
      if (next.has(key)) next.delete(key); else next.add(key)
      return next
    })
  }

  // 函数/存储过程/事件/触发器/序列: 单击在新窗口打开 DDL(轻量, 便于复制)
  const viewObjectDdl = async (node: TreeNode) => {
    if (!node.conn || !node.db || !node.table) return
    const kind = OBJ_LEVEL_TO_KIND[node.level]
    if (!kind) return
    try {
      const ddl = await getObjectDefinition(node.conn.id, node.db, node.table, kind)
      const win = window.open('', '_blank')
      if (win) {
        win.document.write(
          `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><title>${escapeHtml(node.table)} — DDL</title>` +
          `<style>body{margin:24px;background:#0f1115;color:#d4d4d4;font:13px/1.7 Consolas,Menlo,monospace;white-space:pre-wrap;word-break:break-all;}</style></head>` +
          `<body>${escapeHtml(ddl || '(无 DDL)')}</body></html>`,
        )
        win.document.close()
        notify(true, '已在新窗口打开 DDL')
      } else {
        await navigator.clipboard?.writeText(ddl)
        notify(true, '已复制 DDL 到剪贴板')
      }
    } catch (e: any) {
      notify(false, '获取 DDL 失败: ' + (e?.message || e))
    }
  }

  const onNodeClick = (node: TreeNode) => {
    setActiveKey(node.key)
    if (node.level === 'conn' && node.conn) {
      onSelectConn(node.conn)
      toggle(node.key)
      // 钻取模式: 默认加载由懒加载 effect 全量负责, 手动展开时再按需补齐缓存即可
      if (drill ? !dbCache[node.conn.id] : !expanded.has(node.key)) loadDbs(node.conn.id)
      return
    }
    if (node.level === 'connGroup') { toggleGroup(node.key); return }
    if (node.level === 'db' || node.level === 'schema' || node.level === 'group') { toggle(node.key); return }
    if ((node.level === 'table' || node.level === 'view') && node.conn && node.db && node.table) {
      onOpenTable(node.conn, node.db, node.table, node.level === 'view')
    }
    if (OBJ_LEVEL_TO_KIND[node.level] && node.level !== 'view' && node.conn && node.db && node.table) {
      viewObjectDdl(node)
    }
  }

  // dbx TreeItem 原版行类: group flex items-center gap-2 min-h-7 py-1 px-2 relative
  const renderRow = (node: TreeNode, depth: number, children: React.ReactNode) => {
    const selected = activeKey === node.key
    const canExpand = !node.leaf
    const isOpen = node.level === 'connGroup' ? !collapsedGroups.has(node.key) : (drill ? !collapsed.has(node.key) : expanded.has(node.key))
    return (
      <div
        className={`group flex cursor-default items-center gap-2 min-h-7 py-1 px-2 relative outline-none rounded-[0.25rem] hover:bg-accent${selected ? ' bg-black/[0.08]' : ''}`}
        style={{ paddingLeft: `${8 + depth * 16}px`, ['--ind' as any]: `${8 + depth * 16}px`, contain: 'layout style', ...(node.sys ? { opacity: 0.6 } : {}) }}
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
            className="flex h-5 w-5 shrink-0 items-center justify-center rounded-sm border-0 bg-transparent p-0 text-muted-foreground outline-none hover:text-foreground"
            onClick={e => { e.stopPropagation(); onNodeClick(node) }}
            aria-label={isOpen ? '收起' : '展开'}
          >
            <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" style={{ transform: isOpen ? 'rotate(90deg)' : 'none', transition: 'transform .12s' }}>
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
        ...(onOpenDash ? [{ label: '服务器仪表盘', icon: <ActionIcon kind="chart" />, onClick: () => onOpenDash(node.conn!) }] : []),
        { label: '新建查询', icon: <ActionIcon kind="query" />, onClick: () => onNewQuery(node.conn!, node.db || '') },
        { label: '测试连接', icon: <ActionIcon kind="test" />, onClick: () => quickTest(node.conn!) },
        { divider: true },
        { label: '移动到分组…', icon: <ActionIcon kind="transfer" />, onClick: () => {
            setMoveToGroupTarget(node.conn!)
            setNewGroupName(node.conn!.config.group || '')
          } },
        { label: '编辑连接', icon: <ActionIcon kind="edit" />, onClick: () => onEditConn(node.conn!) },
        { label: '删除连接', icon: <ActionIcon kind="delete" />, danger: true, onClick: () => remove(node.conn!) },
      ]
    }
    if (OBJ_LEVEL_TO_KIND[node.level] && node.level !== 'view' && node.conn && node.db && node.table) {
      const kindLabel = node.level === 'function' ? '函数' : node.level === 'procedure' ? '存储包' : node.level === 'event' ? '事件' : node.level === 'trigger' ? '触发器' : node.level === 'sequence' ? '序列' : '对象'
      return [
        { label: '查看 DDL', icon: <ActionIcon kind="doc" />, onClick: () => viewObjectDdl(node) },
        { divider: 'heavy' },
        { label: '新建查询', icon: <ActionIcon kind="query" />, onClick: () => onNewQuery(node.conn!, node.db!) },
        { divider: 'light' },
        { label: `复制${kindLabel}名`, icon: <ActionIcon kind="copy" />, onClick: () => { navigator.clipboard?.writeText(node.table!); notify(true, `已复制 ${node.table}`) } },
        { label: '复制对象路径', icon: <ActionIcon kind="copy" />, onClick: () => { navigator.clipboard?.writeText(`${node.db}.${node.table}`); notify(true, `已复制 ${node.db}.${node.table}`) } },
      ]
    }
    if (node.level === 'schema' && node.conn && node.db && node.schema) {
      return [
        { label: '新建查询', icon: <ActionIcon kind="query" />, onClick: () => onNewQuery(node.conn!, node.db!) },
        { label: '刷新列表', icon: <ActionIcon kind="refresh" />, onClick: () => { loadTables(node.conn!.id, node.db!) } },
        { divider: 'heavy' },
        ...(onSyncSchema ? [{ label: '跨库同步此模式', icon: <ActionIcon kind="transfer" />, onClick: () => onSyncSchema(node.conn!, node.db!, node.schema!) }] : []),
        { divider: 'heavy' },
        { label: '复制模式名', icon: <ActionIcon kind="copy" />, onClick: () => { navigator.clipboard?.writeText(node.schema!); notify(true, `已复制 ${node.schema}`) } },
      ]
    }
    if (node.level === 'db' && node.conn && node.db) {
      const sys = isSystemDb(node.db)
      return [
        { label: '新建查询', icon: <ActionIcon kind="query" />, onClick: () => onNewQuery(node.conn!, node.db!) },
        { label: '刷新列表', icon: <ActionIcon kind="refresh" />, onClick: () => { loadTables(node.conn!.id, node.db!) } },
        { divider: 'heavy' },
        { label: '新建表', icon: <ActionIcon kind="plus" />, onClick: () => onNewTable?.(node.conn!, node.db!) },
        { label: '导出表结构 (SQL)', icon: <ActionIcon kind="download" />, onClick: () => onExportSchema?.(node.conn!, node.db!) },
        { label: '跨库同步此库', icon: <ActionIcon kind="transfer" />, onClick: () => onSyncDb(node.conn!, node.db!) },
        ...(onOpenEr ? [{ label: '关系图 (ER)', icon: <ActionIcon kind="chart" />, onClick: () => onOpenEr(node.conn!, node.db!) }] : []),
        ...(onOpenOverview ? [{ label: '表概览', icon: <ActionIcon kind="chart" />, onClick: () => onOpenOverview(node.conn!, node.db!) }] : []),
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
        ...(isTable && onSyncTable ? [{ label: '跨库同步此表', icon: <ActionIcon kind="transfer" />, onClick: () => onSyncTable(node.conn!, node.db!, node.table!) }] : []),
        ...(isTable && onOpenEr ? [{ label: '关系图 (此表为中心)', icon: <ActionIcon kind="chart" />, onClick: () => onOpenEr(node.conn!, node.db!, node.table!) }] : []),
      ]
      // ── SQL 与结构 ──
      const sqlItems: ContextMenuItem[] = [
        { label: '新建查询 (FROM)', icon: <ActionIcon kind="query" />, onClick: () => onNewQuery(node.conn!, node.db!) },
        ...(isTable ? [
          { label: '生成 SQL', icon: <ActionIcon kind="query" />, children: [
            { label: '生成 SELECT', icon: <ActionIcon kind="query" />, onClick: () => onNewQueryWithSQL(node.conn!, node.db!, `SELECT * FROM ${node.table} LIMIT 100`) },
          { label: '生成 INSERT', icon: <ActionIcon kind="query" />, onClick: () => {
              // 列清单异步拉取 describe, 生成带占位值的 INSERT 模板
              describeTable(node.conn!.id, node.db!, node.table!).then(d => {
                const cols = d.columns.map(c => c.name)
                const vals = d.columns.map(c => (c.key === 'PRI' ? '/* 自增 */' : '?'))
                const sqlText = `INSERT INTO ${node.table} (${cols.join(', ')}) VALUES (${vals.join(', ')})`
                onNewQueryWithSQL(node.conn!, node.db!, sqlText)
              }).catch((e: any) => notify(false, '生成失败: ' + (e.message || e)))
            } },
          { label: '生成 UPDATE', icon: <ActionIcon kind="query" />, onClick: () => {
              describeTable(node.conn!.id, node.db!, node.table!).then(d => {
                const pk = d.columns.filter(c => c.key === 'PRI').map(c => c.name)
                const cols = d.columns.map(c => c.name)
                const where = pk.length ? pk.map(pk => `${pk} = ?`).join(' AND ') : '/* 无主键, 请补 WHERE */'
                onNewQueryWithSQL(node.conn!, node.db!, `UPDATE ${node.table} SET\n  ${cols.map(c => `${c} = ?`).join(',\n  ')}\nWHERE ${where}`)
              }).catch((e: any) => notify(false, '生成失败: ' + (e.message || e)))
            } },
          { label: '生成 DELETE', icon: <ActionIcon kind="query" />, onClick: () => {
              describeTable(node.conn!.id, node.db!, node.table!).then(d => {
                const pk = d.columns.filter(c => c.key === 'PRI').map(c => c.name)
                const where = pk.length ? pk.map(pk => `${pk} = ?`).join(' AND ') : '/* 无主键, 请补 WHERE */'
                onNewQueryWithSQL(node.conn!, node.db!, `DELETE FROM ${node.table} WHERE ${where}`)
              }).catch((e: any) => notify(false, '生成失败: ' + (e.message || e)))
            } },
          ],
          title: '生成常用 SQL 模板到查询窗口' },
          { label: '在新标签打开数据', icon: <ActionIcon kind="chart" />, onClick: () => onOpenTable(node.conn!, node.db!, node.table!) },
          { label: '执行计划 (EXPLAIN)', icon: <ActionIcon kind="search" />, onClick: () => onOpenExplain(node.conn!, node.db!, node.table!) },
        ] : []),
        { label: '查看结构 / DDL', icon: <ActionIcon kind="doc" />, onClick: () => onOpenDoc(node.conn!, node.db!, node.table!) },
      ]
      // ── 复制与导出 ──
      const copyItems: ContextMenuItem[] = [
        { label: '复制表名', icon: <ActionIcon kind="copy" />, onClick: () => { navigator.clipboard?.writeText(node.table!); notify(true, `已复制 ${node.table}`) } },
        { label: '复制表路径', icon: <ActionIcon kind="copy" />, onClick: () => { navigator.clipboard?.writeText(`${node.db}.${node.table}`); notify(true, `已复制 ${node.db}.${node.table}`) } },
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
      const bt = (name: string) => '`' + name.replace(/`/g, '``') + '`'
      const qt = (name: string) => '"' + name.replace(/"/g, '""') + '"'
      const quoteTable = () => {
        const eng = node.conn!.engine
        if (eng === 'mysql' || eng === 'mariadb' || eng === 'goldendb') return bt(node.db!) + '.' + bt(node.table!)
        return node.table.includes('.') ? node.table.split('.').map(qt).join('.') : qt(node.table)
      }
      const runDanger = async (label: string, sqlText: string) => {
        if (!confirm(`确认${label}表 ${node.table}? 该操作不可撤销。`)) return
        try {
          const r = await runQueryRaw(node.conn!.id, sqlText)
          if (r.data.code === 'write_locked') { notify(false, '写操作被拦截: 请先解锁写模式'); return }
          notify(true, `${label}完成: ${node.table}${r.data.affected != null ? ` (影响 ${r.data.affected} 行)` : ''}`)
          loadTables(node.conn!.id, node.db!)
        } catch (e: any) {
          notify(false, `${label}失败: ${e.message || e}`)
        }
      }
      const maintainItems: ContextMenuItem[] = [
        { label: pinned ? '取消置顶' : '置顶表', icon: <ActionIcon kind="pin" />, onClick: () => togglePin(node.conn!.id, node.db!, node.table!) },
        { label: '刷新行数统计', icon: <ActionIcon kind="refresh" />, onClick: () => notify(true, `${node.table}: 统计已刷新`) },
        ...(isTable ? [
          { divider: 'heavy' as const },
          { label: '清空表 (TRUNCATE)', icon: <ActionIcon kind="refresh" />, onClick: () => runDanger('清空', `TRUNCATE TABLE ${quoteTable()}`) },
          { label: '删除表 (DROP)', icon: <ActionIcon kind="delete" />, danger: true, onClick: () => runDanger('删除', `DROP TABLE ${quoteTable()}`) },
        ] : []),
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
    setDbCache({}); setTablesCache({}); setSchemaCache({}); setExpanded(new Set())
    notify(true, '已刷新, 重新展开连接加载')
  }

  // 同步完成等外部动作触发的静默刷新(不走手动 notify)
  useEffect(() => {
    const h = () => { setDbCache({}); setTablesCache({}); setSchemaCache({}); setExpanded(new Set()) }
    window.addEventListener('dbmanager:tree-refresh', h)
    return () => window.removeEventListener('dbmanager:tree-refresh', h)
  }, [])

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

  // 连接分组(复用 config.group 字段)
  const connGroups = useMemo(() => {
    const ungrouped: ConnectionInfo[] = []
    const map: Record<string, ConnectionInfo[]> = {}
    for (const c of visibleConns) {
      const g = (c.config.group || '').trim()
      if (g) (map[g] = map[g] || []).push(c)
      else ungrouped.push(c)
    }
    const byGroup = Object.keys(map).sort().map(name => ({ name, conns: map[name] }))
    return { ungrouped, byGroup }
  }, [visibleConns])

  const [collapsedGroups, setCollapsedGroups] = useState<Set<string>>(new Set())
  const toggleGroup = (name: string) => setCollapsedGroups(prev => {
    const next = new Set(prev)
    if (next.has(name)) next.delete(name); else next.add(name)
    return next
  })

  const moveToGroup = async (c: ConnectionInfo, group: string) => {
    try {
      await updateConnection(c.id, c.name, { ...c.config, group: group.trim() }, '')
      notify(true, group.trim() ? `已移入分组「${group.trim()}」` : '已移出分组')
      onConnsChange(await listConnections())
      setMoveToGroupTarget(null)
    } catch (e: any) {
      notify(false, '移动失败: ' + e.message)
    }
  }

  // 连接唯一分组名(用于 drawer 面板列表)
  const uniqueGroups = useMemo(() => {
    const s = new Set<string>()
    for (const c of conns) { const g = c.config.group?.trim(); if (g) s.add(g) }
    return [...s].sort()
  }, [conns])

  // 组装可见行
  const rows: TreeNode[] = []

  // 组装单个连接的可见行(钻取模式=只返回"匹配对象+直系上级"链, 无匹配整段隐藏, 对齐 GoNavi filterV2ExplorerTreeByKind)
  const buildConnRows = (c: ConnectionInfo, groupName: string): TreeNode[] => {
    const ckey = `conn:${c.id}`
    const isConnOpen = drill || expanded.has(ckey)
    const dbs = [...(dbCache[c.id] || [])].sort((a, b) => Number(isSysDbName(a)) - Number(isSysDbName(b)))
    const out: TreeNode[] = []

    if (drill) {
      // 先筛出有匹配对象的库(模式层级/平铺二选一判定), 无则可整段跳过本连接
      const matchedDbs: string[] = []
      for (const db of dbs) {
        const schemas = (schemaCache[c.id] || []).filter(sc => !f || sc.toLowerCase().includes(f))
        const objs = tablesCache[`${c.id}|${db}`]
        if (schemas.length > 0) {
          if (schemas.some(sc => itemsForCategory(objs, category, sc + '.', f).length > 0)) matchedDbs.push(db)
        } else if (objs && itemsForCategory(objs, category, '', f).length > 0) {
          matchedDbs.push(db)
        }
      }
      if (matchedDbs.length === 0) return out
      out.push({ key: ckey, level: 'conn', label: c.name, conn: c, group: groupName })
      // 用户手动收起该连接 → 只保留连接行, 不展开子节点
      if (collapsed.has(ckey)) return out
      for (const db of matchedDbs) {
        const sys = isSysDbName(db)
        const dkey = `${ckey}|db:${db}`
        out.push({ key: dkey, level: 'db', label: db, conn: c, db, sys })
        // 用户手动收起该库 → 跳过该库的模式/对象
        if (collapsed.has(dkey)) continue
        const schemas = (schemaCache[c.id] || []).filter(sc => !f || sc.toLowerCase().includes(f))
        const objs = tablesCache[`${c.id}|${db}`]
        if (schemas.length > 0) {
          // 三级命名: 库 → 模式 → 对象(只保留有匹配对象的模式)
          for (const sc of schemas) {
            const skey = `${dkey}|schema:${sc}`
            const pfx = sc + '.'
            const items = itemsForCategory(objs, category, pfx, f)
            if (items.length === 0) continue
            out.push({ key: skey, level: 'schema', label: sc, conn: c, db, schema: sc })
            // 用户手动收起该模式 → 跳过该模式下的对象
            if (collapsed.has(skey)) continue
            for (const it of items) {
              const shortName = it.name.startsWith(pfx) ? it.name.slice(pfx.length) : it.name
              out.push({ key: `${skey}|${it.name}`, level: it.level, label: shortName, conn: c, db, table: it.name, leaf: true, sys: isSysObjName(shortName) })
            }
          }
          continue
        }
        if (!objs) continue
        // 库下平铺该类对象(页签选中即只显示该类对象)
        for (const it of itemsForCategory(objs, category, '', f)) {
          out.push({ key: `${dkey}|${it.name}`, level: it.level, label: it.name, conn: c, db, table: it.name, leaf: true, sys: isSysObjName(it.name) })
        }
      }
      return out
    }

    out.push({ key: ckey, level: 'conn', label: c.name, conn: c, group: groupName })
    if (!isConnOpen) return out
    for (const db of dbs) {
      const sys = isSysDbName(db)
      const dkey = `${ckey}|db:${db}`
      out.push({ key: dkey, level: 'db', label: db, conn: c, db, sys })
      if (expanded.has(dkey)) {
        const schemas = (schemaCache[c.id] || []).filter(sc => !f || sc.toLowerCase().includes(f))
        const ck2 = `${c.id}|${db}`
        const objs = tablesCache[ck2]
        if (schemas.length > 0) {
          // 三级命名: 库 → 模式 → 对象(按第二层类别页签过滤)
          for (const sc of schemas) {
            const skey = `${dkey}|schema:${sc}`
            out.push({ key: skey, level: 'schema', label: sc, conn: c, db, schema: sc })
            if (!expanded.has(skey) || !objs) continue
            const pfx = sc + '.'
            for (const it of itemsForCategory(objs, category, pfx, f)) {
              const shortName = it.name.startsWith(pfx) ? it.name.slice(pfx.length) : it.name
              out.push({ key: `${skey}|${it.name}`, level: it.level, label: shortName, conn: c, db, table: it.name, leaf: true, sys: isSysObjName(shortName) })
            }
          }
          continue
        }
        if (!objs) continue
        // 库下按第二层类别页签过滤后平铺(GoNavi: 页签选中即只显示该类对象)
        for (const it of itemsForCategory(objs, category, '', f)) {
          out.push({ key: `${dkey}|${it.name}`, level: it.level, label: it.name, conn: c, db, table: it.name, leaf: true, sys: isSysObjName(it.name) })
        }
      }
    }
    return out
  }

  for (const c of connGroups.ungrouped) rows.push(...buildConnRows(c, ''))
  for (const g of connGroups.byGroup) {
    const gkey = `cgroup:${g.name}`
    if (drill) {
      // 钻取模式: 分组同样可手动收起(默认展开)
      const isOpen = !collapsedGroups.has(gkey)
      const childRows: TreeNode[] = []
      for (const c of g.conns) childRows.push(...buildConnRows(c, g.name))
      if (!isOpen) {
        // 分组被收起: 若无匹配连接则整组隐藏, 否则仅显示分组行
        if (childRows.length === 0) continue
        rows.push({ key: gkey, level: 'connGroup', label: g.name, group: g.name, count: childRows.length })
        continue
      }
      if (childRows.length === 0) continue
      rows.push({ key: gkey, level: 'connGroup', label: g.name, group: g.name, count: childRows.length })
      rows.push(...childRows)
      continue
    }
    const isOpen = !collapsedGroups.has(gkey)
    rows.push({ key: gkey, level: 'connGroup', label: g.name, group: g.name, count: g.conns.length })
    if (isOpen) for (const c of g.conns) rows.push(...buildConnRows(c, g.name))
  }

  // 懒加载触发: 常规=只加载已展开节点; 钻取模式(=类别页签非"全部")=全量加载所有连接/库/模式
  useEffect(() => {
    if (drill) {
      for (const c of visibleConns) {
        if (!dbCache[c.id]) loadDbs(c.id)
        if (!schemaCache[c.id]) probeSchemas(c.id)
      }
      for (const conn of visibleConns) {
        for (const db of (dbCache[conn.id] || [])) {
          if (!tablesCache[`${conn.id}|${db}`]) loadTables(conn.id, db)
        }
      }
      return
    }
    for (const key of expanded) {
      const [connPart, dbPart] = key.split('|')
      if (key.startsWith('conn:') && !dbPart) {
        const connId = key.slice(5)
        if (connId && !dbCache[connId]) loadDbs(connId)
        if (connId && !schemaCache[connId]) probeSchemas(connId)
      }
      if (dbPart && dbPart.startsWith('db:')) {
        const connId = connPart.slice(5)
        const db = dbPart.slice(3)
        if (connId && db && !tablesCache[`${connId}|${db}`]) loadTables(connId, db)
      }
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [expanded, drill, visibleConns, dbCache, schemaCache, tablesCache])

  useEffect(() => {
    if (!menu) return
    const close = () => setMenu(null)
    window.addEventListener('click', close)
    return () => window.removeEventListener('click', close)
  }, [menu])

  return (
    <div className="db-tree" style={{ fontSize: '14px' }}>
      <div className="db-tree-header">
        <div className="db-tree-row-1">
          <div className="db-tree-search-row">
            <span className="db-tree-search-ico"><ActionIcon kind="search" size={12} /></span>
            <input
              className="db-tree-search"
              placeholder="搜索连接 / 库 / 表..."
              value={filter}
              onChange={e => setFilter(e.target.value)}
            />
          </div>
          <div className="db-tree-header-actions">
            <button className="db-tree-tool" onClick={() => { refreshAll(); onRefresh() }} title="刷新全部缓存"><ActionIcon kind="refresh" /></button>
            <button className="db-tree-tool" onClick={onExportConns} title="导出连接配置 (JSON)"><ActionIcon kind="upload" /></button>
            <label className="db-tree-tool" title="导入连接配置 (JSON)">
              <ActionIcon kind="download" />
              <input type="file" accept=".json" style={{ display: 'none' }} onChange={e => {
                const f = e.target.files?.[0]
                if (f) onImportConns(f)
                e.target.value = ''
              }} />
            </label>
            <button className="db-tree-add" onClick={onNewConn} title="新建连接"><ActionIcon kind="plus" /></button>
            {onToggleSide && (
              <button className="db-tree-tool" onClick={onToggleSide} title="收纳 / 展开侧栏"><ActionIcon kind="panel" /></button>
            )}
          </div>
        </div>
        <div className="db-tree-catbar" role="tablist">
          {OBJ_CATEGORY_ORDER.map(cat => (
            <button
              key={cat}
              role="tab"
              aria-selected={category === cat}
              className={category === cat ? 'cat-on' : ''}
              onClick={() => setCategory(cat)}
            >{OBJ_CATEGORY_LABEL[cat]}</button>
          ))}
        </div>
      </div>
      <div className="db-tree-body">
        {rows.length === 0 && <div className="db-empty-sm">{conns.length === 0 ? '暂无连接, 点右上 + 新建' : '无匹配对象'}</div>}
        {rows.map(node => {
          const depth = node.key.split('|').length - 1
          const isConn = node.level === 'conn'
          const isDb = node.level === 'db'
          const isConnGroup = node.level === 'connGroup'
          const selected = activeKey === node.key

          // group 节点(表/视图分组) — 已移除固定类别,此分支不再触发
          if (node.level === 'group') return null

          if (isConnGroup) {
            return renderRow(node, depth, (
              <>
                <NodeIcon level={node.level} />
                <span className="truncate font-medium">{node.label}</span>
                {node.count !== undefined && (
                  <span className="ml-0.5 inline-flex h-4 items-center rounded bg-muted px-1.5 text-[10px] text-muted-foreground">{node.count}</span>
                )}
              </>
            ))
          }

          return renderRow(node, depth, (
            <>
              {isConn && node.conn ? <EngineIcon engine={node.conn.engine} size={14} /> : <NodeIcon level={node.level} />}
              {(node.level === 'table' || node.level === 'view') && node.conn && node.db && node.table && (() => {
                const cnt = rowCounts[`${node.conn.id}|${node.db}`]?.[node.table]
                return cnt != null && cnt >= 0 ? (
                  <span className="db-tree-rcount" title={`约 ${cnt} 行`}>{fmtCount(cnt)}</span>
                ) : null
              })()}
              <span className="truncate">{node.label}</span>
              {isConn && node.conn && (() => {
                const h = connHealth[node.conn.id]
                const color = h === 'ok' ? '#30d158' : h === 'fail' ? '#ff453a' : 'var(--text-dim)'
                return <span title={h === 'ok' ? '连接正常' : h === 'fail' ? '连接失败(可能密码已变更或服务不可达)' : '未测试'} style={{ width: 7, height: 7, borderRadius: '50%', background: color, flexShrink: 0, marginLeft: 4 }} />
              })()}
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
      {moveToGroupTarget && (
        <div className="db-group-drawer">
          <div className="db-group-drawer-head">
            <span className="db-group-drawer-title">移动到分组</span>
            <button className="db-group-drawer-close" onClick={() => setMoveToGroupTarget(null)}><ActionIcon kind="close" /></button>
          </div>
          <div className="db-group-drawer-body">
            <div className="db-group-drawer-row db-group-drawer-new">
              <input
                className="db-group-drawer-input"
                placeholder="新建分组名…"
                value={newGroupName}
                onChange={e => setNewGroupName(e.target.value)}
                onKeyDown={e => { if (e.key === 'Enter' && newGroupName.trim()) { moveToGroup(moveToGroupTarget!, newGroupName.trim()) } }}
              />
              <button
                className="db-group-drawer-go"
                disabled={!newGroupName.trim()}
                onClick={() => moveToGroup(moveToGroupTarget!, newGroupName.trim())}
              >移入</button>
            </div>
            <div className="db-group-drawer-sep" />
            {uniqueGroups.map(g => (
              <button
                key={g}
                className={`db-group-drawer-row${moveToGroupTarget.config.group?.trim() === g ? ' active' : ''}`}
                onClick={() => moveToGroup(moveToGroupTarget!, g)}
              >
                <span className="db-group-drawer-gname">{g}</span>
                {moveToGroupTarget.config.group?.trim() === g && <span className="db-group-drawer-check">✓</span>}
              </button>
            ))}
            {moveToGroupTarget.config.group?.trim() && (
              <button className="db-group-drawer-row danger" onClick={() => moveToGroup(moveToGroupTarget!, '')}>
                <span className="db-group-drawer-gname">移出分组</span>
              </button>
            )}
          </div>
        </div>
      )}
    </div>
  )
}
