import { useQuery } from "@connectrpc/connect-query";
import { timestampDate } from "@bufbuild/protobuf/wkt";
import { MessageSquare } from "lucide-react";

import { CampaignService } from "@gen/glyphoxa/management/v1/management_pb";
import { useI18n, type Lang } from "@/i18n";

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

export function NodeAppearances({
  nodeID,
  onOpenLine,
}: {
  nodeID: string;
  /** Deep-link to the exact Transcript Line on the Session screen. */
  onOpenLine?: (sessionID: string, lineID: string) => void;
}) {
  const { t, lang } = useI18n();
  const { data, status } = useQuery(CampaignService.method.listNodeAppearances, { nodeId: nodeID });

  if (status === "pending") {
    return <div className="gx-skeleton gx-kg-appearances__skel" data-testid="appearances-loading" />;
  }
  if (status === "error") {
    // Best-effort: a failed retrieval list is not worth a scary error on an entry
    // the GM is editing. The entry itself is unaffected.
    return null;
  }

  const rows = data.appearances;
  return (
    <div className="gx-field gx-kg-appearances">
      <span className="gx-field__label">
        <MessageSquare size={12} /> {t("knowledge.mentionedLabel")}
      </span>
      {rows.length === 0 ? (
        <span className="gx-field__hint">{t("knowledge.mentionedEmpty")}</span>
      ) : (
        <ul className="gx-kg-appearances__list">
          {rows.map((a) => {
            const at = a.at ? timestampDate(a.at) : null;
            return (
              <li key={`${a.voiceSessionId}:${a.lineId}`}>
                <button
                  type="button"
                  className="gx-kg-appearances__row"
                  onClick={() => onOpenLine?.(a.voiceSessionId, a.lineId)}
                  aria-label={t("knowledge.mentionAria", {
                    who: a.who,
                    when: at ? fmtWhen(at, lang) : t("knowledge.unknownTime"),
                    text: a.text,
                  })}
                >
                  <span className="gx-kg-appearances__who" data-kind={a.kind}>
                    {a.who}
                  </span>
                  <span className="gx-kg-appearances__text">{a.text}</span>
                  {at && <span className="gx-kg-appearances__when">{fmtWhen(at, lang)}</span>}
                </button>
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}
