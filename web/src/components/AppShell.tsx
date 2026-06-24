import type { ReactNode } from "react";
import { Link, Outlet, useParams } from "@tanstack/react-router";
import { Dices, Settings, Swords, ScrollText, ChevronsUpDown } from "lucide-react";

import { Avatar } from "./ui/Avatar";

// The persistent app shell — sidebar + topbar — ported from the handoff
// ui_kits/glyphoxa-web/shell.jsx (its inline styles lifted onto .gx-shell* /
// .gx-topbar* in styles/components.css) and driven by TanStack Router's active
// route rather than page state (ADR-0018). The Tenant lives in the path
// (/t/:tenantSlug/...); for the single-operator MVP (ADR-0039) it is a thin
// pass-through slug, and the sidebar user footer is a static placeholder until
// the auth/me RPC lands.

type NavItem = { to: string; label: string; icon: ReactNode; title: string };

const NAV: NavItem[] = [
  { to: "configuration", label: "Configuration", icon: <Settings size={18} />, title: "Providers" },
  { to: "campaign", label: "Campaign", icon: <Swords size={18} />, title: "Campaign" },
  { to: "session", label: "Session", icon: <ScrollText size={18} />, title: "Session" },
];

export function AppShell({ tenantSlug }: { tenantSlug: string }) {
  const { screen } = useParams({ strict: false }) as { screen?: string };
  const active = NAV.find((n) => n.to === screen);

  return (
    <div className="gx-shell">
      <aside className="gx-sidebar">
        <div className="gx-sidebar__brand">
          <span className="gx-sidebar__sigil">
            <Dices size={17} />
          </span>
          <span className="gx-sidebar__wordmark gx-gradient-text">Glyphoxa</span>
        </div>

        <nav className="gx-nav">
          {NAV.map((item) => (
            <Link
              key={item.to}
              className="gx-nav__item"
              to="/t/$tenantSlug/$screen"
              params={{ tenantSlug, screen: item.to }}
              activeProps={{ "data-active": "true" }}
            >
              {item.icon}
              {item.label}
            </Link>
          ))}
        </nav>

        <div className="gx-sidebar__user">
          <Avatar name="Operator" size="sm" status="live" />
          <div className="gx-sidebar__user-meta">
            <div className="gx-sidebar__user-name">Operator</div>
            <div className="gx-sidebar__user-role">Self-host</div>
          </div>
          <ChevronsUpDown size={15} style={{ color: "var(--text-subtle)" }} />
        </div>
      </aside>

      <div className="gx-main">
        <header className="gx-topbar">
          <div className="gx-topbar__titles">
            <div className="gx-topbar__title">{active?.title ?? "Glyphoxa"}</div>
          </div>
        </header>

        <main className="gx-content">
          <Outlet />
        </main>
      </div>
    </div>
  );
}
