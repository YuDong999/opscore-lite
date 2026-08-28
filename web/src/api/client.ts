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
  let lastErr: any
  for (let attempt = 0; attempt < 2; attempt++) {
    try {
      const r = await fetch(url, { headers: authHeaders() })
      if (r.status === 401) {
        localStorage.removeItem('opscore-token')
        window.location.reload()
        throw new Error('未授权')
      }
      if (!r.ok) throw new Error(await parseError(r))
      return r.json()
    } catch (e) {
      // 仅对真正的网络层错误(部署重启窗口瞬时断连)重试一次
      const msg = (e as Error)?.message || ''
      const netErr = e instanceof TypeError || msg.includes('fetch') || msg.includes('NetworkError')
      if (attempt === 0 && netErr) {
        lastErr = e
        await new Promise((res) => setTimeout(res, 800))
        continue
      }
      throw e
    }
  }
  throw lastErr
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

// POST 请求, body 自动序列化 JSON, 返回 JSON
// 写操作后整表清空 GET 缓存: 写操作低频, 而所影响的读路径(列表/详情)未必与本次
// URL 同前缀(如删除集群清的是 /k8s/clusters), 全清比按前缀精确失效更可靠。
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
  swrCache.clear()
  if (!r.ok) throw new Error(await parseError(r))
  const d = r.json()
  return d
}
