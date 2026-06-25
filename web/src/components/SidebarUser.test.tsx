import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor, fireEvent } from "@testing-library/react";
import { createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";

import {
  AuthService,
  UserSchema,
  LogoutResponseSchema,
  GetCurrentUserResponseSchema,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { SidebarUser } from "./SidebarUser";

const { navigate } = vi.hoisted(() => ({ navigate: vi.fn() }));
vi.mock("@tanstack/react-router", () => ({ useNavigate: () => navigate }));

beforeEach(() => navigate.mockClear());

describe("SidebarUser", () => {
  it("renders the real operator identity", () => {
    render(
      <Providers transport={createRouterTransport(() => {})} queryClient={makeQueryClient()}>
        <SidebarUser user={create(UserSchema, { name: "Sora Vance", role: "operator", avatar: "" })} />
      </Providers>,
    );
    expect(screen.getByText("Sora Vance")).toBeInTheDocument();
    expect(screen.getByText("operator")).toBeInTheDocument();
  });

  it("logs out — calls Logout and redirects to /login", async () => {
    let loggedOut = false;
    const transport = createRouterTransport(({ service }) => {
      service(AuthService, {
        getCurrentUser: () =>
          create(GetCurrentUserResponseSchema, { user: create(UserSchema, {}) }),
        logout: () => {
          loggedOut = true;
          return create(LogoutResponseSchema, {});
        },
      });
    });

    render(
      <Providers transport={transport} queryClient={makeQueryClient()}>
        <SidebarUser user={create(UserSchema, { name: "Sora Vance", role: "operator", avatar: "" })} />
      </Providers>,
    );

    fireEvent.click(screen.getByRole("button", { name: /log out/i }));

    await waitFor(() => expect(loggedOut).toBe(true));
    await waitFor(() => expect(navigate).toHaveBeenCalledWith({ to: "/login" }));
  });
});
