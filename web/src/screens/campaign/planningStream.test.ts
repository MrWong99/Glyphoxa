import { afterEach, describe, expect, it } from "vitest";
import { waitFor } from "@testing-library/react";
import { Code, ConnectError } from "@connectrpc/connect";

import { makeQueryClient } from "@/lib/queryClient";
import { message, mockChat, thread } from "./planningTestKit";
import {
  classifyExchangeError,
  exchangeKey,
  exchangeView,
  getPlanningState,
  resetPlanningStreams,
  settleExchange,
  startExchange,
  stopExchange,
  stopExchangesOutside,
  streamingExchange,
  type Exchange,
} from "./planningStream";

afterEach(() => resetPlanningStreams());

const CAMPAIGN = "c-1";

function exchange(over: Partial<Exchange> = {}): Exchange {
  return {
    campaignId: CAMPAIGN,
    threadId: "t",
    userMessage: "Who guards the docks?",
    reply: "",
    tools: [],
    status: "streaming",
    error: null,
    baseCount: 2,
    reusesTurn: false,
    assistantMessageId: null,
    noticeDismissed: false,
    draftReturned: false,
    ...over,
  };
}

const before = [message("1", "user", "Earlier"), message("2", "assistant", "Answer")];

describe("classifyExchangeError", () => {
  it("tells the refusals apart by reason and code", () => {
    const allowance = new ConnectError("x", Code.ResourceExhausted, { "Glyphoxa-Error-Reason": "allowance_exhausted" });
    expect(classifyExchangeError(allowance).kind).toBe("allowance");
    expect(classifyExchangeError(new ConnectError("rate limited", Code.ResourceExhausted)).kind).toBe("provider");
    expect(classifyExchangeError(new ConnectError("check the key", Code.Unavailable))).toEqual({
      kind: "provider",
      message: "check the key",
    });
    expect(classifyExchangeError(new ConnectError("no key", Code.FailedPrecondition)).kind).toBe("noKey");
    expect(classifyExchangeError(new ConnectError("thread not found", Code.NotFound)).kind).toBe("gone");
    expect(classifyExchangeError(new Error("network")).kind).toBe("generic");
  });
});

describe("exchangeView", () => {
  it("draws the pending turn until the cache has it, then only the reply", () => {
    expect(exchangeView(exchange({ reply: "The" }), before)).toMatchObject({ showUser: true, showReply: true, settled: false });
    const withUser = [...before, message("3", "user", "Who guards the docks?")];
    expect(exchangeView(exchange({ reply: "The" }), withUser)).toMatchObject({ showUser: false, showReply: true });
  });

  it("settles a done exchange once its reply id is in the cache", () => {
    const done = exchange({ status: "done", reply: "The watch.", assistantMessageId: "4" });
    const stale = [...before, message("3", "user", "Who guards the docks?")];
    expect(exchangeView(done, stale)).toMatchObject({ showReply: true, settled: false });
    const fresh = [...stale, message("4", "assistant", "The watch.")];
    expect(exchangeView(done, fresh)).toMatchObject({ showUser: false, showReply: false, persistedReplyId: "4", settled: true });
  });

  it("finds a stopped reply by position after the user turn", () => {
    const stopped = exchange({ status: "stopped", reply: "The wa" });
    const fresh = [...before, message("3", "user", "Who guards the docks?"), message("4", "assistant", "The wa")];
    expect(exchangeView(stopped, fresh)).toMatchObject({ showReply: false, persistedReplyId: "4", settled: true });
  });

  it("knows a retried turn is the existing row", () => {
    const dangling = [...before, message("3", "user", "Who guards the docks?")];
    const retry = exchange({ baseCount: 3, reusesTurn: true, reply: "x" });
    expect(exchangeView(retry, dangling)).toMatchObject({ showUser: false });
  });

  it("keeps a failed exchange's text after the notice is closed", () => {
    const withUser = [...before, message("3", "user", "Who guards the docks?")];
    const failed = exchange({ status: "failed", reply: "The docks", error: { kind: "provider", message: "x" } });
    expect(exchangeView(failed, withUser).settled).toBe(false);
    expect(exchangeView({ ...failed, noticeDismissed: true }, withUser)).toMatchObject({ showReply: true, settled: false });
    expect(exchangeView({ ...failed, reply: "", noticeDismissed: true }, withUser).settled).toBe(true);
  });
});

describe("the exchange store", () => {
  const start = (chat: ReturnType<typeof mockChat>, threadId = "t-1", message = "Who guards the docks?") =>
    startExchange({ transport: chat.transport, queryClient: makeQueryClient(), campaignId: CAMPAIGN, threadId, message, persisted: [] });

  it("reads the stream to its end with nobody watching", async () => {
    const chat = mockChat({ threads: [thread("t-1", "Docks", new Date())], scripts: [{ deltas: ["The ", "watch."], tools: [{ name: "find_node" }] }] });
    start(chat);
    const key = exchangeKey(CAMPAIGN, "t-1");
    await waitFor(() => expect(getPlanningState().exchanges[key]?.status).toBe("done"));
    const ex = getPlanningState().exchanges[key];
    expect(ex.reply).toBe("The watch.");
    expect(ex.assistantMessageId).not.toBeNull();
    // The chips are kept for the persisted reply.
    expect(getPlanningState().toolsByMessage[ex.assistantMessageId!]).toEqual([{ toolName: "find_node", isError: false }]);

    settleExchange(CAMPAIGN, "t-1", chat.messages.get("t-1")!);
    expect(getPlanningState().exchanges[key]).toBeUndefined();
  });

  it("runs one exchange per campaign at a time", async () => {
    const chat = mockChat({ threads: [thread("t-1", "A", new Date()), thread("t-2", "B", new Date())], scripts: [{ deltas: ["x"], hold: true }] });
    start(chat, "t-1");
    start(chat, "t-2");
    await waitFor(() => expect(chat.exchanges).toHaveLength(1));
    expect(streamingExchange(getPlanningState(), CAMPAIGN)?.threadId).toBe("t-1");
    expect(getPlanningState().exchanges[exchangeKey(CAMPAIGN, "t-2")]).toBeUndefined();
  });

  it("Stop aborts the request and keeps what streamed", async () => {
    const chat = mockChat({ threads: [thread("t-1", "A", new Date())], scripts: [{ deltas: ["The wa"], hold: true }] });
    start(chat);
    const key = exchangeKey(CAMPAIGN, "t-1");
    await waitFor(() => expect(getPlanningState().exchanges[key]?.reply).toBe("The wa"));
    stopExchange(CAMPAIGN);
    expect(getPlanningState().exchanges[key]).toMatchObject({ status: "stopped", reply: "The wa" });
    await waitFor(() => expect(chat.aborted).toEqual([true]));
  });

  it("a campaign change stops and forgets the other campaign's exchanges", async () => {
    const chat = mockChat({ threads: [thread("t-1", "A", new Date())], scripts: [{ deltas: ["x"], hold: true }] });
    start(chat);
    await waitFor(() => expect(chat.exchanges).toHaveLength(1));
    stopExchangesOutside(CAMPAIGN);
    expect(chat.aborted).toEqual([false]);
    stopExchangesOutside("c-2");
    await waitFor(() => expect(chat.aborted).toEqual([true]));
    expect(getPlanningState().exchanges).toEqual({});
  });

  it("records a failure with its kind and the server's sentence", async () => {
    const chat = mockChat({
      threads: [thread("t-1", "A", new Date())],
      scripts: [{ refuse: new ConnectError("no LLM provider is available", Code.FailedPrecondition) }],
    });
    start(chat);
    const key = exchangeKey(CAMPAIGN, "t-1");
    await waitFor(() => expect(getPlanningState().exchanges[key]?.status).toBe("failed"));
    expect(getPlanningState().exchanges[key].error).toEqual({ kind: "noKey", message: "no LLM provider is available" });
  });
});
