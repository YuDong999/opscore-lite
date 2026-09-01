// ── 网络拓扑: 主机实体 / 网卡 / 网关 / 主机发现 / LLDP 设备 ──
// 图模式: ECharts force 图; 树模式: 从实体/self 出发按边层级展开

import { useEffect, useMemo, useState } from 'react'
import { getJSON } from '../api/client'
import { useHost } from '../components/HostContext'
import EChart from '../charts/EChart'

interface Segment { cidr: string; gateway?: string; iface?: string; via: string; localIp?: string; remoteOf?: string }
interface Device { ip: string; mac?: string; hostname?: string; segment?: string; source: string; type: string; online: boolean; inInventory: boolean; hostId?: string; alias?: string; entity?: string; iface?: string }
interface Link { from: string; to: string; type: string }
interface Topology { segments: Segment[]; devices: Device[]; links: Link[]; scanned: boolean; elapsed: string }

const keyOf = (d: Device) => d.type === 'entity' ? `entity:${d.hostname || '本机'}` : d.ip || `lldp:${d.hostname || ''}`

const NODE_STYLE: Record<string, { size: number; color: string; symbol: string }> = {
  entity:  { size: 34, color: '#8f7ce0', symbol: 'circle' },
  self:    { size: 30, color: '#7c9cff', symbol: 'circle' },
  gateway: { size: 20, color: '#e6b450', symbol: 'diamond' },
  switch:  { size: 18, color: '#5ab0e8', symbol: 'rect' },
  nic:     { size: 14, color: '#59b8e8', symbol: 'circle' },
  online:  { size: 13, color: '#5ac466', symbol: 'circle' },
  offline: { size: 11, color: '#8a8f98', symbol: 'circle' },
}

const nodeStyle = (d: Device) => {
  if (d.type === 'entity') return { st: NODE_STYLE.entity, cat: 0 }
  const isSelf = d.type === 'host' && d.source === 'local' && !d.iface
  if (isSelf) return { st: NODE_STYLE.self, cat: 1 }
  if (d.type === 'gateway') return { st: NODE_STYLE.gateway, cat: 2 }
  if (d.type === 'switch') return { st: NODE_STYLE.switch, cat: 3 }
  if (d.source === 'local' && d.iface) return { st: NODE_STYLE.nic, cat: 4 }
  return d.online ? { st: NODE_STYLE.online, cat: 5 } : { st: NODE_STYLE.offline, cat: 6 }
}

const nodeLabel = (d: Device) => {
  if (d.type === 'entity') return d.hostname || '本机'
  if (d.source === 'local' && d.iface) {
    const short = d.iface.replace(/^VMware Network Adapter /, '').replace(/^Ethernet adapter /, '')
    return `${short}\n${d.ip}`
  }
  return d.hostname || d.alias || d.ip || d.source
}

const treeLabel = (d: Device) => {
  if (d.type === 'entity') return d.hostname || '本机'
  if (d.source === 'local' && d.iface) {
    const short = d.iface.replace(/^VMware Network Adapter /, '').replace(/^Ethernet adapter /, '')
    return `${short} (${d.ip})`
  }
  return d.hostname || d.alias || d.ip || d.source
}

const treeKind = (d: Device) => {
  if (d.type === 'entity') return 'entity'
  if (d.type === 'host' && d.source === 'local' && !d.iface) return 'self'
  if (d.type === 'gateway') return 'gateway'
  if (d.type === 'switch') return 'switch'
  if (d.source === 'local' && d.iface) return 'nic'
  return d.online ? 'online' : 'offline'
}

const CATEGORIES = ['主机实体', '本机', '网关/路由', '交换机', '网卡', '在线主机', '离线']

export default function TopologyPanel() {
  const { selected } = useHost()
  const url = selected?.id
    ? `/api/core/network/topology?host=${encodeURIComponent(selected.id)}`
    : '/api/core/network/topology'
  const [data, setData] = useState<Topology | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [view, setView] = useState<'graph' | 'dir'>('graph')
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set())

  useEffect(() => {
    let alive = true
    const load = () =>
      getJSON<Topology>(url)
        .then((d) => {
          if (!alive) return
          setData(d)
          setError(null)
        })
        .catch((e) => alive && setError(e instanceof Error ? e.message : String(e)))
        .finally(() => alive && setLoading(false))
    load()
    const t = setInterval(load, 5000)
    return () => { alive = false; clearInterval(t) }
  }, [url])

  const rescan = () => {
    setLoading(true)
    getJSON<Topology>(`${url}${url.includes('?') ? '&' : '?'}force=1`)
      .then((d) => { setData(d); setError(null) })
      .catch((e) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setLoading(false))
  }

  const option = useMemo(() => {
    if (!data) return {}
    const nodes = data.devices.map((d) => {
      const { st, cat } = nodeStyle(d)
      return {
        id: keyOf(d),
        name: nodeLabel(d),
        symbolSize: st.size,
        symbol: st.symbol,
        itemStyle: {
          color: st.color,
          borderColor: d.inInventory ? '#fff' : undefined,
          borderWidth: d.inInventory ? 2 : undefined,
        },
        category: cat,
        value: [d.ip || (d.type === 'entity' ? '—' : d.mac || '—'), d.mac || '—', d.source, d.online ? '在线' : '离线', d.inInventory ? '清单内' : '—', d.iface || '—'].join('  ·  '),
      }
    })
    const ids = new Set(nodes.map((n) => n.id))
    const links = data.links
      .filter((l) => ids.has(l.from) && ids.has(l.to))
      .map((l) => ({ source: l.from, target: l.to, lineStyle: { opacity: 0.5 } }))
    return {
      tooltip: {
        formatter: (p: any) => p.dataType === 'node' ? `<b>${p.name}</b><br/>${p.value ?? ''}` : '',
      },
      legend: { data: CATEGORIES, top: 0, textStyle: { fontSize: 11, color: '#999' } },
      series: [{
        type: 'graph',
        layout: 'force',
        roam: true,
        draggable: true,
        data: nodes,
        links,
        categories: CATEGORIES.map((name) => ({ name })),
        force: { repulsion: 150, edgeLength: 90, gravity: 0.1, friction: 0.6 },
        label: { show: true, fontSize: 10, color: '#ccc' },
        emphasis: { focus: 'adjacency', lineStyle: { width: 2 } },
      }],
    }
  }, [data])

  // ===== 树视图 =====
  const tree = useMemo(() => {
    if (!data) return []
    const byKey = new Map<string, Device>()
    data.devices.forEach((d) => byKey.set(keyOf(d), d))
    // 根: 本机实体 (source=entity), 无则 self (source=local 无 iface)
    let root: Device | undefined = data.devices.find((d) => d.type === 'entity')
    if (!root) root = data.devices.find((d) => d.source === 'local' && d.type === 'host' && !d.iface)
    if (!root) return []
    let selfKey: string | null = null
    // 远程主机视角 (无实体节点, 根为 self): 将 self 的 route 网关提升为树根,
    // 使同网段主机(含 self)成为网关的平级子节点; self 保留高亮并标注「(当前)」
    if (root.type === 'host' && root.source === 'local' && !root.iface) {
      selfKey = keyOf(root)
      const gwLink = data.links.find((l) => (l.from === selfKey || l.to === selfKey) && l.type === 'route')
      if (gwLink) {
        const gwKey = gwLink.from === selfKey ? gwLink.to : gwLink.from
        const gw = byKey.get(gwKey)
        if (gw && gw.type === 'gateway') root = gw
      }
    }

    const adj = new Map<string, { to: string; type: string }[]>()
    const pushEdge = (a: string, b: string, t: string) => {
      if (!adj.has(a)) adj.set(a, [])
      if (!adj.has(b)) adj.set(b, [])
      adj.get(a)!.push({ to: b, type: t })
      adj.get(b)!.push({ to: a, type: t })
    }
    data.links.forEach((l) => pushEdge(l.from, l.to, l.type))

    const EDGE_PRIO: Record<string, number> = { iface: 0, route: 1, reach: 2, uplink: 3 }
    // 树层级辅助: 实体根键 / 本机网卡键 / 每个主机归属的网关键
    // (route 边优先定归属, 缺失时按网关 reach 边补; 归属网段的主机只允许挂在网关下)
    const entityDev = data.devices.find((d) => d.type === 'entity')
    const entityK = entityDev ? keyOf(entityDev) : null
    const isLocalIface = new Set<string>()
    const isGateway = new Set<string>()
    const subnetOf = new Map<string, string>()
    data.devices.forEach((d) => {
      const k = keyOf(d)
      if (d.source === 'local' && d.iface) isLocalIface.add(k)
      if (d.type === 'gateway') isGateway.add(k)
    })
    const setSubnet = (host: string, gw: string) => {
      if (!subnetOf.has(host) && host !== gw) subnetOf.set(host, gw)
    }
    data.links.forEach((l) => {
      if (l.type !== 'route') return
      const a = byKey.get(l.from)
      const b = byKey.get(l.to)
      if (a && a.type === 'gateway') setSubnet(l.to, l.from)
      else if (b && b.type === 'gateway') setSubnet(l.from, l.to)
    })
    data.links.forEach((l) => {
      if (l.type !== 'reach') return
      if (isGateway.has(l.from)) setSubnet(l.to, l.from)
      if (isGateway.has(l.to)) setSubnet(l.from, l.to)
    })
    const visited = new Set<string>()
    interface TN { key: string; name: string; detail: string; kind: string; children: TN[] }
    const build = (key: string): TN | null => {
      if (visited.has(key)) return null
      visited.add(key)
      const d = byKey.get(key)
      const label = d ? treeLabel(d) : key
      const tn: TN = { key, name: selfKey && key === selfKey ? `${label} (当前)` : label, detail: '', kind: d ? treeKind(d) : '', children: [] }
      if (d) {
        const parts: string[] = []
        if (d.ip) parts.push(d.ip)
        if (d.mac) parts.push(d.mac)
        if (d.entity) parts.push(`属 ${d.entity}`)
        if (d.iface) parts.push(d.iface)
        tn.detail = parts.join(' · ')
      }
      // 1) 本机网卡只允许挂在实体根下, 避免被嵌套进其它接口的网关分支
      // 2) 归属网关的网段主机只挂在它的网关卡下: 展开 X 时跳过归属其它网关的主机 Y,
      //    使其与 X 平级同列, 避免被中间节点(如 .94.254)提前捕获造成嵌套
      let next = (adj.get(key) || []).filter((e) => !visited.has(e.to))
      next = next.filter((e) => {
        if (isLocalIface.has(e.to) && key !== entityK) return false
        const own = subnetOf.get(e.to)
        return !(own && own !== key && !isLocalIface.has(e.to))
      })
      next.sort((a, b) => (EDGE_PRIO[a.type] ?? 9) - (EDGE_PRIO[b.type] ?? 9))
      for (const e of next) {
        const c = build(e.to)
        if (c) tn.children.push(c)
      }
      return tn
    }
    return [build(keyOf(root))].filter(Boolean) as TN[]
  }, [data])

  // ===== 目录树视图 (tree /F 制表符连线) =====
  interface DLine { key: string; node: TN; prefix: string; isLast: boolean }
  const flattenTree = (nodes: TN[], prefix: string, closed: Set<string>, out: DLine[]): DLine[] => {
    nodes.forEach((n, i) => {
      const isLast = i === nodes.length - 1
      out.push({ key: n.key, node: n, prefix, isLast })
      if (!closed.has(n.key)) flattenTree(n.children, prefix + (isLast ? '    ' : '│   '), closed, out)
    })
    return out
  }
  const dirLines = useMemo(() => flattenTree(tree, '', collapsed, []), [tree, collapsed])
  const toggleCollapse = (key: string) =>
    setCollapsed((prev) => {
      const next = new Set(prev)
      if (next.has(key)) next.delete(key); else next.add(key)
      return next
    })

  return (
    <div>
      <div className="flex-between" style={{ marginBottom: 8 }}>
        <div className="btn-row">
          <button className="btn-glass-soft btn-glass-soft-sm" onClick={rescan}>重新扫描</button>
          <button className={`btn-glass-soft btn-glass-soft-sm ${view === 'graph' ? 'btn-active' : ''}`} onClick={() => setView('graph')}>图</button>
          <button className={`btn-glass-soft btn-glass-soft-sm ${view === 'dir' ? 'btn-active' : ''}`} onClick={() => setView('dir')}>目录树</button>
          {loading && <span className="dim" style={{ fontSize: 12 }}>扫描中…</span>}
          {data && <span className="dim" style={{ fontSize: 12 }}>{selected?.label ? `视角: ${selected.label} · ` : ''}耗时 {data.elapsed} · {data.devices.length} 设备 · {data.links.length} 连接</span>}
        </div>
      </div>

      {error && <div className="banner banner-error">拓扑加载失败: {error}</div>}
      {data && !data.scanned && (
        <div className="banner banner-err small">非 root / 无原始套接字权限：已降级为只读 ARP 表，仅显示与本机通信过的设备。以 root 运行可主动扫描整个网段。</div>
      )}

      {view === 'graph' ? (
        <Cardless height={460}><EChart option={option} height={460} /></Cardless>
      ) : (
        <div className="card topo-dirtree" style={{ maxHeight:'28.75rem', overflow: 'auto', padding: '0.625rem 0.875rem' }}>
          {dirLines.length === 0 && <div className="dim" style={{ padding: 8 }}>无可用树结构</div>}
          {dirLines.map((l) => {
            const hasKids = l.node.children.length > 0
            const isRoot = l.prefix === ''
            return (
              <div
                key={l.key}
                className="tree-row"
                data-clickable={hasKids || undefined}
                onClick={hasKids ? () => toggleCollapse(l.node.key) : undefined}
                title={l.node.detail || undefined}
              >
                <span className="tree-guide">{isRoot ? '' : `${l.prefix}${l.isLast ? '└── ' : '├── '}`}</span>
                {hasKids && <span className="tree-fold">{collapsed.has(l.node.key) ? '▸' : '▾'}</span>}
                <span className="tree-name" style={{ color: kindColor(l.node.kind) }}>{l.node.name}</span>
                {l.node.detail && <span className="tree-detail">{l.node.detail}</span>}
              </div>
            )
          })}
        </div>
      )}

      {data && (
        <div className="grid grid-2" style={{ marginTop: 12 }}>
          <div className="card">
            <h3 style={{ marginBottom: 8 }}>网段 / 路由</h3>
            <div className="table-wrap">
              <table className="data-table">
                <thead><tr><th>网段</th><th>网关</th><th>接口</th><th>类型</th><th>归属</th></tr></thead>
                <tbody>
                  {data.segments.map((s, i) => (
                    <tr key={i}>
                      <td className="mono">{s.cidr}</td>
                      <td className="mono">{s.gateway || '—'}</td>
                      <td className="mono">{s.iface || '—'}</td>
                      <td>{s.via === 'connected' ? '直连' : s.via === 'remote' ? '远程' : '静态路由'}</td>
                      <td>{s.remoteOf || (s.localIp ? `本机 (${s.localIp})` : '—')}</td>
                    </tr>
                  ))}
                  {data.segments.length === 0 && <tr><td colSpan={5} style={{ textAlign: 'center', color: 'var(--text-dim)' }}>未发现网段</td></tr>}
                </tbody>
              </table>
            </div>
          </div>

          <div className="card">
            <h3 style={{ marginBottom: 8 }}>发现的设备</h3>
            <div className="table-wrap">
              <table className="data-table">
                <thead><tr><th>IP</th><th>MAC</th><th>名称</th><th>类型</th><th>来源</th><th>状态</th></tr></thead>
                <tbody>
                  {data.devices.map((d, i) => (
                    <tr key={i}>
                      <td className="mono">{d.type === 'entity' ? '—' : d.ip || `lldp:${d.hostname}`}</td>
                      <td className="mono small">{d.mac || '—'}</td>
                      <td>{d.hostname || d.alias || '—'}{d.inInventory && <span className="badge badge-info" style={{ fontSize:'0.625rem', marginLeft: 6 }}>清单</span>}</td>
                      <td>{d.type === 'gateway' ? '网关' : d.type === 'switch' ? '交换机' : d.type === 'entity' ? '主机实体' : '主机'}</td>
                      <td>{d.source}</td>
                      <td><span className={`status-dot ${d.online ? 'online' : 'offline'}`} /></td>
                    </tr>
                  ))}
                  {data.devices.length === 0 && <tr><td colSpan={6} style={{ textAlign: 'center', color: 'var(--text-dim)' }}>未发现设备</td></tr>}
                </tbody>
              </table>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}

interface TN { key: string; name: string; detail: string; kind: string; children: TN[] }

function kindColor(kind: string) {
  switch (kind) {
    case 'entity': return '#8f7ce0'
    case 'self': return '#7c9cff'
    case 'gateway': return '#e6b450'
    case 'switch': return '#5ab0e8'
    case 'nic': return '#59b8e8'
    case 'offline': return '#8a8f98'
    default: return '#5ac466'
  }
}

function Cardless({ children, height }: { children: React.ReactNode; height: number }) {
  return (
    <div className="card" style={{ height, padding: 0, overflow: 'hidden' }}>
      {children}
    </div>
  )
}
