import { useEffect, useState } from "react";
import { useQuery, useMutation, createConnectQueryKey } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { Play, Square } from "lucide-react";
import { toast } from "sonner";
import { timestampMs } from "@bufbuild/protobuf/wkt";

import { SessionService, CampaignService } from "@gen/glyphoxa/management/v1/management_pb";
import type { VoiceSession } from "@gen/glyphoxa/management/v1/management_pb";
import { Card } from "@/components/ui/Card";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { useSessionEvents, formatClock } from "./useSessionEvents";

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

  return (
    <div className="gx-session">
      <header className="gx-session__header">
        {campaignName && <span className="gx-overline">{campaignName}</span>}
        <h1>Voice session</h1>
      </header>

      <Card accent className="gx-session__control">
        <div className="gx-session__status">
          {active ? (
            <Badge variant="live" dot pulse>
              Live
            </Badge>
          ) : (
            <Badge variant="neutral" dot>
              Idle
            </Badge>
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

      {!active && session && session.status === "ended" && (
        <div className="gx-session__last">{lastSummary(session)}</div>
      )}

      <section className="gx-session__transcript">
        <h2 className="gx-section-title">{active ? "Live transcript" : "Session transcript"}</h2>
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
                <li key={line.id} className="gx-line">
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
  );
}
