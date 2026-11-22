import { describe, it, expect } from "vitest";
import { act, render, screen, fireEvent, waitFor } from "@testing-library/react";
import {
  Outlet,
  RouterProvider,
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
} from "@tanstack/react-router";

import { guardedSearchChanged } from "@/lib/navigationGuard";
import { RouterScreenNav } from "./RouterScreenNav";
import {
  MemoryScreenNav,
  compactSearch,
  useGuardedSearchKeys,
  useScreenNav,
  type ScreenLocation,
  type ScreenNav,
} from "./screenNav";

// A screen-shaped probe: shows where the seam says the app is, and exposes the
// nav object so a test can drive it exactly as a screen would.
let nav: ScreenNav;
function Probe() {
  nav = useScreenNav();
  return (
    <p data-testid="where">
      {`${nav.screen ?? "-"}/${nav.view ?? "-"}?${new URLSearchParams(nav.search as Record<string, string>).toString()}`}
    </p>
  );
}
const where = () => screen.getByTestId("where").textContent;

describe("useScreenNav outside a provider", () => {
  it("is inert: empty search, moves that do nothing — a screen renders without a router", () => {
    render(<Probe />);
    expect(where()).toBe("-/-?");
    expect(() => {
      nav.setSearch({ node: "n1" });
      nav.goScreen("session");
      nav.goCampaignView("cast");
    }).not.toThrow();
    expect(where()).toBe("-/-?");
  });
});

describe("compactSearch", () => {
  it("keeps the known ids and drops empty, undefined and unknown keys", () => {
    expect(
      compactSearch({ node: "n1", agent: "", map: undefined, bogus: "x" } as never),
    ).toEqual({ node: "n1" });
  });
});

describe("MemoryScreenNav", () => {
  function mount(initial?: Partial<ScreenLocation>) {
    const moves: { next: ScreenLocation; replace: boolean }[] = [];
    render(
      <MemoryScreenNav initial={initial} onNavigate={(next, opts) => moves.push({ next, replace: opts.replace })}>
        <Probe />
      </MemoryScreenNav>,
    );
    return moves;
  }

  it("setSearch keeps the keys the patch does not name, and removes one patched to undefined", () => {
    const moves = mount({ screen: "campaign", view: "knowledge", search: { node: "n1", thread: "t1" } });
    expect(where()).toBe("campaign/knowledge?node=n1&thread=t1");

    act(() => nav.setSearch({ map: "m1" }));
    expect(where()).toBe("campaign/knowledge?node=n1&map=m1&thread=t1");
    act(() => nav.setSearch({ node: undefined }, { replace: true }));
    expect(where()).toBe("campaign/knowledge?map=m1&thread=t1");

    expect(moves.map((m) => m.replace)).toEqual([false, true]);
  });

  it("goCampaignView and goScreen start from a clean search", () => {
    mount({ screen: "campaign", view: "knowledge", search: { node: "n1" } });

    act(() => nav.goCampaignView("cast", { agent: "a1" }));
    expect(where()).toBe("campaign/cast?agent=a1");
    act(() => nav.goScreen("session", { session: "s1", line: "u:7" }));
    expect(where()).toBe("session/-?session=s1&line=u%3A7");
    act(() => nav.goScreen("campaigns"));
    expect(where()).toBe("campaigns/-?");
    // The Campaign screen has no view-less address: it opens on its Overview.
    act(() => nav.goScreen("campaign"));
    expect(where()).toBe("campaign/overview?");
  });
});

// The route layer's half, on a real router whose paths mirror the app's
// (/t/$tenantSlug/$screen and /t/$tenantSlug/$screen/$view). `as never`: the app
// registers ITS router's types globally.
function renderRouted(path: string) {
  const str = (v: unknown) => (typeof v === "string" && v !== "" ? v : undefined);
  const validateSearch = (s: Record<string, unknown>) => ({
    node: str(s.node),
    agent: str(s.agent),
    map: str(s.map),
    thread: str(s.thread),
    session: str(s.session),
    line: str(s.line),
    highlight: str(s.highlight),
  });
  const root = createRootRoute();
  const tenant = createRoute({
    getParentRoute: () => root,
    path: "t/$tenantSlug",
    component: () => (
      <RouterScreenNav tenantSlug="acme">
        {/* A shell-level consumer, OUTSIDE the Outlet — the sidebar's seat. */}
        <Probe />
        <Outlet />
      </RouterScreenNav>
    ),
  });
  const screenRoute = createRoute({
    getParentRoute: () => tenant,
    path: "$screen",
    validateSearch,
    component: () => <p>a screen</p>,
  });
  const viewRoute = createRoute({
    getParentRoute: () => tenant,
    path: "$screen/$view",
    validateSearch,
    component: () => <p>a view</p>,
  });
  const router = createRouter({
    routeTree: root.addChildren([tenant.addChildren([screenRoute, viewRoute])]),
    history: createMemoryHistory({ initialEntries: [path] }),
  } as never);
  render(<RouterProvider router={router as never} />);
  return router;
}

describe("RouterScreenNav", () => {
  it("reports the screen, the view and the search of the deepest match to a shell-level consumer", async () => {
    renderRouted("/t/acme/campaign/knowledge?node=n1");
    await waitFor(() => expect(where()).toBe("campaign/knowledge?node=n1"));
  });

  it("does not report a view for a screen that has none, nor an unknown screen", async () => {
    const router = renderRouted("/t/acme/session/extra");
    await waitFor(() => expect(where()).toBe("session/-?"));
    await act(() => router.navigate({ to: "/t/acme/nonsense" } as never));
    await waitFor(() => expect(where()).toBe("-/-?"));
  });

  it("setSearch patches the current URL, pushing by default and replacing on request", async () => {
    const router = renderRouted("/t/acme/campaign/knowledge?node=n1&thread=t1");
    await waitFor(() => expect(where()).toBe("campaign/knowledge?node=n1&thread=t1"));
    const before = router.history.length;

    await act(async () => nav.setSearch({ map: "m1" }));
    await waitFor(() =>
      expect(router.state.location.search).toEqual({ node: "n1", thread: "t1", map: "m1" }),
    );
    expect(router.state.location.pathname).toBe("/t/acme/campaign/knowledge");
    expect(router.history.length).toBe(before + 1);
    // The shell-level consumer follows a search-only change too.
    await waitFor(() => expect(where()).toBe("campaign/knowledge?node=n1&map=m1&thread=t1"));

    // The one-shot strip: replace, no history entry.
    await act(async () => nav.setSearch({ node: undefined }, { replace: true }));
    await waitFor(() => expect(router.state.location.search).toEqual({ thread: "t1", map: "m1" }));
    expect(router.history.length).toBe(before + 1);
    await waitFor(() => expect(where()).toBe("campaign/knowledge?map=m1&thread=t1"));
  });

  it("two patches issued in the same tick both land", async () => {
    const router = renderRouted("/t/acme/campaign/knowledge?node=n1");
    await waitFor(() => expect(where()).toBe("campaign/knowledge?node=n1"));
    await act(async () => {
      nav.setSearch({ map: "m1" }, { replace: true });
      nav.setSearch({ thread: "t1" }, { replace: true });
    });
    await waitFor(() =>
      expect(router.state.location.search).toEqual({ node: "n1", map: "m1", thread: "t1" }),
    );
  });

  it("setSearch works on a screen without a view", async () => {
    const router = renderRouted("/t/acme/session");
    await waitFor(() => expect(where()).toBe("session/-?"));
    await act(async () => nav.setSearch({ session: "s1" }));
    await waitFor(() => expect(router.state.location.search).toEqual({ session: "s1" }));
    expect(router.state.location.pathname).toBe("/t/acme/session");
  });

  it("goCampaignView and goScreen navigate with a clean search", async () => {
    const router = renderRouted("/t/acme/campaign/knowledge?node=n1");
    await waitFor(() => expect(where()).toBe("campaign/knowledge?node=n1"));

    await act(async () => nav.goCampaignView("cast", { agent: "a1" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/t/acme/campaign/cast"));
    expect(router.state.location.search).toEqual({ agent: "a1" });

    await act(async () => nav.goScreen("session", { session: "s1", line: "u:7" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/t/acme/session"));
    expect(router.state.location.search).toEqual({ session: "s1", line: "u:7" });

    await act(async () => nav.goScreen("campaigns"));
    await waitFor(() => expect(router.state.location.pathname).toBe("/t/acme/campaigns"));
    expect(router.state.location.search).toEqual({});

    // The Campaign screen goes straight to its default view.
    await act(async () => nav.goScreen("campaign"));
    await waitFor(() => expect(router.state.location.pathname).toBe("/t/acme/campaign/overview"));
  });
});

describe("useGuardedSearchKeys", () => {
  function Wiki() {
    useGuardedSearchKeys("node");
    return <p>wiki</p>;
  }
  function Toggle() {
    const nav = useScreenNav();
    return (
      <button type="button" onClick={() => nav.goCampaignView("cast")}>
        leave
      </button>
    );
  }

  it("declares the key while the component is mounted, and only then", () => {
    expect(guardedSearchChanged({ node: "a" }, { node: "b" })).toBe(false);
    render(
      <MemoryScreenNav initial={{ screen: "campaign", view: "knowledge" }}>
        {(loc) => (
          <>
            <Toggle />
            {loc.view === "knowledge" && <Wiki />}
          </>
        )}
      </MemoryScreenNav>,
    );
    expect(guardedSearchChanged({ node: "a" }, { node: "b" })).toBe(true);
    expect(guardedSearchChanged({ map: "a" }, { map: "b" })).toBe(false);

    fireEvent.click(screen.getByRole("button", { name: "leave" }));
    expect(screen.queryByText("wiki")).not.toBeInTheDocument();
    expect(guardedSearchChanged({ node: "a" }, { node: "b" })).toBe(false);
  });
});
