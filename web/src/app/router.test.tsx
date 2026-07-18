import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
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
//   - /login?error=not_authorized (notAuthorizedRedirect, ADR-0041): the
//     operator-allowlist denial signal the login route must surface as the
//     banner.
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

describe("onboardingRoute /onboarding/create-tenant contract", () => {
  it("renders the name-your-Tenant screen at /onboarding/create-tenant", async () => {
    renderAt("/onboarding/create-tenant");
    expect(await screen.findByLabelText(/name your table/i)).toHaveValue("Rin's Table");
  });
});
