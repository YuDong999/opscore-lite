// 列类型 → 网格视觉类别的归一化器。
// 逐条移植自 dbx `apps/desktop/src/lib/dataGrid/dataGridColumnType.ts`, 目的是让单元格着色/数字右对齐
// 与 dbx 在同一列上判出同一类, 而不是各写一套"看起来差不多"的规则。
// 未搬的两条需要驱动厂商信息(sqlserver 的 timestamp/rowversion→binary、postgres 的 bit→binary),
// 本网格拿不到厂商, 这两类各自落到默认分支(timestamp→temporal, bit→boolean)。

export type TypeVisualKind =
  | 'integer' | 'numeric' | 'string' | 'boolean' | 'temporal'
  | 'structured' | 'identifier' | 'binary' | 'spatial' | 'unknown'

const has = (set: Set<string>, base: string) => set.has(base)

const INTEGER_BASES = new Set(['tinyint', 'smallint', 'mediumint', 'int', 'integer', 'bigint', 'serial', 'smallserial', 'bigserial', 'int1', 'int2', 'int4', 'int8', 'int16', 'int32', 'int64', 'int128', 'int256', 'intn', 'uint', 'uint8', 'uint16', 'uint32', 'uint64', 'uint128', 'uint256', 'year'])

// dbx 把"数值对齐"与"数值着色"分开: 对齐看 numeric 全集(含浮点/定点/money), 着色先判整数。
const NUMERIC_BASES = new Set([...INTEGER_BASES, 'float', 'float4', 'float8', 'float16', 'float32', 'float64', 'floatn', 'real', 'double', 'decimal', 'decimal32', 'decimal64', 'decimal128', 'decimal256', 'decimaln', 'numeric', 'numericn', 'number', 'dec', 'fixed', 'money', 'money4', 'moneyn', 'smallmoney', 'smallmoneyn', 'binary_float', 'binary_double'])

const STRING_BASES = new Set(['varchar', 'varchar2', 'nvarchar', 'nvarchar2', 'text', 'char', 'nchar', 'ntext', 'string', 'fixedstring', 'tinytext', 'mediumtext', 'longtext', 'clob', 'nclob', 'long', 'enum', 'enum8', 'enum16', 'set', 'character', 'character varying', 'national character', 'national character varying'])
const BOOLEAN_BASES = new Set(['bool', 'boolean', 'bit'])
const TEMPORAL_BASES = new Set(['date', 'date32', 'daten', 'time', 'time64', 'timen', 'timetz', 'datetime', 'datetime2', 'datetime4', 'datetime64', 'datetimen', 'datetimeoffset', 'datetimeoffsetn', 'smalldatetime', 'timestamp', 'timestampdty', 'timestamptz', 'interval'])
const STRUCTURED_BASES = new Set(['json', 'jsonb', 'jsonpath', 'xml', 'xmltype', 'array', 'map', 'tuple', 'struct', 'row', 'object', 'document', 'variant'])
const IDENTIFIER_BASES = new Set(['uuid', 'uniqueidentifier', 'rowid', 'urowid'])
const BINARY_BASES = new Set(['bytea', 'blob', 'tinyblob', 'mediumblob', 'longblob', 'binary', 'varbinary', 'image', 'raw', 'long raw', 'bfile'])
const SPATIAL_BASES = new Set(['geometry', 'geography', 'sdo_geometry', 'point', 'linestring', 'polygon', 'multipoint', 'multilinestring', 'multipolygon', 'geometrycollection'])

// Nullable(LowCardinality(String)) / Array(T(String)) / _int4 这类包装要剥到底层类型再判
function unwrap(dataType: string): { normalized: string; array: boolean } {
  let normalized = dataType.trim().toLowerCase().replace(/\s+/g, ' ')
  let array = false
  while (normalized) {
    if (/\[\s*\]$/.test(normalized)) {
      array = true
      normalized = normalized.replace(/(?:\[\s*\])+$/, '').trim()
      continue
    }
    const m = normalized.match(/^([a-z][a-z0-9_]*)\s*\((.*)\)$/s)
    if (!m) break
    if (m[1] === 'array') {
      array = true
      normalized = (m[2] || '').trim()
      continue
    }
    if (m[1] !== 'nullable' && m[1] !== 'lowcardinality') break
    normalized = (m[2] || '').trim()
  }
  if (normalized.length > 1 && normalized.startsWith('_')) array = true // PG 目录里数组类型名以 _ 开头
  return { normalized, array }
}

function typeBase(dataType: string): { base: string; array: boolean } {
  const { normalized, array } = unwrap(dataType)
  let base = normalized.replace(/\s+unsigned\b/g, '').trim()
  const paren = base.indexOf('(')
  if (paren >= 0) base = base.slice(0, paren).trim()
  if (base.startsWith('timestamp with ')) base = 'timestamptz'
  else if (base.startsWith('timestamp without ')) base = 'timestamp'
  else if (base.startsWith('time with ')) base = 'timetz'
  else if (base.startsWith('time without ')) base = 'time'
  else if (base === 'double precision') base = 'double'
  return { base, array }
}

export function resolveTypeVisualKind(dataType: string | undefined): TypeVisualKind {
  if (!dataType?.trim()) return 'unknown'
  const { base, array } = typeBase(dataType)
  if (array) return 'structured'
  if (has(INTEGER_BASES, base)) return 'integer'
  if (has(NUMERIC_BASES, base)) return 'numeric'
  if (has(BOOLEAN_BASES, base)) return 'boolean'
  if (has(TEMPORAL_BASES, base) || base.startsWith('timestamp_')) return 'temporal'
  if (has(STRUCTURED_BASES, base)) return 'structured'
  if (has(IDENTIFIER_BASES, base)) return 'identifier'
  if (has(BINARY_BASES, base)) return 'binary'
  if (has(SPATIAL_BASES, base)) return 'spatial'
  if (has(STRING_BASES, base)) return 'string'
  return 'unknown'
}

export function isNumericType(dataType: string | undefined): boolean {
  if (!dataType?.trim()) return false
  const { base, array } = typeBase(dataType)
  return !array && has(NUMERIC_BASES, base)
}
