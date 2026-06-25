import { describe, it, expect } from "vitest";
import { render, screen, within, fireEvent } from "@testing-library/react";
import { createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";

import {
  CampaignService,
  ProviderService,
  GetActiveCampaignResponseSchema,
  CampaignSchema,
  ListProviderConfigsResponseSchema,
  ProviderCredentialSchema,
  SaveProviderConfigResponseSchema,
  SaveDiscordSettingsResponseSchema,
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
function cred(component: string, provider: string, last4?: string) {
  return create(ProviderCredentialSchema, {
    component,
    provider,
    everSaved: Boolean(last4),
    showMasked: Boolean(last4),
    last4: last4 ?? "",
  });
}

// stateful mock backend: List reflects what Save mutates, so an invalidation
// refetch shows the saved credential — proving the write-only round-trip from
// the screen's side (the RPC never returns a secret value).
function mockBackend() {
  const state = {
    groq: undefined as string | undefined,
    elevenlabs: undefined as string | undefined,
    discord: undefined as string | undefined,
    guildId: "",
    voiceChannelId: "",
  };
  return createRouterTransport(({ service }) => {
    service(CampaignService, {
      getActiveCampaign: () =>
        create(GetActiveCampaignResponseSchema, { campaign: create(CampaignSchema, CAMPAIGN) }),
    });
    service(ProviderService, {
      listProviderConfigs: () =>
        create(ListProviderConfigsResponseSchema, {
          credentials: [
            cred("discord", "discord", state.discord),
            cred("llm", "groq", state.groq),
            cred("tts", "elevenlabs", state.elevenlabs),
          ],
          guildId: state.guildId,
          voiceChannelId: state.voiceChannelId,
        }),
      saveProviderConfig: (req) => {
        const last4 = req.secret.slice(-4);
        if (req.provider === "groq") state.groq = last4;
        if (req.provider === "elevenlabs") state.elevenlabs = last4;
        return create(SaveProviderConfigResponseSchema, {
          credential: cred(req.provider === "groq" ? "llm" : "tts", req.provider, last4),
        });
      },
      saveDiscordSettings: (req) => {
        if (req.botToken !== undefined) state.discord = req.botToken.slice(-4);
        state.guildId = req.guildId;
        state.voiceChannelId = req.voiceChannelId;
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
});
