import { GuestRight } from "@gen/glyphoxa/management/v1/management_pb";
import type { MessageKey } from "@/i18n";

// The Guest Right vocabulary as the web shows it (ADR-0065): one label and one
// sentence per right, in the order a person reads them — the wiki, then maps,
// then what was played, then the planning chat. The join page lists what an
// invitation grants from here, and the GM's guest editor offers the same rows,
// so a right is never called two things.
//
// The server stores the NORMALIZED set (wiki.edit and planning.use imply
// wiki.read + wiki.secrets; maps.edit implies maps.read), so a list rendered
// from a response already shows the implied rights; nothing here re-derives
// them.

export type GuestRightInfo = {
  right: GuestRight;
  label: MessageKey;
  description: MessageKey;
};

export const GUEST_RIGHTS: readonly GuestRightInfo[] = [
  { right: GuestRight.WIKI_READ, label: "access.rightWikiRead", description: "access.rightWikiReadDesc" },
  {
    right: GuestRight.WIKI_SECRETS,
    label: "access.rightWikiSecrets",
    description: "access.rightWikiSecretsDesc",
  },
  { right: GuestRight.WIKI_EDIT, label: "access.rightWikiEdit", description: "access.rightWikiEditDesc" },
  { right: GuestRight.MAPS_READ, label: "access.rightMapsRead", description: "access.rightMapsReadDesc" },
  { right: GuestRight.MAPS_EDIT, label: "access.rightMapsEdit", description: "access.rightMapsEditDesc" },
  {
    right: GuestRight.TRANSCRIPTS_READ,
    label: "access.rightTranscriptsRead",
    description: "access.rightTranscriptsReadDesc",
  },
  {
    right: GuestRight.HIGHLIGHTS_READ,
    label: "access.rightHighlightsRead",
    description: "access.rightHighlightsReadDesc",
  },
  {
    right: GuestRight.PLANNING_USE,
    label: "access.rightPlanningUse",
    description: "access.rightPlanningUseDesc",
  },
];

/**
 * The rows for a set of rights, in display order. A right this build does not
 * know (a newer server) is left out rather than shown as a number.
 */
export function describeGuestRights(rights: readonly GuestRight[]): GuestRightInfo[] {
  return GUEST_RIGHTS.filter((info) => rights.includes(info.right));
}

// ── The GM's rights editor (ADR-0065) ──────────────────────────────────────
// Everything below serves the Campaign's Sharing view: which rights drag
// others along, how the rows are grouped, and the three starting points.

const R = GuestRight;

/**
 * What a right implies — the server's normalization, mirrored so the editor
 * can tick and lock the implied boxes before the round trip instead of
 * surprising the GM after it:
 *   wiki.edit    ⇒ wiki.read + wiki.secrets (an editor blind to private notes
 *                  would erase them on save)
 *   maps.edit    ⇒ maps.read
 *   planning.use ⇒ wiki.read + wiki.secrets (the Butler answers from the whole
 *                  Knowledge Graph)
 */
export const GUEST_RIGHT_IMPLIES: Readonly<Partial<Record<GuestRight, readonly GuestRight[]>>> = {
  [R.WIKI_EDIT]: [R.WIKI_READ, R.WIKI_SECRETS],
  [R.MAPS_EDIT]: [R.MAPS_READ],
  [R.PLANNING_USE]: [R.WIKI_READ, R.WIKI_SECRETS],
};

const ORDER = GUEST_RIGHTS.map((info) => info.right);

/**
 * The set as the server will store it: implied rights added, duplicates and
 * rights this build does not know dropped, in display order (so two sets
 * compare with a plain element-wise check).
 */
export function normalizeGuestRights(rights: readonly GuestRight[]): GuestRight[] {
  const set = new Set<GuestRight>(rights);
  for (const right of rights) for (const implied of GUEST_RIGHT_IMPLIES[right] ?? []) set.add(implied);
  return ORDER.filter((right) => set.has(right));
}

/**
 * The rights in a set that are there because another one needs them, each with
 * the rights that hold it. The editor shows those boxes ticked and locked.
 */
export function lockedGuestRights(rights: readonly GuestRight[]): Map<GuestRight, GuestRight[]> {
  const locked = new Map<GuestRight, GuestRight[]>();
  for (const right of normalizeGuestRights(rights)) {
    for (const implied of GUEST_RIGHT_IMPLIES[right] ?? []) {
      locked.set(implied, [...(locked.get(implied) ?? []), right]);
    }
  }
  return locked;
}

/**
 * The set after one box is ticked or cleared. Clearing a locked box changes
 * nothing (the right that needs it has to go first). "Including GM secrets" is
 * a refinement of reading the wiki, so ticking it ticks reading and it goes
 * when reading goes — the server would accept secrets alone, but that would
 * grant nothing a guest could open.
 */
export function toggleGuestRight(
  rights: readonly GuestRight[],
  right: GuestRight,
  on: boolean,
): GuestRight[] {
  const current = normalizeGuestRights(rights);
  if (on) {
    const next = [...current, right];
    if (right === R.WIKI_SECRETS) next.push(R.WIKI_READ);
    return normalizeGuestRights(next);
  }
  if (lockedGuestRights(current).has(right)) return current;
  return normalizeGuestRights(
    current.filter((r) => r !== right && !(right === R.WIKI_READ && r === R.WIKI_SECRETS)),
  );
}

/** True when two sets grant the same thing, whatever order they arrive in. */
export function sameGuestRights(a: readonly GuestRight[], b: readonly GuestRight[]): boolean {
  const x = normalizeGuestRights(a);
  const y = normalizeGuestRights(b);
  return x.length === y.length && x.every((right, i) => right === y[i]);
}

/** One checkbox of the editor: the short label it has inside its group. */
export type GuestRightChoice = {
  right: GuestRight;
  label: MessageKey;
  /** What ticking it reveals or costs, said to the GM. */
  description: MessageKey;
};

export type GuestRightGroup = { id: string; label: MessageKey; choices: readonly GuestRightChoice[] };

/** The editor's rows, grouped by what they open. */
export const GUEST_RIGHT_GROUPS: readonly GuestRightGroup[] = [
  {
    id: "wiki",
    label: "access.groupWiki",
    choices: [
      { right: R.WIKI_READ, label: "access.pickRead", description: "access.rightWikiReadDesc" },
      { right: R.WIKI_SECRETS, label: "access.pickSecrets", description: "access.rightWikiSecretsDesc" },
      { right: R.WIKI_EDIT, label: "access.pickEdit", description: "access.rightWikiEditDesc" },
    ],
  },
  {
    id: "maps",
    label: "access.groupMaps",
    choices: [
      { right: R.MAPS_READ, label: "access.pickView", description: "access.rightMapsReadDesc" },
      { right: R.MAPS_EDIT, label: "access.pickEditPins", description: "access.rightMapsEditDesc" },
    ],
  },
  {
    id: "sessions",
    label: "access.groupSessions",
    choices: [
      {
        right: R.TRANSCRIPTS_READ,
        label: "access.pickTranscripts",
        description: "access.rightTranscriptsReadDesc",
      },
    ],
  },
  {
    id: "highlights",
    label: "access.groupHighlights",
    choices: [
      { right: R.HIGHLIGHTS_READ, label: "access.pickView", description: "access.rightHighlightsReadDesc" },
    ],
  },
  {
    id: "planning",
    label: "access.groupPlanning",
    choices: [{ right: R.PLANNING_USE, label: "access.pickUse", description: "access.pickPlanningDesc" }],
  },
];

export type GuestRightPreset = {
  id: "player" | "reader" | "coGm";
  label: MessageKey;
  description: MessageKey;
  rights: readonly GuestRight[];
};

const PLAYER = [R.WIKI_READ, R.MAPS_READ, R.HIGHLIGHTS_READ];

/** One-click starting points; the GM can change any box afterwards. */
export const GUEST_RIGHT_PRESETS: readonly GuestRightPreset[] = [
  { id: "player", label: "access.presetPlayer", description: "access.presetPlayerDesc", rights: PLAYER },
  {
    id: "reader",
    label: "access.presetReader",
    description: "access.presetReaderDesc",
    rights: [...PLAYER, R.TRANSCRIPTS_READ],
  },
  { id: "coGm", label: "access.presetCoGm", description: "access.presetCoGmDesc", rights: ORDER },
];

/** The preset a set is exactly, if any — so its button can read as chosen. */
export function matchingPreset(rights: readonly GuestRight[]): GuestRightPreset | undefined {
  return GUEST_RIGHT_PRESETS.find((preset) => sameGuestRights(preset.rights, rights));
}
