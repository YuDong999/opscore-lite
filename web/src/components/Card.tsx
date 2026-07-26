// ── 通用玻璃卡片组件: 标题 + 副标题 + 内容区 ──

import type { ReactNode } from 'react'

export default function Card({
  title,
  subtitle,
  children,
  className = '',
}: {
  title?: string        // 卡片标题(可选)
  subtitle?: string     // 右侧副标题(可选)
  children: ReactNode   // 卡片内容
  className?: string    // 额外 CSS 类
}) {
  return (
    <section className={`card glass ${className}`}>
      {title && (
        <div className="card-head">
          <h3>{title}</h3>
          {subtitle && <span className="card-sub">{subtitle}</span>}
        </div>
      )}
      <div className="card-body">{children}</div>
    </section>
  )
}
