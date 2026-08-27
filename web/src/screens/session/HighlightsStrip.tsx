import { useEffect, useState, type ReactNode } from "react";
import { useMutation } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { timestampMs } from "@bufbuild/protobuf/wkt";
import { Sparkles, Trash2 } from "lucide-react";
import { toast } from "sonner";

import { SessionService } from "@gen/glyphoxa/management/v1/management_pb";
import type { Highlight } from "@gen/glyphoxa/management/v1/management_pb";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { stripMarkdown } from "@/components/ui/Markdown";
import { ConfirmDialog } from "@/components/ui/ConfirmDialog";
import { invalidateMethodQueries } from "@/lib/queryClient";
import { useI18n, type Lang } from "@/i18n";
import { formatClock } from "./useSessionEvents";
import { HighlightSoundMenu } from "./HighlightSoundMenu";
import { useHighlights } from "./useHighlights";
import { errorMessage } from "@/lib/connectError";

// clipClock renders a Highlight bound (starts_at/ends_at) as an "HH:MM:SS" clock,
// reusing the transcript's formatClock. An unset bound renders "".
function clipClock(ts: Highlight["startsAt"]): string {
  const ms = ts ? Number(timestampMs(ts)) : null;
  return ms == null ? "" : formatClock(new Date(ms).toISOString());
}

// fmtScore renders the classifier's score to one decimal in the DISPLAY language:
// a German operator reads "8,5", not "8.5". The clock range above it is a
// wall-clock HH:MM:SS and stays language-neutral by design.
function fmtScore(score: number, lang: Lang): string {
  return score.toLocaleString(lang, { minimumFractionDigits: 1, maximumFractionDigits: 1 });
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
  focusHighlightId = null,
  onFocusHandled,
}: {
  sessionId: string | undefined;
  live: boolean;
  // renderActions is a per-row action slot the Session screen can fill — reserved
  // for #310's Share button, kept a slot here so this slice ships no share UI.
  renderActions?: (h: Highlight) => ReactNode;
  // focusHighlightId scrolls the strip to that row once it renders — the palette
  // deep link (#591), the KnowledgePanel focusNodeID pattern: the strip owns the
  // "my data has landed" moment, so the scroll waits here, not in the parent. It
  // no-ops until the row is present and reports back via onFocusHandled.
  focusHighlightId?: string | null;
  onFocusHandled?: () => void;
}) {
  const { t, lang } = useI18n();
  const queryClient = useQueryClient();
  const query = useHighlights(sessionId, live);
  const highlights = query.data?.highlights ?? [];

  useEffect(() => {
    if (!focusHighlightId) return;
    if (!highlights.some((h) => h.id === focusHighlightId)) return; // wait for the row
    const el = document.querySelector(`[data-highlight-id="${focusHighlightId}"]`);
    try {
      (el as HTMLElement | null)?.scrollIntoView?.({ block: "center", behavior: "smooth" });
    } catch {
      // jsdom / older browsers: scrollIntoView is a no-op; the focus still resolves.
    }
    onFocusHandled?.();
    // highlights is the "row landed" trigger; onFocusHandled is a stable seam.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [focusHighlightId, highlights]);

  // The Highlight a delete has been requested for; drives the confirm dialog.
  // Deletion cascades the clip through the blob seam (ADR-0051/0048) and is
  // irreversible, so no DeleteHighlight fires until the operator confirms here.
  const [confirming, setConfirming] = useState<Highlight | null>(null);

  // A promote/delete must refresh the list read (the single state tree, ADR-0018)
  // — never a hand-patch. No input key = prefix match across every session's
  // cached ListHighlights.
  const invalidate = () =>
    void invalidateMethodQueries(queryClient, SessionService.method.listHighlights);

  const promote = useMutation(SessionService.method.promoteHighlight, {
    onSuccess: () => invalidate(),
    onError: (err: Error) => toast.error(t("session.couldntPromote", { message: errorMessage(err) })),
  });

  const remove = useMutation(SessionService.method.deleteHighlight, {
    onSuccess: () => invalidate(),
    onError: (err: Error) => toast.error(t("session.couldntDelete", { message: errorMessage(err) })),
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
        {t("session.couldntLoadHighlights", { message: errorMessage(query.error) })}
      </p>
    );
  }
  // Before the first read lands there is nothing to show; the empty-state copy is
  // reserved for a settled, genuinely-empty list so it never flashes while loading.
  if (query.isPending) {
    return <div className="gx-skeleton" data-testid="highlights-loading" />;
  }

  // A refetch failure over cached data (any shape — full or EMPTY list) keeps the
  // last loaded set on screen; the notice flags the staleness in both branches so
  // a cached-empty list + failed refetch doesn't read as a clean, settled empty.
  const staleNotice = query.isError ? (
    <p className="gx-highlights__stale" role="alert" data-testid="highlights-stale-error">
      {t("session.couldntRefreshHighlights")}
    </p>
  ) : null;

  if (highlights.length === 0) {
    // "Rollover tape" is internal vocabulary — users see "Highlight recording"
    // (glossary), matching the renamed switch in Campaign settings.
    return (
      <>
        {staleNotice}
        <p className="gx-highlights__empty">{t("session.noHighlights")}</p>
      </>
    );
  }

  return (
    <>
    {staleNotice}
    <ul className="gx-highlights__list">
      {highlights.map((h) => {
        const isCandidate = h.status === "candidate";
        const range = `${clipClock(h.startsAt)}–${clipClock(h.endsAt)}`;
        // The excerpt can quote Agent speech (the Rollover Tape records Agent
        // lines too) and the reason is classifier output — both markdown-prone.
        // These are quote/caption surfaces, so markdown is DISABLED (flattened)
        // rather than rendered, matching the appearance rows and palette.
        const excerpt = stripMarkdown(h.excerpt);
        const reason = stripMarkdown(h.reason);
        // A short, speakable label for the otherwise-anonymous native controls.
        const clipLabel = t("session.clipLabel", { excerpt: excerpt.slice(0, 40) });
        return (
          <li key={h.id} className="gx-highlight" data-highlight-id={h.id}>
            <div className="gx-highlight__head">
              {isCandidate ? (
                <Badge variant="neutral" size="sm">
                  {t("session.candidateBadge")}
                </Badge>
              ) : (
                <Badge variant="live" size="sm">
                  {t("session.promotedBadge")}
                </Badge>
              )}
              <time className="gx-highlight__clock">{range}</time>
              <span className="gx-highlight__score">{fmtScore(h.score, lang)}</span>
            </div>
            <blockquote className="gx-highlight__excerpt">{excerpt}</blockquote>
            <p className="gx-highlight__reason">{reason}</p>
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
                alt={reason}
                loading="lazy"
              />
            )}
            {/* Generated sound (#312): a separate audio next to the clip — the
                attach-as-blob decision, layered/offered client-side, zero DSP.
                A requested-but-unlanded sound shows a pending note instead
                (generating, or failed/unconfigured — re-run or Remove clears it). */}
            {h.soundContentType !== "" && (
              <audio
                className="gx-highlight__audio gx-highlight__sound"
                controls
                preload="none"
                aria-label={t("session.soundClipLabel")}
                src={`/api/v1/highlights/${h.id}/sound`}
              />
            )}
            {h.soundKind !== "" && h.soundContentType === "" && (
              <p className="gx-highlight__sound-pending" data-testid="sound-pending">
                {t("session.soundPending")}
              </p>
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
                  {t("session.promote")}
                </Button>
              )}
              <Button
                variant="ghost"
                size="sm"
                iconStart={<Trash2 size={14} />}
                aria-label={t("session.deleteHighlightAria", { range })}
                onClick={() => setConfirming(h)}
                disabled={remove.isPending}
              >
                {t("common.delete")}
              </Button>
              {/* Sound is opt-in AFTER promotion (#312), so the action renders
                  on promoted rows only — everywhere the strip renders them. */}
              {!isCandidate && <HighlightSoundMenu highlight={h} />}
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
          title={t("session.deleteHighlightTitle")}
          description={t("session.deleteHighlightDescription")}
          confirmLabel={t("session.deleteHighlightConfirm")}
          onConfirm={() => {
            remove.mutate({ id: confirming.id });
            setConfirming(null);
          }}
        />
      )}
    </>
  );
}
