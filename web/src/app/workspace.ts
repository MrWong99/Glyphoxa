import type { GuestWorkspace } from "@gen/glyphoxa/management/v1/management_pb";

// The Workspace a URL names (ADR-0065). A browser session acts as ONE
// Workspace at a time — the user's own Tenant, or one Campaign they are a
// Campaign Guest of — and the server keeps that choice on the session row. The
// SPA mirrors it in the path's tenant slug so a link says where it belongs:
//
//   /t/default/…             the user's own Workspace (the slug every link has
//                            had since ADR-0039; any slug that is not a guest
//                            slug means the same)
//   /t/guest-<campaignId>/…  that Campaign, as its guest
//
// The slug never authorizes anything: no request carries it, and the server
// resolves the Workspace from the session on every call. It only tells the
// shell which Workspace this TAB is about, and the WorkspaceGate makes the
// session follow (SwitchWorkspace) when the two disagree. That is what keeps
// bookmarks sane: a bookmarked guest view reopens that Campaign instead of
// showing the same view of whatever Workspace the session happened to be in,
// and an old /t/default bookmark keeps meaning "my own table".

/** The slug of the user's own Workspace. */
export const OWN_WORKSPACE_SLUG = "default";

const GUEST_PREFIX = "guest-";

/** The slug of a Campaign opened as its guest. */
export function guestWorkspaceSlug(campaignId: string): string {
  return GUEST_PREFIX + campaignId;
}

/** The guest Campaign a slug names, or null when it names the own Workspace. */
export function slugGuestCampaign(slug: string): string | null {
  return slug.startsWith(GUEST_PREFIX) && slug.length > GUEST_PREFIX.length
    ? slug.slice(GUEST_PREFIX.length)
    : null;
}

/** The slug of the Workspace a session is acting as. */
export function workspaceSlug(user: { guest?: Pick<GuestWorkspace, "campaignId"> } | undefined): string {
  return user?.guest ? guestWorkspaceSlug(user.guest.campaignId) : OWN_WORKSPACE_SLUG;
}
