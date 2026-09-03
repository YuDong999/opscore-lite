// ── 插件中心模块: 插件列表 / 激活/接入 / 扫描 ──

import { useEffect, useState } from 'react'
import Card from '../components/Card'

function pluginIcon(name: string): React.ReactNode {
  const common = { width: 14, height: 14, viewBox: '0 0 24 24', fill: 'none', stroke: 'currentColor', strokeWidth: 2, strokeLinecap: 'round' as const, strokeLinejoin: 'round' as const }
  switch (name) {
    case 'cpu':
      return (
        <svg {...common}>
          <rect x="4" y="4" width="16" height="16" rx="2" />
          <rect x="9" y="9" width="6" height="6" />
          <path d="M9 1v3M15 1v3M9 20v3M15 20v3M1 9h3M1 15h3M20 9h3M20 15h3" />
        </svg>
      )
    case 'server':
      return (
        <svg {...common}>
          <rect x="2" y="3" width="20" height="7" rx="2" />
          <rect x="2" y="14" width="20" height="7" rx="2" />
          <path d="M6 6.5h.01M6 17.5h.01M10 6.5h.01M10 17.5h.01" />
        </svg>
      )
    case 'network':
      return (
        <svg {...common}>
          <circle cx="12" cy="12" r="2.5" />
          <circle cx="5" cy="5" r="2.5" />
          <circle cx="19" cy="5" r="2.5" />
          <circle cx="5" cy="19" r="2.5" />
          <circle cx="19" cy="19" r="2.5" />
        </svg>
      )
    case 'activity':
      return (
        <svg {...common}>
          <path d="M22 12h-4l-3 9L9 3l-3 9H2" />
        </svg>
      )
    case 'clipboard':
      return (
        <svg {...common}>
          <path d="M16 4h2a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2h2" />
          <rect x="8" y="2" width="8" height="4" rx="1" />
        </svg>
      )
    case 'puzzle':
      return (
        <svg {...common}>
          <path d="M19.44 12.06 16 8.62V7a2 2 0 0 0-2-2h-1.15V3.2a1.5 1.5 0 0 0-3 0V5H8.62a2 2 0 0 0-2 2v1.15H5a1.5 1.5 0 0 0 0 3h1.62V13a2 2 0 0 0 2 2h1.15v1.15a1.5 1.5 0 0 0 3 0V15h1.15a2 2 0 0 0 2-2v-1.15h1.52a1 1 0 0 1 0 2.44l-1.24.86" />
        </svg>
      )
    case 'box':
      return (
        <svg {...common}>
          <path d="M21 8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z" />
          <path d="M3.3 8.3 12 12l8.7-3.7M12 22V12" />
        </svg>
      )
    default:
      return (
        <svg {...common}>
          <path d="M12 3v18M3 12h18" />
        </svg>
      )
  }
}

interface PluginInfo {
  id: string
  name: string
  icon: string
  routePath: string
  group: string
  description: string
  active: boolean
}

export default function PluginsModule() {
  const [plugins, setPlugins] = useState<PluginInfo[]>([])
  const [loading, setLoading] = useState(true)
  const [scanning, setScanning] = useState(false)

  const loadPlugins = () => {
    fetch('/api/plugins')
      .then((r) => r.json())
      .then((data: PluginInfo[]) => setPlugins(data))
      .catch(() => setPlugins([]))
      .finally(() => setLoading(false))
  }

  useEffect(() => {
    loadPlugins()
  }, [])

  const handleScan = () => {
    setScanning(true)
    loadPlugins()
    setTimeout(() => setScanning(false), 800)
  }

  const handleToggle = (id: string, active: boolean) => {
    const action = active ? 'activate' : 'deactivate'
    fetch(`/api/plugins/${id}/${action}`, { method: 'POST' })
      .then((r) => r.json())
      .then((d) => {
        if (d.ok) {
          setPlugins((prev) =>
            prev.map((p) => (p.id === id ? { ...p, active } : p))
          )
          window.dispatchEvent(new Event('manifest-changed'))
        }
      })
  }

  const activeCount = plugins.filter((p) => p.active).length

  return (
    <div className="module">
      <div className="module-head">
        <h2>插件中心</h2>
        <span className="pill">可插拔 · 编译期注册</span>
      </div>

      <Card title="模块契约 (ModuleManifest)">
        <p className="dim">
          所有扩展模块都通过一个 Manifest 注册:
        </p>
        <pre className="code-block">{`type Manifest struct {
    ID          string  // 唯一标识
    Name        string  // 侧栏显示名
    Icon        string  // 图标
    RoutePath   string  // 前端路由
    Group       string  // "core" | "plugin"
    Description string  // 描述
}`}</pre>
        <p className="dim">
          Host Shell 启动时扫描 Manifest 动态生成侧栏与路由;模块只需提供一组 <code>/api/...</code> 与对应前端页面即可被宿主发现。
        </p>
      </Card>

      <Card title="插件管理">
        <div className="btn-row" style={{ marginBottom: 14 }}>
          <button className="btn-glass-soft btn-glass-soft-accent" onClick={handleScan} disabled={scanning}>
            {scanning ? '扫描中...' : '扫描插件'}
          </button>
          <span className="dim" style={{ fontSize:'0.75rem', alignSelf: 'center' }}>
            {activeCount}/{plugins.length} 已激活
          </span>
        </div>

        {loading ? (
          <div className="loading">加载中...</div>
        ) : (
          <div className="table-wrap">
            <table className="data-table">
              <thead>
                <tr>
                  <th>图标</th>
                  <th>名称</th>
                  <th>描述</th>
                  <th>分组</th>
                  <th>状态</th>
                  <th>操作</th>
                </tr>
              </thead>
              <tbody>
                {plugins.map((p) => (
                  <tr key={p.id}>
                    <td><span className="nav-icon" style={{ width:'1.5rem', height:'1.5rem', fontSize: 12 }}>
                      {pluginIcon(p.icon)}
                    </span></td>
                    <td>{p.name}</td>
                    <td className="dim" style={{ maxWidth:'12.5rem', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                      {p.description}
                    </td>
                    <td>
                      <span className={`badge ${p.group === 'core' ? 'badge-ok' : 'badge-warn'}`}>
                        {p.group === 'core' ? '核心' : '插件'}
                      </span>
                    </td>
                    <td>
                      <span className={`badge ${p.active ? 'badge-ok' : 'badge-off'}`}>
                        {p.active ? '已激活' : '未激活'}
                      </span>
                    </td>
                    <td>
                      {p.group !== 'core' && p.id !== 'plugins' && (
                        <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => handleToggle(p.id, !p.active)}
                          style={{ fontSize:'0.6875rem', padding: '0.1875rem 0.5rem' }}>
                          {p.active ? '移除' : '接入'}
                        </button>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>
    </div>
  )
}