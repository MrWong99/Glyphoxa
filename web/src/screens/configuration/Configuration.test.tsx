import { describe, it, expect } from "vitest";
import { render, screen, within, fireEvent, waitFor } from "@testing-library/react";
import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";

import {
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
  GetProviderHealthResponseSchema,
  ProviderHealthSchema,
  ListModelsResponseSchema,
  ResolveGuildInviteResponseSchema,
  GetSpendCapsResponseSchema,
  SetSpendCapsResponseSchema,
  SpendCapsSchema,
  type ProviderHealth,
  type SaveDiscordSettingsRequest,
  type SetSpendCapsRequest,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { Configuration } from "./Configuration";

const CAMPAIGN = {
  id: "11111111-1111-1111-1111-111111111111",
  tenantId: "22222222-2222-2222-2222-222222222222",
  name: "The Sunless Citadel",
  system: "D&D 5e",
  language: "en",
};

// cred builds a ProviderCredential the List RPC returns. saved => masked + last4.
function cred(component: string, provider: string, last4?: string, model = "") {
  return create(ProviderCredentialSchema, {
    component,
    provider,
    everSaved: Boolean(last4),
    showMasked: Boolean(last4),
    last4: last4 ?? "",
    model,
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
    saved?: Partial<Record<"groq" | "elevenlabs" | "discord", string>>;
    health?: ProviderHealth[];
    // discordSaves captures every SaveDiscordSettings request so tests can pin
    // the wire shape (#142: a token-only save must omit the ID fields).
    discordSaves?: SaveDiscordSettingsRequest[];
    // discordSaveError makes SaveDiscordSettings fail (simulates a server-side
    // failure) so tests can pin that the screen surfaces the rejection.
    discordSaveError?: string;
    // providerSaves captures every SaveProviderConfig request so tests can pin
    // the model-only wire shape (#227: secret "" + model).
    providerSaves?: Array<{ provider: string; secret: string; model: string }>;
    // listModelsError makes the catalog fetch fail at the transport, pinning
    // that free-text model entry survives a dead catalog (#227).
    listModelsError?: string;
    // discordApplicationId seeds the server-provided Discord application id the
    // read echoes so the screen composes the bot-authorization URL (#110).
    discordApplicationId?: string;
    // inviteResolve seeds the guild + voice channels ResolveGuildInvite returns
    // for a pasted invite (#105). inviteError makes it fail instead.
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
    discord: opts.saved?.discord,
    guildId: "",
    voiceChannelId: "",
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
    service(ProviderService, {
      listProviderConfigs: () =>
        create(ListProviderConfigsResponseSchema, {
          credentials: [
            cred("discord", "discord", state.discord),
            cred("llm", "groq", state.groq, state.groqModel),
            cred("tts", "elevenlabs", state.elevenlabs),
          ],
          guildId: state.guildId,
          voiceChannelId: state.voiceChannelId,
          discordApplicationId: opts.discordApplicationId ?? "",
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
        return create(SaveProviderConfigResponseSchema, {
          credential: cred(req.provider === "groq" ? "llm" : "tts", req.provider, last4, req.model),
        });
      },
      saveDiscordSettings: (req) => {
        opts.discordSaves?.push(req);
        if (opts.discordSaveError) throw new ConnectError(opts.discordSaveError, Code.Internal);
        // Presence semantics mirror the real server (#142): omitted IDs leave
        // the stored ones untouched, and present-but-empty is REJECTED exactly
        // like internal/rpc/provider.go — so a client that regresses into
        // sending blank IDs fails loudly here instead of silently diverging.
        const hasIDs = req.guildId !== undefined || req.voiceChannelId !== undefined;
        if (hasIDs && (!req.guildId || !req.voiceChannelId)) {
          throw new ConnectError(
            "guild_id and voice_channel_id must both be non-empty when provided",
            Code.InvalidArgument,
          );
        }
        if (req.botToken !== undefined) state.discord = req.botToken.slice(-4);
        if (req.guildId !== undefined) state.guildId = req.guildId;
        if (req.voiceChannelId !== undefined) state.voiceChannelId = req.voiceChannelId;
        return create(SaveDiscordSettingsResponseSchema, {
          credential: cred("discord", "discord", state.discord),
          guildId: state.guildId,
          voiceChannelId: state.voiceChannelId,
        });
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

function renderScreen(transport = mockBackend()) {
  return render(
    <Providers transport={transport} queryClient={makeQueryClient()}>
      <Configuration />
    </Providers>,
  );
}

describe("Configuration", () => {
  it("renders the active campaign from the RPC", async () => {
    renderScreen();
    expect(await screen.findByText(CAMPAIGN.name)).toBeInTheDocument();
    expect(screen.getByText(CAMPAIGN.system)).toBeInTheDocument();
    expect(screen.getByText(CAMPAIGN.language)).toBeInTheDocument();
  });

  it("shows a Key-needed badge for each unsaved credential", async () => {
    renderScreen();
    // Three secret slots start unsaved → three Key-needed badges.
    expect(await screen.findAllByText(/key needed/i)).toHaveLength(3);
    expect(screen.queryByText(/healthy/i)).not.toBeInTheDocument();
  });

  it("saves a provider key write-only: the row masks and the badge turns Healthy", async () => {
    renderScreen();

    const groqInput = await screen.findByLabelText("Groq key");
    fireEvent.change(groqInput, { target: { value: "test-groq-secret-eeee" } });

    // The Save button in the Groq row.
    const groqRow = groqInput.closest(".gx-provider-row") as HTMLElement;
    fireEvent.click(within(groqRow).getByRole("button", { name: "Save" }));

    // After the invalidated list refetches, the row shows a masked value +
    // Replace and a Healthy badge — derived from key-presence, never the secret.
    expect(await within(groqRow).findByText("••••••••")).toBeInTheDocument();
    expect(within(groqRow).getByRole("button", { name: /replace/i })).toBeInTheDocument();
    expect(within(groqRow).getByText(/healthy/i)).toBeInTheDocument();
    // The plaintext key never appears in the DOM.
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
      service(ProviderService, {
        listProviderConfigs: () =>
          create(ListProviderConfigsResponseSchema, {
            credentials: [cred("discord", "discord"), cred("llm", "groq"), cred("tts", "elevenlabs")],
            guildId: "",
            voiceChannelId: "",
          }),
        saveProviderConfig: () => {
          throw new Error("secret sealing unavailable");
        },
      });
    });
    renderScreen(transport);

    const groqInput = await screen.findByLabelText("Groq key");
    fireEvent.change(groqInput, { target: { value: "sk-will-fail" } });
    const groqRow = groqInput.closest(".gx-provider-row") as HTMLElement;
    fireEvent.click(within(groqRow).getByRole("button", { name: "Save" }));

    // The failure renders in the row…
    const alert = await within(groqRow).findByRole("alert");
    expect(alert).toHaveTextContent(/couldn't save/i);
    // …and the key field stays editable for a retry, badge still unsaved.
    expect(within(groqRow).getByLabelText("Groq key")).toBeInTheDocument();
    expect(within(groqRow).getByText(/key needed/i)).toBeInTheDocument();
  });

  it("clears the save error when the Replace edit is cancelled", async () => {
    // Replace on a SAVED key → failing save → Cancel returns the row to the
    // masked+Healthy state; a stale "Couldn't save" alert must not linger there.
    const transport = createRouterTransport(({ service }) => {
      service(VoiceService, {
        getProviderHealth: () => create(GetProviderHealthResponseSchema, { providers: [] }),
        listModels: () => create(ListModelsResponseSchema, { models: GROQ_MODELS }),
      });
      service(CampaignService, {
        getActiveCampaign: () =>
          create(GetActiveCampaignResponseSchema, { campaign: create(CampaignSchema, CAMPAIGN) }),
      });
      service(ProviderService, {
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

    // The Groq key is saved → masked with a Replace affordance.
    const mask = await screen.findByLabelText("Groq saved");
    const groqRow = mask.closest(".gx-provider-row") as HTMLElement;
    fireEvent.click(within(groqRow).getByRole("button", { name: /replace/i }));

    // Replacement attempt fails → alert renders.
    fireEvent.change(within(groqRow).getByLabelText("Groq key"), { target: { value: "sk-new" } });
    fireEvent.click(within(groqRow).getByRole("button", { name: "Save" }));
    expect(await within(groqRow).findByRole("alert")).toHaveTextContent(/couldn't save/i);

    // Cancel returns to the masked view — the stale alert goes with it.
    fireEvent.click(within(groqRow).getByRole("button", { name: /cancel/i }));
    expect(within(groqRow).getByLabelText("Groq saved")).toBeInTheDocument();
    expect(within(groqRow).queryByRole("alert")).not.toBeInTheDocument();
  });

  it("persists Guild ID and Voice channel ID", async () => {
    renderScreen();

    const guild = await screen.findByLabelText("Guild ID");
    const voice = screen.getByLabelText("Voice channel ID");
    fireEvent.change(guild, { target: { value: "472093001100" } });
    fireEvent.change(voice, { target: { value: "472093774421" } });
    fireEvent.click(screen.getByRole("button", { name: /save discord settings/i }));

    // Values survive the invalidation refetch (round-tripped through the RPC).
    expect(await screen.findByDisplayValue("472093001100")).toBeInTheDocument();
    expect(screen.getByDisplayValue("472093774421")).toBeInTheDocument();
  });

  it("omits the guild/voice IDs from a token-only save (#142)", async () => {
    const discordSaves: SaveDiscordSettingsRequest[] = [];
    renderScreen(mockBackend({ discordSaves }));

    // Operator only edits the bot token — the IDs inputs are untouched (still
    // seeding from the in-flight config load).
    const tokenInput = await screen.findByLabelText("Bot token key");
    fireEvent.change(tokenInput, { target: { value: "test-discord-token-zzzz" } });
    const botRow = tokenInput.closest(".gx-provider-row") as HTMLElement;
    fireEvent.click(within(botRow).getByRole("button", { name: "Save" }));
    expect(await within(botRow).findByText("••••••••")).toBeInTheDocument();

    // The request carries the token and NOTHING else: no guild/voice fields on
    // the wire, so a slow/failed config load can never clobber the stored IDs.
    expect(discordSaves).toHaveLength(1);
    expect(discordSaves[0].botToken).toBe("test-discord-token-zzzz");
    expect(discordSaves[0].guildId).toBeUndefined();
    expect(discordSaves[0].voiceChannelId).toBeUndefined();
  });

  it("disables the IDs save until both IDs are non-empty (#142)", async () => {
    renderScreen();

    // Fresh install: guild pasted, voice still blank. The server rejects
    // present-but-empty IDs, so the client must not offer the save at all —
    // a click here used to fail invisibly and leave nothing stored.
    const guild = await screen.findByLabelText("Guild ID");
    fireEvent.change(guild, { target: { value: "472093001100" } });
    const save = screen.getByRole("button", { name: /save discord settings/i });
    expect(save).toBeDisabled();

    // Both filled -> the save unlocks.
    fireEvent.change(screen.getByLabelText("Voice channel ID"), { target: { value: "472093774421" } });
    expect(save).toBeEnabled();
  });

  it("surfaces a failed IDs save as a visible alert (#142)", async () => {
    renderScreen(mockBackend({ discordSaveError: "database is down" }));

    // Both IDs filled, save offered — but the RPC fails. The rejection must
    // leave visible evidence: nothing was stored, and a silent failure here
    // resurfaces later as an unrelated-looking session-start precondition error.
    fireEvent.change(await screen.findByLabelText("Guild ID"), { target: { value: "472093001100" } });
    fireEvent.change(screen.getByLabelText("Voice channel ID"), { target: { value: "472093774421" } });
    fireEvent.click(screen.getByRole("button", { name: /save discord settings/i }));

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(/couldn't save/i);
    expect(alert).toHaveTextContent(/database is down/);
  });

  it("renders the Groq model combobox defaulting to the catalog's first model (ListModels)", async () => {
    renderScreen();
    // The combobox mounts before the catalog resolves (#227 renders it even for
    // an empty list), so await the default value, not just the control.
    const groqModelSelect = await screen.findByLabelText("Groq model");
    expect(await within(groqModelSelect).findByText("llama-3.3-70b-versatile")).toBeInTheDocument();
  });

  it("upgrades a saved provider's badge to Degraded from the health RPC", async () => {
    renderScreen(
      mockBackend({
        saved: { elevenlabs: "abcd" },
        health: [health("elevenlabs", HealthStatus.DEGRADED)],
      }),
    );
    // ElevenLabs is saved → renders presence-Healthy instantly, then the async
    // health RPC downgrades it to Degraded.
    const degraded = await screen.findByText(/degraded/i);
    const row = degraded.closest(".gx-provider-row") as HTMLElement;
    expect(within(row).getByText("ElevenLabs")).toBeInTheDocument();
  });

  it("shows the resolved Discord bot tag from the live login", async () => {
    renderScreen(
      mockBackend({
        saved: { discord: "tok9" },
        health: [health("discord", HealthStatus.HEALTHY, "Glyphoxa#4823")],
      }),
    );
    expect(await screen.findByText(/Connected as Glyphoxa#4823/)).toBeInTheDocument();
  });

  it("shows the Add-Glyphoxa-to-your-server link built from the server app id (#110)", async () => {
    renderScreen(mockBackend({ discordApplicationId: "987654321098765432" }));
    const link = await screen.findByRole("link", { name: /add glyphoxa to your server/i });
    expect(link).toHaveAttribute("href", expect.stringContaining("client_id=987654321098765432"));
    expect(link).toHaveAttribute("target", "_blank");
  });

  it("disables the Add-Glyphoxa action when no application id is configured (#110)", async () => {
    renderScreen(); // default mock: empty discordApplicationId
    expect(await screen.findByText(/no discord application id is configured/i)).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /add glyphoxa to your server/i })).not.toBeInTheDocument();
  });

  it("does not flash the missing-app-id note while the config query is loading (#110)", async () => {
    renderScreen(); // default mock resolves success with an empty id
    // Synchronously after render the list query is still pending — the note must
    // not flash (nor would it stick on a query error): only a resolved empty id
    // is the disabled fallback.
    expect(screen.queryByText(/no discord application id is configured/i)).not.toBeInTheDocument();
    // Once the read resolves with an empty id, the disabled action + note appear.
    expect(await screen.findByText(/no discord application id is configured/i)).toBeInTheDocument();
  });

  it("autofills both ID fields from a pasted channel link with no network request (#101)", async () => {
    const discordSaves: SaveDiscordSettingsRequest[] = [];
    renderScreen(mockBackend({ discordSaves }));

    const paste = await screen.findByLabelText(/paste a discord link/i);
    fireEvent.change(paste, {
      target: { value: "https://discord.com/channels/472093001100472093/987654321098765432" },
    });

    // Both snowflakes land in the (still editable) ID fields, purely client-side.
    expect((screen.getByLabelText("Guild ID") as HTMLInputElement).value).toBe("472093001100472093");
    expect((screen.getByLabelText("Voice channel ID") as HTMLInputElement).value).toBe(
      "987654321098765432",
    );
    // Parsing issues NO save RPC — the autofill is local until the operator Saves.
    expect(discordSaves).toHaveLength(0);
  });

  it("persists autofilled IDs through Save into the right wire fields and re-seeds them on reload (#101)", async () => {
    const discordSaves: SaveDiscordSettingsRequest[] = [];
    const transport = mockBackend({ discordSaves });
    const { unmount } = renderScreen(transport);

    fireEvent.change(await screen.findByLabelText(/paste a discord link/i), {
      target: { value: "discord.com/channels/472093001100472093/987654321098765432" },
    });
    // The autofill marks the fields dirty, so Save picks up the pasted values.
    fireEvent.click(screen.getByRole("button", { name: /save discord settings/i }));

    // Pin the exact snowflake in the exact wire field: a guild/voice SWAP would
    // still round-trip through the display, so a value-only check can't catch it.
    await waitFor(() => expect(discordSaves).toHaveLength(1));
    expect(discordSaves[0].guildId).toBe("472093001100472093");
    expect(discordSaves[0].voiceChannelId).toBe("987654321098765432");

    // Reload (AC2): a fresh mount against the SAME stateful backend re-seeds both
    // fields from what was persisted — the values survive the round-trip, and
    // each lands back in its OWN field (guild in Guild ID, channel in Voice).
    unmount();
    renderScreen(transport);
    await waitFor(() =>
      expect((screen.getByLabelText("Guild ID") as HTMLInputElement).value).toBe(
        "472093001100472093",
      ),
    );
    expect((screen.getByLabelText("Voice channel ID") as HTMLInputElement).value).toBe(
      "987654321098765432",
    );
  });

  it("rejects a non-link paste with an inline hint and leaves fields unchanged (#101)", async () => {
    renderScreen();

    // Operator already has a Guild ID typed in.
    const guild = await screen.findByLabelText("Guild ID");
    fireEvent.change(guild, { target: { value: "existing-guild" } });

    // A paste that is not a channel deep-link surfaces a hint…
    const paste = screen.getByLabelText(/paste a discord link/i);
    fireEvent.change(paste, { target: { value: "not a discord link" } });
    expect(screen.getByText(/couldn't read that link/i)).toBeInTheDocument();

    // …and does not clobber what the operator already had.
    expect((guild as HTMLInputElement).value).toBe("existing-guild");
    expect((screen.getByLabelText("Voice channel ID") as HTMLInputElement).value).toBe("");
  });

  it("tolerates scheme/subdomain/trailing-slash/query variants when autofilling (#101)", async () => {
    renderScreen();

    const paste = await screen.findByLabelText(/paste a discord link/i);
    fireEvent.change(paste, {
      target: { value: "ptb.discord.com/channels/472093001100472093/987654321098765432/?jump=1" },
    });

    expect((screen.getByLabelText("Guild ID") as HTMLInputElement).value).toBe("472093001100472093");
    expect((screen.getByLabelText("Voice channel ID") as HTMLInputElement).value).toBe(
      "987654321098765432",
    );
    // A rejected paste's hint must not linger after a successful one.
    expect(screen.queryByText(/couldn't read that link/i)).not.toBeInTheDocument();
  });
});

describe("Configuration invite picker (#105)", () => {
  it("resolves a pasted invite to the guild's voice channels, sending the bare code", async () => {
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

    const paste = await screen.findByLabelText(/paste a discord link/i);
    fireEvent.change(paste, { target: { value: "https://discord.gg/abc123" } });

    // The BARE code crosses the wire — the SPA extracted it, not the whole URL.
    await waitFor(() => expect(inviteCodes).toEqual(["abc123"]));

    // The guild header + exactly the returned voice channels (name + snowflake).
    expect(await screen.findByText("The Keep")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /War Room/ })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Tavern/ })).toBeInTheDocument();
    expect(screen.getByText("900")).toBeInTheDocument();
  });

  it("fills both ID fields from a picked channel and persists them on Save", async () => {
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

    fireEvent.change(await screen.findByLabelText(/paste a discord link/i), {
      target: { value: "discord.gg/abc123" },
    });
    // Pick the channel from the resolved picker.
    fireEvent.click(await screen.findByRole("button", { name: /War Room/ }));

    // Both fields fill from the RESOLVED guild + the PICKED channel.
    expect((screen.getByLabelText("Guild ID") as HTMLInputElement).value).toBe("472093001100472093");
    expect((screen.getByLabelText("Voice channel ID") as HTMLInputElement).value).toBe(
      "987654321098765432",
    );

    // Save carries BOTH exact snowflakes in their own wire fields (a swap would
    // still round-trip through the display, so pin the exact field).
    fireEvent.click(screen.getByRole("button", { name: /save discord settings/i }));
    await waitFor(() => expect(discordSaves).toHaveLength(1));
    expect(discordSaves[0].guildId).toBe("472093001100472093");
    expect(discordSaves[0].voiceChannelId).toBe("987654321098765432");
  });

  it("renders a precondition failure verbatim with an add-bot hint, leaving fields unchanged", async () => {
    renderScreen(
      mockBackend({
        inviteError: { code: Code.FailedPrecondition, message: "the Bot is not a member of that server" },
      }),
    );

    const guild = await screen.findByLabelText("Guild ID");
    fireEvent.change(guild, { target: { value: "existing-guild" } });

    fireEvent.change(screen.getByLabelText(/paste a discord link/i), {
      target: { value: "discord.gg/abc123" },
    });

    // The server message renders VERBATIM — no-token vs not-a-member share the
    // FailedPrecondition code and differ only by this message.
    expect(await screen.findByText("the Bot is not a member of that server")).toBeInTheDocument();
    // …plus the hint pointing back at the Add-Glyphoxa action, which renders at
    // the FOOT of the card (below the Save button) — the direction word must match.
    expect(screen.getByText(/at the foot of this card/i)).toBeInTheDocument();
    expect(screen.getByText(/then paste the invite again/i)).toBeInTheDocument();

    // A failed resolve touches nothing the operator already had.
    expect((guild as HTMLInputElement).value).toBe("existing-guild");
    expect((screen.getByLabelText("Voice channel ID") as HTMLInputElement).value).toBe("");
  });

  it("renders a DISTINCT precondition message verbatim without the add-bot hint (no-token FP)", async () => {
    // The no-token precondition shares FailedPrecondition with not-a-member but
    // differs by message. Using a message distinct from the production not-a-member
    // string pins that the SERVER text is rendered — a hardcoded client message
    // dies here — and that the add-bot hint (apt only for not-a-member) is absent.
    renderScreen(
      mockBackend({
        inviteError: { code: Code.FailedPrecondition, message: "save the Discord bot token first" },
      }),
    );

    fireEvent.change(await screen.findByLabelText(/paste a discord link/i), {
      target: { value: "discord.gg/abc123" },
    });

    expect(await screen.findByText("save the Discord bot token first")).toBeInTheDocument();
    // The add-bot hint would be wrong guidance here (the token, not the Bot, is
    // missing), so it must NOT append to this message.
    expect(screen.queryByText(/then paste the invite again/i)).not.toBeInTheDocument();
    expect(screen.queryByText(/at the foot of this card/i)).not.toBeInTheDocument();
  });

  it("debounces the invite resolve so typing fires a single resolve, not one per keystroke", async () => {
    const inviteCodes: string[] = [];
    renderScreen(
      mockBackend({
        inviteCodes,
        inviteResolve: { guildId: "111", guildName: "The Keep", voiceChannels: [] },
      }),
    );

    // Four change events in a burst (as if typing the code). Without a debounce
    // each fires an authed GET /invites — three of them 404s that burn Discord's
    // rate budget; with it, only the final value resolves, exactly once.
    const paste = await screen.findByLabelText(/paste a discord link/i);
    fireEvent.change(paste, { target: { value: "discord.gg/ab" } });
    fireEvent.change(paste, { target: { value: "discord.gg/abc" } });
    fireEvent.change(paste, { target: { value: "discord.gg/abcd" } });
    fireEvent.change(paste, { target: { value: "discord.gg/abc123" } });

    await waitFor(() => expect(inviteCodes).toEqual(["abc123"]));
    // Let any wrongly-scheduled extra resolve fire — none does.
    await new Promise((r) => setTimeout(r, 500));
    expect(inviteCodes).toEqual(["abc123"]);
  });

  it("ignores a late resolve for a superseded invite (latest-wins)", async () => {
    // Paste invite A (slow), then invite B (fast). B's picker renders; A's LATE
    // response must not swap it back — picking a channel then would fill the wrong
    // guild's snowflake and Save would persist it.
    let releaseA: (v: {
      guildId: string;
      guildName: string;
      voiceChannels: { id: string; name: string }[];
    }) => void = () => {};
    const gateA = new Promise<{
      guildId: string;
      guildName: string;
      voiceChannels: { id: string; name: string }[];
    }>((res) => {
      releaseA = res;
    });
    const inviteResolver = (code: string) =>
      code === "slowaaa"
        ? gateA
        : Promise.resolve({ guildId: "222", guildName: "Guild B", voiceChannels: [] });

    renderScreen(mockBackend({ inviteResolver }));

    const paste = await screen.findByLabelText(/paste a discord link/i);
    // A's debounce ticks and A's resolve goes in flight (gated open).
    fireEvent.change(paste, { target: { value: "discord.gg/slowaaa" } });
    await new Promise((r) => setTimeout(r, 500));

    // B supersedes A and resolves immediately → B's picker wins.
    fireEvent.change(paste, { target: { value: "discord.gg/fastbbb" } });
    expect(await screen.findByText("Guild B")).toBeInTheDocument();

    // A's slow resolve lands late — the guard drops it, the picker stays B.
    releaseA({ guildId: "111", guildName: "Guild A", voiceChannels: [] });
    await new Promise((r) => setTimeout(r, 50));
    expect(screen.getByText("Guild B")).toBeInTheDocument();
    expect(screen.queryByText("Guild A")).not.toBeInTheDocument();
  });

  it("surfaces an invalid/expired invite inline, leaving fields unchanged", async () => {
    renderScreen(
      mockBackend({
        inviteError: { code: Code.NotFound, message: "invalid or expired invite" },
      }),
    );

    const guild = await screen.findByLabelText("Guild ID");
    fireEvent.change(guild, { target: { value: "existing-guild" } });

    fireEvent.change(screen.getByLabelText(/paste a discord link/i), {
      target: { value: "discord.gg/gone" },
    });

    expect(await screen.findByText(/invalid or expired/i)).toBeInTheDocument();
    expect((guild as HTMLInputElement).value).toBe("existing-guild");
    expect((screen.getByLabelText("Voice channel ID") as HTMLInputElement).value).toBe("");
  });

  it("shows an empty-state line when the resolved guild has no voice channels", async () => {
    renderScreen(
      mockBackend({
        inviteResolve: { guildId: "111", guildName: "The Keep", voiceChannels: [] },
      }),
    );

    fireEvent.change(await screen.findByLabelText(/paste a discord link/i), {
      target: { value: "discord.gg/abc123" },
    });

    expect(await screen.findByText(/no voice channels in the keep/i)).toBeInTheDocument();
  });
});

describe("Configuration model entry (#227)", () => {
  it("saves a free-text model without re-pasting the key (model-only save)", async () => {
    const providerSaves: Array<{ provider: string; secret: string; model: string }> = [];
    renderScreen(mockBackend({ saved: { groq: "eeee" }, providerSaves }));

    // Open the Groq model combobox and type a model the catalog doesn't list.
    fireEvent.click(await screen.findByRole("button", { name: "Groq model" }));
    fireEvent.change(screen.getByPlaceholderText(/search or type/i), {
      target: { value: "my-custom-model" },
    });
    fireEvent.click(screen.getByRole("option", { name: /use "my-custom-model"/i }));

    // The dirty pick grows a Save model button; clicking it fires the
    // model-only mutation: empty secret, the typed model.
    fireEvent.click(await screen.findByRole("button", { name: "Save model" }));
    await screen.findByRole("button", { name: "Groq model" }); // settle
    expect(providerSaves).toContainEqual({ provider: "groq", secret: "", model: "my-custom-model" });
    // The key was never touched: the row still shows the masked saved state.
    expect(screen.getByText("••••••••")).toBeInTheDocument();
  });

  it("keeps free-text model entry usable when the catalog fetch fails", async () => {
    const providerSaves: Array<{ provider: string; secret: string; model: string }> = [];
    renderScreen(
      mockBackend({ saved: { groq: "eeee" }, providerSaves, listModelsError: "catalog down" }),
    );

    // The combobox renders despite the dead catalog (empty list)…
    fireEvent.click(await screen.findByRole("button", { name: "Groq model" }));
    fireEvent.change(screen.getByPlaceholderText(/search or type/i), {
      target: { value: "llama-fallback" },
    });
    // …and the typed id is still offered and saveable.
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

describe("Configuration spend caps (#130)", () => {
  it("seeds the inputs from stored caps and saves edits (blank field clears)", async () => {
    const spendSaves: SetSpendCapsRequest[] = [];
    renderScreen(mockBackend({ spendCaps: { softUsd: 5, hardUsd: 10 }, spendSaves }));

    const soft = (await screen.findByLabelText("Soft cap (USD)")) as HTMLInputElement;
    const hard = screen.getByLabelText("Hard cap (USD)") as HTMLInputElement;
    await waitFor(() => expect(soft.value).toBe("5"));
    expect(hard.value).toBe("10");

    // Raise the soft cap and clear the hard cap.
    fireEvent.change(soft, { target: { value: "7" } });
    fireEvent.change(hard, { target: { value: "" } });
    fireEvent.click(screen.getByRole("button", { name: /save spend caps/i }));

    await waitFor(() => expect(spendSaves).toHaveLength(1));
    // A blank field is omitted (undefined) so the server clears it; 7 is sent.
    expect(spendSaves[0].softUsd).toBe(7);
    expect(spendSaves[0].hardUsd).toBeUndefined();
  });

  it("surfaces a server rejection (hard < soft) inline", async () => {
    renderScreen(mockBackend({ spendSaves: [] }));

    fireEvent.change(await screen.findByLabelText("Soft cap (USD)"), { target: { value: "10" } });
    fireEvent.change(screen.getByLabelText("Hard cap (USD)"), { target: { value: "5" } });
    fireEvent.click(screen.getByRole("button", { name: /save spend caps/i }));

    expect(await screen.findByRole("alert")).toHaveTextContent(/couldn't save/i);
  });
});

describe("Configuration first-run (#267)", () => {
  // A fresh, unseeded install: GetActiveCampaign is CodeNotFound until a campaign
  // is created here. CreateCampaign mints it, SetActiveCampaign selects it, and a
  // fresh GetActiveCampaign then resolves it — the create-then-activate flow.
  function firstRunBackend(activeErrorCode?: Code) {
    const state: { campaign?: { id: string; name: string; system: string; language: string } } = {};
    return createRouterTransport(({ service }) => {
      service(VoiceService, {
        getProviderHealth: () => create(GetProviderHealthResponseSchema, { providers: [] }),
        listModels: () => create(ListModelsResponseSchema, { models: [] }),
      });
      service(ProviderService, {
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
          state.campaign = { id: "c-new", name: req.name, system: req.system, language: req.language };
          return create(CreateCampaignResponseSchema, {
            campaign: create(CampaignSchema, state.campaign),
          });
        },
        setActiveCampaign: () =>
          create(SetActiveCampaignResponseSchema, {
            campaign: create(CampaignSchema, state.campaign!),
          }),
      });
    });
  }

  it("replaces the CodeNotFound error card with a create-first-campaign CTA", async () => {
    renderScreen(firstRunBackend());

    // The create CTA — pre-filled with the seed defaults — stands in for the card.
    expect(await screen.findByText(/create your first campaign/i)).toBeInTheDocument();
    expect((screen.getByLabelText("System") as HTMLInputElement).value).toBe("dnd5e");
    expect((screen.getByLabelText("Language") as HTMLInputElement).value).toBe("en");
    // …and the old error card is gone.
    expect(screen.queryByText(/could not load the active campaign/i)).not.toBeInTheDocument();
  });

  it("creates the first campaign from the CTA and swaps in the live header", async () => {
    renderScreen(firstRunBackend());

    fireEvent.change(await screen.findByLabelText("Name"), {
      target: { value: "The Sunless Citadel" },
    });
    fireEvent.click(screen.getByRole("button", { name: /^create campaign$/i }));

    // After create → activate → sweep, GetActiveCampaign resolves and the header
    // replaces the CTA — no reload, no `glyphoxa seed`.
    expect(await screen.findByText("The Sunless Citadel")).toBeInTheDocument();
    expect(screen.queryByText(/create your first campaign/i)).not.toBeInTheDocument();
  });

  it("keeps the error card (not the CTA) for a non-NotFound getActiveCampaign failure", async () => {
    // Only CodeNotFound means "no campaign yet". A real backend failure (server
    // down) must still show the error card — otherwise an install that already has
    // a campaign would be lured into creating a duplicate.
    renderScreen(firstRunBackend(Code.Internal));

    expect(await screen.findByText(/could not load the active campaign/i)).toBeInTheDocument();
    expect(screen.queryByText(/create your first campaign/i)).not.toBeInTheDocument();
  });
});
