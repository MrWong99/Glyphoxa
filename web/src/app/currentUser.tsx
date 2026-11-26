import { createContext, useContext, type ReactNode } from "react";

import type {
  GetCurrentUserResponse,
  GuestWorkspace,
  User,
} from "@gen/glyphoxa/management/v1/management_pb";

// CurrentUser is the signed-in user as the app shell knows them: the identity
// GetCurrentUser returns plus the access facts that ride beside it on the same
// response (ADR-0065). The AuthGate assembles it once, so nothing below has to
// know that the two halves arrive in different places of one message.
export type CurrentUser = User & {
  /** On the deployment's Platform Admin list: the Admin screen exists for them. */
  isPlatformAdmin: boolean;
  /** Operates a Tenant of their own, whatever Workspace this session acts as. */
  hasOwnTenant: boolean;
  /** Set while this session acts as a Campaign Guest. */
  guest?: GuestWorkspace;
  /** Every Campaign the user is a Campaign Guest of. */
  guestWorkspaces: GuestWorkspace[];
  /**
   * The Tenant this session resolves to: the user's own, or — while acting as
   * a guest — the HOST's. Empty when the session resolves to no Workspace.
   */
  tenantId: string;
};

/**
 * True when the session has nowhere to be: no Tenant of the user's own and no
 * Campaign shared with them (a Campaign Guest whose last Campaign was taken
 * away). The shell has nothing to show such a user and says so instead of
 * rendering screens whose every read would be refused.
 */
export function hasNoWorkspace(user: CurrentUser): boolean {
  return !user.guest && !user.hasOwnTenant && user.tenantId === "" && user.guestWorkspaces.length === 0;
}

/** Folds a GetCurrentUser answer into a CurrentUser; undefined when it names no user. */
export function toCurrentUser(res: GetCurrentUserResponse): CurrentUser | undefined {
  if (!res.user) return undefined;
  return {
    ...res.user,
    isPlatformAdmin: res.isPlatformAdmin,
    hasOwnTenant: res.hasOwnTenant,
    guest: res.guest,
    guestWorkspaces: res.guestWorkspaces,
    tenantId: res.tenantId,
  };
}

const CurrentUserContext = createContext<CurrentUser | undefined>(undefined);

/** Hands the signed-in user to everything below (mounted by the AuthGate). */
export function CurrentUserProvider({ user, children }: { user: CurrentUser; children: ReactNode }) {
  return <CurrentUserContext.Provider value={user}>{children}</CurrentUserContext.Provider>;
}

/**
 * The signed-in user, or undefined outside the AuthGate (the public pages, a
 * component under test). Callers gate on a field, so "no user" reads as the
 * least-privileged answer: `useCurrentUser()?.isPlatformAdmin` is not true.
 */
export function useCurrentUser(): CurrentUser | undefined {
  return useContext(CurrentUserContext);
}
