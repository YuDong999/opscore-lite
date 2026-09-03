// 连接管理面板: 列表 + 新建/编辑/测试/删除。
// 流程:
//   1. 列表页 — 展示已有连接, 支持快速测试/编辑/删除
//   2. 新建/编辑 — 两步向导: 选引擎 → 填配置
//   引擎类别不同, 字段不同(关系型需要 database, 向量库需要 collection, MQ 需要 topic)

import { useEffect, useState } from 'react'
import { createPortal } from 'react-dom'
import { useToast } from '../Toast'
import { CategoryIcon } from './DbIcons'
import {
  type ConnectionInfo,
  type ConnectionConfig,
  type EngineType,
  type EngineCategory,
  type EngineMeta,
  ENGINES,
  getEngineMeta,
  statusLabel,
  listConnections,
  createConnection,
  updateConnection,
  deleteConnection,
  testConnection,
  loadEngines,
} from './api'

const CATEGORY_LABELS: Record<EngineCategory, string> = {
  relational: '关系型数据库',
  document:   '文档型',
  vector:     '向量数据库',
  timeseries: '时序数据库',
  search:     '搜索/分析',
  mq:         '消息队列',
  custom:     '自定义',
}

const CATEGORY_ORDER: EngineCategory[] = ['relational', 'document', 'timeseries', 'vector', 'search', 'mq', 'custom']

type Step = 'list' | 'pick-engine' | 'fill-config'

export default function ConnectionPanel({
  selected,
  onSelect,
  onConnsChange,
  newConnTrigger,
}: {
  selected: ConnectionInfo | null
  onSelect: (c: ConnectionInfo) => void
  onConnsChange?: (list: ConnectionInfo[]) => void
  newConnTrigger?: number
}) {
  const toast = useToast()
  const [conns, setConns] = useState<ConnectionInfo[]>([])
  const [loading, setLoading] = useState(false)
  const [step, setStep] = useState<Step>('list')
  const [pickedEngine, setPickedEngine] = useState<EngineType | null>(null)
  const [editing, setEditing] = useState<Partial<ConnectionInfo> | null>(null)
  const [password, setPassword] = useState('')
  const [busy, setBusy] = useState(false)
  const [showAdvanced, setShowAdvanced] = useState(false)

  const reload = async () => {
    setLoading(true)
    try {
      const list = await listConnections()
      setConns(list)
      onConnsChange?.(list)
      if (selected && !list.find(c => c.id === selected.id)) onSelect(null as any)
    } catch (e: any) {
      toast.error('加载连接失败: ' + e.message)
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    loadEngines() // 拉取后端运行时 status
    reload()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // 监听外部"新建连接"触发(概览页大按钮)
  useEffect(() => {
    const handler = () => startNew()
    window.addEventListener('dbmanager:new-conn', handler)
    return () => window.removeEventListener('dbmanager:new-conn', handler)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // 监听树上的"编辑连接"触发(detail = ConnectionInfo)
  useEffect(() => {
    const handler = (e: Event) => {
      const c = (e as CustomEvent<ConnectionInfo>).detail
      if (c?.id) startEdit(c)
    }
    window.addEventListener('dbmanager:edit-conn', handler)
    return () => window.removeEventListener('dbmanager:edit-conn', handler)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const startNew = () => {
    setEditing({ engine: undefined, config: { host: '127.0.0.1', port: 0, database: '', username: '', sslMode: 'preferred' } })
    setPassword('')
    setPickedEngine(null)
    setStep('pick-engine')
  }

  const startEdit = (c: ConnectionInfo) => {
    setEditing({ ...c, config: { ...c.config } })
    setPassword('')
    setPickedEngine(c.engine)
    setStep('fill-config')
  }

  const cancelWizard = () => {
    setEditing(null)
    setPassword('')
    setPickedEngine(null)
    setStep('list')
  }

  const onEnginePicked = (eng: EngineType) => {
    const meta = getEngineMeta(eng)!
    setPickedEngine(eng)
    setEditing((prev) => ({
      ...prev,
      engine: eng,
      config: {
        host: prev?.config?.host || '127.0.0.1',
        port: prev?.config?.port || meta.defaultPort,
        database: prev?.config?.database || meta.defaultDb,
        username: prev?.config?.username || meta.defaultUser,
        sslMode: prev?.config?.sslMode || meta.defaultSsl,
        envTag: prev?.config?.envTag || '',
      },
    }))
    setStep('fill-config')
  }

  const save = async () => {
    if (!editing) return
    if (!editing.name?.trim()) { toast.error('名称不能为空'); return }
    if (!editing.engine) { toast.error('请选择引擎'); return }
    const meta = getEngineMeta(editing.engine)
    const isCustom = meta?.category === 'custom'
    if (isCustom) {
      if (!editing.config?.dsn?.trim() && !editing.config?.driver?.trim()) {
        toast.error('自定义连接需填写 Driver 或 DSN'); return
      }
    } else {
      if (!editing.config?.host) { toast.error('主机不能为空'); return }
    }
    setBusy(true)
    try {
      if (editing.id) {
        await updateConnection(editing.id, editing.name!, editing.config!, password)
        toast.success('已更新')
      } else {
        if (!password && !isCustom) { toast.error('新建连接必须输入密码'); setBusy(false); return }
        const c = await createConnection(editing.name!, editing.engine!, editing.config!, password)
        toast.success('已创建')
        onSelect(c)
      }
      cancelWizard()
      await reload()
    } catch (e: any) {
      toast.error('保存失败: ' + e.message)
    } finally {
      setBusy(false)
    }
  }

  const remove = async (c: ConnectionInfo) => {
    if (!confirm(`确认删除连接「${c.name}」?`)) return
    try {
      await deleteConnection(c.id)
      toast.success('已删除')
      await reload()
    } catch (e: any) {
      toast.error('删除失败: ' + e.message)
    }
  }

  const quickTest = async (c: ConnectionInfo) => {
    try {
      const r = await testConnection({ id: c.id })
      if (r.ok) toast.success(`连接成功: ${r.version}`)
      else toast.error('连接失败: ' + r.error)
    } catch (e: any) {
      toast.error('测试失败: ' + e.message)
    }
  }

  // ── 列表已由 ConnectionTree 承担; 面板只负责新建/编辑向导 ──
  if (step === 'list') {
    return null
  }

  // ── 第 1 步: 选引擎 ──
  if (step === 'pick-engine') {
    const groups: Record<EngineCategory, EngineMeta[]> = {
      relational: [], document: [], vector: [], timeseries: [], search: [], mq: [], custom: [],
    }
    ENGINES.forEach(e => groups[e.category]?.push(e))

    return createPortal(
      <div className="db-conn-panel">
        <div className="db-conn-list-head">
          <button className="btn-glass-soft btn-glass-soft-sm db-icon-btn" onClick={cancelWizard} title="返回">
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
              <path d="M19 12H5M12 19l-7-7 7-7" />
            </svg>
          </button>
          <span>选择引擎</span>
          <span />
        </div>
        {CATEGORY_ORDER.filter(c => groups[c]?.length > 0).map(cat => (
          <div key={cat} className="db-engine-group">
            <div className="db-engine-group-label">
              <span className="db-engine-cat-icon"><CategoryIcon cat={cat} size={14} /></span>
              {CATEGORY_LABELS[cat]}
              <span className="db-engine-group-count">{groups[cat].length}</span>
            </div>
            <div className="db-engine-grid">
              {groups[cat].map(e => {
                const sl = statusLabel(e.status)
                const off = e.status === 'disabled'
                return (
                  <button
                    key={e.type}
                    className={`db-engine-card db-engine-${e.type}${off ? ' db-engine-card-off' : ''}`}
                    onClick={() => { if (!off) onEnginePicked(e.type) }}
                    title={off
                      ? `暂不可用: ${e.reason || '需安装驱动'}`
                      : e.description + (e.reason ? `\n${e.reason}` : '')}
                    style={off ? { opacity: 0.45, cursor: 'not-allowed' } : undefined}
                  >
                    <div className="db-engine-card-name">{e.label}</div>
                    <div className="db-engine-card-meta">
                      <span className={`pill ${sl.cls}`} style={{ fontSize: '0.5rem', padding: '0 0.25rem' }}>{sl.text}</span>
                      <span style={{ marginLeft: 4 }}>
                        {e.defaultPort > 0 ? `:${e.defaultPort}` : 'DSN'}
                      </span>
                    </div>
                  </button>
                )
              })}
            </div>
          </div>
        ))}
      </div>,
      document.body,
    )
  }

  // ── 第 2 步: 填配置 ──
  if (step === 'fill-config' && editing && pickedEngine) {
    const meta = getEngineMeta(pickedEngine)!
    const cfg = editing.config!
    const isCustom = meta.category === 'custom'
    const isMQ = meta.category === 'mq'
    const isVector = meta.category === 'vector'

    return createPortal(
      <div className="db-conn-panel">
        <div className="db-conn-list-head">
          <button className="btn-glass-soft btn-glass-soft-sm db-icon-btn" onClick={() => setStep(editing.id ? 'list' : 'pick-engine')} title="返回">
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
              <path d="M19 12H5M12 19l-7-7 7-7" />
            </svg>
          </button>
          <span>{editing.id ? '编辑连接' : '新建连接'}</span>
          <span />
        </div>
        <div className="db-conn-editor">
          <div className="db-conn-editor-title">
            <span className={`db-engine-badge db-engine-${pickedEngine}`}>{meta.label.split(' ')[0]}</span>
            <span style={{ marginLeft: 6 }}>{meta.label}</span>
          </div>
          <div className="db-form">
            <label>
              名称
              <input className="input" value={editing.name || ''} onChange={e => setEditing({ ...editing, name: e.target.value })} placeholder="如: 生产 MySQL" />
            </label>
            {!isCustom && (
              <>
                <div className="db-form-row">
                  <label className="db-form-grow">
                    主机 / 地址
                    <input className="input" value={cfg.host || ''} onChange={e => setEditing({ ...editing, config: { ...cfg, host: e.target.value } })} />
                  </label>
                  <label style={{ width: 110 }}>
                    端口
                    <input className="input" type="number" value={cfg.port || 0} onChange={e => setEditing({ ...editing, config: { ...cfg, port: Number(e.target.value) } })} />
                  </label>
                </div>
                <label>
                  用户名
                  <input className="input" value={cfg.username || ''} onChange={e => setEditing({ ...editing, config: { ...cfg, username: e.target.value } })} placeholder={meta.defaultUser || '(可选)'} />
                </label>
                <label>
                  密码{editing.id && <span className="dim" style={{ fontSize: '0.625rem' }}> (留空表示不修改)</span>}
                  <input className="input" type="password" value={password} onChange={e => setPassword(e.target.value)} />
                </label>
                {meta.hasDatabase && (
                  <label>
                    默认数据库
                    <input className="input" value={cfg.database || ''} onChange={e => setEditing({ ...editing, config: { ...cfg, database: e.target.value } })} placeholder="连接时使用的库名 (可选)" />
                  </label>
                )}
                {meta.hasCollection && !meta.hasDatabase && (
                  <label>
                    {isVector ? '默认 Collection' : '默认 Topic / Queue'}
                    <input className="input" value={cfg.database || ''} onChange={e => setEditing({ ...editing, config: { ...cfg, database: e.target.value } })} placeholder={isVector ? '向量集合名 (可选)' : '主题/队列名 (可选)'} />
                  </label>
                )}
                <label>
                  环境标记 <span className="dim" style={{ fontSize: '0.625rem' }}>(dev / staging / prod, 影响风险评估)</span>
                  <select className="input" value={cfg.envTag || ''} onChange={e => setEditing({ ...editing, config: { ...cfg, envTag: e.target.value } })}>
                    <option value="">(自动判断)</option>
                    <option value="dev">开发</option>
                    <option value="staging">预发</option>
                    <option value="prod">生产</option>
                  </select>
                </label>
                <label>
                  SSL
                  <select className="input" value={cfg.sslMode || ''} onChange={e => setEditing({ ...editing, config: { ...cfg, sslMode: e.target.value } })}>
                    <option value="disable">关闭</option>
                    <option value="preferred">首选</option>
                    <option value="required">强制</option>
                    <option value="verify-ca">验证 CA</option>
                    <option value="verify-full">完全验证</option>
                    <option value="skip-verify">跳过验证</option>
                  </select>
                </label>
              </>
            )}
            {isCustom && (
              <>
                <label>
                  Driver
                  <input className="input" value={cfg.driver || ''} onChange={e => setEditing({ ...editing, config: { ...cfg, driver: e.target.value } })} placeholder="如: mysql / postgres / oracle" />
                </label>
                <label>
                  DSN
                  <input className="input" value={cfg.dsn || ''} onChange={e => setEditing({ ...editing, config: { ...cfg, dsn: e.target.value } })} placeholder="如: user:pass@tcp(127.0.0.1:3306)/db" />
                </label>
              </>
            )}

            {/* 高级选项折叠面板: SSH / SSL 证书 / 代理 / 超时 / MaxRows / ExtraParams */}
            <details className="db-advanced" open={showAdvanced} onToggle={e => setShowAdvanced((e.target as HTMLDetailsElement).open)}>
              <summary>高级选项 <span className="dim" style={{ fontSize: '0.625rem' }}>(SSH 隧道 / SSL 证书 / 代理 / 超时 / 最大行数)</span></summary>
              <div className="db-advanced-body">
                {!isCustom && (
                  <>
                    <label className="db-advanced-toggle">
                      <input type="checkbox" checked={!!cfg.useSSH} onChange={e => setEditing({ ...editing, config: { ...cfg, useSSH: e.target.checked, ssh: cfg.ssh || { enabled: false, host: '', port: 22, user: '', authMode: 'password' } } })} />
                      使用 SSH 隧道
                    </label>
                    {cfg.useSSH && (
                      <div className="db-advanced-block">
                        <div className="db-form-row">
                          <label className="db-form-grow">
                            SSH 主机
                            <input className="input" value={cfg.ssh?.host || ''} onChange={e => setEditing({ ...editing, config: { ...cfg, ssh: { ...(cfg.ssh || { enabled: true, port: 22, user: '', authMode: 'password' as const }), host: e.target.value } } })} />
                          </label>
                          <label style={{ width: 90 }}>
                            SSH 端口
                            <input className="input" type="number" value={cfg.ssh?.port || 22} onChange={e => setEditing({ ...editing, config: { ...cfg, ssh: { ...(cfg.ssh || { enabled: true, host: '', user: '', authMode: 'password' as const }), port: Number(e.target.value) } } })} />
                          </label>
                        </div>
                        <label>
                          SSH 用户
                          <input className="input" value={cfg.ssh?.user || ''} onChange={e => setEditing({ ...editing, config: { ...cfg, ssh: { ...(cfg.ssh || { enabled: true, host: '', port: 22, authMode: 'password' as const }), user: e.target.value } } })} />
                        </label>
                        <label>
                          SSH 认证
                          <select className="input" value={cfg.ssh?.authMode || 'password'} onChange={e => setEditing({ ...editing, config: { ...cfg, ssh: { ...(cfg.ssh || { enabled: true, host: '', port: 22, user: '' }), authMode: e.target.value as 'password' | 'privateKey' } } })}>
                            <option value="password">密码</option>
                            <option value="privateKey">私钥</option>
                          </select>
                        </label>
                        {cfg.ssh?.authMode === 'privateKey' ? (
                          <label>
                            私钥路径
                            <input className="input" value={cfg.ssh?.keyPath || ''} onChange={e => setEditing({ ...editing, config: { ...cfg, ssh: { ...(cfg.ssh || { enabled: true, host: '', port: 22, user: '', authMode: 'privateKey' as const }), keyPath: e.target.value } } })} placeholder="C:/Users/.../id_rsa" />
                          </label>
                        ) : (
                          <label>
                            SSH 密码
                            <input className="input" type="password" value={cfg.ssh?.password || ''} onChange={e => setEditing({ ...editing, config: { ...cfg, ssh: { ...(cfg.ssh || { enabled: true, host: '', port: 22, user: '', authMode: 'password' as const }), password: e.target.value } } })} />
                          </label>
                        )}
                      </div>
                    )}

                    <label className="db-advanced-toggle">
                      <input type="checkbox" checked={!!cfg.useProxy} onChange={e => setEditing({ ...editing, config: { ...cfg, useProxy: e.target.checked, proxy: cfg.proxy || { enabled: false, type: 'http', host: '', port: 0 } } })} />
                      使用网络代理
                    </label>
                    {cfg.useProxy && (
                      <div className="db-advanced-block">
                        <div className="db-form-row">
                          <label style={{ width: 120 }}>
                            代理类型
                            <select className="input" value={cfg.proxy?.type || 'http'} onChange={e => setEditing({ ...editing, config: { ...cfg, proxy: { ...(cfg.proxy || { enabled: true, host: '', port: 0 }), type: e.target.value as 'http' | 'socks5' } } })}>
                              <option value="http">HTTP</option>
                              <option value="socks5">SOCKS5</option>
                            </select>
                          </label>
                          <label className="db-form-grow">
                            代理主机
                            <input className="input" value={cfg.proxy?.host || ''} onChange={e => setEditing({ ...editing, config: { ...cfg, proxy: { ...(cfg.proxy || { enabled: true, type: 'http', port: 0 }), host: e.target.value } } })} />
                          </label>
                          <label style={{ width: 90 }}>
                            端口
                            <input className="input" type="number" value={cfg.proxy?.port || 0} onChange={e => setEditing({ ...editing, config: { ...cfg, proxy: { ...(cfg.proxy || { enabled: true, type: 'http', host: '' }), port: Number(e.target.value) } } })} />
                          </label>
                        </div>
                      </div>
                    )}

                    <div className="db-advanced-section">SSL 证书 (verify-full 模式)</div>
                    <div className="db-advanced-block">
                      <label>
                        CA 证书路径
                        <input className="input" value={cfg.ssl?.caCert || ''} onChange={e => setEditing({ ...editing, config: { ...cfg, ssl: { ...(cfg.ssl || { mode: cfg.sslMode }), caCert: e.target.value } } })} placeholder="/path/to/ca.pem" />
                      </label>
                      <label>
                        SNI ServerName (可选)
                        <input className="input" value={cfg.ssl?.serverName || ''} onChange={e => setEditing({ ...editing, config: { ...cfg, ssl: { ...(cfg.ssl || { mode: cfg.sslMode }), serverName: e.target.value } } })} placeholder="db.example.com" />
                      </label>
                    </div>
                  </>
                )}

                <div className="db-advanced-section">查询调优</div>
                <div className="db-advanced-block">
                  <div className="db-form-row">
                    <label style={{ width: 110 }}>
                      连接超时 (秒)
                      <input className="input" type="number" value={cfg.timeoutSec || 15} onChange={e => setEditing({ ...editing, config: { ...cfg, timeoutSec: Number(e.target.value) } })} />
                    </label>
                    <label style={{ width: 110 }}>
                      语句超时 (秒)
                      <input className="input" type="number" value={cfg.queryTimeoutSec || 30} onChange={e => setEditing({ ...editing, config: { ...cfg, queryTimeoutSec: Number(e.target.value) } })} />
                    </label>
                    <label style={{ width: 110 }}>
                      最大行数
                      <input className="input" type="number" value={cfg.maxRows || 5000} onChange={e => setEditing({ ...editing, config: { ...cfg, maxRows: Number(e.target.value) } })} />
                    </label>
                  </div>
                  <label>
                    额外 DSN 参数 <span className="dim" style={{ fontSize: '0.625rem' }}>(透传到驱动, 如: charset=utf8mb4&parseTime=true)</span>
                    <input className="input" value={cfg.extraParams || ''} onChange={e => setEditing({ ...editing, config: { ...cfg, extraParams: e.target.value } })} />
                  </label>
                </div>

                <div className="db-advanced-section">分组与备注</div>
                <div className="db-advanced-block">
                  <div className="db-form-row">
                    <label className="db-form-grow">
                      分组 <span className="dim" style={{ fontSize: '0.625rem' }}>(如: 团队/项目/区域)</span>
                      <input className="input" value={cfg.group || ''} onChange={e => setEditing({ ...editing, config: { ...cfg, group: e.target.value } })} placeholder="可选" />
                    </label>
                    <label style={{ width: 90 }}>
                      图标 (emoji)
                      <input className="input" value={cfg.icon || ''} onChange={e => setEditing({ ...editing, config: { ...cfg, icon: e.target.value } })} placeholder="🏠" />
                    </label>
                  </div>
                  <label>
                    备注
                    <input className="input" value={cfg.note || ''} onChange={e => setEditing({ ...editing, config: { ...cfg, note: e.target.value } })} placeholder="如: 双写备库" />
                  </label>
                </div>

                {/* 引擎特参 */}
                {pickedEngine === 'mongodb' && (
                  <div className="db-advanced-section">MongoDB 特参</div>
                )}
                {pickedEngine === 'mongodb' && (
                  <div className="db-advanced-block">
                    <div className="db-form-row">
                      <label className="db-form-grow">
                        Replica Set
                        <input className="input" value={cfg.mongoReplicaSet || ''} onChange={e => setEditing({ ...editing, config: { ...cfg, mongoReplicaSet: e.target.value } })} placeholder="rs0" />
                      </label>
                      <label style={{ width: 130 }}>
                        Auth Source
                        <input className="input" value={cfg.mongoAuthSource || ''} onChange={e => setEditing({ ...editing, config: { ...cfg, mongoAuthSource: e.target.value } })} placeholder="admin" />
                      </label>
                    </div>
                    <div className="db-form-row">
                      <label style={{ width: 150 }}>
                        Read Preference
                        <select className="input" value={cfg.mongoReadPreference || 'primary'} onChange={e => setEditing({ ...editing, config: { ...cfg, mongoReadPreference: e.target.value } })}>
                          <option value="primary">primary</option>
                          <option value="secondary">secondary</option>
                          <option value="nearest">nearest</option>
                          <option value="primaryPreferred">primaryPreferred</option>
                          <option value="secondaryPreferred">secondaryPreferred</option>
                        </select>
                      </label>
                      <label className="db-advanced-toggle" style={{ marginLeft: 8 }}>
                        <input type="checkbox" checked={!!cfg.mongoSrv} onChange={e => setEditing({ ...editing, config: { ...cfg, mongoSrv: e.target.checked } })} />
                        SRV 记录 (mongodb+srv://)
                      </label>
                    </div>
                  </div>
                )}

                {pickedEngine === 'clickhouse' && (
                  <div className="db-advanced-section">ClickHouse 特参</div>
                )}
                {pickedEngine === 'clickhouse' && (
                  <div className="db-advanced-block">
                    <label style={{ width: 150 }}>
                      协议
                      <select className="input" value={cfg.clickHouseProtocol || 'auto'} onChange={e => setEditing({ ...editing, config: { ...cfg, clickHouseProtocol: e.target.value } })}>
                        <option value="auto">auto</option>
                        <option value="http">HTTP</option>
                        <option value="native">Native</option>
                      </select>
                    </label>
                  </div>
                )}

                {pickedEngine === 'oceanbase' && (
                  <div className="db-advanced-section">OceanBase 特参</div>
                )}
                {pickedEngine === 'oceanbase' && (
                  <div className="db-advanced-block">
                    <label style={{ width: 150 }}>
                      兼容协议
                      <select className="input" value={cfg.oceanBaseProtocol || 'mysql'} onChange={e => setEditing({ ...editing, config: { ...cfg, oceanBaseProtocol: e.target.value } })}>
                        <option value="mysql">MySQL</option>
                        <option value="oracle">Oracle</option>
                      </select>
                    </label>
                  </div>
                )}
              </div>
            </details>
            <div className="db-form-actions">
              <button className="btn-glass-soft btn-glass-soft-sm" onClick={cancelWizard} disabled={busy}>取消</button>
              <button className="btn-glass-soft btn-glass-soft-sm btn-glass-soft-accent" onClick={save} disabled={busy}>
                {busy ? '保存中...' : '保存'}
              </button>
            </div>
          </div>
        </div>
      </div>,
      document.body,
    )
  }

  return null
}
