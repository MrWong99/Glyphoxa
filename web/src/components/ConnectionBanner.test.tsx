import { describe, it, expect, afterEach } from "vitest";
import { act, render, screen, fireEvent, waitFor } from "@testing-library/react";
import { Code, ConnectError, createRouterTransport, type Transport } from "@connectrpc/connect";
import { useQuery } from "@connectrpc/connect-query";
import { onlineManager } from "@tanstack/react-query";
import { create } from "@bufbuild/protobuf";

import {
  AuthService,
  CampaignService,
  CampaignSchema,
  GetActiveCampaignResponseSchema,
  GetCurrentUserResponseSchema,
  UserSchema,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { ConnectionBanner } from "./ConnectionBanner";

// The shell's connection bar (#695). Two things must hold: it appears and
// clears on its own with the network, and "Retry now" really asks.

// A backend that can be switched off: while `down`, every call fails the way a
// dead network does (fetch rejects with a TypeError); while `refusing`, the
// server answers — with an error of its own.
function backend() {
  const state = { down: false, refusing: false, calls: 0 };
  const real = createRouterTransport(({ service }) => {
    service(AuthService, {
      getCurrentUser: () => {
        if (state.refusing) throw new ConnectError("no session", Code.Unauthenticated);
        return create(GetCurrentUserResponseSchema, { user: create(UserSchema, { name: "Rin" }) });
      },
    });
    service(CampaignService, {
      getActiveCampaign: () => {
        if (state.refusing) throw new ConnectError("no session", Code.Unauthenticated);
        return create(GetActiveCampaignResponseSchema, {
          campaign: create(CampaignSchema, { id: "c1", name: "The Sunless Citadel" }),
        });
      },
    });
  });
  const transport: Transport = {
    unary: (...args) => {
      state.calls++;
      if (state.down) return Promise.reject(new TypeError("Failed to fetch"));
      return real.unary(...args);
    },
    stream: (...args) => real.stream(...args),
  };
  return { state, transport };
}

// A screen's read, standing in for whatever is on screen when the network goes.
function Screen() {
  const q = useQuery(CampaignService.method.getActiveCampaign, {}, { retry: false });
  if (q.data?.campaign) return <p>working in {q.data.campaign.name}</p>;
  return <p>{q.isError ? "screen failed to load" : "loading"}</p>;
}

function renderApp(b = backend()) {
  const queryClient = makeQueryClient();
  render(
    <Providers transport={b.transport} queryClient={queryClient}>
      <ConnectionBanner />
      <Screen />
    </Providers>,
  );
  return { ...b, queryClient };
}

afterEach(() => {
  act(() => onlineManager.setOnline(true));
});

describe("ConnectionBanner", () => {
  it("is silent while everything works — an empty live region, no text, no button", async () => {
    renderApp();
    await screen.findByText("working in The Sunless Citadel");
    const region = screen.getByRole("status");
    expect(region).toBeEmptyDOMElement();
    expect(screen.queryByRole("button", { name: "Retry now" })).not.toBeInTheDocument();
  });

  it("says 'You are offline' when the browser loses its network, and clears when it is back", async () => {
    renderApp();
    await screen.findByText("working in The Sunless Citadel");

    act(() => onlineManager.setOnline(false));
    const region = screen.getByRole("status");
    expect(region).toHaveTextContent(/you are offline/i);
    expect(region).toHaveAttribute("data-state", "offline");
    // Nothing to retry: reads are paused and resume on their own.
    expect(screen.queryByRole("button", { name: "Retry now" })).not.toBeInTheDocument();

    act(() => onlineManager.setOnline(true));
    expect(region).toBeEmptyDOMElement();
    expect(region).not.toHaveAttribute("data-state");
  });

  it("says it can't reach Glyphoxa when reads come back unanswered, with a Retry that really asks", async () => {
    const b = backend();
    b.state.down = true;
    renderApp(b);

    const region = screen.getByRole("status");
    await waitFor(() => expect(region).toHaveTextContent(/can't reach glyphoxa/i));
    expect(region).toHaveAttribute("data-state", "unreachable");
    expect(screen.getByText("screen failed to load")).toBeInTheDocument();

    // Still down: the click is answered — the bar says so — and the button is
    // there to press again.
    const before = b.state.calls;
    fireEvent.click(screen.getByRole("button", { name: "Retry now" }));
    await waitFor(() => expect(region).toHaveTextContent(/still can't reach glyphoxa/i));
    expect(b.state.calls).toBeGreaterThan(before);
    expect(screen.getByRole("button", { name: "Retry now" })).not.toHaveAttribute("aria-disabled");

    // The server is back: Retry clears the bar and the screen's failed read
    // is re-run without the GM touching the screen.
    b.state.down = false;
    fireEvent.click(screen.getByRole("button", { name: "Retry now" }));
    expect(await screen.findByText("working in The Sunless Citadel")).toBeInTheDocument();
    expect(region).toBeEmptyDOMElement();
  });

  it("clears on its own when the next read succeeds", async () => {
    const b = backend();
    b.state.down = true;
    const { queryClient } = renderApp(b);
    const region = screen.getByRole("status");
    await waitFor(() => expect(region).toHaveTextContent(/can't reach glyphoxa/i));

    // A poll, a refocus — any read that gets an answer.
    b.state.down = false;
    await act(async () => {
      await queryClient.refetchQueries();
    });
    expect(await screen.findByText("working in The Sunless Citadel")).toBeInTheDocument();
    expect(region).toBeEmptyDOMElement();
  });

  it("treats an error the server wrote as an answer: Retry clears the bar even if the answer is 'not signed in'", async () => {
    const b = backend();
    b.state.down = true;
    renderApp(b);
    const region = screen.getByRole("status");
    await waitFor(() => expect(region).toHaveTextContent(/can't reach glyphoxa/i));

    b.state.down = false;
    b.state.refusing = true;
    fireEvent.click(screen.getByRole("button", { name: "Retry now" }));
    await waitFor(() => expect(region).toBeEmptyDOMElement());
  });

  it("keeps the Retry button focusable while it works (no focus drop to the page)", async () => {
    const b = backend();
    b.state.down = true;
    renderApp(b);
    const button = await screen.findByRole("button", { name: "Retry now" });
    button.focus();
    fireEvent.click(button);
    expect(button).toHaveAttribute("aria-disabled", "true");
    expect(button).not.toBeDisabled();
    expect(button).toHaveFocus();
    await waitFor(() => expect(button).not.toHaveAttribute("aria-disabled"));
  });

  it("is localized", async () => {
    localStorage.setItem("gx-lang", "de");
    try {
      const b = backend();
      b.state.down = true;
      renderApp(b);
      expect(await screen.findByRole("button", { name: "Jetzt erneut versuchen" })).toBeInTheDocument();
      expect(screen.getByRole("status")).toHaveTextContent(/nicht erreichbar/i);
    } finally {
      localStorage.removeItem("gx-lang");
    }
  });
});
