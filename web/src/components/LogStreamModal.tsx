// Pod 日志流式跟随 (kubectl logs -f 等价)

import { useEffect, useRef, useState } from 'react'

interface LogStreamProps {
  cluster: string
  ns: string
  pod: string
  container?: string
  containers: string[]
  onClose: () => void
  onMsg?: (m: string) => void
}

export default function LogStreamModal({
  cluster, ns, pod, containers, onClose, onMsg,
}: LogStreamProps) {
  const [container, setContainer] = useState(containers[0] || '')
  const [tail, setTail] = useState(200)
  const [follow, setFollow] = useState(true)
  const [previous, setPrevious] = useState(false)
  const [timestamps, setTimestamps] = useState(true)
  const [log, setLog] = useState('')
  const [err, setErr] = useState<string | null>(null)
  const [open, setOpen] = useState(false)
  const [exited, setExited] = useState(false)
  const wsRef = useRef<WebSocket | null>(null)
  const scrollRef = useRef<HTMLPreElement | null>(null)

  useEffect(() => {
    if (!open) return
    const proto = window.location.protocol === 'https:' ? 'wss' : 'ws'
    const url = `${proto}://${window.location.host}/api/plugins/containers/k8s/pod/logs/stream?cluster=${encodeURIComponent(cluster)}&ns=${encodeURIComponent(ns)}&pod=${encodeURIComponent(pod)}&container=${encodeURIComponent(container)}&tail=${tail}&previous=${previous ? 1 : 0}&follow=${follow ? 1 : 0}&timestamps=${timestamps ? 1 : 0}`
    let ws: WebSocket
    try {
      ws = new WebSocket(url)
    } catch (e) {
      setErr(`WS 构造失败: ${String(e)}`)
      return
    }
    wsRef.current = ws
    ws.onopen = () => {
      setErr(null)
      setLog('')
    }
    ws.onmessage = (ev) => {
      try {
        const f = JSON.parse(ev.data)
        if (f.type === 'log') {
          setLog((s) => s + (f.data || ''))
        } else if (f.type === 'log_meta') {
          setLog((s) => s + `\x1b[36m# 起始 ${f.tail} 行, follow=${f.follow}\x1b[0m\n`)
        } else if (f.type === 'exit') {
          setExited(true)
          setLog((s) => s + `\n\x1b[33m# ws 关闭 (code=${f.code})\x1b[0m\n`)
        } else if (f.type === 'error') {
          setErr(f.message || 'WS 错误')
        }
      } catch { /* ignore */ }
    }
    ws.onerror = () => setErr('WS 连接失败')
    ws.onclose = () => setExited(true)
    return () => { try { ws.close() } catch { /* ignore */ }; wsRef.current = null }
  }, [open, cluster, ns, pod, container, tail, follow, previous, timestamps])

  useEffect(() => {
    if (scrollRef.current) scrollRef.current.scrollTop = scrollRef.current.scrollHeight
  }, [log])

  return (
    <div className="modal-overlay" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()}
        style={{ maxWidth: 900, width: '95vw', height: '70vh', display: 'flex', flexDirection: 'column' }}>
        <h3>日志跟随 — {ns ? `${ns}/` : ''}{pod}{container ? ` · ${container}` : ''}</h3>
        <div className="btn-row" style={{ marginTop: '0.5rem' }}>
          <span className="dim" style={{ fontSize: '0.75rem' }}>容器</span>
          <select className="input" style={{ width: 200 }} value={container}
            onChange={(e) => setContainer(e.target.value)} disabled={open && !exited}>
            {containers.length === 0 && <option value="">(默认)</option>}
            {containers.map((c) => <option key={c} value={c}>{c}</option>)}
          </select>
          <span className="dim" style={{ fontSize: '0.75rem' }}>起始</span>
          <input className="input" type="number" style={{ width: 80 }} value={tail}
            onChange={(e) => setTail(Number(e.target.value) || 200)} disabled={open && !exited} />
          <label className="dim" style={{ fontSize: '0.75rem', display: 'inline-flex', gap: 4 }}>
            <input type="checkbox" checked={follow} onChange={(e) => setFollow(e.target.checked)} disabled={open && !exited} /> 跟随
          </label>
          <label className="dim" style={{ fontSize: '0.75rem', display: 'inline-flex', gap: 4 }}>
            <input type="checkbox" checked={previous} onChange={(e) => setPrevious(e.target.checked)} disabled={open && !exited} /> previous
          </label>
          <label className="dim" style={{ fontSize: '0.75rem', display: 'inline-flex', gap: 4 }}>
            <input type="checkbox" checked={timestamps} onChange={(e) => setTimestamps(e.target.checked)} disabled={open && !exited} /> 时间戳
          </label>
          <span style={{ flex: 1 }} />
          {!open && <button className="btn-glass-soft btn-glass-soft-accent" onClick={() => setOpen(true)}>连接</button>}
          {open && <button className="btn-glass-soft" onClick={() => { wsRef.current?.close(); onMsg?.('已断开') }}>断开</button>}
          <button className="btn-glass-soft" onClick={onClose}>关闭</button>
        </div>
        {err && <div className="banner-warn" style={{ marginTop: '0.5rem' }}>✗ {err}</div>}
        <pre className="exec-terminal" ref={scrollRef} style={{ flex: 1, marginTop: '0.5rem' }}>{log || (open ? '' : '点"连接"开始')}</pre>
      </div>
    </div>
  )
}
