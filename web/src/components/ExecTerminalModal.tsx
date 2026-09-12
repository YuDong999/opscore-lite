// 交互式 Pod 终端 (kubectl exec -it 等价)
// 不依赖 xterm.js, 用 textarea + 自实现 ANSI 适配(基础)。
// WS 协议: 服务端帧 {type:"stdout"|"stderr"|"resize"|"exit"|"error", data, ...}
//          客户端帧 {type:"stdin"|"resize", data, rows, cols}

import { useEffect, useRef, useState } from 'react'

interface ExecTerminalProps {
  cluster: string
  ns: string
  pod: string
  container?: string
  containers: string[]
  command?: string
  onClose: () => void
  onMsg?: (m: string) => void
}

export default function ExecTerminalModal({
  cluster, ns, pod, containers, onClose, onMsg,
}: ExecTerminalProps) {
  const [container, setContainer] = useState<string>(containers[0] || '')
  const [cmd, setCmd] = useState('sh')
  const [open, setOpen] = useState(false)
  const [inputBuf, setInputBuf] = useState('')
  const [lines, setLines] = useState<string[]>([])
  const [err, setErr] = useState<string | null>(null)
  const [exited, setExited] = useState(false)
  const wsRef = useRef<WebSocket | null>(null)
  const scrollRef = useRef<HTMLDivElement | null>(null)
  const inputRef = useRef<HTMLInputElement | null>(null)

  useEffect(() => {
    if (!open) return
    const proto = window.location.protocol === 'https:' ? 'wss' : 'ws'
    const url = `${proto}://${window.location.host}/api/plugins/containers/k8s/pod/exec/ws?cluster=${encodeURIComponent(cluster)}&ns=${encodeURIComponent(ns)}&pod=${encodeURIComponent(pod)}&container=${encodeURIComponent(container)}&command=${encodeURIComponent(cmd)}&rows=24&cols=80&tty=1`
    let ws: WebSocket
    try {
      ws = new WebSocket(url)
    } catch (e) {
      setErr(`WS 构造失败: ${String(e)}`)
      return
    }
    wsRef.current = ws
    ws.onopen = () => {
      setOpen(true)
      setErr(null)
      setLines((l) => [...l, `\x1b[36m[已连接 ws]\x1b[0m`])
    }
    ws.onmessage = (ev) => {
      try {
        const f = JSON.parse(ev.data)
        if (f.type === 'stdout' || f.type === 'stderr') {
          setLines((l) => [...l, f.data || ''])
        } else if (f.type === 'exit') {
          setExited(true)
          setLines((l) => [...l, `\n\x1b[33m[进程退出 code=${f.code}]\x1b[0m`])
        } else if (f.type === 'error') {
          setErr(f.message || 'WS 错误')
        } else if (f.type === 'resize') {
          // ignore
        }
      } catch { /* ignore parse */ }
    }
    ws.onerror = () => setErr('WS 连接失败')
    ws.onclose = () => { setExited(true) }
    return () => {
      try { ws.close() } catch { /* ignore */ }
      wsRef.current = null
    }
  }, [open, cluster, ns, pod, container, cmd])

  // 自动滚到底
  useEffect(() => {
    if (scrollRef.current) scrollRef.current.scrollTop = scrollRef.current.scrollHeight
  }, [lines])

  const sendStdin = (s: string) => {
    const w = wsRef.current
    if (!w || w.readyState !== WebSocket.OPEN) return
    w.send(JSON.stringify({ type: 'stdin', data: s }))
  }

  const handleEnter = () => {
    if (exited) return
    sendStdin(inputBuf + '\n')
    setInputBuf('')
  }

  const handleSignal = (sig: string) => {
    sendStdin('')  // 不直接 send signal; 仅 ctrl+c 用 SIGINT 提示
    if (sig === 'INT') sendStdin('\x03')
  }

  return (
    <div className="modal-overlay" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()}
        style={{ maxWidth: 900, width: '95vw', height: '70vh', display: 'flex', flexDirection: 'column' }}>
        <h3>进入终端 — {ns ? `${ns}/` : ''}{pod}{container ? ` · ${container}` : ''}</h3>
        <div className="btn-row" style={{ marginTop: '0.5rem' }}>
          <span className="dim" style={{ fontSize: '0.75rem' }}>容器</span>
          <select className="input" style={{ width: 200 }} value={container}
            onChange={(e) => setContainer(e.target.value)} disabled={open && !exited}>
            {containers.length === 0 && <option value="">(默认)</option>}
            {containers.map((c) => <option key={c} value={c}>{c}</option>)}
          </select>
          <span className="dim" style={{ fontSize: '0.75rem' }}>命令</span>
          <input className="input" style={{ width: 200 }} value={cmd}
            onChange={(e) => setCmd(e.target.value)} disabled={open && !exited} />
          <span style={{ flex: 1 }} />
          {!open && <button className="btn-glass-soft btn-glass-soft-accent" onClick={() => setOpen(true)}>连接</button>}
          {open && <button className="btn-glass-soft" onClick={() => { handleSignal('INT'); onMsg?.('已发送 SIGINT (Ctrl-C)') }}>Ctrl-C</button>}
          <button className="btn-glass-soft" onClick={onClose}>关闭</button>
        </div>
        {err && <div className="banner-warn" style={{ marginTop: '0.5rem' }}>✗ {err}</div>}
        <div className="exec-terminal" ref={scrollRef} onClick={() => inputRef.current?.focus()}>
          <AnsiText text={lines.join('')} />
        </div>
        <div className="btn-row" style={{ marginTop: '0.5rem' }}>
          <span className="prompt">$</span>
          <input ref={inputRef} className="input" style={{ flex: 1, fontFamily: 'monospace' }}
            value={inputBuf} onChange={(e) => setInputBuf(e.target.value)}
            onKeyDown={(e) => { if (e.key === 'Enter') handleEnter() }}
            placeholder={open ? (exited ? '已断开' : '输入命令, 回车发送') : '点上方"连接"开始'} disabled={!open || exited} />
        </div>
      </div>
    </div>
  )
}

// 基础 ANSI 颜色解析, 其它控制字符(光标移动/状态查询)忽略。
// CSI 序列 = ESC '[' 数字/分号(参数) + 结尾字母。只有结尾 'm' 是颜色, 其余整体吞掉。
function AnsiText({ text }: { text: string }) {
  const out: { t: string; c?: string }[] = []
  let i = 0
  while (i < text.length) {
    if (text[i] === '\x1b' && text[i + 1] === '[') {
      // 找到 CSI 结尾字母: 跳过参数(0x30-0x3F 数字)、中间字节(0x20-0x2F), 定位最终字节(0x40-0x7E)
      let j = i + 2
      let fin = -1
      while (j < text.length) {
        const ch = text.charCodeAt(j)
        if (ch >= 0x40 && ch <= 0x7e) { fin = j; break }
        if ((ch >= 0x20 && ch <= 0x2f) || (ch >= 0x30 && ch <= 0x3f)) { j++ } else { break }
      }
      if (fin >= 0 && text[fin] === 'm') {
        const code = text.slice(i + 2, fin)
        const c = ansiColor(code)
        i = fin + 1
        if (c) out.push({ t: '', c })
        continue
      }
      // 颜色之外的 CSI(光标移动/状态查询如 \x1b[6n): 整体跳过
      if (fin < 0) break
      i = fin + 1
      continue
    }
    let j = i
    while (j < text.length && !(text[j] === '\x1b' && text[j + 1] === '[')) j++
    out.push({ t: text.slice(i, j) })
    i = j
  }
  return (
    <pre className="exec-ansi">
      {out.map((o, idx) => o.t ? <span key={idx} style={{ color: o.c || 'inherit' }}>{o.t}</span> : <span key={idx} />)}
    </pre>
  )
}

function ansiColor(code: string): string | undefined {
  // 简化: 0=重置, 31=红, 32=绿, 33=黄, 36=青, 37=白
  if (code === '0' || code === '') return undefined
  const map: Record<string, string> = {
    '31': '#ef4444', '32': '#22c55e', '33': '#eab308',
    '36': '#06b6d4', '37': '#e5e7eb', '91': '#f87171', '92': '#86efac', '93': '#facc15',
  }
  return map[code]
}
