import { describe, it, expect } from "vitest";
import { render, screen, within } from "@testing-library/react";
import {
  RouterProvider,
  createRootRoute,
  createRoute,
  createRouter,
  createMemoryHistory,
} from "@tanstack/react-router";

import { I18nProvider } from "@/i18n";
import { Placeholder } from "./Placeholder";

// A minimal tree mirroring the shell's /t/$tenantSlug/$screen(/$view) shape, so
// the placeholder's links resolve against the visited tenant.
function renderAt(path: string) {
  const root = createRootRoute();
  const screenRoute = createRoute({
    getParentRoute: () => root,
    path: "t/$tenantSlug/$screen",
    component: () => <Placeholder title="Not found" />,
  });
  const viewRoute = createRoute({
    getParentRoute: () => root,
    path: "t/$tenantSlug/$screen/$view",
    component: () => <Placeholder title="Not found" />,
  });
  const router = createRouter({
    routeTree: root.addChildren([screenRoute, viewRoute]),
    history: createMemoryHistory({ initialEntries: [path] }),
  });
  render(
    <I18nProvider>
      <RouterProvider router={router} />
    </I18nProvider>,
  );
}

describe("Placeholder (unknown screen)", () => {
  it("says the page does not exist — no 'coming soon' promise", async () => {
    renderAt("/t/acme/nonsense");
    expect(await screen.findByText(/this page doesn't exist/i)).toBeInTheDocument();
    expect(screen.queryByText(/coming/i)).not.toBeInTheDocument();
  });

  it("lists every destination that does exist, as links into the visited tenant", async () => {
    renderAt("/t/acme/nonsense");
    const list = await screen.findByRole("navigation", { name: "Go somewhere that exists" });
    const expected: [string, string][] = [
      // The campaign's views, in sidebar order…
      ["Overview", "/t/acme/campaign/overview"],
      ["Cast", "/t/acme/campaign/cast"],
      ["World wiki", "/t/acme/campaign/knowledge"],
      ["Maps", "/t/acme/campaign/maps"],
      ["Players", "/t/acme/campaign/players"],
      ["Suggestions", "/t/acme/campaign/proposals"],
      ["Planning", "/t/acme/campaign/planning"],
      ["Settings", "/t/acme/campaign/settings"],
      // …then the other screens.
      ["Session", "/t/acme/session"],
      ["Campaigns", "/t/acme/campaigns"],
      ["Setup", "/t/acme/configuration"],
    ];
    const links = within(list).getAllByRole("link");
    expect(links.map((a) => [a.textContent, a.getAttribute("href")])).toEqual(expected);
  });

  it("renders for a screen with a stray second segment too", async () => {
    renderAt("/t/acme/session/extra");
    const list = await screen.findByRole("navigation", { name: "Go somewhere that exists" });
    expect(within(list).getByRole("link", { name: "Setup" })).toHaveAttribute("href", "/t/acme/configuration");
  });
});
