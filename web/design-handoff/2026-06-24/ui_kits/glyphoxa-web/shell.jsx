// Glyphoxa Design System — app shell + sidebar nav (ui_kits/glyphoxa-web)
// The persistent shell: brand sigil, primary nav (Configuration / Campaign /
// Session), and an Outlet slot for the active screen. The SPA ports this onto
// TanStack Router's <Link> + <Outlet> (ADR-0018) keeping the class vocabulary.

import { BrandSigil } from "./icons.jsx";
import { navItems } from "./data.jsx";

export function AppShell({ activeId, children }) {
  return (
    <div className="app-shell">
      <aside className="sidebar">
        <div className="sidebar-brand">
          <span className="sidebar-sigil">
            <BrandSigil />
          </span>
          Glyphoxa
        </div>
        <nav className="nav">
          {navItems.map((item) => (
            <a
              key={item.id}
              className="nav-item"
              href={`#${item.to}`}
              data-active={item.id === activeId}
            >
              {item.label}
            </a>
          ))}
        </nav>
      </aside>
      <main className="content">
        <div className="content-inner">{children}</div>
      </main>
    </div>
  );
}
