import { EdgeType, NodeType } from "@gen/glyphoxa/management/v1/management_pb";
import type { Agent, GraphEdge, GraphNode } from "@gen/glyphoxa/management/v1/management_pb";

// World-health derivations (#536). Every one of these is computed from the graph
// payload the Knowledge tab already holds — no new storage, and no extra read.
//
// They exist because each failure below is currently invisible until it bites at
// the table. The worst is the empty auto-created NPC entry: ADR-0008's second
// amendment creates a Node on Character-NPC create with an EMPTY body and never
// copies the Persona, so "added an NPC, it has a voice and a persona and nothing
// to say about the world" is the DEFAULT state after creation — and it is
// currently discovered live, mid-session.
//
// Nothing here mutates anything (ADR-0052's no-auto-merge posture generalised):
// every finding is a pointer to the editor, never a button that rewrites the wiki.

export type Finding = {
  /** The Node to open in the editor, or "" when the finding is about an Agent. */
  nodeID: string;
  /** The Agent to open on the roster, for findings whose fix is not a Node. */
  agentID?: string;
  name: string;
  nodeType: NodeType;
  /** Extra context for the row, when the name alone does not explain the problem. */
  detail?: string;
};

export type HealthCategory = {
  key: string;
  title: string;
  /** Why this matters at the table — the row is useless without the consequence. */
  why: string;
  findings: Finding[];
};

/**
 * worldHealth derives every category from (nodes, edges, roster). Empty categories
 * are dropped here rather than rendered empty: a wall of "0 problems" sections
 * trains the GM to skim past the one that is not zero.
 */
export function worldHealth(
  nodes: GraphNode[],
  edges: GraphEdge[],
  roster: Agent[],
): HealthCategory[] {
  const degree = new Map<string, number>();
  const incomingParticipation = new Set<string>();
  for (const e of edges) {
    degree.set(e.fromNodeId, (degree.get(e.fromNodeId) ?? 0) + 1);
    degree.set(e.toNodeId, (degree.get(e.toNodeId) ?? 0) + 1);
    if (e.edgeType === EdgeType.PARTICIPATED_IN) incomingParticipation.add(e.toNodeId);
  }
  const finding = (n: GraphNode, detail?: string): Finding => ({
    nodeID: n.id,
    name: n.name,
    nodeType: n.nodeType,
    detail,
  });

  const categories: HealthCategory[] = [];
  const push = (key: string, title: string, why: string, findings: Finding[]) => {
    if (findings.length > 0) categories.push({ key, title, why, findings });
  };

  // An orphan can never enter an NPC's Hot Context through the neighbour walk. It
  // is reachable only if the Node IS an Agent's own linked Node — so a linked one
  // is not an orphan in the sense that matters.
  push(
    "orphans",
    "Unconnected entries",
    "No relations, so no NPC can ever reach them through the neighbour walk — they only reach the table if an NPC is linked to them directly.",
    nodes.filter((n) => (degree.get(n.id) ?? 0) === 0 && n.agentId === "").map((n) => finding(n)),
  );

  // The ADR-0008 second-amendment auto-node: created empty, never filled.
  push(
    "empty-voiced",
    "Voiced NPCs with nothing to say",
    "These have an Agent and a voice, but their entry is empty — so they will improvise instead of speaking to your world.",
    nodes
      .filter((n) => n.agentId !== "" && n.bodyLen === 0 && n.aspectCount === 0)
      .map((n) => finding(n, "entry has no facts and no content")),
  );

  push(
    "unlinked-npcs",
    "NPC entries with no voice",
    "Written up but not voiced by any Agent. Fine if deliberate — a reminder if not.",
    nodes
      .filter((n) => n.nodeType === NodeType.NPC && n.agentId === "")
      .map((n) => finding(n)),
  );

  push(
    "dangling-threads",
    "Plot threads nobody is in",
    "Nothing participates_in them, so no NPC's context connects to them.",
    nodes
      .filter((n) => n.nodeType === NodeType.PLOT_THREAD && !incomingParticipation.has(n.id))
      .map((n) => finding(n)),
  );

  // The inverse of "empty voiced entry": a cast Agent with no entry at all. Keyed
  // off the roster rather than the graph, because the missing thing is a Node.
  const linkedAgents = new Set(nodes.map((n) => n.agentId).filter((id) => id !== ""));
  const unlinkedCast = roster.filter((a) => a.role === "character" && !linkedAgents.has(a.id));
  if (unlinkedCast.length > 0) {
    categories.push({
      key: "cast-without-entry",
      title: "Cast with no wiki entry",
      why: "Their world knowledge is empty by construction — the fact read is keyed by Agent, with no campaign-wide fallback.",
      findings: unlinkedCast.map((a) => ({
        nodeID: "",
        agentID: a.id,
        name: a.name,
        nodeType: NodeType.NPC,
        detail: "link an NPC entry to this Agent",
      })),
    });
  }

  return categories;
}
