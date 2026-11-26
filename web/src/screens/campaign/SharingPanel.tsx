import { useId, useMemo, useState, type FormEvent } from "react";
import { useMutation, useQuery } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { timestampDate } from "@bufbuild/protobuf/wkt";
import { toast } from "sonner";
import { Link2, Plus, UserPlus, UsersRound } from "lucide-react";

import {
  CampaignService,
  type CampaignGuest,
  type GuestRight,
  type Invitation,
  type ListCampaignAccessResponse,
} from "@gen/glyphoxa/management/v1/management_pb";
import {
  InvitationList,
  LinkCreatedDialog,
  LinkLabelField,
  LinkPinField,
  LinkUseFields,
  SNOWFLAKE_ERRORS,
  useLinkLimits,
} from "@/components/access/invitationParts";
import { DiscordUserField } from "@/components/DiscordUserField";
import { Alert } from "@/components/ui/Alert";
import { Avatar } from "@/components/ui/Avatar";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { ConfirmDialog } from "@/components/ui/ConfirmDialog";
import { Dialog, DialogClose } from "@/components/ui/Dialog";
import { EmptyState } from "@/components/ui/EmptyState";
import { QueryBoundary } from "@/components/ui/QueryBoundary";
import { Skeleton } from "@/components/ui/Skeleton";
import { useI18n, type TFunc } from "@/i18n";
import { errorMessage } from "@/lib/connectError";
import {
  GUEST_RIGHT_PRESETS,
  describeGuestRights,
  normalizeGuestRights,
  sameGuestRights,
} from "@/lib/guestRights";
import { invitationLink, snowflakeProblem } from "@/lib/invitationLink";
import { invalidateMethodQueries } from "@/lib/queryClient";
import { relativeTime } from "./entryFormat";
import { GuestRightsEditor } from "./GuestRightsEditor";

import "./sharing.css";

// The Campaign's Sharing view (ADR-0065): who else may open THIS Campaign, and
// what they may do in it. Three parts, top to bottom:
//
//   Guests            the Campaign Guests, each with their Guest Rights, an
//                     editor for them, and Remove
//   Add someone       by picking them from the voice channel or typing their
//                     Discord User ID — no link needed, and it works before
//                     the person has ever signed in
//   Invitation links  `/join/<token>` links for people whose ID the GM does
//                     not have; the full link is shown once, at creation
//
// One read (ListCampaignAccess) feeds the first and the last; every write
// re-reads it. All six RPCs are the GM's alone — a Campaign Guest is never
// offered this view (app/destinations.ts) and the server refuses them the
// calls whatever rights they hold.

/** What a new guest or a new link starts with: the Player preset. */
const STARTING_RIGHTS = normalizeGuestRights(GUEST_RIGHT_PRESETS[0].rights);

function useRefreshAccess(): () => Promise<void> {
  const queryClient = useQueryClient();
  return () => invalidateMethodQueries(queryClient, CampaignService.method.listCampaignAccess);
}

/** What the GM reads as a guest's name: theirs, or their Discord ID until they sign in. */
const guestName = (g: CampaignGuest) => g.name || g.discordUserId;

export function SharingPanel() {
  const { t } = useI18n();
  const access = useQuery(CampaignService.method.listCampaignAccess, {});
  return (
    // gx-admin: the wrapping card rows and section spacing of the Admin
    // screen's People and links lists (components/access/access.css).
    <div className="gx-admin gx-sharing">
      <QueryBoundary
        query={access}
        skeleton={<Skeleton.Row count={4} label={t("access.sharingLoading")} />}
        errorTitle={t("access.sharingLoadError")}
      >
        {(data) => <Sharing data={data} />}
      </QueryBoundary>
    </div>
  );
}

function Sharing({ data }: { data: ListCampaignAccessResponse }) {
  return (
    <>
      <GuestsSection guests={data.guests} />
      <AddGuestCard guests={data.guests} />
      <CampaignLinks invitations={data.invitations} />
    </>
  );
}

// ── Guests ──────────────────────────────────────────────────────────────────

function GuestsSection({ guests }: { guests: CampaignGuest[] }) {
  const { t } = useI18n();
  const titleId = useId();
  const refresh = useRefreshAccess();
  const [editing, setEditing] = useState<CampaignGuest | null>(null);
  const [removing, setRemoving] = useState<CampaignGuest | null>(null);

  const remove = useMutation(CampaignService.method.removeCampaignGuest, {
    onError: (err) => toast.error(t("access.actionFailed", { message: errorMessage(err) })),
  });

  return (
    <section className="gx-admin__section" aria-labelledby={titleId}>
      <h2 id={titleId} className="gx-section-title">
        {t("access.guestsTitle")}
      </h2>
      <p className="gx-admin__lede">{t("access.guestsLede")}</p>

      {guests.length === 0 ? (
        <EmptyState
          icon={<UsersRound size={22} />}
          title={t("access.guestsEmptyTitle")}
          description={t("access.guestsEmptyDesc")}
        />
      ) : (
        <ul className="gx-admin__list">
          {guests.map((g) => (
            <GuestRow
              key={g.userId}
              guest={g}
              busy={remove.isPending && remove.variables?.userId === g.userId}
              onEdit={() => setEditing(g)}
              onRemove={() => setRemoving(g)}
            />
          ))}
        </ul>
      )}

      {editing && <EditGuestDialog guest={editing} onClose={() => setEditing(null)} />}

      <ConfirmDialog
        open={removing !== null}
        onOpenChange={(next) => {
          if (!next) setRemoving(null);
        }}
        title={removing ? t("access.removeGuestTitle", { name: guestName(removing) }) : ""}
        description={t("access.removeGuestDesc")}
        confirmLabel={t("access.removeGuest")}
        onConfirm={() => {
          const g = removing;
          setRemoving(null);
          if (!g) return;
          remove.mutate(
            { userId: g.userId },
            {
              onSuccess: () => {
                toast.success(t("access.toastGuestRemoved", { name: guestName(g) }));
                void refresh();
              },
            },
          );
        }}
      />
    </section>
  );
}

function rightsSummary(rights: readonly GuestRight[], t: TFunc) {
  const rows = describeGuestRights(rights);
  if (rows.length === 0) return <span className="gx-access-row__sub">{t("access.guestNoRights")}</span>;
  return rows.map((r) => (
    <Badge key={r.right} variant="neutral" size="sm">
      {t(r.label)}
    </Badge>
  ));
}

function GuestRow({
  guest: g,
  busy,
  onEdit,
  onRemove,
}: {
  guest: CampaignGuest;
  busy: boolean;
  onEdit: () => void;
  onRemove: () => void;
}) {
  const { t, lang } = useI18n();
  const name = guestName(g);
  return (
    <li className="gx-access-row" aria-label={name}>
      <div className="gx-access-row__who">
        <Avatar name={name} src={g.avatar || null} size="md" />
        <div className="gx-access-row__id">
          <span className="gx-access-row__name">{name}</span>
          <span className="gx-access-row__sub">
            {/* Until the first sign-in the Discord ID IS the name; after it,
                it is the detail that tells two people of one name apart. */}
            {!g.hasSignedIn
              ? t("access.notSignedInYet")
              : g.name
                ? t("access.discordId", { id: g.discordUserId })
                : null}
          </span>
          {g.addedAt && (
            <span className="gx-access-row__sub">
              {t("access.guestAdded", { when: relativeTime(timestampDate(g.addedAt), lang) })}
            </span>
          )}
        </div>
      </div>

      <div className="gx-access-row__badges gx-sharing__rights" aria-label={t("access.guestRightsOf", { name })}>
        {rightsSummary(g.rights, t)}
      </div>

      <div className="gx-access-row__actions">
        <Button
          variant="secondary"
          size="sm"
          aria-label={t("access.editRightsNamed", { name })}
          disabled={busy}
          onClick={onEdit}
        >
          {t("access.editRights")}
        </Button>
        <Button
          variant="ghost"
          size="sm"
          aria-label={t("access.removeGuestNamed", { name })}
          loading={busy}
          onClick={onRemove}
        >
          {t("access.removeGuest")}
        </Button>
      </div>
    </li>
  );
}

function EditGuestDialog({ guest, onClose }: { guest: CampaignGuest; onClose: () => void }) {
  const { t } = useI18n();
  const refresh = useRefreshAccess();
  const [rights, setRights] = useState<GuestRight[]>(() => normalizeGuestRights(guest.rights));
  const name = guestName(guest);

  const update = useMutation(CampaignService.method.updateCampaignGuest, {
    onSuccess: () => {
      toast.success(t("access.toastRightsSaved", { name }));
      void refresh();
      onClose();
    },
  });

  return (
    <Dialog
      open
      onOpenChange={(next) => {
        if (!next) onClose();
      }}
      title={t("access.editRightsTitle", { name })}
      description={t("access.editRightsDesc")}
      size="lg"
      footer={
        <>
          <DialogClose>
            <Button variant="ghost">{t("common.cancel")}</Button>
          </DialogClose>
          <Button
            variant="primary"
            disabled={sameGuestRights(rights, guest.rights)}
            loading={update.isPending}
            onClick={() => update.mutate({ userId: guest.userId, rights })}
          >
            {t("access.saveRights")}
          </Button>
        </>
      }
    >
      <div className="gx-admin__form">
        <GuestRightsEditor value={rights} onChange={setRights} disabled={update.isPending} />
        {/* An empty set is allowed: the guest keeps the campaign in their list
            and can open nothing in it — say so rather than refuse it. */}
        {rights.length === 0 && <Alert tone="info">{t("access.emptyRightsNote")}</Alert>}
        {update.isError && (
          <Alert tone="danger" title={t("access.couldntSaveRights")}>
            {errorMessage(update.error)}
          </Alert>
        )}
      </div>
    </Dialog>
  );
}

// ── Add someone ─────────────────────────────────────────────────────────────

function AddGuestCard({ guests }: { guests: CampaignGuest[] }) {
  const { t } = useI18n();
  const titleId = useId();
  const refresh = useRefreshAccess();
  const [discordUserId, setDiscordUserId] = useState("");
  const [rights, setRights] = useState<GuestRight[]>(STARTING_RIGHTS);

  // The people in the voice channel right now (#279). Soft like the Players
  // view's read of it: a Bot that is offline leaves the list empty and the
  // typed ID as the only way in.
  const membersQuery = useQuery(CampaignService.method.listDiscordVoiceMembers, {}, { retry: false });
  const members = useMemo(() => membersQuery.data?.members ?? [], [membersQuery.data]);
  const isGuest = useMemo(() => new Set(guests.map((g) => g.discordUserId)), [guests]);

  const add = useMutation(CampaignService.method.addCampaignGuest, {
    onSuccess: (res, req) => {
      // Someone who has never signed in has no name on the server yet; the
      // voice channel may know one, and the ID is the last resort.
      const member = members.find((m) => m.discordUserId === req.discordUserId);
      toast.success(
        t("access.toastGuestAdded", {
          name: res.guest?.name || member?.displayName || (req.discordUserId ?? ""),
        }),
      );
      setDiscordUserId("");
      setRights(STARTING_RIGHTS);
      void refresh();
    },
  });

  const id = discordUserId.trim();
  const problem = snowflakeProblem(id);
  // Adding someone who is a guest already would REPLACE their rights; that is
  // what "Change rights" on their row is for, where the current set is shown.
  const already = problem === null && isGuest.has(id);
  const error = problem ? t(SNOWFLAKE_ERRORS[problem]) : already ? t("access.alreadyGuest") : null;
  const canSubmit = id !== "" && error === null && rights.length > 0;

  const submit = (e?: FormEvent) => {
    e?.preventDefault();
    if (!canSubmit || add.isPending) return;
    add.mutate({ discordUserId: id, rights });
  };

  return (
    <section className="gx-admin__section" aria-labelledby={titleId}>
      <h2 id={titleId} className="gx-section-title">
        {t("access.addGuestTitle")}
      </h2>
      <p className="gx-admin__lede">{t("access.addGuestLede")}</p>
      <Card className="gx-admin__card">
        <form className="gx-admin__form" onSubmit={submit} noValidate>
          <DiscordUserField
            value={discordUserId}
            onChange={(next) => {
              add.reset();
              setDiscordUserId(next);
            }}
            members={members}
            error={error}
            unavailable={(memberId) => (isGuest.has(memberId) ? t("access.alreadyGuestShort") : undefined)}
            memberHint={(name) => t("access.addGuestIs", { name })}
            onEnter={submit}
          />
          <GuestRightsEditor value={rights} onChange={setRights} disabled={add.isPending} />
          {rights.length === 0 && <span className="gx-admin__hint">{t("access.pickAtLeastOne")}</span>}
          {add.isError && (
            <Alert tone="danger" title={t("access.couldntAddGuest")}>
              {errorMessage(add.error)}
            </Alert>
          )}
          <div className="gx-admin__submit">
            <Button
              type="submit"
              variant="primary"
              size="sm"
              iconStart={<UserPlus size={14} />}
              disabled={!canSubmit}
              loading={add.isPending}
            >
              {t("access.addGuest")}
            </Button>
          </div>
        </form>
      </Card>
    </section>
  );
}

// ── Invitation links ────────────────────────────────────────────────────────

function CampaignLinks({ invitations }: { invitations: Invitation[] }) {
  const { t } = useI18n();
  const titleId = useId();
  const refresh = useRefreshAccess();
  const [creating, setCreating] = useState(false);

  const revoke = useMutation(CampaignService.method.revokeCampaignInvitation, {
    onSuccess: () => {
      toast.success(t("access.toastLinkRevoked"));
      void refresh();
    },
    onError: (err) => toast.error(t("access.actionFailed", { message: errorMessage(err) })),
  });

  const newLink = (
    <Button variant="secondary" size="sm" iconStart={<Plus size={14} />} onClick={() => setCreating(true)}>
      {t("access.newLink")}
    </Button>
  );

  return (
    <section className="gx-admin__section" aria-labelledby={titleId}>
      <div className="gx-admin__section-head">
        <h2 id={titleId} className="gx-section-title">
          {t("access.linksTitle")}
        </h2>
        {invitations.length > 0 && newLink}
      </div>
      <p className="gx-admin__lede">{t("access.campaignLinksLede")}</p>

      {invitations.length === 0 ? (
        <EmptyState
          icon={<Link2 size={22} />}
          title={t("access.linksEmptyTitle")}
          description={t("access.campaignLinksEmptyDesc")}
          action={newLink}
        />
      ) : (
        <InvitationList
          invitations={invitations}
          grants={(inv) => rightsSummary(inv.rights, t)}
          revokeDescription={t("access.revokeCampaignLinkDesc")}
          revoking={revoke.isPending}
          onRevoke={(inv) => revoke.mutate({ id: inv.id })}
        />
      )}

      {/* Mounted only while open: closing it unmounts the one place the full
          link was held, which is what "shown once" means. */}
      {creating && <CreateCampaignLinkDialog onClose={() => setCreating(false)} />}
    </section>
  );
}

function CreateCampaignLinkDialog({ onClose }: { onClose: () => void }) {
  const { t } = useI18n();
  const formId = useId();
  const refresh = useRefreshAccess();
  const limits = useLinkLimits();
  const [rights, setRights] = useState<GuestRight[]>(STARTING_RIGHTS);
  // The full link, held in this component's state and nowhere else.
  const [link, setLink] = useState<string | null>(null);

  const create = useMutation(CampaignService.method.createCampaignInvitation, {
    onSuccess: (res) => {
      setLink(invitationLink(res.token));
      void refresh();
    },
  });

  const valid = limits.valid && rights.length > 0;
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (!valid || create.isPending) return;
    create.mutate({ ...limits.request, rights });
  };

  const onOpenChange = (next: boolean) => {
    if (!next) onClose();
  };

  if (link !== null) return <LinkCreatedDialog link={link} onOpenChange={onOpenChange} />;

  return (
    <Dialog
      open
      onOpenChange={onOpenChange}
      title={t("access.newLink")}
      description={t("access.campaignLinkDesc")}
      size="lg"
      closeOnOutsideClick={false}
      footer={
        <>
          <DialogClose>
            <Button variant="ghost">{t("common.cancel")}</Button>
          </DialogClose>
          <Button type="submit" form={formId} variant="primary" disabled={!valid} loading={create.isPending}>
            {t("access.createLink")}
          </Button>
        </>
      }
    >
      <form id={formId} className="gx-admin__form" onSubmit={submit} noValidate>
        <LinkLabelField limits={limits} />
        <GuestRightsEditor value={rights} onChange={setRights} disabled={create.isPending} />
        {rights.length === 0 && <span className="gx-admin__hint">{t("access.pickAtLeastOne")}</span>}
        <LinkUseFields limits={limits} />
        <LinkPinField limits={limits} />
        {create.isError && (
          <Alert tone="danger" title={t("access.couldntCreateLink")}>
            {errorMessage(create.error)}
          </Alert>
        )}
      </form>
    </Dialog>
  );
}
