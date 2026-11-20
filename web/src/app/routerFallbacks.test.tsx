import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { useEffect, useState } from "react";
import {
  Outlet,
  RouterProvider,
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  useNavigate,
  type AnyRoute,
} from "@tanstack/react-router";
import { createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";

import {
  AuthService,
  AdmissionMode,
  GetAdmissionModeResponseSchema,
  GetCurrentUserResponseSchema,
  UserSchema,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { useDirtyGuard } from "@/components/ui/useDirtyGuard";
import { makeQueryClient } from "@/lib/queryClient";
import { rememberReturnTo } from "@/lib/returnTo";
import { NavigationGuard } from "./RouteFallbacks";
import { routeTree, routerDefaults } from "./router";

function transport() {
  return createRouterTransport(({ service }) => {
    service(AuthService, {
      getAdmissionMode: () =>
        create(GetAdmissionModeResponseSchema, { admissionMode: AdmissionMode.ALLOWLIST }),
      getCurrentUser: () =>
        create(GetCurrentUserResponseSchema, {
          user: create(UserSchema, { name: "Rin", role: "operator", avatar: "" }),
        }),
    });
  });
}

// The real tree with the real defaults — what main.tsx mounts.
function renderApp(path: string) {
  const router = createRouter({
    routeTree,
    history: createMemoryHistory({ initialEntries: [path] }),
    ...routerDefaults,
  });
  render(
    <Providers transport={transport()} queryClient={makeQueryClient()}>
      <RouterProvider router={router} />
    </Providers>,
  );
  return router;
}

// A throwaway tree for the behaviours the real one cannot be made to show on
// demand (a throwing route, an editor with unsaved edits). `as never` because
// the app registers ITS router's types globally.
function renderTree(children: (root: AnyRoute) => AnyRoute[], path: string) {
  const root = createRootRoute({
    component: () => (
      <>
        <Outlet />
        <NavigationGuard />
      </>
    ),
  });
  const router = createRouter({
    routeTree: root.addChildren(children(root)),
    history: createMemoryHistory({ initialEntries: [path] }),
    ...routerDefaults,
  } as never);
  render(
    <Providers transport={transport()} queryClient={makeQueryClient()}>
      <RouterProvider router={router as never} />
    </Providers>,
  );
  return router;
}

beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
});
afterEach(() => {
  vi.restoreAllMocks();
  localStorage.clear();
});

describe("router-level not-found page", () => {
  it("renders the app's own page for a URL no route matches", async () => {
    renderApp("/no/such/page");
    expect(await screen.findByRole("heading", { name: "Not found" })).toBeInTheDocument();
    expect(screen.getByText("There is no page at this address.")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Back to the start page" })).toHaveAttribute("href", "/");
  });

  it("is localized", async () => {
    localStorage.setItem("gx-lang", "de");
    renderApp("/no/such/page");
    expect(await screen.findByRole("heading", { name: "Nicht gefunden" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Zur Startseite" })).toBeInTheDocument();
  });
});

describe("router-level error page", () => {
  it("catches a throwing route, shows the reason folded away, and recovers on Try again", async () => {
    // React logs caught render errors; keep the expected one out of the output.
    vi.spyOn(console, "error").mockImplementation(() => {});
    let healthy = false;
    function Flaky() {
      if (!healthy) throw new Error("chunk 42 failed to load");
      return <p>screen is back</p>;
    }
    renderTree((root) => [createRoute({ getParentRoute: () => root, path: "/flaky", component: Flaky })], "/flaky");

    expect(await screen.findByRole("heading", { name: "Something went wrong" })).toBeInTheDocument();
    expect(screen.getByText("chunk 42 failed to load")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Reload page" })).toBeInTheDocument();

    healthy = true;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("screen is back")).toBeInTheDocument();
  });
});

describe("code-split routes", () => {
  it("loads a legal page on demand", async () => {
    renderApp("/privacy");
    expect(await screen.findByRole("heading", { level: 1 })).toHaveTextContent(/datenschutz/i);
  });
});

describe("return path after a re-login", () => {
  it("sends a signed-in visit to / back to the parked in-app path", async () => {
    rememberReturnTo("/t/acme/nowhere");
    const router = renderApp("/");
    await waitFor(() => expect(router.state.location.pathname).toBe("/t/acme/nowhere"));
  });

  it("falls back to the default screen with nothing parked", async () => {
    const router = renderApp("/");
    await waitFor(() => expect(router.state.location.pathname).toBe("/t/default/configuration"));
  });
});

describe("NavigationGuard (W10)", () => {
  function Editor() {
    const edits = useDirtyGuard();
    const navigate = useNavigate();
    const [text, setText] = useState("");
    const { markDirty } = edits;
    useEffect(() => {
      markDirty(text !== "");
      return () => markDirty(false);
    }, [text, markDirty]);
    return (
      <>
        <input aria-label="notes" value={text} onChange={(e) => setText(e.target.value)} />
        <button onClick={() => void navigate({ to: "/b" as never })}>go to b</button>
        <button onClick={() => void navigate({ to: "/a" as never, search: { x: 1 } as never })}>
          same page, new query
        </button>
        {edits.dialog}
      </>
    );
  }
  const tree = (root: AnyRoute) => [
    createRoute({ getParentRoute: () => root, path: "/a", component: Editor }),
    createRoute({ getParentRoute: () => root, path: "/b", component: () => <p>page b</p> }),
  ];

  it("lets a navigation through when nothing is unsaved", async () => {
    renderTree(tree, "/a");
    fireEvent.click(await screen.findByRole("button", { name: "go to b" }));
    expect(await screen.findByText("page b")).toBeInTheDocument();
  });

  it("holds a navigation that would unmount an editor with unsaved edits", async () => {
    const router = renderTree(tree, "/a");
    fireEvent.change(await screen.findByLabelText("notes"), { target: { value: "half a thought" } });
    fireEvent.click(screen.getByRole("button", { name: "go to b" }));

    expect(await screen.findByText("Discard unsaved changes?")).toBeInTheDocument();
    expect(router.state.location.pathname).toBe("/a");

    // Cancel keeps the GM — and the text — where they were.
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByText("Discard unsaved changes?")).not.toBeInTheDocument());
    expect(router.state.location.pathname).toBe("/a");
    expect((screen.getByLabelText("notes") as HTMLInputElement).value).toBe("half a thought");

    // Confirming goes through.
    fireEvent.click(screen.getByRole("button", { name: "go to b" }));
    fireEvent.click(await screen.findByRole("button", { name: "Discard changes" }));
    expect(await screen.findByText("page b")).toBeInTheDocument();
  });

  it("does not hold a same-path query rewrite (the deep-link strip)", async () => {
    const router = renderTree(tree, "/a");
    fireEvent.change(await screen.findByLabelText("notes"), { target: { value: "half a thought" } });
    fireEvent.click(screen.getByRole("button", { name: "same page, new query" }));

    await waitFor(() => expect(router.state.location.search).toEqual({ x: 1 }));
    expect(screen.queryByText("Discard unsaved changes?")).not.toBeInTheDocument();
  });
});
