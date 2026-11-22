import { useEffect, useId, useRef, useState } from "react";
import { Link, Outlet, useParams } from "@tanstack/react-router";
import { useQuery } from "@connectrpc/connect-query";
import { Toaster } from "sonner";
import { Dices, PanelLeft, Search } from "lucide-react";

import { CampaignService } from "@gen/glyphoxa/management/v1/management_pb";
import type { User } from "@gen/glyphoxa/management/v1/management_pb";

import { Button } from "./ui/Button";
import { SidebarUser } from "./SidebarUser";
import { LegalFooter } from "./LegalFooter";
import { CampaignSwitcher } from "./CampaignSwitcher";
import { CommandPalette } from "./CommandPalette";
import { LanguageSwitcher } from "./LanguageSwitcher";
import {
  CAMPAIGN_DESTINATIONS,
  SCREEN_DESTINATIONS,
  campaignDestination,
  screenDestination,
} from "@/app/destinations";
import { useI18n } from "@/i18n";
import { isNotFound } from "@/lib/connectError";
import { formatElapsed, useElapsedSeconds, useLiveSession } from "@/lib/useLiveSession";
import { isCampaignView } from "@/screens/campaign/views";

import "./appShell.css";

// The persistent app shell — sidebar + topbar — ported from the handoff
// ui_kits/glyphoxa-web/shell.jsx (its inline styles lifted onto .gx-shell* /
// .gx-topbar* in styles/components.css; what the shell has grown since lives in
// ./appShell.css) and driven by TanStack Router's active route rather than page
// state (ADR-0018). The Tenant lives in the path (/t/:tenantSlug/...); for the
// single-operator MVP (ADR-0039) it is a thin pass-through slug. The sidebar
// user footer shows the real signed-in operator (ADR-0016), passed in by the
// AuthGate that wraps the shell.
//
// The sidebar is campaign-first (ADR-0063 amendment): a section labelled with
// the Active Campaign's name lists that campaign's views on EVERY screen — not
// only while the Campaign screen is open — then Session, and, pinned at the
// bottom, the two Tenant-wide destinations: Campaigns and Setup. The lists
// themselves are app/destinations.ts.

// The sidebar's off-canvas drawer breakpoint. Must stay in sync with the
// @media block in styles/components.css; every JS check goes through
// isDrawerViewport so the width lives in one place on this side.
const DRAWER_BREAKPOINT = "(max-width: 880px)";
const isDrawerViewport = () =>
  typeof window !== "undefined" && (window.matchMedia?.(DRAWER_BREAKPOINT).matches ?? false);

// How often the pending-suggestions count is re-read while a Voice Session is
// live — that is when the Butler and the NPCs file them. Idle, the count only
// moves when the GM reviews one (which invalidates the read) or refocuses the
// tab.
const PROPOSALS_REFETCH_MS = 60_000;

export function AppShell({ tenantSlug, user }: { tenantSlug: string; user: User }) {
  const { t } = useI18n();
  // Derived by hand rather than via Link activeProps so the mocked-router unit
  // test can observe it, and because the topbar needs the values anyway.
  const { screen, view } = useParams({ strict: false }) as { screen?: string; view?: string };
  const activeView = screen === "campaign" && isCampaignView(view) ? view : undefined;
  const activeScreen = screenDestination(screen);

  // The Active Campaign names the sidebar section, the breadcrumb and the
  // browser tab. retry:false like every other observer of this shared cache
  // entry (the switcher, Setup, Session), so a fresh install's CodeNotFound
  // settles at once whichever of them mounted first. Rendered from DATA, which
  // React Query retains through a failed background refetch — a transient blip
  // never blanks a name already shown.
  const activeQ = useQuery(CampaignService.method.getActiveCampaign, {}, { retry: false });
  const campaignName = activeQ.data?.campaign?.name;
  const noCampaign = !activeQ.data && activeQ.isError && isNotFound(activeQ.error);

  // Live and attention signals, on every screen.
  const { live, startedMs } = useLiveSession();
  // The same cache entry the Suggestions view reads, so approving or dismissing
  // one there moves this count without a second source of truth. retry:false: a
  // count that cannot be read is simply not shown.
  const proposalsQ = useQuery(
    CampaignService.method.listKnowledgeProposals,
    {},
    { staleTime: PROPOSALS_REFETCH_MS, retry: false, refetchInterval: live ? PROPOSALS_REFETCH_MS : false },
  );
  const pendingSuggestions = proposalsQ.data?.proposals.length ?? 0;

  // What the topbar and the browser tab call this page. Campaign views and the
  // Session screen are about ONE campaign, so they carry its name; Campaigns
  // and Setup are Tenant-wide and do not.
  const pageTitle = activeView
    ? t(campaignDestination(activeView).label)
    : activeScreen
      ? t(activeScreen.title)
      : null;
  const crumbCampaign = activeView || screen === "session" ? campaignName : undefined;

  // "<View> · <Campaign> · Glyphoxa" — with several Glyphoxa tabs open, an
  // all-"Glyphoxa" tab strip is a guessing game, and two campaigns' Cast tabs
  // are no better.
  const documentTitle = [pageTitle, crumbCampaign, "Glyphoxa"].filter(Boolean).join(" · ");
  useEffect(() => {
    document.title = documentTitle;
  }, [documentTitle]);

  // A new screen or view starts at its top. .gx-content is the scroll
  // container (the shell itself never scrolls), so without this a GM who
  // scrolled down the wiki would land halfway down Cast.
  const contentRef = useRef<HTMLElement>(null);
  useEffect(() => {
    if (contentRef.current) contentRef.current.scrollTop = 0;
  }, [screen, view]);

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

  const sectionLabelId = useId();

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
        {/* The brand is the way home: the tenant index, which opens the
            campaign's Overview. */}
        <Link
          className="gx-sidebar__brand"
          to="/t/$tenantSlug"
          params={{ tenantSlug }}
          aria-label={t("shell.home")}
          onClick={closeDrawerOnMobile}
        >
          <span className="gx-sidebar__sigil">
            <Dices size={17} />
          </span>
          <span className="gx-sidebar__wordmark gx-gradient-text">Glyphoxa</span>
        </Link>

        <nav className="gx-nav" aria-label={t("shell.navLabel")}>
          <div className="gx-nav__main">
            {/* The campaign's views — always rendered, whatever screen is open,
                so World wiki is one click from Session or Setup (ADR-0063
                amendment). The group is named by its heading: "Campaign" plus
                the Active Campaign's name. */}
            <div className="gx-nav__section" role="group" aria-labelledby={sectionLabelId}>
              <div className="gx-nav__section-head" id={sectionLabelId}>
                <span className="gx-overline">{t("shell.navCampaign")}</span>
                {campaignName ? (
                  // title: the name ellipsizes in a fixed-width sidebar.
                  <span className="gx-nav__section-name" title={campaignName}>
                    {campaignName}
                  </span>
                ) : noCampaign ? (
                  <span className="gx-nav__section-name gx-nav__section-name--none">
                    {t("shell.noCampaign")}
                  </span>
                ) : activeQ.isPending ? (
                  <span className="gx-skeleton" data-testid="nav-campaign-loading" />
                ) : null}
              </div>
              <div className="gx-nav__sub">
                {CAMPAIGN_DESTINATIONS.map((d) => {
                  const Icon = d.icon;
                  const label = t(d.label);
                  const isActive = activeView === d.view;
                  const count = d.view === "proposals" ? pendingSuggestions : 0;
                  return (
                    <Link
                      key={d.view}
                      className="gx-nav__subitem"
                      to="/t/$tenantSlug/$screen/$view"
                      params={{ tenantSlug, screen: "campaign", view: d.view }}
                      data-active={isActive ? "true" : undefined}
                      aria-current={isActive ? "page" : undefined}
                      // The count rides in the accessible name ("Suggestions,
                      // 3 pending"); the pill below is its visual twin.
                      aria-label={count > 0 ? t("shell.suggestionsPending", { label, count }) : undefined}
                      onClick={closeDrawerOnMobile}
                    >
                      <Icon size={16} aria-hidden="true" />
                      <span className="gx-nav__label">{label}</span>
                      {count > 0 && (
                        <span className="gx-nav__count" aria-hidden="true">
                          {count > 99 ? "99+" : count}
                        </span>
                      )}
                    </Link>
                  );
                })}
              </div>
            </div>

            <div className="gx-nav__rule" aria-hidden="true" />

            {SCREEN_DESTINATIONS.filter((d) => d.screen === "session").map((d) => {
              const Icon = d.icon;
              return (
                <Link
                  key={d.screen}
                  className="gx-nav__item"
                  to="/t/$tenantSlug/$screen"
                  params={{ tenantSlug, screen: d.screen }}
                  data-active={screen === d.screen ? "true" : undefined}
                  aria-current={screen === d.screen ? "page" : undefined}
                  onClick={closeDrawerOnMobile}
                >
                  <Icon size={18} aria-hidden="true" />
                  <span className="gx-nav__label">{t(d.label)}</span>
                  {live && <NavLiveMarker startedMs={startedMs} />}
                </Link>
              );
            })}
          </div>

          {/* Tenant-wide destinations, pinned under the scrolling list. */}
          <div className="gx-nav__foot">
            {SCREEN_DESTINATIONS.filter((d) => d.screen !== "session").map((d) => {
              const Icon = d.icon;
              return (
                <Link
                  key={d.screen}
                  className="gx-nav__item"
                  to="/t/$tenantSlug/$screen"
                  params={{ tenantSlug, screen: d.screen }}
                  data-active={screen === d.screen ? "true" : undefined}
                  aria-current={screen === d.screen ? "page" : undefined}
                  onClick={closeDrawerOnMobile}
                >
                  <Icon size={18} aria-hidden="true" />
                  <span className="gx-nav__label">{t(d.label)}</span>
                </Link>
              );
            })}
          </div>
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
            {pageTitle && crumbCampaign ? (
              // "<Campaign> / <View>": which campaign this page is about, then
              // where in it. The campaign crumb leads to its Overview — except
              // on the Overview itself, where a link to the current page would
              // be a dead control.
              <nav className="gx-crumbs" aria-label={t("shell.breadcrumb")}>
                <ol>
                  <li className="gx-crumbs__campaign">
                    {activeView === "overview" ? (
                      <span title={crumbCampaign}>{crumbCampaign}</span>
                    ) : (
                      <Link
                        to="/t/$tenantSlug/$screen/$view"
                        params={{ tenantSlug, screen: "campaign", view: "overview" }}
                        title={crumbCampaign}
                      >
                        {crumbCampaign}
                      </Link>
                    )}
                  </li>
                  <li className="gx-crumbs__page" aria-current="page">
                    <span className="gx-crumbs__sep" aria-hidden="true">
                      /
                    </span>
                    <span className="gx-topbar__title">{pageTitle}</span>
                  </li>
                </ol>
              </nav>
            ) : (
              <div className="gx-topbar__title">{pageTitle ?? "Glyphoxa"}</div>
            )}
          </div>
          {/* A Voice Session is live and the GM is somewhere else: say so, and
              offer the way back. On the Session screen itself the screen's own
              controls already do. */}
          {live && screen !== "session" && <LivePill tenantSlug={tenantSlug} startedMs={startedMs} />}
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

        <main className="gx-content" ref={contentRef}>
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

// The clocks are their own components on purpose: useElapsedSeconds re-renders
// its caller every second, and that caller must not be the whole shell.

// NavLiveMarker is the live cue on the sidebar's Session item: a pulsing dot
// and the session's running time. The word "Live" is there for screen readers;
// sighted users read the dot.
function NavLiveMarker({ startedMs }: { startedMs: number | null }) {
  const { t } = useI18n();
  const elapsed = useElapsedSeconds(startedMs);
  return (
    <span className="gx-nav__live" data-testid="nav-live">
      <span className="gx-live-dot" aria-hidden="true" />
      <span className="gx-shell__sr">{t("shell.live")}</span>
      {startedMs != null && <span className="gx-nav__live-time">{formatElapsed(elapsed)}</span>}
    </span>
  );
}

// LivePill is the topbar's "Live 01:12:40 · Open session" link to the Session
// screen. On a narrow topbar the words after the clock are hidden VISUALLY only
// (see appShell.css), so the link keeps its full accessible name.
function LivePill({ tenantSlug, startedMs }: { tenantSlug: string; startedMs: number | null }) {
  const { t } = useI18n();
  const elapsed = useElapsedSeconds(startedMs);
  return (
    <Link className="gx-live-pill" to="/t/$tenantSlug/$screen" params={{ tenantSlug, screen: "session" }}>
      <span className="gx-live-dot" aria-hidden="true" />
      <span>{t("shell.live")}</span>
      {startedMs != null && <span className="gx-live-pill__time">{formatElapsed(elapsed)}</span>}
      <span className="gx-live-pill__more">
        <span aria-hidden="true">·</span> {t("shell.openSession")}
      </span>
    </Link>
  );
}
