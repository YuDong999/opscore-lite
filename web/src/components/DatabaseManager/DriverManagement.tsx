import { useEffect, useState } from 'react'
import { getDrivers, type DriverInfo , installDriver } from './api'

export default function DriverManagement() {
  const [drivers, setDrivers] = useState<DriverInfo[]>([])
  const [loading, setLoading] = useState(false)
  const [err, setErr] = useState('')

  const reload = () => {
    setLoading(true)
    getDrivers()
      .then(d => setDrivers(d))
      .catch(e => setErr(e.message || '加载驱动列表失败'))
      .finally(() => setLoading(false))
  }
  useEffect(() => { reload() }, [])

  const [installing, setInstalling] = useState<string | null>(null)
  const handleInstall = (type: string) => {
    setInstalling(type)
    installDriver(type)
      .then(() => reload())
      .catch(e => setErr(`安装失败: ${e.message || e}`))
      .finally(() => setInstalling(null))
  }

  if (loading) return <div className="log-loading">加载驱动列表中...</div>
  if (err) return <div className="banner banner-err">{err}</div>
  if (!drivers.length) return <div className="db-empty">暂无驱动信息</div>

  const builtin = drivers.filter(d => d.builtin)
  const optional = drivers.filter(d => !d.builtin && d.status !== 'unknown')
  const unknown  = drivers.filter(d => d.status === 'unknown')

  return (
    <div className="db-drv-wrap">
      <div className="db-drv-header">
        <h3>驱动管理</h3>
        <span className="dim">共 {drivers.length} 种引擎驱动</span>
      </div>

      {builtin.length > 0 && (
        <div className="db-drv-group">
          <h4>内置驱动</h4>
          <div className="db-drv-grid">
            {builtin.map(d => (
              <DriverCard key={d.type} driver={d} onInstall={handleInstall} />
            ))}
          </div>
        </div>
      )}

      {optional.length > 0 && (
        <div className="db-drv-group">
          <h4>可选驱动</h4>
          <div className="db-drv-grid">
            {optional.map(d => (
              <DriverCard key={d.type} driver={d} onInstall={handleInstall} />
            ))}
          </div>
        </div>
      )}

      {unknown.length > 0 && (
        <div className="db-drv-group">
          <h4>未知 / 未检测</h4>
          <div className="db-drv-grid">
            {unknown.map(d => (
              <DriverCard key={d.type} driver={d} onInstall={handleInstall} />
            ))}
          </div>
        </div>
      )}
    </div>
  )
}

function DriverCard({ driver: d, onInstall, installing }: { driver: DriverInfo; onInstall?: (type: string) => void; installing?: string | null }) {
  const statusCls = d.builtin ? 'pill-ok' : d.installed ? 'pill-ok' : 'pill-err'
  const statusText = d.builtin ? '内置' : d.installed ? '已安装' : '未安装'
  const canInstall = !d.builtin && !d.installed && onInstall

  return (
    <div className="db-drv-card">
      <div className="db-drv-card-head">
        <span className="db-engine-dot" style={{ background: d.color || '#888' }} />
        <span className="db-drv-name">{d.label}</span>
        <span className={`pill ${statusCls}`}>{statusText}</span>
      </div>
      <div className="db-drv-meta dim">
        <span>{d.short}</span>
        <span>·</span>
        <span>{d.category}</span>
      </div>
      {d.reason && <div className="db-drv-reason dim">{d.reason}</div>}
      {canInstall && (
        <button
          className="btn-glass-soft btn-glass-soft-sm"
          style={{ marginTop: 6 }}
          disabled={installing === d.type}
          onClick={() => onInstall(d.type)}
          title="驱动实现已内置, 安装=写入启用标记, 即时生效"
        >
          {installing === d.type ? '安装中...' : '安装启用'}
        </button>
      )}
    </div>
  )
}
