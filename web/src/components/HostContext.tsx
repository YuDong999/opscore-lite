import { createContext, useContext, useEffect, useState, useCallback, type ReactNode } from 'react'
import { getJSON } from '../api/client'

export interface HostOption {
  id: string
  label: string
  addr: string
}

interface HostCtx {
  hosts: HostOption[]
  selected: HostOption | null
  setSelected: (h: HostOption | null) => void
  refreshHosts: () => void
}

const HostContext = createContext<HostCtx>({ hosts: [], selected: null, setSelected: () => {}, refreshHosts: () => {} })

const STORE_KEY = 'opscore-selected-host'

// 仅会话内记忆(关闭页面/重启服务后默认回到本机), 不再跨启动按历史恢复远程主机
function loadStoredHost(): HostOption | null {
  try {
    const raw = sessionStorage.getItem(STORE_KEY)
    if (raw) return JSON.parse(raw)
  } catch {}
  return null
}

export function HostProvider({ children }: { children: ReactNode }) {
  const [hosts, setHosts] = useState<HostOption[]>([])
  const [selected, setSelected] = useState<HostOption | null>(loadStoredHost)
  const [ver, setVer] = useState(0)

  const refreshHosts = useCallback(() => setVer(v => v + 1), [])

  const persistSelected = useCallback((h: HostOption | null) => {
    setSelected(h)
    try {
      if (h) sessionStorage.setItem(STORE_KEY, JSON.stringify(h))
      else sessionStorage.removeItem(STORE_KEY)
    } catch {}
  }, [])

  useEffect(() => {
    getJSON<any[]>('/api/ansible/hosts')
      .then((list) => {
        const opts: HostOption[] = list.map((h: any) => ({
          id: h.id,
          label: (h.alias || h.addr) + (h.alias && h.alias !== h.addr ? ` (${h.addr})` : ''),
          addr: h.addr,
        }))
        setHosts(opts)
        if (selected && !opts.some(x => x.id === selected.id)) {
          persistSelected(null)
        }
      })
      .catch(() => {
        setHosts([{ id: '', label: '本机', addr: '' }])
        if (selected && selected.id) persistSelected(null)
      })
  }, [ver])

  return (
    <HostContext.Provider value={{ hosts, selected, setSelected: persistSelected, refreshHosts }}>
      {children}
    </HostContext.Provider>
  )
}

export function useHost() {
  return useContext(HostContext)
}
