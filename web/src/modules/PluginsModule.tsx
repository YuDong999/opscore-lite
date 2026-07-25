import { useEffect, useState } from 'react'
import Card from '../components/Card'

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
          <button className="btn btn-accent" onClick={handleScan} disabled={scanning}>
            {scanning ? '扫描中...' : '扫描插件'}
          </button>
          <span className="dim" style={{ fontSize: 12, alignSelf: 'center' }}>
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
                    <td><span className="nav-icon" style={{ width: 24, height: 24, fontSize: 12 }}>
                      {p.icon === 'cpu' && '⚙'}
                      {p.icon === 'server' && '🖥'}
                      {p.icon === 'network' && '🌐'}
                      {p.icon === 'activity' && '📊'}
                      {p.icon === 'clipboard' && '📋'}
                      {p.icon === 'puzzle' && '🧩'}
                    </span></td>
                    <td>{p.name}</td>
                    <td className="dim" style={{ maxWidth: 200, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
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
                        <button className="btn btn-sm" onClick={() => handleToggle(p.id, !p.active)}
                          style={{ fontSize: 11, padding: '3px 8px' }}>
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