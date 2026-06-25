import { useMutation } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { LogOut } from "lucide-react";

import { AuthService } from "@gen/glyphoxa/management/v1/management_pb";
import type { User } from "@gen/glyphoxa/management/v1/management_pb";

import { Avatar } from "./ui/Avatar";

// SidebarUser is the app-shell footer identity (ADR-0016 / ADR-0039): it renders
// the real signed-in operator (name / role / avatar) the AuthGate resolved,
// replacing the design's hardcoded "Operator / Sora Vance". The logout button
// calls AuthService.Logout, which deletes the server-side session row and clears
// the cookies; on success it drops the cached queries and routes to /login.
export function SidebarUser({ user }: { user: User }) {
  const navigate = useNavigate();
  const queryClient = useQueryClient();

  const logout = useMutation(AuthService.method.logout, {
    onSuccess: () => {
      queryClient.clear();
      void navigate({ to: "/login" });
    },
  });

  return (
    <div className="gx-sidebar__user">
      <Avatar name={user.name} src={user.avatar || null} size="sm" status="live" />
      <div className="gx-sidebar__user-meta">
        <div className="gx-sidebar__user-name">{user.name}</div>
        <div className="gx-sidebar__user-role">{user.role}</div>
      </div>
      <button
        type="button"
        className="gx-sidebar__logout"
        aria-label="Log out"
        onClick={() => logout.mutate({})}
        disabled={logout.isPending}
        style={{
          background: "none",
          border: "none",
          color: "var(--text-subtle)",
          cursor: "pointer",
          padding: 4,
        }}
      >
        <LogOut size={15} />
      </button>
    </div>
  );
}
