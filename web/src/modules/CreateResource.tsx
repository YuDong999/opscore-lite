// ── 可视化创建资源(页面级) ──
// 类型卡 → 必填常显 + 可选折叠 → 右栏 YAML 实时预览 ⇄ 手改模式 → apply
// 引用类字段分两档: 严格下拉(只从集群已有选) / 宽松 combobox(可输可选)。

import { useEffect, useMemo, useState } from 'react'
import yaml from 'js-yaml'
import { getJSON, postJSON } from '../api/client'

type Kv = { k: string; v: string }
type Toleration = { key: string; op: string; effect: string; seconds: string }
type SvcPort = { port: string; targetPort: string; protocol: string; nodePort: string }
type Volume = { type: 'configmap' | 'secret' | 'pvc'; name: string; mountPath: string; subPath: string; readOnly: boolean }
type EnvFrom = { type: 'configmap' | 'secret'; name: string }
type IngressPath = { path: string; pathType: string; svc: string; svcPort: string }
type IngressRule = { host: string; paths: IngressPath[] }

// 主题色线性图标(feather 风格, currentColor 跟随主题), 替代 emoji
function KindIcon({ kind, size = 13 }: { kind: string; size?: number }) {
  const paths: Record<string, any> = {
    Deployment: <><polygon points="12 2 2 7 12 12 22 7 12 2" /><polyline points="2 12 12 17 22 12" /><polyline points="2 17 12 22 22 17" /></>,
    StatefulSet: <><ellipse cx="12" cy="5" rx="9" ry="3" /><path d="M21 12c0 1.66-4 3-9 3s-9-1.34-9-3" /><path d="M3 5v14c0 1.66 4 3 9 3s9-1.34 9-3V5" /></>,
    Service: <><circle cx="18" cy="5" r="3" /><circle cx="6" cy="12" r="3" /><circle cx="18" cy="19" r="3" /><line x1="8.59" y1="13.51" x2="15.42" y2="17.49" /><line x1="15.41" y1="6.51" x2="8.59" y2="10.49" /></>,
    Ingress: <><path d="M15 3h4a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2h-4" /><polyline points="10 17 15 12 10 7" /><line x1="15" y1="12" x2="3" y2="12" /></>,
    ConfigMap: <><line x1="4" y1="21" x2="4" y2="14" /><line x1="4" y1="10" x2="4" y2="3" /><line x1="12" y1="21" x2="12" y2="12" /><line x1="12" y1="8" x2="12" y2="3" /><line x1="20" y1="21" x2="20" y2="16" /><line x1="20" y1="12" x2="20" y2="3" /><line x1="1" y1="14" x2="7" y2="14" /><line x1="9" y1="8" x2="15" y2="8" /><line x1="17" y1="16" x2="23" y2="16" /></>,
    Secret: <><rect x="3" y="11" width="18" height="11" rx="2" ry="2" /><path d="M7 11V7a5 5 0 0 1 10 0v4" /></>,
    CronJob: <><circle cx="12" cy="12" r="10" /><polyline points="12 6 12 12 16 14" /></>,
    PVC: <><line x1="22" y1="12" x2="2" y2="12" /><path d="M5.45 5.11L2 12v6a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2v-6l-3.45-6.89A2 2 0 0 0 16.76 4H7.24a2 2 0 0 0-1.79 1.11z" /><line x1="6" y1="16" x2="6.01" y2="16" /><line x1="10" y1="16" x2="10.01" y2="16" /></>,
  }
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"
      strokeLinecap="round" strokeLinejoin="round"
      style={{ flexShrink: 0, verticalAlign: '-0.15em', marginRight: 4 }}>
      {paths[kind] || null}
    </svg>
  )
}

const KINDS = [
  { k: 'Deployment', res: 'deployments', desc: '无状态应用' },
  { k: 'StatefulSet', res: 'statefulsets', desc: '有状态应用' },
  { k: 'Service', res: 'services', desc: '服务暴露' },
  { k: 'Ingress', res: 'ingresses', desc: '七层路由 · host/path → Service' },
  { k: 'ConfigMap', res: 'configmaps', desc: '配置 k/v' },
  { k: 'Secret', res: 'secrets', desc: '敏感数据' },
  { k: 'CronJob', res: 'cronjobs', desc: '定时任务' },
  { k: 'PVC', res: 'persistentvolumeclaims', desc: '存储申请' },
]

const AM = [['ReadWriteOnce', 'RWO'], ['ReadOnlyMany', 'ROX'], ['ReadWriteMany', 'RWX']] as const

type Model = {
  kind: string; name: string; ns: string
  image: string; replicas: string; cpuReq: string; cpuLim: string; memReq: string; memLim: string
  containerPort: string
  env: Kv[]; envFrom: EnvFrom[]
  volumes: Volume[]
  useProbe: boolean; probePath: string; probePort: string; probeDelay: string; probeInterval: string
  nodeSelector: Kv[]; antiAffinity: boolean; tolerations: Toleration[]
  extraLabels: Kv[]; annotations: Kv[]
  svcType: string; svcSelector: Kv[]; svcPorts: SvcPort[]
  ingressClass: string; ingressRules: IngressRule[]; tlsSecret: string
  dataItems: Kv[]; secretType: string
  schedule: string; command: string
  storage: string; storageClass: string; accessModes: string[]
}

const blank = (kind: string): Model => ({
  kind, name: '', ns: 'default',
  image: '', replicas: '1', cpuReq: '', cpuLim: '', memReq: '', memLim: '', containerPort: '',
  env: [], envFrom: [], volumes: [],
  useProbe: false, probePath: '/healthz', probePort: '80', probeDelay: '10', probeInterval: '10',
  nodeSelector: [], antiAffinity: false, tolerations: [],
  extraLabels: [], annotations: [],
  svcType: 'ClusterIP', svcSelector: [{ k: 'app', v: '' }], svcPorts: [{ port: '80', targetPort: '80', protocol: 'TCP', nodePort: '' }],
  ingressClass: '', ingressRules: [{ host: '', paths: [{ path: '/', pathType: 'Prefix', svc: '', svcPort: '80' }] }], tlsSecret: '',
  dataItems: [], secretType: 'Opaque',
  schedule: '*/10 * * * *', command: '',
  storage: '5Gi', storageClass: '', accessModes: ['ReadWriteOnce'],
})

// ── 集群已有资源选项(严格下拉数据源) ──
type Opts = { scs: string[]; ics: string[]; nodes: string[]; cms: string[]; secrets: string[]; svcs: string[] }
const EMPTY_OPTS: Opts = { scs: [], ics: [], nodes: [], cms: [], secrets: [], svcs: [] }

function useClusterOptions(cluster: string, ns: string): Opts {
  const [o, setO] = useState<Opts>(EMPTY_OPTS)
  useEffect(() => {
    if (!cluster) return
    const names = (d: any) => (d.rows || []).map((r: any) => String(r.name)).filter(Boolean)
    getJSON(`/api/plugins/containers/k8s/resources?cluster=${cluster}&res=storageclasses`).then((d) => setO((x) => ({ ...x, scs: names(d) }))).catch(() => {})
    getJSON(`/api/plugins/containers/k8s/resources?cluster=${cluster}&res=ingressclasses`).then((d) => setO((x) => ({ ...x, ics: names(d) }))).catch(() => {})
    getJSON(`/api/plugins/containers/k8s/resources?cluster=${cluster}&res=nodes`).then((d) => setO((x) => ({ ...x, nodes: names(d) }))).catch(() => {})
  }, [cluster])
  useEffect(() => {
    if (!cluster) return
    const names = (d: any) => (d.rows || []).map((r: any) => String(r.name)).filter(Boolean)
    getJSON(`/api/plugins/containers/k8s/resources?cluster=${cluster}&res=configmaps&ns=${encodeURIComponent(ns)}`).then((d) => setO((x) => ({ ...x, cms: names(d) }))).catch(() => {})
    getJSON(`/api/plugins/containers/k8s/resources?cluster=${cluster}&res=secrets&ns=${encodeURIComponent(ns)}`).then((d) => setO((x) => ({ ...x, secrets: names(d) }))).catch(() => {})
    getJSON(`/api/plugins/containers/k8s/resources?cluster=${cluster}&res=services&ns=${encodeURIComponent(ns)}`).then((d) => setO((x) => ({ ...x, svcs: names(d) }))).catch(() => {})
  }, [cluster, ns])
  return o
}

// ── 表单 → K8s 对象 ──
function buildManifest(m: Model): Record<string, any> | null {
  if (!m.name.trim()) return null
  const name = m.name.trim()
  const extra = Object.fromEntries(m.extraLabels.filter((x) => x.k).map((x) => [x.k, x.v]))
  const annos = Object.fromEntries(m.annotations.filter((x) => x.k).map((x) => [x.k, x.v]))
  const meta: any = { name, namespace: m.ns || 'default' }
  if (Object.keys(extra).length) meta.labels = extra
  if (Object.keys(annos).length) meta.annotations = annos

  const podSpec = (): any => {
    const c: any = { name: 'main', image: m.image || 'nginx:alpine', imagePullPolicy: 'IfNotPresent' }
    if (m.containerPort) c.ports = [{ containerPort: Number(m.containerPort), protocol: 'TCP' }]
    if (m.env.length) c.env = m.env.filter((e) => e.k).map((e) => ({ name: e.k, value: e.v }))
    if (m.envFrom.length) c.envFrom = m.envFrom.filter((e) => e.name).map((e) =>
      e.type === 'secret' ? { secretRef: { name: e.name } } : { configMapRef: { name: e.name } })
    const req: any = {}, lim: any = {}
    if (m.cpuReq) req.cpu = m.cpuReq
    if (m.memReq) req.memory = m.memReq
    if (m.cpuLim) lim.cpu = m.cpuLim
    if (m.memLim) lim.memory = m.memLim
    if (Object.keys(req).length || Object.keys(lim).length)
      c.resources = { ...(Object.keys(req).length ? { requests: req } : {}), ...(Object.keys(lim).length ? { limits: lim } : {}) }
    if (m.useProbe) {
      const probe = {
        httpGet: { path: m.probePath || '/healthz', port: Number(m.probePort) || 80, scheme: 'HTTP' },
        periodSeconds: Number(m.probeInterval) || 10,
      }
      c.readinessProbe = { ...probe, initialDelaySeconds: 5 }
      c.livenessProbe = { ...probe, initialDelaySeconds: Number(m.probeDelay) || 10 }
    }
    if (m.volumes.length) {
      c.volumeMounts = m.volumes.filter((v) => v.mountPath).map((v, i) => ({
        name: `vol-${i + 1}`, mountPath: v.mountPath, readOnly: v.readOnly || undefined,
        ...(v.subPath ? { subPath: v.subPath } : {}),
      }))
    }
    const ps: any = { containers: [c] }
    if (m.volumes.length) {
      ps.volumes = m.volumes.map((v, i) => ({
        name: `vol-${i + 1}`,
        ...(v.type === 'configmap' ? { configMap: { name: v.name } } :
          v.type === 'secret' ? { secret: { secretName: v.name } } :
            { persistentVolumeClaim: { claimName: v.name } }),
      }))
    }
    if (m.nodeSelector.length) ps.nodeSelector = Object.fromEntries(m.nodeSelector.filter((x) => x.k).map((x) => [x.k, x.v]))
    if (m.antiAffinity) {
      ps.affinity = {
        podAntiAffinity: {
          preferredDuringSchedulingIgnoredDuringExecution: [{
            weight: 100,
            podAffinityTerm: { topologyKey: 'kubernetes.io/hostname', labelSelector: { matchLabels: { app: name } } },
          }],
        },
      }
    }
    if (m.tolerations.length) {
      ps.tolerations = m.tolerations.filter((t) => t.key).map((t) => ({
        key: t.key, operator: t.op || 'Equal',
        ...(t.effect ? { effect: t.effect } : {}),
        ...(t.seconds ? { tolerationSeconds: Number(t.seconds) } : {}),
      }))
    }
    return ps
  }

  const podTemplate = { metadata: { labels: { app: name, ...(Object.keys(extra).length ? extra : {}) } }, spec: podSpec() }
  const out: any = { apiVersion: 'v1', kind: m.kind, metadata: meta }
  switch (m.kind) {
    case 'Deployment':
      out.apiVersion = 'apps/v1'
      out.spec = { replicas: Number(m.replicas) || 1, selector: { matchLabels: { app: name } }, template: podTemplate }
      break
    case 'StatefulSet':
      out.apiVersion = 'apps/v1'
      out.spec = { serviceName: `${name}-headless`, replicas: Number(m.replicas) || 1, selector: { matchLabels: { app: name } }, template: podTemplate }
      break
    case 'Service':
      out.spec = {
        type: m.svcType,
        selector: Object.fromEntries(m.svcSelector.filter((x) => x.k).map((x) => [x.k, x.v])),
        ports: m.svcPorts.filter((p) => p.port).map((p) => ({
          port: Number(p.port), targetPort: Number(p.targetPort) || Number(p.port), protocol: p.protocol || 'TCP',
          ...(m.svcType === 'NodePort' && p.nodePort ? { nodePort: Number(p.nodePort) } : {}),
        })),
      }
      if (!out.spec.ports.length) out.spec.ports = [{ port: 80, targetPort: 80, protocol: 'TCP' }]
      break
    case 'Ingress': {
      out.apiVersion = 'networking.k8s.io/v1'
      const rules = m.ingressRules
        .filter((r) => r.host || r.paths.some((p) => p.svc))
        .map((r) => ({
          ...(r.host ? { host: r.host } : {}),
          http: {
            paths: r.paths.filter((p) => p.svc).map((p) => ({
              path: p.path || '/', pathType: p.pathType || 'Prefix',
              backend: { service: { name: p.svc, port: { number: Number(p.svcPort) || 80 } } },
            })),
          },
        }))
      const spec: any = {}
      if (m.ingressClass) spec.ingressClassName = m.ingressClass
      if (m.tlsSecret) spec.tls = [{ ...(m.ingressRules[0]?.host ? { hosts: [m.ingressRules[0].host] } : {}), secretName: m.tlsSecret }]
      if (rules.length) spec.rules = rules
      out.spec = spec
      break
    }
    case 'ConfigMap':
      delete out.spec
      out.data = Object.fromEntries(m.dataItems.filter((x) => x.k).map((x) => [x.k, x.v]))
      break
    case 'Secret':
      delete out.spec
      out.type = m.secretType || 'Opaque'
      out[!m.secretType || m.secretType === 'Opaque' ? 'stringData' : 'data'] =
        Object.fromEntries(m.dataItems.filter((x) => x.k).map((x) => [x.k, x.v]))
      break
    case 'CronJob':
      out.apiVersion = 'batch/v1'
      out.spec = {
        schedule: m.schedule || '*/10 * * * *',
        ...(m.suspend ? { suspend: true } : {}),
        jobTemplate: { spec: { template: { spec: {
          restartPolicy: 'OnFailure',
          containers: [{ name: 'main', image: m.image || 'busybox:1.36', ...(m.command ? { args: ['/bin/sh', '-c', m.command] } : {}) }],
        } } } } }
      break
    case 'PVC':
      out.spec = {
        accessModes: m.accessModes.length ? m.accessModes : ['ReadWriteOnce'],
        ...(m.storageClass ? { storageClassName: m.storageClass } : {}),
        resources: { requests: { storage: m.storage || '5Gi' } },
      }
      break
  }
  return out
}

// ── 控件 ──
const IN = 'input'

function F({ label, children, hint }: { label: string; children: any; hint?: string }) {
  return (
    <label style={{ display: 'flex', flexDirection: 'column', gap: 4, fontSize: '0.75rem', color: 'var(--text-dim)', minWidth: 0 }}>
      <span>{label}{hint && <i style={{ fontStyle: 'normal', opacity: 0.7 }}> · {hint}</i>}</span>
      {children}
    </label>
  )
}

function Grid({ cols = 3, children }: { cols?: number; children: any }) {
  return <div style={{ display: 'grid', gridTemplateColumns: `repeat(${cols},minmax(0,1fr))`, gap: '0.5rem' }}>{children}</div>
}

function KvRows({ items, onChange, kHint, vHint, vOptions }: { items: Kv[]; onChange: (x: Kv[]) => void; kHint: string; vHint: string; vOptions?: string[] }) {
  const lid = useMemo(() => 'dl' + Math.random().toString(36).slice(2, 8), [])
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 4, gridColumn: '1 / -1' }}>
      {items.map((it, i) => (
        <div key={i} style={{ display: 'flex', gap: 4 }}>
          <input className={IN} value={it.k} placeholder={kHint} onChange={(e) => { const n = [...items]; n[i] = { ...it, k: e.target.value }; onChange(n) }} />
          <>
            <input className={IN} list={vOptions ? lid : undefined} value={it.v} placeholder={vHint}
              onChange={(e) => { const n = [...items]; n[i] = { ...it, v: e.target.value }; onChange(n) }} />
            {vOptions && <datalist id={lid}>{vOptions.map((v) => <option key={v} value={v} />)}</datalist>}
          </>
          <button className="btn btn-sm k8s-del-cluster" style={{ width: '2rem', height: '2rem', opacity: 1 }} onClick={() => onChange(items.filter((_, j) => j !== i))}>✕</button>
        </div>
      ))}
      <button className="btn btn-sm" style={{ alignSelf: 'flex-start' }} onClick={() => onChange([...items, { k: '', v: '' }])}>+ 添加</button>
    </div>
  )
}

// 严格下拉: 只能从集群已有资源中选
function Strict({ label, value, onChange, options, emptyHint, allowEmpty, emptyText }: {
  label: string; value: string; onChange: (v: string) => void; options: string[]
  emptyHint?: string; allowEmpty?: boolean; emptyText?: string
}) {
  return (
    <F label={label} hint={emptyHint}>
      <select className={`${IN} sel`} value={value} onChange={(e) => onChange(e.target.value)}>
        {(allowEmpty ?? true) && <option value="">{emptyText ?? '(不指定)'}</option>}
        {options.length === 0 && <option value="" disabled>{emptyHint ?? '集群中暂无可选项'}</option>}
        {options.map((o) => <option key={o} value={o}>{o}</option>)}
      </select>
    </F>
  )
}

function Section({ title, children, defaultOpen, badge }: { title: string; children: any; defaultOpen?: boolean; badge?: string }) {
  const [open, setOpen] = useState(!!defaultOpen)
  return (
    <div style={{ border: '1px solid var(--border)', borderRadius: 'var(--radius-sm)', padding: '0.5rem 0.75rem' }}>
      <div onClick={() => setOpen(!open)} style={{ cursor: 'pointer', display: 'flex', justifyContent: 'space-between', fontSize: '0.8125rem', fontWeight: 600, userSelect: 'none' }}>
        <span>{open ? '▾' : '▸'} {title}</span>
        {badge && <span className="dim" style={{ fontSize: '0.6875rem' }}>{badge}</span>}
      </div>
      {open && <div style={{ display: 'grid', gridTemplateColumns: 'repeat(3,minmax(0,1fr))', gap: '0.5rem', marginTop: '0.625rem' }}>{children}</div>}
    </div>
  )
}

const imgKey = 'cr-recent-images'
function loadRecentImages(): string[] {
  try { return JSON.parse(localStorage.getItem(imgKey) || '[]') } catch { return [] }
}

export default function CreateResource({ cluster, namespaces, initialKind, onCreated, onMsg }: {
  cluster: string
  namespaces: string[]
  initialKind?: string
  onCreated: (res: string, name: string) => void
  onMsg: (m: string) => void
}) {
  const [m, setM] = useState<Model>(blank(initialKind && KINDS.some((x) => x.k === initialKind) ? initialKind : 'Deployment'))
  const [mode, setMode] = useState<'form' | 'yaml'>('form')
  const [yamlText, setYamlText] = useState('')
  const [overwrite, setOverwrite] = useState(false)
  const [busy, setBusy] = useState(false)
  const [nsManual, setNsManual] = useState(false)
  const [nsFallback, setNsFallback] = useState<string[]>([])
  const opts = useClusterOptions(cluster, m.ns)
  useEffect(() => {
    if (!cluster || namespaces.length || nsFallback.length) return
    getJSON<{ rows: any[] }>(`/api/plugins/containers/k8s/resources?cluster=${cluster}&res=namespaces`)
      .then((d) => setNsFallback((d.rows || []).map((r: any) => r.name)))
      .catch(() => {})
  }, [cluster, namespaces.length, nsFallback.length])
  const nsList = namespaces.length ? namespaces : nsFallback
  const set = (patch: Partial<Model>) => setM((x) => ({ ...x, ...patch }))
  const recentImages = loadRecentImages()

  const generated = useMemo(() => {
    const obj = buildManifest(m)
    if (!obj) return ''
    try { return yaml.dump(obj, { lineWidth: -1, noRefs: true }) } catch { return '' }
  }, [m])

  useEffect(() => { if (mode === 'yaml' && !yamlText) setYamlText(generated) }, [mode])

  const submit = () => {
    const finalYaml = (mode === 'yaml' ? yamlText : generated).trim()
    if (!finalYaml) return
    setBusy(true)
    postJSON('/api/plugins/containers/k8s/apply', { cluster, yaml: finalYaml, overwrite })
      .then((d: any) => {
        setBusy(false)
        if (d.ok) {
          try {
            const arr = loadRecentImages().filter((x) => x !== m.image)
            if (m.image) { arr.unshift(m.image); localStorage.setItem(imgKey, JSON.stringify(arr.slice(0, 8))) }
          } catch { /* ignore */ }
          const res = KINDS.find((x) => x.k === d.kind)?.res
          onMsg(`✓ ${d.kind} ${d.name} ${d.created ? '创建成功' : '更新成功'}`)
          if (res) onCreated(res, String(d.name || ''))
        } else onMsg('✗ ' + (d.error || '创建失败'))
      })
      .catch((e) => { setBusy(false); onMsg('✗ ' + String(e)) })
  }

  const isWl = m.kind === 'Deployment' || m.kind === 'StatefulSet'
  const kindMeta = KINDS.find((x) => x.k === m.kind)

  return (
    <div className="cr-page">
      <div className="module-head" style={{ gridColumn: '1 / -1', flexWrap: 'wrap' }}>
        <h2 style={{ marginRight: 0 }}>创建资源</h2>
        <span className="pill pill-sub">骨架自动生成 · 引用类从集群已有选择</span>
      </div>

      <div style={{ gridColumn: '1 / -1', display: 'flex', gap: '0.5rem', flexWrap: 'wrap' }}>
        {KINDS.map((x) => (
          <button key={x.k} className={`btn btn-sm ${m.kind === x.k ? 'btn-accent' : ''}`} title={x.desc}
            onClick={() => { setM(blank(x.k)); setYamlText(''); setMode('form') }}>
            <KindIcon kind={x.k} /> {x.k}
          </button>
        ))}
      </div>

      {/* 左: 表单 */}
      <div className="card" style={{ minWidth: 0 }}>
        <div className="card-head"><h3><KindIcon kind={m.kind} size={15} />{m.kind}</h3><span className="card-sub">{kindMeta?.desc}</span></div>
        <div className="card-body" style={{ display: 'flex', flexDirection: 'column', gap: '0.625rem' }}>
          <Grid cols={2}>
            <F label="名称 *"><input className={IN} value={m.name} placeholder="如 my-nginx" onChange={(e) => set({ name: e.target.value })} /></F>
            <F label="命名空间 *" hint="从已有选择或手动输入">
              {!nsManual ? (
                <select className={`${IN} sel`} value={nsList.includes(m.ns) ? m.ns : '__manual__'}
                  onChange={(e) => { if (e.target.value === '__manual__') setNsManual(true); else set({ ns: e.target.value }) }}>
                  {nsList.length === 0 && <option value={m.ns || 'default'}>{m.ns || 'default'}</option>}
                  {!nsList.includes(m.ns) && <option value="__manual__">{m.ns || '(未设置)'} ✎ 改为手动输入</option>}
                  {nsList.map((n) => <option key={n} value={n}>{n}</option>)}
                  <option value="__manual__">✎ 手动输入新命名空间…</option>
                </select>
              ) : (
                <div style={{ display: 'flex', gap: 4 }}>
                  <input className={IN} autoFocus value={m.ns} placeholder="新命名空间(需已存在, 否则创建时报错)"
                    onChange={(e) => set({ ns: e.target.value })} />
                  <button className="btn btn-sm" title="返回选择列表" onClick={() => setNsManual(false)}>↺</button>
                </div>
              )}
            </F>
          </Grid>

          {isWl && (
            <>
              <Grid cols={2}>
                <F label="镜像 *">
                  <>
                    <input className={IN} list="cr-img" value={m.image} placeholder="nginx:1.27-alpine" onChange={(e) => set({ image: e.target.value })} />
                    <datalist id="cr-img">{recentImages.map((i) => <option key={i} value={i} />)}</datalist>
                  </>
                </F>
                <F label="副本数"><input className={IN} type="number" min={0} value={m.replicas} onChange={(e) => set({ replicas: e.target.value })} /></F>
              </Grid>
              <Grid cols={5}>
                <F label="容器端口"><input className={IN} type="number" value={m.containerPort} placeholder="80" onChange={(e) => set({ containerPort: e.target.value })} /></F>
                <F label="CPU 请求"><input className={IN} value={m.cpuReq} placeholder="100m" onChange={(e) => set({ cpuReq: e.target.value })} /></F>
                <F label="CPU 上限"><input className={IN} value={m.cpuLim} placeholder="500m" onChange={(e) => set({ cpuLim: e.target.value })} /></F>
                <F label="内存 请求"><input className={IN} value={m.memReq} placeholder="64Mi" onChange={(e) => set({ memReq: e.target.value })} /></F>
                <F label="内存 上限"><input className={IN} value={m.memLim} placeholder="128Mi" onChange={(e) => set({ memLim: e.target.value })} /></F>
              </Grid>
              <span className="dim" style={{ fontSize: '0.6875rem' }}>资源限制留空即不生成该字段</span>
            </>
          )}

          {m.kind === 'Service' && (
            <>
              <Grid cols={2}>
                <F label="类型">
                  <select className={`${IN} sel`} value={m.svcType} onChange={(e) => set({ svcType: e.target.value })}>
                    {['ClusterIP', 'NodePort', 'LoadBalancer'].map((t) => <option key={t}>{t}</option>)}
                  </select>
                </F>
              </Grid>
              <F label="Selector(匹配 Pod 标签)"><KvRows items={m.svcSelector} onChange={(x) => set({ svcSelector: x })} kHint="app" vHint="名称" /></F>
              <F label="端口映射">
                <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
                  {m.svcPorts.map((p, i) => (
                    <div key={i} style={{ display: 'grid', gridTemplateColumns: '1fr 1fr 1fr 1fr auto', gap: 4 }}>
                      <input className={IN} value={p.port} placeholder="port" onChange={(e) => { const n = [...m.svcPorts]; n[i] = { ...p, port: e.target.value }; set({ svcPorts: n }) }} />
                      <input className={IN} value={p.targetPort} placeholder="targetPort" onChange={(e) => { const n = [...m.svcPorts]; n[i] = { ...p, targetPort: e.target.value }; set({ svcPorts: n }) }} />
                      <select className={IN} value={p.protocol} onChange={(e) => { const n = [...m.svcPorts]; n[i] = { ...p, protocol: e.target.value }; set({ svcPorts: n }) }}><option>TCP</option><option>UDP</option></select>
                      <input className={IN} value={p.nodePort} placeholder="nodePort" disabled={m.svcType !== 'NodePort'} onChange={(e) => { const n = [...m.svcPorts]; n[i] = { ...p, nodePort: e.target.value }; set({ svcPorts: n }) }} />
                      <button className="btn btn-sm k8s-del-cluster" style={{ width: '2rem', height: '2rem', opacity: 1 }} onClick={() => set({ svcPorts: m.svcPorts.filter((_, j) => j !== i) })}>✕</button>
                    </div>
                  ))}
                  <button className="btn btn-sm" style={{ alignSelf: 'flex-start' }} onClick={() => set({ svcPorts: [...m.svcPorts, { port: '', targetPort: '', protocol: 'TCP', nodePort: '' }] })}>+ 端口</button>
                </div>
              </F>
            </>
          )}

          {m.kind === 'Ingress' && (
            <>
              <Strict label="IngressClass(网关控制器)" value={m.ingressClass} onChange={(v) => set({ ingressClass: v })}
                options={opts.ics} emptyHint="集群中暂无 IngressClass, 请先部署 Ingress Controller" />
              <F label="路由规则 (host + path → Service)">
                <div style={{ display: 'flex', flexDirection: 'column', gap: '0.5rem' }}>
                  {m.ingressRules.map((r, ri) => (
                    <div key={ri} style={{ border: '1px solid var(--border)', borderRadius: 'var(--radius-sm)', padding: '0.5rem', display: 'flex', flexDirection: 'column', gap: 4 }}>
                      <div style={{ display: 'flex', gap: 4, alignItems: 'center' }}>
                        <input className={IN} value={r.host} placeholder="域名, 如 app.example.com (留空=全host)"
                          onChange={(e) => { const n = [...m.ingressRules]; n[ri] = { ...r, host: e.target.value }; set({ ingressRules: n }) }} />
                        {m.ingressRules.length > 1 && <button className="btn btn-sm k8s-del-cluster" style={{ width: '2rem', height: '2rem', opacity: 1 }} onClick={() => set({ ingressRules: m.ingressRules.filter((_, j) => j !== ri) })}>✕</button>}
                      </div>
                      {r.paths.map((p, pi) => (
                        <div key={pi} style={{ display: 'grid', gridTemplateColumns: '1.2fr 1fr 1.6fr 0.8fr auto', gap: 4 }}>
                          <input className={IN} value={p.path} placeholder="/api" onChange={(e) => { const n = [...m.ingressRules]; const ps = [...r.paths]; ps[pi] = { ...p, path: e.target.value }; n[ri] = { ...r, paths: ps }; set({ ingressRules: n }) }} />
                          <select className={IN} value={p.pathType} onChange={(e) => { const n = [...m.ingressRules]; const ps = [...r.paths]; ps[pi] = { ...p, pathType: e.target.value }; n[ri] = { ...r, paths: ps }; set({ ingressRules: n }) }}>
                            <option>Prefix</option><option>Exact</option><option>ImplementationSpecific</option>
                          </select>
                          <select className={IN} value={p.svc} onChange={(e) => { const n = [...m.ingressRules]; const ps = [...r.paths]; ps[pi] = { ...p, svc: e.target.value }; n[ri] = { ...r, paths: ps }; set({ ingressRules: n }) }}>
                            <option value="" disabled>{opts.svcs.length ? '选择 Service' : '命名空间暂无 Service'}</option>
                            {opts.svcs.map((s) => <option key={s} value={s}>{s}</option>)}
                          </select>
                          <input className={IN} value={p.svcPort} placeholder="端口" onChange={(e) => { const n = [...m.ingressRules]; const ps = [...r.paths]; ps[pi] = { ...p, svcPort: e.target.value }; n[ri] = { ...r, paths: ps }; set({ ingressRules: n }) }} />
                          <button className="btn btn-sm k8s-del-cluster" style={{ width: '2rem', height: '2rem', opacity: 1 }} onClick={() => { const n = [...m.ingressRules]; n[ri] = { ...r, paths: r.paths.filter((_, j) => j !== pi) }; set({ ingressRules: n }) }}>✕</button>
                        </div>
                      ))}
                      <button className="btn btn-sm" style={{ alignSelf: 'flex-start' }}
                        onClick={() => { const n = [...m.ingressRules]; n[ri] = { ...r, paths: [...r.paths, { path: '/', pathType: 'Prefix', svc: '', svcPort: '80' }] }; set({ ingressRules: n }) }}>+ path</button>
                    </div>
                  ))}
                  <button className="btn btn-sm" style={{ alignSelf: 'flex-start' }} onClick={() => set({ ingressRules: [...m.ingressRules, { host: '', paths: [{ path: '/', pathType: 'Prefix', svc: '', svcPort: '80' }] }] })}>+ 路由规则</button>
                </div>
              </F>
              <Strict label="TLS 证书 Secret(引用已有)" value={m.tlsSecret} onChange={(v) => set({ tlsSecret: v })}
                options={opts.secrets} emptyText="(无 TLS)" emptyHint="命名空间中暂无 Secret" />
            </>
          )}

          {(m.kind === 'ConfigMap' || m.kind === 'Secret') && (
            <>
              {m.kind === 'Secret' && (
                <F label="类型">
                  <select className={`${IN} sel`} value={m.secretType} onChange={(e) => set({ secretType: e.target.value })}>
                    <option value="Opaque">Opaque(通用 k/v)</option>
                    <option value="kubernetes.io/tls">tls</option>
                    <option value="kubernetes.io/dockerconfigjson">dockerconfigjson</option>
                  </select>
                </F>
              )}
              <F label={m.kind === 'Secret' ? '数据(明文 → stringData)' : '数据 k/v'}>
                <KvRows items={m.dataItems} onChange={(x) => set({ dataItems: x })} kHint="key" vHint="value" />
              </F>
            </>
          )}

          {m.kind === 'CronJob' && (
            <>
              <Grid cols={2}>
                <F label="Schedule *"><input className={IN} value={m.schedule} onChange={(e) => set({ schedule: e.target.value })} /></F>
                <F label="镜像 *">
                  <>
                    <input className={IN} list="cr-img" value={m.image} placeholder="busybox:1.36" onChange={(e) => set({ image: e.target.value })} />
                    <datalist id="cr-img">{recentImages.map((i) => <option key={i} value={i} />)}</datalist>
                  </>
                </F>
              </Grid>
              <F label="Shell 命令"><input className={IN} value={m.command} placeholder="echo hello" onChange={(e) => set({ command: e.target.value })} /></F>
            </>
          )}

          {m.kind === 'PVC' && (
            <Grid cols={2}>
              <F label="容量 *"><input className={IN} value={m.storage} placeholder="5Gi" onChange={(e) => set({ storage: e.target.value })} /></F>
              <Strict label="StorageClass(存储类)" value={m.storageClass} onChange={(v) => set({ storageClass: v })}
                options={opts.scs} emptyText="(集群默认 SC)" emptyHint="集群中暂无 StorageClass" />
              <F label="访问模式">
                <div style={{ display: 'flex', gap: 8, alignItems: 'center', height: '2.2rem' }}>
                  {AM.map(([full, short]) => (
                    <label key={full} style={{ display: 'flex', gap: 2, fontSize: '0.75rem', cursor: 'pointer', color: 'var(--text)' }}>
                      <input type="checkbox" checked={m.accessModes.includes(full)}
                        onChange={(e) => set({ accessModes: e.target.checked ? [...m.accessModes, full] : m.accessModes.filter((x) => x !== full) })} />
                      {short}
                    </label>
                  ))}
                </div>
              </F>
            </Grid>
          )}

          {/* ── workload 可选区 ── */}
          {isWl && (
            <>
              <Section title="卷挂载" badge="引用已有 ConfigMap / Secret / PVC">
                <div style={{ gridColumn: '1 / -1', display: 'flex', flexDirection: 'column', gap: '0.5rem' }}>
                  {m.volumes.map((v, i) => (
                    <div key={i} style={{ display: 'grid', gridTemplateColumns: '1fr 1.4fr 1.4fr 1fr auto auto', gap: 4, alignItems: 'center' }}>
                      <select className={IN} value={v.type} onChange={(e) => { const n = [...m.volumes]; n[i] = { ...v, type: e.target.value, name: '' }; set({ volumes: n }) }}>
                        <option value="configmap">ConfigMap</option>
                        <option value="secret">Secret</option>
                        <option value="pvc">PVC</option>
                      </select>
                      <select className={IN} value={v.name}
                        onChange={(e) => { const n = [...m.volumes]; n[i] = { ...v, name: e.target.value }; set({ volumes: n }) }}>
                        <option value="" disabled>{v.type === 'configmap' ? (opts.cms.length ? '选择 ConfigMap' : '暂无 ConfigMap') : v.type === 'secret' ? (opts.secrets.length ? '选择 Secret' : '暂无 Secret') : (opts.scs.length ? '选择 PVC' : '暂无 PVC')}</option>
                        {(v.type === 'configmap' ? opts.cms : v.type === 'secret' ? opts.secrets : []).map((s) => <option key={s} value={s}>{s}</option>)}
                      </select>
                      <input className={IN} value={v.mountPath} placeholder="挂载路径 /app/conf" onChange={(e) => { const n = [...m.volumes]; n[i] = { ...v, mountPath: e.target.value }; set({ volumes: n }) }} />
                      <input className={IN} value={v.subPath} placeholder="subPath(可选)" onChange={(e) => { const n = [...m.volumes]; n[i] = { ...v, subPath: e.target.value }; set({ volumes: n }) }} />
                      <label style={{ display: 'flex', gap: 2, fontSize: '0.7rem', alignItems: 'center', color: 'var(--text)' }}>
                        <input type="checkbox" checked={v.readOnly} onChange={(e) => { const n = [...m.volumes]; n[i] = { ...v, readOnly: e.target.checked }; set({ volumes: n }) }} />只读
                      </label>
                      <button className="btn btn-sm k8s-del-cluster" style={{ width: '2rem', height: '2rem', opacity: 1 }} onClick={() => set({ volumes: m.volumes.filter((_, j) => j !== i) })}>✕</button>
                    </div>
                  ))}
                  <button className="btn btn-sm" style={{ alignSelf: 'flex-start' }} onClick={() => set({ volumes: [...m.volumes, { type: 'configmap', name: '', mountPath: '', subPath: '', readOnly: true }] })}>+ 挂载卷</button>
                </div>
              </Section>
              <Section title="环境变量">
                <F label="envFrom(整批注入)" hint="从已有 ConfigMap/Secret">
                  <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
                    {m.envFrom.map((e, i) => (
                      <div key={i} style={{ display: 'grid', gridTemplateColumns: '1fr 2fr auto', gap: 4 }}>
                        <select className={IN} value={e.type} onChange={(ev) => { const n = [...m.envFrom]; n[i] = { ...e, type: ev.target.value, name: '' }; set({ envFrom: n }) }}>
                          <option value="configmap">ConfigMap</option><option value="secret">Secret</option>
                        </select>
                        <select className={IN} value={e.name} onChange={(ev) => { const n = [...m.envFrom]; n[i] = { ...e, name: ev.target.value }; set({ envFrom: n }) }}>
                          <option value="" disabled>{e.type === 'configmap' ? (opts.cms.length ? '选择' : '暂无') : (opts.secrets.length ? '选择' : '暂无')}</option>
                          {(e.type === 'configmap' ? opts.cms : opts.secrets).map((s) => <option key={s} value={s}>{s}</option>)}
                        </select>
                        <button className="btn btn-sm k8s-del-cluster" style={{ width: '2rem', height: '2rem', opacity: 1 }} onClick={() => set({ envFrom: m.envFrom.filter((_, j) => j !== i) })}>✕</button>
                      </div>
                    ))}
                    <button className="btn btn-sm" style={{ alignSelf: 'flex-start' }} onClick={() => set({ envFrom: [...m.envFrom, { type: 'configmap', name: '' }] })}>+ 引用</button>
                  </div>
                </F>
                <F label="逐条 k/v"><KvRows items={m.env} onChange={(x) => set({ env: x })} kHint="NAME" vHint="value" /></F>
              </Section>
              <Section title="健康探针 (HTTP)">
                <div style={{ display: 'flex', alignItems: 'center', gap: 6, gridColumn: '1 / -1' }}>
                  <input type="checkbox" id="cr-probe" checked={m.useProbe} onChange={(e) => set({ useProbe: e.target.checked })} />
                  <label htmlFor="cr-probe" className="dim" style={{ fontSize: '0.75rem', cursor: 'pointer' }}>生成 readiness + liveness</label>
                </div>
                {m.useProbe && (
                  <>
                    <F label="路径"><input className={IN} value={m.probePath} onChange={(e) => set({ probePath: e.target.value })} /></F>
                    <F label="端口"><input className={IN} value={m.probePort} onChange={(e) => set({ probePort: e.target.value })} /></F>
                    <F label="启动延迟(s)"><input className={IN} value={m.probeDelay} onChange={(e) => set({ probeDelay: e.target.value })} /></F>
                    <F label="间隔(s)"><input className={IN} value={m.probeInterval} onChange={(e) => set({ probeInterval: e.target.value })} /></F>
                  </>
                )}
              </Section>
              <Section title="调度 (亲和 / 污点)">
                <div style={{ gridColumn: '1 / -1', display: 'flex', gap: 12, alignItems: 'center', flexWrap: 'wrap' }}>
                  <label style={{ display: 'flex', gap: 4, fontSize: '0.75rem', alignItems: 'center', color: 'var(--text)' }}>
                    <input type="checkbox" checked={m.antiAffinity} onChange={(e) => set({ antiAffinity: e.target.checked })} />
                    副本尽量分散到不同节点(podAntiAffinity)
                  </label>
                </div>
                <F label="nodeSelector" hint="value 可选已有节点" wide>
                  <KvRows items={m.nodeSelector} onChange={(x) => set({ nodeSelector: x })} kHint="kubernetes.io/hostname" vHint="节点名" vOptions={opts.nodes} />
                </F>
                <F label="污点容忍" wide>
                  <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
                    {m.tolerations.map((t, i) => (
                      <div key={i} style={{ display: 'grid', gridTemplateColumns: '1.4fr 1fr 1.2fr 0.8fr auto', gap: 4 }}>
                        <input className={IN} value={t.key} placeholder="key" onChange={(e) => { const n = [...m.tolerations]; n[i] = { ...t, key: e.target.value }; set({ tolerations: n }) }} />
                        <select className={IN} value={t.op} onChange={(e) => { const n = [...m.tolerations]; n[i] = { ...t, op: e.target.value }; set({ tolerations: n }) }}><option>Equal</option><option>Exists</option></select>
                        <select className={IN} value={t.effect} onChange={(e) => { const n = [...m.tolerations]; n[i] = { ...t, effect: e.target.value }; set({ tolerations: n }) }}>
                          <option value="">任意</option><option>NoSchedule</option><option>PreferNoSchedule</option><option>NoExecute</option>
                        </select>
                        <input className={IN} value={t.seconds} placeholder="秒" onChange={(e) => { const n = [...m.tolerations]; n[i] = { ...t, seconds: e.target.value }; set({ tolerations: n }) }} />
                        <button className="btn btn-sm k8s-del-cluster" style={{ width: '2rem', height: '2rem', opacity: 1 }} onClick={() => set({ tolerations: m.tolerations.filter((_, j) => j !== i) })}>✕</button>
                      </div>
                    ))}
                    <button className="btn btn-sm" style={{ alignSelf: 'flex-start' }} onClick={() => set({ tolerations: [...m.tolerations, { key: '', op: 'Equal', effect: 'NoSchedule', seconds: '' }] })}>+ 容忍规则</button>
                  </div>
                </F>
              </Section>
              <Section title="标签与注解">
                <F label="labels"><KvRows items={m.extraLabels} onChange={(x) => set({ extraLabels: x })} kHint="key" vHint="value" /></F>
                <F label="annotations"><KvRows items={m.annotations} onChange={(x) => set({ annotations: x })} kHint="key" vHint="value" /></F>
              </Section>
            </>
          )}
        </div>
      </div>

      {/* 右: YAML 预览/编辑 */}
      <div className="card" style={{ minWidth: 0, display: 'flex', flexDirection: 'column' }}>
        <div className="card-head">
          <h3>YAML</h3>
          <div style={{ display: 'flex', gap: '0.375rem' }}>
            <button className={`btn btn-sm ${mode === 'form' ? 'btn-accent' : ''}`} onClick={() => { setMode('form'); setYamlText('') }}>表单生成</button>
            <button className={`btn btn-sm ${mode === 'yaml' ? 'btn-accent' : ''}`} onClick={() => setMode('yaml')}>手改模式</button>
          </div>
        </div>
        <div className="card-body" style={{ flex: 1, display: 'flex', flexDirection: 'column', minHeight: 0 }}>
          <textarea className="input mono" value={mode === 'yaml' ? yamlText : (generated || '# 填写名称后自动生成骨架')}
            readOnly={mode === 'form'}
            onChange={(e) => setYamlText(e.target.value)}
            style={{ flex: 1, resize: 'none', fontSize: '0.6875rem', lineHeight: 1.55, minHeight: 380, fontFamily: 'ui-monospace, monospace' }} />
          <span className="dim" style={{ fontSize: '0.6875rem', marginTop: 6 }}>
            {mode === 'form' ? '右侧随表单实时生成' : '以当前内容为最终提交'}
          </span>
        </div>
      </div>

      <div style={{ gridColumn: '1 / -1', display: 'flex', gap: '0.625rem', alignItems: 'center' }}>
        <label style={{ display: 'flex', alignItems: 'center', gap: 4, fontSize: '0.75rem', color: 'var(--text-dim)', marginRight: 'auto' }}>
          <input type="checkbox" checked={overwrite} onChange={(e) => setOverwrite(e.target.checked)} />
          同名资源存在时覆盖更新
        </label>
        <button className="btn-glass is-accent" disabled={busy || (!generated && !yamlText.trim())} onClick={submit}>
          {busy ? '提交中…' : `创建 ${m.kind}`}
        </button>
      </div>
    </div>
  )
}
