// 数据库管理模块首页: 未选连接时展示。
// 展示连接数/引擎分布/快速新建入口/安全提示。

import { type ConnectionInfo, getEngineMeta, ENGINES, statusLabel, type EngineCategory, type EngineMeta } from './api'

const CATEGORY_LABEL: Record<EngineCategory, string> = {
  relational: '关系型',
  document: '文档型',
  vector: '向量库',
  timeseries: '时序',
  search: '搜索',
  mq: '消息队列',
  custom: '自定义',
}

const CATEGORY_ORDER: EngineCategory[] = ['relational', 'document', 'timeseries', 'vector', 'search', 'mq', 'custom']

export default function OverviewPanel({
  conns,
  onNewConn,
  onPickConn,
}: {
  conns: ConnectionInfo[]
  onNewConn: () => void
  onPickConn: (c: ConnectionInfo) => void
}) {
  const total = conns.length
  const byCategory = (cat: EngineCategory) => conns.filter(c => {
    const meta = getEngineMeta(c.engine)
    return meta?.category === cat
  }).length

  const stats: Array<{ label: string; value: number; color: string }> = [
    { label: '连接总数', value: total, color: 'var(--accent)' },
    { label: '关系型', value: byCategory('relational'), color: 'var(--accent-2)' },
    { label: '文档/时序', value: byCategory('document') + byCategory('timeseries'), color: '#10b981' },
    { label: '向量库', value: byCategory('vector'), color: '#a78bfa' },
    { label: '搜索/MQ', value: byCategory('search') + byCategory('mq'), color: '#fb923c' },
    { label: '自定义', value: byCategory('custom'), color: '#94a3b8' },
  ]

  const grouped: Record<EngineCategory, EngineMeta[]> = { relational: [], document: [], vector: [], timeseries: [], search: [], mq: [], custom: [] }
  ENGINES.forEach(e => grouped[e.category].push(e))

  return (
    <div className="db-overview">
      <div className="db-overview-hero">
        <div className="db-overview-title">
          <span className="db-overview-ico">
            <svg width="32" height="32" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
              <ellipse cx="12" cy="5" rx="9" ry="3" />
              <path d="M3 5v6c0 1.66 4.03 3 9 3s9-1.34 9-3V5" />
              <path d="M3 11v6c0 1.66 4.03 3 9 3s9-1.34 9-3v-6" />
            </svg>
          </span>
          <div>
            <h1>数据库管理</h1>
            <p className="dim">统一管理 12 种数据源, 可视化查询 + 风险拦截 + 审计追溯</p>
          </div>
        </div>
        <button className="btn-glass-soft btn-glass-soft-accent" onClick={onNewConn}>+ 新建连接</button>
      </div>

      <div className="db-overview-stats" style={{ gridTemplateColumns: 'repeat(6, 1fr)' }}>
        {stats.map(s => (
          <div key={s.label} className="db-stat-card">
            <div className="db-stat-value" style={{ color: s.color }}>{s.value}</div>
            <div className="db-stat-label">{s.label}</div>
          </div>
        ))}
      </div>

      {total > 0 ? (
        <div className="db-overview-section">
          <div className="db-overview-section-title">已有连接</div>
          <div className="db-conn-quick-grid">
            {conns.map(c => {
              const meta = getEngineMeta(c.engine)
              return (
                <div key={c.id} className="db-conn-quick-card" onClick={() => onPickConn(c)}>
                  <div className="db-conn-quick-name">
                    <span className={`db-engine-badge db-engine-${c.engine}`}>{meta?.label.split(' ')[0] || c.engine}</span>
                    {c.name}
                  </div>
                  <div className="db-conn-quick-host dim">
                    {c.config.username ? `${c.config.username}@` : ''}{c.config.host}:{c.config.port}
                    {c.config.database && ` / ${c.config.database}`}
                  </div>
                </div>
              )
            })}
          </div>
        </div>
      ) : (
        <div className="db-overview-section">
          <div className="db-overview-section-title">快速开始</div>
          <div className="db-engine-quick-grid">
            <button className="db-engine-quick db-engine-mysql" onClick={onNewConn}>
              <div className="db-engine-quick-name">MySQL / MariaDB</div>
              <div className="db-engine-quick-meta">3306 · SQL</div>
            </button>
            <button className="db-engine-quick db-engine-postgres" onClick={onNewConn}>
              <div className="db-engine-quick-name">PostgreSQL</div>
              <div className="db-engine-quick-meta">5432 · SQL</div>
            </button>
            <button className="db-engine-quick db-engine-oracle" onClick={onNewConn}>
              <div className="db-engine-quick-name">Oracle</div>
              <div className="db-engine-quick-meta">1521 · SQL</div>
            </button>
            <button className="db-engine-quick db-engine-milvus" onClick={onNewConn}>
              <div className="db-engine-quick-name">Milvus</div>
              <div className="db-engine-quick-meta">19530 · 向量</div>
            </button>
            <button className="db-engine-quick db-engine-kafka" onClick={onNewConn}>
              <div className="db-engine-quick-name">Kafka</div>
              <div className="db-engine-quick-meta">9092 · MQ</div>
            </button>
            <button className="db-engine-quick db-engine-custom" onClick={onNewConn}>
              <div className="db-engine-quick-name">更多引擎...</div>
              <div className="db-engine-quick-meta">{ENGINES.length} 种支持</div>
            </button>
          </div>
        </div>
      )}

      <div className="db-overview-section">
        <div className="db-overview-section-title">支持的引擎 <span className="dim" style={{ fontWeight: 400, fontSize: '0.6875rem' }}>· 内置 = 开箱即用 · 可启用 = 需在驱动管理中安装 · 需驱动 = 未包含</span></div>
        <div className="db-engine-table">
          {CATEGORY_ORDER.filter(c => grouped[c].length > 0).map(cat => (
            <div key={cat} className="db-engine-cat">
              <div className="db-engine-cat-label">
                {CATEGORY_LABEL[cat]}
                <span className="db-engine-group-count" style={{ marginLeft: 6 }}>{grouped[cat].length}</span>
              </div>
              <div className="db-engine-cat-list">
                {grouped[cat].map(e => {
                  const sl = statusLabel(e.status)
                  return (
                    <div key={e.type} className="db-engine-cat-item" title={e.description + (e.reason ? `\n${e.reason}` : '')}>
                      <span className={`db-engine-badge db-engine-${e.type}`}>{e.short}</span>
                      <span className={`pill ${sl.cls}`} style={{ fontSize: '0.5rem', padding: '0 0.25rem' }}>{sl.text}</span>
                      <span className="dim" style={{ fontSize: '0.6875rem' }}>{e.defaultPort > 0 ? `:${e.defaultPort}` : 'DSN'}</span>
                    </div>
                  )
                })}
              </div>
            </div>
          ))}
        </div>
      </div>

      <div className="db-overview-tips">
        <div className="db-tip">
          <span className="db-tip-icon">🔒</span>
          <div>
            <div className="db-tip-title">写操作默认锁定</div>
            <div className="db-tip-body dim">执行 INSERT/UPDATE/DELETE/DDL 前需主动解锁(临时授权), 期限一到自动回落</div>
          </div>
        </div>
        <div className="db-tip">
          <span className="db-tip-icon">📋</span>
          <div>
            <div className="db-tip-title">全量审计</div>
            <div className="db-tip-body dim">所有 SQL 写入审计日志, 包含风险分级与决策记录</div>
          </div>
        </div>
        <div className="db-tip">
          <span className="db-tip-icon">⚙️</span>
          <div>
            <div className="db-tip-title">环境感知</div>
            <div className="db-tip-body dim">生产库(名称/标签包含 prod)执行高危操作自动升级二次确认</div>
          </div>
        </div>
      </div>
    </div>
  )
}
