// ── 主题系统: 5 套主题色 + Context 提供全局使用 ──

import { createContext, useContext, useEffect, useState, type ReactNode } from 'react'

// 可选主题: light(蓝) obsidian(紫) forest(绿) twilight(暮光) amber(琥珀)
export type Theme = 'light' | 'obsidian' | 'forest' | 'twilight' | 'amber'

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
}

const Ctx = createContext<ThemeCtx>({
  theme: 'light',
  setTheme: () => {},
  dark: false,
  meta: THEMES[0],
})

// 从 localStorage 读取 → 设置 data-theme 属性 → 改变所有 CSS 变量
export function ThemeProvider({ children }: { children: ReactNode }) {
  const [theme, setThemeState] = useState<Theme>(
    () => (localStorage.getItem('opscore-theme') as Theme) || 'light',
  )

  // 主题变化时: 写 data-theme 属性 + 持久化到 localStorage
  useEffect(() => {
    document.documentElement.setAttribute('data-theme', theme)
    localStorage.setItem('opscore-theme', theme)
  }, [theme])

  const setTheme = (t: Theme) => setThemeState(t)
  const meta = THEMES.find((t) => t.id === theme) || THEMES[0]

  return (
    <Ctx.Provider value={{ theme, setTheme, dark: meta.dark, meta }}>
      {children}
    </Ctx.Provider>
  )
}

export const useTheme = () => useContext(Ctx)
