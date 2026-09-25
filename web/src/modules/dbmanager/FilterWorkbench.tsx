// 行过滤工作台(对标 dbx DataGridFilterWorkbench) —— DatabaseManager 模块级共享。
// 只负责条件的"编辑 UI"; 条件状态与 where 执行判定留在 DataPanel,
// 而 SQL 片段与 chip 文案统一出自 filterSql.ts(不再两处各写一套引号)。

import type { KeyboardEvent } from 'react'
import { FILTER_OPS, opTakesNoValue, opIsRange, opIsList, type FilterCond } from './filterSql'

export function FilterWorkbench({ columns, filters, onChange, joiner, onJoinerToggle, onClearAll, onApply, dirty, hint }: {
  columns: string[]
  filters: FilterCond[]
  onChange: (fs: FilterCond[]) => void
  joiner: 'AND' | 'OR'
  onJoinerToggle: () => void
  onClearAll: () => void
  onApply: () => void
  dirty: boolean
  hint?: string
}) {
  const patch = (i: number, p: Partial<FilterCond>) => onChange(filters.map((x, j) => (j === i ? { ...x, ...p } : x)))
  // 回车即应用: 键盘流不必去够按钮
  const onKey = (e: KeyboardEvent) => { if (e.key === 'Enter') { e.preventDefault(); onApply() } }

  return (
    <div className="db-row-filter">
      {filters.map((f, fi) => (
        <div key={fi} className="db-row-filter-row">
          <select className="input" value={f.col} onChange={e => patch(fi, { col: e.target.value })}>
            {columns.map(c => <option key={c} value={c}>{c}</option>)}
          </select>
          <select className="input" value={f.op} onChange={e => patch(fi, { op: e.target.value })}>
            {FILTER_OPS.map(op => <option key={op} value={op}>{op}</option>)}
          </select>
          <input
            className="input"
            placeholder={opTakesNoValue(f.op) ? '' : opIsList(f.op) ? '多个值用逗号或换行分隔' : opIsRange(f.op) ? '下界' : '值'}
            value={f.value}
            disabled={opTakesNoValue(f.op)}
            onKeyDown={onKey}
            onChange={e => patch(fi, { value: e.target.value })}
          />
          {opIsRange(f.op) && !opTakesNoValue(f.op) && (
            <input
              className="input"
              placeholder="上界"
              value={f.value2 || ''}
              onKeyDown={onKey}
              onChange={e => patch(fi, { value2: e.target.value })}
            />
          )}
          <button className="btn-glass-soft btn-glass-soft-sm" title="移除" onClick={() => onChange(filters.filter((_, i) => i !== fi))}>✕</button>
        </div>
      ))}
      <div style={{ display: 'flex', gap: '0.4rem', alignItems: 'center' }}>
        <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => onChange([...filters, { col: columns[0] || '', op: '=', value: '' }])}>+ 条件</button>
        {filters.length > 1 && (
          <button className="btn-glass-soft btn-glass-soft-sm" title="切换条件间组合方式" onClick={onJoinerToggle}>
            组合: {joiner}
          </button>
        )}
        <button className="btn-glass-soft btn-glass-soft-sm" disabled={!dirty} onClick={onApply}>应用筛选</button>
        {filters.length > 0 && <button className="btn-glass-soft btn-glass-soft-sm" onClick={onClearAll}>清除全部</button>}
        {hint && <span className="dim" style={{ fontSize: '0.625rem' }}>{hint}</span>}
      </div>
    </div>
  )
}
