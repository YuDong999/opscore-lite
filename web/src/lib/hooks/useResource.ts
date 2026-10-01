// 列表加载样板收敛(R2) —— L2 公共 hook
// 数据 + 错误 + 手动刷新。url 为空串时不发起(调用方可用它表达"尚未就绪")。
import { useCallback, useEffect, useState } from 'react'
import { getJSON } from '../../api/client'

export function useResource<T>(url: string) {
  const [data, setData] = useState<T | null>(null)
  const [err, setErr] = useState('')
  // force=true 走 getJSON 的强制刷新(绕缓存读)。
  // 首次挂载用 force=false(命中 2.5s 缓存可秒出); **手动 reload 一律 force=true** ——
  // 否则"点刷新/跑完流水线后 reload"会拿到缓存旧值, 界面不更新(2026-10-02 真机踩到)。
  const load = useCallback((force = false) => {
    if (!url) return
    getJSON<T>(url, { force }).then(d => { setData(d); setErr('') }).catch(e => setErr(e.message))
  }, [url])
  useEffect(() => { load(false) }, [load])
  return { data, err, setErr, reload: () => load(true) }
}
