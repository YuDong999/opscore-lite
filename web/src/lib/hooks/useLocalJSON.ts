// localStorage JSON 读写 hook —— L2 公共层(B5 视图偏好/B6 星标共用)
// 写穿(状态与本地存储同步更新), JSON 解析失败回退初始值, 隐私模式写入失败静默。
import { useCallback, useState } from 'react'

export function useLocalJSON<T>(key: string, initial: T) {
  const [value, setValue] = useState<T>(() => {
    try {
      const raw = localStorage.getItem(key)
      return raw ? JSON.parse(raw) as T : initial
    } catch { return initial }
  })
  const set = useCallback((v: T) => {
    setValue(v)
    try { localStorage.setItem(key, JSON.stringify(v)) } catch { /* 隐私模式等写入失败静默 */ }
  }, [key])
  return [value, set] as const
}
