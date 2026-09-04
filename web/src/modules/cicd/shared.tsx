// ── CI/CD 模块共享层(shadcn 版): API 常量 / useResource / useConfirm / 状态徽标 ──
// 审查落地: R1 API 常量单一来源 · R2 列表加载样板收敛为 hook · R3 弹窗统一 Dialog
//           U1 confirm() 改为 Promise 化 AlertDialog · U3 等宽数字 · 5 主题自动适配

import { useCallback, useEffect, useState } from 'react'
import { getJSON } from '../../api/client'
import { cn } from '@/lib/utils'
import { Badge } from '@/components/ui/badge'
import { Alert, AlertDescription } from '@/components/ui/alert'
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent,
  AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
} from '@/components/ui/alert-dialog'

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
  runs: '/api/cicd/runs',
  overview: '/api/cicd/overview',
  webhook: (id: string) => `/api/cicd/webhook/${id}`,
  artifactDownload: '/api/cicd/artifact/download',
  credentials: '/api/cicd/credentials',
  credentialSave: '/api/cicd/credential/save',
  credentialDelete: '/api/cicd/credential/delete',
  repos: '/api/cicd/repos',
  repoSave: '/api/cicd/repo/save',
  repoDelete: '/api/cicd/repo/delete',
  repoTest: '/api/cicd/repo/test',
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
}
export interface Stage { name: string; host: string; workspace: string; approval: boolean; steps: Step[] }
export interface Source { repoId: string; branch: string }
export interface Pipeline {
  id: string; name: string; description: string
  env: Var[]; trigger: Trigger; stages: Stage[]
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
export interface StepRun { name: string; command: string; status: string; exitCode: number; startedAt?: string; durationMs: number; artifacts?: Artifact[] }
export interface StageRun { name: string; host: string; workspace: string; status: string; steps: StepRun[] }
export interface Run {
  id: string; pipelineId: string; pipeline: string; trigger: string; status: string
  commit?: string
  canceling?: boolean; progress: number; stages: StageRun[]; startedAt?: string; finishedAt?: string
  durationMs: number; error?: string
}
export interface HostOpt { id: string; label: string }
export interface Credential { id: string; name: string; type: string; username?: string; server?: string; hasData: boolean; note?: string; updatedAt: string }
export interface Repo { id: string; name: string; url: string; credId: string; defaultBranch: string; note?: string }
export interface Registry { id: string; name: string; server: string; credId: string; note?: string }
export interface Script { id: string; name: string; description: string; content: string; updatedAt: string }

// ── 列表加载样板收敛(R2): 数据 + 错误 + 手动刷新 ──
export function useResource<T>(url: string) {
  const [data, setData] = useState<T | null>(null)
  const [err, setErr] = useState('')
  const load = useCallback(() => {
    getJSON<T>(url).then(d => { setData(d); setErr('') }).catch(e => setErr(e.message))
  }, [url])
  useEffect(load, [load])
  return { data, err, setErr, reload: load }
}

// ── confirm() 的 Promise 化替代(U1): 与原生调用形态一致 ──
interface ConfirmState {
  title: string
  desc?: string
  okText?: string
  danger?: boolean
  resolve: (ok: boolean) => void
}

export function useConfirm() {
  const [state, setState] = useState<ConfirmState | null>(null)
  const confirm = useCallback((title: string, opts?: { desc?: string; okText?: string; danger?: boolean }) =>
    new Promise<boolean>(resolve => setState({ title, ...opts, resolve })), [])
  const element = (
    <AlertDialog open={!!state}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{state?.title}</AlertDialogTitle>
          {state?.desc && <AlertDialogDescription>{state.desc}</AlertDialogDescription>}
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel onClick={() => { state?.resolve(false); setState(null) }}>取消</AlertDialogCancel>
          <AlertDialogAction
            className={cn(state?.danger && 'bg-destructive text-destructive-foreground hover:bg-destructive/90')}
            onClick={() => { state?.resolve(true); setState(null) }}
          >{state?.okText ?? '确定'}</AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
  return { confirm, confirmEl: element }
}

// ── 状态徽标: 颜色引用主题变量, color-mix 派生底色(全部主题自动适配) ──
export const STATUS_TEXT: Record<string, string> = {
  queued: '排队中', running: '运行中', waiting: '等待审批', success: '成功', failed: '失败',
  canceled: '已取消', skipped: '已跳过', pending: '等待',
}
const STATUS_COLOR: Record<string, string> = {
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
    <Alert variant="destructive" className={cn('mb-3', className)}>
      <AlertDescription className="flex items-center justify-between gap-3">
        <span className="whitespace-pre-wrap">{msg}</span>
        {onClose && <button className="opacity-70 hover:opacity-100" onClick={onClose}>✕</button>}
      </AlertDescription>
    </Alert>
  )
}

// ── 格式化 ──
export function fmtDur(ms: number): string {
  if (!ms) return '-'
  if (ms < 1000) return `${ms}ms`
  if (ms < 60000) return `${(ms / 1000).toFixed(1)}s`
  const m = Math.floor(ms / 60000), s = Math.round((ms % 60000) / 1000)
  return `${m}m${s}s`
}
export function fmtTime(t?: string) { return t ? new Date(t).toLocaleString() : '-' }
export function fmtSize(n: number): string {
  if (n >= 1 << 20) return `${(n / (1 << 20)).toFixed(1)}MB`
  if (n >= 1 << 10) return `${(n / (1 << 10)).toFixed(1)}KB`
  return `${n}B`
}
export const TRIGGER_TEXT: Record<string, string> = { manual: '手动', webhook: 'Webhook', cron: '定时' }
