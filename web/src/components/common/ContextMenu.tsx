// 通用右键菜单(公共层): 定位式浮层, 点击外部/Esc/滚动/缩放自动关闭。
// 功能基线 = DatabaseManager 版(超集): 二级子菜单(hover 展开 / 右侧优先 / 越界翻左)、
//   useLayoutEffect 实测尺寸后的视口翻转、divider 三档(light/heavy)、空 items 不渲染。
// 兼容层: 同时识别 DatabaseManager 版字段(onClick / divider)与 cicd 版字段(onSelect / sep),
//   让两个旧调用点零改动迁移。样式沿用 .db-ctx-menu* 老类名(公共层既定策略, 保证调用点视觉零变化)。
// 已移植 cicd 版能力: 滚动关闭(容器内滚动也要关, 否则菜单与内容错位)。

import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'

export interface ContextMenuItem {
  label?: string
  icon?: React.ReactNode
  /** 'light' = 同类功能间细分隔线; 'heavy' = 不同类别间粗分隔线(参考 dbx) */
  divider?: boolean | 'light' | 'heavy'
  /** 兼容别名(cicd 版): 等价于 divider === true */
  sep?: boolean
  danger?: boolean
  disabled?: boolean
  /** 主点击回调(DatabaseManager 版) */
  onClick?: () => void
  /** 兼容别名(cicd 版): 等价于 onClick */
  onSelect?: () => void
  title?: string
  /** 二级子菜单: hover 展开; 子项点击后整体关闭 */
  children?: ContextMenuItem[]
}

/** 兼容别名类型(cicd 版导出名) */
export type CtxMenuItem = ContextMenuItem

export function ContextMenu({
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
    // 移植 cicd 版: 捕获阶段监听滚动, 容器内滚动也要关(否则菜单与内容错位)
    const onScrollOrResize = () => onClose()
    document.addEventListener('mousedown', onDocClick)
    document.addEventListener('keydown', onKey)
    window.addEventListener('scroll', onScrollOrResize, true)
    window.addEventListener('resize', onScrollOrResize)
    return () => {
      document.removeEventListener('mousedown', onDocClick)
      document.removeEventListener('keydown', onKey)
      window.removeEventListener('scroll', onScrollOrResize, true)
      window.removeEventListener('resize', onScrollOrResize)
    }
  }, [onClose])

  const closeAll = () => onClose()

  const style: React.CSSProperties = { left: pos.x, top: pos.y }

  // 子菜单渲染器: 挂在父菜单容器内, absolute 右侧展开, 越界翻左侧
  const renderItems = (list: ContextMenuItem[], isSub: boolean) => (
    <>
      {list.map((it, i) => {
        const isDivider = it.divider ?? it.sep
        const click = it.onClick ?? it.onSelect
        return isDivider ? (
          <div key={i} className={`db-ctx-menu-divider ${it.divider === 'heavy' ? 'db-ctx-menu-divider-heavy' : ''}`} />
        ) : it.children?.length ? (
          <div key={i} className="db-ctx-menu-subwrap">
            <button
              className={`db-ctx-menu-item db-ctx-menu-sub ${it.danger ? 'db-ctx-menu-danger' : ''}`}
              onClick={e => e.stopPropagation()}
            >
              {it.icon && <span className="db-ctx-menu-icon">{it.icon}</span>}
              <span>{it.label}</span>
              <svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" style={{ marginLeft: 'auto', flexShrink: 0 }}>
                <path d="m9 18 6-6-6-6" />
              </svg>
            </button>
            <div className="db-ctx-menu db-ctx-menu-subpanel">
              {renderItems(it.children, true)}
            </div>
          </div>
        ) : (
          <button
            key={i}
            className={`db-ctx-menu-item ${it.danger ? 'db-ctx-menu-danger' : ''} ${it.disabled ? 'db-ctx-menu-disabled' : ''}`}
            disabled={it.disabled}
            title={it.title}
            onClick={() => {
              if (it.disabled) return
              closeAll()
              click?.()
            }}
          >
            {it.icon && <span className="db-ctx-menu-icon">{it.icon}</span>}
            <span>{it.label}</span>
          </button>
        )
      })}
    </>
  )

  // 空菜单不渲染壳(避免一条细线假菜单)
  if (!items.length) return null

  // portal 到 body: 脱离 .db-side 层叠上下文, 否则被兄弟 .db-main 盖住
  return createPortal(
    <div className="db-ctx-menu" ref={ref} style={style}>
      {renderItems(items, false)}
    </div>,
    document.body,
  )
}

export default ContextMenu
