// 跨库同步弹窗: 复用 SyncPanel(功能全在), 只做 dbx 数据对比式弹窗容器。
import { useEffect } from 'react'
import { createPortal } from 'react-dom'
import SyncPanel from './SyncPanel'
import { type ConnectionInfo } from './api'

export default function SyncDialog({
  conns, activeConnId, presetDb, onClose,
}: {
  conns: ConnectionInfo[]
  activeConnId?: string
  presetDb?: string
  onClose: () => void
}) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [onClose])

  return createPortal(
    <div className="qo-overlay" onClick={onClose}>
      <div className="db-conn-panel db-sync-dialog" onClick={e => e.stopPropagation()}>
        <div className="db-conn-list-head">
          <span>跨库同步 <span className="dim" style={{ fontWeight: 400, fontSize: '0.6875rem' }}>源/目标 → 选表(可自定义目标名) → 计划预览 → 执行</span></span>
          <button className="btn-glass-soft btn-glass-soft-sm" onClick={onClose}>✕</button>
        </div>
        <SyncPanel conns={conns} activeConnId={activeConnId} presetDb={presetDb} />
      </div>
    </div>,
    document.body,
  )
}
