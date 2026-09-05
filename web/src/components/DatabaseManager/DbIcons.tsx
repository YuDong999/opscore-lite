// 数据库类型 SVG 图标(内联, 零依赖)。风格对齐 dbx/GoNavi: 连接带引擎色、库为桶形、表为网格、视图为眼睛。
import type { EngineType, EngineCategory } from './api'

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

/** 小操作图标(全部为 SVG, 禁止 emoji/dingbat): 测试/编辑/删除/刷新/关闭/新建查询/查看数据/复制/齿轮/表结构 */
export function ActionIcon({ kind, size = 13 }: { kind: 'test' | 'edit' | 'delete' | 'refresh' | 'close' | 'query' | 'chart' | 'copy' | 'gear' | 'doc' | 'search' | 'lock' | 'upload' | 'pin' | 'transfer'; size?: number }) {
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
    case 'query':
      return (
        <svg {...common}>
          <path d="M13 2 3 14h7l-1 8 10-12h-7l1-8z" />
        </svg>
      )
    case 'chart':
      return (
        <svg {...common}>
          <path d="M3 3v18h18" />
          <path d="M7 14l4-4 3 3 5-6" />
        </svg>
      )
    case 'copy':
      return (
        <svg {...common}>
          <rect x="9" y="9" width="13" height="13" rx="2" />
          <path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1" />
        </svg>
      )
    case 'gear':
      return (
        <svg {...common}>
          <circle cx="12" cy="12" r="3" />
          <path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 1 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 1 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 1 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 1 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z" />
        </svg>
      )
    case 'doc':
      return (
        <svg {...common}>
          <path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z" />
          <path d="M14 2v6h6M9 13h6M9 17h6" />
        </svg>
      )
    case 'search':
      return (
        <svg {...common}>
          <circle cx="11" cy="11" r="8" />
          <path d="m21 21-4.35-4.35" />
        </svg>
      )
    case 'lock':
      return (
        <svg {...common}>
          <rect x="3" y="11" width="18" height="11" rx="2" />
          <path d="M7 11V7a5 5 0 0 1 10 0v4" />
        </svg>
      )
    case 'upload':
      return (
        <svg {...common}>
          <path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4" />
          <path d="M7 8l5-5 5 5M12 3v12" />
        </svg>
      )
    case 'transfer':
      return (
        <svg {...common}>
          <path d="M8 3L4 7l4 4" />
          <path d="M4 7h16" />
          <path d="M16 21l4-4-4-4" />
          <path d="M20 17H4" />
        </svg>
      )
    case 'pin':
      return (
        <svg {...common}>
          <path d="M12 17v5M9 10.76a2 2 0 0 1-1.11 1.79l-1.78.9A2 2 0 0 0 5 15.24V16a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1v-.76a2 2 0 0 0-1.11-1.79l-1.78-.9A2 2 0 0 1 15 10.76V7a1 1 0 0 1 1-1 2 2 0 0 0 0-4H8a2 2 0 0 0 0 4 1 1 0 0 1 1 1z" />
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

/** 引擎类别图标(SVG, 替代原 emoji 图标) */
export function CategoryIcon({ cat, size = 13 }: { cat: EngineCategory | 'relational' | 'document' | 'vector' | 'timeseries' | 'search' | 'mq' | 'custom'; size?: number }) {
  const common = { width: size, height: size, viewBox: '0 0 24 24', fill: 'none', stroke: 'currentColor', strokeWidth: 2, strokeLinecap: 'round' as const, strokeLinejoin: 'round' as const }
  switch (cat) {
    case 'relational':
      return (
        <svg {...common}>
          <ellipse cx="12" cy="5" rx="9" ry="3" />
          <path d="M3 5v14c0 1.66 4 3 9 3s9-1.34 9-3V5" />
          <path d="M3 12c0 1.66 4 3 9 3s9-1.34 9-3" />
        </svg>
      )
    case 'document':
      return (
        <svg {...common}>
          <path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z" />
          <path d="M14 2v6h6M16 13H8M16 17H8M10 9H8" />
        </svg>
      )
    case 'vector':
      return (
        <svg {...common}>
          <path d="M12 3 3 8l9 5 9-5z" />
          <path d="M3 8v8l9 5 9-5V8" />
          <path d="M12 13v8" />
        </svg>
      )
    case 'timeseries':
      return (
        <svg {...common}>
          <path d="M3 3v18h18" />
          <path d="M7 15l4-5 3 3 4-7" />
        </svg>
      )
    case 'search':
      return (
        <svg {...common}>
          <circle cx="11" cy="11" r="8" />
          <path d="m21 21-4.35-4.35" />
        </svg>
      )
    case 'mq':
      return (
        <svg {...common}>
          <path d="M4 3h16v4H4zM4 10h16v4H4zM4 17h16v4H4z" />
        </svg>
      )
    default:
      return (
        <svg {...common}>
          <rect x="3" y="3" width="7" height="7" rx="1.5" />
          <rect x="14" y="3" width="7" height="7" rx="1.5" />
          <rect x="3" y="14" width="7" height="7" rx="1.5" />
          <rect x="14" y="14" width="7" height="7" rx="1.5" />
        </svg>
      )
  }
}
