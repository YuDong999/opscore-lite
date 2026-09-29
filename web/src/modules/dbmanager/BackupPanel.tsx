// 数据库备份面板(P1-7 前端)。
//
// 这个面板的设计重点不是"点一下就备份", 而是**让人在跑之前就知道这份备份完不完整**:
//  1. 选库 → **先探路**(backupPlan) → 展示"会走哪条路径 / 包含什么 / **不含什么** / 一致性口径";
//  2. 确认后再跑;
//  3. 历史列表里每条都带 mode 与 excludes —— "内置导出"那几条会明确标着缺触发器/例程/权限。
//
// 为什么这么较真: 两条路径(native 原生工具 / builtin 内置导出)的保真度**不同**,
// 而用户拿到一个 .sql.gz 时默认会以为它能完整恢复。把"不含什么"摆在明面上,
// 比事后解释"为什么恢复完少了触发器"便宜得多。

import { useCallback, useEffect, useState } from 'react'
import {
  backupList, backupPlan, backupRun,
  type BackupPlan, type BackupRecord, type ConnectionInfo,
} from './api'
import { useToast } from '../../components/Toast'
import { useConfirm } from '../../lib/hooks/useConfirm'

function fmtSize(n: number) {
  if (!n || n < 0) return '—'
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  if (n < 1024 * 1024 * 1024) return `${(n / 1048576).toFixed(1)} MB`
  return `${(n / 1073741824).toFixed(2)} GB`
}

function fmtTime(sec: number) {
  if (!sec) return '—'
  const d = new Date(sec * 1000)
  const p = (x: number) => String(x).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

// modeLabel 把路径名翻成人能判断的短语, 并给出"完不完整"的第一印象。
function modeLabel(m?: string) {
  switch (m) {
    case 'native': return { text: '原生工具', cls: 'ok' }
    case 'builtin': return { text: '内置导出', cls: 'warn' }
    default: return { text: m || '—', cls: '' }
  }
}

export default function BackupPanel({
  conns, activeConn,
}: {
  conns: ConnectionInfo[]
  activeConn: ConnectionInfo | null
}) {
  const toast = useToast()
  const { confirm: askConfirm, confirmEl } = useConfirm()
  const [database, setDatabase] = useState(activeConn?.config?.database || '')
  const [dbs, setDbs] = useState<string[]>([])
  const [tablesInput, setTablesInput] = useState('')
  const [dir, setDir] = useState('')
  const [keep, setKeep] = useState(0)
  const [forceBuiltin, setForceBuiltin] = useState(false)
  const [plan, setPlan] = useState<BackupPlan | null>(null)
  const [records, setRecords] = useState<BackupRecord[]>([])
  const [defaults, setDefaults] = useState<{ dir: string; keep: number }>({ dir: '', keep: 5 })
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')

  const reload = useCallback(async () => {
    try {
      // 历史按当前连接过滤: 面板的上下文就是"这台连接"
      const r = await backupList(activeConn?.id)
      setRecords(r.records || [])
      setDefaults({ dir: r.defaultDir, keep: r.defaultKeep })
    } catch {
      setRecords([])
    }
  }, [activeConn?.id])

  useEffect(() => { void reload() }, [reload])
  useEffect(() => {
    // 换连接时清掉上一次的探路结果 —— 留着最容易让人看错库
    setPlan(null)
    setDatabase(activeConn?.config?.database || '')
  }, [activeConn?.id])

  // 库列表: 复用元数据端点(与其他面板一致)
  useEffect(() => {
    if (!activeConn) { setDbs([]); return }
    let dead = false
    const t = localStorage.getItem('opscore-token')
    fetch(`/api/dbmanager/metadata?type=databases&id=${encodeURIComponent(activeConn.id)}`, {
      headers: t ? { Authorization: `Bearer ${t}` } : {},
    }).then(r => r.json()).then(d => { if (!dead) setDbs(d.databases || []) }).catch(() => {})
    return () => { dead = true }
  }, [activeConn?.id])

  const parseTables = () => tablesInput.split(/[,\s]+/).map(s => s.trim()).filter(Boolean)

  const buildReq = () => ({
    connId: activeConn!.id,
    database: database.trim(),
    tables: parseTables(),
    dir: dir.trim() || undefined,
    keep: keep > 0 ? keep : undefined,
    forceBuiltin,
  })

  const doPlan = async () => {
    if (!activeConn) { toast.error('先选一个连接'); return }
    if (!database.trim()) { toast.error('先选要备份的库'); return }
    setBusy(true); setErr('')
    try {
      const r = await backupPlan(buildReq())
      if (r.error || !r.plan) { setErr(r.error || '探路失败'); setPlan(null); return }
      // 容错: 接口可能给 null(后端不守 [] 约定时), 这里归一到空数组 ——
      // 渲染不因为一个 null 整块崩(崩了的表现是"模块渲染出错", 完全看不出原因)。
      setPlan({
        ...r.plan,
        includes: r.plan.includes || [],
        excludes: r.plan.excludes || [],
        nativeCandidates: r.plan.nativeCandidates || [],
      })
    } catch (e: any) {
      setErr(e.message || '探路失败')
    } finally {
      setBusy(false)
    }
  }

  const doRun = async () => {
    if (!plan) { await doPlan(); return }
    const incomplete = (plan.excludes || []).length > 0
    const ok = await askConfirm(`备份 ${plan.database}?`, {
      desc: incomplete
        ? `⚠ 这份备份**不含**: ${plan.excludes.join('、')}。需要完整恢复请用原生工具(装 mysqldump/pg_dump)。`
        : `走原生工具 ${plan.tool}, 一致性: ${plan.consistency}`,
      okText: '开始备份',
      danger: incomplete,
      maxWidth: 620,
    })
    if (!ok) return
    setBusy(true); setErr('')
    try {
      const r = await backupRun(buildReq())
      const rec = r.record
      if (rec?.status === 'ok') {
        toast.success(`备份完成: ${fmtSize(rec.sizeBytes)}${(r.pruned?.length || 0) > 0 ? ` · 清理 ${r.pruned!.length} 份旧备份` : ''}`)
      } else {
        const msg = rec?.error || r.error || '备份失败'
        setErr(msg)
        toast.error(msg)
      }
      await reload()
    } catch (e: any) {
      setErr(e.message || '备份失败')
    } finally {
      setBusy(false)
    }
  }

  if (!activeConn) return <div className="db-empty">先在上面的连接列表里选一个连接</div>

  // 渲染前统一归一: 接口给 null 也不能让整块崩(崩了的表现是"模块渲染出错", 看不出原因)。
  // 教训: 2026-09-29 真机上 excludes=null 就报了
  // "Cannot read properties of undefined (reading 'length')"。
  const planExcludes = plan?.excludes || []
  const planIncludes = plan?.includes || []

  return (
    <div className="db-backup">
      <div className="db-backup-form">
        <div className="db-backup-row">
          <label className="db-backup-field">
            <span className="dim">数据库</span>
            <input className="input" list="db-backup-dbs" value={database} placeholder="要备份的库"
              onChange={e => { setDatabase(e.target.value); setPlan(null) }} />
            <datalist id="db-backup-dbs">{dbs.map(d => <option key={d} value={d} />)}</datalist>
          </label>
          <label className="db-backup-field">
            <span className="dim">目标目录(目标机上)</span>
            <input className="input" value={dir} placeholder={defaults.dir || '/var/backups/opscore'}
              onChange={e => setDir(e.target.value)} />
          </label>
          <label className="db-backup-field" style={{ maxWidth: '7rem' }}>
            <span className="dim">保留份数</span>
            <input className="input" type="number" min={0} value={keep || ''}
              placeholder={String(defaults.keep || 5)} onChange={e => setKeep(Number(e.target.value))} />
          </label>
        </div>
        <div className="db-backup-row">
          <label className="db-backup-field" style={{ flex: 1 }}>
            <span className="dim">只备份这些表(留空=整库; 逗号/空格分隔)</span>
            <input className="input" value={tablesInput} placeholder="例如 employees, dept_emp"
              onChange={e => { setTablesInput(e.target.value); setPlan(null) }} />
          </label>
          <label className="db-backup-check" title="不依赖工具探测, 强制走内置导出(跨引擎口径一致, 但不含触发器/例程/权限)">
            <input type="checkbox" checked={forceBuiltin}
              onChange={e => { setForceBuiltin(e.target.checked); setPlan(null) }} />
            <span>强制内置导出</span>
          </label>
        </div>
        <div className="db-backup-acts">
          <button className="btn-glass-soft btn-glass-soft-sm" disabled={busy} onClick={doPlan}>
            {busy ? '处理中…' : '探路(先看会做什么)'}
          </button>
          <button className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-accent" disabled={busy} onClick={doRun}>
            开始备份
          </button>
          <button className="btn-glass-soft btn-glass-soft-sm" disabled={busy} onClick={reload}>刷新历史</button>
        </div>
      </div>

      {err && <div className="banner banner-err small">{err}</div>}

      {plan && (
        <div className="db-backup-plan">
          <div className="db-backup-plan-head">
            <span className={`db-backup-mode db-backup-mode-${modeLabel(plan.mode).cls}`}>
              {modeLabel(plan.mode).text}
            </span>
            <span className="dim">{plan.tool || '（内置导出，不用外部工具）'}</span>
            <span className="dim" style={{ marginLeft: 'auto' }}>写到 {plan.dir}</span>
          </div>
          <div className="db-backup-plan-grid">
            <div>
              <div className="dim small">一致性</div>
              <div>{plan.consistency}</div>
            </div>
            <div>
              <div className="dim small">包含</div>
              <div>{(plan.includes || []).join(' · ')}</div>
            </div>
            <div>
              <div className="dim small">不含{planExcludes.length === 0 ? '（完整）' : ''}</div>
              <div className={planExcludes.length ? 'db-backup-warn' : 'dim'}>
                {planExcludes.length ? planExcludes.join(' · ') : '——'}
              </div>
            </div>
          </div>
          {plan.mode === 'builtin' && (plan.nativeCandidates?.length || 0) > 0 && (
            <div className="db-backup-hint">
              目标机上没找到 {plan.nativeCandidates!.join(' / ')} —— 装上它就能走原生路径(保真度更高)。
            </div>
          )}
        </div>
      )}

      <div className="table-wrap" style={{ maxHeight: '24rem', overflow: 'auto' }}>
        <table className="db-table">
          <thead>
            <tr>
              <th style={{ width: '9rem' }}>时间</th>
              <th>库</th>
              <th style={{ width: '6.5rem' }}>方式</th>
              <th style={{ width: '5.5rem' }}>大小</th>
              <th>文件</th>
              <th>不含</th>
              <th style={{ width: '4.5rem' }}>结果</th>
            </tr>
          </thead>
          <tbody>
            {records.length === 0 ? (
              <tr><td colSpan={7} className="dim" style={{ textAlign: 'center', padding: 12 }}>
                还没有备份记录 —— 选好库后点「探路」看清会做什么, 再点「开始备份」
              </td></tr>
            ) : records.map(r => (
              <tr key={r.id}>
                <td className="mono small">{fmtTime(r.startedAt)}</td>
                <td>{r.database}</td>
                <td>
                  <span className={`db-backup-mode db-backup-mode-${modeLabel(r.mode).cls}`}>
                    {modeLabel(r.mode).text}
                  </span>
                </td>
                <td className="mono small">{fmtSize(r.sizeBytes)}</td>
                <td className="mono small" title={r.filePath}>{r.filePath}</td>
                <td className={r.excludes?.length ? 'db-backup-warn small' : 'dim small'}>
                  {r.excludes?.length ? r.excludes.join(' · ') : '——'}
                </td>
                <td>
                  {r.status === 'ok'
                    ? <span className="db-backup-ok">成功</span>
                    : <span className="db-backup-bad" title={r.error}>失败</span>}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {confirmEl}
    </div>
  )
}
