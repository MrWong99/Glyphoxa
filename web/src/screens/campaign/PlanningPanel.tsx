import { useEffect, useId, useLayoutEffect, useMemo, useRef, useState, type ReactNode, type RefObject } from "react";
import { useQuery, useMutation, useTransport } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { timestampDate } from "@bufbuild/protobuf/wkt";
import type { Timestamp } from "@bufbuild/protobuf/wkt";
import { toast } from "sonner";
import {
  ArrowDown,
  BookOpen,
  CalendarClock,
  Check,
  History,
  Lightbulb,
  ListChecks,
  MessageSquarePlus,
  MessagesSquare,
  MoreHorizontal,
  NotebookPen,
  Pencil,
  Search,
  SendHorizontal,
  Square,
  Trash2,
  TriangleAlert,
  X,
} from "lucide-react";

import { ChatService, ChatLlmKeySource, ProviderService } from "@gen/glyphoxa/management/v1/management_pb";
import type { PlanningMessage, PlanningThread } from "@gen/glyphoxa/management/v1/management_pb";
import { useCan } from "@/app/capabilities";
import { useScreenNav } from "@/app/screenNav";
import { invalidateMethodQueries } from "@/lib/queryClient";
import { errorMessage, isNotFound } from "@/lib/connectError";
import { useI18n, type Lang, type MessageKey, type TFunc } from "@/i18n";
import { Button } from "@/components/ui/Button";
import { ConfirmDialog } from "@/components/ui/ConfirmDialog";
import { CopyButton } from "@/components/ui/CopyButton";
import { Dialog } from "@/components/ui/Dialog";
import { EmptyState } from "@/components/ui/EmptyState";
import { ErrorState } from "@/components/ui/ErrorState";
import { IconButton } from "@/components/ui/IconButton";
import { Markdown } from "@/components/ui/Markdown";
import { Menu, MenuItem, MenuSeparator } from "@/components/ui/Menu";
import { Skeleton } from "@/components/ui/Skeleton";
import { Textarea } from "@/components/ui/Textarea";
import { CHAT_MODEL_ANCHOR } from "@/screens/configuration/chatModelAnchor";
import { llmProviderLabel } from "@/screens/configuration/llmProviders";

import "./planning.css";
import { loadDraft, saveDraft, messageLength, MESSAGE_COUNTER_FROM, MESSAGE_MAX } from "./planningDrafts";
import {
  busyExchange,
  dismissNotice,
  exchangeKey,
  exchangeView,
  forgetExchange,
  markDraftReturned,
  settleExchange,
  startExchange,
  stopExchange,
  stopExchangesOutside,
  streamingExchange,
  usePlanningStreams,
  discardThread,
  type Exchange,
  type ExchangeErrorKind,
  type ToolChip,
} from "./planningStream";

// The Planning panel (#592, ADR-0062) backs the Campaign screen's "Planning"
// view: the Butler as a GM-only prep chat over the ChatService RPCs.
//
//   - Threads are persisted per Campaign; list/create/rename/delete are
//     ordinary unary calls through connect-query. The open thread lives in the
//     URL (?thread=) through useScreenNav, so Back steps through conversations.
//   - "New conversation" writes nothing: it opens an empty composer with
//     starter questions, and the thread is created by the first message.
//   - One exchange streams over the server-streaming PlanningExchange RPC. It
//     lives in planningStream.ts, outside this component, so switching to the
//     World wiki and back keeps the reply streaming (ADR-0062 amendment
//     2026-11-26). Stop ends it; the server keeps what had streamed.
//   - The composer's text is a draft per campaign and thread
//     (planningDrafts.ts), and it stays editable while the Butler replies —
//     only sending waits.

// TITLE_MAX mirrors maxThreadTitleRunes (internal/rpc/chat.go) — the input
// stops where the server would reject.
const TITLE_MAX = 200;

// stamp renders a thread/message timestamp in the display language
// ("Aug 3, 19:30" / "3. Aug., 19:30") — same treatment as the palette (#591).
function stamp(ts: Timestamp | undefined, lang: Lang): string | null {
  if (!ts) return null;
  try {
    return timestampDate(ts).toLocaleString(lang, {
      month: "short",
      day: "numeric",
      hour: "2-digit",
      minute: "2-digit",
    });
  } catch {
    return null;
  }
}

// The planning belt's wire names (internal/planchat/tools.go) rendered as
// friendly labels — "search_transcripts" is internal vocabulary, not GM copy.
// A tool this map doesn't know (a belt addition that outruns the web tier)
// still renders, de-snake_cased, rather than leaking underscores.
const TOOL_LABEL_KEYS: Record<string, MessageKey> = {
  search_transcripts: "chat.toolSearchTranscripts",
  find_node: "chat.toolFindNode",
  appearances_of: "chat.toolAppearancesOf",
  propose_knowledge: "chat.toolProposeKnowledge",
  recap: "chat.toolRecap",
};

function toolLabel(t: TFunc, toolName: string): string {
  const key = TOOL_LABEL_KEYS[toolName];
  return key ? t(key) : toolName.replace(/_/g, " ");
}

// setupChatModelHref is the Setup screen's Planning chat model card, for the
// footer's "Change" link: a real link (it can open in a new tab), built from
// the tenant prefix of the current URL — every app route lives under
// /t/<slug>/. The screen-nav seam has no anchors.
function setupChatModelHref(): string {
  const tenantPrefix = /^\/t\/[^/]+/.exec(window.location.pathname)?.[0] ?? "";
  return `${tenantPrefix}/configuration#${CHAT_MODEL_ANCHOR}`;
}

// ── Starters ────────────────────────────────────────────────────────────────

// One per tool on the planning belt. A prefill starter ends in a blank the GM
// fills: it goes into the composer instead of being sent.
type Starter = { label: MessageKey; tool: MessageKey; icon: ReactNode; prefill?: MessageKey };

const STARTERS: Starter[] = [
  { label: "chat.starterRecap", tool: "chat.starterRecapTool", icon: <History size={16} /> },
  { label: "chat.starterLooseEnds", tool: "chat.starterLooseEndsTool", icon: <ListChecks size={16} /> },
  { label: "chat.starterKnow", tool: "chat.starterKnowTool", icon: <BookOpen size={16} />, prefill: "chat.starterKnowPrefill" },
  {
    label: "chat.starterLastSeen",
    tool: "chat.starterLastSeenTool",
    icon: <CalendarClock size={16} />,
    prefill: "chat.starterLastSeenPrefill",
  },
  { label: "chat.starterHooks", tool: "chat.starterHooksTool", icon: <Lightbulb size={16} /> },
  { label: "chat.starterNote", tool: "chat.starterNoteTool", icon: <NotebookPen size={16} />, prefill: "chat.starterNotePrefill" },
];

// ── Thread groups ───────────────────────────────────────────────────────────

type GroupKey = "today" | "week" | "earlier";
const GROUP_LABEL: Record<GroupKey, MessageKey> = {
  today: "chat.groupToday",
  week: "chat.groupWeek",
  earlier: "chat.groupEarlier",
};
const DAY_MS = 24 * 60 * 60 * 1000;

// groupThreads buckets the list (already newest first) by last activity in the
// GM's local time: today, within the last seven days, earlier.
export function groupThreads(threads: PlanningThread[], now: Date): { key: GroupKey; threads: PlanningThread[] }[] {
  const startOfToday = new Date(now.getFullYear(), now.getMonth(), now.getDate()).getTime();
  const weekAgo = startOfToday - 6 * DAY_MS;
  const buckets: Record<GroupKey, PlanningThread[]> = { today: [], week: [], earlier: [] };
  for (const th of threads) {
    const at = th.updatedAt ? timestampDate(th.updatedAt).getTime() : 0;
    const key: GroupKey = at >= startOfToday ? "today" : at >= weekAgo ? "week" : "earlier";
    buckets[key].push(th);
  }
  return (["today", "week", "earlier"] as const)
    .map((key) => ({ key, threads: buckets[key] }))
    .filter((g) => g.threads.length > 0);
}

// ── The panel ───────────────────────────────────────────────────────────────

export function PlanningPanel({ campaignId }: { campaignId: string }) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const transport = useTransport();
  const nav = useScreenNav();
  const streams = usePlanningStreams();

  // An exchange of another campaign has no place here (the Active Campaign
  // changed under it): stop it. Campaign.tsx does the same on the switch
  // itself; this covers a panel that mounts into a different campaign.
  useEffect(() => stopExchangesOutside(campaignId), [campaignId]);

  const threadsQuery = useQuery(ChatService.method.listPlanningThreads, { campaignId });
  // Most-recently-touched first — the server's order, kept verbatim.
  const threads = useMemo(() => threadsQuery.data?.threads ?? [], [threadsQuery.data]);

  // Which model answers (ADR-0062 amendment): the server's own resolution, the
  // one the chat engine runs. A side read — while it is pending or has failed
  // the footer simply is not there and the chat works as before.
  // The GM's read: the provider configuration is the host table's, so a
  // Campaign Guest's chat has no model line and no link to Setup (ADR-0065).
  const can = useCan();
  const chatLlm = useQuery(
    ProviderService.method.listProviderConfigs,
    {},
    { retry: false, enabled: can.manage },
  ).data?.effectiveChatLlm;
  // Where a reply's "Review" and a failure's "Open Setup" lead are the GM's
  // screens; for a guest the buttons are not there.
  const onReview = can.manage ? () => nav.goCampaignView("proposals") : undefined;
  const onOpenSetup = can.manage ? () => nav.goScreen("configuration") : undefined;

  // ── Selection: the URL's ?thread, mirrored locally ──────────────────────
  // Local state is the truth the panel renders from; the URL follows it (and
  // a URL change from outside — Back, the palette — moves it). Outside a
  // router the seam is inert and the panel still works on its own state.
  const urlThread = nav.search.thread;
  const [selectedId, setSelectedId] = useState<string | null>(urlThread ?? null);
  const [composingNew, setComposingNew] = useState(false);
  const lastUrlThread = useRef(urlThread);
  useEffect(() => {
    if (urlThread === lastUrlThread.current) return;
    lastUrlThread.current = urlThread;
    setSelectedId(urlThread ?? null);
    if (urlThread) setComposingNew(false);
  }, [urlThread]);
  const { setSearch } = nav;
  const select = (id: string | null, opts?: { replace?: boolean }) => {
    setSelectedId(id);
    lastUrlThread.current = id ?? undefined;
    setSearch({ thread: id ?? undefined }, opts);
  };

  // Ids this session already deleted. The cached list is STALE between a
  // delete's success and the refetch landing, so the auto-select below must
  // never re-pick one of these.
  const deletedIds = useRef<Set<string>>(new Set());

  // With nothing selected and no new conversation being composed, open the
  // conversation a reply is streaming into, else the most recent one.
  const inFlight = streamingExchange(streams, campaignId);
  useEffect(() => {
    if (selectedId !== null || composingNew || threadsQuery.status !== "success") return;
    const alive = threads.filter((th) => !deletedIds.current.has(th.id));
    const next = alive.find((th) => th.id === inFlight?.threadId) ?? alive[0];
    if (next) select(next.id, { replace: true });
    // select is recreated per render; the inputs that matter are listed.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selectedId, composingNew, threads, threadsQuery.status, inFlight?.threadId]);

  const selected = threads.find((th) => th.id === selectedId) ?? null;

  const threadQuery = useQuery(
    ChatService.method.getPlanningThread,
    { campaignId, threadId: selectedId ?? "" },
    // A conversation that is gone stays gone: no retries before falling back.
    { enabled: selectedId !== null, retry: (count, err) => !isNotFound(err) && count < 3 },
  );
  const messages: PlanningMessage[] = useMemo(
    () => (selectedId !== null && threadQuery.data ? threadQuery.data.messages : []),
    [selectedId, threadQuery.data],
  );

  const invalidateThreads = () => invalidateMethodQueries(queryClient, ChatService.method.listPlanningThreads);
  const invalidateThread = () => invalidateMethodQueries(queryClient, ChatService.method.getPlanningThread);

  // A ?thread that no longer exists (deleted in another tab, a stale link):
  // say so once, re-read the list and fall back to the newest conversation.
  const threadGone = selectedId !== null && threadQuery.status === "error" && isNotFound(threadQuery.error);
  useEffect(() => {
    if (!threadGone || selectedId === null) return;
    deletedIds.current.add(selectedId);
    toast.error(t("chat.threadGone"));
    void invalidateThreads();
    select(null, { replace: true });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [threadGone]);

  const createThread = useMutation(ChatService.method.createPlanningThread, {
    onSuccess: () => void invalidateThreads(),
    onError: (err) => toast.error(t("chat.createError", { message: errorMessage(err) })),
  });
  const renameThread = useMutation(ChatService.method.renamePlanningThread, {
    onSuccess: () => {
      void invalidateThreads();
      void invalidateThread();
    },
    onError: (err) => toast.error(t("chat.renameError", { message: errorMessage(err) })),
  });
  const deleteThread = useMutation(ChatService.method.deletePlanningThread, {
    onSuccess: (_res, req) => {
      const deletedId = req.threadId ?? "";
      deletedIds.current.add(deletedId);
      saveDraft(campaignId, deletedId, "");
      if (selectedId === deletedId) {
        const next = threads.find((th) => !deletedIds.current.has(th.id));
        select(next?.id ?? null, { replace: true });
      }
      void invalidateThreads();
    },
    onError: (err) => toast.error(t("chat.deleteError", { message: errorMessage(err) })),
  });

  // The thread a delete has been requested for; drives the confirm dialog —
  // no RPC fires until the GM confirms (the #209 pattern).
  const [confirmThread, setConfirmThread] = useState<PlanningThread | null>(null);
  // The phone layout's conversation sheet.
  const [railOpen, setRailOpen] = useState(false);

  // ── Draft ───────────────────────────────────────────────────────────────
  // One per conversation (null: the one not yet started). Re-read when the
  // conversation changes, during render, so a switch never shows one frame of
  // the previous conversation's draft.
  const [draftSlot, setDraftSlot] = useState<string | null>(selectedId);
  const [draft, setDraftState] = useState(() => loadDraft(campaignId, selectedId));
  if (draftSlot !== selectedId) {
    setDraftSlot(selectedId);
    setDraftState(loadDraft(campaignId, selectedId));
  }
  const setDraft = (text: string) => {
    setDraftState(text);
    saveDraft(campaignId, selectedId, text);
  };

  const composerRef = useRef<HTMLTextAreaElement | null>(null);
  // Focus that should land in the composer once a sheet or dialog has closed:
  // moved while the modal still traps focus, it would be pulled back.
  const focusComposerNext = useRef(false);

  // ── The exchange ────────────────────────────────────────────────────────
  const exchange: Exchange | null = selectedId !== null ? (streams.exchanges[exchangeKey(campaignId, selectedId)] ?? null) : null;
  const view = exchange ? exchangeView(exchange, messages) : null;
  const streamingHere = exchange?.status === "streaming";
  const streamingElsewhere = inFlight !== null && inFlight.threadId !== selectedId ? inFlight : null;
  // Sending waits while an exchange streams, and after one ends until its
  // thread has been re-read (planningStream's awaitingRead).
  const busyEx = busyExchange(streams, campaignId);
  const busy = busyEx !== null;
  const settling = busyEx !== null && busyEx.status !== "streaming";
  const [creating, setCreating] = useState(false);

  // The reply's end, for a screen reader: the streamed text itself is not a
  // live region (every delta would be read out), so its end is said once.
  // Failures speak through their own alert.
  const [announcement, setAnnouncement] = useState("");
  const lastStatus = useRef<string | null>(null);
  const exchangeStatus = exchange?.status ?? null;
  useEffect(() => {
    if (lastStatus.current === "streaming" && exchangeStatus === "done") setAnnouncement(t("chat.replyDone"));
    else if (lastStatus.current === "streaming" && exchangeStatus === "stopped") setAnnouncement(t("chat.stopped"));
    else if (exchangeStatus === "streaming") setAnnouncement("");
    lastStatus.current = exchangeStatus;
  }, [exchangeStatus, t]);

  // Retire an exchange once the thread's re-read carries all of it.
  useEffect(() => {
    if (exchange && exchange.status !== "streaming" && selectedId !== null && threadQuery.isSuccess) {
      settleExchange(campaignId, selectedId, messages);
    }
  }, [exchange, messages, campaignId, selectedId, threadQuery.isSuccess]);

  // Refusals that persisted nothing hand the message back: an allowance
  // refusal has no retry, so the words go back into the composer.
  useEffect(() => {
    for (const ex of Object.values(streams.exchanges)) {
      if (ex.campaignId !== campaignId || ex.status !== "failed" || ex.draftReturned) continue;
      if (ex.error?.kind === "allowance") {
        if (ex.threadId === selectedId) {
          if (draft === "") setDraft(ex.userMessage);
        } else if (loadDraft(campaignId, ex.threadId) === "") {
          saveDraft(campaignId, ex.threadId, ex.userMessage);
        }
        markDraftReturned(campaignId, ex.threadId);
      } else if (ex.error?.kind === "gone") {
        // The conversation was deleted while the GM was asking in it.
        deletedIds.current.add(ex.threadId);
        forgetExchange(campaignId, ex.threadId);
        toast.error(t("chat.threadGone"));
        void invalidateThreads();
        if (ex.threadId === selectedId) select(null, { replace: true });
      }
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [streams.exchanges, campaignId, selectedId]);

  // A Stop that landed before the server had saved the message: once the
  // stop's re-reads are in and the thread still has no such turn, the words
  // go back into the composer rather than sit as a turn a reload would drop.
  // (A composer the GM has typed into since keeps the turn, marked not sent.)
  const userPersisted = view?.userPersisted ?? false;
  const threadCurrent = threadQuery.isSuccess && !threadQuery.isFetching;
  useEffect(() => {
    if (!exchange || exchange.status !== "stopped" || exchange.awaitingRead || exchange.draftReturned) return;
    if (!threadCurrent || userPersisted || draft !== "") return;
    setDraft(exchange.userMessage);
    markDraftReturned(campaignId, exchange.threadId);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [exchange, userPersisted, threadCurrent, campaignId]);

  const length = messageLength(draft);
  const tooLong = length > MESSAGE_MAX;
  // An open conversation's rows must be in before a message is lined up
  // against them.
  const threadReady = selectedId === null || threadQuery.isSuccess;
  const canSend = length > 0 && !tooLong && !busy && !creating && threadReady;

  const send = async (override?: string) => {
    const text = (override ?? draft).trim();
    if (text === "" || messageLength(text) > MESSAGE_MAX || busy || creating || !threadReady) return;
    const fromComposer = override === undefined;

    // No conversation yet: the first message creates it (it auto-titles from
    // this very message).
    let threadId = selectedId;
    let persisted = messages;
    let current = false;
    if (threadId === null) {
      setCreating(true);
      try {
        const res = await createThread.mutateAsync({ campaignId, title: "" });
        threadId = res.thread?.id ?? null;
      } catch {
        return; // the mutation's onError toast already spoke
      } finally {
        setCreating(false);
      }
      if (threadId === null) return;
      if (fromComposer) saveDraft(campaignId, null, "");
      persisted = [];
      current = true;
      setComposingNew(false);
      select(threadId, { replace: true });
    } else if (fromComposer) {
      setDraft("");
    }

    startExchange({ transport, queryClient, campaignId, threadId, message: text, persisted, current });
  };

  const retry = (ex: Exchange) =>
    startExchange({ transport, queryClient, campaignId, threadId: ex.threadId, message: ex.userMessage, persisted: messages });

  const startNew = () => {
    setComposingNew(true);
    select(null);
    if (railOpen) {
      focusComposerNext.current = true;
      setRailOpen(false);
    } else {
      composerRef.current?.focus();
    }
  };

  useEffect(() => {
    if (!focusComposerNext.current || railOpen || confirmThread) return;
    focusComposerNext.current = false;
    composerRef.current?.focus();
  }, [railOpen, confirmThread]);

  // The sheet is the phone's rail: a viewport that grows past the phone
  // layout shows the rail beside the chat again, so the sheet closes.
  useEffect(() => {
    if (!railOpen || typeof window.matchMedia !== "function") return;
    const phone = window.matchMedia("(max-width: 720px)");
    const onChange = (e: MediaQueryListEvent) => {
      if (!e.matches) setRailOpen(false);
    };
    phone.addEventListener("change", onChange);
    return () => phone.removeEventListener("change", onChange);
  }, [railOpen]);

  const stop = () => {
    stopExchange(campaignId, queryClient);
    // Stop turns back into Send, which is disabled with an empty composer: a
    // focused disabled button drops focus to <body>.
    composerRef.current?.focus();
  };

  const pickStarter = (s: Starter) => {
    if (s.prefill) {
      const text = t(s.prefill);
      setDraft(text);
      const el = composerRef.current;
      if (el) {
        el.focus();
        // Caret after the prefill, where the GM continues typing.
        requestAnimationFrame(() => el.setSelectionRange(text.length, text.length));
      }
      return;
    }
    // The starters go once the exchange starts, taking the focused button.
    composerRef.current?.focus();
    void send(t(s.label));
  };

  // ── Follow the newest message ───────────────────────────────────────────
  // Keep the newest message in view as deltas append — but ONLY while the GM
  // is already at (or near) the bottom. Once they scroll up to re-read, the
  // pane stays put and a "Jump to latest" pill offers the way back.
  const paneRef = useRef<HTMLOListElement | null>(null);
  const stickRef = useRef(true);
  const [following, setFollowing] = useState(true);
  const onPaneScroll = () => {
    const pane = paneRef.current;
    if (!pane) return;
    const atBottom = pane.scrollHeight - pane.scrollTop - pane.clientHeight < 48;
    stickRef.current = atBottom;
    setFollowing(atBottom);
  };
  const jumpToLatest = () => {
    const pane = paneRef.current;
    if (pane) pane.scrollTop = pane.scrollHeight;
    stickRef.current = true;
    setFollowing(true);
  };
  // A thread switch is a fresh pane: re-arm the follow before its content lands.
  useEffect(() => {
    stickRef.current = true;
    setFollowing(true);
  }, [selectedId]);
  useEffect(() => {
    const pane = paneRef.current;
    if (pane && stickRef.current) pane.scrollTop = pane.scrollHeight;
  }, [selectedId, messages.length, exchange?.reply, exchange?.tools.length, exchange?.status]);

  const threadTitle = (id: string) => threads.find((th) => th.id === id)?.title || t("chat.untitledThread");

  const showStarters =
    !(view?.showUser || view?.showReply || streamingHere) &&
    (selectedId === null ? threadsQuery.status !== "pending" : threadQuery.isSuccess && messages.length === 0);

  const rail = (
    <ThreadRail
      threads={threads}
      status={threadsQuery.status}
      error={threadsQuery.error}
      fetching={threadsQuery.isFetching}
      onRetry={() => void threadsQuery.refetch()}
      selectedId={selectedId}
      streamingId={inFlight?.threadId ?? null}
      renamingId={renameThread.isPending ? (renameThread.variables?.threadId ?? null) : null}
      onNew={startNew}
      onSelect={(id) => {
        setComposingNew(false);
        select(id);
        setRailOpen(false);
      }}
      onRename={(id, title) => renameThread.mutate({ campaignId, threadId: id, title })}
      onDelete={(th) => setConfirmThread(th)}
    />
  );

  return (
    <div className="gx-planning">
      {/* The rail: a column beside the chat, a sheet behind a button on a phone. */}
      <aside className="gx-planning__rail" aria-label={t("chat.threadsAria")}>
        {rail}
      </aside>

      <section className="gx-planning__chat" aria-label={selected?.title || t("chat.newThread")}>
        <div className="gx-planning__phonebar">
          <Button
            variant="secondary"
            size="sm"
            iconStart={<MessagesSquare size={14} />}
            aria-haspopup="dialog"
            onClick={() => setRailOpen(true)}
          >
            {t("chat.threadsButton", { count: String(threads.length) })}
          </Button>
          <Button variant="ghost" size="sm" iconStart={<MessageSquarePlus size={14} />} onClick={startNew}>
            {t("chat.newThread")}
          </Button>
        </div>

        <div className="gx-planning__pane">
          <ol
            className="gx-planning__messages"
            aria-label={t("chat.messagesAria")}
            aria-busy={streamingHere || undefined}
            ref={paneRef}
            onScroll={onPaneScroll}
            // A scroll region needs to be reachable by keyboard to be scrolled.
            tabIndex={0}
          >
            {selectedId !== null && threadQuery.status === "error" && !threadGone && (
              <li className="gx-planning__notice-item">
                <ErrorState
                  size="inline"
                  title={t("chat.threadLoadError")}
                  message={errorMessage(threadQuery.error)}
                  onRetry={() => void threadQuery.refetch()}
                  retrying={threadQuery.isFetching}
                />
              </li>
            )}
            {selectedId !== null && threadQuery.isPending && (
              <li className="gx-planning__notice-item" data-testid="thread-loading">
                <Skeleton.Row count={3} avatar={false} />
              </li>
            )}
            {showStarters && (
              <li className="gx-planning__notice-item">
                <Starters disabled={busy || creating} onPick={pickStarter} />
              </li>
            )}

            {messages.map((m) =>
              m.role === "user" ? (
                <UserMessage key={m.id} content={m.content} when={m.createdAt} />
              ) : (
                <AssistantMessage
                  key={m.id}
                  content={m.content}
                  when={m.createdAt}
                  tools={streams.toolsByMessage[m.id] ?? (view?.persistedReplyId === m.id ? exchange?.tools : undefined)}
                  stopped={streams.stoppedMessages[m.id] === true || (view?.persistedReplyId === m.id && exchange?.status === "stopped")}
                  onReview={onReview}
                />
              ),
            )}

            {exchange && view && (
              <ExchangeTail
                exchange={exchange}
                showUser={view.showUser}
                showReply={view.showReply}
                showTools={view.persistedReplyId === null}
                onReview={onReview}
                retryDisabled={busy}
                onRetry={() => retry(exchange)}
                onOpenSetup={onOpenSetup}
                onDismiss={() => dismissNotice(campaignId, exchange.threadId)}
              />
            )}
          </ol>
          {!following && (
            <button type="button" className="gx-planning__jump" onClick={jumpToLatest}>
              <ArrowDown size={14} aria-hidden="true" />
              {t("chat.jumpLatest")}
            </button>
          )}
        </div>

        {streamingElsewhere && (
          <p className="gx-planning__elsewhere" role="status">
            <span>{t("chat.replyingIn", { title: threadTitle(streamingElsewhere.threadId) })}</span>
            <Button
              variant="ghost"
              size="sm"
              onClick={() => {
                setComposingNew(false);
                select(streamingElsewhere.threadId);
              }}
            >
              {t("chat.replyingInOpen")}
            </Button>
          </p>
        )}

        {/* No key is reachable for the chat's model: say so BEFORE the GM
            types a message that would only be refused. */}
        {chatLlm?.keySource === ChatLlmKeySource.NONE && (
          <p className="gx-planning__nokey" role="status">
            {t("chat.noKeyNotice")}
          </p>
        )}

        <p className="gx-visually-hidden" aria-live="polite">
          {announcement}
        </p>

        <Composer
          inputRef={composerRef}
          value={draft}
          length={length}
          tooLong={tooLong}
          canSend={canSend}
          streaming={streamingHere}
          sending={creating || settling}
          onChange={setDraft}
          onSend={() => void send()}
          onStop={stop}
        />
        <div className="gx-planning__foot">
          {chatLlm && (
            <span className="gx-planning__model" data-testid="planning-model">
              <span>
                {t("chat.answeringWith", {
                  provider: llmProviderLabel(chatLlm.provider),
                  model: chatLlm.model,
                })}
              </span>
              <a className="gx-planning__model-link" href={setupChatModelHref()} aria-label={t("chat.changeModelAria")}>
                {t("chat.changeModel")}
              </a>
            </span>
          )}
          <span className="gx-field__hint gx-planning__hint">{t("chat.sendHint")}</span>
        </div>
      </section>

      {railOpen && (
        <Dialog
          open
          onOpenChange={setRailOpen}
          variant="sheet"
          size="sm"
          title={t("chat.threadsSheetTitle")}
          className="gx-planning__sheet"
        >
          {rail}
        </Dialog>
      )}

      {confirmThread && (
        <ConfirmDialog
          open
          onOpenChange={(open) => {
            if (!open) setConfirmThread(null);
          }}
          title={t("chat.deleteTitle", {
            name: confirmThread.title || t("chat.untitledThread"),
          })}
          description={t("chat.deleteBody")}
          confirmLabel={t("chat.deleteConfirm")}
          onConfirm={() => {
            // The row and its menu go with the conversation: focus moves on
            // to the composer, not to <body>.
            focusComposerNext.current = true;
            discardThread(campaignId, confirmThread.id);
            deleteThread.mutate({ campaignId, threadId: confirmThread.id });
            setConfirmThread(null);
          }}
        />
      )}
    </div>
  );
}

// ── Messages ────────────────────────────────────────────────────────────────

function UserMessage({ content, when, pending, notSent }: { content: string; when?: Timestamp; pending?: boolean; notSent?: boolean }) {
  const { t, lang } = useI18n();
  const at = stamp(when, lang);
  return (
    <li className="gx-planning__msg" data-role="user" data-pending={pending ? "true" : undefined}>
      <span className="gx-visually-hidden">{t("chat.roleUser")}</span>
      {/* The GM's own words stay verbatim, pre-wrapped — formatting what a
          user typed would silently alter it. */}
      <div className="gx-planning__bubble">{content}</div>
      {(at || notSent) && (
        <span className="gx-planning__when">
          {notSent ? (
            <span className="gx-planning__notsent">
              <TriangleAlert size={12} aria-hidden="true" />
              {t("chat.notSent")}
            </span>
          ) : (
            at
          )}
        </span>
      )}
    </li>
  );
}

function AssistantMessage({
  content,
  when,
  tools,
  stopped,
  onReview,
}: {
  content: string;
  when?: Timestamp;
  tools?: ToolChip[];
  stopped?: boolean;
  onReview?: () => void;
}) {
  const { t, lang } = useI18n();
  const at = stamp(when, lang);
  return (
    <li className="gx-planning__msg" data-role="assistant">
      <span className="gx-visually-hidden">{t("chat.roleAssistant")}</span>
      {tools && tools.length > 0 && <ToolChips tools={tools} onReview={onReview} />}
      <div className="gx-planning__reply">
        <Markdown text={content} copyable />
      </div>
      {stopped && <p className="gx-planning__partial">{t("chat.stopped")}</p>}
      <div className="gx-planning__msgfoot">
        <CopyButton text={content} showLabel label={t("chat.copy")} ariaLabel={t("chat.copyAria")} />
        {at && <span className="gx-planning__when">{at}</span>}
      </div>
    </li>
  );
}

// ToolChips are the tools the Butler ran for one reply — name and outcome
// only, the arguments stay server-side (ADR-0062). A filed wiki suggestion
// gets a way to the Suggestions inbox, where the GM approves it.
function ToolChips({ tools, onReview }: { tools: ToolChip[]; onReview?: () => void }) {
  const { t } = useI18n();
  const filed = tools.some((tool) => tool.toolName === "propose_knowledge" && !tool.isError);
  return (
    <div className="gx-planning__tools">
      <ul className="gx-planning__chips" aria-label={t("chat.toolsAria")}>
        {tools.map((tool, i) => (
          <li key={i} className="gx-planning__chip" data-error={tool.isError ? "true" : undefined}>
            {tool.isError
              ? t("chat.toolChipError", { tool: toolLabel(t, tool.toolName) })
              : t("chat.toolChip", { tool: toolLabel(t, tool.toolName) })}
          </li>
        ))}
      </ul>
      {filed && (
        <span className="gx-planning__filed">
          <Check size={14} aria-hidden="true" />
          {t("chat.suggestionFiled")}
          {onReview && (
            <Button variant="ghost" size="sm" aria-label={t("chat.suggestionReviewAria")} onClick={onReview}>
              {t("chat.suggestionReview")}
            </Button>
          )}
        </span>
      )}
    </div>
  );
}

const ERROR_TITLE: Record<ExchangeErrorKind, MessageKey> = {
  allowance: "chat.errAllowanceTitle",
  noKey: "chat.errNoKeyTitle",
  provider: "chat.errProviderTitle",
  gone: "chat.errGenericTitle",
  generic: "chat.errGenericTitle",
};

// ExchangeTail draws what of the exchange the thread's cache does not carry
// yet: the pending user turn, the tool chips, the streamed reply, the
// "writing" line, and how it ended.
function ExchangeTail({
  exchange,
  showUser,
  showReply,
  showTools,
  retryDisabled,
  onReview,
  onRetry,
  onOpenSetup,
  onDismiss,
}: {
  exchange: Exchange;
  showUser: boolean;
  showReply: boolean;
  showTools: boolean;
  retryDisabled: boolean;
  onReview?: () => void;
  onRetry: () => void;
  onOpenSetup?: () => void;
  onDismiss: () => void;
}) {
  const { t } = useI18n();
  const { status, error } = exchange;
  const kind = error?.kind ?? null;
  // Refused before anything was written (ADR-0055 gate, key resolution), or
  // stopped before the server had saved the message.
  const notSent =
    (status === "failed" && (kind === "allowance" || kind === "noKey")) || (status === "stopped" && !exchange.awaitingRead);
  const showNotice = status === "failed" && !exchange.noticeDismissed && kind !== "gone";
  // allowance: our own sentence (the server's is English and says the same);
  // otherwise the server's sentence, which names the actual cause.
  const body =
    kind === "allowance" ? t("chat.errAllowance") : error?.message || (kind === "noKey" ? t("chat.errNoKey") : t("chat.errGeneric"));
  return (
    <>
      {showUser && <UserMessage content={exchange.userMessage} pending notSent={notSent} />}
      {showTools && exchange.tools.length > 0 && (
        <li className="gx-planning__notice-item">
          <ToolChips tools={exchange.tools} onReview={onReview} />
        </li>
      )}
      {showReply && (
        <li className="gx-planning__msg" data-role="assistant" data-pending="true">
          <span className="gx-visually-hidden">{t("chat.roleAssistant")}</span>
          {/* The streamed reply renders markdown mid-stream too: a re-parse per
              delta is cheap at chat size, and a raw-text flash that reformats
              on the done frame would be worse. */}
          <div className="gx-planning__reply">
            <Markdown text={exchange.reply} copyable={status !== "streaming"} />
          </div>
          {status === "stopped" && <p className="gx-planning__partial">{t("chat.stopped")}</p>}
          {status === "failed" && <p className="gx-planning__partial">{t("chat.incomplete")}</p>}
        </li>
      )}
      {status === "streaming" && exchange.reply === "" && (
        <li className="gx-planning__notice-item">
          <p className="gx-planning__working" aria-live="polite">
            {t("chat.working")}
          </p>
        </li>
      )}
      {showNotice && kind && (
        <li className="gx-planning__notice-item">
          <div className="gx-planning__error" role="alert">
            <TriangleAlert size={18} aria-hidden="true" className="gx-planning__error-icon" />
            <div className="gx-planning__error-text">
              <p className="gx-planning__error-title">{t(ERROR_TITLE[kind])}</p>
              <p className="gx-planning__error-body">{body}</p>
              <div className="gx-planning__error-actions">
                {onOpenSetup && (kind === "noKey" || kind === "provider") && (
                  <Button variant="secondary" size="sm" onClick={onOpenSetup}>
                    {t("chat.openSetup")}
                  </Button>
                )}
                {kind !== "allowance" && (
                  <Button variant="secondary" size="sm" disabled={retryDisabled} onClick={onRetry}>
                    {t("chat.retry")}
                  </Button>
                )}
              </div>
            </div>
            <IconButton label={t("common.close")} size="sm" tooltip={false} onClick={onDismiss}>
              <X size={14} />
            </IconButton>
          </div>
        </li>
      )}
    </>
  );
}

// Starters say what the Butler can do, one per tool on its belt.
function Starters({ disabled, onPick }: { disabled: boolean; onPick: (s: Starter) => void }) {
  const { t } = useI18n();
  const titleId = useId();
  return (
    <div className="gx-planning__starters">
      <p className="gx-planning__starters-title" id={titleId}>
        {t("chat.startersTitle")}
      </p>
      <p className="gx-planning__starters-lede">{t("chat.startersLede")}</p>
      <ul className="gx-planning__starter-list" aria-labelledby={titleId}>
        {STARTERS.map((s) => (
          <li key={s.label}>
            <button
              type="button"
              className="gx-planning__starter"
              disabled={disabled}
              aria-description={s.prefill ? t("chat.starterFills") : undefined}
              onClick={() => onPick(s)}
            >
              <span className="gx-planning__starter-icon" aria-hidden="true">
                {s.icon}
              </span>
              <span className="gx-planning__starter-text">
                <span className="gx-planning__starter-label">{t(s.label)}</span>
                <span className="gx-planning__starter-tool">{t(s.tool)}</span>
              </span>
            </button>
          </li>
        ))}
      </ul>
    </div>
  );
}

// ── Composer ────────────────────────────────────────────────────────────────

function Composer({
  inputRef,
  value,
  length,
  tooLong,
  canSend,
  streaming,
  sending,
  onChange,
  onSend,
  onStop,
}: {
  inputRef: RefObject<HTMLTextAreaElement | null>;
  value: string;
  length: number;
  tooLong: boolean;
  canSend: boolean;
  streaming: boolean;
  sending: boolean;
  onChange: (text: string) => void;
  onSend: () => void;
  onStop: () => void;
}) {
  const { t, lang } = useI18n();
  const counterId = useId();
  const showCounter = length >= MESSAGE_COUNTER_FROM;
  const formRef = useRef<HTMLFormElement | null>(null);

  // A reply that ends while the keyboard is on Stop: the button turns into
  // Send, disabled with an empty composer, and a focused button that becomes
  // disabled drops focus to <body>. Before the browser gets there (a layout
  // effect runs before it does), focus moves to the composer.
  const wasStreaming = useRef(streaming);
  useLayoutEffect(() => {
    if (wasStreaming.current && !streaming) {
      const active = document.activeElement;
      if (active instanceof HTMLButtonElement && formRef.current?.contains(active)) inputRef.current?.focus();
    }
    wasStreaming.current = streaming;
  }, [streaming, inputRef]);

  return (
    <form
      ref={formRef}
      className="gx-planning__composer"
      onSubmit={(e) => {
        e.preventDefault();
        onSend();
      }}
    >
      <div className="gx-planning__field">
        {/* Textarea's autoGrow: one row to eight, then it scrolls. */}
        <Textarea
          className={tooLong ? "gx-input--invalid" : undefined}
          ref={inputRef}
          autoGrow
          rows={1}
          maxRows={8}
          value={value}
          aria-label={t("chat.inputAria")}
          aria-invalid={tooLong || undefined}
          aria-describedby={showCounter ? counterId : undefined}
          placeholder={t("chat.inputPlaceholder")}
          onChange={(e) => onChange(e.target.value)}
          onKeyDown={(e) => {
            // Enter sends; Shift+Enter stays a newline. An Enter that commits
            // an IME candidate (CJK composition) must NOT send: isComposing
            // covers the spec path, keyCode 229 the engines that report the
            // composition keydown after composition already ended.
            if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing && e.keyCode !== 229) {
              e.preventDefault();
              if (canSend) onSend();
            }
          }}
        />
        {showCounter && (
          <span id={counterId} className="gx-planning__counter" data-over={tooLong ? "true" : undefined}>
            {t("chat.counter", { count: length.toLocaleString(lang), max: MESSAGE_MAX.toLocaleString(lang) })}
            {tooLong && (
              <span className="gx-planning__counter-error">
                {" "}
                {t("chat.tooLong", { over: (length - MESSAGE_MAX).toLocaleString(lang), max: MESSAGE_MAX.toLocaleString(lang) })}
              </span>
            )}
          </span>
        )}
      </div>
      {streaming ? (
        <Button type="button" variant="secondary" iconStart={<Square size={14} />} aria-label={t("chat.stopAria")} onClick={onStop}>
          {t("chat.stop")}
        </Button>
      ) : (
        <Button type="submit" variant="primary" iconStart={<SendHorizontal size={14} />} disabled={!canSend} loading={sending}>
          {t("chat.send")}
        </Button>
      )}
    </form>
  );
}

// ── Thread rail ─────────────────────────────────────────────────────────────

function ThreadRail({
  threads,
  status,
  error,
  fetching,
  onRetry,
  selectedId,
  streamingId,
  renamingId,
  onNew,
  onSelect,
  onRename,
  onDelete,
}: {
  threads: PlanningThread[];
  status: "pending" | "error" | "success";
  error: unknown;
  fetching: boolean;
  onRetry: () => void;
  selectedId: string | null;
  streamingId: string | null;
  renamingId: string | null;
  onNew: () => void;
  onSelect: (id: string) => void;
  onRename: (id: string, title: string) => void;
  onDelete: (thread: PlanningThread) => void;
}) {
  const { t } = useI18n();
  const [query, setQuery] = useState("");
  const searchId = useId();
  const needle = query.trim().toLocaleLowerCase();
  const shown = needle === "" ? threads : threads.filter((th) => (th.title || t("chat.untitledThread")).toLocaleLowerCase().includes(needle));
  const groups = groupThreads(shown, new Date());

  return (
    <div className="gx-planning__threads">
      <Button variant="secondary" size="sm" iconStart={<MessageSquarePlus size={14} />} className="gx-planning__new" onClick={onNew}>
        {t("chat.newThread")}
      </Button>

      {status === "pending" ? (
        <div data-testid="planning-threads-loading">
          <Skeleton.Row count={4} avatar={false} />
        </div>
      ) : status === "error" ? (
        <ErrorState size="inline" title={t("chat.threadsLoadError")} message={errorMessage(error)} onRetry={onRetry} retrying={fetching} />
      ) : threads.length === 0 ? (
        <EmptyState size="inline" icon={<MessagesSquare size={16} />} title={t("chat.threadsEmpty")} />
      ) : (
        <>
          <div className="gx-planning__search">
            <label htmlFor={searchId} className="gx-visually-hidden">
              {t("chat.threadSearchLabel")}
            </label>
            <Search size={14} aria-hidden="true" className="gx-planning__search-icon" />
            <input
              id={searchId}
              type="search"
              className="gx-input gx-input--has-icon"
              placeholder={t("chat.threadSearchPlaceholder")}
              value={query}
              onChange={(e) => setQuery(e.target.value)}
            />
          </div>
          {groups.length === 0 ? (
            <p className="gx-planning__empty">{t("chat.threadSearchEmpty", { query: query.trim() })}</p>
          ) : (
            <div className="gx-planning__groups">
              {groups.map((g) => (
                <ThreadGroup
                  key={g.key}
                  label={t(GROUP_LABEL[g.key])}
                  threads={g.threads}
                  selectedId={selectedId}
                  streamingId={streamingId}
                  renamingId={renamingId}
                  onSelect={onSelect}
                  onRename={onRename}
                  onDelete={onDelete}
                />
              ))}
            </div>
          )}
        </>
      )}
    </div>
  );
}

function ThreadGroup({
  label,
  threads,
  selectedId,
  streamingId,
  renamingId,
  onSelect,
  onRename,
  onDelete,
}: {
  label: string;
  threads: PlanningThread[];
  selectedId: string | null;
  streamingId: string | null;
  renamingId: string | null;
  onSelect: (id: string) => void;
  onRename: (id: string, title: string) => void;
  onDelete: (thread: PlanningThread) => void;
}) {
  const headingId = useId();
  return (
    <div className="gx-planning__group" role="group" aria-labelledby={headingId}>
      <h2 className="gx-planning__group-title" id={headingId}>
        {label}
      </h2>
      <ul className="gx-planning__thread-list">
        {threads.map((th) => (
          <ThreadRow
            key={th.id}
            thread={th}
            active={th.id === selectedId}
            streaming={th.id === streamingId}
            renaming={th.id === renamingId}
            onSelect={() => onSelect(th.id)}
            onRename={(title) => onRename(th.id, title)}
            onDelete={() => onDelete(th)}
          />
        ))}
      </ul>
    </div>
  );
}

// ThreadRow is one conversation in the rail: title + last-touched stamp, and an
// overflow menu with Rename (the row turns into a text field: Enter or the
// check commits, Escape or the cross cancels) and Delete (arms the panel's
// confirm dialog).
function ThreadRow({
  thread,
  active,
  streaming,
  renaming,
  onSelect,
  onRename,
  onDelete,
}: {
  thread: PlanningThread;
  active: boolean;
  streaming: boolean;
  renaming: boolean;
  onSelect: () => void;
  onRename: (title: string) => void;
  onDelete: () => void;
}) {
  const { t, lang } = useI18n();
  const [editing, setEditing] = useState(false);
  const [title, setTitle] = useState("");
  const displayName = thread.title || t("chat.untitledThread");
  const when = stamp(thread.updatedAt, lang);

  // Focus follows the mode: into the field when Rename is chosen (after the
  // menu has put focus back on its trigger, which this swap unmounts), back
  // to the row when the rename ends — never left on <body>.
  const inputRef = useRef<HTMLInputElement | null>(null);
  const openRef = useRef<HTMLButtonElement | null>(null);
  const wasEditing = useRef(false);
  useEffect(() => {
    if (editing === wasEditing.current) return;
    wasEditing.current = editing;
    const frame = requestAnimationFrame(() => (editing ? inputRef.current?.focus() : openRef.current?.focus()));
    return () => cancelAnimationFrame(frame);
  }, [editing]);

  const commit = () => {
    const next = title.trim();
    setEditing(false);
    // The server rejects an empty title; an unchanged one is a no-op not worth
    // a round-trip.
    if (next === "" || next === thread.title) return;
    onRename(next);
  };

  if (editing) {
    return (
      <li className="gx-planning__thread" data-active={active ? "true" : undefined}>
        <input
          ref={inputRef}
          className="gx-input gx-planning__rename"
          aria-label={t("chat.renameInputAria")}
          value={title}
          maxLength={TITLE_MAX}
          disabled={renaming}
          onChange={(e) => setTitle(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              commit();
            } else if (e.key === "Escape") {
              // Inside the phone sheet, Escape cancels the rename, not the sheet.
              e.preventDefault();
              e.stopPropagation();
              setEditing(false);
            }
          }}
        />
        <div className="gx-planning__thread-actions">
          <IconButton label={t("chat.renameSaveAria")} size="sm" tooltip={false} disabled={renaming} onClick={commit}>
            <Check size={14} />
          </IconButton>
          <IconButton label={t("chat.renameCancelAria")} size="sm" tooltip={false} onClick={() => setEditing(false)}>
            <X size={14} />
          </IconButton>
        </div>
      </li>
    );
  }

  return (
    <li
      className="gx-planning__thread"
      data-active={active ? "true" : undefined}
      data-streaming={streaming ? "true" : undefined}
    >
      <button
        ref={openRef}
        type="button"
        className="gx-planning__thread-open"
        aria-current={active ? "true" : undefined}
        onClick={onSelect}
      >
        <span className="gx-planning__thread-title">{displayName}</span>
        <span className="gx-planning__thread-when">
          {streaming ? <span className="gx-planning__thread-live">{t("chat.working")}</span> : when}
        </span>
      </button>
      <Menu
        trigger={
          <IconButton label={t("chat.threadActionsAria", { name: displayName })} size="sm" tooltip={false}>
            <MoreHorizontal size={14} />
          </IconButton>
        }
      >
        <MenuItem
          icon={<Pencil size={14} />}
          onSelect={() => {
            setTitle(thread.title);
            setEditing(true);
          }}
        >
          {t("chat.renameAction")}
        </MenuItem>
        <MenuSeparator />
        <MenuItem tone="danger" icon={<Trash2 size={14} />} onSelect={onDelete}>
          {t("chat.deleteAction")}
        </MenuItem>
      </Menu>
    </li>
  );
}
