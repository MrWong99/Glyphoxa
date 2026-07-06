import { describe, it, expect } from "vitest";
import { render, screen, within, fireEvent } from "@testing-library/react";
import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";

import {
  CampaignService,
  ProviderService,
  VoiceService,
  HealthStatus,
  GetActiveCampaignResponseSchema,
  CampaignSchema,
  ListProviderConfigsResponseSchema,
  ProviderCredentialSchema,
  SaveProviderConfigResponseSchema,
  SaveDiscordSettingsResponseSchema,
  GetProviderHealthResponseSchema,
  ProviderHealthSchema,
  ListModelsResponseSchema,
  type ProviderHealth,
  type SaveDiscordSettingsRequest,
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
  } = {},
) {
  const state = {
    groqModel: "",
    groq: opts.saved?.groq,
    elevenlabs: opts.saved?.elevenlabs,
    discord: opts.saved?.discord,
    guildId: "",
    voiceChannelId: "",
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
