import { createContext, useContext, useEffect, useState, type ReactNode } from 'react'

export type Theme = 'light' | 'obsidian' | 'forest' | 'twilight' | 'amber'

export interface ThemeMeta {
  id: Theme
  label: string
  dark: boolean
  colors: [string, string]
}

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

export function ThemeProvider({ children }: { children: ReactNode }) {
  const [theme, setThemeState] = useState<Theme>(
    () => (localStorage.getItem('opscore-theme') as Theme) || 'light',
  )

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
