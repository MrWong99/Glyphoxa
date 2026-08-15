import { describe, it, expect } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { RouterProvider, createRouter, createMemoryHistory } from "@tanstack/react-router";
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
import { makeQueryClient } from "@/lib/queryClient";
import { routeTree } from "./router";

// Pins the Go↔TS URL contracts owned by internal/auth/oauth.go:
//   - /login?error=not_authorized (notAuthorizedRedirect): the admission
//     denial signal — an allowlist miss (ADR-0041) or an open-mode suspension
//     (ADR-0055) — the login route must surface as the banner.
//   - /onboarding/create-tenant (onboardingRedirect, ADR-0055): where the OAuth
//     callback 302s a fresh open-mode signup; the route must exist top-level and
//     render the name-your-Tenant screen.
// The component tests cover the screens' props/behaviour; these cover the
// URL→screen wiring, so a drift on either side of a contract fails a test.

// A minimal AuthService backend for the routes under test: the login screen
// probes GetAdmissionMode (allowlist keeps today's framing) and the onboarding
// screen probes GetCurrentUser (answered with the pre-provisioned Tenant name).
function testTransport() {
  return createRouterTransport(({ service }) => {
    service(AuthService, {
      getAdmissionMode: () =>
        create(GetAdmissionModeResponseSchema, { admissionMode: AdmissionMode.ALLOWLIST }),
      getCurrentUser: () =>
        create(GetCurrentUserResponseSchema, {
          user: create(UserSchema, { name: "Rin", role: "operator", avatar: "" }),
          tenantId: "5b3f7c1e-0000-0000-0000-000000000000",
          tenantName: "Rin's Table",
        }),
    });
  });
}

function renderAt(path: string) {
  const router = createRouter({
    routeTree,
    history: createMemoryHistory({ initialEntries: [path] }),
  });
  render(
    <Providers transport={testTransport()} queryClient={makeQueryClient()}>
      <RouterProvider router={router} />
    </Providers>,
  );
  return router;
}

describe("loginRoute ?error=not_authorized contract", () => {
  it("renders the not-authorized banner for /login?error=not_authorized", async () => {
    renderAt("/login?error=not_authorized");
    expect(await screen.findByRole("alert")).toHaveTextContent(
      /isn't authorized here/i,
    );
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

describe("onboardingRoute /onboarding/create-tenant contract", () => {
  it("renders the name-your-Tenant screen at /onboarding/create-tenant", async () => {
    renderAt("/onboarding/create-tenant");
    expect(await screen.findByLabelText(/name your table/i)).toHaveValue("Rin's Table");
  });
});

// The Campaign sub-view lives in the path (ADR-0063): /campaign/:view. A bare
// /campaign visit — and any pre-ADR-0063 ?view=/?node= deep link (#591) — must
// redirect onto the path so old bookmarks and palette links keep working.
describe("campaign sub-view path (ADR-0063)", () => {
  it("redirects a bare /campaign to the default cast sub-view", async () => {
    const router = renderAt("/t/acme/campaign");
    await waitFor(() =>
      expect(router.state.location.pathname).toBe("/t/acme/campaign/cast"),
    );
  });

  it("maps a legacy ?view= deep link onto the path", async () => {
    const router = renderAt("/t/acme/campaign?view=players");
    await waitFor(() =>
      expect(router.state.location.pathname).toBe("/t/acme/campaign/players"),
    );
    expect(router.state.location.search).toEqual({});
  });

  it("an unknown ?view= value falls back to cast", async () => {
    const router = renderAt("/t/acme/campaign?view=nonsense");
    await waitFor(() =>
      expect(router.state.location.pathname).toBe("/t/acme/campaign/cast"),
    );
  });

  it("a legacy ?node= link lands on knowledge, and the screen strips the param", async () => {
    const router = renderAt("/t/acme/campaign?node=n-bart");
    await waitFor(() =>
      expect(router.state.location.pathname).toBe("/t/acme/campaign/knowledge"),
    );
    // The redirect carries ?node= along; once the screen mounts it consumes the
    // focus and strips the URL (consume-then-strip, see ScreenSearch). Campaign's
    // own test covers the focus hand-off; here we pin the URL contract.
    await waitFor(() => expect(router.state.location.search).toEqual({}));
  });
});
