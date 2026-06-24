import type { ReactNode } from "react";
import { Link, Outlet } from "@tanstack/react-router";

import { BrandSigil } from "./BrandSigil";

// The persistent app shell + sidebar nav, ported from the handoff shell.jsx
// (ADR-0017 class vocabulary) onto TanStack Router's <Link>/<Outlet> (ADR-0018).
// The Tenant lives in the path (/t/:tenantSlug/...) per ADR-0018; for the
// single-operator MVP (ADR-0039) it is a thin pass-through slug.

type NavItem = { to: string; label: string };

const NAV: NavItem[] = [
  { to: "configuration", label: "Configuration" },
  { to: "campaign", label: "Campaign" },
  { to: "session", label: "Session" },
];

export function AppShell({ tenantSlug }: { tenantSlug: string }): ReactNode {
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
          {NAV.map((item) => (
            <Link
              key={item.to}
              className="nav-item"
              to="/t/$tenantSlug/$screen"
              params={{ tenantSlug, screen: item.to }}
              activeProps={{ "data-active": "true" }}
            >
              {item.label}
            </Link>
          ))}
        </nav>
      </aside>
      <main className="content">
        <div className="content-inner">
          <Outlet />
        </div>
      </main>
    </div>
  );
}
