import { describe, it, expect } from "vitest";
import { render, screen, within, fireEvent, waitFor } from "@testing-library/react";
import { createRouterTransport } from "@connectrpc/connect";
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
  GetProviderHealthResponseSchema,
  ProviderHealthSchema,
  ListModelsResponseSchema,
  GetSpendCapsResponseSchema,
  EffectiveLlmSchema,
  type SaveProviderConfigRequest,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { Configuration } from "./Configuration";

// The voice-LLM choice on the Configuration screen (#702): two LLM slots (Groq,
// Anthropic), one of them in effect — the tenant's own row or the platform
// default — with the health of the provider actually in use.

const MODELS: Record<string, string[]> = {
  groq: ["openai/gpt-oss-120b", "openai/gpt-oss-20b"],
  anthropic: ["claude-haiku-5-5", "claude-haiku-4-5"],
};

type Slot = { last4?: string; model?: string; platform?: boolean; row?: boolean };

// A stateful backend mirroring internal/rpc/provider.go: the newest LLM row
// decides the provider in effect, else the platform default does.
function backend(opts: {
  slots?: Partial<Record<"groq" | "anthropic", Slot>>;
  // newest names the slot whose row is the tenant's newest LLM row.
  newest?: "groq" | "anthropic";
  platformDefault?: { provider: "groq" | "anthropic"; model: string };
  healthy?: string[];
  saves?: SaveProviderConfigRequest[];
}) {
  const slots: Record<"groq" | "anthropic", Slot> = {
    groq: { ...opts.slots?.groq },
    anthropic: { ...opts.slots?.anthropic },
  };
  let newest = opts.newest;
  const def = opts.platformDefault ?? { provider: "groq" as const, model: "openai/gpt-oss-120b" };
  const effective = () =>
    newest
      ? { provider: newest, model: slots[newest].model || MODELS[newest][0], platformDefault: false }
      : { provider: def.provider, model: def.model, platformDefault: true };
  const cred = (provider: "groq" | "anthropic") =>
    create(ProviderCredentialSchema, {
      component: "llm",
      provider,
      everSaved: Boolean(slots[provider].last4),
      showMasked: Boolean(slots[provider].last4),
      last4: slots[provider].last4 ?? "",
      model: slots[provider].model ?? "",
      providedByPlatform: Boolean(slots[provider].platform) && !slots[provider].last4,
      inEffect: effective().provider === provider,
    });
  return createRouterTransport(({ service }) => {
    service(CampaignService, {
      getActiveCampaign: () =>
        create(GetActiveCampaignResponseSchema, {
          campaign: create(CampaignSchema, { id: "c", tenantId: "t", name: "Saltmarsh", system: "5e", language: "en" }),
        }),
    });
    service(VoiceService, {
      getProviderHealth: () =>
        create(GetProviderHealthResponseSchema, {
          // Exactly one LLM entry: the provider in effect.
          providers: (opts.healthy ?? []).map((provider) =>
            create(ProviderHealthSchema, { provider, status: HealthStatus.HEALTHY }),
          ),
        }),
      listModels: (req) => create(ListModelsResponseSchema, { models: MODELS[req.provider] ?? [] }),
    });
    service(ProviderService, {
      listProviderConfigs: () =>
        create(ListProviderConfigsResponseSchema, {
          credentials: [
            create(ProviderCredentialSchema, { component: "discord", provider: "discord" }),
            cred("groq"),
            cred("anthropic"),
            create(ProviderCredentialSchema, { component: "tts", provider: "elevenlabs" }),
            create(ProviderCredentialSchema, { component: "image", provider: "gemini" }),
          ],
          effectiveLlm: create(EffectiveLlmSchema, effective()),
        }),
      saveProviderConfig: (req) => {
        opts.saves?.push(req);
        const provider = req.provider as "groq" | "anthropic";
        if (req.secret) slots[provider].last4 = req.secret.slice(-4);
        if (req.model) slots[provider].model = req.model;
        slots[provider].row = true;
        newest = provider;
        return create(SaveProviderConfigResponseSchema, { credential: cred(provider) });
      },
      getSpendCaps: () => create(GetSpendCapsResponseSchema, {}),
    });
  });
}

function renderScreen(transport: ReturnType<typeof backend>) {
  return render(
    <Providers transport={transport} queryClient={makeQueryClient()}>
      <Configuration />
    </Providers>,
  );
}

async function row(keyLabel: string) {
  return (await screen.findByLabelText(keyLabel)).closest(".gx-provider-row") as HTMLElement;
}

describe("Configuration voice-LLM choice (#702)", () => {
  it("names the platform default as the voice model in use for a tenant with no row", async () => {
    renderScreen(backend({ slots: { groq: { platform: true } }, healthy: ["groq"] }));
    const inUse = await screen.findByTestId("llm-in-use");
    expect(inUse).toHaveTextContent("Groq / openai/gpt-oss-120b");
    expect(inUse).toHaveTextContent(/platform default/i);
    expect(within(await row("Groq key")).getByText(/^in use$/i)).toBeInTheDocument();
    expect(within(await row("Anthropic key")).queryByText(/^in use$/i)).not.toBeInTheDocument();
  });

  it("names a deployment default on Anthropic", async () => {
    renderScreen(
      backend({
        slots: { anthropic: { platform: true } },
        platformDefault: { provider: "anthropic", model: "claude-haiku-4-5" },
        healthy: ["anthropic"],
      }),
    );
    const inUse = await screen.findByTestId("llm-in-use");
    expect(inUse).toHaveTextContent("Anthropic / claude-haiku-4-5");
    expect(inUse).toHaveTextContent(/platform default/i);
    expect(within(await row("Anthropic key")).getByText(/^in use$/i)).toBeInTheDocument();
  });

  it("names the tenant's own row and shows the health of the provider in use", async () => {
    renderScreen(
      backend({
        slots: { anthropic: { last4: "9999", model: "claude-haiku-4-5" }, groq: { last4: "1111" } },
        newest: "anthropic",
        healthy: ["anthropic"],
      }),
    );
    const inUse = await screen.findByTestId("llm-in-use");
    expect(inUse).toHaveTextContent("Anthropic / claude-haiku-4-5");
    expect(inUse).toHaveTextContent(/your choice/i);
    // The health badge sits on the Anthropic row; the Groq row, which no
    // session calls, claims neither "Working" nor trouble — just "Saved".
    const anthropic = (await screen.findByLabelText("Anthropic saved")).closest(".gx-provider-row") as HTMLElement;
    expect(await within(anthropic).findByText(/^working$/i)).toBeInTheDocument();
    const groq = screen.getByLabelText("Groq saved").closest(".gx-provider-row") as HTMLElement;
    expect(within(groq).getByText(/^saved$/i)).toBeInTheDocument();
    expect(within(groq).queryByText(/^working$/i)).not.toBeInTheDocument();
  });

  it("does not demand a key for the LLM slot that is not in use", async () => {
    renderScreen(backend({}));
    // Groq is in use and has no key anywhere: that one is needed. Anthropic is
    // a choice the tenant has not made: optional.
    expect(await within(await row("Groq key")).findByText(/key needed/i)).toBeInTheDocument();
    const anthropic = await row("Anthropic key");
    expect(within(anthropic).getByText(/^optional$/i)).toBeInTheDocument();
    expect(within(anthropic).queryByText(/key needed/i)).not.toBeInTheDocument();
  });

  it("lists each slot's own model catalog", async () => {
    renderScreen(backend({}));
    const anthropic = await row("Anthropic key");
    expect(
      await within(within(anthropic).getByLabelText("Anthropic model")).findByText("claude-haiku-5-5"),
    ).toBeInTheDocument();
    const groq = await row("Groq key");
    expect(
      await within(within(groq).getByLabelText("Groq model")).findByText("openai/gpt-oss-120b"),
    ).toBeInTheDocument();
  });

  it("switches to a platform-provided slot without a key", async () => {
    const saves: SaveProviderConfigRequest[] = [];
    renderScreen(
      backend({ slots: { groq: { platform: true }, anthropic: { platform: true } }, healthy: ["groq"], saves }),
    );
    const anthropic = await row("Anthropic key");
    fireEvent.click(await within(anthropic).findByRole("button", { name: /use for voice/i }));
    await waitFor(() => expect(saves).toHaveLength(1));
    expect(saves[0]).toMatchObject({ provider: "anthropic", secret: "", activate: true });
    await waitFor(() => expect(screen.getByTestId("llm-in-use")).toHaveTextContent(/Anthropic .*your choice/i));
    // The slot now in use offers no "use" button; the other one does.
    await waitFor(() =>
      expect(within(anthropic).queryByRole("button", { name: /use for voice/i })).not.toBeInTheDocument(),
    );
    expect(within(await row("Groq key")).getByRole("button", { name: /use for voice/i })).toBeInTheDocument();
  });

  it("switches back to a saved key's slot without re-entering it", async () => {
    const saves: SaveProviderConfigRequest[] = [];
    renderScreen(
      backend({
        slots: { anthropic: { last4: "9999" }, groq: { last4: "1111", model: "openai/gpt-oss-20b" } },
        newest: "anthropic",
        saves,
      }),
    );
    const groq = (await screen.findByLabelText("Groq saved")).closest(".gx-provider-row") as HTMLElement;
    fireEvent.click(within(groq).getByRole("button", { name: /use for voice/i }));
    await waitFor(() => expect(saves).toHaveLength(1));
    expect(saves[0]).toMatchObject({ provider: "groq", secret: "", model: "openai/gpt-oss-20b", activate: true });
  });

  it("offers no switch to a slot with no key behind it", async () => {
    renderScreen(backend({ slots: { groq: { platform: true } } }));
    const anthropic = await row("Anthropic key");
    await screen.findByTestId("llm-in-use");
    expect(within(anthropic).queryByRole("button", { name: /use for voice/i })).not.toBeInTheDocument();
  });

  it("saves an Anthropic key into the anthropic slot", async () => {
    const saves: SaveProviderConfigRequest[] = [];
    renderScreen(backend({ saves }));
    const input = await screen.findByLabelText("Anthropic key");
    fireEvent.change(input, { target: { value: "sk-ant-api-secret-9999" } });
    fireEvent.click(within(await row("Anthropic key")).getByRole("button", { name: /^save$/i }));
    await waitFor(() => expect(saves).toHaveLength(1));
    expect(saves[0]).toMatchObject({ provider: "anthropic", secret: "sk-ant-api-secret-9999", slot: "" });
    expect(await screen.findByLabelText("Anthropic saved")).toBeInTheDocument();
  });
});
