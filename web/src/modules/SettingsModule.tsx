import { useState, useEffect } from 'react'
import { useTheme, THEMES } from '../theme'
import { getJSON, postJSON } from '../api/client'

interface MigrationStatus {
  currentDB: string
  dsn: string
  keyCount: number
}

interface MigrationResult {
  ok: boolean
  message: string
  keys?: string[]
}

export default function SettingsModule() {
  const { theme, setTheme, meta, surface, setSurface, corner, setCorner } = useTheme()
  const [token, setToken] = useState('')
  const [configured, setConfigured] = useState(false)
  const [saved, setSaved] = useState(false)

  const [dbStatus, setDbStatus] = useState<MigrationStatus | null>(null)
  const [pgDSN, setPgDSN] = useState('')
  const [migrating, setMigrating] = useState(false)
  const [migrateResult, setMigrateResult] = useState<MigrationResult | null>(null)
  const [maintenance, setMaintenance] = useState(false)
  const [info, setInfo] = useState<any>(null)

  useEffect(() => {
    getJSON<any>('/api/auth/token').then((d) => {
      setConfigured(d.configured === 'true')
      if (d.token) setToken(d.token)
    }).catch(() => {})
    getJSON<MigrationStatus>('/api/system/migration-status').then(setDbStatus).catch(() => {})
    getJSON<{ enabled: boolean }>('/api/cicd/maintenance').then(d => setMaintenance(d.enabled)).catch(() => {})
    getJSON<any>('/api/system/info').then(setInfo).catch(() => {})
  }, [])

  const toggleMaintenance = async () => {
    const next = !maintenance
    try {
      await postJSON('/api/cicd/maintenance', { enabled: next })
      setMaintenance(next)
    } catch {}
  }

  const saveToken = async () => {
    try {
      await postJSON('/api/auth/token', { token: token.trim() })
      if (token.trim()) {
        localStorage.setItem('opscore-token', token.trim())
      } else {
        localStorage.removeItem('opscore-token')
      }
      setConfigured(!!token.trim())
      setSaved(true)
      setTimeout(() => setSaved(false), 2000)
    } catch {}
  }

  const doMigrate = async () => {
    setMigrating(true)
    setMigrateResult(null)
    try {
      const r = await postJSON<MigrationResult>('/api/system/migrate', { dsn: pgDSN })
      setMigrateResult(r)
      if (r.ok) {
        setDbStatus({ currentDB: 'postgres', dsn: pgDSN, keyCount: r.keys?.length || 0 })
      }
    } catch {
      setMigrateResult({ ok: false, message: '迁移请求失败' })
    } finally {
      setMigrating(false)
    }
  }

  return (
    <div>
      <div style={{ marginBottom: 24 }}>
        <h2 style={{ fontSize:'1.125rem', fontWeight: 700, marginBottom: 4 }}>主题设置</h2>
        <p style={{ fontSize:'0.8125rem', color: 'var(--text-dim)', marginBottom: 16 }}>
          当前：{meta.label}（{meta.dark ? '暗色' : '亮色'}）
        </p>
        <div style={{ display: 'flex', gap:'0.75rem', flexWrap: 'wrap' }}>
          {THEMES.map((t) => (
            <button
              key={t.id}
              onClick={() => setTheme(t.id)}
              style={{
                padding: '0.75rem 1rem',
                borderRadius:'0.75rem',
                border: `0.125rem solid ${theme === t.id ? 'var(--accent)' : 'var(--border)'}`,
                background: theme === t.id ? 'var(--accent)' : 'var(--surface-solid)',
                color: theme === t.id ? '#fff' : 'var(--text)',
                cursor: 'pointer',
                display: 'flex',
                flexDirection: 'column',
                alignItems: 'center',
                gap:'0.5rem',
                minWidth:'6.25rem',
                transition: 'all 0.15s ease',
              }}
            >
              <div style={{ display: 'flex', gap: 4 }}>
                <div style={{
                  width:'1.25rem', height:'1.25rem', borderRadius: '50%',
                  background: t.colors[0], border: '1px solid rgba(0,0,0,0.1)',
                }} />
                <div style={{
                  width:'1.25rem', height:'1.25rem', borderRadius: '50%',
                  background: t.colors[1], border: '1px solid rgba(0,0,0,0.1)',
                }} />
              </div>
              <span style={{ fontSize:'0.8125rem', fontWeight: 600 }}>{t.label}</span>
            </button>
          ))}
        </div>
      </div>

      <div style={{ borderTop: '1px solid var(--border)', paddingTop: 20, marginBottom: 24 }}>
        <h2 style={{ fontSize:'1.125rem', fontWeight: 700, marginBottom: 4 }}>界面质感</h2>
        <p style={{ fontSize:'0.8125rem', color: 'var(--text-dim)', marginBottom: 16 }}>
          主题管强调色，扁平档会把表面换成中性灰（dbx 风格）；圆角尺度独立可调。
        </p>
        <div style={{ display:'flex', gap:'0.5rem', flexWrap:'wrap', marginBottom: 14 }}>
          {([
            { id: 'glass', label: '玻璃', hint: '半透明 + 背景模糊 + 渐变底，保留主题色调' },
            { id: 'flat', label: '扁平', hint: '中性灰实色 + 无模糊无渐变' },
          ] as const).map((s) => (
            <button
              key={s.id}
              onClick={() => setSurface(s.id)}
              title={s.hint}
              style={{
                padding: '0.5rem 0.875rem', cursor: 'pointer', textAlign: 'left',
                borderRadius: 'var(--radius-sm)',
                border: `2px solid ${surface === s.id ? 'var(--accent)' : 'var(--border)'}`,
                background: surface === s.id ? 'var(--surface-tint)' : 'var(--surface-solid)',
                color: 'var(--text)',
              }}
            >
              <div style={{ fontSize:'0.8125rem', fontWeight: 600 }}>{s.label}</div>
              <div style={{ fontSize:'0.6875rem', color: 'var(--text-dim)' }}>{s.hint}</div>
            </button>
          ))}
        </div>
        <div style={{ display:'flex', gap:'0.5rem', flexWrap:'wrap' }}>
          {([
            { id: 'round', label: '圆角 · 柔和', hint: '.75rem 基准' },
            { id: 'compact', label: '圆角 · 紧凑', hint: '4/6px，工具型密度' },
          ] as const).map((c) => (
            <button
              key={c.id}
              onClick={() => setCorner(c.id)}
              title={c.hint}
              style={{
                padding: '0.5rem 0.875rem', cursor: 'pointer',
                borderRadius: 'var(--radius-sm)',
                border: `2px solid ${corner === c.id ? 'var(--accent)' : 'var(--border)'}`,
                background: corner === c.id ? 'var(--surface-tint)' : 'var(--surface-solid)',
                color: 'var(--text)', fontSize:'0.8125rem', fontWeight: 600,
              }}
            >
              {c.label}
            </button>
          ))}
        </div>
      </div>

      <div style={{ borderTop: '1px solid var(--border)', paddingTop: 20, marginBottom: 24 }}>
        <h2 style={{ fontSize:'1.125rem', fontWeight: 700, marginBottom: 4 }}>访问令牌</h2>
        <p style={{ fontSize:'0.8125rem', color: 'var(--text-dim)', marginBottom: 4 }}>
          设置静态 Token 进行登录认证（留空则不启用认证）
        </p>
        {configured && (
          <p style={{ fontSize:'0.75rem', color: 'var(--ok)', marginBottom: 8 }}>
            ✓ 认证已启用
          </p>
        )}
        <div style={{ display: 'flex', gap:'0.5rem', alignItems: 'center' }}>
          <input
            className="ipt"
            type="password"
            value={token}
            onChange={(e) => setToken(e.target.value)}
            placeholder="输入 Token..."
            style={{ flex: 1, maxWidth: 400 }}
          />
          <button className="btn-glass-soft btn-glass-soft-accent" onClick={saveToken}>
            {saved ? '✓ 已保存' : '保存'}
          </button>
        </div>
      </div>

      <div style={{ borderTop: '1px solid var(--border)', paddingTop: 20, marginBottom: 24 }}>
        <h2 style={{ fontSize:'1.125rem', fontWeight: 700, marginBottom: 4 }}>CI/CD 引擎</h2>
        <p style={{ fontSize:'0.8125rem', color: 'var(--text-dim)', marginBottom: 12 }}>
          维护模式：暂停接受新的运行（定时/Webhook/手动全部拦截），正在运行的流水线不受影响。适用于服务重启前排水，避免产生中断记录。
        </p>
        <div style={{ display: 'flex', gap: '0.75rem', alignItems: 'center' }}>
          <button
            onClick={toggleMaintenance}
            style={{
              padding: '0.5rem 1rem', borderRadius: '0.5rem', cursor: 'pointer',
              border: `1px solid ${maintenance ? 'var(--danger)' : 'var(--border)'}`,
              background: maintenance ? 'var(--danger)' : 'var(--surface-solid)',
              color: maintenance ? '#fff' : 'var(--text)',
              fontWeight: 600, fontSize: '0.8125rem',
            }}
          >
            {maintenance ? '■ 关闭维护模式' : '▶ 开启维护模式'}
          </button>
          <span style={{ fontSize: '0.8125rem', color: maintenance ? 'var(--danger)' : 'var(--ok)', fontWeight: 600 }}>
            {maintenance ? '● 维护中：新的运行将被拒绝' : '○ 正常接收运行'}
          </span>
        </div>
      </div>

      <div style={{ borderTop: '1px solid var(--border)', paddingTop: 20 }}>
        <h2 style={{ fontSize:'1.125rem', fontWeight: 700, marginBottom: 8 }}>关于</h2>
        {info ? (
          <div className="sysinfo" style={{ maxWidth: 480 }}>
            <div className="sysinfo-item"><span className="sysinfo-k">OpsCore 版本</span><span className="sysinfo-v mono">{info.version}</span></div>
            <div className="sysinfo-item"><span className="sysinfo-k">Go 版本</span><span className="sysinfo-v mono">{info.goVersion}</span></div>
            <div className="sysinfo-item"><span className="sysinfo-k">已运行</span><span className="sysinfo-v tabular-nums">{Math.floor(info.uptimeMs / 3600000)}h {Math.floor(info.uptimeMs % 3600000 / 60000)}m</span></div>
          </div>
        ) : (
          <p style={{ fontSize: '0.8125rem', color: 'var(--text-dim)' }}>加载中...</p>
        )}
      </div>

      <div style={{ borderTop: '1px solid var(--border)', paddingTop: 20 }}>
        <h2 style={{ fontSize:'1.125rem', fontWeight: 700, marginBottom: 4 }}>数据库迁移</h2>
        {dbStatus && (
          <p style={{ fontSize:'0.8125rem', color: 'var(--text-dim)', marginBottom: 8 }}>
            当前数据库：<strong>{dbStatus.currentDB === 'sqlite' ? 'SQLite' : 'PostgreSQL'}</strong>
            {' · '}配置项：<strong>{dbStatus.keyCount}</strong> 条
          </p>
        )}
        {dbStatus?.currentDB === 'sqlite' ? (
          <>
            <p style={{ fontSize:'0.75rem', color: 'var(--text-dim)', marginBottom: 8 }}>
              将 SQLite 中的数据迁移到 PostgreSQL。输入 PostgreSQL 连接串，迁移后需手动重启服务并指定 --database 参数。
            </p>
            <div style={{ display: 'flex', gap:'0.5rem', alignItems: 'center' }}>
              <input
                className="ipt"
                value={pgDSN}
                onChange={(e) => setPgDSN(e.target.value)}
                placeholder="postgres://user:pass@host:5432/opscore"
                style={{ flex: 1, maxWidth:'31.25rem', fontFamily: 'monospace', fontSize: 12 }}
              />
              <button className="btn-glass-soft btn-glass-soft-accent" onClick={doMigrate} disabled={migrating || !pgDSN}>
                {migrating ? '迁移中...' : '开始迁移'}
              </button>
            </div>
            {migrateResult && (
              <div style={{
                marginTop:'0.75rem', padding: 12, borderRadius:'0.5rem',
                background: migrateResult.ok ? 'var(--ok-bg, #0a2e1a)' : 'var(--err-bg, #2e0a0a)',
                color: migrateResult.ok ? 'var(--ok, #4ade80)' : 'var(--danger)',
                fontSize:'0.8125rem',
              }}>
                {migrateResult.message}
              </div>
            )}
          </>
        ) : dbStatus?.currentDB === 'postgres' ? (
          <p style={{ fontSize:'0.8125rem', color: 'var(--ok)' }}>✓ 已在使用 PostgreSQL，无需迁移</p>
        ) : (
          <p style={{ fontSize:'0.8125rem', color: 'var(--text-dim)' }}>加载中...</p>
        )}
      </div>
    </div>
  )
}
