import { useEffect } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";

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

// Transcript is the cached view the screen renders: the lines plus status/typing.
export interface Transcript {
  lines: TranscriptLine[];
  status: "live" | "idle";
  typing: TypingState;
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
  const enabled = active && !!sessionId;

  // Snapshot seed: the initial state the live stream then tails. staleTime is
  // Infinity because the EventSource — not refetching — keeps it current.
  const { data } = useQuery<Transcript>({
    queryKey: transcriptKey(sessionId ?? ""),
    enabled,
    staleTime: Infinity,
    refetchOnWindowFocus: false,
    queryFn: async () => {
      const res = await fetch(`/api/v1/sessions/${sessionId}`, { credentials: "same-origin" });
      if (!res.ok) throw new Error(`session snapshot failed: ${res.status}`);
      return (await res.json()) as Transcript;
    },
  });

  useEffect(() => {
    if (!enabled || !sessionId) return;
    const key = transcriptKey(sessionId);
    const es = new EventSource(`/api/v1/sessions/${sessionId}/events`);

    es.addEventListener("line", (e) => {
      const line = JSON.parse((e as MessageEvent).data) as TranscriptLine;
      queryClient.setQueryData<Transcript>(key, (prev) => upsertLine(prev, line));
    });
    es.addEventListener("status", (e) => {
      const s = JSON.parse((e as MessageEvent).data) as { status: "live" | "idle"; typing: TypingState };
      queryClient.setQueryData<Transcript>(key, (prev) => ({ ...(prev ?? EMPTY), status: s.status, typing: s.typing }));
    });

    return () => es.close();
  }, [enabled, sessionId, queryClient]);

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
