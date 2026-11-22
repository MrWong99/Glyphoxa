import { describe, it, expect } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { createRouterTransport, ConnectError, Code } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";

import {
  CampaignService,
  ProviderService,
  VoiceService,
  ChatLlmKeySource,
  ChatLlmSource,
  ChatProviderOptionSchema,
  EffectiveChatLlmSchema,
  EffectiveLlmSchema,
  GetActiveCampaignResponseSchema,
  CampaignSchema,
  ListProviderConfigsResponseSchema,
  ProviderCredentialSchema,
  SaveProviderConfigResponseSchema,
  DeleteProviderConfigResponseSchema,
  GetProviderHealthResponseSchema,
  ListModelsResponseSchema,
  GetSpendCapsResponseSchema,
  type SaveProviderConfigRequest,
  type DeleteProviderConfigRequest,
  type ListModelsRequest,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { Configuration } from "./Configuration";

// The Planning chat model choice wired into the Setup screen (ADR-0062
// amendment): what the screen sends to ProviderService and how it re-reads the
// truth afterwards. The card's own behaviour is pinned in ChatModelCard.test.

type ChatChoice = { provider: "groq" | "anthropic"; model: string } | null;

// A stateful backend mirroring internal/rpc/provider_chat.go for a table on
// the plan's keys: no chat row means the chat follows the voice model.
function backend(opts: {
  chat?: ChatChoice;
  saves?: SaveProviderConfigRequest[];
  deletes?: DeleteProviderConfigRequest[];
  catalogs?: ListModelsRequest[];
  saveError?: string;
  listError?: string;
}) {
  let chat: ChatChoice = opts.chat ?? null;
  const defaults = { groq: "openai/gpt-oss-120b", anthropic: "claude-sonnet-5-5" };
  const voice = { provider: "anthropic", model: "claude-haiku-5-5", platformDefault: true };
  const effectiveChat = () =>
    create(EffectiveChatLlmSchema, {
      provider: chat ? chat.provider : voice.provider,
      model: chat ? chat.model || defaults[chat.provider] : voice.model,
      storedModel: chat?.model ?? "",
      source: chat ? ChatLlmSource.CHOICE : ChatLlmSource.PLATFORM_DEFAULT,
      keySource: ChatLlmKeySource.PLATFORM,
      providers: (["groq", "anthropic"] as const).map((provider) =>
        create(ChatProviderOptionSchema, {
          provider,
          available: true,
          keySource: ChatLlmKeySource.PLATFORM,
          defaultModel: defaults[provider],
        }),
      ),
    });
  return createRouterTransport(({ service }) => {
    service(CampaignService, {
      getActiveCampaign: () =>
        create(GetActiveCampaignResponseSchema, {
          campaign: create(CampaignSchema, { id: "c", tenantId: "t", name: "Saltmarsh", system: "5e", language: "en" }),
        }),
    });
    service(VoiceService, {
      getProviderHealth: () => create(GetProviderHealthResponseSchema, { providers: [] }),
      listModels: (req) => {
        opts.catalogs?.push(req);
        const chatCatalog = req.provider === "anthropic" ? ["claude-sonnet-5-5", "claude-opus-5-5"] : ["openai/gpt-oss-120b"];
        const voiceCatalog = req.provider === "anthropic" ? ["claude-haiku-5-5"] : ["openai/gpt-oss-120b"];
        return create(ListModelsResponseSchema, { models: req.slot === "chat" ? chatCatalog : voiceCatalog });
      },
    });
    service(ProviderService, {
      listProviderConfigs: () => {
        if (opts.listError) throw new ConnectError(opts.listError, Code.Unavailable);
        return create(ListProviderConfigsResponseSchema, {
          // The chat is not a credential slot: five entries, none for it.
          credentials: [
            create(ProviderCredentialSchema, { component: "discord", provider: "discord" }),
            create(ProviderCredentialSchema, { component: "llm", provider: "groq", providedByPlatform: true }),
            create(ProviderCredentialSchema, {
              component: "llm",
              provider: "anthropic",
              providedByPlatform: true,
              inEffect: true,
            }),
            create(ProviderCredentialSchema, { component: "tts", provider: "elevenlabs", providedByPlatform: true }),
            create(ProviderCredentialSchema, { component: "image", provider: "gemini" }),
          ],
          effectiveLlm: create(EffectiveLlmSchema, voice),
          effectiveChatLlm: effectiveChat(),
        });
      },
      saveProviderConfig: (req) => {
        opts.saves?.push(req);
        if (opts.saveError) throw new ConnectError(opts.saveError, Code.InvalidArgument);
        if (req.slot === "chat") chat = { provider: req.provider as "groq" | "anthropic", model: req.model };
        return create(SaveProviderConfigResponseSchema, {});
      },
      deleteProviderConfig: (req) => {
        opts.deletes?.push(req);
        chat = null;
        return create(DeleteProviderConfigResponseSchema, {});
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

const providerSelect = () => screen.findByRole("combobox", { name: "Planning chat provider" });
const status = () => screen.getByTestId("chat-model-status");

async function choose(name: RegExp) {
  fireEvent.keyDown(await providerSelect(), { key: "Enter" });
  fireEvent.click(await screen.findByRole("option", { name }));
}

describe("Configuration planning chat model", () => {
  it("renders the chat as a choice below the key rows, not as a third AI-model key row", async () => {
    renderScreen(backend({}));
    expect(await providerSelect()).toHaveTextContent("Same as voice (Anthropic / claude-haiku-5-5)");
    expect(status()).toHaveTextContent("Answers with Anthropic / claude-haiku-5-5");
    // Still exactly two "AI model" overlines (the voice-LLM slots) and one for
    // the planning chat.
    expect(screen.getAllByText("AI model")).toHaveLength(2);
    expect(screen.getAllByText("Planning chat model")).toHaveLength(1);
    // The chat has no key row of its own any more.
    expect(screen.queryByLabelText("Planning chat (Groq) key")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Planning chat key")).not.toBeInTheDocument();
    // The card is the anchor the Planning panel links to.
    expect(document.getElementById("planning-chat")).not.toBeNull();
  });

  it("saves a choice of Anthropic as slot chat with no key and no model, then names the new model", async () => {
    const saves: SaveProviderConfigRequest[] = [];
    const catalogs: ListModelsRequest[] = [];
    renderScreen(backend({ saves, catalogs }));
    await choose(/^Anthropic · included/);
    fireEvent.click(screen.getByRole("button", { name: "Save chat model" }));

    await waitFor(() => expect(saves).toHaveLength(1));
    expect(saves[0]).toMatchObject({ slot: "chat", provider: "anthropic", model: "", secret: "", activate: false });
    // The re-read after the save is the source of the status line.
    await waitFor(() => expect(status()).toHaveTextContent("Answers with Anthropic / claude-sonnet-5-5"));
    expect(await providerSelect()).toHaveTextContent("Anthropic · included");
    // The chat catalog was asked for as the chat slot, never the voice one.
    expect(catalogs.some((r) => r.provider === "anthropic" && r.slot === "chat")).toBe(true);
    // The voice line above is untouched by a chat save.
    expect(screen.getByTestId("llm-in-use")).toHaveTextContent("Anthropic / claude-haiku-5-5");
  });

  it("sends an actively picked chat model", async () => {
    const saves: SaveProviderConfigRequest[] = [];
    renderScreen(backend({ saves }));
    await choose(/^Anthropic/);
    fireEvent.click(screen.getByRole("button", { name: "Planning chat model" }));
    fireEvent.click(await screen.findByRole("option", { name: "claude-opus-5-5" }));
    fireEvent.click(screen.getByRole("button", { name: "Save chat model" }));

    await waitFor(() => expect(saves).toHaveLength(1));
    expect(saves[0]).toMatchObject({ slot: "chat", provider: "anthropic", model: "claude-opus-5-5", secret: "" });
    await waitFor(() => expect(status()).toHaveTextContent("Answers with Anthropic / claude-opus-5-5"));
  });

  it("goes back to 'Same as voice' through DeleteProviderConfig", async () => {
    const saves: SaveProviderConfigRequest[] = [];
    const deletes: DeleteProviderConfigRequest[] = [];
    renderScreen(backend({ chat: { provider: "groq", model: "" }, saves, deletes }));
    expect(await providerSelect()).toHaveTextContent("Groq · included");
    await waitFor(() => expect(status()).toHaveTextContent("Answers with Groq / openai/gpt-oss-120b"));

    await choose(/^Same as voice/);
    fireEvent.click(screen.getByRole("button", { name: "Save chat model" }));

    await waitFor(() => expect(deletes).toHaveLength(1));
    expect(deletes[0]).toMatchObject({ slot: "chat" });
    expect(saves).toHaveLength(0);
    await waitFor(() => expect(status()).toHaveTextContent("Answers with Anthropic / claude-haiku-5-5"));
  });

  it("shows a refused chat save on the chat card, not on a key row", async () => {
    renderScreen(backend({ saveError: "model has no price on record" }));
    await choose(/^Anthropic/);
    fireEvent.click(screen.getByRole("button", { name: "Save chat model" }));

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(/couldn't save.*no price on record/i);
    expect(alert.closest("#planning-chat")).not.toBeNull();
    // Nothing was stored: the status line still names the voice model.
    expect(status()).toHaveTextContent("Answers with Anthropic / claude-haiku-5-5");
  });

  it("says so when the Setup read fails instead of showing a control that cannot be trusted", async () => {
    renderScreen(backend({ listError: "database unavailable" }));
    const card = await waitFor(() => {
      const el = document.getElementById("planning-chat");
      expect(el?.querySelector('[role="alert"]')).not.toBeNull();
      return el as HTMLElement;
    });
    expect(card).toHaveTextContent(/couldn't load the planning chat model/i);
    expect(screen.queryByRole("combobox", { name: "Planning chat provider" })).not.toBeInTheDocument();
  });
});
