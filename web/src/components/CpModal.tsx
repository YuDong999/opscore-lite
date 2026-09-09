// Pod 文件互拷 (kubectl cp 等价, WS + tar)
// from-pod: 服务端 tar 二进制流 → 前端收集成 .tar 下载
// to-pod:   前端读文件 → 二进制帧 → 服务端打成单成员 tar 解包到 podPath 目录

import { useEffect, useRef, useState } from 'react'

interface Props {
  cluster: string
  ns: string
  pod: string
  onClose: () => void
  onMsg?: (m: string) => void
}

export default function CpModal({ cluster, ns, pod, onClose, onMsg }: Props) {
  const [direction, setDirection] = useState<'from-pod' | 'to-pod'>('from-pod')
  const [podPath, setPodPath] = useState('')
  const [localName, setLocalName] = useState('')
  const [file, setFile] = useState<File | null>(null)
  const [busy, setBusy] = useState(false)
  const [result, setResult] = useState('')
  const [err, setErr] = useState<string | null>(null)
  const wsRef = useRef<WebSocket | null>(null)

  useEffect(() => () => { try { wsRef.current?.close() } catch { /* ignore */ } }, [])

  const url = (baseName: string) => {
    const proto = window.location.protocol === 'https:' ? 'wss' : 'ws'
    return `${proto}://${window.location.host}/api/plugins/containers/k8s/cp/ws?cluster=${encodeURIComponent(cluster)}&ns=${encodeURIComponent(ns)}&pod=${encodeURIComponent(pod)}&direction=${direction}&podPath=${encodeURIComponent(podPath.trim())}&baseName=${encodeURIComponent(baseName)}`
  }

  const download = () => {
    if (!podPath.trim()) return
    setBusy(true); setErr(null); setResult('')
    const ws = new WebSocket(url(''))
    wsRef.current = ws
    const chunks: BlobPart[] = []
    ws.binaryType = 'arraybuffer'
    ws.onmessage = (ev) => {
      if (typeof ev.data === 'string') {
        try {
          const f = JSON.parse(ev.data)
          if (f.type === 'error') setErr(f.message || 'cp 失败')
          if (f.type === 'exit') {
            if (chunks.length) {
              const blob = new Blob(chunks)
              const a = document.createElement('a')
              const base = podPath.trim().split('/').filter(Boolean).pop() || 'pod'
              a.href = URL.createObjectURL(blob)
              a.download = `${pod}-${base}.tar`
              a.click()
              setTimeout(() => URL.revokeObjectURL(a.href), 5000)
              setResult(`已下载 ${pod}-${base}.tar (${(blob.size / 1024).toFixed(1)} KiB)`)
            }
            ws.close()
          }
        } catch { /* ignore */ }
      } else {
        chunks.push(ev.data)
      }
    }
    ws.onerror = () => setErr('WS 连接失败')
    ws.onclose = () => setBusy(false)
  }

  const upload = () => {
    if (!podPath.trim() || !file) return
    setBusy(true); setErr(null); setResult('')
    const ws = new WebSocket(url(file.name))
    wsRef.current = ws
    ws.binaryType = 'arraybuffer'
    ws.onopen = () => {
      file.arrayBuffer().then((buf) => {
        const CHUNK = 256 * 1024
        for (let i = 0; i < buf.byteLength; i += CHUNK) {
          ws.send(buf.slice(i, Math.min(i + CHUNK, buf.byteLength)))
        }
        ws.send(JSON.stringify({ type: 'done' }))
      }).catch((e) => setErr(String(e)))
    }
    ws.onmessage = (ev) => {
      try {
        const f = JSON.parse(ev.data)
        if (f.type === 'error') setErr(f.message || 'cp 失败')
        if (f.type === 'exit') {
          setResult(`已上传 ${file.name} → ${podPath.trim()}`)
          setBusy(false)
          ws.close()
        }
      } catch { /* ignore */ }
    }
    ws.onerror = () => setErr('WS 连接失败')
    ws.onclose = () => setBusy(false)
  }

  return (
    <div className="modal-overlay" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()} style={{ maxWidth: 640, width: '90vw' }}>
        <h3>文件互拷 — Pod {pod}</h3>
        <div className="btn-row" style={{ marginTop: '0.5rem' }}>
          <button className={`btn-glass-soft btn-glass-soft-sm ${direction === 'from-pod' ? 'btn-glass-soft-accent' : ''}`} onClick={() => { setDirection('from-pod'); setResult('') }}>从 Pod 下载 → 本机</button>
          <button className={`btn-glass-soft btn-glass-soft-sm ${direction === 'to-pod' ? 'btn-glass-soft-accent' : ''}`} onClick={() => { setDirection('to-pod'); setResult('') }}>本机上传 → Pod</button>
        </div>
        <div className="btn-row" style={{ marginTop: '0.75rem', flexWrap: 'wrap', gap: 8 }}>
          <input className="input mono" style={{ flex: 1, minWidth: 240 }} placeholder="Pod 内路径 (下载: 文件路径; 上传: 目标目录, 如 /data)" value={podPath} onChange={(e) => setPodPath(e.target.value)} disabled={busy} />
          {direction === 'from-pod' && (
            <button className="btn-glass-soft btn-glass-soft-accent" disabled={busy || !podPath.trim()} onClick={download}>{busy ? '下载中…' : '下载'}</button>
          )}
          {direction === 'to-pod' && (
            <>
              <input type="file" onChange={(e) => setFile(e.target.files?.[0] || null)} disabled={busy} />
              <button className="btn-glass-soft btn-glass-soft-accent" disabled={busy || !podPath.trim() || !file} onClick={upload}>{busy ? '上传中…' : `上传 ${file?.name || ''}`}</button>
            </>
          )}
        </div>
        {localName && <div className="dim" style={{ marginTop: '0.25rem', fontSize: '0.75rem' }}>{localName}</div>}
        {err && <div style={{ marginTop: '0.5rem', color: 'var(--danger)' }}>{err}</div>}
        {result && <div style={{ marginTop: '0.5rem', color: 'var(--ok)' }}>{result}</div>}
        <div className="btn-row" style={{ marginTop: '1rem', justifyContent: 'flex-end' }}>
          <button className="btn-glass-soft" onClick={onClose}>关闭</button>
        </div>
      </div>
    </div>
  )
}