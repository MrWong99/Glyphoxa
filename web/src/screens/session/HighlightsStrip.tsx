import { useState, type ReactNode } from "react";
import { useMutation, createConnectQueryKey } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { timestampMs } from "@bufbuild/protobuf/wkt";
import { Sparkles, Trash2 } from "lucide-react";
import { toast } from "sonner";

import { SessionService } from "@gen/glyphoxa/management/v1/management_pb";
import type { Highlight } from "@gen/glyphoxa/management/v1/management_pb";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { ConfirmDialog } from "@/components/ui/ConfirmDialog";
import { formatClock } from "./useSessionEvents";
import { useHighlights } from "./useHighlights";

// clipClock renders a Highlight bound (starts_at/ends_at) as an "HH:MM:SS" clock,
// reusing the transcript's formatClock. An unset bound renders "".
function clipClock(ts: Highlight["startsAt"]): string {
  const ms = ts ? Number(timestampMs(ts)) : null;
  return ms == null ? "" : formatClock(new Date(ms).toISOString());
}

// HighlightsStrip is the Session screen's highlight-replay surface (#309, Epic 8):
// the Session Highlights of the rendered Voice Session, newest moment first (the
// server's #308 order). Each row shows its status, clock range, score, the
// caption excerpt and the classifier's reason, and a native <audio> element that
// streams the clip from the cookie-authed same-origin blob path
// (/api/v1/highlights/{id}/clip) — native controls give scrub/replay/Range for
// free, so no audio.ts helper is involved.
export function HighlightsStrip({
  sessionId,
  live,
  renderActions,
}: {
  sessionId: string | undefined;
  live: boolean;
  // renderActions is a per-row action slot the Session screen can fill — reserved
  // for #310's Share button, kept a slot here so this slice ships no share UI.
  renderActions?: (h: Highlight) => ReactNode;
}) {
  const queryClient = useQueryClient();
  const query = useHighlights(sessionId, live);
  const highlights = query.data?.highlights ?? [];

  // The Highlight a delete has been requested for; drives the confirm dialog.
  // Deletion cascades the clip through the blob seam (ADR-0051/0048) and is
  // irreversible, so no DeleteHighlight fires until the operator confirms here.
  const [confirming, setConfirming] = useState<Highlight | null>(null);

  // A promote/delete must refresh the list read (the single state tree, ADR-0018)
  // — never a hand-patch. No input key = prefix match across every session's
  // cached ListHighlights.
  const invalidate = () =>
    void queryClient.invalidateQueries({
      queryKey: createConnectQueryKey({
        schema: SessionService.method.listHighlights,
        cardinality: "finite",
      }),
    });

  const promote = useMutation(SessionService.method.promoteHighlight, {
    onSuccess: () => invalidate(),
    onError: (err: Error) => toast.error(`Couldn't promote the highlight: ${err.message}`),
  });

  const remove = useMutation(SessionService.method.deleteHighlight, {
    onSuccess: () => invalidate(),
    onError: (err: Error) => toast.error(`Couldn't delete the highlight: ${err.message}`),
  });

  // No rendered session (fresh install, never started) — there is nothing to list,
  // and the Session screen's own transcript card already prompts to start one, so
  // the strip stays out of the way entirely.
  if (!sessionId) return null;

  // A load failure with NO cached data must not masquerade as the empty state
  // (#270 lesson): "no highlights yet" reads as a consent-off / never-armed
  // session, hiding a real failure. Full-replace with the error only in that
  // no-data case. A failure WITH stale data (a 10s-poll blip mid-playback, or a
  // refetch-on-focus on a settled ended session) is handled below by retaining
  // the list — full-replacing there would unmount every <audio> and, since the
  // ended-session interval is false, strand the strip in error forever (mirrors
  // the getSession "renders from retained data" posture, Session.tsx).
  if (query.isError && !query.data) {
    return (
      <p className="gx-highlights__error" role="alert">
        Couldn't load the highlights: {query.error.message}
      </p>
    );
  }
  // Before the first read lands there is nothing to show; the empty-state copy is
  // reserved for a settled, genuinely-empty list so it never flashes while loading.
  if (query.isPending) {
    return <div className="gx-skeleton" data-testid="highlights-loading" />;
  }
  if (highlights.length === 0) {
    return (
      <p className="gx-highlights__empty">
        No highlights yet — epic moments appear here when the rollover tape is armed
        and consented.
      </p>
    );
  }

  return (
    <>
    {query.isError && (
      <p className="gx-highlights__stale" role="alert" data-testid="highlights-stale-error">
        Couldn't refresh the highlights — showing the last loaded set.
      </p>
    )}
    <ul className="gx-highlights__list">
      {highlights.map((h) => {
        const isCandidate = h.status === "candidate";
        const range = `${clipClock(h.startsAt)}–${clipClock(h.endsAt)}`;
        // A short, speakable label for the otherwise-anonymous native controls.
        const clipLabel = `Clip: ${h.excerpt.slice(0, 40)}`;
        return (
          <li key={h.id} className="gx-highlight" data-highlight-id={h.id}>
            <div className="gx-highlight__head">
              {isCandidate ? (
                <Badge variant="neutral" size="sm">
                  Candidate — auto-deletes in 7 days
                </Badge>
              ) : (
                <Badge variant="live" size="sm">
                  Promoted
                </Badge>
              )}
              <time className="gx-highlight__clock">{range}</time>
              <span className="gx-highlight__score">{h.score.toFixed(1)}</span>
            </div>
            <blockquote className="gx-highlight__excerpt">{h.excerpt}</blockquote>
            <p className="gx-highlight__reason">{h.reason}</p>
            <audio
              className="gx-highlight__audio"
              controls
              preload="none"
              aria-label={clipLabel}
              src={`/api/v1/highlights/${h.id}/clip`}
            />
            {h.imageContentType !== "" && (
              <img
                className="gx-highlight__image"
                src={`/api/v1/highlights/${h.id}/image`}
                alt={h.reason}
                loading="lazy"
              />
            )}
            <div className="gx-highlight__actions">
              {isCandidate && (
                <Button
                  variant="secondary"
                  size="sm"
                  iconStart={<Sparkles size={14} />}
                  onClick={() => promote.mutate({ id: h.id })}
                  disabled={promote.isPending}
                >
                  Promote
                </Button>
              )}
              <Button
                variant="ghost"
                size="sm"
                iconStart={<Trash2 size={14} />}
                aria-label={`Delete highlight ${range}`}
                onClick={() => setConfirming(h)}
                disabled={remove.isPending}
              >
                Delete
              </Button>
              {renderActions?.(h)}
            </div>
          </li>
        );
      })}
    </ul>
      {confirming && (
        <ConfirmDialog
          open
          onOpenChange={(open) => {
            if (!open) setConfirming(null);
          }}
          title="Delete this highlight?"
          description="This permanently deletes the highlight and its clip. This can't be undone."
          confirmLabel="Delete highlight"
          onConfirm={() => {
            remove.mutate({ id: confirming.id });
            setConfirming(null);
          }}
        />
      )}
    </>
  );
}
