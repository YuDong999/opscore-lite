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
    url.includes('/query?') ||
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
// force=true 时**绕过缓存读取**, 直接拉新值并写回缓存 —— 供"用户主动刷新/写操作后重载"使用。
//
// 为什么必须有这个开关(2026-10-02 真机): 原来的 SWR 分支在缓存过期时
// `return Promise.resolve(hit.data)` 立刻把**旧值**交给调用方, 新数据只在后台写进缓存。
// 但这里的唯一消费者 useResource 拿到 Promise 就结束了 —— 没有任何订阅者会被通知,
// 于是"后台拉到的新值"永远没人用。表现就是: 跑完流水线 / 点刷新按钮, 左侧列表纹丝不动,
// 只有硬刷新(Ctrl+F5, 缓存整个没了)才变。SWR 的"stale"部分生效了, "revalidate"部分断了。
export function getJSON<T = any>(url: string, opts?: { force?: boolean }): Promise<T> {
  if (swrBypassed(url)) return fetchJSON<T>(url)
  const hit = swrCache.get(url)
  if (hit && !opts?.force) {
    const age = Date.now() - hit.ts
    if (age < SWR_TTL) return Promise.resolve(hit.data as T)
    // 过期: 立刻给旧值让界面先渲染, 同时后台刷新(去重)。下一次 reload(force) 就能拿到新值。
    if (!swrInflight.has(url)) {
      const bg = fetchJSON<T>(url)
        .then((d) => {
          swrCache.set(url, { data: d, ts: Date.now() })
          swrInflight.delete(url)
          return d
        })
        .catch(() => {
          swrInflight.delete(url)
          return hit.data as T
        })
      swrInflight.set(url, bg)
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
