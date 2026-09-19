// ── CI/CD 模块内共享的表单控件(唯一来源, 页面从这里引用) ──

import { useEffect, useState } from 'react'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { API, SELECT_NONE, type HostOpt } from './shared'
import { cn } from '@/lib/utils'

// ── 主机列表(本机 + Ansible 清单, 与 HostSelector 同源) ──
export function useHosts(): HostOpt[] {
  const [hosts, setHosts] = useState<HostOpt[]>([{ id: '', label: '本机' }])
  useEffect(() => {
    fetch(API.hosts).then(r => r.json())
      .then((list: any[]) => {
        // 本机已由下方硬编码项承载; API 清单里的 isLocal 条目(及空 id)跳过, 防止"本机"重复
        const opts = list.filter(h => !h.isLocal && h.id !== '').map(h => ({
          id: h.id as string,
          label: (h.alias || h.addr) + (h.alias && h.alias !== h.addr ? ` (${h.addr})` : ''),
        }))
        setHosts([{ id: '', label: '本机' }, ...opts])
      })
      .catch(() => {})
  }, [])
  return hosts
}

// 主机下拉(阶段卡用; NONE 哨兵承载"本机"空串语义)
export function HostSelect({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  const hosts = useHosts()
  return (
    <Select value={value || SELECT_NONE} onValueChange={v => onChange(v === SELECT_NONE ? '' : v)}>
      <SelectTrigger><SelectValue /></SelectTrigger>
      <SelectContent>
        {hosts.map(h => <SelectItem key={h.id || SELECT_NONE} value={h.id || SELECT_NONE}>{h.label}</SelectItem>)}
      </SelectContent>
    </Select>
  )
}
