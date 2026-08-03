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
  /** Where the GM goes to satisfy it. */
  fix: "persona" | "voice" | "link" | "content" | "relations";
};

export type RosterEntry = {
  agent: Agent;
  /** The Agent's linked Node, when it has one. */
  node: GraphNode | null;
  checks: Check[];
  /** True once every check passes. */
  ready: boolean;
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
 * rosterPrep groups cast NPCs by their linked Node's faction/location neighbours
 * and computes each one's readiness.
 *
 * The Butler is deliberately absent from the CHECKS: it is Address-Only,
 * auto-created and undeletable (ADR-0009) and has no linked Node by design, so
 * grading it against character-oriented checks would be a permanent false alarm.
 * The caller still lists it.
 */
export function rosterPrep(
  roster: Agent[],
  nodes: GraphNode[],
  edges: GraphEdge[],
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
    if (agent.role !== "character") continue;
    const node = nodeByAgent.get(agent.id) ?? null;
    const entry: RosterEntry = { agent, node, checks: checksFor(agent, node), ready: false };
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

function checksFor(agent: Agent, node: GraphNode | null): Check[] {
  return [
    {
      key: "persona",
      label: "Persona written",
      ok: agent.persona.trim() !== "",
      fix: "persona",
    },
    { key: "voice", label: "Voice configured", ok: agent.voice !== "", fix: "voice" },
    { key: "link", label: "Wiki entry linked", ok: node !== null, fix: "link" },
    {
      key: "content",
      label: "Entry has content",
      // The ADR-0008 auto-node starts empty; either facts or prose counts.
      ok: node !== null && (node.bodyLen > 0 || node.aspectCount > 0),
      fix: "content",
    },
  ];
}
