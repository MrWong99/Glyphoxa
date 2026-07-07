import { useEffect, useMemo, useState } from "react";
import { useQuery, useMutation, createConnectQueryKey } from "@connectrpc/connect-query";
import { useQueryClient, keepPreviousData } from "@tanstack/react-query";
import { Play, Square, Search } from "lucide-react";
import { toast } from "sonner";
import { timestampMs } from "@bufbuild/protobuf/wkt";

import { SessionService, CampaignService } from "@gen/glyphoxa/management/v1/management_pb";
import type { VoiceSession, TranscriptLineMatch } from "@gen/glyphoxa/management/v1/management_pb";
import { Card } from "@/components/ui/Card";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { Input } from "@/components/ui/Input";
import { useSessionEvents, formatClock } from "./useSessionEvents";
import { VoicePanel } from "./VoicePanel";

import "./session.css";

// The Session screen (#72) drives the live Voice Session from the UI on the live
// SessionService (ADR-0039): Start/Stop call the in-process SessionManager, the
// status badge + elapsed timer reflect the running session, and an idle screen
// shows a summary of the last session that ended. The live transcript feed itself
// is a separate issue (#73/SSE) — the timer is client-side from started_at and
// the status comes from GetSession.

// formatElapsed renders a non-negative second count as zero-padded HH:MM:SS
// (the design's exact format). Exported so the format is unit-tested directly.
export function formatElapsed(totalSeconds: number): string {
  const s = Math.max(0, Math.floor(totalSeconds));
  return [Math.floor(s / 3600), Math.floor((s % 3600) / 60), s % 60]
    .map((n) => String(n).padStart(2, "0"))
    .join(":");
}

// SESSION_REFETCH_MS is the getSession poll cadence while a session is live —
// belt and suspenders for #144: even if the SSE terminal frame is missed, a
// session that dies server-side flips the badge within one interval.
export const SESSION_REFETCH_MS = 5000;

// sessionRefetchInterval is the getSession refetchInterval policy: poll while
// the last read said active, stop polling when idle. Exported so the config is
// pinned by a unit test.
export function sessionRefetchInterval(query: { state: { data?: { active?: boolean } } }): number | false {
  return query.state.data?.active ? SESSION_REFETCH_MS : false;
}

// tsMs converts a protobuf Timestamp to epoch milliseconds, or null when unset.
function tsMs(ts: VoiceSession["startedAt"] | undefined): number | null {
  return ts ? Number(timestampMs(ts)) : null;
}

// matchClock renders a search hit's protobuf Timestamp as HH:MM:SS, matching the
// transcript line timestamps (reusing formatClock via the RFC3339 instant).
function matchClock(ts: TranscriptLineMatch["ts"] | undefined): string {
  const ms = ts ? Number(timestampMs(ts)) : null;
  return ms == null ? "" : formatClock(new Date(ms).toISOString());
}

// useElapsed ticks a once-per-second elapsed-seconds counter from a start instant
// (epoch ms), resetting to 0 when idle (start === null).
function useElapsed(startMs: number | null): number {
  const [elapsed, setElapsed] = useState(0);
  useEffect(() => {
    if (startMs == null) {
      setElapsed(0);
      return;
    }
    const tick = () => setElapsed(Math.floor((Date.now() - startMs) / 1000));
    tick();
    const id = setInterval(tick, 1000);
    return () => clearInterval(id);
  }, [startMs]);
  return elapsed;
}

// connectionLabel renders the live gateway connection sub-state beside the Live
// badge during a normal start (#123): "Connecting…" then "Connected". A failed
// state is rendered as its own badge + reason, not here, so this returns null for
// it (and for the pre-first-transition undefined).
function connectionLabel(state: string | undefined): string | null {
  switch (state) {
    case "connecting":
      return "Connecting…";
    case "connected":
      return "Connected";
    default:
      return null;
  }
}

// formatUsd renders a USD estimate as $X.XX (#130). Always paired with an
// "(estimated)" label at the call site — it is an approximate figure, not a bill.
function formatUsd(usd: number): string {
  return `$${usd.toFixed(2)}`;
}

// lastSummary renders the idle "Last session ended …" line from an ended session.
function lastSummary(session: VoiceSession): string {
  const endedMs = tsMs(session.endedAt);
  const startedMs = tsMs(session.startedAt);
  const ended = endedMs != null ? new Date(endedMs) : null;

  const when = ended
    ? `${String(ended.getHours()).padStart(2, "0")}:${String(ended.getMinutes()).padStart(2, "0")}`
    : "—";

  let duration = "0h 0m";
  if (endedMs != null && startedMs != null) {
    const minutes = Math.max(0, Math.round((endedMs - startedMs) / 60000));
    duration = `${Math.floor(minutes / 60)}h ${minutes % 60}m`;
  }
  return `Last session ended ${when} · ${duration} · ${session.lineCount} lines transcribed.`;
}

export function Session() {
  const queryClient = useQueryClient();
  const { data } = useQuery(SessionService.method.getSession, {}, { refetchInterval: sessionRefetchInterval });
  const campaignQ = useQuery(CampaignService.method.getActiveCampaign, {});
  const campaignName = campaignQ.data?.campaign?.name;

  const active = data?.active ?? false;
  const session = data?.session;

  // Spend-cap-reached state (#130, ADR-0046): the live reload truth is GetSession
  // (spend_cap_state + estimated_spend_usd); the SSE "spendcap" frame patches the
  // same cache so it appears without waiting for the interval refetch. Every
  // surfaced figure is labelled an ESTIMATE.
  const spendCapState = active ? data?.spendCapState : undefined;
  const estimatedSpendUsd = data?.estimatedSpendUsd ?? 0;

  const invalidate = () =>
    queryClient.invalidateQueries({
      queryKey: createConnectQueryKey({
        schema: SessionService.method.getSession,
        cardinality: "finite",
      }),
    });

  // A failing Start/Stop must not be swallowed (#144): surface it (ADR-0017:
  // sonner) and invalidate — a Stop that hits "no active session" means the
  // loop already died server-side, and the refetch snaps the badge off Live.
  const onError = (verb: string) => (err: Error) => {
    toast.error(`Couldn't ${verb} the session: ${err.message}`);
    void invalidate();
  };
  const start = useMutation(SessionService.method.startSession, {
    onSuccess: () => void invalidate(),
    onError: onError("start"),
  });
  const stop = useMutation(SessionService.method.stopSession, {
    onSuccess: () => void invalidate(),
    onError: onError("stop"),
  });

  // The timer runs only while live, counting up from the running session's start.
  const elapsed = useElapsed(active ? tsMs(session?.startedAt) : null);

  // Live transcript: snapshot + SSE tail into the query cache (#73).
  const transcript = useSessionEvents(session?.id, active);
  const hasLines = transcript.lines.length > 0;
  const showTyping = active && transcript.typing.active;

  // Gateway connection state (#123): a fatal rejection is failed from EITHER the
  // durable session status (the reload/poll truth: status "failed" + end_reason) OR
  // the live SSE "connection" frame (immediate, with its detail). The live
  // connecting/connected labels reflect a normal start without a reload.
  const sessionFailed = session?.status === "failed";
  const liveFailed = active && transcript.connection === "failed";
  const failed = sessionFailed || liveFailed;
  const failureReason = sessionFailed ? session?.endReason : transcript.connectionDetail;
  const connectingLabel = active && !failed ? connectionLabel(transcript.connection) : null;

  // Transcript search deep-link (#120): clicking a search hit highlights (and,
  // where the browser supports it, scrolls to) that line in the rendered
  // transcript. A hit for a line NOT in the current view — e.g. an older session —
  // can't scroll anywhere, so the search box shows an inline "not in the view" hint
  // instead of a dead click (ADR-0011 amendment). Relay line ids RESTART per
  // session ("u:<n>"/"a:<turn>"), so "in view" must match the hit's SESSION too —
  // renderedSessionId + renderedLineIds — otherwise an older session's "u:3" would
  // collide with the rendered session's own "u:3" and highlight the wrong line.
  const [highlightedLineId, setHighlightedLineId] = useState<string | null>(null);
  const renderedSessionId = session?.id ?? null;
  const renderedLineIds = useMemo(
    () => new Set(transcript.lines.map((l) => l.id)),
    [transcript.lines],
  );
  const jumpToLine = (lineId: string) => {
    setHighlightedLineId(lineId);
    const el = document.querySelector(`[data-line-id="${lineId}"]`);
    try {
      (el as HTMLElement | null)?.scrollIntoView?.({ block: "center", behavior: "smooth" });
    } catch {
      // jsdom / older browsers: scrollIntoView is a no-op; the highlight still applies.
    }
  };

  return (
    <div className="gx-session">
      <div className="gx-session__main">
      <header className="gx-session__header">
        {campaignName && <span className="gx-overline">{campaignName}</span>}
        <h1>Voice session</h1>
      </header>

      <Card accent className="gx-session__control">
        <div className="gx-session__status">
          {failed ? (
            <Badge variant="danger" dot>
              Failed
            </Badge>
          ) : active ? (
            <Badge variant="live" dot pulse>
              Live
            </Badge>
          ) : (
            <Badge variant="neutral" dot>
              Idle
            </Badge>
          )}
          {connectingLabel && (
            <span className="gx-session__conn" data-testid="connection-state">
              {connectingLabel}
            </span>
          )}
          <span className="gx-session__timer" data-testid="elapsed">
            {formatElapsed(elapsed)}
          </span>
        </div>

        <div className="gx-session__actions">
          {active ? (
            <Button
              variant="danger"
              iconStart={<Square size={15} />}
              onClick={() => stop.mutate({})}
              disabled={stop.isPending}
            >
              Stop session
            </Button>
          ) : (
            <Button
              variant="primary"
              iconStart={<Play size={15} />}
              onClick={() => start.mutate({})}
              disabled={start.isPending}
            >
              Start session
            </Button>
          )}
        </div>
      </Card>

      {failed && (
        <div className="gx-session__failed" role="alert" data-testid="connection-failed">
          {failureReason ? `Voice connection failed: ${failureReason}` : "Voice connection failed."}
        </div>
      )}

      {(spendCapState === "soft" || spendCapState === "hard") && (
        <div className="gx-session__spendcap" role="alert" data-testid="spend-cap">
          {spendCapState === "hard"
            ? "Hard spend cap reached — the session is ending."
            : "Soft spend cap reached — no new Agent turns (in-flight replies finish)."}{" "}
          Estimated spend {formatUsd(estimatedSpendUsd)} (estimated).
        </div>
      )}

      {!active && session && session.status === "ended" && (
        <div className="gx-session__last">{lastSummary(session)}</div>
      )}

      <section className="gx-session__transcript">
        <h2 className="gx-section-title">{active ? "Live transcript" : "Session transcript"}</h2>
        <TranscriptSearch
          onJump={jumpToLine}
          renderedSessionId={renderedSessionId}
          renderedLineIds={renderedLineIds}
        />
        <Card>
          {!hasLines && !showTyping ? (
            <p className="gx-session__transcript-empty">
              {active
                ? "Listening… transcript lines will appear here."
                : "Start a session to capture the table's voice transcript."}
            </p>
          ) : (
            <ol className="gx-transcript">
              {transcript.lines.map((line) => (
                <li
                  key={line.id}
                  className={`gx-line${highlightedLineId === line.id ? " gx-line--highlighted" : ""}`}
                  data-line-id={line.id}
                  data-highlighted={highlightedLineId === line.id ? "true" : undefined}
                >
                  <span className="gx-line__who" data-kind={line.kind}>
                    {line.who}
                  </span>
                  {line.tag && (
                    <span className="gx-line__tag" data-kind={line.kind}>
                      {line.tag}
                    </span>
                  )}
                  <time className="gx-line__ts">{formatClock(line.ts)}</time>
                  <span className="gx-line__text">{line.text}</span>
                </li>
              ))}
              {showTyping && (
                <li className="gx-typing" aria-live="polite" data-testid="typing">
                  <span className="gx-typing__dots" aria-hidden="true">
                    <i />
                    <i />
                    <i />
                  </span>
                  <span className="gx-typing__label">{transcript.typing.label}</span>
                </li>
              )}
            </ol>
          )}
        </Card>
      </section>
      </div>

      <VoicePanel active={active} mutedIds={data?.mutedAgentIds ?? []} />
    </div>
  );
}

// TranscriptSearch is the Session screen's transcript search box (#120, ADR-0011
// amendment). It debounces the raw box value into a SearchTranscriptLines query
// that runs ONLY while the trimmed query is non-empty — an empty box is the prompt
// state, no RPC (keepPreviousData holds the last matches steady across keystrokes).
// The server scopes the search to the operator's Active Campaign and shares the
// one storage search path with /glyphoxa search (AC4/AC5). Each hit renders its
// speaker, tag, timestamp, and matched text; clicking it asks the parent to
// deep-link the line in the rendered transcript (onJump). "In view" means the hit
// belongs to the RENDERED session (renderedSessionId) AND its line is on screen
// (renderedLineIds) — line ids restart per session, so the session must match too.
// A hit that isn't in view (an older session's line) shows an inline "not in the
// view" hint on that result and does NOT highlight, rather than jumping to an
// unrelated line that happens to share the id.
function TranscriptSearch({
  onJump,
  renderedSessionId,
  renderedLineIds,
}: {
  onJump: (lineId: string) => void;
  renderedSessionId: string | null;
  renderedLineIds: Set<string>;
}) {
  const [search, setSearch] = useState("");
  const [debounced, setDebounced] = useState("");
  useEffect(() => {
    const t = setTimeout(() => setDebounced(search), 200);
    return () => clearTimeout(t);
  }, [search]);

  const trimmed = debounced.trim();
  const searching = trimmed !== "";
  const searchQuery = useQuery(
    SessionService.method.searchTranscriptLines,
    { query: debounced },
    // retry:false surfaces a failure promptly; a typeahead re-fires on the next
    // keystroke anyway. keepPreviousData avoids flashing empty between keystrokes.
    { enabled: searching, placeholderData: keepPreviousData, retry: false },
  );
  const lines = searchQuery.data?.lines ?? [];

  // The clicked hit that isn't in the rendered view — flagged for the inline hint,
  // keyed by sessionId:lineId (line ids collide across sessions). Cleared on a new
  // query so a stale hint never lingers.
  const [notInViewKey, setNotInViewKey] = useState<string | null>(null);
  useEffect(() => setNotInViewKey(null), [debounced]);
  const hitKey = (sessionId: string, lineId: string) => `${sessionId}:${lineId}`;
  const openHit = (sessionId: string, lineId: string) => {
    if (sessionId === renderedSessionId && renderedLineIds.has(lineId)) {
      onJump(lineId); // in view: highlight + scroll the rendered line
      setNotInViewKey(null);
    } else {
      setNotInViewKey(hitKey(sessionId, lineId)); // older session: hint, no false highlight
    }
  };

  return (
    <div className="gx-tsearch">
      <Input
        type="search"
        aria-label="Search the transcript"
        icon={<Search size={15} />}
        placeholder="Search the transcript — speakers and text"
        value={search}
        onChange={(e) => setSearch(e.target.value)}
        className="gx-tsearch__input"
      />
      {searching &&
        (searchQuery.isError ? (
          <p className="gx-session__transcript-empty" role="alert">
            Couldn't search the transcript: {searchQuery.error?.message}
          </p>
        ) : lines.length > 0 ? (
          <ul className="gx-tsearch__results" data-testid="transcript-search-results">
            {lines.map((m) => (
              <li key={hitKey(m.sessionId, m.lineId)}>
                <button type="button" className="gx-tsearch__result" onClick={() => openHit(m.sessionId, m.lineId)}>
                  <span className="gx-line__who" data-kind={m.kind}>
                    {m.who}
                  </span>
                  {m.tag && (
                    <span className="gx-line__tag" data-kind={m.kind}>
                      {m.tag}
                    </span>
                  )}
                  <time className="gx-line__ts">{matchClock(m.ts)}</time>
                  <span className="gx-tsearch__text">{m.text}</span>
                </button>
                {notInViewKey === hitKey(m.sessionId, m.lineId) && (
                  <span className="gx-tsearch__hint" role="note">
                    From an earlier session — not in the view.
                  </span>
                )}
              </li>
            ))}
          </ul>
        ) : (
          !searchQuery.isPending && <p className="gx-tsearch__empty">No lines match “{trimmed}”.</p>
        ))}
    </div>
  );
}
