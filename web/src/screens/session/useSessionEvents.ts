import { useEffect } from "react";
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

export function useSessionEvents(sessionId: string | undefined, active: boolean): Transcript {
  const queryClient = useQueryClient();
  // The SSE "mute" frame patches the SHARED getSession cache (not the transcript
  // cache), so the Voice panel reflects a mute from EITHER surface without a
  // reload (#211, AC5). patchOne is referentially stable, so it is a safe effect dep.
  const { patchOne, patchSpendCap } = useMuteCache();
  // Fetch the snapshot for ANY session that exists — live OR ended. A reload of
  // an ended session replays its persisted history from the DB-backed snapshot
  // (#74); the live stream below only opens while the session is active.
  const haveSession = !!sessionId;

  // Snapshot seed: the initial state the live stream then tails (live), or the
  // persisted history (ended). staleTime is Infinity because the EventSource —
  // not refetching — keeps a live session current, and an ended one is immutable.
  const { data, isSuccess } = useQuery<Transcript>({
    queryKey: transcriptKey(sessionId ?? ""),
    enabled: haveSession,
    staleTime: Infinity,
    refetchOnWindowFocus: false,
    queryFn: async () => {
      const res = await fetch(`/api/v1/sessions/${sessionId}`, { credentials: "same-origin" });
      if (!res.ok) throw new Error(`session snapshot failed: ${res.status}`);
      return (await res.json()) as Transcript;
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

  return data ?? EMPTY;
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
