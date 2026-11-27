import { useEffect, useLayoutEffect, useMemo, useRef, useState, type CSSProperties } from "react";
import { useQuery, useMutation } from "@connectrpc/connect-query";
import { Code, ConnectError } from "@connectrpc/connect";
import { useQueryClient } from "@tanstack/react-query";
import { ArrowLeft, Copy, Download, Mic, Play, ScrollText, Square, TriangleAlert } from "lucide-react";
import { toast } from "sonner";

import { SessionService, CampaignService, ProviderService } from "@gen/glyphoxa/management/v1/management_pb";
import type { VoiceSession } from "@gen/glyphoxa/management/v1/management_pb";
import { Alert } from "@/components/ui/Alert";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { ConfirmDialog } from "@/components/ui/ConfirmDialog";
import { EmptyState } from "@/components/ui/EmptyState";
import { ErrorState } from "@/components/ui/ErrorState";
import { Page, PageHeader } from "@/components/ui/Page";
import { SegmentedControl } from "@/components/ui/SegmentedControl";
import { Select } from "@/components/ui/Select";
import { Skeleton } from "@/components/ui/Skeleton";
import { FirstCampaignCTA } from "@/components/FirstCampaignCTA";
import { useScreenNav, type ScreenSearch } from "@/app/screenNav";
import { errorMessage, isNotFound } from "@/lib/connectError";
import { downloadBlob } from "@/lib/download";
import { invalidateMethodQueries } from "@/lib/queryClient";
import { formatElapsed, useElapsedSeconds } from "@/lib/useLiveSession";
import { useI18n, type Lang, type MessageKey, type TFunc } from "@/i18n";
import { failureText, reasonNeedsSetup } from "./failureText";
import { HighlightsStrip } from "./HighlightsStrip";
import { readLastVoiceChannel, rememberVoiceChannel } from "./lastVoiceChannel";
import { Preflight, discordCheck } from "./Preflight";
import { RecapCard, copyText } from "./RecapCard";
import { SessionBindAffordance, type BindPrefill } from "./SessionBindAffordance";
import { SessionBoard } from "./SessionBoard";
import { SessionHistory } from "./SessionHistory";
import { formatDay, formatDuration, formatStamp, formatTime, formatUsd, sessionDurationMs, tsMs } from "./sessionFormat";
import { ShareHighlightDialog } from "./ShareHighlightDialog";
import { speakingAgentName } from "./speakers";
import { sessionActionErrorMessage } from "./startStopError";
import { transcriptFilename, transcriptMarkdown } from "./transcriptMarkdown";
import { TranscriptPane } from "./TranscriptPane";
import { TranscriptSearch } from "./TranscriptSearch";
import { UnlinkedVoiceNotice } from "./UnlinkedVoiceNotice";
import { useHighlights } from "./useHighlights";
import { useGenerateRecap, useRecapResult, useRecapsInFlight } from "./useRecap";
import { useSessionEvents } from "./useSessionEvents";
import { VoicePanel } from "./VoicePanel";

import "./session.css";

// The Session screen (#72) drives the live Voice Session on the live
// SessionService (ADR-0039): Start/Stop call the in-process SessionManager, the
// status badge and elapsed timer reflect the running session, and an idle screen
// summarizes the last one that ended.
//
// It is laid out for the table, not for a settings page:
//
//   - while live, the control bar (status, clock, Stop) sticks to the top of
//     the content area and the voice rail stays beside it, so Stop and mute are
//     always one move away;
//   - the transcript scrolls in its OWN bounded pane, following the newest
//     line, so the page under those controls never moves;
//   - Transcript, Highlights and the prep Board are tabs over one pane instead
//     of three sections stacked down a page;
//   - idle, the control card answers "will tonight work?" before Start is
//     pressed (pre-flight), and afterwards says why a session ended.
//
// Which session is on screen is the URL's ?session= (useScreenNav, ADR-0063
// amendment): Back steps through the sessions the GM looked at and a reload
// restores the view. ?line= and ?highlight= are one-shot deep links into that
// session, applied once and stripped.

export { formatElapsed };

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

// STOP_CONFIRM_AFTER_S is how long a session must have run before Stop asks
// first. Under a minute a Stop is a false start being corrected; after it, the
// button ends an evening for a whole table, so a slip must not.
export const STOP_CONFIRM_AFTER_S = 60;

type Tab = "transcript" | "highlights" | "board";

// lastSummary renders the idle "Last session ended …" line from an ended session.
// The clock follows the display language rather than a hand-rolled 24-hour stamp,
// so an English reader gets "09:00 PM" where a German one gets "21:00".
function lastSummary(session: VoiceSession, t: TFunc, lang: Lang): string {
  const endedMs = tsMs(session.endedAt);
  const ended = endedMs != null ? new Date(endedMs) : null;

  // Time-only for a session that ended today; older sessions carry the date
  // (and the year once it differs) — a bare "ended 11:59 PM" days later reads
  // as tonight.
  const when = ended
    ? ended.toDateString() === new Date().toDateString()
      ? formatTime(ended, lang)
      : formatStamp(ended, lang)
    : "—";

  const duration = formatDuration(sessionDurationMs(session) ?? 0, t);
  return session.lineCount === 1
    ? t("session.lastSummaryOne", { when, duration })
    : t("session.lastSummary", { when, duration, n: session.lineCount });
}

// SessionClock is the live count-up. Its own component because it re-renders
// every second, and the screen around it should not.
function SessionClock({ startMs }: { startMs: number | null }) {
  const elapsed = useElapsedSeconds(startMs);
  return (
    <span className="gx-session__timer" data-testid="elapsed">
      {formatElapsed(elapsed)}
    </span>
  );
}

export function Session() {
  const { t, lang } = useI18n();
  const nav = useScreenNav();
  const queryClient = useQueryClient();
  const { data, isPending: sessionPending } = useQuery(
    SessionService.method.getSession,
    {},
    { refetchInterval: sessionRefetchInterval },
  );
  // retry:false matches every other observer of this shared cache entry (the
  // topbar switcher, Configuration): a fresh install's CodeNotFound settles at
  // once whichever observer triggers the fetch, instead of retry semantics
  // depending on mount order. The header renders from retained data, so a
  // transient blip costs nothing visible here.
  const campaignQ = useQuery(CampaignService.method.getActiveCampaign, {}, { retry: false });
  const campaign = campaignQ.data?.campaign;
  const campaignName = campaign?.name;
  const activeCampaignId = campaign?.id ?? null;

  // The campaign's Agents: the voice rail lists them, the pre-flight counts
  // them, and the transcript draws each NPC in its rail colour. One cache
  // entry, shared with the rail's own observer.
  const rosterQ = useQuery(CampaignService.method.getCampaignRoster, {});
  const roster = useMemo(() => rosterQ.data?.roster ?? [], [rosterQ.data]);

  const active = data?.active ?? false;
  const session = data?.session;
  const currentId = session?.id ?? null;

  // ---- which session is on screen ------------------------------------------
  //
  // ListSessions returns the campaign's sessions newest-first (server-scoped,
  // never a client id). retry:false keeps the history a soft feature — a server
  // without the RPC just shows none.
  const sessionsQ = useQuery(SessionService.method.listSessions, {}, { retry: false });
  const pastSessions = useMemo(() => sessionsQ.data?.sessions ?? [], [sessionsQ.data]);

  // ?session= is the session being VIEWED; absent means the current one — the
  // live feed stays the default (#270 AC5). Until getSession has settled there
  // is no "current" to compare against, so nothing is rendered as past yet: a
  // link to the LIVE session must not flash the past-session view (#591).
  const viewedId = nav.search.session ?? null;
  const viewingPast = !sessionPending && viewedId != null && viewedId !== currentId;
  const renderedSessionId = sessionPending ? null : (viewedId ?? currentId);
  const viewedSession = viewingPast ? pastSessions.find((s) => s.id === viewedId) : undefined;
  // The session on screen, when its row is known: the viewed past one, else
  // the current one.
  const renderedSession = viewingPast ? viewedSession : session;
  const renderedLive = active && !viewingPast;

  // Spend-cap-reached state (#130, ADR-0046): the live reload truth is GetSession
  // (spend_cap_state + estimated_spend_usd); the SSE "spendcap" frame patches the
  // same cache so it appears without waiting for the interval refetch. Every
  // surfaced figure is labelled an ESTIMATE.
  const spendCapState = active ? data?.spendCapState : undefined;
  const estimatedSpendUsd = data?.estimatedSpendUsd ?? 0;

  const invalidate = () => invalidateMethodQueries(queryClient, SessionService.method.getSession);
  // The history list goes stale on a Start: the new running row must appear
  // without waiting for a window refocus. Stop's stale is covered by the
  // end-sweep (campaignCache watchVoiceSessionEnd).
  const invalidateSessions = () => invalidateMethodQueries(queryClient, SessionService.method.listSessions);

  // A failing Start/Stop must not be swallowed (#144): surface it (ADR-0017:
  // sonner) and invalidate — a Stop that hits "no active session" means the
  // loop already died server-side, and the refetch snaps the badge off Live.
  // The reasons the server marks are translated (startStopError.ts); any other
  // message stays verbatim, interpolated into the localized template.
  const guildIdRef = useRef("");
  const onError = (key: MessageKey) => (err: Error) => {
    toast.error(t(key, { message: sessionActionErrorMessage(err, t) }));
    void invalidate();
  };
  const start = useMutation(SessionService.method.startSession, {
    onSuccess: (_res, req) => {
      // Remember where this session joined, so the next visit pre-selects it.
      // Only on success: a channel the Bot could not join is not worth keeping.
      rememberVoiceChannel(guildIdRef.current, req.voiceChannelId ?? "");
      void invalidate();
      void invalidateSessions();
    },
    onError: onError("session.couldntStart"),
  });
  const stop = useMutation(SessionService.method.stopSession, {
    onSuccess: () => void invalidate(),
    onError: onError("session.couldntStop"),
  });
  const [confirmingStop, setConfirmingStop] = useState(false);
  const startedMs = active ? tsMs(session?.startedAt) : null;
  const requestStop = () => {
    const ranSeconds = startedMs != null ? (Date.now() - startedMs) / 1000 : 0;
    if (ranSeconds < STOP_CONFIRM_AFTER_S) stop.mutate({});
    else setConfirmingStop(true);
  };

  // ---- voice channel ---------------------------------------------------------
  //
  // The session joins the picked channel; the last-used channel, else the
  // stored Default Voice Channel, pre-selects it. retry:false keeps the read
  // cheap — a precondition (unlinked guild / missing Bot token) fails once and
  // becomes the pre-flight's Discord row. Fetched while idle: the pick is a
  // START-time input, and a live session's channel can't change.
  const channelsQ = useQuery(
    SessionService.method.listSessionVoiceChannels,
    {},
    { retry: false, enabled: !active },
  );
  const channels = useMemo(() => channelsQ.data?.channels ?? [], [channelsQ.data]);
  const defaultChannelId = channelsQ.data?.defaultChannelId ?? "";
  const channelsUnsupported =
    channelsQ.error != null && ConnectError.from(channelsQ.error).code === Code.Unimplemented;
  // The raw server message without the "[failed_precondition]" prefix; null
  // when there is nothing to surface (loaded fine, or no such RPC).
  const channelsError =
    channelsQ.error != null && !channelsUnsupported ? ConnectError.from(channelsQ.error).rawMessage : null;
  const [pickedChannelId, setPickedChannelId] = useState<string | null>(null);
  // Selection precedence: what the GM picked on this visit → the channel the
  // last session was started in (remembered per Discord server in this
  // browser, lastVoiceChannel.ts) → the Tenant's Default Voice Channel → none.
  // The guild id keys the memory; it rides the Tenant's provider config read
  // (shared with Configuration, campaign-invariant). retry:false: without it
  // the memory is simply skipped.
  const guildId =
    useQuery(ProviderService.method.listProviderConfigs, {}, { retry: false }).data?.guildId ?? "";
  guildIdRef.current = guildId;
  const lastChannelId = useMemo(() => readLastVoiceChannel(guildId), [guildId]);
  // A remembered or default id that is no longer among the guild's channels
  // (deleted in Discord) must not ride a Start invisibly, so each rung only
  // counts when the list still has it. With nothing known the Select shows its
  // placeholder and Start waits for a pick — silently joining whichever channel
  // Discord happens to list first put the Bot in the wrong room.
  const inList = (id: string | null | undefined) => !!id && channels.some((c) => c.id === id);
  const selectedChannelId =
    pickedChannelId ?? (inList(lastChannelId) ? lastChannelId! : inList(defaultChannelId) ? defaultChannelId : "");
  // Only a LOADED, non-empty list can demand a pick: an empty list still starts
  // with no channel and lets the server report its precondition.
  const channelPickNeeded = channels.length > 0 && selectedChannelId === "";

  const discord = discordCheck(
    {
      loading: channelsQ.isLoading,
      error: channelsError,
      unsupported: channelsUnsupported,
      channelCount: channels.length,
      selectedChannelName: channels.find((c) => c.id === selectedChannelId)?.name ?? null,
    },
    t,
  );

  // "Set as default" persists the current pick as the guild's Default Voice
  // Channel (SaveDiscordSettings with only voice_channel_id on the wire — the
  // guild stays untouched). Refresh the picker read so the default marker and
  // the Configuration read both follow.
  const saveDefaultChannel = useMutation(ProviderService.method.saveDiscordSettings, {
    onSuccess: () => {
      void invalidateMethodQueries(
        queryClient,
        SessionService.method.listSessionVoiceChannels,
        ProviderService.method.listProviderConfigs,
      );
    },
    onError: (err: Error) => toast.error(t("session.couldntSaveDefaultChannel", { message: errorMessage(err) })),
  });

  // ---- transcript --------------------------------------------------------------
  //
  // Snapshot + SSE tail into the query cache (#73). The live tail opens only
  // when the rendered session IS the running one; a past session replays its
  // persisted snapshot with no stream (#270, AC5).
  const transcript = useSessionEvents(renderedSessionId ?? undefined, renderedLive, viewingPast);
  const hasLines = transcript.lines.length > 0;
  // The RUNNING session's transcript, whichever session is on screen: the
  // control bar describes the live session, and a past session's snapshot
  // carries no connection state. While a past one is viewed, a second,
  // stream-less read holds the live session's entry: the state its stream last
  // reported, or its snapshot when the page opened straight onto a past one.
  const liveWhileBrowsing = useSessionEvents(active && viewingPast ? (currentId ?? undefined) : undefined, false);
  const liveTranscript = viewingPast ? liveWhileBrowsing : transcript;
  const liveConnection = liveTranscript.connection;
  const liveHasLines = liveTranscript.lines.length > 0;

  // Gateway connection state (#123): a fatal rejection is failed from EITHER the
  // durable session status (the reload/poll truth: status "failed" + end_reason) OR
  // the live SSE "connection" frame (immediate, with its detail).
  const sessionFailed = session?.status === "failed";
  const liveFailed = active && liveConnection === "failed";
  const failed = sessionFailed || liveFailed;
  const failureReason = sessionFailed ? session?.endReason : liveTranscript.connectionDetail;
  // "Live" is a claim that the Bot is in the channel, so the badge only makes
  // it once the relay reports the gateway connected — until then it reads
  // "Connecting…". A transcript line is proof of the same thing, for a relay
  // that reports no connection state.
  const connected =
    active && !failed && (liveConnection === "connected" || (liveConnection === undefined && liveHasLines));

  // ---- tabs --------------------------------------------------------------------
  //
  // One pane, three views. A view is mounted the first time it is opened and
  // then kept (hidden) — a clip keeps playing while the GM reads the
  // transcript, and the board does not refetch on every glance.
  const [tab, setTab] = useState<Tab>("transcript");
  const [visited, setVisited] = useState<ReadonlySet<Tab>>(() => new Set<Tab>(["transcript"]));
  const showTab = (next: Tab) => {
    setTab(next);
    setVisited((prev) => (prev.has(next) ? prev : new Set([...prev, next])));
  };

  // ---- deep links: jump to a line, focus a highlight ---------------------------
  //
  // Clicking a search hit highlights (and scrolls to) that line. Relay line ids
  // RESTART per session ("u:<n>"/"a:<turn>"), so both "in view" and the pending
  // jump key on the hit's SESSION too — otherwise an older session's "u:3"
  // would collide with the rendered "u:3" (#270).
  const [highlightedLineId, setHighlightedLineId] = useState<string | null>(null);
  const renderedLineIds = useMemo(() => new Set(transcript.lines.map((l) => l.id)), [transcript.lines]);
  const jumpToLine = (lineId: string) => {
    setHighlightedLineId(lineId);
    const el = document.querySelector(`[data-line-id="${lineId}"]`);
    try {
      (el as HTMLElement | null)?.scrollIntoView?.({ block: "center", behavior: "smooth" });
    } catch {
      // jsdom / older browsers: scrollIntoView is a no-op; the highlight still applies.
    }
  };
  // A stale highlight must not carry across a session switch — the same id in
  // the newly-viewed session is a different line.
  useEffect(() => setHighlightedLineId(null), [renderedSessionId]);

  // A jump to a line of a session that is not on screen yet waits here until
  // that session's snapshot has loaded (#270 AC4).
  const [pendingJump, setPendingJump] = useState<{ sessionId: string; lineId: string } | null>(null);
  useEffect(() => {
    if (!pendingJump) return;
    if (renderedSessionId === pendingJump.sessionId && renderedLineIds.has(pendingJump.lineId)) {
      jumpToLine(pendingJump.lineId);
      setPendingJump(null);
    }
  }, [pendingJump, renderedSessionId, renderedLineIds]);

  // A palette Highlight hit: once the strip renders THAT session's rows it
  // scrolls to the highlight and reports back (#591).
  const [focusHighlight, setFocusHighlight] = useState<{ sessionId: string; highlightId: string } | null>(null);

  // viewSession is the ONE way the viewed session changes by hand: it always
  // drops a queued jump or focus, so a manual pick never inherits a stale one
  // from an earlier search click (#270 finding 3). null returns to current/live.
  const { setSearch } = nav;
  const viewSession = (id: string | null) => {
    setPendingJump(null);
    setFocusHighlight(null);
    setSearch({ session: id == null || id === currentId ? undefined : id, line: undefined, highlight: undefined });
  };

  // openHit routes a clicked search hit: if its line is on screen, highlight it
  // at once; otherwise view that session and queue the jump (#270 AC4).
  const openHit = (sessionId: string, lineId: string) => {
    showTab("transcript");
    if (sessionId === renderedSessionId && renderedLineIds.has(lineId)) {
      jumpToLine(lineId);
      return;
    }
    setFocusHighlight(null);
    setPendingJump({ sessionId, lineId });
    if (sessionId !== renderedSessionId) {
      setSearch({ session: sessionId === currentId ? undefined : sessionId, line: undefined, highlight: undefined });
    }
  };

  // The one-shot URL keys. ?line= and ?highlight= name something INSIDE
  // ?session= (always the pair — line ids restart per session): they are
  // applied once getSession has settled and then stripped (replace, no history
  // entry), so a later jump to the same target fires again. ?session= itself
  // stays — it is the view — unless it names the current session, where it is
  // only noise. All of it is ONE patch: two setSearch calls in a tick would
  // race each other.
  const dlSession = nav.search.session;
  const dlLine = nav.search.line;
  const dlHighlight = nav.search.highlight;
  const appliedLink = useRef("");
  useEffect(() => {
    if (sessionPending) return;
    const patch: ScreenSearch = {};
    let strip = false;
    if (dlLine || dlHighlight) {
      const signature = `${dlSession ?? ""}|${dlLine ?? ""}|${dlHighlight ?? ""}`;
      // line/highlight are meaningless without their session half: strays are
      // only stripped.
      if (dlSession && appliedLink.current !== signature) {
        appliedLink.current = signature;
        if (dlLine) {
          setPendingJump({ sessionId: dlSession, lineId: dlLine });
          showTab("transcript");
        }
        if (dlHighlight) {
          setFocusHighlight({ sessionId: dlSession, highlightId: dlHighlight });
          if (!dlLine) showTab("highlights");
        }
      }
      patch.line = undefined;
      patch.highlight = undefined;
      strip = true;
    } else {
      appliedLink.current = "";
    }
    if (dlSession && dlSession === currentId) {
      patch.session = undefined;
      strip = true;
    }
    if (strip) setSearch(patch, { replace: true });
    // The params and the settle gate are the triggers; setSearch is read as of
    // the run.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [dlSession, dlLine, dlHighlight, sessionPending, currentId]);

  // Active-Campaign switch (#270 finding 1): the switcher sweeps the
  // Active-Campaign-scoped caches, but ?session= names a session of the
  // PREVIOUS campaign — without this the old campaign's transcript keeps
  // rendering under the new campaign's name. Drop the view when the resolved
  // campaign id CHANGES from one campaign to another. The initial null→id
  // arrival (a cold load) is not a switch: resetting there would throw away a
  // deep link, for no gain.
  const prevCampaignRef = useRef<string | null>(null);
  useEffect(() => {
    const prev = prevCampaignRef.current;
    prevCampaignRef.current = activeCampaignId;
    if (prev != null && prev !== activeCampaignId) {
      setPendingJump(null);
      setFocusHighlight(null);
      setSearch({ session: undefined, line: undefined, highlight: undefined }, { replace: true });
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [activeCampaignId]);

  // A failed transcript snapshot (#270 finding 4) must NOT masquerade as the
  // empty state — "start a session" reads as a lost archive, "Listening…" as a
  // mute table. It is an inline error with a retry, and a toast once.
  const snapshotFailed = transcript.snapshotFailed;
  useEffect(() => {
    if (snapshotFailed) {
      toast.error(viewingPast ? t("session.transcriptLoadFailedPastToast") : t("session.transcriptLoadFailedCurrent"));
    }
    // t is stable per language; re-toasting on a language switch would be noise.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [snapshotFailed, viewingPast]);

  // ---- recap -------------------------------------------------------------------
  //
  // Recaps live in the query cache, keyed by the session they cover
  // (useRecap.ts), so the one on screen is simply the rendered session's.
  const generateRecap = useGenerateRecap();
  const recap = useRecapResult(renderedSessionId);
  const recapsInFlight = useRecapsInFlight();
  const recapPending = renderedSessionId != null && recapsInFlight.has(renderedSessionId);
  // A recap needs an ended session's persisted lines.
  // A session with no lines (a false start) has nothing to recap.
  const canRecap = renderedSession != null && renderedSession.status !== "running" && renderedSession.lineCount > 0;

  // ---- highlights --------------------------------------------------------------
  //
  // The strip reads this same cache entry; observing it here too gives the tab
  // its count and lets a highlight captured mid-session announce itself while
  // the GM is on another tab.
  const highlightsQ = useHighlights(renderedSessionId ?? undefined, renderedLive);
  const highlights = useMemo(() => highlightsQ.data?.highlights ?? [], [highlightsQ.data]);
  const seenHighlights = useRef<{ sessionId: string | null; ids: Set<string> | null }>({ sessionId: null, ids: null });
  useEffect(() => {
    const seen = seenHighlights.current;
    if (seen.sessionId !== renderedSessionId) {
      seen.sessionId = renderedSessionId;
      seen.ids = null;
    }
    if (!highlightsQ.data) return;
    const ids = new Set(highlights.map((h) => h.id));
    // The first load is what was already there, not news.
    if (seen.ids && renderedLive) {
      const fresh = highlights.filter((h) => !seen.ids!.has(h.id));
      if (fresh.length > 0) {
        toast(t("session.highlightCaptured"), {
          action: { label: t("session.highlightShow"), onClick: () => showTab("highlights") },
        });
      }
    }
    seen.ids = ids;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [highlights, highlightsQ.data, renderedSessionId, renderedLive]);

  // ---- the end of a session ----------------------------------------------------
  //
  // live → idle is an event the GM may not have caused (the idle watchdog, a
  // spending limit, a dead gateway), so it is announced once, with the reason
  // when there is one, and with the obvious next step: the recap.
  const wasActive = useRef<boolean | null>(null);
  useEffect(() => {
    const prev = wasActive.current;
    wasActive.current = sessionPending ? null : active;
    if (prev !== true || active || !session) return;
    const reason = failureText(session.endReason, t);
    // A false start with no lines has nothing to recap.
    const recapAction =
      session.status === "ended" && session.lineCount > 0
        ? { action: { label: t("session.generateRecap"), onClick: () => generateRecap(session.id) } }
        : {};
    if (session.status === "failed") {
      toast.error(reason ? t("session.endedFailedToast", { reason }) : t("session.connectionFailed"));
    } else if (reason) {
      toast.warning(t("session.endedAutoToast", { reason }), { duration: 15_000, ...recapAction });
    } else {
      toast(t("session.endedToast"), recapAction);
    }
    // The active edge is the trigger; the session row is read as of that edge.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [active, sessionPending]);

  // Start and Stop swap places when the session flips, and a button that goes
  // busy is disabled — either way a keyboard user's focus would be left on
  // <body>. Hand it to the button that took the old one's place.
  const primaryRef = useRef<HTMLButtonElement | null>(null);
  const flipped = useRef<boolean | null>(null);
  useLayoutEffect(() => {
    const prev = flipped.current;
    flipped.current = sessionPending ? null : active;
    if (prev == null || prev === active) return;
    const focused = document.activeElement;
    if (focused === null || focused === document.body) primaryRef.current?.focus();
  }, [active, sessionPending]);

  // The sticky bar's height feeds the voice rail's sticky offset (the bar wraps
  // on a narrow screen, so it is measured, not assumed).
  // The bar is held in state (a callback ref), not a ref object: the first
  // render can be the zero-campaign card, and the observer must attach when
  // the bar mounts later.
  const [bar, setBar] = useState<HTMLDivElement | null>(null);
  const [barHeight, setBarHeight] = useState<number | null>(null);
  useLayoutEffect(() => {
    if (!bar || typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(() => setBarHeight(Math.round(bar.getBoundingClientRect().height)));
    observer.observe(bar);
    return () => observer.disconnect();
  }, [bar]);

  // ---- in-flight player bind ---------------------------------------------------
  const [bindOpen, setBindOpen] = useState(false);
  const [bindPrefill, setBindPrefill] = useState<BindPrefill | null>(null);

  // What belongs to the running session goes with it when it ends, whoever
  // ended it (the idle watchdog, a spending limit): an open Stop confirm would
  // otherwise send a Stop to nothing, and the bind form would greet the next
  // evening already open, seeded with tonight's person.
  useEffect(() => {
    if (active) return;
    setConfirmingStop(false);
    setBindOpen(false);
    setBindPrefill(null);
  }, [active]);

  // "Back to live" unmounts itself; the transcript it returned to takes the
  // keyboard focus instead of <body>.
  const focusPaneOnReturn = useRef(false);
  useEffect(() => {
    if (!focusPaneOnReturn.current || viewingPast) return;
    focusPaneOnReturn.current = false;
    document.querySelector<HTMLElement>('[data-testid="transcript-pane"]')?.focus({ preventScroll: true });
  });

  // Zero campaigns (#267): GetActiveCampaign is CodeNotFound and a session can't
  // start, so the create-first-campaign card replaces the screen instead of
  // surfacing raw "no active campaign" errors. Creating swaps the live screen in.
  if (!campaignQ.data && campaignQ.isError && isNotFound(campaignQ.error)) {
    return (
      <Page className="gx-session-screen">
        <PageHeader title={t("session.title")} />
        <Card accent>
          <FirstCampaignCTA />
        </Card>
      </Page>
    );
  }

  const exportable = !renderedLive && hasLines;
  const renderedStartedMs = tsMs(renderedSession?.startedAt);
  const exportMarkdown = () =>
    transcriptMarkdown({
      campaignName,
      startedAt: renderedStartedMs != null ? new Date(renderedStartedMs) : null,
      lines: transcript.lines,
      t,
      lang,
    });

  const viewedStartedMs = tsMs(viewedSession?.startedAt);
  const paneLabel = renderedLive ? t("session.liveTranscript") : t("session.sessionTranscript");

  const recapButton = canRecap && renderedSession && !recap && (
    <Button
      variant="secondary"
      size="sm"
      iconStart={<ScrollText size={15} />}
      onClick={() => generateRecap(renderedSession.id)}
      disabled={recapPending}
      data-testid="recap-button"
    >
      {t("session.recap")}
    </Button>
  );

  return (
    <Page
      className="gx-session-screen"
      data-live={active || undefined}
      style={barHeight != null ? ({ "--gx-session-bar-h": `${barHeight}px` } as CSSProperties) : undefined}
    >
      <PageHeader
        eyebrow={campaignName}
        title={t("session.title")}
        actions={
          pastSessions.length > 0 && (
            <SessionHistory sessions={pastSessions} viewedSessionId={renderedSessionId} onPick={viewSession} />
          )
        }
      />

      <div className="gx-session__layout">
        {/* The sticky element is this wrapper, not the card: its page-coloured
            padding is what keeps a scrolling line from showing above the card
            or around its rounded corners. */}
        <div className="gx-session__top" ref={setBar}>
        <Card accent className="gx-session__control">
          <div className="gx-session__bar">
            <div className="gx-session__status">
              {sessionPending ? (
                <Skeleton lines={1} className="gx-session__status-loading" />
              ) : failed ? (
                <Badge variant="danger" dot>
                  {t("session.statusFailed")}
                </Badge>
              ) : active && connected ? (
                <Badge variant="live" dot pulse>
                  {t("session.statusLive")}
                </Badge>
              ) : active ? (
                <Badge variant="warning" dot pulse data-testid="connection-state">
                  {t("session.connecting")}
                </Badge>
              ) : (
                <Badge variant="neutral" dot>
                  {t("session.statusIdle")}
                </Badge>
              )}
              {/* The clock only means something while a session runs; idle, a
                  frozen 00:00:00 reads as a stuck timer. */}
              {active && <SessionClock startMs={startedMs} />}
              {active && estimatedSpendUsd > 0 && (
                <span className="gx-session__spend" data-testid="spend-inline">
                  {t("session.spendInline", { usd: formatUsd(estimatedSpendUsd, lang) })}
                </span>
              )}
            </div>

            <div className="gx-session__actions">
              {active ? (
                <Button
                  ref={primaryRef}
                  variant="danger"
                  iconStart={<Square size={15} />}
                  onClick={requestStop}
                  loading={stop.isPending}
                >
                  {stop.isPending ? t("session.stopping") : t("session.stopSession")}
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
                          loading={saveDefaultChannel.isPending}
                          data-testid="set-default-channel"
                        >
                          {t("session.setAsDefault")}
                        </Button>
                      )}
                    </div>
                  )}
                  <Button
                    ref={primaryRef}
                    variant="primary"
                    iconStart={<Play size={15} />}
                    onClick={() => start.mutate({ voiceChannelId: selectedChannelId })}
                    loading={start.isPending}
                    // Held while the session read or the channel list is still
                    // loading (a click then would send no channel), while a
                    // loaded list waits for a pick, and while Discord is not
                    // set up at all — the pre-flight row below says which, and
                    // where to fix it.
                    disabled={
                      sessionPending || channelsQ.isLoading || channelPickNeeded || discord.tone === "block"
                    }
                  >
                    {start.isPending ? t("session.starting") : t("session.startSession")}
                  </Button>
                </>
              )}
            </div>
          </div>

          {!active && !sessionPending && (
            <Preflight
              discord={discord}
              roster={roster}
              rosterState={rosterQ.isPending ? "pending" : rosterQ.isError && !rosterQ.data ? "error" : "ready"}
              tapeArmed={campaign?.tapeArmed}
            />
          )}
        </Card>
        </div>

        <VoicePanel
          active={active}
          mutedIds={data?.mutedAgentIds ?? []}
          speakingName={speakingAgentName(transcript.typing, renderedLive)}
        />

        <div className="gx-session__main">
          {failed && (
            <div data-testid="connection-failed">
              <Alert
                tone="danger"
                title={t("session.connectionFailed")}
                action={
                  reasonNeedsSetup(failureReason) && (
                    <Button variant="secondary" size="sm" onClick={() => nav.goScreen("configuration")}>
                      {t("session.preflightOpenSetup")}
                    </Button>
                  )
                }
              >
                {/* A known cause is a sentence with the next step; an unknown
                    one is the server's own words, verbatim (i18n rule 3). */}
                {failureReason ? failureText(failureReason, t) : t("session.connectionFailedNoReason")}
              </Alert>
            </div>
          )}

          {(spendCapState === "soft" || spendCapState === "hard") && (
            <div data-testid="spend-cap">
              <Alert tone="warning">
                {spendCapState === "hard" ? t("session.spendCapHard") : t("session.spendCapSoft")}{" "}
                {t("session.spendEstimate", { usd: formatUsd(estimatedSpendUsd, lang) })}
              </Alert>
            </div>
          )}

          {active && session && activeCampaignId && (
            <UnlinkedVoiceNotice
              sessionId={session.id}
              campaignId={activeCampaignId}
              connected={connected}
              onLink={(person) => {
                setBindPrefill(person);
                setBindOpen(true);
              }}
            />
          )}

          {/* In-flight bind affordance (#279): while a session is live the GM
              can link a player to a Character without leaving or restarting it. */}
          {active && (
            <SessionBindAffordance
              open={bindOpen}
              prefill={bindPrefill}
              onOpenChange={(open) => {
                setBindOpen(open);
                if (!open) setBindPrefill(null);
              }}
            />
          )}

          {!active && !viewingPast && session && session.status === "ended" && (
            <div className="gx-session__last">
              <div className="gx-session__last-text">
                <span>{lastSummary(session, t, lang)}</span>
                {session.endReason !== "" && (
                  <span className="gx-session__last-reason" role="status" data-testid="end-reason">
                    <TriangleAlert size={14} aria-hidden="true" />
                    {failureText(session.endReason, t)}
                  </span>
                )}
              </div>
              {recapButton}
            </div>
          )}

          {viewingPast && (
            <div data-testid="viewing-past">
              <Alert
                tone="info"
                role="status"
                action={
                  <>
                    {recapButton}
                    <Button
                      variant="secondary"
                      size="sm"
                      iconStart={active ? <Mic size={14} /> : <ArrowLeft size={14} />}
                      onClick={() => {
                        focusPaneOnReturn.current = true;
                        viewSession(null);
                      }}
                    >
                      {active ? t("session.backToLive") : t("session.backToLatest")}
                    </Button>
                  </>
                }
              >
                {viewedStartedMs != null
                  ? t("session.viewingPast", {
                      date: formatDay(new Date(viewedStartedMs), lang),
                      time: formatTime(new Date(viewedStartedMs), lang),
                    })
                  : t("session.viewingPastUnknown")}
              </Alert>
            </div>
          )}

          {renderedSessionId && (
            <RecapCard
              recap={recap}
              pending={recapPending}
              sessionStartedMs={renderedStartedMs}
              onRegenerate={() => generateRecap(renderedSessionId)}
            />
          )}

          <div className="gx-session__tabs">
            <SegmentedControl<Tab>
              label={t("session.tabsLabel")}
              value={tab}
              onChange={showTab}
              options={[
                { value: "transcript", label: t("session.tabTranscript") },
                {
                  value: "highlights",
                  label: t("session.tabHighlights"),
                  count: highlightsQ.data ? highlights.length : undefined,
                },
                { value: "board", label: t("session.tabBoard") },
              ]}
            />
          </div>

          {/* No accessible name on the section: the scrolling pane inside is the
              "Live transcript" region, and two nested regions of one name read twice. */}
          <section className="gx-session__panel" hidden={tab !== "transcript"}>
            <div className="gx-session__panel-head">
              <h2 className="gx-session__panel-title">{paneLabel}</h2>
              {exportable && (
                <div className="gx-session__export">
                  <Button
                    variant="ghost"
                    size="sm"
                    iconStart={<Copy size={14} />}
                    onClick={() =>
                      void copyText(exportMarkdown(), t("session.transcriptCopied"), t("session.copyFailed"))
                    }
                  >
                    {t("session.copy")}
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    iconStart={<Download size={14} />}
                    onClick={() =>
                      downloadBlob(
                        new Blob([exportMarkdown()], { type: "text/markdown;charset=utf-8" }),
                        transcriptFilename(
                          campaignName,
                          renderedStartedMs != null ? new Date(renderedStartedMs) : null,
                        ),
                      )
                    }
                    data-testid="transcript-download"
                  >
                    {t("session.downloadMarkdown")}
                  </Button>
                </div>
              )}
            </div>
            <TranscriptSearch sessions={pastSessions} currentSessionId={renderedSessionId} onOpen={openHit} />
            {snapshotFailed ? (
              <div data-testid="snapshot-error">
                <ErrorState
                  title={t("session.transcriptLoadFailedTitle")}
                  description={
                    viewingPast
                      ? t("session.transcriptLoadFailedPastInline")
                      : t("session.transcriptLoadFailedCurrent")
                  }
                  onRetry={transcript.retrySnapshot}
                />
              </div>
            ) : sessionPending || transcript.snapshotLoading ? (
              <Card className="gx-session__pane-loading">
                <Skeleton lines={5} />
              </Card>
            ) : (
              <TranscriptPane
                // One pane per session: a switch starts at that session's own
                // position (a live one at its tail and following, a past one at
                // its top), not at the scroll offset the previous one left.
                key={renderedSessionId ?? ""}
                lines={transcript.lines}
                typing={transcript.typing}
                live={renderedLive}
                visible={tab === "transcript"}
                roster={roster}
                highlightedLineId={highlightedLineId}
                label={paneLabel}
                empty={
                  <EmptyState
                    size="inline"
                    icon={<Mic size={16} />}
                    title={
                      renderedLive
                        ? t("session.listening")
                        : viewingPast || renderedSessionId
                          ? t("session.noTranscriptLines")
                          : t("session.startToCapture")
                    }
                  />
                }
              />
            )}
          </section>

          {visited.has("highlights") && (
            <section
              className="gx-session__panel gx-session__highlights"
              hidden={tab !== "highlights"}
              aria-label={t("session.highlightsTitle")}
            >
              <h2 className="gx-session__panel-title">{t("session.highlightsTitle")}</h2>
              {renderedSessionId ? (
                <HighlightsStrip
                  sessionId={renderedSessionId}
                  live={renderedLive}
                  sessionEndedMs={tsMs(renderedSession?.endedAt)}
                  tapeArmed={campaign?.tapeArmed}
                  onOpenSettings={() => nav.goCampaignView("settings")}
                  focusHighlightId={
                    focusHighlight && focusHighlight.sessionId === renderedSessionId
                      ? focusHighlight.highlightId
                      : null
                  }
                  onFocusHandled={() => setFocusHighlight(null)}
                  renderActions={(h) =>
                    h.status === "promoted" ? <ShareHighlightDialog highlight={h} sessionLive={active} /> : null
                  }
                />
              ) : (
                // No session has run yet, so there is nothing to list — but the
                // tab still says what would fill it.
                <EmptyState
                  size="inline"
                  title={
                    campaign?.tapeArmed === false
                      ? t("session.highlightsOffTitle")
                      : t("session.highlightsNoneTitle")
                  }
                  description={
                    campaign?.tapeArmed === false
                      ? t("session.highlightsOffDesc")
                      : t("session.highlightsNoSessionDesc")
                  }
                  action={
                    campaign?.tapeArmed === false && (
                      <Button variant="secondary" size="sm" onClick={() => nav.goCampaignView("settings")}>
                        {t("session.openCampaignSettings")}
                      </Button>
                    )
                  }
                />
              )}
            </section>
          )}

          {/* Session prep board (#543): the GM's own running order for tonight.
              It belongs HERE, not only in the wiki, because at the table the
              board IS the session — and it is read-only here for the same
              reason: a reference, not an editor to fall into mid-scene. */}
          {visited.has("board") && (
            <section className="gx-session__panel" hidden={tab !== "board"} aria-label={t("session.boardTitle")}>
              <h2 className="gx-session__panel-title">{t("session.boardTitle")}</h2>
              <SessionBoard campaignId={activeCampaignId ?? ""} />
            </section>
          )}
        </div>
      </div>

      <ConfirmDialog
        open={confirmingStop}
        onOpenChange={setConfirmingStop}
        title={t("session.stopConfirmTitle")}
        description={t("session.stopConfirmDesc", {
          duration: formatDuration(startedMs != null ? Date.now() - startedMs : 0, t),
        })}
        confirmLabel={t("session.stopSession")}
        cancelLabel={t("session.stopConfirmKeep")}
        onConfirm={() => {
          setConfirmingStop(false);
          stop.mutate({});
        }}
      />
    </Page>
  );
}
