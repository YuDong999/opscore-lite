// 行过滤工作台(对标 dbx DataGridFilterWorkbench 的最小形态) —— DatabaseManager 模块级共享。
// 只负责条件的"编辑 UI"(条件行/添加/AND OR 组合/清除); 状态与 where 生成的"执行判定"
// 留在 DataPanel(核心不变式: 显示与执行共用同一套判定逻辑, 见 DataPanel 的 where 生成处)。
// 未来出现第二个消费方时整块提升 L2(components/common/)。

export interface FilterCond { col: string; op: string; value: string }

const OPS = ['=', '!=', 'LIKE', '>', '<', '>=', '<=', 'IS NULL', 'IS NOT NULL']

export function FilterWorkbench({ columns, filters, onChange, joiner, onJoinerToggle, onClearAll, hint }: {
  columns: string[]
  filters: FilterCond[]
  onChange: (fs: FilterCond[]) => void
  joiner: 'AND' | 'OR'
  onJoinerToggle: () => void
  onClearAll: () => void
  hint?: string
}) {
  return (
    <div className="db-row-filter">
      {filters.map((f, fi) => (
        <div key={fi} className="db-row-filter-row">
          <select className="input" value={f.col} onChange={e => onChange(filters.map((x, i) => i === fi ? { ...x, col: e.target.value } : x))}>
            {columns.map(c => <option key={c} value={c}>{c}</option>)}
          </select>
          <select className="input" value={f.op} onChange={e => onChange(filters.map((x, i) => i === fi ? { ...x, op: e.target.value } : x))}>
            {OPS.map(op => <option key={op} value={op}>{op}</option>)}
          </select>
          <input className="input" placeholder="值" value={f.value} disabled={f.op.startsWith('IS')}
            onChange={e => onChange(filters.map((x, i) => i === fi ? { ...x, value: e.target.value } : x))} />
          <button className="btn-glass-soft btn-glass-soft-sm" title="移除" onClick={() => onChange(filters.filter((_, i) => i !== fi))}>✕</button>
        </div>
      ))}
      <div style={{ display: 'flex', gap: '0.4rem', alignItems: 'center' }}>
        <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => onChange([...filters, { col: columns[0] || '', op: '=', value: '' }])}>+ 条件</button>
        {filters.length > 1 && (
          <button className="btn-glass-soft btn-glass-soft-sm" title="切换条件间组合方式"
            onClick={onJoinerToggle}>
            组合: {joiner}
          </button>
        )}
        {filters.length > 0 && <button className="btn-glass-soft btn-glass-soft-sm" onClick={onClearAll}>清除全部</button>}
        {hint && <span className="dim" style={{ fontSize: '0.625rem' }}>{hint}</span>}
      </div>
    </div>
  )
}
