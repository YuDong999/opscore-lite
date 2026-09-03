// ── CI/CD 流水线模块: 流水线编排(阶段/步骤) / 运行历史 / Webhook·定时·手动触发 / SSE 实时日志 ──
//    v2: 代码仓库 / 镜像仓库 / 凭据中心 / 脚本库 / 发布模板 / 环节进度条

import { useCallback, useEffect, useRef, useState } from 'react'
import { getJSON, postJSON } from '../api/client'
import Card from '../components/Card'

// ── 类型(与 internal/cicd 模型对应) ──
interface Var { name: string; value: string; secret: boolean }
interface Trigger { manual: boolean; webhook: boolean; secret: string; cron: string }
interface Step { name: string; command: string; continueOnFail: boolean; timeoutMin: number; artifacts?: string[]; pullArtifact?: string }
interface Stage { name: string; host: string; workspace: string; approval: boolean; steps: Step[] }
interface Source { repoId: string; branch: string }
interface Pipeline {
  id: string; name: string; description: string
  env: Var[]; trigger: Trigger; stages: Stage[]
  source: Source; registryId: string; kubeCredId: string
  timeoutMin: number; maxRuns: number; notifyURL: string
}
interface PipelineView extends Pipeline {
  stageCount: number
  lastRun?: Run
}
interface Artifact { step: string; file: string; size: number; paths: string }
interface StepRun { name: string; command: string; status: string; exitCode: number; durationMs: number; artifacts?: Artifact[] }
interface StageRun { name: string; host: string; workspace: string; status: string; steps: StepRun[] }
interface Run {
  id: string; pipelineId: string; pipeline: string; trigger: string; status: string
  canceling?: boolean; progress: number; stages: StageRun[]; startedAt?: string; finishedAt?: string
  durationMs: number; error?: string
}
interface HostOpt { id: string; label: string }
interface Credential { id: string; name: string; type: string; username?: string; server?: string; hasData: boolean; note?: string; updatedAt: string }
interface Repo { id: string; name: string; url: string; credId: string; defaultBranch: string; note?: string }
interface Registry { id: string; name: string; server: string; credId: string; note?: string }
interface Script { id: string; name: string; description: string; content: string; updatedAt: string }

const CRED_TYPE_TEXT: Record<string, string> = {
  git: '代码库', registry: '镜像仓库', kubeconfig: 'K8s 配置', generic: '通用密文',
}

// 发布模板步骤: 一键插入常见发布动作(命令引用注入的环境变量)
const STEP_TEMPLATES: { name: string; steps: Step[] }[] = [
  {
    name: 'Docker 构建+推送镜像', steps: [
      { name: '登录镜像仓库', command: 'docker login $REGISTRY -u "$REGISTRY_USER" -p "$REGISTRY_PASS"', continueOnFail: false, timeoutMin: 0 },
      { name: '构建镜像', command: 'docker build -t $REGISTRY/myapp:${BUILD_NUMBER} .', continueOnFail: false, timeoutMin: 0 },
      { name: '推送镜像', command: 'docker push $REGISTRY/myapp:${BUILD_NUMBER}', continueOnFail: false, timeoutMin: 0 },
    ],
  },
  {
    name: 'Docker 主机发布', steps: [
      { name: '拉取镜像', command: 'docker pull $REGISTRY/myapp:${BUILD_NUMBER}', continueOnFail: false, timeoutMin: 0 },
      { name: '重建容器', command: 'docker rm -f myapp 2>/dev/null; docker run -d --name myapp --restart unless-stopped -p 8080:8080 $REGISTRY/myapp:${BUILD_NUMBER}', continueOnFail: false, timeoutMin: 0 },
    ],
  },
  {
    name: 'K8s 发布(apply+滚动状态)', steps: [
      { name: '应用清单', command: 'kubectl apply -f k8s/', continueOnFail: false, timeoutMin: 0 },
      { name: '等待滚动完成', command: 'kubectl rollout status deploy/myapp --timeout=180s', continueOnFail: false, timeoutMin: 0 },
    ],
  },
  {
    name: '裸机发布(备份+重启)', steps: [
      { name: '备份旧版本', command: 'tar czf backup-$(date +%Y%m%d%H%M%S).tgz -C . . --exclude=.git --exclude="backup-*.tgz" || true', continueOnFail: true, timeoutMin: 0 },
      { name: '重启服务', command: 'systemctl restart myapp && systemctl is-active myapp', continueOnFail: false, timeoutMin: 0 },
    ],
  },
]

// ── 状态徽标与文案 ──
const STATUS_TEXT: Record<string, string> = {
  queued: '排队中', running: '运行中', waiting: '等待审批', success: '成功', failed: '失败',
  canceled: '已取消', skipped: '已跳过', pending: '等待',
}
const STATUS_CLS: Record<string, string> = {
  queued: 'badge-warn', running: 'badge-info', waiting: 'badge-warn', success: 'badge-ok',
  failed: 'badge-danger', canceled: 'badge-off', skipped: 'badge-off', pending: 'badge-off',
}
const TRIGGER_TEXT: Record<string, string> = { manual: '手动', webhook: 'Webhook', cron: '定时' }

function badgeCls(s: string) { return STATUS_CLS[s] || 'badge-off' }
function badgeText(s: string) { return STATUS_TEXT[s] || s }

function fmtDur(ms: number): string {
  if (!ms) return '-'
  if (ms < 1000) return `${ms}ms`
  if (ms < 60000) return `${(ms / 1000).toFixed(1)}s`
  const m = Math.floor(ms / 60000), s = Math.round((ms % 60000) / 1000)
  return `${m}m${s}s`
}
function fmtTime(t?: string) { return t ? new Date(t).toLocaleString() : '-' }
function fmtSize(n: number): string {
  if (n >= 1 << 20) return `${(n / (1 << 20)).toFixed(1)}MB`
  if (n >= 1 << 10) return `${(n / (1 << 10)).toFixed(1)}KB`
  return `${n}B`
}

// 进度条(usage-bar/usage-fill 为项目既有样式)
function ProgressBar({ value, status }: { value: number; status: string }) {
  const color = status === 'failed' ? 'bg-danger' : status === 'success' ? 'bg-ok' : 'bg-warn'
  return (
    <div className="usage-bar" title={`${value}%`}>
      <span className={`usage-fill ${color}`} style={{ width: `${value}%` }} />
    </div>
  )
}

const emptyPipeline = (): Pipeline => ({
  id: '', name: '', description: '',
  env: [], trigger: { manual: true, webhook: false, secret: '', cron: '' },
  stages: [{ name: '构建', host: '', workspace: '', approval: false, steps: [{ name: '示例步骤', command: 'echo hello', continueOnFail: false, timeoutMin: 0 }] }],
  source: { repoId: '', branch: '' }, registryId: '', kubeCredId: '',
  timeoutMin: 0, maxRuns: 50, notifyURL: '',
})

export default function CicdModule() {
  const [tab, setTab] = useState('pipelines')
  const [overview, setOverview] = useState<any>(null)

  const loadOverview = useCallback(() => {
    getJSON('/api/cicd/overview').then(setOverview).catch(() => {})
  }, [])

  useEffect(() => {
    loadOverview()
    const t = setInterval(loadOverview, 5000)
    return () => clearInterval(t)
  }, [loadOverview, tab])

  return (
    <div className="module">
      <div className="module-head">
        <div className="module-head-row"><h2>CI/CD 流水线</h2></div>
        <span className="pill">
          {overview ? `运行中 ${overview.running} · 排队 ${overview.queued}` : '加载中...'}
        </span>
      </div>

      <div className="tabs">
        {[
          { id: 'pipelines', label: '流水线' },
          { id: 'runs', label: '运行历史' },
          { id: 'scripts', label: '脚本库' },
          { id: 'repos', label: '仓库' },
          { id: 'creds', label: '凭据' },
        ].map(t => (
          <button key={t.id} className={`tab ${tab === t.id ? 'tab-on' : ''}`} onClick={() => setTab(t.id)}>{t.label}</button>
        ))}
      </div>

      {tab === 'pipelines' && <PipelinesTab onChanged={loadOverview} />}
      {tab === 'runs' && <RunsTab onChanged={loadOverview} />}
      {tab === 'scripts' && <ScriptsTab />}
      {tab === 'repos' && <ReposTab />}
      {tab === 'creds' && <CredentialsTab />}
    </div>
  )
}

// ── 主机列表(本机 + Ansible 清单, 与 HostSelector 同源) ──
function useHosts(): HostOpt[] {
  const [hosts, setHosts] = useState<HostOpt[]>([{ id: '', label: '本机' }])
  useEffect(() => {
    getJSON<any[]>('/api/ansible/hosts')
      .then(list => {
        const opts = list.map((h: any) => ({
          id: h.id,
          label: (h.alias || h.addr) + (h.alias && h.alias !== h.addr ? ` (${h.addr})` : ''),
        }))
        setHosts([{ id: '', label: '本机' }, ...opts])
      })
      .catch(() => {})
  }, [])
  return hosts
}

// ==================== 流水线 Tab ====================

function PipelinesTab({ onChanged }: { onChanged: () => void }) {
  const [pipes, setPipes] = useState<PipelineView[]>([])
  const [err, setErr] = useState('')
  const [editing, setEditing] = useState<Pipeline | null>(null)
  const [webhookOf, setWebhookOf] = useState<Pipeline | null>(null)
  const [detailRun, setDetailRun] = useState('')

  const load = useCallback(() => {
    getJSON<PipelineView[]>('/api/cicd/pipelines').then(setPipes).catch(e => setErr(e.message))
  }, [])
  useEffect(load, [load])

  const run = (id: string, name: string) => {
    postJSON('/api/cicd/pipeline/run', { id })
      .then((d: any) => { setErr(''); setDetailRun(d.run.id); onChanged() })
      .catch(e => setErr(`触发 ${name} 失败: ${e.message}`))
  }
  const remove = (p: PipelineView) => {
    if (!confirm(`删除流水线「${p.name}」？其运行历史与日志将一并删除。`)) return
    postJSON('/api/cicd/pipeline/delete', { id: p.id })
      .then(() => { load(); onChanged() })
      .catch(e => setErr(e.message))
  }
  const copy = async (p: PipelineView) => {
    try {
      const full = await getJSON<Pipeline>(`/api/cicd/pipeline/get?id=${p.id}`)
      const clone: Pipeline = { ...full, id: '', name: `${p.name} 副本`, trigger: { ...full.trigger, secret: '' } }
      setEditing(clone)
    } catch (e: any) { setErr(e.message) }
  }

  return (
    <div className="section">
      {err && <div className="banner banner-err" style={{ marginBottom: 12 }}>{err}</div>}
      <Card title={`流水线 (${pipes.length})`}>
        <div style={{ display: 'flex', justifyContent: 'flex-end', marginBottom: 8 }}>
          <button className="btn btn-sm btn-accent" onClick={() => setEditing(emptyPipeline())}>+ 新建流水线</button>
        </div>
        <div className="table-wrap">
          <table className="data-table">
            <thead>
              <tr><th>名称</th><th>阶段</th><th>触发器</th><th>最近运行</th><th style={{ width: 240 }}>操作</th></tr>
            </thead>
            <tbody>
              {pipes.length === 0 && (
                <tr><td colSpan={5} style={{ textAlign: 'center', opacity: 0.6, padding: 24 }}>
                  暂无流水线, 点击右上角「新建流水线」开始编排
                </td></tr>
              )}
              {pipes.map(p => (
                <tr key={p.id}>
                  <td>
                    <div style={{ fontWeight: 600 }}>{p.name}</div>
                    {p.description && <div className="small dim">{p.description}</div>}
                  </td>
                  <td>{p.stageCount}</td>
                  <td>
                    <div style={{ display: 'flex', gap: 4, flexWrap: 'wrap' }}>
                      {p.trigger.manual && <span className="badge badge-info">手动</span>}
                      {p.trigger.webhook && <span className="badge badge-on">Webhook</span>}
                      {p.trigger.cron && <span className="badge badge-on">定时</span>}
                    </div>
                  </td>
                  <td>
                    {p.lastRun ? (
                      <div style={{ display: 'flex', gap: 6, alignItems: 'center' }}>
                        <span className={`badge ${badgeCls(p.lastRun.status)}`}>{badgeText(p.lastRun.status)}</span>
                        <span className="small dim">{fmtTime(p.lastRun.startedAt)}</span>
                        <span className="small dim">{fmtDur(p.lastRun.durationMs)}</span>
                      </div>
                    ) : <span className="dim small">从未运行</span>}
                  </td>
                  <td>
                    <div style={{ display: 'flex', gap: 4, flexWrap: 'wrap' }}>
                      <button className="btn btn-sm btn-accent" onClick={() => run(p.id, p.name)}>运行</button>
                      <button className="btn btn-sm" onClick={() => copy(p)}>复制</button>
                      <button className="btn btn-sm" onClick={async () => {
                        try { setWebhookOf(await getJSON<Pipeline>(`/api/cicd/pipeline/get?id=${p.id}`)) } catch (e: any) { setErr(e.message) }
                      }}>Webhook</button>
                      <button className="btn btn-sm" onClick={async () => {
                        try { setEditing(await getJSON<Pipeline>(`/api/cicd/pipeline/get?id=${p.id}`)) } catch (e: any) { setErr(e.message) }
                      }}>编辑</button>
                      <button className="btn btn-sm btn-danger" onClick={() => remove(p)}>删除</button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </Card>

      {editing && (
        <PipelineEditor
          value={editing}
          onClose={() => setEditing(null)}
          onSaved={() => { setEditing(null); load(); onChanged() }}
        />
      )}
      {webhookOf && <WebhookModal pipeline={webhookOf} onClose={() => setWebhookOf(null)} />}
      {detailRun && <RunDetail runId={detailRun} onClose={() => { setDetailRun(''); load(); onChanged() }} />}
    </div>
  )
}

// 枚举同流水线中当前步骤之前已声明制品的步骤(跨阶段+同阶段在前), 作为拉取候选
function priorArtifactSteps(p: Pipeline, si: number, i: number): { value: string; label: string }[] {
  const out: { value: string; label: string }[] = []
  p.stages.forEach((st, x) => {
    st.steps.forEach((sp, y) => {
      if ((sp.artifacts?.length ?? 0) > 0 && (x < si || (x === si && y < i))) {
        out.push({ value: `s${x + 1}-step${y + 1}.tar.gz`, label: `阶段${x + 1}·步骤${y + 1} ${sp.name}` })
      }
    })
  })
  return out
}

// ==================== 流水线编辑器 ====================

function PipelineEditor({ value, onClose, onSaved }: { value: Pipeline; onClose: () => void; onSaved: () => void }) {
  const [p, setP] = useState<Pipeline>(JSON.parse(JSON.stringify(value)))
  const [err, setErr] = useState('')
  const [saving, setSaving] = useState(false)
  const hosts = useHosts()
  const [repos, setRepos] = useState<Repo[]>([])
  const [registries, setRegistries] = useState<Registry[]>([])
  const [creds, setCreds] = useState<Credential[]>([])
  const [scripts, setScripts] = useState<Script[]>([])
  const isNew = !value.id

  useEffect(() => {
    getJSON<Repo[]>('/api/cicd/repos').then(setRepos).catch(() => {})
    getJSON<Registry[]>('/api/cicd/registries').then(setRegistries).catch(() => {})
    getJSON<Credential[]>('/api/cicd/credentials').then(setCreds).catch(() => {})
    getJSON<Script[]>('/api/cicd/scripts').then(setScripts).catch(() => {})
  }, [])

  const set = (patch: Partial<Pipeline>) => setP({ ...p, ...patch })
  const setStage = (i: number, patch: Partial<Stage>) => {
    const stages = p.stages.map((s, idx) => idx === i ? { ...s, ...patch } : s)
    set({ stages })
  }
  const setStep = (si: number, i: number, patch: Partial<Step>) => {
    const stages = p.stages.map((s, idx) => idx !== si ? s : {
      ...s, steps: s.steps.map((sp, idx) => idx === i ? { ...sp, ...patch } : sp),
    })
    set({ stages })
  }
  const move = (arr: any[], i: number, dir: number) => {
    const j = i + dir
    if (j < 0 || j >= arr.length) return arr
    const next = [...arr];[next[i], next[j]] = [next[j], next[i]]
    return next
  }

  const save = () => {
    if (!p.name.trim()) { setErr('流水线名称不能为空'); return }
    if (!p.stages.length) { setErr('至少需要一个阶段'); return }
    if (p.source.repoId && !p.stages[0].workspace.trim()) {
      setErr('启用代码源后, 首阶段必须设置工作目录(安全护栏: 防止 git 操作落在服务器目录)')
      return
    }
    setSaving(true)
    setErr('')
    postJSON('/api/cicd/pipeline/save', p)
      .then(onSaved)
      .catch(e => { setErr(e.message); setSaving(false) })
  }

  const regenerateSecret = () => {
    const chars = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789'
    let s = ''
    const buf = new Uint32Array(32)
    crypto.getRandomValues(buf)
    for (let i = 0; i < 32; i++) s += chars[buf[i] % chars.length]
    set({ trigger: { ...p.trigger, secret: s } })
  }

  return (
    <div className="modal-overlay">
      <div className="modal" style={{ maxWidth: 860, width: '92%', maxHeight: '88vh', overflowY: 'auto' }}>
        <div className="modal-head">
          <span className="modal-title">{isNew ? '新建流水线' : `编辑流水线: ${value.name}`}</span>
        </div>

        {err && <div className="banner banner-err">{err}</div>}

        <div className="form-row">
          <div style={{ flex: 2 }}>
            <div className="field-label">名称 *</div>
            <input className="input" value={p.name} onChange={e => set({ name: e.target.value })} placeholder="如: 前端构建部署" />
          </div>
          <div style={{ flex: 3 }}>
            <div className="field-label">描述</div>
            <input className="input" value={p.description} onChange={e => set({ description: e.target.value })} placeholder="可选" />
          </div>
        </div>
        <div className="form-row">
          <div style={{ flex: 1 }}>
            <div className="field-label">整体超时(分钟, 0=不限)</div>
            <input className="input" type="number" min={0} max={1440} value={p.timeoutMin} onChange={e => set({ timeoutMin: +e.target.value })} />
          </div>
          <div style={{ flex: 1 }}>
            <div className="field-label">历史保留条数(5-500)</div>
            <input className="input" type="number" min={5} max={500} value={p.maxRuns} onChange={e => set({ maxRuns: +e.target.value })} />
          </div>
          <div style={{ flex: 2 }}>
            <div className="field-label">完成通知 URL(可选)</div>
            <input className="input" value={p.notifyURL} onChange={e => set({ notifyURL: e.target.value })} placeholder="https://... 运行结束后 POST 结果 JSON" />
          </div>
        </div>

        <div className="form-row" style={{ alignItems: 'flex-end', gap: 16 }}>
          <div>
            <div className="field-label">触发方式</div>
            <label className="chk"><input type="checkbox" checked={p.trigger.manual} onChange={e => set({ trigger: { ...p.trigger, manual: e.target.checked } })} /> 手动</label>
            <label className="chk"><input type="checkbox" checked={p.trigger.webhook} onChange={e => set({ trigger: { ...p.trigger, webhook: e.target.checked } })} /> Webhook</label>
            <label className="chk"><input type="checkbox" checked={!!p.trigger.cron} onChange={e => set({ trigger: { ...p.trigger, cron: e.target.checked ? '0 3 * * *' : '' } })} /> 定时</label>
          </div>
          {p.trigger.webhook && (
            <div style={{ flex: 2 }}>
              <div className="field-label">Webhook 凭证</div>
              <div style={{ display: 'flex', gap: 6 }}>
                <input className="input mono" value={p.trigger.secret} onChange={e => set({ trigger: { ...p.trigger, secret: e.target.value } })} placeholder="保存时自动生成" />
                <button className="btn btn-sm" onClick={regenerateSecret}>重新生成</button>
              </div>
            </div>
          )}
          {!!p.trigger.cron && (
            <div style={{ flex: 2 }}>
              <div className="field-label">cron 表达式(分 时 日 月 周)</div>
              <input className="input mono" value={p.trigger.cron} onChange={e => set({ trigger: { ...p.trigger, cron: e.target.value } })} placeholder="0 3 * * *" />
              <div className="small dim">支持 * 、*/n 、a-b 、a,b; 如 0 3 * * * = 每天 03:00</div>
            </div>
          )}
        </div>

        <div className="form-row" style={{ flexDirection: 'column', alignItems: 'stretch' }}>
          <div className="field-label">代码源与凭据(内置变量: $BUILD_NUMBER $CICD_RUN_ID $CICD_BRANCH $REGISTRY $REGISTRY_USER $REGISTRY_PASS $GIT_REPO_USER $GIT_REPO_TOKEN)</div>
          <div className="form-row" style={{ alignItems: 'flex-end' }}>
            <div style={{ flex: 2 }}>
              <div className="field-label">代码仓库(选择后首阶段自动拉取代码)</div>
              <select className="input" value={p.source.repoId} onChange={e => set({ source: { ...p.source, repoId: e.target.value } })}>
                <option value="">不自动拉取</option>
                {repos.map(r => <option key={r.id} value={r.id}>{r.name}</option>)}
              </select>
            </div>
            <div style={{ flex: 1 }}>
              <div className="field-label">分支(空=默认)</div>
              <input className="input" value={p.source.branch} onChange={e => set({ source: { ...p.source, branch: e.target.value } })} placeholder={repos.find(r => r.id === p.source.repoId)?.defaultBranch || 'master'} />
            </div>
            <div style={{ flex: 2 }}>
              <div className="field-label">镜像仓库</div>
              <select className="input" value={p.registryId} onChange={e => set({ registryId: e.target.value })}>
                <option value="">不使用</option>
                {registries.map(r => <option key={r.id} value={r.id}>{r.name} ({r.server})</option>)}
              </select>
            </div>
            <div style={{ flex: 2 }}>
              <div className="field-label">KUBECONFIG 凭据(kubectl 发布用)</div>
              <select className="input" value={p.kubeCredId} onChange={e => set({ kubeCredId: e.target.value })}>
                <option value="">不使用</option>
                {creds.filter(c => c.type === 'kubeconfig').map(c => <option key={c.id} value={c.id}>{c.name}</option>)}
              </select>
            </div>
          </div>
        </div>

        <div className="form-row" style={{ flexDirection: 'column', alignItems: 'stretch' }}>
          <div className="field-label">环境变量(步骤命令中以 $NAME 引用; 敏感值日志自动掩码)</div>
          {p.env.map((v, i) => (
            <div key={i} style={{ display: 'flex', gap: 6, marginBottom: 6 }}>
              <input className="input" style={{ flex: 1 }} value={v.name} placeholder="NAME" onChange={e => set({ env: p.env.map((x, idx) => idx === i ? { ...x, name: e.target.value } : x) })} />
              <input className="input" style={{ flex: 2 }} value={v.value} placeholder="值" onChange={e => set({ env: p.env.map((x, idx) => idx === i ? { ...x, value: e.target.value } : x) })} />
              <label className="chk"><input type="checkbox" checked={v.secret} onChange={e => set({ env: p.env.map((x, idx) => idx === i ? { ...x, secret: e.target.checked } : x) })} /> 敏感</label>
              <button className="btn btn-sm btn-danger" onClick={() => set({ env: p.env.filter((_, idx) => idx !== i) })}>×</button>
            </div>
          ))}
          <button className="btn btn-sm" style={{ alignSelf: 'flex-start' }} onClick={() => set({ env: [...p.env, { name: '', value: '', secret: false }] })}>+ 添加变量</button>
        </div>

        <div className="field-label" style={{ marginTop: 8 }}>阶段(顺序执行, 任一失败后后续阶段跳过)</div>
        {p.stages.map((st, si) => (
          <div key={si} className="card glass" style={{ marginBottom: 10, padding: 12 }}>
            <div className="form-row" style={{ alignItems: 'center' }}>
              <b style={{ whiteSpace: 'nowrap' }}>阶段 {si + 1}</b>
              <input className="input" style={{ flex: 1 }} value={st.name} placeholder="阶段名" onChange={e => setStage(si, { name: e.target.value })} />
              <select className="input" style={{ flex: 1 }} value={st.host} onChange={e => setStage(si, { host: e.target.value })}>
                {hosts.map(h => <option key={h.id} value={h.id}>{h.label}</option>)}
              </select>
              <input className="input" style={{ flex: 1 }} value={st.workspace} placeholder="工作目录(可选)" onChange={e => setStage(si, { workspace: e.target.value })} />
              <button className="btn btn-sm" onClick={() => set({ stages: move(p.stages, si, -1) })}>↑</button>
              <button className="btn btn-sm" onClick={() => set({ stages: move(p.stages, si, 1) })}>↓</button>
              <button className="btn btn-sm btn-danger" onClick={() => { if (confirm(`删除阶段「${st.name}」？`)) set({ stages: p.stages.filter((_, idx) => idx !== si) }) }}>×</button>
            </div>
            <div style={{ display: 'flex', gap: 6, alignItems: 'center', marginBottom: 6 }}>
              <label className="chk" title="阶段执行前暂停, 等待人工批准(发布门禁)">
                <input type="checkbox" checked={st.approval} onChange={e => setStage(si, { approval: e.target.checked })} /> 执行前需审批
              </label>
              <span className="small dim">插入模板:</span>
              <select className="input sel-xs" value="" onChange={e => {
                const tpl = STEP_TEMPLATES.find(t => t.name === e.target.value)
                if (!tpl) return
                setStage(si, { steps: [...st.steps, ...tpl.steps.map(s => ({ ...s }))] })
                e.target.value = ''
              }}>
                <option value="">选择发布模板…</option>
                {STEP_TEMPLATES.map(t => <option key={t.name} value={t.name}>{t.name}</option>)}
              </select>
              <span className="small dim" style={{ marginLeft: 8 }}>裸机=重启脚本 · Docker=镜像步骤 · K8s=需选 kubeconfig 凭据</span>
            </div>
            {st.steps.map((sp, i) => (
              <div key={i} style={{ border: '1px solid var(--border)', borderRadius: 8, padding: 8, marginBottom: 6 }}>
                <div className="form-row" style={{ alignItems: 'center', marginBottom: 4 }}>
                  <input className="input" style={{ flex: 1 }} value={sp.name} placeholder="步骤名" onChange={e => setStep(si, i, { name: e.target.value })} />
                  <input className="input" style={{ width: 110 }} type="number" min={0} max={1440} title="步骤超时(分钟)" value={sp.timeoutMin} onChange={e => setStep(si, i, { timeoutMin: +e.target.value })} />
                  <label className="chk" title="失败后继续执行后续步骤"><input type="checkbox" checked={sp.continueOnFail} onChange={e => setStep(si, i, { continueOnFail: e.target.checked })} /> 失败继续</label>
                  <select className="input sel-xs" title="从脚本库插入" value="" onChange={e => {
                    const sc = scripts.find(s => s.id === e.target.value)
                    if (sc) setStep(si, i, { command: sc.content })
                    e.target.value = ''
                  }}>
                    <option value="">脚本库…</option>
                    {scripts.map(s => <option key={s.id} value={s.id}>{s.name}</option>)}
                  </select>
                  <button className="btn btn-sm" onClick={() => setStage(si, { steps: move(st.steps, i, -1) })}>↑</button>
                  <button className="btn btn-sm" onClick={() => setStage(si, { steps: move(st.steps, i, 1) })}>↓</button>
                  <button className="btn btn-sm btn-danger" onClick={() => setStage(si, { steps: st.steps.filter((_, idx) => idx !== i) })}>×</button>
                </div>
                <textarea
                  className="editor-textarea mono" rows={2}
                  value={sp.command} placeholder="shell 命令, 如: make build"
                  onChange={e => setStep(si, i, { command: e.target.value })}
                />
                <div style={{ display: 'flex', gap: 6, alignItems: 'center', marginTop: 4 }}>
                  <span className="small dim" style={{ whiteSpace: 'nowrap' }}>📦 制品:</span>
                  <input
                    className="input" value={(sp.artifacts || []).join(', ')}
                    placeholder="构建产物路径, 逗号分隔, 支持 * 通配; 步骤成功后自动归档到服务端可下载"
                    onChange={e => setStep(si, i, { artifacts: e.target.value.split(',').map(x => x.trim()).filter(Boolean) })}
                  />
                  <select
                    className="input sel-xs" title="执行前把已收集制品推送到本步骤主机工作目录, 命令中用 $CICD_ARTIFACT 引用"
                    value={sp.pullArtifact || ''}
                    onChange={e => setStep(si, i, { pullArtifact: e.target.value || undefined })}
                  >
                    <option value="">不拉取制品</option>
                    {priorArtifactSteps(p, si, i).map(o => <option key={o.value} value={o.value}>{o.label}</option>)}
                  </select>
                </div>
              </div>
            ))}
            <button className="btn btn-sm" onClick={() => setStage(si, { steps: [...st.steps, { name: `步骤 ${st.steps.length + 1}`, command: '', continueOnFail: false, timeoutMin: 0 }] })}>+ 添加步骤</button>
          </div>
        ))}
        <button className="btn btn-sm" onClick={() => set({ stages: [...p.stages, { name: `阶段 ${p.stages.length + 1}`, host: '', workspace: '', approval: false, steps: [] }] })}>+ 添加阶段</button>

        <div className="modal-actions">
          <button className="btn" onClick={onClose}>取消</button>
          <button className="btn btn-accent" disabled={saving} onClick={save}>{saving ? '保存中...' : '保存'}</button>
        </div>
      </div>
    </div>
  )
}

// ==================== Webhook 弹窗 ====================

function WebhookModal({ pipeline, onClose }: { pipeline: Pipeline; onClose: () => void }) {
  const [copied, setCopied] = useState('')
  const url = `${location.origin}/api/cicd/webhook/${pipeline.id}`
  const copy = async (text: string, tag: string) => {
    try {
      await navigator.clipboard.writeText(text)
      setCopied(tag); setTimeout(() => setCopied(''), 1500)
    } catch { /* 剪贴板不可用时忽略 */ }
  }
  const curl = `curl -X POST '${url}' -H 'X-Opscore-Token: ${pipeline.trigger.secret}'`
  return (
    <div className="modal-overlay" onClick={onClose}>
      <div className="modal" style={{ maxWidth: 640, width: '92%' }} onClick={e => e.stopPropagation()}>
        <div className="modal-head"><span className="modal-title">Webhook 触发: {pipeline.name}</span></div>
        <div className="form-row" style={{ flexDirection: 'column', alignItems: 'stretch', gap: 10 }}>
          <div>
            <div className="field-label">触发地址</div>
            <div style={{ display: 'flex', gap: 6 }}>
              <input className="input mono" readOnly value={url} onFocus={e => e.target.select()} />
              <button className="btn btn-sm" onClick={() => copy(url, 'url')}>{copied === 'url' ? '已复制' : '复制'}</button>
            </div>
          </div>
          <div>
            <div className="field-label">凭证(X-Opscore-Token)</div>
            <div style={{ display: 'flex', gap: 6 }}>
              <input className="input mono" readOnly value={pipeline.trigger.secret} onFocus={e => e.target.select()} />
              <button className="btn btn-sm" onClick={() => copy(pipeline.trigger.secret, 'secret')}>{copied === 'secret' ? '已复制' : '复制'}</button>
            </div>
            <div className="small dim">也可经 ?token= 或 body.secret 传递; 在 Git 仓库 Webhook 设置中填入地址与凭证即可 push 自动触发</div>
          </div>
          <div>
            <div className="field-label">curl 示例</div>
            <div style={{ display: 'flex', gap: 6 }}>
              <textarea className="editor-textarea mono" rows={2} readOnly value={curl} />
              <button className="btn btn-sm" onClick={() => copy(curl, 'curl')}>{copied === 'curl' ? '已复制' : '复制'}</button>
            </div>
          </div>
        </div>
        <div className="modal-actions">
          <button className="btn" onClick={onClose}>关闭</button>
        </div>
      </div>
    </div>
  )
}

// ==================== 运行历史 Tab ====================

function RunsTab({ onChanged }: { onChanged: () => void }) {
  const [runs, setRuns] = useState<Run[]>([])
  const [pipes, setPipes] = useState<PipelineView[]>([])
  const [filter, setFilter] = useState('')
  const [detailRun, setDetailRun] = useState('')
  const [err, setErr] = useState('')

  const load = useCallback(() => {
    getJSON<Run[]>(`/api/cicd/runs?limit=100${filter ? `&pipeline=${filter}` : ''}`).then(setRuns).catch(e => setErr(e.message))
  }, [filter])
  useEffect(load, [load])
  useEffect(() => {
    getJSON<PipelineView[]>('/api/cicd/pipelines').then(setPipes).catch(() => {})
  }, [])

  const cancel = (run: Run) => {
    if (!confirm(`取消运行 ${run.pipeline}（${run.id}）？`)) return
    postJSON('/api/cicd/run/cancel', { runId: run.id })
      .then(() => { load(); onChanged() })
      .catch(e => setErr(e.message))
  }

  return (
    <div className="section">
      {err && <div className="banner banner-err" style={{ marginBottom: 12 }}>{err}</div>}
      <Card title={`运行历史 (${runs.length})`}>
        <div style={{ display: 'flex', justifyContent: 'flex-end', marginBottom: 8 }}>
          <select className="input sel-xs" value={filter} onChange={e => setFilter(e.target.value)}>
            <option value="">全部流水线</option>
            {pipes.map(p => <option key={p.id} value={p.id}>{p.name}</option>)}
          </select>
        </div>
        <div className="table-wrap">
          <table className="data-table">
            <thead>
              <tr><th>流水线</th><th>触发</th><th>状态</th><th style={{ minWidth: 90 }}>进度</th><th>开始时间</th><th>耗时</th><th style={{ width: 150 }}>操作</th></tr>
            </thead>
            <tbody>
              {runs.length === 0 && (
                <tr><td colSpan={7} style={{ textAlign: 'center', opacity: 0.6, padding: 24 }}>暂无运行记录</td></tr>
              )}
              {runs.map(r => (
                <tr key={r.id}>
                  <td style={{ fontWeight: 600 }}>{r.pipeline}</td>
                  <td><span className="badge badge-info">{TRIGGER_TEXT[r.trigger] || r.trigger}</span></td>
                  <td>
                    <span className={`badge ${badgeCls(r.status)}`}>
                      {badgeText(r.status)}{r.status === 'running' && r.canceling ? '(取消中)' : ''}
                    </span>
                    {r.error && <div className="small dim" style={{ maxWidth: 260 }}>{r.error}</div>}
                  </td>
                  <td>
                    <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                      <ProgressBar value={r.progress || 0} status={r.status} />
                      <span className="small dim" style={{ minWidth: 32 }}>{r.progress || 0}%</span>
                    </div>
                  </td>
                  <td className="small">{fmtTime(r.startedAt)}</td>
                  <td>{fmtDur(r.durationMs)}</td>
                  <td>
                    <div style={{ display: 'flex', gap: 4 }}>
                      <button className="btn btn-sm" onClick={() => setDetailRun(r.id)}>详情</button>
                      {(r.status === 'running' || r.status === 'queued') && (
                        <button className="btn btn-sm btn-danger" onClick={() => cancel(r)}>取消</button>
                      )}
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </Card>
      {detailRun && <RunDetail runId={detailRun} onClose={() => { setDetailRun(''); load(); onChanged() }} />}
    </div>
  )
}

// ==================== 运行详情(SSE 实时日志) ====================

function RunDetail({ runId, onClose }: { runId: string; onClose: () => void }) {
  const [run, setRun] = useState<Run | null>(null)
  const [lines, setLines] = useState<string[]>([])
  const [err, setErr] = useState('')
  const logRef = useRef<HTMLDivElement>(null)
  const stickBottom = useRef(true)

  // 自动滚动: 用户上滚时暂停
  const onScroll = () => {
    const el = logRef.current
    if (!el) return
    stickBottom.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40
  }
  useEffect(() => {
    const el = logRef.current
    if (el && stickBottom.current) el.scrollTop = el.scrollHeight
  }, [lines])

  useEffect(() => {
    let alive = true
    const ctrl = new AbortController()
    // ① 立即拉取状态 + 全量回填已有日志, 再增量接 SSE
    getJSON<Run>(`/api/cicd/run/get?id=${runId}`).then(setRun).catch(() => {})
    getJSON<{ content: string; offset: number }>(`/api/cicd/run/log?id=${runId}`)
      .then(d => {
        if (!alive) return
        if (d.content) setLines(d.content.replace(/\n$/, '').split('\n'))
        stream(d.offset)
      })
      .catch(() => stream(0))

    const stream = (offset: number) => {
      const headers: Record<string, string> = { 'Content-Type': 'application/json' }
      const t = localStorage.getItem('opscore-token')
      if (t) headers.Authorization = `Bearer ${t}`
      fetch('/api/cicd/run/stream', {
        method: 'POST', headers, body: JSON.stringify({ runId }), signal: ctrl.signal,
      }).then(async resp => {
        if (!resp.ok || !resp.body) throw new Error(`HTTP ${resp.status}`)
        const reader = resp.body.getReader()
        const decoder = new TextDecoder()
        let buf = ''
        for (; ;) {
          const { done, value } = await reader.read()
          if (done) break
          buf += decoder.decode(value, { stream: true })
          const frames = buf.split('\n\n')
          buf = frames.pop() || ''
          for (const frame of frames) {
            const line = frame.split('\n').find(l => l.startsWith('data: '))
            if (!line) continue
            let evt: any
            try { evt = JSON.parse(line.slice(6)) } catch { continue }
            if (evt.type === 'log') setLines(prev => [...prev, evt.payload])
            else if (evt.type === 'status') setRun(evt.payload)
            else if (evt.type === 'error') setErr(evt.payload)
          }
        }
      }).catch(e => {
        if (alive && e.name !== 'AbortError') setErr('日志流中断: ' + e.message)
      })
    }
    return () => { alive = false; ctrl.abort() }
  }, [runId])

  // 无运行中流时兜底轮询状态(历史记录直接 GET)
  useEffect(() => {
    if (run && ['success', 'failed', 'canceled'].includes(run.status)) return
    const t = setInterval(() => {
      getJSON<Run>(`/api/cicd/run/get?id=${runId}`).then(setRun).catch(() => {})
    }, 1500)
    return () => clearInterval(t)
  }, [runId, run?.status])

  const cancel = () => {
    postJSON('/api/cicd/run/cancel', { runId }).catch(e => setErr(e.message))
  }

  if (!run) return null
  const active = run.status === 'running' || run.status === 'queued'

  return (
    <div className="modal-overlay" onClick={onClose}>
      <div className="modal" style={{ maxWidth: 980, width: '94%', maxHeight: '90vh', overflowY: 'auto' }} onClick={e => e.stopPropagation()}>
        <div className="modal-head">
          <span className="modal-title">
            {run.pipeline}
            <span className={`badge ${badgeCls(run.status)}`} style={{ marginLeft: 8 }}>
              {badgeText(run.status)}{run.status === 'running' && run.canceling ? '(取消中)' : ''}
            </span>
          </span>
          <span className="small dim">
            {TRIGGER_TEXT[run.trigger] || run.trigger} · {fmtDur(run.durationMs)} · {run.id}
          </span>
        </div>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 10 }}>
          <ProgressBar value={run.progress || 0} status={run.status} />
          <span className="small dim" style={{ minWidth: 38 }}>{run.progress || 0}%</span>
        </div>

        {run.error && <div className="banner banner-err">{run.error}</div>}
        {err && <div className="banner banner-err">{err}</div>}

        {run.stages.map((st, i) => (
          <div key={i} className="card glass" style={{ marginBottom: 10, borderColor: st.status === 'waiting' ? 'var(--warn)' : undefined }}>
            <div className="card-head">
              <h3>
                <span className={`badge ${badgeCls(st.status)}`} style={{ marginRight: 8 }}>{badgeText(st.status)}</span>
                阶段 {i + 1}: {st.name}
              </h3>
              <span className="card-sub">
                {st.host ? `主机 ${st.host}` : '本机'}{st.workspace ? ` · ${st.workspace}` : ''}
                {st.status === 'waiting' && active && (
                  <span style={{ marginLeft: 10 }}>
                    <button className="btn btn-sm btn-accent" onClick={() => postJSON('/api/cicd/run/approve', { runId, approve: true }).catch(e => setErr(e.message))}>✓ 批准执行</button>
                    <button className="btn btn-sm btn-danger" style={{ marginLeft: 6 }} onClick={() => { if (confirm(`拒绝执行阶段「${st.name}」？该运行将标记为已取消。`)) postJSON('/api/cicd/run/approve', { runId, approve: false }).catch(e => setErr(e.message)) }}>✗ 拒绝</button>
                  </span>
                )}
              </span>
            </div>
            <div className="card-body" style={{ padding: 0 }}>
              <table className="data-table">
                <tbody>
                  {st.steps.map((sp, j) => (
                    <tr key={j}>
                      <td style={{ width: '40%' }}>
                        <span className="mono small dim">{String(j + 1).padStart(2, '0')}</span>{' '}
                        <b>{sp.name}</b>
                      </td>
                      <td>
                        <span className={`badge ${badgeCls(sp.status)}`}>{badgeText(sp.status)}</span>
                        {sp.artifacts && sp.artifacts.length > 0 && (
                          <div style={{ marginTop: 4, display: 'flex', gap: 6, flexWrap: 'wrap' }}>
                            {sp.artifacts.map(a => (
                              <button
                                key={a.file} className="btn btn-sm" title={`${a.paths} · 点击下载 ${a.file}`}
                                onClick={() => {
                                  const t = localStorage.getItem('opscore-token')
                                  window.open(`/api/cicd/artifact/download?run=${run.id}&file=${a.file}${t ? `&token=${encodeURIComponent(t)}` : ''}`)
                                }}
                              >📦 {fmtSize(a.size)}</button>
                            ))}
                          </div>
                        )}
                      </td>
                      <td className="small dim">exit {sp.status === 'pending' ? '-' : sp.exitCode}</td>
                      <td className="small dim">{fmtDur(sp.durationMs)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </div>
        ))}

        <div className="log-panel-head" style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
          <h3>执行日志({lines.length} 行)</h3>
          {active && <span className="small dim">实时跟随时勿上滚</span>}
        </div>
        <div className="log-text" ref={logRef} onScroll={onScroll} style={{ maxHeight: 320, overflowY: 'auto' }}>
          {lines.length === 0 && <div className="log-loading">暂无日志输出</div>}
          {lines.map((l, i) => <div key={i} className="log-line">{l}</div>)}
        </div>

        <div className="modal-actions">
          {active && <button className="btn btn-danger" onClick={cancel}>取消运行</button>}
          <button className="btn" onClick={onClose}>关闭</button>
        </div>
      </div>
    </div>
  )
}

// ==================== 脚本库 Tab ====================

function ScriptsTab() {
  const [scripts, setScripts] = useState<Script[]>([])
  const [editing, setEditing] = useState<Script | null>(null)
  const [err, setErr] = useState('')

  const load = useCallback(() => {
    getJSON<Script[]>('/api/cicd/scripts').then(setScripts).catch(e => setErr(e.message))
  }, [])
  useEffect(load, [load])

  const remove = (s: Script) => {
    if (!confirm(`删除脚本「${s.name}」？`)) return
    postJSON('/api/cicd/script/delete', { id: s.id }).then(load).catch(e => setErr(e.message))
  }

  return (
    <div className="section">
      {err && <div className="banner banner-err" style={{ marginBottom: 12 }}>{err}</div>}
      <Card title={`脚本库 (${scripts.length})`}>
        <div style={{ display: 'flex', justifyContent: 'flex-end', marginBottom: 8 }}>
          <button className="btn btn-sm btn-accent" onClick={() => setEditing({ id: '', name: '', description: '', content: '', updatedAt: '' })}>+ 新建脚本</button>
        </div>
        <div className="table-wrap">
          <table className="data-table">
            <thead><tr><th>名称</th><th>描述</th><th>更新时间</th><th style={{ width: 140 }}>操作</th></tr></thead>
            <tbody>
              {scripts.length === 0 && (
                <tr><td colSpan={4} style={{ textAlign: 'center', opacity: 0.6, padding: 24 }}>暂无脚本; 流水线步骤中可直接引用</td></tr>
              )}
              {scripts.map(s => (
                <tr key={s.id}>
                  <td style={{ fontWeight: 600 }}>{s.name}</td>
                  <td className="small dim">{s.description}</td>
                  <td className="small">{s.updatedAt ? new Date(s.updatedAt).toLocaleString() : '-'}</td>
                  <td>
                    <div style={{ display: 'flex', gap: 4 }}>
                      <button className="btn btn-sm" onClick={() => setEditing(s)}>编辑</button>
                      <button className="btn btn-sm btn-danger" onClick={() => remove(s)}>删除</button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </Card>

      {editing && (
        <div className="modal-overlay">
          <div className="modal" style={{ maxWidth: 720, width: '92%' }}>
            <div className="modal-head">
              <span className="modal-title">{editing.id ? `编辑脚本: ${editing.name}` : '新建脚本'}</span>
            </div>
            <div className="form-row">
              <div style={{ flex: 1 }}>
                <div className="field-label">名称 *</div>
                <input className="input" value={editing.name} onChange={e => setEditing({ ...editing, name: e.target.value })} />
              </div>
              <div style={{ flex: 2 }}>
                <div className="field-label">描述</div>
                <input className="input" value={editing.description} onChange={e => setEditing({ ...editing, description: e.target.value })} />
              </div>
            </div>
            <div className="field-label">脚本内容(POSIX shell, 可使用流水线注入的环境变量)</div>
            <textarea className="editor-textarea mono" rows={12} value={editing.content} onChange={e => setEditing({ ...editing, content: e.target.value })} placeholder={'#!/bin/sh\nset -e\ndocker compose pull && docker compose up -d'} />
            <div className="modal-actions">
              <button className="btn" onClick={() => setEditing(null)}>取消</button>
              <button className="btn btn-accent" onClick={() => {
                postJSON('/api/cicd/script/save', editing)
                  .then(() => { setEditing(null); load() })
                  .catch(e => setErr(e.message))
              }}>保存</button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}

// ==================== 仓库 Tab(代码仓库 + 镜像仓库) ====================

function ReposTab() {
  const [repos, setRepos] = useState<Repo[]>([])
  const [registries, setRegistries] = useState<Registry[]>([])
  const [creds, setCreds] = useState<Credential[]>([])
  const [editingRepo, setEditingRepo] = useState<Repo | null>(null)
  const [editingReg, setEditingReg] = useState<Registry | null>(null)
  const [err, setErr] = useState('')
  const [testMsg, setTestMsg] = useState('')

  const load = useCallback(() => {
    getJSON<Repo[]>('/api/cicd/repos').then(setRepos).catch(e => setErr(e.message))
    getJSON<Registry[]>('/api/cicd/registries').then(setRegistries).catch(() => {})
    getJSON<Credential[]>('/api/cicd/credentials').then(setCreds).catch(() => {})
  }, [])
  useEffect(load, [load])

  const testRepo = (r: Repo) => {
    setTestMsg('测试中...')
    postJSON<{ ok: boolean; output?: string; error?: string }>('/api/cicd/repo/test', { id: r.id })
      .then(d => setTestMsg(d.ok ? `✓ 连接成功\n${d.output || ''}` : `✗ ${d.error}\n${d.output || ''}`))
      .catch(e => setTestMsg(`✗ ${e.message}`))
  }
  const testReg = (r: Registry) => {
    setTestMsg('测试中...')
    postJSON<{ ok: boolean; output?: string; error?: string }>('/api/cicd/registry/test', { id: r.id })
      .then(d => setTestMsg(d.ok ? `✓ ${d.output || '服务存活'}` : `✗ ${d.error}`))
      .catch(e => setTestMsg(`✗ ${e.message}`))
  }

  const credName = (id: string) => creds.find(c => c.id === id)?.name || '-'

  return (
    <div className="section">
      {err && <div className="banner banner-err" style={{ marginBottom: 12 }}>{err}</div>}
      {testMsg && (
        <div className={`banner ${testMsg.startsWith('✓') ? 'banner-ok' : 'banner-info'}`} style={{ marginBottom: 12, whiteSpace: 'pre-wrap', fontFamily: 'monospace', fontSize: '0.75rem' }}>
          {testMsg}
          <button className="btn btn-sm" style={{ marginLeft: 10 }} onClick={() => setTestMsg('')}>关闭</button>
        </div>
      )}

      <Card title={`代码仓库 (${repos.length})`}>
        <div style={{ display: 'flex', justifyContent: 'flex-end', marginBottom: 8 }}>
          <button className="btn btn-sm btn-accent" onClick={() => setEditingRepo({ id: '', name: '', url: '', credId: '', defaultBranch: 'master' })}>+ 新建代码仓库</button>
        </div>
        <div className="table-wrap">
          <table className="data-table">
            <thead><tr><th>名称</th><th>地址</th><th>凭据</th><th>默认分支</th><th style={{ width: 200 }}>操作</th></tr></thead>
            <tbody>
              {repos.length === 0 && <tr><td colSpan={5} style={{ textAlign: 'center', opacity: 0.6, padding: 16 }}>暂无代码仓库</td></tr>}
              {repos.map(r => (
                <tr key={r.id}>
                  <td style={{ fontWeight: 600 }}>{r.name}</td>
                  <td className="small mono">{r.url}</td>
                  <td className="small">{credName(r.credId)}</td>
                  <td className="small">{r.defaultBranch}</td>
                  <td>
                    <div style={{ display: 'flex', gap: 4 }}>
                      <button className="btn btn-sm" onClick={() => testRepo(r)}>测试</button>
                      <button className="btn btn-sm" onClick={() => setEditingRepo(r)}>编辑</button>
                      <button className="btn btn-sm btn-danger" onClick={() => { if (confirm(`删除仓库「${r.name}」？`)) postJSON('/api/cicd/repo/delete', { id: r.id }).then(load).catch(e => setErr(e.message)) }}>删除</button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </Card>

      <Card title={`镜像仓库 (${registries.length})`}>
        <div style={{ display: 'flex', justifyContent: 'flex-end', marginBottom: 8 }}>
          <button className="btn btn-sm btn-accent" onClick={() => setEditingReg({ id: '', name: '', server: '', credId: '' })}>+ 新建镜像仓库</button>
        </div>
        <div className="table-wrap">
          <table className="data-table">
            <thead><tr><th>名称</th><th>地址</th><th>凭据</th><th style={{ width: 200 }}>操作</th></tr></thead>
            <tbody>
              {registries.length === 0 && <tr><td colSpan={4} style={{ textAlign: 'center', opacity: 0.6, padding: 16 }}>暂无镜像仓库</td></tr>}
              {registries.map(r => (
                <tr key={r.id}>
                  <td style={{ fontWeight: 600 }}>{r.name}</td>
                  <td className="small mono">{r.server}</td>
                  <td className="small">{credName(r.credId)}</td>
                  <td>
                    <div style={{ display: 'flex', gap: 4 }}>
                      <button className="btn btn-sm" onClick={() => testReg(r)}>测试</button>
                      <button className="btn btn-sm" onClick={() => setEditingReg(r)}>编辑</button>
                      <button className="btn btn-sm btn-danger" onClick={() => { if (confirm(`删除镜像仓库「${r.name}」？`)) postJSON('/api/cicd/registry/delete', { id: r.id }).then(load).catch(e => setErr(e.message)) }}>删除</button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </Card>

      {editingRepo && (
        <div className="modal-overlay">
          <div className="modal" style={{ maxWidth: 620, width: '92%' }}>
            <div className="modal-head"><span className="modal-title">{editingRepo.id ? '编辑代码仓库' : '新建代码仓库'}</span></div>
            <div className="form-row"><div style={{ flex: 1 }}>
              <div className="field-label">名称 *</div>
              <input className="input" value={editingRepo.name} onChange={e => setEditingRepo({ ...editingRepo, name: e.target.value })} placeholder="如: 业务后端" />
            </div><div style={{ flex: 1 }}>
              <div className="field-label">默认分支</div>
              <input className="input" value={editingRepo.defaultBranch} onChange={e => setEditingRepo({ ...editingRepo, defaultBranch: e.target.value })} placeholder="master" />
            </div></div>
            <div className="field-label">仓库地址 *(https:// 或 git@ / ssh://)</div>
            <input className="input mono" value={editingRepo.url} onChange={e => setEditingRepo({ ...editingRepo, url: e.target.value })} placeholder="https://git.example.com/team/app.git" />
            <div className="field-label" style={{ marginTop: 8 }}>访问凭据(https 私有库需 git 类型凭据; ssh 形态依赖主机 ssh key)</div>
            <select className="input" value={editingRepo.credId} onChange={e => setEditingRepo({ ...editingRepo, credId: e.target.value })}>
              <option value="">无(公开库 / 主机 ssh key)</option>
              {creds.filter(c => c.type === 'git').map(c => <option key={c.id} value={c.id}>{c.name}</option>)}
            </select>
            <div className="modal-actions">
              <button className="btn" onClick={() => setEditingRepo(null)}>取消</button>
              <button className="btn btn-accent" onClick={() => postJSON('/api/cicd/repo/save', editingRepo).then(() => { setEditingRepo(null); load() }).catch(e => setErr(e.message))}>保存</button>
            </div>
          </div>
        </div>
      )}

      {editingReg && (
        <div className="modal-overlay">
          <div className="modal" style={{ maxWidth: 620, width: '92%' }}>
            <div className="modal-head"><span className="modal-title">{editingReg.id ? '编辑镜像仓库' : '新建镜像仓库'}</span></div>
            <div className="form-row"><div style={{ flex: 1 }}>
              <div className="field-label">名称 *</div>
              <input className="input" value={editingReg.name} onChange={e => setEditingReg({ ...editingReg, name: e.target.value })} placeholder="如: 生产 Harbor" />
            </div><div style={{ flex: 1 }}>
              <div className="field-label">地址 *(域名[:端口], 不含协议)</div>
              <input className="input mono" value={editingReg.server} onChange={e => setEditingReg({ ...editingReg, server: e.target.value })} placeholder="registry.example.com:5000" />
            </div></div>
            <div className="field-label" style={{ marginTop: 8 }}>访问凭据</div>
            <select className="input" value={editingReg.credId} onChange={e => setEditingReg({ ...editingReg, credId: e.target.value })}>
              <option value="">无(匿名)</option>
              {creds.filter(c => c.type === 'registry').map(c => <option key={c.id} value={c.id}>{c.name}</option>)}
            </select>
            <div className="modal-actions">
              <button className="btn" onClick={() => setEditingReg(null)}>取消</button>
              <button className="btn btn-accent" onClick={() => postJSON('/api/cicd/registry/save', editingReg).then(() => { setEditingReg(null); load() }).catch(e => setErr(e.message))}>保存</button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}

// ==================== 凭据 Tab ====================

function CredentialsTab() {
  const [creds, setCreds] = useState<Credential[]>([])
  const [editing, setEditing] = useState<(Credential & { data?: string }) | null>(null)
  const [err, setErr] = useState('')

  const load = useCallback(() => {
    getJSON<Credential[]>('/api/cicd/credentials').then(setCreds).catch(e => setErr(e.message))
  }, [])
  useEffect(load, [load])

  const remove = (c: Credential) => {
    if (!confirm(`删除凭据「${c.name}」？引用它的仓库/流水线将回退为无凭据。`)) return
    postJSON('/api/cicd/credential/delete', { id: c.id }).then(load).catch(e => setErr(e.message))
  }

  const isKube = editing?.type === 'kubeconfig'

  return (
    <div className="section">
      {err && <div className="banner banner-err" style={{ marginBottom: 12 }}>{err}</div>}
      <Card title={`凭据中心 (${creds.length})`}>
        <div style={{ display: 'flex', justifyContent: 'flex-end', marginBottom: 8 }}>
          <button className="btn btn-sm btn-accent" onClick={() => setEditing({ id: '', name: '', type: 'git', username: '', server: '', hasData: false, note: '', updatedAt: '', data: '' })}>+ 新建凭据</button>
        </div>
        <div className="table-wrap">
          <table className="data-table">
            <thead><tr><th>名称</th><th>类型</th><th>用户名</th><th>备注</th><th>更新时间</th><th style={{ width: 140 }}>操作</th></tr></thead>
            <tbody>
              {creds.length === 0 && <tr><td colSpan={6} style={{ textAlign: 'center', opacity: 0.6, padding: 24 }}>暂无凭据; 密文保存后仅写不读, 日志中自动掩码</td></tr>}
              {creds.map(c => (
                <tr key={c.id}>
                  <td style={{ fontWeight: 600 }}>{c.name}</td>
                  <td><span className="badge badge-info">{CRED_TYPE_TEXT[c.type] || c.type}</span></td>
                  <td className="small">{c.username || '-'}</td>
                  <td className="small dim">{c.note || c.server || '-'}</td>
                  <td className="small">{c.updatedAt ? new Date(c.updatedAt).toLocaleString() : '-'}</td>
                  <td>
                    <div style={{ display: 'flex', gap: 4 }}>
                      <button className="btn btn-sm" onClick={() => setEditing({ ...c, data: '' })}>编辑</button>
                      <button className="btn btn-sm btn-danger" onClick={() => remove(c)}>删除</button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </Card>

      {editing && (
        <div className="modal-overlay">
          <div className="modal" style={{ maxWidth: 640, width: '92%' }}>
            <div className="modal-head"><span className="modal-title">{editing.id ? `编辑凭据: ${editing.name}` : '新建凭据'}</span></div>
            <div className="form-row">
              <div style={{ flex: 1 }}>
                <div className="field-label">名称 *</div>
                <input className="input" value={editing.name} onChange={e => setEditing({ ...editing, name: e.target.value })} placeholder="如: gitlab-ci-token" />
              </div>
              <div style={{ flex: 1 }}>
                <div className="field-label">类型 *</div>
                <select className="input" value={editing.type} onChange={e => setEditing({ ...editing, type: e.target.value })} disabled={!!editing.id}>
                  <option value="git">代码库(token/密码)</option>
                  <option value="registry">镜像仓库(用户名+密码)</option>
                  <option value="kubeconfig">K8s kubeconfig</option>
                  <option value="generic">通用密文</option>
                </select>
              </div>
              {(editing.type === 'git' || editing.type === 'registry') && (
                <div style={{ flex: 1 }}>
                  <div className="field-label">用户名</div>
                  <input className="input" value={editing.username || ''} onChange={e => setEditing({ ...editing, username: e.target.value })} placeholder={editing.type === 'registry' ? '如: robot$ci' : '可空(纯 token)'} />
                </div>
              )}
            </div>
            <div className="field-label">{isKube ? 'kubeconfig 内容 *' : '密文 *'}{editing.id ? ' (留空保持原值)' : ''}</div>
            {isKube ? (
              <textarea className="editor-textarea mono" rows={10} value={editing.data || ''} onChange={e => setEditing({ ...editing, data: e.target.value })} placeholder="apiVersion: v1&#10;clusters: ..." />
            ) : (
              <input className="input mono" type="password" value={editing.data || ''} onChange={e => setEditing({ ...editing, data: e.target.value })} placeholder={editing.id ? '留空保持原值' : 'token / 密码'} />
            )}
            {editing.type === 'generic' && (
              <>
                <div className="field-label" style={{ marginTop: 8 }}>备注</div>
                <input className="input" value={editing.note || ''} onChange={e => setEditing({ ...editing, note: e.target.value })} placeholder="用途说明" />
              </>
            )}
            <div className="small dim" style={{ marginTop: 8 }}>
              安全说明: 密文保存后不可回读(仅可覆盖); 列表只显示"已配置"标记; 运行日志中自动掩码; kubeconfig 在目标主机落盘为 600 权限临时文件并在运行后清理。
            </div>
            <div className="modal-actions">
              <button className="btn" onClick={() => setEditing(null)}>取消</button>
              <button className="btn btn-accent" onClick={() => {
                const payload: any = { ...editing }
                delete payload.hasData
                delete payload.updatedAt
                postJSON('/api/cicd/credential/save', payload)
                  .then(() => { setEditing(null); load() })
                  .catch(e => setErr(e.message))
              }}>保存</button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
