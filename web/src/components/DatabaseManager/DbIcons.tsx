// 数据库类型 SVG 图标(内联, 零依赖)。风格对齐 dbx/GoNavi: 连接带引擎色、库为桶形、表为网格、视图为眼睛。
import type { EngineType } from './api'

// 每引擎主色(与 ENGINES.color 对齐的精选子集, 其余走默认色)
const ENGINE_COLORS: Record<string, string> = {
  mysql: '#3b82f6', mysql_agent: '#3b82f6', mariadb: '#a855f7', goldendb: '#f59e0b',
  postgres: '#0ea5e9', opengauss: '#059669', gaussdb: '#10b981', kingbase: '#0891b2',
  highgo: '#0d9488', vastbase: '#9333ea', oceanbase: '#0ea5e9',
  oracle: '#ef4444', dameng: '#7c3aed', sqlserver: '#dc2626',
  clickhouse: '#facc15', starrocks: '#0f766e', diros: '#9333ea', sphinx: '#a3a3a3',
  sqlite: '#52525b', duckdb: '#fde047',
  mongodb: '#10b981', elasticsearch: '#10b981',
  chroma: '#a78bfa', qdrant: '#ef4444', milvus: '#06b6d4',
  iotdb: '#f59e0b', tdengine: '#dc2626',
  kafka: '#1f2937', rabbitmq: '#f97316', rocketmq: '#1d4ed8', mqtt: '#8b5cf6',
  custom: '#94a3b8', redis: '#ef4444',
}

export function engineColor(engine: string | undefined): string {
  return ENGINE_COLORS[String(engine || '').toLowerCase()] || '#5b6abf'
}

/** 引擎图标: 圆角方块底 + 数据库桶形线条 */
export function EngineIcon({ engine, size = 16 }: { engine: string | EngineType | undefined; size?: number }) {
  const color = engineColor(engine as string)
  return (
    <span
      className="dbx-engine-icon"
      style={{ width: size, height: size, background: `${color}22`, borderColor: `${color}55` }}
      title={String(engine || '')}
    >
      <svg width={size * 0.62} height={size * 0.62} viewBox="0 0 24 24" fill="none" stroke={color} strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
        <ellipse cx="12" cy="5" rx="8" ry="3" />
        <path d="M4 5v14c0 1.66 3.58 3 8 3s8-1.34 8-3V5" />
        <path d="M4 12c0 1.66 3.58 3 8 3s8-1.34 8-3" />
      </svg>
    </span>
  )
}

/** 树节点图标: db / folder / table / view */
export function NodeIcon({ level, size = 14 }: { level: 'conn' | 'db' | 'group' | 'table' | 'view'; size?: number }) {
  const common = { width: size, height: size, viewBox: '0 0 24 24', fill: 'none', stroke: 'currentColor', strokeWidth: 2, strokeLinecap: 'round' as const, strokeLinejoin: 'round' as const }
  switch (level) {
    case 'conn':
      return null // 连接用 EngineIcon
    case 'db':
      return (
        <svg {...common} className="dbx-node-icon icon-db">
          <ellipse cx="12" cy="5" rx="8" ry="3" />
          <path d="M4 5v14c0 1.66 3.58 3 8 3s8-1.34 8-3V5" />
          <path d="M4 12c0 1.66 3.58 3 8 3s8-1.34 8-3" />
        </svg>
      )
    case 'group':
      return (
        <svg {...common} className="dbx-node-icon icon-group">
          <path d="M22 19a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5l2 3h9a2 2 0 0 1 2 2z" />
        </svg>
      )
    case 'view':
      return (
        <svg {...common} className="dbx-node-icon icon-view">
          <path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z" />
          <circle cx="12" cy="12" r="3" />
        </svg>
      )
    default:
      return (
        <svg {...common} className="dbx-node-icon icon-table">
          <rect x="3" y="3" width="18" height="18" rx="2" />
          <path d="M3 9h18M3 15h18M9 3v18" />
        </svg>
      )
  }
}

/** 小操作图标: 测试(插头)/编辑(铅笔)/删除(垃圾桶)/刷新 */
export function ActionIcon({ kind, size = 13 }: { kind: 'test' | 'edit' | 'delete' | 'refresh' | 'close'; size?: number }) {
  const common = { width: size, height: size, viewBox: '0 0 24 24', fill: 'none', stroke: 'currentColor', strokeWidth: 2, strokeLinecap: 'round' as const, strokeLinejoin: 'round' as const }
  switch (kind) {
    case 'test':
      return (
        <svg {...common}>
          <path d="M22 12h-4l-3 9L9 3l-3 9H2" />
        </svg>
      )
    case 'edit':
      return (
        <svg {...common}>
          <path d="M17 3a2.83 2.83 0 1 1 4 4L7.5 20.5 2 22l1.5-5.5z" />
        </svg>
      )
    case 'delete':
      return (
        <svg {...common}>
          <path d="M3 6h18M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2m3 0v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6" />
        </svg>
      )
    case 'refresh':
      return (
        <svg {...common}>
          <path d="M21 12a9 9 0 1 1-2.64-6.36L21 8" />
          <path d="M21 3v5h-5" />
        </svg>
      )
    default:
      return (
        <svg {...common}>
          <path d="M18 6L6 18M6 6l12 12" />
        </svg>
      )
  }
}
