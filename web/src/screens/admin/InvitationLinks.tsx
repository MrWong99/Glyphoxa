import { useId, useState, type FormEvent } from "react";
import { useMutation } from "@connectrpc/connect-query";
import { timestampDate, timestampMs } from "@bufbuild/protobuf/wkt";
import { toast } from "sonner";
import { Link2, Plus } from "lucide-react";

import { AdminService, type Invitation } from "@gen/glyphoxa/management/v1/management_pb";
import { Alert } from "@/components/ui/Alert";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { ConfirmDialog } from "@/components/ui/ConfirmDialog";
import { CopyButton } from "@/components/ui/CopyButton";
import { Dialog, DialogClose } from "@/components/ui/Dialog";
import { EmptyState } from "@/components/ui/EmptyState";
import { Input } from "@/components/ui/Input";
import { Switch } from "@/components/ui/Switch";
import { useI18n, type Lang, type MessageKey, type TFunc } from "@/i18n";
import { errorMessage } from "@/lib/connectError";
import { invitationLink, snowflakeProblem } from "@/lib/invitationLink";
import { SNOWFLAKE_ERRORS } from "./AddPersonCard";
import { useRefreshOverview } from "./refreshOverview";

// Access invitation links (ADR-0065): a `/join/<token>` link a Platform Admin
// hands to someone whose Discord ID they do not know. Whoever opens it and
// signs in gets an Access Grant — and so a table of their own.
//
// The token is a secret the server keeps only as a hash. It is readable exactly
// once, in the CreateAccessInvitation response, so the create dialog shows the
// full link right there, with a copy button and the plain statement that it
// will not be shown again. The list below never has it: a link is identified by
// its label, its limits and its state.

type LinkState = "active" | "expired" | "usedUp" | "revoked" | "inactive";

const LINK_STATES: Record<LinkState, { label: MessageKey; variant: "success" | "neutral" | "danger" }> = {
  active: { label: "access.linkActive", variant: "success" },
  expired: { label: "access.linkExpired", variant: "neutral" },
  usedUp: { label: "access.linkUsedUp", variant: "neutral" },
  revoked: { label: "access.linkRevoked", variant: "danger" },
  inactive: { label: "access.linkInactive", variant: "neutral" },
};

// Whether a link works is the SERVER's verdict (`live`); this only names the
// reason a dead one is dead, from the facts on the row. A dead link whose
// reason cannot be read off them (a clock a few seconds apart, a rule a newer
// server added) is "no longer works" rather than a guess.
export function linkState(inv: Invitation, now: number = Date.now()): LinkState {
  if (inv.revokedAt) return "revoked";
  if (inv.live) return "active";
  if (inv.expiresAt && timestampMs(inv.expiresAt) <= now) return "expired";
  if (inv.maxUses > 0 && inv.uses >= inv.maxUses) return "usedUp";
  return "inactive";
}

function usesLine(inv: Invitation, t: TFunc): string {
  return inv.maxUses > 0
    ? t("access.linkUses", { uses: inv.uses, max: inv.maxUses })
    : t("access.linkUsesUnlimited", { uses: inv.uses });
}

function expiryLine(inv: Invitation, t: TFunc, lang: Lang): string {
  if (!inv.expiresAt) return t("access.linkNeverExpires");
  return t("access.linkExpires", {
    date: timestampDate(inv.expiresAt).toLocaleDateString(lang, { dateStyle: "medium" }),
  });
}

export function InvitationLinks({ invitations, open }: { invitations: Invitation[]; open: boolean }) {
  const { t, lang } = useI18n();
  const titleId = useId();
  const refresh = useRefreshOverview();
  const [creating, setCreating] = useState(false);
  const [revoking, setRevoking] = useState<Invitation | null>(null);

  const revoke = useMutation(AdminService.method.revokeAccessInvitation, {
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
      <p className="gx-admin__lede">{t("access.linksLede")}</p>

      {invitations.length === 0 ? (
        <EmptyState
          icon={<Link2 size={22} />}
          title={t("access.linksEmptyTitle")}
          description={t("access.linksEmptyDesc")}
          action={newLink}
        />
      ) : (
        <ul className="gx-admin__list">
          {invitations.map((inv) => {
            const state = linkState(inv);
            const label = inv.label || t("access.linkUntitled");
            return (
              <li key={inv.id} className="gx-link-row" aria-label={label} data-state={state}>
                <div className="gx-link-row__main">
                  <span className="gx-link-row__label">{label}</span>
                  <Badge variant={LINK_STATES[state].variant} size="sm">
                    {t(LINK_STATES[state].label)}
                  </Badge>
                  {inv.platformKeys && (
                    <Badge variant="gold" size="sm">
                      {t("access.linkWithKeys")}
                    </Badge>
                  )}
                </div>
                <div className="gx-link-row__meta">
                  <span>{usesLine(inv, t)}</span>
                  <span>{expiryLine(inv, t, lang)}</span>
                  {inv.discordUserId && <span>{t("access.linkPinned", { id: inv.discordUserId })}</span>}
                </div>
                {/* Only a link that still works can be revoked; a dead one
                    stays listed as the record of what was handed out. */}
                {state === "active" && (
                  <div className="gx-link-row__actions">
                    <Button
                      variant="ghost"
                      size="sm"
                      aria-label={t("access.revokeLinkNamed", { label })}
                      disabled={revoke.isPending}
                      onClick={() => setRevoking(inv)}
                    >
                      {t("access.revokeLink")}
                    </Button>
                  </div>
                )}
              </li>
            );
          })}
        </ul>
      )}

      {/* Mounted only while open: closing it unmounts the one place the full
          link was held, which is what "shown once" means. */}
      {creating && <CreateLinkDialog open={open} onClose={() => setCreating(false)} />}

      <ConfirmDialog
        open={revoking !== null}
        onOpenChange={(next) => {
          if (!next) setRevoking(null);
        }}
        title={t("access.revokeLinkTitle")}
        description={t("access.revokeLinkDesc")}
        confirmLabel={t("access.revokeLink")}
        onConfirm={() => {
          const inv = revoking;
          setRevoking(null);
          if (inv) revoke.mutate({ id: inv.id });
        }}
      />
    </section>
  );
}

/**
 * A field's text as a count: 0 for blank ("unlimited" / "never" — the wire's
 * own zero), NaN when it is not a whole number within [1, max]. Text, not
 * type="number": a number field reports an entry it cannot parse as "", which
 * would read as "unlimited" and mint a link wider than the admin typed.
 */
function parseCount(value: string, max: number): number {
  const v = value.trim();
  if (v === "") return 0;
  if (!/^\d+$/.test(v)) return NaN;
  const n = Number(v);
  return n >= 1 && n <= max ? n : NaN;
}

const MAX_USES = 2_147_483_647; // int32 on the wire
const MAX_EXPIRY_DAYS = 365;

function CreateLinkDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const { t } = useI18n();
  const formId = useId();
  const keysHintId = useId();
  const refresh = useRefreshOverview();

  const [label, setLabel] = useState("");
  const [maxUses, setMaxUses] = useState("");
  const [expiry, setExpiry] = useState("");
  const [platformKeys, setPlatformKeys] = useState(false);
  const [pin, setPin] = useState("");
  // The full link, held in this component's state and nowhere else.
  const [link, setLink] = useState<string | null>(null);

  const create = useMutation(AdminService.method.createAccessInvitation, {
    onSuccess: (res) => {
      setLink(invitationLink(res.token));
      void refresh();
    },
  });

  const maxUsesV = parseCount(maxUses, MAX_USES);
  const expiryV = parseCount(expiry, MAX_EXPIRY_DAYS);
  const pinProblem = snowflakeProblem(pin);
  const valid = !Number.isNaN(maxUsesV) && !Number.isNaN(expiryV) && pinProblem === null;

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (!valid || create.isPending) return;
    create.mutate({
      label: label.trim(),
      maxUses: maxUsesV,
      expiresInDays: expiryV,
      platformKeys,
      discordUserId: pin.trim(),
    });
  };

  const onOpenChange = (next: boolean) => {
    if (!next) onClose();
  };

  if (link !== null) {
    return (
      <Dialog
        open
        onOpenChange={onOpenChange}
        title={t("access.linkCreatedTitle")}
        // A stray click must not throw away the only showing of the link.
        closeOnOutsideClick={false}
        footer={
          <DialogClose>
            <Button variant="primary">{t("access.done")}</Button>
          </DialogClose>
        }
      >
        <div className="gx-admin__form">
          <Input
            label={t("access.linkField")}
            value={link}
            readOnly
            className="gx-admin__link"
            onFocus={(e) => e.currentTarget.select()}
          />
          <div className="gx-admin__copy">
            <CopyButton text={link} label={t("access.copyLink")} showLabel />
          </div>
          <Alert tone="warning">{t("access.linkCreatedOnce")}</Alert>
        </div>
      </Dialog>
    );
  }

  return (
    <Dialog
      open
      onOpenChange={onOpenChange}
      title={t("access.newLink")}
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
        <Input
          label={t("access.linkLabel")}
          hint={t("access.linkLabelHint")}
          autoComplete="off"
          maxLength={200}
          value={label}
          onChange={(e) => setLabel(e.target.value)}
        />
        <div className="gx-admin__pair">
          <Input
            label={t("access.linkMaxUses")}
            hint={t("access.linkMaxUsesHint")}
            error={Number.isNaN(maxUsesV) ? t("access.linkMaxUsesInvalid") : null}
            placeholder={t("access.unlimited")}
            inputMode="numeric"
            autoComplete="off"
            value={maxUses}
            onChange={(e) => setMaxUses(e.target.value)}
          />
          <Input
            label={t("access.linkExpiry")}
            hint={t("access.linkExpiryHint")}
            error={Number.isNaN(expiryV) ? t("access.linkExpiryInvalid") : null}
            placeholder={t("access.neverPlaceholder")}
            inputMode="numeric"
            autoComplete="off"
            value={expiry}
            onChange={(e) => setExpiry(e.target.value)}
          />
        </div>
        <div className="gx-admin__switch">
          <Switch
            label={t("access.mayUseKeys")}
            aria-describedby={keysHintId}
            checked={platformKeys}
            disabled={open}
            onCheckedChange={setPlatformKeys}
          />
          <span className="gx-admin__hint" id={keysHintId}>
            {t(open ? "access.keysOpenMode" : "access.mayUseKeysHint")}
          </span>
        </div>
        <Input
          label={t("access.linkPin")}
          hint={t("access.linkPinHint")}
          error={pinProblem ? t(SNOWFLAKE_ERRORS[pinProblem]) : null}
          inputMode="numeric"
          autoComplete="off"
          value={pin}
          onChange={(e) => setPin(e.target.value)}
        />
        {create.isError && (
          <Alert tone="danger" title={t("access.couldntCreateLink")}>
            {errorMessage(create.error)}
          </Alert>
        )}
      </form>
    </Dialog>
  );
}
