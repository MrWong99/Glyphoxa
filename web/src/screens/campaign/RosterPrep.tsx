import { useMemo } from "react";
import { useQuery } from "@connectrpc/connect-query";
import { Check, X } from "lucide-react";

import { timestampDate } from "@bufbuild/protobuf/wkt";

import { CampaignService } from "@gen/glyphoxa/management/v1/management_pb";
import type { Agent } from "@gen/glyphoxa/management/v1/management_pb";
import { useI18n } from "@/i18n";
import {
  UNPLACED_TITLE,
  rosterPrep,
  type Check as ReadinessCheck,
  type Readiness,
} from "./rosterPrep";

// The roster prep dashboard (#544). The roster was a flat list of Agents; this
// shows what a GM needs BEFORE a session: where each NPC sits in the world, and
// whether it can actually say anything.
//
// The failure it exists for: an NPC created through the normal flow has a voice, a
// persona, and an entry that is empty (ADR-0008 second amendment auto-creates the
// Node without copying the Persona). Every signal below was already queryable and
// none of it was shown, so that state was found live, at the table.

export function RosterPrep({
  roster,
  onSelectAgent,
  onOpenNode,
  onOpenKnowledge,
}: {
  roster: Agent[];
  onSelectAgent: (id: string) => void;
  /** Opens ONE entry in the Knowledge editor. */
  onOpenNode: (id: string) => void;
  /** Opens the Knowledge tab, where the "Voiced by" link control lives. */
  onOpenKnowledge: () => void;
}) {
  const { t, lang } = useI18n();
  // Two reads on top of the roster the screen already has — one graph, one batch
  // readiness. Not a per-NPC RPC storm on a screen opened before every session.
  const graphQuery = useQuery(CampaignService.method.getKnowledgeGraph, {});
  const readyQuery = useQuery(CampaignService.method.getRosterReadiness, {});

  const readiness = useMemo(() => {
    const m = new Map<string, Readiness>();
    for (const r of readyQuery.data?.agents ?? []) {
      m.set(r.agentId, {
        linked: r.linked,
        factCount: r.factCount,
        chars: r.chars,
        maxChars: r.maxChars,
        truncated: r.truncated,
        lastSpokeAt: r.lastSpokeAt ? timestampDate(r.lastSpokeAt) : undefined,
      });
    }
    return m;
  }, [readyQuery.data]);

  const groups = useMemo(
    () => rosterPrep(roster, graphQuery.data?.nodes ?? [], graphQuery.data?.edges ?? [], readiness),
    [roster, graphQuery.data, readiness],
  );

  // Both reads must land before grading: showing "0 facts in reach" for every NPC
  // while the readiness read is still in flight would be a screen full of false
  // alarms on the exact screen the GM consults to trust the roster.
  if (graphQuery.isPending || readyQuery.isPending) {
    return <div className="gx-skeleton" data-testid="prep-loading" />;
  }
  if (graphQuery.isError || readyQuery.isError) {
    return (
      <p className="gx-campaign__error" role="alert">
        {t("campaign.prepLoadError", { message: (graphQuery.error ?? readyQuery.error)?.message ?? "" })}
      </p>
    );
  }
  if (groups.length === 0) {
    return <p className="gx-roster__empty">{t("campaign.prepEmpty")}</p>;
  }

  // The "last spoke" prep signal is context, not a check. Rendered here (not in
  // the pure helper) because the date format and copy follow the display
  // language, which only the React layer knows.
  const spokeLabel = (r?: Readiness) =>
    r?.lastSpokeAt
      ? t("campaign.prepLastSpoke", { date: r.lastSpokeAt.toLocaleDateString(lang) })
      : t("campaign.prepNeverSpoke");

  return (
    <div className="gx-prep" aria-label={t("campaign.prepAria")}>
      {groups.map((g) => {
        // Named groups carry a Node name (data); only the unplaced catch-all is
        // ours to translate — the helper hands back its sentinel key.
        const title = g.title === UNPLACED_TITLE ? t("campaign.prepUnplacedTitle") : g.title;
        return (
        <section key={g.key} className="gx-prep__group" aria-label={title}>
          <h4 className="gx-prep__title">{title}</h4>
          {g.entries.map((e) => (
            <div key={`${g.key}-${e.agent.id}`} className="gx-prep__row" data-ready={e.ready || undefined}>
              <button
                type="button"
                className="gx-prep__name"
                onClick={() => onSelectAgent(e.agent.id)}
              >
                {e.agent.name}
              </button>
              {e.exempt ? (
                // Listed, not graded — see rosterPrep's Butler note.
                <span className="gx-prep__exempt">{t("campaign.prepButlerExempt")}</span>
              ) : (
                <>
                  <div className="gx-prep__checks">
                    {e.checks.map((c) => (
                      <ReadinessMark
                        key={c.key}
                        check={c}
                        agentName={e.agent.name}
                        onFix={() => {
                          // Each fix goes where it can ACTUALLY be made: the link
                          // control lives in the Knowledge tab's relations card, not
                          // in the Agent editor.
                          if (c.fix === "entry" && e.node) onOpenNode(e.node.id);
                          else if (c.fix === "link" || c.fix === "entry") onOpenKnowledge();
                          else onSelectAgent(e.agent.id);
                        }}
                      />
                    ))}
                  </div>
                  <span className="gx-prep__spoke">{spokeLabel(e.readiness)}</span>
                </>
              )}
            </div>
          ))}
        </section>
        );
      })}
    </div>
  );
}

// ReadinessMark is one satisfied/unsatisfied mark. An UNSATISFIED one is a button
// that goes to the thing that fixes it — a checklist you cannot act on from is
// just a nag.
function ReadinessMark({
  check,
  agentName,
  onFix,
}: {
  check: ReadinessCheck;
  agentName: string;
  onFix: () => void;
}) {
  const { t } = useI18n();
  // The helper hands back a message key + params; the string exists only here.
  const label = t(check.labelKey, check.labelParams);
  if (check.ok) {
    return (
      <span
        className="gx-prep__mark"
        data-ok
        aria-label={t("campaign.checkDoneAria", { agent: agentName, check: label })}
      >
        <Check size={12} aria-hidden /> {label}
      </span>
    );
  }
  return (
    <button
      type="button"
      className="gx-prep__mark"
      aria-label={t("campaign.checkMissingAria", { agent: agentName, check: label })}
      onClick={onFix}
    >
      <X size={12} aria-hidden /> {label}
    </button>
  );
}
