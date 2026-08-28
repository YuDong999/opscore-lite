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
  if (type === 'success') return '✓'
  if (type === 'error') return '✗'
  if (type === 'warn') return '⚠'
  return 'ℹ'
}
