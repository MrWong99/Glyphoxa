import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import { RouterProvider, createRouter, createMemoryHistory } from "@tanstack/react-router";

import { routeTree } from "./router";

// Pins the Go↔TS contract for the operator-allowlist denial signal (ADR-0041):
// the OAuth callback redirects to the literal /login?error=not_authorized
// (notAuthorizedRedirect in internal/auth/oauth.go), and the login route's
// validateSearch must surface exactly that param name and value as the
// not-authorized banner. Login.test.tsx covers the component prop; this covers
// the URL→prop wiring, so a drift on either side of the contract fails a test.
function renderAt(path: string) {
  const router = createRouter({
    routeTree,
    history: createMemoryHistory({ initialEntries: [path] }),
  });
  render(<RouterProvider router={router} />);
}

describe("loginRoute ?error=not_authorized contract", () => {
  it("renders the allowlist banner for /login?error=not_authorized", async () => {
    renderAt("/login?error=not_authorized");
    expect(await screen.findByRole("alert")).toHaveTextContent(/allowlist/i);
  });

  it("renders no banner on a plain /login visit", async () => {
    renderAt("/login");
    await screen.findByRole("link", { name: /continue with discord/i });
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("renders no banner for an unrecognized error value", async () => {
    renderAt("/login?error=server_flu");
    await screen.findByRole("link", { name: /continue with discord/i });
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });
});
