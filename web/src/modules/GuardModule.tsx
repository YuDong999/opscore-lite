import { useEffect, useState } from 'react'
import { getJSON, postJSON } from '../api/client'

type Tab = 'cron' | 'scripts' | 'backup'

interface CronEntry { id: string; schedule: string; command: string; comment: string; enabled: boolean; line: number }
interface Script { id: string; name: string; content: string; vars: any[]; created: string; updated: string }
interface BackupSnap { id: string; name: string; srcPath: string; dstPath: string; size: number; created: string }

export default function GuardModule() {
  const [tab, setTab] = useState<Tab>('cron')
  const tabs: [Tab, string][] = [['cron', '定时任务'], ['scripts', '脚本库'], ['backup', '备份快照']]

  return (
    <div>
      <div className="tab-row" style={{ marginBottom: 16 }}>
        {tabs.map(([id, label]) => (
          <button key={id} className={`tab ${tab === id ? 'tab-active' : ''}`} onClick={() => setTab(id)}>{label}</button>
        ))}
      </div>
      {tab === 'cron' && <CronTab />}
      {tab === 'scripts' && <ScriptsTab />}
      {tab === 'backup' && <BackupTab />}
    </div>
  )
}

// ═══════════════════════════════════════════════
// Cron Tab
// ═══════════════════════════════════════════════
function CronTab() {
  const [entries, setEntries] = useState<CronEntry[]>([])
  const [showAdd, setShowAdd] = useState(false)
  const [form, setForm] = useState({ schedule: '', command: '', comment: '' })

  const load = () => getJSON<any>('/api/guard/cron').then((d) => setEntries(d.entries || [])).catch(() => {})
  useEffect(() => { load() }, [])

  const add = async () => {
    if (!form.schedule || !form.command) return
    await postJSON('/api/guard/cron/action', { action: 'add', entry: form })
    setForm({ schedule: '', command: '', comment: '' })
    setShowAdd(false)
    load()
  }

  const del = async (id: string) => { await postJSON('/api/guard/cron/action', { action: 'delete', entry: { id } }); load() }
  const toggle = async (e: CronEntry) => { await postJSON('/api/guard/cron/action', { action: 'toggle', entry: { id: e.id } }); load() }

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 12 }}>
        <h3 style={{ margin: 0, fontSize: 15 }}>系统 Crontab</h3>
        <button className="btn btn-accent" onClick={() => setShowAdd(!showAdd)}>{showAdd ? '取消' : '+ 添加'}</button>
      </div>
      {showAdd && (
        <div className="card" style={{ padding: 14, marginBottom: 12 }}>
          <div style={{ display: 'grid', gridTemplateColumns: '1fr 2fr 1fr', gap: 8 }}>
            <input className="ipt" placeholder="* * * * *" value={form.schedule} onChange={(e) => setForm({ ...form, schedule: e.target.value })} />
            <input className="ipt" placeholder="命令..." value={form.command} onChange={(e) => setForm({ ...form, command: e.target.value })} />
            <input className="ipt" placeholder="备注" value={form.comment} onChange={(e) => setForm({ ...form, comment: e.target.value })} />
          </div>
          <button className="btn btn-accent" style={{ marginTop: 8 }} onClick={add}>保存</button>
        </div>
      )}
      <div className="tbl-wrap">
        <table className="tbl">
          <thead><tr><th>状态</th><th>计划</th><th>命令</th><th>备注</th><th>操作</th></tr></thead>
          <tbody>
            {entries.length === 0 && <tr><td colSpan={5} className="empty">暂无定时任务</td></tr>}
            {entries.map((e) => (
              <tr key={e.id}>
                <td><span className={`badge ${e.enabled ? 'badge-ok' : 'badge-off'}`} style={{ cursor: 'pointer' }} onClick={() => toggle(e)}>{e.enabled ? '启用' : '禁用'}</span></td>
                <td style={{ fontFamily: 'monospace', fontSize: 13 }}>{e.schedule}</td>
                <td style={{ fontFamily: 'monospace', fontSize: 13, maxWidth: 300, overflow: 'hidden', textOverflow: 'ellipsis' }}>{e.command}</td>
                <td style={{ color: 'var(--text-dim)' }}>{e.comment}</td>
                <td><button className="btn btn-danger" style={{ fontSize: 12, padding: '4px 10px' }} onClick={() => del(e.id)}>删除</button></td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  )
}

// ═══════════════════════════════════════════════
// Scripts Tab — 模板化快速脚本
// ═══════════════════════════════════════════════

interface ParamDef {
  name: string
  label: string
  type: 'text' | 'number' | 'select'
  placeholder?: string
  options?: string[]
  default?: string
}

interface Template {
  id: string
  name: string
  desc: string
  icon: string
  params: ParamDef[]
  generate: (vals: Record<string, string>) => string
}

const TEMPLATES: Template[] = [
  {
    id: 'awk-col',
    name: '文本列提取',
    desc: '用 awk 按分隔符提取指定列',
    icon: '📋',
    params: [
      { name: 'file', label: '文件路径', type: 'text', placeholder: '/var/log/syslog' },
      { name: 'sep', label: '分隔符', type: 'select', options: ['空格(默认)', 'Tab', '逗号', '冒号', '竖线'], default: '空格(默认)' },
      { name: 'col', label: '提取第几列', type: 'number', placeholder: '1', default: '1' },
      { name: 'condition', label: '过滤条件(可选, awk模式)', type: 'text', placeholder: 'NR<=10 或 $3=="ERROR"' },
    ],
    generate(v) {
      const sepMap: Record<string, string> = { '空格(默认)': '', 'Tab': '\\t', '逗号': ',', '冒号': ':', '竖线': '|' }
      const sep = sepMap[v.sep] || ''
      const fs = sep ? `-F'${sep}'` : ''
      const cond = v.condition ? `{ if (${v.condition}) print $${v.col} }` : `{ print $${v.col} }`
      return `PRINT "── 提取第${v.col}列 ──"\n# ${v.file}\n# 命令: awk ${fs} '${cond.trim()}' ${v.file}\nLET cmd = "awk ${fs} '${cond.trim()}' ${v.file} 2>&1"\nLET output = EXEC(cmd)\nPRINT output`
    },
  },
  {
    id: 'line-count',
    name: '行数/词数统计',
    desc: '统计文件行数、单词数、字符数',
    icon: '🔢',
    params: [
      { name: 'file', label: '文件路径', type: 'text', placeholder: '/var/log/syslog' },
    ],
    generate(v) {
      return `PRINT "── 文件统计 ──"\nLET lines = EXEC("wc -l < ${v.file} 2>&1")\nLET words = EXEC("wc -w < ${v.file} 2>&1")\nLET chars = EXEC("wc -c < ${v.file} 2>&1")\nPRINT "行数: " + lines\nPRINT "词数: " + words\nPRINT "字符: " + chars`
    },
  },
  {
    id: 'disk-check',
    name: '磁盘使用检查',
    desc: '检查指定路径磁盘使用率是否超阈值',
    icon: '💾',
    params: [
      { name: 'path', label: '检查路径', type: 'text', placeholder: '/' },
      { name: 'threshold', label: '告警阈值(%)', type: 'number', placeholder: '80', default: '80' },
    ],
    generate(v) {
      return `PRINT "── 磁盘检查 ${v.path} ──"\nLET usage = EXEC("df ${v.path} | awk 'NR==2{print $5}' | tr -d '%'")\nLET usageNum = usage\nPRINT "当前使用率: " + usage + "%"\nIF usageNum >= ${v.threshold}, PRINT "⚠ 超过阈值 ${v.threshold}%!", PRINT "✓ 正常"`
    },
  },
  {
    id: 'dir-size',
    name: '目录大小统计',
    desc: '统计指定目录总大小',
    icon: '📁',
    params: [
      { name: 'path', label: '目录路径', type: 'text', placeholder: '/var/log' },
      { name: 'top', label: '显示前 N 个大文件', type: 'number', placeholder: '10', default: '10' },
    ],
    generate(v) {
      return `PRINT "── 目录大小 ${v.path} ──"\nLET total = EXEC("du -sh ${v.path} 2>/dev/null | cut -f1")\nPRINT "总大小: " + total\nPRINT ""\nPRINT "前 ${v.top} 个大文件:"\nLET big = EXEC("find ${v.path} -type f -exec du -h {} + 2>/dev/null | sort -rh | head -n ${v.top}")\nPRINT big`
    },
  },
  {
    id: 'port-check',
    name: '端口存活检测',
    desc: '检测指定端口是否在监听',
    icon: '🔌',
    params: [
      { name: 'ports', label: '端口号(逗号分隔)', type: 'text', placeholder: '80,443,8080' },
    ],
    generate(v) {
      const ports = v.ports.split(',').map(p => p.trim()).filter(Boolean)
      let lines = ['PRINT "── 端口检测 ──"']
      for (const p of ports) {
        lines.push(`LET port_${p} = EXEC("ss -tlnp | grep ':${p} ' | wc -l")`)
        lines.push(`IF port_${p} != "0", PRINT ":${p} ✓ 监听中", PRINT ":${p} ✗ 未监听"`)
      }
      return lines.join('\n')
    },
  },
  {
    id: 'log-grep',
    name: '日志关键词统计',
    desc: '统计日志文件中关键词出现次数',
    icon: '🔍',
    params: [
      { name: 'file', label: '日志文件路径', type: 'text', placeholder: '/var/log/syslog' },
      { name: 'keyword', label: '关键词', type: 'text', placeholder: 'error' },
      { name: 'recent', label: '只看最近N行(0=全部)', type: 'number', placeholder: '1000', default: '1000' },
    ],
    generate(v) {
      const tail = v.recent && v.recent !== '0' ? ` | tail -n ${v.recent}` : ''
      return `PRINT "── 日志搜索 ──"\nPRINT "文件: ${v.file}"\nPRINT "关键词: ${v.keyword}"\nLET count = EXEC("cat ${v.file}${tail} | grep -ic '${v.keyword}' 2>/dev/null || echo 0")\nPRINT "匹配行数: " + count\nPRINT ""\nPRINT "最近 5 条匹配:"\nLET lines = EXEC("cat ${v.file}${tail} | grep -i '${v.keyword}' 2>/dev/null | tail -n 5")\nPRINT lines`
    },
  },
  {
    id: 'run-cmd',
    name: '自定义命令',
    desc: '直接执行 shell 命令并获取输出',
    icon: '🖥️',
    params: [
      { name: 'cmd', label: 'Shell 命令', type: 'text', placeholder: 'uptime' },
    ],
    generate(v) {
      return `PRINT "── 执行命令 ──"\nPRINT "> ${v.cmd}"\nLET output = EXEC("${v.cmd} 2>&1")\nPRINT output`
    },
  },
  {
    id: 'calc',
    name: '数学计算器',
    desc: '输入数学表达式直接求值',
    icon: '🧮',
    params: [
      { name: 'expr', label: '表达式', type: 'text', placeholder: 'SUM(12, 34, 56) / 3' },
    ],
    generate(v) {
      return `PRINT "── 计算 ──"\nPRINT "${v.expr}"\nPRINT "= " + ${v.expr}`
    },
  },
]

function ScriptsTab() {
  const [scripts, setScripts] = useState<Script[]>([])
  const [editing, setEditing] = useState<Script | null>(null)
  const [result, setResult] = useState<any>(null)
  const [tpl, setTpl] = useState<string>('')
  const [tplVals, setTplVals] = useState<Record<string, string>>({})
  const [mode, setMode] = useState<'template' | 'code'>('template')

  const load = () => getJSON<any>('/api/guard/script').then((d) => setScripts(d.scripts || [])).catch(() => {})
  useEffect(() => { load() }, [])

  const save = async () => {
    if (!editing) return
    await postJSON('/api/guard/script/action', { action: 'save', script: editing })
    setEditing(null)
    load()
  }

  const run = async (id: string) => {
    const r = await postJSON<any>('/api/guard/script/action', { action: 'run', id })
    setResult(r.error ? { error: r.error } : r.result)
  }

  const del = async (id: string) => { await postJSON('/api/guard/script/action', { action: 'delete', id }); load() }

  const selectTemplate = (tplId: string) => {
    const t = TEMPLATES.find(x => x.id === tplId)
    if (!t) return
    setTpl(tplId)
    const vals: Record<string, string> = {}
    t.params.forEach(p => { vals[p.name] = p.default || '' })
    setTplVals(vals)
  }

  const generateScript = () => {
    const t = TEMPLATES.find(x => x.id === tpl)
    if (!t) return
    const content = t.generate(tplVals)
    const name = t.name
    setEditing({ id: editing?.id || '', name: editing?.name || name, content, vars: [], created: editing?.created || '', updated: '' })
    setMode('code')
  }

  if (editing) {
    return (
      <div>
        <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: 12 }}>
          <input className="ipt" placeholder="脚本名称" value={editing.name}
            onChange={(e) => setEditing({ ...editing, name: e.target.value })} style={{ width: 250 }} />
          <div style={{ display: 'flex', gap: 8 }}>
            <button className="btn" onClick={() => { setEditing(null); setMode('template'); setTpl('') }}>取消</button>
            <button className="btn btn-accent" onClick={save}>保存</button>
          </div>
        </div>
        <div className="card" style={{ padding: 14 }}>
          <p style={{ fontSize: 12, color: 'var(--text-dim)', marginBottom: 8 }}>
            支持: LET x = 10 | PRINT expr | EXEC("cmd") | SUM/AVG/MAX/MIN/ABS/ROUND/SQRT/POW/IF/NOW/DATE | 算术: + - * / %
          </p>
          <textarea
            value={editing.content}
            onChange={(e) => setEditing({ ...editing, content: e.target.value })}
            style={{
              width: '100%', minHeight: 200, fontFamily: 'monospace', fontSize: 13,
              background: 'var(--surface-solid)', color: 'var(--text)', border: '1px solid var(--border)',
              borderRadius: 8, padding: 12, resize: 'vertical', lineHeight: 1.6,
            }}
            placeholder="# 示例&#10;LET output = EXEC(&quot;df -h /&quot;)&#10;PRINT output"
          />
        </div>
      </div>
    )
  }

  // ── 模板选择 + 参数表单 ──
  const currentTpl = TEMPLATES.find(t => t.id === tpl)

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 12 }}>
        <h3 style={{ margin: 0, fontSize: 15 }}>脚本库</h3>
        <div style={{ display: 'flex', gap: 8 }}>
          <button className="btn" onClick={() => { setEditing({ id: '', name: '', content: '', vars: [], created: '', updated: '' }); setMode('code') }}>✍ 手写脚本</button>
        </div>
      </div>

      {/* 模板选择器 */}
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(180px, 1fr))', gap: 10, marginBottom: 16 }}>
        {TEMPLATES.map((t) => (
          <button
            key={t.id}
            onClick={() => selectTemplate(t.id)}
            style={{
              padding: '10px 12px', borderRadius: 10, cursor: 'pointer', textAlign: 'left',
              border: `2px solid ${tpl === t.id ? 'var(--accent)' : 'var(--border)'}`,
              background: tpl === t.id ? 'rgba(99,102,241,0.08)' : 'var(--surface-solid)',
              transition: 'all 0.15s ease',
            }}
          >
            <div style={{ fontSize: 18, marginBottom: 4 }}>{t.icon}</div>
            <div style={{ fontSize: 13, fontWeight: 600 }}>{t.name}</div>
            <div style={{ fontSize: 11, color: 'var(--text-dim)', marginTop: 2 }}>{t.desc}</div>
          </button>
        ))}
      </div>

      {/* 参数表单 */}
      {currentTpl && (
        <div className="card" style={{ padding: 14, marginBottom: 16 }}>
          <h4 style={{ margin: '0 0 10px', fontSize: 14 }}>
            {currentTpl.icon} {currentTpl.name}
          </h4>
          <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 10, marginBottom: 12 }}>
            {currentTpl.params.map((p) => (
              <div key={p.name}>
                <label style={{ display: 'block', fontSize: 12, color: 'var(--text-dim)', marginBottom: 4, fontWeight: 600 }}>
                  {p.label}
                </label>
                {p.type === 'select' ? (
                  <select className="sel" style={{ width: '100%' }}
                    value={tplVals[p.name] || ''}
                    onChange={(e) => setTplVals({ ...tplVals, [p.name]: e.target.value })}>
                    {p.options?.map(o => <option key={o} value={o}>{o}</option>)}
                  </select>
                ) : (
                  <input className="ipt" style={{ width: '100%' }}
                    type={p.type === 'number' ? 'number' : 'text'}
                    placeholder={p.placeholder}
                    value={tplVals[p.name] || ''}
                    onChange={(e) => setTplVals({ ...tplVals, [p.name]: e.target.value })} />
                )}
              </div>
            ))}
          </div>
          <button className="btn btn-accent" onClick={generateScript}>⚡ 生成脚本</button>
        </div>
      )}

      {/* 已有脚本列表 */}
      <div className="tbl-wrap">
        <table className="tbl">
          <thead><tr><th>名称</th><th>更新时间</th><th>操作</th></tr></thead>
          <tbody>
            {scripts.length === 0 && <tr><td colSpan={3} className="empty">暂无脚本，选一个模板快速开始</td></tr>}
            {scripts.map((s) => (
              <tr key={s.id}>
                <td style={{ fontWeight: 600 }}>{s.name}</td>
                <td style={{ fontSize: 12, color: 'var(--text-dim)' }}>{new Date(s.updated).toLocaleString('zh-CN')}</td>
                <td>
                  <div style={{ display: 'flex', gap: 6 }}>
                    <button className="btn" style={{ fontSize: 12, padding: '4px 10px' }} onClick={() => { setEditing(s); setMode('code') }}>编辑</button>
                    <button className="btn btn-accent" style={{ fontSize: 12, padding: '4px 10px' }} onClick={() => run(s.id)}>执行</button>
                    <button className="btn btn-danger" style={{ fontSize: 12, padding: '4px 10px' }} onClick={() => del(s.id)}>删除</button>
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      {result && (
        <div className="card" style={{ padding: 14, marginTop: 12 }}>
          <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: 8 }}>
            <strong style={{ fontSize: 13 }}>执行结果</strong>
            <button className="btn" style={{ fontSize: 12 }} onClick={() => setResult(null)}>关闭</button>
          </div>
          {result.error ? (
            <div className="lockout-warn">{result.error}</div>
          ) : (
            <pre style={{ margin: 0, fontSize: 13, fontFamily: 'monospace', whiteSpace: 'pre-wrap' }}>
              {typeof result.output === 'string' ? result.output : JSON.stringify(result, null, 2)}
            </pre>
          )}
        </div>
      )}
    </div>
  )
}

// ═══════════════════════════════════════════════
// Backup Tab
// ═══════════════════════════════════════════════
function BackupTab() {
  const [snapshots, setSnapshots] = useState<BackupSnap[]>([])
  const [showCreate, setShowCreate] = useState(false)
  const [form, setForm] = useState({ srcPath: '', dstPath: '', name: '' })

  const load = () => getJSON<any>('/api/guard/backup').then((d) => setSnapshots(d.snapshots || [])).catch(() => {})
  useEffect(() => { load() }, [])

  const create = async () => { if (!form.srcPath) return; await postJSON('/api/guard/backup/action', { action: 'create', ...form }); setForm({ srcPath: '', dstPath: '', name: '' }); setShowCreate(false); load() }
  const del = async (id: string) => { await postJSON('/api/guard/backup/action', { action: 'delete', id }); load() }
  const restore = async (id: string) => { if (!confirm('确认恢复此备份？')) return; await postJSON('/api/guard/backup/action', { action: 'restore', id }) }

  const fmtSize = (b: number) => { if (b < 1024) return b + ' B'; if (b < 1048576) return (b / 1024).toFixed(1) + ' KB'; return (b / 1048576).toFixed(1) + ' MB' }

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 12 }}>
        <h3 style={{ margin: 0, fontSize: 15 }}>备份快照</h3>
        <button className="btn btn-accent" onClick={() => setShowCreate(!showCreate)}>{showCreate ? '取消' : '+ 创建备份'}</button>
      </div>
      {showCreate && (
        <div className="card" style={{ padding: 14, marginBottom: 12 }}>
          <div style={{ display: 'grid', gridTemplateColumns: '2fr 1fr 1fr', gap: 8, marginBottom: 8 }}>
            <input className="ipt" placeholder="源路径 /path/to/dir" value={form.srcPath} onChange={(e) => setForm({ ...form, srcPath: e.target.value })} />
            <input className="ipt" placeholder="目标路径(可选)" value={form.dstPath} onChange={(e) => setForm({ ...form, dstPath: e.target.value })} />
            <input className="ipt" placeholder="备份名称(可选)" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} />
          </div>
          <button className="btn btn-accent" onClick={create}>创建</button>
        </div>
      )}
      <div className="tbl-wrap">
        <table className="tbl">
          <thead><tr><th>名称</th><th>源路径</th><th>大小</th><th>创建时间</th><th>操作</th></tr></thead>
          <tbody>
            {snapshots.length === 0 && <tr><td colSpan={5} className="empty">暂无备份</td></tr>}
            {snapshots.map((s) => (
              <tr key={s.id}>
                <td style={{ fontWeight: 600 }}>{s.name}</td>
                <td style={{ fontFamily: 'monospace', fontSize: 12 }}>{s.srcPath}</td>
                <td>{fmtSize(s.size)}</td>
                <td style={{ fontSize: 12, color: 'var(--text-dim)' }}>{new Date(s.created).toLocaleString('zh-CN')}</td>
                <td>
                  <div style={{ display: 'flex', gap: 6 }}>
                    <button className="btn" style={{ fontSize: 12, padding: '4px 10px' }} onClick={() => restore(s.id)}>恢复</button>
                    <button className="btn btn-danger" style={{ fontSize: 12, padding: '4px 10px' }} onClick={() => del(s.id)}>删除</button>
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  )
}
