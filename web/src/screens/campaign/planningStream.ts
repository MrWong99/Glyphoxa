import { useSyncExternalStore } from "react";
import { Code, ConnectError, createClient, type Transport } from "@connectrpc/connect";
import type { QueryClient } from "@tanstack/react-query";

import { ChatService, type PlanningMessage } from "@gen/glyphoxa/management/v1/management_pb";
import { errorMessage, isAllowanceExhausted } from "@/lib/connectError";
import { invalidateMethodQueries } from "@/lib/queryClient";

// The Planning chat's in-flight exchanges (ADR-0062 amendment 2026-11-26).
//
// An exchange used to live in the panel's component state and was aborted when
// the panel unmounted, so a GM who glanced at the World wiki mid-reply lost the
// reply. It lives here instead, at module level and keyed by campaign and
// thread: the stream keeps reading while the Planning view is closed, and the
// panel picks it up again when it remounts. Only two things end one early:
// Stop (the GM's choice; the server keeps the partial reply) and a change of
// Active Campaign (stopExchangesOutside).
//
// What is persisted stays the cache's business: once the server has a row, the
// thread query shows it and the exchange stops drawing its own copy
// (exchangeView). The exchange record only holds what the cache cannot know
// yet — the pending turn, the streamed text, the tool chips, and how it ended.

export type ToolChip = { toolName: string; isError: boolean };

/**
 * Why an exchange failed, as the GM needs to act on it:
 *   allowance  the plan's monthly allowance is spent (nothing to retry)
 *   noKey      no AI model key is reachable for the chat (Setup)
 *   provider   the AI provider refused or failed (rate limit, rejected key, outage)
 *   gone       the conversation no longer exists
 *   generic    anything else (a dropped connection, an unexpected server error)
 */
export type ExchangeErrorKind = "allowance" | "noKey" | "provider" | "gone" | "generic";

export type ExchangeStatus = "streaming" | "done" | "stopped" | "failed";

export type Exchange = {
  campaignId: string;
  threadId: string;
  userMessage: string;
  reply: string;
  tools: ToolChip[];
  status: ExchangeStatus;
  /** The server's own sentence and its kind, when status is "failed". */
  error: { kind: ExchangeErrorKind; message: string } | null;
  /**
   * How many persisted messages the thread had when this exchange was sent;
   * the exchange's user turn is the row at that index (or the one before it
   * when the send retried a dangling turn — reusesTurn).
   */
  baseCount: number;
  /** The send repeated a trailing user turn that never got a reply: the server reuses that row. */
  reusesTurn: boolean;
  /** The persisted reply's id, from the done frame. */
  assistantMessageId: string | null;
  /** The GM closed the failure notice. The streamed text stays. */
  noticeDismissed: boolean;
  /** The refused message went back into the composer (allowance refusals). */
  draftReturned: boolean;
};

type State = {
  exchanges: Readonly<Record<string, Exchange>>;
  /** Tool chips of finished replies, by assistant message id — kept for the page session. */
  toolsByMessage: Readonly<Record<string, ToolChip[]>>;
  /** Persisted replies the GM stopped part-way, for the page session. */
  stoppedMessages: Readonly<Record<string, true>>;
};

const EMPTY: State = { exchanges: {}, toolsByMessage: {}, stoppedMessages: {} };

let state: State = EMPTY;
const listeners = new Set<() => void>();
const controllers = new Map<string, AbortController>();
const timers = new Set<ReturnType<typeof setTimeout>>();

function emit(next: State) {
  state = next;
  for (const l of listeners) l();
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

export function getPlanningState(): State {
  return state;
}

/** The exchanges store, re-rendering on every change. */
export function usePlanningStreams(): State {
  return useSyncExternalStore(subscribe, getPlanningState, getPlanningState);
}

export function exchangeKey(campaignId: string, threadId: string): string {
  return `${campaignId}:${threadId}`;
}

function patch(key: string, change: Partial<Exchange>) {
  const cur = state.exchanges[key];
  if (!cur) return;
  emit({ ...state, exchanges: { ...state.exchanges, [key]: { ...cur, ...change } } });
}

function drop(key: string) {
  if (!state.exchanges[key]) return;
  const exchanges = { ...state.exchanges };
  delete exchanges[key];
  emit({ ...state, exchanges });
}

/** The exchange still streaming in campaign, if any — one at a time per campaign. */
export function streamingExchange(s: State, campaignId: string): Exchange | null {
  for (const ex of Object.values(s.exchanges)) {
    if (ex.campaignId === campaignId && ex.status === "streaming") return ex;
  }
  return null;
}

/**
 * classifyExchangeError maps a failed exchange onto the kind the panel shows.
 * The allowance refusal is told apart by its REASON, not by its code:
 * ResourceExhausted is also what a provider's own 429 arrives as, and telling
 * the GM their plan is used up when their provider rate-limited them sends
 * them to the wrong fix.
 */
export function classifyExchangeError(err: unknown): { kind: ExchangeErrorKind; message: string } {
  const ce = ConnectError.from(err);
  const message = errorMessage(ce);
  if (isAllowanceExhausted(ce)) return { kind: "allowance", message };
  switch (ce.code) {
    case Code.FailedPrecondition:
      return { kind: "noKey", message };
    case Code.NotFound:
      return { kind: "gone", message };
    case Code.Unavailable:
    case Code.ResourceExhausted:
      return { kind: "provider", message };
    default:
      return { kind: "generic", message };
  }
}

const invalidateThread = (qc: QueryClient) => invalidateMethodQueries(qc, ChatService.method.getPlanningThread);
const invalidateThreads = (qc: QueryClient) => invalidateMethodQueries(qc, ChatService.method.listPlanningThreads);

/**
 * After a Stop the server writes the partial reply once it notices the
 * cancel, which can be a moment after the browser's abort. The thread is
 * re-read now and twice more, so the persisted row replaces the local copy
 * without the GM doing anything.
 */
export const STOP_REFETCH_DELAYS_MS = [0, 700, 2000] as const;

function refetchSoon(qc: QueryClient, delays: readonly number[]) {
  for (const ms of delays) {
    const timer = setTimeout(() => {
      timers.delete(timer);
      void invalidateThread(qc);
      void invalidateThreads(qc);
    }, ms);
    timers.add(timer);
  }
}

export type StartExchange = {
  transport: Transport;
  queryClient: QueryClient;
  campaignId: string;
  threadId: string;
  message: string;
  /** The thread's persisted messages as the panel shows them at send time. */
  persisted: readonly PlanningMessage[];
};

/**
 * startExchange sends one message and reads its stream into the store. It
 * returns at once; the stream runs on whether or not any panel is mounted.
 * A second send while one is streaming in the same campaign is ignored.
 */
export function startExchange({ transport, queryClient, campaignId, threadId, message, persisted }: StartExchange) {
  const text = message.trim();
  if (text === "" || streamingExchange(state, campaignId)) return;
  const key = exchangeKey(campaignId, threadId);

  const last = persisted[persisted.length - 1];
  const reusesTurn = last !== undefined && last.role === "user" && last.content.trim() === text;
  const exchange: Exchange = {
    campaignId,
    threadId,
    userMessage: text,
    reply: "",
    tools: [],
    status: "streaming",
    error: null,
    baseCount: persisted.length,
    reusesTurn,
    assistantMessageId: null,
    noticeDismissed: false,
    draftReturned: false,
  };
  emit({ ...state, exchanges: { ...state.exchanges, [key]: exchange } });

  const abort = new AbortController();
  controllers.set(key, abort);
  void run(key, abort, { transport, queryClient, campaignId, threadId, text });
}

async function run(
  key: string,
  abort: AbortController,
  { transport, queryClient, campaignId, threadId, text }: { transport: Transport; queryClient: QueryClient; campaignId: string; threadId: string; text: string },
) {
  const client = createClient(ChatService, transport);
  // Only this run may write to its record: a later send in the same thread
  // replaces it, and a late frame from this one must not leak into that.
  const mine = () => controllers.get(key) === abort && !abort.signal.aborted;
  let doneId: string | null = null;
  try {
    for await (const res of client.planningExchange({ campaignId, threadId, message: text }, { signal: abort.signal })) {
      if (!mine()) return;
      const frame = res.frame;
      const cur = state.exchanges[key];
      if (!cur) return;
      if (frame.case === "text") {
        patch(key, { reply: cur.reply + frame.value.delta });
      } else if (frame.case === "tool") {
        patch(key, { tools: [...cur.tools, { toolName: frame.value.toolName, isError: frame.value.isError }] });
      } else if (frame.case === "done") {
        doneId = frame.value.assistantMessage?.id ?? "";
      }
    }
    if (!mine()) return;
    controllers.delete(key);
    if (doneId !== null) {
      const cur = state.exchanges[key];
      const toolsByMessage =
        cur && cur.tools.length > 0 && doneId !== "" ? { ...state.toolsByMessage, [doneId]: cur.tools } : state.toolsByMessage;
      emit({
        ...state,
        toolsByMessage,
        exchanges: cur
          ? { ...state.exchanges, [key]: { ...cur, status: "done", assistantMessageId: doneId || null } }
          : state.exchanges,
      });
      // The done frame's rows are persisted: re-read the thread (and the list,
      // for updated_at and an auto-filled title). The local copy keeps drawing
      // until the re-read carries the reply, so the pane never flashes empty.
      await Promise.all([invalidateThread(queryClient), invalidateThreads(queryClient)]);
    } else {
      // The stream closed without a done frame: a failed exchange.
      patch(key, { status: "failed", error: { kind: "generic", message: "" } });
      refetchSoon(queryClient, [0]);
    }
  } catch (err) {
    // Stop (or a campaign change) ended it: stopExchange already recorded that.
    if (abort.signal.aborted || controllers.get(key) !== abort) return;
    controllers.delete(key);
    patch(key, { status: "failed", error: classifyExchangeError(err) });
    // The user turn may have been persisted before the failure (ADR-0062's
    // persist-first order): re-read so the cache says what exists.
    refetchSoon(queryClient, [0]);
  }
}

/** Stops the exchange streaming in this campaign (Stop). The partial reply stays. */
export function stopExchange(campaignId: string, queryClient?: QueryClient) {
  for (const [key, ex] of Object.entries(state.exchanges)) {
    if (ex.campaignId !== campaignId || ex.status !== "streaming") continue;
    controllers.get(key)?.abort();
    controllers.delete(key);
    patch(key, { status: "stopped" });
    if (queryClient) refetchSoon(queryClient, STOP_REFETCH_DELAYS_MS);
  }
}

/** Stops one thread's exchange — its conversation is being deleted. */
export function discardThread(campaignId: string, threadId: string) {
  const key = exchangeKey(campaignId, threadId);
  controllers.get(key)?.abort();
  controllers.delete(key);
  drop(key);
}

/**
 * The Active Campaign changed: every exchange of another campaign is stopped
 * and forgotten. A reply streaming into a campaign the GM has left would land
 * in a conversation they can no longer see from here.
 */
export function stopExchangesOutside(campaignId: string | undefined) {
  let changed = false;
  const exchanges: Record<string, Exchange> = {};
  for (const [key, ex] of Object.entries(state.exchanges)) {
    if (ex.campaignId === campaignId) {
      exchanges[key] = ex;
      continue;
    }
    controllers.get(key)?.abort();
    controllers.delete(key);
    changed = true;
  }
  if (changed) emit({ ...state, exchanges });
}

/** The GM closed the failure notice. What streamed stays visible. */
export function dismissNotice(campaignId: string, threadId: string) {
  patch(exchangeKey(campaignId, threadId), { noticeDismissed: true });
}

/** The refused message was handed back to the composer. */
export function markDraftReturned(campaignId: string, threadId: string) {
  patch(exchangeKey(campaignId, threadId), { draftReturned: true });
}

/** Forgets one thread's finished exchange (its conversation was deleted elsewhere). */
export function forgetExchange(campaignId: string, threadId: string) {
  const key = exchangeKey(campaignId, threadId);
  if (state.exchanges[key]?.status === "streaming") return;
  drop(key);
}

/** What of an exchange still needs drawing next to the persisted messages. */
export type ExchangeView = {
  /** Draw the pending user turn (the cache does not have it). */
  showUser: boolean;
  /** Draw the streamed reply (the cache does not have it). */
  showReply: boolean;
  /** The persisted reply this exchange produced, once the cache has it. */
  persistedReplyId: string | null;
  /** Nothing is left to draw: the cache carries the whole exchange. */
  settled: boolean;
};

/**
 * exchangeView reconciles an exchange with the thread's persisted messages.
 * The user turn is the row at baseCount (baseCount-1 for a reused dangling
 * turn); the reply is the assistant row right after it. While the exchange
 * streams, only the user turn can already be persisted (the server writes it
 * before calling the provider).
 */
export function exchangeView(ex: Exchange, persisted: readonly PlanningMessage[]): ExchangeView {
  const userIndex = ex.reusesTurn ? ex.baseCount - 1 : ex.baseCount;
  const userRow = persisted[userIndex];
  const userPersisted = userRow !== undefined && userRow.role === "user";
  let replyRow: PlanningMessage | undefined;
  if (ex.assistantMessageId) {
    replyRow = persisted.find((m) => m.id === ex.assistantMessageId);
  } else if (ex.status !== "streaming" && userPersisted) {
    const next = persisted[userIndex + 1];
    if (next?.role === "assistant") replyRow = next;
  }
  const showUser = !userPersisted && !ex.draftReturned;
  const showReply = replyRow === undefined && ex.reply !== "";
  const settled =
    ex.status !== "streaming" &&
    !showUser &&
    !showReply &&
    (ex.status === "done" || ex.status === "stopped" ? replyRow !== undefined || ex.reply === "" : ex.noticeDismissed);
  return { showUser, showReply, persistedReplyId: replyRow?.id ?? null, settled };
}

/**
 * settleExchange retires an exchange the cache now carries in full: its tool
 * chips and its "stopped" mark move onto the persisted reply for the page
 * session, and the record goes.
 */
export function settleExchange(campaignId: string, threadId: string, persisted: readonly PlanningMessage[]) {
  const key = exchangeKey(campaignId, threadId);
  const ex = state.exchanges[key];
  if (!ex) return;
  const view = exchangeView(ex, persisted);
  if (!view.settled) return;
  const exchanges = { ...state.exchanges };
  delete exchanges[key];
  const replyId = view.persistedReplyId;
  emit({
    exchanges,
    toolsByMessage: replyId && ex.tools.length > 0 ? { ...state.toolsByMessage, [replyId]: ex.tools } : state.toolsByMessage,
    stoppedMessages: replyId && ex.status === "stopped" ? { ...state.stoppedMessages, [replyId]: true } : state.stoppedMessages,
  });
}

/** Test seam: aborts everything and empties the store. */
export function resetPlanningStreams() {
  for (const c of controllers.values()) c.abort();
  controllers.clear();
  for (const t of timers) clearTimeout(t);
  timers.clear();
  emit(EMPTY);
}
