import { useId } from "react";
import type { ReactNode } from "react";
import { useQuery } from "@connectrpc/connect-query";
import { timestampDate } from "@bufbuild/protobuf/wkt";
import { MessageSquare } from "lucide-react";

import { CampaignService } from "@gen/glyphoxa/management/v1/management_pb";
import { useI18n, type Lang } from "@/i18n";
import { Button } from "@/components/ui/Button";
import { stripMarkdown } from "@/components/ui/Markdown";

// Where an entry was mentioned in play (#545, ADR-0008 amendment).
//
// "When did we last see that ogre" is the question the wiki could not answer: an
// entry records what a thing IS, never where it came up. This is the answer, and
// it is deliberately a RETRIEVAL list rather than anything in the graph — an Edge
// would be world truth an NPC's Hot Context walks, so mentions would silently
// become facts the NPC recites.
//
// Each row carries the line's own text, so the GM reads WHAT was said without
// following anything, and follows the link only when they want the scene around
// it.

// The stamp follows the DISPLAY language, not the browser's: a German operator on
// an English-locale machine reads "1. Aug. 2026, 20:15", not "Aug 1, 2026, 8:15 PM".
// lang is threaded from the component — this helper holds no translation.
function fmtWhen(at: Date, lang: Lang): string {
  return at.toLocaleString(lang, { dateStyle: "medium", timeStyle: "short" });
}

// Frame is the block's heading and wrapper. In the editor it is one more
// labelled field among the Linked ones; on the entry page it is a section with
// the same overline heading as Facts, Tags and Connections beside it.
function Frame({ as, children }: { as: "field" | "section"; children: ReactNode }) {
  const { t } = useI18n();
  const headingId = useId();
  if (as === "section") {
    return (
      <section className="gx-wiki-section gx-kg-appearances" aria-labelledby={headingId}>
        <h3 className="gx-wiki-section__title" id={headingId}>
          {t("knowledge.mentionedLabel")}
        </h3>
        {children}
      </section>
    );
  }
  return (
    <div className="gx-field gx-kg-appearances">
      <span className="gx-field__label">
        <MessageSquare size={12} /> {t("knowledge.mentionedLabel")}
      </span>
      {children}
    </div>
  );
}

export function NodeAppearances({
  nodeID,
  onOpenLine,
  as = "field",
}: {
  nodeID: string;
  /** Deep-link to the exact Transcript Line on the Session screen. */
  onOpenLine?: (sessionID: string, lineID: string) => void;
  /** How it is headed: a form field (the editor) or a page section (the entry page). */
  as?: "field" | "section";
}) {
  const { t, lang } = useI18n();
  const { data, status, refetch, isFetching } = useQuery(
    CampaignService.method.listNodeAppearances,
    { nodeId: nodeID },
  );

  if (status === "pending") {
    return <div className="gx-skeleton gx-kg-appearances__skel" data-testid="appearances-loading" />;
  }
  if (status === "error") {
    // A quiet line, not an error panel: a failed retrieval list is not worth a
    // scary error on an entry the GM is reading, and the entry itself is
    // unaffected. But it is a line and not nothing. Rendering nothing told the
    // GM "never mentioned" about an entry that may have come up every session.
    return (
      <Frame as={as}>
        <span className="gx-kg-appearances__retry">
          <span className="gx-field__hint">{t("knowledge.mentionedLoadError")}</span>
          <Button variant="ghost" size="sm" disabled={isFetching} onClick={() => void refetch()}>
            {t("common.retry")}
          </Button>
        </span>
      </Frame>
    );
  }

  const rows = data.appearances;
  return (
    <Frame as={as}>
      {rows.length === 0 ? (
        <span className="gx-field__hint">{t("knowledge.mentionedEmpty")}</span>
      ) : (
        <ul className="gx-kg-appearances__list">
          {rows.map((a) => {
            const at = a.at ? timestampDate(a.at) : null;
            // An Agent line's text can carry leaked markdown; this one-line
            // quote row can't render it, so flatten (also for the aria label —
            // a screen reader should not hear "star star").
            const text = stripMarkdown(a.text);
            return (
              <li key={`${a.voiceSessionId}:${a.lineId}`}>
                <button
                  type="button"
                  className="gx-kg-appearances__row"
                  onClick={() => onOpenLine?.(a.voiceSessionId, a.lineId)}
                  aria-label={t("knowledge.mentionAria", {
                    who: a.who,
                    when: at ? fmtWhen(at, lang) : t("knowledge.unknownTime"),
                    text,
                  })}
                >
                  <span className="gx-kg-appearances__who" data-kind={a.kind}>
                    {a.who}
                  </span>
                  <span className="gx-kg-appearances__text">{text}</span>
                  {at && <span className="gx-kg-appearances__when">{fmtWhen(at, lang)}</span>}
                </button>
              </li>
            );
          })}
        </ul>
      )}
    </Frame>
  );
}
