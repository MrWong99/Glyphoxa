import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { createRouterTransport, ConnectError, Code } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";

import {
  AuthService,
  GetCurrentUserResponseSchema,
  UserSchema,
  LogoutResponseSchema,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { AuthGate } from "./AuthGate";

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
});
