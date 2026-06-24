// Glyphoxa Design System — Select (components/forms)
// The prototype re-rolls the select inline; the SPA ports this onto a
// Radix-backed Select wrapper at handoff time (ADR-0017), keeping `.select`.
export function Select({ label, value, disabled }) {
  return (
    <div className="field">
      {label && <label className="field-label">{label}</label>}
      <button className="select" disabled={disabled}>
        {value}
        <span aria-hidden="true">▾</span>
      </button>
    </div>
  );
}
