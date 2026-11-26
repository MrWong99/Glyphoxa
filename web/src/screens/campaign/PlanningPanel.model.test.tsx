import { describe, it, expect, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";

import {
  ChatService,
  ProviderService,
  ChatLlmKeySource,
  ChatLlmSource,
  EffectiveChatLlmSchema,
  ListProviderConfigsResponseSchema,
  ListPlanningThreadsResponseSchema,
  GetPlanningThreadResponseSchema,
  PlanningThreadSchema,
  PlanningExchangeResponseSchema,
  PlanningExchangeDoneSchema,
  PlanningMessageSchema,
  type PlanningExchangeRequest,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { PlanningPanel } from "./PlanningPanel";
import { resetPlanningStreams } from "./planningStream";

// The Planning panel's footer (ADR-0062 amendment): which model answers, from
// the server's own resolution, with a way to Setup to change it — and a
// warning before sending when that model has no key to run on.

const thread = create(PlanningThreadSchema, { id: "t-1", campaignId: "c-1", title: "Harbour heist prep" });

type Config = "pending" | "error" | { provider: string; model: string; keySource?: ChatLlmKeySource };

function renderPanel(config: Config, lang?: "de") {
  const exchanges: PlanningExchangeRequest[] = [];
  const transport = createRouterTransport(({ service }) => {
    service(ChatService, {
      listPlanningThreads: () => create(ListPlanningThreadsResponseSchema, { threads: [thread] }),
      getPlanningThread: () => create(GetPlanningThreadResponseSchema, { thread, messages: [] }),
      async *planningExchange(req) {
        exchanges.push(req);
        yield create(PlanningExchangeResponseSchema, {
          frame: {
            case: "done",
            value: create(PlanningExchangeDoneSchema, {
              assistantMessage: create(PlanningMessageSchema, { id: "m-a", role: "assistant", content: "Done." }),
              thread,
            }),
          },
        });
      },
    });
    service(ProviderService, {
      listProviderConfigs: () => {
        if (config === "pending") return new Promise<never>(() => {});
        if (config === "error") throw new ConnectError("database unavailable", Code.Unavailable);
        return create(ListProviderConfigsResponseSchema, {
          effectiveChatLlm: create(EffectiveChatLlmSchema, {
            provider: config.provider,
            model: config.model,
            source: ChatLlmSource.CHOICE,
            keySource: config.keySource ?? ChatLlmKeySource.PLATFORM,
          }),
        });
      },
    });
  });
  // The stored display language wins over the browser's (i18n detectLang).
  if (lang) localStorage.setItem("gx-lang", lang);
  render(
    <Providers transport={transport} queryClient={makeQueryClient()}>
      <PlanningPanel campaignId="c-1" />
    </Providers>,
  );
  return { exchanges };
}

const composer = () => screen.getByRole("textbox", { name: /message to the butler|nachricht an den butler/i });

afterEach(() => {
  window.history.replaceState(null, "", "/");
  localStorage.removeItem("gx-lang");
  // The exchange store is module state (planningStream.ts): one test's stream
  // must not gate the next one's composer.
  resetPlanningStreams();
});

describe("PlanningPanel model footer", () => {
  it("names the provider and model that answer", async () => {
    renderPanel({ provider: "anthropic", model: "claude-sonnet-5-5" });
    const footer = await screen.findByTestId("planning-model");
    expect(footer).toHaveTextContent("Answering with Anthropic · claude-sonnet-5-5");
    // No warning when a key is reachable.
    expect(screen.queryByText(/has no ai model key yet/i)).not.toBeInTheDocument();
  });

  it("links to the Setup screen's planning chat card of the current table", async () => {
    window.history.replaceState(null, "", "/t/dev/campaign/planning");
    renderPanel({ provider: "groq", model: "openai/gpt-oss-120b" });
    const link = await screen.findByRole("link", { name: "Change the planning chat model in Setup" });
    expect(link).toHaveTextContent("Change");
    expect(link).toHaveAttribute("href", "/t/dev/configuration#planning-chat");
  });

  it("has no footer while the read is pending, and the composer still sends", async () => {
    const { exchanges } = renderPanel("pending");
    await screen.findByText("What can the Butler do?");
    expect(screen.queryByTestId("planning-model")).not.toBeInTheDocument();
    // The send hint is still there on its own.
    expect(screen.getByText(/enter sends/i)).toBeInTheDocument();

    fireEvent.change(composer(), { target: { value: "What next?" } });
    fireEvent.keyDown(composer(), { key: "Enter" });
    await waitFor(() => expect(exchanges).toHaveLength(1));
  });

  it("has no footer when the read failed, without an error in the chat", async () => {
    const { exchanges } = renderPanel("error");
    await screen.findByText("What can the Butler do?");
    // Give the failed side read time to settle: it must stay silent.
    await waitFor(() => expect(screen.queryByTestId("planning-model")).not.toBeInTheDocument());
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(screen.queryByText(/database unavailable/i)).not.toBeInTheDocument();

    fireEvent.change(composer(), { target: { value: "What next?" } });
    fireEvent.keyDown(composer(), { key: "Enter" });
    await waitFor(() => expect(exchanges).toHaveLength(1));
  });

  it("warns before sending when the chat's model has no key to run on", async () => {
    renderPanel({ provider: "anthropic", model: "claude-sonnet-5-5", keySource: ChatLlmKeySource.NONE });
    const warning = await screen.findByText("The planning chat has no AI model key yet. Add one in Setup.");
    expect(warning).toHaveTextContent("The planning chat has no AI model key yet. Add one in Setup.");
    // A standing notice, shown before anything was sent: it must not read like
    // the reply to a failed attempt.
    expect(warning).not.toHaveTextContent(/try again/i);
    // The footer still names the model, and the way to fix it is right there.
    expect(screen.getByTestId("planning-model")).toHaveTextContent("Answering with Anthropic · claude-sonnet-5-5");
    expect(screen.getByRole("link", { name: "Change the planning chat model in Setup" })).toBeInTheDocument();
    // The composer is not locked: the server decides, and a key may be seconds away.
    expect(composer()).toBeEnabled();
  });

  it("speaks German", async () => {
    renderPanel({ provider: "anthropic", model: "claude-opus-5-5" }, "de");
    const footer = await screen.findByTestId("planning-model");
    expect(footer).toHaveTextContent("Antwortet mit Anthropic · claude-opus-5-5");
    expect(screen.getByRole("link", { name: /in der einrichtung ändern/i })).toHaveTextContent("Ändern");
  });
});
