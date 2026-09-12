// 集群证书体检 + 续签
//   kubeadm: 体检 GET /api/plugins/containers/k8s/certs/inspect · 续签 POST /certs/renew {cluster, confirm:"renew-certs"}
//   binary : 计划 POST /certs/binary/plan {cluster} · 续签 POST /certs/binary/renew {cluster, confirm, certs}
//           回滚 POST /certs/binary/rollback {cluster, confirm:"rollback", backup}
// 续签是写操作(经 SSH 到 master), 前端强制二次确认, 后端同样校验 confirm 口令。

import { useEffect, useState } from 'react'
import { getJSON, postJSON } from '../api/client'

interface CertRow {
  name: string
  path: string
  type: string
  expires: string
  remain: string
  issuer: string
  sha1: string
}

interface CertPlanRow {
  path: string
  name: string
  issuer: string
  ca: string
  caExpires: string
  key: string
  expires: string
  remain: string
  san: string
  renewable: boolean
  reason?: string
}

interface BinNode {
  nodeName: string
  hostID?: string
  mode: string
  status: string
  before?: string[]
  after?: string[]
  steps?: string[]
  backup?: string
  error?: string
}

interface CertsRes {
  ok: boolean
  error?: string
  master?: { mode: string; hostID: string; reason?: string }
  install?: { flavor: string; certDir: string; kubeadm?: string }
  certs?: CertRow[]
  steps?: string[]
  kubePath?: string
}

interface BinPlanRes {
  ok: boolean
  error?: string
  master?: { mode: string; hostID: string; reason?: string }
  install?: { flavor: string; certDir: string; kubeadm?: string }
  nodes?: BinNode[]
  plan?: CertPlanRow[]
  days?: number
}

interface BinRenewRes {
  ok: boolean
  error?: string
  nodes?: BinNode[]
}

interface K8sCertsModalProps {
  cluster: string
  clusterName: string
  onClose: () => void
  onMsg?: (m: string) => void
}

const TYPE_LABEL: Record<string, string> = {
  'ca-root': '根CA',
  'apiserver': 'API Server',
  'apiserver-kubelet-client': 'apiserver→kubelet',
  'etcd': 'etcd',
  'front-proxy': 'front-proxy',
  'kubelet': 'kubelet',
  'leaf': '叶证书',
  'sa-public-key': 'SA公钥',
}

function remainColor(remain: string): string {
  if (remain.startsWith('已过期')) return 'var(--danger, #e5484d)'
  if (/^\d+h/.test(remain) || /^0d/.test(remain)) return 'var(--warn, #f2a52b)'
  return undefined as any
}

export default function K8sCertsModal({ cluster, clusterName, onClose, onMsg }: K8sCertsModalProps) {
  const [data, setData] = useState<CertsRes | null>(null)
  const [busy, setBusy] = useState(false)
  const [confirmOpen, setConfirmOpen] = useState(false)
  const [confirmText, setConfirmText] = useState('')
  const [steps, setSteps] = useState<string[] | null>(null)

  // ── 二进制续签模式 ──
  const [planRes, setPlanRes] = useState<BinPlanRes | null>(null)
  const [planBusy, setPlanBusy] = useState(false)
  const [checked, setChecked] = useState<Set<string>>(new Set())
  const [binNodes, setBinNodes] = useState<BinNode[] | null>(null)
  const [binConfirmOpen, setBinConfirmOpen] = useState(false)
  const [binConfirmText, setBinConfirmText] = useState('')
  const [rbTarget, setRbTarget] = useState<string | null>(null)
  const [rbConfirmText, setRbConfirmText] = useState('')
  const [binErr, setBinErr] = useState('')

  const load = async () => {
    setBusy(true)
    try {
      const res = await getJSON<CertsRes>(`/api/plugins/containers/k8s/certs/inspect?cluster=${encodeURIComponent(cluster)}&_=${Date.now()}`)
      setData(res)
    } catch (e) {
      setData({ ok: false, error: String(e) })
    } finally {
      setBusy(false)
    }
  }

  useEffect(() => { load() }, [cluster]) // eslint-disable-line react-hooks/exhaustive-deps

  const renew = async () => {
    if (confirmText.trim() !== 'renew-certs') return
    setBusy(true)
    try {
      const res = await postJSON<CertsRes>('/api/plugins/containers/k8s/certs/renew', { cluster, confirm: 'renew-certs' })
      setSteps(res.steps || [])
      if (res.ok) {
        onMsg?.(`集群 ${clusterName} 证书已续签，请在集群列表刷新确认`)
      } else {
        onMsg?.(`续签失败: ${res.error || '未知错误'}`)
      }
      setConfirmOpen(false)
      setConfirmText('')
      setData(res)
    } catch (e) {
      setData({ ok: false, error: String(e) })
    } finally {
      setBusy(false)
    }
  }

  // ── 二进制: 生成计划(只读) ──
  const genPlan = async () => {
    setPlanBusy(true)
    setBinErr('')
    setBinNodes(null)
    setRbTarget(null)
    try {
      const res = await postJSON<BinPlanRes>('/api/plugins/containers/k8s/certs/binary/plan', { cluster })
      setPlanRes(res)
      if (res.ok && res.plan) {
        // 默认勾选: 可续 且 过期/临期(<30d); CA/不可续的天然排除
        const def = new Set<string>(res.plan.filter((c) => c.renewable && (c.remain.startsWith('已过期') || /^\d+d/.test(c.remain))).map((c) => c.path))
        setChecked(def)
      }
    } catch (e) {
      setPlanRes({ ok: false, error: String(e) })
    } finally {
      setPlanBusy(false)
    }
  }

  const toggle = (path: string) => {
    setChecked((prev) => {
      const n = new Set(prev)
      n.has(path) ? n.delete(path) : n.add(path)
      return n
    })
  }

  // ── 二进制: 执行滚动续签 ──
  const binRenew = async () => {
    if (binConfirmText.trim() !== 'renew-certs' || checked.size === 0) return
    setPlanBusy(true)
    setBinErr('')
    try {
      const res = await postJSON<BinRenewRes>('/api/plugins/containers/k8s/certs/binary/renew', {
        cluster, confirm: 'renew-certs', certs: Array.from(checked),
      })
      setBinNodes(res.nodes || [])
      if (res.ok) {
        onMsg?.(`集群 ${clusterName} 证书已滚动续签(${checked.size} 份)，请刷新确认`)
      } else if (res.error) {
        setBinErr(res.error)
      }
      setBinConfirmOpen(false)
      setBinConfirmText('')
    } catch (e) {
      setBinErr(String(e))
    } finally {
      setPlanBusy(false)
    }
  }

  // ── 二进制: 一键恢复备份 ──
  const binRollback = async (backup: string) => {
    if (rbConfirmText.trim() !== 'rollback') return
    setPlanBusy(true)
    try {
      const res = await postJSON<BinRenewRes>('/api/plugins/containers/k8s/certs/binary/rollback', {
        cluster, confirm: 'rollback', backup,
      })
      if (res.ok) {
        onMsg?.(`集群 ${clusterName} 已恢复备份 ${backup}，请刷新确认`)
      } else {
        setBinErr(res.error || '回滚失败')
      }
      setRbTarget(null)
      setRbConfirmText('')
    } catch (e) {
      setBinErr(String(e))
    } finally {
      setPlanBusy(false)
    }
  }

  const expired = (data?.certs || []).filter((c) => c.remain.startsWith('已过期'))
  const soon = (data?.certs || []).filter((c) => !c.remain.startsWith('已过期') && /^\d+d/.test(c.remain))
  const flavor = data?.install?.flavor
  const arenewable = flavor === undefined || flavor === 'kubeadm'
  const isBinary = flavor === 'binary'
  const plan = planRes?.ok ? planRes.plan || [] : []

  return (
    <div className="modal-overlay" onClick={() => { if (!busy && !planBusy) onClose() }} onWheel={(e) => e.stopPropagation()}>
      <div className="modal" onClick={(e) => e.stopPropagation()} style={{ maxWidth: 900, width: '94vw', maxHeight: '80vh', display: 'flex', flexDirection: 'column' }}>
        <div className="modal-head">
          <div className="modal-title">证书管理 — {clusterName}</div>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap', justifyContent: 'flex-end' }}>
            <div style={{ display: 'flex', gap: 8, alignItems: 'center', minWidth: 0, flexWrap: 'wrap' }}>
              {data?.master && (
                <span className="dim" style={{ fontSize: '0.6875rem', whiteSpace: 'nowrap' }} title={data.master.reason}>
                  执行节点: {data.master.mode === 'ssh' ? `SSH → ${data.master.hostID}` : '本机(local)'}
                </span>
              )}
              {data?.install && (
                <span className="dim" style={{ fontSize: '0.6875rem', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }} title={data.install.certDir}>
                  安装形态: <b>{data.install.flavor}</b> · 证书目录 <code className="mono">{data.install.certDir || '未检测到'}</code>
                </span>
              )}
            </div>
            <div style={{ display: 'flex', gap: 6, flexShrink: 0 }}>
              <button className="btn-glass-soft" onClick={load} disabled={busy || planBusy}>{busy ? '加载中…' : '重新体检'}</button>
              <button className="btn-glass-soft" onClick={onClose} disabled={busy || planBusy}>关闭</button>
            </div>
          </div>
        </div>

        <div style={{ padding: '0.75rem 1.25rem 1rem', overflow: 'auto', display: 'flex', flexDirection: 'column', gap: '0.75rem' }}>
          {data?.error && (
            <div className="banner banner-err">
              体检失败: {data.error}
              {data.master?.mode === 'local' && (
                <span className="dim" style={{ display: 'block', fontSize: '0.75rem', marginTop: 4 }}>
                  当前按本机执行（apiserver 地址未在主机清单中反查到）。若本服务未运行在 master 上，请先在主机清单登记 master 主机的 IP/hostname。
                </span>
              )}
            </div>
          )}

          {data?.ok && (
            <>
              <div className="banner" style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 8, padding: '0.5rem 0.75rem' }}>
                <span>共 {data.certs?.length || 0} 份证书 · pki 路径 <code className="mono">{data.kubePath || '/etc/kubernetes/pki'}</code> · 读自 master</span>
                {expired.length > 0
                  ? <span className="badge badge-danger">{expired.length} 份已过期</span>
                  : soon.length > 0
                    ? <span className="badge badge-warn">{soon.length} 份30天内到期</span>
                    : <span className="badge badge-ok">全部健康</span>}
              </div>

              <div className="table-wrap">
                <table className="data-table" style={{ fontSize: '0.8125rem' }}>
                  <thead><tr>
                    <th>证书</th><th>类型</th><th>路径</th><th>到期时间</th><th>剩余</th><th>签发者</th><th>SHA1</th>
                  </tr></thead>
                  <tbody>
                    {(data.certs || []).map((c, i) => (
                      <tr key={i} title={c.path}>
                        <td className="mono">{c.name}</td>
                        <td>{TYPE_LABEL[c.type] || c.type}</td>
                        <td className="mono dim" style={{ fontSize: '0.6875rem', maxWidth: 260, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{c.path}</td>
                        <td className="mono" style={{ fontSize: '0.6875rem' }}>{c.expires}</td>
                        <td style={{ fontWeight: c.remain.startsWith('已过期') ? 700 : undefined, color: remainColor(c.remain) }}>{c.remain}</td>
                        <td className="dim" style={{ fontSize: '0.6875rem', maxWidth: 180, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={c.issuer}>{c.issuer}</td>
                        <td className="mono dim" style={{ fontSize: '0.625rem' }}>{c.sha1}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>

              {steps && steps.length > 0 && (
                <div className="banner banner-ok" style={{ fontSize: '0.75rem', fontFamily: 'monospace', whiteSpace: 'pre-wrap' }}>
                  {steps.map((s, i) => <div key={i} style={{ padding: '2px 0' }}>{s}</div>)}
                </div>
              )}

              <div className="modal-actions" style={{ justifyContent: 'space-between', flexWrap: 'wrap', gap: 8 }}>
                <span className="dim" style={{ fontSize: '0.75rem' }}>
                  {!arenewable
                    ? `二进制安装：引导式重签，仅续签你勾选的证书（openssl 复用私钥，CA 不动）`
                    : expired.length > 0 || soon.length > 0 ? '存在过期/临期证书，建议续签' : '证书全部正常，无需续签'}
                </span>
                {isBinary ? (
                  <>
                    {planRes?.ok === false && (
                      <span className="badge badge-danger" style={{ marginLeft: 8 }}>{planRes.error}</span>
                    )}
                    {binErr && <span className="badge badge-danger" style={{ marginLeft: 8 }}>{binErr}</span>}
                    <button className="btn-glass-soft btn-glass-soft-danger"
                      title="生成可续签候选列表（只读推导），勾选后滚动执行重签"
                      onClick={genPlan} disabled={planBusy}>{planBusy ? '处理中…' : '生成续签计划'}</button>
                  </>
                ) : (
                  !confirmOpen ? (
                    <button className="btn-glass-soft btn-glass-soft-danger"
                      title="写操作：在 master 上执行 kubeadm certs renew all + 重启控制面 + 刷新 kubeconfig"
                      onClick={() => setConfirmOpen(true)}
                      disabled={busy || !arenewable || (data.ok && !data.master)}>一键续签</button>
                  ) : (
                    <div style={{ display: 'flex', gap: 6, alignItems: 'center' }}>
                      <span className="dim" style={{ fontSize: '0.6875rem' }} title="master 上执行 kubeadm certs renew all + 重启控制面 + 刷新 kubeconfig（写操作）">
                        写操作，仅续签临期/过期证书
                      </span>
                      <input className="input" style={{ width: 180 }}
                        placeholder="输入 renew-certs 确认"
                        value={confirmText} onChange={(e) => setConfirmText(e.target.value)} />
                      <button className="btn-glass-soft btn-glass-soft-danger" onClick={renew} disabled={confirmText.trim() !== 'renew-certs' || busy}>确认续签</button>
                      <button className="btn-glass-soft" onClick={() => { setConfirmOpen(false); setConfirmText('') }}>取消</button>
                    </div>
                  )
                )}
              </div>

              {/* ── 二进制: 续签计划(勾选) ── */}
              {isBinary && planRes?.ok && (
                <>
                  <div className="banner" style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '0.5rem 0.75rem' }}>
                    <span>候选 {plan.length} 份 · 已勾选 <b>{checked.size}</b> 份 · 新有效期 {planRes.days || 365} 天</span>
                    {planRes.nodes && <span className="dim">控制面节点: {planRes.nodes.map((n) => n.nodeName).join('、')}</span>}
                  </div>
                  <div className="table-wrap">
                    <table className="data-table" style={{ fontSize: '0.8125rem' }}>
                      <thead><tr>
                        <th style={{ width: 30 }}></th><th>证书</th><th>到期</th><th>剩余</th><th>签发CA</th><th>CA到期</th><th>说明</th>
                      </tr></thead>
                      <tbody>
                        {plan.map((c, i) => (
                          <tr key={i} title={c.path}>
                            <td>
                              <input type="checkbox" checked={checked.has(c.path)} disabled={!c.renewable}
                                onChange={() => toggle(c.path)} />
                            </td>
                            <td className="mono">{c.name}</td>
                            <td className="mono" style={{ fontSize: '0.6875rem' }}>{c.expires}</td>
                            <td style={{ fontWeight: c.remain.startsWith('已过期') ? 700 : undefined, color: remainColor(c.remain) }}>{c.remain}</td>
                            <td className="mono dim" style={{ fontSize: '0.6875rem' }} title={c.ca}>{(c.ca || '-').split('/').pop()}</td>
                            <td className="mono dim" style={{ fontSize: '0.6875rem' }}>{c.caExpires || '-'}</td>
                            <td className="dim" style={{ fontSize: '0.6875rem' }}>{c.renewable ? '' : (c.reason || '不可续')}</td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>

                  {!binConfirmOpen ? (
                    <div className="modal-actions">
                      <button className="btn-glass-soft btn-glass-soft-danger"
                        title="写操作：逐控制面节点 备份→重签→按依赖重启→healthz 校验（滚动）"
                        disabled={checked.size === 0 || planBusy}
                        onClick={() => setBinConfirmOpen(true)}>执行勾选续签 ({checked.size})</button>
                    </div>
                  ) : (
                    <div className="modal-actions" style={{ flexWrap: 'wrap', gap: 6 }}>
                      <span className="dim" style={{ fontSize: '0.6875rem' }}>写操作，仅续签勾选的 {checked.size} 份，每节点自动备份可回滚</span>
                      <input className="input" style={{ width: 180 }} placeholder="输入 renew-certs 确认"
                        value={binConfirmText} onChange={(e) => setBinConfirmText(e.target.value)} />
                      <button className="btn-glass-soft btn-glass-soft-danger" onClick={binRenew}
                        disabled={binConfirmText.trim() !== 'renew-certs' || planBusy}>确认执行</button>
                      <button className="btn-glass-soft" onClick={() => { setBinConfirmOpen(false); setBinConfirmText('') }}>取消</button>
                    </div>
                  )}

                  {/* 每个节点结果 + 一键恢复 */}
                  {binNodes && binNodes.length > 0 && (
                    <div className="table-wrap">
                      <table className="data-table" style={{ fontSize: '0.8125rem' }}>
                        <thead><tr>
                          <th>节点</th><th>状态</th><th>操作明细</th><th>结果</th>
                        </tr></thead>
                        <tbody>
                          {binNodes.map((n, i) => (
                            <tr key={i}>
                              <td className="mono">
                                {n.nodeName}
                                <span className="dim" style={{ fontSize: '0.625rem', display: 'block' }}>
                                  {n.mode === 'ssh' ? `SSH → ${n.hostID || '?'}` : n.mode === 'local' ? '本机' : '未登记主机'}
                                </span>
                              </td>
                              <td>
                                {n.status === 'done' ? <span className="badge badge-ok">成功</span>
                                  : n.status === 'warn' ? <span className="badge badge-warn">已重签但未探测到健康</span>
                                    : n.status === 'error' || n.error ? <span className="badge badge-danger">失败</span>
                                      : <span className="badge">…</span>}
                                {n.error && <div className="dim" style={{ fontSize: '0.6875rem', whiteSpace: 'pre-wrap' }}>{n.error}</div>}
                              </td>
                              <td className="mono dim" style={{ fontSize: '0.6875rem', whiteSpace: 'pre-wrap' }}>
                                {(n.steps || []).map((s, j) => <div key={j} style={{ padding: '1px 0' }}>{s}</div>)}
                              </td>
                              <td>
                                {n.backup && (
                                  rbTarget === n.backup ? (
                                    <div style={{ display: 'flex', gap: 6, alignItems: 'center' }}>
                                      <input className="input" style={{ width: 120 }} placeholder="输入 rollback"
                                        value={rbConfirmText} onChange={(e) => setRbConfirmText(e.target.value)} />
                                      <button className="btn-glass-soft btn-glass-soft-danger" disabled={rbConfirmText.trim() !== 'rollback' || planBusy}
                                        onClick={() => binRollback(n.backup!)}>确认恢复</button>
                                      <button className="btn-glass-soft" onClick={() => { setRbTarget(null); setRbConfirmText('') }}>取消</button>
                                    </div>
                                  ) : (
                                    <button className="btn-glass-soft" disabled={planBusy}
                                      title={`恢复该节点续签前的证书备份（${n.backup}）`}
                                      onClick={() => { setRbTarget(n.backup!); setRbConfirmText(''); setBinErr('') }}>恢复备份</button>
                                  )
                                )}
                              </td>
                            </tr>
                          ))}
                        </tbody>
                      </table>
                      <div className="dim" style={{ fontSize: '0.6875rem', paddingTop: 6 }}>
                        提示：二进制部署的 kubeconfig / admin.conf 内嵌 client 证书不在自动重签范围；worker kubelet 证书不受影响。
                      </div>
                    </div>
                  )}
                </>
              )}
            </>
          )}
        </div>
      </div>
    </div>
  )
}