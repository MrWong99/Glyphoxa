import { useId, useState } from "react";
import { useMutation } from "@connectrpc/connect-query";
import { timestampDate } from "@bufbuild/protobuf/wkt";
import { toast } from "sonner";
import { UsersRound } from "lucide-react";

import {
  AccessStatus,
  AdminService,
  type AccessPerson,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Avatar } from "@/components/ui/Avatar";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { ConfirmDialog } from "@/components/ui/ConfirmDialog";
import { EmptyState } from "@/components/ui/EmptyState";
import { Switch } from "@/components/ui/Switch";
import { useI18n, type MessageKey, type TFunc } from "@/i18n";
import { errorMessage } from "@/lib/connectError";
import { relativeTime } from "@/screens/campaign/entryFormat";
import { useRefreshOverview } from "./refreshOverview";

// People: one row per AccessPerson — everyone the instance knows, once per
// table they run. A row says who they are, how they are admitted, what they
// run, whether their table may spend the Platform Keys, and offers the actions
// that make sense for exactly that status:
//
//   Revoke access     a holder of an Access Grant
//   Grant access      someone with no access — and, on an invite-only
//                     instance, someone who is only there by self-signup or as
//                     a Campaign Guest (a grant is what would let them sign in
//                     to a table of their own)
//   Suspend / Restore everyone who has signed in, except Platform Admins and
//                     the admin's own row
//
// Platform Admin rows are read-only: they are configured in the server
// environment and nothing here can change them (ADR-0065).
//
// The rows are cards in a list, not a table: at phone width a row's parts wrap
// under one another instead of scrolling sideways.

const STATUS: Record<AccessStatus, { label: MessageKey; variant: "gold" | "success" | "info" | "neutral" | "warning" }> = {
  [AccessStatus.UNSPECIFIED]: { label: "access.statusNone", variant: "warning" },
  [AccessStatus.PLATFORM_ADMIN]: { label: "access.statusPlatformAdmin", variant: "gold" },
  [AccessStatus.GRANTED]: { label: "access.statusGranted", variant: "success" },
  [AccessStatus.SIGNUP]: { label: "access.statusSignup", variant: "info" },
  [AccessStatus.GUEST]: { label: "access.statusGuest", variant: "neutral" },
  [AccessStatus.NONE]: { label: "access.statusNone", variant: "warning" },
};

/** A person appears once per table, so the Discord ID alone is not a row key. */
const rowKey = (p: AccessPerson) => `${p.discordUserId}/${p.tenantId}`;

/** What the admin reads as this person's name: theirs, or their Discord ID until they sign in. */
const displayName = (p: AccessPerson) => p.name || p.discordUserId;

function canGrant(p: AccessPerson, open: boolean): boolean {
  if (p.status === AccessStatus.NONE) return true;
  // With open sign-up a grant gates nothing, so offering one to a person who is
  // already in would be a button that does nothing they can see.
  return !open && (p.status === AccessStatus.SIGNUP || p.status === AccessStatus.GUEST);
}

function canSuspend(p: AccessPerson): boolean {
  // has_signed_in: a grant nobody has used yet names no user to suspend —
  // Revoke is the tool for that row.
  return p.status !== AccessStatus.PLATFORM_ADMIN && !p.isSelf && p.hasSignedIn;
}

// Why the Platform Keys switch cannot be used on a row, or null when it can.
function keysBlocked(p: AccessPerson, open: boolean): MessageKey | null {
  if (p.status === AccessStatus.PLATFORM_ADMIN) return "access.adminReadOnly";
  if (open) return "access.keysOpenMode";
  if (!p.tenantId && p.status !== AccessStatus.GRANTED) return "access.keysNoWorkspace";
  return null;
}

function workspaceLine(p: AccessPerson, t: TFunc): string {
  const count = (n: number, one: MessageKey, many: MessageKey) => (n === 1 ? t(one) : t(many, { count: n }));
  const parts: string[] = [];
  if (p.tenantId) {
    parts.push(p.tenantName || p.tenantId);
    parts.push(count(p.campaignCount, "access.campaignCountOne", "access.campaignCount"));
  }
  if (p.guestCampaignCount > 0) {
    parts.push(count(p.guestCampaignCount, "access.guestOfOne", "access.guestOf"));
  }
  return parts.length > 0 ? parts.join(" · ") : t("access.noWorkspace");
}

type Confirm = { kind: "revoke" | "suspend"; person: AccessPerson };

export function PeopleSection({ people, open }: { people: AccessPerson[]; open: boolean }) {
  const { t } = useI18n();
  const titleId = useId();
  const refresh = useRefreshOverview();

  const grant = useMutation(AdminService.method.grantAccess);
  const revoke = useMutation(AdminService.method.revokeAccess);
  const suspend = useMutation(AdminService.method.setUserSuspended);
  const tenantKeys = useMutation(AdminService.method.setTenantPlatformKeys);

  // The row whose write is in flight: its controls are busy, the others stay
  // usable. `keys` is what that row's switch was just set to, shown until the
  // re-read lands so the switch does not flick back to the old value.
  const [busy, setBusy] = useState<{ row: string; keys?: boolean } | null>(null);
  const [confirm, setConfirm] = useState<Confirm | null>(null);

  // One shape for every row action: do it, say what happened, re-read. A
  // failure is a toast (ADR-0017: a failed mutation is an event) and leaves the
  // list as the server last described it.
  const run = async (p: AccessPerson, action: () => Promise<unknown>, done: string, keys?: boolean) => {
    setBusy({ row: rowKey(p), keys });
    try {
      await action();
      toast.success(done);
      await refresh();
    } catch (err) {
      toast.error(t("access.actionFailed", { message: errorMessage(err) }));
    } finally {
      setBusy(null);
    }
  };

  const setKeys = (p: AccessPerson, next: boolean) =>
    run(
      p,
      // A table that exists takes the switch directly. Before the first
      // sign-in there is no table yet: the Access Grant carries the choice, and
      // granting again is how it is changed.
      () =>
        p.tenantId
          ? tenantKeys.mutateAsync({ tenantId: p.tenantId, enabled: next })
          : grant.mutateAsync({ discordUserId: p.discordUserId, note: p.note, platformKeys: next }),
      t(next ? "access.toastKeysOn" : "access.toastKeysOff", { name: displayName(p) }),
      next,
    );

  const confirmed = (c: Confirm) => {
    setConfirm(null);
    const name = displayName(c.person);
    if (c.kind === "revoke") {
      void run(
        c.person,
        () => revoke.mutateAsync({ discordUserId: c.person.discordUserId }),
        t("access.toastRevoked", { name }),
      );
    } else {
      void run(
        c.person,
        () => suspend.mutateAsync({ discordUserId: c.person.discordUserId, suspended: true }),
        t("access.toastSuspended", { name }),
      );
    }
  };

  return (
    <section className="gx-admin__section" aria-labelledby={titleId}>
      <h2 id={titleId} className="gx-section-title">
        {t("access.peopleTitle")}
      </h2>
      <p className="gx-admin__lede">{t("access.peopleLede")}</p>

      {people.length === 0 ? (
        <EmptyState
          icon={<UsersRound size={22} />}
          title={t("access.peopleEmptyTitle")}
          description={t("access.peopleEmptyDesc")}
        />
      ) : (
        <ul className="gx-admin__list">
          {people.map((p) => {
            const key = rowKey(p);
            const rowBusy = busy?.row === key;
            return (
              <PersonRow
                key={key}
                person={p}
                open={open}
                busy={rowBusy}
                keysShown={rowBusy && busy?.keys !== undefined ? busy.keys : p.platformKeys}
                onKeys={(next) => void setKeys(p, next)}
                onGrant={() =>
                  void run(
                    p,
                    // The row's own Platform Keys value rides along, so
                    // granting from a row never flips what it shows.
                    () =>
                      grant.mutateAsync({
                        discordUserId: p.discordUserId,
                        note: p.note,
                        platformKeys: p.platformKeys,
                      }),
                    t("access.toastGranted", { name: displayName(p) }),
                  )
                }
                onRevoke={() => setConfirm({ kind: "revoke", person: p })}
                onSuspend={() => setConfirm({ kind: "suspend", person: p })}
                onRestore={() =>
                  void run(
                    p,
                    () => suspend.mutateAsync({ discordUserId: p.discordUserId, suspended: false }),
                    t("access.toastRestored", { name: displayName(p) }),
                  )
                }
              />
            );
          })}
        </ul>
      )}

      <ConfirmDialog
        open={confirm !== null}
        onOpenChange={(next) => {
          if (!next) setConfirm(null);
        }}
        title={
          confirm
            ? t(confirm.kind === "revoke" ? "access.revokeTitle" : "access.suspendTitle", {
                name: displayName(confirm.person),
              })
            : ""
        }
        description={confirm ? t(confirm.kind === "revoke" ? "access.revokeDesc" : "access.suspendDesc") : undefined}
        confirmLabel={confirm?.kind === "suspend" ? t("access.suspend") : t("access.revoke")}
        onConfirm={() => {
          if (confirm) confirmed(confirm);
        }}
      />
    </section>
  );
}

function PersonRow({
  person: p,
  open,
  busy,
  keysShown,
  onKeys,
  onGrant,
  onRevoke,
  onSuspend,
  onRestore,
}: {
  person: AccessPerson;
  open: boolean;
  busy: boolean;
  keysShown: boolean;
  onKeys: (next: boolean) => void;
  onGrant: () => void;
  onRevoke: () => void;
  onSuspend: () => void;
  onRestore: () => void;
}) {
  const { t, lang } = useI18n();
  const keysHintId = useId();
  const name = displayName(p);
  const status = STATUS[p.status] ?? STATUS[AccessStatus.NONE];
  const admin = p.status === AccessStatus.PLATFORM_ADMIN;
  const blocked = keysBlocked(p, open);

  return (
    <li className="gx-access-row" aria-label={name} data-suspended={p.suspended ? "true" : undefined}>
      <div className="gx-access-row__who">
        <Avatar name={name} src={p.avatar || null} size="md" />
        <div className="gx-access-row__id">
          <span className="gx-access-row__name">{name}</span>
          <span className="gx-access-row__sub">
            {/* Until the first sign-in the Discord ID IS the name; after it, it
                is the detail that tells two people of one name apart. */}
            {!p.hasSignedIn
              ? t("access.notSignedInYet")
              : p.name
                ? t("access.discordId", { id: p.discordUserId })
                : null}
          </span>
          {p.note && <span className="gx-access-row__note">{p.note}</span>}
        </div>
      </div>

      <div className="gx-access-row__badges">
        <Badge variant={status.variant} size="sm">
          {t(status.label)}
        </Badge>
        {p.suspended && (
          <Badge variant="danger" size="sm">
            {t("access.suspended")}
          </Badge>
        )}
        {p.isSelf && (
          <Badge variant="arcane" size="sm">
            {t("access.you")}
          </Badge>
        )}
      </div>

      <dl className="gx-access-row__facts">
        <div>
          <dt>{t("access.workspace")}</dt>
          <dd>{workspaceLine(p, t)}</dd>
        </div>
        <div>
          <dt>{t("access.lastSeen")}</dt>
          <dd>{p.lastSeenAt ? relativeTime(timestampDate(p.lastSeenAt), lang) : t("access.never")}</dd>
        </div>
      </dl>

      <div className="gx-access-row__keys">
        <Switch
          label={t("access.platformKeys")}
          aria-label={t("access.platformKeysFor", { name })}
          aria-describedby={keysHintId}
          checked={keysShown}
          disabled={blocked !== null || busy}
          onCheckedChange={onKeys}
        />
        {/* What the switch means for this table — or, when it cannot be used,
            why (for a Platform Admin: that the row is set in the environment). */}
        <span className="gx-access-row__hint" id={keysHintId}>
          {t(blocked ?? (keysShown ? "access.keysOn" : "access.keysOff"))}
        </span>
      </div>

      {!admin && (
        <div className="gx-access-row__actions">
          {canGrant(p, open) && (
            <Button variant="secondary" size="sm" loading={busy} onClick={onGrant}>
              {t("access.grant")}
            </Button>
          )}
          {p.status === AccessStatus.GRANTED && (
            <Button variant="ghost" size="sm" disabled={busy} onClick={onRevoke}>
              {t("access.revoke")}
            </Button>
          )}
          {canSuspend(p) &&
            (p.suspended ? (
              <Button variant="secondary" size="sm" disabled={busy} onClick={onRestore}>
                {t("access.restore")}
              </Button>
            ) : (
              <Button variant="ghost" size="sm" disabled={busy} onClick={onSuspend}>
                {t("access.suspend")}
              </Button>
            ))}
        </div>
      )}
    </li>
  );
}
