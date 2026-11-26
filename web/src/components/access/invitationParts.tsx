import { useState, type ReactNode } from "react";
import { timestampDate, timestampMs } from "@bufbuild/protobuf/wkt";

import type { Invitation } from "@gen/glyphoxa/management/v1/management_pb";
import { Alert } from "@/components/ui/Alert";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { ConfirmDialog } from "@/components/ui/ConfirmDialog";
import { CopyButton } from "@/components/ui/CopyButton";
import { Dialog, DialogClose } from "@/components/ui/Dialog";
import { Input } from "@/components/ui/Input";
import { useI18n, type Lang, type MessageKey, type TFunc } from "@/i18n";
import { snowflakeProblem, type SnowflakeProblem } from "@/lib/invitationLink";

import "./access.css";

// What the two kinds of invitation link (ADR-0065) show the same way, wherever
// they are minted — an access invitation on the Admin screen, a campaign
// invitation in a Campaign's Sharing view:
//
//   - the list: a link is identified by its label, its limits and its state,
//     never by its token (the server keeps only a hash);
//   - the limits a link can be given (uses, expiry, one pinned Discord User);
//   - the one showing of the full link, right after it is minted.
//
// The RPCs differ by kind, so none lives here: each caller mints and revokes
// through its own service and hands the results in.

export const SNOWFLAKE_ERRORS: Record<SnowflakeProblem, MessageKey> = {
  digits: "access.discordUserIdDigits",
  length: "access.discordUserIdLength",
};

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

/**
 * The links as rows, each with its state and — while it still works — Revoke
 * behind a confirmation. `grants` says what a link gives (the Platform Keys
 * badge of an access invitation, the Guest Rights of a campaign invitation).
 */
export function InvitationList({
  invitations,
  grants,
  revokeDescription,
  revoking,
  onRevoke,
}: {
  invitations: readonly Invitation[];
  grants?: (inv: Invitation) => ReactNode;
  /** What revoking does, in the confirmation. */
  revokeDescription: string;
  /** A revoke is in flight: the Revoke buttons wait. */
  revoking: boolean;
  onRevoke: (inv: Invitation) => void;
}) {
  const { t, lang } = useI18n();
  const [asking, setAsking] = useState<Invitation | null>(null);

  return (
    <>
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
                {grants?.(inv)}
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
                    disabled={revoking}
                    onClick={() => setAsking(inv)}
                  >
                    {t("access.revokeLink")}
                  </Button>
                </div>
              )}
            </li>
          );
        })}
      </ul>

      <ConfirmDialog
        open={asking !== null}
        onOpenChange={(next) => {
          if (!next) setAsking(null);
        }}
        title={t("access.revokeLinkTitle")}
        description={revokeDescription}
        confirmLabel={t("access.revokeLink")}
        onConfirm={() => {
          const inv = asking;
          setAsking(null);
          if (inv) onRevoke(inv);
        }}
      />
    </>
  );
}

/**
 * A field's text as a count: 0 for blank ("unlimited" / "never" — the wire's
 * own zero), NaN when it is not a whole number within [1, max]. Text, not
 * type="number": a number field reports an entry it cannot parse as "", which
 * would read as "unlimited" and mint a link wider than its minter typed.
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

/** What a link's limit fields hold, and what they come to on the wire. */
export type LinkLimits = {
  label: string;
  setLabel: (v: string) => void;
  maxUses: string;
  setMaxUses: (v: string) => void;
  expiry: string;
  setExpiry: (v: string) => void;
  pin: string;
  setPin: (v: string) => void;
  /** False while a field holds something that cannot be sent. */
  valid: boolean;
  /** The request fields both Create*Invitation RPCs share. */
  request: { label: string; maxUses: number; expiresInDays: number; discordUserId: string };
};

/** The state behind LinkLimitFields: label, max uses, expiry and the pinned Discord User. */
export function useLinkLimits(): LinkLimits {
  const [label, setLabel] = useState("");
  const [maxUses, setMaxUses] = useState("");
  const [expiry, setExpiry] = useState("");
  const [pin, setPin] = useState("");
  const maxUsesV = parseCount(maxUses, MAX_USES);
  const expiryV = parseCount(expiry, MAX_EXPIRY_DAYS);
  return {
    label,
    setLabel,
    maxUses,
    setMaxUses,
    expiry,
    setExpiry,
    pin,
    setPin,
    valid: !Number.isNaN(maxUsesV) && !Number.isNaN(expiryV) && snowflakeProblem(pin) === null,
    request: { label: label.trim(), maxUses: maxUsesV, expiresInDays: expiryV, discordUserId: pin.trim() },
  };
}

/** The label of a new link — the first field of its dialog. */
export function LinkLabelField({ limits }: { limits: LinkLimits }) {
  const { t } = useI18n();
  return (
    <Input
      label={t("access.linkLabel")}
      hint={t("access.linkLabelHint")}
      autoComplete="off"
      maxLength={200}
      value={limits.label}
      onChange={(e) => limits.setLabel(e.target.value)}
    />
  );
}

/** Max uses and expiry, side by side. Blank means unlimited / never. */
export function LinkUseFields({ limits }: { limits: LinkLimits }) {
  const { t } = useI18n();
  return (
    <div className="gx-admin__pair">
      <Input
        label={t("access.linkMaxUses")}
        hint={t("access.linkMaxUsesHint")}
        error={Number.isNaN(limits.request.maxUses) ? t("access.linkMaxUsesInvalid") : null}
        placeholder={t("access.unlimited")}
        inputMode="numeric"
        autoComplete="off"
        value={limits.maxUses}
        onChange={(e) => limits.setMaxUses(e.target.value)}
      />
      <Input
        label={t("access.linkExpiry")}
        hint={t("access.linkExpiryHint")}
        error={Number.isNaN(limits.request.expiresInDays) ? t("access.linkExpiryInvalid") : null}
        placeholder={t("access.neverPlaceholder")}
        inputMode="numeric"
        autoComplete="off"
        value={limits.expiry}
        onChange={(e) => limits.setExpiry(e.target.value)}
      />
    </div>
  );
}

/** The optional Discord User a link is pinned to. */
export function LinkPinField({ limits }: { limits: LinkLimits }) {
  const { t } = useI18n();
  const problem = snowflakeProblem(limits.pin);
  return (
    <Input
      label={t("access.linkPin")}
      hint={t("access.linkPinHint")}
      error={problem ? t(SNOWFLAKE_ERRORS[problem]) : null}
      inputMode="numeric"
      autoComplete="off"
      value={limits.pin}
      onChange={(e) => limits.setPin(e.target.value)}
    />
  );
}

/**
 * The one showing of a freshly minted link: the full link, a copy button and
 * the plain statement that it will not be shown again. The link lives in the
 * caller's dialog state and nowhere else; closing unmounts it.
 */
export function LinkCreatedDialog({
  link,
  onOpenChange,
}: {
  link: string;
  onOpenChange: (open: boolean) => void;
}) {
  const { t } = useI18n();
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
