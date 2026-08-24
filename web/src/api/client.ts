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

// ── GET 短缓存(SWR): 切模块/切主机时秒出旧数据, 后台静默刷新 ──
// 实时性敏感端点(轮询图表/日志尾随)排除在外, 行为与从前一致。
const SWR_TTL = 2500
const swrCache = new Map<string, { data: any; ts: number }>()
const swrInflight = new Map<string, Promise<any>>()

function swrBypassed(url: string): boolean {
  return (
    url.includes('/resources') ||
    url.includes('/overview') ||
    url.includes('source=file') ||
    url.includes('/logs?') ||
    url.includes('/stats?') ||
    url.includes('/sites/stats')
  )
}

async function fetchJSON<T = any>(url: string): Promise<T> {
  const r = await fetch(url, { headers: authHeaders() })
  if (r.status === 401) {
    localStorage.removeItem('opscore-token')
    window.location.reload()
    throw new Error('未授权')
  }
  if (!r.ok) throw new Error(await parseError(r))
  return r.json()
}

// GET 请求, 返回 JSON。
// 命中策略:
//   ① TTL(2.5s)内   → 直接回缓存, 不发请求(切换零延迟)
//   ② 过期但有旧值   → 先同步返回旧数据(页面立即可渲染),
//                      同时去重后台拉新并写缓存 —— 下次进入即为新值
//   ③ 无缓存/被排除  → 正常请求
export function getJSON<T = any>(url: string): Promise<T> {
  if (swrBypassed(url)) return fetchJSON<T>(url)
  const hit = swrCache.get(url)
  if (hit) {
    const age = Date.now() - hit.ts
    if (age < SWR_TTL) return Promise.resolve(hit.data as T)
    if (!swrInflight.has(url)) {
      const p = fetchJSON<T>(url)
        .then((d) => {
          swrCache.set(url, { data: d, ts: Date.now() })
          swrInflight.delete(url)
          return d
        })
        .catch((e) => {
          swrInflight.delete(url)
          throw e
        })
      swrInflight.set(url, p)
    }
    return Promise.resolve(hit.data as T)
  }
  const p = fetchJSON<T>(url).then((d) => {
    swrCache.set(url, { data: d, ts: Date.now() })
    swrInflight.delete(url)
    return d
  })
  swrInflight.set(url, p as Promise<any>)
  return p
}

// 使指定路径前缀的 GET 缓存失效(写操作成功后调用, 避免读到操作前的旧状态)
function invalidatePath(url: string) {
  const path = url.split('?')[0]
  for (const k of [...swrCache.keys()]) {
    if (k.split('?')[0] === path || k.startsWith(path)) swrCache.delete(k)
  }
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
  invalidatePath(url)
  if (!r.ok) throw new Error(await parseError(r))
  const d = r.json()
  return d
}
