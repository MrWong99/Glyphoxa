import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import {
  RouterProvider,
  createRootRoute,
  createRoute,
  createRouter,
  createMemoryHistory,
} from "@tanstack/react-router";

import { I18nProvider } from "@/i18n";
import { Placeholder } from "./Placeholder";

// A minimal tree mirroring the shell's /t/$tenantSlug/$screen shape, so the
// placeholder's Setup link resolves against the visited tenant.
function renderAt(path: string) {
  const root = createRootRoute();
  const screenRoute = createRoute({
    getParentRoute: () => root,
    path: "t/$tenantSlug/$screen",
    component: () => <Placeholder title="Not found" />,
  });
  const router = createRouter({
    routeTree: root.addChildren([screenRoute]),
    history: createMemoryHistory({ initialEntries: [path] }),
  });
  render(
    <I18nProvider>
      <RouterProvider router={router} />
    </I18nProvider>,
  );
}

describe("Placeholder (unknown screen)", () => {
  it("says the page does not exist and links to Setup — no 'coming soon' promise", async () => {
    renderAt("/t/acme/nonsense");
    expect(await screen.findByText(/this page doesn't exist/i)).toBeInTheDocument();
    expect(screen.queryByText(/coming/i)).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: /go to setup/i })).toHaveAttribute("href", "/t/acme/configuration");
  });
});
