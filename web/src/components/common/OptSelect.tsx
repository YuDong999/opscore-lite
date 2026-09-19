// 可选值下拉 —— L2 公共层
// 空串语义统一走哨兵(Radix Select 不允许空串 value), 消灭 onChange 手动复位 hack;
// 选中非空值时触发器高亮: 过滤态/已配置态必须一眼可辨(否则用户看不出当前限定了什么)。
// items 自带空串项(如「轮询(默认)」)时不重复渲染 placeholder 项, 避免下拉出现两条同义选项。
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '../ui/select'
import { cn } from '../../lib/utils'

const OPT_NONE = '__none__'

export function OptSelect({ value, onChange, placeholder, items, className }: {
  value: string
  onChange: (v: string) => void
  placeholder: string
  items: { value: string; label: string }[]
  className?: string
}) {
  const set = value !== ''
  const hasNoneItem = items.some(i => i.value === '')
  return (
    <Select value={value || OPT_NONE} onValueChange={v => onChange(v === OPT_NONE ? '' : v)}>
      <SelectTrigger className={cn(className, set && 'border-accent/60 bg-accent/10 font-medium')}>
        <SelectValue placeholder={placeholder} />
      </SelectTrigger>
      <SelectContent>
        {!hasNoneItem && <SelectItem value={OPT_NONE}>{placeholder}</SelectItem>}
        {items.map(i => <SelectItem key={i.value || OPT_NONE} value={i.value || OPT_NONE}>{i.label}</SelectItem>)}
      </SelectContent>
    </Select>
  )
}
