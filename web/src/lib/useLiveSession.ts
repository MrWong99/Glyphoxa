import { useEffect, useState } from "react";
import { useQuery } from "@connectrpc/connect-query";
import { timestampMs } from "@bufbuild/protobuf/wkt";

import { SessionService, type VoiceSession } from "@gen/glyphoxa/management/v1/management_pb";

// useLiveSession is the app-wide "is a Voice Session live right now" signal.
// The shell mounts it on every screen, so the sidebar's Session item and the
// topbar pill can say Live from the wiki or Setup, not only from the Session
// screen itself.
//
// It observes the SAME cache entry the Session screen reads (getSession with an
// empty request), which matters twice over:
//
//   - one answer: the Session screen's own poll, its SSE patches (mute, spend
//     cap) and this hook all read and write one entry, so the sidebar can never
//     disagree with the screen;
//   - watchVoiceSessionEnd (lib/campaignCache) watches that entry for the
//     active true→false edge to re-run the Active-Campaign sweep. Before this
//     hook the entry was only polled while the Session screen was open, so a
//     campaign switch deferred by a live session never "came due" on any other
//     screen. Polling here makes the promise hold everywhere.
//
// Cadence: brisk while live (an ended session should drop its marker within
// seconds), slow while idle (a session started from Discord's /session start or
// another tab should still surface without a reload). React Query pauses both
// while the tab is hidden.

/** getSession poll cadence while a Voice Session is live. */
export const LIVE_SESSION_REFETCH_MS = 5_000;
/** getSession poll cadence while idle. */
export const IDLE_SESSION_REFETCH_MS = 30_000;

/** The refetchInterval policy, exported so the cadence is pinned by a test. */
export function liveSessionRefetchInterval(query: { state: { data?: { active?: boolean } } }): number {
  return query.state.data?.active ? LIVE_SESSION_REFETCH_MS : IDLE_SESSION_REFETCH_MS;
}

export type LiveSession = {
  /** True exactly while a Voice Session is running. */
  live: boolean;
  /** The running session while live; undefined otherwise. */
  session: VoiceSession | undefined;
  /** When the live session started, in epoch milliseconds; null when idle. */
  startedMs: number | null;
};

export function useLiveSession(): LiveSession {
  const { data } = useQuery(SessionService.method.getSession, {}, { refetchInterval: liveSessionRefetchInterval });
  // Rendered from retained data: a failed background refetch keeps the last
  // known answer instead of flickering the marker off.
  const live = data?.active ?? false;
  const session = live ? data?.session : undefined;
  const startedMs = session?.startedAt ? Number(timestampMs(session.startedAt)) : null;
  return { live, session, startedMs };
}

// useElapsedSeconds ticks once a second from a start instant (epoch ms) and
// rests at 0 while there is none. Call it in the SMALLEST component that shows
// the clock — it re-renders its caller every second.
export function useElapsedSeconds(startMs: number | null): number {
  const [elapsed, setElapsed] = useState(() => secondsSince(startMs));
  useEffect(() => {
    if (startMs == null) {
      setElapsed(0);
      return;
    }
    const tick = () => setElapsed(secondsSince(startMs));
    tick();
    const id = setInterval(tick, 1000);
    return () => clearInterval(id);
  }, [startMs]);
  return elapsed;
}

function secondsSince(startMs: number | null): number {
  return startMs == null ? 0 : Math.max(0, Math.floor((Date.now() - startMs) / 1000));
}

// formatElapsed renders a second count as zero-padded HH:MM:SS — the format the
// Session screen's own clock uses. Restated here rather than imported from the
// Session screen: that would pull the Session chunk into the entry bundle the
// shell lives in.
export function formatElapsed(totalSeconds: number): string {
  const s = Math.max(0, Math.floor(totalSeconds));
  return [Math.floor(s / 3600), Math.floor((s % 3600) / 60), s % 60]
    .map((n) => String(n).padStart(2, "0"))
    .join(":");
}
