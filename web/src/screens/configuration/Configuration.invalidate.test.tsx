import { describe, it, expect } from "vitest";
import { render, screen, within, fireEvent, waitFor } from "@testing-library/react";
import { createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";
import { useQuery } from "@connectrpc/connect-query";

import {
  AuthService,
  CampaignService,
  ProviderService,
  SessionService,
  VoiceService,
  HealthStatus,
  CampaignSchema,
  GetActiveCampaignResponseSchema,
  GetProviderHealthResponseSchema,
  GetSpendCapsResponseSchema,
  ListModelsResponseSchema,
  ListProviderConfigsResponseSchema,
  ListSessionVoiceChannelsResponseSchema,
  ListVoicesResponseSchema,
  ProviderCredentialSchema,
  ProviderHealthSchema,
  SaveDiscordSettingsResponseSchema,
  SaveProviderConfigResponseSchema,
  SpendCapsSchema,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { Configuration } from "./Configuration";
import { currentUser, usageSummary } from "./testRows";

// W8: saving a key changes what the vendor-backed reads return — provider
// health, the model catalog, the voice catalog, the guild's voice channels —
// but only ListProviderConfigs was invalidated. The badge kept its pre-save
// verdict and the Cast screen kept an empty voice list until a reload.
function backend() {
  const calls = { health: 0, models: 0, voices: 0, channels: 0 };
  let groq = "";
  const cred = (component: string, provider: string, last4 = "") =>
    create(ProviderCredentialSchema, {
      component,
      provider,
      everSaved: last4 !== "",
      showMasked: last4 !== "",
      last4,
      inEffect: component === "llm" && provider === "groq",
    });
  const transport = createRouterTransport(({ service }) => {
    service(VoiceService, {
      getProviderHealth: () => {
        calls.health++;
        return create(GetProviderHealthResponseSchema, {
          // The live probe only reports a provider once its key exists.
          providers: groq
            ? [
                create(ProviderHealthSchema, {
                  provider: "groq",
                  status: HealthStatus.DEGRADED,
                  detail: "the key was rejected",
                }),
              ]
            : [],
        });
      },
      listModels: () => {
        calls.models++;
        return create(ListModelsResponseSchema, { models: ["m1"] });
      },
      listVoices: () => {
        calls.voices++;
        return create(ListVoicesResponseSchema, { voices: [] });
      },
    });
    service(SessionService, {
      listSessionVoiceChannels: () => {
        calls.channels++;
        return create(ListSessionVoiceChannelsResponseSchema, { channels: [] });
      },
    });
    service(CampaignService, {
      getActiveCampaign: () =>
        create(GetActiveCampaignResponseSchema, {
          campaign: create(CampaignSchema, { id: "c1", name: "The Sunless Citadel" }),
        }),
    });
    service(AuthService, { getCurrentUser: currentUser });
    service(ProviderService, {
      getUsageSummary: usageSummary,
      listProviderConfigs: () =>
        create(ListProviderConfigsResponseSchema, {
          credentials: [
            cred("discord", "discord"),
            cred("llm", "groq", groq),
            cred("tts", "elevenlabs"),
            cred("image", "gemini"),
          ],
        }),
      saveProviderConfig: (req) => {
        groq = req.secret.slice(-4);
        return create(SaveProviderConfigResponseSchema, { credential: cred("llm", "groq", groq) });
      },
      saveDiscordSettings: () =>
        create(SaveDiscordSettingsResponseSchema, { credential: cred("discord", "discord", "abcd") }),
      getSpendCaps: () => create(GetSpendCapsResponseSchema, { caps: create(SpendCapsSchema, {}) }),
    });
  });
  return { transport, calls };
}

// Stand-ins for the OTHER screens' observers of the same cache entries (the
// Cast editor's voice list, the Session screen's channel picker).
function OtherScreens() {
  useQuery(VoiceService.method.listVoices, {});
  useQuery(SessionService.method.listSessionVoiceChannels, {}, { retry: false });
  return null;
}

function mount() {
  const h = backend();
  render(
    <Providers transport={h.transport} queryClient={makeQueryClient()}>
      <Configuration />
      <OtherScreens />
    </Providers>,
  );
  return h;
}

describe("Configuration saves refresh the reads a key changes (W8)", () => {
  it("re-probes health and re-reads the model and voice catalogs after a provider key save", async () => {
    const { calls } = mount();
    const input = await screen.findByLabelText("Groq key");
    // Two model catalogs: one per voice-LLM slot (Groq, Anthropic; #702).
    await waitFor(() => expect(calls).toMatchObject({ health: 1, models: 2, voices: 1 }));

    fireEvent.change(input, { target: { value: "test-groq-secret-eeee" } });
    const row = input.closest(".gx-provider-card") as HTMLElement;
    fireEvent.click(within(row).getByRole("button", { name: "Save" }));

    await waitFor(() => expect(calls).toMatchObject({ health: 2, models: 4, voices: 2 }));
    // The row now shows the LIVE verdict for the new key, not key-presence.
    expect(await within(row).findByText("the key was rejected")).toBeInTheDocument();
  });

  it("re-reads the guild's voice channels after a Discord save", async () => {
    const { calls } = mount();
    // The own-bot token is folded away: only a table running its own bot needs it.
    fireEvent.click(await screen.findByRole("button", { name: /use your own discord bot/i }));
    const token = await screen.findByLabelText(/bot token/i);
    await waitFor(() => expect(calls.channels).toBe(1));

    fireEvent.change(token, { target: { value: "test-discord-token-abcd" } });
    const row = token.closest(".gx-provider-card") as HTMLElement;
    fireEvent.click(within(row).getByRole("button", { name: "Save" }));

    await waitFor(() => expect(calls.channels).toBe(2));
    expect(calls.health).toBeGreaterThanOrEqual(2);
  });
});
