import { lazy, Suspense, useEffect, type ReactNode } from "react";
import {
  createRootRoute,
  createRoute,
  createRouter,
  redirect,
  Outlet,
  useNavigate,
} from "@tanstack/react-router";

import { AppShell } from "@/components/AppShell";
import { AuthGate } from "@/app/AuthGate";
import { RouterScreenNav } from "@/app/RouterScreenNav";
import type { ScreenSearch as NavSearch } from "@/app/screenNav";
import { DEFAULT_CAMPAIGN_VIEW, isCampaignView } from "@/screens/campaign/views";
import { useI18n } from "@/i18n";
import { Login } from "@/screens/login/Login";
import { CreateTenant } from "@/screens/onboarding/CreateTenant";
import { Placeholder } from "@/screens/Placeholder";
import { RootEntry } from "@/screens/landing/RootEntry";
import { NavigationGuard, RouteError, RouteNotFound, RoutePending } from "@/app/RouteFallbacks";

// Route-level code splitting: the four app screens and the legal documents
// are separate chunks, fetched when first visited. The entry bundle keeps what
// every visit needs before it knows where it is going — the router, the public
// landing/login pages and the shell. Each import() is named once here so the
// shell can also warm the chunks in the background (preloadScreens).
const screenChunks = {
  configuration: () => import("@/screens/configuration/Configuration"),
  campaign: () => import("@/screens/campaign/Campaign"),
  session: () => import("@/screens/session/Session"),
  campaigns: () => import("@/screens/campaigns/Campaigns"),
};
const Configuration = lazy(() => screenChunks.configuration().then((m) => ({ default: m.Configuration })));
const Campaign = lazy(() => screenChunks.campaign().then((m) => ({ default: m.Campaign })));
const Session = lazy(() => screenChunks.session().then((m) => ({ default: m.Session })));
const Campaigns = lazy(() => screenChunks.campaigns().then((m) => ({ default: m.Campaigns })));
const Imprint = lazy(() => import("@/screens/legal/Imprint").then((m) => ({ default: m.Imprint })));
const Privacy = lazy(() => import("@/screens/legal/Privacy").then((m) => ({ default: m.Privacy })));
const Terms = lazy(() => import("@/screens/legal/Terms").then((m) => ({ default: m.Terms })));

// preloadScreens fetches the other app screens' chunks once the shell is up,
// so the first click on a sidebar item does not wait on the network. A failed
// warm-up is silent: the real navigation retries and reports through the
// error boundary.
function preloadScreens() {
  for (const load of Object.values(screenChunks)) void load().catch(() => {});
}

// loaded wraps a lazily loaded screen in its own Suspense boundary, so a chunk
// in flight shows the pending placeholder INSIDE the shell instead of blanking
// the sidebar and topbar around it.
function loaded(screen: ReactNode) {
  return <Suspense fallback={<RoutePending />}>{screen}</Suspense>;
}

// Code-based route tree (ADR-0018). The Tenant lives in the path
// (/t/:tenantSlug/...) so URLs are bookmarkable; for the single-operator MVP
// (ADR-0039) the slug is a thin pass-through. The shell is wrapped in the
// AuthGate (ADR-0016): it probes GetCurrentUser at boot and redirects to /login
// on a 401, then hands the real operator identity to the shell.

const rootRoute = createRootRoute({
  component: () => <Outlet />,
});

// /login — the Discord-only OAuth entry (ADR-0016). It lives OUTSIDE the tenant
// shell so it never triggers the AuthGate (which would loop). The OAuth callback
// bounces an operator-allowlist rejection here with ?error=not_authorized
// (ADR-0041), and the server-side AUP gate (#518) bounces an unacknowledged
// open-mode OAuth start/round-trip with ?error=aup_required; validateSearch
// surfaces both as a typed search param so the screen can render the matching
// banner.
const loginRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/login",
  validateSearch: (search: Record<string, unknown>): { error?: string } => ({
    error: typeof search.error === "string" ? search.error : undefined,
  }),
  component: function LoginScreen() {
    const { error } = loginRoute.useSearch();
    return <Login notAuthorized={error === "not_authorized"} aupRequired={error === "aup_required"} />;
  },
});

// /onboarding/create-tenant — the ADR-0055 open-mode name-your-Tenant step. The
// OAuth callback 302s a FRESH open-mode signup here (onboardingRedirect in
// internal/auth/oauth.go), so the path is a Go↔TS contract pinned by
// router.test.tsx. A top-level sibling of /login: it lives OUTSIDE the tenant
// shell, so no AppShell chrome and no AuthGate (the screen runs its own session
// probe and bounces an unauthenticated visit to /login itself).
const onboardingRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/onboarding/create-tenant",
  component: CreateTenant,
});

// The legal documents (#518): Impressum, Datenschutzerklärung and
// Nutzungsbedingungen. Top-level siblings of /login — OUTSIDE the tenant shell
// and the AuthGate — because they must be readable with no session at all: by a
// visitor deciding whether to sign up, and by a player at a transcribed table
// who will never open the app. /privacy is additionally a Go↔ops contract: the
// chart derives GLYPHOXA_PRIVACY_POLICY_URL as <host>/privacy for the Bot's
// in-channel transcription disclosure (#519), so the path must not drift.
// The German aliases are there because that is what people type.
// Each route is declared individually rather than mapped from a list: TanStack
// Router infers the typed route registry from the literal `addChildren` tree, so
// a spread of mapped routes would compile but leave <Link to="/privacy"> a type
// error (and unnavigable).
const ImprintPage = () => loaded(<Imprint />);
const PrivacyPage = () => loaded(<Privacy />);
const TermsPage = () => loaded(<Terms />);
const imprintRoute = createRoute({ getParentRoute: () => rootRoute, path: "/imprint", component: ImprintPage });
const impressumRoute = createRoute({ getParentRoute: () => rootRoute, path: "/impressum", component: ImprintPage });
const privacyRoute = createRoute({ getParentRoute: () => rootRoute, path: "/privacy", component: PrivacyPage });
const datenschutzRoute = createRoute({ getParentRoute: () => rootRoute, path: "/datenschutz", component: PrivacyPage });
const termsRoute = createRoute({ getParentRoute: () => rootRoute, path: "/terms", component: TermsPage });
const nutzungsbedingungenRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/nutzungsbedingungen",
  component: TermsPage,
});

// "/" → the public landing page for a visitor with no session, and a straight
// redirect into the app for one who has (#521). The decision needs the session
// probe's answer, so it lives in the component rather than a beforeLoad
// redirect — a redirect here could only ever guess.
const indexRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/",
  component: RootEntry,
});

// /t/:tenantSlug — the persistent shell hosting the screen Outlet, gated by the
// AuthGate so an unauthenticated visit redirects to /login and an authenticated
// one renders the shell with the real operator identity.
const tenantRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "t/$tenantSlug",
  component: function TenantLayout() {
    const { tenantSlug } = tenantRoute.useParams();
    // Warm the other screens' chunks shortly after the shell paints. Skipped
    // under test, where a stray late import only produces act() noise.
    useEffect(() => {
      if (import.meta.env.MODE === "test") return;
      const timer = setTimeout(preloadScreens, 1500);
      return () => clearTimeout(timer);
    }, []);
    return (
      // The navigation seam (ADR-0063 amendment) sits ABOVE the shell, so the
      // sidebar, the campaign switcher and the screen in the Outlet all move
      // through the same useScreenNav() and none of them imports the router.
      <RouterScreenNav tenantSlug={tenantSlug}>
        <AuthGate>{(user) => <AppShell tenantSlug={tenantSlug} user={user} />}</AuthGate>
        {/* Holds a navigation away while an editor has unsaved edits (W10). */}
        <NavigationGuard />
      </RouterScreenNav>
    );
  },
});

// /t/:tenantSlug/ → the campaign's Overview (ADR-0063 amendment): a returning
// GM lands on the home of their campaign, not on API keys. A Tenant with no
// campaign yet lands there too — the Campaign screen shows the create-first-
// campaign card on CodeNotFound — and a FRESH signup still goes to Setup,
// because CreateTenant navigates there itself.
const tenantIndexRoute = createRoute({
  getParentRoute: () => tenantRoute,
  path: "/",
  beforeLoad: ({ params }) => {
    throw redirect({
      to: "/t/$tenantSlug/$screen/$view",
      params: { tenantSlug: params.tenantSlug, screen: "campaign", view: DEFAULT_CAMPAIGN_VIEW },
      replace: true,
    });
  },
});

// ScreenSearch is the search vocabulary of the app screens — every key an id
// (app/screenNav.tsx owns the typed shape screens read and write):
//
//   node      a World-wiki entry        (Campaign · knowledge)
//   agent     a cast member             (Campaign · cast)
//   map       a map                     (Campaign · maps)
//   thread    a planning-chat thread    (Campaign · planning)
//   session   a Voice Session           (Session)
//   line      a Transcript Line         (Session, with session — line ids
//                                        restart per session, never alone)
//   highlight a Session Highlight       (Session, with session)
//
// Two lifetimes exist (ADR-0063 amendment). ONE-SHOT keys are a deep link the
// screen applies and then STRIPS (replace, no history entry), so a later jump
// to the same target re-fires and a copied URL acts once: line, highlight and
// — today — node and agent. STICKY keys are a screen's selection held in the
// URL for as long as it is selected, so Back steps through selections and a
// reload restores them: map, thread and session are reserved for that, and
// node becomes sticky when the wiki adopts it. The route does not care which
// is which; the screen that owns a key decides when to strip it.
//
// `view` is legacy vocabulary only: the Campaign sub-view moved into the path
// (ADR-0063), and screenRoute's beforeLoad rewrites an old ?view= link onto it.
type ScreenSearch = NavSearch & { view?: string };

const str = (v: unknown): string | undefined => (typeof v === "string" && v !== "" ? v : undefined);

const validateScreenSearch = (search: Record<string, unknown>): ScreenSearch => ({
  view: str(search.view),
  node: str(search.node),
  session: str(search.session),
  line: str(search.line),
  highlight: str(search.highlight),
  map: str(search.map),
  thread: str(search.thread),
  agent: str(search.agent),
});

// campaignViewFor says which sub-view a set of search params belongs on, or
// undefined when they leave the choice open. ?node= only means something on
// the wiki and ?agent= only on Cast; owning that correction at the ROUTE keeps
// each screen's deep-link handling a single consume-then-strip — a
// screen-issued "switch view, then strip" pair would race (the strip would
// carry the pre-switch :view and undo the switch).
function campaignViewFor(search: ScreenSearch) {
  if (search.node) return "knowledge" as const;
  if (search.agent) return "cast" as const;
  return undefined;
}

// carried keeps only the param that decided the view, so a redirect never
// drags an unrelated key along.
function carried(search: ScreenSearch): NavSearch {
  if (search.node) return { node: search.node };
  if (search.agent) return { agent: search.agent };
  return {};
}

// /t/:tenantSlug/:screen — selects the active screen: Setup (configuration),
// Session and the Campaigns list. The Campaign screen never renders here: its
// sub-view is a path segment (ADR-0063), so a bare /campaign visit redirects
// to a sub-view — the one its search params belong on (a legacy ?node= link,
// #591, or ?agent=), else a legacy ?view= value, else the Overview.
const screenRoute = createRoute({
  getParentRoute: () => tenantRoute,
  path: "$screen",
  validateSearch: validateScreenSearch,
  beforeLoad: ({ params, search }) => {
    if (params.screen !== "campaign") return;
    const view =
      campaignViewFor(search) ?? (isCampaignView(search.view) ? search.view : DEFAULT_CAMPAIGN_VIEW);
    throw redirect({
      to: "/t/$tenantSlug/$screen/$view",
      params: { tenantSlug: params.tenantSlug, screen: "campaign", view },
      search: carried(search),
      replace: true,
    });
  },
  component: function Screen() {
    const { screen, tenantSlug } = screenRoute.useParams();
    const search = screenRoute.useSearch();
    const navigate = useNavigate();
    // useI18n is called unconditionally (hook rules) even though only the
    // notFound branch renders localized copy of its own.
    const { t } = useI18n();
    // Consume-then-strip: the screen applies the deep link, then this replaces
    // the URL without the params so the state isn't sticky (see ScreenSearch).
    const clearDeepLink = () =>
      void navigate({
        to: "/t/$tenantSlug/$screen",
        params: { tenantSlug, screen },
        search: {},
        replace: true,
      });
    switch (screen) {
      case "configuration":
        return loaded(<Configuration />);
      case "session":
        return loaded(<Session deepLink={search} onDeepLinkHandled={clearDeepLink} />);
      case "campaigns":
        return loaded(<Campaigns />);
      default:
        return <Placeholder title={t("auth.notFoundTitle")} />;
    }
  },
});

// /t/:tenantSlug/campaign/:view — the Campaign screen with its sub-view in the
// path (ADR-0063), so the sidebar can link to and highlight it and a bookmark
// restores it. Declared as a generic $screen/$view sibling of screenRoute (only
// Campaign defines sub-views today; any other screen with a second segment is a
// notFound). Sub-view changes are param-only navigations within this one route,
// so the screen stays mounted across a sidebar switch.
const subviewRoute = createRoute({
  getParentRoute: () => tenantRoute,
  path: "$screen/$view",
  validateSearch: validateScreenSearch,
  beforeLoad: ({ params, search }) => {
    if (params.screen !== "campaign") return;
    const wanted = campaignViewFor(search);
    // An unknown view (a typo, a view that was renamed away) lands on the
    // Overview rather than a dead end — unless its search params say where the
    // link meant to go.
    if (!isCampaignView(params.view)) {
      throw redirect({
        to: "/t/$tenantSlug/$screen/$view",
        params: {
          tenantSlug: params.tenantSlug,
          screen: "campaign",
          view: wanted ?? DEFAULT_CAMPAIGN_VIEW,
        },
        search: carried(search),
        replace: true,
      });
    }
    // A hand-edited URL pairing ?node= / ?agent= with another view goes to the
    // view the param belongs on (see campaignViewFor).
    if (wanted && params.view !== wanted) {
      throw redirect({
        to: "/t/$tenantSlug/$screen/$view",
        params: { tenantSlug: params.tenantSlug, screen: "campaign", view: wanted },
        search: carried(search),
        replace: true,
      });
    }
  },
  component: function SubviewScreen() {
    const { screen, view } = subviewRoute.useParams();
    const { t } = useI18n();
    if (screen === "campaign" && isCampaignView(view)) {
      // The screen is told only WHICH view the path names. Everything it used
      // to be handed as callbacks — view changes, the ?node= focus, the jump to
      // a Transcript Line on the Session screen — it now reaches through
      // useScreenNav(), provided by the tenant layout above (ADR-0063
      // amendment), so the screens stay router-free (and router-mock-free in
      // tests).
      return loaded(<Campaign view={view} />);
    }
    return <Placeholder title={t("auth.notFoundTitle")} />;
  },
});

// Exported so tests can mount the real tree on a memory history and pin the
// Go↔TS ?error=not_authorized contract (see router.test.tsx).
export const routeTree = rootRoute.addChildren([
  indexRoute,
  loginRoute,
  onboardingRoute,
  imprintRoute,
  impressumRoute,
  privacyRoute,
  datenschutzRoute,
  termsRoute,
  nutzungsbedingungenRoute,
  tenantRoute.addChildren([tenantIndexRoute, screenRoute, subviewRoute]),
]);

// The fallbacks every route inherits (ADR-0018): a thrown render/loader error
// and an unmatched URL both render in the app's own styling and language
// instead of the router's bare English defaults. Exported with the tree so
// tests mount the same configuration production runs.
export const routerDefaults = {
  defaultErrorComponent: RouteError,
  defaultNotFoundComponent: RouteNotFound,
  defaultPendingComponent: RoutePending,
} as const;

export const router = createRouter({
  routeTree,
  defaultPreload: "intent",
  ...routerDefaults,
});

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}
