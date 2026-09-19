// 列表加载样板收敛(R2) —— L2 公共 hook
// 数据 + 错误 + 手动刷新。url 为空串时不发起(调用方可用它表达"尚未就绪")。
import { useCallback, useEffect, useState } from 'react'
import { getJSON } from '../../api/client'

export function useResource<T>(url: string) {
  const [data, setData] = useState<T | null>(null)
  const [err, setErr] = useState('')
  const load = useCallback(() => {
    if (!url) return
    getJSON<T>(url).then(d => { setData(d); setErr('') }).catch(e => setErr(e.message))
  }, [url])
  useEffect(load, [load])
  return { data, err, setErr, reload: load }
}
