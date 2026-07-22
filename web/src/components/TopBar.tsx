import { useTheme, THEMES } from '../theme'

export default function TopBar() {
  const { theme, setTheme } = useTheme()
  return (
    <header className="topbar">
      <div className="topbar-title">OpsCore · 轻量运维控制台</div>
      <div className="theme-picker">
        {THEMES.map((t) => (
          <button
            key={t.id}
            className={`theme-pill ${theme === t.id ? 'theme-pill-active' : ''}`}
            onClick={() => setTheme(t.id)}
            title={t.label}
          >
            <span className="theme-pill-dot" style={{ background: t.colors[0] }} />
            <span className="theme-pill-dot" style={{ background: t.colors[1] }} />
            {t.label}
          </button>
        ))}
      </div>
    </header>
  )
}
