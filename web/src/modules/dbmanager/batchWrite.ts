import type { GridChange } from '../../components/common/DataGrid'

// 网格"待提交变更"提交前的本地预检(表数据页与查询结果页共用):
// CHAR(n)/VARCHAR(n) 超长这种"一看就知道"的错别丢给 MySQL 报 1406, 在界面上直接点名。
export function charLimitOf(type: string | undefined): number {
  const m = /^\s*(?:var)?char\((\d+)\)/i.exec(type || '')
  return m ? Number(m[1]) : 0
}

export interface PrecheckResult {
  error?: string
  badCells?: Array<{ row: number; col: number }>
}

// columns: 网格列名(与 change.col 对应); meta: 列元数据(可缺, 缺了就只做不了的类型类检查)
export function precheckChanges(
  changes: GridChange[],
  columns: string[],
  meta?: Array<{ name: string; type: string }>,
): PrecheckResult {
  const typeOf = (name: string) => (meta || []).find(m => m.name === name)?.type
  for (const [i, c] of changes.entries()) {
    // 新增行: 检查用户填过的字符串列(新增没有单元格可点名, 只给消息)
    if (c.kind === 'insert' && c.values) {
      for (const [name, v] of Object.entries(c.values)) {
        const limit = charLimitOf(typeOf(name))
        const val = typeof v === 'string' ? v : String(v ?? '')
        if (limit > 0 && val.length > limit) {
          return { error: `新增行: ${name} 是 char/varchar(${limit}), 你填了 ${val.length} 个字符 —— 未提交任何内容` }
        }
      }
      continue
    }
    if (c.kind !== 'update' || c.value === null || c.col === undefined) continue
    const name = columns[c.col]
    const limit = charLimitOf(typeOf(name))
    const val = typeof c.value === 'string' ? c.value : String(c.value ?? '')
    if (limit > 0 && val.length > limit) {
      return {
        badCells: [{ row: c.row!, col: c.col }],
        error: `第 ${i + 1} 处: ${name} 是 ${typeOf(name)}, 你填了 ${val.length} 个字符(上限 ${limit}) —— 未提交任何内容`,
      }
    }
  }
  return {}
}
