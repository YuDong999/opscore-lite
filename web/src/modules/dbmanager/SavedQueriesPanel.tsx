// 已保存查询 = "服务端 SQL 仓库"(P1-10 目录化): 目录树 + 搜索 + 打开 + 改名/删除目录。
//
// 目录不是独立实体: 树由每条查询的 folder 前缀推导(见后端 normalizeSQLFolder 的注释),
// 所以"改目录名"是批量改查询、"删目录"会连它下面的查询一起删 —— 界面上必须说清这一点。
//
// 多端共享是这个方案的真正价值: 服务端存的, 换台机器登录也在。

import { useEffect, useMemo, useState } from 'react'
import {
  listSavedQueries, deleteSavedQuery, renameQueryFolder, deleteQueryFolder,
  type SavedQuery, type ConnectionInfo,
} from './api'
import { useToast } from '../../components/Toast'
import { useConfirm } from '../../lib/hooks/useConfirm'

type Node = {
  path: string          // 目录全路径(空=根)
  name: string
  queries: SavedQuery[] // 直属本目录的查询
  children: Node[]
}

// 由 folder 前缀推导目录树。只建"有查询的目录"(空目录不存在 —— 因为没有独立实体)。
function buildTree(qs: SavedQuery[]): Node {
  const root: Node = { path: '', name: '', queries: [], children: [] }
  const dirs = new Map<string, Node>([['', root]])
  const ensure = (path: string): Node => {
    const hit = dirs.get(path)
    if (hit) return hit
    const idx = path.lastIndexOf('/')
    const parentPath = idx < 0 ? '' : path.slice(0, idx)
    const parent = ensure(parentPath)
    const node: Node = { path, name: idx < 0 ? path : path.slice(idx + 1), queries: [], children: [] }
    parent.children.push(node)
    dirs.set(path, node)
    return node
  }
  for (const q of qs) {
    const f = (q.folder || '').trim().replace(/^\/+|\/+$/g, '')
    if (f) ensure(f)
    ;(f ? ensure(f) : root).queries.push(q)
  }
  const sort = (n: Node) => {
    n.children.sort((a, b) => a.name.localeCompare(b.name, 'zh'))
    n.queries.sort((a, b) => a.name.localeCompare(b.name, 'zh'))
    n.children.forEach(sort)
  }
  sort(root)
  return root
}

export default function SavedQueriesPanel({
  conns,
  activeConn,
  onOpenQuery,
}: {
  conns: ConnectionInfo[]
  activeConn: ConnectionInfo | null
  onOpenQuery?: (sql: string, name: string) => void
}) {
  const toast = useToast()
  const { confirm: askConfirm, confirmEl } = useConfirm()
  const [queries, setQueries] = useState<SavedQuery[]>([])
  const [loading, setLoading] = useState(false)
  const [expanded, setExpanded] = useState<string | null>(null)      // 展开的查询 id
  const [openDirs, setOpenDirs] = useState<Set<string>>(new Set())   // 展开的目录 path
  const [filter, setFilter] = useState('')

  const reload = async () => {
    setLoading(true)
    try {
      setQueries(await listSavedQueries())
    } catch {
      setQueries([])
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => { reload() }, [])

  const remove = async (id: string) => {
    try {
      await deleteSavedQuery(id)
      setQueries(qs => qs.filter(q => q.id !== id))
    } catch {
      toast.error('删除失败')
    }
  }

  const connName = (id?: string) => {
    if (!id) return null
    const c = conns.find(x => x.id === id)
    return c ? c.name : id.slice(0, 8)
  }

  // 搜索直接拍平成列表 —— 树 + 过滤同时开着最容易让人以为"东西没了"
  const filtered = useMemo(() => {
    const q = filter.trim().toLowerCase()
    if (!q) return null
    return queries.filter(x =>
      x.name.toLowerCase().includes(q) ||
      (x.folder || '').toLowerCase().includes(q) ||
      x.sql.toLowerCase().includes(q))
  }, [queries, filter])

  const tree = useMemo(() => buildTree(queries), [queries])

  const toggleDir = (path: string) => setOpenDirs(prev => {
    const next = new Set(prev)
    if (next.has(path)) next.delete(path); else next.add(path)
    return next
  })

  const doRenameDir = async (path: string) => {
    const input = window.prompt(`把目录「${path}」改名/移动为:`, path)
    if (input === null) return
    const to = input.trim()
    if (to === path) return
    try {
      const r = await renameQueryFolder(path, to)
      if (!r.ok) { toast.error(r.error || '改名失败'); return }
      toast.success(`已移动 ${r.affected ?? 0} 条查询`)
      await reload()
    } catch (e: any) {
      toast.error('改名失败: ' + (e.message || e))
    }
  }

  const doDeleteDir = async (path: string) => {
    // 先问后端"会删几条", 让确认弹窗给出有信息量的数字(与写操作同一口径)
    const probe = await deleteQueryFolder(path, false)
    if (!(await askConfirm(`删除目录「${path}」?`, {
      desc: probe.error || '目录下的查询会一起删除, 不可恢复。',
      okText: '删除', danger: true,
    }))) return
    try {
      const r = await deleteQueryFolder(path, true)
      if (!r.ok) { toast.error(r.error || '删除失败'); return }
      toast.success(`已删除 ${r.affected ?? 0} 条查询`)
      await reload()
    } catch (e: any) {
      toast.error('删除失败: ' + (e.message || e))
    }
  }

  const renderQuery = (q: SavedQuery, depth: number) => {
    const isOpen = expanded === q.id
    return (
      <div key={q.id} className="db-query-item" style={{ marginLeft: depth * 12 }}>
        <div className="db-query-item-head" onClick={() => setExpanded(isOpen ? null : q.id)}>
          <span className="db-query-name">{q.name}</span>
          <span className="dim db-query-meta">
            {connName(q.connId) && <>{connName(q.connId)} · </>}
            {q.engine || '—'}
          </span>
          {onOpenQuery && (
            <button className="btn-glass-soft btn-glass-soft-sm"
              title="在新查询标签里打开(不改动保存的这条)"
              onClick={e => { e.stopPropagation(); onOpenQuery(q.sql, q.name) }}>打开</button>
          )}
          <button className="btn-glass-soft btn-glass-soft-sm"
            onClick={e => { e.stopPropagation(); remove(q.id) }}>删除</button>
        </div>
        {isOpen && <pre className="code-block db-query-sql">{q.sql}</pre>}
      </div>
    )
  }

  const renderDir = (n: Node, depth: number): React.ReactNode => {
    const open = openDirs.has(n.path)
    return (
      <div key={'d:' + n.path}>
        <div className="db-query-item-head db-query-dir" style={{ marginLeft: depth * 12 }}
          onClick={() => toggleDir(n.path)}>
          <span className="db-query-caret">{open ? '▾' : '▸'}</span>
          <span className="db-query-name">{n.name}</span>
          <span className="dim db-query-meta">{countAll(n)} 条</span>
          <button className="btn-glass-soft btn-glass-soft-sm" title="改名/移动这个目录(含子目录)"
            onClick={e => { e.stopPropagation(); doRenameDir(n.path) }}>改名</button>
          <button className="btn-glass-soft btn-glass-soft-sm" title="删除目录及其下全部查询"
            onClick={e => { e.stopPropagation(); doDeleteDir(n.path) }}>删除目录</button>
        </div>
        {open && (
          <>
            {n.children.map(c => renderDir(c, depth + 1))}
            {n.queries.map(q => renderQuery(q, depth + 1))}
          </>
        )}
      </div>
    )
  }

  return (
    <div className="db-queries">
      <div className="db-queries-toolbar">
        <span className="dim">当前连接: {activeConn ? `${activeConn.name} (${activeConn.engine})` : '未选择'}</span>
        <span className="dim">共 {queries.length} 条</span>
        <input className="input log-input" style={{ maxWidth: 180 }} placeholder="搜索名称/目录/SQL"
          value={filter} onChange={e => setFilter(e.target.value)} aria-label="搜索已保存查询" />
        <button className="btn-glass-soft btn-glass-soft-sm" onClick={reload} disabled={loading}>
          {loading ? '刷新中...' : '刷新'}
        </button>
      </div>

      {filtered ? (
        filtered.length === 0
          ? <div className="db-empty">没有匹配的查询</div>
          : <div className="db-queries-list">
              {filtered.map(q => (
                <div key={q.id} className="db-query-item">
                  <div className="db-query-item-head" onClick={() => setExpanded(expanded === q.id ? null : q.id)}>
                    <span className="db-query-name">{q.name}</span>
                    <span className="dim db-query-meta">{q.folder || '根目录'}</span>
                    {onOpenQuery && (
                      <button className="btn-glass-soft btn-glass-soft-sm"
                        onClick={e => { e.stopPropagation(); onOpenQuery(q.sql, q.name) }}>打开</button>
                    )}
                    <button className="btn-glass-soft btn-glass-soft-sm"
                      onClick={e => { e.stopPropagation(); remove(q.id) }}>删除</button>
                  </div>
                  {expanded === q.id && <pre className="code-block db-query-sql">{q.sql}</pre>}
                </div>
              ))}
            </div>
      ) : queries.length === 0 ? (
        <div className="db-empty">{loading ? '加载中...' : '暂无已保存查询 —— 在查询编辑器里点「保存」, 可顺便填目录(如 运维/K8s)'}</div>
      ) : (
        <div className="db-queries-list">
          {tree.children.map(n => renderDir(n, 0))}
          {tree.queries.length > 0 && (
            <div>
              <div className="db-query-item-head db-query-dir" onClick={() => toggleDir('')}>
                <span className="db-query-caret">{openDirs.has('') ? '▾' : '▸'}</span>
                <span className="db-query-name">根目录</span>
                <span className="dim db-query-meta">{tree.queries.length} 条</span>
              </div>
              {openDirs.has('') && tree.queries.map(q => renderQuery(q, 1))}
            </div>
          )}
        </div>
      )}
      {confirmEl}
    </div>
  )
}

// 含子目录的总条数(目录行上显示的数字要是"这一整棵树有多少条", 否则改名/删除的影响看不出来)
function countAll(n: Node): number {
  return n.queries.length + n.children.reduce((a, c) => a + countAll(c), 0)
}
