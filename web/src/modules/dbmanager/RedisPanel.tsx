// Redis 管理面板: 键浏览(SCAN) + 值预览 + 键级写操作(SET/EXPIRE/DEL/RENAME)。
//
// 形态与 MqPanel 同一路子: 列表是驱动已有的只读动词(SCAN/TYPE/TTL), 面板不自己拼协议;
// 写操作只发**意图**, 命令由后端类型化发出 —— 值与键名里可能有空格/引号/换行/二进制,
// 前端拼文本命令就得自己转义, 少转一个引号就是一条注入。
//
// 写操作过与查询编辑器同一道链: 只读锁拦下时把解锁提示交回上层(onWriteLocked)。
// 面板不提供 FLUSHDB/FLUSHALL/CONFIG —— 本产品不做没有回滚可言的全库清空。

import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { redisCapabilities, redisWrite, runQueryRaw, type RedisCapability, type RedisWriteBody } from './api'
import RedisChannelsPanel from './RedisChannelsPanel'
import { useToast } from '../../components/Toast'
import { useConfirm } from '../../lib/hooks/useConfirm'
import { SqlPreviewBody } from '../../components/common/SqlPreview'

type Table = { columns: string[]; rows: any[][] } | null

// 值列的列名按类型不同(hash=field/value, list=index/value, zset=member/score, stream=id/fields)。
// 键列表只有一列(SCAN 返回键名), 驱动给的名字可能是 key 或 value。
const KEY_COLS = ['key', 'value', 'name']

function fmtCell(v: any) {
  if (v === null || v === undefined || v === '') return <span className="dim">—</span>
  if (typeof v === 'object') {
    // 二进制值后端给的是 {encoding:"base64", body, size} —— 直接说清楚, 别让用户以为是乱码
    if ((v as any).encoding === 'base64') {
      return <span className="mono small" title={`base64, ${(v as any).size} 字节`}>
        base64:{String((v as any).body).slice(0, 48)}{String((v as any).body).length > 48 ? '…' : ''}
        <span className="dim"> ({String((v as any).size)} 字节)</span>
      </span>
    }
    return <span className="mono small">{JSON.stringify(v)}</span>
  }
  return <span className="mono small">{String(v)}</span>
}

export default function RedisPanel({ connId, database, preset, onWriteLocked }: {
  connId: string; database: string; preset?: string; onWriteLocked?: (msg: string) => void
}) {
  const toast = useToast()
  const { confirm: askConfirm, confirmEl } = useConfirm()
  const [tab, setTab] = useState<'keys' | 'channels'>('keys')
  const [cap, setCap] = useState<RedisCapability | null>(null)
  const [capErr, setCapErr] = useState('')
  const [keys, setKeys] = useState<string[]>([])
  const [dbsize, setDbsize] = useState<number | null>(null)
  const [sizeNote, setSizeNote] = useState('')
  const [selected, setSelected] = useState('')
  const [detail, setDetail] = useState<Table>(null)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [filter, setFilter] = useState('')
  // 写操作表单
  const [editOpen, setEditOpen] = useState(false)
  const [editValue, setEditValue] = useState('')
  const [editTTL, setEditTTL] = useState('')
  const [renameOpen, setRenameOpen] = useState(false)
  const [toKey, setToKey] = useState('')

  useEffect(() => {
    let dead = false
    setCap(null); setCapErr(''); setKeys([]); setDetail(null); setSelected('')
    void redisCapabilities(connId).then(d => {
      if (dead) return
      if (!d.ok || !d.capability) { setCapErr(d.error || '该连接不是 Redis 数据源'); return }
      setCap(d.capability)
    })
    return () => { dead = true }
  }, [connId])

  // 只读动词走 /query(与查询编辑器同一条路, 不另开旁路)
  const run = useCallback(async (verb: string): Promise<Table & { error?: string }> => {
    const { status, data } = await runQueryRaw(connId, verb, 5000, false, database)
    if (status >= 400 || (data as any).error) {
      return { columns: [], rows: [], error: (data as any).error || `执行失败 (HTTP ${status})` }
    }
    return { columns: data.columns || [], rows: data.rows || [] }
  }, [connId, database])

  const reload = useCallback(async () => {
    setBusy(true); setDetail(null)
    const [r, sz] = await Promise.all([run('SCAN 0 COUNT 500'), run('DBSIZE')])
    setErr(r.error || '')
    if (r.error) { setKeys([]); setBusy(false); return }
    const col = KEY_COLS.find(c => r.columns.includes(c)) || r.columns[0]
    const idx = r.columns.indexOf(col)
    setKeys(r.rows.map(row => String(row[idx] ?? '')).filter(Boolean))
    // 单机 DBSIZE 是 {value}; Cluster 版是 {dbsize, primary_nodes, note}(汇总口径, 见驱动)
    const sizeIdx = ['value', 'dbsize'].reduce((acc, c) => acc >= 0 ? acc : sz.columns.indexOf(c), -1)
    if (sizeIdx >= 0 && sz.rows[0]) setDbsize(Number(sz.rows[0][sizeIdx]))
    const noteIdx = sz.columns.indexOf('note')
    setSizeNote(noteIdx >= 0 && sz.rows[0] ? String(sz.rows[0][noteIdx] || '') : '')
    setBusy(false)
  }, [run])

  useEffect(() => { void reload() }, [reload])

  const openKey = useCallback(async (key: string) => {
    setSelected(key); setBusy(true)
    const r = await run(`SELECT * FROM ${key} LIMIT 300`)
    setErr(r.error || '')
    setDetail(r.error ? null : { columns: r.columns, rows: r.rows })
    setBusy(false)
  }, [run])

  // 从树里点键进来: 只认第一次, 否则每次刷新都会重开一遍
  const presetDone = useRef('')
  useEffect(() => {
    if (!preset || keys.length === 0 || presetDone.current === preset) return
    presetDone.current = preset
    setTab('keys') // 人点的是键, 就得落在键空间页签上
    if (!keys.includes(preset)) { setErr(`当前键列表里没有 ${preset}(可能超过 SCAN 上限)`); return }
    void openKey(preset)
  }, [preset, keys, openKey])

  const shown = useMemo(() => {
    const q = filter.trim().toLowerCase()
    return q ? keys.filter(k => k.toLowerCase().includes(q)) : keys
  }, [keys, filter])

  // 写操作: 先确认(带可读预览) -> 发意图 -> 被写锁拦下就把提示交回上层
  // confirm 由这里统一置 true(弹窗已确认过), 调用方只表达意图 —— 免得某条路径漏传又被后端 403
  const doWrite = async (body: Omit<RedisWriteBody, 'confirm'>, preview: string, okText: string, danger = false) => {
    if (!(await askConfirm(preview, {
      desc: '写操作需要连接处于写解锁状态, 并会记入审计流水。',
      content: <SqlPreviewBody sqls={[preview]} caption="将要执行的操作" />,
      okText, danger, maxWidth: 560,
    }))) return false
    setBusy(true)
    try {
      const { status, data } = await redisWrite({ ...body, confirm: true })
      if (status === 403 && (data as any).code === 'write_locked') {
        onWriteLocked?.([(data as any).error, (data as any).reason].filter(Boolean).join(' —— ') || '写操作被拦截: 连接默认只读')
        return false
      }
      if (!(data as any).ok) { toast.error((data as any).error || '写入失败'); return false }
      toast.success(`已执行: ${(data as any).statement || preview}`)
      return true
    } finally {
      setBusy(false)
    }
  }

  const saveValue = async () => {
    const ttl = editTTL.trim() === '' ? 0 : Number(editTTL)
    if (Number.isNaN(ttl) || ttl < 0) { toast.error('TTL 要么留空(不过期), 要么填非负秒数'); return }
    const preview = ttl > 0
      ? `SET ${selected} <值 ${editValue.length} 字节> EX ${ttl}`
      : `SET ${selected} <值 ${editValue.length} 字节>`
    if (await doWrite({ op: 'set', id: connId, database, key: selected, value: editValue, ttlSeconds: ttl }, preview, '写入')) {
      setEditOpen(false); void openKey(selected); void reload()
    }
  }

  const saveTTL = async () => {
    const ttl = editTTL.trim() === '' ? 0 : Number(editTTL)
    if (Number.isNaN(ttl) || ttl < 0) { toast.error('TTL 要么留空(清除过期), 要么填非负秒数'); return }
    const preview = ttl > 0 ? `EXPIRE ${selected} ${ttl}` : `PERSIST ${selected}`
    if (await doWrite({ op: 'expire', id: connId, database, key: selected, ttlSeconds: ttl }, preview, ttl > 0 ? '设置过期' : '清除过期')) {
      setEditOpen(false); void openKey(selected)
    }
  }

  const delKey = async () => {
    if (await doWrite({ op: 'del', id: connId, database, key: selected }, `DEL ${selected}`, '删除', true)) {
      setSelected(''); setDetail(null); void reload()
    }
  }

  const doRename = async () => {
    if (!toKey.trim()) { toast.error('新键名不能为空'); return }
    if (await doWrite({ op: 'rename', id: connId, database, key: selected, toKey: toKey.trim() },
      `RENAME ${selected} ${toKey.trim()}`, '改名')) {
      setRenameOpen(false); setToKey(''); setSelected(toKey.trim()); void reload()
    }
  }

  if (capErr) return <div className="db-empty">{capErr}</div>
  if (!cap) return <div className="db-empty">读取能力清单中…</div>

  const TOPOLOGY: Record<string, string> = { single: '单机', sentinel: 'Sentinel', cluster: 'Cluster' }
  const topoLabel = TOPOLOGY[cap.topology || 'single'] || cap.topology

  return (
    <div className="db-mq">
      <div className="db-mq-tabs">
        <button className={`btn-glass-soft btn-glass-soft-sm ${tab === 'keys' ? 'btn-glass-soft-accent' : ''}`}
          onClick={() => setTab('keys')}>键空间</button>
        {cap.pubsub !== false && (
          <button className={`btn-glass-soft btn-glass-soft-sm ${tab === 'channels' ? 'btn-glass-soft-accent' : ''}`}
            onClick={() => setTab('channels')}>频道</button>
        )}
        <span className="dim" style={{ marginLeft: 'auto', fontSize: '0.75rem' }}>
          {topoLabel}{cap.multiDb === false ? ' · 这个拓扑只有 db0' : ''}
        </span>
        {tab === 'keys' && (
          <>
            <span className="dim" style={{ fontSize: '0.75rem' }}>
              库 <b>{database}</b> · {shown.length} 个键{dbsize !== null ? ` / 共 ${dbsize}` : ''}
              {keys.length >= 2000 ? ' (SCAN 上限 2000, 可能不全)' : ''}
            </span>
            <input className="input log-input" style={{ maxWidth: 220 }} placeholder="过滤键名"
              value={filter} onChange={e => setFilter(e.target.value)} aria-label="过滤键名" />
            <button className="btn-glass-soft btn-glass-soft-sm" onClick={reload} disabled={busy}>
              {busy ? '读取中…' : '刷新'}
            </button>
          </>
        )}
      </div>
      {err && <div className="banner banner-err small">{err}</div>}

      {tab === 'channels' && (
        <RedisChannelsPanel connId={connId} database={database} topology={cap.topology} onWriteLocked={onWriteLocked} />
      )}

      {tab === 'keys' && (<>
      {cap.note && <div className="db-mq-note">{cap.note}</div>}
      {sizeNote && <div className="db-mq-note">{sizeNote}</div>}

      <div className="table-wrap" style={{ maxHeight: '16rem', overflow: 'auto' }}>
        <table className="db-table">
          <thead><tr><th>键</th><th style={{ width: 120 }}>操作</th></tr></thead>
          <tbody>
            {shown.length === 0 ? (
              <tr><td colSpan={2} className="dim" style={{ textAlign: 'center', padding: 12 }}>
                {busy ? '读取中…' : '这个库没有键'}
              </td></tr>
            ) : shown.map(k => (
              <tr key={k} className={k === selected ? 'db-mq-row-active' : undefined} onClick={() => void openKey(k)}>
                <td><span className="mono small">{k}</span></td>
                <td onClick={e => e.stopPropagation()}>
                  <span className="db-mq-acts">
                    <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => void openKey(k)}>查看</button>
                  </span>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      {selected && (
        <div className="db-mq-detail">
          <div className="db-mq-sub-title">
            键 {selected}
            <span style={{ marginLeft: 'auto', display: 'flex', gap: 4 }}>
              <button className="btn-glass-soft btn-glass-soft-sm"
                onClick={() => { setEditOpen(!editOpen); setEditValue(''); setEditTTL('') }}>
                {editOpen ? '收起编辑' : '写入 / 过期'}
              </button>
              <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => { setRenameOpen(!renameOpen); setToKey('') }}>
                {renameOpen ? '收起改名' : '改名'}
              </button>
              <button className="btn-glass-soft btn-glass-soft-sm" onClick={delKey} disabled={busy}>删除</button>
            </span>
          </div>

          {editOpen && (
            <div className="db-mq-publish">
              <div className="db-mq-fields">
                <label className="db-form-grow" title="留空表示不过期">
                  <span className="dim">TTL(秒)</span>
                  <input className="input" type="number" min={0} value={editTTL}
                    onChange={e => setEditTTL(e.target.value)} placeholder="留空=不过期" />
                </label>
              </div>
              <textarea className="db-query-textarea" style={{ minHeight: '5rem' }}
                placeholder="新值(string 键)…" value={editValue} onChange={e => setEditValue(e.target.value)}
                aria-label="键的新值" />
              <div className="db-mq-acts" style={{ marginTop: 4 }}>
                <button className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-accent" disabled={busy}
                  onClick={saveValue}>写入值</button>
                <button className="btn-glass-soft btn-glass-soft-sm" disabled={busy} onClick={saveTTL}>
                  {editTTL.trim() === '' ? '清除过期' : '只改过期'}
                </button>
              </div>
              <div className="dim small">写入值只对 string 键有效; 其它类型请用查询编辑器发对应命令。</div>
            </div>
          )}

          {renameOpen && (
            <div className="db-mq-publish">
              <div className="db-mq-fields">
                <label className="db-form-grow" title="目标键已存在会被拒绝(不静默覆盖)">
                  <span className="dim">新键名</span>
                  <input className="input" value={toKey} onChange={e => setToKey(e.target.value)} placeholder="新的键名" />
                </label>
              </div>
              <div className="db-mq-acts" style={{ marginTop: 4 }}>
                <button className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-accent" disabled={busy || !toKey.trim()}
                  onClick={doRename}>改名</button>
              </div>
            </div>
          )}

          {detail ? (
            <>
              <div className="table-wrap" style={{ maxHeight: '22rem', overflow: 'auto' }}>
                <table className="db-table">
                  <thead><tr>{detail.columns.map(c => <th key={c}>{c}</th>)}</tr></thead>
                  <tbody>
                    {detail.rows.length === 0 ? (
                      <tr><td colSpan={detail.columns.length} className="dim" style={{ textAlign: 'center', padding: 12 }}>这个键没有条目</td></tr>
                    ) : detail.rows.map((r, i) => (
                      <tr key={i}>{detail.columns.map((c, j) => <td key={c}>{fmtCell(r[j])}</td>)}</tr>
                    ))}
                  </tbody>
                </table>
              </div>
              <div className="dim small">{detail.rows.length} 条(最多 300)</div>
            </>
          ) : <div className="db-empty-sm">{busy ? '读取中…' : '没有读到值'}</div>}
        </div>
      )}
      </>)}
      {confirmEl}
    </div>
  )
}
