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
  res, name, ns, cluster, onMsg, filterCategory, exclude, variant = 'inline', onClose,
}: K8sActionPanelProps) {
  const [catalog, setCatalog] = useState<ActionSpec[] | null>(null)
  const [err, setErr] = useState<string | null>(null)
  const [running, setRunning] = useState<string | null>(null)
  const [openAction, setOpenAction] = useState<ActionSpec | null>(null)
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
      if (a.ok) setCatalog(a.actions)
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
                {running === a.name ? '…' : a.label}
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
    </div>
  )
}

// 单 action 弹窗
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
    }
    return v
  })
  const fields: FormField[] = action.params.map((p) => ({
    name: p.name, label: p.label, type: p.type, required: !!p.required,
    help: p.help, options: p.options, min: p.min, max: p.max, pattern: p.pattern,
  }))
  return (
    <div className="modal-overlay" onClick={onCancel}>
      <div className="modal" onClick={(e) => e.stopPropagation()} style={{ maxWidth: 560 }}>
        <h3>{action.label} — {res}/{name}{ns ? ` @ ${ns}` : ''}</h3>
        {action.description && <p className="dim" style={{ marginTop: '0.25rem', fontSize: '0.8125rem' }}>{action.description}</p>}
        <div style={{ marginTop: '0.75rem' }}>
          <DynamicForm fields={fields} values={values} onChange={setValues} />
        </div>
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
