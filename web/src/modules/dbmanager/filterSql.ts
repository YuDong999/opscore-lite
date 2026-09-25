// 筛选条件的单一事实源: 操作符集合、SQL 片段、界面 chip 文案都从这里出。
// 之前 DataPanel 里 where 生成与 filterChipLabel 是两套重复的引号判定(注释还写着"要保持一致"),
// 那正是"显示的和实际执行的"开始漂移的地方。

export interface FilterCond { col: string; op: string; value: string; value2?: string }

export const FILTER_OPS = [
  '=', '!=', '>', '>=', '<', '<=',
  'LIKE', 'NOT LIKE',
  'IN', 'NOT IN',
  'BETWEEN',
  'IS NULL', 'IS NOT NULL',
] as const

export const opTakesNoValue = (op: string) => op === 'IS NULL' || op === 'IS NOT NULL'
export const opIsRange = (op: string) => op === 'BETWEEN'
export const opIsList = (op: string) => op === 'IN' || op === 'NOT IN'

// IN / NOT IN 的值: 逗号或换行分隔, 逐项去空白
export const splitListValue = (v: string) => v.split(/[,\n]/).map(s => s.trim()).filter(Boolean)

// 标识符引用: 列名可能撞保留字(order/from/user) 或含空格, 裸拼必炸。
// 只认显式白名单 —— 非 SQL 引擎(mongodb/es/kafka/向量库等)的 where 不是 SQL 语义,
// 给标识符套引号反而把它们打坏, 所以认不出的一律保持原样裸拼(与改动前行为一致)。
const BACKTICK = new Set(['mysql', 'mariadb', 'goldendb', 'oceanbase', 'starrocks', 'clickhouse', 'sphinx', 'tdengine'])
const BRACKET = new Set(['sqlserver'])
const DQUOTE = new Set(['postgres', 'oracle', 'dameng', 'gaussdb', 'opengauss', 'kingbase', 'highgo', 'vastbase', 'iris', 'diros', 'sqlite', 'duckdb', 'trino'])

export function quoteIdent(name: string, engine: string): string {
  if (!name) return name
  if (BACKTICK.has(engine)) return '`' + name.replace(/`/g, '``') + '`'
  if (BRACKET.has(engine)) return '[' + name.replace(/\]/g, ']]') + ']'
  if (DQUOTE.has(engine)) return '"' + name.replace(/"/g, '""') + '"'
  return name
}

// 展示用标识符: 不加引号, 否则 chip 上满是引号难读
const showIdent = (col: string) => col

const esc = (v: string) => v.replace(/'/g, "''")

export interface FilterSqlCtx {
  engine: string
  // 该列是否数值列(数值不加引号); 取不到类型时按字符串加引号
  isNumeric: (col: string) => boolean
}

/**
 * 生成一条条件的 SQL 片段与对应 chip 文案。
 * 返回 null 表示这条条件不进 WHERE(缺值、空列表、未填完的区间)。
 * 核心不变式: sql 与 label 由同一次判定产出, 不允许两处各算各的。
 */
export function buildFilter(f: FilterCond, ctx: FilterSqlCtx): { sql: string; label: string } | null {
  const col = f.col
  const op = f.op
  const qc = quoteIdent(col, ctx.engine)
  const num = ctx.isNumeric(col)
  const q = (s: string) => (num ? s : `'${esc(s)}'`)

  if (op === 'IS NULL') return { sql: `${qc} IS NULL`, label: `${showIdent(col)} IS NULL` }
  if (op === 'IS NOT NULL') return { sql: `${qc} IS NOT NULL`, label: `${showIdent(col)} IS NOT NULL` }

  const v = (f.value || '').trim()
  if (!v) return null

  switch (op) {
    case '=': case '!=': case '>': case '>=': case '<': case '<=':
      return { sql: `${qc} ${op} ${q(v)}`, label: `${showIdent(col)} ${op} ${v}` }
    // LIKE 恒为字符串字面量: 数值列也要加引号, 否则 MySQL Error 1064
    case 'LIKE': case 'NOT LIKE':
      return { sql: `${qc} ${op} '%${esc(v)}%'`, label: `${showIdent(col)} ${op} %${v}%` }
    case 'IN': case 'NOT IN': {
      const items = splitListValue(v)
      if (!items.length) return null
      const list = items.map(q).join(', ')
      const shown = items.length > 3 ? `${items.slice(0, 3).join(', ')} +${items.length - 3}` : items.join(', ')
      return { sql: `${qc} ${op} (${list})`, label: `${showIdent(col)} ${op} (${shown})` }
    }
    case 'BETWEEN': {
      const v2 = (f.value2 || '').trim()
      if (!v2) return null   // 区间没填完就不进 WHERE, 免得生成 BETWEEN x AND NULL
      return { sql: `${qc} BETWEEN ${q(v)} AND ${q(v2)}`, label: `${showIdent(col)} BETWEEN ${v} AND ${v2}` }
    }
    default:
      return null
  }
}

/** 多条条件合成一个 WHERE 主体(不含 WHERE 关键字), 空列表返回 '' */
export function buildWhere(filters: FilterCond[], joiner: 'AND' | 'OR', ctx: FilterSqlCtx): string {
  return filters.map(f => buildFilter(f, ctx)?.sql ?? '').filter(Boolean).join(` ${joiner} `)
}
