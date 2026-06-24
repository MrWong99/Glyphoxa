import {
  createRootRoute,
  createRoute,
  createRouter,
  redirect,
  Outlet,
} from "@tanstack/react-router";

import { AppShell } from "@/components/AppShell";
import { Configuration } from "@/screens/configuration/Configuration";
import { Placeholder } from "@/screens/Placeholder";

// Code-based route tree (ADR-0018). The Tenant lives in the path
// (/t/:tenantSlug/...) so URLs are bookmarkable; for the single-operator MVP
// (ADR-0039) the slug is a thin pass-through. The boot flow's auth/onboarding
// branches are deferred to later stages — this stage redirects the root to the
// default tenant's Configuration screen.

const DEFAULT_TENANT = "default";

const rootRoute = createRootRoute({
  component: () => <Outlet />,
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

// /t/:tenantSlug — the persistent shell hosting the screen Outlet.
const tenantRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "t/$tenantSlug",
  component: function TenantLayout() {
    const { tenantSlug } = tenantRoute.useParams();
    return <AppShell tenantSlug={tenantSlug} />;
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

// /t/:tenantSlug/:screen — selects the active screen. Only Configuration is
// live this stage; Campaign and Session render styled placeholders.
const screenRoute = createRoute({
  getParentRoute: () => tenantRoute,
  path: "$screen",
  component: function Screen() {
    const { screen } = screenRoute.useParams();
    switch (screen) {
      case "configuration":
        return <Configuration />;
      case "campaign":
        return <Placeholder title="Campaign" />;
      case "session":
        return <Placeholder title="Session" />;
      default:
        return <Placeholder title="Not found" />;
    }
  },
});

const routeTree = rootRoute.addChildren([
  indexRoute,
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
