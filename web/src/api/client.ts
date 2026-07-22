function authHeaders(): Record<string, string> {
  const t = localStorage.getItem('opscore-token')
  return t ? { Authorization: `Bearer ${t}` } : {}
}

export async function getJSON<T = any>(url: string): Promise<T> {
  const r = await fetch(url, { headers: authHeaders() })
  if (r.status === 401) {
    localStorage.removeItem('opscore-token')
    window.location.reload()
    throw new Error('未授权')
  }
  if (!r.ok) throw new Error(`HTTP ${r.status}`)
  return r.json()
}

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
  return r.json()
}
