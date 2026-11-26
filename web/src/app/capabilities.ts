import { useMemo } from "react";

import { GuestRight, type GuestWorkspace } from "@gen/glyphoxa/management/v1/management_pb";
import { useCurrentUser } from "./currentUser";

// What the signed-in principal may do in the Workspace this session acts as
// (ADR-0065) — the ONE place the web decides whether to offer a control.
//
// A Tenant's own operator (the GM) may do everything: GM_CAPABILITIES, which is
// also the answer outside the AuthGate (a screen under test), so nothing that
// existed before Campaign Guests behaves differently. A Campaign Guest may do
// exactly what their Guest Rights say, and nothing that no right covers:
//
//   wikiRead        read the wiki's public material (list, search, relations,
//                   tags, boards, the graph)
//   wikiSecrets     …including GM-only entries and private notes
//   wikiEdit        create, edit and delete entries, relations, tags and boards
//   mapsRead        open maps and see their pins
//   mapsEdit        add, move and delete pins; move the Party Marker
//   transcriptsRead read and search past sessions' transcripts, and the
//                   transcript lines an entry appears in
//   highlightsRead  see promoted Session Highlights
//   planningUse     use the Butler planning chat, in threads of their own
//   manage          everything no Guest Right covers — the GM's alone: cast,
//                   players, suggestions, sharing, campaign settings, Setup,
//                   session controls, AI generation (entries, portraits, map
//                   images, pin suggestions, recaps), creating and deleting
//                   maps, the world checkup, promoting, deleting and sharing
//                   highlights, export and import
//
// The server enforces all of it (the default-deny guest table); this only keeps
// a guest from being shown a control whose call would be refused.

export type Capabilities = {
  /** Acting as a Campaign Guest (false for the GM). */
  guest: boolean;
  manage: boolean;
  wikiRead: boolean;
  wikiSecrets: boolean;
  wikiEdit: boolean;
  mapsRead: boolean;
  mapsEdit: boolean;
  transcriptsRead: boolean;
  highlightsRead: boolean;
  planningUse: boolean;
};

export const GM_CAPABILITIES: Capabilities = {
  guest: false,
  manage: true,
  wikiRead: true,
  wikiSecrets: true,
  wikiEdit: true,
  mapsRead: true,
  mapsEdit: true,
  transcriptsRead: true,
  highlightsRead: true,
  planningUse: true,
};

/** The capabilities of a Workspace: everything for the own one, the rights for a guest one. */
export function capabilitiesFor(guest: Pick<GuestWorkspace, "rights"> | undefined): Capabilities {
  if (!guest) return GM_CAPABILITIES;
  // The server sends the normalized set, so an implied right is already in it.
  const has = (right: GuestRight) => guest.rights.includes(right);
  return {
    guest: true,
    manage: false,
    wikiRead: has(GuestRight.WIKI_READ),
    wikiSecrets: has(GuestRight.WIKI_SECRETS),
    wikiEdit: has(GuestRight.WIKI_EDIT),
    mapsRead: has(GuestRight.MAPS_READ),
    mapsEdit: has(GuestRight.MAPS_EDIT),
    transcriptsRead: has(GuestRight.TRANSCRIPTS_READ),
    highlightsRead: has(GuestRight.HIGHLIGHTS_READ),
    planningUse: has(GuestRight.PLANNING_USE),
  };
}

/**
 * What the current principal may do. Screens ask this — `useCan().wikiEdit` —
 * and never look at Guest Rights themselves.
 */
export function useCan(): Capabilities {
  const guest = useCurrentUser()?.guest;
  return useMemo(() => capabilitiesFor(guest), [guest]);
}
