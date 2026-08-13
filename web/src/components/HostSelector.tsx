import { useHost } from './HostContext'

export default function HostSelector() {
  const { hosts, selected, setSelected } = useHost()

  return (
    <div className="host-selector">
      <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" style={{ opacity: 0.5, marginRight: 6 }}>
        <rect x="2" y="2" width="20" height="8" rx="2" />
        <rect x="2" y="14" width="20" height="8" rx="2" />
        <circle cx="6" cy="6" r="1" fill="currentColor" />
        <circle cx="6" cy="18" r="1" fill="currentColor" />
      </svg>
      <select
        className="host-select"
        value={selected?.id || ''}
        onChange={(e) => {
          const id = e.target.value
          if (!id) {
            setSelected(null)
            return
          }
          const h = hosts.find((x) => x.id === id)
          if (h) setSelected(h)
        }}
      >
        {hosts.map((h) => (
          <option key={h.id} value={h.id}>{h.label}</option>
        ))}
      </select>
    </div>
  )
}
