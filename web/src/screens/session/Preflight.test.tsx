import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react";
import { createRouterTransport, ConnectError, Code } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";

vi.mock("sonner", () => ({
  toast: Object.assign(vi.fn(), { error: vi.fn(), success: vi.fn(), warning: vi.fn(), dismiss: vi.fn() }),
  Toaster: () => null,
}));

import {
  SessionService,
  CampaignService,
  VoiceService,
  HealthStatus,
  GetSessionResponseSchema,
  ListSessionsResponseSchema,
  ListSessionVoiceChannelsResponseSchema,
  VoiceChannelSchema,
  GetActiveCampaignResponseSchema,
  GetCampaignRosterResponseSchema,
  GetRosterReadinessResponseSchema,
  AgentReadinessSchema,
  GetProviderHealthResponseSchema,
  ProviderHealthSchema,
  CampaignSchema,
  AgentSchema,
  type ProviderHealth,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import type { ScreenLocation } from "@/app/screenNav";
import { makeQueryClient } from "@/lib/queryClient";
import { degradedProviders, discordCheck } from "./Preflight";
import { Screen } from "./testScreen";
import { tEn } from "./testT";

// Pre-flight (the idle control card): four rows that say whether tonight will
// work before Start is pressed. Only the Discord rows can hold Start; a
// degraded provider, a voiceless NPC or highlight recording being off are
// warnings the GM may start through.

const BUTLER = create(AgentSchema, { id: "butler-1", role: "butler", name: "Butler", voice: "" });
const BART = create(AgentSchema, { id: "bart-1", role: "character", name: "Bart", voice: "v-bart", speakerColor: 1 });
const GRETA = create(AgentSchema, { id: "greta-1", role: "character", name: "Greta", voice: "", speakerColor: 2 });
const OTTO = create(AgentSchema, { id: "otto-1", role: "character", name: "Otto", voice: "v-otto", speakerColor: 3 });

type Opts = {
  roster?: ReturnType<typeof create<typeof AgentSchema>>[];
  health?: ProviderHealth[] | "fail";
  tapeArmed?: boolean;
  channelsError?: string;
};

function transport(opts: Opts = {}) {
  const roster = opts.roster ?? [BUTLER, BART, GRETA, OTTO];
  return createRouterTransport(({ service }) => {
    service(SessionService, {
      getSession: () => create(GetSessionResponseSchema, { active: false }),
      listSessions: () => create(ListSessionsResponseSchema, { sessions: [] }),
      listSessionVoiceChannels: () => {
        if (opts.channelsError) throw new ConnectError(opts.channelsError, Code.FailedPrecondition);
        return create(ListSessionVoiceChannelsResponseSchema, {
          channels: [create(VoiceChannelSchema, { id: "ch1", name: "Tavern" })],
          defaultChannelId: "ch1",
        });
      },
    });
    service(CampaignService, {
      getActiveCampaign: () =>
        create(GetActiveCampaignResponseSchema, {
          campaign: create(CampaignSchema, { id: "c1", name: "Saltmarsh", tapeArmed: opts.tapeArmed ?? true }),
        }),
      getCampaignRoster: () => create(GetCampaignRosterResponseSchema, { roster }),
      getRosterReadiness: () =>
        create(GetRosterReadinessResponseSchema, {
          agents: [
            create(AgentReadinessSchema, { agentId: "bart-1", linked: true, factCount: 3 }),
            create(AgentReadinessSchema, { agentId: "otto-1", linked: false }),
          ],
        }),
    });
    service(VoiceService, {
      getProviderHealth: () => {
        if (opts.health === "fail") throw new ConnectError("probe down", Code.Unavailable);
        return create(GetProviderHealthResponseSchema, {
          providers: opts.health ?? [
            create(ProviderHealthSchema, { provider: "groq", component: "llm", status: HealthStatus.HEALTHY }),
            create(ProviderHealthSchema, { provider: "elevenlabs", component: "tts", status: HealthStatus.HEALTHY }),
          ],
        });
      },
    });
  });
}

function renderIdle(opts: Opts = {}, onNavigate?: (next: ScreenLocation) => void) {
  const queryClient = makeQueryClient();
  queryClient.setDefaultOptions({ queries: { retry: false } });
  render(
    <Providers transport={transport(opts)} queryClient={queryClient}>
      <Screen onNavigate={onNavigate} />
    </Providers>,
  );
}

const row = (id: string) => screen.getByTestId(`preflight-${id}`);
const startButton = () => screen.getByRole("button", { name: "Start session" });

describe("Pre-flight in the idle control card", () => {
  beforeEach(() => localStorage.clear());

  it("lists the four checks, and a healthy evening is ready on every row", async () => {
    renderIdle({ roster: [BUTLER, BART] });

    const list = await screen.findByRole("list", { name: "Before you start" });
    expect(within(list).getAllByRole("listitem")).toHaveLength(4);

    await waitFor(() => expect(row("providers")).toHaveAttribute("data-tone", "ok"));
    expect(row("discord")).toHaveAttribute("data-tone", "ok");
    expect(row("discord")).toHaveTextContent("The Bot joins “Tavern”");
    expect(row("cast")).toHaveAttribute("data-tone", "ok");
    expect(row("cast")).toHaveTextContent("1 NPC, with its own voice.");
    expect(row("highlights")).toHaveAttribute("data-tone", "ok");
    expect(startButton()).toBeEnabled();
  });

  it("names a degraded provider as a warning that does not hold Start", async () => {
    renderIdle({
      health: [
        create(ProviderHealthSchema, { provider: "groq", component: "llm", status: HealthStatus.HEALTHY }),
        create(ProviderHealthSchema, {
          provider: "elevenlabs",
          component: "tts",
          status: HealthStatus.DEGRADED,
          code: "key_rejected",
          detail: "ElevenLabs rejected the key",
        }),
      ],
    });

    await waitFor(() => expect(row("providers")).toHaveAttribute("data-tone", "warn"));
    // Which service, whose, and the sentence the Setup screen uses for the code.
    expect(row("providers")).toHaveTextContent(
      "Voices (ElevenLabs): The key was rejected — check it or paste a new one.",
    );
    // The verdict is spoken, not only drawn.
    expect(row("providers")).toHaveTextContent("needs attention");
    await waitFor(() => expect(startButton()).toBeEnabled());
  });

  it("a health check that cannot run is information, with a retry, and Start stays possible", async () => {
    renderIdle({ health: "fail" });
    await waitFor(() => expect(row("providers")).toHaveAttribute("data-tone", "info"));
    expect(within(row("providers")).getByRole("button", { name: "Try again" })).toBeInTheDocument();
    await waitFor(() => expect(startButton()).toBeEnabled());
  });

  it("a missing Discord link blocks Start and its row leads to Setup", async () => {
    const onNavigate = vi.fn();
    renderIdle({ channelsError: "link a Discord server first" }, onNavigate);

    await waitFor(() => expect(row("discord")).toHaveAttribute("data-tone", "block"));
    expect(row("discord")).toHaveTextContent("Link a Discord server first.");
    expect(startButton()).toBeDisabled();

    fireEvent.click(within(row("discord")).getByRole("button", { name: "Open Setup" }));
    expect(onNavigate).toHaveBeenCalledWith(
      expect.objectContaining({ screen: "configuration" }),
      expect.anything(),
    );
  });

  it("names the NPCs without a voice and leads to the cast", async () => {
    const onNavigate = vi.fn();
    renderIdle({}, onNavigate);

    await waitFor(() => expect(row("cast")).toHaveAttribute("data-tone", "warn"));
    expect(row("cast")).toHaveTextContent("Greta has no voice picked and will speak with the default voice.");
    // Start is not held by a voiceless NPC.
    await waitFor(() => expect(startButton()).toBeEnabled());

    // With one voiceless NPC, the way to fix it opens that NPC.
    fireEvent.click(within(row("cast")).getByRole("button", { name: "Open Cast" }));
    expect(onNavigate).toHaveBeenCalledWith(
      expect.objectContaining({ screen: "campaign", view: "cast", search: { agent: "greta-1" } }),
      expect.anything(),
    );
  });

  it("warns when the cast is only the Butler", async () => {
    renderIdle({ roster: [BUTLER] });
    await waitFor(() => expect(row("cast")).toHaveTextContent("No NPCs yet — only the Butler will answer."));
    expect(row("cast")).toHaveAttribute("data-tone", "warn");
  });

  it("says when highlight recording is off and leads to the campaign's settings", async () => {
    const onNavigate = vi.fn();
    renderIdle({ tapeArmed: false }, onNavigate);

    await waitFor(() => expect(row("highlights")).toHaveTextContent("Highlight recording is off."));
    expect(row("highlights")).toHaveAttribute("data-tone", "info");
    fireEvent.click(within(row("highlights")).getByRole("button", { name: "Campaign settings" }));
    expect(onNavigate).toHaveBeenCalledWith(
      expect.objectContaining({ screen: "campaign", view: "settings" }),
      expect.anything(),
    );
  });

  it("is in German when the display language is German", async () => {
    localStorage.setItem("gx-lang", "de");
    renderIdle({ tapeArmed: false });
    expect(await screen.findByRole("list", { name: "Vor dem Start" })).toBeInTheDocument();
    await waitFor(() => expect(row("providers")).toHaveTextContent("KI-Dienste"));
    expect(screen.queryByText("Highlight recording is off.", { exact: false })).not.toBeInTheDocument();
  });
});

describe("The idle voice rail says who is actually ready", () => {
  beforeEach(() => localStorage.clear());

  it("shows each NPC's readiness instead of a uniform Ready", async () => {
    renderIdle();

    const rows = await screen.findAllByTestId("voice-row");
    const byName = (name: string) => rows.find((r) => within(r).queryByText(name) != null)!;
    await waitFor(() => expect(byName("Bart")).toHaveTextContent("Ready · 3 facts in reach"));
    // No voice outranks everything else: it is what the GM hears.
    expect(byName("Greta")).toHaveTextContent("No voice picked — uses the default");
    expect(byName("Otto")).toHaveTextContent("No wiki entry linked");
  });
});

describe("discordCheck", () => {
  const base = { loading: false, error: null, unsupported: false, channelCount: 2, selectedChannelName: "Tavern" };

  it("blocks only on the two known preconditions", () => {
    expect(discordCheck({ ...base, error: "link a Discord server first" }, tEn).tone).toBe("block");
    expect(discordCheck({ ...base, error: "save a Discord Bot token first" }, tEn).tone).toBe("block");
    // Anything else is the server's own words, as a warning.
    expect(discordCheck({ ...base, error: "gateway hiccup" }, tEn)).toEqual({
      tone: "warn",
      detail: "gateway hiccup",
      setup: true,
    });
  });

  it("asks for a pick, notes an empty list, and is ready with a channel", () => {
    expect(discordCheck({ ...base, selectedChannelName: null }, tEn).tone).toBe("warn");
    expect(discordCheck({ ...base, channelCount: 0, selectedChannelName: null }, tEn).tone).toBe("warn");
    expect(discordCheck(base, tEn).tone).toBe("ok");
    expect(discordCheck({ ...base, loading: true }, tEn).tone).toBe("pending");
    expect(discordCheck({ ...base, unsupported: true, channelCount: 0 }, tEn).tone).toBe("info");
  });
});

describe("degradedProviders", () => {
  it("names each degraded voice-session provider and skips the image slot", () => {
    const lines = degradedProviders(
      [
        create(ProviderHealthSchema, { provider: "gemini", component: "image", status: HealthStatus.DEGRADED }),
        create(ProviderHealthSchema, {
          provider: "groq",
          component: "llm",
          status: HealthStatus.DEGRADED,
          code: "a_code_from_the_future",
          detail: "Groq is on fire",
        }),
        create(ProviderHealthSchema, { provider: "elevenlabs", component: "tts", status: HealthStatus.HEALTHY }),
      ],
      tEn,
    );
    // An unknown code falls back to the server's English detail.
    expect(lines).toEqual(["Language model (Groq): Groq is on fire"]);
  });
});
