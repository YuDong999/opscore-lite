// Redis 频道面板: 快照(PUBSUB CHANNELS/NUMSUB/NUMPAT) + 实时看一会儿 + 发布。
//
// 三条与后端对齐的口径:
//  1. 快照是**纯只读**, 不需要建立订阅就能看"有哪些频道、各有几个订阅者" —— 所以它挂在 GET 上,
//     只读锁也不拦它。
//  2. "实时看一会儿"是**有限时长**的订阅(到点自动退订)。本产品不做常驻订阅: 控制台留一条
//     没人管的订阅, 既占连接也让"谁在订阅"变模糊。
//  3. PUBLISH 是**写**(会推给所有订阅者, 可能触发别人的业务), 所以走与 SET/DEL 同一条护栏:
//     确认弹窗 + 写锁 + 只发表达意图(正文不进审计)。
//
// 界面要把一件容易误解的事说清楚: Redis 的 Pub/Sub **不属于某个库** —— 频道是全局的,
// 连接配置里选的库只决定用哪条连接发命令。

import { useCallback, useEffect, useMemo, useState } from 'react'
import {
  redisPublish, redisPubSubDrain, redisPubSubSnapshot,
  type RedisPubSubMessage, type RedisPubSubSnapshot,
} from './api'
import { useToast } from '../../components/Toast'
import { useConfirm } from '../../lib/hooks/useConfirm'
import { SqlPreviewBody } from '../../components/common/SqlPreview'

const DRAIN_OPTIONS = [5, 15, 30, 60]

// 非 UTF-8 载荷后端给的是 base64 + 标记 —— 照实说, 别让人以为是显示坏了。
function renderPayload(m: RedisPubSubMessage) {
  if (m.payloadB64) {
    const bytes = Math.floor(m.payload.length * 3 / 4)
    return <span className="mono small">base64:{m.payload.slice(0, 64)}{m.payload.length > 64 ? '…' : ''}
      <span className="dim"> (约 {bytes} 字节, 非文本)</span></span>
  }
  return <span className="mono small">{m.payload.length > 400 ? m.payload.slice(0, 400) + '…' : m.payload}</span>
}

export default function RedisChannelsPanel({ connId, database, topology, onWriteLocked }: {
  connId: string; database: string; topology?: string; onWriteLocked?: (msg: string) => void
}) {
  const toast = useToast()
  const { confirm: askConfirm, confirmEl } = useConfirm()
  const [snap, setSnap] = useState<RedisPubSubSnapshot | null>(null)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [picked, setPicked] = useState<string[]>([])
  const [pattern, setPattern] = useState('')
  const [seconds, setSeconds] = useState(15)
  const [msgs, setMsgs] = useState<RedisPubSubMessage[]>([])
  const [drainNote, setDrainNote] = useState('')
  const [drainBad, setDrainBad] = useState(false)
  const [pubChannel, setPubChannel] = useState('')
  const [pubPayload, setPubPayload] = useState('')

  const reload = useCallback(async () => {
    setBusy(true)
    const r = await redisPubSubSnapshot(connId, database)
    if (!r.ok) setErr(r.error || '读取频道失败')
    else { setErr(''); setSnap(r.snapshot || null) }
    setBusy(false)
  }, [connId, database])

  useEffect(() => { void reload() }, [reload])

  // 快照里的频道会随时间变, 选中的频道要清掉已经不在的(否则订阅一个不存在的频道没意义)
  const shown = useMemo(() => snap?.channels || [], [snap])
  const validPicked = useMemo(() => {
    const names = new Set(shown.map(c => c.name))
    return picked.filter(n => names.has(n))
  }, [picked, shown])

  const toggle = (name: string) =>
    setPicked(p => (p.includes(name) ? p.filter(x => x !== name) : [...p, name]))

  const drain = async () => {
    const patterns = pattern.trim() ? [pattern.trim()] : []
    if (validPicked.length === 0 && patterns.length === 0) {
      toast.error('先勾选至少一个频道, 或填一个订阅模式')
      return
    }
    setBusy(true); setMsgs([]); setDrainBad(false)
    setDrainNote(`正在订阅 ${seconds}s…(到期自动退订)`)
    try {
      const { status, data } = await redisPubSubDrain({
        id: connId, database, channels: validPicked, patterns, seconds,
      })
      if (status === 403 && (data as any).code === 'write_locked') {
        onWriteLocked?.([(data as any).error, (data as any).reason].filter(Boolean).join(' —— ') || '被拦截')
        return
      }
      if (!(data as any).ok) {
        setErr((data as any).error || '订阅失败'); setDrainNote('')
        return
      }
      const res = (data as any).result
      setMsgs(res.messages || [])
      // "没收到"有两种真相: 这段时间确实安静, 或者订阅半路断了。后者必须显眼 ——
      // 把它显示成"没有消息"会让人以为频道是空的。
      const n = res.messages?.length || 0
      if (res.interrupted) {
        setDrainBad(true)
        setDrainNote(res.note || '订阅中途断开, 之后的消息收不到')
      } else {
        setDrainBad(false)
        setDrainNote(n === 0
          ? `${res.seconds}s 窗口内没有消息(订阅没有报错, 到期已退订)`
          : `${n} 条 / ${Math.round((res.elapsedMs || 0) / 1000)}s`
          + (res.slices > 1 ? ` · 分段重订阅 ${res.slices} 次` : '')
          + (res.note ? ` · ${res.note}` : ''))
      }
    } finally {
      setBusy(false)
    }
  }

  // 发布是写: 先确认(预览里不出现正文), 被写锁拦下就把解锁提示交回上层
  const doPublish = async () => {
    const ch = pubChannel.trim()
    if (!ch) { toast.error('频道名不能为空'); return }
    if (!pubPayload) { toast.error('消息正文不能为空'); return }
    const preview = `PUBLISH ${ch} <消息 ${pubPayload.length} 字节>`
    if (!(await askConfirm(preview, {
      desc: '这条消息会立刻推给该频道的**所有订阅者**(可能是别人的业务进程), 且无法撤回。',
      content: <SqlPreviewBody sqls={[preview]} caption="将要执行的操作" />,
      okText: '发布', danger: true, maxWidth: 560,
    }))) return
    setBusy(true)
    try {
      const { status, data } = await redisPublish({ id: connId, database, channel: ch, payload: pubPayload, confirm: true })
      if (status === 403 && (data as any).code === 'write_locked') {
        onWriteLocked?.([(data as any).error, (data as any).reason].filter(Boolean).join(' —— ') || '写操作被拦截: 连接默认只读')
        return
      }
      if (!(data as any).ok) { toast.error((data as any).error || '发布失败'); return }
      toast.success(`已发布: ${data.receivers ?? 0} 个订阅者收到`)
      setPubPayload('')
      void reload()
    } finally {
      setBusy(false)
    }
  }

  return (
    <div>
      <div className="db-mq-note">
        Redis 的频道是<b>全局</b>的, 不属于某个库(这里的“{database}”只决定用哪条连接发命令)。
        订阅数来自 <span className="mono">PUBSUB CHANNELS/NUMSUB</span>, 不需要建立订阅。
        {snap?.note ? ` ${snap.note}` : ''}
      </div>
      {err && <div className="banner banner-err small">{err}</div>}

      {topology === 'cluster' && (
        <div className="banner banner-warn small">
          Cluster 下的<b>实时订阅只挂在其中一条连接所在的节点</b>上: 频道列表与订阅数是各节点汇总的,
          但“实时看一会儿”可能漏掉别的节点上的消息 —— 这里显示 0 条<b>不等于</b>频道没有消息。
          shard 频道更是只在所属节点投递。
        </div>
      )}

      <div className="table-wrap" style={{ maxHeight: '15rem', overflow: 'auto' }}>
        <table className="db-table">
          <thead><tr><th style={{ width: 34 }} />
            <th>频道</th><th style={{ width: 90 }}>类型</th><th style={{ width: 90 }}>订阅者</th></tr></thead>
          <tbody>
            {shown.length === 0 ? (
              <tr><td colSpan={4} className="dim" style={{ textAlign: 'center', padding: 12 }}>
                {busy ? '读取中…' : '现在没有活动频道(没有客户端在订阅任何频道)'}
              </td></tr>
            ) : shown.map(c => (
              <tr key={`${c.kind}:${c.name}`} className={validPicked.includes(c.name) ? 'db-mq-row-active' : undefined}
                onClick={() => toggle(c.name)}>
                <td><input type="checkbox" checked={validPicked.includes(c.name)} onChange={() => toggle(c.name)}
                  aria-label={`订阅 ${c.name}`} onClick={e => e.stopPropagation()} /></td>
                <td><span className="mono small">{c.name}</span></td>
                <td className="small">{c.kind}</td>
                <td className="small">{c.subscribers}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      <div className="db-mq-acts" style={{ marginTop: 6, alignItems: 'center' }}>
        <input className="input log-input" style={{ maxWidth: 220 }} placeholder="订阅模式(如 news.*)"
          value={pattern} onChange={e => setPattern(e.target.value)} aria-label="订阅模式" />
        <label className="dim small">时长
          <select className="input" style={{ marginLeft: 4 }} value={seconds}
            onChange={e => setSeconds(Number(e.target.value))} aria-label="订阅时长">
            {DRAIN_OPTIONS.map(s => <option key={s} value={s}>{s}s</option>)}
          </select>
        </label>
        <button className="btn-glass-soft btn-glass-soft-sm" onClick={drain} disabled={busy}>
          {busy ? '进行中…' : '实时看一会儿'}
        </button>
        <button className="btn-glass-soft btn-glass-soft-sm" style={{ marginLeft: 'auto' }}
          onClick={() => void reload()} disabled={busy}>刷新频道</button>
      </div>
      {drainBad
        ? <div className="banner banner-warn small" style={{ marginTop: 4 }}>{drainNote}</div>
        : <div className="dim small" style={{ marginTop: 4 }}>{drainNote}</div>}

      {msgs.length > 0 && (
        <div className="db-mq-detail">
          <div className="db-mq-sub-title">收到的消息 {msgs.length} 条
            <span className="dim" style={{ marginLeft: 'auto' }}>只保留这次订阅期间收到的</span>
          </div>
          <div className="table-wrap" style={{ maxHeight: '16rem', overflow: 'auto' }}>
            <table className="db-table">
              <thead><tr><th style={{ width: 90 }}>时间</th><th style={{ width: 160 }}>频道</th><th>消息</th></tr></thead>
              <tbody>
                {msgs.map((m, i) => (
                  <tr key={i}>
                    <td className="small">{new Date(m.at * 1000).toLocaleTimeString()}</td>
                    <td><span className="mono small">{m.channel}</span>
                      {m.pattern ? <span className="dim small"> ←{m.pattern}</span> : null}</td>
                    <td>{renderPayload(m)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}

      <div className="db-mq-publish">
        <div className="db-mq-sub-title">发布消息(写操作)</div>
        <div className="db-mq-fields">
          <label className="db-form-grow" title="点上面的频道行也能把频道名填进来">
            <span className="dim">频道</span>
            <input className="input" value={pubChannel} onChange={e => setPubChannel(e.target.value)}
              placeholder="频道名" />
          </label>
        </div>
        <textarea className="db-query-textarea" style={{ minHeight: '4rem' }} placeholder="消息正文…"
          value={pubPayload} onChange={e => setPubPayload(e.target.value)} aria-label="消息正文" />
        <div className="db-mq-acts" style={{ marginTop: 4 }}>
          <button className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-accent" disabled={busy}
            onClick={doPublish}>发布</button>
          <span className="dim small">审计里只记“往哪个频道发了多少字节”, 正文不落日志。</span>
        </div>
      </div>
      {confirmEl}
    </div>
  )
}
