// 查询结果表格: 渲染 columns/rows, 支持溢出截断、null/对象友好显示、单元格编辑。
// 增强: 表头点击排序(本地)、单元格点击复制、导出 CSV/JSON/XLSX(后端流式下载)。
// 编辑(dbx CellDetailDialog 同模式): 单击仅选中; 入口 = 双击详情弹窗内的「编辑」+ 右键「编辑单元格」;
// 提交走 onEdit 单条变更(父级负责 SQL 预览/确认/执行/刷新); 无行内编辑器与底部保存行(省一行高度给数据)。

import { useState, useEffect, useLayoutEffect, useCallback, useMemo, useRef } from 'react'
import { createPortal } from 'react-dom'
import ContextMenu, { type ContextMenuItem } from './ContextMenu'
import { ActionIcon } from './ActionIcon'
import { SqlPreviewBody } from './SqlPreview'
import { useConfirm } from '../../lib/hooks/useConfirm'
import { useToast } from '../Toast'
import { resolveTypeVisualKind, isNumericType } from '../../lib/dataGridTypes'

// ── 数据网格通用类型(单一事实源在本组件; DatabaseManager/api 类型 re-export 自此处) ──
export type ExportFormat = 'csv' | 'json' | 'xlsx'

export interface ColumnInfo {
  name: string
  type: string
  nullable: boolean
  key?: string
  default?: string
  comment?: string
}

export interface GridStatement { sql: string; type: string; rows: number; affected: number; durationMs: number; error?: string }

// 外键对(一条外键的一个列映射; 复合外键 = 同一 name/refTable 的多行)
export interface FkPair { column: string; refTable: string; refColumn: string; name?: string }

export interface QueryResult {
  columns: string[]
  rows: any[][]
  rowCount: number
  affected: number
  durationMs: number
  truncated: boolean
  error?: string
  isEditable?: boolean
  statements?: GridStatement[]
}

// 后端交互通过注入完成(L2 不依赖任何模块的 api 层):
// onExport: 后端流式导出; applyRowWrite: 行级写(置 NULL/删行)。
// 这里没有"给我执行这段 SQL"的入口: 手搓 SQL 会绕过标识符引用与写安全链, 写操作只能报意图。
export interface DataGridBackend {
  onExport?: (sql: string, format: ExportFormat) => Promise<{ fileName: string; size: number }>
  // 行级写: 前端只说"哪一行(哪一列)做什么", SQL 由后端按方言拼 —— 与单元格编辑(apply-edit)同一条通道,
  // 标识符引用/字面量转义/写锁拦截/审计都在后端。dryRun 只取 sql 供预览, 不落库。
  applyRowWrite?: (req: { op: 'set-null' | 'delete-row'; row: number; col?: number; dryRun: boolean })
    => Promise<{ ok: boolean; sql?: string; affected?: number; error?: string }>
  // 生成值里"前端答不了"的两件事也只能报意图: nextId = 该列在表内的最大值+1(必须回字符串,
  // 19 位雪花 ID 过一遍 JSON number 会被 double 截精度); idWorker = 本机派生的雪花 workerId。
  nextId?: (col: number) => Promise<string>
  idWorker?: () => Promise<number>
}

// 网格内"待提交变更"(批量提交模式): 编辑/置 NULL/删行先攒着, 由底部变更条统一走 onCommitBatch。
// 父级负责 SQL 预览/确认/执行/刷新 —— 这里同样不拼 SQL, 只报"哪行哪列改成什么/删哪行"。
export interface GridChange {
  kind: 'update' | 'delete'
  row: number      // 当前页行下标(与 result.rows 同序)
  col?: number     // update 必填
  value?: any      // update 后的值(null = 置为 NULL)
  oldValue?: any
}

// 分页档位唯一来源: DataPanel 数据页 pager 与 DataGrid 查询结果 footer 共用,
// 所有"每页行数"控件选项一致(默认 100 在档位内, 显示值不会与真实值背离)。
export const PAGE_SIZES = [10, 20, 50, 100, 200, 500, 1000]

// 右键「筛选」能用的运算符: 只收"能从被点单元格直接定出操作数"的那些。
// IN / NOT IN 填单值等价于 = / !=, BETWEEN 要两个操作数(单元格只给一个) —— 这三个留在
// 过滤工作台里填(那边有第二个输入框和列表 placeholder), 不在菜单里放假入口。
const FILTER_FROM_CELL: ReadonlyArray<{ op: string; label: string }> = [
  { op: '=', label: '筛选此值' },
  { op: '!=', label: '排除此值' },
  { op: '>', label: '大于此值' },
  { op: '>=', label: '大于等于此值' },
  { op: '<', label: '小于此值' },
  { op: '<=', label: '小于等于此值' },
  { op: 'LIKE', label: '包含此值' },
  { op: 'NOT LIKE', label: '不包含此值' },
]

// ── 右键「生成值」: 生成器 + 按列类型放行哪些项 ──
// 值一律灌进详情弹窗的编辑草稿, 不直接写库 —— 走既有那条 草稿 → 保存修改 → SQL 预览 → 确认 的路。
// NULL 不在这里: 详情弹窗的约定是"空串提交为空字符串; 置 NULL 用右键菜单"(见那个 textarea 的
// placeholder), 置为 NULL 已有单独一条即时写, 再放一份进生成值就是两个入口两套语义。
const pad2 = (n: number) => String(n).padStart(2, '0')
/** 本地时区的 MySQL 字面量(不用 toISOString: 那是 UTC, 会把 23:00 写成前一天) */
function nowLiterals(d = new Date()) {
  const date = `${d.getFullYear()}-${pad2(d.getMonth() + 1)}-${pad2(d.getDate())}`
  return { date, dateTime: `${date} ${pad2(d.getHours())}:${pad2(d.getMinutes())}:${pad2(d.getSeconds())}` }
}
function uuidV4() {
  const c = globalThis.crypto
  // randomUUID 只在安全上下文存在; 本机 http://localhost 算, 但部署到 http://内网IP 就是 undefined
  // —— 那种场合退回 getRandomValues(非安全上下文也可用)手搓 v4。
  if (c?.randomUUID) return c.randomUUID()
  const b = new Uint8Array(16)
  c.getRandomValues(b)
  b[6] = (b[6] & 0x0f) | 0x40
  b[8] = (b[8] & 0x3f) | 0x80
  const h = Array.from(b, x => x.toString(16).padStart(2, '0')).join('')
  return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`
}
// 放行矩阵: 类型取不到(查询页结果无列类型)按 unknown 全放行; 数值/布尔/时空列不给空串和 UUID
const GEN_TEXT_KINDS = new Set(['string', 'identifier', 'unknown'])
const GEN_TIME_KINDS = new Set(['temporal', 'string', 'unknown'])
// 发号只给整数列(decimal 给个雪花号是灾难); 类型未知时放行 —— 查询页结果没有列类型
const GEN_ID_KINDS = new Set(['integer', 'unknown'])

// ── 雪花 ID ──
// 布局与 dbx 完全一致: epoch=2021-01-01, (ms-epoch)<<22 | workerId<<12 | seq(0~4095)。
// 换布局等于换 ID 语义, 所以这里没有"自选"的余地, 只有 workerId 来源可选(见下)。
const SNOWFLAKE_EPOCH_MS = 1_609_459_200_000
const SNOWFLAKE_MAX_SEQ = 4095n
let sfLastMs = -1
let sfSeq = 0n
function snowflakeId(workerId: number): string {
  let ts = Math.max(Date.now(), SNOWFLAKE_EPOCH_MS)
  if (ts < sfLastMs) ts = sfLastMs          // 时钟回拨: 停在最后一毫秒继续排, 绝不发重复号
  if (ts === sfLastMs) {
    sfSeq += 1n
    if (sfSeq > SNOWFLAKE_MAX_SEQ) { ts += 1; sfSeq = 0n }  // 同毫秒 4096 个用满 → 推到下一毫秒
  } else {
    sfSeq = 0n
  }
  sfLastMs = ts
  return (((BigInt(ts) - BigInt(SNOWFLAKE_EPOCH_MS)) << 22n) | (BigInt(workerId) << 12n) | sfSeq).toString()
}

// workerId 来源可选, 默认"本机派生": 业界最普遍的默认就是这么算的(MyBatis-Plus / Hutool 的
// IdWorker 用 IP/MAC + 进程号, 零配置又能把同机多进程错开)。dbx 用纯随机是因为它没有后端;
// 我们有, 所以随机只留作一个选项。要"绝对不撞"得上 ZK/DB 注册分配 —— 那是服务端的活, 不在这里做。
type WorkerPolicy = 'machine' | 'random' | number
const WORKER_POLICY_KEY = 'opscore.db.snowflakeWorker'
function readWorkerPolicy(): WorkerPolicy {
  const raw = localStorage.getItem(WORKER_POLICY_KEY)
  if (raw === null || raw === '') return 'machine'
  if (raw === 'random') return 'random'
  const n = Number(raw)
  return Number.isInteger(n) && n >= 0 && n <= 1023 ? n : 'machine'
}
function writeWorkerPolicy(p: WorkerPolicy) {
  localStorage.setItem(WORKER_POLICY_KEY, p === 'machine' ? '' : String(p))
}
function workerPolicyLabel(p: WorkerPolicy): string {
  return p === 'machine' ? '本机派生' : p === 'random' ? '随机' : `指定 ${p}`
}

// 四个详情浮层(单元格/行/转置/列)共用的骨架: 遮罩 + 面板 + 头(标题+右侧按钮) + body。
// 内容差异走 title/head/children 三个槽位; 视觉类名与原手写逐一对应, 零变化。
function DetailOverlay({ title, onClose, head, children }: {
  title: React.ReactNode
  onClose: () => void
  head?: React.ReactNode
  children: React.ReactNode
}) {
  return createPortal(
    <div className="qo-overlay" onClick={onClose}>
      <div className="db-cell-detail" onClick={e => e.stopPropagation()}>
        <div className="db-cell-detail-head">
          <span className="db-cell-detail-col">{title}</span>
          {head}
          <button className="btn-glass-soft btn-glass-soft-sm" onClick={onClose}>✕</button>
        </div>
        <div className="db-cell-detail-body">{children}</div>
      </div>
    </div>,
    document.body,
  )
}

function renderCell(v: any): string {
  if (v === null || v === undefined) return ''
  if (typeof v === 'object') {
    try { return JSON.stringify(v) } catch { return String(v) }
  }
  if (typeof v === 'string' && v.length > 200) return v.substring(0, 200) + '...'
  return String(v)
}

export default function DataGrid({ result, onEdit, connId, sql, exportSql, columnTypes, columnMeta, onFilter, onClearFilters, onSortDatabase, onAfterWrite, foreignKeys, onFkJump, hidePager, emptyState, backend, onCommitBatch }: {
  result: QueryResult | null
  onEdit?: (changes: Array<{ row: number, col: number, newValue: any, oldValue: any }>) => void | Promise<void>
  connId?: string
  sql?: string
  columnTypes?: (string | undefined)[]  // 列类型(数据浏览模式展示在列头第二行)
  columnMeta?: ColumnInfo[]             // 完整列元数据(describe; 注释/可空/键)
  onFilter?: (col: string, op: string, value: string) => void
  onClearFilters?: () => void
  onSortDatabase?: (col: string, dir: 'asc' | 'desc') => void
  onAfterWrite?: () => void        // 写操作(置NULL等)成功后的刷新回调
  foreignKeys?: FkPair[]           // 本表外键(来自 table-meta); 配合 onFkJump 在单元格菜单给出跳转项
  onFkJump?: (refTable: string, conds: Array<{ col: string; op: string; value: string }>) => void
  exportSql?: string                 // 导出用 SQL(数据页=当前页 LIMIT/OFFSET; 缺省用 sql)
  hidePager?: boolean                // 不渲染内部分页脚(数据页由外层 pager 负责)
  backend?: DataGridBackend          // 后端能力注入(导出/写 SQL); 不提供时相应菜单项隐藏或静默跳过
  // 批量提交模式(dbx dataGrid 同款): 给了它就"先攒着", 底部出变更条统一保存/回滚;
  // 不传则保持原行为(每次编辑立即回调父级), 其它调用方零影响。cancelled=用户在预览弹窗里取消了。
  onCommitBatch?: (changes: GridChange[]) => Promise<{ ok: boolean; cancelled?: boolean; error?: string; affected?: number; badCells?: Array<{ row: number; col: number }> }>
  emptyState?: {                     // 空结果占位(可选, 不传时行为不变)
    hint: string
    actionLabel?: string
    onAction?: () => void
  }
}) {
  const [isEditable, setIsEditable] = useState(false)
  const [sortCol, setSortCol] = useState<number | null>(null)
  const [sortAsc, setSortAsc] = useState(true)
  const [copied, setCopied] = useState('')
  // 写操作(置 NULL / 删除行)确认走公共 useConfirm + SQL 预览 —— 与编辑保存同一条呈现;
  // 失败与写锁走 toast。原先是原生 confirm()/alert(), 弹出来的东西跟全站不是一个体系。
  const { confirm, confirmEl } = useConfirm()
  const toast = useToast()
  const [detail, setDetail] = useState<{ r: number; c: number } | null>(null)
  // 详情弹窗编辑态(dbx 同模式): detailEdit=编辑中, detailDraft=草稿
  const [detailEdit, setDetailEdit] = useState(false)
  const [detailDraft, setDetailDraft] = useState('')
  const [savingEdit, setSavingEdit] = useState(false)
  const [rowDetail, setRowDetail] = useState<number | null>(null)
  const [colDetail, setColDetail] = useState<number | null>(null)
  const [fieldFilter, setFieldFilter] = useState('')
  const [transpose, setTranspose] = useState<{ r: number } | null>(null)
  const [ctxMenu, setCtxMenu] = useState<{ row: number; col: number; x: number; y: number } | null>(null)
  // ── 待提交变更(仅批量模式下生效) ──
  const batchMode = !!onCommitBatch
  const [pending, setPending] = useState<GridChange[]>([])
  const [savingBatch, setSavingBatch] = useState(false)
  // 父级校验失败时点名的单元格(如列长度超限): 标红, 用户改一下就清掉
  const [badCells, setBadCells] = useState<Set<string>>(new Set())
  // 同一个格子改了多次只留最后一次; 删行覆盖该行所有单元格改动
  const markPending = useCallback((c: GridChange) => {
    setBadCells(prev => { const n = new Set(prev); n.delete(`${c.row}:${c.col}`); return n })
    setPending(prev => [
      ...prev.filter(p => !(p.row === c.row && p.kind === c.kind && (c.kind === 'delete' || p.col === c.col))),
      c,
    ])
  }, [])
  const pendingCell = useCallback((row: number, col: number): GridChange | null => {
    for (let i = pending.length - 1; i >= 0; i--) {
      const c = pending[i]
      if (c.row === row && c.kind === 'update' && c.col === col) return c
    }
    return null
  }, [pending])
  const pendingDeleted = useCallback((row: number) => pending.some(c => c.kind === 'delete' && c.row === row), [pending])
  const [rowCtxMenu, setRowCtxMenu] = useState<{ row: number; x: number; y: number } | null>(null)
  const [colHeadMenu, setColHeadMenu] = useState<{ col: number; x: number; y: number } | null>(null)
  // ── 列操作三件套: 虚拟滚动(定高窗口) / 列宽(colgroup) / 冻结(前缀语义) ──
  const gridRef = useRef<HTMLDivElement | null>(null)
  const [vTop, setVTop] = useState(0)          // wrap scrollTop(驱动虚拟窗口)
  const [rowH, setRowH] = useState(35)         // 行高运行时实测, 不硬编码
  const [colW, setColW] = useState<Record<string, number>>({})  // 列宽按列名记(列显隐切换不错位)
  const [frozenN, setFrozenN] = useState(0)    // 冻结的数据列数(前缀)
  const [frozenW, setFrozenW] = useState<number[]>([40])  // [#列宽, 冻结列宽...]: sticky left 偏移依据
  const colResize = useRef<{ col: number; startX: number; startW: number } | null>(null)
  // ── 两阶段布局: 列集变化 → 先 auto 实测自然宽 → 全列写入 colW → fixed 布局生效。
  // 直接 fixed+部分宽度会在窄容器把无宽列塌缩成 0(实测), 故任何列必须有显式宽 ──
  const [measuring, setMeasuring] = useState(true)
  const colSig = result ? result.columns.join('\u0001') : ''
  useEffect(() => { setMeasuring(true); setColW({}); setFrozenN(0) }, [colSig])
  useLayoutEffect(() => {
    if (!measuring || !result?.columns?.length) return
    const ths = gridRef.current?.querySelectorAll('thead th')
    if (!ths || !ths.length) return
    const next: Record<string, number> = {}
    result.columns.forEach((c, i) => {
      const th = ths[i + 1] as HTMLElement | undefined
      if (th && th.offsetWidth > 0) next[c] = th.offsetWidth
    })
    setColW(next)
    setMeasuring(false)
  }, [measuring, result])

  useEffect(() => {
    setSortCol(null); setSortAsc(true)
    if (!result || !result.columns?.length || !result.rows?.length) {
      setIsEditable(false)
      return
    }
    setIsEditable(!!(result as any).isEditable)
  }, [result])

  const closeDetail = useCallback(() => {
    setDetail(null)
    setDetailEdit(false)
  }, [])

  // 进入详情弹窗编辑(右键菜单/铅笔按钮共用); 草稿取原始值
  const openDetailEdit = useCallback((r: number, c: number) => {
    const v = result?.rows?.[r]?.[c]
    setDetailDraft(v === null || v === undefined ? '' : typeof v === 'object' ? JSON.stringify(v, null, 2) : String(v))
    setDetailEdit(true)
  }, [result])

  // 生成值: 打开该单元格的详情弹窗并直接把生成结果写成草稿(用户过目后再保存)
  const applyGenerated = useCallback((r: number, c: number, v: string) => {
    setDetail({ r, c })
    setDetailDraft(v)
    setDetailEdit(true)
  }, [])

  // ── 发号(递增 / 雪花) ──
  const [workerPolicy, setWorkerPolicy] = useState<WorkerPolicy>(readWorkerPolicy)
  const [workerCfg, setWorkerCfg] = useState<{ mode: 'machine' | 'random' | 'fixed'; fixed: string } | null>(null)
  const machineWorker = useRef<number | null>(null)  // 一个进程内问一次就够(后端算的是 IP+PID)

  const resolveWorkerId = useCallback(async (): Promise<number> => {
    if (typeof workerPolicy === 'number') return workerPolicy
    if (workerPolicy === 'random') return Math.floor(Math.random() * 1024)
    if (machineWorker.current === null) {
      // 没有后端能力时(如查询页网格)退化成随机, 而不是拒绝生成
      machineWorker.current = (await backend?.idWorker?.()) ?? Math.floor(Math.random() * 1024)
    }
    return machineWorker.current
  }, [workerPolicy, backend])

  const genIncrement = useCallback(async (r: number, c: number) => {
    try {
      const next = await backend?.nextId?.(c)
      if (!next) { toast.error('取号失败: 该列没有可取的数值'); return }
      applyGenerated(r, c, next)
    } catch (e: any) {
      toast.error('取号失败: ' + (e.message || e))
    }
  }, [backend, applyGenerated, toast])

  const genSnowflake = useCallback(async (r: number, c: number) => {
    applyGenerated(r, c, snowflakeId(await resolveWorkerId()))
  }, [resolveWorkerId, applyGenerated])

  const saveWorkerCfg = useCallback(() => {
    if (!workerCfg) return
    if (workerCfg.mode === 'fixed') {
      const n = Number(workerCfg.fixed.trim())
      if (!/^\d+$/.test(workerCfg.fixed.trim()) || n > 1023) { toast.error('workerId 得是 0~1023 的整数'); return }
      writeWorkerPolicy(n)
      setWorkerPolicy(n)
    } else {
      writeWorkerPolicy(workerCfg.mode)
      setWorkerPolicy(workerCfg.mode)
    }
    setWorkerCfg(null)
  }, [workerCfg, toast])

  // 保存弹窗编辑: 单条变更交给 onEdit(父级做 SQL 预览/确认/执行/刷新)
  const saveDetailEdit = useCallback(async () => {
    if (detail === null || !onEdit || !result?.rows) return
    const orig = result.rows[detail.r]?.[detail.c]
    let nv: any = detailDraft
    if (typeof orig === 'number' && detailDraft.trim() !== '' && !Number.isNaN(Number(detailDraft))) nv = Number(detailDraft)
    if (typeof orig === 'boolean') nv = detailDraft === 'true'
    if (JSON.stringify(nv) === JSON.stringify(orig)) { setDetailEdit(false); return }
    // 有主键才可能攒批量(parent 的 isEditable 已含该判定, runBatch 里还会再验一次)
    if (batchMode) {
      markPending({ kind: 'update', row: detail.r, col: detail.c, value: nv, oldValue: orig })
      toast.success('已加入待提交 —— 点底部「保存」一起写库')
      setDetail(null); setDetailEdit(false)
      return
    }
    setSavingEdit(true)
    try {
      await onEdit([{ row: detail.r, col: detail.c, newValue: nv, oldValue: orig }])
      setDetail(null)
      setDetailEdit(false)
    } finally {
      setSavingEdit(false)
    }
  }, [detail, detailDraft, onEdit, result, batchMode, markPending, toast])

  // 保存/回滚(批量模式): 真正写库在父级 onCommitBatch 里(预览→确认→一个事务提交)
  const saveBatch = useCallback(async () => {
    if (!onCommitBatch || pending.length === 0) return
    setSavingBatch(true)
    try {
      const r = await onCommitBatch(pending)
      if (r?.cancelled) return
      if (r?.ok) {
        setPending([])
        setBadCells(new Set())
        toast.success(`已提交 ${pending.length} 处变更`)
        onAfterWrite?.()
      } else {
        if (r?.badCells?.length) setBadCells(new Set(r.badCells.map(b => `${b.row}:${b.col}`)))
        toast.error('提交失败(改动保留): ' + (r?.error || '未知错误'))
      }
    } catch (e: any) {
      toast.error('提交失败: ' + (e?.message || e))
    } finally {
      setSavingBatch(false)
    }
  }, [onCommitBatch, pending, toast, onAfterWrite])

  // ── 结果分页(dbx 同款): 默认 100 行/页 + 底部翻页栏; 行数据始终全量在内存(客户端分页) ──
  const [page, setPage] = useState(1)
  const [pageSize, setPageSize] = useState(100)
  useEffect(() => { setPage(1) }, [result])

  // 本地排序视图(不影响原始行号映射)
  const rows = result.rows || []
  const viewRows = useMemo(() => {
    if (sortCol === null || !rows.length) return rows.map((_, i) => i)  // 统一语义: viewRows=原行索引数组
    const idx = rows.map((_, i) => i)
    idx.sort((a, b) => {
      const va = rows[a]?.[sortCol], vb = rows[b]?.[sortCol]
      if (va === null || va === undefined) return 1
      if (vb === null || vb === undefined) return -1
      const na = Number(va), nb = Number(vb)
      let cmp: number
      if (!Number.isNaN(na) && !Number.isNaN(nb) && String(va).trim() !== '' && String(vb).trim() !== '') {
        cmp = na - nb
      } else {
        cmp = String(va).localeCompare(String(vb))
      }
      return sortAsc ? cmp : -cmp
    })
    return idx
  }, [rows, sortCol, sortAsc])

  // 当前页的原行索引切片(排序感知; 右键/详情/编辑条全部用原索引, 排序不再错位)
  // hidePager(数据页, 外层 pager 负责)时按全量行数渲染 —— 此前内部 pageSize=100 会把
  // 外层 1000 行/页的取数截到 100 行(存量 bug); 虚拟滚动兜住 DOM 行数, 全量渲染无压力
  const pageStart = hidePager ? 0 : (page - 1) * pageSize
  const effPageSize = hidePager ? Math.max(viewRows.length, 1) : pageSize
  const pageIdx = viewRows.slice(pageStart, pageStart + effPageSize)

  // ── 虚拟滚动: 页行数超阈值才启用; 只渲染可视区 ±OVERSCAN, 垫片行撑总高 ──
  const VIRTUAL_THRESHOLD = 60
  const OVERSCAN = 8
  const virtualize = pageIdx.length > VIRTUAL_THRESHOLD
  const clientH = gridRef.current?.clientHeight || 0
  let visStart = 0
  let visEnd = pageIdx.length
  if (virtualize) {
    const start = Math.max(0, Math.floor(vTop / rowH) - OVERSCAN)
    visStart = Math.min(start, Math.max(0, pageIdx.length - 1))
    visEnd = Math.min(pageIdx.length, start + Math.ceil(clientH / rowH) + OVERSCAN * 2)
  }
  // 行高实测: 每次渲染后量首行(过滤/列显隐不改行高, 量到即稳)
  useLayoutEffect(() => {
    const tr = gridRef.current?.querySelector('tbody tr:not(.db-vspacer)') as HTMLTableRowElement | null
    if (tr) {
      const h = tr.getBoundingClientRect().height
      if (h > 10 && Math.abs(h - rowH) > 0.5) setRowH(h)
    }
  })
  // 翻页/排序/换结果 → 滚动归零(虚拟窗口随之回顶)
  useEffect(() => {
    if (gridRef.current) gridRef.current.scrollTop = 0
    setVTop(0)
  }, [page, pageSize, sortCol, sortAsc, result])

  // ── 列宽拖拽 / 冻结偏移 ──
  const measureHeads = useCallback((): number[] | null => {
    const g = gridRef.current
    if (!g) return null
    return Array.from(g.querySelectorAll<HTMLTableHeaderCellElement>('thead th')).map(t => t.offsetWidth)
  }, [])
  // 冻结列 i 的 sticky left = #列宽 + 前面各冻结列宽
  const frozenLeft = useCallback((i: number): number | undefined => {
    if (i >= frozenN) return undefined
    let left = frozenW[0] ?? 40
    for (let k = 0; k < i; k++) left += frozenW[k + 1] ?? 100
    return left
  }, [frozenN, frozenW])
  const startColResize = useCallback((col: number, e: React.PointerEvent<HTMLSpanElement>) => {
    e.preventDefault()
    e.stopPropagation()
    const th = (e.currentTarget as HTMLElement).parentElement as HTMLTableCellElement
    colResize.current = { col, startX: e.clientX, startW: th.offsetWidth }
    e.currentTarget.setPointerCapture(e.pointerId)
  }, [])
  const onColResizeMove = useCallback((e: React.PointerEvent<HTMLSpanElement>) => {
    const d = colResize.current
    if (!d) return
    const w = Math.min(640, Math.max(64, d.startW + e.clientX - d.startX))
    setColW(prev => ({ ...prev, [result.columns[d.col]]: w }))
    setFrozenW(prev => {
      if (d.col >= frozenN) return prev
      const n = [...prev]
      n[d.col + 1] = w
      return n
    })
  }, [result, frozenN])
  const endColResize = useCallback(() => { colResize.current = null }, [])
  // 双击拖柄清除自定义宽; 冻结列清除会破坏 sticky 偏移 → 保持现状
  const clearColW = useCallback((col: number) => {
    if (col < frozenN) return
    const name = result.columns[col]
    setColW(prev => {
      if (prev[name] === undefined) return prev
      const n = { ...prev }
      delete n[name]
      return n
    })
  }, [result, frozenN])



  // ── 详情(单/行/列三种聚合共用一个原子构建器, dbx dataGridDetail 同构) ──
  const metaByName = useMemo(() => new Map((columnMeta || []).map(m => [m.name, m])), [columnMeta])
  // 列的视觉类别: 类型口径与 buildCellInfo 完全一致(表元数据优先, 退回 result.columnTypes),
  // 保证"看到的类型行"与"单元格着色/右对齐"判自同一个类型, 不会出现表头写 int8 而按字符串对齐。
  const colTypeAt = useCallback((j: number) => columnMeta?.[j]?.type || columnTypes?.[j] || '', [columnMeta, columnTypes])
  const colKind = useMemo(() => (result?.columns || []).map((_, j) => resolveTypeVisualKind(colTypeAt(j))), [result, colTypeAt])
  const colNumeric = useMemo(() => (result?.columns || []).map((_, j) => isNumericType(colTypeAt(j))), [result, colTypeAt])

  // 外键跳转: 同一约束(同 name + 同目标表)的多行合成一组 = 复合外键。
  // 组内每一列都必须在结果集中存在且值非 NULL 才可跳 —— 少一列就拼不出完整 WHERE, 跳过去是误导。
  const fkGroups = useMemo(() => {
    const m = new Map<string, FkPair[]>()
    for (const fk of foreignKeys || []) {
      const key = `${fk.name || fk.column}|${fk.refTable}`
      const arr = m.get(key) || []
      arr.push(fk)
      m.set(key, arr)
    }
    return [...m.values()]
  }, [foreignKeys])

  const fkJumpsFor = useCallback((row: number) => {
    if (!onFkJump || !result?.columns) return []
    const out: Array<{ label: string; refTable: string; conds: Array<{ col: string; op: string; value: string }> }> = []
    for (const pairs of fkGroups) {
      const conds: Array<{ col: string; op: string; value: string }> = []
      let ok = true
      for (const p of pairs) {
        const idx = result.columns.findIndex(c => String(c).toLowerCase() === p.column.toLowerCase())
        const v = idx < 0 ? undefined : result.rows[row]?.[idx]
        if (idx < 0 || v === null || v === undefined) { ok = false; break }
        conds.push({ col: p.refColumn, op: '=', value: String(v) })
      }
      if (!ok) continue
      out.push({
        label: pairs.length > 1 ? `跳到 ${pairs[0].refTable}（复合键 ${pairs.length} 列）` : `跳到 ${pairs[0].refTable}`,
        refTable: pairs[0].refTable, conds,
      })
    }
    return out
  }, [fkGroups, onFkJump, result])
  const buildCellInfo = useCallback((r: number, c: number) => {
    if (!result || !result.columns) return null
    const column = result.columns[c]
    if (column === undefined) return null
    const value = result.rows[r]?.[c] ?? null
    const meta = metaByName.get(column)
    return {
      column, rowNumber: r + 1, value,
      type: columnMeta?.[c]?.type || columnTypes?.[c] || '',
      comment: meta?.comment || '',
      nullable: meta?.nullable,
      key: meta?.key || '',
      length: value === null ? 0 : String(value).length,
    }
  }, [result, metaByName, columnMeta, columnTypes])

  const exportEffective = exportSql?.trim() || sql   // 导出用 SQL(数据页=当前页 LIMIT/OFFSET; 缺省用 sql)

  // 写操作辅助: 从 sql 解析目标表(SELECT * FROM x)、主键列、值转义
  const tableFromSql = useMemo(() => {
    const m = /FROM\s+([`\"\[\]\w.]+)/i.exec(sql || '')
    return m ? m[1] : null
  }, [sql])
  const pkCols = useMemo(() => (columnMeta || []).filter(c => c.key === 'PRI').map(c => c.name), [columnMeta])
  const escVal = useCallback((v: any) => {
    if (v === null) return 'NULL'
    if (typeof v === 'number') return String(v)
    return `'${String(v).replace(/'/g, "''")}'`
  }, [])

  const copyCell = useCallback((v: any) => {
    const text = renderCell(v)
    navigator.clipboard?.writeText(text).then(() => {
      setCopied('已复制')
      setTimeout(() => setCopied(''), 1200)
    }).catch(() => {})
  }, [])

  // 右键菜单: 同类项收进二级(ContextMenu 的 children, hover 展开)。
  // 排序/筛选/复制 放最上面有两层原因 —— 一是 dbx 就是这个顺序, 二是这三块里 筛选 有 11 项,
  // 挂在菜单底部时子面板会顶出视口(子菜单只做了横向翻转, 纵向没做), 挂顶部则怎么翻都还有空间。
  const buildCtxMenu = useCallback((row: number, col: number, x: number, y: number): ContextMenuItem[] => {
    if (!result || !result.columns) return []
    const colName = result.columns[col]
    const cellValue = result.rows[row]?.[col]
    const rowData = result.rows[row] || []
    const fv = String(cellValue ?? '').slice(0, 40)
    const isNullCell = cellValue === null || cellValue === undefined

    const sortChildren: ContextMenuItem[] = [
      ...(onSortDatabase ? [
        { label: '数据库升序排序', icon: <ActionIcon kind="sort-asc" />, onClick: () => onSortDatabase(colName, 'asc') },
        { label: '数据库降序排序', icon: <ActionIcon kind="sort-desc" />, onClick: () => onSortDatabase(colName, 'desc') },
        { divider: 'light' as const },
      ] : []),
      { label: '当前页升序排序', icon: <ActionIcon kind="sort-asc" />, onClick: () => { setSortCol(col); setSortAsc(true) } },
      { label: '当前页降序排序', icon: <ActionIcon kind="sort-desc" />, onClick: () => { setSortCol(col); setSortAsc(false) } },
      ...(sortCol !== null ? [{ label: '清除排序', icon: <ActionIcon kind="close" />, onClick: () => setSortCol(null) }] : []),
    ]
    // 空单元格: 值类运算符没有操作数可用, 只留 NULL 两项 + 清除
    const filterChildren: ContextMenuItem[] = onFilter ? [
      ...(isNullCell ? [] : FILTER_FROM_CELL.map(f => ({
        label: f.label,
        icon: <ActionIcon kind="filter" />,
        title: `${colName} ${f.op} ${fv}`,
        onClick: () => onFilter(colName, f.op, String(cellValue ?? '')),
      }))),
      ...(isNullCell ? [] : [{ divider: 'light' as const }]),
      { label: '仅显示 NULL', icon: <ActionIcon kind="filter" />, onClick: () => onFilter(colName, 'IS NULL', '') },
      { label: '仅显示非 NULL', icon: <ActionIcon kind="filter" />, onClick: () => onFilter(colName, 'IS NOT NULL', '') },
      ...(onClearFilters ? [{ divider: 'light' as const }, { label: '清除筛选', icon: <ActionIcon kind="close" />, onClick: () => onClearFilters() }] : []),
    ] : []
    // 生成值: 只在该单元格可编辑、且列类型至少放行一项时才出现
    const kind = colKind[col] || 'unknown'
    // 纯 DATE 列不给"当前日期时间": '2026-09-23 14:51:56' 塞进 date 列 MySQL 会截断并告警,
    // 生成了就该是个能直接落库的字面量。datetime/timestamp/字符串/类型未知 才给。
    const rawType = colTypeAt(col).toLowerCase()
    const pureDateCol = /^date\b/.test(rawType.trim())
    const genChildren: ContextMenuItem[] = (isEditable && onEdit) ? [
      ...(GEN_TEXT_KINDS.has(kind) ? [
        { label: '空字符串', icon: <ActionIcon kind="wand" />, onClick: () => applyGenerated(row, col, '') },
      ] : []),
      ...(GEN_TIME_KINDS.has(kind) && !pureDateCol ? [
        { label: '当前日期时间', icon: <ActionIcon kind="wand" />, title: '本地时区 YYYY-MM-DD HH:mm:ss', onClick: () => applyGenerated(row, col, nowLiterals().dateTime) },
      ] : []),
      ...(GEN_TIME_KINDS.has(kind) ? [
        { label: '当前日期', icon: <ActionIcon kind="wand" />, title: '本地时区 YYYY-MM-DD', onClick: () => applyGenerated(row, col, nowLiterals().date) },
      ] : []),
      ...(GEN_TEXT_KINDS.has(kind) ? [
        { label: 'UUID', icon: <ActionIcon kind="wand" />, onClick: () => applyGenerated(row, col, uuidV4()) },
      ] : []),
      // 发号两项只给整数列。递增必须问库(前端不知道表里现在最大是多少), 所以没这个能力就不出现;
      // 雪花纯计算, 不依赖库。
      ...(GEN_ID_KINDS.has(kind) && backend?.nextId ? [
        { label: '递增 ID', icon: <ActionIcon kind="wand" />, title: `取 ${colName} 在表内的最大值 +1`, onClick: () => { void genIncrement(row, col) } },
      ] : []),
      ...(GEN_ID_KINDS.has(kind) ? [
        { label: '雪花 ID', icon: <ActionIcon kind="wand" />, title: `时间|workerId|序列 · workerId 来源: ${workerPolicyLabel(workerPolicy)}`, onClick: () => { void genSnowflake(row, col) } },
        {
          label: '雪花 workerId…',
          icon: <ActionIcon kind="gear" />,
          onClick: () => setWorkerCfg({
            mode: typeof workerPolicy === 'number' ? 'fixed' : workerPolicy,
            fixed: typeof workerPolicy === 'number' ? String(workerPolicy) : '',
          }),
        },
      ] : []),
    ] : []

    // 置为 NULL 需要后端拼 SQL(见 DataGridBackend.applyRowWrite); 没注入就整个菜单项不出现 ——
    // 不留"前端手搓一份"的退路, 那条路不会给标识符加引号。
    const applyRowWrite = backend?.applyRowWrite

    const items: ContextMenuItem[] = [
      // ── 排序 / 筛选 / 复制(二级) ──
      { label: '排序', icon: <ActionIcon kind="sort-asc" />, children: sortChildren },
      ...(filterChildren.length ? [{ label: '筛选', icon: <ActionIcon kind="filter" />, children: filterChildren } as ContextMenuItem] : []),
      { label: '复制', icon: <ActionIcon kind="copy" />, children: [
        { label: '复制值', icon: <ActionIcon kind="copy" />, onClick: () => copyCell(cellValue) },
        { label: '复制整行', icon: <ActionIcon kind="copy" />, onClick: () => navigator.clipboard?.writeText(rowData.map(v => renderCell(v)).join('\t')) },
        { label: '复制列名', icon: <ActionIcon kind="copy" />, onClick: () => navigator.clipboard?.writeText(colName) },
      ] },
      { divider: 'heavy' },
      // ── 详情 ──
      { label: '单元格详情', icon: <ActionIcon kind="doc" />, onClick: () => { setDetail({ r: row, c: col }); setDetailEdit(false) } },
      { label: '行详情', icon: <ActionIcon kind="doc" />, onClick: () => { setFieldFilter(''); setRowDetail(row) } },
      { label: '列详情', icon: <ActionIcon kind="doc" />, onClick: () => { setFieldFilter(''); setColDetail(col) } },
      { label: '转置显示此行', icon: <ActionIcon kind="doc" />, onClick: () => setTranspose({ r: row }) },
      // ── 外键跳转(值非 NULL 且复合键各列齐全才出现) ──
      ...fkJumpsFor(row).map(j => ({
        label: j.label, icon: <ActionIcon kind="chevrons-right" />, onClick: () => onFkJump!(j.refTable, j.conds),
      })),
      // 单元格编辑入口(右键): 打开详情弹窗并直接进入编辑模式(无主键表/查询页不可编辑时隐藏)
      ...(isEditable && onEdit ? [{ label: '编辑单元格', icon: <ActionIcon kind="edit" />, onClick: () => { setDetail({ r: row, c: col }); openDetailEdit(row, col) } }] : []),
      ...(genChildren.length ? [{ label: '生成值', icon: <ActionIcon kind="wand" />, children: genChildren } as ContextMenuItem] : []),
      ...(tableFromSql && pkCols.length && !pkCols.includes(colName) && (applyRowWrite || batchMode) ? [{
        label: '置为 NULL',
        icon: <ActionIcon kind="edit" />,
        onClick: async () => {
          if (batchMode) {
            markPending({ kind: 'update', row, col, value: null, oldValue: cellValue })
            toast.success('已加入待提交 —— 点底部「保存」一起写库')
            return
          }
          try {
            // 先干跑拿后端生成的语句, 摆在确认弹窗里给人看, 确认后才真写
            const preview = await applyRowWrite!({ op: 'set-null', row, col, dryRun: true })
            if (!preview.ok || !preview.sql) { toast.error('置 NULL 失败: ' + (preview.error || '后端未生成语句')); return }
            if (!(await confirm(`将 ${colName} 置为 NULL`, { content: <SqlPreviewBody sqls={[preview.sql]} />, okText: '执行', danger: true, maxWidth: 620 }))) return
            const r = await applyRowWrite!({ op: 'set-null', row, col, dryRun: false })
            if (!r.ok) { toast.error('置 NULL 失败: ' + (r.error || '写入失败')); return }
            setCopied('已置 NULL'); setTimeout(() => setCopied(''), 1200)
            onAfterWrite?.()
          } catch (e: any) {
            toast.error('置 NULL 失败: ' + (e.message || e))
          }
        },
      }] : []),
      ...(tableFromSql && pkCols.length && (applyRowWrite || batchMode) ? [{
        label: '删除行',
        icon: <ActionIcon kind="delete" />,
        danger: true,
        onClick: async () => {
          if (batchMode) {
            markPending({ kind: 'delete', row })
            toast.success('已标记删除 —— 点底部「保存」一起写库')
            return
          }
          try {
            const preview = await applyRowWrite!({ op: 'delete-row', row, dryRun: true })
            if (!preview.ok || !preview.sql) { toast.error('删除失败: ' + (preview.error || '后端未生成语句')); return }
            if (!(await confirm('删除该行 · 不可撤销', { content: <SqlPreviewBody sqls={[preview.sql]} />, okText: '执行', danger: true, maxWidth: 620 }))) return
            const r = await applyRowWrite!({ op: 'delete-row', row, dryRun: false })
            if (!r.ok) { toast.error('删除失败: ' + (r.error || '写入失败')); return }
            setCopied('已删除'); setTimeout(() => setCopied(''), 1200)
            onAfterWrite?.()
          } catch (e: any) {
            toast.error('删除失败: ' + (e.message || e))
          }
        },
      }] : []),
    ]
    return items
  }, [result, backend, copyCell, onFilter, onClearFilters, sortCol, genIncrement, genSnowflake, workerPolicy, batchMode, markPending, toast])

  // 列头右键菜单: 排序收二级 + 复制列名 + 列详情 —— 排序入口不再依赖先选中某个单元格
  const buildColHeadMenu = useCallback((col: number): ContextMenuItem[] => {
    const colName = result.columns[col]
    const items: ContextMenuItem[] = [
      { label: '排序', icon: <ActionIcon kind="sort-asc" />, children: [
        ...(onSortDatabase ? [
          { label: '数据库升序排序', icon: <ActionIcon kind="sort-asc" />, onClick: () => onSortDatabase(colName, 'asc') },
          { label: '数据库降序排序', icon: <ActionIcon kind="sort-desc" />, onClick: () => onSortDatabase(colName, 'desc') },
          { divider: 'light' as const },
        ] : []),
        { label: '当前页升序排序', icon: <ActionIcon kind="sort-asc" />, onClick: () => { setSortCol(col); setSortAsc(true) } },
        { label: '当前页降序排序', icon: <ActionIcon kind="sort-desc" />, onClick: () => { setSortCol(col); setSortAsc(false) } },
        ...(sortCol !== null ? [{ label: '清除排序', icon: <ActionIcon kind="close" />, onClick: () => setSortCol(null) }] : []),
      ] },
      { label: '复制列名', icon: <ActionIcon kind="copy" />, onClick: () => copyCell(colName) },
      { label: '列详情', icon: <ActionIcon kind="doc" />, onClick: () => { setFieldFilter(''); setColDetail(col) } },
    ]
    // 冻结(前缀语义): 冻结到此列=冻结 #列+数据列 0..col; 已冻到本列则提供取消
    if (frozenN === col + 1) {
      items.push({ label: '取消冻结', icon: <ActionIcon kind="close" />, onClick: () => setFrozenN(0) })
    } else {
      items.push({
        label: '冻结到此列', icon: <ActionIcon kind="doc" />,
        onClick: () => {
          const widths = measureHeads()
          if (!widths) return
          const next: Record<string, number> = { ...colW }
          for (let i = 0; i <= col; i++) next[result.columns[i]] = widths[i + 1]
          setColW(next)
          setFrozenW([widths[0], ...Array.from({ length: col + 1 }, (_, i) => widths[i + 1])])
          setFrozenN(col + 1)
        },
      })
    }
    return items
  }, [result, onSortDatabase, sortCol, copyCell, frozenN, colW, measureHeads])

  const buildRowCtxMenu = useCallback((row: number, x: number, y: number): ContextMenuItem[] => {
    if (!result || !result.columns) return []
    const rowData = result.rows[row] || []
    const rowObj: Record<string, any> = {}
    result.columns.forEach((c, i) => { rowObj[c] = rowData[i] })
    const items: ContextMenuItem[] = [
      { label: '复制整行 (TAB)', icon: <ActionIcon kind="copy" />, onClick: () => navigator.clipboard?.writeText(rowData.map(v => renderCell(v)).join('\t')) },
      { label: '复制整行 (JSON)', icon: <ActionIcon kind="copy" />, onClick: () => navigator.clipboard?.writeText(JSON.stringify(rowObj, null, 2)) },
      { divider: true },
    ]
    items.push({ label: '导出当前页 (CSV)', icon: <ActionIcon kind="upload" />, disabled: !connId || !exportEffective, onClick: () => { if (backend?.onExport && exportEffective) backend.onExport(exportEffective, 'csv').catch(() => {}) } })
    if (tableFromSql) {
      items.push({ divider: true })
      items.push({
        label: '复制行为新行 (INSERT)', icon: <ActionIcon kind="copy" />, disabled: !pkCols.length && !result.columns.length,
        onClick: () => {
          const cols = result.columns.filter(c => !pkCols.includes(c))
          const vals = cols.map(c => escVal(rowData[result.columns.indexOf(c)]))
          navigator.clipboard?.writeText(`INSERT INTO ${tableFromSql} (${cols.join(', ')}) VALUES (${vals.join(', ')})`)
        },
        title: '生成排除主键的 INSERT 语句到剪贴板',
      })
    }
    return items
  }, [result, connId, exportEffective, tableFromSql, pkCols])

  const [exporting, setExporting] = useState<ExportFormat | null>(null)
  const [exportErr, setExportErr] = useState('')

  const doExport = useCallback(async (format: ExportFormat) => {
    if (!connId || !exportEffective?.trim()) return
    setExporting(format)
    setExportErr('')
    try {
      const { fileName } = (await backend?.onExport?.(exportEffective, format)) ?? { fileName: '' }
      setExportErr(`已导出 ${fileName}`)
    } catch (e: any) {
      setExportErr(e.message || '导出失败')
    } finally {
      setExporting(null)
    }
  }, [connId, exportEffective])

  if (!result) {
    return <div className="db-empty">执行查询后查看结果 · Ctrl+Enter 快速执行</div>
  }
  if (result.error) {
    return <div className="banner banner-err" style={{ margin: 12 }}>{result.error}</div>
  }
  if (!result.columns?.length) {
    return <div className="db-empty">查询成功, 无返回列</div>
  }

  const canExport = !!connId && !!sql?.trim() && !result.error

  return (
    <div className="db-result">
      <div className="db-result-head">
        <span>
          {result.rowCount} 行
          {result.affected > 0 && ` · 影响 ${result.affected} 行`}
          {' · '}{result.durationMs}ms
          {sortCol !== null && ` · 已按 ${result.columns[sortCol]} ${sortAsc ? '↑' : '↓'} 排序`}
          {copied && <span style={{ color: 'var(--ok)' }}> {copied}</span>}
        </span>
        {result.truncated && <span className="pill pill-warn">结果已截断</span>}
        {exportErr && <span style={{ fontSize: '0.6875rem' }}>{exportErr}</span>}
        {canExport && (
          <div className="db-export-controls">
            {(['csv', 'json', 'xlsx'] as ExportFormat[]).map(fmt => (
              <button
                key={fmt}
                onClick={() => doExport(fmt)}
                disabled={!!exporting}
                className="btn-glass-soft btn-glass-soft-sm"
                title={`导出为 ${fmt.toUpperCase()}`}
              >
                {exporting === fmt ? '...' : fmt.toUpperCase()}
              </button>
            ))}
          </div>
        )}
      </div>

      {/* 多语句执行摘要 */}
      {result.statements && result.statements.length > 1 && (
        <div className="db-stmts-summary">
          <div className="db-stmts-title">执行摘要 · {result.statements.length} 条语句</div>
          <div className="db-stmts-list">
            {result.statements.map((s, i) => (
              <div key={i} className={`db-stmt-item db-stmt-${s.type.toLowerCase()}`}>
                <span className="db-stmt-num">{i + 1}</span>
                <span className={`pill db-stmt-type-${s.type.toLowerCase()}`}>{s.type}</span>
                <code className="db-stmt-sql" title={s.sql}>{s.sql.length > 80 ? s.sql.slice(0, 80) + '...' : s.sql}</code>
                {s.error ? (
                  <span className="pill pill-err" title={s.error}>失败</span>
                ) : (
                  <>
                    <span className="dim">{s.durationMs}ms</span>
                    {s.rows > 0 && <span className="dim">{s.rows} 行</span>}
                    {s.affected > 0 && <span className="dim">{s.affected} 行</span>}
                  </>
                )}
              </div>
            ))}
          </div>
        </div>
      )}

      <div className="db-table-grid" ref={gridRef} onScroll={e => setVTop(e.currentTarget.scrollTop)}>
        <table className={'db-table-result' + (measuring ? ' db-measuring' : '')}>
          <colgroup>
            <col style={{ width: 40 }} />
            {result.columns.map((c, j) => (
              <col key={c} style={colW[c] ? { width: colW[c] } : undefined} />
            ))}
          </colgroup>
          <thead>
            <tr>
              <th className="db-col-num db-col-frozen" style={{ left: 0 }}>#</th>
              {result.columns.map((c, j) => {
                // 键列着色: 主键淡红 / 索引(UNI/MUL)淡绿 —— 行内编辑定位与索引感知; 标记走类型行后缀不用 emoji
                const k = columnMeta?.[j]?.key || ''
                const keyCls = k === 'PRI' ? 'db-th-pri' : (k === 'UNI' || k === 'MUL') ? 'db-th-idx' : ''
                const keyTip = k === 'PRI' ? '主键 · ' : keyCls ? '索引 · ' : ''
                const keyTag = k === 'PRI' ? ' · PRI' : k === 'UNI' ? ' · UNI' : k === 'MUL' ? ' · MUL' : ''
                const fz = j < frozenN
                return (
                <th key={c} className={keyCls + (fz ? ' db-col-frozen' : '')} title={keyTip + '点击排序 · 右键更多'}
                  style={fz ? { left: frozenLeft(j) } : undefined}
                  onClick={() => {
                    if (sortCol === j) { setSortAsc(!sortAsc) } else { setSortCol(j); setSortAsc(true) }
                  }}
                  onContextMenu={e => { e.preventDefault(); setColHeadMenu({ col: j, x: e.clientX, y: e.clientY }) }}>
                  {columnTypes ? (
                    <span className="db-th-two-line">
                      <span className="db-th-name">{c}{sortCol === j ? (sortAsc ? ' ↑' : ' ↓') : ''}</span>
                      <span className="db-th-type">{columnTypes[j] || ''}{keyTag}</span>
                    </span>
                  ) : (
                    <>{c}{sortCol === j ? (sortAsc ? ' ↑' : ' ↓') : ''}</>
                  )}
                  <span
                    className="db-col-resize"
                    title="拖拽调宽 · 双击恢复"
                    onPointerDown={e => startColResize(j, e)}
                    onPointerMove={onColResizeMove}
                    onPointerUp={endColResize}
                    onClick={e => e.stopPropagation()}
                    onDoubleClick={e => { e.stopPropagation(); clearColW(j) }}
                  />
                </th>
                )
              })}
            </tr>
          </thead>
          <tbody>
            {virtualize && visStart > 0 && (
              <tr className="db-vspacer" style={{ height: visStart * rowH }}><td colSpan={result.columns.length + 1} /></tr>
            )}
            {pageIdx.slice(visStart, visEnd).map((i) => {
              const row = rows[i] || []
              return (
              <tr
                key={i}
                className={pendingDeleted(i) ? 'dg-row-del' : undefined}
                onContextMenu={e => {
                  e.preventDefault()
                  // 只在行空白处(非单元格)弹行菜单; 打开前行菜单先关单元格菜单
                  if (e.target === e.currentTarget) {
                    setCtxMenu(null)
                    setRowCtxMenu({ row: i, x: e.clientX, y: e.clientY })
                  }
                }}
              >
                <td
                  className="db-col-num db-col-frozen"
                  style={{ left: 0 }}
                  title="右键: 行菜单(复制整行/导出)"
                  onContextMenu={e => {
                    e.preventDefault()
                    setRowCtxMenu({ row: i, x: e.clientX, y: e.clientY })
                  }}
                >{i + 1}</td>
                {row.map((cell, j) => (
                   <td
                    key={j}
                    className={[
                      j < frozenN ? 'db-col-frozen' : '',
                      colKind[j] && colKind[j] !== 'unknown' ? `db-t-${colKind[j]}` : '',
                      colNumeric[j] ? 'db-numr' : '',
                      cell === null ? 'db-isnull' : '',
                      pendingCell(i, j) ? 'dg-dirty' : '',
                      badCells.has(`${i}:${j}`) ? 'dg-bad' : '',
                    ].filter(Boolean).join(' ') || undefined}
                    style={j < frozenN ? { left: frozenLeft(j) } : undefined}
                    title={cell === null || cell === undefined ? undefined : renderCell(cell)}
                    onContextMenu={e => {
                      e.preventDefault()
                      e.stopPropagation() // 防冒泡到 tr 造成双菜单叠加
                      setRowCtxMenu(null)
                      setCtxMenu({ row: i, col: j, x: e.clientX, y: e.clientY })
                    }}
                    onDoubleClick={() => { setDetail({ r: i, c: j }); setDetailEdit(false) }}
                  >
                    {cell === null ? <span className="dim">NULL</span> : renderCell(cell)}
                  </td>
                ))}
              </tr>
              )
            })}
            {virtualize && visEnd < pageIdx.length && (
              <tr className="db-vspacer" style={{ height: (pageIdx.length - visEnd) * rowH }}><td colSpan={result.columns.length + 1} /></tr>
            )}
          </tbody>
        </table>
        {/* 空态必须渲染在 <table> 之外(不能在 tbody 里用 colSpan 横跨全列):
            宽表(数十列)下那个 <td> 会宽达数千像素, 且 textAlign:center 会把内容推到水平中部,
            实测 51 列表格中按钮位于 x≈3505 而视口仅 1700px —— 功能在但"看不见、点不到"。
            放在表格容器的直接子级, 宽度跟随容器而非列宽, 任何列数下都居中可见。 */}
        {pageIdx.length === 0 && emptyState && (
          <div style={{ padding: '2.5rem 1rem', textAlign: 'center', color: 'var(--text-dim)' }}>
            <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', gap: '0.5rem' }}>
              <span>{emptyState.hint}</span>
              {emptyState.actionLabel && emptyState.onAction && (
                <button className="btn-glass-soft btn-glass-soft-sm" onClick={emptyState.onAction}>{emptyState.actionLabel}</button>
              )}
            </div>
          </div>
        )}
      </div>
      {batchMode && pending.length > 0 && (
        <div className="dg-pending-bar">
          <span>待提交 <b>{pending.length}</b> 处变更</span>
          <span className="dim">改动只暂存在本页, 点保存才写库</span>
          <span style={{ marginLeft: 'auto' }} />
          <button className="btn-glass-soft btn-glass-soft-sm" disabled={savingBatch}
            onClick={() => { setPending([]); setBadCells(new Set()); toast.success('已回滚本页未提交的改动') }}>回滚</button>
          <button className="btn-glass-soft btn-glass-soft-sm btn-accent" disabled={savingBatch} onClick={saveBatch}>
            {savingBatch ? '提交中…' : '保存'}
          </button>
        </div>
      )}
      {!hidePager && (
      <div className="db-result-footer">
        <span className="dim">共 {viewRows.length} 行{result.truncated ? ' · 已截断' : ''}</span>
        <select className="input db-page-size" title="每页行数" value={pageSize}
          onChange={e => { setPageSize(Number(e.target.value)); setPage(1) }}>
          {PAGE_SIZES.map(n => <option key={n} value={n}>{n} 行/页</option>)}
        </select>
        <button className="btn-glass-soft btn-glass-soft-sm" disabled={page <= 1} onClick={() => setPage(1)} title="首页" aria-label="首页">«</button>
        <button className="btn-glass-soft btn-glass-soft-sm" disabled={page <= 1} onClick={() => setPage(p => p - 1)} title="上一页" aria-label="上一页">‹</button>
        <span className="dim">{page} / {Math.max(1, Math.ceil(viewRows.length / pageSize))}</span>
        <button className="btn-glass-soft btn-glass-soft-sm" disabled={page >= Math.ceil(viewRows.length / pageSize)} onClick={() => setPage(p => p + 1)} title="下一页" aria-label="下一页">›</button>
        <button className="btn-glass-soft btn-glass-soft-sm" disabled={page >= Math.ceil(viewRows.length / pageSize)} onClick={() => setPage(Math.ceil(viewRows.length / pageSize))} title="末页" aria-label="末页">»</button>
      </div>
      )}
      {ctxMenu && result && result.columns?.length && ctxMenu.col < result.columns.length && (
        <ContextMenu
          x={ctxMenu.x}
          y={ctxMenu.y}
          items={buildCtxMenu(ctxMenu.row, ctxMenu.col, ctxMenu.x, ctxMenu.y)}
          onClose={() => setCtxMenu(null)}
        />
      )}
      {colHeadMenu && result && (
        <ContextMenu
          x={colHeadMenu.x}
          y={colHeadMenu.y}
          items={buildColHeadMenu(colHeadMenu.col)}
          onClose={() => setColHeadMenu(null)}
        />
      )}
      {rowCtxMenu && result && result.rows?.length && rowCtxMenu.row < result.rows.length && (
        <ContextMenu
          x={rowCtxMenu.x}
          y={rowCtxMenu.y}
          items={buildRowCtxMenu(rowCtxMenu.row, rowCtxMenu.x, rowCtxMenu.y)}
          onClose={() => setRowCtxMenu(null)}
        />
      )}
      {/* 单元格详情: 元数据网格 + 注释 + 值(dbx CellDetailDialog 同构); 可编辑表内含「编辑值」入口 */}
      {detail && (() => {
        const info = buildCellInfo(detail.r, detail.c)
        if (!info) return null
        return (
          <DetailOverlay
            title={<>单元格详情{detailEdit ? ' · 编辑中' : ''}</>}
            onClose={closeDetail}
            head={<span style={{ marginLeft: 'auto', display: 'flex', gap: 4 }}>
                  {isEditable && onEdit && !detailEdit && (
                    <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => openDetailEdit(detail.r, detail.c)} title="编辑此单元格(按主键 UPDATE)">编辑值</button>
                  )}
                </span>}>
                <div className="db-cell-meta">
                  <div><span className="dim">列名</span><b>{info.column}</b></div>
                  <div><span className="dim">行号</span>{info.rowNumber}</div>
                  <div><span className="dim">类型</span>{info.type || '-'}</div>
                  <div><span className="dim">长度</span>{info.length}</div>
                  {columnMeta && <div><span className="dim">可空</span>{info.nullable ? 'YES' : 'NO'}</div>}
                  {columnMeta && <div><span className="dim">键</span>{info.key || '-'}</div>}
                </div>
                <div className="db-cell-meta-comment">
                  <span className="dim">注释</span>
                  <span>{info.comment || '暂无注释'}</span>
                </div>
                <div className="db-cell-meta-value">
                  <div className="db-cell-meta-value-head">
                    <span className="dim">值</span>
                    {!detailEdit && (
                      <span style={{ marginLeft: 'auto', display: 'flex', gap: 4 }}>
                        <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => { navigator.clipboard?.writeText(info.value === null ? '' : renderCell(info.value)); setCopied('已复制'); setTimeout(() => setCopied(''), 1200) }}>复制值</button>
                        <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => { navigator.clipboard?.writeText(info.column); setCopied('已复制'); setTimeout(() => setCopied(''), 1200) }}>复制列名</button>
                      </span>
                    )}
                  </div>
                  {detailEdit ? (
                    <>
                      <textarea
                        className="input"
                        style={{ minHeight: '6rem', fontFamily: 'ui-monospace, Consolas, monospace', fontSize: '0.75rem' }}
                        value={detailDraft}
                        placeholder={info.value === null ? 'NULL (空串提交为空字符串; 置 NULL 用右键菜单)' : undefined}
                        onChange={e => setDetailDraft(e.target.value)}
                        autoFocus
                      />
                      <div style={{ display: 'flex', gap: 6, justifyContent: 'flex-end', paddingTop: 6 }}>
                        <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => setDetailEdit(false)} disabled={savingEdit}>取消</button>
                        <button className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-accent" onClick={saveDetailEdit} disabled={savingEdit}>
                          {savingEdit ? '保存中...' : (batchMode ? '加入待提交' : '保存修改 (按主键 UPDATE)')}
                        </button>
                      </div>
                    </>
                  ) : (
                    <pre>{info.value === null ? <i className="dim">NULL</i> : typeof info.value === 'object' ? JSON.stringify(info.value, null, 2) : String(info.value)}</pre>
                  )}
                </div>
          </DetailOverlay>
        )
      })()}
      {/* 行详情: 字段列表(列名+值, 可过滤) + 复制 JSON/TSV */}
      {rowDetail !== null && (() => {
        if (!result || !result.columns) return null
        const fields = result.columns
          .map((column, c) => buildCellInfo(rowDetail, c))
          .filter(Boolean) as NonNullable<ReturnType<typeof buildCellInfo>>[]
        const kw = fieldFilter.trim().toLowerCase()
        const shown = fields.filter(f => !kw || f.column.toLowerCase().includes(kw) || String(f.value ?? '').toLowerCase().includes(kw))
        const json = JSON.stringify(Object.fromEntries(fields.map(f => [f.column, f.value])), null, 2)
        const tsv = fields.map(f => renderCell(f.value)).join('	')
        return (
          <DetailOverlay
            title={<>行详情 · 第 {rowDetail + 1} 行 <span className="dim">{fields.length} 列</span></>}
            onClose={() => setRowDetail(null)}
            head={<>
                <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => { navigator.clipboard?.writeText(json); setCopied('已复制 JSON'); setTimeout(() => setCopied(''), 1200) }}>复制 JSON</button>
                <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => { navigator.clipboard?.writeText(tsv); setCopied('已复制 TSV'); setTimeout(() => setCopied(''), 1200) }}>复制 TSV</button>
                <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => { const NL = String.fromCharCode(10); const md = '| ' + fields.map(f => f.column).join(' | ') + ' |' + NL + '| ' + fields.map(() => '---').join(' | ') + ' |' + NL + fields.map(f => '| ' + (f.value === null ? 'NULL' : String(f.value)) + ' |').join(NL); navigator.clipboard?.writeText(md); setCopied('已复制 Markdown'); setTimeout(() => setCopied(''), 1200) }}>复制 Markdown</button>
                </>}>
                <input className="input input-sm" style={{ marginBottom: 6 }} placeholder="过滤列名 / 值..." value={fieldFilter} onChange={e => setFieldFilter(e.target.value)} />
                <div className="db-detail-fields">
                  {shown.map(f => (
                    <div key={f.column} className="db-detail-field-row">
                      <span className="db-detail-field-name" title={f.column}>{f.column}</span>
                      <span className="db-detail-field-val">{f.value === null ? <i className="dim">NULL</i> : renderCell(f.value)}</span>
                    </div>
                  ))}
                  {shown.length === 0 && <div className="db-empty-sm">无匹配字段</div>}
                </div>
          </DetailOverlay>
        )
      })()}
      {/* 转置显示: 单行按字段名×值两列铺开 */}
      {transpose !== null && (() => {
        if (!result || !result.columns) return null
        const fields = result.columns
          .map((column, c) => buildCellInfo(transpose.r, c))
          .filter(Boolean) as NonNullable<ReturnType<typeof buildCellInfo>>[]
        return (
          <DetailOverlay
            title={<>转置 · 第 {transpose.r + 1} 行</>}
            onClose={() => setTranspose(null)}>
                <div className="db-detail-fields">
                  {fields.map(f => (
                    <div key={f.column} className="db-detail-field-row">
                      <span className="db-detail-field-name" title={f.column}>{f.column}</span>
                      <span className="db-detail-field-val">{f.value === null ? <i className="dim">NULL</i> : renderCell(f.value)}</span>
                    </div>
                  ))}
                </div>
          </DetailOverlay>
        )
      })()}
      {/* 列详情: 列元数据头 + 本列逐行值(可过滤) */}
      {colDetail !== null && (() => {
        if (!result || !result.columns) return null
        const meta = buildCellInfo(0, colDetail)
        if (!meta) return null
        const kw = fieldFilter.trim().toLowerCase()
        const rows = result.rows
          .map((row, r) => ({ rowNumber: r + 1, value: row[colDetail] }))
          .filter(x => !kw || String(x.value ?? '').toLowerCase().includes(kw) || String(x.rowNumber).includes(kw))
        const tsv = result.rows.map(row => renderCell(row[colDetail])).join('\n')
        const json = JSON.stringify(result.rows.map((row, r) => ({ row: r + 1, value: row[colDetail] })), null, 2)
        return (
          <DetailOverlay
            title={<>列详情 · {meta.column}</>}
            onClose={() => setColDetail(null)}
            head={<>
                <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => { navigator.clipboard?.writeText(json); setCopied('已复制 JSON'); setTimeout(() => setCopied(''), 1200) }}>复制 JSON</button>
                <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => { navigator.clipboard?.writeText(tsv); setCopied('已复制 全列值'); setTimeout(() => setCopied(''), 1200) }}>复制全列值</button>
                </>}>
                <div className="db-cell-meta">
                  <div><span className="dim">类型</span>{meta.type || '-'}</div>
                  {columnMeta && <div><span className="dim">可空</span>{meta.nullable ? 'YES' : 'NO'}</div>}
                  {columnMeta && <div><span className="dim">键</span>{meta.key || '-'}</div>}
                  <div><span className="dim">行数</span>{result.rows.length}</div>
                </div>
                <div className="db-cell-meta-comment">
                  <span className="dim">注释</span>
                  <span>{meta.comment || '暂无注释'}</span>
                </div>
                <input className="input input-sm" style={{ margin: '6px 0' }} placeholder="过滤值 / 行号..." value={fieldFilter} onChange={e => setFieldFilter(e.target.value)} />
                <div className="db-detail-fields">
                  {rows.map(x => (
                    <div key={x.rowNumber} className="db-detail-field-row">
                      <span className="db-detail-field-name">{x.rowNumber}</span>
                      <span className="db-detail-field-val">{x.value === null ? <i className="dim">NULL</i> : renderCell(x.value)}</span>
                    </div>
                  ))}
                  {rows.length === 0 && <div className="db-empty-sm">无匹配行</div>}
                </div>
          </DetailOverlay>
        )
      })()}
      {workerCfg && (
        <DetailOverlay title="雪花 workerId" onClose={() => setWorkerCfg(null)}>
          <div className="db-worker-cfg">
            {([
              ['machine', '本机派生（默认）', '由后端按本机 IP 末段 + 进程号算出：零配置，同机多实例能错开。业界 ID 库的默认路子。'],
              ['random', '随机', '每次会话随机取一个（dbx 的做法，它没有后端）。跨进程不保证不撞。'],
              ['fixed', '指定', '你已经有 workerId 分配表（ZK / DB 注册那类）时手填，0~1023。'],
            ] as const).map(([mode, label, hint]) => (
              <div key={mode} className={`db-worker-row${workerCfg.mode === mode ? ' active' : ''}`}>
                <label className="db-worker-name">
                  <input type="radio" name="db-worker-mode" checked={workerCfg.mode === mode} onChange={() => setWorkerCfg({ ...workerCfg, mode })} />
                  <b>{label}</b>
                </label>
                <div className="db-worker-hint">{hint}</div>
                {mode === 'fixed' && workerCfg.mode === 'fixed' && (
                  <input className="input db-worker-input" type="number" min={0} max={1023} placeholder="0 ~ 1023"
                    value={workerCfg.fixed} onChange={e => setWorkerCfg({ ...workerCfg, fixed: e.target.value })} />
                )}
              </div>
            ))}
            <div className="db-worker-actions">
              <span className="dim">当前：{workerPolicyLabel(workerPolicy)}</span>
              <button className="btn-glass-soft btn-glass-soft-sm" onClick={saveWorkerCfg}>保存</button>
            </div>
          </div>
        </DetailOverlay>
      )}
      {confirmEl}
    </div>
  )
}
