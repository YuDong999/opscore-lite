// 数据库管理 API 客户端封装。
// 与后端 internal/dbmanager/types.go 保持一致。

import { getJSON, postJSON } from '../../api/client'

// ── 引擎类型 ──
// 关系型 (23) + 文档 (1) + 向量 (3) + 时序 (1) + 搜索 (1) + MQ (4) + 自定义 (1) = 34
export type EngineType =
  | 'mysql' | 'mariadb' | 'postgres' | 'oracle' | 'goldendb'
  | 'clickhouse' | 'sqlserver' | 'duckdb' | 'dameng' | 'gaussdb' | 'opengauss'
  | 'kingbase' | 'highgo' | 'oceanbase' | 'starrocks' | 'tdengine' | 'trino'
  | 'vastbase' | 'iris' | 'diros' | 'sphinx' | 'sqlite'
  | 'mongodb'
  | 'chroma' | 'qdrant' | 'milvus'
  | 'iotdb'
  | 'elasticsearch'
  | 'kafka' | 'rabbitmq' | 'rocketmq' | 'mqtt'
  | 'custom'

export type EngineCategory =
  | 'relational' | 'document' | 'vector' | 'timeseries' | 'search' | 'mq' | 'custom'

export type EngineStatus = 'builtin' | 'optional' | 'disabled' | 'unknown'

export interface EngineMeta {
  type: EngineType
  label: string
  short: string
  category: EngineCategory
  defaultPort: number
  defaultDb: string
  defaultUser: string
  defaultSsl: string
  hasSql: boolean
  hasSchema: boolean
  hasDatabase: boolean
  hasTable: boolean
  hasCollection: boolean
  supportsDml: boolean
  supportsDdl: boolean
  color: string
  description: string
  status: EngineStatus
  reason?: string
}

// 本地引擎清单（兜底, 后端 /engines 端点会返回更准确的运行时状态）。
export const ENGINES: EngineMeta[] = [
  // 关系型
  { type: 'mysql',        label: 'MySQL',           short: 'MySQL',    category: 'relational', hasDatabase: true, defaultPort: 3306, defaultDb: '',        defaultUser: 'root',     defaultSsl: 'preferred', hasSql: true,  hasSchema: true,  hasTable: true,  hasCollection: false, supportsDml: true,  supportsDdl: true,  color: '#3b82f6', description: '开源 OLTP, 关系型事实标准', status: 'builtin' },
  { type: 'mariadb',      label: 'MariaDB',         short: 'Maria',    category: 'relational', hasDatabase: true, defaultPort: 3306, defaultDb: '',        defaultUser: 'root',     defaultSsl: 'preferred', hasSql: true,  hasSchema: true,  hasTable: true,  hasCollection: false, supportsDml: true,  supportsDdl: true,  color: '#a855f7', description: 'MySQL 兼容分支, 开源', status: 'optional' },
  { type: 'postgres',     label: 'PostgreSQL',      short: 'PG',       category: 'relational', hasDatabase: true, defaultPort: 5432, defaultDb: 'postgres', defaultUser: 'postgres', defaultSsl: 'preferred', hasSql: true,  hasSchema: true,  hasTable: true,  hasCollection: false, supportsDml: true,  supportsDdl: true,  color: '#0ea5e9', description: '强类型 + JSONB + 高级索引', status: 'builtin' },
  { type: 'oracle',       label: 'Oracle',          short: 'Oracle',   category: 'relational', hasDatabase: true, defaultPort: 1521, defaultDb: 'ORCL',    defaultUser: 'system',   defaultSsl: 'preferred', hasSql: true,  hasSchema: true,  hasTable: true,  hasCollection: false, supportsDml: true,  supportsDdl: true,  color: '#ef4444', description: '商业关系型, PL/SQL', status: 'builtin' },
  { type: 'goldendb',     label: 'GoldenDB',        short: 'Gold',     category: 'relational', hasDatabase: true, defaultPort: 1888, defaultDb: '',        defaultUser: 'root',     defaultSsl: 'preferred', hasSql: true,  hasSchema: true,  hasTable: true,  hasCollection: false, supportsDml: true,  supportsDdl: true,  color: '#f59e0b', description: '中兴分布式, MySQL 兼容', status: 'builtin' },
  { type: 'clickhouse',   label: 'ClickHouse',      short: 'CH',       category: 'relational', hasDatabase: true, defaultPort: 9000, defaultDb: 'default', defaultUser: 'default',  defaultSsl: 'preferred', hasSql: true,  hasSchema: true,  hasTable: true,  hasCollection: false, supportsDml: true,  supportsDdl: true,  color: '#facc15', description: 'OLAP 列存, 极致分析性能', status: 'optional' },
  { type: 'sqlserver',    label: 'SQL Server',      short: 'MSSQL',    category: 'relational', hasDatabase: true, defaultPort: 1433, defaultDb: 'master',  defaultUser: 'sa',       defaultSsl: 'preferred', hasSql: true,  hasSchema: true,  hasTable: true,  hasCollection: false, supportsDml: true,  supportsDdl: true,  color: '#dc2626', description: '微软关系型, T-SQL', status: 'optional' },
  { type: 'duckdb',       label: 'DuckDB',          short: 'DuckDB',   category: 'relational', hasDatabase: true, defaultPort: 0,    defaultDb: '',        defaultUser: '',         defaultSsl: 'disable',   hasSql: true,  hasSchema: true,  hasTable: true,  hasCollection: false, supportsDml: true,  supportsDdl: true,  color: '#fde047', description: '进程内 OLAP, 文件型', status: 'optional' },
  { type: 'dameng',       label: '达梦 DM',         short: 'DM',       category: 'relational', hasDatabase: true, defaultPort: 5236, defaultDb: '',        defaultUser: 'SYSDBA',   defaultSsl: 'preferred', hasSql: true,  hasSchema: true,  hasTable: true,  hasCollection: false, supportsDml: true,  supportsDdl: true,  color: '#7c3aed', description: '国产化关系型, 信创', status: 'optional' },
  { type: 'gaussdb',      label: 'GaussDB',         short: 'Gauss',    category: 'relational', hasDatabase: true, defaultPort: 1888, defaultDb: '',        defaultUser: 'root',     defaultSsl: 'preferred', hasSql: true,  hasSchema: true,  hasTable: true,  hasCollection: false, supportsDml: true,  supportsDdl: true,  color: '#10b981', description: '华为分布式, PostgreSQL 兼容', status: 'optional' },
  { type: 'opengauss',    label: 'openGauss',       short: 'oGauss',   category: 'relational', hasDatabase: true, defaultPort: 5432, defaultDb: 'postgres', defaultUser: 'omm',     defaultSsl: 'preferred', hasSql: true,  hasSchema: true,  hasTable: true,  hasCollection: false, supportsDml: true,  supportsDdl: true,  color: '#059669', description: '华为开源, PostgreSQL 兼容', status: 'optional' },
  { type: 'kingbase',     label: 'KingbaseES',      short: 'King',     category: 'relational', hasDatabase: true, defaultPort: 54321, defaultDb: 'test',   defaultUser: 'system',   defaultSsl: 'preferred', hasSql: true,  hasSchema: true,  hasTable: true,  hasCollection: false, supportsDml: true,  supportsDdl: true,  color: '#0891b2', description: '人大金仓, PG 兼容', status: 'optional' },
  { type: 'highgo',       label: 'HighGo',          short: 'HG',       category: 'relational', hasDatabase: true, defaultPort: 5866, defaultDb: 'highgo',  defaultUser: 'highgo',   defaultSsl: 'preferred', hasSql: true,  hasSchema: true,  hasTable: true,  hasCollection: false, supportsDml: true,  supportsDdl: true,  color: '#0d9488', description: '瀚高, PG 兼容', status: 'optional' },
  { type: 'oceanbase',    label: 'OceanBase',       short: 'OB',       category: 'relational', hasDatabase: true, defaultPort: 2881, defaultDb: 'oceanbase', defaultUser: 'root',  defaultSsl: 'preferred', hasSql: true,  hasSchema: true,  hasTable: true,  hasCollection: false, supportsDml: true,  supportsDdl: true,  color: '#0ea5e9', description: '蚂蚁分布式, MySQL/Oracle 兼容', status: 'optional' },
  { type: 'starrocks',    label: 'StarRocks',       short: 'SR',       category: 'relational', hasDatabase: true, defaultPort: 9030, defaultDb: '',        defaultUser: 'root',     defaultSsl: 'preferred', hasSql: true,  hasSchema: true,  hasTable: true,  hasCollection: false, supportsDml: true,  supportsDdl: true,  color: '#0f766e', description: '极速全场景 MPP', status: 'optional' },
  { type: 'tdengine',     label: 'TDengine',        short: 'TD',       category: 'relational', hasDatabase: true, defaultPort: 6041, defaultDb: '',        defaultUser: 'root',     defaultSsl: 'preferred', hasSql: true,  hasSchema: true,  hasTable: true,  hasCollection: false, supportsDml: true,  supportsDdl: true,  color: '#dc2626', description: '时序数据库, 物联网专用', status: 'optional' },
  { type: 'trino',        label: 'Trino',           short: 'Trino',    category: 'relational', hasDatabase: true, defaultPort: 8080, defaultDb: '',        defaultUser: '',         defaultSsl: 'preferred', hasSql: true,  hasSchema: true,  hasTable: true,  hasCollection: false, supportsDml: true,  supportsDdl: false, color: '#f97316', description: '分布式 SQL 查询引擎 (前 PrestoSQL)', status: 'optional' },
  { type: 'vastbase',     label: 'Vastbase',        short: 'VB',       category: 'relational', hasDatabase: true, defaultPort: 5432, defaultDb: '',        defaultUser: 'vastbase', defaultSsl: 'preferred', hasSql: true,  hasSchema: true,  hasTable: true,  hasCollection: false, supportsDml: true,  supportsDdl: true,  color: '#9333ea', description: '海量数据, PG 兼容', status: 'optional' },
  { type: 'iris',         label: 'InterSystems IRIS', short: 'IRIS',  category: 'relational', hasDatabase: true, defaultPort: 1972, defaultDb: '',        defaultUser: '_SYSTEM',  defaultSsl: 'preferred', hasSql: true,  hasSchema: true,  hasTable: true,  hasCollection: false, supportsDml: true,  supportsDdl: true,  color: '#e11d48', description: '多模型, 医疗/金融场景', status: 'optional' },
  { type: 'diros',        label: 'Diros',           short: 'Diros',    category: 'relational', hasDatabase: true, defaultPort: 1888, defaultDb: '',        defaultUser: 'root',     defaultSsl: 'preferred', hasSql: true,  hasSchema: true,  hasTable: true,  hasCollection: false, supportsDml: true,  supportsDdl: true,  color: '#9333ea', description: '国产化数据库', status: 'optional' },
  { type: 'sphinx',       label: 'Sphinx',          short: 'Sphinx',   category: 'relational', hasDatabase: true, defaultPort: 9306, defaultDb: '',        defaultUser: '',         defaultSsl: 'disable',   hasSql: true,  hasSchema: false, hasTable: true,  hasCollection: false, supportsDml: true,  supportsDdl: true,  color: '#a3a3a3', description: '全文检索引擎', status: 'optional' },
  { type: 'sqlite',       label: 'SQLite',          short: 'SQLite',   category: 'relational', hasDatabase: true, defaultPort: 0,    defaultDb: '',        defaultUser: '',         defaultSsl: 'disable',   hasSql: true,  hasSchema: true,  hasTable: true,  hasCollection: false, supportsDml: true,  supportsDdl: true,  color: '#52525b', description: '进程内数据库, 嵌入式', status: 'optional' },
  // 文档
  { type: 'mongodb',      label: 'MongoDB',         short: 'Mongo',    category: 'document', hasDatabase: true,   defaultPort: 27017, defaultDb: 'admin',  defaultUser: '',         defaultSsl: 'preferred', hasSql: true,  hasSchema: true,  hasTable: false, hasCollection: true,  supportsDml: true,  supportsDdl: true,  color: '#10b981', description: '文档型, JSON 原生', status: 'optional' },
  // 向量
  { type: 'chroma',       label: 'Chroma',          short: 'Chroma',   category: 'vector', hasDatabase: false,     defaultPort: 8000, defaultDb: '',        defaultUser: '',         defaultSsl: 'disable',   hasSql: false, hasSchema: true,  hasTable: false, hasCollection: true,  supportsDml: false, supportsDdl: false, color: '#a78bfa', description: '向量库, RAG 友好', status: 'builtin' },
  { type: 'qdrant',       label: 'Qdrant',          short: 'Qdrant',   category: 'vector', hasDatabase: false,     defaultPort: 6333, defaultDb: '',        defaultUser: '',         defaultSsl: 'disable',   hasSql: false, hasSchema: true,  hasTable: false, hasCollection: true,  supportsDml: false, supportsDdl: false, color: '#ef4444', description: '向量库, Rust 实现, 高性能', status: 'builtin' },
  { type: 'milvus',       label: 'Milvus',          short: 'Milvus',   category: 'vector', hasDatabase: false,     defaultPort: 19530, defaultDb: '',       defaultUser: '',         defaultSsl: 'disable',   hasSql: false, hasSchema: true,  hasTable: false, hasCollection: true,  supportsDml: false, supportsDdl: false, color: '#06b6d4', description: '向量库, 大规模 AI 检索', status: 'builtin' },
  // 时序
  { type: 'iotdb',        label: 'IoTDB',           short: 'IoTDB',    category: 'timeseries', hasDatabase: true, defaultPort: 6667, defaultDb: 'root',   defaultUser: 'root',     defaultSsl: 'preferred', hasSql: true,  hasSchema: true,  hasTable: true,  hasCollection: false, supportsDml: true,  supportsDdl: true,  color: '#f59e0b', description: '时序数据库, 物联网/工业', status: 'optional' },
  // 搜索
  { type: 'elasticsearch', label: 'Elasticsearch',  short: 'ES',       category: 'search', hasDatabase: false,     defaultPort: 9200, defaultDb: '',        defaultUser: '',         defaultSsl: 'disable',   hasSql: false, hasSchema: true,  hasTable: true,  hasCollection: false, supportsDml: true,  supportsDdl: true,  color: '#10b981', description: '分布式搜索, 文档型索引', status: 'optional' },
  // MQ
  { type: 'kafka',        label: 'Kafka',           short: 'Kafka',    category: 'mq', hasDatabase: false,         defaultPort: 9092, defaultDb: '',        defaultUser: '',         defaultSsl: 'disable',   hasSql: false, hasSchema: true,  hasTable: false, hasCollection: true,  supportsDml: false, supportsDdl: false, color: '#1f2937', description: '高吞吐日志流, 消息队列', status: 'builtin' },
  { type: 'rabbitmq',     label: 'RabbitMQ',        short: 'Rabbit',   category: 'mq', hasDatabase: false,         defaultPort: 5672, defaultDb: '/',      defaultUser: 'guest',    defaultSsl: 'disable',   hasSql: false, hasSchema: true,  hasTable: false, hasCollection: true,  supportsDml: false, supportsDdl: false, color: '#f97316', description: 'AMQP 标准, 灵活路由', status: 'builtin' },
  { type: 'rocketmq',     label: 'RocketMQ',        short: 'Rocket',   category: 'mq', hasDatabase: false,         defaultPort: 9876, defaultDb: '',        defaultUser: '',         defaultSsl: 'disable',   hasSql: false, hasSchema: true,  hasTable: false, hasCollection: true,  supportsDml: false, supportsDdl: false, color: '#1d4ed8', description: '阿里开源, 金融级可靠', status: 'builtin' },
  { type: 'mqtt',         label: 'MQTT',            short: 'MQTT',     category: 'mq', hasDatabase: false,         defaultPort: 1883, defaultDb: '',        defaultUser: '',         defaultSsl: 'disable',   hasSql: false, hasSchema: true,  hasTable: false, hasCollection: true,  supportsDml: false, supportsDdl: false, color: '#8b5cf6', description: 'IoT 消息协议事实标准', status: 'builtin' },
  // 自定义
  { type: 'custom',       label: 'Custom DSN',      short: 'Custom',   category: 'custom', hasDatabase: false,     defaultPort: 0,    defaultDb: '',        defaultUser: '',         defaultSsl: 'disable',   hasSql: true,  hasSchema: true,  hasTable: true,  hasCollection: false, supportsDml: true,  supportsDdl: true,  color: '#94a3b8', description: '透传 DSN 到 GoNavi 底座', status: 'builtin' },
]

// 缓存后端运行时 engines, 启动时 fetch 一次覆盖本地默认值。
let _enginesCache: EngineMeta[] | null = null
export async function loadEngines(): Promise<EngineMeta[]> {
  if (_enginesCache) return _enginesCache
  try {
    const r = await getJSON<{ engines: EngineMeta[] }>('/api/dbmanager/engines')
    if (r.engines && r.engines.length > 0) {
      // 用后端 status/reason 覆盖本地默认值
      const byType = new Map<EngineType, EngineMeta>(ENGINES.map(e => [e.type, e]))
      for (const m of r.engines) {
        const local = byType.get(m.type)
        if (local) byType.set(m.type, { ...local, status: m.status, reason: m.reason })
      }
      _enginesCache = Array.from(byType.values())
      return _enginesCache
    }
  } catch (e) { /* 后端不可达时回退到本地 */ }
  _enginesCache = ENGINES
  return ENGINES
}

export function getEngineMeta(t: EngineType): EngineMeta | undefined {
  // 优先读后端合并后的缓存(带运行时 status), 否则回退本地清单
  return (_enginesCache ?? ENGINES).find(e => e.type === t)
}

// ── 查询结果导出 ──
// POST /api/dbmanager/export -> 后端直接流式返回文件字节, 前端触发浏览器下载。
export type ExportFormat = 'csv' | 'json' | 'xlsx'

export async function exportQuery(connId: string, sql: string, format: ExportFormat, maxRows = 10000): Promise<{ fileName: string; size: number }> {
  const token = localStorage.getItem('opscore-token')
  const r = await fetch('/api/dbmanager/export', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', ...(token ? { Authorization: `Bearer ${token}` } : {}) },
    body: JSON.stringify({ id: connId, sql, format, maxRows }),
  })
  if (!r.ok) {
    let msg = `导出失败 (HTTP ${r.status})`
    try {
      const j = await r.json()
      if (j && j.error) msg = j.error
    } catch { /* 非 JSON 响应 */ }
    throw new Error(msg)
  }
  const blob = await r.blob()
  const dispo = r.headers.get('Content-Disposition') || ''
  const m = dispo.match(/filename="?([^";]+)"?/)
  const fileName = m ? m[1] : `query_export_${Date.now()}.${format}`
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = fileName
  document.body.appendChild(a)
  a.click()
  a.remove()
  URL.revokeObjectURL(url)
  return { fileName, size: blob.size }
}

// ── 连接配置 v2 ──
export interface SSHConfig {
  enabled: boolean
  host: string
  port: number
  user: string
  authMode: 'password' | 'privateKey'
  password?: string
  keyPath?: string
}

export interface SSLConfig {
  mode: string // disable / preferred / required / verify-ca / verify-full / skip-verify
  caCert?: string
  cert?: string
  key?: string
  serverName?: string
}

export interface ProxyConfig {
  enabled: boolean
  type: 'http' | 'socks5'
  host: string
  port: number
  user?: string
  pass?: string
}

export interface ConnectionConfig {
  host: string
  port: number
  database: string
  username: string
  sslMode: string
  envTag?: string
  options?: Record<string, string>

  // v2 高级
  useSSH?: boolean
  ssh?: SSHConfig
  useProxy?: boolean
  proxy?: ProxyConfig
  ssl?: SSLConfig
  timeoutSec?: number
  queryTimeoutSec?: number
  maxRows?: number
  extraParams?: string
  driver?: string
  dsn?: string

  // 展示
  group?: string
  icon?: string
  note?: string

  // 引擎特参
  mongoReplicaSet?: string
  mongoAuthSource?: string
  mongoReadPreference?: string
  mongoSrv?: boolean
  clickHouseProtocol?: string
  oceanBaseProtocol?: string
  topology?: string
  hosts?: string
}

export const DEFAULT_CONFIG: ConnectionConfig = {
  host: '',
  port: 0,
  database: '',
  username: '',
  sslMode: 'preferred',
  timeoutSec: 15,
  queryTimeoutSec: 30,
  maxRows: 5000,
}

export function defaultConfigFor(t: EngineType): ConnectionConfig {
  const meta = getEngineMeta(t)
  if (!meta) return { ...DEFAULT_CONFIG }
  return {
    ...DEFAULT_CONFIG,
    port: meta.defaultPort,
    database: meta.defaultDb,
    username: meta.defaultUser,
    sslMode: meta.defaultSsl,
  }
}

export interface ConnectionInfo {
  id: string
  name: string
  engine: EngineType
  config: ConnectionConfig
  createdAt: number
  updatedAt: number
}

export interface TableInfo {
  name: string
  type: string
  schema?: string
  comment?: string
}

export interface ColumnInfo {
  name: string
  type: string
  nullable: boolean
  key?: string
  default?: string
  comment?: string
}

export interface IndexInfo {
  name: string
  columns: string[]
  unique: boolean
  primary: boolean
}

export interface StatementResult {
  sql: string
  type: string
  rows: number
  affected: number
  durationMs: number
  error?: string
}

export interface QueryResult {
  columns: string[]
  rows: any[][]
  rowCount: number
  affected: number
  durationMs: number
  truncated: boolean
  error?: string
  isEditable?: boolean
  statements?: StatementResult[]
}

export interface InterceptionBody {
  code?: 'write_locked' | 'confirm_required' | 'blocked'
  risk?: string
  reason?: string
}

export async function listConnections(): Promise<ConnectionInfo[]> {
  const r = await getJSON<{ connections: ConnectionInfo[] }>('/api/dbmanager/connections')
  return r.connections || []
}

export async function createConnection(
  name: string,
  engine: EngineType,
  config: ConnectionConfig,
  password: string,
): Promise<ConnectionInfo> {
  const r = await postJSON<{ ok: boolean; connection: ConnectionInfo }>(
    '/api/dbmanager/connections',
    { name, engine, config, password },
  )
  return r.connection
}

export async function updateConnection(
  id: string,
  name: string,
  config: ConnectionConfig,
  password: string,
): Promise<ConnectionInfo> {
  const t = localStorage.getItem('opscore-token')
  const r = await fetch('/api/dbmanager/connections', {
    method: 'PUT',
    headers: {
      'Content-Type': 'application/json',
      ...(t ? { Authorization: `Bearer ${t}` } : {}),
    },
    body: JSON.stringify({ id, name, config, password }),
  })
  if (!r.ok) {
    const e = await r.json().catch(() => ({ error: `HTTP ${r.status}` }))
    throw new Error(e.error || `HTTP ${r.status}`)
  }
  const data = await r.json()
  return data.connection
}

export async function deleteConnection(id: string): Promise<void> {
  const t = localStorage.getItem('opscore-token')
  const r = await fetch('/api/dbmanager/connections', {
    method: 'DELETE',
    headers: {
      'Content-Type': 'application/json',
      ...(t ? { Authorization: `Bearer ${t}` } : {}),
    },
    body: JSON.stringify({ id }),
  })
  if (!r.ok) {
    const e = await r.json().catch(() => ({ error: `HTTP ${r.status}` }))
    throw new Error(e.error || `HTTP ${r.status}`)
  }
}

export async function testConnection(
  payload:
    | { id: string }
    | { engine: EngineType; config: ConnectionConfig; password: string },
): Promise<{ ok: boolean; version?: string; error?: string }> {
  return postJSON('/api/dbmanager/connections/test', payload)
}

export async function listDatabases(id: string): Promise<string[]> {
  const r = await getJSON<{ databases: string[] }>(
    `/api/dbmanager/metadata?type=databases&id=${id}`,
  )
  return r.databases || []
}

export async function listTables(id: string, database: string): Promise<TableInfo[]> {
  const r = await getJSON<{ tables: TableInfo[] }>(
    `/api/dbmanager/metadata?type=tables&id=${id}&database=${encodeURIComponent(database)}`,
  )
  return r.tables || []
}

export async function describeTable(
  id: string,
  database: string,
  table: string,
): Promise<{ columns: ColumnInfo[]; indexes: IndexInfo[]; ddl: string; engine: string }> {
  return getJSON(
    `/api/dbmanager/describe?id=${id}&database=${encodeURIComponent(database)}&table=${encodeURIComponent(table)}`,
  )
}

export async function runQueryRaw(
  id: string,
  sql: string,
  maxRows = 5000,
  confirm = false,
  database?: string,
): Promise<{ status: number; data: QueryResult & InterceptionBody }> {
  const t = localStorage.getItem('opscore-token')
  const r = await fetch('/api/dbmanager/query', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      ...(t ? { Authorization: `Bearer ${t}` } : {}),
    },
    body: JSON.stringify({ id, sql, maxRows, confirm, database }),
  })
  const data = await r.json().catch(() => ({ error: `HTTP ${r.status}` }))
  return { status: r.status, data }
}

// ── 写解锁 / 审计 ──
export interface UnlockState {
  unlocked: boolean
  remainingSec: number
  maxMinutes: number
}

export interface AuditEntry {
  time: number
  connId: string
  connName: string
  engine: string
  sql: string
  risk: string
  decision: 'executed' | 'denied' | 'failed'
  detail?: string
}

export async function getUnlockState(id: string): Promise<UnlockState> {
  return getJSON(`/api/dbmanager/write-unlock?id=${id}`)
}

export async function unlockWrite(id: string, minutes: number): Promise<{ ok: boolean; remainingSec: number }> {
  return postJSON('/api/dbmanager/write-unlock', { id, minutes })
}

export async function lockWrite(id: string): Promise<void> {
  await postJSON('/api/dbmanager/write-lock', { id })
}

export async function getAudit(connId?: string): Promise<AuditEntry[]> {
  const r = await getJSON<{ entries: AuditEntry[] }>(
    `/api/dbmanager/audit${connId ? `?id=${connId}` : ''}`,
  )
  return r.entries || []
}

export async function getSlowSQL(id: string, limit = 20): Promise<{ engine: string; columns: string[]; rows: any[]; note?: string }> {
  return getJSON(`/api/dbmanager/slow-sql?id=${id}&limit=${limit}`)
}

export async function getTableStatus(id: string, database: string, table: string): Promise<{ engine: string; columns: string[]; rows: any[]; note?: string }> {
  return getJSON(`/api/dbmanager/table-status?id=${id}&database=${encodeURIComponent(database)}&table=${encodeURIComponent(table)}`)
}

export async function explainSQL(id: string, sql: string, format = 'json'): Promise<{ engine: string; sql: string; format: string; columns: string[]; rows: any[] }> {
  return postJSON('/api/dbmanager/explain', { id, sql, format })
}

// ── 引擎状态标签 ──
export interface DriverInfo {
  type: EngineType
  label: string
  short: string
  category: string
  color: string
  status: 'builtin' | 'optional' | 'disabled' | 'unknown'
  reason?: string
  installed: boolean
  builtin: boolean
}

export async function getDrivers(): Promise<DriverInfo[]> {
  const r = await getJSON<{ drivers: DriverInfo[] }>('/api/dbmanager/drivers')
  return r.drivers || []
}

export function statusLabel(s: EngineStatus): { text: string; cls: string } {
  switch (s) {
    case 'builtin':  return { text: '内置',  cls: 'pill-ok' }
    case 'optional': return { text: '可启用', cls: 'pill-warn' }
    case 'disabled': return { text: '需驱动', cls: 'pill-err' }
    default:         return { text: '未知',  cls: 'pill' }
  }
}

// ── 表数据分页浏览（P0 树状工作台） ──
export interface TableData {
  columns: string[]
  rows: any[][]
  total: number
  page: number
  pageSize: number
  durationMs?: number
}

export async function fetchData(
  id: string, database: string, table: string,
  page = 1, pageSize = 100, orderBy = '', orderDir: 'ASC' | 'DESC' = 'ASC', where = '',
): Promise<TableData> {
  const p = new URLSearchParams({ id, database, table, page: String(page), pageSize: String(pageSize) })
  if (orderBy) { p.set('orderBy', orderBy); p.set('orderDir', orderDir) }
  if (where) p.set('where', where)
  return getJSON<TableData>(`/api/dbmanager/data?${p.toString()}`)
}

// ── 保存的查询 ──
export interface SavedQuery {
  id: string
  name: string
  sql: string
  engine?: string
  connId?: string
  createdAt: number
  updatedAt: number
}

export async function listSavedQueries(): Promise<SavedQuery[]> {
  const r = await getJSON<{ queries: SavedQuery[] }>('/api/dbmanager/queries')
  return r.queries || []
}

export async function saveQuery(q: { name: string; sql: string; engine?: string; connId?: string }): Promise<SavedQuery> {
  return postJSON<SavedQuery>('/api/dbmanager/queries/save', q)
}

export async function deleteSavedQuery(id: string): Promise<void> {
  await fetch('/api/dbmanager/queries/delete', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ id }),
  }).then(r => { if (!r.ok) throw new Error('删除失败') })
}

// ── 右键菜单: DDL / 全表 INSERT ──
export async function fetchTableDDL(id: string, database: string, table: string): Promise<string> {
  const d = await describeTable(id, database, table)
  return d.ddl || ''
}

export async function fetchTableInserts(id: string, database: string, table: string, maxRows = 1000): Promise<{ text: string; rows: number; truncated: boolean }> {
  return getJSON(`/api/dbmanager/table-inserts?id=${id}&database=${encodeURIComponent(database)}&table=${encodeURIComponent(table)}&maxRows=${maxRows}`)
}

// ── 行内编辑: 按主键生成 UPDATE 并执行(后端走拦截链+审计) ──
export async function applyCellEdit(
  id: string, database: string, table: string,
  pkCols: string[], row: Record<string, any>,
  setCol: string, setValue: any, confirm = false,
): Promise<{ ok: boolean; affected: number; error?: string }> {
  return postJSON('/api/dbmanager/apply-edit', { id, database, table, pkCols, row, setCol, setValue, confirm })
}
