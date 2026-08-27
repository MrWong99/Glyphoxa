import { Fragment, useEffect, useState, type ReactNode } from "react";
import { Link, Outlet, useParams } from "@tanstack/react-router";
import { Toaster } from "sonner";
import {
  BookOpen,
  Dices,
  Lightbulb,
  Map as MapIcon,
  MessagesSquare,
  PanelLeft,
  ScrollText,
  Search,
  Settings,
  Swords,
  Users,
  UsersRound,
} from "lucide-react";

import type { User } from "@gen/glyphoxa/management/v1/management_pb";

import { Button } from "./ui/Button";
import { SidebarUser } from "./SidebarUser";
import { LegalFooter } from "./LegalFooter";
import { CampaignSwitcher } from "./CampaignSwitcher";
import { CommandPalette } from "./CommandPalette";
import { LanguageSwitcher } from "./LanguageSwitcher";
import { useI18n, type MessageKey } from "@/i18n";
import { isCampaignView, type CampaignView } from "@/screens/campaign/views";

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

// The Campaign screen's sub-views as sidebar sub-navigation (ADR-0063) — the
// screen's former in-page tab bar, one Link per view, reusing the tab labels.
// Rendered accordion-style under the Campaign item while it is active; the
// active sub-view comes from the route's :view path param.
const CAMPAIGN_SUBNAV: { view: CampaignView; label: MessageKey; icon: ReactNode }[] = [
  { view: "cast", label: "campaign.tabCast", icon: <Users size={15} /> },
  // The internal "Knowledge Graph" never faces the GM — the item reads
  // "World wiki" (the copy-simplification glossary).
  { view: "knowledge", label: "campaign.tabWiki", icon: <BookOpen size={15} /> },
  { view: "maps", label: "campaign.tabMaps", icon: <MapIcon size={15} /> },
  { view: "players", label: "campaign.tabPlayers", icon: <UsersRound size={15} /> },
  // "Knowledge Proposals" simplifies to "Suggestions" for the same reason the
  // graph became a wiki: GMs, not developers.
  { view: "proposals", label: "campaign.tabSuggestions", icon: <Lightbulb size={15} /> },
  // Butler planning chat (#592, ADR-0062).
  { view: "planning", label: "chat.tabPlanning", icon: <MessagesSquare size={15} /> },
];

// The sidebar's off-canvas drawer breakpoint. Must stay in sync with the
// @media block in styles/components.css; every JS check goes through
// isDrawerViewport so the width lives in one place on this side.
const DRAWER_BREAKPOINT = "(max-width: 880px)";
const isDrawerViewport = () =>
  typeof window !== "undefined" && (window.matchMedia?.(DRAWER_BREAKPOINT).matches ?? false);

export function AppShell({ tenantSlug, user }: { tenantSlug: string; user: User }) {
  const { t } = useI18n();
  // view is present only under the campaign sub-view route (ADR-0063); it
  // drives the sub-navigation's active highlight. Derived by hand rather than
  // via Link activeProps so the mocked-router unit test can observe it, and
  // because the parent Campaign link needs the value anyway (see below).
  const { screen, view } = useParams({ strict: false }) as { screen?: string; view?: string };
  const active = NAV.find((n) => n.to === screen);
  const activeView = screen === "campaign" && isCampaignView(view) ? view : undefined;

  // The browser tab names the screen ("Session · Glyphoxa") — with several
  // Glyphoxa tabs open, an all-"Glyphoxa" tab strip is a guessing game.
  const screenTitle = active ? t(active.title) : null;
  useEffect(() => {
    document.title = screenTitle ? `${screenTitle} · Glyphoxa` : "Glyphoxa";
  }, [screenTitle]);

  // Sidebar collapse (#88 slice 4). On narrow viewports the sidebar is an
  // off-canvas drawer the topbar toggle opens; on wide viewports the toggle hides
  // it for focus. State lives on data-collapsed so the CSS breakpoints (and the
  // unit test) read it without media-query support. It starts collapsed on a
  // small viewport so mobile loads with the drawer shut (matchMedia is absent in
  // jsdom → the shell defaults to expanded under test).
  const [collapsed, setCollapsed] = useState(isDrawerViewport);

  // The Ctrl+K palette, lifted so the topbar's search button can open it too —
  // a keyboard chord alone is undiscoverable and unreachable on touch.
  const [paletteOpen, setPaletteOpen] = useState(false);

  // On the drawer breakpoint a nav tap should navigate AND shut the drawer —
  // otherwise it keeps covering the screen it just navigated to. On wide
  // viewports this is a no-op so the sidebar stays put.
  const closeDrawerOnMobile = () => {
    if (isDrawerViewport()) setCollapsed(true);
  };

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
            <Fragment key={item.to}>
              {item.to === "campaign" && activeView ? (
                // While a Campaign sub-view is open, the parent item links to
                // the CURRENT sub-view — a bare /campaign would redirect to
                // cast and stomp the open view (a habit-click hazard the old
                // in-page tabs didn't have).
                <Link
                  className="gx-nav__item"
                  to="/t/$tenantSlug/$screen/$view"
                  params={{ tenantSlug, screen: "campaign", view: activeView }}
                  data-active="true"
                  onClick={closeDrawerOnMobile}
                >
                  {item.icon}
                  {t(item.label)}
                </Link>
              ) : (
                <Link
                  className="gx-nav__item"
                  to="/t/$tenantSlug/$screen"
                  params={{ tenantSlug, screen: item.to }}
                  activeProps={{ "data-active": "true" }}
                  onClick={closeDrawerOnMobile}
                >
                  {item.icon}
                  {t(item.label)}
                </Link>
              )}
              {/* Campaign sub-views (ADR-0063): the screen's former in-page
                  tabs, revealed while Campaign is the active screen. */}
              {item.to === "campaign" && screen === "campaign" && (
                <div className="gx-nav__sub" role="group" aria-label={t("campaign.viewTablist")}>
                  {CAMPAIGN_SUBNAV.map((sub) => (
                    <Link
                      key={sub.view}
                      className="gx-nav__subitem"
                      to="/t/$tenantSlug/$screen/$view"
                      params={{ tenantSlug, screen: "campaign", view: sub.view }}
                      data-active={activeView === sub.view ? "true" : undefined}
                      aria-current={activeView === sub.view ? "page" : undefined}
                      onClick={closeDrawerOnMobile}
                    >
                      {sub.icon}
                      {t(sub.label)}
                    </Link>
                  ))}
                </div>
              )}
            </Fragment>
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
          <Button
            variant="ghost"
            size="sm"
            className="gx-topbar__search"
            aria-label={t("palette.openSearch")}
            title={t("palette.openSearch")}
            onClick={() => setPaletteOpen(true)}
            iconStart={<Search size={17} />}
          />
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

      {/* Ctrl+K campaign search (#591). Mounted in the shell like the Toaster —
          the global-overlay convention — so it is reachable from every
          authenticated screen and never renders for an unauthenticated visitor
          (the shell sits inside the AuthGate). Closed it renders nothing and
          fires no RPCs, so screen tests stay clean. */}
      <CommandPalette tenantSlug={tenantSlug} open={paletteOpen} onOpenChange={setPaletteOpen} />

      {/* Single toast host for the whole app (ADR-0017: sonner). Mounted here, not
          in Providers, so the screen unit tests that render without the shell get a
          clean DOM and their deterministic inline cues instead of toast portals. */}
      <Toaster theme="dark" position="bottom-right" richColors />
    </div>
  );
}
