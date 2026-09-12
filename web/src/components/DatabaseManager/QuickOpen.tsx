// Ctrl+K 快速打开(借鉴 dbx quick-open): 逐级导航 连接→库→表, 输入过滤, 键盘操作。
// Enter: 连接/库=进入下一级, 表=打开数据浏览。Esc 关闭。

import { useEffect, useMemo, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { type ConnectionInfo, listDatabases, listTables } from './api'
import { EngineIcon, NodeIcon } from './DbIcons'

type Item = { kind: 'conn' | 'db' | 'table' | 'view'; conn: ConnectionInfo; db?: string; table?: string; label: string }

export default function QuickOpen({
  conns, open, onClose, onOpenTable, onNewQuery,
}: {
  conns: ConnectionInfo[]
  open: boolean
  onClose: () => void
  onOpenTable: (conn: ConnectionInfo, db: string, table: string, isView?: boolean) => void
  onNewQuery: (conn: ConnectionInfo, db: string) => void
}) {
  const [q, setQ] = useState('')
  const [path, setPath] = useState<{ conn?: ConnectionInfo; db?: string }>({})
  const [dbs, setDbs] = useState<string[]>([])
  const [objs, setObjs] = useState<Array<{ kind: 'table' | 'view'; label: string }>>([])
  const [loading, setLoading] = useState(false)
  const [sel, setSel] = useState(0)
  const inputRef = useRef<HTMLInputElement>(null)
  const listRef = useRef<HTMLDivElement>(null)

  const f = q.trim().toLowerCase()

  const items = useMemo<Item[]>(() => {
    if (!path.conn) {
      return conns.filter(c => !f || c.name.toLowerCase().includes(f))
        .map(c => ({ kind: 'conn' as const, conn: c, label: c.name }))
    }
    if (!path.db) {
      return dbs.filter(d => !f || d.toLowerCase().includes(f))
        .map(d => ({ kind: 'db' as const, conn: path.conn!, db: d, label: d }))
    }
    return objs.filter(o => !f || o.label.toLowerCase().includes(f))
      .map(o => ({ kind: o.kind, conn: path.conn!, db: path.db!, table: o.label, label: o.label }))
  }, [path, dbs, objs, f, conns])

  useEffect(() => { if (items.length) setSel(0) }, [items.length])

  // 只在层级变化时请求; q 过滤纯本地
  useEffect(() => {
    if (!open || !path.conn || path.db) return
    let alive = true
    setLoading(true)
    listDatabases(path.conn.id)
      .then(dbs => { if (alive) setDbs(dbs || []) })
      .catch(() => { if (alive) setDbs([]) })
      .finally(() => { if (alive) setLoading(false) })
    return () => { alive = false }
  }, [open, path.conn])

  useEffect(() => {
    if (!open || !path.conn || !path.db) return
    let alive = true
    setLoading(true)
    listTables(path.conn.id, path.db)
      .then(ts => {
        if (!alive) return
        setObjs([
          ...ts.filter(t => t.type !== 'VIEW').map(t => ({ kind: 'table' as const, label: t.name })),
          ...ts.filter(t => t.type === 'VIEW').map(t => ({ kind: 'view' as const, label: t.name })),
        ])
      })
      .catch(() => { if (alive) setObjs([]) })
      .finally(() => { if (alive) setLoading(false) })
    return () => { alive = false }
  }, [open, path.conn, path.db])

  const choose = (it: Item) => {
    if (it.kind === 'conn') { setPath({ conn: it.conn }); setQ(''); return }
    if (it.kind === 'db') { setPath(p => ({ ...p, db: it.db })); setQ(''); return }
    if (it.db && it.table) onOpenTable(it.conn, it.db, it.table, it.kind === 'view')
    onClose()
  }

  const onKey = (e: React.KeyboardEvent) => {
    if (e.key === 'ArrowDown') { e.preventDefault(); setSel(s => Math.min(s + 1, items.length - 1)) }
    else if (e.key === 'ArrowUp') { e.preventDefault(); setSel(s => Math.max(s - 1, 0)) }
    else if (e.key === 'Enter') {
      e.preventDefault()
      const it = items[sel]
      if (it) choose(it)
      else if (path.conn && path.db && q.trim()) onNewQuery(path.conn, path.db) // 无匹配表: 直接新建查询
    } else if (e.key === 'Escape') { onClose() }
  }

  useEffect(() => {
    listRef.current?.querySelector('.qo-item.active')?.scrollIntoView({ block: 'nearest' })
  }, [sel])

  if (!open) return null

  const crumbs = [path.conn?.name, path.db].filter(Boolean).join(' / ')

  return createPortal(
    <div className="qo-overlay" onClick={onClose}>
      <div className="qo-panel" onClick={e => e.stopPropagation()}>
        <input
          ref={inputRef}
          className="qo-input"
          placeholder={crumbs ? `${crumbs} — 搜索... (Enter 无匹配则新建查询)` : '搜索连接 / 库 / 表... (↑↓ 导航, Enter 打开)'}
          value={q}
          onChange={e => { setQ(e.target.value); setSel(0) }}
          onKeyDown={onKey}
        />
        <div className="qo-list" ref={listRef}>
          {loading && <div className="qo-empty">加载中...</div>}
          {!loading && items.length === 0 && (
            <div className="qo-empty">{path.conn && path.db ? '无匹配 — 按 Enter 新建查询' : '无匹配'}</div>
          )}
          {items.map((it, i) => (
            <div
              key={`${it.kind}:${it.conn.id}:${it.db || ''}:${it.table || it.label}`}
              className={`qo-item${i === sel ? ' active' : ''}`}
              onMouseEnter={() => setSel(i)}
              onClick={() => choose(it)}
            >
              {it.kind === 'conn' ? <EngineIcon engine={it.conn.engine} /> : <NodeIcon level={it.kind === 'view' ? 'view' : it.kind === 'db' ? 'db' : 'table'} />}
              <span className="qo-label">{it.label}</span>
              {it.kind === 'conn' && <span className="qo-hint">{it.conn.engine}</span>}
            </div>
          ))}
        </div>
      </div>
    </div>,
    document.body,
  )
}
