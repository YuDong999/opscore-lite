// confirm() 的 Promise 化替代(U1) —— L2 公共 hook
// 调用形态与原生 confirm 一致: const ok = await confirm('标题', { desc, okText, danger })
// 渲染内核走公共 ConfirmModal(index.css 体系), 与全站弹层同一套视觉。
import { useCallback, useState } from 'react'
import { ConfirmModal } from '../../components/common/Modal'

interface ConfirmState {
  title: string
  desc?: string
  okText?: string
  danger?: boolean
  resolve: (ok: boolean) => void
}

export function useConfirm() {
  const [state, setState] = useState<ConfirmState | null>(null)
  const confirm = useCallback((title: string, opts?: { desc?: string; okText?: string; danger?: boolean }) =>
    new Promise<boolean>(resolve => setState({ title, ...opts, resolve })), [])
  const element = state ? (
    <ConfirmModal title={state.title} desc={state.desc} okLabel={state.okText} danger={state.danger}
      onOk={() => { state.resolve(true); setState(null) }}
      onCancel={() => { state.resolve(false); setState(null) }} />
  ) : null
  return { confirm, confirmEl: element }
}
