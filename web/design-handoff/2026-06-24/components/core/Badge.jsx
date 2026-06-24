// Glyphoxa Design System — Badge / Tag (components/core)
// tone ∈ {success, warning, danger, accent, undefined(neutral)}.
export function Badge({ tone, children }) {
  return (
    <span className="tag" data-tone={tone}>
      {children}
    </span>
  );
}
