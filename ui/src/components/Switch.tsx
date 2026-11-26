/** An on/off switch with its words beside it, e.g. "Show hidden files"; name, when given, is what a screen reader calls it instead. */
export function Switch({ label, checked, onChange, name, disabled }: { label: string; checked: boolean; onChange: (checked: boolean) => void; name?: string; disabled?: boolean }) {
  return (
    <button type="button" role="switch" aria-checked={checked} aria-label={name} disabled={disabled} className="switch" onClick={() => onChange(!checked)}>
      <span className="switch-track" aria-hidden="true">
        <span className="switch-thumb" />
      </span>
      <span className="switch-label">{label}</span>
    </button>
  )
}
