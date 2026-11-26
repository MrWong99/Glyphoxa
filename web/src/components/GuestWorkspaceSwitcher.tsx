import { useRef, useState } from "react";
import { Check, ChevronDown, Eye, House } from "lucide-react";

import type { GuestWorkspace } from "@gen/glyphoxa/management/v1/management_pb";
import type { CurrentUser } from "@/app/currentUser";
import { useScreenNav } from "@/app/screenNav";
import { usePopoverDismiss } from "@/components/ui/usePopoverDismiss";
import { useI18n } from "@/i18n";

import "./campaignSwitcher.css";

// Workspace switching (ADR-0065). A browser session acts as one Workspace at a
// time: the user's own Tenant, or one Campaign they are a Campaign Guest of.
//
//   SharedWithMe            the "Shared with me" group — every Campaign the
//                           user is a guest of, as "campaign · host table". The
//                           GM's campaign switcher shows it under their own
//                           campaigns; the guest's switcher is mostly it.
//   GuestWorkspaceSwitcher  the topbar control while ACTING as a guest. It
//                           stands where the campaign switcher stands, and its
//                           face is the persistent indicator: "Guest of <host
//                           table>" over the campaign's name, on every screen.
//                           It offers the other shared campaigns and — for a
//                           user who has a table of their own — the way back
//                           to "My workspace". Nothing else: a guest has no
//                           campaigns to manage and none to create.
//
// Picking one only navigates (nav.goWorkspace): the path names the Workspace,
// and the WorkspaceGate switches the session and empties the cache before the
// other Workspace's shell mounts. Because it is a navigation, unsaved edits are
// asked about first, like any other.

export function SharedWithMe({
  workspaces,
  currentId,
  onPick,
}: {
  workspaces: readonly GuestWorkspace[];
  /** The Campaign the session is acting as a guest of, if any. */
  currentId?: string;
  onPick: (workspace: GuestWorkspace) => void;
}) {
  const { t } = useI18n();
  if (workspaces.length === 0) return null;
  return (
    <div className="gx-campaign-switcher__group">
      <span className="gx-overline gx-campaign-switcher__group-label" aria-hidden="true">
        {t("access.sharedWithMe")}
      </span>
      <ul className="gx-campaign-switcher__list" role="group" aria-label={t("access.sharedWithMe")}>
        {workspaces.map((w) => {
          const isCurrent = w.campaignId === currentId;
          return (
            <li key={w.campaignId} className="gx-campaign-switcher__row">
              <button
                type="button"
                className="gx-select__item gx-campaign-switcher__item"
                aria-current={isCurrent || undefined}
                data-active={isCurrent || undefined}
                onClick={() => onPick(w)}
              >
                <span className="gx-campaign-switcher__item-meta">
                  <span className="gx-campaign-switcher__item-name">{w.campaignName}</span>
                  {/* Whose table it is: two shared campaigns can share a name. */}
                  <span className="gx-campaign-switcher__item-system">{w.tenantName}</span>
                </span>
                {isCurrent && <Check size={14} aria-hidden />}
              </button>
            </li>
          );
        })}
      </ul>
    </div>
  );
}

export function GuestWorkspaceSwitcher({ user }: { user: CurrentUser }) {
  const { t } = useI18n();
  const nav = useScreenNav();
  const [open, setOpen] = useState(false);
  const wrapRef = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const guest = user.guest;

  const close = () => {
    if (wrapRef.current?.contains(document.activeElement)) triggerRef.current?.focus();
    setOpen(false);
  };
  usePopoverDismiss(wrapRef, open, close);

  if (!guest) return null;
  const indicator = t("access.guestIndicator", { tenant: guest.tenantName, campaign: guest.campaignName });

  return (
    <div className="gx-campaign-switcher" ref={wrapRef}>
      <button
        ref={triggerRef}
        type="button"
        className="gx-campaign-switcher__trigger"
        aria-expanded={open}
        // The indicator first, then what the button does.
        aria-label={`${indicator}. ${t("access.switchWorkspace")}`}
        title={indicator}
        data-testid="workspace-switcher-trigger"
        data-guest
        onClick={() => (open ? close() : setOpen(true))}
      >
        <span className="gx-campaign-switcher__sigil" aria-hidden>
          <Eye size={15} />
        </span>
        <span className="gx-campaign-switcher__label">
          <span className="gx-campaign-switcher__next gx-campaign-switcher__guest-of">
            {t("access.guestOfTenant", { tenant: guest.tenantName })}
          </span>
          <span className="gx-campaign-switcher__name">{guest.campaignName}</span>
        </span>
        <ChevronDown size={14} className="gx-campaign-switcher__chevron" aria-hidden />
      </button>

      {open && (
        <div className="gx-select__content gx-campaign-switcher__panel">
          {user.hasOwnTenant && (
            <ul className="gx-campaign-switcher__list" role="group" aria-label={t("access.myWorkspace")}>
              <li className="gx-campaign-switcher__row">
                <button
                  type="button"
                  className="gx-select__item gx-campaign-switcher__item"
                  onClick={() => {
                    close();
                    nav.goWorkspace(null);
                  }}
                >
                  <House size={15} aria-hidden />
                  <span className="gx-campaign-switcher__item-meta">
                    <span className="gx-campaign-switcher__item-name">{t("access.myWorkspace")}</span>
                    <span className="gx-campaign-switcher__item-system">{t("access.myWorkspaceHint")}</span>
                  </span>
                </button>
              </li>
            </ul>
          )}
          <SharedWithMe
            workspaces={user.guestWorkspaces}
            currentId={guest.campaignId}
            onPick={(w) => {
              close();
              // The current one is already open: picking it just closes.
              if (w.campaignId !== guest.campaignId) nav.goWorkspace(w.campaignId);
            }}
          />
        </div>
      )}
    </div>
  );
}
