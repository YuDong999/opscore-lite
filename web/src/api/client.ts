// ── HTTP 客户端: 自动附加 Bearer Token, 401 时跳登录页 ──

// 从 localStorage 读取 Token, 拼成 Authorization 请求头
function authHeaders(): Record<string, string> {
  const t = localStorage.getItem('opscore-token')
  return t ? { Authorization: `Bearer ${t}` } : {}
}

async function parseError(r: Response): Promise<string> {
  try {
    const body = await r.json()
    if (body && body.error) return body.error
  } catch {}
  return `HTTP ${r.status}`
}

// GET 请求, 返回 JSON
export async function getJSON<T = any>(url: string): Promise<T> {
  const r = await fetch(url, { headers: authHeaders() })
  if (r.status === 401) {
    localStorage.removeItem('opscore-token')
    window.location.reload()
    throw new Error('未授权')
  }
  if (!r.ok) throw new Error(await parseError(r))
  return r.json()
}

// POST 请求, body 自动序列化 JSON, 返回 JSON
export async function postJSON<T = any>(url: string, body: any): Promise<T> {
  const r = await fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', ...authHeaders() },
    body: JSON.stringify(body),
  })
  if (r.status === 401) {
    localStorage.removeItem('opscore-token')
    window.location.reload()
    throw new Error('未授权')
  }
  if (!r.ok) throw new Error(await parseError(r))
  return r.json()
}
