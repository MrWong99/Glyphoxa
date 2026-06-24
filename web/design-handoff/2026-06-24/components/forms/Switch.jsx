// Glyphoxa Design System — Switch (components/forms)
// The prototype re-rolls the switch inline; the SPA ports this onto a
// Radix-backed Switch wrapper at handoff time (ADR-0017), keeping `.switch`.
export function Switch({ checked, disabled }) {
  return (
    <button
      className="switch"
      data-state={checked ? "checked" : "unchecked"}
      disabled={disabled}
      role="switch"
      aria-checked={checked}
    >
      <span className="switch-thumb" />
    </button>
  );
}
