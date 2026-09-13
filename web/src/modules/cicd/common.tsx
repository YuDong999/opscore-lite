// ── CI/CD 模块内共享的表单控件(唯一来源, 页面从这里引用) ──
// 与 plain-merge-demo 谱系的 cicd/common.tsx 保持同名同 API, 便于跨分支 cherry-pick。

import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { SELECT_NONE } from './shared'
import { cn } from '@/lib/utils'

// 可选值下拉(空串语义统一走哨兵, 消灭 onChange 手动复位 hack)
// 选中非空值时触发器高亮: 过滤态/已配置态必须一眼可辨(否则用户看不出当前限定了什么)
export function OptSelect({ value, onChange, placeholder, items, className }: {
  value: string
  onChange: (v: string) => void
  placeholder: string
  items: { value: string; label: string }[]
  className?: string
}) {
  const set = value !== ''
  // items 自带空串项(如 LB 的「轮询(默认)」)时不再重复渲染 placeholder 项, 避免下拉出现两条同义选项
  const hasNoneItem = items.some(i => i.value === '')
  return (
    <Select value={value || SELECT_NONE} onValueChange={v => onChange(v === SELECT_NONE ? '' : v)}>
      <SelectTrigger className={cn(className, set && 'border-accent/60 bg-accent/10 font-medium')}>
        <SelectValue placeholder={placeholder} />
      </SelectTrigger>
      <SelectContent>
        {!hasNoneItem && <SelectItem value={SELECT_NONE}>{placeholder}</SelectItem>}
        {items.map(i => <SelectItem key={i.value || SELECT_NONE} value={i.value || SELECT_NONE}>{i.label}</SelectItem>)}
      </SelectContent>
    </Select>
  )
}
