// ── 通用右键菜单: 固定定位 + 视口内收敛, 点击外部/Esc/滚动关闭 ──
// 与业务解耦: 调用方持有 {x, y} 与 items, 在 onContextMenu 里 setState 即可。

import { useEffect, useRef, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { cn } from '@/lib/utils'

export interface CtxMenuItem {
  label: string
  icon?: ReactNode
  onSelect?: () => void
  disabled?: boolean
  danger?: boolean
  sep?: boolean // 该项渲染为分隔线(label 忽略)
}

export function ContextMenu({ x, y, items, onClose }: {
  x: number
  y: number
  items: CtxMenuItem[]
  onClose: () => void
}) {
  const ref = useRef<HTMLDivElement>(null)

  useEffect(() => {
    const onDown = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) onClose()
    }
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    // capture: 容器内滚动也要关(否则菜单与内容错位)
    const onScroll = () => onClose()
    window.addEventListener('mousedown', onDown)
    window.addEventListener('keydown', onKey)
    window.addEventListener('scroll', onScroll, true)
    window.addEventListener('resize', onScroll)
    return () => {
      window.removeEventListener('mousedown', onDown)
      window.removeEventListener('keydown', onKey)
      window.removeEventListener('scroll', onScroll, true)
      window.removeEventListener('resize', onScroll)
    }
  }, [onClose])

  // 视口收敛: 右/下边缘内收(高度按行高估算, 足够防溢出)
  const W = 190
  const H = items.length * 33 + 8
  const left = Math.max(4, Math.min(x, window.innerWidth - W - 8))
  const top = Math.max(4, Math.min(y, window.innerHeight - H - 8))

  // portal 挂 body: 卡片的 backdrop-filter 会劫持 fixed 定位基准(变成相对该祖先),
  // 不出 portal 菜单会偏移出视口
  return createPortal(
    <div ref={ref} className="fixed z-[300] min-w-[11rem] rounded-md border bg-popover text-popover-foreground shadow-md p-1 animate-in fade-in-0 zoom-in-95"
      style={{ left, top }} onContextMenu={e => e.preventDefault()}>
      {items.map((it, i) => it.sep ? <div key={i} className="my-1 h-px bg-border" /> : (
        <button key={i} disabled={it.disabled}
          className={cn('w-full flex items-center gap-2 rounded-sm px-2 py-1.5 text-sm text-left transition-colors',
            it.disabled ? 'opacity-40 cursor-not-allowed' : 'hover:bg-muted',
            it.danger && !it.disabled && 'text-destructive hover:bg-destructive/10')}
          onClick={() => { onClose(); it.onSelect?.() }}>
          {it.icon && <span className="shrink-0 [&_svg]:size-4 [&_svg]:shrink-0">{it.icon}</span>}
          <span className="truncate">{it.label}</span>
        </button>
      ))}
    </div>,
    document.body
  )
}
