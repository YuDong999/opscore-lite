import { createContext, useContext, useState, useCallback, useRef, type ReactNode } from 'react'

export type ToastType = 'success' | 'error' | 'info' | 'warn'

interface ToastItem {
  id: number
  message: string
  type: ToastType
}

interface ToastCtx {
  toast: (message: string, type?: ToastType) => void
}

const ToastContext = createContext<ToastCtx>({ toast: () => {} })

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
    setItems(list => [...list, { id, message, type }])
    const timer = setTimeout(() => remove(id), 3200)
    timers.current.set(id, timer)
  }, [remove])

  return (
    <ToastContext.Provider value={{ toast }}>
      {children}
      <div className="toast-container">
        {items.map(t => (
          <div key={t.id}
            className={`toast-card toast-${t.type}`}
            onAnimationEnd={() => remove(t.id)}>
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
