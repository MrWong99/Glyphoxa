import { useEffect, useRef } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";

import { useMuteCache } from "./muteCache";

// useSessionEvents is the SSE transcript client (#73, ADR-0014 Hop-B). When a
// voice session is live it seeds a TanStack query from the JSON snapshot
// (GET /api/v1/sessions/:id) then keeps it fresh by amending the SAME cache
// entry from an EventSource (ADR-0018: no parallel React state tree). The
// EventSource closes on unmount / when the session ends.

export type LineKind = "gm" | "player" | "npc" | "butler";

// TranscriptLine mirrors the relay's Line JSON. id is stable so a turn's
// coalescing NPC reply upserts in place; ts is RFC3339.
export interface TranscriptLine {
  id: string;
  who: string;
  tag?: string;
  kind: LineKind;
  ts: string;
  text: string;
}

// TypingState is the relay's derived "is speaking" / "listening" indicator.
export interface TypingState {
  active: boolean;
  label: string;
}

// ConnectionState is the Voice Session's Discord gateway connection lifecycle as
// the Session screen renders it (#123): connecting → connected on a normal start,
// or failed with a readable reason on a fatal rejection.
export type ConnectionState = "connecting" | "connected" | "failed";

// Transcript is the cached view the screen renders: the lines plus status/typing
// and the live gateway connection state (#123).
export interface Transcript {
  lines: TranscriptLine[];
  status: "live" | "idle";
  typing: TypingState;
  // connection is the latest gateway connection state, undefined before the first
  // transition; connectionDetail is the readable reason on a failed connection.
  connection?: ConnectionState;
  connectionDetail?: string;
}

const EMPTY: Transcript = { lines: [], status: "idle", typing: { active: false, label: "" } };

// transcriptKey is the cache key for one session's live transcript.
export function transcriptKey(id: string) {
  return ["sessionTranscript", id] as const;
}

// upsertLine replaces a line with the same id (the turn's coalescing reply) or
// appends a new one, preserving arrival order.
function upsertLine(prev: Transcript | undefined, line: TranscriptLine): Transcript {
  const base = prev ?? EMPTY;
  const lines = base.lines.slice();
  const i = lines.findIndex((l) => l.id === line.id);
  if (i >= 0) lines[i] = line;
  else lines.push(line);
  return { ...base, lines };
}

// SessionTranscript is the cached Transcript plus a transport signal the screen
// needs but the cache must not carry: snapshotFailed is true when the DB-backed
// snapshot fetch failed with nothing cached, so the screen — a picked past session
// AND the current/live one — can surface an error instead of masquerading as the
// empty "start a session" / "Listening…" state (#270).
export type SessionTranscript = Transcript & { snapshotFailed: boolean };

export function useSessionEvents(
  sessionId: string | undefined,
  active: boolean,
  viewingPast = false,
): SessionTranscript {
  const queryClient = useQueryClient();
  // The SSE "mute" frame patches the SHARED getSession cache (not the transcript
  // cache), so the Voice panel reflects a mute from EITHER surface without a
  // reload (#211, AC5). patchOne is referentially stable, so it is a safe effect dep.
  const { patchOne, patchSpendCap } = useMuteCache();
  // Sessions whose live stream this hook has already opened at least once — the
  // reopen-after-browse resync (#270) keys off it. A ref so it survives re-renders
  // and effect re-runs without itself triggering one.
  const streamedBefore = useRef<Set<string>>(new Set());
  // Fetch the snapshot for ANY session that exists — live OR ended. A reload of
  // an ended session replays its persisted history from the DB-backed snapshot
  // (#74); the live stream below only opens while the session is active.
  const haveSession = !!sessionId;

  // Snapshot seed: the initial state the live stream then tails (live), or the
  // persisted history (ended). staleTime is Infinity because the EventSource —
  // not refetching — keeps a live session current, and an ended one is immutable.
  const { data, isSuccess, isError } = useQuery<Transcript>({
    queryKey: transcriptKey(sessionId ?? ""),
    enabled: haveSession,
    staleTime: Infinity,
    refetchOnWindowFocus: false,
    // Retry policy splits by view (#270): for a PAST session, retry:false surfaces
    // a failed snapshot at once (the screen shows an error) instead of masking it
    // behind backoff. For the CURRENT/live session, KEEP retries so a transient 500
    // is absorbed — else isSuccess never flips, the SSE tail never opens, and the
    // live screen is stuck on "Listening…" forever (staleTime Infinity, no refocus
    // refetch); a failure that outlasts the budget surfaces the same snapshot
    // error. A reopen resync or manual re-pick re-fetches either way.
    retry: viewingPast ? false : 2,
    queryFn: async () => {
      const res = await fetch(`/api/v1/sessions/${sessionId}`, { credentials: "same-origin" });
      if (!res.ok) throw new Error(`session snapshot failed: ${res.status}`);
      // Normalize lines defensively: a zero-line snapshot must never surface as
      // null or the screen's .length reads crash (#248) — the server contract is
      // "lines":[], but the screen should survive an older/other server too.
      const t = (await res.json()) as Transcript;
      return { ...t, lines: t.lines ?? [] };
    },
  });

  useEffect(() => {
    // The live tail is only meaningful while the session is active; an ended
    // session has no stream, just the persisted snapshot above.
    // Gate the stream on snapshot success (FIX 4): if the snapshot resolved AFTER
    // an SSE line write it would clobber the cache and drop that line. Opening
    // only once the snapshot has landed makes that ordering impossible; the
    // first-connect ring replay (idempotent upsert) re-covers the small window
    // between the snapshot capture and the stream opening.
    if (!active || !sessionId || !isSuccess) return;
    const key = transcriptKey(sessionId);

    // Reopen-after-browse resync (#270): browsing a past session closes this
    // stream; returning re-runs the effect with a fresh everConnected=false, so the
    // reconnect-invalidate below never fires for the first open. If we have streamed
    // THIS session before (in this hook's life), the browse may have skipped more
    // than the ring's 500-frame replay can recover, so re-fetch the authoritative
    // snapshot on reopen. upsert-by-id makes the refetch + replay overlap idempotent.
    if (streamedBefore.current.has(sessionId)) {
      void queryClient.invalidateQueries({ queryKey: key });
    }
    streamedBefore.current.add(sessionId);

    const es = new EventSource(`/api/v1/sessions/${sessionId}/events`);

    // Re-sync the authoritative snapshot on a RECONNECT (any "open" after the
    // first, FIX 3): the Last-Event-ID ring replay covers bounded lag, this
    // covers an unbounded gap. upsert-by-id makes the refetch + replay overlap
    // idempotent.
    let everConnected = false;
    es.addEventListener("open", () => {
      if (everConnected) void queryClient.invalidateQueries({ queryKey: key });
      everConnected = true;
    });

    es.addEventListener("line", (e) => {
      const line = JSON.parse((e as MessageEvent).data) as TranscriptLine;
      queryClient.setQueryData<Transcript>(key, (prev) => upsertLine(prev, line));
    });
    es.addEventListener("status", (e) => {
      const s = JSON.parse((e as MessageEvent).data) as { status: "live" | "idle"; typing: TypingState };
      queryClient.setQueryData<Transcript>(key, (prev) => ({ ...(prev ?? EMPTY), status: s.status, typing: s.typing }));
    });
    es.addEventListener("mute", (e) => {
      const m = JSON.parse((e as MessageEvent).data) as { agent_id: string; muted: boolean };
      patchOne(m.agent_id, m.muted);
    });
    es.addEventListener("spendcap", (e) => {
      // The session's estimated spend crossed a cap (#130): flip the spend-cap
      // state on the shared getSession cache so the Session screen shows the
      // spend-cap-reached badge immediately; the interval refetch fills in the
      // estimated spend the frame does not carry.
      const s = JSON.parse((e as MessageEvent).data) as { level: string };
      patchSpendCap(s.level);
    });
    es.addEventListener("connection", (e) => {
      // The gateway connection state moved (#123): reflect connecting → connected
      // live, or failed with its readable reason — no page reload.
      const c = JSON.parse((e as MessageEvent).data) as { state: ConnectionState; detail?: string };
      queryClient.setQueryData<Transcript>(key, (prev) => ({
        ...(prev ?? EMPTY),
        connection: c.state,
        connectionDetail: c.detail ?? "",
      }));
    });

    return () => es.close();
  }, [active, sessionId, isSuccess, queryClient, patchOne, patchSpendCap]);

  // snapshotFailed only counts for a session that exists — a null id is the
  // never-run state, not a failure — and only when the snapshot NEVER landed (no
  // cached data): a failed resync refetch keeps rendering the retained lines
  // instead of swapping a visible transcript for an error card.
  return { ...(data ?? EMPTY), snapshotFailed: haveSession && isError && !data };
}

// formatClock renders an RFC3339 instant as zero-padded HH:MM:SS (the design's
// per-line timestamp), or "" when unparseable.
export function formatClock(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  return [d.getHours(), d.getMinutes(), d.getSeconds()]
    .map((n) => String(n).padStart(2, "0"))
    .join(":");
}
