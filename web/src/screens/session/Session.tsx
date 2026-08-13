import { useEffect, useMemo, useRef, useState } from "react";
import { useQuery, useMutation, createConnectQueryKey } from "@connectrpc/connect-query";
import { Code, ConnectError } from "@connectrpc/connect";
import { useQueryClient, keepPreviousData } from "@tanstack/react-query";
import { Play, Square, Search, ScrollText, Loader2 } from "lucide-react";
import { toast } from "sonner";
import { timestampMs } from "@bufbuild/protobuf/wkt";

import { SessionService, CampaignService, ProviderService } from "@gen/glyphoxa/management/v1/management_pb";
import type { VoiceSession, TranscriptLineMatch } from "@gen/glyphoxa/management/v1/management_pb";
import { Card } from "@/components/ui/Card";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { Input } from "@/components/ui/Input";
import { Select } from "@/components/ui/Select";
import { useI18n, type Lang, type MessageKey, type TFunc } from "@/i18n";
import { useSessionEvents, formatClock } from "./useSessionEvents";
import { VoicePanel } from "./VoicePanel";
import { SessionBindAffordance } from "./SessionBindAffordance";
import { HighlightsStrip } from "./HighlightsStrip";
import { PrepBoards } from "../campaign/PrepBoards";
import { ShareHighlightDialog } from "./ShareHighlightDialog";

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

// connectionLabelKey picks the live gateway connection sub-state beside the Live
// badge during a normal start (#123): "Connecting…" then "Connected". A failed
// state is rendered as its own badge + reason, not here, so this returns null for
// it (and for the pre-first-transition undefined). Returns a MessageKey — the
// caller translates at render time, so no baked-in language (i18n rule 5).
function connectionLabelKey(state: string | undefined): MessageKey | null {
  switch (state) {
    case "connecting":
      return "session.connecting";
    case "connected":
      return "session.connected";
    default:
      return null;
  }
}

// formatUsd renders a USD estimate as a currency amount (#130). Always paired
// with an "(estimated)" label at the call site — it is an approximate figure, not
// a bill. The CURRENCY stays USD (that is what the providers bill in); only its
// presentation follows the display language, so a German operator reads
// "3,21 $" rather than "$3.21".
function formatUsd(usd: number, lang: Lang): string {
  try {
    return new Intl.NumberFormat(lang, { style: "currency", currency: "USD" }).format(usd);
  } catch {
    // A runtime without the currency data still owes the operator a figure.
    return `$${usd.toFixed(2)}`;
  }
}

// formatStamp renders a session's started_at instant as a short "Mon D, HH:MM"
// stamp for the past-session picker label, in the display language's locale.
function formatStamp(d: Date, lang: Lang): string {
  return d.toLocaleString(lang, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}

// sessionOption renders one past-session picker row's label: its start stamp plus
// its line count — or "live" for the still-running session, whose line_count is 0
// until it closes (#270). t/lang are threaded from the calling component so the
// label follows the display language (module-level helpers hold no translation).
function sessionOption(vs: VoiceSession, t: TFunc, lang: Lang): string {
  const startedMs = tsMs(vs.startedAt);
  const when = startedMs != null ? formatStamp(new Date(startedMs), lang) : "—";
  const count =
    vs.status === "running" ? t("session.pickerLive") : t("session.pickerLines", { n: vs.lineCount });
  return `${when} · ${count}`;
}

// lastSummary renders the idle "Last session ended …" line from an ended session.
// The clock follows the display language rather than a hand-rolled 24-hour stamp,
// so an English reader gets "09:00 PM" where a German one gets "21:00".
function lastSummary(session: VoiceSession, t: TFunc, lang: Lang): string {
  const endedMs = tsMs(session.endedAt);
  const startedMs = tsMs(session.startedAt);
  const ended = endedMs != null ? new Date(endedMs) : null;

  const when = ended
    ? ended.toLocaleTimeString(lang, { hour: "2-digit", minute: "2-digit" })
    : "—";

  let h = 0;
  let m = 0;
  if (endedMs != null && startedMs != null) {
    const minutes = Math.max(0, Math.round((endedMs - startedMs) / 60000));
    h = Math.floor(minutes / 60);
    m = minutes % 60;
  }
  const duration = t("session.durationHm", { h, m });
  return t("session.lastSummary", { when, duration, n: session.lineCount });
}

// SessionDeepLink is the palette's arrival vocabulary (#591): session+line
// views a Voice Session and jumps to a transcript Line (always the PAIR — line
// ids restart per session); session+highlight scrolls the Highlights strip to a
// row. The route strips the params after onDeepLinkHandled.
export type SessionDeepLink = { session?: string; line?: string; highlight?: string };

export function Session({
  deepLink,
  onDeepLinkHandled,
}: {
  deepLink?: SessionDeepLink;
  onDeepLinkHandled?: () => void;
} = {}) {
  const { t, lang } = useI18n();
  const queryClient = useQueryClient();
  const { data } = useQuery(SessionService.method.getSession, {}, { refetchInterval: sessionRefetchInterval });
  // retry:false matches every other observer of this shared cache entry (the
  // topbar switcher, Configuration): a fresh install's CodeNotFound settles at
  // once whichever observer triggers the fetch, instead of retry semantics
  // depending on mount order. The header renders from retained data, so a
  // transient blip costs nothing visible here.
  const campaignQ = useQuery(CampaignService.method.getActiveCampaign, {}, { retry: false });
  const campaignName = campaignQ.data?.campaign?.name;
  const activeCampaignId = campaignQ.data?.campaign?.id ?? null;

  const active = data?.active ?? false;
  const session = data?.session;

  // Past-session picker (#270): the operator can browse a prior Voice Session's
  // persisted transcript. ListSessions returns the campaign's sessions newest-first
  // (server-scoped, never a client id). viewedId is the session being VIEWED — null
  // means the current/live default (AC5: the live feed stays the default). retry:false
  // keeps the picker a soft feature — a server without the RPC just shows none.
  const sessionsQ = useQuery(SessionService.method.listSessions, {}, { retry: false });
  const pastSessions = sessionsQ.data?.sessions ?? [];
  const [viewedId, setViewedId] = useState<string | null>(null);
  const currentId = session?.id ?? null;
  const viewingPast = viewedId != null && viewedId !== currentId;
  const renderedSessionId = viewedId ?? currentId;

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

  // The past-session picker list goes stale on a Start: the new running row must
  // appear (labelled "live") without waiting for a window refocus. Stop's stale is
  // covered by the end-sweep (campaignCache watchVoiceSessionEnd), but Start has no
  // such trigger, so refresh listSessions on a successful Start (#270).
  const invalidateSessions = () =>
    queryClient.invalidateQueries({
      queryKey: createConnectQueryKey({
        schema: SessionService.method.listSessions,
        cardinality: "finite",
      }),
    });

  // A failing Start/Stop must not be swallowed (#144): surface it (ADR-0017:
  // sonner) and invalidate — a Stop that hits "no active session" means the
  // loop already died server-side, and the refetch snaps the badge off Live.
  // The server's message stays verbatim, interpolated into the localized template.
  const onError = (key: MessageKey) => (err: Error) => {
    toast.error(t(key, { message: err.message }));
    void invalidate();
  };
  const start = useMutation(SessionService.method.startSession, {
    onSuccess: () => {
      void invalidate();
      void invalidateSessions();
    },
    onError: onError("session.couldntStart"),
  });
  const stop = useMutation(SessionService.method.stopSession, {
    onSuccess: () => void invalidate(),
    onError: onError("session.couldntStop"),
  });

  // Voice-channel picker: the session joins the picked channel; the stored
  // Default Voice Channel pre-selects it (default → first channel → none, the
  // ShareHighlightDialog precedent). retry:false keeps it cheap — a
  // precondition (unlinked guild / missing Bot token) fails once and its
  // actionable message renders as an inline hint beside Start (silence here
  // once dead-ended an operator: no picker, no clue why, and Start falling
  // back to an empty default). Only an Unimplemented server (the RPC not
  // deployed yet) stays silent — the pre-picker experience. Fetched while
  // idle: the pick is a START-time input, and a live session's channel can't
  // change.
  const channelsQ = useQuery(SessionService.method.listSessionVoiceChannels, {}, { retry: false, enabled: !active });
  const channels = channelsQ.data?.channels ?? [];
  const defaultChannelId = channelsQ.data?.defaultChannelId ?? "";
  // The raw server message without the "[failed_precondition]" prefix; null
  // when there is nothing to surface (loaded fine, or Unimplemented).
  const channelsError =
    channelsQ.error != null && ConnectError.from(channelsQ.error).code !== Code.Unimplemented
      ? ConnectError.from(channelsQ.error).rawMessage
      : null;
  const [pickedChannelId, setPickedChannelId] = useState<string | null>(null);
  // A stored default that is no longer among the guild's channels (deleted in
  // Discord) must not ride a Start invisibly: fall back to the first listed
  // channel, which the Select then actually shows.
  const defaultInList = channels.some((c) => c.id === defaultChannelId);
  const selectedChannelId =
    pickedChannelId ?? ((defaultInList ? defaultChannelId : channels[0]?.id) || "");

  // "Set as default" persists the current pick as the guild's Default Voice
  // Channel (SaveDiscordSettings with only voice_channel_id on the wire — the
  // guild stays untouched). Refresh the picker read so the default marker and
  // the Configuration read both follow.
  const invalidateChannels = () =>
    queryClient.invalidateQueries({
      queryKey: createConnectQueryKey({
        schema: SessionService.method.listSessionVoiceChannels,
        cardinality: "finite",
      }),
    });
  const saveDefaultChannel = useMutation(ProviderService.method.saveDiscordSettings, {
    onSuccess: () => {
      void invalidateChannels();
      void queryClient.invalidateQueries({
        queryKey: createConnectQueryKey({
          schema: ProviderService.method.listProviderConfigs,
          cardinality: "finite",
        }),
      });
    },
    onError: (err: Error) =>
      toast.error(t("session.couldntSaveDefaultChannel", { message: err.message })),
  });

  // The timer runs only while live, counting up from the running session's start.
  const elapsed = useElapsed(active ? tsMs(session?.startedAt) : null);

  // Transcript: snapshot + SSE tail into the query cache (#73). The rendered
  // session is the one being VIEWED (viewedId ?? current); the live tail opens only
  // when that IS the live session (active && !viewingPast) — browsing a past session
  // replays its persisted snapshot with no stream (#270, AC5).
  const transcript = useSessionEvents(renderedSessionId ?? undefined, active && !viewingPast, viewingPast);
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
  const connectingKey = active && !failed ? connectionLabelKey(transcript.connection) : null;

  // Transcript search deep-link (#120, extended by #270): clicking a search hit
  // highlights (and, where supported, scrolls to) that line. When the hit is on
  // screen (same rendered session) it highlights at once; when it belongs to
  // ANOTHER session — an older one — the click no longer dead-ends but navigates to
  // that session's persisted transcript and jumps there once it loads (AC4). Relay
  // line ids RESTART per session ("u:<n>"/"a:<turn>"), so both "in view" and the
  // pending jump key on the hit's SESSION too (renderedSessionId + renderedLineIds),
  // otherwise an older session's "u:3" would collide with the rendered "u:3".
  const [highlightedLineId, setHighlightedLineId] = useState<string | null>(null);
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

  // A stale highlight must not carry across a session switch — relay line ids
  // restart per session, so the same id in the newly-viewed session is a different
  // line. Clear it whenever the rendered session changes; the pending jump below
  // re-applies the correct highlight once the new session's lines land.
  useEffect(() => setHighlightedLineId(null), [renderedSessionId]);

  // Pending cross-session jump (#270, AC4): clicking a search hit for a session
  // that isn't on screen sets viewedId + this pending {sessionId, lineId}. Keyed on
  // session AND line because line ids collide across sessions. Once that session's
  // snapshot has loaded (renderedSessionId matches and the line is present), scroll
  // + highlight it, then clear.
  const [pendingJump, setPendingJump] = useState<{ sessionId: string; lineId: string } | null>(null);
  useEffect(() => {
    if (!pendingJump) return;
    if (renderedSessionId === pendingJump.sessionId && renderedLineIds.has(pendingJump.lineId)) {
      jumpToLine(pendingJump.lineId);
      setPendingJump(null);
    }
  }, [pendingJump, renderedSessionId, renderedLineIds]);

  // Pending highlight focus (#591): a palette Highlight hit navigates here with
  // {session, highlight}; once the strip renders THAT session's rows it scrolls
  // to the highlight and reports back. Keyed on session like pendingJump —
  // highlight ids are globally unique, but the strip only lists one session.
  const [focusHighlight, setFocusHighlight] = useState<{
    sessionId: string;
    highlightId: string;
  } | null>(null);

  // viewSession is the ONE navigation seam: switching the viewed session ALWAYS
  // drops any queued cross-session jump, so a manual pick never inherits a stale
  // pendingJump from an earlier search click (which would surprise-scroll once that
  // session's snapshot loads, #270 finding 3). Passing null returns to current/live.
  // The pending highlight focus is state of the same kind and drops with it.
  const viewSession = (id: string | null) => {
    setViewedId(id);
    setPendingJump(null);
    setFocusHighlight(null);
  };

  // Active-Campaign switch reset (#270 finding 1): the topbar switcher sweeps the
  // Active-Campaign-scoped caches (campaignCache.ts), refetching listSessions +
  // getSession for the NEW campaign — but viewedId/pendingJump are local state the
  // sweep can't see, so without this the PREVIOUS campaign's past session keeps
  // rendering under the new campaign's header ("silently serving the previous
  // campaign's data" — the worst failure mode). Reset the view when the resolved
  // Active Campaign id CHANGES from one campaign to another. The initial
  // null→id arrival (a cold getActiveCampaign load) is deliberately not a
  // switch: resetting there would clobber a palette deep link applied on mount
  // (#591) for no gain — on load there is no previous campaign's state to shed.
  const prevCampaignRef = useRef<string | null>(null);
  useEffect(() => {
    const prev = prevCampaignRef.current;
    prevCampaignRef.current = activeCampaignId;
    if (prev != null && prev !== activeCampaignId) viewSession(null);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [activeCampaignId]);

  // openHit routes a clicked search hit: if its line is on screen (same rendered
  // session), highlight it immediately; otherwise navigate to that session and
  // queue the jump for after its transcript loads (no more dead-end, #270 AC4).
  const openHit = (sessionId: string, lineId: string) => {
    if (sessionId === renderedSessionId && renderedLineIds.has(lineId)) {
      jumpToLine(lineId);
      return;
    }
    setViewedId(sessionId === currentId ? null : sessionId);
    setPendingJump({ sessionId, lineId });
  };

  // Palette deep link (#591): {session, line} rides the existing openHit seam
  // (same-session jump or cross-session pending jump); {session, highlight}
  // views the session and queues the strip scroll; a bare {session} just views
  // it. A hit with no line (a semantic chunk that resolved no anchor) opens the
  // session unscrolled — exactly the honest fallback the RPC documents. The
  // params are consumed once; onDeepLinkHandled strips them from the URL.
  const dlSession = deepLink?.session;
  const dlLine = deepLink?.line;
  const dlHighlight = deepLink?.highlight;
  useEffect(() => {
    if (!dlSession) {
      // line/highlight are meaningless without their session half; strip strays.
      if (dlLine || dlHighlight) onDeepLinkHandled?.();
      return;
    }
    if (dlLine) {
      openHit(dlSession, dlLine);
    } else {
      viewSession(dlSession === currentId ? null : dlSession);
    }
    if (dlHighlight) {
      setFocusHighlight({ sessionId: dlSession, highlightId: dlHighlight });
    }
    onDeepLinkHandled?.();
    // The params are the triggers; the handlers are stable seams. currentId is
    // deliberately NOT a trigger: re-running on its load would double-apply.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [dlSession, dlLine, dlHighlight]);

  // Failed transcript snapshot (#270 finding 4, extended to the current session):
  // a DB-backed snapshot fetch that errors must NOT masquerade as the empty state —
  // "start a session" reads as a lost archive, "Listening…" as a mute table. A past
  // session fails fast (retry:false); the current one lands here only after the
  // retry budget absorbed transient blips (useSessionEvents retry policy). Surface
  // it either way: a toast once + an inline error in the transcript card.
  const snapshotFailed = transcript.snapshotFailed;
  useEffect(() => {
    if (snapshotFailed) {
      toast.error(
        viewingPast
          ? t("session.transcriptLoadFailedPastToast")
          : t("session.transcriptLoadFailedCurrent"),
      );
    }
    // t is stable per language; re-toasting on a language switch would be noise.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [snapshotFailed, viewingPast]);

  // Recap (#274, epic #252): the operator regenerates a Butler-flavoured recap of a
  // Voice Session on demand — GenerateRecap is REGENERATED per call and never
  // persisted (gate #271), spends provider money (guarded like a mutation), and can
  // take ~2 min for a long session, so the pending state must survive that whole
  // wait. The button covers the session ON SCREEN: the idle latest-session card
  // recaps that ended session; while browsing a past session it recaps THAT viewed
  // one. The result is a labelled card carrying the covered session's start stamp;
  // it is cleared whenever the rendered session changes so a stale recap never sits
  // under a different session. A failure surfaces as a toast (ADR-0017: sonner).
  const [recapResult, setRecapResult] = useState<{ startedMs: number | null; text: string } | null>(null);
  const generateRecap = useMutation(SessionService.method.generateRecap, {
    onError: (err: Error) => toast.error(t("session.couldntGenerateRecap", { message: err.message })),
  });
  // The rendered session the operator is looking at RIGHT NOW, tracked in a ref so
  // the async onSuccess below can compare against it — a ~2min recap may resolve
  // long after the operator switched sessions.
  const renderedSessionIdRef = useRef(renderedSessionId);
  useEffect(() => {
    renderedSessionIdRef.current = renderedSessionId;
  }, [renderedSessionId]);
  const runRecap = (vs: VoiceSession) => {
    const coveredId = vs.id;
    generateRecap.mutate(
      { sessionIds: [coveredId] },
      {
        onSuccess: (res) => {
          // Drop a recap whose covered session is no longer on screen: an in-flight
          // call that resolves AFTER the operator switched sessions must not render
          // "session of <old>" above the new session's transcript. The clear-on-change
          // useEffect can't catch this — onSuccess fires after it already ran.
          if (renderedSessionIdRef.current !== coveredId) return;
          setRecapResult({ startedMs: tsMs(vs.startedAt), text: res.text });
        },
      },
    );
  };
  // A recap belongs to the session it covered; when the rendered session changes
  // (picker pick or back-to-current) drop it so it never mislabels another session.
  useEffect(() => setRecapResult(null), [renderedSessionId]);

  // The session the on-screen Recap button covers: the picked past session while
  // browsing one, else the idle ended latest-session summary.
  const viewedSession = viewingPast ? pastSessions.find((s) => s.id === viewedId) : undefined;

  return (
    <div className="gx-session">
      <div className="gx-session__main">
      <header className="gx-session__header">
        {campaignName && <span className="gx-overline">{campaignName}</span>}
        <h1>{t("session.title")}</h1>
      </header>

      <Card accent className="gx-session__control">
        <div className="gx-session__status">
          {failed ? (
            <Badge variant="danger" dot>
              {t("session.statusFailed")}
            </Badge>
          ) : active ? (
            <Badge variant="live" dot pulse>
              {t("session.statusLive")}
            </Badge>
          ) : (
            <Badge variant="neutral" dot>
              {t("session.statusIdle")}
            </Badge>
          )}
          {connectingKey && (
            <span className="gx-session__conn" data-testid="connection-state">
              {t(connectingKey)}
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
              {t("session.stopSession")}
            </Button>
          ) : (
            <>
              {channels.length > 0 && (
                <div className="gx-session__channel" data-testid="channel-picker">
                  <Select
                    aria-label={t("session.voiceChannel")}
                    placeholder={t("session.voiceChannelPlaceholder")}
                    options={channels.map((c) => ({ value: c.id, label: c.name }))}
                    value={selectedChannelId || undefined}
                    onValueChange={setPickedChannelId}
                  />
                  {selectedChannelId !== "" && selectedChannelId !== defaultChannelId && (
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => saveDefaultChannel.mutate({ voiceChannelId: selectedChannelId })}
                      disabled={saveDefaultChannel.isPending}
                      data-testid="set-default-channel"
                    >
                      {t("session.setAsDefault")}
                    </Button>
                  )}
                </div>
              )}
              {/* The picker's failure/empty states surface HERE, not as silence:
                  the lister's precondition carries the operator's next step
                  ("link a Discord server first" / "save a Discord Bot token
                  first"), and hiding it once left Start dead-ending on an empty
                  default with no visible cause. */}
              {channelsError && (
                <span className="gx-session__channel-hint" role="alert" data-testid="channel-hint">
                  {channelsError}
                </span>
              )}
              {channelsQ.isSuccess && channels.length === 0 && (
                <span className="gx-session__channel-hint" data-testid="channel-hint">
                  {t("session.noVoiceChannels")}
                </span>
              )}
              <Button
                variant="primary"
                iconStart={<Play size={15} />}
                onClick={() => start.mutate({ voiceChannelId: selectedChannelId })}
                // Held while the channel list is still loading: a click in that
                // window would send no channel and fail on deployments with no
                // stored default, even though a pre-selected channel was moments
                // away. An errored/empty list re-enables (Start then reports the
                // server's actionable precondition).
                disabled={start.isPending || channelsQ.isLoading}
              >
                {t("session.startSession")}
              </Button>
            </>
          )}
        </div>
      </Card>

      {/* In-flight bind affordance (#279): while a session is live the GM can map an
          unmapped Player to a Character without leaving/restarting the session. */}
      {active && <SessionBindAffordance />}

      {failed && (
        <div className="gx-session__failed" role="alert" data-testid="connection-failed">
          {/* The end reason / connection detail is a server string — verbatim,
              interpolated into the localized template (i18n rule 3). */}
          {failureReason
            ? t("session.connectionFailedReason", { reason: failureReason })
            : t("session.connectionFailed")}
        </div>
      )}

      {(spendCapState === "soft" || spendCapState === "hard") && (
        <div className="gx-session__spendcap" role="alert" data-testid="spend-cap">
          {spendCapState === "hard" ? t("session.spendCapHard") : t("session.spendCapSoft")}{" "}
          {t("session.spendEstimate", { usd: formatUsd(estimatedSpendUsd, lang) })}
        </div>
      )}

      {!active && session && session.status === "ended" && (
        <div className="gx-session__last">
          <span className="gx-session__last-text">{lastSummary(session, t, lang)}</span>
          {/* The latest-card Recap covers the idle ended session; hidden while
              browsing a past one, whose own Recap button lives in the picker view
              (so only one Recap button is ever on screen). */}
          {!viewingPast && (
            <Button
              variant="secondary"
              size="sm"
              iconStart={<ScrollText size={15} />}
              onClick={() => runRecap(session)}
              disabled={generateRecap.isPending}
              data-testid="recap-button"
            >
              {t("session.recap")}
            </Button>
          )}
        </div>
      )}

      <section className="gx-session__transcript">
        <h2 className="gx-section-title">
          {active && !viewingPast ? t("session.liveTranscript") : t("session.sessionTranscript")}
        </h2>
        {pastSessions.length > 0 && (
          <SessionPicker
            sessions={pastSessions}
            renderedSessionId={renderedSessionId}
            viewingPast={viewingPast}
            onPick={(id) => viewSession(id === currentId ? null : id)}
            onBackToCurrent={() => viewSession(null)}
          />
        )}
        {viewingPast && viewedSession && (
          <div className="gx-session__recap-bar">
            <Button
              variant="secondary"
              size="sm"
              iconStart={<ScrollText size={15} />}
              onClick={() => runRecap(viewedSession)}
              disabled={generateRecap.isPending}
              data-testid="recap-button"
            >
              {t("session.recap")}
            </Button>
          </div>
        )}
        <RecapView pending={generateRecap.isPending} result={recapResult} />
        <TranscriptSearch onOpen={openHit} />
        <Card>
          {snapshotFailed ? (
            <p className="gx-session__transcript-empty" role="alert" data-testid="snapshot-error">
              {viewingPast
                ? t("session.transcriptLoadFailedPastInline")
                : t("session.transcriptLoadFailedCurrent")}
            </p>
          ) : !hasLines && !showTyping ? (
            <p className="gx-session__transcript-empty">
              {active && !viewingPast
                ? t("session.listening")
                : viewingPast
                  ? t("session.noTranscriptLines")
                  : t("session.startToCapture")}
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

      {/* Session Highlights (#309, Epic 8): the rendered session's epic moments —
          live promotions stream in while running, an ended session shows its
          persisted set on selection. renderedSessionId unifies live + picked past
          session; the whole section stays out when there is no session at all. */}
      {renderedSessionId && (
        <section className="gx-session__highlights">
          <h2 className="gx-section-title">{t("session.highlightsTitle")}</h2>
          <HighlightsStrip
            sessionId={renderedSessionId}
            live={active && !viewingPast}
            focusHighlightId={
              focusHighlight && focusHighlight.sessionId === renderedSessionId
                ? focusHighlight.highlightId
                : null
            }
            onFocusHandled={() => setFocusHighlight(null)}
            renderActions={(h) =>
              h.status === "promoted" ? (
                <ShareHighlightDialog highlight={h} sessionLive={active} />
              ) : null
            }
          />
        </section>
      )}
      {/* Session prep boards (#543): the GM's own running order for tonight —
          the handful of entries this session is actually about, in the order they
          expect to need them. It belongs HERE rather than only in the Knowledge
          tab, because at the table the board IS the session, and the alternative
          is scrolling a wiki mid-scene. */}
      <section className="gx-session__boards">
        <h2 className="gx-section-title">{t("session.boardTitle")}</h2>
        {/* No onOpenNode: at the table the board is a REFERENCE — the names and
            the order — not a way to start editing the wiki mid-scene. */}
        <PrepBoards />
      </section>
      </div>

      <VoicePanel active={active} mutedIds={data?.mutedAgentIds ?? []} />
    </div>
  );
}

// SessionPicker is the Session screen's past-session picker (#270): a compact list
// of the campaign's Voice Sessions newest-first (ListSessions, server-scoped). Each
// row is a button labelled by start stamp + line count ("live" for the running one);
// picking it views that session's persisted transcript. The row matching the
// rendered session is aria-pressed. While viewing a PAST session a "Back to current
// session" control returns to the live/latest default (AC5).
function SessionPicker({
  sessions,
  renderedSessionId,
  viewingPast,
  onPick,
  onBackToCurrent,
}: {
  sessions: VoiceSession[];
  renderedSessionId: string | null;
  viewingPast: boolean;
  onPick: (id: string) => void;
  onBackToCurrent: () => void;
}) {
  const { t, lang } = useI18n();
  return (
    <div className="gx-session__picker" data-testid="session-picker">
      <span className="gx-session__picker-label">{t("session.pickerLabel")}</span>
      <ul className="gx-session__picker-list">
        {sessions.map((vs) => (
          <li key={vs.id}>
            <button
              type="button"
              className="gx-session__picker-item"
              aria-pressed={vs.id === renderedSessionId}
              onClick={() => onPick(vs.id)}
            >
              {sessionOption(vs, t, lang)}
            </button>
          </li>
        ))}
      </ul>
      {viewingPast && (
        <button type="button" className="gx-session__picker-back" onClick={onBackToCurrent}>
          {t("session.backToCurrent")}
        </button>
      )}
    </div>
  );
}

// RecapView renders the Recap request's pending + result states (#274). While the
// GenerateRecap call is in flight it shows a spinner (the button is disabled at the
// call site) — the call can take ~2 min for a long session, so this pending row
// must survive the whole wait. On success it shows a card labelled with the covered
// session's start stamp holding the recap prose. Nothing renders before the first
// Recap of the current view.
function RecapView({
  pending,
  result,
}: {
  pending: boolean;
  result: { startedMs: number | null; text: string } | null;
}) {
  const { t, lang } = useI18n();
  if (pending) {
    return (
      <div className="gx-session__recap-pending" role="status" data-testid="recap-pending">
        <Loader2 size={15} className="gx-spin" aria-hidden="true" />
        <span>{t("session.generatingRecap")}</span>
      </div>
    );
  }
  if (!result) return null;
  const label = result.startedMs != null ? formatStamp(new Date(result.startedMs), lang) : "—";
  return (
    <Card className="gx-session__recap" data-testid="recap-result">
      <div className="gx-session__recap-label">{t("session.recapLabel", { stamp: label })}</div>
      <p className="gx-session__recap-text">{result.text}</p>
    </Card>
  );
}

// TranscriptSearch is the Session screen's transcript search box (#120, ADR-0011
// amendment). It debounces the raw box value into a SearchTranscriptLines query
// that runs ONLY while the trimmed query is non-empty — an empty box is the prompt
// state, no RPC (keepPreviousData holds the last matches steady across keystrokes).
// The server scopes the search to the operator's Active Campaign and shares the
// one storage search path with /glyphoxa search (AC4/AC5). Each hit renders its
// speaker, tag, timestamp, and matched text; clicking it asks the parent to open
// the line (onOpen) — the parent highlights it when it is on screen, else navigates
// to that session's transcript and jumps there once it loads (#270, AC4). Line ids
// restart per session, so onOpen always carries the hit's session id too.
function TranscriptSearch({ onOpen }: { onOpen: (sessionId: string, lineId: string) => void }) {
  const { t } = useI18n();
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
  const hitKey = (sessionId: string, lineId: string) => `${sessionId}:${lineId}`;

  return (
    <div className="gx-tsearch">
      <Input
        type="search"
        aria-label={t("session.searchTranscript")}
        icon={<Search size={15} />}
        placeholder={t("session.searchPlaceholder")}
        value={search}
        onChange={(e) => setSearch(e.target.value)}
        className="gx-tsearch__input"
      />
      {searching &&
        (searchQuery.isError ? (
          <p className="gx-session__transcript-empty" role="alert">
            {t("session.couldntSearch", { message: searchQuery.error?.message ?? "" })}
          </p>
        ) : lines.length > 0 ? (
          <ul className="gx-tsearch__results" data-testid="transcript-search-results">
            {lines.map((m) => (
              <li key={hitKey(m.sessionId, m.lineId)}>
                <button type="button" className="gx-tsearch__result" onClick={() => onOpen(m.sessionId, m.lineId)}>
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
              </li>
            ))}
          </ul>
        ) : (
          !searchQuery.isPending && (
            <p className="gx-tsearch__empty">{t("session.noLinesMatch", { query: trimmed })}</p>
          )
        ))}
    </div>
  );
}
