// ── 任务与存储模块: 定时任务编辑器 / 磁盘挂载 / SMART 健康 ──

import { useCallback, useEffect, useState } from 'react'
import { getJSON, postJSON } from '../api/client'
import Card from '../components/Card'
import { useHost } from '../components/HostContext'
import HostSelector from '../components/HostSelector'
import { useToast } from '../components/Toast'

// ── LVM 类型 ──
interface PV { name: string; size: string; free: string; vg: string }
interface VG { name: string; size: string; free: string; pv: string; lv: string }
interface LV { name: string; size: string; vg: string; path: string; mounted: string }
interface LvmData { hasLvm: boolean; pvs: PV[]; vgs: VG[]; lvs: LV[] }

type Permission = 'root' | 'user'
type Crontab = { content: string; error?: string; permission: Permission }
type DeviceInfo = { name: string; size: string; type: string; fstype: string; mountpoint: string }
type FreeSpace = { start: string; end: string; size: string }
type Disks = { lsblk: string; mounts: string; df: string; devices: DeviceInfo[]; permission: Permission }
type DiskActionResult = { ok?: boolean; error?: string; output?: string; newPartition?: string; permission: Permission }

export default function TasksModule() {
  const { selected } = useHost()
  const h = selected?.id ? `?host=${selected.id}` : ''
  const [tab, setTab] = useState('crontab')
  const [perm, setPerm] = useState<Permission>('user')

  useEffect(() => {
    getJSON<{ permission: Permission }>('/api/core/tasks/disks'+h).then(d => setPerm(d.permission)).catch(() => {})
  }, [selected])

  const tabs = [
    { id: 'crontab', label: '定时任务' },
    { id: 'disks', label: '磁盘挂载' },
    { id: 'lvm', label: 'LVM 存储' },
    { id: 'smart', label: 'SMART 健康' },
  ]

  return (
    <div className="module">
      <div className="module-head">
        <div className="module-head-row"><h2>任务与存储</h2><HostSelector /></div>
        <span className="pill">{perm === 'root' ? 'root 权限' : '受限模式'}</span>
      </div>

      <div className="tabs">
        {tabs.map(t => (
          <button key={t.id} className={`tab ${tab === t.id ? 'tab-on' : ''}`} onClick={() => setTab(t.id)}>{t.label}</button>
        ))}
      </div>

      {tab === 'crontab' && <CrontabSection />}
      {tab === 'disks' && <DisksSection />}
      {tab === 'lvm' && <LvmSection />}
      {tab === 'smart' && <SmartSection />}
    </div>
  )
}

// ================= Cron 解析/生成工具 =================

type CronTask = {
  id: string
  minute: string
  hour: string
  dayOfMonth: string
  month: string
  dayOfWeek: string
  command: string
  comment: string
}

function parseCronLine(line: string): CronTask | null {
  // 移除前后空格
  const trimmed = line.trim()
  if (!trimmed || trimmed.startsWith('#')) return null
  
  // 检查是否有注释（# 开头的部分）
  const commentIndex = trimmed.indexOf('#')
  let cleanLine = trimmed
  let comment = ''
  
  if (commentIndex !== -1) {
    // 分离命令部分和注释部分
    comment = trimmed.substring(commentIndex + 1).trim()
    cleanLine = trimmed.substring(0, commentIndex).trim()
  }
  
  // 解析时间和命令部分
  const parts = cleanLine.split(/\s+/)
  if (parts.length < 6) return null
  
  // 生成基于调度和命令的确定性ID（忽略注释）
  const id = `${parts[0]}|${parts[1]}|${parts[2]}|${parts[3]}|${parts[4]}|${parts.slice(5).join(' ')}`
  
  return {
    id,
    minute: parts[0],
    hour: parts[1],
    dayOfMonth: parts[2],
    month: parts[3],
    dayOfWeek: parts[4],
    command: parts.slice(5).join(' '),
    comment,
  }
}

function parseCrontab(text: string): CronTask[] {
  return text.split('\n').map(l => parseCronLine(l)).filter((t): t is CronTask => t !== null)
}

function buildCronLine(task: CronTask): string {
  const timePart = [task.minute, task.hour, task.dayOfMonth, task.month, task.dayOfWeek].join(' ')
  if (task.comment.trim() !== '') {
    return `${timePart} ${task.command} # ${task.comment}`
  } else {
    return `${timePart} ${task.command}`
  }
}

function buildCrontab(tasks: CronTask[]): string {
  return tasks.map(buildCronLine).join('\n')
}

function cronToHuman(t: CronTask): string {
  const parts: string[] = []
  if (t.minute === '*') parts.push('每分钟')
   else if (t.minute.startsWith('*/')) parts.push(`每 ${t.minute.slice(2)} 分钟`)
  else if (t.minute.includes(',')) parts.push(`分钟 ${t.minute}`)
  else parts.push(`${t.minute} 分`)
  if (t.hour === '*') parts.push('每小时')
  else if (t.hour.startsWith('*/')) parts.push(`每 ${t.hour.slice(2)} 小时`)
  else if (t.hour.includes(',')) parts.push(`小时 ${t.hour}`)
  else parts.push(`${t.hour} 时`)
  if (t.dayOfMonth === '*' && t.dayOfWeek === '*') parts.push('每天')
  else if (t.dayOfMonth !== '*') {
    if (t.dayOfMonth.startsWith('*/')) parts.push(`每 ${t.dayOfMonth.slice(2)} 天`)
    else if (t.dayOfMonth.includes(',')) parts.push(`每月 ${t.dayOfMonth} 号`)
    else parts.push(`每月 ${t.dayOfMonth} 号`)
  } else if (t.dayOfWeek !== '*') {
    const weekMap: Record<string, string> = { '0': '日', '7': '日', '1': '一', '2': '二', '3': '三', '4': '四', '5': '五', '6': '六' }
    if (t.dayOfWeek.startsWith('*/')) parts.push(`每 ${t.dayOfWeek.slice(2)} 周`)
    else if (t.dayOfWeek.includes(',')) parts.push(`周 ${t.dayOfWeek.split(',').map(w => weekMap[w] || w).join(',')}`)
    else parts.push(`周${weekMap[t.dayOfWeek] || t.dayOfWeek}`)
  }
  if (t.month !== '*') {
    if (t.month.startsWith('*/')) parts.push(`每 ${t.month.slice(2)} 个月`)
    else if (t.month.includes(',')) parts.push(`月份 ${t.month}`)
    else parts.push(`${t.month} 月`)
  }
  return parts.join(' · ') || '每分钟'
}

const MINUTE_OPTS = ['*', '0', '1', '2', '3', '4', '5', '10', '15', '20', '30', '*/5', '*/10', '*/15', '*/30']
const HOUR_OPTS = ['*', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9', '10', '11', '12', '13', '14', '15', '16', '17', '18', '19', '20', '21', '22', '23', '*/2', '*/4', '*/6', '*/12']
const DOM_OPTS = ['*', '1', '2', '3', '4', '5', '6', '7', '8', '9', '10', '11', '12', '13', '14', '15', '16', '17', '18', '19', '20', '21', '22', '23', '24', '25', '26', '27', '28', '29', '30', '*/2', '*/3']
const MONTH_OPTS = ['*', '1', '2', '3', '4', '5', '6', '7', '8', '9', '10', '11', '12', '*/2', '*/3']
const DOW_OPTS = ['*', '0', '1', '2', '3', '4', '5', '6', '7', '*/2', '*/3']

// ================= 定时任务可视化编辑器 =================

function CrontabSection() {
  const { selected } = useHost()
  const h = selected?.id ? `&host=${selected.id}` : ''
  const [user, setUser] = useState('root')
  const [tasks, setTasks] = useState<CronTask[]>([])
  const [rawContent, setRawContent] = useState('')
  const [editingId, setEditingId] = useState<string | null>(null)
  const [form, setForm] = useState<CronTask | null>(null)
  const [msg, setMsg] = useState('')
  const [loading, setLoading] = useState(true)
  const [showRaw, setShowRaw] = useState(false)

  const load = useCallback(() => {
    setLoading(true)
    getJSON<Crontab>(`/api/core/tasks/crontab?user=${user}${h}`).then(d => {
      setRawContent(d.content || '')
      setTasks(parseCrontab(d.content || ''))
      setLoading(false)
    }).catch(() => setLoading(false))
  }, [user, selected])

  useEffect(() => { load() }, [load])

  const save = async () => {
    const content = showRaw ? rawContent : buildCrontab(tasks)
    try {
      const body: Record<string, any> = { user, content }
      const q = selected?.id ? `?host=${selected.id}` : ''
      const res = await postJSON<{ ok?: boolean; error?: string; permission: Permission }>(`/api/core/tasks/crontab${q}`, body)
      if (res.ok) { setMsg('✓ 已保存') }
      else setMsg(`✗ ${res.error || '保存失败'}`)
    } catch { setMsg('✗ 保存失败') }
    setTimeout(() => setMsg(''), 3000)
  }

  const startEdit = (task: CronTask) => setForm({ ...task })
  const cancelEdit = () => setForm(null)

  const handleChange = (field: keyof CronTask, value: string) => {
    setForm(f => f ? { ...f, [field]: value } : null)
  }

  const submitEdit = () => {
    if (!form) return
    setTasks(tasks.map(t => t.id === form.id ? form : t))
    setForm(null)
  }

  const deleteTask = (id: string) => {
    if (!confirm('确定删除该任务？')) return
    setTasks(tasks.filter(t => t.id !== id))
  }

  const addTask = () => {
    const newTask: CronTask = {
      id: 'new-task-' + Date.now(), // 临时ID，保存时会被替换为稳定ID
      minute: '0',
      hour: '3',
      dayOfMonth: '*',
      month: '*',
      dayOfWeek: '*',
      command: '',
      comment: '',
    }
    setTasks([...tasks, newTask])
    setTimeout(() => setForm({ ...newTask, command: '' }), 0)
  }

  if (loading) return <div className="loading">加载中…</div>

  return (
    <Card title="定时任务" subtitle={showRaw ? '原始文本模式' : '可视化编辑'}>
      <div className="form-inline" style={{ marginBottom:'0.75rem', flexWrap: 'wrap', gap: 8 }}>
        <span className="field-label" style={{ margin: 0 }}>用户</span>
        <select className="sel" value={user} onChange={e => { setUser(e.target.value); setShowRaw(false) }}>
          <option value="root">root</option>
        </select>
        <div style={{ flex: 1 }} />
        <button className="btn btn-accent" onClick={addTask}>+ 新增任务</button>
        <button className="btn" onClick={() => setShowRaw(!showRaw)}>{showRaw ? '可视化' : '原始文本'}</button>
        <button className="btn btn-accent" onClick={save}>保存</button>
      </div>

      {msg && <div className={`banner ${msg.startsWith('✓') ? 'banner-ok' : 'banner-err'}`}>{msg}</div>}

      {showRaw ? (
        <div className="code-block" style={{ fontSize:'0.7812rem', whiteSpace: 'pre-wrap' }}>
<textarea className="input" style={{ width: '100%', minHeight:'15rem', fontFamily: 'ui-monospace,monospace', fontSize:'0.7812rem', resize: 'vertical' }}
              value={rawContent} onChange={e => setRawContent(e.target.value)} />
        </div>
      ) : (
        <>
          {tasks.length === 0 && (
            <div className="banner banner-info" style={{ textAlign: 'center', padding: 24 }}>
              暂无定时任务，点击「+ 新增任务」创建
            </div>
          )}
          {tasks.map(task => (
            <CronCard
              key={task.id}
              task={task}
              isEditing={form?.id === task.id}
              onEdit={startEdit}
              onDelete={deleteTask}
              onCancel={cancelEdit}
              onSubmit={submitEdit}
              form={form}
              onChange={handleChange}
            />
          ))}
        </>
      )}
    </Card>
  )
}

function CronCard({ task, isEditing, onEdit, onDelete, onCancel, onSubmit, form, onChange }: {
  task: CronTask
  isEditing: boolean
  onEdit: (t: CronTask) => void
  onDelete: (id: string) => void
  onCancel: () => void
  onSubmit: () => void
  form: CronTask | null
  onChange: (field: keyof CronTask, value: string) => void
}) {
  return (
    <div className="card glass" style={{ marginBottom: 12 }}>
      {isEditing ? (
        <div style={{ padding: 16, display: 'flex', flexDirection: 'column', gap: 10 }}>
          <div style={{ display: 'grid', gridTemplateColumns: 'repeat(5, 1fr)', gap: 8 }}>
            <SelectField label="分" value={form!.minute} onChange={v => onChange('minute', v)} options={MINUTE_OPTS} />
            <SelectField label="时" value={form!.hour} onChange={v => onChange('hour', v)} options={HOUR_OPTS} />
            <SelectField label="日" value={form!.dayOfMonth} onChange={v => onChange('dayOfMonth', v)} options={DOM_OPTS} />
            <SelectField label="月" value={form!.month} onChange={v => onChange('month', v)} options={MONTH_OPTS} />
            <SelectField label="周" value={form!.dayOfWeek} onChange={v => onChange('dayOfWeek', v)} options={DOW_OPTS} />
          </div>
          <div style={{ display: 'flex', flexDirection: 'column', gap:'0.25rem', flex: 1 }}>
            <label className="field-label" style={{ fontSize: 11 }}>命令</label>
            <input className="input" value={form!.command} onChange={e => onChange('command', e.target.value)} placeholder="如 /usr/local/bin/backup.sh" style={{ fontSize: 12.5 }} />
          </div>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
            <label className="field-label" style={{ fontSize: 11 }}>备注</label>
            <input className="input" value={form!.comment} onChange={e => onChange('comment', e.target.value)} placeholder="备注说明（可选）" style={{ fontSize: 12.5 }} />
          </div>
          <div className="form-inline" style={{ justifyContent: 'flex-end', marginTop: 8 }}>
            <button className="btn" onClick={onCancel}>取消</button>
            <button className="btn btn-accent" onClick={onSubmit}>保存</button>
          </div>
        </div>
      ) : (
        <div style={{ padding: 16 }}>
          <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 8 }}>
            <div>
              <span className="pill" style={{ marginRight:'0.5rem', fontSize: 12 }}>{cronToHuman(task)}</span>
              <span style={{ fontSize:'0.8125rem', fontFamily: 'monospace', color: 'var(--text-dim)' }}>{task.command}</span>
              {task.comment.trim() !== '' && (
                <span style={{ marginLeft:'0.5rem', fontSize:'0.75rem', color: 'var(--text-dim)' }}>{`# ${task.comment}`}</span>
              )}
            </div>
            <div style={{ display: 'flex', gap: 6 }}>
              <button className="btn btn-sm" onClick={() => onEdit(task)}>编辑</button>
              <button className="btn btn-sm btn-danger" onClick={() => onDelete(task.id)}>删除</button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}

function SelectField({ label, value, onChange, options }: { label: string; value: string; onChange: (v: string) => void; options: string[] }) {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
      <label className="field-label" style={{ fontSize: 11 }}>{label}</label>
      <select className="sel" value={value} onChange={e => onChange(e.target.value)} style={{ fontSize: 12.5 }}>
        {options.map(o => <option key={o} value={o}>{o}</option>)}
      </select>
    </div>
  )
}

// ================= Disks / Smart =================

// ── 磁盘挂载子组件: lsblk / mount / df ──

function DisksSection() {
  const { selected } = useHost()
  const h = selected?.id ? `?host=${selected.id}` : ''
  const toast = useToast()
  const [data, setData] = useState<Disks | null>(null)
  const [mountDev, setMountDev] = useState('')
  const [mountPoint, setMountPoint] = useState('')
  const [mountFstype, setMountFstype] = useState('')
  const [mountMsg, setMountMsg] = useState('')
  const [copied, setCopied] = useState('')
  const [allocDev, setAllocDev] = useState('')
  const [allocInfo, setAllocInfo] = useState('')
  const [allocLoading, setAllocLoading] = useState(false)
  const [allocErr, setAllocErr] = useState('')
  const [allocResult, setAllocResult] = useState('')
  const [selectedFreeIdx, setSelectedFreeIdx] = useState(0)
  const [highlightDev, setHighlightDev] = useState('')

  const units: Record<string, number> = { k: 1024, m: 1024 ** 2, g: 1024 ** 3, t: 1024 ** 4 }
  function parseSize(s: string): number {
    const m = s.match(/^([\d.]+)([kmgt]b?)?$/i)
    if (!m) return 0
    const num = parseFloat(m[1])
    const unit = (m[2] || 'b')[0].toLowerCase()
    return num * (units[unit] || 1)
  }

  function parseFreeSpaces(output: string): FreeSpace[] {
    if (!output) return []
    const spaces: FreeSpace[] = []
    for (const line of output.split('\n')) {
      if (!line.includes('Free Space')) continue
      const fields = line.trim().split(/\s+/)
      if (fields.length < 3) continue
      if (/^\d+$/.test(fields[0])) continue
      spaces.push({ start: fields[0], end: fields[1], size: fields[2] })
    }
    return spaces
  }

  const SYS_PATHS = ['/', '/boot', '/var', '/usr', '/etc', '/home', '/tmp', '/root', '/snap', '/opt']

  function isSysPath(p: string): boolean {
    const norm = p.replace(/\/+$/, '') || '/'
    return SYS_PATHS.includes(norm) || norm.startsWith('/usr/') || norm.startsWith('/var/') || norm.startsWith('/etc/') || norm.startsWith('/boot/')
  }

  function fmtDiskErr(action: string, err?: string): string {
    if (!err) return `${action}失败`
    if (/exit status 32|busy/.test(err)) return `${action}失败: 设备正忙，有进程或文件占用。已尝试延迟卸载，仍失败请手动结束占用进程后重试。`
    if (/权限|Permission/.test(err)) return `${action}失败: 权限不足，请确认使用 root 账号。`
    return `${action}失败: ${err}`
  }

  function flash(name: string) {
    setHighlightDev(name)
    setTimeout(() => setHighlightDev(''), 1200)
  }

  const copyPath = async (devName: string, devPath: string) => {
    try {
      await navigator.clipboard.writeText(devPath)
    } catch {
      const ta = document.createElement('textarea')
      ta.value = devPath
      ta.style.position = 'fixed'
      ta.style.opacity = '0'
      document.body.appendChild(ta)
      ta.select()
      document.execCommand('copy')
      document.body.removeChild(ta)
    }
    setCopied(devName)
    setTimeout(() => setCopied(''), 2000)
  }

  const load = useCallback(() => {
    getJSON<Disks>('/api/core/tasks/disks'+h).then(setData).catch(() => {})
  }, [selected])

  useEffect(() => { load() }, [load])

  const mountAction = async (action: 'mount' | 'umount', device: string, mountpoint?: string) => {
    const mp = mountpoint || ''
    if (action === 'mount' && isSysPath(mp)) {
      if (!window.confirm(`警告：挂载到 "${mp}" 属于系统关键目录，可能影响系统运行。是否继续？`)) return
    }
    if (action === 'umount' && isSysPath(mp)) {
      if (!window.confirm(`警告：卸载 "${mp}" 可能导致系统异常。是否继续？`)) return
    }
    try {
      const body: Record<string, any> = { action, device, mountpoint: mp }
      if (selected?.id) body.host = selected.id
      const res = await postJSON<DiskActionResult>('/api/core/tasks/disks/action', body)
      if (res.ok) {
        const label = action === 'mount' ? `挂载 ${device} → ${mp || '(无)'}` : `卸载 ${device}`
        toast.success(label)
        flash(device.replace(/^\/dev\//, ''))
        load()
      } else {
        toast.error(fmtDiskErr(action, res.error))
      }
    } catch {
      toast.error('请求失败')
    }
  }

  const doPartition = async () => {
    if (!allocDev.trim()) return
    if (!window.confirm(`将在 ${allocDev.trim()} 上创建新分区，该操作不可逆。是否继续？`)) return
    setAllocLoading(true)
    setAllocErr('')
    setAllocResult('')
    try {
      const spaces = parseFreeSpaces(allocInfo)
      const sel = spaces[selectedFreeIdx]
      const body: Record<string, string> = { action: 'partition', device: allocDev.trim() }
      if (selected?.id) body.host = selected.id
      if (sel) {
        body.start = sel.start
        body.end = sel.end
      }
      const res = await postJSON<DiskActionResult>('/api/core/tasks/disks/action', body)
      if (res.error) {
        setAllocErr(res.error)
        toast.error(fmtDiskErr('分区创建', res.error))
      } else {
        const msg = res.output || '分区创建成功'
        setAllocResult(msg)
        toast.success(msg)
        if (res.newPartition) {
          setAllocDev(res.newPartition)
          flash(res.newPartition.replace(/^\/dev\//, ''))
        }
      }
      load()
    } catch {
      const err = '请求失败'
      setAllocErr(err)
      toast.error(fmtDiskErr('分区创建', err))
    }
    setAllocLoading(false)
  }

  const doDelete = async (partition: string) => {
    if (!window.confirm(`将删除分区 ${partition}，该操作不可逆。是否继续？`)) return
    setAllocLoading(true)
    setAllocErr('')
    setAllocResult('')
    try {
      const body: Record<string, any> = { action: 'delete', device: allocDev.trim(), partition }
      if (selected?.id) body.host = selected.id
      const res = await postJSON<DiskActionResult>('/api/core/tasks/disks/action', body)
      if (res.error) {
        setAllocErr(res.error)
        toast.error(fmtDiskErr('删除分区', res.error))
      } else {
        const msg = res.output || '分区已删除'
        setAllocResult(msg)
        toast.success(msg)
      }
      load()
    } catch {
      const err = '请求失败'
      setAllocErr(err)
      toast.error(fmtDiskErr('删除分区', err))
    }
    setAllocLoading(false)
  }

  const doFormat = async () => {
    if (!allocDev.trim()) return
    if (!window.confirm(`将格式化 ${allocDev.trim()} 为 ${mountFstype || 'xfs'}，所有数据将丢失！是否继续？`)) return
    setAllocLoading(true)
    setAllocErr('')
    setAllocResult('')
    try {
      const body: Record<string, any> = { action: 'format', device: allocDev.trim(), fstype: mountFstype || 'xfs' }
      if (selected?.id) body.host = selected.id
      const res = await postJSON<DiskActionResult>('/api/core/tasks/disks/action', body)
      if (res.error) {
        setAllocErr(res.error)
        toast.error(fmtDiskErr('格式化', res.error))
      } else {
        const msg = res.output || '格式化成功'
        setAllocResult(msg)
        toast.success(msg)
      }
      load()
    } catch {
      const err = '请求失败'
      setAllocErr(err)
      toast.error(fmtDiskErr('格式化', err))
    }
    setAllocLoading(false)
  }

  const loadAllocInfo = async () => {
    if (!allocDev.trim()) return
    setAllocLoading(true)
    setAllocErr('')
    try {
      const body: Record<string, any> = { action: 'info', device: allocDev.trim() }
      if (selected?.id) body.host = selected.id
      const res = await postJSON<{ output?: string; error?: string }>('/api/core/tasks/disks/action', body)
      if (res.error) {
        setAllocErr(res.error)
        toast.error(fmtDiskErr('查询', res.error))
      } else setAllocInfo(res.output || '')
    } catch {
      const err = '请求失败'
      setAllocErr(err)
      toast.error(fmtDiskErr('查询', err))
    }
  }

  useEffect(() => { loadAllocInfo() }, [allocDev])

  if (!data) return <div className="loading">加载中…</div>

  const isRoot = data.permission === 'root'

  return (
    <>
      {mountMsg && <div className={`banner ${mountMsg.startsWith('✓') ? 'banner-ok' : 'banner-err'}`}>{mountMsg}</div>}

      <Card title="块设备" subtitle="lsblk">
        <div className="code-block" style={{ fontSize:'0.7812rem', whiteSpace: 'pre-wrap' }}>{data.lsblk}</div>
      </Card>

      {data.devices && data.devices.length > 0 && (
        <Card title="设备列表" subtitle="双击设备名复制路径">
          <div className="device-list">
            {data.devices.filter(d => d.type !== 'rom' && d.type !== 'lvm').map(d => {
              const devPath = d.name.startsWith('/dev/') ? d.name : '/dev/' + d.name
              const devTag = d.name.replace(/^\/dev\//, '')
              const mounted = !!d.mountpoint
              return (
                <div key={d.name}
                  className={`device-row ${mounted ? 'device-mounted' : ''} ${highlightDev === devTag ? 'device-highlight' : ''}`}
                  style={{ display: 'flex', alignItems: 'center', gap:'0.5rem', padding: '0.25rem 0.5rem', borderBottom: '1px solid var(--border)', transition: 'background .3s' }}>
                  <code className={`device-tag ${d.type}`} style={{ cursor: 'copy' }} title="双击复制" onDoubleClick={() => copyPath(d.name, devPath)}>{devPath}</code>
                  <span className="mono" style={{ fontSize:'0.7188rem', color: '#888' }}>{d.size}</span>
                  <span className="mono" style={{ fontSize:'0.6875rem', color: '#666' }}>{d.fstype || '—'}</span>
                  {mounted
                    ? <span className="pill pill-ok" style={{ fontSize:'0.7188rem', color: '#30d158', borderColor: 'rgba(48,209,88,0.3)', background: 'rgba(48,209,88,0.08)' }}>↦ {d.mountpoint}</span>
                    : <span className="pill" style={{ fontSize:'0.7188rem', color: 'var(--text-dim)' }}>未挂载</span>
                  }
                  {copied === d.name && <span style={{ fontSize:'0.6875rem', color: '#22c55e', marginLeft: 'auto' }}>✓ 已复制</span>}
                </div>
              )
            })}
          </div>
        </Card>
      )}

      {isRoot && (
        <Card title="挂载操作" subtitle="root">
          <div className="form-inline">
            <input className="input" placeholder="设备 (如 /dev/sdb1)" value={mountDev} onChange={e => setMountDev(e.target.value)} />
            <input className="input" placeholder="挂载点 (如 /mnt/data)" value={mountPoint} onChange={e => setMountPoint(e.target.value)} />
            <select className="sel" value={mountFstype} onChange={e => setMountFstype(e.target.value)}>
              <option value="">自动</option>
              <option value="ext4">ext4</option>
              <option value="xfs">xfs</option>
              <option value="ntfs">ntfs</option>
              <option value="vfat">vfat</option>
            </select>
            {isSysPath(mountPoint) && <span className="banner banner-err" style={{ padding: '0.25rem 0.75rem', fontSize: '0.75rem', marginBottom: 0 }}>⚠ 系统关键路径，挂载将覆盖原有内容</span>}
            <button className="btn btn-accent" disabled={!mountDev || !mountPoint}
              onClick={() => mountAction('mount', mountDev, mountPoint)}>挂载</button>
            <button className="btn btn-danger" disabled={!mountDev || !mountPoint}
              onClick={() => mountAction('umount', mountDev, mountPoint)}>卸载</button>
          </div>
        </Card>
      )}

      {isRoot && (
        <Card title="磁盘分配" subtitle="分区 / 格式化">
          <div className="form-inline" style={{ marginBottom: 12 }}>
            <span className="field-label" style={{ margin: 0 }}>设备</span>
            <select className="sel" value={allocDev} onChange={e => setAllocDev(e.target.value)}>
              <option value="">选择设备</option>
              {data.devices.filter(d => d.type === 'disk').map(d => {
                const devPath = d.name.startsWith('/dev/') ? d.name : '/dev/' + d.name
                return <option key={d.name} value={devPath}>{devPath} ({d.size})</option>
              })}
            </select>
          </div>
          {allocInfo && <div className="code-block" style={{ fontSize:'0.7812rem', whiteSpace: 'pre-wrap', marginBottom: 12 }}>{allocInfo}</div>}
          {allocInfo && (() => {
            const spaces = parseFreeSpaces(allocInfo)
            if (spaces.length === 0) return null
            const largestIdx = spaces.reduce((best, s, i, a) => parseSize(s.size) > parseSize(a[best].size) ? i : best, 0)
            if (selectedFreeIdx >= spaces.length) setSelectedFreeIdx(largestIdx)
            return (
              <div style={{ marginTop: 12 }}>
                <span className="field-label">可用空闲空间</span>
                {spaces.map((s, i) => (
                  <div key={i} className="form-inline" style={{ marginTop:'0.25rem', cursor: 'pointer', opacity: selectedFreeIdx === i ? 1 : 0.5 }}
                    onClick={() => setSelectedFreeIdx(i)}>
                    <input type="radio" checked={selectedFreeIdx === i} onChange={() => setSelectedFreeIdx(i)}
                      style={{ margin: 0, accentColor: 'var(--accent)' }} />
                    <span className="mono">{s.start} → {s.end}</span>
                    <span className="pill" style={{ fontSize: 10.5 }}>{s.size}</span>
                    {i === largestIdx && <span className="badge badge-info" style={{ fontSize: 10 }}>最大</span>}
                  </div>
                ))}
              </div>
            )
          })()}
          {allocErr && <div className="banner banner-err">{allocErr}</div>}
          {allocResult && <div className="banner banner-ok">{allocResult}</div>}
          <div className="form-inline" style={{ gap:'0.5rem', flexWrap: 'wrap', marginTop: 8 }}>
            <button className="btn btn-accent" disabled={allocLoading || !allocDev} onClick={doPartition}>创建分区</button>
            <span className="field-label" style={{ margin: 0 }}>格式</span>
            <select className="sel" value={mountFstype} onChange={e => setMountFstype(e.target.value)}>
              <option value="xfs">xfs</option>
              <option value="ext4">ext4</option>
            </select>
            <button className="btn btn-warn" disabled={allocLoading || !allocDev} onClick={doFormat}>格式化</button>
            {allocDev && !allocDev.match(/^\/dev\/[a-z]+$/i) && (
              <button className="btn btn-danger" disabled={allocLoading}
                onClick={() => doDelete(allocDev.replace(/^\/dev\/[a-z]+\/?/, '').replace(/p/, ''))}>删除分区</button>
            )}
          </div>
          <div style={{ marginTop:'0.5rem', fontSize:'0.75rem', color: '#888' }}>
            提示: 创建分区后自动选中新分区。删除分区需先选中该分区（在设备下拉选单中手动输入分区名）。
          </div>
        </Card>
      )}

      <Card title="磁盘使用" subtitle="df -h">
        <div className="code-block" style={{ fontSize:'0.7812rem', whiteSpace: 'pre-wrap' }}>{data.df}</div>
      </Card>

      <Card title="挂载点" subtitle="mount">
        <div className="code-block" style={{ fontSize:'0.7812rem', whiteSpace: 'pre-wrap' }}>{data.mounts}</div>
      </Card>
    </>
  )
}

// ── LVM 存储子组件 ──

function LvmSection() {
  const { selected } = useHost()
  const h = selected?.id ? `?host=${selected.id}` : ''
  const [data, setData] = useState<LvmData | null>(null)
  const [msg, setMsg] = useState('')
  const [tab, setTab] = useState<'pvs' | 'vgs' | 'lvs'>('pvs')
  const [pvDev, setPvDev] = useState('')
  const [vgName, setVgName] = useState('')
  const [vgPv, setVgPv] = useState('')
  const [lvName, setLvName] = useState('')
  const [lvVg, setLvVg] = useState('')
  const [lvSize, setLvSize] = useState('')
  const [extLv, setExtLv] = useState('')
  const [extSize, setExtSize] = useState('')
  const [mountLv, setMountLv] = useState('')
  const [mountPoint, setMountPoint] = useState('')

  const load = useCallback(() => {
    getJSON<LvmData>('/api/core/lvm'+h).then(setData).catch(() => {})
  }, [selected])

  useEffect(() => { load(); const t = setInterval(load, 10000); return () => clearInterval(t) }, [load])

  const act = async (action: string, extra?: Record<string, any>) => {
    try {
      const body: Record<string, any> = { action, device: pvDev, vg: vgName || lvVg, lv: lvName || extLv || mountLv, size: lvSize || extSize, mount: mountPoint, ...extra }
      if (selected?.id) body.host = selected.id
      const r = await postJSON<any>('/api/core/lvm', body)
      setMsg(r.ok ? '✓ '+action : '✗ '+(r.error||'失败'))
    } catch { setMsg('✗ 请求失败') }
    setTimeout(() => setMsg(''), 5000)
    load()
  }

  if (!data) return <div className="loading">加载 LVM 信息中…</div>

  return (
    <>
      {msg && <div className={`banner ${msg.startsWith('✓') ? 'banner-ok' : 'banner-err'}`}>{msg}</div>}

      <Card title="LVM 概览" subtitle="PV / VG / LV">
        <div className="grid grid-3" style={{textAlign:'center'}}>
          <div><div className="stat-num">{(data.pvs || []).length}</div><div className="stat-label">物理卷 (PV)</div></div>
          <div><div className="stat-num">{(data.vgs || []).length}</div><div className="stat-label">卷组 (VG)</div></div>
          <div><div className="stat-num">{(data.lvs || []).length}</div><div className="stat-label">逻辑卷 (LV)</div></div>
        </div>
      </Card>

      <div className="tabs">
        <button className={`tab ${tab === 'pvs' ? 'tab-on' : ''}`} onClick={() => setTab('pvs')}>物理卷 ({(data.pvs || []).length})</button>
        <button className={`tab ${tab === 'vgs' ? 'tab-on' : ''}`} onClick={() => setTab('vgs')}>卷组 ({(data.vgs || []).length})</button>
        <button className={`tab ${tab === 'lvs' ? 'tab-on' : ''}`} onClick={() => setTab('lvs')}>逻辑卷 ({(data.lvs || []).length})</button>
      </div>

      {tab === 'pvs' && (
        <Card title="物理卷" subtitle="pvcreate / pvs">
          <div className="form-inline" style={{marginBottom:8}}>
            <input className="input" placeholder="设备 /dev/sdb" value={pvDev} onChange={e => setPvDev(e.target.value)} />
            <button className="btn btn-accent" disabled={!pvDev} onClick={() => act('pvcreate')}>创建 PV</button>
          </div>
          <div className="table-wrap">
            <table className="data-table">
              <thead><tr><th>PV</th><th>大小</th><th>空闲</th><th>所属 VG</th></tr></thead>
              <tbody>
                {(data.pvs || []).map((p: PV, i: number) => (
                  <tr key={i}><td className="mono small">{p.name}</td><td>{p.size}</td><td>{p.free}</td><td className="mono small">{p.vg || '—'}</td></tr>
                ))}
                {(data.pvs || []).length === 0 && <tr><td colSpan={4} className="dim">无物理卷</td></tr>}
              </tbody>
            </table>
          </div>
        </Card>
      )}

      {tab === 'vgs' && (
        <Card title="卷组" subtitle="vgcreate">
          <div className="form-inline" style={{marginBottom:8}}>
            <input className="input" placeholder="VG 名" value={vgName} onChange={e => setVgName(e.target.value)} style={{width:120}} />
            <input className="input" placeholder="PV 设备" value={vgPv} onChange={e => setVgPv(e.target.value)} />
            <button className="btn btn-accent" disabled={!vgName || !vgPv} onClick={() => act('vgcreate', { device: vgPv })}>创建 VG</button>
          </div>
          <div className="table-wrap">
            <table className="data-table">
              <thead><tr><th>VG</th><th>大小</th><th>空闲</th><th>PV 数</th><th>LV 数</th></tr></thead>
              <tbody>
                {(data.vgs || []).map((v: VG, i: number) => (
                  <tr key={i}><td className="mono">{v.name}</td><td>{v.size}</td><td>{v.free}</td><td>{v.pv}</td><td>{v.lv}</td></tr>
                ))}
                {(data.vgs || []).length === 0 && <tr><td colSpan={5} className="dim">无卷组</td></tr>}
              </tbody>
            </table>
          </div>
        </Card>
      )}

      {tab === 'lvs' && (
        <>
          <Card title="创建逻辑卷" subtitle="lvcreate">
            <div className="form-inline">
              <input className="input" placeholder="VG 名" value={lvVg} onChange={e => setLvVg(e.target.value)} style={{width:120}} />
              <input className="input" placeholder="LV 名" value={lvName} onChange={e => setLvName(e.target.value)} style={{width:120}} />
              <input className="input" placeholder="大小, 如 10G" value={lvSize} onChange={e => setLvSize(e.target.value)} style={{width:100}} />
              <button className="btn btn-accent" disabled={!lvVg || !lvName || !lvSize} onClick={() => act('lvcreate')}>创建 LV</button>
            </div>
          </Card>
          <Card title="扩展/挂载" subtitle="lvextend + mount">
            <div className="form-inline" style={{marginBottom:8}}>
              <input className="input" placeholder="LV 路径 /dev/vg0/data" value={extLv} onChange={e => setExtLv(e.target.value)} style={{width:200}} />
              <input className="input" placeholder="扩展 +5G" value={extSize} onChange={e => setExtSize(e.target.value)} style={{width:100}} />
              <button className="btn btn-accent" disabled={!extLv || !extSize} onClick={() => act('lvextend')}>扩展</button>
            </div>
            <div className="form-inline">
              <input className="input" placeholder="LV 路径 /dev/vg0/data" value={mountLv} onChange={e => setMountLv(e.target.value)} style={{width:200}} />
              <input className="input" placeholder="挂载点 /mnt/data" value={mountPoint} onChange={e => setMountPoint(e.target.value)} style={{width:200}} />
              <button className="btn btn-sm" disabled={!mountLv || !mountPoint} onClick={() => act('mount')}>挂载</button>
            </div>
          </Card>
          <Card title="逻辑卷列表" subtitle="lvs">
            <div className="table-wrap">
              <table className="data-table">
                <thead><tr><th>LV</th><th>大小</th><th>所属 VG</th><th>路径</th><th>挂载点</th></tr></thead>
                <tbody>
                  {(data.lvs || []).map((l: LV, i: number) => (
                    <tr key={i}>
                      <td className="mono small">{l.name}</td>
                      <td>{l.size}</td>
                      <td className="mono small">{l.vg}</td>
                      <td className="mono small">{l.path}</td>
                      <td className="mono small">{l.mounted || <span className="dim">未挂载</span>}</td>
                    </tr>
                  ))}
                  {(data.lvs || []).length === 0 && <tr><td colSpan={5} className="dim">无逻辑卷</td></tr>}
                </tbody>
              </table>
            </div>
          </Card>
        </>
      )}
    </>
  )
}

// ── SMART 健康子组件: smartctl -a 查看硬盘状态 ──

function SmartSection() {
  const { selected } = useHost()
  const h = selected?.id ? `?host=${selected.id}` : ''
  const [device, setDevice] = useState('sda')
  const [output, setOutput] = useState('')
  const [loading, setLoading] = useState(false)
  const [err, setErr] = useState('')
  const [perm, setPerm] = useState<Permission | null>(null)

  useEffect(() => {
    getJSON<{ permission: Permission }>('/api/core/tasks/disks'+h).then(d => setPerm(d.permission)).catch(() => {})
  }, [selected])

  const load = async () => {
    if (!device.trim()) return
    setLoading(true)
    setErr('')
    try {
      const body: Record<string, any> = { action: 'smart', device: device.trim() }
      if (selected?.id) body.host = selected.id
      const res = await postJSON<DiskActionResult>('/api/core/tasks/disks/action', body)
      if (res.error) setErr(res.error)
      else setOutput(res.output || '')
      setPerm(res.permission)
    } catch { setErr('请求失败') }
    setLoading(false)
  }

  return (
    <Card title="SMART 健康" subtitle="smartctl -a">
      {perm === 'user' && <div className="banner banner-err">需要 root 权限</div>}
      <div className="form-inline" style={{ marginBottom: 12 }}>
        <span className="field-label" style={{ margin: 0 }}>设备</span>
        <select className="sel" value={device} onChange={e => setDevice(e.target.value)}>
          {['sda', 'sdb', 'sdc', 'sdd', 'nvme0n1', 'nvme1n1'].map(d => <option key={d} value={d}>{d}</option>)}
        </select>
        <button className="btn btn-accent" disabled={loading || perm === 'user'} onClick={load}>{loading ? '读取中…' : '读取 SMART'}</button>
      </div>
      {err && <div className="banner banner-err">{err}</div>}
      {output && <div className="code-block" style={{ fontSize:'0.7812rem', whiteSpace: 'pre-wrap', maxHeight:'31.25rem', overflowY: 'auto' }}>{output}</div>}
    </Card>
  )
}