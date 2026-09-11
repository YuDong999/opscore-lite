// ── 流量分发 Tab(级联: 主机→探测→upstream 参数编辑→应用) ──
// 参数三类控件: 状态类(LB 模式)下拉 / 数值类(权重)数字输入 / 开关类(down/backup)Switch

import { useState } from 'react'
import { postJSON } from '../../api/client'
import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Badge } from '@/components/ui/badge'
import { Checkbox } from '@/components/ui/checkbox'
import { RefreshCw, LoaderCircle, Check, Network } from 'lucide-react'
import { API, ErrBanner } from './shared'
import { OptSelect, HostSelect } from './common'
import { useToast } from '../../components/Toast'

interface NginxUpstreamSrv { addr: string; weight: number; down: boolean; backup: boolean; rawArgs: string }
interface NginxUpstream { name: string; lb: string; file: string; servers: NginxUpstreamSrv[]; raw: string }
interface NginxConfFile { path: string; upstreams: NginxUpstream[]; servers: { listen: string; serverName: string; proxyPass: string[]; file: string }[] }
interface NginxProbeT { host: string; mainConf: string; nginxActive: boolean; files: NginxConfFile[] }

const LB_OPTIONS = [
  { value: '', label: '轮询(默认)' },
  { value: 'least_conn', label: '最少连接 least_conn' },
  { value: 'ip_hash', label: 'IP 哈希 ip_hash' },
  { value: 'random', label: '随机 random' },
]

// 编辑副本重建 upstream 块文本(LB 指令 + server 行, weight/down/backup 全量写入)
function buildUpstream(u: NginxUpstream): string {
  const lines = u.servers.map(s => {
    if (s.down) return '    server ' + s.addr + ' down;'
    const parts = ['server ' + s.addr]
    if (s.weight !== 1) parts.push('weight=' + s.weight)
    if (s.backup) parts.push('backup')
    return '    ' + parts.join(' ') + ';'
  })
  if (u.lb) lines.unshift('    ' + u.lb + ';')
  return 'upstream ' + u.name + ' {\n' + lines.join('\n') + '\n}'
}

export default function TrafficTab() {
  const [host, setHost] = useState('')
  const [probing, setProbing] = useState(false)
  const [probe, setProbe] = useState<NginxProbeT | null>(null)
  const [confPath, setConfPath] = useState('')
  const [err, setErr] = useState('')
  const [applying, setApplying] = useState(false)
  const [applyMsg, setApplyMsg] = useState('')
  const { toast } = useToast()
  const [draft, setDraft] = useState<NginxUpstream[]>([])
  const [dirty, setDirty] = useState(false)

  const doProbe = () => {
    if (host === undefined) return
    setProbing(true); setErr(''); setProbe(null); setDraft([]); setDirty(false)
    postJSON<NginxProbeT>(API.nginxProbe, { host })
      .then(d => {
        setProbe(d)
        const f = d.files.find(f => f.upstreams.length > 0) || d.files[0]
        if (f) { setConfPath(f.path); setDraft((f.upstreams || []).map(u => ({ ...u, servers: u.servers.map(s => ({ ...s })) }))) }
      })
      .catch(e => setErr('探测失败: ' + e.message))
      .finally(() => setProbing(false))
  }

  const selectConf = (p: string) => {
    setConfPath(p)
    const f = probe?.files.find(f => f.path === p)
    setDraft(f ? (f.upstreams || []).map(u => ({ ...u, servers: u.servers.map(s => ({ ...s })) })) : [])
    setDirty(false)
  }

  const mut = (ui: number, fn: (u: NginxUpstream) => void) => {
    setDraft(list => list.map((u, i) => i === ui ? (() => { const c = { ...u, servers: u.servers.map(s => ({ ...s })) }; fn(c); return c })() : u))
    setDirty(true)
  }

  const doApply = async () => {
    if (!confPath) return
    setApplying(true); setApplyMsg('')
    try {
      const orig = probe?.files.find(f => f.path === confPath)?.upstreams || []
      const edits = draft
        .map((u, i) => ({ u, o: orig[i] }))
        .filter(({ u, o }) => JSON.stringify(u) !== JSON.stringify(o))
        .map(({ u, o }) => ({ file: confPath, orig: o?.raw || '', new: buildUpstream(u) }))
      if (edits.length === 0) { toast.info('没有变更'); return }
      await postJSON(API.nginxApply, { host, mainConf: probe?.mainConf, edits })
      toast.success(`已应用 ${edits.length} 个 upstream 修改(nginx -t 通过并 reload)`)
      setDirty(false); setApplyMsg(`应用成功: ${edits.length} 个 upstream`)
      doProbe()
    } catch (e: any) {
      setErr(e.message)
    } finally { setApplying(false) }
  }

  const confFiles = (probe?.files || []).filter(f => f.upstreams.length > 0 || f.servers.length > 0)
  const curServers = probe?.files.find(f => f.path === confPath)?.servers || []

  return (
    <div className="lg:h-[calc(100vh-14rem)] lg:flex lg:flex-col lg:gap-3">
      {err && <ErrBanner msg={err} onClose={() => setErr('')} />}
      <Card className="py-3 shrink-0">
        <CardContent className="flex items-end gap-3 flex-wrap py-0">
          <div>
            <Label className="text-xs">目标主机</Label>
            <HostSelect value={host} onChange={v => { setHost(v); setProbe(null); setDraft([]) }} />
          </div>
          <Button size="sm" className="h-9" disabled={!host || probing} onClick={doProbe}>
            {probing ? <LoaderCircle className="animate-spin" /> : <RefreshCw />}{probing ? '检测中...' : '检测 nginx 配置'}
          </Button>
          {probe && confFiles.length > 0 && (
            <div>
              <Label className="text-xs">配置文件</Label>
              <OptSelect className="w-72" value={confPath} onChange={selectConf} placeholder="选择配置文件"
                items={confFiles.map(f => ({ value: f.path, label: `${f.path} (upstream×${f.upstreams.length} server×${f.servers.length})` }))} />
            </div>
          )}
          {draft.length > 0 && (
            <Button size="sm" disabled={applying || !dirty} onClick={doApply}>
              {applying ? <LoaderCircle className="animate-spin" /> : <Check />}{dirty ? '应用修改' : '无变更'}
            </Button>
          )}
          {applyMsg && <span className="text-xs text-ok self-center">{applyMsg}</span>}
        </CardContent>
      </Card>

      {!probe && (
        <Card className="flex-1 min-h-0 flex flex-col overflow-hidden">
          <CardContent className="flex-1 flex flex-col items-center justify-center gap-2 text-muted-foreground">
            <Network className="size-8 opacity-50" />
            <div className="text-sm">选择主机后点击「检测 nginx 配置」</div>
            <div className="text-xs">自动解析 include 链 → upstream 结构 → 级联编辑流量参数(模式/权重/下线/备用)</div>
          </CardContent>
        </Card>
      )}
      {probe && !probe.nginxActive && (
        <Card><CardContent className="py-5 text-sm text-muted-foreground text-center">
          该主机上 nginx 未在运行(systemctl is-active ≠ active)或未安装 —— 无法可视化流量分发; 安装/启动后重新检测
        </CardContent></Card>
      )}

      {probe && (
        <div className="flex-1 min-h-0 hover-scroll space-y-3">
          {draft.length === 0 && (
            <Card><CardContent className="py-6 text-sm text-muted-foreground text-center">
              当前配置文件里没有 upstream(负载均衡组)—— 裸机直连型站点无流量分发可调; 可先在配置中创建 upstream 后重新检测
            </CardContent></Card>
          )}
          {draft.map((u, ui) => (
            <Card key={u.name} className="gap-0 py-3 flex flex-col overflow-hidden">
              <CardHeader className="gap-0 pb-2 shrink-0 grid-rows-[auto]">
                <div className="flex items-center justify-between gap-2 flex-wrap">
                  <CardTitle className="text-sm">upstream: {u.name}</CardTitle>
                  <div className="flex items-center gap-2">
                    <Label className="text-xs text-muted-foreground">流量分布模式</Label>
                    <OptSelect className="w-48" value={u.lb} onChange={v => mut(ui, x => { x.lb = v })}
                      placeholder="轮询(默认)" items={LB_OPTIONS} />
                  </div>
                </div>
              </CardHeader>
              <CardContent className="pt-0 space-y-1.5">
                {u.servers.map((srv, si) => (
                  <div key={si} className="flex items-center gap-2 flex-wrap rounded-md border px-2.5 py-1.5">
                    <span className="font-mono text-xs shrink-0">{srv.addr}</span>
                    <div className="flex items-center gap-1">
                      <Label className="text-xs text-muted-foreground">权重</Label>
                      <Input type="number" min={1} max={100} className="w-20 h-7 text-xs"
                        value={srv.down ? 0 : srv.weight}
                        disabled={srv.down}
                        onChange={e => mut(ui, x => { x.servers[si].weight = Math.max(1, parseInt(e.target.value) || 1) })} />
                    </div>
                    <div className="flex items-center gap-1.5">
                      <Checkbox checked={srv.down} onCheckedChange={c => mut(ui, x => { x.servers[si].down = !!c; if (c) x.servers[si].weight = 1 })} id={`down-${ui}-${si}`} />
                      <Label htmlFor={`down-${ui}-${si}`} className="text-xs text-muted-foreground">下线</Label>
                    </div>
                    <div className="flex items-center gap-1.5">
                      <Checkbox checked={srv.backup} onCheckedChange={c => mut(ui, x => { x.servers[si].backup = !!c })} id={`bk-${ui}-${si}`} />
                      <Label htmlFor={`bk-${ui}-${si}`} className="text-xs text-muted-foreground">备用</Label>
                    </div>
                    {srv.backup && <Badge variant="outline" className="text-[10px]">backup</Badge>}
                  </div>
                ))}
              </CardContent>
            </Card>
          ))}

          {curServers.length > 0 && (
            <Card className="gap-0 py-3 flex flex-col overflow-hidden">
              <CardHeader className="gap-0 pb-2 shrink-0 grid-rows-[auto]"><CardTitle className="text-sm">server 块(只读 · 转发目标)</CardTitle></CardHeader>
              <CardContent className="pt-0 space-y-1">
                {curServers.map((sv, i) => (
                  <div key={i} className="text-xs text-muted-foreground flex gap-2 items-center px-2 py-1 flex-wrap">
                    <span className="font-mono">listen {sv.listen}</span>
                    {sv.serverName && <span>· {sv.serverName}</span>}
                    {sv.proxyPass.map((pp, j) => <Badge key={j} variant="secondary" className="font-mono text-[10px]">→ {pp}</Badge>)}
                  </div>
                ))}
              </CardContent>
            </Card>
          )}
        </div>
      )}
    </div>
  )
}
