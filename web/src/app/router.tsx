import {
  createRootRoute,
  createRoute,
  createRouter,
  redirect,
  Outlet,
} from "@tanstack/react-router";

import { AppShell } from "@/components/AppShell";
import { AuthGate } from "@/app/AuthGate";
import { Login } from "@/screens/login/Login";
import { CreateTenant } from "@/screens/onboarding/CreateTenant";
import { Configuration } from "@/screens/configuration/Configuration";
import { Campaign } from "@/screens/campaign/Campaign";
import { Session } from "@/screens/session/Session";
import { Placeholder } from "@/screens/Placeholder";

// Code-based route tree (ADR-0018). The Tenant lives in the path
// (/t/:tenantSlug/...) so URLs are bookmarkable; for the single-operator MVP
// (ADR-0039) the slug is a thin pass-through. The shell is wrapped in the
// AuthGate (ADR-0016): it probes GetCurrentUser at boot and redirects to /login
// on a 401, then hands the real operator identity to the shell.

const DEFAULT_TENANT = "default";

const rootRoute = createRootRoute({
  component: () => <Outlet />,
});

// /login — the Discord-only OAuth entry (ADR-0016). It lives OUTSIDE the tenant
// shell so it never triggers the AuthGate (which would loop). The OAuth callback
// bounces an operator-allowlist rejection here with ?error=not_authorized
// (ADR-0041); validateSearch surfaces it as a typed search param so the screen
// can render the not-authorized banner.
const loginRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/login",
  validateSearch: (search: Record<string, unknown>): { error?: string } => ({
    error: typeof search.error === "string" ? search.error : undefined,
  }),
  component: function LoginScreen() {
    const { error } = loginRoute.useSearch();
    return <Login notAuthorized={error === "not_authorized"} />;
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

// "/" → the default tenant's Configuration (boot flow stand-in for this stage).
const indexRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/",
  beforeLoad: () => {
    throw redirect({
      to: "/t/$tenantSlug/$screen",
      params: { tenantSlug: DEFAULT_TENANT, screen: "configuration" },
    });
  },
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
    switch (screen) {
      case "configuration":
        return <Configuration />;
      case "campaign":
        return <Campaign />;
      case "session":
        return <Session />;
      default:
        return <Placeholder title="Not found" />;
    }
  },
});

// Exported so tests can mount the real tree on a memory history and pin the
// Go↔TS ?error=not_authorized contract (see router.test.tsx).
export const routeTree = rootRoute.addChildren([
  indexRoute,
  loginRoute,
  onboardingRoute,
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
