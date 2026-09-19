/**
 * 公共弹层组件 —— 公共层 L2
 *
 * 由来：此前全仓手写 32 处 modal 骨架（DockerModule 17、K8sModule 7、
 * AnsibleModule 3，另有 5 处散在 components/），每处都要重复写
 * 「遮罩 + 面板 + 点遮罩关闭 + stopPropagation + 标题栏 + 底部按钮区」。
 *
 * 设计原则：**沿用 index.css 已有的类名，不引入新样式**——
 * 替换后视觉完全不变，也不触碰 index.css 的版本化覆盖段（V1-V19）。
 *
 * 形态来自实际调研：
 *   - 宽度取值 20 余种（190 ~ 1100），用 maxWidth 传入
 *   - 标题栏 16 处，右侧多为「关闭」按钮，也有把「刷新」塞进标题里的
 *   - 底部按钮区 21 处（Docker 9、LogMonitor 5、K8sCerts 3、K8s 2、Firewall 1）
 *   - 日志/表单类弹层用 modal log-modal（padding 0，内容自己控制滚动）
 */

import { useEffect, type CSSProperties, type ReactNode } from 'react'

/** default = 标准面板(.modal)；log = 日志/表单面板(.modal.log-modal，无内边距) */
export type ModalSize = 'default' | 'log'

interface ModalProps {
  onClose: () => void
  children: ReactNode
  /** 标题栏内容。可放任意节点（现有代码常把「刷新」按钮塞在标题里） */
  title?: ReactNode
  /** 标题栏右侧内容。不传时，若 title 存在则默认渲染「关闭」按钮 */
  headRight?: ReactNode
  /** 是否渲染默认关闭按钮（仅在未传 headRight 时生效） */
  showClose?: boolean
  /** 底部操作区，渲染为 modal-actions */
  footer?: ReactNode
  size?: ModalSize
  /** 面板最大宽度，对应现有代码里的 style={{ maxWidth: n }} */
  maxWidth?: number | string
  /** 附加到面板上的类名 */
  className?: string
  /** 点击遮罩是否关闭，默认 true */
  closeOnOverlay?: boolean
  /**
   * 是否吞掉遮罩上的滚轮事件，默认 true。
   * 原代码在 K8s 侧 5 个弹层里逐个手写 onWheel={(e) => e.stopPropagation()}，
   * 目的是不让滚轮事件继续冒泡到父级监听器。
   */
  stopWheelPropagation?: boolean
  /** 其余内联样式 */
  style?: CSSProperties
}

export function Modal({
  onClose,
  children,
  title,
  headRight,
  showClose = true,
  footer,
  size = 'default',
  maxWidth,
  className = '',
  closeOnOverlay = true,
  stopWheelPropagation = true,
  style,
}: ModalProps) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [onClose])

  const panelStyle: CSSProperties = { ...style }
  // 只设 maxWidth，不覆盖 .modal 自带的 width: min(35rem, 100%)，保持与原写法一致
  if (maxWidth !== undefined) panelStyle.maxWidth = maxWidth

  const panelClass = (size === 'log' ? 'modal log-modal' : 'modal') + (className ? ' ' + className : '')

  return (
    <div
      className="modal-overlay"
      onClick={closeOnOverlay ? onClose : undefined}
      onWheel={stopWheelPropagation ? (e) => e.stopPropagation() : undefined}
    >
      <div className={panelClass} style={panelStyle} onClick={(e) => e.stopPropagation()}>
        {title !== undefined && (
          <div className="modal-head">
            <div className="modal-title">{title}</div>
            {headRight !== undefined
              ? headRight
              : showClose && (
                  <button className="btn-glass-soft btn-glass-soft-sm" onClick={onClose}>
                    关闭
                  </button>
                )}
          </div>
        )}
        {children}
        {footer !== undefined && <div className="modal-actions">{footer}</div>}
      </div>
    </div>
  )
}

interface ConfirmModalProps {
  title: string
  /** 描述文本。需要更复杂的内容时改用 children */
  desc?: string
  children?: ReactNode
  okLabel?: string
  cancelLabel?: string
  /** 高危操作：确认按钮用 btn-danger */
  danger?: boolean
  /** 执行中：按钮禁用，确认按钮显示「执行中…」 */
  busy?: boolean
  onOk: () => void
  onCancel: () => void
  maxWidth?: number | string
}

/**
 * 二次确认弹窗
 *
 * API 沿用 DockerModule 内部既有的局部 ConfirmModal 约定（title/desc/onCancel/onOk/okLabel），
 * 使该模块原有的调用点无需改动即可切换到公共版。
 * 对应展开写法：modal-overlay > modal(maxWidth 420) > h3 + p.dim + modal-actions
 */
export function ConfirmModal({
  title,
  desc,
  children,
  okLabel,
  cancelLabel = '取消',
  danger = false,
  busy = false,
  onOk,
  onCancel,
  maxWidth = 420,
}: ConfirmModalProps) {
  return (
    <Modal onClose={onCancel} maxWidth={maxWidth}>
      <h3>{title}</h3>
      {children ?? (desc ? <p className="dim">{desc}</p> : null)}
      <div className="modal-actions">
        <button className="btn-glass-soft" onClick={onCancel} disabled={busy}>
          {cancelLabel}
        </button>
        <button className={`btn ${danger ? 'btn-danger' : 'btn-accent'}`} disabled={busy} onClick={onOk}>
          {busy ? '执行中…' : okLabel || '确认'}
        </button>
      </div>
    </Modal>
  )
}
