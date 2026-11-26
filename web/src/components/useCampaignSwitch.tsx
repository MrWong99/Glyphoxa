import { useCallback, useState, useSyncExternalStore, type ReactNode } from "react";
import { createClient } from "@connectrpc/connect";
import { useMutation, useTransport } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";

import { CampaignService } from "@gen/glyphoxa/management/v1/management_pb";
import { ConfirmDialog } from "@/components/ui/ConfirmDialog";
import { useI18n } from "@/i18n";
import { invalidateActiveCampaignScopedQueries } from "@/lib/campaignCache";
import { errorMessage } from "@/lib/connectError";
import { hasUnsavedEdits } from "@/lib/navigationGuard";
import { flushPendingWrites, hasPendingWrites } from "@/lib/pendingWrites";

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
//   2. sends any write a screen is holding back (lib/pendingWrites: a
//      dismissal in its Undo window) while the old campaign is still active,
//      then writes the durable selection and runs the Active-Campaign cache
//      sweep;
//   3. says what happened: "Now working in {name}", or — because resolution is
//      live-first (#222) and a running Voice Session's campaign still wins —
//      that the switch takes effect when the session ends.
//
// Every flow that ends in "this campaign is now the active one" goes through
// here: a pick in the topbar, Open on the Campaigns page, a created campaign, an
// imported one, "Switch back". Create and import bring their own wording
// (`announce`) and their own failure handling (`onError`), but not their own
// SetActiveCampaign.
//
// The dialog is returned for the caller to render once, like useDirtyGuard's.

type Target = { id: string; name: string };

export type SwitchResult = {
  campaign: Target;
  /** A live Voice Session holds the Active Campaign: the switch is stored, not yet in effect. */
  deferred: boolean;
};

export type SwitchOptions = {
  /**
   * Runs after the selection is written — whether it applies now or is
   * deferred by a live Voice Session (`deferred`). Close a menu, navigate.
   */
  onSwitched?: (result: SwitchResult) => void;
  /**
   * Says what happened INSTEAD of the default toast ("Now working in {name}",
   * or the deferred notice). For a flow with a better sentence: "Created …".
   */
  announce?: (result: SwitchResult) => void;
  /**
   * Handles a refused switch INSTEAD of the default "Couldn't switch campaign"
   * toast. For a flow that has to say more: "created it, but couldn't switch".
   */
  onError?: (error: unknown) => void;
};

// ── "This tab did it" flag ─────────────────────────────────────────────────
// The Active Campaign can also change WITHOUT this tab: another tab, another
// device, a Discord slash command. To tell the two apart, the id this tab last
// asked for is remembered at module level (a hook's state would be per mounted
// caller, and the switch can be issued from the topbar, the Campaigns page or
// the palette). An observer comparing getActiveCampaign against this can raise
// "changed elsewhere" only for changes it did not initiate.
//
// The flag is STICKY: it names the last campaign this tab asked for until the
// tab asks for another one, and nothing clears it on a timer, on logout or when
// the change has been observed. So it answers "did this tab ask for X?", not
// "is this particular change to X ours?" — a later switch to that same id made
// elsewhere still matches. An observer should consult it when the Active
// Campaign CHANGES, and treat a match as "probably ours". A switch the server
// refuses takes its mark back (see commit below).

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

// ── "This tab is about to change it, to whatever comes next" ───────────────
// Archiving or deleting the Active Campaign changes it too, but not TO a
// campaign this tab named: the server falls back to the next one. The tab
// announces that here, and an observer treats any change inside the window as
// local. A window, not a flag: the change is observed through a refetch that
// lands some time after the write, and a flag nobody clears would swallow a
// real change made elsewhere an hour later.
const LOCAL_CHANGE_WINDOW_MS = 15_000;
let localChangeUntil = 0;

/** Announces that this tab is doing something that moves the Active Campaign. */
export function noteLocalActiveCampaignChange(): void {
  localChangeUntil = Date.now() + LOCAL_CHANGE_WINDOW_MS;
}

/**
 * Closes the window again: the action that opened it was refused, so nothing
 * moved, and a change seen now was made somewhere else.
 */
export function endLocalActiveCampaignChange(): void {
  localChangeUntil = 0;
}

/** True while a change of the Active Campaign is explained by this tab's own action. */
export function expectsLocalActiveCampaignChange(): boolean {
  return Date.now() < localChangeUntil;
}

// ── The switch a live Voice Session is holding back ────────────────────────
// A switch made while a session is live is stored and takes effect when the
// session ends. Until then nothing on screen showed which campaign is waiting:
// the toast is gone after a few seconds and the topbar still (correctly) names
// the session's campaign. The pending target is kept here so the topbar can say
// "Next: {name}" for as long as it is true. Module-level for the same reason as
// the flag above: any caller of the hook may have issued the switch.
//
// It is this tab's knowledge only — GetActiveCampaign returns the RESOLVED
// campaign, not the stored selection — so a reload forgets it.

let pendingSwitch: Target | null = null;
const pendingListeners = new Set<() => void>();

function setPendingSwitch(next: Target | null): void {
  if (pendingSwitch?.id === next?.id && pendingSwitch?.name === next?.name) return;
  pendingSwitch = next;
  for (const listener of pendingListeners) listener();
}

function subscribePendingSwitch(listener: () => void): () => void {
  pendingListeners.add(listener);
  return () => {
    pendingListeners.delete(listener);
  };
}

/** The campaign that becomes active when the live Voice Session ends, if a switch is waiting. */
export function usePendingCampaignSwitch(): Target | null {
  return useSyncExternalStore(
    subscribePendingSwitch,
    () => pendingSwitch,
    () => null,
  );
}

/** Forgets the waiting switch: the session ended, or the campaign became active. */
export function clearPendingCampaignSwitch(): void {
  setPendingSwitch(null);
}

/** Forgets everything this module remembers. Nothing in the app calls this today; tests do. */
export function resetLocalCampaignSwitch(): void {
  localSwitchTarget = null;
  localChangeUntil = 0;
  setPendingSwitch(null);
}

// ── Navigating after a switch ──────────────────────────────────────────────
// A flow that ends on the new campaign's Overview (create, import) navigates
// AFTER the switch. If the GM had unsaved edits and chose to discard them, the
// editor holding them is still mounted for a moment — it goes when the sweep's
// refetch remounts the Campaign screen on the new id — and a navigation in that
// moment would make the route guard ask "Discard unsaved changes?" a second
// time. This runs `action` once no editor reports unsaved edits any more,
// checking on a few successive ticks; if one still does after that (an editor
// that is not per-campaign, on Setup), it runs anyway and the route guard asks,
// which is then the right question.
const SETTLE_CHECKS_MS = [0, 30, 80, 160, 320];

export function afterEditorsSettle(action: () => void): void {
  let attempt = 0;
  const check = () => {
    if (!hasUnsavedEdits() || attempt >= SETTLE_CHECKS_MS.length) {
      action();
      return;
    }
    setTimeout(check, SETTLE_CHECKS_MS[attempt++]);
  };
  check();
}

export function useCampaignSwitch(defaults: SwitchOptions = {}): {
  /** Switches to `campaign`, asking first if edits would be lost. */
  switchTo: (campaign: Target, opts?: SwitchOptions) => void;
  /**
   * Switches to `campaign` WITHOUT asking. For a flow that asked already
   * (confirmDiscard) before doing the work that led here.
   */
  activate: (campaign: Target, opts?: SwitchOptions) => void;
  /**
   * Runs `action` at once when nothing is unsaved; otherwise asks the same
   * "Discard unsaved changes?" question and runs it only on confirm. For a flow
   * that ENDS in a switch but has work to do first (create a campaign).
   */
  confirmDiscard: (action: () => void) => void;
  /**
   * Runs `write` — something that creates or restores a campaign: an import, a
   * duplicate, an unarchive — and keeps the Active Campaign where it was.
   *
   * Without a durable selection the server resolves to the most recently
   * created campaign that is not archived, so a campaign that becomes the
   * newest one would become active by itself: the GM's next write would land
   * in it, and the topbar would blame "another tab or Discord". When the
   * resolved campaign moved across `write`, the previous one is selected again
   * (and that selection is durable from then on).
   */
  keepActive: <T>(write: () => Promise<T>) => Promise<T>;
  /**
   * Makes `campaign` — or, when none is given, whatever resolves right now —
   * the durable selection, without a word. For the flows that move the Active
   * Campaign to "whatever comes next" (archiving or deleting it): the server
   * clears the selection then, and an implicit fallback would let the next
   * import or duplicate take over. Resolves to the campaign selected, or
   * undefined when nothing resolves or the write was refused.
   */
  pinActive: (campaign?: Target) => Promise<Target | undefined>;
  /** True while the selection is being written. */
  pending: boolean;
  /** Render this once — it is the "Discard unsaved changes?" question. */
  dialog: ReactNode;
} {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const [asking, setAsking] = useState<(() => void) | null>(null);

  const setActive = useMutation(CampaignService.method.setActiveCampaign);
  const { mutateAsync } = setActive;
  const defaultOnSwitched = defaults.onSwitched;
  const defaultAnnounce = defaults.announce;
  const defaultOnError = defaults.onError;

  const activate = useCallback(
    (campaign: Target, opts?: SwitchOptions) => {
      // Marked BEFORE the write: the sweep's refetch can land before the
      // response is handled, and an observer must already see this change as
      // ours.
      const before = localSwitchTarget;
      // A switch to another campaign while one was waiting as "Next:" — the
      // session's own campaign, in practice — takes the waiting one back.
      const waiting = pendingSwitch;
      markLocalCampaignSwitch(campaign.id);
      // A write a screen is holding back (a dismissal in its Undo window)
      // carries no campaign id: it is sent while the OLD campaign is still
      // active, and the switch is written once it has settled.
      // mutateAsync, not mutate with callbacks: those are dropped when the
      // calling component unmounts mid-flight (a dialog that closes), and the
      // sweep and the "what happened" must not depend on who is still mounted.
      const write = () =>
        mutateAsync({ campaignId: campaign.id }).then(
          (res) => {
            void invalidateActiveCampaignScopedQueries(queryClient);
            // The response carries the RESOLVED Active Campaign. While a Voice
            // Session is live that is still the session's campaign, not the
            // one just selected — the switch is stored, and comes due when the
            // session ends (watchVoiceSessionEnd re-runs the sweep then).
            const resolved = res.campaign?.id;
            const deferred = resolved !== undefined && resolved !== "" && resolved !== campaign.id;
            setPendingSwitch(deferred ? campaign : null);
            const result: SwitchResult = { campaign, deferred };
            const announce = opts?.announce ?? defaultAnnounce;
            if (announce) announce(result);
            else if (deferred) toast.info(t("shell.switchDeferred", { name: campaign.name }));
            else if (waiting && waiting.id !== campaign.id)
              toast.success(t("campaigns.nextCancelled", { name: campaign.name }));
            else toast.success(t("shell.nowWorkingIn", { name: campaign.name }));
            (opts?.onSwitched ?? defaultOnSwitched)?.(result);
          },
          (err: unknown) => {
            // The switch was refused (or never confirmed), so as far as this
            // tab knows the campaign did not become active through it: put the
            // flag back to what it said before — unless a newer switch has
            // claimed it meanwhile.
            if (localSwitchTarget === campaign.id) localSwitchTarget = before;
            const onError = opts?.onError ?? defaultOnError;
            if (onError) onError(err);
            else toast.error(t("components.couldntSwitchCampaign", { message: errorMessage(err) }));
          },
        );
      if (hasPendingWrites()) void flushPendingWrites().then(write);
      else void write();
    },
    [mutateAsync, queryClient, t, defaultOnSwitched, defaultAnnounce, defaultOnError],
  );

  const confirmDiscard = useCallback((action: () => void) => {
    if (hasUnsavedEdits()) setAsking(() => action);
    else action();
  }, []);

  const switchTo = useCallback(
    (campaign: Target, opts?: SwitchOptions) => confirmDiscard(() => activate(campaign, opts)),
    [confirmDiscard, activate],
  );

  // The resolved Active Campaign, read now rather than from the cache (which
  // can be a refetch behind); undefined when nothing resolves or the read fails.
  const transport = useTransport();
  const readActive = useCallback(
    () =>
      createClient(CampaignService, transport)
        .getActiveCampaign({})
        .then(
          (res) => res.campaign,
          () => undefined,
        ),
    [transport],
  );

  // Writes `campaign` as the durable selection, quietly and awaited: nothing
  // is said, and the caller's own refresh reads the selection back. The id is
  // marked as this tab's switch first, like every other switch.
  const select = useCallback(
    async (campaign: Target): Promise<boolean> => {
      const before = localSwitchTarget;
      markLocalCampaignSwitch(campaign.id);
      try {
        await mutateAsync({ campaignId: campaign.id });
        return true;
      } catch {
        if (localSwitchTarget === campaign.id) localSwitchTarget = before;
        return false;
      }
    },
    [mutateAsync],
  );

  const pinActive = useCallback(
    async (campaign?: Target): Promise<Target | undefined> => {
      const target = campaign ?? (await readActive());
      if (!target) return undefined;
      return (await select({ id: target.id, name: target.name })) ? { id: target.id, name: target.name } : undefined;
    },
    [readActive, select],
  );

  const keepActive = useCallback(
    async <T,>(write: () => Promise<T>): Promise<T> => {
      const before = await readActive();
      noteLocalActiveCampaignChange();
      let result: T;
      try {
        result = await write();
      } catch (err) {
        endLocalActiveCampaignChange();
        throw err;
      }
      const after = await readActive();
      if (before && after && after.id !== before.id) {
        await select({ id: before.id, name: before.name });
        // A refetch may have shown the newcomer in the meantime.
        void invalidateActiveCampaignScopedQueries(queryClient);
      }
      return result;
    },
    [readActive, select, queryClient],
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
        const action = asking;
        setAsking(null);
        action();
      }}
    />
  ) : null;

  return { switchTo, activate, confirmDiscard, pinActive, keepActive, pending: setActive.isPending, dialog };
}
