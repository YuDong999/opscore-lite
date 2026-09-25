// confirm() 的 Promise 化替代(U1) —— L2 公共 hook
// 调用形态与原生 confirm 一致: const ok = await confirm('标题', { desc, okText, danger })
// 渲染内核走公共 ConfirmModal(index.css 体系), 与全站弹层同一套视觉。
import { useCallback, useState, type ReactNode } from 'react'
import { ConfirmModal } from '../../components/common/Modal'

interface ConfirmState {
  title: string
  desc?: string
  content?: ReactNode   // 需要展示结构化正文时用内容块(如 SQL 预览), 优先于 desc
  okText?: string
  danger?: boolean
  maxWidth?: number
  resolve: (ok: boolean) => void
}

export function useConfirm() {
  const [state, setState] = useState<ConfirmState | null>(null)
  const confirm = useCallback((title: string, opts?: { desc?: string; content?: ReactNode; okText?: string; danger?: boolean; maxWidth?: number }) =>
    new Promise<boolean>(resolve => setState({ title, ...opts, resolve })), [])
  const element = state ? (
    <ConfirmModal title={state.title} desc={state.desc} okLabel={state.okText} danger={state.danger} maxWidth={state.maxWidth}
      children={state.content}
      onOk={() => { state.resolve(true); setState(null) }}
      onCancel={() => { state.resolve(false); setState(null) }} />
  ) : null
  return { confirm, confirmEl: element }
}
