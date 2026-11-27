import type { ReactNode } from "react";
import { useMutation } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { toast } from "sonner";
import { LogOut } from "lucide-react";

import { AuthService } from "@gen/glyphoxa/management/v1/management_pb";
import type { CurrentUser } from "@/app/currentUser";
import { OWN_WORKSPACE_SLUG, guestWorkspaceSlug } from "@/app/workspace";
import { SET_UP_OWN_TABLE_HREF, canSetUpOwnTable } from "@/components/GuestWorkspaceSwitcher";
import { Button } from "@/components/ui/Button";
import { Card, CardBody } from "@/components/ui/Card";
import { useI18n } from "@/i18n";
import { errorMessage } from "@/lib/connectError";

import "./workspaceNotice.css";

// The page the WorkspaceGate shows INSTEAD of the app shell when there is no
// Workspace to render it for (ADR-0065): the Campaign the URL names is not
// shared with this user (any more), the switch to it failed, or the account
// has nowhere to be at all. It says what happened in one sentence and offers
// every way out there is — the user's other Workspaces and signing out — so it
// is never a dead end, and never a shell full of refused requests.

export function WorkspaceNotice({
  title,
  body,
  onRetry,
  children,
}: {
  title: string;
  body: string;
  onRetry?: () => void;
  /** The ways out (WorkspaceExits). */
  children?: ReactNode;
}) {
  const { t } = useI18n();
  return (
    <main className="gx-workspace-notice">
      <Card>
        <CardBody>
          <h1 className="gx-workspace-notice__title">{title}</h1>
          <p className="gx-workspace-notice__body" role="status">
            {body}
          </p>
          {onRetry && (
            <Button variant="primary" size="sm" onClick={onRetry}>
              {t("common.retry")}
            </Button>
          )}
          {children}
        </CardBody>
      </Card>
    </main>
  );
}

/**
 * Every Workspace the user can open except the one that just failed them, and
 * signing out. `except` is the Workspace key the gate uses: a guest Campaign's
 * id, or "" for the own Workspace.
 */
export function WorkspaceExits({
  user,
  except,
  onOpen,
}: {
  user: CurrentUser;
  except: string;
  onOpen: (tenantSlug: string) => void;
}) {
  const { t } = useI18n();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const logout = useMutation(AuthService.method.logout, {
    onSuccess: () => {
      queryClient.clear();
      void navigate({ to: "/login" });
    },
    onError: (err) => toast.error(t("components.logOutFailed", { message: errorMessage(err) })),
  });
  const shared = user.guestWorkspaces.filter((w) => w.campaignId !== except);

  return (
    <div className="gx-workspace-notice__exits">
      {(user.hasOwnTenant && except !== "") || canSetUpOwnTable(user) || shared.length > 0 ? (
        <ul className="gx-workspace-notice__list" aria-label={t("access.exitsLabel")}>
          {user.hasOwnTenant && except !== "" && (
            <li>
              <Button variant="secondary" size="sm" onClick={() => onOpen(OWN_WORKSPACE_SLUG)}>
                {t("access.myWorkspace")}
              </Button>
            </li>
          )}
          {canSetUpOwnTable(user) && (
            <li>
              {/* A full page load: signing in again is what founds the table. */}
              <a className="gx-btn gx-btn--secondary gx-btn--sm gx-workspace-notice__link" href={SET_UP_OWN_TABLE_HREF}>
                {t("access.setUpTable")}
              </a>
            </li>
          )}
          {shared.map((w) => (
            <li key={w.campaignId}>
              <Button variant="secondary" size="sm" onClick={() => onOpen(guestWorkspaceSlug(w.campaignId))}>
                {t("access.sharedCampaign", { campaign: w.campaignName, tenant: w.tenantName })}
              </Button>
            </li>
          ))}
        </ul>
      ) : null}
      <Button
        variant="ghost"
        size="sm"
        iconStart={<LogOut size={14} />}
        loading={logout.isPending}
        onClick={() => logout.mutate({})}
      >
        {t("components.logOut")}
      </Button>
    </div>
  );
}
