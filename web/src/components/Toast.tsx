import { createContext, useContext, useState, useCallback, useRef, useMemo, type ReactNode } from 'react'

export type ToastType = 'success' | 'error' | 'info' | 'warn'

interface ToastItem {
  id: number
  message: string
  type: ToastType
  exiting?: boolean
}

interface ToastCtx {
  success: (message: string) => void
  error: (message: string) => void
  info: (message: string) => void
  warn: (message: string) => void
}

const ToastContext = createContext<ToastCtx>({
  success: () => {},
  error: () => {},
  info: () => {},
  warn: () => {},
})

let nextId = 0

export function ToastProvider({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<ToastItem[]>([])
  const timers = useRef<Map<number, ReturnType<typeof setTimeout>>>(new Map())

  const remove = useCallback((id: number) => {
    setItems(list => list.filter(t => t.id !== id))
    const t = timers.current.get(id)
    if (t) clearTimeout(t)
    timers.current.delete(id)
  }, [])

  const toast = useCallback((message: string, type: ToastType = 'info') => {
    const id = nextId++
    setItems(list => [...list, { id, message, type, exiting: false }])
    const t1 = setTimeout(() => {
      setItems(list => list.map(t => t.id === id ? { ...t, exiting: true } : t))
      const t2 = setTimeout(() => remove(id), 320)
      timers.current.set(id, t2)
    }, 2800)
    timers.current.set(id, t1)
  }, [remove])

  const api = useMemo<ToastCtx>(() => ({
    success: (m) => toast(m, 'success'),
    error: (m) => toast(m, 'error'),
    info: (m) => toast(m, 'info'),
    warn: (m) => toast(m, 'warn'),
  }), [toast])

  return (
    <ToastContext.Provider value={api}>
      {children}
      <div className="toast-container">
        {items.map(t => (
          <div key={t.id}
            className={`toast-card toast-${t.type} ${t.exiting ? 'exit' : ''}`}>
            <span className="toast-ico">{icon(t.type)}</span>
            <span className="toast-msg">{t.message}</span>
          </div>
        ))}
      </div>
    </ToastContext.Provider>
  )
}

export function useToast() {
  return useContext(ToastContext)
}

function icon(type: ToastType) {
  const common = { width: 15, height: 15, viewBox: '0 0 24 24', fill: 'none', stroke: 'currentColor', strokeWidth: 2.2, strokeLinecap: 'round' as const, strokeLinejoin: 'round' as const }
  if (type === 'success') {
    return (
      <svg {...common}>
        <path d="M20 6 9 17l-5-5" />
      </svg>
    )
  }
  if (type === 'error') {
    return (
      <svg {...common}>
        <path d="M18 6 6 18M6 6l12 12" />
      </svg>
    )
  }
  if (type === 'warn') {
    return (
      <svg {...common}>
        <path d="M10.29 3.86 1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z" />
        <path d="M12 9v4M12 17h.01" />
      </svg>
    )
  }
  return (
    <svg {...common}>
      <circle cx="12" cy="12" r="10" />
      <path d="M12 16v-4M12 8h.01" />
    </svg>
  )
}
