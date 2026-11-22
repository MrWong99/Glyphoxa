import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react";
import { createRouterTransport, ConnectError, Code } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";

import {
  VoiceService,
  ChatLlmKeySource,
  ChatLlmSource,
  ChatProviderOptionSchema,
  EffectiveChatLlmSchema,
  EffectiveLlmSchema,
  ListModelsResponseSchema,
  type EffectiveChatLlm,
  type ListModelsRequest,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { ChatModelCard } from "./ChatModelCard";
import { CHAT_MODEL_ANCHOR } from "./chatModelAnchor";

// The Planning chat model choice on the Setup screen (ADR-0062 amendment): a
// choice, not a key row — "same as voice", or a provider and a model of its
// own, with a status line that names what the server says actually answers.

const CHAT_MODELS: Record<string, string[]> = {
  groq: ["openai/gpt-oss-120b", "openai/gpt-oss-20b"],
  anthropic: ["claude-sonnet-5-5", "claude-opus-5-5", "claude-haiku-5-5"],
};

const VOICE = create(EffectiveLlmSchema, { provider: "anthropic", model: "claude-haiku-5-5", platformDefault: true });

type Opt = { keySource: ChatLlmKeySource };
// effective builds an EffectiveChatLlm; both providers ride the plan's key
// unless a test says otherwise.
function effective(
  over: Partial<Pick<EffectiveChatLlm, "provider" | "model" | "storedModel" | "keyLast4" | "source" | "keySource">> = {},
  providers: Partial<Record<"groq" | "anthropic", Opt>> = {},
) {
  const option = (provider: "groq" | "anthropic", defaultModel: string) => {
    const keySource = providers[provider]?.keySource ?? ChatLlmKeySource.PLATFORM;
    return create(ChatProviderOptionSchema, {
      provider,
      keySource,
      available: keySource !== ChatLlmKeySource.NONE,
      defaultModel,
    });
  };
  return create(EffectiveChatLlmSchema, {
    provider: "anthropic",
    model: "claude-haiku-5-5",
    source: ChatLlmSource.PLATFORM_DEFAULT,
    keySource: ChatLlmKeySource.PLATFORM,
    ...over,
    providers: [option("groq", "openai/gpt-oss-120b"), option("anthropic", "claude-sonnet-5-5")],
  });
}

function renderCard(
  props: Partial<Parameters<typeof ChatModelCard>[0]> = {},
  catalogRequests: ListModelsRequest[] = [],
) {
  const transport = createRouterTransport(({ service }) => {
    service(VoiceService, {
      listModels: (req) => {
        catalogRequests.push(req);
        return create(ListModelsResponseSchema, { models: CHAT_MODELS[req.provider] ?? [] });
      },
    });
  });
  const onSave = vi.fn(() => Promise.resolve());
  const onReset = vi.fn(() => Promise.resolve());
  const view = render(
    <Providers transport={transport} queryClient={makeQueryClient()}>
      <ChatModelCard effective={effective()} voice={VOICE} onSave={onSave} onReset={onReset} {...props} />
    </Providers>,
  );
  return { ...view, onSave, onReset };
}

const providerSelect = () => screen.getByRole("combobox", { name: "Planning chat provider" });
const modelBox = () => screen.getByRole("button", { name: "Planning chat model" });
const saveButton = () => screen.getByRole("button", { name: "Save chat model" });

// choose opens the provider select by keyboard (as a GM tabbing through Setup
// would) and picks the option whose label starts with `name`.
async function choose(name: RegExp) {
  fireEvent.keyDown(providerSelect(), { key: "Enter" });
  fireEvent.click(await screen.findByRole("option", { name }));
}

describe("ChatModelCard", () => {
  it("shows 'Same as voice' with the voice model and says the chat uses the platform default", () => {
    renderCard();
    expect(providerSelect()).toHaveTextContent("Same as voice (Anthropic / claude-haiku-5-5)");
    const status = screen.getByTestId("chat-model-status");
    expect(status).toHaveTextContent("Answers with Anthropic / claude-haiku-5-5");
    expect(status).toHaveTextContent("Included with your plan.");
    expect(status).toHaveTextContent(/haven't picked a chat model or a voice model.*platform default/i);
    // Following voice there is no model to pick and nothing to save.
    expect(screen.queryByRole("button", { name: "Planning chat model" })).not.toBeInTheDocument();
    expect(saveButton()).toBeDisabled();
  });

  it("says the chat follows the voice model when the table has its own voice model", () => {
    renderCard({
      effective: effective({
        provider: "groq",
        model: "qwen/qwen3-32b",
        source: ChatLlmSource.VOICE,
        keySource: ChatLlmKeySource.PROVIDER_KEY,
        keyLast4: "9876",
      }),
      voice: create(EffectiveLlmSchema, { provider: "groq", model: "qwen/qwen3-32b" }),
    });
    expect(providerSelect()).toHaveTextContent("Same as voice (Groq / qwen/qwen3-32b)");
    const status = screen.getByTestId("chat-model-status");
    expect(status).toHaveTextContent("Answers with Groq / qwen/qwen3-32b");
    expect(status).toHaveTextContent("On your Groq key (ends in 9876).");
    expect(status).toHaveTextContent(/follows your voice model/i);
  });

  it("names a platform choice and what it runs on", async () => {
    renderCard({
      effective: effective({
        provider: "anthropic",
        model: "claude-sonnet-5-5",
        source: ChatLlmSource.CHOICE,
        keySource: ChatLlmKeySource.PLATFORM,
      }),
    });
    expect(providerSelect()).toHaveTextContent("Anthropic · included");
    const status = screen.getByTestId("chat-model-status");
    expect(status).toHaveTextContent("Answers with Anthropic / claude-sonnet-5-5");
    expect(status).toHaveTextContent("Included with your plan.");
    // A choice is not "following" anything: no chain line.
    expect(status).not.toHaveTextContent(/haven't picked/i);
    // The model box shows the provider's chat default without it being stored.
    expect(modelBox()).toHaveTextContent("claude-sonnet-5-5");
    expect(saveButton()).toBeDisabled();
  });

  it("labels every provider option with the key it would run on", async () => {
    renderCard({
      effective: effective({}, { groq: { keySource: ChatLlmKeySource.NONE }, anthropic: { keySource: ChatLlmKeySource.PROVIDER_KEY } }),
    });
    fireEvent.keyDown(providerSelect(), { key: "Enter" });
    expect(await screen.findByRole("option", { name: "Groq · needs a key" })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: "Anthropic · your key" })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: "Same as voice (Anthropic / claude-haiku-5-5)" })).toBeInTheDocument();
  });

  it("saves a choice of Anthropic with no key and no model: the passive default is not stored", async () => {
    const requests: ListModelsRequest[] = [];
    const { onSave } = renderCard({}, requests);
    await choose(/^Anthropic/);

    // The chat catalog of the chosen provider is asked for, as the chat slot.
    await waitFor(() => expect(requests.some((r) => r.provider === "anthropic" && r.slot === "chat")).toBe(true));
    expect(modelBox()).toHaveTextContent("claude-sonnet-5-5");
    // Nothing is saved until the GM says so, and the status line still names
    // what is stored.
    expect(onSave).not.toHaveBeenCalled();
    expect(screen.getByText("Not saved yet.")).toBeInTheDocument();
    expect(screen.getByTestId("chat-model-status")).toHaveTextContent("Answers with Anthropic / claude-haiku-5-5");
    // No key field: the plan's key covers it.
    expect(screen.queryByLabelText("Anthropic key for the planning chat")).not.toBeInTheDocument();

    fireEvent.click(saveButton());
    await waitFor(() => expect(onSave).toHaveBeenCalledTimes(1));
    expect(onSave).toHaveBeenCalledWith("anthropic", "", "");
  });

  it("sends an actively picked model", async () => {
    const { onSave } = renderCard();
    await choose(/^Anthropic/);
    fireEvent.click(modelBox());
    fireEvent.click(await screen.findByRole("option", { name: "claude-opus-5-5" }));
    expect(modelBox()).toHaveTextContent("claude-opus-5-5");

    fireEvent.click(saveButton());
    await waitFor(() => expect(onSave).toHaveBeenCalledWith("anthropic", "claude-opus-5-5", ""));
  });

  it("offers a model-only save on the stored choice and keeps the stored model otherwise", async () => {
    const { onSave } = renderCard({
      effective: effective({
        provider: "anthropic",
        model: "claude-opus-5-5",
        storedModel: "claude-opus-5-5",
        source: ChatLlmSource.CHOICE,
      }),
    });
    expect(modelBox()).toHaveTextContent("claude-opus-5-5");
    expect(saveButton()).toBeDisabled();

    fireEvent.click(modelBox());
    fireEvent.click(await screen.findByRole("option", { name: "claude-haiku-5-5" }));
    fireEvent.click(saveButton());
    await waitFor(() => expect(onSave).toHaveBeenCalledWith("anthropic", "claude-haiku-5-5", ""));
  });

  it("asks for a key when the chosen provider has none, and saves it with the choice", async () => {
    const { onSave } = renderCard({
      effective: effective({}, { anthropic: { keySource: ChatLlmKeySource.NONE } }),
    });
    await choose(/^Anthropic · needs a key/);

    const key = await screen.findByLabelText("Anthropic key for the planning chat");
    expect(key).toHaveAttribute("type", "password");
    // The hint says where the key goes: the chat only, never the voice model.
    expect(screen.getByText(/saved for the planning chat only/i)).toBeInTheDocument();
    // Without a key the choice cannot be saved: it could not answer.
    expect(saveButton()).toBeDisabled();

    fireEvent.change(key, { target: { value: "sk-ant-chat-only-4321" } });
    expect(saveButton()).toBeEnabled();
    fireEvent.keyDown(key, { key: "Enter" });
    await waitFor(() => expect(onSave).toHaveBeenCalledWith("anthropic", "", "sk-ant-chat-only-4321"));
    // The field is cleared after the save: the key is write-only.
    await waitFor(() =>
      expect(screen.queryByDisplayValue("sk-ant-chat-only-4321")).not.toBeInTheDocument(),
    );
  });

  it("keeps the key field behind 'use a separate key' when a key is already reachable", async () => {
    const { onSave } = renderCard({
      effective: effective({ provider: "anthropic", model: "claude-sonnet-5-5", source: ChatLlmSource.CHOICE }),
    });
    expect(screen.queryByLabelText("Anthropic key for the planning chat")).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Use a separate key for the chat" }));
    const key = screen.getByLabelText("Anthropic key for the planning chat");
    // Revealing the field alone changes nothing.
    expect(saveButton()).toBeDisabled();
    // Cancel hides it again.
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(screen.queryByLabelText("Anthropic key for the planning chat")).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Use a separate key for the chat" }));
    fireEvent.change(screen.getByLabelText("Anthropic key for the planning chat"), {
      target: { value: "sk-ant-separate-7777" },
    });
    expect(key).not.toBeInTheDocument();
    fireEvent.click(saveButton());
    await waitFor(() => expect(onSave).toHaveBeenCalledWith("anthropic", "", "sk-ant-separate-7777"));
  });

  it("goes back to 'Same as voice' through the reset, warning that a chat-only key is removed", async () => {
    const { onSave, onReset } = renderCard({
      effective: effective({
        provider: "anthropic",
        model: "claude-sonnet-5-5",
        source: ChatLlmSource.CHOICE,
        keySource: ChatLlmKeySource.CHAT_KEY,
        keyLast4: "4321",
      }, { anthropic: { keySource: ChatLlmKeySource.CHAT_KEY } }),
    });
    expect(screen.getByTestId("chat-model-status")).toHaveTextContent("On a key saved for the chat only (ends in 4321).");
    expect(screen.queryByText(/removes the key saved for the chat/i)).not.toBeInTheDocument();

    await choose(/^Same as voice/);
    expect(screen.getByText(/removes the key saved for the chat/i)).toBeInTheDocument();
    fireEvent.click(saveButton());
    await waitFor(() => expect(onReset).toHaveBeenCalledTimes(1));
    expect(onSave).not.toHaveBeenCalled();
  });

  it("warns that switching provider removes a chat-only key too", async () => {
    renderCard({
      effective: effective({
        provider: "anthropic",
        model: "claude-sonnet-5-5",
        source: ChatLlmSource.CHOICE,
        keySource: ChatLlmKeySource.CHAT_KEY,
        keyLast4: "4321",
      }, { anthropic: { keySource: ChatLlmKeySource.CHAT_KEY } }),
    });
    await choose(/^Groq/);
    expect(screen.getByText(/removes the key saved for the chat/i)).toBeInTheDocument();
  });

  it("does not warn about a key when there is none to lose", async () => {
    const { onReset } = renderCard({
      effective: effective({ provider: "groq", model: "openai/gpt-oss-120b", source: ChatLlmSource.CHOICE }),
    });
    await choose(/^Same as voice/);
    expect(screen.queryByText(/removes the key saved for the chat/i)).not.toBeInTheDocument();
    fireEvent.click(saveButton());
    await waitFor(() => expect(onReset).toHaveBeenCalledTimes(1));
  });

  it("warns when no key is reachable for what is stored", () => {
    renderCard({
      effective: effective({
        provider: "anthropic",
        model: "claude-sonnet-5-5",
        source: ChatLlmSource.CHOICE,
        keySource: ChatLlmKeySource.NONE,
      }, { anthropic: { keySource: ChatLlmKeySource.NONE } }),
    });
    const warning = screen.getByRole("status");
    expect(warning).toHaveTextContent(/no anthropic key is available.*can't answer yet/i);
    // The fix is on the card: the key field is already open.
    expect(screen.getByLabelText("Anthropic key for the planning chat")).toBeInTheDocument();
  });

  it("shows a refused save inline and keeps the pending choice for a retry", async () => {
    const onSave = vi.fn(() =>
      Promise.reject(new ConnectError("model \"x\" has no price on record", Code.InvalidArgument)),
    );
    renderCard({ onSave });
    await choose(/^Anthropic/);
    fireEvent.click(saveButton());

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(/couldn't save/i);
    expect(alert).toHaveTextContent(/no price on record/i);
    // The choice is still pending and can be saved again.
    expect(providerSelect()).toHaveTextContent("Anthropic · included");
    expect(saveButton()).toBeEnabled();
  });

  it("renders a skeleton while the Setup read is pending, and an error when it failed", () => {
    const { rerender, onSave, onReset } = renderCard({ effective: undefined });
    expect(screen.getByTestId("chat-model-loading")).toBeInTheDocument();
    expect(screen.queryByRole("combobox", { name: "Planning chat provider" })).not.toBeInTheDocument();

    const transport = createRouterTransport(() => {});
    rerender(
      <Providers transport={transport} queryClient={makeQueryClient()}>
        <ChatModelCard
          effective={undefined}
          loadError={new ConnectError("database unavailable", Code.Unavailable)}
          onSave={onSave}
          onReset={onReset}
        />
      </Providers>,
    );
    expect(screen.getByRole("alert")).toHaveTextContent(/couldn't load the planning chat model.*database unavailable/i);
    expect(screen.queryByTestId("chat-model-loading")).not.toBeInTheDocument();
  });

  it("is the link target the Planning panel points at", () => {
    const { container } = renderCard();
    const card = container.querySelector(`#${CHAT_MODEL_ANCHOR}`) as HTMLElement;
    expect(card).not.toBeNull();
    expect(within(card).getByText("Planning chat model")).toBeInTheDocument();
  });

  it("names an unknown provider id as-is instead of hiding it", () => {
    renderCard({
      effective: effective({ provider: "gemini", model: "gemini-2.5-flash", source: ChatLlmSource.VOICE }),
      voice: create(EffectiveLlmSchema, { provider: "newvendor", model: "nv-1" }),
    });
    expect(screen.getByTestId("chat-model-status")).toHaveTextContent("Answers with Gemini / gemini-2.5-flash");
    expect(providerSelect()).toHaveTextContent("Same as voice (newvendor / nv-1)");
  });
});
