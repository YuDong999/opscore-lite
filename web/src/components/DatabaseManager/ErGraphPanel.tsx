// ER 关系图视图: 表卡片 + 外键箭头 (React Flow + dagre 自动布局, 只读探查)
// 对标 GoNavi DataGridErDiagram(自动布局) + dbx 位置持久化/SVG·PNG 导出(精选)。
// 位置持久化: 拖放后存 localStorage(key=conn:db), 重开不丢; 导出: SVG/PNG(html-to-image)。

import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import ReactFlow, {
  Background,
  Controls,
  MiniMap,
  useNodesState,
  useEdgesState,
  useReactFlow,
  ReactFlowProvider,
  type Node,
  type Edge,
} from 'reactflow'
import 'reactflow/dist/style.css'
import { Handle, Position } from 'reactflow'
import dagre from 'dagre'
import { toPng, toSvg } from 'html-to-image'
import { getFkGraph, type FkGraph } from './api'

const POS_KEY = (connId: string, db: string) => `er-positions:${connId}:${db}`

interface ErGraphPanelProps {
  connId: string
  database: string
  /** 表级入口: 以该表为中心(高亮+首屏聚焦) */
  focusTable?: string
}

// dagre 自动布局: 从左到右, 节点间距固定
function layoutWithDagre(tables: string[], edges: FkGraph['edges']): Map<string, { x: number; y: number }> {
  const g = new dagre.graphlib.Graph()
  g.setGraph({ rankdir: 'LR', nodesep: 60, ranksep: 120 })
  g.setDefaultEdgeLabel(() => ({}))
  const NODE_W = 190
  for (const t of tables) g.setNode(t, { width: NODE_W, height: 60 })
  for (const e of edges) {
    if (tables.includes(e.fromTable) && tables.includes(e.toTable)) {
      g.setEdge(e.fromTable, e.toTable)
    }
  }
  dagre.layout(g)
  const pos = new Map<string, { x: number; y: number }>()
  for (const t of tables) {
    const n = g.node(t)
    pos.set(t, { x: (n?.x || 0) - NODE_W / 2, y: (n?.y || 0) - 30 })
  }
  return pos
}

function ErGraphInner({ connId, database, focusTable }: ErGraphPanelProps) {
  const [graph, setGraph] = useState<FkGraph | null>(null)
  const [err, setErr] = useState('')
  const [loading, setLoading] = useState(true)
  const [nodes, setNodes, onNodesChange] = useNodesState([])
  const [edges, setEdges, onEdgesChange] = useEdgesState([])
  const { fitView } = useReactFlow()
  const wrapRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    setLoading(true)
    getFkGraph(connId, database)
      .then(setGraph)
      .catch(e => setErr(e.message || '加载关系图失败'))
      .finally(() => setLoading(false))
  }, [connId, database])

  // 建图: 优先 localStorage 持久化位置, 否则 dagre 自动布局
  useEffect(() => {
    if (!graph) return
    const saved = (() => {
      try { return JSON.parse(localStorage.getItem(POS_KEY(connId, database)) || 'null') } catch { return null }
    })() as Record<string, { x: number; y: number }> | null

    const auto = layoutWithDagre(graph.tables, graph.edges)
    const erNodes: Node[] = graph.tables.map(t => {
      const p = saved?.[t] || auto.get(t) || { x: 0, y: 0 }
      const isFocus = !!focusTable && (t === focusTable || t.endsWith('.' + focusTable))
      return {
        id: t,
        position: p,
        data: { label: t },
        type: 'table',
        style: isFocus
          ? { border: '2.5px solid #f59e0b', boxShadow: '0 0 0 4px rgba(245, 158, 11, 0.25)', zIndex: 10 }
          : focusTable ? { opacity: 0.55 } : undefined,
      }
    })
    const erEdges: Edge[] = graph.edges.map((e, i) => ({
      id: `e${i}`,
      source: e.fromTable,
      target: e.toTable,
      label: `${e.fromColumn} → ${e.toColumn}`,
      type: 'smoothstep',
      animated: false,
      style: { stroke: 'var(--accent)', strokeWidth: 1.5 },
      labelStyle: { fontSize: 10, fill: 'var(--text-dim)' },
      labelBgStyle: { fill: 'var(--surface-solid)', fillOpacity: 0.85 },
    }))
    setNodes(erNodes)
    setEdges(erEdges)
    setTimeout(() => {
      if (focusTable) {
        const focusNode = erNodes.find(n => n.id === focusTable || n.id.endsWith('.' + focusTable))
        if (focusNode) { fitView({ nodes: [{ id: focusNode.id }], padding: 2.5, duration: 300, maxZoom: 1 }); return }
      }
      fitView({ padding: 0.15, maxZoom: 1.2 })
    }, 60)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [graph])

  // 位置持久化: 拖停即存
  const persist = useCallback((_, node) => {
    try {
      const key = POS_KEY(connId, database)
      const saved = JSON.parse(localStorage.getItem(key) || '{}')
      saved[node.id] = node.position
      localStorage.setItem(key, JSON.stringify(saved))
    } catch { /* 存储满等静默 */ }
  }, [connId, database])

  const doExport = useCallback(async (format: 'png' | 'svg') => {
    const el = wrapRef.current?.querySelector('.react-flow__viewport')?.parentElement
    if (!el) return
    const fn = format === 'png' ? toPng : toSvg
    const dataUrl = await fn(el as HTMLElement, { backgroundColor: '#ffffff', filter: (n) => !(n.classList?.contains('react-flow__minimap') || n.classList?.contains('react-flow__controls')) })
    const a = document.createElement('a')
    a.href = dataUrl
    a.download = `er-${database}.${format}`
    a.click()
  }, [database])

  if (loading) return <div className="log-loading">加载关系图中...</div>
  if (err) return <div className="banner banner-err">{err}</div>
  if (!graph || graph.tables.length === 0) return <div className="db-empty">该库暂无表, 无法生成关系图</div>

  return (
    <div style={{ display: 'flex', flexDirection: 'column', height: '100%', gap: 6 }}>
      <div style={{ display: 'flex', gap: 6, alignItems: 'center' }}>
        <span className="db-engine-badge">ER 关系图</span>
        <span className="dim" style={{ fontSize: '0.72rem' }}>{graph.tables.length} 表 · {graph.edges.length} 条外键关系 · 拖动可调整布局(自动记忆)</span>
        <span style={{ marginLeft: 'auto', display: 'flex', gap: 6 }}>
          <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => doExport('svg')}>导出 SVG</button>
          <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => doExport('png')}>导出 PNG</button>
          <button className="btn-glass-soft btn-glass-soft-sm" title="清空本图保存的布局, 恢复自动排列"
            onClick={() => { localStorage.removeItem(POS_KEY(connId, database)); setGraph(g => (g ? { ...g } : g)) }}>重置布局</button>
        </span>
      </div>
      <div ref={wrapRef} style={{ flex: 1, minHeight: 0, border: '1px solid var(--border)', borderRadius: 6, overflow: 'hidden' }}>
        <ReactFlow
          nodes={nodes}
          edges={edges}
          onNodesChange={onNodesChange}
          onEdgesChange={onEdgesChange}
          onNodeDragStop={persist}
          fitView
          minZoom={0.2}
          maxZoom={2}
          proOptions={{ hideAttribution: true }}
          nodeTypes={{ table: TableCard }}
        >
          <Background gap={18} />
          <Controls showInteractive={false} />
          <MiniMap pannable zoomable style={{ width: 140, height: 90 }} />
        </ReactFlow>
      </div>
    </div>
  )
}

// 表卡片节点(dbx TableNode 同构极简版): 表名头 + PK/FK 标记列
function TableCard({ data }: { data: { label: string } }) {
  return (
    <div style={{
      background: 'var(--surface-solid)', border: '1.5px solid var(--accent)', borderRadius: 6,
      minWidth: 170, fontSize: 12, overflow: 'hidden',
    }}>
      <div style={{ padding: '4px 8px', fontWeight: 650, borderBottom: '1px solid var(--border)', background: 'var(--surface-tint)', display: 'flex', alignItems: 'center', gap: 5 }}>
        <svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="#16a34a" strokeWidth="2"><rect x="3" y="3" width="18" height="18" rx="2" /><path d="M3 9h18M3 15h18M9 3v18" /></svg>
        <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{data.label}</span>
      </div>
      <div style={{ padding: '3px 8px', color: 'var(--text-dim)', fontSize: 10 }}>拖动调整布局(自动记忆)</div>
      <Handles />
    </div>
  )
}

function Handles() {
  const s = { width: 6, height: 6, background: 'var(--accent)' }
  return (
    <>
      <Handle type="source" position={Position.Right} style={{ ...s, right: -3 }} />
      <Handle type="target" position={Position.Left} style={{ ...s, left: -3 }} />
      <Handle type="source" position={Position.Bottom} style={{ ...s, bottom: -3 }} />
      <Handle type="target" position={Position.Top} style={{ ...s, top: -3 }} />
    </>
  )
}

export default function ErGraphPanel(props: ErGraphPanelProps) {
  return (
    <div className="db-doc-section" style={{ flex: 1, minHeight: 0 }}>
      <ReactFlowProvider>
        <ErGraphInner {...props} />
      </ReactFlowProvider>
    </div>
  )
}
