import { useQuery } from "@connectrpc/connect-query";

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

// HIGHLIGHTS_IMAGE_POLL_CAP bounds the image-wait cadence. When image enrichment
// is unconfigured server-side (a nil enqueuer disables it permanently, #311), a
// promoted Highlight's imageContentType stays "" forever — an uncapped 15s poll
// would then run for the life of the screen. Cap it: ~40 polls ≈ 10 min is far
// past any real generation latency, so a settled ended session stops polling.
export const HIGHLIGHTS_IMAGE_POLL_CAP = 40;

// highlightsRefetchInterval is the ListHighlights refetchInterval policy, kept a
// PURE function of (live, highlights) so its four branches are pinned by a unit
// test (mirrors sessionRefetchInterval, Session.tsx). While live, poll to catch
// promotions as they happen — even on an empty list, since the FIRST highlight
// must appear. Once ended, poll only while a promoted Highlight still awaits its
// async image; otherwise the list is settled, so stop.
export function highlightsRefetchInterval(
  live: boolean,
  highlights: Highlight[],
): number | false {
  if (live) return HIGHLIGHTS_LIVE_MS;
  if (highlights.length === 0) return false;
  const awaitingImage = highlights.some(
    (h) => h.status === "promoted" && h.imageContentType === "",
  );
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
      refetchInterval: (query) => {
        const base = highlightsRefetchInterval(live, query.state.data?.highlights ?? []);
        // Cap the ended-session image-wait cadence so an unconfigured enricher
        // (imageContentType never fills) can't poll forever. dataUpdateCount
        // counts successful fetches since mount; once past the cap, stop.
        if (base === HIGHLIGHTS_IMAGE_MS && query.state.dataUpdateCount > HIGHLIGHTS_IMAGE_POLL_CAP) {
          return false;
        }
        return base;
      },
    },
  );
}
