import { useMemo, type ReactNode } from "react";
import { useNavigate, useParams, useSearch } from "@tanstack/react-router";

import { DEFAULT_CAMPAIGN_VIEW, isCampaignView } from "@/screens/campaign/views";
import {
  SCREENS,
  ScreenNavProvider,
  compactSearch,
  type ScreenName,
  type ScreenNav,
  type ScreenSearch,
} from "./screenNav";

// RouterScreenNav is the route layer's half of the navigation seam: it turns
// the router-free ScreenNav vocabulary (./screenNav.tsx) into TanStack Router
// navigations. Mounted once by the tenant layout, ABOVE the shell, so shell
// components (the sidebar, the campaign switcher) and the screens in the
// Outlet share one implementation.
//
// It reads params and search non-strictly: the layout route itself matches
// neither $screen nor $view, but the hooks answer for the deepest match, which
// is what "where is the app right now" means here.

function isScreen(v: unknown): v is ScreenName {
  return typeof v === "string" && (SCREENS as readonly string[]).includes(v);
}

export function RouterScreenNav({ tenantSlug, children }: { tenantSlug: string; children: ReactNode }) {
  const navigate = useNavigate();
  const params = useParams({ strict: false }) as { screen?: string; view?: string };
  const rawSearch = useSearch({ strict: false }) as Record<string, unknown>;

  const screen = isScreen(params.screen) ? params.screen : undefined;
  const view = screen === "campaign" && isCampaignView(params.view) ? params.view : undefined;

  // Rebuilt from the raw search each render, then memoized on its VALUE: the
  // router hands out a fresh object per navigation even when nothing a screen
  // reads has changed, and an effect keyed on `search` must not re-fire then.
  const search = compactSearch(rawSearch as ScreenSearch);
  const searchKey = JSON.stringify(search);

  const nav = useMemo<ScreenNav>(
    () => {
      const current: ScreenSearch = JSON.parse(searchKey) as ScreenSearch;
      const goCampaignView: ScreenNav["goCampaignView"] = (next, s) =>
        void navigate({
          to: "/t/$tenantSlug/$screen/$view",
          params: { tenantSlug, screen: "campaign", view: next },
          search: compactSearch(s ?? {}),
        });
      return {
        screen,
        view,
        search: current,
        setSearch: (patch, opts) => {
          // A function of the LIVE search, not of the snapshot this closure
          // captured: two patches issued in the same tick must both land.
          const merge = (prev: Record<string, unknown>) =>
            compactSearch({ ...(prev as ScreenSearch), ...patch });
          if (params.screen && params.view) {
            void navigate({
              to: "/t/$tenantSlug/$screen/$view",
              params: { tenantSlug, screen: params.screen, view: params.view },
              search: merge,
              replace: opts?.replace,
            });
          } else if (params.screen) {
            void navigate({
              to: "/t/$tenantSlug/$screen",
              params: { tenantSlug, screen: params.screen },
              search: merge,
              replace: opts?.replace,
            });
          }
        },
        goScreen: (next, s) => {
          // The Campaign screen has no view-less URL: go straight to its
          // default view rather than through the /campaign redirect, so the
          // search params arrive untouched.
          if (next === "campaign") {
            goCampaignView(DEFAULT_CAMPAIGN_VIEW, s);
            return;
          }
          void navigate({
            to: "/t/$tenantSlug/$screen",
            params: { tenantSlug, screen: next },
            search: compactSearch(s ?? {}),
          });
        },
        goCampaignView,
      };
    },
    [navigate, tenantSlug, screen, view, searchKey, params.screen, params.view],
  );

  return <ScreenNavProvider value={nav}>{children}</ScreenNavProvider>;
}
