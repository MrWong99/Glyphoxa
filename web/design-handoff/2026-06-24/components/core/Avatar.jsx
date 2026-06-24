// Glyphoxa Design System — Avatar (components/core)
// Initial-letter avatar for the campaign header.
export function Avatar({ name }) {
  return <span className="avatar">{(name || "?").charAt(0).toUpperCase()}</span>;
}
