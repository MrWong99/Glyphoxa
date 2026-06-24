// Glyphoxa Design System — Card (components/core)
export function Card({ title, desc, action, disabled, children }) {
  return (
    <section className="card" data-disabled={disabled || undefined}>
      {(title || action) && (
        <div className="card-header">
          <div>
            {title && <h2 className="card-title">{title}</h2>}
            {desc && <p className="card-desc">{desc}</p>}
          </div>
          {action}
        </div>
      )}
      {children}
    </section>
  );
}
