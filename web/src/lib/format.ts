/**
 * 全局格式化工具 —— 公共层 L2
 *
 * 由来：本文件合并了此前散落在 10 处的重复实现。各调用点的原有口径如下（已逐一核对，
 * 差异处见「有意变更」说明）：
 *
 *   字节 · KiB/MiB/GiB 系
 *     modules/DockerModule.tsx:815            fmtBytes
 *     modules/TasksModule.tsx:1043            fmtBytes (内联, 只有 GiB/MiB 两档)
 *   字节 · KB/MB/GB 系
 *     modules/dbmanager/ServerDashboardPanel.tsx:15   fmtBytes
 *     modules/dbmanager/TableOverviewPanel.tsx:9      fmtBytes
 *     components/MultiOverview.tsx:31                          fmtBytes
 *     modules/ResourcesModule.tsx:52                           fmtBytes
 *     modules/LogMonitorModule.tsx:245                         fmtBytes
 *     modules/cicd/shared.tsx:195                              fmtSize
 *   速率
 *     components/MultiOverview.tsx:38                          fmtRate  (字节/秒)
 *     modules/dbmanager/ServerDashboardPanel.tsx:22   fmtRate  (次/秒, 不是字节!)
 *   时间 / 时长
 *     modules/LogMonitorModule.tsx:238                         fmtTime  (固定格式, 非 locale)
 *     modules/LogMonitorModule.tsx:1162                        fmtDur   (中文长格式)
 *     modules/cicd/shared.tsx:187                              fmtDur
 *     modules/dbmanager/AuditPanel.tsx:19             timeAgo  (紧凑格式)
 *   数字
 *     modules/dbmanager/ServerDashboardPanel.tsx:14   fmtNum   (en-US, 最多 1 位小数)
 *     modules/ServicesModule.tsx:422                           fmtPct
 *
 * 命名上刻意区分了两个此前同名但语义不同的东西：
 *   fmtByteRate = 字节/秒（会自动缩放到 KiB/s）  ← 原 MultiOverview.fmtRate
 *   fmtPerSec   = 次数/秒（不缩放，QPS/TPS 用）  ← 原 ServerDashboardPanel.fmtRate
 *
 * 「无值」统一用 U+2014 长破折号 `—`，不再混用 ASCII `-`。
 */

/** 无值占位符。全文件统一用它，避免各模块里 '-' / '—' / '暂无' 混用 */
export const EMPTY = '—'

/**
 * 1024 进制字节格式化，单位 KiB/MiB/GiB/TiB/PiB
 *
 * 进位规则：**当前档位数值达到 1024 才进位**，所以显示值永远落在 [1, 1024)，
 * 不会出现 0.04 GiB / 0.7 GB 这类需要换算才看得懂的小数值。
 *   716 MB  → "716.00 MiB"（而不是 0.70 GiB）
 *   1 GiB   → "1.00 GiB"
 *   1023 B  → "1023 B"
 */
export function fmtBytes(n: number | null | undefined, digits = 2): string {
  if (n == null || !isFinite(n) || n < 0) return EMPTY
  if (n < 1024) return `${Math.round(n)} B`
  return scale(n, digits, ['KiB', 'MiB', 'GiB', 'TiB', 'PiB'])
}

/**
 * 1024 进制字节格式化，单位 KB/MB/GB/TB（历史口径，资源/总览/数据库类面板在用）
 * 进位规则与 fmtBytes 一致，同样不会产出小于 1 的显示值。
 */
export function fmtSize(n: number | null | undefined, digits = 2): string {
  if (n == null || !isFinite(n) || n < 0) return EMPTY
  if (n < 1024) return `${Math.round(n)} B`
  return scale(n, digits, ['KB', 'MB', 'GB', 'TB', 'PB'])
}

/** 1024 进制缩放。进位判据用「四舍五入后的值」，否则 1073741823 会输出 1024.00 MiB */
function scale(n: number, digits: number, units: string[]): string {
  const p = 10 ** digits
  const rounded = (x: number) => Math.round(x * p) / p
  let v = n
  let i = -1
  do {
    v /= 1024
    i++
  } while (rounded(v) >= 1024 && i < units.length - 1)
  return `${v.toFixed(digits)} ${units[i]}`
}

/** 字节速率：自动缩放成 KiB/s、MiB/s… */
export function fmtByteRate(n: number | null | undefined, digits = 2): string {
  return `${fmtBytes(n, digits)}/s`
}

/**
 * 计数速率：QPS / TPS / 次每秒。**不缩放单位**，只做千分位。
 * 小数最多 digits 位且不补零（1234 → "1,234/s"，1234.56 → "1,234.6/s"），
 * 保持数据库监控面板原有显示。
 */
export function fmtPerSec(n: number | null | undefined, digits = 1): string {
  if (n == null || !isFinite(n)) return `${EMPTY}/s`
  return `${n.toLocaleString('en-US', { maximumFractionDigits: digits })}/s`
}

/**
 * 绝对时间（跟随浏览器 locale）。接受 ISO 字符串、时间戳（秒或毫秒）。
 *   需要稳定格式（可 slice、可比较字符串）时改用 fmtDateTime
 */
export function fmtTime(t?: string | number | Date | null): string {
  const d = toDate(t)
  return d ? d.toLocaleString() : EMPTY
}

/**
 * 固定格式时间 `YYYY-MM-DD HH:mm:ss`，与 locale 无关。
 * 日志时间轴这类「截取子串」「字符串排序」的场景必须用它——
 * toLocaleString 在中文环境下是 `2026/9/16 16:30:00`，字符位置对不上。
 */
export function fmtDateTime(t?: string | number | Date | null): string {
  const d = toDate(t)
  if (!d) return EMPTY
  const p = (x: number) => String(x).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} `
    + `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

/**
 * 相对时间。
 *   compact=false（默认）→ 刚刚 / 3 分钟前 / 2 小时前 / 3 天前
 *   compact=true         → 45s 前 / 3m 前 / 2h 前 / 绝对时间（表格里更省宽度）
 */
export function timeAgo(t: number | string | null | undefined, compact = false): string {
  const d = toDate(t)
  if (!d) return EMPTY
  const s = Math.floor((Date.now() - d.getTime()) / 1000)
  if (s < 0) return fmtDateTime(d)
  if (compact) {
    if (s < 60) return `${s}s 前`
    if (s < 3600) return `${Math.floor(s / 60)}m 前`
    if (s < 86400) return `${Math.floor(s / 3600)}h 前`
    return fmtTime(d)
  }
  if (s < 60) return '刚刚'
  if (s < 3600) return `${Math.floor(s / 60)} 分钟前`
  if (s < 86400) return `${Math.floor(s / 3600)} 小时前`
  if (s < 2592000) return `${Math.floor(s / 86400)} 天前`
  return fmtTime(d)
}

/**
 * 时长（紧凑，贴在数字旁边用）：1.2s / 3m05s / 1h30m
 * 0 与空值都返回 EMPTY —— 原 cicd/LogMonitor 的实现都是 `!ms → '-'`，
 * 保留这个语义，避免「没有时长记录」被显示成 0ms。
 */
export function fmtDur(ms: number | null | undefined): string {
  if (ms == null || !isFinite(ms) || ms <= 0) return EMPTY
  if (ms < 1000) return `${Math.round(ms)}ms`
  const s = ms / 1000
  if (s < 60) return `${s.toFixed(1)}s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m${String(Math.floor(s % 60)).padStart(2, '0')}s`
  return `${Math.floor(m / 60)}h${String(m % 60).padStart(2, '0')}m`
}

/**
 * 时长（中文长格式，给中文表格/描述句用）：30 秒 / 5 分钟 / 2 小时
 * 只在使用者明确要读成中文时才用，机器感强的位置用 fmtDur。
 */
export function fmtDurCn(ms: number | null | undefined): string {
  if (ms == null || !isFinite(ms) || ms <= 0) return EMPTY
  if (ms % 3600000 === 0) return `${ms / 3600000} 小时`
  if (ms % 60000 === 0) return `${ms / 60000} 分钟`
  return `${Math.round(ms / 1000)} 秒`
}

/** 大数字千分位。默认 0 位小数（计数场景），需要固定位数就传 digits */
export function fmtNum(n: number | null | undefined, digits = 0): string {
  if (n == null || !isFinite(n)) return EMPTY
  return n.toLocaleString('zh-CN', { minimumFractionDigits: digits, maximumFractionDigits: digits })
}

/** 百分比。注意 0 会正常显示成 0.00%，不当作"无值" */
export function fmtPct(n: number | null | undefined, digits = 2): string {
  if (n == null || !isFinite(n)) return EMPTY
  return `${n.toFixed(digits)}%`
}

/** 截断长文本（按字符数，非字节） */
export function truncate(s: string, max = 40): string {
  if (!s) return ''
  return s.length <= max ? s : s.slice(0, max - 1) + '…'
}

/**
 * 归一化成 Date。数字时间戳按 <1e12 视为「秒」，否则视为「毫秒」。
 * 秒/毫秒混用是这个仓的历史现实（后端有的接口给秒有的给毫秒），在此统一兜住。
 */
function toDate(t?: string | number | Date | null): Date | null {
  if (t == null || t === '') return null
  if (t instanceof Date) return isNaN(t.getTime()) ? null : t
  const d = typeof t === 'number' ? new Date(t < 1e12 ? t * 1000 : t) : new Date(t)
  return isNaN(d.getTime()) ? null : d
}
