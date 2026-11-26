import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@connectrpc/connect-query";
import { keepPreviousData } from "@tanstack/react-query";
import { timestampMs } from "@bufbuild/protobuf/wkt";
import { Search, X } from "lucide-react";

import { SessionService } from "@gen/glyphoxa/management/v1/management_pb";
import type { TranscriptLineMatch, VoiceSession } from "@gen/glyphoxa/management/v1/management_pb";
import { IconButton } from "@/components/ui/IconButton";
import { Input } from "@/components/ui/Input";
import { errorMessage } from "@/lib/connectError";
import { useI18n } from "@/i18n";
import { formatDay, formatTime, tsMs } from "./sessionFormat";
import { speakerName, tagLabel } from "./speakers";
import { lineText } from "./TranscriptPane";
import { formatClock } from "./useSessionEvents";

// matchClock renders a search hit's protobuf Timestamp as HH:MM:SS, matching the
// transcript line timestamps (reusing formatClock via the RFC3339 instant).
function matchClock(ts: TranscriptLineMatch["ts"] | undefined): string {
  const ms = ts ? Number(timestampMs(ts)) : null;
  return ms == null ? "" : formatClock(new Date(ms).toISOString());
}

type Group = { sessionId: string; when: Date | null; hits: TranscriptLineMatch[] };

// TranscriptSearch is the Session screen's transcript search box (#120, ADR-0011
// amendment). It debounces the raw box value into a SearchTranscriptLines query
// that runs ONLY while the trimmed query is non-empty — an empty box is the prompt
// state, no RPC (keepPreviousData holds the last matches steady across keystrokes).
// The server scopes the search to the GM's Active Campaign and shares the one
// storage search path with /glyphoxa search (AC4/AC5).
//
// The search covers EVERY session of the campaign, so hits are grouped under the
// session they were said in, headed by its date: "the dragon" said last Saturday
// and "the dragon" said tonight are different finds. Groups keep the server's
// rank order (a group sits where its best hit ranked). Clicking a hit asks the
// parent to open the line (onOpen) — it highlights the line when it is on screen,
// else views that session and jumps there once it loads (#270, AC4). Line ids
// restart per session, so onOpen always carries the hit's session id too.
//
// The box clears with its X or with Escape, which also closes the results.
export function TranscriptSearch({
  sessions,
  currentSessionId,
  onOpen,
}: {
  /** The campaign's sessions, to date a hit's group. */
  sessions: readonly VoiceSession[];
  /** The session on screen: its group is marked so the GM knows a click stays here. */
  currentSessionId: string | null;
  onOpen: (sessionId: string, lineId: string) => void;
}) {
  const { t, lang } = useI18n();
  const [search, setSearch] = useState("");
  const [debounced, setDebounced] = useState("");
  useEffect(() => {
    const timer = setTimeout(() => setDebounced(search), 200);
    return () => clearTimeout(timer);
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
  const hits = useMemo(() => searchQuery.data?.lines ?? [], [searchQuery.data]);

  const groups = useMemo<Group[]>(() => {
    const startedAt = new Map<string, number | null>();
    for (const s of sessions) startedAt.set(s.id, tsMs(s.startedAt));
    const bySession = new Map<string, Group>();
    for (const hit of hits) {
      let group = bySession.get(hit.sessionId);
      if (!group) {
        // A session the list does not carry (it is capped) is dated by its hit.
        const ms = startedAt.get(hit.sessionId) ?? (hit.ts ? Number(timestampMs(hit.ts)) : null);
        group = { sessionId: hit.sessionId, when: ms != null ? new Date(ms) : null, hits: [] };
        bySession.set(hit.sessionId, group);
      }
      group.hits.push(hit);
    }
    return [...bySession.values()];
  }, [hits, sessions]);

  const clear = () => {
    setSearch("");
    setDebounced("");
  };

  return (
    <div className="gx-tsearch">
      <div className="gx-tsearch__box">
        <Input
          type="search"
          aria-label={t("session.searchTranscript")}
          icon={<Search size={15} />}
          placeholder={t("session.searchPlaceholder")}
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          onKeyDown={(e) => {
            // Escape empties the box; with nothing typed it is left to whatever
            // is listening further up.
            if (e.key === "Escape" && search !== "") {
              e.preventDefault();
              e.stopPropagation();
              clear();
            }
          }}
          className="gx-tsearch__input"
        />
        {search !== "" && (
          <IconButton label={t("session.clearSearch")} size="sm" onClick={clear} className="gx-tsearch__clear">
            <X size={15} />
          </IconButton>
        )}
      </div>
      {searching &&
        (searchQuery.isError ? (
          <p className="gx-tsearch__empty" role="alert">
            {t("session.couldntSearch", { message: errorMessage(searchQuery.error) })}
          </p>
        ) : hits.length > 0 ? (
          <div className="gx-tsearch__results" data-testid="transcript-search-results">
            <p className="gx-tsearch__count" role="status">
              {hits.length === 1
                ? t("session.searchCountOne")
                : groups.length === 1
                  ? t("session.searchCount", { n: hits.length })
                  : t("session.searchCountSessions", { n: hits.length, sessions: groups.length })}
            </p>
            {groups.map((group) => (
              <section key={group.sessionId} className="gx-tsearch__group" data-testid="search-group">
                <h3 className="gx-tsearch__group-title">
                  {group.when
                    ? t("session.searchGroup", {
                        date: formatDay(group.when, lang),
                        time: formatTime(group.when, lang),
                      })
                    : t("session.searchGroupUndated")}
                  {group.sessionId === currentSessionId && (
                    <span className="gx-tsearch__here">{t("session.searchGroupHere")}</span>
                  )}
                  <span className="gx-tsearch__group-count">
                    <span aria-hidden="true">{group.hits.length}</span>
                    <span className="gx-visually-hidden">
                      {group.hits.length === 1
                        ? t("session.searchGroupCountOne")
                        : t("session.searchGroupCount", { n: group.hits.length })}
                    </span>
                  </span>
                </h3>
                <ul className="gx-tsearch__hits">
                  {group.hits.map((m) => {
                    const tag = tagLabel(m.tag, m.kind, t);
                    return (
                      <li key={m.lineId}>
                        <button
                          type="button"
                          className="gx-tsearch__result"
                          onClick={() => onOpen(m.sessionId, m.lineId)}
                        >
                          <span className="gx-line__who" data-kind={m.kind}>
                            {speakerName(m.who, m.kind, t)}
                          </span>
                          {tag && (
                            <span className="gx-line__tag" data-kind={m.kind}>
                              {tag}
                            </span>
                          )}
                          <time className="gx-line__ts">{matchClock(m.ts)}</time>
                          <span className="gx-tsearch__text">{lineText(m.kind, m.text)}</span>
                        </button>
                      </li>
                    );
                  })}
                </ul>
              </section>
            ))}
          </div>
        ) : (
          !searchQuery.isPending && (
            <p className="gx-tsearch__empty">{t("session.noLinesMatch", { query: trimmed })}</p>
          )
        ))}
    </div>
  );
}
