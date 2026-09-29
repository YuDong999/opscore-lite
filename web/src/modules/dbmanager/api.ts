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
  | 'redis'
  | 'custom'

export type EngineCategory =
  | 'relational' | 'document' | 'vector' | 'timeseries' | 'search' | 'mq' | 'keyvalue' | 'custom'

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
  { type: 'redis',        label: 'Redis',           short: 'Redis',    category: 'keyvalue', hasDatabase: true,  defaultPort: 6379, defaultDb: '',        defaultUser: '',         defaultSsl: 'disable',   hasSql: false, hasSchema: false, hasTable: false, hasCollection: true,  supportsDml: true,  supportsDdl: false, color: '#dc382c', description: '键值存储, 键空间 + 六类值 + TTL', status: 'builtin' },
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
import type { ExportFormat, QueryResult, ColumnInfo } from '../../components/common/DataGrid'
export type { ExportFormat, QueryResult, ColumnInfo } from '../../components/common/DataGrid'

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

export interface InterceptionBody {
  code?: 'write_locked' | 'confirm_required' | 'blocked'
  risk?: string
  reason?: string
}

export async function getEngineConfig(engine: EngineType): Promise<{ engine: string; config: ConnectionConfig }> {
  return getJSON('/api/dbmanager/engine-config?engine=' + encodeURIComponent(engine))
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

// 列出连接引擎的命名空间(模式)。三级命名引擎(PG 族)返回模式列表,
// 其余引擎返回空数组 —— 空=无模式层级, 前端据此动态显隐模式下拉(能力驱动)。
export async function listSchemas(id: string): Promise<string[]> {
  const r = await getJSON<{ schemas: string[] }>(`/api/dbmanager/schemas?id=${id}`)
  return r.schemas || []
}

// 整库各表行数估算(信息库统计, 与 dbx/gonavi 树徽标一致); key 与 listTables 一致(PG=限定名)
export async function getTableCounts(id: string, database: string): Promise<Record<string, number>> {
  const r = await getJSON<{ counts: Record<string, number> }>(
    `/api/dbmanager/table-counts?id=${id}&database=${encodeURIComponent(database)}`,
  )
  return r.counts || {}
}

export async function listTables(id: string, database: string): Promise<TableInfo[]> {
  const r = await getJSON<{ tables: TableInfo[] }>(
    `/api/dbmanager/metadata?type=tables&id=${id}&database=${encodeURIComponent(database)}`,
  )
  return r.tables || []
}

// ── 对象级枚举(视图/函数/存储过程/事件/触发器/序列) ──
export type DbObjectKind =
  | 'VIEW' | 'MATERIALIZED VIEW'
  | 'FUNCTION' | 'PROCEDURE'
  | 'EVENT' | 'TRIGGER' | 'SEQUENCE'

export interface DbObject {
  name: string
  kind: DbObjectKind
  table?: string // 触发器所属表(可选, 部分引擎提供)
}

export async function listObjects(id: string, database: string): Promise<DbObject[]> {
  const r = await getJSON<{ objects: DbObject[] }>(
    `/api/dbmanager/metadata?type=objects&id=${id}&database=${encodeURIComponent(database)}`,
  )
  return r.objects || []
}

export async function getObjectDefinition(
  id: string,
  database: string,
  object: string,
  kind: DbObjectKind,
): Promise<string> {
  const r = await getJSON<{ ddl: string }>(
    `/api/dbmanager/metadata?type=object-ddl&id=${id}&database=${encodeURIComponent(database)}&object=${encodeURIComponent(object)}&kind=${kind}`,
  )
  return r.ddl || ''
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

export interface TableMeta {
  columns: ColumnInfo[]
  indexes: IndexInfo[]
  foreignKeys: Array<{ name: string; column: string; refTable: string; refColumn: string; constraint: string }>
  triggers: Array<{ name: string; timing: string; event: string; statement: string }>
  ddl: string
}

// 完整表信息(列/索引/外键/触发器/DDL), 表信息抽屉页签数据源
export async function getTableMeta(id: string, database: string, table: string): Promise<TableMeta> {
  const r = await getJSON<{ meta: TableMeta }>(
    `/api/dbmanager/table-meta?id=${id}&database=${encodeURIComponent(database)}&table=${encodeURIComponent(table)}`,
  )
  return r.meta
}

// ── 手动事务(编辑器事务模式) ──
// 一条物理连接上的事务跨请求挂着: begin 是懒的(首次执行才开), 空闲 5 分钟服务端自动回滚。
// 语义与 dbx 一致, 但每条语句仍要过风险判定/写锁, 并且 MySQL 系的 DDL 会被拒绝
// (DDL 隐式提交, 放进"待会儿能回滚"的事务里是骗人的)。
export interface TxStatement {
  seq: number
  sql: string
  type: string
  affected: number
  rows: number
  durationMs: number
  error?: string
}
export interface TxResp {
  status: number
  data: {
    ok?: boolean
    txId?: string
    statement?: string  // mq/publish 回的是"将要/已经执行的那句"(PRODUCE ...), 用于提示与确认预览
    receivers?: number  // redis/publish 回的是"几个订阅者收到了"(0 是有意义的信息, 不是失败)
    active?: boolean
    count?: number
    affected?: number
    statements?: TxStatement[]
    batch?: TxStatement[]
    idleSecLeft?: number
    openSec?: number
    idleTimeoutSec?: number
    finished?: boolean
    busy?: boolean
    columns?: string[]
    rows?: any[][]
    rowCount?: number
    truncated?: boolean
    rolledBack?: boolean
    discarded?: number
    noop?: boolean
    note?: string
    message?: string
    error?: string
    code?: string
    reason?: string
    risk?: string
  }
}

async function txPost(path: string, body: Record<string, unknown>): Promise<TxResp> {
  const t = localStorage.getItem('opscore-token')
  const r = await fetch('/api/dbmanager/' + path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', ...(t ? { Authorization: `Bearer ${t}` } : {}) },
    body: JSON.stringify(body),
  })
  return { status: r.status, data: await r.json().catch(() => ({})) }
}

export const txBegin = (id: string, database: string) => txPost('tx/begin', { id, database })
export const txExecute = (txId: string, sql: string, confirm = false, params?: SQLParam[]) =>
  txPost('tx/execute', { txId, sql, confirm, params: params?.length ? params : undefined })
export const txCommit = (txId: string) => txPost('tx/commit', { txId, confirm: true })
export const txRollback = (txId: string) => txPost('tx/rollback', { txId })

export async function txStatus(txId: string): Promise<TxResp['data'] & { status: number }> {
  const r = await fetch('/api/dbmanager/tx/status?txId=' + encodeURIComponent(txId))
  const d = await r.json().catch(() => ({}))
  return { ...(d as object), status: r.status }
}

// ── 消息队列管理(MQ) ──
// 能力清单由后端给(mq_handler.go 里那张表), 面板按它显隐 —— 前端不再自己维护一份引擎差异。
export interface MqView {
  key: string
  label: string
  list: string
  detail?: string
  peek?: string
  publishable: boolean
  note?: string
  cols?: { key: string; label: string }[]
}
export interface MqField {
  name: string
  label: string
  kind: 'text' | 'int' | 'bool'
  hint?: string
  min?: number
  max?: number
  default?: any
}
export interface MqCapability { engine: string; views: MqView[]; fields: MqField[]; note?: string }

export async function mqCapabilities(id: string): Promise<{ ok?: boolean; capability?: MqCapability; error?: string }> {
  const t = localStorage.getItem('opscore-token')
  const r = await fetch('/api/dbmanager/mq/capabilities?id=' + encodeURIComponent(id), {
    headers: t ? { Authorization: `Bearer ${t}` } : {},
  })
  return r.json().catch(() => ({ error: `HTTP ${r.status}` }))
}

// 发送消息: 只发表达意图, 协议 JSON 由后端按引擎拼(与 apply-* 同一规矩)
export const mqPublish = (body: Record<string, unknown>) => txPost('mq/publish', body)

// ── Redis 面板: 能力清单 + 键的写操作 ──
// 写操作只发**意图**(键名/值/ttl), 命令由后端用 go-redis 的类型化调用发出 ——
// 前端拼 `SET k <value>` 就得自己处理转义, 少转一个引号就是一条注入。
export interface RedisCapability {
  engine: string
  valueKinds: string[]
  ops: string[]
  note?: string
  /** single | sentinel | cluster —— 面板据此说明"多库/频道"的差别, 而不是点了才报错 */
  topology?: string
  /** Cluster 只有 db0: false 时要把多库入口收起并说明原因 */
  multiDb?: boolean
  pubsub?: boolean
}

export async function redisCapabilities(id: string): Promise<{ ok?: boolean; capability?: RedisCapability; error?: string }> {
  const t = localStorage.getItem('opscore-token')
  const r = await fetch('/api/dbmanager/redis/capabilities?id=' + encodeURIComponent(id), {
    headers: t ? { Authorization: `Bearer ${t}` } : {},
  })
  return r.json().catch(() => ({ error: `HTTP ${r.status}` }))
}

export interface RedisWriteBody {
  op: 'set' | 'expire' | 'del' | 'rename'
  id: string
  database: string
  key: string
  toKey?: string
  value?: string
  valueBase64?: boolean
  ttlSeconds?: number
  confirm: boolean
}

export const redisWrite = (body: RedisWriteBody) => txPost('redis/write', body as unknown as Record<string, unknown>)

// ── Redis Pub/Sub ──
// 快照(PUBSUB CHANNELS/NUMSUB/NUMPAT)是**纯只读**, 挂在 GET 上;
// "实时看一会儿"要真的订阅, 有限时长、到期自动退订 —— 控制台不留常驻订阅。
export interface RedisPubSubChannel {
  name: string
  kind: 'channel' | 'pattern' | 'shard'
  subscribers: number
}
export interface RedisPubSubSnapshot {
  channels: RedisPubSubChannel[]
  numPat: number
  shardSupported: boolean
  note?: string
}
export interface RedisPubSubMessage {
  channel: string
  pattern?: string
  payload: string
  payloadB64?: boolean
  at: number
}
export interface RedisPubSubResult {
  messages: RedisPubSubMessage[]
  seconds: number
  truncated: boolean
  /** 实际盯了多久(ms) —— 比窗口短很多就说明是提前退出, 不是"看完了这段时间" */
  elapsedMs?: number
  /** 重订阅了几段: 长窗口按 5 秒一段, 段首要重新订阅(落在那几百毫秒里的消息会漏) */
  slices?: number
  /** 订阅中途断了: 这时 messages=0 的含义是"收不到了", 不是"没有消息" */
  interrupted?: boolean
  note?: string
}

export async function redisPubSubSnapshot(
  id: string, database: string,
): Promise<{ ok?: boolean; snapshot?: RedisPubSubSnapshot; error?: string }> {
  const t = localStorage.getItem('opscore-token')
  const r = await fetch(`/api/dbmanager/redis/pubsub?id=${encodeURIComponent(id)}&database=${encodeURIComponent(database)}`, {
    headers: t ? { Authorization: `Bearer ${t}` } : {},
  })
  return r.json().catch(() => ({ error: `HTTP ${r.status}` }))
}

export const redisPubSubDrain = (body: {
  id: string; database: string; channels: string[]; patterns: string[]; seconds: number
}) => txPost('redis/pubsub', body as unknown as Record<string, unknown>)

// PUBLISH 是写: 会推给所有订阅者(可能触发别人的业务), 所以与 SET/DEL 走同一条护栏链。
export const redisPublish = (body: {
  id: string; database: string; channel: string; payload: string; confirm: boolean
}) => txPost('redis/publish', body as unknown as Record<string, unknown>)

export async function runQueryRaw(
  id: string,
  sql: string,
  maxRows = 5000,
  confirm = false,
  database?: string,
  params?: SQLParam[],
): Promise<{ status: number; data: QueryResult & InterceptionBody }> {
  const t = localStorage.getItem('opscore-token')
  const r = await fetch('/api/dbmanager/query', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      ...(t ? { Authorization: `Bearer ${t}` } : {}),
    },
    body: JSON.stringify({ id, sql, maxRows, confirm, database, params: params?.length ? params : undefined }),
  })
  const data = await r.json().catch(() => ({ error: `HTTP ${r.status}` }))
  return { status: r.status, data }
}

// ── P1-7 数据库备份 ──
// 三个端点对应三个语义完全不同的动作: **先探路 → 再跑 → 查历史**。
// "先探路"是刻意的: 备份是"你以为完整、其实缺东西"会出大事的操作, 让人跑之前就看清
// 这份备份将包含/不包含什么(backupPlan 的 includes/excludes), 而不是跑完才发现。
export type BackupMode = 'native' | 'builtin' | 'native-unavailable'

export interface BackupRecord {
  id: string
  connId: string
  connName: string
  engine: string
  database: string
  mode: BackupMode
  tool?: string
  consistency?: string
  hostId?: string
  filePath: string
  sizeBytes: number
  tables?: string[]
  /** 这份备份**包含**什么 */
  includes: string[]
  /** 这份备份**不含**什么(空 = 完整) */
  excludes: string[]
  durationMs: number
  startedAt: number
  status: 'ok' | 'failed'
  error?: string
}

export interface BackupPlan {
  engine: string
  database: string
  hostId?: string
  mode: BackupMode
  tool: string
  dir: string
  keep: number
  includes: string[]
  excludes: string[]
  consistency: string
  nativeCandidates: string[] | null
}

export interface BackupRequest {
  connId: string
  database: string
  tables?: string[]
  hostId?: string
  dir?: string
  keep?: number
  /** 显式要求走内置导出(不依赖探测结果) */
  forceBuiltin?: boolean
}

/** 探路: 不执行任何备份, 只回答"如果现在跑, 会走哪条路径、包含/不含什么、写到哪"。 */
export async function backupPlan(req: BackupRequest): Promise<{ plan?: BackupPlan; error?: string }> {
  return postJSON('/api/dbmanager/backup/plan', req)
}

export async function backupRun(req: BackupRequest): Promise<{
  ok?: boolean; record?: BackupRecord; pruned?: BackupRecord[]; error?: string
}> {
  return postJSON('/api/dbmanager/backup/run', req)
}

export async function backupList(connId?: string): Promise<{
  records: BackupRecord[]; defaultDir: string; defaultKeep: number
}> {
  const q = connId ? `?id=${encodeURIComponent(connId)}` : ''
  const r = await getJSON<{ records: BackupRecord[]; defaultDir: string; defaultKeep: number }>(
    `/api/dbmanager/backup/list${q}`)
  return { records: r.records || [], defaultDir: r.defaultDir || '', defaultKeep: r.defaultKeep || 5 }
}

// ── P1-13 单元格分片读 / 下载 ──
// 定位方式是**按主键重查**(与 apply-edit 同一口径), 不是"记住结果集第几行" ——
// 结果集可能带 ORDER BY 或来自多表拼接, 只有主键能可靠指回某一行。
import type { CellChunk } from '../../components/common/DataGrid'
export type { CellChunk }

export interface CellReadBody {
  id: string
  database: string
  table: string
  schema?: string
  column: string
  pkCols: string[]
  row: Record<string, any>
  offset: number
  limit: number
  asBase64: boolean
}

// 返回体形状与 DataGrid 的 CellChunk 对齐(后端在失败时会只给 ok/error)。
export async function readCell(id: string, body: Omit<CellReadBody, 'id'>): Promise<CellChunk> {
  const r = await postJSON<Partial<CellChunk>>('/api/dbmanager/cell/read', { id, ...body })
  return {
    ok: !!r.ok,
    offset: r.offset ?? body.offset,
    bytes: r.bytes ?? 0,
    total: r.total ?? -1,
    nextOffset: r.nextOffset ?? -1,
    encoding: r.encoding,
    body: r.body,
    text: r.text,
    error: r.error,
  }
}

// 下载: 后端流式返回, 这里把响应体接成 Blob 再触发浏览器下载。
export async function downloadCell(id: string, body: Omit<CellReadBody, 'id' | 'offset' | 'limit' | 'asBase64'>): Promise<void> {
  const t = localStorage.getItem('opscore-token')
  const r = await fetch('/api/dbmanager/cell/download', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', ...(t ? { Authorization: `Bearer ${t}` } : {}) },
    body: JSON.stringify({ id, ...body }),
  })
  if (!r.ok) {
    // 失败时后端回的是 JSON 错误(不是文件)
    const msg = await r.json().catch(() => ({ error: `HTTP ${r.status}` }))
    throw new Error((msg as any).error || `HTTP ${r.status}`)
  }
  const blob = await r.blob()
  const dispo = r.headers.get('Content-Disposition') || ''
  const m = /filename="([^"]+)"/.exec(dispo)
  const name = m ? m[1] : `${body.column || 'cell'}.bin`
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = name
  document.body.appendChild(a)
  a.click()
  a.remove()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}

// ── P1-9 参数化查询 ──
// 前端只发参数值; 占位符替换在服务端按方言做(类型感知转义)。**不是真 prepared** ——
// 底座执行接口没有 args 形参, 这点在面板上也要说清。
export interface SQLParam {
  name: string
  type: 'string' | 'number' | 'bool' | 'null' | 'raw'
  value: string
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

export async function getUnlockState(id: string): Promise<UnlockState> {  return getJSON(`/api/dbmanager/write-unlock?id=${id}`)
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

export interface FkGraph {
  tables: string[]
  edges: Array<{ fromTable: string; fromColumn: string; toTable: string; toColumn: string }>
}

// 库级表+外键关系(ER 图数据源)
export async function getFkGraph(id: string, database: string): Promise<FkGraph> {
  const r = await getJSON<FkGraph>(
    `/api/dbmanager/fk-graph?id=${id}&database=${encodeURIComponent(database)}`,
  )
  return r
}

export interface TableOverviewRow {
  name: string; rows: number; dataSize: number; indexSize: number
  engine: string; comment: string; createdAt: string; updatedAt: string
}

// 库级表概览(行数/大小/引擎/注释/时间, GoNavi 同款字段)
export async function getTableOverview(id: string, database: string): Promise<TableOverviewRow[]> {
  const r = await getJSON<{ overview: TableOverviewRow[] }>(
    `/api/dbmanager/table-overview?id=${id}&database=${encodeURIComponent(database)}`,
  )
  return r.overview || []
}

// CSV 导入(首行=列名, 事务内批量 INSERT, 任一行失败整体回滚)
export async function importTableCsv(id: string, database: string, table: string, csv: string): Promise<{ imported: number }> {
  return postJSON('/api/dbmanager/table-import', { id, database, table, csv })
}

// 安装启用可选驱动(写 installed.json 标记; 驱动实现已随主二进制编译)
export async function installDriver(type: string): Promise<{ ok: boolean; marker: string }> {
  return postJSON('/api/dbmanager/drivers/install', { type })
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
  // P1-10 "服务端 SQL 仓库"的目录路径(用 / 分隔, 如 "运维/K8s"), 空 = 根目录。
  // 目录不是独立实体: 树由每条查询的 folder 前缀推导 —— 所以改名/删除目录是批量操作。
  folder?: string
  createdAt: number
  updatedAt: number
}

export async function listSavedQueries(): Promise<SavedQuery[]> {
  const r = await getJSON<{ queries: SavedQuery[] }>('/api/dbmanager/queries')
  return r.queries || []
}

export async function saveQuery(q: { name: string; sql: string; engine?: string; connId?: string; folder?: string }): Promise<SavedQuery> {
  return postJSON<SavedQuery>('/api/dbmanager/queries/save', q)
}

// 目录改名/移动(含子目录)。目录是推导出来的, 所以这是**批量改查询的 folder**。
export async function renameQueryFolder(from: string, to: string): Promise<{ ok: boolean; affected?: number; error?: string }> {
  return postJSON('/api/dbmanager/queries/folder/rename', { from, to })
}

// 删除目录(含其下全部查询)。confirm=false 先拿"会删几条", 让确认弹窗有信息量。
export async function deleteQueryFolder(path: string, confirm = false): Promise<{
  ok: boolean; affected?: number; needsConfirm?: boolean; error?: string
}> {
  return postJSON('/api/dbmanager/queries/folder/delete', { path, confirm })
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
): Promise<{ ok: boolean; affected: number; error?: string; sql?: string; needsConfirm?: boolean }> {
  return postJSON('/api/dbmanager/apply-edit', { id, database, table, pkCols, row, setCol, setValue, confirm })
}

// ── 行删除: 按主键生成 DELETE 并执行(与 applyCellEdit 同一条链, 只是没有 SET 子句) ──
export async function applyRowDelete(
  id: string, database: string, table: string,
  pkCols: string[], row: Record<string, any>, confirm = false,
): Promise<{ ok: boolean; affected: number; error?: string; sql?: string; needsConfirm?: boolean }> {
  return postJSON('/api/dbmanager/apply-delete', { id, database, table, pkCols, row, confirm })
}

// ── 表级破坏性 DDL: 删表 / 清空表(后端按方言引用标识符 + 走写安全链与审计) ──
// 别再用 runQueryRaw 发 DDL: /query 的二次确认看的是请求体里的 confirm, 弹窗确认了不传就等于没确认。
export async function applyTableDDL(
  id: string, database: string, table: string,
  action: 'drop' | 'truncate', confirm = false,
): Promise<{ ok: boolean; affected: number; error?: string; sql?: string; needsConfirm?: boolean }> {
  return postJSON('/api/dbmanager/apply-ddl', { id, database, table, action, confirm })
}

// 批量写(网格"待提交变更"保存): 一个事务里提交多格/多行/新增行, 任一条失败整体回滚。
// 前端只报"哪行列改成什么 / 删哪行 / 新增哪些列", 语句与事务由后端拼(apply-batch)。
export async function applyBatch(
  id: string, database: string, table: string, pkCols: string[],
  ops: Array<{
    kind: 'update' | 'delete' | 'insert'
    row?: Record<string, any>
    setCol?: string
    setValue?: any
    values?: Record<string, any>
  }>,
  confirm = false,
): Promise<{ ok: boolean; affected?: number; count?: number; error?: string; sqls?: string[]; failedAt?: number; rolledBack?: boolean }> {
  return postJSON('/api/dbmanager/apply-batch', { id, database, table, pkCols, ops, confirm })
}

// 列级结构变更(表结构编辑器): 前端只说"哪一列变成什么样", ALTER 语句由后端按方言拼。
// 与 apply-batch 同一套契约 —— confirm=false 拿预览(不落库), true 才在一个事务里执行。
export interface AlterCol {
  kind: 'add' | 'drop' | 'modify'
  name: string
  origName?: string
  type?: string
  nullable: boolean
  default?: string
  comment?: string
}
export async function applyAlter(
  id: string, database: string, table: string, cols: AlterCol[], confirm = false,
): Promise<{ ok: boolean; sqls?: string[]; error?: string; count?: number; failedAt?: number; rolledBack?: boolean }> {
  return postJSON('/api/dbmanager/apply-alter', { id, database, table, cols, confirm })
}

// ── 结构对比(schema diff) ──────────────────────────────────────────────────
// 与 apply-alter 同一套纪律: 前端只发"比哪两侧 + 勾选了哪几条变更(key)", 永不发 SQL。
// 后端执行前会**重算**差异, 选中的 key 若已不存在差异就报 stale —— 看到什么就等于执行什么。
export interface SchemaDiffEnd { id: string; database: string; schema?: string }
export interface SchemaChange {
  key: string
  kind: string
  object: string
  detail: string[]
  sqls: string[]
  destructive: boolean
}
export interface SchemaTableDiff {
  table: string
  action: 'create' | 'modify' | 'delete' | 'none'
  changes: SchemaChange[]
  sqls: string[]
  notes: string[]
}
export interface SchemaDiffOptions { indexes?: boolean; foreignKeys?: boolean; comments?: boolean }

export async function schemaDiff(
  src: SchemaDiffEnd, dst: SchemaDiffEnd, tables: string[], options: SchemaDiffOptions,
): Promise<{ ok: boolean; tables?: SchemaTableDiff[]; notes?: string[]; summary?: Record<string, number>; error?: string }> {
  return postJSON('/api/dbmanager/schema-diff', { src, dst, tables, options })
}

export async function schemaDiffApply(
  src: SchemaDiffEnd, dst: SchemaDiffEnd, tables: string[], options: SchemaDiffOptions, keys: string[],
  // sqls 非空 = "用户在预览里改过语句", 后端以文本为准(仍然过风险链/写锁/审计)。空 = 按 key 重算取语句。
  sqls?: string[],
): Promise<{ ok: boolean; count?: number; affected?: number; error?: string; stale?: boolean; unmatched?: string[]; noop?: boolean; message?: string }> {
  return postJSON('/api/dbmanager/schema-diff/apply', { src, dst, tables, options, keys, sqls, confirm: true })
}

// ── 数据对比(行级) ──
// 比对是**任务制**的: 大表要两遍扫描, 一次请求挂不住。前端拿 jobId 轮询进度,
// 结果里的 rows 供勾选, apply 只回传**键** —— 语句由后端按此刻读到的行重新生成。
export interface DataDiffEnd { id: string; database: string; schema?: string }
export interface DataDiffOptions { batchRows?: number; maxRows?: number; maxDiffs?: number }
export interface DataCellDiff { column: string; src: string; dst: string; differs?: boolean; isKey?: boolean }
export interface DataRowDiff {
  id: string
  kind: 'insert' | 'update' | 'delete'
  key: DataCellDiff[]
  cells: DataCellDiff[]
  sqls: string[]
}
export interface DataTableDiff {
  table: string
  status: 'same' | 'different' | 'partial' | 'error'
  error?: string
  keyColumns: string[]
  keySource?: string
  columns: string[]
  skipped: string[]
  srcRows: number
  dstRows: number
  scannedSrc: number
  scannedDst: number
  summary: { inserts: number; updates: number; deletes: number }
  rows: DataRowDiff[]
  notes: string[]
  partial: boolean
  reason?: string
}
export interface DataDiffJob {
  ok: boolean
  error?: string
  expired?: boolean
  id?: string
  status?: string
  current?: string
  tables?: number
  doneTables?: number
  scannedSrc?: number
  scannedDst?: number
  diffs?: number
  results?: DataTableDiff[]
}

export async function dataDiffStart(
  src: DataDiffEnd, dst: DataDiffEnd, tables: string[], keyColumns: string[],
  ignoreColumns: string[], options: DataDiffOptions,
): Promise<{ ok: boolean; jobId?: string; error?: string }> {
  return postJSON('/api/dbmanager/data-diff', { src, dst, tables, keyColumns, ignoreColumns, options })
}

export async function fetchDataDiffJob(id: string): Promise<DataDiffJob> {
  return getJSON(`/api/dbmanager/data-diff/job?id=${encodeURIComponent(id)}`)
}

export async function dataDiffCancel(id: string): Promise<{ ok: boolean; error?: string }> {
  return postJSON('/api/dbmanager/data-diff/cancel', { id })
}

export async function dataDiffApply(
  src: DataDiffEnd, dst: DataDiffEnd, table: string, keyColumns: string[],
  rows: { key: { column: string; value: string }[] }[],
): Promise<{ ok: boolean; count?: number; affected?: number; stale?: number; staleKeys?: string[]; error?: string; noop?: boolean; message?: string }> {
  return postJSON('/api/dbmanager/data-diff/apply', { src, dst, table, keyColumns, rows, confirm: true })
}

// 终止会话(进程列表): 只报 pid, 语句由后端按引擎生成。confirm=false 先拿预览。
export async function killSession(
  id: string, pid: number, confirm = false,
): Promise<{ ok: boolean; affected?: number; error?: string; sql?: string; needsConfirm?: boolean }> {
  return postJSON('/api/dbmanager/kill-session', { id, pid, confirm })
}

// ── 生成值: 表内最大值+1 / 本机派生的雪花 workerId ──
// next 必须是字符串: 19 位雪花 ID 过一遍 JSON number 会被 double 截掉精度
export async function fetchNextId(id: string, database: string, table: string, column: string): Promise<string> {
  const r = await postJSON<{ ok: boolean; next?: string; error?: string }>('/api/dbmanager/next-id', { id, database, table, column })
  if (!r.ok) throw new Error(r.error || '取号失败')
  return r.next || ''
}

let idWorkerCache: number | null = null
export async function fetchIdWorker(): Promise<number> {
  if (idWorkerCache !== null) return idWorkerCache
  const r = await getJSON<{ workerId: number }>('/api/dbmanager/id-worker')
  idWorkerCache = r.workerId
  return idWorkerCache
}
