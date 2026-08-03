import { useMemo } from "react";
import { useQuery } from "@connectrpc/connect-query";
import { Check, X } from "lucide-react";

import { CampaignService } from "@gen/glyphoxa/management/v1/management_pb";
import type { Agent } from "@gen/glyphoxa/management/v1/management_pb";
import { rosterPrep, type Check as ReadinessCheck } from "./rosterPrep";

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
}: {
  roster: Agent[];
  onSelectAgent: (id: string) => void;
  onOpenNode: (id: string) => void;
}) {
  // One graph read on top of the roster read the screen already does — not a
  // per-NPC RPC storm.
  const graphQuery = useQuery(CampaignService.method.getKnowledgeGraph, {});
  const groups = useMemo(
    () => rosterPrep(roster, graphQuery.data?.nodes ?? [], graphQuery.data?.edges ?? []),
    [roster, graphQuery.data],
  );

  if (graphQuery.isPending) return <div className="gx-skeleton" data-testid="prep-loading" />;
  if (graphQuery.isError) {
    return (
      <p className="gx-campaign__error" role="alert">
        Could not load the world: {graphQuery.error.message}
      </p>
    );
  }
  if (groups.length === 0) {
    return <p className="gx-roster__empty">No NPCs yet — the readiness view fills in as you add them.</p>;
  }

  return (
    <div className="gx-prep" aria-label="NPC readiness">
      {groups.map((g) => (
        <section key={g.key} className="gx-prep__group" aria-label={g.title}>
          <h4 className="gx-prep__title">{g.title}</h4>
          {g.entries.map((e) => (
            <div key={`${g.key}-${e.agent.id}`} className="gx-prep__row" data-ready={e.ready || undefined}>
              <button
                type="button"
                className="gx-prep__name"
                onClick={() => onSelectAgent(e.agent.id)}
              >
                {e.agent.name}
              </button>
              <div className="gx-prep__checks">
                {e.checks.map((c) => (
                  <ReadinessMark
                    key={c.key}
                    check={c}
                    agentName={e.agent.name}
                    onFix={() => (c.fix === "content" && e.node ? onOpenNode(e.node.id) : onSelectAgent(e.agent.id))}
                  />
                ))}
              </div>
            </div>
          ))}
        </section>
      ))}
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
  if (check.ok) {
    return (
      <span className="gx-prep__mark" data-ok aria-label={`${agentName}: ${check.label} — done`}>
        <Check size={12} aria-hidden /> {check.label}
      </span>
    );
  }
  return (
    <button
      type="button"
      className="gx-prep__mark"
      aria-label={`${agentName}: ${check.label} — missing, fix it`}
      onClick={onFix}
    >
      <X size={12} aria-hidden /> {check.label}
    </button>
  );
}
