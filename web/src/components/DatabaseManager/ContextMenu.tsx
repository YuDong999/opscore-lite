// 通用右键菜单: 定位式浮层, 点击外部自动关闭。

import { useEffect, useRef } from 'react'
import { createPortal } from 'react-dom'

export interface ContextMenuItem {
  label?: string
  icon?: React.ReactNode
  divider?: boolean
  danger?: boolean
  disabled?: boolean
  onClick?: () => void
}

export default function ContextMenu({
  x,
  y,
  items,
  onClose,
}: {
  x: number
  y: number
  items: ContextMenuItem[]
  onClose: () => void
}) {
  const ref = useRef<HTMLDivElement>(null)

  useEffect(() => {
    const onDocClick = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) onClose()
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    document.addEventListener('mousedown', onDocClick)
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('mousedown', onDocClick)
      document.removeEventListener('keydown', onKey)
    }
  }, [onClose])

  // 将菜单限制在视口内
  const style: React.CSSProperties = { left: x, top: y }
  const estW = 220
  const estH = items.length * 32 + 12
  if (x + estW > window.innerWidth) style.left = Math.max(4, x - estW)
  if (y + estH > window.innerHeight) style.top = Math.max(4, y - estH)

  // portal 到 body: 脱离 .db-side 层叠上下文, 否则被兄弟 .db-main 盖住
  return createPortal(
    <div className="db-ctx-menu" ref={ref} style={style}>
      {items.map((it, i) =>
        it.divider ? (
          <div key={i} className="db-ctx-menu-divider" />
        ) : (
          <button
            key={i}
            className={`db-ctx-menu-item ${it.danger ? 'db-ctx-menu-danger' : ''} ${it.disabled ? 'db-ctx-menu-disabled' : ''}`}
            disabled={it.disabled}
            onClick={() => {
              if (it.disabled) return
              onClose()
              it.onClick?.()
            }}
          >
            {it.icon && <span className="db-ctx-menu-icon">{it.icon}</span>}
            <span>{it.label}</span>
          </button>
        ),
      )}
    </div>,
    document.body,
  )
}
