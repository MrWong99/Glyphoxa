import { EdgeType, NodeType } from "@gen/glyphoxa/management/v1/management_pb";
import type { Agent, GraphEdge, GraphNode } from "@gen/glyphoxa/management/v1/management_pb";
import type { MessageKey } from "@/i18n";

// Roster prep derivations (#544): where each cast NPC sits in the world, and
// whether it is actually ready to speak.
//
// Every signal here was already queryable and none of it was displayed. The
// concrete failure it addresses: ADR-0008's second amendment auto-creates an NPC
// Node on Character-NPC create with an EMPTY body and never copies the Persona, so
// "added an NPC, it has a voice and a persona but nothing to say about the world"
// is the DEFAULT state after creation — discovered live, at the table.
//
// Three surfaces read the same derivation, so they can never disagree about who
// is ready: the roster list's warning marker, the agent editor's callout and the
// Readiness view.

/** One readiness check, with the thing that fixes it. */
export type Check = {
  key: "persona" | "voice" | "link" | "content" | "facts";
  /**
   * The check's display label as a MESSAGE KEY (plus interpolation params) —
   * this module runs outside React, so it never bakes a translated string into
   * derived state; the components translate at render time.
   */
  labelKey: MessageKey;
  labelParams?: { n: number };
  /**
   * The same check worded as what is MISSING ("No voice"), for the places that
   * only ever name failures: the roster marker's tooltip and the editor callout.
   */
  missingKey: MessageKey;
  ok: boolean;
  /**
   * Where the GM goes to satisfy it — `agent` opens the Agent editor, `entry`
   * opens the linked wiki entry, and `link` opens the World wiki, where the
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
  /**
   * With no linked Node: an unlinked NPC entry that carries the Agent's name —
   * the one the GM almost certainly means to link. A failing link check opens
   * it (its relations card holds the "Voiced by" control) instead of dropping
   * the GM on the bare wiki list to hunt for it.
   */
  linkCandidate: GraphNode | null;
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

type RosterGroup = {
  /** The faction or location name, or "" for the explicit unplaced group. */
  key: string;
  title: string;
  entries: RosterEntry[];
};

/**
 * Whether the wiki-side checks (link, content, facts) can be graded at all:
 * for everyone (a boolean, default true) or Agent by Agent (a predicate).
 */
export type WorldKnowledge = { world?: boolean | ((agentID: string) => boolean) };

/**
 * UNPLACED_TITLE is the catch-all group's sentinel key. It is explicit rather
 * than hidden. RosterPrep renders it through the message catalog (the English
 * copy matches this constant); every other group title is a Node name (data).
 */
export const UNPLACED_TITLE = "Not placed in the world";

/**
 * rosterEntries grades every roster member ONCE, in roster order (the Butler
 * first). It is the de-duplicated view: the grouped view below lists an NPC
 * under every place it belongs to, this lists it exactly once.
 *
 * `world: false` is for the moment before the Knowledge Graph and readiness
 * reads have landed (or after they failed): only the checks the roster payload
 * itself answers (personality, voice) are graded, so the roster never raises a
 * "no wiki entry" alarm it cannot yet know to be true. As a predicate it
 * answers per Agent — an NPC created a moment ago is not in a readiness read
 * that predates it, and must not be graded against that read either.
 */
export function rosterEntries(
  roster: Agent[],
  nodes: GraphNode[],
  readiness: Map<string, Readiness> = new Map(),
  opts: WorldKnowledge = {},
): RosterEntry[] {
  const known = opts.world;
  const world = typeof known === "function" ? known : () => known ?? true;
  const nodeByAgent = new Map<string, GraphNode>();
  const unlinkedByName = new Map<string, GraphNode>();
  for (const n of nodes) {
    if (n.agentId !== "") nodeByAgent.set(n.agentId, n);
    else if (n.nodeType === NodeType.NPC && !unlinkedByName.has(nameKey(n.name))) {
      unlinkedByName.set(nameKey(n.name), n);
    }
  }

  return roster.map((agent) => {
    const node = nodeByAgent.get(agent.id) ?? null;
    // The Butler is present, ungraded. Excluding it entirely would hide a roster
    // member the GM is looking for; grading it would flag it forever.
    const exempt = agent.role !== "character";
    const r = readiness.get(agent.id);
    const checks = exempt ? [] : checksFor(agent, node, r, world(agent.id));
    return {
      agent,
      node,
      linkCandidate: node || exempt ? null : (unlinkedByName.get(nameKey(agent.name)) ?? null),
      checks,
      ready: checks.every((c) => c.ok),
      exempt,
      readiness: r,
    };
  });
}

function nameKey(name: string): string {
  return name.trim().toLocaleLowerCase();
}

/**
 * prepSummary is the headline over a graded roster: how many NPCs there are,
 * how many are ready, and which ones are not. The Butler is exempt from all
 * three — "3 of 3 NPCs ready" counts Character NPCs only.
 */
export function prepSummary(entries: RosterEntry[]): {
  total: number;
  ready: number;
  attention: RosterEntry[];
} {
  const graded = entries.filter((e) => !e.exempt);
  const attention = graded.filter((e) => !e.ready);
  return { total: graded.length, ready: graded.length - attention.length, attention };
}

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
  opts: WorldKnowledge = {},
): RosterGroup[] {
  const byID = new Map(nodes.map((n) => [n.id, n]));

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

  for (const entry of rosterEntries(roster, nodes, readiness, opts)) {
    const places = entry.node ? (placement.get(entry.node.id) ?? []) : [];
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

function checksFor(agent: Agent, node: GraphNode | null, r: Readiness | undefined, world: boolean): Check[] {
  const own: Check[] = [
    {
      key: "persona",
      labelKey: "campaign.checkPersona",
      missingKey: "cast.missingPersona",
      ok: agent.persona.trim() !== "",
      fix: "agent",
    },
    {
      key: "voice",
      labelKey: "campaign.checkVoice",
      missingKey: "cast.missingVoice",
      ok: agent.voice !== "",
      fix: "agent",
    },
  ];
  if (!world) return own;
  return [
    ...own,
    {
      key: "link",
      labelKey: "campaign.checkLink",
      missingKey: "cast.missingLink",
      ok: node !== null,
      fix: "link",
    },
    {
      key: "content",
      labelKey: "campaign.checkContent",
      missingKey: "cast.missingContent",
      // The ADR-0008 auto-node starts empty; either facts or prose counts.
      ok: node !== null && (node.bodyLen > 0 || node.publicAspectCount > 0),
      fix: "entry",
    },
    {
      key: "facts",
      // The figure the voice loop will actually inject, from the same renderer
      // (#535) — content on the entry does not guarantee the NPC receives it.
      labelKey: r
        ? r.factCount === 1
          ? "campaign.checkFactsCountOne"
          : "campaign.checkFactsCount"
        : "campaign.checkFacts",
      labelParams: r && r.factCount !== 1 ? { n: r.factCount } : undefined,
      missingKey: "cast.missingFacts",
      ok: (r?.factCount ?? 0) > 0,
      fix: "entry",
    },
  ];
}
