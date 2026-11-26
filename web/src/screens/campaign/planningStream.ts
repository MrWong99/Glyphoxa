import { useSyncExternalStore } from "react";
import { Code, ConnectError, createClient, type Transport } from "@connectrpc/connect";
import { createQueryOptions } from "@connectrpc/connect-query";
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
//
// Lining the two up needs a cache that is current when a message goes out, so
// a send waits while the campaign's previous exchange has ended but its
// thread has not been re-read (awaitingRead), and startExchange re-reads a
// copy that may be behind before the message leaves.

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
  /** Tells this exchange apart from a later one in the same thread. */
  id: number;
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
  /**
   * The message went back into the composer: an allowance refusal, or a Stop
   * that landed before the server had saved it.
   */
  draftReturned: boolean;
  /**
   * The stream has ended but the thread has not been re-read since. Until it
   * has, the cache may not hold what this exchange wrote (after a Stop, the
   * server saves the partial reply only once it notices the cancel), and a
   * new message sent now would be lined up against the wrong rows — so the
   * campaign's next send waits (busyExchange).
   */
  awaitingRead: boolean;
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
let lastId = 0;

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

/** patch, but only while the record is still exchange `id` (not a later send in the thread). */
function patchIf(key: string, id: number, change: Partial<Exchange>) {
  if (state.exchanges[key]?.id === id) patch(key, change);
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
 * The exchange that holds sending in campaign: one still streaming, or one
 * that ended and whose thread has not been re-read yet (awaitingRead).
 */
export function busyExchange(s: State, campaignId: string): Exchange | null {
  for (const ex of Object.values(s.exchanges)) {
    if (ex.campaignId === campaignId && (ex.status === "streaming" || ex.awaitingRead)) return ex;
  }
  return null;
}

/**
 * The reason the server puts on a failure it could not put a name to (a
 * storage fault, an unclassified provider error): it is not the provider's
 * configuration, so the panel must not send the GM to Setup for it.
 */
const REASON_UNCLASSIFIED = "unclassified";
// The header the reason travels in (errorReasonHeader in internal/rpc, the
// one lib/connectError reads for the allowance refusal).
const ERROR_REASON_HEADER = "Glyphoxa-Error-Reason";

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
      if (ce.metadata.get(ERROR_REASON_HEADER) === REASON_UNCLASSIFIED) return { kind: "generic", message };
      return { kind: "provider", message };
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

/**
 * Re-reads the thread (and the list) after each delay; once the last re-read
 * has landed, onDone runs.
 */
function refetchSoon(qc: QueryClient, delays: readonly number[], onDone?: () => void) {
  const last = delays.length - 1;
  delays.forEach((ms, i) => {
    const timer = setTimeout(() => {
      timers.delete(timer);
      const reads = Promise.all([invalidateThread(qc), invalidateThreads(qc)]);
      if (i === last && onDone) void reads.finally(onDone);
    }, ms);
    timers.add(timer);
  });
}

/**
 * The thread's rows as the server has them now, when the cached copy may be
 * behind: a re-read is under way or due (a reply just finished, the panel
 * just remounted), or there is no copy yet. null when the cached copy is
 * current, or the read failed (the exchange then goes by what it was given).
 */
async function currentRows(
  transport: Transport,
  qc: QueryClient,
  campaignId: string,
  threadId: string,
): Promise<readonly PlanningMessage[] | null> {
  const options = createQueryOptions(ChatService.method.getPlanningThread, { campaignId, threadId }, { transport });
  const cached = qc.getQueryState(options.queryKey);
  if (cached && cached.status === "success" && cached.fetchStatus === "idle" && !cached.isInvalidated) return null;
  try {
    // A read already in flight is joined, not repeated.
    return (await qc.fetchQuery({ ...options, staleTime: 0 })).messages;
  } catch {
    return null;
  }
}

/** Where an exchange's user turn will be, given the rows the thread has before it. */
function baseOf(persisted: readonly PlanningMessage[], text: string): Pick<Exchange, "baseCount" | "reusesTurn"> {
  const last = persisted[persisted.length - 1];
  return {
    baseCount: persisted.length,
    reusesTurn: last !== undefined && last.role === "user" && last.content.trim() === text,
  };
}

export type StartExchange = {
  transport: Transport;
  queryClient: QueryClient;
  campaignId: string;
  threadId: string;
  message: string;
  /** The thread's persisted messages as the panel shows them at send time. */
  persisted: readonly PlanningMessage[];
  /**
   * persisted is known to be what the server has (a conversation created for
   * this very message): no re-read before sending.
   */
  current?: boolean;
};

/**
 * startExchange sends one message and reads its stream into the store. It
 * returns at once; the stream runs on whether or not any panel is mounted.
 * A send while the campaign is busy (busyExchange) is ignored.
 *
 * The exchange is lined up with the thread's rows by where its user turn
 * lands. When the cached rows may be behind, they are re-read before the
 * message goes out, so that position is the server's, not a stale copy's.
 */
export function startExchange({ transport, queryClient, campaignId, threadId, message, persisted, current = false }: StartExchange) {
  const text = message.trim();
  if (text === "" || busyExchange(state, campaignId)) return;
  const key = exchangeKey(campaignId, threadId);

  const exchange: Exchange = {
    id: ++lastId,
    campaignId,
    threadId,
    userMessage: text,
    reply: "",
    tools: [],
    status: "streaming",
    error: null,
    ...baseOf(persisted, text),
    assistantMessageId: null,
    noticeDismissed: false,
    draftReturned: false,
    awaitingRead: false,
  };
  // A finished exchange of this thread is replaced: what it marked on its
  // persisted reply (tool chips, "stopped") moves there first.
  const prev = state.exchanges[key];
  emit({ ...carryOver(prev, persisted), exchanges: { ...state.exchanges, [key]: exchange } });

  const abort = new AbortController();
  controllers.set(key, abort);
  void run(key, exchange.id, abort, { transport, queryClient, campaignId, threadId, text, current });
}

async function run(
  key: string,
  id: number,
  abort: AbortController,
  {
    transport,
    queryClient,
    campaignId,
    threadId,
    text,
    current,
  }: { transport: Transport; queryClient: QueryClient; campaignId: string; threadId: string; text: string; current: boolean },
) {
  const client = createClient(ChatService, transport);
  // Only this run may write to its record: a later send in the same thread
  // replaces it, and a late frame from this one must not leak into that.
  const mine = () => controllers.get(key) === abort && !abort.signal.aborted;
  let doneId: string | null = null;
  try {
    if (!current) {
      const rows = await currentRows(transport, queryClient, campaignId, threadId);
      if (!mine()) return;
      if (rows) patch(key, baseOf(rows, text));
    }
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
          ? { ...state.exchanges, [key]: { ...cur, status: "done", assistantMessageId: doneId || null, awaitingRead: true } }
          : state.exchanges,
      });
      // The done frame's rows are persisted: re-read the thread (and the list,
      // for updated_at and an auto-filled title). The local copy keeps drawing
      // until the re-read carries the reply, so the pane never flashes empty.
      await Promise.all([invalidateThread(queryClient), invalidateThreads(queryClient)]);
      patchIf(key, id, { awaitingRead: false });
    } else {
      // The stream closed without a done frame: a failed exchange.
      patch(key, { status: "failed", error: { kind: "generic", message: "" }, awaitingRead: true });
      refetchSoon(queryClient, [0], () => patchIf(key, id, { awaitingRead: false }));
    }
  } catch (err) {
    // Stop (or a campaign change) ended it: stopExchange already recorded that.
    if (abort.signal.aborted || controllers.get(key) !== abort) return;
    controllers.delete(key);
    patch(key, { status: "failed", error: classifyExchangeError(err), awaitingRead: true });
    // The user turn may have been persisted before the failure (ADR-0062's
    // persist-first order): re-read so the cache says what exists.
    refetchSoon(queryClient, [0], () => patchIf(key, id, { awaitingRead: false }));
  }
}

/**
 * Stops the exchange streaming in this campaign (Stop). The partial reply
 * stays. The campaign's next send waits until the server's copy of it has
 * been read back, or the last re-read has landed without one.
 */
export function stopExchange(campaignId: string, queryClient?: QueryClient) {
  for (const [key, ex] of Object.entries(state.exchanges)) {
    if (ex.campaignId !== campaignId || ex.status !== "streaming") continue;
    controllers.get(key)?.abort();
    controllers.delete(key);
    patch(key, { status: "stopped", awaitingRead: queryClient !== undefined });
    if (queryClient) refetchSoon(queryClient, STOP_REFETCH_DELAYS_MS, () => patchIf(key, ex.id, { awaitingRead: false }));
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
  /** The cache has the exchange's user turn. */
  userPersisted: boolean;
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
 * The index of the exchange's user turn in persisted, or -1 while the cache
 * does not have it. The turn is the first user row from its expected place
 * on (baseCount, or baseCount-1 for a reused dangling turn) that carries the
 * GM's words — anchored on the words, not on the bare position, so a row
 * written in between (a stopped reply the server saved a moment late) does
 * not shift it onto the wrong turn.
 */
function userTurnIndex(ex: Exchange, persisted: readonly PlanningMessage[]): number {
  const from = Math.max(0, ex.reusesTurn ? ex.baseCount - 1 : ex.baseCount);
  for (let i = from; i < persisted.length; i++) {
    const m = persisted[i];
    if (m.role === "user" && m.content.trim() === ex.userMessage) return i;
  }
  return -1;
}

/**
 * exchangeView reconciles an exchange with the thread's persisted messages:
 * its user turn (userTurnIndex), and its reply — the row the done frame
 * named, or else the assistant row right after the user turn. While the
 * exchange streams, only the user turn can already be persisted (the server
 * writes it before calling the provider).
 */
export function exchangeView(ex: Exchange, persisted: readonly PlanningMessage[]): ExchangeView {
  const userIndex = userTurnIndex(ex, persisted);
  const userPersisted = userIndex >= 0;
  let replyRow: PlanningMessage | undefined;
  if (ex.assistantMessageId) {
    replyRow = persisted.find((m) => m.id === ex.assistantMessageId);
  } else if (ex.status !== "streaming" && userPersisted) {
    const next = persisted[userIndex + 1];
    if (next?.role === "assistant") replyRow = next;
  }
  const showUser = !userPersisted && !ex.draftReturned;
  const showReply = replyRow === undefined && ex.reply !== "";
  // A stop with nothing streamed may still gain a reply row (the server can
  // have sent text the browser never read): settle it only once re-read.
  const settled =
    ex.status !== "streaming" &&
    !showUser &&
    !showReply &&
    (ex.status === "failed" ? ex.noticeDismissed : replyRow !== undefined || (ex.reply === "" && !ex.awaitingRead));
  return { userPersisted, showUser, showReply, persistedReplyId: replyRow?.id ?? null, settled };
}

/**
 * What a retiring exchange leaves on its persisted reply for the page
 * session: its tool chips and its "stopped" mark.
 */
function carryOver(ex: Exchange | undefined, persisted: readonly PlanningMessage[]): Omit<State, "exchanges"> {
  const { toolsByMessage, stoppedMessages } = state;
  if (!ex || ex.status === "streaming") return { toolsByMessage, stoppedMessages };
  const replyId = exchangeView(ex, persisted).persistedReplyId;
  if (!replyId) return { toolsByMessage, stoppedMessages };
  return {
    toolsByMessage: ex.tools.length > 0 ? { ...toolsByMessage, [replyId]: ex.tools } : toolsByMessage,
    stoppedMessages: ex.status === "stopped" ? { ...stoppedMessages, [replyId]: true } : stoppedMessages,
  };
}

/**
 * settleExchange retires an exchange the cache now carries in full: its tool
 * chips and its "stopped" mark move onto the persisted reply for the page
 * session, and the record goes.
 */
export function settleExchange(campaignId: string, threadId: string, persisted: readonly PlanningMessage[]) {
  const key = exchangeKey(campaignId, threadId);
  const ex = state.exchanges[key];
  if (!ex || !exchangeView(ex, persisted).settled) return;
  const exchanges = { ...state.exchanges };
  delete exchanges[key];
  emit({ ...carryOver(ex, persisted), exchanges });
}

/** Test seam: aborts everything and empties the store. */
export function resetPlanningStreams() {
  for (const c of controllers.values()) c.abort();
  controllers.clear();
  for (const t of timers) clearTimeout(t);
  timers.clear();
  emit(EMPTY);
}
