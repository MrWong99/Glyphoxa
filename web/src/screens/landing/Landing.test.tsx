import { describe, it, expect } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { RouterProvider, createRouter, createMemoryHistory } from "@tanstack/react-router";
import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";

import {
  AuthService,
  GetCurrentUserResponseSchema,
  UserSchema,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { routeTree } from "@/app/router";
import { Landing } from "./Landing";

// The public landing surface (#521). Two things are load-bearing:
//   1. the ROOT serves it to a visitor with no session (it used to bounce them
//      into a bare login form), while a signed-in visitor still goes straight to
//      the app;
//   2. the CTA goes to /login — the OAuth flow, and #518's open-mode
//      acknowledgment in front of it, are untouched.

function backend(opts: { signedIn?: boolean } = {}) {
  return createRouterTransport(({ service }) => {
    service(AuthService, {
      getCurrentUser: () => {
        if (!opts.signedIn) throw new ConnectError("no session", Code.Unauthenticated);
        return create(GetCurrentUserResponseSchema, {
          user: create(UserSchema, { name: "Rin", role: "operator", avatar: "" }),
          tenantId: "5b3f7c1e-0000-0000-0000-000000000000",
          tenantName: "Rin's Table",
        });
      },
    });
  });
}

function renderRoot(opts: { signedIn?: boolean } = {}) {
  const router = createRouter({
    routeTree,
    history: createMemoryHistory({ initialEntries: ["/"] }),
  });
  render(
    <Providers transport={backend(opts)} queryClient={makeQueryClient()}>
      <RouterProvider router={router} />
    </Providers>,
  );
  return router;
}

describe("root landing surface", () => {
  it("serves the landing page to a visitor with no session", async () => {
    renderRoot();
    expect(await screen.findByRole("heading", { level: 1 })).toHaveTextContent(/NPCs a voice/i);
  });

  it("sends the CTA into the unchanged auth flow", async () => {
    renderRoot();
    await screen.findByRole("heading", { level: 1 });
    // /login, not /auth/discord/login: the login screen is where the open-mode
    // AUP acknowledgment (#518) gates the OAuth start.
    for (const name of [/start with discord/i, /sign in/i]) {
      expect(screen.getByRole("link", { name })).toHaveAttribute("href", "/login");
    }
  });

  it("takes a signed-in visitor straight to the app", async () => {
    const router = renderRoot({ signedIn: true });
    await waitFor(() =>
      expect(router.state.location.pathname).toBe("/t/default/configuration"),
    );
    expect(screen.queryByText(/NPCs a voice/i)).not.toBeInTheDocument();
  });
});

describe("landing copy", () => {
  it("states the beta bargain: a small allowance, then your own keys", async () => {
    render(<Landing />);
    const doc = document.body.textContent ?? "";
    expect(doc).toMatch(/small monthly allowance/i);
    expect(doc).toMatch(/your own provider keys/i);
    // Honest about what the beta is (#258's copy policy): no card, rough edges,
    // and the transcription disclosure players will meet in Discord.
    expect(doc).toMatch(/no card/i);
    expect(doc).toMatch(/transcribed/i);
  });

  it("quotes no allowance figure the operator can change out from under it", async () => {
    // included_usage_usd is an operator-adjustable chart value (#521's plan
    // catalog), so a dollar figure in this copy would become false on the first
    // `helm upgrade` that tunes it.
    render(<Landing />);
    expect(document.body.textContent ?? "").not.toMatch(/\$\s?\d/);
  });

  it("captions every screenshot slot, placeholder or not", async () => {
    render(<Landing />);
    const shots = screen.getByLabelText("Screenshots");
    const figures = shots.querySelectorAll("figure");
    expect(figures.length).toBeGreaterThanOrEqual(3);
    for (const fig of figures) {
      expect(fig.querySelector("figcaption")?.textContent ?? "").not.toBe("");
    }
  });

  it("carries the legal footer for an unauthenticated visitor", async () => {
    render(<Landing />);
    expect(screen.getByRole("link", { name: "Impressum" })).toHaveAttribute("href", "/imprint");
  });
});
