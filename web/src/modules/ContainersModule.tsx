// ── 容器管理插件: 侧栏单级入口 → 页内一级 Tab(Docker / K8S) ──
// Docker 视图与 K8S 视图均为独立内嵌侧栏布局(对齐 kubevision)

import { useEffect, useState } from 'react'
import DockerModule from './DockerModule'
import K8sModule from './K8sModule'

export default function ContainersModule() {
  const [view, setView] = useState<'docker' | 'k8s'>('docker')
  const [msg, setMsg] = useState('')

  // 提示条自动消失(含 join-token 的长消息给 12s)
  useEffect(() => {
    if (!msg) return
    const t = setTimeout(() => setMsg(''), msg.includes('\n') ? 12000 : 5000)
    return () => clearTimeout(t)
  }, [msg])

  return (
    <div className="module">
      <div className="module-head" style={{ flexWrap: 'wrap', gap: '0.5rem 1rem' }}>
        <h2 style={{ marginRight: 0 }}>容器管理</h2>
        <div className="view-switch">
          <button className={view === 'docker' ? 'active' : ''} onClick={() => setView('docker')}>Docker</button>
          <button className={view === 'k8s' ? 'active' : ''} onClick={() => setView('k8s')}>Kubernetes</button>
        </div>
      </div>

      {msg && <div className={`banner ${msg.startsWith('✓') ? 'banner-ok' : 'banner-err'}`}>{msg}</div>}

      {view === 'docker' ? <DockerModule onMsg={setMsg} /> : <K8sModule onMsg={setMsg} />}
    </div>
  )
}
