import { describe, it, expect } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { RouterProvider, createRouter, createMemoryHistory } from "@tanstack/react-router";
import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";

import {
  AuthService,
  AdmissionMode,
  GetAdmissionModeResponseSchema,
  GetCurrentUserResponseSchema,
  UserSchema,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { routeTree } from "@/app/router";
import { Landing } from "./Landing";

// The public landing surface (#521). Three things are load-bearing:
//   1. the ROOT serves it to a visitor with no session (it used to bounce them
//      into a bare login form), while a signed-in visitor still goes straight to
//      the app;
//   2. the CTA goes to /login — the OAuth flow, and #518's open-mode
//      acknowledgment in front of it, are untouched;
//   3. the free-beta economics only render on an explicitly OPEN deployment —
//      the SPA ships identically to every operator, and a stock allowlist
//      install must not advertise a signup it rejects or an allowance it does
//      not fund. Fail-safe (probe down) is the neutral page.

function backend(opts: { signedIn?: boolean; mode?: AdmissionMode; down?: boolean } = {}) {
  return createRouterTransport(({ service }) => {
    service(AuthService, {
      getCurrentUser: () => {
        if (opts.down) throw new ConnectError("origin wedged", Code.Unavailable);
        if (!opts.signedIn) throw new ConnectError("no session", Code.Unauthenticated);
        return create(GetCurrentUserResponseSchema, {
          user: create(UserSchema, { name: "Rin", role: "operator", avatar: "" }),
          tenantId: "5b3f7c1e-0000-0000-0000-000000000000",
          tenantName: "Rin's Table",
        });
      },
      getAdmissionMode: () => {
        if (opts.down) throw new ConnectError("origin wedged", Code.Unavailable);
        return create(GetAdmissionModeResponseSchema, {
          admissionMode: opts.mode ?? AdmissionMode.OPEN,
        });
      },
    });
  });
}

function renderRoot(opts: { signedIn?: boolean; mode?: AdmissionMode; down?: boolean } = {}) {
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

  it("serves the landing page when the backend is down, not a spinner", async () => {
    // The stated design goal: the marketing surface is a static document and
    // stays up when the API is not (a rejected probe — a HUNG one is bounded
    // by RootEntry's grace timer, not exercised here).
    renderRoot({ down: true });
    expect(await screen.findByRole("heading", { level: 1 })).toHaveTextContent(/NPCs a voice/i);
    // Fail-safe posture: no free-beta economics without an explicit OPEN answer.
    expect(screen.queryByText(/free while in beta/i)).not.toBeInTheDocument();
  });

  it("only advertises the free beta on an explicitly OPEN deployment", async () => {
    renderRoot({ mode: AdmissionMode.ALLOWLIST });
    await screen.findByRole("heading", { level: 1 });
    // A stock allowlist install: neutral sign-in CTA, no signup framing, no
    // allowance promise this instance does not fund.
    expect(screen.getByRole("link", { name: /sign in with discord/i })).toHaveAttribute(
      "href",
      "/login",
    );
    expect(screen.queryByText(/free while in beta/i)).not.toBeInTheDocument();
    expect(screen.queryByText(/small monthly allowance/i)).not.toBeInTheDocument();
    // The transcription honesty note is deployment-independent and stays.
    expect(screen.getByText(/sessions are transcribed/i)).toBeInTheDocument();
  });
});

describe("landing copy", () => {
  it("states the beta bargain on an open deployment: a small allowance, then your own keys", async () => {
    render(<Landing open />);
    const doc = document.body.textContent ?? "";
    expect(doc).toMatch(/small monthly allowance/i);
    expect(doc).toMatch(/your own provider keys/i);
    // Honest about what the beta is (#258's copy policy): no card, rough edges,
    // and the transcription disclosure players will meet in Discord.
    expect(doc).toMatch(/no card/i);
    expect(doc).toMatch(/transcribed/i);
    // The allowance gate is plan-based: adding keys alone does not unpause an
    // exhausted trial, the operator moves the tenant to the BYOK plan. The copy
    // must not promise the self-service switch that does not exist.
    expect(doc).not.toMatch(/until you add your own keys/i);
    expect(doc).toMatch(/ask the operator/i);
  });

  it("quotes no allowance figure the operator can change out from under it", async () => {
    // included_usage_usd is an operator-adjustable chart value (#521's plan
    // catalog), so a dollar figure in this copy would become false on the first
    // `helm upgrade` that tunes it.
    render(<Landing open />);
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
