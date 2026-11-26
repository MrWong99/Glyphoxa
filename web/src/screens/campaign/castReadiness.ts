import { useMemo } from "react";
import { useQuery } from "@connectrpc/connect-query";
import { timestampDate } from "@bufbuild/protobuf/wkt";

import { CampaignService } from "@gen/glyphoxa/management/v1/management_pb";
import type { Agent } from "@gen/glyphoxa/management/v1/management_pb";
import {
  prepSummary,
  rosterEntries,
  rosterPrep,
  type Readiness,
  type RosterEntry,
  type WorldKnowledge,
} from "./rosterPrep";

// useCastReadiness grades the roster for every surface that shows readiness:
// the roster list's markers, the agent editor's callout, the count on the
// Readiness segment and the Readiness view itself. One hook, so they all read
// the SAME two queries (one graph, one batch readiness: not a per-NPC storm)
// under the same cache keys, and can never disagree about who is ready.
//
// `worldKnown` says whether both reads have landed. Until then (and if they
// fail) the entries are graded on what the roster payload itself answers —
// personality and voice — so nothing claims "no wiki entry" before it can know.
// The Readiness view waits for both; the roster list does not have to.
//
// The same caution applies Agent by Agent. GetRosterReadiness answers with a
// row for EVERY Character NPC, linked or not, so an NPC without a row is one
// the read predates (it was created a moment ago and the re-read is still on
// its way). It is graded on personality and voice until its row arrives, and
// `complete` stays false, so nothing says "all ready" over an NPC nobody has
// looked at yet.
export function useCastReadiness(roster: Agent[]) {
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

  const worldKnown = graphQuery.data !== undefined && readyQuery.data !== undefined;
  const world = useMemo<WorldKnowledge>(
    () => ({ world: worldKnown ? (agentID: string) => readiness.has(agentID) : false }),
    [worldKnown, readiness],
  );

  const entries = useMemo(
    () => rosterEntries(roster, graphQuery.data?.nodes ?? [], readiness, world),
    [roster, graphQuery.data, readiness, world],
  );
  const byAgent = useMemo(() => {
    const m = new Map<string, RosterEntry>();
    for (const e of entries) m.set(e.agent.id, e);
    return m;
  }, [entries]);
  const groups = useMemo(
    () =>
      worldKnown
        ? rosterPrep(roster, graphQuery.data?.nodes ?? [], graphQuery.data?.edges ?? [], readiness, world)
        : [],
    [roster, graphQuery.data, readiness, world, worldKnown],
  );
  const summary = useMemo(() => prepSummary(entries), [entries]);
  const complete = worldKnown && entries.every((e) => e.exempt || readiness.has(e.agent.id));

  return { graphQuery, readyQuery, worldKnown, complete, entries, byAgent, groups, summary };
}
