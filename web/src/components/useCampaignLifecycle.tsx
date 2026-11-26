import { createElement, useCallback, useState, type ReactNode } from "react";
import { useMutation } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { timestampMs } from "@bufbuild/protobuf/wkt";
import { toast } from "sonner";
import { Download } from "lucide-react";

import { CampaignService, SessionService, type Campaign } from "@gen/glyphoxa/management/v1/management_pb";
import { CampaignExportDialog } from "@/components/CampaignExportDialog";
import { CampaignSettingsDialog } from "@/components/CampaignSettingsDialog";
import { ConfirmDialog } from "@/components/ui/ConfirmDialog";
import { toastUndo } from "@/components/ui/toast";
import {
  endLocalActiveCampaignChange,
  noteLocalActiveCampaignChange,
  useCampaignSwitch,
} from "@/components/useCampaignSwitch";
import { WorkingButton } from "@/components/WorkingButton";
import { useI18n } from "@/i18n";
import {
  invalidateActiveCampaignScopedQueries,
  resetActiveCampaignScopedQueries,
} from "@/lib/campaignCache";
import { errorMessage, failedPreconditionMessage } from "@/lib/connectError";
import {
  downloadBlob,
  fetchCampaignExport,
  importCampaignBundle,
  isBundleBusy,
  isBundleTooLarge,
} from "@/lib/download";
import { invalidateMethodQueries } from "@/lib/queryClient";

import "./campaignRowActions.css";

// useCampaignLifecycle — what can be done TO a campaign, in one place: its
// settings, export, duplicate, archive / unarchive and delete, with the dialogs
// and the feedback each needs. The Campaigns page uses it for every card's
// menu, the campaign's Settings view for its Data and Danger zone sections.
//
// One instance per page, not one per card: a card that is archived moves to
// another section (and one that is deleted disappears) while its own flow is
// still running, and a flow that lived in the card would be unmounted with it.
//
// The rules it carries:
//
//   Archive    is immediate and undoable ("Archived {name}" · Undo) — except on
//              the Active Campaign, where it asks first and names the campaign
//              that becomes active instead. Undo there also re-selects it.
//   Delete     is behind the re-typed name whatever state the campaign is in.
//              The server only deletes an ARCHIVED campaign, so on a campaign
//              that is not archived yet it archives first, then deletes; if the
//              delete then fails, it says the campaign is archived, not gone.
//   Duplicate  is an export piped into an import, then a rename to
//              "{name} (copy)". It copies the map images and leaves the session
//              history behind (said in the confirm); if the bundle is too large
//              to upload it retries without the images and says so.
//   The live Voice Session's campaign can be neither archived nor deleted — the
//   server refuses (FailedPrecondition), and the refusal is shown as what it is.
//
// Every change refreshes the campaign lists (campaign-invariant, so the sweep
// skips them) and runs the Active-Campaign sweep, so a screen scoped to a
// campaign that just went away follows resolution without a reload.
//
// And none of them leaves the Active Campaign to chance. Archiving or deleting
// it clears the durable selection on the server, and without one the server
// resolves to the most recently created campaign that is not archived — so the
// next import, duplicate or unarchive of a newer campaign would quietly become
// the Active Campaign. Archive and Delete therefore select the campaign the
// confirm named; Duplicate and Unarchive keep the Active Campaign where it was
// (useCampaignSwitch's pinActive / keepActive).

type Target = { id: string; name: string };

type OpenDialog =
  | { kind: "settings"; campaign: Campaign }
  | { kind: "export"; campaign: Campaign }
  | { kind: "duplicate"; campaign: Campaign }
  | { kind: "archive"; campaign: Campaign }
  | { kind: "delete"; campaign: Campaign };

const createdMs = (c: Campaign) => (c.createdAt ? timestampMs(c.createdAt) : 0);

/**
 * The campaign that becomes active once `removedId` is archived or deleted:
 * the server's fallback (storage.GetActiveCampaignInTenant) — the most recently
 * created campaign that is not archived, ties broken by id, descending.
 * Archiving clears the durable selection that pointed at the campaign, so the
 * fallback is what resolves next.
 */
export function nextActiveCampaign(
  campaigns: readonly Campaign[],
  removedId: string,
): Campaign | undefined {
  return campaigns
    .filter((c) => c.id !== removedId && c.archivedAt === undefined)
    .sort((a, b) => createdMs(b) - createdMs(a) || (a.id < b.id ? 1 : a.id > b.id ? -1 : 0))[0];
}

// The label is a component so it follows the display language even if that
// changes while the toast is up (the pattern toastUndo uses for "Undo").
function OpenLabel() {
  return useI18n().t("shell.campaignOpen");
}

export type CampaignLifecycle = {
  openSettings: (campaign: Campaign) => void;
  exportBundle: (campaign: Campaign) => void;
  duplicate: (campaign: Campaign) => void;
  archive: (campaign: Campaign) => void;
  unarchive: (campaign: Campaign) => void;
  remove: (campaign: Campaign) => void;
  /** Ids of the campaigns with a flow in flight (archive, delete, duplicate). */
  busy: ReadonlySet<string>;
  /** False until the Active Campaign is known: Archive and Delete wait for it. */
  ready: boolean;
  /** Render this once — every dialog the actions above can open. */
  dialogs: ReactNode;
};

export function useCampaignLifecycle({
  activeId,
  activeSettled = true,
  campaigns,
  onOpen,
  onActiveRemoved,
}: {
  /** The resolved Active Campaign's id. */
  activeId: string | undefined;
  /**
   * False while the Active Campaign is still being read. Archive and Delete
   * wait for it: whether they ask first, and which campaign they name, depends
   * on it.
   */
  activeSettled?: boolean;
  /** The campaigns that are not archived, once known: names what becomes active after an archive. */
  campaigns: readonly Campaign[] | undefined;
  /** Opens a campaign (switch, then its Overview): the action on "Duplicated as …". */
  onOpen: (campaign: Target) => void;
  /** The Active Campaign was just archived or deleted from here. */
  onActiveRemoved?: () => void;
}): CampaignLifecycle {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const [dialog, setDialog] = useState<OpenDialog | null>(null);
  const [busy, setBusy] = useState<ReadonlySet<string>>(() => new Set());
  const campaignSwitch = useCampaignSwitch();

  const { mutateAsync: archiveAsync } = useMutation(CampaignService.method.archiveCampaign);
  const { mutateAsync: unarchiveAsync } = useMutation(CampaignService.method.unarchiveCampaign);
  const { mutateAsync: deleteAsync } = useMutation(CampaignService.method.deleteCampaign);
  const { mutateAsync: updateAsync } = useMutation(CampaignService.method.updateCampaign);

  const markBusy = useCallback((id: string, on: boolean) => {
    setBusy((prev) => {
      const next = new Set(prev);
      if (on) next.add(id);
      else next.delete(id);
      return next;
    });
  }, []);

  // The lists across every include_archived input (the key is built without an
  // input, so it prefix-matches them all), plus the Active-Campaign sweep —
  // as a RESET when the Active Campaign went away and nothing took its place,
  // so nothing keeps showing a campaign that no longer resolves (see
  // campaignCache). When another campaign took its place, its reads replace
  // the old ones like after any switch, and the rest of the screen (and an
  // open form) is left alone.
  const refresh = useCallback(
    (nothingResolves = false) => {
      void invalidateMethodQueries(queryClient, CampaignService.method.listCampaigns);
      if (nothingResolves) void resetActiveCampaignScopedQueries(queryClient);
      else void invalidateActiveCampaignScopedQueries(queryClient);
    },
    [queryClient],
  );

  // After this tab archived or deleted the Active Campaign: select the campaign
  // the confirm named, or — when the list was not in hand — whatever resolves
  // now. False when nothing is left to select.
  const pinNext = async (removedId: string): Promise<boolean> => {
    const next = campaigns ? nextActiveCampaign(campaigns, removedId) : undefined;
    const pinned = await campaignSwitch.pinActive(next && { id: next.id, name: next.name });
    return pinned !== undefined;
  };

  // The one refusal both Archive and Delete can get that is not a failure: the
  // campaign backs the live Voice Session. (Delete's other FailedPrecondition,
  // "not archived yet", cannot happen here — the flow archives first.) Said in
  // the GM's language, and the live marker is refreshed so the screen agrees.
  const refusal = useCallback(
    (err: unknown, campaign: Target, fallback: "components.couldntArchiveCampaign" | "components.couldntDeleteCampaign") => {
      if (failedPreconditionMessage(err) !== null) {
        void invalidateMethodQueries(queryClient, SessionService.method.getSession);
        return t("campaigns.liveRefusal", { name: campaign.name });
      }
      return t(fallback, { message: errorMessage(err) });
    },
    [queryClient, t],
  );

  // ── Archive / unarchive ──────────────────────────────────────────────────

  const undoArchive = async (campaign: Campaign, wasActive: boolean) => {
    try {
      // When it was the Active Campaign it is selected again below; otherwise
      // a restored campaign must not take the Active Campaign's place.
      if (wasActive) await unarchiveAsync({ id: campaign.id });
      else await campaignSwitch.keepActive(() => unarchiveAsync({ id: campaign.id }));
    } catch (err) {
      toast.error(t("components.couldntUnarchiveCampaign", { message: errorMessage(err) }));
      return;
    }
    refresh();
    if (!wasActive) {
      toast.success(t("campaigns.restored", { name: campaign.name }));
      return;
    }
    // Archiving cleared the selection that pointed at it; an Undo that left
    // another campaign active would only half undo.
    campaignSwitch.activate(
      { id: campaign.id, name: campaign.name },
      {
        announce: ({ deferred }) =>
          toast.success(
            t(deferred ? "campaigns.restored" : "campaigns.restoredActive", { name: campaign.name }),
          ),
      },
    );
  };

  const runArchive = async (campaign: Campaign) => {
    const wasActive = campaign.id === activeId;
    // The Active Campaign is about to change to "whatever resolves next": that
    // is this tab's doing, not something that happened elsewhere.
    if (wasActive) noteLocalActiveCampaignChange();
    markBusy(campaign.id, true);
    let nothingResolves = false;
    try {
      await archiveAsync({ id: campaign.id });
      if (wasActive) nothingResolves = !(await pinNext(campaign.id));
    } catch (err) {
      // Refused: nothing moved, so a change seen now happened elsewhere.
      if (wasActive) endLocalActiveCampaignChange();
      toast.error(refusal(err, campaign, "components.couldntArchiveCampaign"));
      return;
    } finally {
      markBusy(campaign.id, false);
    }
    refresh(nothingResolves);
    toastUndo(t("campaigns.archived", { name: campaign.name }), () => void undoArchive(campaign, wasActive));
    if (wasActive) onActiveRemoved?.();
  };

  const archive = (campaign: Campaign) => {
    if (!activeSettled) return;
    if (campaign.id === activeId) setDialog({ kind: "archive", campaign });
    else void runArchive(campaign);
  };

  const unarchive = async (campaign: Campaign) => {
    markBusy(campaign.id, true);
    try {
      await campaignSwitch.keepActive(() => unarchiveAsync({ id: campaign.id }));
    } catch (err) {
      toast.error(t("components.couldntUnarchiveCampaign", { message: errorMessage(err) }));
      return;
    } finally {
      markBusy(campaign.id, false);
    }
    refresh();
    toast.success(t("campaigns.restored", { name: campaign.name }));
  };

  // ── Delete ───────────────────────────────────────────────────────────────

  const runDelete = async (campaign: Campaign) => {
    const wasArchived = campaign.archivedAt !== undefined;
    const wasActive = campaign.id === activeId;
    // Set once the campaign is at least archived: from then on it no longer
    // resolves as the Active Campaign, whatever the delete does.
    let activeGone = false;
    let nothingResolves = false;
    markBusy(campaign.id, true);
    try {
      if (!wasArchived) {
        if (wasActive) noteLocalActiveCampaignChange();
        try {
          await archiveAsync({ id: campaign.id });
        } catch (err) {
          if (wasActive) endLocalActiveCampaignChange();
          toast.error(refusal(err, campaign, "components.couldntDeleteCampaign"));
          return;
        }
        activeGone = wasActive;
        if (activeGone) nothingResolves = !(await pinNext(campaign.id));
      }
      try {
        await deleteAsync({ id: campaign.id });
      } catch (err) {
        if (failedPreconditionMessage(err) !== null || wasArchived) {
          toast.error(refusal(err, campaign, "components.couldntDeleteCampaign"));
        } else {
          // It is archived now, and still there: say exactly that.
          toast.error(
            t("campaigns.archivedNotDeleted", { name: campaign.name, message: errorMessage(err) }),
          );
        }
        return;
      }
      toast.success(t("campaigns.deleted", { name: campaign.name }));
    } finally {
      markBusy(campaign.id, false);
      refresh(nothingResolves);
      // Archived counts: from then on it is no longer the Active Campaign,
      // whether or not the delete went through.
      if (activeGone) onActiveRemoved?.();
    }
  };

  const remove = (campaign: Campaign) => {
    if (activeSettled) setDialog({ kind: "delete", campaign });
  };

  // ── Duplicate ────────────────────────────────────────────────────────────

  const runDuplicate = async (campaign: Campaign) => {
    markBusy(campaign.id, true);
    const progress = toast.loading(t("campaigns.duplicating", { name: campaign.name }));
    let copyId: string | undefined;
    try {
      // History off: a copy is a fresh start for another table, not a second
      // set of the same transcripts. Images on: maps without their picture are
      // not a copy of the maps.
      let withImages = true;
      // The copy is the newest campaign: it must not become the Active
      // Campaign by being that (see the header).
      const imported = await campaignSwitch.keepActive(async () => {
        const bundle = await fetchCampaignExport(campaign.id, { includeHistory: false, includeImages: true });
        try {
          return await importCampaignBundle(bundle.blob, bundle.filename);
        } catch (err) {
          if (!isBundleTooLarge(err)) throw err;
          // Over the upload cap: the images are what made it that big.
          withImages = false;
          const smaller = await fetchCampaignExport(campaign.id, { includeHistory: false, includeImages: false });
          return await importCampaignBundle(smaller.blob, smaller.filename);
        }
      });
      copyId = imported.campaignId;
      // The importer keeps the bundle's name; two campaigns called the same
      // would be indistinguishable in every list.
      const copyName = t("campaigns.copyName", { name: campaign.name });
      await updateAsync({
        id: copyId,
        name: copyName,
        system: campaign.system,
        language: campaign.language,
        // A bundle does not carry the Session Highlights tuning; the source row
        // does. Highlight recording itself stays off on the copy: arming it is
        // a consent decision for the table that plays it.
        highlightBar: campaign.highlightBar,
        highlightConfirmWindows: campaign.highlightConfirmWindows,
      });
      const copy = { id: copyId, name: copyName };
      toast.success(
        t(withImages ? "campaigns.duplicated" : "campaigns.duplicatedNoImages", { name: copyName }),
        {
          id: progress,
          // Long enough to read and reach: it carries the only shortcut to the copy.
          duration: 10_000,
          action: { label: createElement(OpenLabel), onClick: () => onOpen(copy) },
        },
      );
    } catch (err) {
      const message = isBundleBusy(err)
        ? t("campaigns.bundleBusy")
        : copyId
          ? // The copy exists, under the original's name.
            t("campaigns.duplicatedNotRenamed", { name: campaign.name, message: errorMessage(err) })
          : t("campaigns.couldntDuplicate", { name: campaign.name, message: errorMessage(err) });
      toast.error(message, { id: progress });
    } finally {
      markBusy(campaign.id, false);
      // Whatever happened, a copy may exist now.
      void invalidateMethodQueries(queryClient, CampaignService.method.listCampaigns);
    }
  };

  // ── Dialogs ──────────────────────────────────────────────────────────────

  const close = () => setDialog(null);
  const onOpenChange = (open: boolean) => {
    if (!open) close();
  };

  // What the archive / delete confirms say about the Active Campaign moving on.
  const nextActiveNote = (campaign: Campaign): string | null => {
    if (campaign.id !== activeId) return null;
    if (!campaigns) return t("campaigns.nextActiveUnknown");
    const next = nextActiveCampaign(campaigns, campaign.id);
    return next ? t("campaigns.nextActive", { name: next.name }) : t("campaigns.nextActiveNone");
  };

  const dialogs = (
    <>
      {dialog?.kind === "settings" && <CampaignSettingsDialog campaign={dialog.campaign} onClose={close} />}
      {dialog?.kind === "export" && <CampaignExportDialog campaign={dialog.campaign} onClose={close} />}
      {dialog?.kind === "archive" && (
        <ConfirmDialog
          open
          onOpenChange={onOpenChange}
          title={t("campaigns.archiveTitle", { name: dialog.campaign.name })}
          description={
            <>
              {t("campaigns.archiveActiveDesc")} {nextActiveNote(dialog.campaign)}
            </>
          }
          confirmLabel={t("components.archive")}
          destructive={false}
          onConfirm={() => void runArchive(dialog.campaign)}
        />
      )}
      {dialog?.kind === "duplicate" && (
        <ConfirmDialog
          open
          onOpenChange={onOpenChange}
          title={t("campaigns.duplicateTitle", { name: dialog.campaign.name })}
          description={t("campaigns.duplicateDesc", {
            copy: t("campaigns.copyName", { name: dialog.campaign.name }),
          })}
          confirmLabel={t("campaigns.duplicate")}
          destructive={false}
          onConfirm={() => void runDuplicate(dialog.campaign)}
        />
      )}
      {dialog?.kind === "delete" && (
        <ConfirmDialog
          open
          onOpenChange={onOpenChange}
          title={t("components.deleteCampaignTitle", { name: dialog.campaign.name })}
          description={
            <>
              {t(
                dialog.campaign.archivedAt === undefined
                  ? "campaigns.deleteDescActive"
                  : "components.deleteCampaignDescription",
              )}{" "}
              {nextActiveNote(dialog.campaign)}
              <BackupFirst campaign={dialog.campaign} />
            </>
          }
          confirmLabel={t("components.deleteCampaignConfirm")}
          confirmText={dialog.campaign.name}
          confirmTextLabel={t("components.typeCampaignNameToConfirm")}
          onConfirm={() => void runDelete(dialog.campaign)}
        />
      )}
      {campaignSwitch.dialog}
    </>
  );

  return {
    openSettings: (campaign) => setDialog({ kind: "settings", campaign }),
    exportBundle: (campaign) => setDialog({ kind: "export", campaign }),
    duplicate: (campaign) => setDialog({ kind: "duplicate", campaign }),
    archive,
    unarchive: (campaign) => void unarchive(campaign),
    remove,
    busy,
    ready: activeSettled,
    dialogs,
  };
}

// "Download full bundle first", inside the delete dialog: the last moment a
// bundle of this campaign can be made is also the moment a GM is most likely to
// want one. A complete bundle — history and map images.
//
// It renders inside the dialog's description (a <p>), so everything here is
// phrasing content: spans and a button.
function BackupFirst({ campaign }: { campaign: Target }) {
  const { t } = useI18n();
  const [state, setState] = useState<
    { kind: "idle" } | { kind: "busy" } | { kind: "done" } | { kind: "failed"; message: string }
  >({ kind: "idle" });

  const run = async () => {
    if (state.kind === "busy") return;
    setState({ kind: "busy" });
    try {
      const { blob, filename } = await fetchCampaignExport(campaign.id);
      downloadBlob(blob, filename);
      setState({ kind: "done" });
    } catch (err) {
      setState({
        kind: "failed",
        message: isBundleBusy(err) ? t("campaigns.bundleBusy") : errorMessage(err),
      });
    }
  };

  return (
    <span className="gx-campaign-delete__backup">
      <WorkingButton
        type="button"
        variant="secondary"
        size="sm"
        iconStart={<Download />}
        working={state.kind === "busy"}
        onClick={() => void run()}
      >
        {state.kind === "busy" ? t("campaigns.exportPacking") : t("campaigns.backupFirst")}
      </WorkingButton>
      {state.kind === "done" && (
        <span className="gx-campaign-delete__backup-note" role="status">
          {t("campaigns.backupDone")}
        </span>
      )}
      {state.kind === "failed" && (
        <span className="gx-campaign-delete__backup-note gx-campaign-delete__backup-note--error" role="alert">
          {t("components.couldntExportCampaign", { message: state.message })}
        </span>
      )}
    </span>
  );
}
