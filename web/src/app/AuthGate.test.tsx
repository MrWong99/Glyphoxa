import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { createRouterTransport, ConnectError, Code } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";

import {
  AuthService,
  GetCurrentUserResponseSchema,
  GuestRight,
  GuestWorkspaceSchema,
  UserSchema,
  LogoutResponseSchema,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { AuthGate } from "./AuthGate";
import { useCurrentUser } from "./currentUser";

// useNavigate is mocked so the redirect is observable without a live router.
const { navigate } = vi.hoisted(() => ({ navigate: vi.fn() }));
vi.mock("@tanstack/react-router", () => ({ useNavigate: () => navigate }));

beforeEach(() => navigate.mockClear());

// authedTransport answers GetCurrentUser with a canned operator.
function authedTransport() {
  return createRouterTransport(({ service }) => {
    service(AuthService, {
      getCurrentUser: () =>
        create(GetCurrentUserResponseSchema, {
          user: create(UserSchema, { name: "Sora Vance", role: "operator", avatar: "" }),
        }),
      logout: () => create(LogoutResponseSchema, {}),
    });
  });
}

// unauthTransport fails GetCurrentUser with CodeUnauthenticated (no session).
function unauthTransport() {
  return createRouterTransport(({ service }) => {
    service(AuthService, {
      getCurrentUser: () => {
        throw new ConnectError("no session", Code.Unauthenticated);
      },
      logout: () => create(LogoutResponseSchema, {}),
    });
  });
}

describe("AuthGate", () => {
  it("redirects to /login when unauthenticated", async () => {
    render(
      <Providers transport={unauthTransport()} queryClient={makeQueryClient()}>
        <AuthGate>{(user) => <div>{user.name}</div>}</AuthGate>
      </Providers>,
    );
    await waitFor(() => expect(navigate).toHaveBeenCalledWith({ to: "/login" }));
  });

  it("renders the shell with the real operator identity when authenticated", async () => {
    render(
      <Providers transport={authedTransport()} queryClient={makeQueryClient()}>
        <AuthGate>{(user) => <div>shell for {user.name}</div>}</AuthGate>
      </Providers>,
    );
    expect(await screen.findByText("shell for Sora Vance")).toBeInTheDocument();
    expect(navigate).not.toHaveBeenCalled();
  });

  // ADR-0065: the access facts ride on the same response as the identity. The
  // gate folds them into the one user object it hands on — and puts that object
  // in context for screens deep in the Outlet.
  it("threads the access facts of GetCurrentUser through the user it hands on", async () => {
    const saltmarsh = create(GuestWorkspaceSchema, {
      campaignId: "c-salt",
      campaignName: "Tides over Saltmarsh",
      tenantName: "Mira's Table",
      rights: [GuestRight.WIKI_READ, GuestRight.MAPS_READ],
    });
    const transport = createRouterTransport(({ service }) => {
      service(AuthService, {
        getCurrentUser: () =>
          create(GetCurrentUserResponseSchema, {
            user: create(UserSchema, { name: "Sora Vance", role: "operator", avatar: "" }),
            isPlatformAdmin: true,
            hasOwnTenant: true,
            guest: saltmarsh,
            guestWorkspaces: [saltmarsh],
          }),
      });
    });
    function FromContext() {
      const user = useCurrentUser();
      return <div data-testid="ctx">{user?.isPlatformAdmin ? `${user.name} is a Platform Admin` : "no"}</div>;
    }
    render(
      <Providers transport={transport} queryClient={makeQueryClient()}>
        <AuthGate>
          {(user) => (
            <div>
              <div data-testid="handed">
                {JSON.stringify({
                  name: user.name,
                  isPlatformAdmin: user.isPlatformAdmin,
                  hasOwnTenant: user.hasOwnTenant,
                  guest: user.guest?.campaignName,
                  guestWorkspaces: user.guestWorkspaces.map((w) => [w.campaignId, w.tenantName, w.rights]),
                })}
              </div>
              <FromContext />
            </div>
          )}
        </AuthGate>
      </Providers>,
    );
    expect(JSON.parse((await screen.findByTestId("handed")).textContent ?? "")).toEqual({
      name: "Sora Vance",
      isPlatformAdmin: true,
      hasOwnTenant: true,
      guest: "Tides over Saltmarsh",
      guestWorkspaces: [["c-salt", "Mira's Table", [GuestRight.WIKI_READ, GuestRight.MAPS_READ]]],
    });
    expect(screen.getByTestId("ctx")).toHaveTextContent("Sora Vance is a Platform Admin");
  });

  it("hands on a plain GM as not a Platform Admin, with no guest Workspaces", async () => {
    render(
      <Providers transport={authedTransport()} queryClient={makeQueryClient()}>
        <AuthGate>
          {(user) => (
            <div data-testid="handed">
              {JSON.stringify([user.isPlatformAdmin, user.hasOwnTenant, user.guest ?? null, user.guestWorkspaces])}
            </div>
          )}
        </AuthGate>
      </Providers>,
    );
    expect(JSON.parse((await screen.findByTestId("handed")).textContent ?? "")).toEqual([false, false, null, []]);
  });
});
