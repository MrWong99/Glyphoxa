import { useEffect, useMemo, useRef, useState } from "react";
import { useQuery } from "@connectrpc/connect-query";
import { ScrollText } from "lucide-react";

import { SessionService } from "@gen/glyphoxa/management/v1/management_pb";
import { useCan } from "@/app/capabilities";
import { useCurrentUser } from "@/app/currentUser";
import { useScreenNav, type ScreenSearch } from "@/app/screenNav";
import { Card } from "@/components/ui/Card";
import { EmptyState } from "@/components/ui/EmptyState";
import { ErrorState } from "@/components/ui/ErrorState";
import { Page, PageHeader } from "@/components/ui/Page";
import { QueryBoundary } from "@/components/ui/QueryBoundary";
import { SegmentedControl } from "@/components/ui/SegmentedControl";
import { Skeleton } from "@/components/ui/Skeleton";
import { useI18n } from "@/i18n";
import { HighlightsStrip } from "./HighlightsStrip";
import { SessionHistory } from "./SessionHistory";
import { formatDay, formatTime, tsMs } from "./sessionFormat";
import { TranscriptPane } from "./TranscriptPane";
import { TranscriptSearch } from "./TranscriptSearch";
import { useSessionEvents } from "./useSessionEvents";

import "./session.css";

// SessionArchive is the Session screen of a Campaign Guest (ADR-0065): the
// campaign's PAST sessions, read-only. It is a screen of its own rather than
// the GM's Session screen with its controls switched off, because almost
// nothing of that screen is a guest's: the live session and its controls, the
// voice channel, the cast and its mutes, preflight, spend, recaps and the prep
// board are all the GM's, and each is a call a guest would be refused. What a
// guest may have is two things, each behind its own Guest Right:
//
//   transcripts.read   a session's transcript, and search across all of them
//   highlights.read    a session's promoted Session Highlights (the server
//                      never sends a guest a candidate)
//
// It is built from the same parts the GM's screen uses (SessionHistory,
// TranscriptPane, TranscriptSearch, HighlightsStrip) and keeps the same URL
// vocabulary — ?session= is the session on screen, ?line= and ?highlight= are
// applied once and stripped — so a link into a session works for both.

type Tab = "transcript" | "highlights";

// The transcript pane names speakers from the cast; a guest has no cast to
// read, and lines carry their own speaker names.
const NO_ROSTER = [] as const;

export function SessionArchive() {
  const { t, lang } = useI18n();
  const nav = useScreenNav();
  const can = useCan();
  const guest = useCurrentUser()?.guest;

  const sessionsQ = useQuery(SessionService.method.listSessions, {}, { retry: false });
  // Past sessions only: one that is still running is the GM's live desk.
  const sessions = useMemo(
    () => (sessionsQ.data?.sessions ?? []).filter((s) => s.status !== "running"),
    [sessionsQ.data],
  );

  // The session on screen is the URL's, else the most recent one.
  const viewedId = nav.search.session ?? sessions[0]?.id ?? null;
  const viewed = sessions.find((s) => s.id === viewedId);
  const startedMs = tsMs(viewed?.startedAt);

  const tabs = useMemo<Tab[]>(
    () => [
      ...(can.transcriptsRead ? (["transcript"] as const) : []),
      ...(can.highlightsRead ? (["highlights"] as const) : []),
    ],
    [can.transcriptsRead, can.highlightsRead],
  );
  const [picked, setPicked] = useState<Tab>("transcript");
  const tab = tabs.includes(picked) ? picked : tabs[0];

  const transcript = useSessionEvents(can.transcriptsRead ? (viewedId ?? undefined) : undefined, false, true);

  // ── deep links: a line, a highlight (the GM screen's rules, minus "live") ──
  const [highlightedLineId, setHighlightedLineId] = useState<string | null>(null);
  useEffect(() => setHighlightedLineId(null), [viewedId]);
  const lineIds = useMemo(() => new Set(transcript.lines.map((l) => l.id)), [transcript.lines]);
  // Line ids restart per session, so a jump is always a (session, line) pair.
  const [pendingJump, setPendingJump] = useState<{ sessionId: string; lineId: string } | null>(null);
  useEffect(() => {
    if (!pendingJump || viewedId !== pendingJump.sessionId || !lineIds.has(pendingJump.lineId)) return;
    setHighlightedLineId(pendingJump.lineId);
    try {
      document
        .querySelector<HTMLElement>(`[data-line-id="${pendingJump.lineId}"]`)
        ?.scrollIntoView?.({ block: "center", behavior: "smooth" });
    } catch {
      // jsdom / older browsers: the highlight still applies.
    }
    setPendingJump(null);
  }, [pendingJump, viewedId, lineIds]);
  const [focusHighlight, setFocusHighlight] = useState<{ sessionId: string; highlightId: string } | null>(null);

  const { setSearch } = nav;
  const viewSession = (id: string) => {
    setPendingJump(null);
    setFocusHighlight(null);
    setSearch({ session: id, line: undefined, highlight: undefined });
  };
  const openHit = (sessionId: string, lineId: string) => {
    setPicked("transcript");
    setPendingJump({ sessionId, lineId });
    if (sessionId !== viewedId) setSearch({ session: sessionId, line: undefined, highlight: undefined });
  };

  const dlSession = nav.search.session;
  const dlLine = nav.search.line;
  const dlHighlight = nav.search.highlight;
  const applied = useRef("");
  useEffect(() => {
    if (!dlLine && !dlHighlight) {
      applied.current = "";
      return;
    }
    const signature = `${dlSession ?? ""}|${dlLine ?? ""}|${dlHighlight ?? ""}`;
    if (dlSession && applied.current !== signature) {
      applied.current = signature;
      // A link to something the guest's rights do not cover is just dropped.
      if (dlLine && can.transcriptsRead) {
        setPendingJump({ sessionId: dlSession, lineId: dlLine });
        setPicked("transcript");
      }
      if (dlHighlight && can.highlightsRead) {
        setFocusHighlight({ sessionId: dlSession, highlightId: dlHighlight });
        if (!dlLine || !can.transcriptsRead) setPicked("highlights");
      }
    }
    const patch: ScreenSearch = { line: undefined, highlight: undefined };
    setSearch(patch, { replace: true });
    // The params are the trigger; setSearch and the rights are read as of the run.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [dlSession, dlLine, dlHighlight]);

  const paneLabel = t("session.sessionTranscript");

  return (
    <Page className="gx-session-screen gx-session-archive">
      <PageHeader
        eyebrow={guest?.campaignName}
        title={t("access.archiveTitle")}
        description={t("access.archiveLede")}
        actions={
          sessions.length > 1 && (
            <SessionHistory sessions={sessions} viewedSessionId={viewedId} onPick={viewSession} />
          )
        }
      />

      <QueryBoundary
        query={sessionsQ}
        skeleton={<Skeleton.Row count={4} avatar={false} />}
        errorTitle={t("access.archiveLoadError")}
      >
        {sessions.length === 0 || !viewedId ? (
          <EmptyState
            icon={<ScrollText size={22} />}
            title={t("access.archiveEmptyTitle")}
            description={t("access.archiveEmptyDesc")}
          />
        ) : (
          <div className="gx-session__main">
            <p className="gx-session__archive-when" role="status">
              {startedMs != null
                ? t("session.viewingPast", {
                    date: formatDay(new Date(startedMs), lang),
                    time: formatTime(new Date(startedMs), lang),
                  })
                : t("session.viewingPastUnknown")}
            </p>

            {tabs.length > 1 && (
              <div className="gx-session__tabs">
                <SegmentedControl<Tab>
                  label={t("session.tabsLabel")}
                  value={tab}
                  onChange={setPicked}
                  options={[
                    { value: "transcript", label: t("session.tabTranscript") },
                    { value: "highlights", label: t("session.tabHighlights") },
                  ]}
                />
              </div>
            )}

            {can.transcriptsRead && (
              <section className="gx-session__panel" hidden={tab !== "transcript"}>
                <div className="gx-session__panel-head">
                  <h2 className="gx-session__panel-title">{paneLabel}</h2>
                </div>
                <TranscriptSearch sessions={sessions} currentSessionId={viewedId} onOpen={openHit} />
                {transcript.snapshotFailed ? (
                  <ErrorState
                    title={t("session.transcriptLoadFailedTitle")}
                    description={t("session.transcriptLoadFailedPastInline")}
                    onRetry={transcript.retrySnapshot}
                  />
                ) : transcript.snapshotLoading ? (
                  <Card className="gx-session__pane-loading">
                    <Skeleton lines={5} />
                  </Card>
                ) : (
                  <TranscriptPane
                    key={viewedId}
                    lines={transcript.lines}
                    typing={transcript.typing}
                    live={false}
                    visible={tab === "transcript"}
                    roster={NO_ROSTER}
                    highlightedLineId={highlightedLineId}
                    label={paneLabel}
                    empty={<EmptyState size="inline" title={t("session.noTranscriptLines")} />}
                  />
                )}
              </section>
            )}

            {can.highlightsRead && (
              <section
                className="gx-session__panel gx-session__highlights"
                hidden={tab !== "highlights"}
                aria-label={t("session.highlightsTitle")}
              >
                <h2 className="gx-session__panel-title">{t("session.highlightsTitle")}</h2>
                <HighlightsStrip
                  sessionId={viewedId}
                  live={false}
                  sessionEndedMs={tsMs(viewed?.endedAt)}
                  focusHighlightId={
                    focusHighlight && focusHighlight.sessionId === viewedId ? focusHighlight.highlightId : null
                  }
                  onFocusHandled={() => setFocusHighlight(null)}
                />
              </section>
            )}
          </div>
        )}
      </QueryBoundary>
    </Page>
  );
}
