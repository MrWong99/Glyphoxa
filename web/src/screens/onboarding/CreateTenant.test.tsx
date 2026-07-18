import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import type { Transport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";

import {
  AuthService,
  GetCurrentUserResponseSchema,
  RenameTenantResponseSchema,
  UserSchema,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { CreateTenant } from "./CreateTenant";

// useNavigate is mocked so the redirects are observable without a live router
// (same pattern as AuthGate.test.tsx).
const { navigate } = vi.hoisted(() => ({ navigate: vi.fn() }));
vi.mock("@tanstack/react-router", () => ({ useNavigate: () => navigate }));

beforeEach(() => navigate.mockClear());

// The navigate call both continue paths must land on: the app entry under the
// cosmetic default slug (the server scopes by session, ADR-0039 pass-through).
const APP_ENTRY = {
  to: "/t/$tenantSlug/$screen",
  params: { tenantSlug: "default", screen: "configuration" },
};

// backend fakes AuthService for the onboarding screen: GetCurrentUser answers
// with the pre-provisioned Tenant name (or CodeUnauthenticated), and
// RenameTenant records what rode the wire into `renamed` so a test can assert
// exactly what was sent — or that nothing was.
function backend(
  opts: { unauthenticated?: boolean; renameError?: boolean; renamed?: { name?: string } } = {},
): Transport {
  return createRouterTransport(({ service }) => {
    service(AuthService, {
      getCurrentUser: () => {
        if (opts.unauthenticated) throw new ConnectError("no session", Code.Unauthenticated);
        return create(GetCurrentUserResponseSchema, {
          user: create(UserSchema, { name: "Rin", role: "operator", avatar: "" }),
          tenantId: "5b3f7c1e-0000-0000-0000-000000000000",
          tenantName: "Rin's Table",
        });
      },
      renameTenant: (req) => {
        if (opts.renamed) opts.renamed.name = req.name;
        if (opts.renameError) throw new ConnectError("pq: connection reset", Code.Internal);
        return create(RenameTenantResponseSchema, {
          tenantId: "5b3f7c1e-0000-0000-0000-000000000000",
          tenantName: req.name,
        });
      },
    });
  });
}

function renderScreen(transport: Transport) {
  render(
    <Providers transport={transport} queryClient={makeQueryClient()}>
      <CreateTenant />
    </Providers>,
  );
}

describe("CreateTenant", () => {
  it("redirects to /login when there is no session", async () => {
    renderScreen(backend({ unauthenticated: true }));
    await waitFor(() => expect(navigate).toHaveBeenCalledWith({ to: "/login" }));
  });

  it("pre-fills the name field with the pre-provisioned Tenant name", async () => {
    renderScreen(backend());
    expect(await screen.findByLabelText(/name your table/i)).toHaveValue("Rin's Table");
  });

  it("renames the Tenant and navigates into the app on save", async () => {
    const renamed: { name?: string } = {};
    renderScreen(backend({ renamed }));
    const field = await screen.findByLabelText(/name your table/i);
    fireEvent.change(field, { target: { value: "  The Broken Crown  " } });
    fireEvent.click(screen.getByRole("button", { name: /save and continue/i }));
    await waitFor(() => expect(navigate).toHaveBeenCalledWith(APP_ENTRY));
    expect(renamed.name).toBe("The Broken Crown");
  });

  it("skips into the app without calling RenameTenant", async () => {
    const renamed: { name?: string } = {};
    renderScreen(backend({ renamed }));
    await screen.findByLabelText(/name your table/i);
    fireEvent.click(screen.getByRole("button", { name: /skip for now/i }));
    await waitFor(() => expect(navigate).toHaveBeenCalledWith(APP_ENTRY));
    expect(renamed.name).toBeUndefined();
  });

  it("shows a generic inline error when the rename fails and stays on the screen", async () => {
    renderScreen(backend({ renameError: true }));
    await screen.findByLabelText(/name your table/i);
    fireEvent.click(screen.getByRole("button", { name: /save and continue/i }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(/couldn.t save/i);
    // Non-leaky: the server's raw failure detail never reaches the card.
    expect(alert).not.toHaveTextContent(/connection reset/i);
    expect(navigate).not.toHaveBeenCalled();
  });
});
