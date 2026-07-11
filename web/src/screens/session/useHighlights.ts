import { useQuery } from "@connectrpc/connect-query";
import { timestampMs } from "@bufbuild/protobuf/wkt";

import { SessionService } from "@gen/glyphoxa/management/v1/management_pb";
import type { Highlight } from "@gen/glyphoxa/management/v1/management_pb";

// HIGHLIGHTS_LIVE_MS is the ListHighlights poll cadence while a session is live —
// promotions land server-side (a candidate flips to promoted, #308) and the strip
// must show them "as they're promoted" (AC) without a reload. Modest: highlights
// are coarse-grained, so 10s catches them promptly without hammering the RPC.
export const HIGHLIGHTS_LIVE_MS = 10_000;

// HIGHLIGHTS_IMAGE_MS is the poll cadence for an ENDED session that still holds a
// promoted Highlight without an image: the AI scene (#311) is enriched
// asynchronously after promotion, so a slower 15s poll lets the <img> appear once
// the generation lands, then stops once every promoted Highlight has its image.
export const HIGHLIGHTS_IMAGE_MS = 15_000;

// IMAGE_WAIT_MAX_MS bounds how long after promotion the strip keeps polling for a
// promoted Highlight's async image. A TIME bound (vs a poll counter) because the
// counter is cumulative per cache entry — live 10s polls, focus refetches and
// invalidations all increment it, so a long live session would exhaust a poll cap
// before it even ends, and failed fetches increment errorUpdateCount not
// dataUpdateCount, so a permanently-500ing session would never hit the cap. Time
// keyed on promotedAt closes both: 10 min is far past any real generation
// latency, so an unconfigured enricher (imageContentType never fills, #311)
// settles the strip instead of polling forever.
export const IMAGE_WAIT_MAX_MS = 10 * 60_000;

// highlightsRefetchInterval is the ListHighlights refetchInterval policy, kept a
// PURE function of (live, highlights, now) so its branches are pinned by a unit
// test (mirrors sessionRefetchInterval, Session.tsx). While live, poll to catch
// promotions as they happen — even on an empty list, since the FIRST highlight
// must appear. Once ended, poll only while a promoted Highlight is still WITHIN
// the post-promotion image-wait window; otherwise the list is settled, so stop.
export function highlightsRefetchInterval(
  live: boolean,
  highlights: Highlight[],
  now: number = Date.now(),
): number | false {
  if (live) return HIGHLIGHTS_LIVE_MS;
  if (highlights.length === 0) return false;
  const awaitingImage = highlights.some((h) => {
    if (h.status !== "promoted" || h.imageContentType !== "") return false;
    // Only a RECENTLY-promoted image-less Highlight is worth waiting on; an
    // unset promotedAt (shouldn't happen for a promoted row) is treated as not
    // waiting rather than waiting forever.
    const promotedMs = h.promotedAt ? Number(timestampMs(h.promotedAt)) : null;
    return promotedMs != null && now - promotedMs < IMAGE_WAIT_MAX_MS;
  });
  return awaitingImage ? HIGHLIGHTS_IMAGE_MS : false;
}

// useHighlights loads one Voice Session's Session Highlights (#308) into the
// connect-query cache — the single state tree (ADR-0018), never a parallel
// useState list. Disabled until a session id is known; the refetch cadence is the
// pure policy above. `live` reflects whether the rendered session is the running
// one (promotions stream in) versus a settled ended session.
export function useHighlights(sessionId: string | undefined, live: boolean) {
  return useQuery(
    SessionService.method.listHighlights,
    { voiceSessionId: sessionId ?? "" },
    {
      enabled: !!sessionId,
      // retry:false settles a failure into isError at once instead of backing off
      // silently — the strip surfaces it inline (never a false empty state, #270),
      // and while live the refetch interval re-fires the read on its own.
      retry: false,
      refetchInterval: (query) =>
        highlightsRefetchInterval(live, query.state.data?.highlights ?? []),
    },
  );
}
