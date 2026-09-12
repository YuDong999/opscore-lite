// K8s 资源 × 操作 通用面板
// 根据后端 /action-catalog 返回的 actions 动态渲染按钮组与表单。
// 调用方只需传 (res, name, ns, cluster) + onMsg, 即可得到一个完整的操作面板。

import { useEffect, useMemo, useRef, useState } from 'react'
import { getJSON, postJSON } from '../api/client'
import DynamicForm, { type FormField, type FormValues } from './DynamicForm'

export interface ActionParam {
  name: string
  label: string
  type: 'string' | 'number' | 'bool' | 'select' | 'code' | 'secret'
  required?: boolean
  default?: any
  help?: string
  options?: { label: string; value: string }[]
  min?: number
  max?: number
  pattern?: string
  useObjName?: boolean
}

export interface ActionSpec {
  name: string
  label: string
  category: string
  allowedRes: string[]
  params: ActionParam[]
  requiresTTY?: boolean
  description?: string
}

export interface K8sActionPanelProps {
  res: string
  name: string
  ns: string
  cluster: string
  onMsg?: (m: string) => void
  // 限制显示哪些 action, 例如只给 "lifecycle" 类别
  filterCategory?: string
  // 排除某些 action 名
  exclude?: string[]
  // 自定义按钮样式变体
  variant?: 'inline' | 'toolbar'
  // 关闭回调 (用于嵌入外层 modal)
  onClose?: () => void
  // 目录加载后自动打开某个 action 的表单 (如右键 → 编辑扩缩规则)
  openActionName?: string
}

const CATEGORY_ORDER = ['lifecycle', 'scale', 'edit', 'debug', 'stream', 'network']
const CATEGORY_LABEL: Record<string, string> = {
  lifecycle: '生命周期',
  scale: '扩缩容',
  edit: '编辑',
  debug: '调试',
  stream: '流式',
  network: '网络',
}

export default function K8sActionPanel({
  res, name, ns, cluster, onMsg, filterCategory, exclude, variant = 'inline', onClose, openActionName,
}: K8sActionPanelProps) {
  const [catalog, setCatalog] = useState<ActionSpec[] | null>(null)
  const [err, setErr] = useState<string | null>(null)
  const [running, setRunning] = useState<string | null>(null)
  const [openAction, setOpenAction] = useState<ActionSpec | null>(null)
  const [streamAction, setStreamAction] = useState<{ action: ActionSpec; values: FormValues } | null>(null)
  const [features, setFeatures] = useState<{ ephemeral: boolean }>({ ephemeral: false })

  // 拉取目录 + 平台开关 (一次)
  useEffect(() => {
    let stop = false
    Promise.all([
      getJSON<{ ok: boolean; actions: ActionSpec[]; ephemeral: boolean }>(
        `/api/plugins/containers/k8s/action-catalog?res=${encodeURIComponent(res)}&_=${Date.now()}`),
      getJSON<{ ok: boolean; ephemeral: boolean }>(
        `/api/plugins/containers/k8s/feature-flags?_=${Date.now()}`),
    ]).then(([a, f]) => {
      if (stop) return
      if (a.ok) {
        setCatalog(a.actions)
        if (openActionName) {
          const target = a.actions.find((x) => x.name === openActionName)
          if (target) setOpenAction(target)
        }
      }
      else setErr(a.error || '目录加载失败')
      if (f.ok) setFeatures({ ephemeral: !!f.ephemeral })
    }).catch((e) => { if (!stop) setErr(String(e)) })
    return () => { stop = true }
  }, [res])

  const filtered = useMemo(() => {
    if (!catalog) return []
    return catalog.filter((a) => {
      if (filterCategory && a.category !== filterCategory) return false
      if (exclude && exclude.includes(a.name)) return false
      if (a.name === 'ephemeral-add' || a.name === 'ephemeral-remove') {
        if (!features.ephemeral) return false
      }
      return true
    })
  }, [catalog, filterCategory, exclude, features])

  const grouped = useMemo(() => {
    const g: Record<string, ActionSpec[]> = {}
    for (const a of filtered) {
      g[a.category] = g[a.category] || []
      g[a.category].push(a)
    }
    return g
  }, [filtered])

  if (err) return <div className="dim" style={{ fontSize: '0.75rem' }}>操作目录: {err}</div>
  if (!catalog) return <div className="dim" style={{ fontSize: '0.75rem' }}>操作目录加载中…</div>
  if (!filtered.length) return <div className="dim" style={{ fontSize: '0.75rem' }}>此资源无可用操作</div>

  const run = async (action: ActionSpec, values: FormValues) => {
    if (action.requiresTTY) {
      if (action.name === 'port-forward' || action.name === 'cp') {
        if (action.name === 'cp' && !values.container && res === 'pods') {
          // 自动填充当前对象(pod)的第一个容器名, 可编辑
          try {
            const d = await getJSON<{ ok: boolean; detail: any }>(
              `/api/plugins/containers/k8s/pod/detail?cluster=${encodeURIComponent(cluster)}&res=pods&ns=${encodeURIComponent(ns)}&name=${encodeURIComponent(name)}`)
            if (d.ok && Array.isArray(d.detail?.containers) && d.detail.containers.length) {
              values = { ...values, container: String(d.detail.containers[0].name || '') }
            }
          } catch { /* 保持空, 后端默认首个容器 */ }
        }
        setStreamAction({ action, values })
        return
      }
      onMsg?.(`✗ ${action.label}: 流式操作请用专属入口(终端/日志按钮)`)
      return
    }
    setRunning(action.name)
    try {
      const d = await postJSON<{ ok: boolean; error?: string }>(
        '/api/plugins/containers/k8s/action', {
          cluster, res, ns, name, action: action.name, params: values,
        })
      if (d.ok) onMsg?.(`✓ ${action.label} 成功: ${name}`)
      else onMsg?.(`✗ ${action.label} 失败: ${d.error || '未知'}`)
    } catch (e) {
      onMsg?.(`✗ ${action.label} 失败: ${String(e)}`)
    } finally {
      setRunning(null)
      setOpenAction(null)
      onClose?.()
    }
  }

  return (
    <div className="k8s-action-panel" data-variant={variant}>
      {CATEGORY_ORDER.filter((c) => grouped[c]).map((cat) => (
        <div key={cat} className="k8s-action-group">
          <div className="k8s-action-group-label">{CATEGORY_LABEL[cat] || cat}</div>
          <div className="k8s-action-buttons">
            {grouped[cat].map((a) => (
              <button key={a.name}
                className={`btn-glass-soft btn-glass-soft-sm ${a.category === 'edit' ? 'btn-glass-soft-accent' : ''}`}
                disabled={!!running}
                onClick={() => setOpenAction(a)}
                title={a.description || a.label}>
                <span className="k8s-action-label">{a.label}</span>
                {running === a.name && <span className="k8s-action-running">执行中</span>}
              </button>
            ))}
          </div>
        </div>
      ))}

      {openAction && (
        <ActionModal
          action={openAction}
          res={res} name={name} ns={ns} cluster={cluster}
          onCancel={() => setOpenAction(null)}
          onSubmit={(v) => run(openAction, v)}
        />
      )}
      {streamAction && (
        <K8sStreamModal
          action={streamAction.action}
          values={streamAction.values}
          res={res} name={name} ns={ns} cluster={cluster}
          onCancel={() => setStreamAction(null)}
          onMsg={onMsg}
        />
      )}
    </div>
  )
}

// 单 action 弹窗: 表单 + 实时命令预览(确认前展示"将要执行的 kubectl 命令")
function ActionModal({
  action, res, name, ns, cluster, onCancel, onSubmit,
}: {
  action: ActionSpec
  res: string; name: string; ns: string; cluster: string
  onCancel: () => void
  onSubmit: (v: FormValues) => void
}) {
  const [values, setValues] = useState<FormValues>(() => {
    const v: FormValues = {}
    for (const p of action.params) {
      if (p.default !== undefined) v[p.name] = p.default
      else if (p.type === 'bool') v[p.name] = false
      else if (p.useObjName) v[p.name] = name
    }
    return v
  })
  const [preview, setPreview] = useState<{ command: string; supported: boolean; loading: boolean }>({ command: '', supported: false, loading: false })
  // 是否已应用"当前值"回填 (避免每次编辑都覆盖用户手改)
  const filledRef = useRef(false)

  // 首次打开: 请求命令预览 + 当前值回填
  useEffect(() => {
    if (filledRef.current) return
    filledRef.current = true
    let stop = false
    setPreview((p) => ({ ...p, loading: true }))
    const qs = new URLSearchParams({
      cluster, res, ns: ns || '', name, action: action.name,
    })
    getJSON<{ ok: boolean; command?: string; defaults?: Record<string, any>; supported?: boolean; error?: string }>(
      `/api/plugins/containers/k8s/action-preview?${qs.toString()}&_=${Date.now()}`,
    ).then((d) => {
      if (stop) return
      if (d.ok) {
        setPreview({ command: d.command || '', supported: !!d.supported, loading: false })
        if (d.supported && d.defaults) {
          setValues((prev) => {
            const next = { ...prev }
            for (const [k, v] of Object.entries(d.defaults!)) {
              if (v !== undefined && v !== null) next[k] = v
            }
            return next
          })
        }
      } else {
        setPreview({ command: '', supported: false, loading: false })
      }
    }).catch(() => { if (!stop) setPreview({ command: '', supported: false, loading: false }) })
    return () => { stop = true }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // 表单变化 → 重新请求预览
  useEffect(() => {
    if (!filledRef.current) return
    let stop = false
    const qs = new URLSearchParams({ cluster, res, ns: ns || '', name, action: action.name })
    for (const [k, v] of Object.entries(values)) {
      if (v === undefined || v === null) continue
      qs.set(k, String(v))
    }
    const tid = setTimeout(() => {
      getJSON<{ ok: boolean; command?: string; supported?: boolean; error?: string }>(
        `/api/plugins/containers/k8s/action-preview?${qs.toString()}&_=${Date.now()}`,
      ).then((d) => {
        if (stop) return
        if (d.ok && d.command) setPreview((p) => ({ command: d.command, supported: !!d.supported, loading: false }))
      }).catch(() => {})
    }, 250)
    return () => { stop = true; clearTimeout(tid) }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [values])

  const fields: FormField[] = action.params.map((p) => ({
    name: p.name, label: p.label, type: p.type, required: !!p.required,
    help: p.help, options: p.options, min: p.min, max: p.max, pattern: p.pattern,
  }))
  return (
    <div className="modal-overlay" onClick={onCancel}>
      <div className="modal" onClick={(e) => e.stopPropagation()} style={{ maxWidth: 620 }}>
        <h3>{action.label} — {res}/{name}{ns ? ` @ ${ns}` : ''}</h3>
        {action.description && <p className="dim" style={{ marginTop: '0.25rem', fontSize: '0.8125rem' }}>{action.description}</p>}
        <div style={{ marginTop: '0.75rem' }}>
          <DynamicForm fields={fields} values={values} onChange={setValues} />
        </div>

        {/* 命令预览框: 透明合同, 确认后仍走后端原生执行 */}
        {preview.supported && (preview.command || preview.loading) && (
          <div className="k8s-action-preview" style={{ marginTop: '0.75rem' }}>
            <div className="dim" style={{ fontSize: '0.75rem', marginBottom: '0.25rem' }}>
              即将执行的命令 (后端按此语义原生执行, 非 shell 执行):
            </div>
            <pre className="k8s-action-cmd">{preview.loading ? '生成中…' : preview.command}</pre>
          </div>
        )}

        <div className="btn-row" style={{ marginTop: '1rem' }}>
          <span className="dim" style={{ fontSize: '0.75rem' }}>cluster: {cluster}</span>
          <button className="btn-glass-soft" onClick={onCancel}>取消</button>
          <button className="btn-glass-soft btn-glass-soft-accent"
            onClick={() => onSubmit(values)}>确认</button>
        </div>
      </div>
    </div>
  )
}

// 流式 action (port-forward / cp) 专属弹窗: 生命周期绑定 WS。
// 帧协议: 服务端 → {type:"ready"|"log"|"error"|"exit", message, data}
//         port-forward 二进制不使用; cp 用 BinaryMessage 传输 tar/文件数据。
function K8sStreamModal({
  action, values: initValues, res, name, ns, cluster, onCancel, onMsg,
}: {
  action: ActionSpec
  values: FormValues
  res: string; name: string; ns: string; cluster: string
  onCancel: () => void
  onMsg?: (m: string) => void
}) {
  const [values, setValues] = useState<FormValues>(initValues)
  const [connected, setConnected] = useState(false)
  const [lines, setLines] = useState<string[]>([])
  const [err, setErr] = useState<string | null>(null)
  const [exited, setExited] = useState(false)
  const [sentBytes, setSentBytes] = useState(0)
  const [recvBytes, setRecvBytes] = useState(0)
  const wsRef = useRef<WebSocket | null>(null)
  const chunkRef = useRef<ArrayBuffer[]>([])
  const fileRef = useRef<HTMLInputElement | null>(null)

  const isPF = action.name === 'port-forward'

  const setParam = (k: string, v: any) => setValues((p) => ({ ...p, [k]: v }))

  const close = () => {
    const w = wsRef.current
    wsRef.current = null
    if (w) { try { w.close() } catch { /* noop */ } }
  }

  const stop = () => {
    const w = wsRef.current
    if (w && w.readyState === WebSocket.OPEN) w.send(JSON.stringify({ type: 'stop' }))
    else close()
  }

  useEffect(() => () => { close() }, [])

  const connect = () => {
    setErr(null); setConnected(false); setExited(false); setLines([])
    setSentBytes(0); setRecvBytes(0); chunkRef.current = []
    const proto = window.location.protocol === 'https:' ? 'wss' : 'ws'
    let url: string
    if (isPF) {
      const ports = String(values.ports || '8080:80')
      const address = String(values.address || '127.0.0.1')
      url = `${proto}://${window.location.host}/api/plugins/containers/k8s/port-forward/ws?cluster=${encodeURIComponent(cluster)}&res=${encodeURIComponent(res)}&ns=${encodeURIComponent(ns)}&name=${encodeURIComponent(name)}&ports=${encodeURIComponent(ports)}&address=${encodeURIComponent(address)}`
    } else {
      const direction = String(values.direction || 'from-pod')
      const podPath = String(values.podPath || '')
      const container = String(values.container || '')
      const baseName = String(values.baseName || '')
      const parts = [
        `cluster=${encodeURIComponent(cluster)}`, `ns=${encodeURIComponent(ns)}`, `pod=${encodeURIComponent(name)}`,
        `direction=${encodeURIComponent(direction)}`, `podPath=${encodeURIComponent(podPath)}`,
        `container=${encodeURIComponent(container)}`,
      ]
      if (direction === 'to-pod') parts.push(`baseName=${encodeURIComponent(baseName)}`)
      url = `${proto}://${window.location.host}/api/plugins/containers/k8s/cp/ws?${parts.join('&')}`
    }
    let ws: WebSocket
    try {
      ws = new WebSocket(url)
    } catch (e) {
      setErr(`WS 构造失败: ${String(e)}`)
      return
    }
    ws.binaryType = 'arraybuffer'
    wsRef.current = ws

    if (isPF) {
      ws.onmessage = (ev) => {
        try {
          const f = JSON.parse(ev.data)
          if (f.type === 'ready') {
            setConnected(true)
            setLines((l) => [...l, `[就绪] ${f.message || ''} — 连接关闭即停止转发`])
          } else if (f.type === 'log') {
            setLines((l) => [...l, String(f.data || '').trimEnd()])
          } else if (f.type === 'error') {
            setErr(f.message || 'WS 错误')
            setLines((l) => [...l, `[错误] ${f.message || ''}`])
          } else if (f.type === 'exit') {
            setExited(true)
            setLines((l) => [...l, `[转发已停止]`])
            close()
          }
        } catch { /* 非 JSON 帧忽略 */ }
      }
    } else {
      const direction = String(values.direction || 'from-pod')
      ws.onmessage = (ev) => {
        if (typeof ev.data === 'string') {
          try {
            const f = JSON.parse(ev.data)
            if (f.type === 'error') {
              setErr(f.message || 'WS 错误')
              setLines((l) => [...l, `[错误] ${f.message || ''}`])
            } else if (f.type === 'exit') {
              setExited(true)
              close()
            }
          } catch { /* ignore */ }
          return
        }
        // 二进制: from-pod 收集 tar
        if (direction === 'from-pod') {
          chunkRef.current.push(ev.data as ArrayBuffer)
          setRecvBytes((n) => n + (ev.data as ArrayBuffer).byteLength)
        }
      }
    }

    ws.onopen = () => {
      setConnected(true)
      // cp to-pod: 文件就绪后开始发送
      const w = wsRef.current
      if (!isPF && w && values.direction === 'to-pod' && fileRef.current?.files?.[0]) {
        void uploadToPod(w, fileRef.current.files[0])
      }
    }
    ws.onclose = () => { wsRef.current = null; setConnected(false) }
    ws.onerror = () => setErr('WS 连接异常')
  }

  const uploadToPod = async (ws: WebSocket, file: File) => {
    try {
      const buf = await file.arrayBuffer()
      if (!fileRef.current?.files?.[0]) return
      const CHUNK = 1 << 20
      for (let i = 0; i < buf.byteLength; i += CHUNK) {
        if (ws.readyState !== WebSocket.OPEN) return
        ws.send(buf.slice(i, i + CHUNK))
        setSentBytes((n) => n + Math.min(CHUNK, buf.byteLength - i))
      }
      ws.send(JSON.stringify({ type: 'done' }))
      setLines((l) => [...l, `[已发送 ${buf.byteLength} 字节, 等待解包…]`])
    } catch (e) {
      setErr(`读取文件失败: ${String(e)}`)
    }
  }

  const downloadTar = () => {
    if (!chunkRef.current.length) return
    const blob = new Blob(chunkRef.current, { type: 'application/x-tar' })
    const a = document.createElement('a')
    a.href = URL.createObjectURL(blob)
    a.download = `${name}-${String(values.podPath || 'file').split('/').filter(Boolean).pop()}.tar`
    a.click()
    URL.revokeObjectURL(a.href)
  }

  const isToPod = !isPF && values.direction === 'to-pod'

  return (
    <div className="modal-backdrop" onClick={() => { if (!connected) onCancel() }}>
      <div className="modal-card" style={{ maxWidth: '560px' }} onClick={(e) => e.stopPropagation()}>
        <div className="modal-title">{isPF ? '端口转发 (kubectl port-forward 等价)' : '文件互拷 (kubectl cp 等价)'}</div>

        {!connected && (
          <div className="k8s-stream-form" style={{ display: 'grid', gap: '.5rem', marginTop: '.5rem' }}>
            {isPF ? (
              <>
                <label className="dim" style={{ fontSize: '.75rem' }}>端口映射 (local:remote, 多组逗号分隔):
                  <input className="input" value={String(values.ports ?? '')} onChange={(e) => setParam('ports', e.target.value)}
                    placeholder="8080:80" style={{ width: '100%' }} />
                </label>
                <label className="dim" style={{ fontSize: '.75rem' }}>监听地址:
                  <input className="input" value={String(values.address ?? '')} onChange={(e) => setParam('address', e.target.value)}
                    placeholder="127.0.0.1" style={{ width: '100%' }} />
                </label>
              </>
            ) : (
              <>
                <label className="dim" style={{ fontSize: '.75rem' }}>方向:
                  <select className="input" value={String(values.direction ?? 'from-pod')}
                    onChange={(e) => setParam('direction', e.target.value)} style={{ width: '100%' }}>
                    <option value="from-pod">下载 (pod → 本机)</option>
                    <option value="to-pod">上传 (本机 → pod)</option>
                  </select>
                </label>
                <label className="dim" style={{ fontSize: '.75rem' }}>容器:
                  <input className="input" value={String(values.container ?? '')} onChange={(e) => setParam('container', e.target.value)}
                    placeholder="默认第一个容器" style={{ width: '100%' }} />
                </label>
                <label className="dim" style={{ fontSize: '.75rem' }}>
                  {isToPod ? '目标目录 (解包到该目录 /):' : 'Pod 内路径:'}
                  <input className="input" value={String(values.podPath ?? '')} onChange={(e) => setParam('podPath', e.target.value)}
                    placeholder={isToPod ? '/data/uploads' : '/app/config.yaml'} style={{ width: '100%' }} />
                </label>
                {isToPod && (
                  <>
                    <label className="dim" style={{ fontSize: '.75rem' }}>本地文件:
                      <input ref={fileRef} type="file" className="input" style={{ width: '100%' }} />
                    </label>
                    <label className="dim" style={{ fontSize: '.75rem' }}>落盘文件名 (baseName):
                      <input className="input" value={String(values.baseName ?? '')} onChange={(e) => setParam('baseName', e.target.value)}
                        placeholder="无则用本机文件名" style={{ width: '100%' }} />
                    </label>
                  </>
                )}
              </>
            )}
          </div>
        )}

        {connected && (
          <div className="k8s-stream-log" style={{ marginTop: '.5rem' }}>
            <pre className="k8s-log-pane" style={{ background: '#0d1117', color: '#c9d1d9', fontSize: '.75rem', padding: '.5rem', borderRadius: 6, maxHeight: '220px', overflow: 'auto' }}>
              {lines.length ? lines.join('\n') : '[等待输出…]'}
              {(isPF ? recvBytes : (isToPod ? sentBytes : recvBytes)) > 0 &&
                `\n[数据 ${(isPF ? recvBytes : (isToPod ? sentBytes : recvBytes))} 字节]`}
            </pre>
            {!isPF && !isToPod && recvBytes > 0 && !exited && (
              <button className="btn-glass-soft btn-glass-soft-accent" onClick={downloadTar}
                style={{ marginTop: '.5rem' }}>保存为 tar 文件 ({recvBytes} 字节)</button>
            )}
            {isToPod && sentBytes > 0 && !exited && <div className="dim" style={{ fontSize: '.75rem', marginTop: '.5rem' }}>[上传中/等待解包…]</div>}
          </div>
        )}

        {err && <div className="dim" style={{ color: '#e5534b', fontSize: '.75rem', marginTop: '.5rem' }}>{err}</div>}

        <div className="btn-row" style={{ marginTop: '1rem' }}>
          <span className="dim" style={{ fontSize: '.75rem' }}>cluster: {cluster} / {ns}/{name}</span>
          <button className="btn-glass-soft" onClick={() => { close(); onCancel() }}>关闭</button>
          {connected ? (
            <button className="btn-glass-soft" onClick={stop}>{exited ? '关闭' : '停止'}</button>
          ) : (
            <button className="btn-glass-soft btn-glass-soft-accent" onClick={connect}
              disabled={isToPod && !values.baseName && !fileRef.current?.files?.[0]}>连接</button>
          )}
        </div>
      </div>
    </div>
  )
}
