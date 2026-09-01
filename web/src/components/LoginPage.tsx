// ── 登录页: Bearer Token 身份验证 ──
// 用户输入 Token → 请求 /api/manifest → 成功则存 localStorage 并跳转

import { useState } from 'react'
import { postJSON } from '../api/client'

interface Props {
  onLogin: () => void  // 登录成功回调
}

export default function LoginPage({ onLogin }: Props) {
  const [token, setToken] = useState('')
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)

  // 提交 Token 验证
  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!token.trim()) return
    setLoading(true)
    setError('')
    try {
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
      <div className="card" style={{ width:'23.75rem', padding: 32 }}>
        <div style={{ textAlign: 'center', marginBottom: 24 }}>
          <div className="brand-dot" style={{ width:'2.5rem', height:'2.5rem', margin: '0 auto 0.75rem' }} />
          <h1 style={{ fontSize:'1.375rem', fontWeight: 800, margin: 0 }}>OpsCore</h1>
          <p style={{ fontSize:'0.8125rem', color: 'var(--text-dim)', margin: '0.25rem 0 0' }}>运维控制台 · 请验证身份</p>
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
            className="btn-glass-soft btn-glass-soft-accent"
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
