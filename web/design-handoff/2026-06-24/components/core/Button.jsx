// Glyphoxa Design System — Button (components/core)
// Trivial primitive, rolled by hand (ADR-0017). variant ∈ {primary, ghost}.
export function Button({ variant = "primary", children, ...props }) {
  return (
    <button className="btn" data-variant={variant} {...props}>
      {children}
    </button>
  );
}
