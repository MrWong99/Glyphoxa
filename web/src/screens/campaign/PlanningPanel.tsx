import { useEffect, useRef, useState } from "react";
import { useQuery, useMutation, createConnectQueryKey, useTransport } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { createClient, Code, ConnectError } from "@connectrpc/connect";
import { timestampDate } from "@bufbuild/protobuf/wkt";
import type { Timestamp } from "@bufbuild/protobuf/wkt";
import { toast } from "sonner";
import { Check, MessageSquarePlus, Pencil, SendHorizontal, Trash2, X } from "lucide-react";

import { ChatService } from "@gen/glyphoxa/management/v1/management_pb";
import type { PlanningMessage, PlanningThread } from "@gen/glyphoxa/management/v1/management_pb";
import { useI18n, type Lang, type MessageKey } from "@/i18n";
import { Button } from "@/components/ui/Button";
import { ConfirmDialog } from "@/components/ui/ConfirmDialog";
import { Markdown } from "@/components/ui/Markdown";

import "./planning.css";

// The Planning panel (#592, ADR-0062) backs the Campaign screen's "Planning"
// view: the Butler as a GM-only prep chat over the ChatService RPCs. Threads
// are persisted per-Campaign (list/create/rename/delete are ordinary unary
// calls through connect-query); one exchange streams over the server-streaming
// PlanningExchange RPC, hand-rolled with a Connect client because connect-query
// hooks model finite reads, not a token stream.
//
// The IN-FLIGHT exchange is ephemeral UI state (pending user bubble, streamed
// reply, tool chips) held in local component state; the done frame carries the
// PERSISTED rows, at which point the thread + list queries are invalidated and
// the local stream state clears — the cache is the only durable source.

// MESSAGE_MAX mirrors the server-side bound on one exchange's user message.
const MESSAGE_MAX = 4000;

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

// ToolChip is one executed tool call reported by the stream — name + outcome
// only, the arguments stay server-side (ADR-0062).
type ToolChip = { toolName: string; isError: boolean };

// StreamState is the whole in-flight exchange. errorKey null = still streaming;
// set = the stream failed and the localized notice renders in-chat until the GM
// dismisses it.
type StreamState = {
  threadId: string;
  userMessage: string;
  reply: string;
  tools: ToolChip[];
  errorKey: MessageKey | null;
};

export function PlanningPanel({ campaignId }: { campaignId: string }) {
  const { t, lang } = useI18n();
  const queryClient = useQueryClient();
  const transport = useTransport();

  const threadsQuery = useQuery(ChatService.method.listPlanningThreads, { campaignId });
  // Most-recently-touched first — the server's order, kept verbatim.
  const threads = threadsQuery.data?.threads ?? [];

  const [selectedId, setSelectedId] = useState<string | null>(null);
  const selected = threads.find((th) => th.id === selectedId) ?? null;

  // Ids this session already deleted. The cached list is STALE between a
  // delete's success and the refetch landing, so both the auto-select below and
  // the delete handler must never re-pick one of these — selecting a deleted
  // thread would stick the pane on a NotFound error.
  const deletedIds = useRef<Set<string>>(new Set());

  // Default to the most recent surviving thread once the list lands; also picks
  // the successor after a delete cleared the selection.
  useEffect(() => {
    if (selectedId === null) {
      const next = threads.find((th) => !deletedIds.current.has(th.id));
      if (next) setSelectedId(next.id);
    }
  }, [selectedId, threads]);

  const threadQuery = useQuery(
    ChatService.method.getPlanningThread,
    { campaignId, threadId: selectedId ?? "" },
    { enabled: selectedId !== null },
  );

  const invalidateThreads = () =>
    queryClient.invalidateQueries({
      queryKey: createConnectQueryKey({
        schema: ChatService.method.listPlanningThreads,
        cardinality: "finite",
      }),
    });
  const invalidateThread = () =>
    queryClient.invalidateQueries({
      queryKey: createConnectQueryKey({
        schema: ChatService.method.getPlanningThread,
        cardinality: "finite",
      }),
    });

  const createThread = useMutation(ChatService.method.createPlanningThread, {
    onSuccess: (res) => {
      void invalidateThreads();
      if (res.thread) setSelectedId(res.thread.id);
    },
    onError: (err) => toast.error(t("chat.createError", { message: err.message })),
  });
  const renameThread = useMutation(ChatService.method.renamePlanningThread, {
    onSuccess: () => {
      void invalidateThreads();
      void invalidateThread();
    },
    onError: (err) => toast.error(t("chat.renameError", { message: err.message })),
  });
  const deleteThread = useMutation(ChatService.method.deletePlanningThread, {
    onSuccess: (_res, req) => {
      // The mutation variables are an init shape, so threadId is optional on
      // the type; this panel always sends it.
      const deletedId = req.threadId ?? "";
      deletedIds.current.add(deletedId);
      // Hand the selection straight to the next surviving thread, computed from
      // the CURRENT (still stale) list minus everything known-deleted — waiting
      // for the refetch would let the auto-select re-pick the deleted id.
      setSelectedId((cur) => {
        if (cur !== deletedId) return cur;
        return threads.find((th) => !deletedIds.current.has(th.id))?.id ?? null;
      });
      void invalidateThreads();
    },
    onError: (err) => toast.error(t("chat.deleteError", { message: err.message })),
  });

  // The thread a delete has been requested for; drives the confirm dialog —
  // no RPC fires until the GM confirms (the #209 pattern).
  const [confirmThread, setConfirmThread] = useState<PlanningThread | null>(null);

  // ── The in-flight exchange ────────────────────────────────────────────────
  const [stream, setStream] = useState<StreamState | null>(null);
  const streaming = stream !== null && stream.errorKey === null;
  const [draft, setDraft] = useState("");

  const send = async () => {
    const text = draft.trim();
    if (text === "" || streaming || createThread.isPending) return;

    // No thread yet? Create one first — it auto-titles from this very message.
    let threadId = selectedId;
    if (threadId === null) {
      try {
        const res = await createThread.mutateAsync({ campaignId, title: "" });
        threadId = res.thread?.id ?? null;
      } catch {
        return; // the mutation's onError toast already spoke
      }
      if (threadId === null) return;
    }

    // The pending user message renders immediately.
    setDraft("");
    setStream({ threadId, userMessage: text, reply: "", tools: [], errorKey: null });

    const client = createClient(ChatService, transport);
    let done = false;
    try {
      for await (const res of client.planningExchange({ campaignId, threadId, message: text })) {
        const frame = res.frame;
        if (frame.case === "text") {
          const delta = frame.value.delta;
          setStream((s) => (s ? { ...s, reply: s.reply + delta } : s));
        } else if (frame.case === "tool") {
          const chip = { toolName: frame.value.toolName, isError: frame.value.isError };
          setStream((s) => (s ? { ...s, tools: [...s.tools, chip] } : s));
        } else if (frame.case === "done") {
          done = true;
        }
      }
      if (done) {
        // Write through: the done frame's rows are persisted, so the thread and
        // the list (updated_at, auto-filled title) re-read before the ephemeral
        // stream state clears — no flash of a message-less pane.
        await Promise.all([invalidateThread(), invalidateThreads()]);
        setStream(null);
      } else {
        // The stream ended without a done frame — treat as a failed exchange.
        setStream((s) => (s ? { ...s, errorKey: "chat.errGeneric" } : s));
      }
    } catch (err) {
      const code = ConnectError.from(err).code;
      const errorKey: MessageKey =
        code === Code.ResourceExhausted
          ? "chat.errAllowance"
          : code === Code.FailedPrecondition
            ? "chat.errNoKey"
            : "chat.errGeneric";
      // Allowance/no-key refusals reject BEFORE the server persists the user
      // message (the ADR-0055 gate runs first), so the GM's text would be lost
      // with the ephemeral state — restore it into the composer for a retry.
      // On other failures the message was persisted first (ADR-0062) and the
      // dismiss-refetch will show it, so restoring would duplicate it.
      if (code === Code.ResourceExhausted || code === Code.FailedPrecondition) {
        setDraft((d) => (d === "" ? text : d));
      }
      setStream((s) => (s ? { ...s, errorKey } : s));
    }
  };

  // Dismissing the error clears the ephemeral state and re-reads the thread:
  // the user message may have been persisted before the exchange failed
  // (persisted-first, ADR-0062), so the cache decides what actually exists.
  const dismissError = () => {
    setStream(null);
    void invalidateThread();
    void invalidateThreads();
  };

  // Only render the stream into the pane it belongs to — switching threads
  // mid-exchange must not paint the reply into the wrong conversation.
  const activeStream = stream && stream.threadId === selectedId ? stream : null;
  const messages: PlanningMessage[] =
    selectedId !== null && threadQuery.data ? threadQuery.data.messages : [];

  // Keep the newest message in view as deltas append — but ONLY while the GM
  // is already at (or near) the bottom. Once they scroll up to re-read during
  // a streaming reply, the pane must stay put; scrolling back down re-arms the
  // follow. stickRef tracks "was near bottom before this update" via onScroll.
  const paneRef = useRef<HTMLOListElement | null>(null);
  const stickRef = useRef(true);
  const onPaneScroll = () => {
    const pane = paneRef.current;
    if (!pane) return;
    stickRef.current = pane.scrollHeight - pane.scrollTop - pane.clientHeight < 48;
  };
  // A thread switch is a fresh pane: re-arm the follow before its content lands.
  useEffect(() => {
    stickRef.current = true;
  }, [selectedId]);
  useEffect(() => {
    const pane = paneRef.current;
    if (pane && stickRef.current) pane.scrollTop = pane.scrollHeight;
  }, [messages.length, activeStream?.reply, activeStream?.tools.length, activeStream?.errorKey]);

  return (
    <div className="gx-planning">
      {/* Thread rail */}
      <div className="gx-planning__threads">
        <Button
          variant="secondary"
          size="sm"
          iconStart={<MessageSquarePlus size={14} />}
          className="gx-planning__new"
          disabled={createThread.isPending}
          onClick={() => createThread.mutate({ campaignId, title: "" })}
        >
          {t("chat.newThread")}
        </Button>

        {threadsQuery.status === "pending" ? (
          <div className="gx-skeleton" data-testid="planning-threads-loading" />
        ) : threadsQuery.status === "error" ? (
          <p className="gx-campaign__error" role="alert">
            {t("chat.threadsLoadError", { message: threadsQuery.error.message })}
          </p>
        ) : threads.length === 0 ? (
          <p className="gx-planning__empty">{t("chat.threadsEmpty")}</p>
        ) : (
          // role="list" wraps ONLY the rows — the create button and the
          // loading/error states above are not list items.
          <div className="gx-planning__thread-list" role="list" aria-label={t("chat.threadsAria")}>
            {threads.map((th) => (
              <ThreadRow
                key={th.id}
                thread={th}
                active={selected?.id === th.id}
                renaming={renameThread.isPending && renameThread.variables?.threadId === th.id}
                onSelect={() => setSelectedId(th.id)}
                onRename={(title) =>
                  renameThread.mutate({ campaignId, threadId: th.id, title })
                }
                onDelete={() => setConfirmThread(th)}
              />
            ))}
          </div>
        )}
      </div>

      {/* Message pane + composer */}
      <div className="gx-planning__chat">
        <ol
          className="gx-planning__messages"
          aria-label={t("chat.messagesAria")}
          ref={paneRef}
          onScroll={onPaneScroll}
        >
          {selectedId !== null && threadQuery.status === "error" && (
            <li className="gx-planning__notice-item">
              <p className="gx-campaign__error" role="alert">
                {t("chat.threadLoadError", { message: threadQuery.error.message })}
              </p>
            </li>
          )}
          {messages.length === 0 && !activeStream && threadQuery.status !== "error" && (
            <li className="gx-planning__notice-item">
              <p className="gx-planning__empty">{t("chat.emptyThread")}</p>
            </li>
          )}
          {messages.map((m) => {
            const when = stamp(m.createdAt, lang);
            return (
              <li
                key={m.id}
                className="gx-planning__msg"
                data-role={m.role === "user" ? "user" : "assistant"}
              >
                {/* Butler replies are markdown and render as such; the GM's own
                    messages stay verbatim pre-wrapped text — formatting what a
                    user typed would silently alter their words. */}
                <div className="gx-planning__bubble">
                  {m.role === "user" ? m.content : <Markdown text={m.content} />}
                </div>
                {when && <span className="gx-planning__when">{when}</span>}
              </li>
            );
          })}
          {activeStream && (
            <>
              <li className="gx-planning__msg" data-role="user" data-pending="true">
                <div className="gx-planning__bubble">{activeStream.userMessage}</div>
              </li>
              {activeStream.tools.length > 0 && (
                <li className="gx-planning__notice-item">
                  <div className="gx-planning__tools">
                    {activeStream.tools.map((tool, i) => (
                      <span
                        key={i}
                        className="gx-planning__chip"
                        data-error={tool.isError ? "true" : undefined}
                      >
                        {/* toolName is a wire value and stays untranslated. */}
                        {tool.isError
                          ? t("chat.toolChipError", { tool: tool.toolName })
                          : t("chat.toolChip", { tool: tool.toolName })}
                      </span>
                    ))}
                  </div>
                </li>
              )}
              {activeStream.reply !== "" ? (
                <li className="gx-planning__msg" data-role="assistant" data-pending="true">
                  {/* The streamed reply renders markdown mid-stream too: a
                      re-parse per delta is cheap at chat size, and a raw-text
                      flash that reformats on the done frame would be worse. */}
                  <div className="gx-planning__bubble">
                    <Markdown text={activeStream.reply} />
                  </div>
                </li>
              ) : activeStream.errorKey === null ? (
                <li className="gx-planning__notice-item">
                  <p className="gx-planning__working" aria-live="polite">
                    {t("chat.working")}
                  </p>
                </li>
              ) : null}
              {activeStream.errorKey !== null && (
                <li className="gx-planning__notice-item">
                  <div className="gx-planning__error" role="alert">
                    <span>{t(activeStream.errorKey)}</span>
                    <Button variant="ghost" size="sm" onClick={dismissError}>
                      {t("common.close")}
                    </Button>
                  </div>
                </li>
              )}
            </>
          )}
        </ol>

        <form
          className="gx-planning__composer"
          onSubmit={(e) => {
            e.preventDefault();
            void send();
          }}
        >
          <textarea
            className="gx-input gx-textarea gx-planning__input"
            rows={2}
            maxLength={MESSAGE_MAX}
            value={draft}
            aria-label={t("chat.inputAria")}
            placeholder={t("chat.inputPlaceholder")}
            disabled={streaming}
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={(e) => {
              // Enter sends; Shift+Enter stays a newline. An Enter that commits
              // an IME candidate (CJK composition) must NOT send: isComposing
              // covers the spec path, keyCode 229 the engines that report the
              // composition keydown after composition already ended.
              if (
                e.key === "Enter" &&
                !e.shiftKey &&
                !e.nativeEvent.isComposing &&
                e.keyCode !== 229
              ) {
                e.preventDefault();
                void send();
              }
            }}
          />
          <Button
            type="submit"
            variant="primary"
            iconStart={<SendHorizontal size={14} />}
            disabled={streaming || draft.trim() === ""}
          >
            {t("chat.send")}
          </Button>
        </form>
        <span className="gx-field__hint gx-planning__hint">{t("chat.sendHint")}</span>
      </div>

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
            deleteThread.mutate({ campaignId, threadId: confirmThread.id });
            setConfirmThread(null);
          }}
        />
      )}
    </div>
  );
}

// ThreadRow is one conversation in the rail: title + last-touched stamp, with
// inline rename (pencil → text field, Enter/check commits, Esc/x cancels) and a
// delete that only ARMS the panel's confirm dialog.
function ThreadRow({
  thread,
  active,
  renaming,
  onSelect,
  onRename,
  onDelete,
}: {
  thread: PlanningThread;
  active: boolean;
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

  const startEdit = () => {
    setTitle(thread.title);
    setEditing(true);
  };
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
      <div className="gx-planning__thread" data-active={active ? "true" : undefined} role="listitem">
        <input
          className="gx-input gx-planning__rename"
          aria-label={t("chat.renameInputAria")}
          value={title}
          autoFocus
          disabled={renaming}
          onChange={(e) => setTitle(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              commit();
            } else if (e.key === "Escape") {
              setEditing(false);
            }
          }}
        />
        <div className="gx-planning__thread-actions">
          <button
            type="button"
            className="gx-kg-iconbtn"
            aria-label={t("chat.renameSaveAria")}
            disabled={renaming}
            onClick={commit}
          >
            <Check size={14} />
          </button>
          <button
            type="button"
            className="gx-kg-iconbtn"
            aria-label={t("chat.renameCancelAria")}
            onClick={() => setEditing(false)}
          >
            <X size={14} />
          </button>
        </div>
      </div>
    );
  }

  return (
    <div className="gx-planning__thread" data-active={active ? "true" : undefined} role="listitem">
      <button type="button" className="gx-planning__thread-open" onClick={onSelect}>
        <span className="gx-planning__thread-title">{displayName}</span>
        {when && <span className="gx-planning__thread-when">{when}</span>}
      </button>
      <div className="gx-planning__thread-actions">
        <button
          type="button"
          className="gx-kg-iconbtn"
          aria-label={t("chat.renameAria", { name: displayName })}
          onClick={startEdit}
        >
          <Pencil size={14} />
        </button>
        <button
          type="button"
          className="gx-kg-iconbtn gx-kg-iconbtn--danger"
          aria-label={t("chat.deleteAria", { name: displayName })}
          onClick={onDelete}
        >
          <Trash2 size={14} />
        </button>
      </div>
    </div>
  );
}
