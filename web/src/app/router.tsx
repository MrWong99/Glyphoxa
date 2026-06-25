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
import { Configuration } from "@/screens/configuration/Configuration";
import { Campaign } from "@/screens/campaign/Campaign";
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
// shell so it never triggers the AuthGate (which would loop).
const loginRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/login",
  component: Login,
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
        return <Placeholder title="Session" />;
      default:
        return <Placeholder title="Not found" />;
    }
  },
});

const routeTree = rootRoute.addChildren([
  indexRoute,
  loginRoute,
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
