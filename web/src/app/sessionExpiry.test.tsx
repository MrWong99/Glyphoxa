import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { createRouterTransport, ConnectError, Code } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";
import { useMutation, useQuery } from "@connectrpc/connect-query";

import {
  AuthService,
  CampaignService,
  CreateAgentResponseSchema,
  GetActiveCampaignResponseSchema,
  GetCurrentUserResponseSchema,
  UserSchema,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { consumeReturnTo } from "@/lib/returnTo";
import { AuthGate } from "./AuthGate";

const { navigate } = vi.hoisted(() => ({ navigate: vi.fn() }));
vi.mock("@tanstack/react-router", () => ({ useNavigate: () => navigate }));

beforeEach(() => {
  navigate.mockClear();
  sessionStorage.clear();
  window.history.replaceState(null, "", "/t/default/campaign/knowledge?node=n1");
});

// A backend whose session can lapse: once `expired`, every call — the probe
// included — answers CodeUnauthenticated, like the real interceptor.
function backend() {
  const state = { expired: false, down: false, probes: 0 };
  const gate = () => {
    if (state.expired) throw new ConnectError("no session", Code.Unauthenticated);
  };
  const transport = createRouterTransport(({ service }) => {
    service(AuthService, {
      getCurrentUser: () => {
        state.probes++;
        if (state.down) throw new ConnectError("upstream restarting", Code.Unavailable);
        gate();
        return create(GetCurrentUserResponseSchema, {
          user: create(UserSchema, { name: "Sora Vance", role: "operator" }),
        });
      },
    });
    service(CampaignService, {
      getActiveCampaign: () => {
        gate();
        return create(GetActiveCampaignResponseSchema, {});
      },
      createAgent: () => {
        gate();
        return create(CreateAgentResponseSchema, {});
      },
    });
  });
  return { transport, state };
}

// A stand-in screen: one read it can re-run, one write.
function Screen() {
  const read = useQuery(CampaignService.method.getActiveCampaign, {}, { retry: false });
  const write = useMutation(CampaignService.method.createAgent);
  return (
    <>
      <p>shell is up</p>
      <button onClick={() => void read.refetch()}>read</button>
      <button onClick={() => write.mutate({ name: "x" })}>write</button>
    </>
  );
}

function mount() {
  const h = backend();
  const queryClient = makeQueryClient();
  render(
    <Providers transport={h.transport} queryClient={queryClient}>
      <AuthGate>{() => <Screen />}</AuthGate>
    </Providers>,
  );
  return { ...h, queryClient };
}

describe("expired-session handling", () => {
  it("redirects to /login when a READ comes back Unauthenticated mid-use", async () => {
    const { state } = mount();
    await screen.findByText("shell is up");
    expect(navigate).not.toHaveBeenCalled();

    state.expired = true;
    fireEvent.click(screen.getByRole("button", { name: "read" }));

    await waitFor(() => expect(navigate).toHaveBeenCalledWith({ to: "/login" }));
  });

  it("redirects to /login when a WRITE comes back Unauthenticated mid-use", async () => {
    const { state } = mount();
    await screen.findByText("shell is up");

    state.expired = true;
    fireEvent.click(screen.getByRole("button", { name: "write" }));

    await waitFor(() => expect(navigate).toHaveBeenCalledWith({ to: "/login" }));
  });

  it("parks the path being left so the sign-in can return to it", async () => {
    const { state } = mount();
    await screen.findByText("shell is up");
    state.expired = true;
    fireEvent.click(screen.getByRole("button", { name: "read" }));
    await waitFor(() => expect(navigate).toHaveBeenCalled());

    expect(consumeReturnTo()).toBe("/t/default/campaign/knowledge?node=n1");
  });

  it("does not re-probe in a loop when the probe itself is what failed", async () => {
    const { state } = mount();
    await screen.findByText("shell is up");
    state.expired = true;
    fireEvent.click(screen.getByRole("button", { name: "read" }));
    await waitFor(() => expect(navigate).toHaveBeenCalled());

    const probesAfterRedirect = state.probes;
    await new Promise((r) => setTimeout(r, 50));
    expect(state.probes).toBe(probesAfterRedirect);
  });

  it("keeps the shell up when a background re-probe fails for a non-auth reason", async () => {
    const { state, queryClient } = mount();
    await screen.findByText("shell is up");

    state.down = true;
    await queryClient.invalidateQueries();

    await waitFor(() => expect(state.probes).toBeGreaterThanOrEqual(2));
    expect(screen.getByText("shell is up")).toBeInTheDocument();
    expect(navigate).not.toHaveBeenCalled();
  });
});
