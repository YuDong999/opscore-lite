import { useState, useEffect } from 'react'
import { useTheme, THEMES } from '../theme'
import { getJSON, postJSON } from '../api/client'

export default function SettingsModule() {
  const { theme, setTheme, meta } = useTheme()
  const [token, setToken] = useState('')
  const [configured, setConfigured] = useState(false)
  const [saved, setSaved] = useState(false)

  useEffect(() => {
    getJSON<any>('/api/auth/token').then((d) => {
      setConfigured(d.configured === 'true')
      if (d.token) setToken(d.token)
    }).catch(() => {})
  }, [])

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

  return (
    <div>
      <div style={{ marginBottom: 24 }}>
        <h2 style={{ fontSize: 18, fontWeight: 700, marginBottom: 4 }}>主题设置</h2>
        <p style={{ fontSize: 13, color: 'var(--text-dim)', marginBottom: 16 }}>
          当前：{meta.label}（{meta.dark ? '暗色' : '亮色'}）
        </p>
        <div style={{ display: 'flex', gap: 12, flexWrap: 'wrap' }}>
          {THEMES.map((t) => (
            <button
              key={t.id}
              onClick={() => setTheme(t.id)}
              style={{
                padding: '12px 16px',
                borderRadius: 12,
                border: `2px solid ${theme === t.id ? 'var(--accent)' : 'var(--border)'}`,
                background: theme === t.id ? 'var(--accent)' : 'var(--surface-solid)',
                color: theme === t.id ? '#fff' : 'var(--text)',
                cursor: 'pointer',
                display: 'flex',
                flexDirection: 'column',
                alignItems: 'center',
                gap: 8,
                minWidth: 100,
                transition: 'all 0.15s ease',
              }}
            >
              <div style={{ display: 'flex', gap: 4 }}>
                <div style={{
                  width: 20, height: 20, borderRadius: '50%',
                  background: t.colors[0], border: '1px solid rgba(0,0,0,0.1)',
                }} />
                <div style={{
                  width: 20, height: 20, borderRadius: '50%',
                  background: t.colors[1], border: '1px solid rgba(0,0,0,0.1)',
                }} />
              </div>
              <span style={{ fontSize: 13, fontWeight: 600 }}>{t.label}</span>
            </button>
          ))}
        </div>
      </div>

      <div style={{ borderTop: '1px solid var(--border)', paddingTop: 20 }}>
        <h2 style={{ fontSize: 18, fontWeight: 700, marginBottom: 4 }}>访问令牌</h2>
        <p style={{ fontSize: 13, color: 'var(--text-dim)', marginBottom: 4 }}>
          设置静态 Token 进行登录认证（留空则不启用认证）
        </p>
        {configured && (
          <p style={{ fontSize: 12, color: 'var(--ok)', marginBottom: 8 }}>
            ✓ 认证已启用
          </p>
        )}
        <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
          <input
            className="ipt"
            type="password"
            value={token}
            onChange={(e) => setToken(e.target.value)}
            placeholder="输入 Token..."
            style={{ flex: 1, maxWidth: 400 }}
          />
          <button className="btn btn-accent" onClick={saveToken}>
            {saved ? '✓ 已保存' : '保存'}
          </button>
        </div>
      </div>
    </div>
  )
}
