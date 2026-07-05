import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor, act, within } from "@testing-library/react";
import { createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";

vi.mock("sonner", () => ({
  toast: { error: vi.fn(), success: vi.fn() },
  Toaster: () => null,
}));

import {
  SessionService,
  CampaignService,
  VoiceSessionSchema,
  GetSessionResponseSchema,
  StartSessionResponseSchema,
  StopSessionResponseSchema,
  SetAgentMuteResponseSchema,
  SetAllMuteResponseSchema,
  GetActiveCampaignResponseSchema,
  GetCampaignRosterResponseSchema,
  CampaignSchema,
  AgentSchema,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { MockEventSource } from "@/test/mockEventSource";
import { Session } from "./Session";

const BUTLER = create(AgentSchema, { id: "butler-1", campaignId: "c1", role: "butler", name: "Butler", speakerColor: 0 });
const BART = create(AgentSchema, { id: "bart-1", campaignId: "c1", role: "character", name: "Bart", speakerColor: 1 });
const GRETA = create(AgentSchema, { id: "greta-1", campaignId: "c1", role: "character", name: "Greta", speakerColor: 2 });
const ROSTER = [BUTLER, BART, GRETA];

// panelTransport serves a live-or-idle session with a mutable muted set plus the
// campaign roster, so the Voice panel renders and its mutations round-trip in the
// closure (mirroring the server's authoritative muted-id response).
function panelTransport(opts: { active: boolean; muted?: string[] }) {
  let muted = opts.muted ?? [];
  const current = create(VoiceSessionSchema, {
    id: "vs1",
    campaignId: "c1",
    status: "running",
    startedAt: timestampFromDate(new Date()),
  });
  return createRouterTransport(({ service }) => {
    service(SessionService, {
      getSession: () =>
        create(GetSessionResponseSchema, {
          session: opts.active ? current : undefined,
          active: opts.active,
          mutedAgentIds: muted,
        }),
      startSession: () => create(StartSessionResponseSchema, { session: current }),
      stopSession: () => create(StopSessionResponseSchema, { session: current }),
      setAgentMute: (req) => {
        muted = req.muted ? [...new Set([...muted, req.agentId])] : muted.filter((id) => id !== req.agentId);
        return create(SetAgentMuteResponseSchema, { mutedAgentIds: muted });
      },
      setAllMute: (req) => {
        muted = req.muted ? ROSTER.map((a) => a.id) : [];
        return create(SetAllMuteResponseSchema, { mutedAgentIds: muted });
      },
    });
    service(CampaignService, {
      getActiveCampaign: () =>
        create(GetActiveCampaignResponseSchema, {
          campaign: create(CampaignSchema, { id: "c1", name: "The Sunless Citadel" }),
        }),
      getCampaignRoster: () =>
        create(GetCampaignRosterResponseSchema, {
          campaign: create(CampaignSchema, { id: "c1", name: "The Sunless Citadel" }),
          roster: ROSTER,
        }),
    });
  });
}

function renderPanel(opts: { active: boolean; muted?: string[] }) {
  render(
    <Providers transport={panelTransport(opts)} queryClient={makeQueryClient()}>
      <Session />
    </Providers>,
  );
}

describe("VoicePanel (#211)", () => {
  beforeEach(() => {
    // A live snapshot so the SSE stream opens for the no-reload test.
    globalThis.fetch = (async () =>
      new Response(
        JSON.stringify({ lines: [], status: "live", typing: { active: false, label: "" } }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      )) as typeof fetch;
  });

  it("renders one row per Agent (Butler + NPCs) with the voicing count", async () => {
    renderPanel({ active: true });
    expect(await screen.findByText("NPC voices")).toBeInTheDocument();
    await waitFor(() => expect(screen.getAllByTestId("voice-row")).toHaveLength(3));
    expect(screen.getByText("Butler")).toBeInTheDocument();
    expect(screen.getByText("Bart")).toBeInTheDocument();
    expect(screen.getByText("Greta")).toBeInTheDocument();
    expect(screen.getByTestId("voicing-count")).toHaveTextContent("3 of 3 voicing");
  });

  it("disables every control when no Voice Session is live", async () => {
    renderPanel({ active: false });
    expect(await screen.findByText("NPC voices")).toBeInTheDocument();
    await waitFor(() => expect(screen.getAllByTestId("voice-row")).toHaveLength(3));
    // Mute-all + every per-Agent toggle disabled; count reads 0 while idle.
    expect(screen.getByRole("button", { name: /unmute all/i })).toBeDisabled();
    expect(screen.getByRole("button", { name: /mute bart/i })).toBeDisabled();
    expect(screen.getByTestId("voicing-count")).toHaveTextContent("0 of 3 voicing");
  });

  it("mutes an Agent on click, dimming its row and dropping the count", async () => {
    renderPanel({ active: true });
    await screen.findByText("Bart");
    const bartRow = () => screen.getByText("Bart").closest('[data-testid="voice-row"]') as HTMLElement;
    expect(bartRow()).not.toHaveAttribute("data-muted");

    fireEvent.click(screen.getByRole("button", { name: /mute bart/i }));

    await waitFor(() => expect(bartRow()).toHaveAttribute("data-muted"));
    expect(within(bartRow()).getByText("Muted")).toBeInTheDocument();
    // The mutation patched the shared cache, so the count reflects the mute.
    await waitFor(() => expect(screen.getByTestId("voicing-count")).toHaveTextContent("2 of 3 voicing"));
    // The toggle now offers to unmute Bart.
    expect(screen.getByRole("button", { name: /unmute bart/i })).toBeInTheDocument();
  });

  it("flips the mute-all button label between Mute all and Unmute all", async () => {
    renderPanel({ active: true });
    await screen.findByText("Bart");
    // Any Agent voicing → Mute all.
    expect(screen.getByRole("button", { name: /mute all/i })).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: /mute all/i }));

    // Now everyone is muted → Unmute all.
    expect(await screen.findByRole("button", { name: /unmute all/i })).toBeInTheDocument();
    await waitFor(() => expect(screen.getByTestId("voicing-count")).toHaveTextContent("0 of 3 voicing"));
  });

  it("flips a row from an SSE mute frame without a reload (AC5)", async () => {
    renderPanel({ active: true });
    await screen.findByText("Bart");
    const bartRow = () => screen.getByText("Bart").closest('[data-testid="voice-row"]') as HTMLElement;
    expect(bartRow()).not.toHaveAttribute("data-muted");

    const es = await waitFor(() => {
      const inst = MockEventSource.last();
      if (!inst) throw new Error("EventSource not opened yet");
      return inst;
    });

    // A mute from the OTHER surface (Discord) arrives over the relay: the row dims
    // without any refetch/reload.
    act(() => es.emit("mute", { agent_id: "bart-1", muted: true }));

    await waitFor(() => expect(bartRow()).toHaveAttribute("data-muted"));
    expect(within(bartRow()).getByText("Muted")).toBeInTheDocument();
    expect(screen.getByTestId("voicing-count")).toHaveTextContent("2 of 3 voicing");
  });
});
