import { useMemo, useState } from "react";
import { ChevronDown, ChevronRight, History } from "lucide-react";

import type { VoiceSession } from "@gen/glyphoxa/management/v1/management_pb";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { Popover } from "@/components/ui/Popover";
import { useI18n } from "@/i18n";
import { failureText } from "./failureText";
import {
  formatDay,
  formatDuration,
  formatTime,
  isMinorSession,
  linesLabel,
  sessionDurationMs,
  tsMs,
} from "./sessionFormat";

// SessionHistory is the campaign's past Voice Sessions (#270) behind one
// button: a list, newest first (ListSessions, server-scoped), each row saying
// WHEN in the terms a GM remembers a session by — the weekday and date — then
// how long it ran and how much was said. Picking a row views that session's
// persisted transcript.
//
// A row is marked when the session is the one running, when it failed, and
// when it ended by itself rather than by Stop (the reason is spelled out under
// it). Sessions that never amounted to anything — nothing transcribed, or
// under a minute — are folded into one "N short or empty sessions" line at the
// end, so three false starts before the real evening do not bury it.
export function SessionHistory({
  sessions,
  viewedSessionId,
  onPick,
}: {
  sessions: readonly VoiceSession[];
  /** The session whose transcript is on screen. */
  viewedSessionId: string | null;
  onPick: (id: string) => void;
}) {
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  const [showMinor, setShowMinor] = useState(false);

  const { main, minor } = useMemo(() => {
    const main: VoiceSession[] = [];
    const minor: VoiceSession[] = [];
    for (const s of sessions) {
      // The session on screen is never folded away: the list must show where
      // the GM is.
      if (isMinorSession(s) && s.id !== viewedSessionId) minor.push(s);
      else main.push(s);
    }
    return { main, minor };
  }, [sessions, viewedSessionId]);

  const pick = (id: string) => {
    setOpen(false);
    onPick(id);
  };

  return (
    <Popover
      label={t("session.historyLabel")}
      open={open}
      onOpenChange={setOpen}
      align="end"
      className="gx-session-history"
      trigger={
        <Button variant="secondary" size="sm" iconStart={<History size={15} />} data-testid="session-history-open">
          {t("session.history")}
        </Button>
      }
    >
      <div data-testid="session-picker">
        <h3 className="gx-session-history__title">{t("session.historyLabel")}</h3>
        <ul className="gx-session-history__list">
          {main.map((s) => (
            <HistoryRow key={s.id} session={s} viewed={s.id === viewedSessionId} onPick={pick} />
          ))}
        </ul>
        {minor.length > 0 && (
          <>
            <button
              type="button"
              className="gx-session-history__more"
              aria-expanded={showMinor}
              onClick={() => setShowMinor((v) => !v)}
            >
              {showMinor ? <ChevronDown size={14} aria-hidden="true" /> : <ChevronRight size={14} aria-hidden="true" />}
              {minor.length === 1
                ? t("session.historyMinorOne")
                : t("session.historyMinor", { n: minor.length })}
            </button>
            {showMinor && (
              <ul className="gx-session-history__list">
                {minor.map((s) => (
                  <HistoryRow key={s.id} session={s} viewed={false} onPick={pick} />
                ))}
              </ul>
            )}
          </>
        )}
      </div>
    </Popover>
  );
}

function HistoryRow({
  session,
  viewed,
  onPick,
}: {
  session: VoiceSession;
  viewed: boolean;
  onPick: (id: string) => void;
}) {
  const { t, lang } = useI18n();
  const startedMs = tsMs(session.startedAt);
  const started = startedMs != null ? new Date(startedMs) : null;
  const durationMs = sessionDurationMs(session);
  const running = session.status === "running";
  const failed = session.status === "failed";
  // An "ended" row with a reason stopped by itself (a spending limit, the idle
  // watchdog) rather than by the GM.
  const autoEnded = session.status === "ended" && session.endReason !== "";

  const facts: string[] = [];
  if (durationMs != null) facts.push(formatDuration(durationMs, t));
  // A running session's line_count is 0 until it closes (#270) — say nothing
  // rather than "0 lines" beside a live transcript.
  if (!running) facts.push(linesLabel(session.lineCount, t));

  return (
    <li>
      <button
        type="button"
        className="gx-session-history__row"
        aria-current={viewed ? "true" : undefined}
        onClick={() => onPick(session.id)}
      >
        <span className="gx-session-history__when">
          {started ? (
            <>
              <span className="gx-session-history__day">{formatDay(started, lang)}</span>
              <span className="gx-session-history__time">{formatTime(started, lang)}</span>
            </>
          ) : (
            "—"
          )}
          {running && (
            <Badge variant="live" size="sm" dot pulse>
              {t("session.statusLive")}
            </Badge>
          )}
          {failed && (
            <Badge variant="danger" size="sm">
              {t("session.statusFailed")}
            </Badge>
          )}
          {autoEnded && (
            <Badge variant="warning" size="sm">
              {t("session.historyAutoEnded")}
            </Badge>
          )}
        </span>
        {facts.length > 0 && <span className="gx-session-history__facts">{facts.join(" · ")}</span>}
        {(failed || autoEnded) && session.endReason !== "" && (
          <span className="gx-session-history__reason">{failureText(session.endReason, t)}</span>
        )}
      </button>
    </li>
  );
}
