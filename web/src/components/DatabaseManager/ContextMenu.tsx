// 通用右键菜单: 定位式浮层, 点击外部自动关闭。

import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'

export interface ContextMenuItem {
  label?: string
  icon?: React.ReactNode
  /** 'light' = 同类功能间细分隔线; 'heavy' = 不同类别间粗分隔线(参考 dbx) */
  divider?: boolean | 'light' | 'heavy'
  danger?: boolean
  disabled?: boolean
  onClick?: () => void
}

export default function ContextMenu({
  x,
  y,
  items: rawItems,
  onClose,
}: {
  x: number
  y: number
  items: ContextMenuItem[]
  onClose: () => void
}) {
  const ref = useRef<HTMLDivElement>(null)
  // 组件自防御: 调用方可能传 undefined(并发渲染/状态未就绪), 保证不崩
  const items = rawItems || []
  // 标准定位: 光标点=菜单左上角; 渲染后实测尺寸, 视口边缘翻转(下→上, 右→左), 保证完整可见
  const [pos, setPos] = useState({ x, y })

  useLayoutEffect(() => {
    const el = ref.current
    if (!el) return
    const r = el.getBoundingClientRect()
    const margin = 8
    let nx = x
    let ny = y
    if (x + r.width > window.innerWidth - margin) nx = Math.max(margin, x - r.width)
    if (y + r.height > window.innerHeight - margin) ny = Math.max(margin, y - r.height)
    setPos((prev) => (prev.x === nx && prev.y === ny ? prev : { x: nx, y: ny }))
  }, [x, y, items.length])

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

  const style: React.CSSProperties = { left: pos.x, top: pos.y }

  // 空菜单不渲染壳(避免一条细线假菜单)
  if (!items.length) return null

  // portal 到 body: 脱离 .db-side 层叠上下文, 否则被兄弟 .db-main 盖住
  return createPortal(
    <div className="db-ctx-menu" ref={ref} style={style}>
      {items.map((it, i) =>
        it.divider ? (
          <div key={i} className={`db-ctx-menu-divider ${it.divider === 'heavy' ? 'db-ctx-menu-divider-heavy' : ''}`} />
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
