// ── CI/CD 流水线模块(shadcn 版): 流水线编排 / 运行历史 / 脚本库 / 仓库 / 凭据 / 概览 ──
//    v3 布局: 流水线页改主从式 —— 左列清单选条目, 右栏单一详情区(运行记录/配置摘要),
//    层级用面板切换表达而非表格+弹窗堆叠; 其余页签维持表格布局。

import { Fragment, useCallback, useEffect, useRef, useState } from 'react'
import { postJSON } from '../api/client'
import { useToast } from '../components/Toast'
import { ContextMenu, type CtxMenuItem } from './cicd/ContextMenu'
import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Checkbox } from '@/components/ui/checkbox'
import { Label } from '@/components/ui/label'
import { Badge } from '@/components/ui/badge'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import {
  Play, Copy, Link2, Pencil, Trash2, Plus, ChevronUp, ChevronDown, Download,
  Upload, RefreshCw, X, Check, Package, FileCode2, LoaderCircle, Pause, Minus, GitCommitHorizontal, ChevronRight,
} from 'lucide-react'
import {
  API, SELECT_NONE, useResource, useConfirm, StatusBadge, statusText, ErrBanner,
  fmtDur, fmtTime, fmtSize, TRIGGER_TEXT,
  type Pipeline, type PipelineView, type Run, type Stage, type Step, type HostOpt,
  type Credential, type Repo, type Registry, type Script,
  type StageRun, type ActionSpec,
} from './cicd/shared'

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

// ── Blue Ocean 式阶段节点 ──
const STAGE_COLOR: Record<string, string> = {
  success: 'var(--ok)', failed: 'var(--danger)', running: 'var(--accent)', waiting: 'var(--warn)',
  canceled: 'var(--text-dim)', skipped: 'var(--text-dim)', pending: 'var(--border)',
}

// 阶段耗时: 已完成步骤累加 + 运行中步骤按 now 实时计算
function stageElapsedMs(st: StageRun, now: number): number {
  let total = 0
  for (const sp of st.steps) {
    if (sp.status === 'running' && sp.startedAt) {
      total += Math.max(0, now - new Date(sp.startedAt).getTime())
    } else {
      total += sp.durationMs || 0
    }
  }
  return total
}

function StageNodeIcon({ status }: { status: string }) {
  if (status === 'success') return <Check className="size-4" />
  if (status === 'failed') return <X className="size-4" />
  if (status === 'running') return <LoaderCircle className="size-4 animate-spin" />
  if (status === 'waiting') return <Pause className="size-3.5" />
  if (status === 'canceled') return <Minus className="size-4" />
  return <span className="size-1.5 rounded-full bg-current opacity-50" />
}

// StepDot: 步骤小圆点(状态色)
function StepDot({ status }: { status: string }) {
  const color = STAGE_COLOR[status] || 'var(--border)'
  return (
    <span
      className={cn('size-3.5 rounded-full border-2 flex items-center justify-center shrink-0', status === 'running' && 'animate-pulse')}
      style={{
        borderColor: color,
        background: ['success', 'failed'].includes(status) ? color : undefined,
        color: '#fff',
      }}
    >
      {status === 'success' && <Check className="size-2.5" />}
      {status === 'failed' && <X className="size-2.5" />}
    </span>
  )
}

// 详情页横向节点流(Blue Ocean 式): 阶段节点 + 纵向步骤链(可点击跳日志), 实时耗时
function StageFlow({ stages, now, onStepClick }: { stages: StageRun[]; now: number; onStepClick?: (si: number, j: number) => void }) {
  return (
    <div className="flex items-start w-full py-1 overflow-x-auto">
      {stages.map((st, i) => {
        const color = STAGE_COLOR[st.status] || 'var(--border)'
        const solid = ['success', 'failed', 'running', 'waiting'].includes(st.status)
        return (
          <Fragment key={i}>
            {i > 0 && (
              <div
                className="flex-1 min-w-8 h-0.5 rounded-full mt-[17px]"
                style={{ background: stages[i - 1].status === 'success' ? 'var(--ok)' : 'var(--border)' }}
              />
            )}
            <div className="flex flex-col items-center w-40 shrink-0">
              <div
                className={cn('size-9 rounded-full border-2 flex items-center justify-center bg-background', st.status === 'running' && 'animate-pulse')}
                style={{
                  borderColor: color,
                  color: solid ? color : 'var(--text-dim)',
                  background: solid ? `color-mix(in srgb, ${color} 14%, var(--surface-solid))` : undefined,
                }}
              >
                <StageNodeIcon status={st.status} />
              </div>
              <div className="text-xs font-medium text-center leading-tight break-all px-0.5 mt-1">{st.name}</div>
              <div className="text-[10px] text-muted-foreground tabular-nums">{fmtDur(stageElapsedMs(st, now))}</div>
              {st.steps.length > 0 && (
                <div className="mt-2 w-full flex flex-col items-stretch">
                  {st.steps.map((sp, j) => (
                    <Fragment key={j}>
                      {j > 0 && <div className="w-0.5 h-1.5 bg-border mx-auto" style={{ marginLeft: 0 }} />}
                      <button
                        className="flex items-center gap-1.5 w-full px-1 py-0.5 rounded text-xs hover:bg-muted/60 transition-colors"
                        title={sp.command}
                        onClick={() => onStepClick?.(i, j)}
                      >
                        <StepDot status={sp.status} />
                        <span className={cn('truncate flex-1 text-left', sp.status === 'running' && 'font-medium')}>{sp.name}</span>
                        <span className="text-[10px] text-muted-foreground tabular-nums shrink-0">
                          {sp.status === 'running' ? fmtDur(Math.max(0, now - (sp.startedAt ? new Date(sp.startedAt).getTime() : now))) : fmtDur(sp.durationMs)}
                        </span>
                      </button>
                    </Fragment>
                  ))}
                </div>
              )}
            </div>
          </Fragment>
        )
      })}
    </div>
  )
}

// 列表用迷你分段条: 每阶段一段, 颜色即状态, 悬停看名称
function StageSegments({ stages }: { stages: StageRun[] }) {
  return (
    <div className="flex gap-0.5 w-full min-w-20"
      title={stages.map(s => `${s.name}: ${statusText(s.status)}`).join('\n')}>
      {stages.map((st, i) => (
        <div
          key={i}
          className={cn(
            'h-1.5 flex-1 rounded-full',
            st.status === 'success' && 'bg-ok',
            st.status === 'failed' && 'bg-danger',
            st.status === 'running' && 'bg-primary animate-pulse',
            st.status === 'waiting' && 'bg-warn',
            (st.status === 'skipped' || st.status === 'canceled') && 'bg-muted-foreground/40',
            st.status === 'pending' && 'bg-border',
          )}
        />
      ))}
    </div>
  )
}

// 项目类型构建模板(对标 Jenkins 各语言入门教程): 选类型 → 生成可直接运行的流水线骨架
const PIPELINE_TEMPLATES: { name: string; desc: string; make: () => Pipeline }[] = [
  { name: '空白流水线', desc: '从零开始编排', make: () => emptyPipeline() },
  {
    name: '前端项目 (Node)', desc: '安装依赖 → 构建 → 归档 dist/', make: () => ({
      ...emptyPipeline(), name: '前端构建', description: 'Node 前端构建',
      stages: [
        { name: '构建', host: '', workspace: '', approval: false, steps: [
          { name: '安装依赖', command: 'npm ci', continueOnFail: false, timeoutMin: 0 },
          { name: '构建', command: 'npm run build', continueOnFail: false, timeoutMin: 0, artifacts: ['dist/*'] },
        ] },
      ],
    }),
  },
  {
    name: 'Java 项目 (Maven)', desc: 'mvn 打包 → 归档 jar', make: () => ({
      ...emptyPipeline(), name: 'Java 构建', description: 'Maven 构建打包',
      stages: [
        { name: '构建', host: '', workspace: '', approval: false, steps: [
          { name: '打包', command: 'mvn -B -DskipTests clean package', continueOnFail: false, timeoutMin: 0, artifacts: ['target/*.jar'] },
        ] },
        { name: '测试', host: '', workspace: '', approval: false, steps: [
          { name: '单元测试', command: 'mvn -B test', continueOnFail: false, timeoutMin: 0 },
        ] },
      ],
    }),
  },
  {
    name: 'Go 项目', desc: 'go build → 归档二进制', make: () => ({
      ...emptyPipeline(), name: 'Go 构建', description: 'Go 编译构建',
      stages: [
        { name: '构建', host: '', workspace: '', approval: false, steps: [
          { name: '编译', command: 'go build -o bin/app ./...', continueOnFail: false, timeoutMin: 0, artifacts: ['bin/*'] },
          { name: '测试', command: 'go test ./...', continueOnFail: false, timeoutMin: 0 },
        ] },
      ],
    }),
  },
  {
    name: 'Python 项目', desc: 'pip 安装 → 测试 → 归档', make: () => ({
      ...emptyPipeline(), name: 'Python 构建', description: 'Python 构建测试',
      stages: [
        { name: '构建', host: '', workspace: '', approval: false, steps: [
          { name: '安装依赖', command: 'pip install -r requirements.txt', continueOnFail: false, timeoutMin: 0 },
          { name: '测试', command: 'python -m pytest', continueOnFail: false, timeoutMin: 0 },
          { name: '打包', command: 'python -m build', continueOnFail: false, timeoutMin: 0, artifacts: ['dist/*'] },
        ] },
      ],
    }),
  },
  {
    name: 'Docker 镜像', desc: '构建 → 推送镜像(需配置镜像仓库)', make: () => ({
      ...emptyPipeline(), name: 'Docker 构建', description: '构建并推送镜像',
      stages: [
        { name: '镜像', host: '', workspace: '', approval: false, steps: [
          { name: '登录镜像仓库', command: 'docker login $REGISTRY -u "$REGISTRY_USER" -p "$REGISTRY_PASS"', continueOnFail: false, timeoutMin: 0 },
          { name: '构建镜像', command: 'docker build -t $REGISTRY/myapp:${BUILD_NUMBER} .', continueOnFail: false, timeoutMin: 0 },
          { name: '推送镜像', command: 'docker push $REGISTRY/myapp:${BUILD_NUMBER}', continueOnFail: false, timeoutMin: 0 },
        ] },
      ],
    }),
  },
]

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
  const [detailRunId, setDetailRunId] = useState('')

  const loadOverview = useCallback(() => {
    fetch(API.overview).then(r => r.json()).then(setOverview).catch(() => {})
  }, [])

  useEffect(() => {
    loadOverview()
    const t = setInterval(loadOverview, 5000)
    return () => clearInterval(t)
  }, [loadOverview, tab])

  const waiting = overview?.waitingApproval ?? 0

  return (
    <div className="module">
      <div className="module-head">
        <div className="module-head-row"><h2>CI/CD 流水线</h2></div>
        <div className="flex items-center gap-2 flex-wrap">
          {overview?.maintenance && (
            <Badge variant="destructive">维护模式：暂停接受新运行（设置中关闭）</Badge>
          )}
          <Badge variant="secondary" className="tabular-nums">
            运行中 {overview?.running ?? '-'} · 排队 {overview?.queued ?? '-'}{waiting > 0 ? ` · 待审批 ${waiting}` : ''}
          </Badge>
        </div>
      </div>

      <Tabs value={tab} onValueChange={setTab}>
        <TabsList className="mb-3 flex-wrap h-auto">
          <TabsTrigger value="pipelines">流水线</TabsTrigger>
          <TabsTrigger value="runs">运行历史</TabsTrigger>
          <TabsTrigger value="scripts">脚本库</TabsTrigger>
          <TabsTrigger value="repos">仓库</TabsTrigger>
          <TabsTrigger value="creds">
            凭据
          </TabsTrigger>
          <TabsTrigger value="overview">
            概览{waiting > 0 && <span className="ml-1 text-warn">•{waiting}</span>}
          </TabsTrigger>
        </TabsList>

        <TabsContent value="pipelines"><PipelinesTab onChanged={loadOverview} /></TabsContent>
        <TabsContent value="runs"><RunsTab onChanged={loadOverview} onOpenRun={setDetailRunId} /></TabsContent>
        <TabsContent value="scripts"><ScriptsTab /></TabsContent>
        <TabsContent value="repos"><ReposTab /></TabsContent>
        <TabsContent value="creds"><CredentialsTab /></TabsContent>
        <TabsContent value="overview"><OverviewTab data={overview} onOpenRun={setDetailRunId} onMore={() => setTab('runs')} /></TabsContent>
      </Tabs>

      {detailRunId && <RunDetail runId={detailRunId} onRerun={setDetailRunId} onChanged={loadOverview} onClose={() => setDetailRunId('')} />}
    </div>
  )
}

// ── 主机列表(本机 + Ansible 清单, 与 HostSelector 同源) ──
function useHosts(): HostOpt[] {
  const [hosts, setHosts] = useState<HostOpt[]>([{ id: '', label: '本机' }])
  useEffect(() => {
    fetch(API.hosts).then(r => r.json())
      .then((list: any[]) => {
        const opts = list.map(h => ({
          id: h.id as string,
          label: (h.alias || h.addr) + (h.alias && h.alias !== h.addr ? ` (${h.addr})` : ''),
        }))
        setHosts([{ id: '', label: '本机' }, ...opts])
      })
      .catch(() => {})
  }, [])
  return hosts
}

// 主机下拉(阶段卡用; NONE 哨兵承载"本机"空串语义)
function HostSelect({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  const hosts = useHosts()
  return (
    <Select value={value || SELECT_NONE} onValueChange={v => onChange(v === SELECT_NONE ? '' : v)}>
      <SelectTrigger><SelectValue /></SelectTrigger>
      <SelectContent>
        {hosts.map(h => <SelectItem key={h.id || SELECT_NONE} value={h.id || SELECT_NONE}>{h.label}</SelectItem>)}
      </SelectContent>
    </Select>
  )
}

// 可选值下拉(空串语义统一走哨兵, 消灭 onChange 手动复位 hack · R4)
function OptSelect({ value, onChange, placeholder, items, className }: {
  value: string
  onChange: (v: string) => void
  placeholder: string
  items: { value: string; label: string }[]
  className?: string
}) {
  return (
    <Select value={value || SELECT_NONE} onValueChange={v => onChange(v === SELECT_NONE ? '' : v)}>
      <SelectTrigger className={className}><SelectValue placeholder={placeholder} /></SelectTrigger>
      <SelectContent>
        <SelectItem value={SELECT_NONE}>{placeholder}</SelectItem>
        {items.map(i => <SelectItem key={i.value} value={i.value}>{i.label}</SelectItem>)}
      </SelectContent>
    </Select>
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

// ==================== 流水线 Tab(v3 主从布局: 左列表 + 右详情) ====================

function PipelinesTab({ onChanged }: { onChanged: () => void }) {
  const { data, err, setErr, reload } = useResource<PipelineView[]>(API.pipelines)
  const pipes = data || []
  const [selId, setSelId] = useState('')
  const [editing, setEditing] = useState<Pipeline | null>(null)
  const [webhookOf, setWebhookOf] = useState<Pipeline | null>(null)
  const [detailRun, setDetailRun] = useState('')
  const [busy, setBusy] = useState('')
  const [runSel, setRunSel] = useState<Pipeline | null>(null)
  const [tplOpen, setTplOpen] = useState(false)
  const [tick, setTick] = useState(0) // 右栏运行列表手动刷新信号
  const { confirm, confirmEl } = useConfirm()

  // 选中项不存在(未加载/已被删)时回落到第一条
  const sel = pipes.find(p => p.id === selId) || pipes[0] || null

  const run = async (p: PipelineView) => {
    if (p.source.repoId || (p.params?.length ?? 0) > 0) {
      // 有代码源或参数定义 → 弹运行配置(选分支/填参数)
      try {
        const full = await fetch(`${API.pipelineGet}?id=${p.id}`).then(r => r.json()) as Pipeline
        setRunSel(full)
      } catch (e: any) { setErr(e.message) }
      return
    }
    doRun(p.id, '', {})
  }
  const doRun = async (id: string, branch: string, params: Record<string, string>) => {
    setBusy(id)
    try {
      const d = await postJSON<{ run: Run }>(API.pipelineRun, { id, branch, params })
      setErr(''); setDetailRun(d.run.id); setTick(t => t + 1); onChanged()
    } catch (e: any) { setErr(`触发失败: ${e.message}`) } finally { setBusy('') }
  }
  const remove = async (p: PipelineView) => {
    if (!(await confirm(`删除流水线「${p.name}」？`, { desc: '其运行历史、日志与制品将一并删除。', danger: true, okText: '删除' }))) return
    setBusy(p.id)
    try { await postJSON(API.pipelineDelete, { id: p.id }); reload(); onChanged() }
    catch (e: any) { setErr(e.message) } finally { setBusy('') }
  }
  const copy = async (p: PipelineView) => {
    try {
      const full = await fetch(`${API.pipelineGet}?id=${p.id}`).then(r => r.json()) as Pipeline
      setEditing({ ...full, id: '', name: `${p.name} 副本`, trigger: { ...full.trigger, secret: '' } })
    } catch (e: any) { setErr(e.message) }
  }
  const loadOne = async (p: PipelineView, set: (v: Pipeline) => void) => {
    try { set(await fetch(`${API.pipelineGet}?id=${p.id}`).then(r => r.json())) } catch (e: any) { setErr(e.message) }
  }

  // 右键菜单(左列表): 对象级操作收纳, 表格行不再堆按钮
  const [ctx, setCtx] = useState<{ x: number; y: number; p: PipelineView } | null>(null)
  const toast = useToast()
  const exportOne = async (p: PipelineView) => {
    try {
      const full = await fetch(`${API.pipelineGet}?id=${p.id}`).then(r => r.json()) as Pipeline
      const clean = { ...full, trigger: { ...full.trigger, secret: '' } } // 与全局导出一致: 不带触发凭证
      const blob = new Blob([JSON.stringify(clean, null, 2)], { type: 'application/json' })
      const a = document.createElement('a')
      a.href = URL.createObjectURL(blob)
      a.download = `pipeline-${p.name}.json`
      a.click(); URL.revokeObjectURL(a.href)
      toast.success(`已导出 ${p.name}.json`)
    } catch (e: any) { setErr('导出失败: ' + e.message) }
  }
  const pipelineMenu = (p: PipelineView): CtxMenuItem[] => [
    { label: '运行', icon: <Play />, onSelect: () => run(p) },
    { label: '编辑', icon: <Pencil />, onSelect: () => loadOne(p, setEditing) },
    { label: 'Webhook', icon: <Link2 />, onSelect: () => loadOne(p, setWebhookOf) },
    { label: '复制流水线', icon: <Copy />, onSelect: () => copy(p) },
    { label: '导出此流水线 (JSON)', icon: <Download />, onSelect: () => exportOne(p) },
    { sep: true, label: '' },
    { label: '删除', icon: <Trash2 />, danger: true, disabled: !!busy, onSelect: () => remove(p) },
  ]

  return (
    <div>
      {confirmEl}
      <ErrBanner msg={err} onClose={() => setErr('')} />
      {/* 主从两栏撑满视口剩余高度, 各自框内滑动(滚动条悬停才显示) */}
      <div className="grid grid-cols-1 lg:grid-cols-[300px_minmax(0,1fr)] gap-3 items-start lg:items-stretch lg:h-[calc(100vh-14rem)]">
        {/* 左: 流水线清单(主) */}
        <Card className="flex flex-col overflow-hidden lg:h-full">
          <CardHeader className="pb-2 shrink-0">
            <div className="flex items-center justify-between gap-1">
              <CardTitle className="text-sm tabular-nums">流水线 ({pipes.length})</CardTitle>
              <div className="flex gap-1">
                <Button asChild variant="ghost" size="icon" className="size-7" title="导入流水线 JSON(重置 ID 与凭证, 重名自动加后缀)">
                  <label className="cursor-pointer">
                    <Upload className="size-4" />
                    <input type="file" accept=".json,application/json" className="hidden" onChange={async ev => {
                      const f = ev.target.files?.[0]
                      ev.target.value = ''
                      if (!f) return
                      try {
                        const d = await postJSON<{ imported: number; skipped: number }>(API.pipelineImport, JSON.parse(await f.text()))
                        setErr('')
                        await confirm(`导入完成: 成功 ${d.imported} 条${d.skipped ? `, 跳过 ${d.skipped} 条(结构无效)` : ''}`)
                        reload(); onChanged()
                      } catch (e: any) { setErr('导入失败: ' + e.message) }
                    }} />
                  </label>
                </Button>
                <Button variant="ghost" size="icon" className="size-7" title="导出全部流水线为 JSON(不含触发凭证)" onClick={() => {
                  const t = localStorage.getItem('opscore-token')
                  window.open(`${API.pipelineExport}${t ? `?token=${encodeURIComponent(t)}` : ''}`)
                }}><Download className="size-4" /></Button>
                <Button size="sm" className="h-7" onClick={() => setTplOpen(true)}><Plus />新建</Button>
              </div>
            </div>
          </CardHeader>
          <CardContent className="px-2 pb-2 pt-0 flex-1 min-h-0 hover-scroll">
            {pipes.length === 0 && (
              <div className="h-20 flex items-center justify-center text-sm text-muted-foreground">暂无流水线, 点「新建」开始编排</div>
            )}
            <div className="flex flex-col gap-1">
              {pipes.map(p => {
                const active = p.id === sel?.id
                return (
                  <button key={p.id} onClick={() => setSelId(p.id)}
                    onContextMenu={e => { e.preventDefault(); setSelId(p.id); setCtx({ x: e.clientX, y: e.clientY, p }) }}
                    className={cn('text-left rounded-lg px-3 py-2 border transition-colors',
                      active ? 'border-accent bg-accent/5' : 'border-transparent hover:bg-muted/60')}>
                    <div className="flex items-center gap-2">
                      <LastRunDot run={p.lastRun} />
                      <span className="font-medium text-sm truncate flex-1">{p.name}</span>
                    </div>
                    <div className="text-xs text-muted-foreground mt-0.5 flex gap-2 items-center flex-wrap">
                      {p.lastRun ? (
                        <>
                          <span>{fmtTime(p.lastRun.startedAt)}</span>
                          <span className="tabular-nums">{fmtDur(p.lastRun.durationMs)}</span>
                        </>
                      ) : <span>从未运行</span>}
                      {p.trigger.webhook && <Badge variant="outline" className="px-1 py-0 text-[10px]">Hook</Badge>}
                      {p.trigger.cron && <Badge variant="outline" className="px-1 py-0 text-[10px]">定时</Badge>}
                    </div>
                  </button>
                )
              })}
            </div>
          </CardContent>
        </Card>

        {/* 右: 选中流水线详情(从) */}
        {sel && (
          <PipelineDetail p={sel} busy={busy} tick={tick}
            onRun={() => run(sel)} onEdit={() => loadOne(sel, setEditing)}
            onWebhook={() => loadOne(sel, setWebhookOf)} onCopy={() => copy(sel)}
            onDelete={() => remove(sel)} onOpenRun={setDetailRun}
            onRunsChanged={() => setTick(t => t + 1)} />
        )}
      </div>

      {runSel && <RunBranchDialog pipeline={runSel} onClose={() => setRunSel(null)} onRun={(b, params) => { const id = runSel.id; setRunSel(null); doRun(id, b, params) }} />}
      {tplOpen && (
        <Dialog open onOpenChange={o => !o && setTplOpen(false)}>
          <DialogContent className="sm:max-w-2xl">
            <DialogHeader><DialogTitle>选择项目类型</DialogTitle></DialogHeader>
            <div className="grid grid-cols-2 md:grid-cols-3 gap-2">
              {PIPELINE_TEMPLATES.map(t => (
                <button key={t.name} className="rounded-lg border p-3 text-left hover:border-accent hover:bg-accent/5 transition-colors"
                  onClick={() => { setTplOpen(false); setEditing(t.make()) }}>
                  <div className="font-semibold text-sm">{t.name}</div>
                  <div className="text-xs text-muted-foreground mt-1">{t.desc}</div>
                </button>
              ))}
            </div>
            <div className="text-xs text-muted-foreground">模板生成骨架后可继续编辑: 换目标主机/工作目录/加审批门禁/挂代码源与制品</div>
          </DialogContent>
        </Dialog>
      )}
      {editing && (
        <PipelineEditor value={editing} onClose={() => setEditing(null)} onSaved={() => { setEditing(null); reload(); onChanged() }} />
      )}
      {webhookOf && <WebhookDialog pipeline={webhookOf} onClose={() => setWebhookOf(null)} />}
      {detailRun && <RunDetail runId={detailRun} onRerun={setDetailRun} onChanged={() => { reload(); setTick(t => t + 1); onChanged() }} onClose={() => { setDetailRun(''); reload(); setTick(t => t + 1); onChanged() }} />}
      {ctx && <ContextMenu x={ctx.x} y={ctx.y} items={pipelineMenu(ctx.p)} onClose={() => setCtx(null)} />}
    </div>
  )
}

// ── 左列表状态点(最近一次运行的状态色) ──
function LastRunDot({ run }: { run?: Run }) {
  const c = run ? (STAGE_COLOR[run.status] || 'var(--text-dim)') : 'var(--border)'
  return (
    <span
      className={cn('size-2 rounded-full shrink-0', run?.status === 'running' && 'animate-pulse')}
      style={{ background: c }}
    />
  )
}

// ── 右详情面板: 头部操作区 + 运行记录 / 配置摘要 两个子页签 ──
function PipelineDetail({ p, busy, tick, onRun, onEdit, onWebhook, onCopy, onDelete, onOpenRun, onRunsChanged }: {
  p: PipelineView
  busy: string
  tick: number
  onRun: () => void
  onEdit: () => void
  onWebhook: () => void
  onCopy: () => void
  onDelete: () => void
  onOpenRun: (id: string) => void
  onRunsChanged: () => void
}) {
  const [now, setNow] = useState(Date.now())
  const live = ['running', 'queued', 'waiting'].includes(p.lastRun?.status || '')
  useEffect(() => {
    if (!live) return
    const t = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(t)
  }, [live, p.lastRun?.id])

  return (
    <Card className="flex flex-col overflow-hidden lg:h-full min-h-0">
      <CardHeader className="pb-3 shrink-0">
        <div className="flex items-start justify-between gap-2 flex-wrap">
          <div className="min-w-0">
            <CardTitle className="flex items-center gap-2 flex-wrap">
              {p.name}
              {p.lastRun && <StatusBadge status={p.lastRun.status} suffix={p.lastRun.status === 'running' && p.lastRun.canceling ? '(取消中)' : undefined} />}
            </CardTitle>
            {p.description && <div className="text-xs text-muted-foreground mt-1">{p.description}</div>}
            <div className="text-xs text-muted-foreground mt-1 flex gap-2 items-center flex-wrap">
              <span className="tabular-nums">{p.stageCount} 阶段</span>
              {p.trigger.manual && <Badge variant="secondary">手动</Badge>}
              {p.trigger.webhook && <Badge variant="outline">Webhook</Badge>}
              {p.trigger.cron && <Badge variant="outline">定时</Badge>}
              {p.nextCron && <span className="tabular-nums">下次 {fmtTime(p.nextCron)}</span>}
            </div>
          </div>
          <div className="flex gap-1 flex-wrap">
            <Button size="sm" disabled={!!busy} title="运行" onClick={onRun}><Play />运行</Button>
            <Button variant="outline" size="icon" className="size-8" disabled={!!busy} title="编辑" onClick={onEdit}><Pencil /></Button>
            <Button variant="outline" size="icon" className="size-8" disabled={!!busy} title="Webhook" onClick={onWebhook}><Link2 /></Button>
            <Button variant="outline" size="icon" className="size-8" disabled={!!busy} title="复制" onClick={onCopy}><Copy /></Button>
            <Button variant="destructive" size="icon" className="size-8" disabled={!!busy} title="删除" onClick={onDelete}><Trash2 /></Button>
          </div>
        </div>
      </CardHeader>
      <CardContent className="flex-1 min-h-0 overflow-y-auto hover-scroll">
        <Tabs defaultValue="runs">
          <TabsList className="mb-3">
            <TabsTrigger value="runs">运行记录</TabsTrigger>
            <TabsTrigger value="config">流水线配置</TabsTrigger>
          </TabsList>
          <TabsContent value="runs" className="mt-0">
            {p.lastRun && (
              <div className="mb-4">
                <div className="text-xs text-muted-foreground mb-1 flex gap-2 items-center flex-wrap">
                  <span>最近一次运行 {fmtTime(p.lastRun.startedAt)}</span>
                  {p.lastRun.branch && <span className="font-mono">{p.lastRun.branch}</span>}
                  {p.lastRun.commit && (
                    <span className="font-mono truncate max-w-52" title={p.lastRun.commit}>
                      <GitCommitHorizontal className="inline size-3.5 mr-0.5 -mt-0.5" />{p.lastRun.commit}
                    </span>
                  )}
                </div>
                <StageFlow stages={p.lastRun.stages} now={now} onStepClick={() => p.lastRun && onOpenRun(p.lastRun.id)} />
              </div>
            )}
            <PipelineRunsList pipelineId={p.id} tick={tick} onOpenRun={onOpenRun} onChanged={onRunsChanged} />
          </TabsContent>
          <TabsContent value="config" className="mt-0">
            <PipelineConfigSummary p={p} onEdit={onEdit} />
          </TabsContent>
        </Tabs>
      </CardContent>
    </Card>
  )
}

// ── 单条流水线的运行列表(后端按 pipeline 过滤, 5s 轮询跟活; 行右键: 详情/重跑/导出日志/删除) ──
function PipelineRunsList({ pipelineId, tick, onOpenRun, onChanged }: { pipelineId: string; tick: number; onOpenRun: (id: string) => void; onChanged: () => void }) {
  const [runs, setRuns] = useState<Run[]>([])
  const [ctx, setCtx] = useState<{ x: number; y: number; r: Run } | null>(null)
  const [busy, setBusy] = useState('')
  const { confirm, confirmEl } = useConfirm()
  const toast = useToast()
  useEffect(() => {
    let on = true
    const load = () => {
      fetch(`${API.runs}?limit=20&pipeline=${encodeURIComponent(pipelineId)}`)
        .then(r => r.json()).then((d: Run[]) => { if (on) setRuns(d || []) }).catch(() => {})
    }
    load()
    const t = setInterval(load, 5000)
    return () => { on = false; clearInterval(t) }
  }, [pipelineId, tick])

  const rerun = async (r: Run) => {
    setBusy(r.id)
    try {
      const d = await postJSON<{ run: Run }>(API.pipelineRun, { id: r.pipelineId, branch: r.branch || '' })
      toast.success('已重新触发运行'); onOpenRun(d.run.id); onChanged()
    } catch (e: any) { toast.error('重新执行失败: ' + e.message) } finally { setBusy('') }
  }
  const removeRun = async (r: Run) => {
    if (!(await confirm('删除这条运行记录？', { desc: '日志与制品将一并删除。', danger: true, okText: '删除' }))) return
    setBusy(r.id)
    try { await postJSON(API.runDelete, { runId: r.id }); onChanged() } catch (e: any) { toast.error(e.message) } finally { setBusy('') }
  }
  const downloadLog = (r: Run) => {
    const t = localStorage.getItem('opscore-token')
    window.open(`${API.runLogDownload}?id=${r.id}${t ? `&token=${encodeURIComponent(t)}` : ''}`)
  }
  // 回滚: 钉住该次运行的 commit 重新触发完整流水线
  const rollbackRun = async (r: Run) => {
    const c = r.commit?.split(/\s/)[0] || ''
    if (!c) return
    if (!(await confirm(`回滚到 ${c}？`, { desc: '将重新拉取该提交并执行完整流水线(部署随之回退)。', okText: '回滚' }))) return
    setBusy(r.id)
    try {
      const d = await postJSON<{ run: Run }>(API.pipelineRun, { id: r.pipelineId, branch: r.branch || '', commit: c })
      toast.success('已触发回滚'); onOpenRun(d.run.id); onChanged()
    } catch (e: any) { toast.error('回滚失败: ' + e.message) } finally { setBusy('') }
  }
  const runMenu = (r: Run): CtxMenuItem[] => [
    { label: '查看详情', icon: <FileCode2 />, onSelect: () => onOpenRun(r.id) },
    { label: '重新执行', icon: <RefreshCw />, disabled: !!busy, onSelect: () => rerun(r) },
    ...(r.commit ? [{ label: '回滚此版本', icon: <GitCommitHorizontal />, disabled: !!busy, onSelect: () => rollbackRun(r) }] : []),
    { label: '导出日志 txt', icon: <Download />, onSelect: () => downloadLog(r) },
    { sep: true, label: '' },
    ...(r.status !== 'running' && r.status !== 'queued' ? [
      { label: '删除记录', icon: <Trash2 />, danger: true, disabled: !!busy, onSelect: () => removeRun(r) },
    ] : []),
  ]

  return (
    <div>
      {confirmEl}
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>状态</TableHead><TableHead>触发</TableHead>
            <TableHead className="min-w-24">进度</TableHead><TableHead>开始时间</TableHead>
            <TableHead>耗时</TableHead><TableHead className="w-20">操作</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {runs.length === 0 && (
            <TableRow><TableCell colSpan={6} className="h-16 text-center text-muted-foreground">该流水线还没有运行记录</TableCell></TableRow>
          )}
          {runs.map(r => (
            <TableRow key={r.id}
              onContextMenu={e => { e.preventDefault(); setCtx({ x: e.clientX, y: e.clientY, r }) }}>
              <TableCell>
                <StatusBadge status={r.status} suffix={r.status === 'running' && r.canceling ? '(取消中)' : undefined} />
                {r.error && <div className="text-xs text-muted-foreground max-w-64">{r.error}</div>}
              </TableCell>
              <TableCell><Badge variant="secondary">{TRIGGER_TEXT[r.trigger] || r.trigger}</Badge></TableCell>
              <TableCell>
                <div className="flex items-center gap-2 min-w-36">
                  <StageSegments stages={r.stages} />
                  <span className="text-xs text-muted-foreground tabular-nums shrink-0">{r.progress || 0}%</span>
                </div>
              </TableCell>
              <TableCell className="text-xs">{fmtTime(r.startedAt)}</TableCell>
              <TableCell className="tabular-nums">{fmtDur(r.durationMs)}</TableCell>
              <TableCell>
                <Button variant="outline" size="sm" onClick={() => onOpenRun(r.id)}>详情</Button>
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
      {ctx && <ContextMenu x={ctx.x} y={ctx.y} items={runMenu(ctx.r)} onClose={() => setCtx(null)} />}
    </div>
  )
}

// ── 配置摘要(不弹窗即可读: 基础信息 + 参数 + 阶段编排一览) ──
function PipelineConfigSummary({ p, onEdit }: { p: PipelineView; onEdit: () => void }) {
  return (
    <div className="space-y-4">
      <div className="flex justify-end">
        <Button variant="outline" size="sm" onClick={onEdit}><Pencil />编辑流水线</Button>
      </div>
      <div className="grid grid-cols-2 md:grid-cols-4 gap-2 text-sm">
        <ConfigCell label="代码源" value={p.source.repoId || '未配置'} />
        <ConfigCell label="分支" value={p.source.branch || '仓库默认'} />
        <ConfigCell label="超时" value={p.timeoutMin ? `${p.timeoutMin} 分钟` : '不限'} />
        <ConfigCell label="历史保留" value={`${p.maxRuns} 次`} />
      </div>
      {(p.params?.length ?? 0) > 0 && (
        <div>
          <div className="text-xs text-muted-foreground mb-1.5">构建参数(触发运行时填写)</div>
          <div className="flex gap-1.5 flex-wrap">
            {p.params!.map(d => (
              <Badge key={d.name} variant="outline" className="font-normal">
                {d.label || d.name}<span className="text-muted-foreground ml-1">{d.type || 'text'}</span>
              </Badge>
            ))}
          </div>
        </div>
      )}
      <div>
        <div className="text-xs text-muted-foreground mb-1.5">阶段编排</div>
        <div className="space-y-1.5">
          {p.stages.map((st, i) => (
            <div key={i} className="rounded-md border px-3 py-1.5 text-sm flex gap-2 items-center flex-wrap">
              <span className="font-medium">{i + 1}. {st.name}</span>
              {st.approval && <Badge variant="outline" className="text-warn">审批门禁</Badge>}
              <span className="text-xs text-muted-foreground">{st.host ? `主机 ${st.host}` : '本机'}</span>
              <span className="text-xs text-muted-foreground truncate flex-1 min-w-40">
                {st.steps.map(s => s.name).join(' · ')}
              </span>
            </div>
          ))}
        </div>
      </div>
    </div>
  )
}

function ConfigCell({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-md border px-3 py-2">
      <div className="text-xs text-muted-foreground">{label}</div>
      <div className="truncate" title={value}>{value}</div>
    </div>
  )
}

// ==================== 流水线编辑器 ====================

function PipelineEditor({ value, onClose, onSaved }: { value: Pipeline; onClose: () => void; onSaved: () => void }) {
  const [p, setP] = useState<Pipeline>(JSON.parse(JSON.stringify(value)))
  const [err, setErr] = useState('')
  const [saving, setSaving] = useState(false)
  const repos = useResource<Repo[]>(API.repos)
  const registries = useResource<Registry[]>(API.registries)
  const creds = useResource<Credential[]>(API.credentials)
  const scripts = useResource<Script[]>(API.scripts)
  const actions = useResource<ActionSpec[]>(API.actions)
  const { confirm, confirmEl } = useConfirm()
  const isNew = !value.id

  const set = (patch: Partial<Pipeline>) => setP(x => ({ ...x, ...patch }))
  const setStage = (i: number, patch: Partial<Stage>) => {
    setP(x => ({ ...x, stages: x.stages.map((s, idx) => idx === i ? { ...s, ...patch } : s) }))
  }
  const setStep = (si: number, i: number, patch: Partial<Step>) => {
    setP(x => ({
      ...x,
      stages: x.stages.map((s, idx) => idx !== si ? s : {
        ...s, steps: s.steps.map((sp, j) => j === i ? { ...sp, ...patch } : sp),
      }),
    }))
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
    postJSON(API.pipelineSave, p).then(onSaved).catch(e => { setErr(e.message); setSaving(false) })
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
    <Dialog open onOpenChange={o => !o && onClose()}>
      <DialogContent className="sm:max-w-5xl h-[90vh] flex flex-col overflow-hidden">
        <DialogHeader className="px-6 pt-6 pb-2 shrink-0">
          <DialogTitle>{isNew ? '新建流水线' : `编辑流水线: ${value.name}`}</DialogTitle>
        </DialogHeader>

        <div className="flex-1 overflow-y-auto px-6 space-y-3 min-h-0">
        <ErrBanner msg={err} onClose={() => setErr('')} />

        <div className="flex gap-3 flex-wrap">
          <div className="flex-[2] min-w-48">
            <Label>名称 *</Label>
            <Input value={p.name} onChange={e => set({ name: e.target.value })} placeholder="如: 前端构建部署" />
          </div>
          <div className="flex-[3] min-w-48">
            <Label>描述</Label>
            <Input value={p.description} onChange={e => set({ description: e.target.value })} placeholder="可选" />
          </div>
        </div>
        <div className="flex gap-3 flex-wrap">
          <div className="flex-1 min-w-36">
            <Label>整体超时(分钟, 0=不限)</Label>
            <Input type="number" min={0} max={1440} value={p.timeoutMin} onChange={e => set({ timeoutMin: +e.target.value })} />
          </div>
          <div className="flex-1 min-w-36">
            <Label>历史保留条数(5-500)</Label>
            <Input type="number" min={5} max={500} value={p.maxRuns} onChange={e => set({ maxRuns: +e.target.value })} />
          </div>
          <div className="flex-[2] min-w-48">
            <Label>完成通知地址(可选)</Label>
            <Input value={p.notifyURL} onChange={e => set({ notifyURL: e.target.value })} placeholder="https://... 机器人 webhook 地址" />
          </div>
          <div className="flex-1 min-w-32">
            <Label>通知渠道</Label>
            <OptSelect value={p.notifyChannel || ''} onChange={v => set({ notifyChannel: v })} placeholder="通用 JSON"
              items={[{ value: 'dingtalk', label: '钉钉机器人' }, { value: 'feishu', label: '飞书机器人' }, { value: 'wecom', label: '企业微信机器人' }]} />
          </div>
          {p.notifyChannel === 'dingtalk' && (
            <div className="flex-[2] min-w-48">
              <Label>钉钉加签密钥(SEC 开头, 可空)</Label>
              <Input className="font-mono" value={p.notifySecret || ''} onChange={e => set({ notifySecret: e.target.value })} placeholder="SEC..." />
            </div>
          )}
        </div>

        <div className="flex gap-4 items-end flex-wrap">
          <div>
            <Label>触发方式</Label>
            <div className="flex gap-4 h-9 items-center">
              <label className="flex items-center gap-1.5 text-sm"><Checkbox checked={p.trigger.manual} onCheckedChange={c => set({ trigger: { ...p.trigger, manual: !!c } })} />手动</label>
              <label className="flex items-center gap-1.5 text-sm"><Checkbox checked={p.trigger.webhook} onCheckedChange={c => set({ trigger: { ...p.trigger, webhook: !!c } })} />Webhook</label>
              <label className="flex items-center gap-1.5 text-sm"><Checkbox checked={!!p.trigger.cron} onCheckedChange={c => set({ trigger: { ...p.trigger, cron: c ? '0 3 * * *' : '' } })} />定时</label>
            </div>
          </div>
          {p.trigger.webhook && (
            <div className="flex-[2] min-w-48">
              <Label>Webhook 凭证</Label>
              <div className="flex gap-2">
                <Input className="font-mono" value={p.trigger.secret} onChange={e => set({ trigger: { ...p.trigger, secret: e.target.value } })} placeholder="保存时自动生成" />
                <Button variant="outline" size="icon" onClick={regenerateSecret} title="重新生成"><RefreshCw /></Button>
              </div>
            </div>
          )}
          {!!p.trigger.cron && (
            <div className="flex-[4] min-w-96">
              <Label>cron 表达式(分 时 日 月 周)</Label>
              <div className="flex items-center gap-2">
                <Input className="font-mono w-40 shrink-0" value={p.trigger.cron} onChange={e => set({ trigger: { ...p.trigger, cron: e.target.value } })} placeholder="0 3 * * *" />
                <span className="text-xs text-muted-foreground whitespace-nowrap">支持 * 、*/n 、a-b 、a,b; 如 0 3 * * * = 每天 03:00</span>
              </div>
            </div>
          )}
        </div>

        <div>
          <Label className="text-muted-foreground">代码源与凭据(内置变量: $BUILD_NUMBER $CICD_RUN_ID $CICD_BRANCH $REGISTRY $REGISTRY_USER $REGISTRY_PASS $GIT_REPO_USER $GIT_REPO_TOKEN)</Label>
          <div className="flex gap-3 flex-wrap mt-1">
            <div className="flex-[2] min-w-40">
              <Label className="text-xs">代码仓库(选择后首阶段自动拉取代码)</Label>
              <OptSelect value={p.source.repoId} onChange={v => set({ source: { ...p.source, repoId: v } })} placeholder="不自动拉取"
                items={(repos.data || []).map(r => ({ value: r.id, label: r.name }))} />
            </div>
            <div className="flex-1 min-w-28">
              <Label className="text-xs">分支(空=默认)</Label>
              <Input value={p.source.branch} onChange={e => set({ source: { ...p.source, branch: e.target.value } })}
                placeholder={(repos.data || []).find(r => r.id === p.source.repoId)?.defaultBranch || 'master'} />
            </div>
            <div className="flex-[2] min-w-40">
              <Label className="text-xs">镜像仓库</Label>
              <OptSelect value={p.registryId} onChange={v => set({ registryId: v })} placeholder="不使用"
                items={(registries.data || []).map(r => ({ value: r.id, label: `${r.name} (${r.server})` }))} />
            </div>
            <div className="flex-[2] min-w-40">
              <Label className="text-xs">KUBECONFIG 凭据(kubectl 发布用)</Label>
              <OptSelect value={p.kubeCredId} onChange={v => set({ kubeCredId: v })} placeholder="不使用"
                items={(creds.data || []).filter(c => c.type === 'kubeconfig').map(c => ({ value: c.id, label: c.name }))} />
            </div>
          </div>
        </div>

        <div>
          <Label className="text-muted-foreground">环境变量(步骤命令中以 $NAME 引用; 敏感值日志自动掩码)</Label>
          <div className="mt-1">
            {p.env.map((v, i) => (
              <div key={i} className="flex gap-2 mb-2">
                <Input className="flex-1" value={v.name} placeholder="NAME" onChange={e => set({ env: p.env.map((x, idx) => idx === i ? { ...x, name: e.target.value } : x) })} />
                <Input className="flex-[2]" value={v.value} placeholder="值" onChange={e => set({ env: p.env.map((x, idx) => idx === i ? { ...x, value: e.target.value } : x) })} />
                <label className="flex items-center gap-1.5 text-sm px-1"><Checkbox checked={v.secret} onCheckedChange={c => set({ env: p.env.map((x, idx) => idx === i ? { ...x, secret: !!c } : x) })} />敏感</label>
                <Button variant="ghost" size="icon" onClick={() => set({ env: p.env.filter((_, idx) => idx !== i) })}><X /></Button>
              </div>
            ))}
            <Button variant="outline" size="sm" onClick={() => set({ env: [...p.env, { name: '', value: '', secret: false }] })}><Plus />添加变量</Button>
          </div>
        </div>

        <div>
          <Label className="text-muted-foreground">阶段(顺序执行, 任一失败后后续阶段跳过)</Label>
          <div className="mt-2 space-y-3">
            {p.stages.map((st, si) => (
              <div key={si} className="rounded-lg border p-3">
                <div className="flex gap-2 items-center flex-wrap">
                  <Badge variant="secondary" className="tabular-nums">阶段 {si + 1}</Badge>
                  <Input className="flex-1 min-w-32" value={st.name} placeholder="阶段名" onChange={e => setStage(si, { name: e.target.value })} />
                  <div className="w-40"><HostSelect value={st.host} onChange={v => setStage(si, { host: v })} /></div>
                  <Input className="flex-1 min-w-36" value={st.workspace} placeholder="工作目录(可选)" onChange={e => setStage(si, { workspace: e.target.value })} />
                  <Button variant="ghost" size="icon" onClick={() => set({ stages: move(p.stages, si, -1) })}><ChevronUp /></Button>
                  <Button variant="ghost" size="icon" onClick={() => set({ stages: move(p.stages, si, 1) })}><ChevronDown /></Button>
                  <Button variant="ghost" size="icon" className="text-destructive" onClick={async () => {
                    if (await confirm(`删除阶段「${st.name}」？`, { danger: true, okText: '删除' })) set({ stages: p.stages.filter((_, idx) => idx !== si) })
                  }}><Trash2 /></Button>
                </div>
                <div className="flex gap-3 items-center my-2 flex-wrap">
                  <label className="flex items-center gap-1.5 text-sm" title="阶段执行前暂停, 等待人工批准(发布门禁)">
                    <Checkbox checked={st.approval} onCheckedChange={c => setStage(si, { approval: !!c })} />执行前需审批
                  </label>
                  <Select onValueChange={v => {
                    const tpl = STEP_TEMPLATES.find(t => t.name === v)
                    if (tpl) setStage(si, { steps: [...st.steps, ...tpl.steps.map(s => ({ ...s }))] })
                  }}>
                    <SelectTrigger className="h-8 w-44 text-xs"><SelectValue placeholder="插入发布模板…" /></SelectTrigger>
                    <SelectContent>{STEP_TEMPLATES.map(t => <SelectItem key={t.name} value={t.name}>{t.name}</SelectItem>)}</SelectContent>
                  </Select>
                  <span className="text-xs text-muted-foreground">裸机=重启脚本 · Docker=镜像步骤 · K8s=需选 kubeconfig 凭据</span>
                </div>
                {st.steps.map((sp, i) => (
                  <div key={i} className="border rounded-lg p-2 mb-2">
                    <div className="flex gap-2 items-center mb-1.5 flex-wrap">
                      <Select value={sp.action || '__shell__'} onValueChange={v => {
                        if (v === '__shell__') { setStep(si, i, { action: undefined, params: undefined }); return }
                        const spec = (actions.data || []).find(a => a.type === v)
                        const params: Record<string, string> = {}
                        spec?.fields.forEach(f => { params[f.name] = f.placeholder || '' })
                        setStep(si, i, { action: v, params })
                      }}>
                        <SelectTrigger className="h-8 w-44 text-xs"><SelectValue placeholder="Shell 命令" /></SelectTrigger>
                        <SelectContent>
                          <SelectItem value="__shell__">Shell 命令</SelectItem>
                          {(actions.data || []).map(a => <SelectItem key={a.type} value={a.type}>{a.title}</SelectItem>)}
                        </SelectContent>
                      </Select>
                      <Input className="flex-1 min-w-32" value={sp.name} placeholder="步骤名" onChange={e => setStep(si, i, { name: e.target.value })} />
                      <Input className="w-24" type="number" min={0} max={1440} title="步骤超时(分钟)" value={sp.timeoutMin} onChange={e => setStep(si, i, { timeoutMin: +e.target.value })} />
                      <label className="flex items-center gap-1.5 text-sm" title="失败后继续执行后续步骤">
                        <Checkbox checked={sp.continueOnFail} onCheckedChange={c => setStep(si, i, { continueOnFail: !!c })} />失败继续
                      </label>
                      <Select onValueChange={v => {
                        const sc = (scripts.data || []).find(s => s.id === v)
                        if (sc) setStep(si, i, { command: sc.content })
                      }}>
                        <SelectTrigger className="h-8 w-32 text-xs" title="从脚本库插入"><SelectValue placeholder="脚本库…" /></SelectTrigger>
                        <SelectContent>{(scripts.data || []).map(s => <SelectItem key={s.id} value={s.id}>{s.name}</SelectItem>)}</SelectContent>
                      </Select>
                      <Button variant="ghost" size="icon" onClick={() => setStage(si, { steps: move(st.steps, i, -1) })}><ChevronUp /></Button>
                      <Button variant="ghost" size="icon" onClick={() => setStage(si, { steps: move(st.steps, i, 1) })}><ChevronDown /></Button>
                      <Button variant="ghost" size="icon" className="text-destructive" onClick={() => setStage(si, { steps: st.steps.filter((_, idx) => idx !== i) })}><Trash2 /></Button>
                    </div>
                    {sp.action ? (
                      <div className="grid grid-cols-1 md:grid-cols-2 gap-2">
                        {((actions.data || []).find(a => a.type === sp.action)?.fields || []).map(f => (
                          <div key={f.name}>
                            <Label className="text-xs">{f.label}{f.required ? ' *' : ''}</Label>
                            <Input className="h-8 text-xs font-mono" value={sp.params?.[f.name] || ''} placeholder={f.placeholder || f.label}
                              onChange={e => setStep(si, i, { params: { ...sp.params, [f.name]: e.target.value } })} />
                          </div>
                        ))}
                        <div className="md:col-span-2 flex items-center gap-2">
                          <span className="text-[10px] text-muted-foreground font-mono truncate flex-1" title="运行时按参数合成 shell 命令, $变量自动注入">
                            ${((actions.data || []).find(a => a.type === sp.action)?.fields || []).map(f => f.name).join(' $')}
                          </span>
                          <Button variant="ghost" size="sm" className="h-6 text-xs" title="按当前参数合成 Shell 命令, 转为普通步骤"
                            onClick={() => {
                              fetch(API.actions, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ action: sp.action, params: sp.params }) })
                                .then(r => r.json())
                                .then((d: any) => setStep(si, i, { action: undefined, params: undefined, command: d.command || sp.command }))
                                .catch(() => setStep(si, i, { action: undefined, params: undefined }))
                            }}>转 Shell</Button>
                        </div>
                      </div>
                    ) : (
                    <Textarea className="font-mono min-h-16" rows={2} value={sp.command} placeholder="shell 命令, 如: make build"
                      onChange={e => setStep(si, i, { command: e.target.value })} />
                    )}
                    <div className="flex gap-2 items-center mt-1.5 flex-wrap">
                      <span className="text-xs text-muted-foreground whitespace-nowrap"><Package className="inline size-3.5 mr-0.5" />制品:</span>
                      <Input className="flex-1 min-w-48" value={(sp.artifacts || []).join(', ')}
                        placeholder="构建产物路径, 逗号分隔, 支持 * 通配; 步骤成功后自动归档到服务端可下载"
                        onChange={e => setStep(si, i, { artifacts: e.target.value.split(',').map(x => x.trim()).filter(Boolean) })} />
                      <Select value={sp.pullArtifact || SELECT_NONE}
                        onValueChange={v => setStep(si, i, { pullArtifact: v === SELECT_NONE ? undefined : v })}>
                        <SelectTrigger className="h-8 w-48 text-xs" title="执行前把已收集制品推送到本步骤主机工作目录, 命令中用 $CICD_ARTIFACT 引用">
                          <SelectValue placeholder="不拉取制品" />
                        </SelectTrigger>
                        <SelectContent>
                          <SelectItem value={SELECT_NONE}>不拉取制品</SelectItem>
                          {priorArtifactSteps(p, si, i).map(o => <SelectItem key={o.value} value={o.value}>{o.label}</SelectItem>)}
                        </SelectContent>
                      </Select>
                    </div>
                  </div>
                ))}
                <Button variant="outline" size="sm" onClick={() => setStage(si, { steps: [...st.steps, { name: `步骤 ${st.steps.length + 1}`, command: '', continueOnFail: false, timeoutMin: 0 }] })}><Plus />添加步骤</Button>
              </div>
            ))}
            <Button variant="outline" size="sm" onClick={() => set({ stages: [...p.stages, { name: `阶段 ${p.stages.length + 1}`, host: '', workspace: '', approval: false, steps: [] }] })}><Plus />添加阶段</Button>
          </div>
        </div>

        {confirmEl}
        </div>

        <DialogFooter className="px-6 pb-4 pt-2 shrink-0">
          <Button variant="outline" onClick={onClose}>取消</Button>
          <Button disabled={saving} onClick={save}>{saving ? '保存中...' : '保存'}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// ==================== 运行分支选择弹窗 ====================

function RunBranchDialog({ pipeline, onClose, onRun }: { pipeline: Pipeline; onClose: () => void; onRun: (branch: string, params: Record<string, string>) => void }) {
  const hasSource = !!pipeline.source.repoId
  const hasParams = (pipeline.params?.length ?? 0) > 0
  const defaultBranch = pipeline.source.branch
  const [branches, setBranches] = useState<string[] | null>(null)
  const [sel, setSel] = useState('__default__')
  const [err, setErr] = useState('')
  const [loading, setLoading] = useState(hasSource)
  const [pvals, setPvals] = useState<Record<string, string>>({})

  useEffect(() => {
    if (!hasSource) return
    fetch(`${API.repoBranches}?id=${pipeline.source.repoId}`)
      .then(r => { if (!r.ok) throw new Error('HTTP ' + r.status); return r.json() })
      .then((list: string[]) => {
        setBranches(list)
        // 默认选中流水线配置的分支或仓库默认分支
        const want = pipeline.source.branch
        if (want && list.includes(want)) setSel(want)
        else if (list.includes('master')) setSel('master')
        else if (list.includes('main')) setSel('main')
      })
      .catch(e => { setErr('获取分支失败: ' + e.message); setBranches([]) })
      .finally(() => setLoading(false))
  }, [])

  const missing = (pipeline.params || []).filter(d => d.required && !(pvals[d.name] || d.default || '').trim())

  return (
    <Dialog open onOpenChange={o => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader><DialogTitle>运行流水线: {pipeline.name}</DialogTitle></DialogHeader>
        <ErrBanner msg={err} onClose={() => setErr('')} />
        {hasSource && (
        <div>
          <Label>选择分支(构建发布用)</Label>
          {loading ? <div className="text-sm text-muted-foreground py-2">获取远端分支中...</div> : (
            <Select value={sel} onValueChange={setSel}>
              <SelectTrigger><SelectValue placeholder="选择分支" /></SelectTrigger>
              <SelectContent>
                <SelectItem value="__default__">默认({defaultBranch || '流水线配置'})</SelectItem>
                {(branches || []).map(b => <SelectItem key={b} value={b}>{b}</SelectItem>)}
              </SelectContent>
            </Select>
          )}
          <div className="text-xs text-muted-foreground mt-1">首阶段将从所选分支拉取代码并构建发布</div>
        </div>
        )}
        {hasParams && (
        <div>
          <Label>构建参数</Label>
          <div className="space-y-2 mt-1">
            {(pipeline.params || []).map(d => (
              <div key={d.name}>
                <Label className="text-xs">{d.label}{d.required ? ' *' : ''}</Label>
                {d.type === 'select' && d.options?.length ? (
                  <Select value={pvals[d.name] || d.default || '__none__'} onValueChange={v => setPvals({ ...pvals, [d.name]: v === '__none__' ? '' : v })}>
                    <SelectTrigger className="h-8 text-xs"><SelectValue /></SelectTrigger>
                    <SelectContent>{d.options.map(o => <SelectItem key={o} value={o}>{o}</SelectItem>)}</SelectContent>
                  </Select>
                ) : d.type === 'textarea' ? (
                  <Textarea className="font-mono min-h-16 text-xs" value={pvals[d.name] || d.default || ''} onChange={e => setPvals({ ...pvals, [d.name]: e.target.value })} />
                ) : (
                  <Input className="h-8 text-xs" type={d.type === 'number' ? 'number' : 'text'} value={pvals[d.name] || d.default || ''} onChange={e => setPvals({ ...pvals, [d.name]: e.target.value })} placeholder={d.default ? `默认: ${d.default}` : ''} />
                )}
              </div>
            ))}
          </div>
        </div>
        )}
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>取消</Button>
          <Button disabled={loading || missing.length > 0}
            title={missing.length ? `缺少必填参数: ${missing.map(d => d.label).join(', ')}` : ''}
            onClick={() => {
              const params: Record<string, string> = {}
              for (const d of pipeline.params || []) {
                const v = (pvals[d.name] ?? d.default ?? '').trim()
                if (v) params[d.name] = v
              }
              onRun(sel === '__default__' ? '' : sel, params)
            }}><Play />运行</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// ==================== 编辑流水线弹窗(从运行详情进入) ====================

function EditPipelineDialog({ pipelineId, onClose }: { pipelineId: string; onClose: () => void }) {
  const [pipeline, setPipeline] = useState<Pipeline | null>(null)
  const [err, setErr] = useState('')
  useEffect(() => {
    fetch(`${API.pipelineGet}?id=${pipelineId}`)
      .then(r => { if (!r.ok) throw new Error('HTTP ' + r.status); return r.json() })
      .then(setPipeline)
      .catch(e => setErr(e.message))
  }, [pipelineId])

  if (err) {
    return (
      <Dialog open onOpenChange={o => !o && onClose()}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader><DialogTitle>编辑流水线</DialogTitle></DialogHeader>
          <ErrBanner msg={err} onClose={onClose} />
          <DialogFooter><Button variant="outline" onClick={onClose}>关闭</Button></DialogFooter>
        </DialogContent>
      </Dialog>
    )
  }
  if (!pipeline) return null
  return <PipelineEditor value={pipeline} onClose={onClose} onSaved={onClose} />
}

// ==================== Webhook 弹窗 ====================

function WebhookDialog({ pipeline, onClose }: { pipeline: Pipeline; onClose: () => void }) {
  const [copied, setCopied] = useState('')
  const url = `${location.origin}${API.webhook(pipeline.id)}`
  const badgeUrl = `${location.origin}${API.badge(pipeline.id)}`
  const copy = async (text: string, tag: string) => {
    try {
      await navigator.clipboard.writeText(text)
      setCopied(tag); setTimeout(() => setCopied(''), 1500)
    } catch { /* 剪贴板不可用时忽略 */ }
  }
  const curl = `curl -X POST '${url}' -H 'X-Opscore-Token: ${pipeline.trigger.secret}'`
  return (
    <Dialog open onOpenChange={o => !o && onClose()}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader><DialogTitle>Webhook 触发: {pipeline.name}</DialogTitle></DialogHeader>
        <div className="space-y-3">
          <div>
            <Label>触发地址</Label>
            <div className="flex gap-2">
              <Input className="font-mono" readOnly value={url} onFocus={e => e.target.select()} />
              <Button variant="outline" size="sm" onClick={() => copy(url, 'url')}>{copied === 'url' ? '已复制' : '复制'}</Button>
            </div>
          </div>
          <div>
            <Label>凭证(X-Opscore-Token)</Label>
            <div className="flex gap-2">
              <Input className="font-mono" readOnly value={pipeline.trigger.secret} onFocus={e => e.target.select()} />
              <Button variant="outline" size="sm" onClick={() => copy(pipeline.trigger.secret, 'secret')}>{copied === 'secret' ? '已复制' : '复制'}</Button>
            </div>
            <div className="text-xs text-muted-foreground mt-1">也可经 ?token= 或 body.secret 传递; 在 Git 仓库 Webhook 设置中填入地址与凭证即可 push 自动触发</div>
          </div>
          <div>
            <Label>curl 示例</Label>
            <Textarea className="font-mono" rows={2} readOnly value={curl} onClick={e => (e.target as HTMLTextAreaElement).select()} />
          </div>
          <div>
            <Label>状态徽章(公开只读, README 可贴)</Label>
            <div className="flex gap-2 items-center">
              <img src={badgeUrl} alt="badge" className="h-5 shrink-0" />
              <Input className="font-mono text-xs" readOnly value={badgeUrl} onFocus={e => e.target.select()} />
              <Button variant="outline" size="sm" onClick={() => copy(badgeUrl, 'badge')}>{copied === 'badge' ? '已复制' : '复制'}</Button>
            </div>
          </div>
        </div>
        <DialogFooter><Button variant="outline" onClick={onClose}>关闭</Button></DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// ==================== 运行历史 Tab ====================

function RunsTab({ onChanged, onOpenRun }: { onChanged: () => void; onOpenRun: (id: string) => void }) {
  const [filter, setFilter] = useState('')
  const { data, err, setErr, reload } = useResource<Run[]>(`${API.runs}?limit=100${filter ? `&pipeline=${filter}` : ''}`)
  const pipes = useResource<PipelineView[]>(API.pipelines)
  const runs = data || []
  const [busy, setBusy] = useState('')
  const { confirm, confirmEl } = useConfirm()

  const cancel = async (r: Run) => {
    if (!(await confirm(`取消运行 ${r.pipeline}?`, { desc: r.id, danger: true, okText: '取消运行' }))) return
    setBusy(r.id)
    try { await postJSON(API.runCancel, { runId: r.id }); reload(); onChanged() }
    catch (e: any) { setErr(e.message) } finally { setBusy('') }
  }

  return (
    <div>
      {confirmEl}
      <ErrBanner msg={err} onClose={() => setErr('')} />
      <Card>
        <CardHeader className="pb-3">
          <div className="flex items-center justify-between gap-2 flex-wrap">
            <CardTitle className="tabular-nums">运行历史 ({runs.length})</CardTitle>
            <OptSelect className="w-52" value={filter} onChange={setFilter} placeholder="全部流水线"
              items={(pipes.data || []).map(p => ({ value: p.id, label: p.name }))} />
          </div>
        </CardHeader>
        <CardContent>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>流水线</TableHead><TableHead>触发</TableHead><TableHead>状态</TableHead>
                <TableHead className="min-w-24">进度</TableHead><TableHead>开始时间</TableHead>
                <TableHead>耗时</TableHead><TableHead className="w-32">操作</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {runs.length === 0 && (
                <TableRow><TableCell colSpan={7} className="h-24 text-center text-muted-foreground">暂无运行记录</TableCell></TableRow>
              )}
              {runs.map(r => (
                <TableRow key={r.id}>
                  <TableCell className="font-semibold">{r.pipeline}</TableCell>
                  <TableCell><Badge variant="secondary">{TRIGGER_TEXT[r.trigger] || r.trigger}</Badge></TableCell>
                  <TableCell>
                    <StatusBadge status={r.status} suffix={r.status === 'running' && r.canceling ? '(取消中)' : undefined} />
                    {r.error && <div className="text-xs text-muted-foreground max-w-64">{r.error}</div>}
                  </TableCell>
                  <TableCell>
                    <div className="flex items-center gap-2 min-w-36">
                      <StageSegments stages={r.stages} />
                      <span className="text-xs text-muted-foreground tabular-nums shrink-0">{r.progress || 0}%</span>
                    </div>
                  </TableCell>
                  <TableCell className="text-xs">{fmtTime(r.startedAt)}</TableCell>
                  <TableCell className="tabular-nums">{fmtDur(r.durationMs)}</TableCell>
                  <TableCell>
                    <div className="flex gap-1">
                      <Button variant="outline" size="sm" onClick={() => onOpenRun(r.id)}>详情</Button>
                      {(r.status === 'running' || r.status === 'queued') && (
                        <Button variant="destructive" size="sm" disabled={!!busy} onClick={() => cancel(r)}>取消</Button>
                      )}
                      {r.status !== 'running' && r.status !== 'queued' && (
                        <Button variant="outline" size="icon" className="size-8 text-destructive" title="删除记录"
                          onClick={async () => {
                            if (!(await confirm('删除这条运行记录？', { desc: '日志与制品将一并删除。', danger: true, okText: '删除' }))) return
                            setBusy(r.id)
                            try { await postJSON(API.runDelete, { runId: r.id }); reload(); onChanged() } catch (e: any) { setErr(e.message) } finally { setBusy('') }
                          }}><Trash2 /></Button>
                      )}
                    </div>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </CardContent>
      </Card>
    </div>
  )
}

// ==================== 运行详情(SSE 实时日志) ====================

function RunDetail({ runId, onClose, onChanged, onRerun }: { runId: string; onClose: () => void; onChanged?: () => void; onRerun?: (newRunId: string) => void }) {
  const [run, setRun] = useState<Run | null>(null)
  const [editOpen, setEditOpen] = useState(false)
  const [busy, setBusy] = useState('')
  const { confirm, confirmEl } = useConfirm()
  const [lines, setLines] = useState<string[]>([])
  const [err, setErr] = useState('')
  const logRef = useRef<HTMLDivElement>(null)
  const stickBottom = useRef(true)
  const [now, setNow] = useState(Date.now())

  // 运行中每秒跳表, 驱动节点流上的实时耗时
  useEffect(() => {
    if (!run || !['running', 'queued'].includes(run.status)) return
    const t = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(t)
  }, [run?.status])

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
    fetch(`${API.runGet}?id=${runId}`).then(r => r.json()).then(setRun).catch(() => {})
    fetch(`${API.runLog}?id=${runId}`).then(r => r.json())
      .then((d: { content: string; offset: number }) => {
        if (!alive) return
        if (d.content) setLines(d.content.replace(/\n$/, '').split('\n'))
        stream(d.offset)
      })
      .catch(() => stream(0))

    const stream = (offset: number) => {
      const headers: Record<string, string> = { 'Content-Type': 'application/json' }
      const t = localStorage.getItem('opscore-token')
      if (t) headers.Authorization = `Bearer ${t}`
      fetch(API.runStream, {
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

  useEffect(() => {
    if (run && ['success', 'failed', 'canceled'].includes(run.status)) return
    const t = setInterval(() => {
      fetch(`${API.runGet}?id=${runId}`).then(r => r.json()).then(setRun).catch(() => {})
    }, 1500)
    return () => clearInterval(t)
  }, [runId, run?.status])

  const cancel = () => {
    postJSON(API.runCancel, { runId }).catch(e => setErr(e.message))
  }
  const rerun = async () => {
    setBusy('rerun')
    try {
      const d = await postJSON<{ run: Run }>(API.pipelineRun, { id: run!.pipelineId, branch: run!.branch || '' })
      setErr('')
      if (onRerun) onRerun(d.run.id) // 详情切换到新运行
      else onClose()
      onChanged?.()
    } catch (e: any) { setErr('重新执行失败: ' + e.message) } finally { setBusy('') }
  }
  // 回滚到此版本: 以该次运行的 commit 重新触发完整流水线(引擎钉住提交拉取)
  const rollback = async () => {
    const c = run!.commit?.split(/\s/)[0] || ''
    if (!c) return
    if (!(await confirm('回滚到此版本？', { desc: `将重新拉取 ${c} 并执行完整流水线(部署随之回退)。`, okText: '回滚' }))) return
    setBusy('rollback')
    try {
      const d = await postJSON<{ run: Run }>(API.pipelineRun, { id: run!.pipelineId, branch: run!.branch || '', commit: c })
      setErr('')
      if (onRerun) onRerun(d.run.id)
      onChanged?.()
    } catch (e: any) { setErr('回滚失败: ' + e.message) } finally { setBusy('') }
  }
  const removeRun = async () => {
    if (!(await confirm('删除这条运行记录？', { desc: '日志与制品将一并删除。', danger: true, okText: '删除' }))) return
    setBusy('del')
    try {
      await postJSON(API.runDelete, { runId })
      setErr(''); onChanged?.(); onClose()
    } catch (e: any) { setErr(e.message) } finally { setBusy('') }
  }

  // 点击节点流中的步骤 → 日志滚动到该步骤的起始位置
  const scrollToStep = (si: number, j: number, name: string) => {
    const el = logRef.current
    if (!el) return
    const lineEls = el.querySelectorAll('.log-line')
    let curStage = 0
    let found: Element | null = null
    for (const l of Array.from(lineEls)) {
      const t = l.textContent || ''
      const m = t.match(/\[阶段 (\d+)\/\d+\]/)
      if (m) curStage = parseInt(m[1])
      if (curStage === si + 1 && t.includes(`[步骤 ${j + 1}/`) && t.includes(name)) found = l
    }
    found?.scrollIntoView({ block: 'center', behavior: 'smooth' })
  }

  if (!run) return null
  const active = run.status === 'running' || run.status === 'queued'

  return (
    <Dialog open onOpenChange={o => !o && onClose()}>
      <DialogContent className="sm:max-w-6xl h-[92vh] flex flex-col overflow-hidden">
        <DialogHeader className="px-6 pt-6 pb-2 shrink-0">
          <div className="flex items-start justify-between gap-3 flex-wrap">
            <div>
              <DialogTitle className="flex items-center gap-2 flex-wrap">
                {run.pipeline}
                <StatusBadge status={run.status} suffix={run.status === 'running' && run.canceling ? '(取消中)' : undefined} />
              </DialogTitle>
              <div className="text-xs text-muted-foreground font-normal tabular-nums mt-0.5">
                {TRIGGER_TEXT[run.trigger] || run.trigger} · {fmtDur(run.durationMs)}{run.branch ? ` · ${run.branch}` : ''} · {run.id}
                {run.runParams && Object.keys(run.runParams).length > 0 && (
                  <span className="font-mono"> · {Object.entries(run.runParams).map(([k, v]) => `${k}=${v}`).join(' ')}</span>
                )}
              </div>
            </div>
            <div className="flex gap-1.5 shrink-0">
              <Button variant="outline" size="sm" disabled={busy !== ''} title="以本次运行相同分支重新执行"
                onClick={rerun}><RefreshCw />重新执行</Button>
              {run.commit && run.trigger !== 'rollback' && (
                <Button variant="outline" size="sm" disabled={busy !== ''} title={`钉住 ${run.commit.split(/\s/)[0]} 重新拉取并执行(部署回退)`}
                  onClick={rollback}><GitCommitHorizontal />回滚此版本</Button>
              )}
              <Button variant="outline" size="sm" title="编辑流水线定义, 保存后可重新执行"
                onClick={() => setEditOpen(true)}><Pencil />编辑</Button>
              {run.status !== 'running' && run.status !== 'queued' && (
                <Button variant="outline" size="sm" disabled={busy !== ''} title="删除本条记录(含日志与制品)"
                  onClick={removeRun}><Trash2 />删除</Button>
              )}
            </div>
          </div>
        </DialogHeader>

        {run.error && <div className="mx-6 mb-1 shrink-0"><ErrBanner msg={run.error} /></div>}
        <ErrBanner msg={err} onClose={() => setErr('')} className="mx-6 shrink-0" />

        <div className="flex-1 overflow-y-auto px-6 space-y-3 min-h-0">
        <div className="rounded-lg border bg-background/60 px-4 py-2">
          <StageFlow stages={run.stages} now={now} onStepClick={(si, j) => {
            const st = run.stages[si]
            if (st && st.steps[j]) scrollToStep(si, j, st.steps[j].name)
          }} />
        </div>
        {run.commit && (
          <div className="text-xs text-muted-foreground font-mono truncate" title={run.commit}>
            <GitCommitHorizontal className="inline size-3.5 mr-1 -mt-0.5" />{run.commit}
          </div>
        )}


        {run.stages.map((st, i) => (
          <div key={i} className={cn('rounded-lg border', st.status === 'waiting' && 'border-warn')}>
            <div className="flex items-center justify-between gap-2 flex-wrap px-3 py-2 border-b bg-muted/30 rounded-t-lg">
              <div className="flex items-center gap-2 font-semibold">
                <StatusBadge status={st.status} />
                阶段 {i + 1}: {st.name}
                <span className="text-xs font-normal text-muted-foreground tabular-nums">
                  {fmtDur(stageElapsedMs(st, now))}
                </span>
              </div>
              <div className="flex items-center gap-2 text-xs text-muted-foreground">
                {st.host ? `主机 ${st.host}` : '本机'}{st.workspace ? ` · ${st.workspace}` : ''}
                {st.status === 'waiting' && active && (
                  <span className="flex gap-1.5">
                    <Button size="sm" onClick={() => postJSON(API.runApprove, { runId, approve: true }).catch(e => setErr(e.message))}>
                      <Check />批准执行
                    </Button>
                    <Button variant="destructive" size="sm" onClick={() => postJSON(API.runApprove, { runId, approve: false }).catch(e => setErr(e.message))}>
                      <X />拒绝
                    </Button>
                  </span>
                )}
              </div>
            </div>
            <div className="px-3 py-1">
              {st.steps.map((sp, j) => (
                <div key={j} className="flex items-center gap-3 py-1.5 border-b last:border-b-0 text-sm flex-wrap">
                  <span className="min-w-36">
                    <span className="font-mono text-xs text-muted-foreground tabular-nums">{String(j + 1).padStart(2, '0')}</span>{' '}
                    <span className="font-medium">{sp.name}</span>
                  </span>
                  <span className="flex-1 min-w-40 font-mono text-xs text-muted-foreground truncate" title={sp.command}>{sp.command}</span>
                  <span className="flex flex-col gap-1">
                    <StatusBadge status={sp.status} />
                    {sp.artifacts && sp.artifacts.length > 0 && (
                      <span className="flex gap-1.5 flex-wrap">
                        {sp.artifacts.map(a => (
                          <Button key={a.file} variant="outline" size="sm" className="h-6 text-xs tabular-nums"
                            title={`${a.paths} · 点击下载 ${a.file}`}
                            onClick={() => {
                              const t = localStorage.getItem('opscore-token')
                              window.open(`${API.artifactDownload}?run=${run.id}&file=${a.file}${t ? `&token=${encodeURIComponent(t)}` : ''}`)
                            }}
                          ><Package />{fmtSize(a.size)}</Button>
                        ))}
                      </span>
                    )}
                  </span>
                  <span className="text-xs text-muted-foreground tabular-nums">exit {sp.status === 'pending' ? '-' : sp.exitCode}</span>
                  <span className="text-xs text-muted-foreground tabular-nums">{fmtDur(sp.durationMs)}</span>
                </div>
              ))}
            </div>
          </div>
        ))}

        <div className="flex items-center justify-between">
          <h3 className="font-semibold text-sm tabular-nums">执行日志({lines.length} 行)</h3>
          {active && <span className="text-xs text-muted-foreground">实时跟随时勿上滚</span>}
        </div>
        {/* 终端样式保留 legacy 主题适配类 */}
        <div className="log-text" ref={logRef} onScroll={onScroll} style={{ maxHeight: 320, overflowY: 'auto' }}>
          {lines.length === 0 && <div className="log-loading">暂无日志输出</div>}
          {lines.map((l, i) => <div key={i} className="log-line">{l}</div>)}
        </div>

        </div>

        <DialogFooter className="px-6 pb-4 pt-2 shrink-0">
          {active && <Button variant="destructive" onClick={cancel}>取消运行</Button>}
          <Button variant="outline" onClick={onClose}>关闭</Button>
        </DialogFooter>

        {editOpen && <EditPipelineDialog pipelineId={run.pipelineId} onClose={() => setEditOpen(false)} />}
        {confirmEl}
      </DialogContent>
    </Dialog>
  )
}

// ==================== Stage View(Jenkins 式阶段矩阵) ====================

const STAGE_CELL_COLOR: Record<string, string> = {
  success: 'var(--ok)', failed: 'var(--danger)', running: 'var(--accent)', waiting: 'var(--warn)',
  canceled: 'var(--text-dim)', skipped: 'var(--text-dim)', pending: 'var(--text-dim)',
}

function StageViewCard({ onOpenRun }: { onOpenRun: (id: string) => void }) {
  const pipes = useResource<PipelineView[]>(API.pipelines)
  const list = pipes.data || []
  const [pid, setPid] = useState('') // 空 = 全部流水线(默认)
  const all = pid === ''
  const sel = list.find(p => p.id === pid) || null
  const runsRes = useResource<Run[]>(all ? `${API.runs}?limit=20` : `${API.runs}?limit=15&pipeline=${pid}`)
  const runsNewest = runsRes.data || []
  const runs = runsNewest.slice().reverse() // 旧→新, 矩阵视图用(#1 在最下)

  // 列 = 流水线定义的阶段名(稳定顺序); 仅单流水线矩阵用
  const cols = (sel?.stages || []).map(st => st.name)
  const cellOf = (r: Run, col: string, idx: number): StageRun | undefined =>
    r.stages.find(x => x.name === col) || r.stages[idx]
  const stageDur = (st?: StageRun) => st ? st.steps.reduce((a, s) => a + (s.durationMs || 0), 0) : 0

  // 每列平均耗时
  const avgOf = (col: string, idx: number) => {
    let sum = 0, n = 0
    for (const r of runs) {
      const st = cellOf(r, col, idx)
      if (st && ['success', 'failed'].includes(st.status)) { sum += stageDur(st); n++ }
    }
    return n > 0 ? sum / n : 0
  }

  return (
    <Card className="h-full flex flex-col overflow-hidden min-h-0">
      <CardHeader className="pb-2 shrink-0">
        <div className="flex items-center justify-between gap-2 flex-wrap">
          <CardTitle>Stage View</CardTitle>
          <OptSelect className="w-52" value={pid} onChange={setPid} placeholder="全部流水线"
            items={list.map(p => ({ value: p.id, label: p.name }))} />
        </div>
      </CardHeader>
      <CardContent className="flex-1 min-h-0 flex flex-col">
        {all ? (
          // 全部流水线视图: 阶段结构各不相同, 用通用的阶段进度段条呈现
          runsNewest.length === 0 ? (
            <div className="text-sm text-muted-foreground py-4">暂无运行记录</div>
          ) : (
            <div className="flex-1 min-h-0 overflow-auto hover-scroll">
              <Table className="[&_td]:py-1 [&_th]:py-1.5">
                <TableHeader>
                  <TableRow>
                    <TableHead>构建名</TableHead>
                    <TableHead className="min-w-32">阶段进度</TableHead>
                    <TableHead>状态</TableHead>
                    <TableHead>耗时</TableHead>
                    <TableHead>时间</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {runsNewest.map(r => (
                    <TableRow key={r.id} className="cursor-pointer" onClick={() => onOpenRun(r.id)}>
                      <TableCell className="text-xs font-semibold max-w-36 truncate" title={r.pipeline}>{r.pipeline}</TableCell>
                      <TableCell><StageSegments stages={r.stages} /></TableCell>
                      <TableCell><StatusBadge status={r.status} suffix={r.status === 'running' && r.canceling ? '(取消中)' : undefined} /></TableCell>
                      <TableCell className="text-xs tabular-nums">{fmtDur(r.durationMs)}</TableCell>
                      <TableCell className="text-xs text-muted-foreground tabular-nums whitespace-nowrap">
                        {r.startedAt ? new Date(r.startedAt).toLocaleString('zh-CN', { month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit' }) : '-'}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>
          )
        ) : !sel ? (
          <div className="text-sm text-muted-foreground py-4">暂无流水线</div>
        ) : runs.length === 0 ? (
          <div className="text-sm text-muted-foreground py-4">该流水线暂无运行记录</div>
        ) : (
          <div className="flex-1 min-h-0 overflow-auto hover-scroll">
            <Table className="[&_td]:py-1 [&_th]:py-1.5">
              <TableHeader>
                <TableRow>
                  <TableHead className="sticky left-0 bg-card min-w-24 z-10">构建</TableHead>
                  {cols.map((c, i) => (
                    <TableHead key={c + i} className="text-center min-w-24">
                      <div>{c}</div>
                      <div className="text-[10px] font-normal text-muted-foreground tabular-nums">
                        平均 {avgOf(c, i) > 0 ? fmtDur(Math.round(avgOf(c, i))) : '-'}
                      </div>
                    </TableHead>
                  ))}
                </TableRow>
              </TableHeader>
              <TableBody>
                {runs.map((r, ri) => (
                  <TableRow key={r.id}>
                    <TableCell className="sticky left-0 bg-card z-10">
                      <button className="text-left hover:text-accent leading-tight" onClick={() => onOpenRun(r.id)}>
                        <div className="text-xs font-semibold tabular-nums">#{runs.length - ri} <span className="font-normal text-muted-foreground">{new Date(r.startedAt || '').toLocaleString('zh-CN', { month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit' })}</span></div>
                      </button>
                    </TableCell>
                    {cols.map((c, i) => {
                      const st = cellOf(r, c, i)
                      if (!st || st.status === 'pending' || st.status === 'skipped') {
                        return <TableCell key={c + i} className="text-center text-muted-foreground/30">—</TableCell>
                      }
                      const color = STAGE_CELL_COLOR[st.status] || 'var(--text-dim)'
                      return (
                        <TableCell key={c + i} className="p-1">
                          <button
                            className="w-full rounded-md px-2 py-1 text-xs tabular-nums hover:opacity-75 transition-opacity"
                            title={`${c}: ${statusText(st.status)}`}
                            style={{
                              background: `color-mix(in srgb, ${color} 13%, transparent)`,
                              color: st.status === 'success' || st.status === 'failed' || st.status === 'running' || st.status === 'waiting' ? color : undefined,
                            }}
                            onClick={() => onOpenRun(r.id)}
                          >
                            {st.status === 'running' ? '运行中' : st.status === 'waiting' ? '待审批' : fmtDur(stageDur(st))}
                          </button>
                        </TableCell>
                      )
                    })}
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        )}
      </CardContent>
    </Card>
  )
}

// ==================== 脚本库 Tab ====================

function ScriptsTab() {
  const { data, err, setErr, reload } = useResource<Script[]>(API.scripts)
  const scripts = data || []
  const [editing, setEditing] = useState<Script | null>(null)
  const [busy, setBusy] = useState('')
  const { confirm, confirmEl } = useConfirm()

  const remove = async (s: Script) => {
    if (!(await confirm(`删除脚本「${s.name}」？`, { danger: true, okText: '删除' }))) return
    setBusy(s.id)
    try { await postJSON(API.scriptDelete, { id: s.id }); reload() } catch (e: any) { setErr(e.message) } finally { setBusy('') }
  }

  return (
    <div>
      {confirmEl}
      <ErrBanner msg={err} onClose={() => setErr('')} />
      <Card>
        <CardHeader className="pb-3">
          <div className="flex items-center justify-between gap-2">
            <CardTitle className="tabular-nums">脚本库 ({scripts.length})</CardTitle>
            <Button size="sm" onClick={() => setEditing({ id: '', name: '', description: '', content: '', updatedAt: '' })}>
              <FileCode2 />新建脚本
            </Button>
          </div>
        </CardHeader>
        <CardContent>
          <Table>
            <TableHeader>
              <TableRow><TableHead>名称</TableHead><TableHead>描述</TableHead><TableHead>更新时间</TableHead><TableHead className="w-36">操作</TableHead></TableRow>
            </TableHeader>
            <TableBody>
              {scripts.length === 0 && (
                <TableRow><TableCell colSpan={4} className="h-24 text-center text-muted-foreground">暂无脚本; 流水线步骤中可直接引用</TableCell></TableRow>
              )}
              {scripts.map(s => (
                <TableRow key={s.id}>
                  <TableCell className="font-semibold">{s.name}</TableCell>
                  <TableCell className="text-xs text-muted-foreground">{s.description}</TableCell>
                  <TableCell className="text-xs">{s.updatedAt ? new Date(s.updatedAt).toLocaleString() : '-'}</TableCell>
                  <TableCell>
                    <div className="flex gap-1">
                      <Button variant="outline" size="sm" onClick={() => setEditing(s)}>编辑</Button>
                      <Button variant="destructive" size="sm" disabled={!!busy} onClick={() => remove(s)}>删除</Button>
                    </div>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </CardContent>
      </Card>

      {editing && (
        <Dialog open onOpenChange={o => !o && setEditing(null)}>
          <DialogContent className="sm:max-w-2xl">
            <DialogHeader>
              <DialogTitle>{editing.id ? `编辑脚本: ${editing.name}` : '新建脚本'}</DialogTitle>
            </DialogHeader>
            <div className="flex gap-3">
              <div className="flex-1">
                <Label>名称 *</Label>
                <Input value={editing.name} onChange={e => setEditing({ ...editing, name: e.target.value })} />
              </div>
              <div className="flex-[2]">
                <Label>描述</Label>
                <Input value={editing.description} onChange={e => setEditing({ ...editing, description: e.target.value })} />
              </div>
            </div>
            <div>
              <Label>脚本内容(POSIX shell, 可使用流水线注入的环境变量)</Label>
              <Textarea className="font-mono" rows={12} value={editing.content}
                onChange={e => setEditing({ ...editing, content: e.target.value })}
                placeholder={'#!/bin/sh\nset -e\ndocker compose pull && docker compose up -d'} />
            </div>
            <DialogFooter>
              <Button variant="outline" onClick={() => setEditing(null)}>取消</Button>
              <Button onClick={() => {
                postJSON(API.scriptSave, editing)
                  .then(() => { setEditing(null); reload() })
                  .catch(e => setErr(e.message))
              }}>保存</Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      )}
    </div>
  )
}

// ==================== 仓库 Tab(代码仓库 + 镜像仓库) ====================

function ReposTab() {
  const repos = useResource<Repo[]>(API.repos)
  const registries = useResource<Registry[]>(API.registries)
  const creds = useResource<Credential[]>(API.credentials)
  const [editingRepo, setEditingRepo] = useState<Repo | null>(null)
  const [editingReg, setEditingReg] = useState<Registry | null>(null)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState('')
  const { confirm, confirmEl } = useConfirm()
  const toast = useToast()

  const reloadAll = () => { repos.reload(); registries.reload(); creds.reload() }

  // 测试连接: 结果用顶部居中 Toast 弹出(不再原地铺原始 ls-remote 文本)
  const testRepo = (r: Repo) => {
    toast.info('正在测试连接...')
    postJSON<{ ok: boolean; output?: string; error?: string }>(API.repoTest, { id: r.id })
      .then(d => {
        if (!d.ok) { toast.error(`连接失败: ${(d.error || '未知错误').slice(0, 100)}`); return }
        const branch = (d.output || '').split('\n')
          .map(l => l.match(/refs\/heads\/(\S+)/)).find(m => m)?.[1]
        toast.success(`连接成功${branch ? ` · 分支 ${branch}` : ''}`)
      })
      .catch(e => toast.error('连接失败: ' + e.message))
  }
  const testReg = (r: Registry) => {
    toast.info('正在测试连接...')
    postJSON<{ ok: boolean; output?: string; error?: string }>(API.registryTest, { id: r.id })
      .then(d => d.ok ? toast.success(d.output || '服务存活') : toast.error(`连接失败: ${d.error || '未知错误'}`))
      .catch(e => toast.error('连接失败: ' + e.message))
  }
  const removeRepo = async (r: Repo) => {
    if (!(await confirm(`删除仓库「${r.name}」？`, { danger: true, okText: '删除' }))) return
    setBusy(r.id)
    try { await postJSON(API.repoDelete, { id: r.id }); repos.reload() } catch (e: any) { setErr(e.message) } finally { setBusy('') }
  }
  const removeReg = async (r: Registry) => {
    if (!(await confirm(`删除镜像仓库「${r.name}」？`, { danger: true, okText: '删除' }))) return
    setBusy(r.id)
    try { await postJSON(API.registryDelete, { id: r.id }); registries.reload() } catch (e: any) { setErr(e.message) } finally { setBusy('') }
  }

  const credName = (id: string) => (creds.data || []).find(c => c.id === id)?.name || '-'

  return (
    <div>
      {confirmEl}
      <ErrBanner msg={err} onClose={() => setErr('')} />

      <Card className="mb-4">
        <CardHeader className="pb-3">
          <div className="flex items-center justify-between gap-2">
            <CardTitle className="tabular-nums">代码仓库 ({(repos.data || []).length})</CardTitle>
            <Button size="sm" onClick={() => setEditingRepo({ id: '', name: '', url: '', credId: '', defaultBranch: 'master' })}><Plus />新建代码仓库</Button>
          </div>
        </CardHeader>
        <CardContent>
          <Table>
            <TableHeader>
              <TableRow><TableHead>名称</TableHead><TableHead>地址</TableHead><TableHead>凭据</TableHead><TableHead>默认分支</TableHead><TableHead className="w-52">操作</TableHead></TableRow>
            </TableHeader>
            <TableBody>
              {(repos.data || []).length === 0 && <TableRow><TableCell colSpan={5} className="h-16 text-center text-muted-foreground">暂无代码仓库</TableCell></TableRow>}
              {(repos.data || []).map(r => (
                <TableRow key={r.id}>
                  <TableCell className="font-semibold">{r.name}</TableCell>
                  <TableCell className="text-xs font-mono">{r.url}</TableCell>
                  <TableCell className="text-xs">{credName(r.credId)}</TableCell>
                  <TableCell className="text-xs">{r.defaultBranch}</TableCell>
                  <TableCell>
                    <div className="flex gap-1">
                      <Button variant="outline" size="sm" disabled={!!busy} onClick={() => testRepo(r)}>测试</Button>
                      <Button variant="outline" size="sm" onClick={() => setEditingRepo(r)}>编辑</Button>
                      <Button variant="destructive" size="sm" disabled={!!busy} onClick={() => removeRepo(r)}>删除</Button>
                    </div>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </CardContent>
      </Card>

      <Card>
        <CardHeader className="pb-3">
          <div className="flex items-center justify-between gap-2">
            <CardTitle className="tabular-nums">镜像仓库 ({(registries.data || []).length})</CardTitle>
            <Button size="sm" onClick={() => setEditingReg({ id: '', name: '', server: '', credId: '' })}><Plus />新建镜像仓库</Button>
          </div>
        </CardHeader>
        <CardContent>
          <Table>
            <TableHeader>
              <TableRow><TableHead>名称</TableHead><TableHead>地址</TableHead><TableHead>凭据</TableHead><TableHead className="w-52">操作</TableHead></TableRow>
            </TableHeader>
            <TableBody>
              {(registries.data || []).length === 0 && <TableRow><TableCell colSpan={4} className="h-16 text-center text-muted-foreground">暂无镜像仓库</TableCell></TableRow>}
              {(registries.data || []).map(r => (
                <TableRow key={r.id}>
                  <TableCell className="font-semibold">{r.name}</TableCell>
                  <TableCell className="text-xs font-mono">{r.server}</TableCell>
                  <TableCell className="text-xs">{credName(r.credId)}</TableCell>
                  <TableCell>
                    <div className="flex gap-1">
                      <Button variant="outline" size="sm" disabled={!!busy} onClick={() => testReg(r)}>测试</Button>
                      <Button variant="outline" size="sm" onClick={() => setEditingReg(r)}>编辑</Button>
                      <Button variant="destructive" size="sm" disabled={!!busy} onClick={() => removeReg(r)}>删除</Button>
                    </div>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </CardContent>
      </Card>

      {editingRepo && (
        <Dialog open onOpenChange={o => !o && setEditingRepo(null)}>
          <DialogContent className="sm:max-w-xl">
            <DialogHeader><DialogTitle>{editingRepo.id ? '编辑代码仓库' : '新建代码仓库'}</DialogTitle></DialogHeader>
            <div className="flex gap-3">
              <div className="flex-1">
                <Label>名称 *</Label>
                <Input value={editingRepo.name} onChange={e => setEditingRepo({ ...editingRepo, name: e.target.value })} placeholder="如: 业务后端" />
              </div>
              <div className="flex-1">
                <Label>默认分支</Label>
                <Input value={editingRepo.defaultBranch} onChange={e => setEditingRepo({ ...editingRepo, defaultBranch: e.target.value })} placeholder="master" />
              </div>
            </div>
            <div>
              <Label>仓库地址 *(https:// 或 git@ / ssh://)</Label>
              <Input className="font-mono" value={editingRepo.url} onChange={e => setEditingRepo({ ...editingRepo, url: e.target.value })} placeholder="https://git.example.com/team/app.git" />
            </div>
            <div>
              <Label>访问凭据(https 私有库需 git 类型凭据; ssh 形态依赖主机 ssh key)</Label>
              <OptSelect value={editingRepo.credId} onChange={v => setEditingRepo({ ...editingRepo, credId: v })} placeholder="无(公开库 / 主机 ssh key)"
                items={(creds.data || []).filter(c => c.type === 'git').map(c => ({ value: c.id, label: c.name }))} />
            </div>
            <DialogFooter>
              <Button variant="outline" onClick={() => setEditingRepo(null)}>取消</Button>
              <Button onClick={() => postJSON(API.repoSave, editingRepo).then(() => { setEditingRepo(null); reloadAll() }).catch(e => setErr(e.message))}>保存</Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      )}

      {editingReg && (
        <Dialog open onOpenChange={o => !o && setEditingReg(null)}>
          <DialogContent className="sm:max-w-xl">
            <DialogHeader><DialogTitle>{editingReg.id ? '编辑镜像仓库' : '新建镜像仓库'}</DialogTitle></DialogHeader>
            <div className="flex gap-3">
              <div className="flex-1">
                <Label>名称 *</Label>
                <Input value={editingReg.name} onChange={e => setEditingReg({ ...editingReg, name: e.target.value })} placeholder="如: 生产 Harbor" />
              </div>
              <div className="flex-1">
                <Label>地址 *(域名[:端口], 不含协议)</Label>
                <Input className="font-mono" value={editingReg.server} onChange={e => setEditingReg({ ...editingReg, server: e.target.value })} placeholder="registry.example.com:5000" />
              </div>
            </div>
            <div>
              <Label>访问凭据</Label>
              <OptSelect value={editingReg.credId} onChange={v => setEditingReg({ ...editingReg, credId: v })} placeholder="无(匿名)"
                items={(creds.data || []).filter(c => c.type === 'registry').map(c => ({ value: c.id, label: c.name }))} />
            </div>
            <DialogFooter>
              <Button variant="outline" onClick={() => setEditingReg(null)}>取消</Button>
              <Button onClick={() => postJSON(API.registrySave, editingReg).then(() => { setEditingReg(null); reloadAll() }).catch(e => setErr(e.message))}>保存</Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      )}
    </div>
  )
}

// ==================== 凭据 Tab ====================

function CredentialsTab() {
  const { data, err, setErr, reload } = useResource<Credential[]>(API.credentials)
  const creds = data || []
  const [editing, setEditing] = useState<(Credential & { data?: string }) | null>(null)
  const [busy, setBusy] = useState('')
  const { confirm, confirmEl } = useConfirm()

  const remove = async (c: Credential) => {
    if (!(await confirm(`删除凭据「${c.name}」？`, { desc: '引用它的仓库/流水线将回退为无凭据。', danger: true, okText: '删除' }))) return
    setBusy(c.id)
    try { await postJSON(API.credentialDelete, { id: c.id }); reload() } catch (e: any) { setErr(e.message) } finally { setBusy('') }
  }

  const isKube = editing?.type === 'kubeconfig'

  return (
    <div>
      {confirmEl}
      <ErrBanner msg={err} onClose={() => setErr('')} />
      <Card>
        <CardHeader className="pb-3">
          <div className="flex items-center justify-between gap-2">
            <CardTitle className="tabular-nums">凭据中心 ({creds.length})</CardTitle>
            <Button size="sm" onClick={() => setEditing({ id: '', name: '', type: 'git', username: '', server: '', hasData: false, note: '', updatedAt: '', data: '' })}><Plus />新建凭据</Button>
          </div>
        </CardHeader>
        <CardContent>
          <Table>
            <TableHeader>
              <TableRow><TableHead>名称</TableHead><TableHead>类型</TableHead><TableHead>用户名</TableHead><TableHead>备注</TableHead><TableHead>更新时间</TableHead><TableHead className="w-36">操作</TableHead></TableRow>
            </TableHeader>
            <TableBody>
              {creds.length === 0 && <TableRow><TableCell colSpan={6} className="h-24 text-center text-muted-foreground">暂无凭据; 密文保存后仅写不读, 日志中自动掩码</TableCell></TableRow>}
              {creds.map(c => (
                <TableRow key={c.id}>
                  <TableCell className="font-semibold">{c.name}</TableCell>
                  <TableCell><Badge variant="secondary">{CRED_TYPE_TEXT[c.type] || c.type}</Badge></TableCell>
                  <TableCell className="text-xs">{c.username || '-'}</TableCell>
                  <TableCell className="text-xs text-muted-foreground">{c.note || c.server || '-'}</TableCell>
                  <TableCell className="text-xs">{c.updatedAt ? new Date(c.updatedAt).toLocaleString() : '-'}</TableCell>
                  <TableCell>
                    <div className="flex gap-1">
                      <Button variant="outline" size="sm" onClick={() => setEditing({ ...c, data: '' })}>编辑</Button>
                      <Button variant="destructive" size="sm" disabled={!!busy} onClick={() => remove(c)}>删除</Button>
                    </div>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </CardContent>
      </Card>

      {editing && (
        <Dialog open onOpenChange={o => !o && setEditing(null)}>
          <DialogContent className="sm:max-w-xl">
            <DialogHeader><DialogTitle>{editing.id ? `编辑凭据: ${editing.name}` : '新建凭据'}</DialogTitle></DialogHeader>
            <div className="flex gap-3 flex-wrap">
              <div className="flex-1 min-w-36">
                <Label>名称 *</Label>
                <Input value={editing.name} onChange={e => setEditing({ ...editing, name: e.target.value })} placeholder="如: gitlab-ci-token" />
              </div>
              <div className="flex-1 min-w-40">
                <Label>类型 *</Label>
                <Select value={editing.type} onValueChange={v => setEditing({ ...editing, type: v })} disabled={!!editing.id}>
                  <SelectTrigger><SelectValue /></SelectTrigger>
                  <SelectContent>
                    <SelectItem value="git">代码库(token/密码)</SelectItem>
                    <SelectItem value="registry">镜像仓库(用户名+密码)</SelectItem>
                    <SelectItem value="kubeconfig">K8s kubeconfig</SelectItem>
                    <SelectItem value="generic">通用密文</SelectItem>
                  </SelectContent>
                </Select>
              </div>
              {(editing.type === 'git' || editing.type === 'registry') && (
                <div className="flex-1 min-w-36">
                  <Label>用户名</Label>
                  <Input value={editing.username || ''} onChange={e => setEditing({ ...editing, username: e.target.value })}
                    placeholder={editing.type === 'registry' ? '如: robot$ci' : '可空(纯 token)'} />
                </div>
              )}
            </div>
            <div>
              <Label>{isKube ? 'kubeconfig 内容 *' : '密文 *'}{editing.id ? ' (留空保持原值)' : ''}</Label>
              {isKube ? (
                <Textarea className="font-mono" rows={10} value={editing.data || ''} onChange={e => setEditing({ ...editing, data: e.target.value })} placeholder={'apiVersion: v1\nclusters: ...'} />
              ) : (
                <Input className="font-mono" type="password" value={editing.data || ''} onChange={e => setEditing({ ...editing, data: e.target.value })} placeholder={editing.id ? '留空保持原值' : 'token / 密码'} />
              )}
            </div>
            {editing.type === 'generic' && (
              <div>
                <Label>备注</Label>
                <Input value={editing.note || ''} onChange={e => setEditing({ ...editing, note: e.target.value })} placeholder="用途说明" />
              </div>
            )}
            <p className="text-xs text-muted-foreground">
              安全说明: 密文保存后不可回读(仅可覆盖); 列表只显示"已配置"标记; 运行日志中自动掩码; kubeconfig 在目标主机落盘为 600 权限临时文件并在运行后清理。
            </p>
            <DialogFooter>
              <Button variant="outline" onClick={() => setEditing(null)}>取消</Button>
              <Button onClick={() => {
                const payload: any = { ...editing }
                delete payload.hasData
                delete payload.updatedAt
                postJSON(API.credentialSave, payload)
                  .then(() => { setEditing(null); reload() })
                  .catch(e => setErr(e.message))
              }}>保存</Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      )}
    </div>
  )
}

// ==================== 概览 Tab ====================

// ==================== 概览 Tab(单屏仪表盘: 指标条 + Stage View + 紧凑活动列表) ====================

function OverviewTab({ data, onOpenRun, onMore }: { data: any; onOpenRun: (id: string) => void; onMore?: () => void }) {
  if (!data) return <div className="text-center text-muted-foreground py-8">加载中...</div>
  const ok24 = data.success24h ?? 0
  const fail24 = data.failed24h ?? 0
  const total24 = ok24 + fail24
  const waiting = data.waitingApproval ?? 0
  const waitingRun: Run | undefined = (data.recentRuns || []).find((r: Run) => r.status === 'waiting')

  return (
    <div className="space-y-3 lg:h-[calc(100vh-14rem)] lg:flex lg:flex-col">
      {/* 指标条: 一行内联, 不再一张卡一个数字 */}
      <div className="flex gap-2 flex-wrap lg:shrink-0">
        <StatChip label="流水线" value={String(data.pipelines)} />
        <StatChip label="运行中 / 排队" value={`${data.running} / ${data.queued}`} />
        <StatChip label="等待审批" value={String(waiting)} warn={waiting > 0} />
        <StatChip label="24h 成功" value={String(ok24)} tone="text-ok" />
        <StatChip label="24h 失败" value={String(fail24)} tone={fail24 > 0 ? 'text-destructive' : undefined} />
        <StatChip label="24h 成功率" value={total24 > 0 ? `${Math.round(ok24 * 100 / total24)}%` : '-'} />
      </div>

      {/* 待审批: 可点击直达运行详情, 没有则不占位 */}
      {waiting > 0 && (
        <button
          onClick={() => { if (waitingRun) onOpenRun(waitingRun.id); else onMore?.() }}
          className="w-full shrink-0 rounded-lg border border-warn/40 bg-warn/10 px-3 py-2 text-sm text-warn flex items-center justify-between hover:bg-warn/15 transition-colors">
          <span className="flex items-center gap-2"><Pause className="size-4 shrink-0" />{waiting} 个运行待审批{waitingRun ? ', 点击直接处理' : ''}</span>
          <ChevronRight className="size-4" />
        </button>
      )}

      {/* 双栏底对齐撑满剩余高度, 内容各自框内滑动(整页不滚) */}
      <div className="grid grid-cols-1 lg:grid-cols-3 gap-3 items-stretch lg:flex-1 lg:min-h-0">
        <div className="lg:col-span-2 min-w-0 min-h-0">
          <StageViewCard onOpenRun={onOpenRun} />
        </div>
        <div className="flex flex-col gap-3 min-w-0 min-h-0">
          <div className="lg:flex-[3] min-h-0">
            <RecentActivity runs={data.recentRuns} onOpenRun={onOpenRun} onMore={onMore} />
          </div>
          <div className="lg:flex-[2] min-h-0">
            <AuditCard />
          </div>
        </div>
      </div>
    </div>
  )
}

const AUDIT_TEXT: Record<string, string> = {
  'trigger(manual)': '手动触发', 'trigger(webhook)': 'Webhook 触发', 'trigger(cron)': '定时触发',
  'trigger(rollback)': '回滚触发', approve: '批准', reject: '拒绝', cancel: '取消运行',
  delete_run: '删除记录', delete_pipeline: '删除流水线', maintenance: '维护模式',
}
const AUDIT_DANGER = new Set(['reject', 'delete_run', 'delete_pipeline'])

// 操作审计卡: 引擎级动作链(触发/审批/取消/删除/回滚/维护), 环形保留最近 200 条
function AuditCard() {
  const { data } = useResource<{ ts: string; actor: string; action: string; target: string; detail: string }[]>(API.audit)
  const list = (data || []).slice(0, 8)
  return (
    <Card className="h-full flex flex-col overflow-hidden min-h-0">
      <CardHeader className="pb-2 shrink-0">
        <CardTitle className="text-sm">操作审计</CardTitle>
      </CardHeader>
      <CardContent className="pt-0 flex-1 min-h-0 hover-scroll">
        <div className="flex flex-col">
          {list.length === 0 && <div className="text-sm text-muted-foreground py-3">暂无记录</div>}
          {list.map((a, i) => (
            <div key={i} className="flex items-center gap-2 px-2 py-1 rounded-md hover:bg-muted/60 text-xs">
              <span className={cn('shrink-0 font-medium', AUDIT_DANGER.has(a.action) && 'text-destructive', a.action === 'approve' && 'text-ok', a.action.startsWith('trigger') && 'text-accent')}>
                {AUDIT_TEXT[a.action] || a.action}
              </span>
              <span className="truncate flex-1 min-w-0 text-muted-foreground" title={`${a.target} ${a.detail}`}>{a.target}{a.detail ? ` · ${a.detail}` : ''}</span>
              <span className="shrink-0 text-muted-foreground tabular-nums">{a.ts}</span>
            </div>
          ))}
        </div>
      </CardContent>
    </Card>
  )
}

function StatChip({ label, value, warn, tone }: { label: string; value: string; warn?: boolean; tone?: string }) {
  return (
    <div className="rounded-lg border bg-card/60 px-3 py-1.5 flex items-baseline gap-2">
      <span className="text-sm text-muted-foreground whitespace-nowrap">{label}</span>
      <span className={cn('text-sm font-bold tabular-nums', warn && 'text-warn', tone)}>{value}</span>
    </div>
  )
}

// 紧凑活动列表: 状态点 + 名称 + 触发 + 耗时 + 时间, 行高减半
function RecentActivity({ runs, onOpenRun, onMore }: { runs: Run[]; onOpenRun: (id: string) => void; onMore?: () => void }) {
  const list = (runs || []).slice(0, 9)
  return (
    <Card className="h-full flex flex-col overflow-hidden">
      <CardHeader className="pb-2 shrink-0">
        <div className="flex items-center justify-between gap-2">
          <CardTitle className="text-sm">最近运行</CardTitle>
          {onMore && <Button variant="ghost" size="sm" className="h-7 text-xs px-2" onClick={onMore}>全部 <ChevronRight className="size-3.5" /></Button>}
        </div>
      </CardHeader>
      <CardContent className="pt-0 flex-1 min-h-0 hover-scroll">
        <div className="flex flex-col">
          {list.length === 0 && <div className="text-sm text-muted-foreground py-4">暂无数据</div>}
          {list.map((r: Run) => (
            <button key={r.id} onClick={() => onOpenRun(r.id)}
              className="flex items-center gap-2 px-2 py-1.5 rounded-md hover:bg-muted/60 text-left transition-colors">
              <LastRunDot run={r} />
              <span className="text-sm font-medium truncate flex-1 min-w-0">{r.pipeline}</span>
              {r.status === 'running' && <span className="text-[10px] text-muted-foreground tabular-nums shrink-0">{r.progress || 0}%</span>}
              <Badge variant="secondary" className="px-1.5 py-0 text-[10px] shrink-0">{TRIGGER_TEXT[r.trigger] || r.trigger}</Badge>
              <span className="text-xs text-muted-foreground tabular-nums w-16 text-right shrink-0">{fmtDur(r.durationMs)}</span>
              <span className="text-[11px] text-muted-foreground tabular-nums w-24 text-right shrink-0 hidden lg:block">
                {r.startedAt ? new Date(r.startedAt).toLocaleString('zh-CN', { month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit' }) : '-'}
              </span>
            </button>
          ))}
        </div>
      </CardContent>
    </Card>
  )
}
