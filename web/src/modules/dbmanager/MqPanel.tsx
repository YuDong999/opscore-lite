// 消息队列管理面板: 浏览(topic/队列/交换机/vhost/消费组) + 详情 + 取数 + 发送消息。
//
// 形态上跟 ProcessListPanel 同一路子: 列表是驱动已有的只读动词, 面板不自己拼协议;
// 差别在"能做什么"完全由后端那份能力清单决定(mq/capabilities) —— 四种 MQ 的动词和
// 发送可选项都不一样, 差异只写在一个地方, 面板这里不出现引擎名分支。
//
// 发送是写操作: 走 interceptWrite 那道(只读锁 + 审计), 被拦时把解锁提示交回上层,
// 与查询编辑器同一套反应; 确认弹窗里给的是可读的 PRODUCE 文本, 不是驱动内部的 JSON。

import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { mqCapabilities, mqPublish, runQueryRaw, type MqCapability, type MqView } from './api'
import { useToast } from '../../components/Toast'
import { useConfirm } from '../../lib/hooks/useConfirm'
import { SqlPreviewBody } from '../../components/common/SqlPreview'

// 列表里"名字"在哪一列, 各引擎叫法不同(topic/queue/exchange/group...), 按这个顺序取第一个有值的。
const NAME_KEYS = ['queue', 'topic', 'group', 'vhost', 'exchange', 'name']

type Table = { columns: string[]; rows: any[][] } | null

function fmtCell(v: any) {
  if (isNilish(v)) return <span className="dim">—</span>
  return <span className="mono small">{typeof v === 'object' ? JSON.stringify(v) : String(v)}</span>
}

// 按给定列顺序取一行的单元格(列不在这张表里就留空, 不会错位)
function cellsOf(table: Table, cols: { key: string; label: string }[], row: any[]) {
  return cols.map(c => {
    const i = table ? table.columns.indexOf(c.key) : -1
    return <td key={c.key}>{fmtCell(i >= 0 ? row[i] : undefined)}</td>
  })
}

function pickName(columns: string[], row: any[]): string {
  for (const k of NAME_KEYS) {
    const i = columns.indexOf(k)
    if (i >= 0 && row[i] != null && String(row[i]).trim() !== '') return String(row[i])
  }
  return ''
}

// 驱动对缺失字段给的是字符串 "<nil>"(GoNavi 移植带的口径), 界面上直接说"没有"更诚实
function isNilish(v: any) {
  return v === null || v === undefined || v === '' || v === '<nil>' || (typeof v === 'object' && v !== null && Object.keys(v).length === 0)
}

// 列顺序: 先按给的偏好排, 没列进去的原样追加 —— 驱动多给一列时我们会看到, 而不是丢。
function orderCols(columns: string[], pref: { key: string; label: string }[] = []) {
  const hit = (pref || []).filter(p => columns.includes(p.key))
  const rest = columns.filter(c => !hit.some(p => p.key === c))
  return [...hit, ...rest.map(c => ({ key: c, label: c }))]
}

// 消息/详情这类结果没有引擎给的偏好, 但驱动的列是按字母排的(payload 会夹在一堆元数据中间),
// 所以这里给一个跨引擎的"先看这些"顺序。
const MSG_COLS = ['queue', 'topic', 'offset', 'partition', 'queue_id', 'msg_id', 'key', 'payload', 'value',
  'body', 'tag', 'tags', 'qos', 'retain', 'redelivered', 'message_count', 'timestamp', 'born_timestamp', 'received_at']
  .map(k => ({ key: k, label: k }))

export default function MqPanel({ connId, preset, onWriteLocked }: { connId: string; engine: string; preset?: string; onWriteLocked?: (msg: string) => void }) {
  const toast = useToast()
  const { confirm: askConfirm, confirmEl } = useConfirm()
  const [cap, setCap] = useState<MqCapability | null>(null)
  const [capErr, setCapErr] = useState('')
  const [view, setView] = useState<MqView | null>(null)
  const [list, setList] = useState<Table>(null)
  const [detail, setDetail] = useState<Table>(null)
  const [peek, setPeek] = useState<Table>(null)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [filter, setFilter] = useState('')
  const [selected, setSelected] = useState('')
  const [pubOpen, setPubOpen] = useState(false)
  const [pubVals, setPubVals] = useState<Record<string, any>>({})
  const [payload, setPayload] = useState('')

  useEffect(() => {
    let dead = false
    setCap(null); setView(null); setList(null); setDetail(null); setPeek(null); setCapErr('')
    void mqCapabilities(connId).then(d => {
      if (dead) return
      if (!d.ok || !d.capability) { setCapErr(d.error || '该连接不支持消息队列管理'); return }
      setCap(d.capability)
      setView(d.capability.views[0] || null)
    })
    return () => { dead = true }
  }, [connId])

  const run = useCallback(async (verb: string): Promise<Table & { error?: string }> => {
    const { status, data } = await runQueryRaw(connId, verb)
    if (status >= 400 || (data as any).error) {
      return { columns: [], rows: [], error: (data as any).error || `执行失败 (HTTP ${status})` }
    }
    return { columns: data.columns || [], rows: data.rows || [] }
  }, [connId])

  const reload = useCallback(async () => {
    if (!view) return
    setBusy(true)
    setDetail(null); setPeek(null); setSelected('')
    const r = await run(view.list)
    setErr((r as any).error || '')
    setList(r.error ? null : { columns: r.columns, rows: r.rows })
    setBusy(false)
  }, [view, run])

  useEffect(() => { void reload() }, [reload])

  const rows = useMemo(() => {
    const all = list?.rows || []
    const q = filter.trim().toLowerCase()
    if (!q) return all
    return all.filter(r => r.some(c => String(c ?? '').toLowerCase().includes(q)))
  }, [list, filter])

  const shownCols = useMemo(() => orderCols(list?.columns || [], view?.cols || []), [list, view])

  // 从树里「查看消息」进来: 列表一到手就选中这一项并取数一次。
  // 只认第一次(presetDone) —— 否则每次手动刷新列表都会重跑一遍取数。
  const presetDone = useRef('')
  useEffect(() => {
    if (!preset || !list || presetDone.current === preset) return
    presetDone.current = preset
    const hit = list.rows.some(r => pickName(list.columns, r) === preset)
    if (!hit) { setErr(`列表里没有 ${preset}(可能被过滤了, 或它不在这一页签下)`); return }
    setSelected(preset)
    if (view?.peek) {
      void (async () => {
        const r = await run(view.peek!.replace('%s', preset))
        setErr((r as any).error || '')
        setPeek(r.error ? null : { columns: r.columns, rows: r.rows })
      })()
    }
  }, [preset, list, view, run])

  const openDetail = async () => {
    if (!view?.detail || !selected) return
    const r = await run(view.detail.replace('%s', selected))
    setErr((r as any).error || '')
    setDetail(r.error ? null : { columns: r.columns, rows: r.rows })
  }
  const openPeek = async () => {
    if (!view?.peek || !selected) return
    setBusy(true)
    const r = await run(view.peek.replace('%s', selected))
    setErr((r as any).error || '')
    setPeek(r.error ? null : { columns: r.columns, rows: r.rows })
    setBusy(false)
    if (!r.error) toast.info(`从 ${selected} 取了 ${r.rows.length} 条`)
  }

  const send = async () => {
    if (!view || !selected || !payload) { toast.error('先选一个目的地, 并填消息正文'); return }
    const extras = Object.entries(pubVals).filter(([, v]) => v !== '' && v !== undefined && v !== null)
    const preview = `PRODUCE ${cap?.engine} ${view.key === 'queues' ? 'queue' : 'publish'}=${selected}` +
      (extras.length ? ' ' + extras.map(([k, v]) => `${k}=${v}`).join(' ') : '') + ` bytes=${new Blob([payload]).size}`
    if (!(await askConfirm(`向 ${selected} 投递 1 条消息?`, {
      desc: '投递后消息就进了队列/topic, 这里撤不回来。连接需处于写解锁状态。',
      content: <SqlPreviewBody sqls={[preview, payload]} caption="将要执行的操作 / 消息正文" />,
      okText: '投递', danger: true, maxWidth: 640,
    }))) return
    setBusy(true)
    try {
      const { status, data } = await mqPublish({ id: connId, destination: selected, payload, confirm: true, ...pubVals })
      if (status === 403 && data.code === 'write_locked') {
        onWriteLocked?.([data.error, data.reason].filter(Boolean).join(' —— ') || '写操作被拦截: 连接默认只读')
        return
      }
      if (!data.ok) { toast.error(data.error || '投递失败'); return }
      toast.success(`已投递: ${data.statement || selected}`)
      setPubOpen(false); setPayload('')
    } finally {
      setBusy(false)
    }
  }

  if (capErr) return <div className="db-empty">{capErr}</div>
  if (!cap) return <div className="db-empty">读取能力清单中…</div>

  return (
    <div className="db-mq">
      <div className="db-mq-tabs">
        {cap.views.map(v => (
          <button key={v.key} className={`btn-glass-soft btn-glass-soft-sm ${view?.key === v.key ? 'btn-glass-soft-accent' : ''}`}
            onClick={() => setView(v)}>{v.label}</button>
        ))}
        <span className="dim" style={{ marginLeft: 'auto', fontSize: '0.75rem' }}>
          {view?.label} {rows.length}{list && list.rows.length !== rows.length ? ` / 共 ${list.rows.length}` : ''}
        </span>
        <input className="input log-input" style={{ maxWidth: 200 }} placeholder="过滤名称" value={filter}
          onChange={e => setFilter(e.target.value)} aria-label="过滤 MQ 对象" />
        <button className="btn-glass-soft btn-glass-soft-sm" onClick={reload} disabled={busy}>
          {busy ? '读取中…' : '刷新'}
        </button>
      </div>
      {view?.note && <div className="db-mq-note">{view.note}</div>}
      {err && <div className="banner banner-err small">{err}</div>}

      <div className="table-wrap">
        <table className="db-table">
          <thead>
            <tr>{shownCols.map(c => <th key={c.key}>{c.label}</th>)}<th style={{ width: 176 }}>操作</th></tr>
          </thead>
          <tbody>
            {rows.length === 0 ? (
              <tr><td colSpan={shownCols.length + 1} className="dim" style={{ textAlign: 'center', padding: 12 }}>
                {busy ? '读取中…' : `没有${view?.label || '对象'}`}
              </td></tr>
            ) : rows.map((r, i) => {
              const name = pickName(list?.columns || [], r)
              return (
                <tr key={i} className={name && name === selected ? 'db-mq-row-active' : undefined}
                  onClick={() => setSelected(name)}>
                  {cellsOf(list, shownCols, r)}
                  <td onClick={e => e.stopPropagation()}>
                    <span className="db-mq-acts">
                      <button className="btn-glass-soft btn-glass-soft-sm" disabled={!name || !view?.detail} onClick={openDetail}
                        title={name ? `查看 ${name}` : '这一行没取到名字'}>详情</button>
                      <button className="btn-glass-soft btn-glass-soft-sm" disabled={!name || !view?.peek || busy} onClick={openPeek}>取数</button>
                      <button className="btn-glass-soft btn-glass-soft-sm" disabled={!name || !view?.publishable}
                        onClick={() => { setPubOpen(true); setPubVals(Object.fromEntries((cap.fields || []).filter(f => f.default !== undefined).map(f => [f.name, f.default]))) }}
                        title={view?.publishable ? '向这一项投递消息' : '这类对象不能直接投递'}>投递</button>
                    </span>
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>

      {pubOpen && view?.publishable && (
        <div className="db-mq-publish">
          <div className="db-mq-sub-title">向 {selected} 投递消息</div>
          <div className="db-mq-fields">
            {(cap.fields || []).map(f => (
              <label key={f.name} className="db-form-grow" title={f.hint}>
                <span className="dim">{f.label}</span>
                {f.kind === 'bool' ? (
                  <input type="checkbox" checked={!!pubVals[f.name]} onChange={e => setPubVals(v => ({ ...v, [f.name]: e.target.checked }))} />
                ) : (
                  <input className="input" type={f.kind === 'int' ? 'number' : 'text'}
                    min={f.min} max={f.max} value={pubVals[f.name] ?? ''}
                    onChange={e => setPubVals(v => ({ ...v, [f.name]: e.target.value }))} />
                )}
                {f.hint && <span className="dim small">{f.hint}</span>}
              </label>
            ))}
          </div>
          <textarea className="db-query-textarea" style={{ minHeight: '5rem' }} placeholder="消息正文…" value={payload}
            onChange={e => setPayload(e.target.value)} aria-label="消息正文" />
          <div className="db-mq-acts" style={{ marginTop: 4 }}>
            <button className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-accent" disabled={busy || !payload} onClick={send}>投递</button>
            <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => setPubOpen(false)}>取消</button>
          </div>
        </div>
      )}

      {detail && (
        <div className="db-mq-detail">
          <div className="db-mq-sub-title">详情 · {selected}</div>
          <div className="db-cell-meta">
            {orderCols(detail.columns, [...(view?.cols || []), ...MSG_COLS]).map(c => (
              <div key={c.key}><span className="dim">{c.label}</span>
                {fmtCell(detail.rows[0]?.[detail.columns.indexOf(c.key)])}
              </div>
            ))}
          </div>
        </div>
      )}

      {peek && (
        <div className="db-mq-peek">
          <div className="db-mq-sub-title">消息 · {selected} · {peek.rows.length} 条</div>
          <div className="table-wrap">
            <table className="db-table">
              <thead><tr>{orderCols(peek.columns, MSG_COLS).map(c => <th key={c.key}>{c.label}</th>)}</tr></thead>
              <tbody>
                {peek.rows.map((r, i) => (
                  <tr key={i}>{cellsOf(peek, orderCols(peek.columns, MSG_COLS), r)}</tr>
                ))}
              </tbody>
            </table>
          </div>
          {view?.note && <div className="db-mq-note">{view.note}</div>}
        </div>
      )}
      {confirmEl}
    </div>
  )
}
