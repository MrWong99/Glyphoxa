import { useCallback, useState, type ReactNode } from "react";
import { useMutation } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";

import { CampaignService } from "@gen/glyphoxa/management/v1/management_pb";
import { ConfirmDialog } from "@/components/ui/ConfirmDialog";
import { useI18n } from "@/i18n";
import { invalidateActiveCampaignScopedQueries } from "@/lib/campaignCache";
import { errorMessage } from "@/lib/connectError";
import { hasUnsavedEdits } from "@/lib/navigationGuard";

// useCampaignSwitch is the ONE way the web console changes the Active Campaign
// by hand (ADR-0063 amendment). The Active Campaign is ambient server state
// (ADR-0039): no request carries the campaign id, so a switch silently
// retargets every write on screen, and the Campaign screen remounts on the new
// id — dropping whatever was typed and not saved. Before this hook a switch was
// a bare SetActiveCampaign from the topbar that did exactly that, without a
// word. It now:
//
//   1. asks first while any editor reports unsaved edits (the same register the
//      route guard reads — lib/navigationGuard);
//   2. writes the durable selection and runs the Active-Campaign cache sweep;
//   3. says what happened: "Now working in {name}", or — because resolution is
//      live-first (#222) and a running Voice Session's campaign still wins —
//      that the switch takes effect when the session ends.
//
// The dialog is returned for the caller to render once, like useDirtyGuard's.

type Target = { id: string; name: string };

type SwitchOptions = {
  /**
   * Runs after the selection is written — whether it applies now or is
   * deferred by a live Voice Session (`deferred`). Close a menu, navigate.
   */
  onSwitched?: (result: { campaign: Target; deferred: boolean }) => void;
};

// ── "This tab did it" flag ─────────────────────────────────────────────────
// The Active Campaign can also change WITHOUT this tab: another tab, another
// device, a Discord slash command. To tell the two apart, the id this tab last
// asked for is remembered at module level (a hook's state would be per mounted
// caller, and the switch can be issued from the topbar, the Campaigns page or
// the palette). An observer comparing getActiveCampaign against this can raise
// "changed elsewhere" only for changes it did not initiate.

let localSwitchTarget: string | null = null;

/** Records that THIS tab asked for `campaignId` to become the Active Campaign. */
export function markLocalCampaignSwitch(campaignId: string): void {
  localSwitchTarget = campaignId;
}

/**
 * The campaign id this tab last asked to make active — through this hook, or
 * through a create/import flow that called markLocalCampaignSwitch — or null
 * if it never has.
 */
export function lastLocalCampaignSwitch(): string | null {
  return localSwitchTarget;
}

/** True when `campaignId` becoming active is this tab's own doing. */
export function isLocalCampaignSwitch(campaignId: string): boolean {
  return localSwitchTarget === campaignId;
}

/** Forgets the flag (tests; and a logout). */
export function resetLocalCampaignSwitch(): void {
  localSwitchTarget = null;
}

export function useCampaignSwitch(defaults: SwitchOptions = {}): {
  /** Switches to `campaign`, asking first if edits would be lost. */
  switchTo: (campaign: Target, opts?: SwitchOptions) => void;
  /** True while the selection is being written. */
  pending: boolean;
  /** Render this once — it is the "Discard unsaved changes?" question. */
  dialog: ReactNode;
} {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const [asking, setAsking] = useState<{ campaign: Target; opts?: SwitchOptions } | null>(null);

  const setActive = useMutation(CampaignService.method.setActiveCampaign);
  const { mutate } = setActive;
  const defaultOnSwitched = defaults.onSwitched;

  const commit = useCallback(
    (campaign: Target, opts?: SwitchOptions) => {
      // Marked BEFORE the write: the sweep's refetch can land before onSuccess
      // runs, and an observer must already see this change as ours.
      markLocalCampaignSwitch(campaign.id);
      mutate(
        { campaignId: campaign.id },
        {
          onSuccess: (res) => {
            void invalidateActiveCampaignScopedQueries(queryClient);
            // The response carries the RESOLVED Active Campaign. While a Voice
            // Session is live that is still the session's campaign, not the
            // one just selected — the switch is stored, and comes due when the
            // session ends (watchVoiceSessionEnd re-runs the sweep then).
            const resolved = res.campaign?.id;
            const deferred = resolved !== undefined && resolved !== "" && resolved !== campaign.id;
            if (deferred) toast.info(t("shell.switchDeferred", { name: campaign.name }));
            else toast.success(t("shell.nowWorkingIn", { name: campaign.name }));
            (opts?.onSwitched ?? defaultOnSwitched)?.({ campaign, deferred });
          },
          onError: (err) =>
            toast.error(t("components.couldntSwitchCampaign", { message: errorMessage(err) })),
        },
      );
    },
    [mutate, queryClient, t, defaultOnSwitched],
  );

  const switchTo = useCallback(
    (campaign: Target, opts?: SwitchOptions) => {
      if (hasUnsavedEdits()) setAsking({ campaign, opts });
      else commit(campaign, opts);
    },
    [commit],
  );

  const dialog = asking ? (
    <ConfirmDialog
      open
      onOpenChange={(open) => {
        if (!open) setAsking(null);
      }}
      title={t("common.discardChangesTitle")}
      description={t("common.discardChangesDesc")}
      confirmLabel={t("common.discardChangesConfirm")}
      onConfirm={() => {
        const { campaign, opts } = asking;
        setAsking(null);
        commit(campaign, opts);
      }}
    />
  ) : null;

  return { switchTo, pending: setActive.isPending, dialog };
}
