import { Component, useEffect, useState, useMemo, type ReactNode } from 'react'
import { NavLink, Navigate, Route, Routes } from 'react-router-dom'
import { getJSON } from './api/client'
import TopBar from './components/TopBar'
import LoginPage from './components/LoginPage'
import { HostProvider } from './components/HostContext'
import { ToastProvider } from './components/Toast'
import ResourcesModule from './modules/ResourcesModule'
import ServicesModule from './modules/ServicesModule'
import NetworkModule from './modules/NetworkModule'
import PluginsModule from './modules/PluginsModule'
import SettingsModule from './modules/SettingsModule'
import DiagnosticsModule from './modules/DiagnosticsModule'
import TasksModule from './modules/TasksModule'
import AnsibleModule from './modules/AnsibleModule'
import ContainersModule from './modules/ContainersModule'
import DatabaseManagerModule from './modules/DatabaseManagerModule'
import CicdModule from './modules/CicdModule'
import LogMonitorModule from './modules/LogMonitorModule'

interface Manifest {
  id: string
  name: string
  icon: string
  routePath: string
  group: string
  description: string
}

const MODULE_MAP: Record<string, () => JSX.Element> = {
  resources: ResourcesModule,
  services: ServicesModule,
  network: NetworkModule,
  diagnostics: DiagnosticsModule,
  tasks: TasksModule,
  plugins: PluginsModule,
  ansible: AnsibleModule,
  containers: ContainersModule,
  dbmanager: DatabaseManagerModule,
  cicd: CicdModule,
  logmonitor: LogMonitorModule,
}

class ErrorBoundary extends Component<{ children: ReactNode }, { error: Error | null }> {
  state: { error: Error | null } = { error: null }

  static getDerivedStateFromError(error: Error) {
    return { error }
  }

  componentDidCatch(error: Error, info: React.ErrorInfo) {
    // eslint-disable-next-line no-console
    console.error('[Boundary]', error.message, 'componentStack:', info.componentStack)
  }

  render() {
    if (this.state.error) {
      return (
        <div className="banner banner-err" style={{ margin: 16 }}>
          模块渲染出错: {this.state.error.message}
          <button className="btn-glass-soft btn-glass-soft-sm" style={{ marginLeft: 8 }} onClick={() => this.setState({ error: null })}>
            重试
          </button>
        </div>
      )
    }
    return this.props.children
  }
}

export default function App() {
  const [modules, setModules] = useState<Manifest[]>([])
  const [authRequired, setAuthRequired] = useState<boolean | null>(null)
  const [authError, setAuthError] = useState<string>('')

  useEffect(() => {
    fetch('/api/auth/token')
      .then((r) => r.json())
      .then((d: any) => {
        if (d.configured === 'true' && !localStorage.getItem('opscore-token')) {
          setAuthRequired(true)
        } else {
          setAuthRequired(false)
          loadModules()
        }
      })
      .catch(() => {
        setAuthRequired(null)
        setAuthError('无法连接到服务端，请确认 opscore 正在运行')
      })
  }, [])

  useEffect(() => {
    const handler = () => loadModules()
    window.addEventListener('manifest-changed', handler)
    return () => window.removeEventListener('manifest-changed', handler)
  }, [])

  const loadModules = () => {
    getJSON<Manifest[]>('/api/manifest').then(setModules).catch(() => setModules([]))
  }

  const handleLogin = () => {
    setAuthRequired(false)
    loadModules()
  }

  const core = useMemo(() => modules.filter((m) => m.group === 'core'), [modules])
  const plugins = useMemo(() => modules.filter((m) => m.group === 'plugin'), [modules])

  if (authRequired === null) return <div className="log-loading">加载中...</div>
  if (authRequired) return <LoginPage onLogin={handleLogin} />

  return (
    <div className="layout">
      <aside className="sidebar">
        <div className="brand">
          <span className="brand-dot" />
          <div>
            <div className="brand-name">OpsCore</div>
            <div className="brand-sub">运维控制台</div>
          </div>
        </div>

        <div className="nav-group-label">核心模块</div>
        <nav>
          {core.map((m) => (
            <NavLink key={m.id} to={m.routePath} className="nav-item">
              <span className="nav-icon">{icon(m.icon, 20)}</span>
              <span className="nav-text">
                <span className="nav-title">{m.name}</span>
                <span className="nav-desc">{m.description}</span>
              </span>
            </NavLink>
          ))}
        </nav>

        {plugins.length > 0 && (
          <>
            <div className="nav-group-label">插件</div>
            <nav>
              {plugins.map((m) => (
                <NavLink key={m.id} to={m.routePath} className="nav-item nav-item-plugin">
                  <span className="nav-icon">{icon(m.icon, 16)}</span>
                  <span className="nav-text">
                    <span className="nav-title">{m.name}</span>
                    <span className="nav-desc">{m.description}</span>
                  </span>
                </NavLink>
              ))}
            </nav>
          </>
        )}
        <div className="sidebar-foot">编译期内置 · 其余可插拔</div>

        <nav style={{ marginTop: 'auto', paddingTop: 12, borderTop: '1px solid var(--border)' }}>
          <NavLink to="/settings" className="nav-item" style={{ fontSize:'0.8125rem', opacity: 0.7 }}>
            <span className="nav-icon">
              <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                <circle cx="12" cy="12" r="3" />
                <path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 0 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 0 1-2.83-2.83l.06-.06A1.65 1.65 0 0 0 4.68 15a1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 0 1 2.83-2.83l.06.06A1.65 1.65 0 0 0 9 4.68a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 0 1 2.83 2.83l-.06.06A1.65 1.65 0 0 0 19.4 9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z" />
              </svg>
            </span>
            <span className="nav-text">
              <span className="nav-title">设置</span>
              <span className="nav-desc">主题 · 认证</span>
            </span>
          </NavLink>
        </nav>
      </aside>

      <div className="main">
        <TopBar />
        <main className="content">
          <HostProvider>
          <ErrorBoundary>
          <ToastProvider>
          <Routes>
            <Route path="/" element={core[0] ? <Navigate to={core[0].routePath} replace /> : <div className="log-loading">加载中...</div>} />
            {modules.map((m) => {
              const Comp = MODULE_MAP[m.id]
              return Comp ? <Route key={m.id} path={m.routePath} element={<Comp />} /> : null
            })}
            {/* 旧版容器管理深链接兼容(routePath 已改为 /containers, 老书签仍指 /containers/docker|k8s) */}
            <Route path="/containers/docker" element={<Navigate to="/containers" replace />} />
            <Route path="/containers/k8s" element={<Navigate to="/containers" replace />} />
            <Route path="/settings" element={<SettingsModule />} />
            <Route path="*" element={modules.length === 0 ? <div className="log-loading">加载中...</div> : <Navigate to="/resources" replace />} />
          </Routes>
          </ToastProvider>
          </ErrorBoundary>
          </HostProvider>
        </main>
      </div>
    </div>
  )
}

function icon(name: string, size = 18) {  const s = size
  const icons: Record<string, JSX.Element> = {
    cpu: (
      <svg width={s} height={s} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
        <rect x="4" y="4" width="16" height="16" rx="2" />
        <rect x="9" y="9" width="6" height="6" />
        <path d="M15 2v2M9 2v2M15 20v2M9 20v2M2 15h2M2 9h2M20 15h2M20 9h2" />
      </svg>
    ),
    server: (
      <svg width={s} height={s} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
        <rect x="2" y="2" width="20" height="8" rx="2" />
        <rect x="2" y="14" width="20" height="8" rx="2" />
        <circle cx="6" cy="6" r="1" fill="currentColor" />
        <circle cx="6" cy="18" r="1" fill="currentColor" />
      </svg>
    ),
    network: (
      <svg width={s} height={s} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
        <path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z" />
        <path d="M9 12l2 2 4-4" />
      </svg>
    ),
    shield: (
      <svg width={s} height={s} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
        <path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z" />
        <path d="M12 8v4M12 16h.01" />
      </svg>
    ),
    activity: (
      <svg width={s} height={s} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
        <polyline points="22 12 18 12 15 21 9 3 6 12 2 12" />
      </svg>
    ),
    clipboard: (
      <svg width={s} height={s} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
        <path d="M16 4h2a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2h2" />
        <rect x="8" y="2" width="8" height="4" rx="1" ry="1" />
      </svg>
    ),
    puzzle: (
      <svg width={s} height={s} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
        <path d="M19.439 7.85c-.049.322.059.648.289.878l1.568 1.568c.47.47.706 1.087.706 1.704s-.235 1.233-.706 1.704l-1.611 1.611a.98.98 0 0 1-.837.276c-.47-.07-.802-.48-.968-.925a2.501 2.501 0 1 0-3.214 3.214c.446.166.855.497.925.968a.979.979 0 0 1-.276.837l-1.61 1.61a2.404 2.404 0 0 1-1.705.707 2.402 2.402 0 0 1-1.704-.706l-1.568-1.568a1.026 1.026 0 0 0-.877-.29c-.493.074-.84.504-1.02.968a2.5 2.5 0 1 1-3.237-3.237c.464-.18.894-.527.967-1.02a1.026 1.026 0 0 0-.289-.877l-1.568-1.568A2.402 2.402 0 0 1 1.998 12c0-.617.236-1.234.706-1.704L4.315 8.685a.98.98 0 0 1 .837-.276c.47.07.802.48.968.925a2.501 2.501 0 1 0 3.214-3.214c-.446-.166-.855-.497-.925-.968a.979.979 0 0 1 .276-.837l1.61-1.61a2.404 2.404 0 0 1 1.705-.706c.617 0 1.234.236 1.704.706l1.568 1.568c.23.23.556.338.877.29.493-.074.84-.504 1.02-.968a2.5 2.5 0 1 1 3.237 3.237c-.464.18-.894.527-.967 1.02Z" />
      </svg>
    ),
    terminal: (
      <svg width={s} height={s} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
        <polyline points="4 17 10 11 4 5" />
        <line x1="12" y1="19" x2="20" y2="19" />
      </svg>
    ),
    box: (
      <svg width={s} height={s} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
        <path d="M21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z" />
        <polyline points="3.27 6.96 12 12.01 20.73 6.96" />
        <line x1="12" y1="22.08" x2="12" y2="12" />
      </svg>
    ),
    database: (
      <svg width={s} height={s} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
        <ellipse cx="12" cy="5" rx="9" ry="3" />
        <path d="M3 5v6c0 1.66 4.03 3 9 3s9-1.34 9-3V5" />
        <path d="M3 11v6c0 1.66 4.03 3 9 3s9-1.34 9-3v-6" />
      </svg>
    ),
    cicd: (
      <svg width={s} height={s} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
        <path d="M4.5 16.5c-1.5 1.26-2 5-2 5s3.74-.5 5-2c.71-.84.7-2.13-.09-2.91a2.18 2.18 0 0 0-2.91-.09z" />
        <path d="m12 15-3-3a22 22 0 0 1 2-3.95A12.88 12.88 0 0 1 22 2c0 2.72-.78 7.5-6 11a22.35 22.35 0 0 1-4 2z" />
        <path d="M9 12H4s.55-3.03 2-4c1.62-1.08 5 0 5 0" />
        <path d="M12 15v5s3.03-.55 4-2c1.08-1.62 0-5 0-5" />
      </svg>
    ),
  }
  return icons[name] || <span>•</span>
}
