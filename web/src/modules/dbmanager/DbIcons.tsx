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

/** 品牌资源表(源自 GoNavi /db-icons, Apache-2.0): 有官方 logo 的引擎优先用图片 */
const BRAND_ASSETS: Record<string, { src: string; scale?: number; bg?: string }> = {
  // ── dbx 品牌图标(优先, 105 种, /db-icons/dbx/) ──
  mysql: { src: '/db-icons/dbx/mysql.svg' },
  mariadb: { src: '/db-icons/dbx/mariadb.svg' },
  postgres: { src: '/db-icons/dbx/postgres.svg' },
  oracle: { src: '/db-icons/dbx/oracle.svg' },
  goldendb: { src: '/db-icons/dbx/goldendb.png' },
  clickhouse: { src: '/db-icons/dbx/clickhouse.svg' },
  sqlserver: { src: '/db-icons/dbx/sqlserver.svg' },
  duckdb: { src: '/db-icons/dbx/duckdb.svg' },
  dameng: { src: '/db-icons/dbx/dm.svg' },
  gaussdb: { src: '/db-icons/dbx/gaussdb.svg' },
  opengauss: { src: '/db-icons/dbx/opengauss.svg' },
  kingbase: { src: '/db-icons/dbx/kingbase.svg' },
  highgo: { src: '/db-icons/dbx/highgo.png' },
  oceanbase: { src: '/db-icons/dbx/oceanbase.svg' },
  starrocks: { src: '/db-icons/dbx/starrocks.svg' },
  tdengine: { src: '/db-icons/dbx/tdengine.svg' },
  trino: { src: '/db-icons/dbx/trino.svg' },
  vastbase: { src: '/db-icons/dbx/vastbase.svg' },
  iris: { src: '/db-icons/dbx/iris.svg' },
  sphinx: { src: '/db-icons/dbx/manticoresearch.png' },
  sqlite: { src: '/db-icons/dbx/sqlite.svg' },
  mongodb: { src: '/db-icons/dbx/mongodb.svg' },
  chroma: { src: '/db-icons/dbx/chromadb.svg' },
  qdrant: { src: '/db-icons/dbx/qdrant.svg' },
  milvus: { src: '/db-icons/dbx/milvus.png' },
  iotdb: { src: '/db-icons/dbx/iotdb.svg' },
  elasticsearch: { src: '/db-icons/dbx/elasticsearch.svg' },
  kafka: { src: '/db-icons/dbx/kafka.svg' },
  rabbitmq: { src: '/db-icons/dbx/rabbitmq.svg' },
  rocketmq: { src: '/db-icons/dbx/rocketmq.svg' },
  mqtt: { src: '/db-icons/dbx/mqtt.svg' },
  // ── GoNavi 图标补缺(dbx 没有) ──
  diros: { src: '/db-icons/diros.svg' },
}

/** 引擎图标: 有品牌 logo 用品牌图, 否则色块+桶形线条 */
export function EngineIcon({ engine, size = 16 }: { engine: string | EngineType | undefined; size?: number }) {
  const key = String(engine || '').toLowerCase()
  const color = engineColor(engine as string)
  const brand = BRAND_ASSETS[key]
  if (brand) {
    const s = size * (brand.scale || 0.86)
    return (
      <span
        className="dbx-engine-icon"
        style={{ width: size, height: size, background: brand.bg || 'transparent', borderColor: 'transparent', borderRadius: Math.max(3, size * 0.18) }}
        title={String(engine || '')}
      >
        <img src={brand.src} width={s} height={s} alt="" draggable={false} style={{ borderRadius: 2 }} />
      </span>
    )
  }
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
export function NodeIcon({ level, size = 14 }: { level: 'conn' | 'db' | 'group' | 'connGroup' | 'schema' | 'table' | 'view' | 'function' | 'procedure' | 'event' | 'trigger' | 'sequence'; size?: number }) {
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
    case 'schema':
      return (
        <svg {...common} className="dbx-node-icon icon-schema">
          <path d="m12 2 10 5-10 5L2 7l10-5z" />
          <path d="m2 12 10 5 10-5" />
          <path d="m2 17 10 5 10-5" />
        </svg>
      )
    case 'connGroup':
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
    case 'function':
    case 'procedure':
    case 'event':
    case 'trigger':
    case 'sequence':
      return (
        <svg {...common} className="dbx-node-icon icon-code">
          <polyline points="16 18 22 12 16 6" />
          <polyline points="8 6 2 12 8 18" />
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

// ActionIcon 已提升到 L2, 此处转发(6 个家族调用点零改动)
export { ActionIcon, type ActionIconKind } from '../../components/common/ActionIcon'

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
