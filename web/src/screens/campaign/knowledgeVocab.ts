import {
  Flag,
  Gem,
  GitBranch,
  MapPin,
  StickyNote,
  User,
  VenetianMask,
  type LucideIcon,
} from "lucide-react";

import { EdgeType, NodeType } from "@gen/glyphoxa/management/v1/management_pb";
import type { MessageKey, TFunc } from "@/i18n";

// The client-side world-wiki vocabulary — the per-type palette and the closed
// relation list — in ONE place. The list view, the relations editor, the proposal
// queue and the graph view (#534) all render the same seven types and nine
// relations, and three copies of the palette meant a new type coloured itself
// differently depending on which surface you looked at.
//
// The vocabularies themselves are the server's (pkg/kgvocab, ADR-0008): this file
// only carries what the wire enums cannot — the GM-facing label, the design colour
// and the icon. Labels are MESSAGE KEYS, not strings: every consumer resolves them
// through t() at render time, so the display language can change without this
// module holding a translated string in module-level state. The wire values
// (snake_case relation names, NodeType/EdgeType enums) are untouched.

type TypeMeta = { labelKey: MessageKey; color: string; Icon: LucideIcon };

/** Per-type label key, design colour and icon (approved #129 design). */
export const TYPE_META: Record<number, TypeMeta> = {
  [NodeType.CHARACTER]: { labelKey: "knowledge.typeCharacter", color: "#4fa9ff", Icon: User },
  [NodeType.NPC]: { labelKey: "knowledge.typeNpc", color: "#9059ff", Icon: VenetianMask },
  [NodeType.LOCATION]: { labelKey: "knowledge.typeLocation", color: "#35c48d", Icon: MapPin },
  [NodeType.FACTION]: { labelKey: "knowledge.typeFaction", color: "#ffbd4f", Icon: Flag },
  [NodeType.ITEM]: { labelKey: "knowledge.typeItem", color: "#ff7139", Icon: Gem },
  [NodeType.PLOT_THREAD]: { labelKey: "knowledge.typePlotThread", color: "#ff4f5e", Icon: GitBranch },
  [NodeType.NOTE]: { labelKey: "knowledge.typeNote", color: "#8b93a7", Icon: StickyNote },
};

/** The authorable types in enum order; UNSPECIFIED (0) is never offered. */
export const TYPE_ORDER: NodeType[] = [
  NodeType.CHARACTER,
  NodeType.NPC,
  NodeType.LOCATION,
  NodeType.FACTION,
  NodeType.ITEM,
  NodeType.PLOT_THREAD,
  NodeType.NOTE,
];

/** metaOf falls back to the Note grey for an unknown/unspecified type. */
export function metaOf(t: NodeType): TypeMeta {
  return TYPE_META[t] ?? TYPE_META[NodeType.NOTE];
}

/** alphaBg tints a type colour to the design's 14%-alpha tile background. */
export function alphaBg(color: string): string {
  return `${color}24`;
}

/**
 * The nine relation ("connection") types in enum order. `wire` is the snake_case
 * relation the server validates against (ADR-0008) — kept for data-* attributes
 * and CSS hooks, never rendered as copy. `labelKey` is the plain-language label
 * a GM reads, resolved through t() by every consumer.
 */
export const EDGE_TYPES: { value: EdgeType; wire: string; labelKey: MessageKey }[] = [
  { value: EdgeType.RESIDES_IN, wire: "resides_in", labelKey: "knowledge.edgeLivesIn" },
  { value: EdgeType.MEMBER_OF, wire: "member_of", labelKey: "knowledge.edgeMemberOf" },
  { value: EdgeType.OWNS, wire: "owns", labelKey: "knowledge.edgeOwns" },
  { value: EdgeType.KNOWS, wire: "knows", labelKey: "knowledge.edgeKnows" },
  { value: EdgeType.ENEMY_OF, wire: "enemy_of", labelKey: "knowledge.edgeEnemyOf" },
  { value: EdgeType.ALLY_OF, wire: "ally_of", labelKey: "knowledge.edgeAllyOf" },
  { value: EdgeType.PARENT_OF, wire: "parent_of", labelKey: "knowledge.edgeParentOf" },
  { value: EdgeType.PARTICIPATED_IN, wire: "participated_in", labelKey: "knowledge.edgeTookPartIn" },
  { value: EdgeType.MENTIONED_IN, wire: "mentioned_in", labelKey: "knowledge.edgeMentionedIn" },
];

/** Relation → display-label KEY. Render with t(EDGE_LABEL.get(...)!). */
const EDGE_LABEL = new Map<EdgeType, MessageKey>(
  EDGE_TYPES.map((e) => [e.value, e.labelKey]),
);
/** Relation → wire name, for data-* attributes and CSS hooks only. */
export const EDGE_WIRE = new Map<EdgeType, string>(EDGE_TYPES.map((e) => [e.value, e.wire]));

/** edgeLabel resolves a relation's display label, empty for an unknown relation. */
export function edgeLabel(t: TFunc, e: EdgeType): string {
  const key = EDGE_LABEL.get(e);
  return key ? t(key) : "";
}

/** edgeOptions builds the localized Select options for the relation vocabulary. */
export function edgeOptions(t: TFunc): { value: string; label: string }[] {
  return EDGE_TYPES.map((e) => ({ value: String(e.value), label: t(e.labelKey) }));
}

/**
 * DISPOSITION_OPTIONS names the -2..+2 relation scale in the words the GM chooses
 * by. A small closed scale rather than a free number: a scale nobody can calibrate
 * is worse than none, and this one drives an edge colour and ONE prompt clause.
 *
 * It lives here, beside the type and relation vocabularies, because two surfaces
 * show it — the relations editor authors it, and the proposal review card has to
 * render what an Agent PROPOSED before the GM approves it.
 */
const DISPOSITION_OPTIONS: { value: string; labelKey: MessageKey }[] = [
  { value: "-2", labelKey: "knowledge.dispositionHostile" },
  { value: "-1", labelKey: "knowledge.dispositionWary" },
  { value: "0", labelKey: "knowledge.dispositionNeutral" },
  { value: "1", labelKey: "knowledge.dispositionWarm" },
  { value: "2", labelKey: "knowledge.dispositionDevoted" },
];

/** Disposition → display-label KEY. Render with t(). */
export const DISPOSITION_LABEL = new Map<number, MessageKey>(
  DISPOSITION_OPTIONS.map((d) => [Number(d.value), d.labelKey]),
);

/** dispositionOptions builds the localized Select options for the scale. */
export function dispositionOptions(t: TFunc): { value: string; label: string }[] {
  return DISPOSITION_OPTIONS.map((d) => ({ value: d.value, label: t(d.labelKey) }));
}

/**
 * MAX_EDGE_NOTE_RUNES mirrors kgvocab.MaxEdgeNoteRunes. The server is the
 * authority and rejects anything longer; this stops the GM typing 300 characters
 * before finding that out.
 */
export const MAX_EDGE_NOTE_RUNES = 280;

const isCharacterOrNpc = (t: NodeType) => t === NodeType.CHARACTER || t === NodeType.NPC;

/**
 * isEdgeLegal mirrors the four rules of the server's validity matrix
 * (internal/storage/kg_edge.go ValidateEdge, ADR-0008 amendment), so the UI can
 * offer only what the server will accept instead of letting the GM pick a pair
 * and then refusing it:
 *
 *   lives in      points at a Location
 *   member of     points at a Faction
 *   took part in  points at a Plot thread
 *   parent of     joins a Character/NPC to a Character/NPC
 *
 * Every other relation (knows, owns, enemy of, ally of, mentioned in) is
 * unconstrained on both ends. The server stays the authority: this is a mirror,
 * and knowledgeVocab.test.ts writes the same table out so the two cannot drift
 * unnoticed.
 */
export function isEdgeLegal(edgeType: EdgeType, fromType: NodeType, toType: NodeType): boolean {
  switch (edgeType) {
    case EdgeType.RESIDES_IN:
      return toType === NodeType.LOCATION;
    case EdgeType.MEMBER_OF:
      return toType === NodeType.FACTION;
    case EdgeType.PARTICIPATED_IN:
      return toType === NodeType.PLOT_THREAD;
    case EdgeType.PARENT_OF:
      return isCharacterOrNpc(fromType) && isCharacterOrNpc(toType);
    case EdgeType.KNOWS:
    case EdgeType.OWNS:
    case EdgeType.ENEMY_OF:
    case EdgeType.ALLY_OF:
    case EdgeType.MENTIONED_IN:
      return true;
    default:
      // An unknown relation is never legal, exactly as on the server.
      return false;
  }
}

/** The relations the server will accept between these two entry types, in vocabulary order. */
export function legalEdgeTypes(fromType: NodeType, toType: NodeType): EdgeType[] {
  return EDGE_TYPES.filter((e) => isEdgeLegal(e.value, fromType, toType)).map((e) => e.value);
}

/**
 * Suggested fact labels per entry type: the handful of things a GM is asked
 * about that kind of entry at the table. A suggestion only pre-fills a row's
 * label (the GM writes the fact), and the ones that are secrets by nature start
 * GM only, so a hidden motive is not public by accident.
 */
export type FactSuggestion = { labelKey: MessageKey; gmPrivate?: boolean };

export const FACT_SUGGESTIONS: Record<number, FactSuggestion[]> = {
  [NodeType.CHARACTER]: [
    { labelKey: "knowledge.factSuggestAppearance" },
    { labelKey: "knowledge.factSuggestBackground" },
    { labelKey: "knowledge.factSuggestGoal" },
  ],
  [NodeType.NPC]: [
    { labelKey: "knowledge.factSuggestRole" },
    { labelKey: "knowledge.factSuggestAppearance" },
    { labelKey: "knowledge.factSuggestWants" },
    { labelKey: "knowledge.factSuggestSecret", gmPrivate: true },
  ],
  [NodeType.LOCATION]: [
    { labelKey: "knowledge.factSuggestAtmosphere" },
    { labelKey: "knowledge.factSuggestWhoIsHere" },
    { labelKey: "knowledge.factSuggestDangers" },
    { labelKey: "knowledge.factSuggestSecret", gmPrivate: true },
  ],
  [NodeType.FACTION]: [
    { labelKey: "knowledge.factSuggestGoal" },
    { labelKey: "knowledge.factSuggestLeader" },
    { labelKey: "knowledge.factSuggestMethods" },
    { labelKey: "knowledge.factSuggestSecret", gmPrivate: true },
  ],
  [NodeType.ITEM]: [
    { labelKey: "knowledge.factSuggestAppearance" },
    { labelKey: "knowledge.factSuggestPowers" },
    { labelKey: "knowledge.factSuggestHistory" },
  ],
  [NodeType.PLOT_THREAD]: [
    { labelKey: "knowledge.factSuggestStatus" },
    { labelKey: "knowledge.factSuggestStakes" },
    { labelKey: "knowledge.factSuggestNextStep" },
    { labelKey: "knowledge.factSuggestTruth", gmPrivate: true },
  ],
  [NodeType.NOTE]: [],
};
