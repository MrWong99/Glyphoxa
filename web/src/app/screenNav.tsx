import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react";

import { registerGuardedSearchKeys } from "@/lib/navigationGuard";
import { DEFAULT_CAMPAIGN_VIEW, type CampaignView } from "@/screens/campaign/views";

// The navigation seam between screens and the router (ADR-0063 amendment).
//
// Screens never import @tanstack/react-router: they ask useScreenNav() to go
// somewhere or to keep a selection in the URL, and the route layer
// (RouterScreenNav, mounted by the tenant layout) turns that into real
// navigations. (The shell's own chrome is the exception: AppShell and the
// not-found page render the router's Link — a real anchor the browser can open
// in a new tab — and the command palette and the user menu, which predate this
// seam, still call useNavigate.) Two things fall out of that:
//
//   - a screen test needs no router and no router mock — outside a provider
//     the hook answers with an inert default (empty search, no-op moves), and
//     a test that wants to observe navigation mounts MemoryScreenNav;
//   - the URL shape stays one file's business. The Active Campaign is ambient
//     today (ADR-0039); if the campaign id ever moves onto the path (#692),
//     the screens do not change.
//
// This module is deliberately router-free. The router-bound provider lives in
// ./RouterScreenNav.tsx.

/**
 * The search vocabulary a screen may read and write. Every key is an id:
 *
 *   node      a World-wiki entry (Campaign · knowledge)
 *   agent     a cast member (Campaign · cast)
 *   map       a map (Campaign · maps)
 *   thread    a planning-chat thread (Campaign · planning)
 *   session   a Voice Session (Session)
 *   line      a Transcript Line within `session` (Session; one-shot)
 *   highlight a Session Highlight within `session` (Session; one-shot)
 */
export type ScreenSearch = {
  node?: string;
  session?: string;
  line?: string;
  highlight?: string;
  map?: string;
  thread?: string;
  agent?: string;
};

export const SCREEN_SEARCH_KEYS = [
  "node",
  "session",
  "line",
  "highlight",
  "map",
  "thread",
  "agent",
] as const satisfies readonly (keyof ScreenSearch)[];

/** The top-level screens under /t/:tenantSlug/. */
export const SCREENS = ["campaign", "session", "campaigns", "configuration"] as const;
export type ScreenName = (typeof SCREENS)[number];

export type ScreenNav = {
  /** The screen the URL is on, when it is one of ours. */
  screen?: ScreenName;
  /** The Campaign sub-view the URL is on (only under the campaign screen). */
  view?: CampaignView;
  /** The current, validated search params. */
  search: ScreenSearch;
  /**
   * Changes search params on the CURRENT screen, keeping every key the patch
   * does not name. A key patched to `undefined` is removed. `replace` rewrites
   * the history entry instead of pushing one — use it for the one-shot
   * "consume then strip" keys; leave it off for a selection the Back button
   * should step through.
   */
  setSearch: (patch: ScreenSearch, opts?: { replace?: boolean }) => void;
  /** Goes to a top-level screen (`campaign` lands on its default view). */
  goScreen: (screen: ScreenName, search?: ScreenSearch) => void;
  /** Goes to a Campaign sub-view. */
  goCampaignView: (view: CampaignView, search?: ScreenSearch) => void;
};

const NOOP_NAV: ScreenNav = {
  search: {},
  setSearch: () => {},
  goScreen: () => {},
  goCampaignView: () => {},
};

const ScreenNavContext = createContext<ScreenNav>(NOOP_NAV);

/** Hands a ScreenNav implementation to everything below. */
export function ScreenNavProvider({ value, children }: { value: ScreenNav; children: ReactNode }) {
  return <ScreenNavContext.Provider value={value}>{children}</ScreenNavContext.Provider>;
}

/**
 * The navigation seam. Inert outside a provider, so a component under test
 * renders without a router.
 */
export function useScreenNav(): ScreenNav {
  return useContext(ScreenNavContext);
}

/**
 * Declares search keys as this component's guarded selection state for as long
 * as it is mounted: while an editor reports unsaved edits, a navigation that
 * changes one of them (browser Back to the previous entry, a palette jump) asks
 * first, exactly like a navigation to another screen does.
 */
export function useGuardedSearchKeys(...keys: (keyof ScreenSearch)[]): void {
  // The keys are a call-site constant; joining them keeps the effect from
  // re-running on every render's fresh rest array.
  const signature = keys.join(",");
  useEffect(
    () => registerGuardedSearchKeys(signature === "" ? [] : signature.split(",")),
    [signature],
  );
}

/** Drops the keys whose value is undefined or empty. */
export function compactSearch(search: ScreenSearch): ScreenSearch {
  const out: ScreenSearch = {};
  for (const key of SCREEN_SEARCH_KEYS) {
    const value = search[key];
    if (typeof value === "string" && value !== "") out[key] = value;
  }
  return out;
}

export type ScreenLocation = { screen?: ScreenName; view?: CampaignView; search: ScreenSearch };

/**
 * An in-memory ScreenNav: the stand-in for the route layer in tests (and
 * anywhere a screen is mounted outside the router). It holds the location in
 * state and applies the same rules the router-bound provider does, so a test
 * drives real navigation semantics — setSearch keeping the other keys, a view
 * change dropping the search — without a router.
 *
 *   <MemoryScreenNav initial={{ screen: "campaign", view: "cast" }} onNavigate={spy}>
 *     {(loc) => <Campaign view={loc.view} />}
 *   </MemoryScreenNav>
 */
export function MemoryScreenNav({
  initial,
  onNavigate,
  children,
}: {
  initial?: Partial<ScreenLocation>;
  /** Called with every location the tree navigates to. */
  onNavigate?: (next: ScreenLocation, opts: { replace: boolean }) => void;
  children: ReactNode | ((location: ScreenLocation) => ReactNode);
}) {
  const [location, setLocation] = useState<ScreenLocation>(() => ({
    screen: initial?.screen,
    view: initial?.view,
    search: compactSearch(initial?.search ?? {}),
  }));

  const move = useCallback(
    (next: ScreenLocation, replace: boolean) => {
      setLocation(next);
      onNavigate?.(next, { replace });
    },
    [onNavigate],
  );

  const nav = useMemo<ScreenNav>(
    () => ({
      screen: location.screen,
      view: location.view,
      search: location.search,
      setSearch: (patch, opts) =>
        move(
          { ...location, search: compactSearch({ ...location.search, ...patch }) },
          opts?.replace ?? false,
        ),
      goScreen: (screen, search) =>
        move(
          {
            screen,
            // The router lands a bare /campaign on its default view.
            view: screen === "campaign" ? DEFAULT_CAMPAIGN_VIEW : undefined,
            search: compactSearch(search ?? {}),
          },
          false,
        ),
      goCampaignView: (view, search) =>
        move({ screen: "campaign", view, search: compactSearch(search ?? {}) }, false),
    }),
    [location, move],
  );

  return (
    <ScreenNavProvider value={nav}>
      {typeof children === "function" ? children(location) : children}
    </ScreenNavProvider>
  );
}
