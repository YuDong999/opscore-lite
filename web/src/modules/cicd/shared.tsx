// ── CI/CD 模块共享层(shadcn 版): API 常量 / useResource / useConfirm / 状态徽标 ──
// 审查落地: R1 API 常量单一来源 · R2 列表加载样板收敛为 hook · R3 弹窗统一 Dialog
//           U1 confirm() 改为 Promise 化 AlertDialog · U3 等宽数字 · 5 主题自动适配

import { cn } from '@/lib/utils'
import { Badge } from '@/components/ui/badge'

// ── API 路径常量(单一来源) ──
export const API = {
  pipelines: '/api/cicd/pipelines',
  pipelineGet: '/api/cicd/pipeline/get',
  pipelineSave: '/api/cicd/pipeline/save',
  pipelineDelete: '/api/cicd/pipeline/delete',
  pipelineRun: '/api/cicd/pipeline/run',
  pipelineExport: '/api/cicd/pipeline/export',
  pipelineImport: '/api/cicd/pipeline/import',
  pipelineNextFire: '/api/cicd/pipeline/nextfire',
  runCancel: '/api/cicd/run/cancel',
  runGet: '/api/cicd/run/get',
  runLog: '/api/cicd/run/log',
  runStream: '/api/cicd/run/stream',
  runApprove: '/api/cicd/run/approve',
  runDelete: '/api/cicd/run/delete',
  runs: '/api/cicd/runs',
  overview: '/api/cicd/overview',
  audit: '/api/cicd/audit',
  nginxProbe: '/api/cicd/nginx/probe',
  nginxApply: '/api/cicd/nginx/apply',
  badge: (id: string) => `/api/cicd/badge/${id}.svg`,
  webhook: (id: string) => `/api/cicd/webhook/${id}`,
  artifactDownload: '/api/cicd/artifact/download',
  credentials: '/api/cicd/credentials',
  credentialSave: '/api/cicd/credential/save',
  credentialDelete: '/api/cicd/credential/delete',
  repos: '/api/cicd/repos',
  repoSave: '/api/cicd/repo/save',
  repoDelete: '/api/cicd/repo/delete',
  repoTest: '/api/cicd/repo/test',
  repoBranches: '/api/cicd/repo/branches',
  actions: '/api/cicd/actions',
  runLogDownload: '/api/cicd/run/log/download',
  registries: '/api/cicd/registries',
  registrySave: '/api/cicd/registry/save',
  registryDelete: '/api/cicd/registry/delete',
  registryTest: '/api/cicd/registry/test',
  scripts: '/api/cicd/scripts',
  scriptSave: '/api/cicd/script/save',
  scriptDelete: '/api/cicd/script/delete',
  hosts: '/api/ansible/hosts',
} as const

// Radix Select 不允许空串 value, 可选"无"语义统一用该哨兵
export const SELECT_NONE = '__none__'

// ── 类型(与 internal/cicd 模型对应) ──
export interface Var { name: string; value: string; secret: boolean }
export interface Trigger { manual: boolean; webhook: boolean; secret: string; cron: string }
export interface Step {
  name: string; command: string; continueOnFail: boolean; timeoutMin: number
  artifacts?: string[]; pullArtifact?: string
  action?: string; params?: Record<string, string>
}
export interface ActionField { name: string; label: string; type: string; placeholder?: string; required?: boolean }
export interface ActionSpec { type: string; title: string; category: string; fields: ActionField[] }
export interface Stage { name: string; host: string; workspace: string; approval: boolean; steps: Step[] }
export interface Source { repoId: string; branch: string }
export interface ParamDef { name: string; label: string; type: string; default?: string; options?: string[]; required?: boolean }
export interface Pipeline {
  id: string; name: string; description: string
  env: Var[]; trigger: Trigger; stages: Stage[]
  params?: ParamDef[]
  source: Source; registryId: string; kubeCredId: string
  timeoutMin: number; maxRuns: number; notifyURL: string
  notifyChannel?: string; notifySecret?: string
}
export interface PipelineView extends Pipeline {
  stageCount: number
  lastRun?: Run
  nextCron?: string
}
export interface Artifact { step: string; file: string; size: number; paths: string }
export interface StepQuality { tests: number; failed: number; skipped: number }
export interface StepRun { name: string; command: string; status: string; exitCode: number; startedAt?: string; durationMs: number; artifacts?: Artifact[]; quality?: StepQuality }
export interface StageRun { name: string; host: string; workspace: string; status: string; steps: StepRun[]; approval?: boolean }
export interface Run {
  id: string; pipelineId: string; pipeline: string; trigger: string; status: string
  commit?: string; branch?: string; runParams?: Record<string, string>
  canceling?: boolean; progress: number; stages: StageRun[]; startedAt?: string; finishedAt?: string
  durationMs: number; error?: string
}
export interface HostOpt { id: string; label: string }
export interface Credential { id: string; name: string; type: string; username?: string; server?: string; hasData: boolean; note?: string; updatedAt: string }
export interface Repo { id: string; name: string; url: string; credId: string; defaultBranch: string; note?: string }
export interface Registry { id: string; name: string; server: string; credId: string; note?: string }
export interface Script { id: string; name: string; description: string; content: string; updatedAt: string }

// useResource / useConfirm / useTableSort / useLocalJSON / SortHead 已提升到 L2 lib/hooks,
// 此处仅转发, 既有调用点零改动。
export { useResource } from '../../lib/hooks/useResource'
export { useConfirm } from '../../lib/hooks/useConfirm'
// ── 状态徽标: 颜色引用主题变量, color-mix 派生底色(全部主题自动适配) ──
export const STATUS_TEXT: Record<string, string> = {
  queued: '排队中', running: '运行中', waiting: '等待审批', success: '成功', failed: '失败',
  canceled: '已取消', skipped: '已跳过', pending: '等待',
}
export const STATUS_COLOR: Record<string, string> = {  // 状态→色 全模块唯一权威
  success: 'var(--ok)', failed: 'var(--danger)', running: 'var(--accent)',
  queued: 'var(--warn)', waiting: 'var(--warn)',
  canceled: 'var(--text-dim)', skipped: 'var(--text-dim)', pending: 'var(--text-dim)',
}
export function statusText(s: string) { return STATUS_TEXT[s] || s }

export function StatusBadge({ status, suffix }: { status: string; suffix?: string }) {
  const color = STATUS_COLOR[status] || 'var(--text-dim)'
  return (
    <Badge
      variant="outline"
      className="border-transparent font-medium"
      style={{ color, background: `color-mix(in srgb, ${color} 14%, transparent)` }}
    >
      {statusText(status)}{suffix}
    </Badge>
  )
}

// ── 错误横幅(可关闭) ──
export function ErrBanner({ msg, onClose, className }: { msg: string; onClose?: () => void; className?: string }) {
  if (!msg) return null
  return (
    <div className={cn('banner banner-err flex items-center justify-between gap-3', className)}>
      <span className="whitespace-pre-wrap">{msg}</span>
      {onClose && <button className="opacity-70 hover:opacity-100" onClick={onClose}>✕</button>}
    </div>
  )
}

// ── 格式化 ──
// fmtDur / fmtTime / fmtSize 已上移到公共层 ../../lib/format，由调用方直接 import，
// 不再在模块的 shared 里转一层（模块 shared 只放「本模块私有」的东西）。
export const TRIGGER_TEXT: Record<string, string> = { manual: '手动', webhook: 'Webhook', cron: '定时', rollback: '回滚' }

// useTableSort / SortHead 已提升到 L2 lib/hooks, 此处仅转发(含类型)。
export { useTableSort, SortHead } from '../../lib/hooks/useTableSort'
export type { SortState } from '../../lib/hooks/useTableSort'
// useLocalJSON 亦在 L2
export { useLocalJSON } from '../../lib/hooks/useLocalJSON'

// ── 状态定序(cicd 语义: 活的/异常在前) ──
const STATUS_ORDER: Record<string, number> = {
  running: 0, queued: 1, waiting: 2, failed: 3, success: 4, canceled: 5, skipped: 6, pending: 7,
}
export const statusWeight = (s: string) => STATUS_ORDER[s] ?? 99
