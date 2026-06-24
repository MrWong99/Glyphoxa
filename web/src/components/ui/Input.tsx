import { useId } from "react";
import type { InputHTMLAttributes, ReactNode } from "react";

// Input — ported from the handoff components/forms/Input.jsx onto .gx-input /
// .gx-field. Inked well on the inset surface with an arcane focus ring.

export function Input({
  label = null,
  hint = null,
  error = null,
  icon = null,
  id,
  className = "",
  ...props
}: {
  label?: ReactNode;
  hint?: ReactNode;
  error?: ReactNode;
  icon?: ReactNode;
} & InputHTMLAttributes<HTMLInputElement>) {
  const generatedId = useId();
  const fid = id || generatedId;
  const invalid = Boolean(error);
  const inputCls = ["gx-input", icon ? "gx-input--has-icon" : "", invalid ? "gx-input--invalid" : "", className]
    .filter(Boolean)
    .join(" ");

  return (
    <div className="gx-field">
      {label && (
        <label className="gx-field__label" htmlFor={fid}>
          {label}
        </label>
      )}
      <div className="gx-field__wrap">
        {icon && <span className="gx-field__icon">{icon}</span>}
        <input id={fid} className={inputCls} aria-invalid={invalid || undefined} {...props} />
      </div>
      {(error || hint) && (
        <span className={"gx-field__hint" + (error ? " gx-field__hint--error" : "")}>{error || hint}</span>
      )}
    </div>
  );
}
