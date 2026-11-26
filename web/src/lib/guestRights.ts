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
