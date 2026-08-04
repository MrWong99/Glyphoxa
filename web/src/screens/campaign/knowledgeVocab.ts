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

// The client-side Knowledge Graph vocabulary — the per-type palette and the closed
// relation list — in ONE place. The list view, the relations editor, the proposal
// queue and the graph view (#534) all render the same seven types and nine
// relations, and three copies of the palette meant a new type coloured itself
// differently depending on which surface you looked at.
//
// The vocabularies themselves are the server's (pkg/kgvocab, ADR-0008): this file
// only carries what the wire enums cannot — the GM-facing label, the design colour
// and the icon.

export type TypeMeta = { label: string; color: string; Icon: LucideIcon };

/** Per-type label, design colour and icon (approved #129 design). */
export const TYPE_META: Record<number, TypeMeta> = {
  [NodeType.CHARACTER]: { label: "Character", color: "#4fa9ff", Icon: User },
  [NodeType.NPC]: { label: "NPC", color: "#9059ff", Icon: VenetianMask },
  [NodeType.LOCATION]: { label: "Location", color: "#35c48d", Icon: MapPin },
  [NodeType.FACTION]: { label: "Faction", color: "#ffbd4f", Icon: Flag },
  [NodeType.ITEM]: { label: "Item", color: "#ff7139", Icon: Gem },
  [NodeType.PLOT_THREAD]: { label: "Plot thread", color: "#ff4f5e", Icon: GitBranch },
  [NodeType.NOTE]: { label: "Note", color: "#8b93a7", Icon: StickyNote },
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
 * The nine edge types in enum order. The label is the snake_case relation itself —
 * rendered in a mono arcane face, matching the approved design and the ADR-0008
 * vocabulary the server validates against.
 */
export const EDGE_TYPES: { value: EdgeType; label: string }[] = [
  { value: EdgeType.RESIDES_IN, label: "resides_in" },
  { value: EdgeType.MEMBER_OF, label: "member_of" },
  { value: EdgeType.OWNS, label: "owns" },
  { value: EdgeType.KNOWS, label: "knows" },
  { value: EdgeType.ENEMY_OF, label: "enemy_of" },
  { value: EdgeType.ALLY_OF, label: "ally_of" },
  { value: EdgeType.PARENT_OF, label: "parent_of" },
  { value: EdgeType.PARTICIPATED_IN, label: "participated_in" },
  { value: EdgeType.MENTIONED_IN, label: "mentioned_in" },
];

export const EDGE_LABEL = new Map<EdgeType, string>(EDGE_TYPES.map((e) => [e.value, e.label]));
export const EDGE_OPTIONS = EDGE_TYPES.map((e) => ({ value: String(e.value), label: e.label }));

/**
 * DISPOSITION_OPTIONS names the -2..+2 relation scale in the words the GM chooses
 * by. A small closed scale rather than a free number: a scale nobody can calibrate
 * is worse than none, and this one drives an edge colour and ONE prompt clause.
 *
 * It lives here, beside the type and relation vocabularies, because two surfaces
 * show it — the relations editor authors it, and the proposal review card has to
 * render what an Agent PROPOSED before the GM approves it.
 */
export const DISPOSITION_OPTIONS = [
  { value: "-2", label: "hostile" },
  { value: "-1", label: "wary" },
  { value: "0", label: "neutral" },
  { value: "1", label: "warm" },
  { value: "2", label: "devoted" },
];

export const DISPOSITION_LABEL = new Map<number, string>(
  DISPOSITION_OPTIONS.map((d) => [Number(d.value), d.label]),
);

/**
 * MAX_EDGE_NOTE_RUNES mirrors kgvocab.MaxEdgeNoteRunes. The server is the
 * authority and rejects anything longer; this stops the GM typing 300 characters
 * before finding that out.
 */
export const MAX_EDGE_NOTE_RUNES = 280;
