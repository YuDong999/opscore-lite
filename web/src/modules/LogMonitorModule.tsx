import { useEffect, useRef, useState } from 'react'
import { getJSON, postJSON } from '../api/client'

interface LogEntry {
  id: number
  ts: number
  level: string
  service: string
  source: string
  filePath: string
  offset: number
  size: number
  summary: string
  raw?: string
}

interface LogQueryResult {
  total: number
  items: LogEntry[]
  tookMs: number
}

interface ServiceStat {
  service: string
  count: number
  levels: Record<string, number>
}

interface LogStats {
  totalCount: number
  levelCounts: Record<string, number>
  services: ServiceStat[]
  oldest: number | null
  newest: number | null
  totalBytes: number
}

interface HistogramBucket {
  ts: number
  count: Record<string, number>
}

interface LogStatsResult {
  stats: LogStats
  histogram: HistogramBucket[]
}

interface LogSource {
  id: string
  name: string
  type: string
  path: string
  service: string
  enabled: boolean
  follow: boolean
}

const LEVELS = ['ERROR', 'WARN', 'INFO', 'DEBUG', 'FATAL']

const LEVEL_COLOR: Record<string, string> = {
  ERROR: '#ff4d4f',
  FATAL: '#a8071a',
  WARN: '#faad14',
  INFO: '#1677ff',
  DEBUG: '#8c8c8c',
}

function fmtTime(ts: number): string {
  if (!ts) return '-'
  const d = new Date(ts)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

function fmtBytes(n: number): string {
  if (n < 1024) return n + ' B'
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KB'
  if (n < 1024 * 1024 * 1024) return (n / 1024 / 1024).toFixed(1) + ' MB'
  return (n / 1024 / 1024 / 1024).toFixed(2) + ' GB'
}

export default function LogMonitorModule() {
  const [tab, setTab] = useState<'search' | 'stats' | 'sources'>('search')

  // 查询条件
  const [service, setService] = useState('')
  const [level, setLevel] = useState('')
  const [keyword, setKeyword] = useState('')
  const [source, setSource] = useState('')
  const [hours, setHours] = useState(24)
  const [page, setPage] = useState(1)
  const pageSize = 100

  const [result, setResult] = useState<LogQueryResult | null>(null)
  const [loading, setLoading] = useState(false)
  const [err, setErr] = useState('')

  // 统计
  const [stats, setStats] = useState<LogStatsResult | null>(null)
  const [statsService, setStatsService] = useState('')

  // 源管理
  const [sources, setSources] = useState<LogSource[]>([])

  // 详情
  const [detailId, setDetailId] = useState<number | null>(null)
  const [detailRaw, setDetailRaw] = useState('')
  const [detailTs, setDetailTs] = useState(0)
  const [detailLvl, setDetailLvl] = useState('')
  const [detailSvc, setDetailSvc] = useState('')

  const latestHist = useRef<HistogramBucket[]>([])

  async function runSearch() {
    setLoading(true)
    setErr('')
    try {
      const now = Date.now()
      const startTs = hours > 0 ? now - hours * 3600 * 1000 : 0
      const params = new URLSearchParams()
      if (service) params.set('service', service)
      if (level) params.set('level', level)
      if (source) params.set('source', source)
      if (keyword) params.set('keyword', keyword)
      if (startTs > 0) params.set('startTs', String(startTs))
      params.set('page', String(page))
      params.set('pageSize', String(pageSize))
      const r = await getJSON<LogQueryResult>('/api/logmonitor/query?' + params.toString())
      setResult(r)
    } catch (e: any) {
      setErr(e?.message || '查询失败')
    } finally {
      setLoading(false)
    }
  }

  async function loadStats(svc: string) {
    const params = new URLSearchParams()
    if (svc) params.set('service', svc)
    params.set('bucketMs', String(60000 * 5)) // 5 分钟桶
    try {
      const r = await getJSON<LogStatsResult>('/api/logmonitor/stats?' + params.toString())
      setStats(r)
      latestHist.current = r.histogram
    } catch (e: any) {
      setErr(e?.message || '加载统计失败')
    }
  }

  async function loadSources() {
    try {
      const r = await getJSON<LogSource[]>('/api/logmonitor/sources')
      setSources(r)
    } catch (e: any) {
      setErr(e?.message || '加载源失败')
    }
  }

  async function scanFile() {
    const path = window.prompt('输入要扫描的日志文件绝对路径(如 /var/log/syslog):')
    if (!path) return
    const svc = window.prompt('该文件属于哪个服务?(留空自动从日志提取)', '') || ''
    try {
      const r = await postJSON('/api/logmonitor/scan', { path, service: svc, source: 'file' })
      window.alert(`扫描完成, 共入库 ${r.scanned} 条日志`)
      runSearch()
    } catch (e: any) {
      window.alert('扫描失败: ' + (e?.message || ''))
    }
  }

  async function viewDetail(id: number) {
    setDetailId(id)
    try {
      const r = await getJSON<LogEntry>('/api/logmonitor/raw?id=' + id)
      setDetailRaw(r.raw || r.summary)
      setDetailTs(r.ts)
      setDetailLvl(r.level)
      setDetailSvc(r.service)
    } catch (e: any) {
      setDetailRaw('读取失败: ' + (e?.message || ''))
    }
  }

  async function addSource() {
    const name = window.prompt('日志源名称:')
    if (!name) return
    const path = window.prompt('文件路径 / 容器名 / URL:')
    if (!path) return
    const svc = window.prompt('所属服务:') || ''
    const type = window.prompt('类型 (file/syslog/http/container):', 'file') || 'file'
    try {
      await postJSON('/api/logmonitor/sources/save', {
        id: '',
        name,
        type,
        path,
        service: svc,
        enabled: true,
        follow: false,
      })
      loadSources()
    } catch (e: any) {
      window.alert('添加失败: ' + (e?.message || ''))
    }
  }

  async function delSource(id: string) {
    if (!window.confirm('确定删除该日志源?')) return
    try {
      await postJSON('/api/logmonitor/sources/delete', { id })
      loadSources()
    } catch (e: any) {
      window.alert('删除失败: ' + (e?.message || ''))
    }
  }

  useEffect(() => {
    runSearch()
  }, [page])

  useEffect(() => {
    if (tab === 'stats') loadStats('')
    if (tab === 'sources') loadSources()
  }, [tab])

  // ── 渲染图表 ──
  function renderHistogram() {
    const hist = latestHist.current
    if (!hist || hist.length === 0) return <div className="log-empty">暂无数据</div>
    const colors = { ERROR: '#ff4d4f', WARN: '#faad14', INFO: '#1677ff', DEBUG: '#8c8c8c', FATAL: '#a8071a' }
    const max = Math.max(1, ...hist.flatMap((b) => Object.values(b.count)))
    const width = 100 / hist.length
    return (
      <div className="log-hist">
        {hist.map((b) => {
          const total = Object.values(b.count).reduce((a, c) => a + c, 0)
          return (
            <div key={b.ts} className="log-hist-col" style={{ width: width + '%' }} title={`${fmtTime(b.ts)} 共 ${total} 条`}>
              {Object.entries(b.count).map(([lvl, cnt]) => (
                <div
                  key={lvl}
                  className="log-hist-seg"
                  style={{ height: (cnt / max) * 100 + '%', backgroundColor: colors[lvl] || '#888' }}
                  title={`${fmtTime(b.ts)} ${lvl}: ${cnt}`}
                />
              ))}
            </div>
          )
        })}
      </div>
    )
  }

  const st = stats?.stats

  return (
    <div className="module">
      <div className="module-header">
        <h2>日志监控</h2>
      </div>

      <div className="tabs compact-tabs">
        <button className={`tab ${tab === 'search' ? 'tab-on' : ''}`} onClick={() => setTab('search')}>多条件检索</button>
        <button className={`tab ${tab === 'stats' ? 'tab-on' : ''}`} onClick={() => setTab('stats')}>统计总览</button>
        <button className={`tab ${tab === 'sources' ? 'tab-on' : ''}`} onClick={() => setTab('sources')}>日志源管理</button>
        <button className="btn-glass-soft btn-glass-soft-sm" style={{ marginLeft: 'auto' }} onClick={scanFile}>扫描文件入库</button>
      </div>

      {err && <div className="banner banner-err log-banner">{err}</div>}

      {tab === 'search' && (
        <div className="glass log-card">
          <div className="log-filter-row">
            <input
              className="input log-input"
              placeholder="服务名 (如 order-api)"
              value={service}
              onChange={(e) => setService(e.target.value)}
            />
            <select className="input log-input" value={level} onChange={(e) => setLevel(e.target.value)}>
              <option value="">全部级别</option>
              {LEVELS.map((l) => (
                <option key={l} value={l}>{l}</option>
              ))}
            </select>
            <input
              className="input log-input"
              placeholder="来源 (file/container/syslog)"
              value={source}
              onChange={(e) => setSource(e.target.value)}
            />
            <input
              className="input log-input log-keyword"
              placeholder="关键字 (日志内容模糊匹配)"
              value={keyword}
              onChange={(e) => setKeyword(e.target.value)}
            />
            <select className="input log-input log-hours" value={hours} onChange={(e) => setHours(Number(e.target.value))}>
              <option value={1}>最近 1 小时</option>
              <option value={6}>最近 6 小时</option>
              <option value={24}>最近 24 小时</option>
              <option value={72}>最近 3 天</option>
              <option value={168}>最近 7 天</option>
              <option value={0}>全部时间</option>
            </select>
            <button className="btn-glass btn-sm" onClick={runSearch} disabled={loading}>
              {loading ? '查询中...' : '查询'}
            </button>
          </div>

          {result && (
            <div className="log-meta">
              共 {result.total} 条 · 耗时 {result.tookMs.toFixed(1)} ms
            </div>
          )}

          {result && result.items.length > 0 && (
            <div className="log-table-wrap">
              <table className="log-table">
                <thead>
                  <tr>
                    <th style={{ width: 170 }}>时间</th>
                    <th style={{ width: 70 }}>级别</th>
                    <th style={{ width: 140 }}>服务</th>
                    <th style={{ width: 90 }}>来源</th>
                    <th>日志内容</th>
                  </tr>
                </thead>
                <tbody>
                  {result.items.map((e) => (
                    <tr key={e.id} onClick={() => viewDetail(e.id)} className="log-row">
                      <td className="log-ts">{fmtTime(e.ts)}</td>
                      <td>
                        <span className="log-level" style={{ color: LEVEL_COLOR[e.level] || '#888' }}>{e.level}</span>
                      </td>
                      <td className="log-svc">{e.service || '-'}</td>
                      <td className="log-src">{e.source || '-'}</td>
                      <td className="log-summary">{e.summary}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
              {result.total > pageSize && (
                <div className="log-pager">
                  <button className="btn-glass-soft btn-glass-soft-sm" disabled={page <= 1} onClick={() => setPage(page - 1)}>上一页</button>
                  <span>第 {page} 页 / 共 {Math.ceil(result.total / pageSize)} 页</span>
                  <button className="btn-glass-soft btn-glass-soft-sm" disabled={page * pageSize >= result.total} onClick={() => setPage(page + 1)}>下一页</button>
                </div>
              )}
            </div>
          )}
        </div>
      )}

      {tab === 'stats' && (
        <div className="glass log-card">
          <div className="log-filter-row">
            <span style={{ marginRight: 8 }}>服务过滤:</span>
            <input
              className="input log-input log-svc-filter"
              placeholder="输入服务名过滤"
              value={statsService}
              onChange={(e) => setStatsService(e.target.value)}
            />
            <button className="btn-glass btn-sm" onClick={() => loadStats(statsService)} disabled={loading}>
              {loading ? '加载中...' : '重新统计'}
            </button>
          </div>

          {st && (
            <>
              <div className="log-stat-grid">
                <div className="log-stat-box">
                  <div className="log-stat-num">{st.totalCount.toLocaleString()}</div>
                  <div className="log-stat-label">日志总数</div>
                </div>
                <div className="log-stat-box">
                  <div className="log-stat-num">{fmtBytes(st.totalBytes)}</div>
                  <div className="log-stat-label">索引字节</div>
                </div>
                <div className="log-stat-box">
                  <div className="log-stat-num">{st.oldest ? fmtTime(st.oldest) : '-'}</div>
                  <div className="log-stat-label">最早时间</div>
                </div>
                <div className="log-stat-box">
                  <div className="log-stat-num">{st.newest ? fmtTime(st.newest) : '-'}</div>
                  <div className="log-stat-label">最新时间</div>
                </div>
              </div>

              {Object.keys(st.levelCounts).length > 0 && (
                <div className="log-level-bars">
                  {Object.entries(st.levelCounts).map(([lvl, cnt]) => (
                    <div key={lvl} className="log-level-bar-row">
                      <span className="log-level-bar-lbl" style={{ color: LEVEL_COLOR[lvl] || '#888' }}>{lvl}</span>
                      <div className="log-level-bar-track">
                        <div className="log-level-bar-fill" style={{ width: (cnt / st.totalCount) * 100 + '%', backgroundColor: LEVEL_COLOR[lvl] || '#888' }} />
                      </div>
                      <span className="log-level-bar-num">{cnt.toLocaleString()}</span>
                    </div>
                  ))}
                </div>
              )}

              <div className="log-hist-wrap">
                <div className="log-hist-title">日志量趋势 (每 5 分钟)</div>
                {renderHistogram()}
              </div>

              {st.services.length > 0 && (
                <div className="log-services">
                  <div className="log-section-title">各服务日志量 Top</div>
                  <table className="log-table log-table-sm">
                    <thead>
                      <tr>
                        <th>服务</th>
                        <th>总条数</th>
                        {LEVELS.map((l) => (
                          <th key={l}>{l}</th>
                        ))}
                      </tr>
                    </thead>
                    <tbody>
                      {st.services.slice(0, 10).map((s) => (
                        <tr key={s.service}>
                          <td className="log-svc">{s.service || '(未标注)'}</td>
                          <td>{s.count.toLocaleString()}</td>
                          {LEVELS.map((l) => (
                            <td key={l} style={{ color: (s.levels || {})[l] ? LEVEL_COLOR[l] : undefined }}>
                              {((s.levels || {})[l] || 0).toLocaleString()}
                            </td>
                          ))}
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </>
          )}
        </div>
      )}

      {tab === 'sources' && (
        <div className="glass log-card">
          <div className="log-filter-row">
            <button className="btn-glass btn-sm" onClick={addSource}>新增日志源</button>
          </div>
          {sources.length === 0 ? (
            <div className="log-empty">暂无日志源。可添加文件/容器/syslog 源, 或在检索页直接"扫描文件入库"</div>
          ) : (
            <table className="log-table">
              <thead>
                <tr>
                  <th>名称</th>
                  <th>类型</th>
                  <th>路径/标识</th>
                  <th>服务</th>
                  <th>状态</th>
                  <th style={{ width: 80 }}>操作</th>
                </tr>
              </thead>
              <tbody>
                {sources.map((s) => (
                  <tr key={s.id}>
                    <td>{s.name}</td>
                    <td>{s.type}</td>
                    <td className="log-mono">{s.path || '-'}</td>
                    <td>{s.service || '-'}</td>
                    <td>
                      <span className={`dot ${s.enabled ? 'dot-ok' : 'dot-off'}`} /> {s.enabled ? '启用' : '停用'}
                    </td>
                    <td>
                      <button className="btn-glass-soft btn-glass-soft-danger btn-glass-soft-sm" onClick={() => delSource(s.id)}>删除</button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      )}

      {detailId !== null && (
        <div className="log-modal-mask" onClick={() => setDetailId(null)}>
          <div className="log-modal" onClick={(e) => e.stopPropagation()}>
            <div className="log-modal-head">
              <strong>
                #{detailId} · <span style={{ color: LEVEL_COLOR[detailLvl] || '#888' }}>{detailLvl}</span> · {detailSvc || '未知服务'} · {fmtTime(detailTs)}
              </strong>
              <button className="btn-glass-soft btn-glass-soft-sm" onClick={() => setDetailId(null)}>关闭</button>
            </div>
            <pre className="log-raw">{detailRaw || '加载中...'}</pre>
          </div>
        </div>
      )}
    </div>
  )
}
