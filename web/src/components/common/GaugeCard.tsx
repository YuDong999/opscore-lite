// 百分比仪表盘卡片 —— L2 公共层
// 框架式设计: 几何全部自适应, 无写死字号 ——
//   弧: 半径 75%(百分比, 随容器缩放), 弧带宽随基准边等比;
//   字号: 用 canvas measureText 实测「本次要画的那串文字」的宽度, 反推出恰好放进弧内径的字号
//   (文字变长自动变小, 变短自动变大, 缩放/窄窗随 ResizeObserver 重算) —— 数字永远在弧内。
// 三条铁律(各翻车一次换来的):
//   0. 几何只能有一份真相: 基准取【实测容器】(宽和高), 并把 ECharts 的 radius 也写成
//      由同一 m 推出的 px 值 —— 库按容器算、代码按 props 算 = 双份真相, 容器一压扁就漂(穿模)
//   1. canvas 不认 CSS var()/color-mix(): 主题色必须经 cssVar() 解析成具体值
//   2. 固定 px 字号 + 百分比弧 = 缩放必穿模; 字号必须实测推导
import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import Card from '../Card'
import EChart from '../../charts/EChart'
import { cssVar } from '../../lib/theme'

// 文字宽度测量(100px 基准, 宽度与字号线性, 换算即可): 同 fontFamily 与 ECharts 默认一致
let measureCtx: CanvasRenderingContext2D | null = null
function textInkWidthAt100(text: string): number {
  if (!measureCtx) measureCtx = document.createElement('canvas').getContext('2d')
  if (!measureCtx) return text.length * 55
  // 字重与 detail 显式设置保持一致; 量「墨迹」(actualBoundingBox) 而不是「字宽 advance」——
  // advance 含左右空隙, 拿它比可见边界会系统性高估溢出
  measureCtx.font = 'normal 100px sans-serif'
  const mt = measureCtx.measureText(text)
  const ink = (mt.actualBoundingBoxLeft ?? 0) + (mt.actualBoundingBoxRight ?? 0)
  return ink > 0 ? ink : mt.width
}

export function GaugeCard({ title, subtitle, value, color, max = 100, height = 240, formatter, footer }: {
  title: string
  subtitle?: string
  value: number
  /** 进度弧颜色：传主题 token 取值（cssVar('--accent') 等）或固定色 */
  color: string
  max?: number
  /** 仪表盘区高度 = 弧的基准边(容器宽超过它后弧不再变大) */
  height?: number
  formatter?: (v: number) => string
  footer?: ReactNode
}) {
  const fmt = formatter ?? ((v: number) => v.toFixed(2) + '%')

  const boxRef = useRef<HTMLDivElement>(null)
  const [box, setBox] = useState({ w: 0, h: 0 })
  useEffect(() => {
    const el = boxRef.current
    if (!el || typeof ResizeObserver === 'undefined') return
    const ro = new ResizeObserver(entries => {
      for (const e of entries) setBox({ w: e.contentRect.width, h: e.contentRect.height })
    })
    ro.observe(el)
    return () => ro.disconnect()
  }, [])

  // ── 几何推导(唯一事实源 = 实测容器宽 w 与 height 取小) ──
  // 基准边 = 实测容器宽高取小(未测量到时退回 height)—【不再直接把 height 当基准】
  const m = Math.min(box.w || height, box.h || height)
  const R = m * 0.375                                    // 与 ECharts '75%'(=0.75×min(半宽,半高)) 同义
  const arcW = Math.min(20, Math.max(10, Math.round(m * 0.067)))  // 弧带宽: 240→16, 150→10
  const inner = R * 2 - arcW * 2                         // 弧内径(数字可用空间, 直径)
  // 字号自适应: 实测墨迹宽(100px 基准) → 缩放到「内径×80%」(留 20% 余量不顶弧)
  const label = fmt(value)
  const fontSize = Math.max(9, Math.floor((inner * 0.8 / textInkWidthAt100(label)) * 100))

  const option = useMemo(() => ({
    series: [{
      type: 'gauge', startAngle: 210, endAngle: -30, center: ['50%', '50%'],
      min: 0, max, radius: `${R}px`,
      progress: { show: true, width: arcW, itemStyle: { color } },
      axisLine: { lineStyle: { width: arcW, color: [[1, cssVar('--border') || 'rgba(127,127,127,0.18)']] } },
      axisTick: { show: false }, splitLine: { show: false }, axisLabel: { show: false }, pointer: { show: false },
      detail: {
        valueAnimation: true, fontSize, color: cssVar('--text') || '#333', offsetCenter: [0, 0],
        fontFamily: 'sans-serif', fontWeight: 'normal',   // 显式: 让测量与渲染一致, 不依赖库默认值
        formatter: fmt,
      },
      data: [{ value }],
    }],
  }), [value, color, max, arcW, fontSize, fmt, R])

  return (
    <Card title={title} subtitle={subtitle}>
      <div ref={boxRef}>
        <EChart option={option} height={height} />
      </div>
      {footer}
    </Card>
  )
}
