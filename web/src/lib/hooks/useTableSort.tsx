// 表头排序 hook + 排序表头组件 —— L2 公共层
// hook: keyOf 取排序键, 切换同列翻转方向, 切换新列回默认(startedAt 类时间列默认降序)。
// SortHead: 原生 button 保持键盘可达, 活列显示方向箭头。
import { useCallback, useMemo, useState } from 'react'
import { TableHead } from '../../components/ui/table'
import { cn } from '../../lib/utils'

export interface SortState { key: string; dir: 1 | -1 }

export function useTableSort<T>(rows: T[], keyOf: Record<string, (r: T) => string | number>) {
  const [sort, setSort] = useState<SortState>({ key: '', dir: -1 })
  const sorted = useMemo(() => {
    if (!sort.key || !keyOf[sort.key]) return rows
    const fn = keyOf[sort.key]
    return [...rows].sort((a, b) => {
      const va = fn(a), vb = fn(b)
      const c = typeof va === 'number' && typeof vb === 'number' ? va - vb : String(va).localeCompare(String(vb), 'zh')
      return c * sort.dir
    })
  }, [rows, sort, keyOf])
  const toggle = useCallback((key: string) => {
    setSort(s => s.key === key ? { key, dir: s.dir === 1 ? -1 : 1 } : { key, dir: key === 'startedAt' ? -1 : 1 })
  }, [])
  return { sorted, sort, toggle }
}

export function SortHead({ label, k, sort, onToggle, className }: {
  label: string; k: string; sort: SortState; onToggle: (k: string) => void; className?: string
}) {
  const active = sort.key === k
  return (
    <TableHead className={className}>
      <button className="inline-flex items-center gap-1 hover:text-foreground transition-colors" onClick={() => onToggle(k)}>
        {label}
        <span className={cn('text-[10px] leading-none', !active && 'opacity-0 hover:opacity-50', active && 'text-accent')}>
          {active && sort.dir === 1 ? '▲' : '▼'}
        </span>
      </button>
    </TableHead>
  )
}
