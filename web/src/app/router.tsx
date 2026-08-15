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
import { isCampaignView } from "@/screens/campaign/views";
import { useI18n } from "@/i18n";
import { Login } from "@/screens/login/Login";
import { CreateTenant } from "@/screens/onboarding/CreateTenant";
import { Configuration } from "@/screens/configuration/Configuration";
import { Campaign } from "@/screens/campaign/Campaign";
import { Session } from "@/screens/session/Session";
import { Placeholder } from "@/screens/Placeholder";
import { RootEntry } from "@/screens/landing/RootEntry";
import { Imprint } from "@/screens/legal/Imprint";
import { Privacy } from "@/screens/legal/Privacy";
import { Terms } from "@/screens/legal/Terms";

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
const imprintRoute = createRoute({ getParentRoute: () => rootRoute, path: "/imprint", component: Imprint });
const impressumRoute = createRoute({ getParentRoute: () => rootRoute, path: "/impressum", component: Imprint });
const privacyRoute = createRoute({ getParentRoute: () => rootRoute, path: "/privacy", component: Privacy });
const datenschutzRoute = createRoute({ getParentRoute: () => rootRoute, path: "/datenschutz", component: Privacy });
const termsRoute = createRoute({ getParentRoute: () => rootRoute, path: "/terms", component: Terms });
const nutzungsbedingungenRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/nutzungsbedingungen",
  component: Terms,
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
    return <AuthGate>{(user) => <AppShell tenantSlug={tenantSlug} user={user} />}</AuthGate>;
  },
});

// /t/:tenantSlug/ → redirect to the Configuration screen.
const tenantIndexRoute = createRoute({
  getParentRoute: () => tenantRoute,
  path: "/",
  beforeLoad: ({ params }) => {
    throw redirect({
      to: "/t/$tenantSlug/$screen",
      params: { tenantSlug: params.tenantSlug, screen: "configuration" },
    });
  },
});

// ScreenSearch is the deep-link vocabulary the command palette navigates with
// (#591): the Campaign screen consumes node (open the World-wiki panel on an
// entry), the Session screen consumes session+line (view a Voice Session and
// jump to a transcript Line — the pair, never the line alone: line ids restart
// per session) and session+highlight (scroll the Highlights strip to a row).
// Screens consume the params once and then STRIP them (replace, no history
// entry), so a later palette jump with the same target re-fires and a copied
// URL acts once instead of pinning the screen. `view` is legacy vocabulary:
// the Campaign sub-view moved into the path (ADR-0063), and screenRoute's
// beforeLoad rewrites an old ?view= link onto it.
export type ScreenSearch = {
  view?: string;
  node?: string;
  session?: string;
  line?: string;
  highlight?: string;
};

const str = (v: unknown): string | undefined => (typeof v === "string" && v !== "" ? v : undefined);

const validateScreenSearch = (search: Record<string, unknown>): ScreenSearch => ({
  view: str(search.view),
  node: str(search.node),
  session: str(search.session),
  line: str(search.line),
  highlight: str(search.highlight),
});

// /t/:tenantSlug/:screen — selects the active screen. Configuration and Campaign
// are live on their RPCs; Session renders a styled placeholder. The Campaign
// screen never renders here: its sub-view is a path segment (ADR-0063), so a
// bare /campaign visit redirects to the default sub-view — mapping a legacy
// ?view=/?node= deep link (#591) onto the path so old links keep working.
const screenRoute = createRoute({
  getParentRoute: () => tenantRoute,
  path: "$screen",
  validateSearch: validateScreenSearch,
  beforeLoad: ({ params, search }) => {
    if (params.screen !== "campaign") return;
    const view = search.node ? "knowledge" : isCampaignView(search.view) ? search.view : "cast";
    throw redirect({
      to: "/t/$tenantSlug/$screen/$view",
      params: { tenantSlug: params.tenantSlug, screen: "campaign", view },
      search: search.node ? { node: search.node } : {},
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
        return <Configuration />;
      case "session":
        return <Session deepLink={search} onDeepLinkHandled={clearDeepLink} />;
      default:
        return <Placeholder title={t("auth.notFoundTitle")} />;
    }
  },
});

// /t/:tenantSlug/campaign/:view — the Campaign screen with its sub-view in the
// path (ADR-0063), so the sidebar sub-navigation can link to and highlight it
// and a bookmark restores it. Declared as a generic $screen/$view sibling of
// screenRoute (only Campaign defines sub-views today; any other screen with a
// second segment is a notFound). Sub-view changes are param-only navigations
// within this one route, so the screen stays mounted and its local state
// (selected agent, wiki focus) survives a sidebar switch.
const subviewRoute = createRoute({
  getParentRoute: () => tenantRoute,
  path: "$screen/$view",
  validateSearch: validateScreenSearch,
  component: function SubviewScreen() {
    const { screen, view, tenantSlug } = subviewRoute.useParams();
    const search = subviewRoute.useSearch();
    const navigate = useNavigate();
    const { t } = useI18n();
    if (screen === "campaign" && isCampaignView(view)) {
      return (
        <Campaign
          view={view}
          onViewChange={(next) =>
            void navigate({
              to: "/t/$tenantSlug/$screen/$view",
              params: { tenantSlug, screen, view: next },
            })
          }
          deepLink={search}
          onDeepLinkHandled={() =>
            // Consume-then-strip, same contract as screenRoute (see ScreenSearch).
            void navigate({
              to: "/t/$tenantSlug/$screen/$view",
              params: { tenantSlug, screen, view },
              search: {},
              replace: true,
            })
          }
        />
      );
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

export const router = createRouter({
  routeTree,
  defaultPreload: "intent",
});

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}
