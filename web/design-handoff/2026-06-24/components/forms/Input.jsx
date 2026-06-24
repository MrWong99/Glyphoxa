// Glyphoxa Design System — Input (components/forms)
export function Input({ label, hint, ...props }) {
  return (
    <div className="field">
      {label && <label className="field-label">{label}</label>}
      <input className="input" {...props} />
      {hint && <span className="field-hint">{hint}</span>}
    </div>
  );
}
