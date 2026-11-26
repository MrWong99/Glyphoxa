import { describe, it, expect, vi } from "vitest";
import { render, screen, within, fireEvent, waitFor } from "@testing-library/react";
import { createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";
import { toast } from "sonner";

import {
  AuthService,
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
  DeleteProviderConfigResponseSchema,
  GetUsageSummaryResponseSchema,
  type DeleteProviderConfigRequest,
  type SaveProviderConfigRequest,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { Configuration } from "./Configuration";
import { currentUser, keyRow } from "./testRows";

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() } }));

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
  // health replaces the `healthy` shorthand with explicit entries, each tagged
  // with the component it is the health of (what a current server sends).
  health?: { provider: string; component: string; status: HealthStatus }[];
  // imageSaved gives the Gemini image slot a saved key.
  imageSaved?: boolean;
  saves?: SaveProviderConfigRequest[];
  // deletes captures every DeleteProviderConfig request; chatModelReset is
  // what the server answers about the planning chat.
  deletes?: DeleteProviderConfigRequest[];
  chatModelReset?: boolean;
  // usesPlatformKeys is GetUsageSummary's answer: whether the plan has a
  // platform default to go back to (default yes, the platformDefault above).
  usesPlatformKeys?: boolean;
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
          providers: opts.health
            ? opts.health.map((h) => create(ProviderHealthSchema, h))
            : (opts.healthy ?? []).map((provider) =>
                create(ProviderHealthSchema, { provider, status: HealthStatus.HEALTHY }),
              ),
        }),
      listModels: (req) => create(ListModelsResponseSchema, { models: MODELS[req.provider] ?? [] }),
    });
    service(AuthService, { getCurrentUser: currentUser });
    service(ProviderService, {
      getUsageSummary: () =>
        create(GetUsageSummaryResponseSchema, { usesPlatformKeys: opts.usesPlatformKeys ?? true }),
      listProviderConfigs: () =>
        create(ListProviderConfigsResponseSchema, {
          credentials: [
            create(ProviderCredentialSchema, { component: "discord", provider: "discord" }),
            cred("groq"),
            cred("anthropic"),
            create(ProviderCredentialSchema, { component: "tts", provider: "elevenlabs" }),
            create(ProviderCredentialSchema, {
              component: "image",
              provider: "gemini",
              everSaved: Boolean(opts.imageSaved),
              showMasked: Boolean(opts.imageSaved),
              last4: opts.imageSaved ? "7777" : "",
            }),
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
      // Mirrors the server (ADR-0004 amendment 2026-11-26): the named voice
      // slots' rows go; the newest remaining row, else the default, is in effect.
      deleteProviderConfig: (req) => {
        opts.deletes?.push(req);
        const gone = req.slot === "voice_llm" ? (["groq", "anthropic"] as const) : [req.slot as "groq" | "anthropic"];
        for (const p of gone) slots[p] = {};
        if (newest && (gone as readonly string[]).includes(newest)) {
          const other = newest === "groq" ? "anthropic" : "groq";
          newest = slots[other].row ? other : undefined;
        }
        return create(DeleteProviderConfigResponseSchema, { chatModelReset: Boolean(opts.chatModelReset) });
      },
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

// A key row by its slot's name ("Groq key" → the Groq row). Found by name, not
// by the key field: a slot the plan provides shows no field until the GM asks
// for one ("Use my own key").
async function row(keyLabel: string) {
  return keyRow(keyLabel.replace(/ key$/, ""));
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
    const anthropic = (await screen.findByLabelText("Anthropic saved")).closest(".gx-provider-card") as HTMLElement;
    expect(await within(anthropic).findByText(/^working$/i)).toBeInTheDocument();
    const groq = screen.getByLabelText("Groq saved").closest(".gx-provider-card") as HTMLElement;
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
    const groq = (await screen.findByLabelText("Groq saved")).closest(".gx-provider-card") as HTMLElement;
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

  // Gemini as the deployment's default LLM makes two "gemini" health entries;
  // the image row must show the IMAGE key's verdict, not the LLM's.
  it("matches a health entry to its row by component, not by provider name", async () => {
    renderScreen(
      backend({
        imageSaved: true,
        health: [
          { provider: "gemini", component: "llm", status: HealthStatus.HEALTHY },
          { provider: "gemini", component: "image", status: HealthStatus.DEGRADED },
        ],
      }),
    );
    const image = (await screen.findByLabelText("Gemini saved")).closest(".gx-provider-card") as HTMLElement;
    expect(await within(image).findByText(/having trouble/i)).toBeInTheDocument();
    expect(within(image).queryByText(/^working$/i)).not.toBeInTheDocument();
  });

  // A probe that timed out before the tenant's LLM row was read can only name
  // the deployment default: the LLM entry still belongs to the row in use.
  it("shows the LLM health entry on the row in use even under another provider's name", async () => {
    renderScreen(
      backend({
        slots: { anthropic: { last4: "9999" }, groq: { last4: "1111" } },
        newest: "anthropic",
        health: [{ provider: "groq", component: "llm", status: HealthStatus.DEGRADED }],
      }),
    );
    const anthropic = (await screen.findByLabelText("Anthropic saved")).closest(".gx-provider-card") as HTMLElement;
    expect(await within(anthropic).findByText(/having trouble/i)).toBeInTheDocument();
    const groq = screen.getByLabelText("Groq saved").closest(".gx-provider-card") as HTMLElement;
    expect(within(groq).queryByText(/having trouble/i)).not.toBeInTheDocument();
  });

  // A key saved into the slot NOT in use becomes the tenant's newest LLM row,
  // i.e. its voice model: the row says so before the save, never silently.
  it("says that saving a key into the idle slot switches the voice model", async () => {
    renderScreen(backend({ slots: { groq: { last4: "1111" } }, newest: "groq" }));
    const anthropic = await row("Anthropic key");
    await screen.findByTestId("llm-in-use");
    expect(within(anthropic).getByText(/saving a key makes Anthropic your voice model/i)).toBeInTheDocument();
    // The slot in use carries no such note.
    const groq = (await screen.findByLabelText("Groq saved")).closest(".gx-provider-card") as HTMLElement;
    expect(within(groq).queryByText(/your voice model/i)).not.toBeInTheDocument();
  });

  // The combobox's passive first entry is a display default, not a choice: a
  // key save stores no model, so the row keeps following the default.
  it("does not store the passive catalog default as the model when a key is saved", async () => {
    const saves: SaveProviderConfigRequest[] = [];
    renderScreen(backend({ saves }));
    const input = await screen.findByLabelText("Anthropic key");
    const anthropic = await row("Anthropic key");
    await within(within(anthropic).getByLabelText("Anthropic model")).findByText("claude-haiku-5-5");
    fireEvent.change(input, { target: { value: "sk-ant-api-secret-9999" } });
    fireEvent.click(within(anthropic).getByRole("button", { name: /^save$/i }));
    await waitFor(() => expect(saves).toHaveLength(1));
    expect(saves[0].model).toBe("");
  });

  it("removes a voice key behind a confirmation and follows what the server runs next", async () => {
    const deletes: DeleteProviderConfigRequest[] = [];
    renderScreen(
      backend({
        slots: { anthropic: { last4: "4242", model: "claude-haiku-4-5", row: true }, groq: { platform: true } },
        newest: "anthropic",
        deletes,
        chatModelReset: true,
      }),
    );
    const anthropic = await row("Anthropic key");
    fireEvent.click(within(anthropic).getByRole("button", { name: "Remove key" }));
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog).toHaveTextContent("Remove your Anthropic key?");
    expect(deletes).toHaveLength(0);

    fireEvent.click(within(dialog).getByRole("button", { name: "Remove key" }));
    await waitFor(() => expect(deletes).toEqual([expect.objectContaining({ slot: "anthropic" })]));
    // effective_llm follows the server: back on the platform default.
    await waitFor(() => expect(screen.getByTestId("llm-in-use")).toHaveTextContent(/Groq .*platform default/i));
    expect(toast.success).toHaveBeenCalledWith("Anthropic key removed", {
      description: expect.stringMatching(/planning chat moved to its default model/i),
    });
  });

  it("goes back to the platform default from the voice section, after confirming", async () => {
    const deletes: DeleteProviderConfigRequest[] = [];
    renderScreen(
      backend({
        slots: { groq: { last4: "1111", row: true }, anthropic: { last4: "4242", row: true } },
        newest: "anthropic",
        deletes,
      }),
    );
    expect(await screen.findByTestId("llm-in-use")).toHaveTextContent(/your choice/i);
    fireEvent.click(screen.getByRole("button", { name: "Back to platform default" }));
    const dialog = await screen.findByRole("alertdialog");
    fireEvent.click(within(dialog).getByRole("button", { name: "Back to platform default" }));

    await waitFor(() => expect(deletes).toEqual([expect.objectContaining({ slot: "voice_llm" })]));
    await waitFor(() => expect(screen.getByTestId("llm-in-use")).toHaveTextContent(/platform default/i));
    // Nothing left to go back from.
    expect(screen.queryByRole("button", { name: "Back to platform default" })).not.toBeInTheDocument();
  });

  it("offers no way back on a plan without platform keys: it would leave no AI model", async () => {
    renderScreen(
      backend({
        slots: { groq: { last4: "1111", row: true }, anthropic: { last4: "4242", row: true } },
        newest: "anthropic",
        usesPlatformKeys: false,
      }),
    );
    expect(await screen.findByTestId("llm-in-use")).toHaveTextContent(/your choice/i);
    // Wait for the plan read to answer, then the button is gone; the rows keep Remove key.
    await waitFor(() =>
      expect(screen.queryByRole("button", { name: "Back to platform default" })).not.toBeInTheDocument(),
    );
    expect(screen.getAllByRole("button", { name: "Remove key" }).length).toBeGreaterThan(0);
  });

  it("offers no way back while the table already runs the platform default", async () => {
    renderScreen(backend({ slots: { groq: { platform: true } } }));
    await screen.findByTestId("llm-in-use");
    expect(screen.queryByRole("button", { name: "Back to platform default" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Remove key" })).not.toBeInTheDocument();
  });
});
