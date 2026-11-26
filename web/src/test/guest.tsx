import type { ReactNode } from "react";
import { create } from "@bufbuild/protobuf";

import { GuestWorkspaceSchema, UserSchema, type GuestRight } from "@gen/glyphoxa/management/v1/management_pb";
import { CurrentUserProvider, type CurrentUser } from "@/app/currentUser";

// Test kit for the guest shell (ADR-0065): a signed-in user acting as a
// Campaign Guest with a given set of Guest Rights, and the provider that makes
// a screen under test see them. Without it a screen renders for the GM —
// "everything allowed" is the answer outside the AuthGate.

export const GUEST_CAMPAIGN_ID = "c-guest";

/** A guest Workspace: "The Sunless Citadel" at "Mira's Table", with these rights. */
export function guestWorkspace(rights: GuestRight[], over: { campaignId?: string; campaignName?: string } = {}) {
  return create(GuestWorkspaceSchema, {
    campaignId: over.campaignId ?? GUEST_CAMPAIGN_ID,
    campaignName: over.campaignName ?? "The Sunless Citadel",
    tenantName: "Mira's Table",
    rights,
  });
}

/** A user whose session acts as a Campaign Guest with exactly these rights. */
export function guestUser(rights: GuestRight[], over: Partial<CurrentUser> = {}): CurrentUser {
  const guest = guestWorkspace(rights);
  return {
    ...create(UserSchema, { id: "u-gwen", name: "Gwen", role: "", avatar: "" }),
    isPlatformAdmin: false,
    hasOwnTenant: false,
    guest,
    guestWorkspaces: [guest],
    tenantId: "t-mira",
    ...over,
  };
}

/** Renders its children as a Campaign Guest with exactly these rights. */
export function AsGuest({ rights, children }: { rights: GuestRight[]; children: ReactNode }) {
  return <CurrentUserProvider user={guestUser(rights)}>{children}</CurrentUserProvider>;
}
