import type { UserRole } from "./types";

const roleLevels: Record<UserRole, number> = {
  viewer: 0,
  dm: 1,
  tenant_admin: 2,
  super_admin: 3,
};

/** Returns true if the user's role meets or exceeds the minimum required role. */
export function hasMinRole(
  userRole: UserRole | undefined,
  minRole: UserRole,
): boolean {
  if (!userRole) return false;
  return (roleLevels[userRole] ?? 0) >= (roleLevels[minRole] ?? 0);
}
