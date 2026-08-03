import { EdgeType, NodeType } from "@gen/glyphoxa/management/v1/management_pb";
import type { Agent, GraphEdge, GraphNode } from "@gen/glyphoxa/management/v1/management_pb";

// Roster prep derivations (#544): where each cast NPC sits in the world, and
// whether it is actually ready to speak.
//
// Every signal here was already queryable and none of it was displayed. The
// concrete failure it addresses: ADR-0008's second amendment auto-creates an NPC
// Node on Character-NPC create with an EMPTY body and never copies the Persona, so
// "added an NPC, it has a voice and a persona but nothing to say about the world"
// is the DEFAULT state after creation — discovered live, at the table.

/** One readiness check, with the thing that fixes it. */
export type Check = {
  key: string;
  label: string;
  ok: boolean;
  /**
   * Where the GM goes to satisfy it — `agent` opens the Agent editor, `entry`
   * opens the linked wiki entry, and `link` opens the Knowledge tab where the
   * "Voiced by" control actually lives. The Agent editor has NO link control, so
   * routing `link` there would land the GM somewhere the fix cannot be made.
   */
  fix: "agent" | "entry" | "link";
};

/** One cast Agent's prompt-side readiness, from the batch preview read (#544). */
export type Readiness = {
  linked: boolean;
  factCount: number;
  chars: number;
  maxChars: number;
  truncated: boolean;
  lastSpokeAt?: Date;
};

export type RosterEntry = {
  agent: Agent;
  /** The Agent's linked Node, when it has one. */
  node: GraphNode | null;
  checks: Check[];
  /** True once every check passes. Always true for the Butler, which is ungraded. */
  ready: boolean;
  /**
   * The Butler is LISTED but not graded: Address-Only, auto-created, undeletable
   * and with no linked Node by design (ADR-0009), so character-oriented checks
   * would be a permanent false alarm rather than information.
   */
  exempt: boolean;
  readiness?: Readiness;
};

export type RosterGroup = {
  /** The faction or location name, or "" for the explicit unplaced group. */
  key: string;
  title: string;
  entries: RosterEntry[];
};

/** UNPLACED_TITLE names the catch-all group. It is explicit rather than hidden. */
export const UNPLACED_TITLE = "Not placed in the world";

/**
 * rosterPrep groups roster members by their linked Node's faction/location
 * neighbours and computes each cast NPC's readiness.
 *
 * The Butler is PRESENT and UNGRADED (`exempt`). It is Address-Only,
 * auto-created and undeletable (ADR-0009) with no linked Node by design, so
 * grading it against character-oriented checks would be a permanent false alarm —
 * but dropping it from the view would hide a roster member the GM looks for.
 */
export function rosterPrep(
  roster: Agent[],
  nodes: GraphNode[],
  edges: GraphEdge[],
  readiness: Map<string, Readiness> = new Map(),
): RosterGroup[] {
  const byID = new Map(nodes.map((n) => [n.id, n]));
  const nodeByAgent = new Map<string, GraphNode>();
  for (const n of nodes) {
    if (n.agentId !== "") nodeByAgent.set(n.agentId, n);
  }

  // Outgoing member_of / resides_in edges are what place an NPC in the world.
  const placement = new Map<string, string[]>();
  for (const e of edges) {
    if (e.edgeType !== EdgeType.MEMBER_OF && e.edgeType !== EdgeType.RESIDES_IN) continue;
    const target = byID.get(e.toNodeId);
    if (!target) continue;
    if (target.nodeType !== NodeType.FACTION && target.nodeType !== NodeType.LOCATION) continue;
    const list = placement.get(e.fromNodeId);
    if (list) list.push(target.name);
    else placement.set(e.fromNodeId, [target.name]);
  }

  const groups = new Map<string, RosterGroup>();
  const put = (title: string, entry: RosterEntry) => {
    const g = groups.get(title);
    if (g) g.entries.push(entry);
    else groups.set(title, { key: title, title, entries: [entry] });
  };

  for (const agent of roster) {
    const node = nodeByAgent.get(agent.id) ?? null;
    // The Butler is present, ungraded. Excluding it entirely would hide a roster
    // member the GM is looking for; grading it would flag it forever.
    const exempt = agent.role !== "character";
    const r = readiness.get(agent.id);
    const entry: RosterEntry = {
      agent,
      node,
      checks: exempt ? [] : checksFor(agent, node, r),
      ready: true,
      exempt,
      readiness: r,
    };
    entry.ready = entry.checks.every((c) => c.ok);

    const places = node ? (placement.get(node.id) ?? []) : [];
    if (places.length === 0) {
      put(UNPLACED_TITLE, entry);
      continue;
    }
    // An NPC in a faction AND a town belongs under both — a GM prepping the town
    // wants them listed there regardless of their guild.
    for (const p of places) put(p, entry);
  }

  // Named groups alphabetically, then the unplaced catch-all last: it is a
  // to-do list, not a place.
  const named = [...groups.values()]
    .filter((g) => g.title !== UNPLACED_TITLE)
    .sort((a, b) => a.title.localeCompare(b.title));
  const unplaced = groups.get(UNPLACED_TITLE);
  return unplaced ? [...named, unplaced] : named;
}

function checksFor(agent: Agent, node: GraphNode | null, r?: Readiness): Check[] {
  return [
    { key: "persona", label: "Persona written", ok: agent.persona.trim() !== "", fix: "agent" },
    { key: "voice", label: "Voice configured", ok: agent.voice !== "", fix: "agent" },
    { key: "link", label: "Wiki entry linked", ok: node !== null, fix: "link" },
    {
      key: "content",
      label: "Entry has content",
      // The ADR-0008 auto-node starts empty; either facts or prose counts.
      ok: node !== null && (node.bodyLen > 0 || node.publicAspectCount > 0),
      fix: "entry",
    },
    {
      key: "facts",
      // The figure the voice loop will actually inject, from the same renderer
      // (#535) — content on the entry does not guarantee the NPC receives it.
      label: r ? `${r.factCount} facts in reach` : "Facts in reach",
      ok: (r?.factCount ?? 0) > 0,
      fix: "entry",
    },
  ];
}

/** lastSpokeLabel renders the "last spoke" prep signal, which is context, not a check. */
export function lastSpokeLabel(r?: Readiness): string {
  if (!r?.lastSpokeAt) return "never spoken";
  return `last spoke ${r.lastSpokeAt.toLocaleDateString()}`;
}
