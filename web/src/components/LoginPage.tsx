import { useState } from 'react'
import { postJSON } from '../api/client'

interface Props {
  onLogin: () => void
}

export default function LoginPage({ onLogin }: Props) {
  const [token, setToken] = useState('')
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!token.trim()) return
    setLoading(true)
    setError('')
    try {
      // 验证 token 是否有效
      const res = await fetch('/api/manifest', {
        headers: { Authorization: `Bearer ${token.trim()}` },
      })
      if (res.ok) {
        localStorage.setItem('opscore-token', token.trim())
        onLogin()
      } else if (res.status === 401) {
        setError('Token 无效')
      } else {
        setError(`服务器错误: ${res.status}`)
      }
    } catch {
      setError('连接失败')
    } finally {
      setLoading(false)
    }
  }

  return (
    <div style={{
      position: 'fixed', inset: 0,
      display: 'flex', alignItems: 'center', justifyContent: 'center',
      background: 'linear-gradient(135deg, var(--bg-grad-1), var(--bg-grad-2))',
      zIndex: 100,
    }}>
      <div className="card" style={{ width: 380, padding: 32 }}>
        <div style={{ textAlign: 'center', marginBottom: 24 }}>
          <div className="brand-dot" style={{ width: 40, height: 40, margin: '0 auto 12px' }} />
          <h1 style={{ fontSize: 22, fontWeight: 800, margin: 0 }}>OpsCore</h1>
          <p style={{ fontSize: 13, color: 'var(--text-dim)', margin: '4px 0 0' }}>运维控制台 · 请验证身份</p>
        </div>
        <form onSubmit={handleSubmit}>
          <input
            className="ipt"
            type="password"
            value={token}
            onChange={(e) => setToken(e.target.value)}
            placeholder="输入访问令牌..."
            autoFocus
            style={{ width: '100%', boxSizing: 'border-box', marginBottom: 12 }}
          />
          {error && (
            <div className="lockout-warn" style={{ marginBottom: 12 }}>{error}</div>
          )}
          <button
            className="btn btn-accent"
            type="submit"
            disabled={loading || !token.trim()}
            style={{ width: '100%' }}
          >
            {loading ? '验证中...' : '登录'}
          </button>
        </form>
      </div>
    </div>
  )
}
