import { describe, it, expect } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";

import {
  ChatService,
  PlanningThreadSchema,
  PlanningMessageSchema,
  ListPlanningThreadsResponseSchema,
  GetPlanningThreadResponseSchema,
  CreatePlanningThreadResponseSchema,
  PlanningExchangeResponseSchema,
  PlanningTextDeltaSchema,
  PlanningExchangeDoneSchema,
  type PlanningMessage,
  type PlanningExchangeRequest,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { PlanningPanel } from "./PlanningPanel";

const CAMPAIGN_ID = "c-1";

const threadRecent = create(PlanningThreadSchema, {
  id: "t-recent",
  campaignId: CAMPAIGN_ID,
  title: "Harbour heist prep",
  updatedAt: timestampFromDate(new Date("2026-08-12T20:00:00Z")),
});
const threadOlder = create(PlanningThreadSchema, {
  id: "t-older",
  campaignId: CAMPAIGN_ID,
  title: "Act two loose ends",
  updatedAt: timestampFromDate(new Date("2026-08-01T18:00:00Z")),
});

const recentMessages = [
  create(PlanningMessageSchema, {
    id: "m-1",
    role: "user",
    content: "What did the players learn at the lighthouse?",
    createdAt: timestampFromDate(new Date("2026-08-12T19:59:00Z")),
  }),
  create(PlanningMessageSchema, {
    id: "m-2",
    role: "assistant",
    content: "They learned the keeper is a smuggler.",
    createdAt: timestampFromDate(new Date("2026-08-12T20:00:00Z")),
  }),
];

// mockTransport wires the ChatService against in-memory threads/messages. The
// planningExchange stream is an async generator held open on `gate` so a test
// can observe the mid-stream (disabled) composer before releasing the done frame.
function mockTransport() {
  const messagesByThread = new Map<string, PlanningMessage[]>([
    ["t-recent", [...recentMessages]],
    ["t-older", []],
  ]);
  const exchangeCalls: PlanningExchangeRequest[] = [];
  let release = () => {};
  const gate = new Promise<void>((r) => {
    release = r;
  });

  const transport = createRouterTransport(({ service }) => {
    service(ChatService, {
      listPlanningThreads: () =>
        create(ListPlanningThreadsResponseSchema, { threads: [threadRecent, threadOlder] }),
      getPlanningThread: (req) =>
        create(GetPlanningThreadResponseSchema, {
          thread: req.threadId === "t-older" ? threadOlder : threadRecent,
          messages: messagesByThread.get(req.threadId) ?? [],
        }),
      createPlanningThread: () =>
        create(CreatePlanningThreadResponseSchema, { thread: threadRecent }),
      async *planningExchange(req) {
        exchangeCalls.push(req);
        yield create(PlanningExchangeResponseSchema, {
          frame: { case: "text", value: create(PlanningTextDeltaSchema, { delta: "Start with " }) },
        });
        await gate;
        const assistant = create(PlanningMessageSchema, {
          id: "m-new-a",
          role: "assistant",
          content: "Start with the docks.",
        });
        messagesByThread.get(req.threadId)?.push(
          create(PlanningMessageSchema, { id: "m-new-u", role: "user", content: req.message }),
          assistant,
        );
        yield create(PlanningExchangeResponseSchema, {
          frame: {
            case: "done",
            value: create(PlanningExchangeDoneSchema, {
              assistantMessage: assistant,
              thread: threadRecent,
            }),
          },
        });
      },
    });
  });
  return { transport, exchangeCalls, release: () => release() };
}

function renderPanel() {
  const ctx = mockTransport();
  render(
    <Providers transport={ctx.transport} queryClient={makeQueryClient()}>
      <PlanningPanel campaignId={CAMPAIGN_ID} />
    </Providers>,
  );
  return ctx;
}

describe("PlanningPanel", () => {
  it("renders the thread list and the most recent thread's messages", async () => {
    renderPanel();

    // Both threads render, server order kept (most recently touched first).
    expect(await screen.findByText("Harbour heist prep")).toBeInTheDocument();
    expect(screen.getByText("Act two loose ends")).toBeInTheDocument();

    // The first (most recent) thread auto-selects and its messages show in order.
    expect(await screen.findByText(/What did the players learn/)).toBeInTheDocument();
    expect(screen.getByText(/the keeper is a smuggler/)).toBeInTheDocument();
  });

  it("sends on Enter, renders the pending message immediately and disables the composer while streaming", async () => {
    const { exchangeCalls, release } = renderPanel();
    await screen.findByText(/the keeper is a smuggler/);

    const input = screen.getByRole("textbox", { name: /message to the butler/i });
    const send = screen.getByRole("button", { name: /send/i });

    // Nothing typed yet — sending is offered but disabled.
    expect(send).toBeDisabled();

    fireEvent.change(input, { target: { value: "Where should the session open?" } });
    expect(send).not.toBeDisabled();
    fireEvent.keyDown(input, { key: "Enter" });

    // The pending user message renders immediately; the stream is now open and
    // the composer is locked until the exchange finishes.
    expect(await screen.findByText("Where should the session open?")).toBeInTheDocument();
    expect(await screen.findByText(/Start with/)).toBeInTheDocument();
    expect(input).toBeDisabled();
    expect(screen.getByRole("button", { name: /send/i })).toBeDisabled();
    expect(exchangeCalls).toHaveLength(1);
    expect(exchangeCalls[0].threadId).toBe("t-recent");
    expect(exchangeCalls[0].message).toBe("Where should the session open?");

    // The done frame lands: the thread re-reads (persisted reply visible) and
    // the composer unlocks.
    release();
    expect(await screen.findByText("Start with the docks.")).toBeInTheDocument();
    await waitFor(() => expect(input).not.toBeDisabled());
  });
});
