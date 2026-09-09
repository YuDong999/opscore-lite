// Pod/Service 端口转发 (kubectl port-forward 等价, opscore 服务端开本地端口)
// WS 帧: 服务端 ready/log/error/exit; 客户端 stop

import { useEffect, useRef, useState } from 'react'

interface Props {
  cluster: string
  ns: string
  name: string
  res?: string // pods | services
  onClose: () => void
  onMsg?: (m: string) => void
}

export default function PortForwardModal({ cluster, ns, name, res = 'pods', onClose, onMsg }: Props) {
  const [ports, setPorts] = useState('')
  const [address, setAddress] = useState('127.0.0.1')
  const [running, setRunning] = useState(false)
  const [lines, setLines] = useState<string[]>([])
  const [err, setErr] = useState<string | null>(null)
  const wsRef = useRef<WebSocket | null>(null)

  const stop = () => {
    if (wsRef.current && wsRef.current.readyState === WebSocket.OPEN) {
      wsRef.current.send(JSON.stringify({ type: 'stop' }))
    }
    try { wsRef.current?.close() } catch { /* ignore */ }
    wsRef.current = null
    setRunning(false)
  }

  useEffect(() => () => { try { wsRef.current?.close() } catch { /* ignore */ } }, [])

  const start = () => {
    if (!ports.trim()) return
    setErr(null)
    setLines([])
    const proto = window.location.protocol === 'https:' ? 'wss' : 'ws'
    const url = `${proto}://${window.location.host}/api/plugins/containers/k8s/port-forward/ws?cluster=${encodeURIComponent(cluster)}&res=${res}&ns=${encodeURIComponent(ns)}&name=${encodeURIComponent(name)}&ports=${encodeURIComponent(ports.trim())}&address=${encodeURIComponent(address)}`
    let ws: WebSocket
    try {
      ws = new WebSocket(url)
    } catch (e) {
      setErr(`WS 构造失败: ${String(e)}`)
      return
    }
    wsRef.current = ws
    ws.onopen = () => setRunning(true)
    ws.onmessage = (ev) => {
      try {
        const f = JSON.parse(ev.data)
        if (f.type === 'ready' || f.type === 'log') {
          setLines((l) => [...l, f.data || ''])
        } else if (f.type === 'error') {
          setErr(f.message || '转发错误')
          setRunning(false)
        } else if (f.type === 'exit') {
          setRunning(false)
        }
      } catch { /* ignore */ }
    }
    ws.onerror = () => { setErr('WS 连接失败'); }
    ws.onclose = () => { setRunning(false); wsRef.current = null }
  }

  return (
    <div className="modal-overlay" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()} style={{ maxWidth: 620, width: '90vw' }}>
        <h3>端口转发 — {res === 'services' ? 'Service' : 'Pod'} {name}</h3>
        {!running ? (
          <div className="btn-row" style={{ marginTop: '0.5rem', flexWrap: 'wrap', gap: 8 }}>
            <input className="input mono" style={{ width: 200 }} placeholder="端口映射 如 8080:80" value={ports} onChange={(e) => setPorts(e.target.value)} />
            <input className="input mono" style={{ width: 160 }} placeholder="本地监听地址" value={address} onChange={(e) => setAddress(e.target.value)} />
            <button className="btn-glass-soft btn-glass-soft-accent" disabled={!ports.trim()} onClick={start}>开始转发</button>
          </div>
        ) : (
          <div className="dim" style={{ marginTop: '0.5rem' }}>
            转发中 · {ports} @ {address} — 保持此窗口打开, 关闭即断开
          </div>
        )}
        {err && <div style={{ marginTop: '0.5rem', color: 'var(--danger)' }}>{err}</div>}
        <pre className="code-block" style={{ marginTop: '0.75rem', minHeight: 120, maxHeight: 300, overflow: 'auto', fontSize: '0.75rem', whiteSpace: 'pre-wrap' }}>
          {lines.length ? lines.join('\n') : (running ? '等待就绪…' : '')}
        </pre>
        <div className="btn-row" style={{ marginTop: '1rem', justifyContent: 'flex-end' }}>
          {running && <button className="btn-glass-soft" onClick={stop}>停止并关闭</button>}
          {!running && <button className="btn-glass-soft" onClick={onClose}>关闭</button>}
        </div>
      </div>
    </div>
  )
}