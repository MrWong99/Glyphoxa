import {
  createRootRoute,
  createRoute,
  createRouter,
  redirect,
  Outlet,
} from "@tanstack/react-router";

import { AppShell } from "@/components/AppShell";
import { AuthGate } from "@/app/AuthGate";
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

// /t/:tenantSlug/:screen — selects the active screen. Configuration and Campaign
// are live on their RPCs; Session renders a styled placeholder.
const screenRoute = createRoute({
  getParentRoute: () => tenantRoute,
  path: "$screen",
  component: function Screen() {
    const { screen } = screenRoute.useParams();
    // useI18n is called unconditionally (hook rules) even though only the
    // notFound branch renders localized copy of its own.
    const { t } = useI18n();
    switch (screen) {
      case "configuration":
        return <Configuration />;
      case "campaign":
        return <Campaign />;
      case "session":
        return <Session />;
      default:
        return <Placeholder title={t("auth.notFoundTitle")} />;
    }
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
  tenantRoute.addChildren([tenantIndexRoute, screenRoute]),
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
