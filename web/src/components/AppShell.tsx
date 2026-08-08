import { useState, type ReactNode } from "react";
import { Link, Outlet, useParams } from "@tanstack/react-router";
import { Toaster } from "sonner";
import { Dices, PanelLeft, Settings, Swords, ScrollText } from "lucide-react";

import type { User } from "@gen/glyphoxa/management/v1/management_pb";

import { Button } from "./ui/Button";
import { SidebarUser } from "./SidebarUser";
import { LegalFooter } from "./LegalFooter";
import { CampaignSwitcher } from "./CampaignSwitcher";
import { LanguageSwitcher } from "./LanguageSwitcher";
import { useI18n, type MessageKey } from "@/i18n";

// The persistent app shell — sidebar + topbar — ported from the handoff
// ui_kits/glyphoxa-web/shell.jsx (its inline styles lifted onto .gx-shell* /
// .gx-topbar* in styles/components.css) and driven by TanStack Router's active
// route rather than page state (ADR-0018). The Tenant lives in the path
// (/t/:tenantSlug/...); for the single-operator MVP (ADR-0039) it is a thin
// pass-through slug. The sidebar user footer now shows the real signed-in
// operator (ADR-0016), passed in by the AuthGate that wraps the shell.

type NavItem = { to: string; label: MessageKey; icon: ReactNode; title: MessageKey };

const NAV: NavItem[] = [
  { to: "configuration", label: "shell.navSetup", icon: <Settings size={18} />, title: "shell.titleSetup" },
  { to: "campaign", label: "shell.navCampaign", icon: <Swords size={18} />, title: "shell.titleCampaign" },
  { to: "session", label: "shell.navSession", icon: <ScrollText size={18} />, title: "shell.titleSession" },
];

export function AppShell({ tenantSlug, user }: { tenantSlug: string; user: User }) {
  const { t } = useI18n();
  const { screen } = useParams({ strict: false }) as { screen?: string };
  const active = NAV.find((n) => n.to === screen);

  // Sidebar collapse (#88 slice 4). On narrow viewports the sidebar is an
  // off-canvas drawer the topbar toggle opens; on wide viewports the toggle hides
  // it for focus. State lives on data-collapsed so the CSS breakpoints (and the
  // unit test) read it without media-query support. It starts collapsed on a
  // small viewport so mobile loads with the drawer shut (matchMedia is absent in
  // jsdom → the shell defaults to expanded under test).
  const [collapsed, setCollapsed] = useState(
    () => typeof window !== "undefined" && (window.matchMedia?.("(max-width: 880px)").matches ?? false),
  );

  return (
    <div className="gx-shell" data-collapsed={collapsed ? "true" : undefined}>
      <button
        type="button"
        className="gx-shell__scrim"
        aria-hidden="true"
        tabIndex={-1}
        onClick={() => setCollapsed(true)}
      />
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
              {t(item.label)}
            </Link>
          ))}
        </nav>

        <SidebarUser user={user} />
      </aside>

      <div className="gx-main">
        <header className="gx-topbar">
          <Button
            variant="ghost"
            size="sm"
            className="gx-topbar__toggle"
            aria-label={t("shell.toggleSidebar")}
            aria-expanded={!collapsed}
            title={t("shell.toggleSidebar")}
            onClick={() => setCollapsed((c) => !c)}
            iconStart={<PanelLeft size={18} />}
          />
          <div className="gx-topbar__divider" />
          <div className="gx-topbar__titles">
            <div className="gx-topbar__title">{active ? t(active.title) : "Glyphoxa"}</div>
          </div>
          {/* The Active-Campaign switcher lives on every screen (#266a): the
              titles' flex:1 pushes it to the topbar's right edge. */}
          <CampaignSwitcher />
          {/* Display-language picker — on every screen so a German group can
              switch before they've learned the UI. */}
          <LanguageSwitcher className="gx-topbar__lang" />
        </header>

        <main className="gx-content">
          <Outlet />
        </main>

        {/* Impressum / Datenschutz / Nutzungsbedingungen on every app screen
            (#518) — German law wants them reachable from anywhere, not buried
            in a settings page. */}
        <LegalFooter />
      </div>

      {/* Single toast host for the whole app (ADR-0017: sonner). Mounted here, not
          in Providers, so the screen unit tests that render without the shell get a
          clean DOM and their deterministic inline cues instead of toast portals. */}
      <Toaster theme="dark" position="bottom-right" richColors />
    </div>
  );
}
