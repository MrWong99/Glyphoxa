import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, within, fireEvent, waitFor } from "@testing-library/react";
import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { toast } from "sonner";

import {
  AuthService,
  CampaignService,
  ProviderService,
  VoiceService,
  HealthStatus,
  GetActiveCampaignResponseSchema,
  CreateCampaignResponseSchema,
  SetActiveCampaignResponseSchema,
  CampaignSchema,
  ListProviderConfigsResponseSchema,
  ProviderCredentialSchema,
  SaveProviderConfigResponseSchema,
  SaveDiscordSettingsResponseSchema,
  ReleaseDiscordGuildResponseSchema,
  GetProviderHealthResponseSchema,
  ProviderHealthSchema,
  ListModelsResponseSchema,
  ResolveGuildInviteResponseSchema,
  GetSpendCapsResponseSchema,
  SetSpendCapsResponseSchema,
  SpendCapsSchema,
  ListSupportedLanguagesResponseSchema,
  type ProviderHealth,
  type SaveDiscordSettingsRequest,
  type SetSpendCapsRequest,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { MemoryScreenNav, type ScreenLocation } from "@/app/screenNav";
import { makeQueryClient } from "@/lib/queryClient";
import { Configuration } from "./Configuration";
import { currentUser, keyRow, usageSummary } from "./testRows";

// Toasts are events the screen reports; the tests assert on them directly.
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() } }));

beforeEach(() => {
  vi.mocked(toast.success).mockClear();
  vi.mocked(toast.error).mockClear();
});

const CAMPAIGN = {
  id: "11111111-1111-1111-1111-111111111111",
  tenantId: "22222222-2222-2222-2222-222222222222",
  name: "The Sunless Citadel",
  system: "D&D 5e",
  language: "en",
};

// cred builds a ProviderCredential the List RPC returns. saved => masked + last4.
// A saved key carries the date it was saved, as the server sends it.
const SAVED_AT = new Date("2026-11-03T12:00:00Z");

function cred(component: string, provider: string, last4?: string, model = "", platform = false) {
  return create(ProviderCredentialSchema, {
    component,
    provider,
    everSaved: Boolean(last4),
    showMasked: Boolean(last4),
    last4: last4 ?? "",
    model,
    updatedAt: last4 ? timestampFromDate(SAVED_AT) : undefined,
    // Mirrors the server: a saved own key is never "provided by the platform".
    providedByPlatform: platform && !last4,
    // Mirrors the server's code default (#702): with no deployment default and
    // no Anthropic row, Groq is the voice LLM in use.
    inEffect: component === "llm" && provider === "groq",
  });
}

// health builds a ProviderHealth entry the GetProviderHealth RPC returns.
function health(provider: string, status: HealthStatus, botTag = ""): ProviderHealth {
  return create(ProviderHealthSchema, { provider, status, botTag });
}

const GROQ_MODELS = ["llama-3.3-70b-versatile", "llama-3.1-8b-instant"];

// stateful mock backend: List reflects what Save mutates, so an invalidation
// refetch shows the saved credential — proving the write-only round-trip from
// the screen's side (the RPC never returns a secret value). `opts` seeds already-
// saved slots and the async health the GetProviderHealth RPC reports (#70).
function mockBackend(
  opts: {
    saved?: Partial<Record<"groq" | "elevenlabs" | "discord" | "gemini", string>>;
    // platform marks the slots the deployment provides for this tenant (W9:
    // trial/central-token tenants) — the server's provided_by_platform.
    platform?: Partial<Record<"groq" | "anthropic" | "elevenlabs" | "discord" | "gemini", boolean>>;
    health?: ProviderHealth[];
    // discordSaves captures every SaveDiscordSettings request so tests can pin
    // the wire shape (#142: a token-only save must omit the ID fields).
    discordSaves?: SaveDiscordSettingsRequest[];
    // discordSaveError makes SaveDiscordSettings fail (simulates a server-side
    // failure) so tests can pin that the screen surfaces the rejection.
    // discordSaveCode overrides its Connect code (default Internal) — #504 pins
    // that the server's PermissionDenied proof message reaches the operator.
    discordSaveError?: string;
    discordSaveCode?: Code;
    // boundGuild seeds an already-linked guild, the state the #504 unlink
    // control acts on. releaseCalls captures every ReleaseDiscordGuild
    // request's guild_id; releaseError fails the release.
    boundGuild?: { guildId: string; guildName?: string };
    // linkName is the server name the guild-admin proof reads when a guild is
    // linked; the server stores it with the link (ADR-0004 amendment 2026-11-26).
    linkName?: string;
    releaseCalls?: string[];
    releaseError?: string;
    // providerSaves captures every SaveProviderConfig request so tests can pin
    // the model-only wire shape (#227: secret "" + model).
    providerSaves?: Array<{ provider: string; secret: string; model: string }>;
    // listModelsError makes the catalog fetch fail at the transport, pinning
    // that free-text model entry survives a dead catalog (#227).
    listModelsError?: string;
    // discordApplicationId seeds the server-provided Discord application id the
    // read echoes so the screen composes the bot-authorization URL (#110).
    discordApplicationId?: string;
    // integrationState / integrationDetail seed this tenant's standing Discord
    // client health the read surfaces (#489): "ok" / "waiting" / "failed" + detail.
    integrationState?: string;
    integrationDetail?: string;
    // inviteResolve seeds the guild ResolveGuildInvite returns for a pasted
    // invite (#105). voiceChannels still ride the wire (the RPC serves the
    // Session screen too) but THIS screen only consumes the guild id/name.
    // inviteError makes the resolve fail instead.
    inviteResolve?: { guildId: string; guildName: string; voiceChannels: { id: string; name: string }[] };
    inviteError?: { code: Code; message: string };
    // inviteResolver, when set, computes the response PER code (and may return a
    // pending promise a test releases later) so a test can force a slow resolve
    // for one invite and a fast one for another — the stale-response race (#245).
    inviteResolver?: (
      code: string,
    ) => Promise<{ guildId: string; guildName: string; voiceChannels: { id: string; name: string }[] }>;
    // inviteCodes captures every ResolveGuildInvite request's code so a test can
    // pin that the SPA sent the BARE code (not the full URL).
    inviteCodes?: string[];
    // spendCaps seeds the stored per-Tenant spend caps GetSpendCaps returns (#130);
    // spendSaves captures every SetSpendCaps request so a test can pin the wire
    // shape (a blank field must be omitted, i.e. undefined = cleared).
    spendCaps?: { softUsd?: number; hardUsd?: number };
    spendSaves?: SetSpendCapsRequest[];
  } = {},
) {
  const state = {
    groqModel: "",
    groq: opts.saved?.groq,
    elevenlabs: opts.saved?.elevenlabs,
    gemini: opts.saved?.gemini,
    discord: opts.saved?.discord,
    guildId: opts.boundGuild?.guildId ?? "",
    guildName: opts.boundGuild?.guildName ?? "",
    spendSoft: opts.spendCaps?.softUsd,
    spendHard: opts.spendCaps?.hardUsd,
  };
  return createRouterTransport(({ service }) => {
    service(VoiceService, {
      getProviderHealth: () =>
        create(GetProviderHealthResponseSchema, { providers: opts.health ?? [] }),
      listModels: () => {
        if (opts.listModelsError) throw new ConnectError(opts.listModelsError, Code.Unavailable);
        return create(ListModelsResponseSchema, { models: GROQ_MODELS });
      },
    });
    service(CampaignService, {
      getActiveCampaign: () =>
        create(GetActiveCampaignResponseSchema, { campaign: create(CampaignSchema, CAMPAIGN) }),
    });
    service(AuthService, { getCurrentUser: currentUser });
    service(ProviderService, {
      getUsageSummary: usageSummary,
      listProviderConfigs: () =>
        create(ListProviderConfigsResponseSchema, {
          credentials: [
            cred("discord", "discord", state.discord, "", opts.platform?.discord),
            cred("llm", "groq", state.groq, state.groqModel, opts.platform?.groq),
            // The second voice-LLM slot (#702): never saved here, not in use.
            cred("llm", "anthropic", undefined, "", opts.platform?.anthropic),
            cred("tts", "elevenlabs", state.elevenlabs, "", opts.platform?.elevenlabs),
            cred("image", "gemini", state.gemini, "", opts.platform?.gemini),
          ],
          guildId: state.guildId,
          guildName: state.guildName,
          discordApplicationId: opts.discordApplicationId ?? "",
          integrationState: opts.integrationState ?? "",
          integrationDetail: opts.integrationDetail ?? "",
        }),
      saveProviderConfig: (req) => {
        opts.providerSaves?.push({ provider: req.provider, secret: req.secret, model: req.model });
        if (req.secret === "") {
          // Model-only save (#227): mirrors internal/rpc/provider.go — a stored
          // key is required, the sealed key is untouched, only model changes.
          if (req.provider !== "groq" || !state.groq) {
            throw new ConnectError("secret is required", Code.InvalidArgument);
          }
          state.groqModel = req.model;
          return create(SaveProviderConfigResponseSchema, {
            credential: cred("llm", "groq", state.groq, state.groqModel),
          });
        }
        const last4 = req.secret.slice(-4);
        if (req.provider === "groq") {
          state.groq = last4;
          state.groqModel = req.model;
        }
        if (req.provider === "elevenlabs") state.elevenlabs = last4;
        if (req.provider === "gemini") state.gemini = last4;
        const comp =
          req.provider === "groq" ? "llm" : req.provider === "gemini" ? "image" : "tts";
        return create(SaveProviderConfigResponseSchema, {
          credential: cred(comp, req.provider, last4, req.model),
        });
      },
      saveDiscordSettings: (req) => {
        opts.discordSaves?.push(req);
        if (opts.discordSaveError)
          throw new ConnectError(opts.discordSaveError, opts.discordSaveCode ?? Code.Internal);
        // Presence semantics mirror the real server (#142): an omitted field
        // leaves the stored value untouched, and present-but-empty is REJECTED
        // per field exactly like internal/rpc/provider.go — so a client that
        // regresses into sending blank IDs fails loudly here instead of
        // silently diverging. (voice_channel_id still exists on the wire for
        // the Session screen's Default Voice Channel; THIS screen never sends it.)
        if (req.guildId !== undefined && req.guildId === "") {
          throw new ConnectError("guild_id must not be empty when provided", Code.InvalidArgument);
        }
        if (req.voiceChannelId !== undefined && req.voiceChannelId === "") {
          throw new ConnectError(
            "voice_channel_id must not be empty when provided",
            Code.InvalidArgument,
          );
        }
        if (req.botToken !== undefined) state.discord = req.botToken.slice(-4);
        if (req.guildId !== undefined) {
          state.guildId = req.guildId;
          state.guildName = opts.linkName ?? "";
        }
        return create(SaveDiscordSettingsResponseSchema, {
          credential: cred("discord", "discord", state.discord),
          guildId: state.guildId,
        });
      },
      releaseDiscordGuild: (req) => {
        opts.releaseCalls?.push(req.guildId);
        if (opts.releaseError) throw new ConnectError(opts.releaseError, Code.FailedPrecondition);
        // Mirror the server (#504): the echo must match the bound guild.
        if (!req.guildId || req.guildId !== state.guildId) {
          throw new ConnectError(
            "that Discord server is not the one currently linked; reload and try again",
            Code.FailedPrecondition,
          );
        }
        state.guildId = "";
        state.guildName = "";
        return create(ReleaseDiscordGuildResponseSchema, { guildId: "" });
      },
      resolveGuildInvite: async (req) => {
        opts.inviteCodes?.push(req.inviteCode);
        if (opts.inviteError) throw new ConnectError(opts.inviteError.message, opts.inviteError.code);
        const r = opts.inviteResolver
          ? await opts.inviteResolver(req.inviteCode)
          : (opts.inviteResolve ?? { guildId: "111", guildName: "The Keep", voiceChannels: [] });
        return create(ResolveGuildInviteResponseSchema, r);
      },
      getSpendCaps: () =>
        create(GetSpendCapsResponseSchema, {
          caps: create(SpendCapsSchema, { softUsd: state.spendSoft, hardUsd: state.spendHard }),
        }),
      setSpendCaps: (req) => {
        opts.spendSaves?.push(req);
        // Mirror the server (#130): negative or hard < soft is rejected; an omitted
        // field clears that cap.
        if ((req.softUsd ?? 0) < 0 || (req.hardUsd ?? 0) < 0) {
          throw new ConnectError("cap must not be negative", Code.InvalidArgument);
        }
        if (req.softUsd !== undefined && req.hardUsd !== undefined && req.hardUsd < req.softUsd) {
          throw new ConnectError("hard cap must be >= soft cap", Code.InvalidArgument);
        }
        state.spendSoft = req.softUsd;
        state.spendHard = req.hardUsd;
        return create(SetSpendCapsResponseSchema, {
          caps: create(SpendCapsSchema, { softUsd: state.spendSoft, hardUsd: state.spendHard }),
        });
      },
    });
  });
}

function renderScreen(transport = mockBackend(), onNavigate?: (loc: ScreenLocation) => void) {
  return render(
    <Providers transport={transport} queryClient={makeQueryClient()}>
      <MemoryScreenNav initial={{ screen: "configuration" }} onNavigate={onNavigate}>
        <Configuration />
      </MemoryScreenNav>
    </Providers>,
  );
}

// The badge of a key row, by its text — not the checklist's "Optional"
// heading, nor a step hint.
const badges = (text: RegExp) => screen.queryAllByText(text, { selector: ".gx-badge" });

// Opens the folded-away manual Server ID field of the Discord card.
async function serverIdField() {
  fireEvent.click(await screen.findByRole("button", { name: /enter the server id instead/i }));
  return screen.getByLabelText("Server ID") as HTMLInputElement;
}

// Opens the "Use your own Discord bot" card and returns the token field.
async function botTokenField() {
  fireEvent.click(await screen.findByRole("button", { name: /use your own discord bot/i }));
  return screen.findByLabelText("Bot token key");
}

describe("Configuration", () => {
  it("opens with the checklist and says the settings are the whole table's", async () => {
    renderScreen();
    const checklist = (await screen.findByRole("heading", { name: /get ready for your first session/i })).closest(
      ".gx-setup-checklist",
    ) as HTMLElement;
    // Step 1 is done and names the campaign; the old "Active campaign" card is gone.
    expect(await within(checklist).findByText(CAMPAIGN.name)).toBeInTheDocument();
    expect(screen.queryByText(CAMPAIGN.system)).not.toBeInTheDocument();
    expect(screen.getByText(/these settings belong to your whole table/i)).toBeInTheDocument();
    // The four steps in order, and the optional extras apart from them.
    const steps = within(checklist).getAllByRole("listitem").map((li) => li.textContent ?? "");
    expect(steps.slice(0, 4)).toEqual([
      expect.stringMatching(/^1\. Create a campaign/),
      expect.stringMatching(/^2\. Connect Discord/),
      expect.stringMatching(/^3\. Voice and speech/),
      expect.stringMatching(/^4\. AI model/),
    ]);
    expect(within(checklist).getByRole("heading", { name: "Optional" })).toBeInTheDocument();
    // Nothing is ready yet: no Discord, no keys.
    expect(within(checklist).getByText("1 of 4 done")).toBeInTheDocument();
    expect(screen.queryByText(/your table is ready to play/i)).not.toBeInTheDocument();
  });

  it("ticks all four steps for a trial table with a linked server and offers Go to Session", async () => {
    const moves: ScreenLocation[] = [];
    renderScreen(
      mockBackend({
        platform: { groq: true, anthropic: true, elevenlabs: true, discord: true },
        boundGuild: { guildId: "472093001100", guildName: "Tides over Saltmarsh" },
      }),
      (loc) => moves.push(loc),
    );
    expect(await screen.findByText(/your table is ready to play/i)).toBeInTheDocument();
    expect(screen.getByText("4 of 4 done")).toBeInTheDocument();
    expect(screen.getByText("Linked to Tides over Saltmarsh")).toBeInTheDocument();
    // No empty password field for a service the plan includes.
    expect(screen.queryByLabelText("Groq key")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("ElevenLabs key")).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Go to Session" }));
    expect(moves.at(-1)?.screen).toBe("session");
  });

  it("links each open step to its section and moves focus there", async () => {
    renderScreen();
    await screen.findByLabelText(/paste a discord link/i);
    const link = screen.getByRole("link", { name: /connect discord/i });
    fireEvent.click(link);
    expect(document.activeElement?.id).toBe("setup-discord");
    fireEvent.click(screen.getByRole("link", { name: /voice and speech/i }));
    expect(document.activeElement?.id).toBe("setup-voice");
  });

  it("shows skeletons while the setup read is out, never Key needed", async () => {
    const transport = createRouterTransport(({ service }) => {
      service(VoiceService, {
        getProviderHealth: () => create(GetProviderHealthResponseSchema, { providers: [] }),
        listModels: () => create(ListModelsResponseSchema, { models: [] }),
      });
      service(CampaignService, {
        getActiveCampaign: () =>
          create(GetActiveCampaignResponseSchema, { campaign: create(CampaignSchema, CAMPAIGN) }),
      });
      service(AuthService, { getCurrentUser: currentUser });
      service(ProviderService, {
        getUsageSummary: usageSummary,
        listProviderConfigs: () => new Promise(() => {}),
        getSpendCaps: () => create(GetSpendCapsResponseSchema, {}),
      });
    });
    renderScreen(transport);
    expect(await screen.findByTestId("config-loading")).toBeInTheDocument();
    await screen.findByText(CAMPAIGN.name);
    expect(screen.queryByText(/key needed/i)).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Groq key")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /link server/i })).not.toBeInTheDocument();
    // The steps that depend on the read are "checking", not to-dos.
    expect(screen.getAllByText("Checking…").length).toBeGreaterThanOrEqual(3);
  });

  it("shows one error with Retry and no editable rows when the setup read fails", async () => {
    let fail = true;
    const transport = createRouterTransport(({ service }) => {
      service(VoiceService, {
        getProviderHealth: () => create(GetProviderHealthResponseSchema, { providers: [] }),
        listModels: () => create(ListModelsResponseSchema, { models: [] }),
      });
      service(CampaignService, {
        getActiveCampaign: () =>
          create(GetActiveCampaignResponseSchema, { campaign: create(CampaignSchema, CAMPAIGN) }),
      });
      service(AuthService, { getCurrentUser: currentUser });
      service(ProviderService, {
        getUsageSummary: usageSummary,
        listProviderConfigs: () => {
          if (fail) throw new ConnectError("database unavailable", Code.Unavailable);
          return create(ListProviderConfigsResponseSchema, {
            credentials: [cred("discord", "discord"), cred("llm", "groq"), cred("tts", "elevenlabs")],
          });
        },
        getSpendCaps: () => create(GetSpendCapsResponseSchema, {}),
      });
    });
    renderScreen(transport);

    expect(await screen.findByText("Couldn't load your setup")).toBeInTheDocument();
    expect(screen.getAllByText("Couldn't load your setup")).toHaveLength(1);
    expect(screen.queryByText(/key needed/i)).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Groq key")).not.toBeInTheDocument();
    expect(screen.queryByLabelText(/paste a discord link/i)).not.toBeInTheDocument();

    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByLabelText("Groq key")).toBeInTheDocument();
    expect(screen.queryByText("Couldn't load your setup")).not.toBeInTheDocument();
  });

  it("says Checking… on a saved key while the health probe is out", async () => {
    const transport = createRouterTransport(({ service }) => {
      service(VoiceService, {
        getProviderHealth: () => new Promise(() => {}),
        listModels: () => create(ListModelsResponseSchema, { models: [] }),
      });
      service(CampaignService, {
        getActiveCampaign: () =>
          create(GetActiveCampaignResponseSchema, { campaign: create(CampaignSchema, CAMPAIGN) }),
      });
      service(AuthService, { getCurrentUser: currentUser });
      service(ProviderService, {
        getUsageSummary: usageSummary,
        listProviderConfigs: () =>
          create(ListProviderConfigsResponseSchema, {
            credentials: [cred("discord", "discord"), cred("llm", "groq"), cred("tts", "elevenlabs", "abcd")],
          }),
        getSpendCaps: () => create(GetSpendCapsResponseSchema, {}),
      });
    });
    renderScreen(transport);
    const row = await keyRow("ElevenLabs");
    expect(within(row).getByText("Checking…", { selector: ".gx-badge" })).toBeInTheDocument();
    expect(within(row).queryByText(/^saved$/i)).not.toBeInTheDocument();
  });

  it("shows Key needed for each required unsaved credential and Optional for the rest", async () => {
    renderScreen();
    // Two Key-needed badges (groq, elevenlabs) — a session needs both. Gemini
    // (images) and the voice-LLM slot that is not in use (Anthropic, #702)
    // read "Optional". The own-bot token is folded away (ADR-0057: only a BYOK
    // Bot needs one), and the Planning chat is not a key row (ADR-0062).
    await waitFor(() => expect(badges(/key needed/i)).toHaveLength(2));
    expect(badges(/^optional$/i)).toHaveLength(2);
    expect(within(await keyRow("Gemini")).getByText(/^optional$/i, { selector: ".gx-badge" })).toBeInTheDocument();
    expect(screen.queryByLabelText("Bot token key")).not.toBeInTheDocument();
    expect(screen.queryByText(/working/i)).not.toBeInTheDocument();
  });

  it("says what each service does and links to where its key is made", async () => {
    renderScreen();
    const groq = await keyRow("Groq");
    expect(within(groq).getByText(/fast language model/i)).toBeInTheDocument();
    expect(within(groq).getByRole("link", { name: /get a groq key/i })).toHaveAttribute(
      "href",
      "https://console.groq.com/keys",
    );
    const eleven = await keyRow("ElevenLabs");
    expect(within(eleven).getByRole("link", { name: /get a elevenlabs key/i })).toHaveAttribute(
      "href",
      "https://elevenlabs.io/app/developers/api-keys",
    );
  });

  it("shows Included with a way to an own key, not an empty field, for slots the platform provides (W9)", async () => {
    renderScreen(
      mockBackend({
        platform: { groq: true, anthropic: true, elevenlabs: true, gemini: true, discord: true },
      }),
    );
    await waitFor(() => expect(badges(/^included$/i)).toHaveLength(4));
    expect(badges(/key needed/i)).toHaveLength(0);
    const groqRow = await keyRow("Groq");
    expect(within(groqRow).getByText("Included with your plan")).toBeInTheDocument();
    expect(within(groqRow).queryByLabelText("Groq key")).not.toBeInTheDocument();

    // The own-key override is one click away, and can be backed out of.
    fireEvent.click(within(groqRow).getByRole("button", { name: "Use my own key" }));
    expect(within(groqRow).getByLabelText("Groq key")).toBeInTheDocument();
    expect(within(groqRow).getByRole("button", { name: /^save$/i })).toBeInTheDocument();
    fireEvent.click(within(groqRow).getByRole("button", { name: /cancel/i }));
    expect(within(groqRow).queryByLabelText("Groq key")).not.toBeInTheDocument();
  });

  it("downgrades an Included slot when its live probe fails (W9)", async () => {
    renderScreen(
      mockBackend({
        platform: { groq: true },
        health: [
          create(ProviderHealthSchema, {
            provider: "groq",
            status: HealthStatus.DEGRADED,
            code: "key_rejected",
            detail: "server fallback text",
          }),
        ],
      }),
    );
    const groqRow = await keyRow("Groq");
    expect(await within(groqRow).findByText(/having trouble/i)).toBeInTheDocument();
    expect(within(groqRow).queryByText(/^included$/i, { selector: ".gx-badge" })).not.toBeInTheDocument();
    // The stable code is translated client-side; the server text is only a fallback.
    expect(within(groqRow).getByText(/the key was rejected/i)).toBeInTheDocument();
    expect(within(groqRow).queryByText("server fallback text")).not.toBeInTheDocument();
  });

  it("falls back to the server's detail for a health code it does not know", async () => {
    renderScreen(
      mockBackend({
        saved: { groq: "abcd" },
        health: [
          create(ProviderHealthSchema, {
            provider: "groq",
            status: HealthStatus.DEGRADED,
            code: "something_new",
            detail: "A newer server says hi.",
          }),
        ],
      }),
    );
    expect(await screen.findByText("A newer server says hi.")).toBeInTheDocument();
  });

  it("gives the planning-chat model row its own overline", async () => {
    renderScreen();
    expect(await screen.findByText("Planning chat model", { selector: ".gx-overline" })).toBeInTheDocument();
    // "AI model" is the overline of the two voice-LLM slots (Groq, Anthropic,
    // #702); the chat row must not make it three.
    expect(screen.getAllByText("AI model", { selector: ".gx-overline" })).toHaveLength(2);
  });

  it("surfaces a failed Discord integration to the tenant (#489)", async () => {
    renderScreen(
      mockBackend({
        integrationState: "failed",
        integrationDetail: "invalid_bot_token: gateway rejected identify (close 4004)",
      }),
    );
    expect(await screen.findByText(/discord connection failed/i)).toBeInTheDocument();
    // The detail renders VISIBLY under the badge.
    expect(screen.getByText(/invalid_bot_token/)).toBeInTheDocument();
  });

  it("shows a reconnecting Discord integration as a warning, not connected (#642)", async () => {
    renderScreen(
      mockBackend({
        integrationState: "reconnecting",
        integrationDetail: "Lost the connection to Discord; next attempt in 2m0s.",
      }),
    );
    expect(await screen.findByText(/reconnecting to discord/i)).toBeInTheDocument();
    expect(screen.getByText(/next attempt in 2m0s/)).toBeInTheDocument();
    expect(screen.queryByText(/bot connected/i)).not.toBeInTheDocument();
  });

  it("never shows an unknown integration state as connected (#642)", async () => {
    renderScreen(mockBackend({ integrationState: "something-new", boundGuild: { guildId: "472093001100" } }));
    await screen.findByTestId("linked-server");
    expect(screen.queryByText(/bot connected/i)).not.toBeInTheDocument();
  });

  it("shows no integration badge in the waiting/web-only state (#489)", async () => {
    renderScreen();
    await screen.findByLabelText(/paste a discord link/i);
    expect(screen.queryByText(/discord connection failed/i)).not.toBeInTheDocument();
    expect(screen.queryByText(/bot connected/i)).not.toBeInTheDocument();
  });

  it("saves the Gemini image key write-only: the row masks with its last four and shows Replace", async () => {
    const saves: Array<{ provider: string; secret: string; model: string }> = [];
    renderScreen(mockBackend({ providerSaves: saves }));

    const geminiRow = await keyRow("Gemini");
    expect(within(geminiRow).getByText("Images")).toBeInTheDocument();
    fireEvent.change(within(geminiRow).getByLabelText("Gemini key"), { target: { value: "gm-secret-abcd" } });
    fireEvent.click(within(geminiRow).getByRole("button", { name: "Save" }));

    // After the invalidated list refetches, the row masks with a Replace affordance.
    expect(await within(geminiRow).findByText("•••• abcd")).toBeInTheDocument();
    expect(within(geminiRow).getByText("saved Nov 3, 2026")).toBeInTheDocument();
    expect(within(geminiRow).getByRole("button", { name: /replace/i })).toBeInTheDocument();
    // The save went out as the gemini provider; plaintext never lands in the DOM.
    expect(saves.some((s) => s.provider === "gemini" && s.secret === "gm-secret-abcd")).toBe(true);
    expect(screen.queryByText(/gm-secret-abcd/)).not.toBeInTheDocument();
  });

  it("saves a provider key write-only: the row masks and the badge says Saved until a probe answers", async () => {
    renderScreen();
    const groqRow = await keyRow("Groq");
    fireEvent.change(within(groqRow).getByLabelText("Groq key"), { target: { value: "test-groq-secret-eeee" } });
    fireEvent.click(within(groqRow).getByRole("button", { name: "Save" }));

    // No probe has reported for the new key, so it must not claim "Working".
    expect(await within(groqRow).findByText("•••• eeee")).toBeInTheDocument();
    expect(within(groqRow).getByRole("button", { name: /replace/i })).toBeInTheDocument();
    expect(await within(groqRow).findByText(/^saved$/i, { selector: ".gx-badge" })).toBeInTheDocument();
    expect(within(groqRow).queryByText(/working/i)).not.toBeInTheDocument();
    expect(screen.queryByText(/test-groq-secret-eeee/)).not.toBeInTheDocument();
  });

  it("renders an error in the row when saving a provider key fails", async () => {
    // A backend whose Save rejects (e.g. FailedPrecondition when the sealing
    // secret is unset) must leave visible evidence, not just un-busy the button.
    const transport = createRouterTransport(({ service }) => {
      service(VoiceService, {
        getProviderHealth: () => create(GetProviderHealthResponseSchema, { providers: [] }),
        listModels: () => create(ListModelsResponseSchema, { models: GROQ_MODELS }),
      });
      service(CampaignService, {
        getActiveCampaign: () =>
          create(GetActiveCampaignResponseSchema, { campaign: create(CampaignSchema, CAMPAIGN) }),
      });
      service(AuthService, { getCurrentUser: currentUser });
      service(ProviderService, {
        getUsageSummary: usageSummary,
        listProviderConfigs: () =>
          create(ListProviderConfigsResponseSchema, {
            credentials: [cred("discord", "discord"), cred("llm", "groq"), cred("tts", "elevenlabs")],
          }),
        saveProviderConfig: () => {
          throw new Error("secret sealing unavailable");
        },
      });
    });
    renderScreen(transport);

    const groqRow = await keyRow("Groq");
    fireEvent.change(within(groqRow).getByLabelText("Groq key"), { target: { value: "sk-will-fail" } });
    fireEvent.click(within(groqRow).getByRole("button", { name: "Save" }));

    const alert = await within(groqRow).findByRole("alert");
    expect(alert).toHaveTextContent(/couldn't save/i);
    // …and the key field stays editable for a retry, badge still unsaved.
    expect(within(groqRow).getByLabelText("Groq key")).toBeInTheDocument();
    expect(within(groqRow).getByText(/key needed/i)).toBeInTheDocument();
  });

  it("clears the save error when the Replace edit is cancelled", async () => {
    const transport = createRouterTransport(({ service }) => {
      service(VoiceService, {
        getProviderHealth: () => create(GetProviderHealthResponseSchema, { providers: [] }),
        listModels: () => create(ListModelsResponseSchema, { models: GROQ_MODELS }),
      });
      service(CampaignService, {
        getActiveCampaign: () =>
          create(GetActiveCampaignResponseSchema, { campaign: create(CampaignSchema, CAMPAIGN) }),
      });
      service(AuthService, { getCurrentUser: currentUser });
      service(ProviderService, {
        getUsageSummary: usageSummary,
        listProviderConfigs: () =>
          create(ListProviderConfigsResponseSchema, {
            credentials: [cred("discord", "discord"), cred("llm", "groq", "eeee"), cred("tts", "elevenlabs")],
          }),
        saveProviderConfig: () => {
          throw new Error("secret sealing unavailable");
        },
      });
    });
    renderScreen(transport);

    const groqRow = await keyRow("Groq");
    await within(groqRow).findByLabelText("Groq saved");
    fireEvent.click(within(groqRow).getByRole("button", { name: /replace/i }));
    fireEvent.change(within(groqRow).getByLabelText("Groq key"), { target: { value: "sk-new" } });
    fireEvent.click(within(groqRow).getByRole("button", { name: "Save" }));
    expect(await within(groqRow).findByRole("alert")).toHaveTextContent(/couldn't save/i);

    // Cancel returns to the masked view — the stale alert goes with it.
    fireEvent.click(within(groqRow).getByRole("button", { name: /cancel/i }));
    expect(within(groqRow).getByLabelText("Groq saved")).toBeInTheDocument();
    expect(within(groqRow).queryByRole("alert")).not.toBeInTheDocument();
  });

  it("renders the Groq model combobox defaulting to the catalog's first model (ListModels)", async () => {
    renderScreen();
    const groqModelSelect = await screen.findByLabelText("Groq model");
    expect(await within(groqModelSelect).findByText("llama-3.3-70b-versatile")).toBeInTheDocument();
  });

  it("upgrades a saved provider's badge to Degraded from the health RPC", async () => {
    renderScreen(mockBackend({ saved: { elevenlabs: "abcd" }, health: [health("elevenlabs", HealthStatus.DEGRADED)] }));
    const row = await keyRow("ElevenLabs");
    expect(await within(row).findByText(/having trouble/i)).toBeInTheDocument();
  });

  it("upgrades a saved provider's badge to Working only once the probe reports healthy", async () => {
    renderScreen(mockBackend({ saved: { elevenlabs: "abcd" }, health: [health("elevenlabs", HealthStatus.HEALTHY)] }));
    const row = await keyRow("ElevenLabs");
    expect(await within(row).findByText(/^working$/i)).toBeInTheDocument();
  });

  it("shows the resolved Discord bot tag from the live login, with the own-bot card open", async () => {
    renderScreen(
      mockBackend({
        saved: { discord: "tok9" },
        health: [health("discord", HealthStatus.HEALTHY, "Glyphoxa#4823")],
      }),
    );
    expect(await screen.findByText(/Connected as Glyphoxa#4823/)).toBeInTheDocument();
  });
});

describe("Configuration Discord card (#695)", () => {
  it("leads with adding the bot as the primary action (#110)", async () => {
    renderScreen(mockBackend({ discordApplicationId: "987654321098765432" }));
    const link = await screen.findByRole("link", { name: /add glyphoxa to your server/i });
    expect(link).toHaveAttribute("href", expect.stringContaining("client_id=987654321098765432"));
    expect(link).toHaveAttribute("target", "_blank");
    expect(link).toHaveClass("gx-btn--primary");
    expect(screen.getByRole("heading", { name: "Add Glyphoxa to your server" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Link your server" })).toBeInTheDocument();
  });

  it("disables the Add-Glyphoxa action when no application id is configured (#110)", async () => {
    renderScreen();
    expect(await screen.findByText(/no discord application id is configured/i)).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /add glyphoxa to your server/i })).not.toBeInTheDocument();
  });

  it("does not flash the missing-app-id note while the config query is loading (#110)", async () => {
    renderScreen();
    expect(screen.queryByText(/no discord application id is configured/i)).not.toBeInTheDocument();
    expect(await screen.findByText(/no discord application id is configured/i)).toBeInTheDocument();
  });

  it("links a server by its Server ID with a guild-only save and toasts it", async () => {
    const discordSaves: SaveDiscordSettingsRequest[] = [];
    renderScreen(mockBackend({ discordSaves }));

    fireEvent.change(await serverIdField(), { target: { value: "472093001100" } });
    fireEvent.click(screen.getByRole("button", { name: "Link server" }));

    // The typed guild rides the wire ALONE: sessions pick their channel on the
    // Session screen, so voice_channel_id stays off the wire.
    await waitFor(() => expect(discordSaves).toHaveLength(1));
    expect(discordSaves[0].guildId).toBe("472093001100");
    expect(discordSaves[0].voiceChannelId).toBeUndefined();
    expect(discordSaves[0].botToken).toBeUndefined();

    // The form gives way to the linked summary; the save is announced.
    expect(await screen.findByTestId("linked-server")).toHaveTextContent("Server ID 472093001100");
    expect(toast.success).toHaveBeenCalledWith("Linked 472093001100");
  });

  it("keeps Link off until a server is entered (#142)", async () => {
    renderScreen();
    const field = await serverIdField();
    const link = screen.getByRole("button", { name: "Link server" });
    expect(link).toBeDisabled();
    fireEvent.change(field, { target: { value: "472093001100" } });
    expect(link).toBeEnabled();
    fireEvent.change(field, { target: { value: "" } });
    expect(link).toBeDisabled();
  });

  it("keeps a refused link on the form with the server's message (#504)", async () => {
    renderScreen(
      mockBackend({
        discordSaveError: "you need the Manage Server permission in that Discord server to link it",
        discordSaveCode: Code.PermissionDenied,
      }),
    );
    fireEvent.change(await serverIdField(), { target: { value: "472093001100" } });
    fireEvent.click(screen.getByRole("button", { name: "Link server" }));

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(/couldn't link the server/i);
    expect(alert).toHaveTextContent(/manage server permission/i);
    // Nothing was linked: the form is still there, no success toast.
    expect(screen.getByRole("button", { name: "Link server" })).toBeInTheDocument();
    expect(toast.success).not.toHaveBeenCalled();
  });

  it("omits the guild/voice IDs from a token-only save (#142)", async () => {
    const discordSaves: SaveDiscordSettingsRequest[] = [];
    renderScreen(mockBackend({ discordSaves }));

    const tokenInput = await botTokenField();
    fireEvent.change(tokenInput, { target: { value: "test-discord-token-zzzz" } });
    const botRow = await keyRow("Bot token");
    fireEvent.click(within(botRow).getByRole("button", { name: "Save" }));
    expect(await within(botRow).findByText("•••• zzzz")).toBeInTheDocument();

    expect(discordSaves).toHaveLength(1);
    expect(discordSaves[0].botToken).toBe("test-discord-token-zzzz");
    expect(discordSaves[0].guildId).toBeUndefined();
    expect(discordSaves[0].voiceChannelId).toBeUndefined();
  });

  it("links a pasted channel link's server with no network request until Link (#101)", async () => {
    const discordSaves: SaveDiscordSettingsRequest[] = [];
    const inviteCodes: string[] = [];
    renderScreen(mockBackend({ discordSaves, inviteCodes }));

    fireEvent.change(await screen.findByLabelText(/paste a discord link/i), {
      target: { value: "https://discord.com/channels/472093001100472093/987654321098765432" },
    });
    // Parsing issues NO RPC — no save, no invite resolve.
    expect(discordSaves).toHaveLength(0);
    expect(inviteCodes).toHaveLength(0);

    fireEvent.click(screen.getByRole("button", { name: "Link server" }));
    // The GUILD snowflake rides guild_id; the channel snowflake is not smuggled
    // into voice_channel_id.
    await waitFor(() => expect(discordSaves).toHaveLength(1));
    expect(discordSaves[0].guildId).toBe("472093001100472093");
    expect(discordSaves[0].voiceChannelId).toBeUndefined();
  });

  it("names the linked server after a reload, not its numeric id", async () => {
    const transport = mockBackend({
      linkName: "The Keep",
      inviteResolve: { guildId: "472093001100472093", guildName: "The Keep", voiceChannels: [] },
    });
    const { unmount } = renderScreen(transport);
    fireEvent.change(await screen.findByLabelText(/paste a discord link/i), { target: { value: "discord.gg/abc123" } });
    fireEvent.click(await screen.findByRole("button", { name: "Link The Keep" }));
    expect(await screen.findByTestId("linked-server")).toHaveTextContent("The Keep");
    expect(toast.success).toHaveBeenCalledWith("Linked The Keep");

    unmount();
    renderScreen(transport);
    const linked = await screen.findByTestId("linked-server");
    expect(linked).toHaveTextContent("The Keep");
    expect(screen.getByText("Server ID 472093001100472093")).toBeInTheDocument();
  });

  it("rejects a non-link paste with an inline hint and keeps Link off (#101)", async () => {
    renderScreen();
    fireEvent.change(await screen.findByLabelText(/paste a discord link/i), { target: { value: "not a discord link" } });
    expect(screen.getByText(/couldn't read that link/i)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Link server" })).toBeDisabled();
  });

  it("tolerates scheme/subdomain/trailing-slash/query variants (#101)", async () => {
    const discordSaves: SaveDiscordSettingsRequest[] = [];
    renderScreen(mockBackend({ discordSaves }));
    fireEvent.change(await screen.findByLabelText(/paste a discord link/i), {
      target: { value: "ptb.discord.com/channels/472093001100472093/987654321098765432/?jump=1" },
    });
    expect(screen.queryByText(/couldn't read that link/i)).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Link server" }));
    await waitFor(() => expect(discordSaves[0]?.guildId).toBe("472093001100472093"));
  });

  it("unlinks the linked server only after confirming in a dialog (#504)", async () => {
    const releaseCalls: string[] = [];
    renderScreen(mockBackend({ boundGuild: { guildId: "472093001100", guildName: "The Keep" }, releaseCalls }));

    expect(await screen.findByTestId("linked-server")).toHaveTextContent("The Keep");
    // No link form while a server is linked.
    expect(screen.queryByLabelText(/paste a discord link/i)).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Unlink server" }));
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog).toHaveTextContent("Unlink The Keep?");
    expect(releaseCalls).toHaveLength(0);

    fireEvent.click(within(dialog).getByRole("button", { name: "Unlink server" }));
    await waitFor(() => expect(releaseCalls).toEqual(["472093001100"]));
    expect(await screen.findByLabelText(/paste a discord link/i)).toBeInTheDocument();
    expect(screen.queryByTestId("linked-server")).not.toBeInTheDocument();
    expect(toast.success).toHaveBeenCalledWith("Unlinked The Keep");
  });

  it("changes the linked server through the form, with a way back", async () => {
    renderScreen(mockBackend({ boundGuild: { guildId: "472093001100", guildName: "The Keep" } }));
    fireEvent.click(await screen.findByRole("button", { name: "Change" }));
    expect(screen.getByLabelText(/paste a discord link/i)).toBeInTheDocument();
    // Re-entering the server already linked is no change.
    fireEvent.change(await serverIdField(), { target: { value: "472093001100" } });
    expect(screen.getByRole("button", { name: "Link server" })).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: /cancel/i }));
    expect(await screen.findByTestId("linked-server")).toHaveTextContent("The Keep");
  });

  it("offers no unlink control while no server is linked (#504)", async () => {
    renderScreen();
    await screen.findByLabelText(/paste a discord link/i);
    expect(screen.queryByRole("button", { name: /unlink server/i })).not.toBeInTheDocument();
  });

  it("reports a failed release (#504)", async () => {
    renderScreen(
      mockBackend({
        boundGuild: { guildId: "472093001100" },
        releaseError: "that Discord server is not the one currently linked; reload and try again",
      }),
    );
    fireEvent.click(await screen.findByRole("button", { name: "Unlink server" }));
    fireEvent.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Unlink server" }));
    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith(expect.stringMatching(/not the one currently linked/i)),
    );
    expect(screen.getByTestId("linked-server")).toBeInTheDocument();
  });
});

describe("Configuration invite autofill (#105)", () => {
  it("resolves a pasted invite, sending the bare code, and offers to link it by name", async () => {
    const inviteCodes: string[] = [];
    renderScreen(
      mockBackend({
        inviteCodes,
        inviteResolve: {
          guildId: "111",
          guildName: "The Keep",
          voiceChannels: [
            { id: "900", name: "War Room" },
            { id: "901", name: "Tavern" },
          ],
        },
      }),
    );

    fireEvent.change(await screen.findByLabelText(/paste a discord link/i), {
      target: { value: "https://discord.gg/abc123" },
    });
    await waitFor(() => expect(inviteCodes).toEqual(["abc123"]));
    expect(await screen.findByTestId("resolved-guild")).toHaveTextContent("Found The Keep.");
    // No auto-save: linking is an explicit press, named for the server.
    expect(screen.getByRole("button", { name: "Link The Keep" })).toBeEnabled();
    // The returned voice channels are NOT rendered.
    expect(screen.queryByRole("button", { name: /War Room/ })).not.toBeInTheDocument();
  });

  it("links the resolved invite's server on Link", async () => {
    const discordSaves: SaveDiscordSettingsRequest[] = [];
    renderScreen(
      mockBackend({
        discordSaves,
        inviteResolve: {
          guildId: "472093001100472093",
          guildName: "The Keep",
          voiceChannels: [{ id: "987654321098765432", name: "War Room" }],
        },
      }),
    );
    fireEvent.change(await screen.findByLabelText(/paste a discord link/i), { target: { value: "discord.gg/abc123" } });
    fireEvent.click(await screen.findByRole("button", { name: "Link The Keep" }));
    await waitFor(() => expect(discordSaves).toHaveLength(1));
    expect(discordSaves[0].guildId).toBe("472093001100472093");
    expect(discordSaves[0].voiceChannelId).toBeUndefined();
  });

  it("renders a precondition failure verbatim with an add-bot hint", async () => {
    renderScreen(
      mockBackend({
        inviteError: { code: Code.FailedPrecondition, message: "the Bot is not a member of that server" },
      }),
    );
    fireEvent.change(await screen.findByLabelText(/paste a discord link/i), { target: { value: "discord.gg/abc123" } });

    expect(await screen.findByText("the Bot is not a member of that server")).toBeInTheDocument();
    // …plus the hint pointing back at step 1, where the Add-Glyphoxa action is.
    expect(screen.getByText(/in step 1/i)).toBeInTheDocument();
    expect(screen.getByText(/then paste the invite again/i)).toBeInTheDocument();
    expect(screen.queryByTestId("resolved-guild")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Link server" })).toBeDisabled();
  });

  it("renders a DISTINCT precondition message verbatim without the add-bot hint (no-token FP)", async () => {
    renderScreen(
      mockBackend({
        inviteError: { code: Code.FailedPrecondition, message: "save the Discord bot token first" },
      }),
    );
    fireEvent.change(await screen.findByLabelText(/paste a discord link/i), { target: { value: "discord.gg/abc123" } });
    expect(await screen.findByText("save the Discord bot token first")).toBeInTheDocument();
    expect(screen.queryByText(/then paste the invite again/i)).not.toBeInTheDocument();
  });

  it("debounces the invite resolve so typing fires a single resolve, not one per keystroke", async () => {
    const inviteCodes: string[] = [];
    renderScreen(mockBackend({ inviteCodes, inviteResolve: { guildId: "111", guildName: "The Keep", voiceChannels: [] } }));

    const paste = await screen.findByLabelText(/paste a discord link/i);
    fireEvent.change(paste, { target: { value: "discord.gg/ab" } });
    fireEvent.change(paste, { target: { value: "discord.gg/abc" } });
    fireEvent.change(paste, { target: { value: "discord.gg/abcd" } });
    fireEvent.change(paste, { target: { value: "discord.gg/abc123" } });

    await waitFor(() => expect(inviteCodes).toEqual(["abc123"]));
    await new Promise((r) => setTimeout(r, 500));
    expect(inviteCodes).toEqual(["abc123"]);
  });

  it("ignores a late resolve for a superseded invite (latest-wins)", async () => {
    type Resolved = { guildId: string; guildName: string; voiceChannels: { id: string; name: string }[] };
    let releaseA: (v: Resolved) => void = () => {};
    const gateA = new Promise<Resolved>((res) => {
      releaseA = res;
    });
    const discordSaves: SaveDiscordSettingsRequest[] = [];
    const inviteResolver = (code: string) =>
      code === "slowaaa" ? gateA : Promise.resolve({ guildId: "222", guildName: "Guild B", voiceChannels: [] });
    renderScreen(mockBackend({ inviteResolver, discordSaves }));

    const paste = await screen.findByLabelText(/paste a discord link/i);
    fireEvent.change(paste, { target: { value: "discord.gg/slowaaa" } });
    await new Promise((r) => setTimeout(r, 500));
    fireEvent.change(paste, { target: { value: "discord.gg/fastbbb" } });
    expect(await screen.findByTestId("resolved-guild")).toHaveTextContent("Found Guild B.");

    // A's slow resolve lands late — the guard drops it.
    releaseA({ guildId: "111", guildName: "Guild A", voiceChannels: [] });
    await new Promise((r) => setTimeout(r, 50));
    expect(screen.getByTestId("resolved-guild")).toHaveTextContent("Found Guild B.");
    expect(screen.queryByText(/Guild A/)).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Link Guild B" }));
    await waitFor(() => expect(discordSaves[0]?.guildId).toBe("222"));
  });

  it("surfaces an invalid/expired invite inline and keeps Link off", async () => {
    renderScreen(mockBackend({ inviteError: { code: Code.NotFound, message: "invalid or expired invite" } }));
    fireEvent.change(await screen.findByLabelText(/paste a discord link/i), { target: { value: "discord.gg/gone" } });
    expect(await screen.findByText(/invalid or expired/i)).toBeInTheDocument();
    expect(screen.queryByTestId("resolved-guild")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Link server" })).toBeDisabled();
  });

  it("offers the link even when the resolved server has no voice channels", async () => {
    renderScreen(mockBackend({ inviteResolve: { guildId: "111", guildName: "The Keep", voiceChannels: [] } }));
    fireEvent.change(await screen.findByLabelText(/paste a discord link/i), { target: { value: "discord.gg/abc123" } });
    expect(await screen.findByTestId("resolved-guild")).toHaveTextContent("Found The Keep.");
    expect(screen.getByRole("button", { name: "Link The Keep" })).toBeEnabled();
  });
});

describe("Configuration model entry (#227)", () => {
  it("saves a free-text model without re-pasting the key (model-only save)", async () => {
    const providerSaves: Array<{ provider: string; secret: string; model: string }> = [];
    renderScreen(mockBackend({ saved: { groq: "eeee" }, providerSaves }));

    fireEvent.click(await screen.findByRole("button", { name: "Groq model" }));
    fireEvent.change(screen.getByPlaceholderText(/search or type/i), { target: { value: "my-custom-model" } });
    fireEvent.click(screen.getByRole("option", { name: /use "my-custom-model"/i }));

    fireEvent.click(await screen.findByRole("button", { name: "Save model" }));
    await screen.findByRole("button", { name: "Groq model" }); // settle
    expect(providerSaves).toContainEqual({ provider: "groq", secret: "", model: "my-custom-model" });
    // The key was never touched: the row still shows the masked saved state.
    expect(screen.getByText("•••• eeee")).toBeInTheDocument();
  });

  it("keeps free-text model entry usable when the catalog fetch fails", async () => {
    const providerSaves: Array<{ provider: string; secret: string; model: string }> = [];
    renderScreen(mockBackend({ saved: { groq: "eeee" }, providerSaves, listModelsError: "catalog down" }));

    fireEvent.click(await screen.findByRole("button", { name: "Groq model" }));
    fireEvent.change(screen.getByPlaceholderText(/search or type/i), { target: { value: "llama-fallback" } });
    fireEvent.click(screen.getByRole("option", { name: /use "llama-fallback"/i }));
    fireEvent.click(await screen.findByRole("button", { name: "Save model" }));
    await screen.findByRole("button", { name: "Groq model" });
    expect(providerSaves).toContainEqual({ provider: "groq", secret: "", model: "llama-fallback" });
  });

  it("shows no Save model button for the passive catalog-default display", async () => {
    renderScreen(mockBackend({ saved: { groq: "eeee" } }));
    await screen.findByRole("button", { name: "Groq model" });
    expect(screen.queryByRole("button", { name: "Save model" })).not.toBeInTheDocument();
  });
});

describe("Configuration spending limits (#130)", () => {
  const open = async () => fireEvent.click(await screen.findByRole("button", { name: /spending limits/i }));

  it("states the current limits on the closed card, labelled by what they do", async () => {
    renderScreen(mockBackend({ spendCaps: { softUsd: 5, hardUsd: 10 } }));
    expect(await screen.findByText("Pause at $5.00 · end at $10.00")).toBeInTheDocument();
    await open();
    expect(await screen.findByLabelText("Pause new AI replies at (USD)")).toBeInTheDocument();
    expect(screen.getByLabelText("End the session at (USD)")).toBeInTheDocument();
  });

  it("says when no limit is set", async () => {
    renderScreen();
    expect(await screen.findByText("No limits set")).toBeInTheDocument();
  });

  it("seeds the inputs, keeps Save off until something changed, saves and toasts", async () => {
    const spendSaves: SetSpendCapsRequest[] = [];
    renderScreen(mockBackend({ spendCaps: { softUsd: 5, hardUsd: 10 }, spendSaves }));
    await open();

    const soft = (await screen.findByLabelText("Pause new AI replies at (USD)")) as HTMLInputElement;
    const hard = screen.getByLabelText("End the session at (USD)") as HTMLInputElement;
    await waitFor(() => expect(soft.value).toBe("5"));
    expect(hard.value).toBe("10");
    const save = screen.getByRole("button", { name: /save spending limits/i });
    expect(save).toBeDisabled();

    // Raise the pause amount and clear the end amount.
    fireEvent.change(soft, { target: { value: "7" } });
    fireEvent.change(hard, { target: { value: "" } });
    expect(save).toBeEnabled();
    fireEvent.click(save);

    await waitFor(() => expect(spendSaves).toHaveLength(1));
    // A blank field is omitted (undefined) so the server clears it; 7 is sent.
    expect(spendSaves[0].softUsd).toBe(7);
    expect(spendSaves[0].hardUsd).toBeUndefined();
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith("Spending limits saved: Pause at $7.00"));
    expect(await screen.findByText("Pause at $7.00")).toBeInTheDocument();
  });

  it("rejects an end amount below the pause amount inline, before the server", async () => {
    const spendSaves: SetSpendCapsRequest[] = [];
    renderScreen(mockBackend({ spendSaves }));
    await open();
    fireEvent.change(await screen.findByLabelText("Pause new AI replies at (USD)"), { target: { value: "10" } });
    fireEvent.change(screen.getByLabelText("End the session at (USD)"), { target: { value: "5" } });

    expect(screen.getByText(/must be at least the amount where replies pause/i)).toBeInTheDocument();
    expect(screen.getByLabelText("End the session at (USD)")).toHaveAttribute("aria-invalid", "true");
    expect(screen.getByRole("button", { name: /save spending limits/i })).toBeDisabled();
    expect(spendSaves).toHaveLength(0);
  });

  it("rejects a negative amount inline", async () => {
    renderScreen();
    await open();
    fireEvent.change(await screen.findByLabelText("Pause new AI replies at (USD)"), { target: { value: "-1" } });
    expect(screen.getByText(/enter an amount of 0 or more/i)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /save spending limits/i })).toBeDisabled();
  });
});

describe("Configuration first-run (#267)", () => {
  // A fresh, unseeded install: GetActiveCampaign is CodeNotFound until a campaign
  // is created here. CreateCampaign mints it, SetActiveCampaign selects it, and a
  // fresh GetActiveCampaign then resolves it — the create-then-activate flow.
  function firstRunBackend(
    opts: {
      activeErrorCode?: Code;
      calls?: Record<string, number>;
    } = {},
  ) {
    const { activeErrorCode } = opts;
    const bump = (m: string) => {
      if (opts.calls) opts.calls[m] = (opts.calls[m] ?? 0) + 1;
    };
    const state: { campaign?: { id: string; name: string; system: string; language: string } } = {};
    return createRouterTransport(({ service }) => {
      service(VoiceService, {
        getProviderHealth: () => create(GetProviderHealthResponseSchema, { providers: [] }),
        listModels: () => create(ListModelsResponseSchema, { models: [] }),
      });
      service(AuthService, { getCurrentUser: currentUser });
      service(ProviderService, {
        getUsageSummary: usageSummary,
        listProviderConfigs: () =>
          create(ListProviderConfigsResponseSchema, {
            credentials: [],
            guildId: "",
            voiceChannelId: "",
            discordApplicationId: "",
          }),
        getSpendCaps: () => create(GetSpendCapsResponseSchema, { caps: create(SpendCapsSchema, {}) }),
      });
      service(CampaignService, {
        getActiveCampaign: () => {
          // A non-NotFound failure (server down) is a real error, NOT first-run.
          if (activeErrorCode !== undefined) throw new ConnectError("boom", activeErrorCode);
          if (!state.campaign) throw new ConnectError("no campaign", Code.NotFound);
          return create(GetActiveCampaignResponseSchema, {
            campaign: create(CampaignSchema, state.campaign),
          });
        },
        createCampaign: (req) => {
          bump("createCampaign");
          state.campaign = { id: "c-new", name: req.name, system: req.system, language: req.language };
          return create(CreateCampaignResponseSchema, {
            campaign: create(CampaignSchema, state.campaign),
          });
        },
        setActiveCampaign: () => {
          bump("setActiveCampaign");
          return create(SetActiveCampaignResponseSchema, {
            campaign: create(CampaignSchema, state.campaign!),
          });
        },
      });
    });
  }

  it("makes step 1 the create-first-campaign form", async () => {
    renderScreen(firstRunBackend());

    expect(await screen.findByText(/create your first campaign/i)).toBeInTheDocument();
    expect((screen.getByLabelText("Game system") as HTMLInputElement).value).toBe("D&D 5e");
    expect(screen.getByRole("combobox", { name: /spoken language/i })).toHaveTextContent("English (en)");
    // The form sits inside step 1 of the checklist.
    const form = screen.getByLabelText("Game system").closest("#setup-campaign");
    expect(form).not.toBeNull();
  });

  it("defaults the new campaign's spoken language to the German display language and sends it", async () => {
    localStorage.setItem("gx-lang", "de");
    try {
      const created: Array<{ system: string; language: string }> = [];
      const transport = createRouterTransport(({ service }) => {
        service(VoiceService, {
          getProviderHealth: () => create(GetProviderHealthResponseSchema, { providers: [] }),
          listModels: () => create(ListModelsResponseSchema, { models: [] }),
        });
        service(AuthService, { getCurrentUser: currentUser });
        service(ProviderService, {
          getUsageSummary: usageSummary,
          listProviderConfigs: () =>
            create(ListProviderConfigsResponseSchema, {
              credentials: [],
              guildId: "",
              voiceChannelId: "",
              discordApplicationId: "",
            }),
          getSpendCaps: () => create(GetSpendCapsResponseSchema, { caps: create(SpendCapsSchema, {}) }),
        });
        service(CampaignService, {
          getActiveCampaign: () => {
            throw new ConnectError("no campaign", Code.NotFound);
          },
          listSupportedLanguages: () => create(ListSupportedLanguagesResponseSchema, { languages: ["de", "en"] }),
          createCampaign: (req) => {
            created.push({ system: req.system, language: req.language });
            throw new ConnectError("stop here", Code.Internal);
          },
        });
      });
      renderScreen(transport);
      await waitFor(() =>
        expect(screen.getByRole("combobox", { name: /gesprochene sprache/i })).toHaveTextContent("Deutsch (de)"),
      );
      fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Fluch" } });
      fireEvent.click(screen.getByRole("button", { name: /^kampagne erstellen$/i }));
      await waitFor(() => expect(created).toEqual([{ system: "D&D 5e", language: "de" }]));
    } finally {
      localStorage.clear();
    }
  });

  it("creates the first campaign from step 1 and ticks it off", async () => {
    renderScreen(firstRunBackend());

    fireEvent.change(await screen.findByLabelText("Name"), { target: { value: "The Sunless Citadel" } });
    fireEvent.click(screen.getByRole("button", { name: /^create campaign$/i }));

    expect(await screen.findByText("The Sunless Citadel")).toBeInTheDocument();
    expect(screen.queryByText(/create your first campaign/i)).not.toBeInTheDocument();
  });

  it("a double-click on Create fires exactly one CreateCampaign (no duplicate)", async () => {
    const calls: Record<string, number> = {};
    renderScreen(firstRunBackend({ calls }));

    fireEvent.change(await screen.findByLabelText("Name"), { target: { value: "Once Only" } });
    const button = screen.getByRole("button", { name: /^create campaign$/i });
    fireEvent.click(button);
    fireEvent.click(button);

    expect(await screen.findByText("Once Only")).toBeInTheDocument();
    expect(calls.createCampaign).toBe(1);
    expect(calls.setActiveCampaign).toBe(1);
  });

  it("shows an error with Retry (not the create form) for a non-NotFound campaign failure", async () => {
    // Only CodeNotFound means "no campaign yet". A real backend failure must
    // not lure a table that has campaigns into creating a duplicate.
    renderScreen(firstRunBackend({ activeErrorCode: Code.Internal }));

    expect(await screen.findByText(/couldn.t check your campaigns/i)).toBeInTheDocument();
    expect(screen.queryByText(/create your first campaign/i)).not.toBeInTheDocument();
  });
});
