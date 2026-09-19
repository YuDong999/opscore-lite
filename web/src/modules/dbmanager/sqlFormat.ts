// SQL 格式化: sql-formatter, 引擎→方言映射与 GoNavi 前端保持一致。
// 参考 ref/GoNavi/frontend/src/utils/ddlFormat.ts 的 resolveDdlFormatterLanguage。
import { format, type SqlLanguage } from 'sql-formatter'
import type { EngineType } from './api'

export function resolveFormatterLanguage(engine: string | undefined): SqlLanguage {
  const e = String(engine || '').trim().toLowerCase()
  switch (e) {
    case 'postgres':
    case 'kingbase':
    case 'highgo':
    case 'opengauss':
    case 'gaussdb':
    case 'vastbase':
      return 'postgresql'
    case 'mariadb':
      return 'mariadb'
    case 'mysql':
    case 'goldendb':
    case 'sphinx':
    case 'oceanbase':
      return 'mysql'
    case 'sqlserver':
      return 'transactsql'
    case 'clickhouse':
      return 'clickhouse'
    case 'duckdb':
      return 'duckdb'
    case 'sqlite':
      return 'sqlite'
    case 'oracle':
    case 'dameng':
      return 'plsql'
    default:
      return 'sql'
  }
}

export function formatSQL(sqlText: string, engine: string | undefined): string {
  const trimmed = sqlText.trim()
  if (!trimmed) return sqlText
  try {
    return format(trimmed, {
      language: resolveFormatterLanguage(engine),
      tabWidth: 2,
      keywordCase: 'upper',
    })
  } catch {
    // 方言解析失败时回退标准 SQL
    try {
      return format(trimmed, { tabWidth: 2, keywordCase: 'upper' })
    } catch {
      return sqlText // 无法解析(如多条 DDL 混合), 原样返回
    }
  }
}
