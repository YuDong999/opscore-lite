// ── 主题系统: 5 套主题色 + 表面风格(玻璃/扁平) + 圆角档位, Context 提供全局使用 ──

import { createContext, useContext, useEffect, useState, type ReactNode } from 'react'

// 可选主题: light(蓝) obsidian(紫) forest(绿) twilight(暮光) amber(琥珀)
export type Theme = 'light' | 'obsidian' | 'forest' | 'twilight' | 'amber'

// 表面风格: glass=半透明+背景模糊+渐变底(原样); flat=把叠加结果算成实色, 无模糊无渐变
export type Surface = 'glass' | 'flat'
// 圆角档位: round=我们原尺寸(.75rem); compact=dbx 档(4/6px)
export type Corner = 'round' | 'compact'

export interface ThemeMeta {
  id: Theme
  label: string
  dark: boolean
  colors: [string, string]  // 双色配色: [主色, 辅色]
}

// 主题定义列表
export const THEMES: ThemeMeta[] = [
  { id: 'light',    label: '北欧蓝',  dark: false, colors: ['#5b6abf', '#0ea5e9'] },
  { id: 'obsidian', label: '黑曜石',  dark: true,  colors: ['#a78bfa', '#f472b6'] },
  { id: 'forest',   label: '森林绿',  dark: false, colors: ['#059669', '#d97706'] },
  { id: 'twilight', label: '暮光紫',  dark: true,  colors: ['#a78bfa', '#fb923c'] },
  { id: 'amber',    label: '琥珀粉',  dark: false, colors: ['#d97706', '#e11d48'] },
]

interface ThemeCtx {
  theme: Theme
  setTheme: (t: Theme) => void
  dark: boolean
  meta: ThemeMeta
  surface: Surface
  setSurface: (s: Surface) => void
  corner: Corner
  setCorner: (c: Corner) => void
}

const Ctx = createContext<ThemeCtx>({
  theme: 'light',
  setTheme: () => {},
  dark: false,
  meta: THEMES[0],
  surface: 'glass',
  setSurface: () => {},
  corner: 'round',
  setCorner: () => {},
})

// 从 localStorage 读取 → 设置 data-* 属性 → 改变所有 CSS 变量
export function ThemeProvider({ children }: { children: ReactNode }) {
  const [theme, setThemeState] = useState<Theme>(
    () => (localStorage.getItem('opscore-theme') as Theme) || 'light',
  )
  const [surface, setSurfaceState] = useState<Surface>(
    () => (localStorage.getItem('opscore-surface') as Surface) || 'glass',
  )
  const [corner, setCornerState] = useState<Corner>(
    () => (localStorage.getItem('opscore-corner') as Corner) || 'round',
  )

  // 主题变化时: 写 data-theme 属性 + 持久化到 localStorage
  useEffect(() => {
    document.documentElement.setAttribute('data-theme', theme)
    localStorage.setItem('opscore-theme', theme)
  }, [theme])

  useEffect(() => {
    document.documentElement.setAttribute('data-surface', surface)
    localStorage.setItem('opscore-surface', surface)
  }, [surface])

  useEffect(() => {
    document.documentElement.setAttribute('data-corner-style', corner)
    localStorage.setItem('opscore-corner', corner)
  }, [corner])

  const setTheme = (t: Theme) => setThemeState(t)
  const setSurface = (s: Surface) => setSurfaceState(s)
  const setCorner = (c: Corner) => setCornerState(c)
  const meta = THEMES.find((t) => t.id === theme) || THEMES[0]

  return (
    <Ctx.Provider value={{ theme, setTheme, dark: meta.dark, meta, surface, setSurface, corner, setCorner }}>
      {children}
    </Ctx.Provider>
  )
}

export const useTheme = () => useContext(Ctx)
