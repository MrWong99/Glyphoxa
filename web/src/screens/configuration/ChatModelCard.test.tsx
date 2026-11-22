import { readFileSync } from "node:fs";
import { resolve } from "node:path";
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

// The shared stylesheet as text: jsdom runs no cascade, so the popup rule
// below is pinned by reading it. Vitest runs from web/, the package root.
const componentsCss = readFileSync(resolve(process.cwd(), "src/styles/components.css"), "utf8");

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
const separateKeyButton = () => screen.getByRole("button", { name: "Use a separate key for the chat" });
const keyField = () => screen.getByLabelText("Anthropic key for the planning chat");

// Save is unavailable through aria-disabled, never the disabled attribute: a
// natively disabled button cannot hold keyboard focus, so a save that empties
// the form would drop the GM to the top of the page.
function expectSaveUnavailable() {
  expect(saveButton()).toHaveAttribute("aria-disabled", "true");
  expect(saveButton()).not.toBeDisabled();
}
function expectSaveAvailable() {
  expect(saveButton()).toHaveAttribute("aria-disabled", "false");
}

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
    expectSaveUnavailable();
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
    expectSaveUnavailable();
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
    expectSaveUnavailable();

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
    expectSaveUnavailable();

    fireEvent.change(key, { target: { value: "sk-ant-chat-only-4321" } });
    expectSaveAvailable();
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
    expectSaveUnavailable();
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

  // ── Keyboard focus ───────────────────────────────────────────────────────
  // Three of the card's own actions remove or disable the control that has
  // focus. Each must hand focus on, or a keyboard or screen-reader user lands
  // on <body>, at the top of Setup.

  it("moves focus into the key field that 'use a separate key' reveals", () => {
    renderCard({
      effective: effective({ provider: "anthropic", model: "claude-sonnet-5-5", source: ChatLlmSource.CHOICE }),
    });
    separateKeyButton().focus();
    fireEvent.click(separateKeyButton());
    expect(keyField()).toHaveFocus();
  });

  it("returns focus to 'use a separate key' when the key field is cancelled", () => {
    renderCard({
      effective: effective({ provider: "anthropic", model: "claude-sonnet-5-5", source: ChatLlmSource.CHOICE }),
    });
    fireEvent.click(separateKeyButton());
    const cancel = screen.getByRole("button", { name: "Cancel" });
    cancel.focus();
    fireEvent.click(cancel);
    expect(separateKeyButton()).toHaveFocus();
  });

  it("does not pull focus into the key field a provider without a key opens", async () => {
    renderCard({
      effective: effective({}, { anthropic: { keySource: ChatLlmKeySource.NONE } }),
    });
    await choose(/^Anthropic · needs a key/);
    // The field appeared because of the select, where the GM still is.
    expect(await screen.findByLabelText("Anthropic key for the planning chat")).not.toHaveFocus();
  });

  it("keeps focus on Save through a save, although nothing is left to save afterwards", async () => {
    let finish: () => void = () => {};
    const onSave = vi.fn(() => new Promise<void>((resolve) => (finish = resolve)));
    renderCard({ onSave });
    await choose(/^Anthropic/);

    saveButton().focus();
    fireEvent.click(saveButton());
    await waitFor(() => expect(onSave).toHaveBeenCalledTimes(1));
    // While the save runs the button is unavailable, yet still the focused element.
    expectSaveUnavailable();
    expect(saveButton()).toHaveFocus();
    // A second activation while busy sends nothing.
    fireEvent.click(saveButton());
    expect(onSave).toHaveBeenCalledTimes(1);

    finish();
    await waitFor(() => expect(screen.queryByText("Not saved yet.")).not.toBeInTheDocument());
    expectSaveUnavailable();
    expect(saveButton()).toHaveFocus();
  });

  it("moves focus to Save when saving from the key field removes that field", async () => {
    const { onSave } = renderCard({
      effective: effective({ provider: "anthropic", model: "claude-sonnet-5-5", source: ChatLlmSource.CHOICE }),
    });
    fireEvent.click(separateKeyButton());
    const key = keyField();
    expect(key).toHaveFocus();
    fireEvent.change(key, { target: { value: "test-anthropic-separate-7777" } });
    fireEvent.keyDown(key, { key: "Enter" });

    await waitFor(() => expect(onSave).toHaveBeenCalledWith("anthropic", "", "test-anthropic-separate-7777"));
    await waitFor(() => expect(key).not.toBeInTheDocument());
    expect(saveButton()).toHaveFocus();
  });

  it("leaves focus in the key field when a save from it is refused", async () => {
    const onSave = vi.fn(() => Promise.reject(new ConnectError("secret is not a valid key", Code.InvalidArgument)));
    renderCard({
      effective: effective({ provider: "anthropic", model: "claude-sonnet-5-5", source: ChatLlmSource.CHOICE }),
      onSave,
    });
    fireEvent.click(separateKeyButton());
    fireEvent.change(keyField(), { target: { value: "test-anthropic-wrong-0000" } });
    fireEvent.keyDown(keyField(), { key: "Enter" });

    expect(await screen.findByRole("alert")).toHaveTextContent(/couldn't save/i);
    // The key is still there to correct, and so is the GM.
    expect(keyField()).toHaveFocus();
  });

  it("does nothing when Save is activated with nothing to save", () => {
    const { onSave, onReset } = renderCard();
    expectSaveUnavailable();
    fireEvent.click(saveButton());
    expect(onSave).not.toHaveBeenCalled();
    expect(onReset).not.toHaveBeenCalled();
  });

  it("announces what answers after a save: the status block is a live region", () => {
    renderCard();
    // role=status is a polite live region read as a whole, so a save that
    // changes "Answers with …" is confirmed without sight of the screen.
    expect(screen.getByTestId("chat-model-status")).toHaveAttribute("role", "status");
  });

  it("asks for the key in the words the other key rows use", async () => {
    renderCard({
      effective: effective({ provider: "anthropic", model: "claude-sonnet-5-5", source: ChatLlmSource.CHOICE }),
    });
    fireEvent.click(separateKeyButton());
    // "Paste a Anthropic API key" was the wrong article for this very provider.
    expect(keyField()).toHaveAttribute("placeholder", "Paste your Anthropic API key");
  });

  it("lists its long 'Same as voice' label in a popup that wraps instead of leaving a phone screen", async () => {
    // The label carries the voice model id, which is free text and can be long.
    // Radix sizes the popup to its longest label, so at 375px the popup was
    // wider than the screen and the label's tail was cut off. jsdom lays
    // nothing out, so this pins the two halves of the fix that can drift
    // apart unnoticed: the rule that bounds the popup, and the fact that the
    // open list is the element that rule selects.
    renderCard({
      voice: create(EffectiveLlmSchema, { provider: "groq", model: "meta-llama/llama-4-maverick-17b-128e-instruct" }),
    });
    fireEvent.keyDown(providerSelect(), { key: "Enter" });
    const list = await screen.findByRole("listbox");
    expect(list).toHaveClass("gx-select__content");
    expect(
      within(list).getByRole("option", {
        name: "Same as voice (Groq / meta-llama/llama-4-maverick-17b-128e-instruct)",
      }),
    ).toHaveClass("gx-select__item");

    expect(componentsCss).toMatch(
      /\.gx-select__content\[role="listbox"\]\s*\{\s*max-width:\s*var\(--radix-select-content-available-width\);/,
    );
    expect(componentsCss).toMatch(/\.gx-select__content\[role="listbox"\] \.gx-select__item\s*\{\s*overflow-wrap:\s*anywhere;/);
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
    // Removing a stored key cannot be undone: the note is styled as a warning,
    // not as the muted "Not saved yet." it replaces.
    expect(screen.getByText(/removes the key saved for the chat/i)).toHaveClass("gx-chat-model__warning");
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
    expectSaveAvailable();
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
