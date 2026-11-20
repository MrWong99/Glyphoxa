import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { cleanup, render, screen, fireEvent, waitFor } from "@testing-library/react";
import { createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";

import {
  CampaignService,
  ProviderService,
  SessionService,
  CampaignSchema,
  GetActiveCampaignResponseSchema,
  GetSessionResponseSchema,
  ListHighlightsResponseSchema,
  ListProviderConfigsResponseSchema,
  ListSessionVoiceChannelsResponseSchema,
  SaveDiscordSettingsResponseSchema,
  StartSessionResponseSchema,
  StopSessionResponseSchema,
  VoiceChannelSchema,
  VoiceSessionSchema,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { Session } from "./Session";

vi.mock("sonner", () => ({ toast: { error: vi.fn(), success: vi.fn() } }));

// W2: the voice-channel pick used to be plain component state, so every page
// load fell back to the Tenant default (or silently to the first channel). The
// channel a session was last STARTED in is now remembered per Discord server.
const channels = [
  { id: "c-general", name: "General" },
  { id: "c-tavern", name: "The Tavern" },
];

function transportFor(opts: { guildId?: string; defaultChannelId?: string; failStart?: boolean } = {}) {
  const state = { defaultChannelId: opts.defaultChannelId ?? "" };
  const startRequests: string[] = [];
  let active = false;
  const session = () => create(VoiceSessionSchema, { id: "vs1", campaignId: "c1", status: "running" });
  const transport = createRouterTransport(({ service }) => {
    service(SessionService, {
      listHighlights: () => create(ListHighlightsResponseSchema, { highlights: [] }),
      getSession: () =>
        create(GetSessionResponseSchema, active ? { session: session(), active } : { active }),
      listSessionVoiceChannels: () =>
        create(ListSessionVoiceChannelsResponseSchema, {
          channels: channels.map((c) => create(VoiceChannelSchema, c)),
          defaultChannelId: state.defaultChannelId,
        }),
      startSession: (req) => {
        startRequests.push(req.voiceChannelId);
        if (opts.failStart) throw new Error("the Bot could not join");
        active = true;
        return create(StartSessionResponseSchema, { session: session() });
      },
      stopSession: () => {
        active = false;
        return create(StopSessionResponseSchema, {});
      },
    });
    service(ProviderService, {
      listProviderConfigs: () =>
        create(ListProviderConfigsResponseSchema, { guildId: opts.guildId ?? "g1" }),
      saveDiscordSettings: (req) => {
        if (req.voiceChannelId !== undefined) state.defaultChannelId = req.voiceChannelId;
        return create(SaveDiscordSettingsResponseSchema, {
          guildId: opts.guildId ?? "g1",
          voiceChannelId: state.defaultChannelId,
        });
      },
    });
    service(CampaignService, {
      getActiveCampaign: () =>
        create(GetActiveCampaignResponseSchema, {
          campaign: create(CampaignSchema, { id: "c1", name: "The Sunless Citadel" }),
        }),
    });
  });
  return { transport, startRequests };
}

function mount(opts: Parameters<typeof transportFor>[0] = {}) {
  const h = transportFor(opts);
  render(
    <Providers transport={h.transport} queryClient={makeQueryClient()}>
      <Session />
    </Providers>,
  );
  return h;
}

const trigger = () => screen.getByRole("combobox", { name: "Voice channel" });
const startButton = () => screen.getByRole("button", { name: /start session/i });

async function pick(name: string) {
  fireEvent.keyDown(trigger(), { key: "Enter" });
  fireEvent.click(await screen.findByRole("option", { name }));
  await waitFor(() => expect(trigger()).toHaveTextContent(name));
}

beforeEach(() => localStorage.clear());
afterEach(() => {
  vi.restoreAllMocks();
  localStorage.clear();
});

describe("Session voice-channel memory (W2)", () => {
  it("pre-selects the channel the last session was started in, over the Tenant default", async () => {
    const first = mount({ defaultChannelId: "c-tavern" });
    await screen.findByTestId("channel-picker");
    await waitFor(() => expect(trigger()).toHaveTextContent("The Tavern"));
    await pick("General");
    fireEvent.click(startButton());
    await waitFor(() => expect(first.startRequests).toEqual(["c-general"]));
    expect(await screen.findByText("Live")).toBeInTheDocument();

    // A fresh page load (new screen, new query cache, same browser storage).
    cleanup();
    const second = mount({ defaultChannelId: "c-tavern" });
    await screen.findByTestId("channel-picker");
    await waitFor(() => expect(trigger()).toHaveTextContent("General"));
    // It differs from the Tenant default, so promoting it is still on offer.
    expect(screen.getByTestId("set-default-channel")).toBeInTheDocument();
    fireEvent.click(startButton());
    await waitFor(() => expect(second.startRequests).toEqual(["c-general"]));
  });

  it("does not remember a channel whose start failed", async () => {
    const h = mount({ defaultChannelId: "c-tavern", failStart: true });
    await screen.findByTestId("channel-picker");
    await pick("General");
    fireEvent.click(startButton());
    await waitFor(() => expect(h.startRequests).toEqual(["c-general"]));

    cleanup();
    mount({ defaultChannelId: "c-tavern" });
    await screen.findByTestId("channel-picker");
    await waitFor(() => expect(trigger()).toHaveTextContent("The Tavern"));
  });

  it("shows a placeholder and holds Start when nothing is known, instead of preselecting the first channel", async () => {
    const h = mount();
    await screen.findByTestId("channel-picker");
    await waitFor(() => expect(trigger()).toHaveTextContent("Voice channel…"));
    expect(trigger()).not.toHaveTextContent("General");
    expect(startButton()).toBeDisabled();
    // Nothing is selected, so there is nothing to promote either.
    expect(screen.queryByTestId("set-default-channel")).not.toBeInTheDocument();

    await pick("The Tavern");
    expect(startButton()).toBeEnabled();
    fireEvent.click(startButton());
    await waitFor(() => expect(h.startRequests).toEqual(["c-tavern"]));
  });

  it("keeps the memory per Discord server", async () => {
    localStorage.setItem("gx-voice-channel:g-other", "c-general");
    mount({ guildId: "g1", defaultChannelId: "c-tavern" });
    await screen.findByTestId("channel-picker");
    await waitFor(() => expect(trigger()).toHaveTextContent("The Tavern"));
  });

  it("ignores a remembered channel that no longer exists and falls back to the default", async () => {
    localStorage.setItem("gx-voice-channel:g1", "c-deleted");
    mount({ defaultChannelId: "c-tavern" });
    await screen.findByTestId("channel-picker");
    await waitFor(() => expect(trigger()).toHaveTextContent("The Tavern"));
  });

  it("works with storage unavailable", async () => {
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("denied");
    });
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("denied");
    });
    const h = mount({ defaultChannelId: "c-tavern" });
    await screen.findByTestId("channel-picker");
    await waitFor(() => expect(trigger()).toHaveTextContent("The Tavern"));
    fireEvent.click(startButton());
    await waitFor(() => expect(h.startRequests).toEqual(["c-tavern"]));
    expect(await screen.findByText("Live")).toBeInTheDocument();
  });
});
