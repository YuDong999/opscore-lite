// ── ECharts React 封装: init / resize / setOption ──
// 用法: <EChart option={...} height={260} />
// option 变化时增量更新(不销毁重建), 窗口缩放时自适应

import { useEffect, useRef } from 'react'
import * as echarts from 'echarts'
import 'echarts-liquidfill'

export default function EChart({
  option,
  height = 260,           // 图表高度(默认260px)
}: {
  option: any
  height?: number | string
}) {
  const ref = useRef<HTMLDivElement>(null)    // 图表容器 DOM
  const chart = useRef<echarts.ECharts | null>(null)  // ECharts 实例

  // 挂载时初始化, 绑定 resize 事件; 卸载时 dispose
  useEffect(() => {
    if (!ref.current) return
    chart.current = echarts.init(ref.current)
    const onResize = () => chart.current?.resize()
    window.addEventListener('resize', onResize)
    return () => {
      window.removeEventListener('resize', onResize)
      chart.current?.dispose()
      chart.current = null
    }
  }, [])

  // option 变化时增量更新
  useEffect(() => {
    chart.current?.setOption(option, true)
  }, [option])

  return <div ref={ref} style={{ width: '100%', height }} />
}
