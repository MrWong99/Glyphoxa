import { useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Upload } from "lucide-react";

import { CampaignService } from "@gen/glyphoxa/management/v1/management_pb";
import { useScreenNav } from "@/app/screenNav";
import { importCampaignBundle, isBundleBusy, type ImportSummary } from "@/lib/download";
import { errorMessage } from "@/lib/connectError";
import { invalidateMethodQueries } from "@/lib/queryClient";
import { ConfirmDialog } from "@/components/ui/ConfirmDialog";
import { WorkingButton } from "@/components/WorkingButton";
import { afterEditorsSettle, useCampaignSwitch } from "@/components/useCampaignSwitch";
import { useI18n, type TFunc } from "@/i18n";

import "./importCampaignButton.css";

// droppedRefsNote phrases the dropped-participant-refs caveat (singular/plural).
// Shared by the success dialog note and the unmount-proof warning toast so they
// never drift. Import still SUCCEEDED — this is a caveat, never an error. Takes
// the caller's `t` so the string resolves at render/toast time — never baked
// into module state.
function droppedRefsNote(t: TFunc, n: number): string {
  return n === 1
    ? t("components.importDroppedRefOne")
    : t("components.importDroppedRefMany", { n });
}

// ImportCampaignButton — "Import bundle" on the Campaigns page (#294). It
// uploads a Campaign Bundle (the export's counterpart) over the plain-net/http
// importer, which mints a NEW campaign and does NOT auto-activate it (ADR-0053
// d7). So the flow is: pick a file → confirm (nothing is overwritten, but an
// upload is deliberate) → pending upload → a success prompt that reports the
// imported name and offers an EXPLICIT switch. Declining leaves the imported
// campaign in the list, unselected.
//
// listCampaigns is campaign-INVARIANT — the Active-Campaign sweep deliberately
// skips it (campaignCache.ts), so a fresh import would be invisible in the lists
// until a reload unless we invalidate it explicitly. We do that the moment the
// import succeeds, BEFORE any switch decision, so the new campaign shows up
// either way. "Switch" is the guarded switch every other switch is
// (useCampaignSwitch: asks before unsaved edits are lost, sweeps, says what
// happened) and then lands on the imported campaign's Overview.
//
// fetch exposes no upload progress, so the pending state is a busy button
// rather than a percentage bar.
export function ImportCampaignButton({
  onSwitched,
  variant = "secondary",
}: {
  onSwitched?: () => void;
  variant?: "primary" | "secondary" | "ghost";
}) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const nav = useScreenNav();
  const inputRef = useRef<HTMLInputElement>(null);

  // pendingFile drives the confirm dialog; summary drives the success/switch
  // dialog. They never coexist — a successful import clears the file and sets the
  // summary. error is the last upload failure, shown under the button.
  const [pendingFile, setPendingFile] = useState<File | null>(null);
  const [summary, setSummary] = useState<ImportSummary | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const campaignSwitch = useCampaignSwitch();

  // Opening the native file picker; the input is reset on each open so re-picking
  // the SAME file still fires a change (browsers suppress an unchanged value).
  const openPicker = () => {
    setError(null);
    inputRef.current?.click();
  };

  const onFileChosen = (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0] ?? null;
    e.target.value = "";
    if (file) {
      setError(null);
      setPendingFile(file);
    }
  };

  // Radix AlertDialog.Action closes the dialog on click, so the confirm dialog is
  // gone by the time the upload resolves. Capture the file, then upload against
  // that value — a failure surfaces in the alert under the button (not the
  // now-closed dialog), and a success opens the switch prompt.
  //
  // Both outcomes ALSO toast (ADR-0017): an import can take a while, the GM may
  // have left the page by the time it answers, and the inline alert/dialog would
  // then report nowhere. The toast is anchored in the shell, so feedback
  // survives that unmount either way.
  const runImport = async (file: File) => {
    setBusy(true);
    setError(null);
    try {
      // The imported campaign is the newest one; it must not become the Active
      // Campaign by being that before the GM chose "Switch" (ADR-0053 d7).
      const result = await campaignSwitch.keepActive(() => importCampaignBundle(file));
      // The new campaign must appear in the lists whether or not the GM switches
      // to it — listCampaigns is sweep-invariant, so invalidate it now.
      void invalidateMethodQueries(queryClient, CampaignService.method.listCampaigns);
      setSummary(result);
      toast.success(t("components.importedTitle", { name: result.name }));
      // The dropped-refs caveat rides its OWN warning toast, not only the success
      // dialog, for the same unmount reason — warning styling (richColors), since
      // the import still succeeded.
      if (result.droppedParticipantRefs > 0) {
        toast.warning(droppedRefsNote(t, result.droppedParticipantRefs));
      }
    } catch (err) {
      // The pod reads one bundle at a time (import or export): that refusal is
      // "try again in a moment", in the GM's language, not the server's.
      const message = isBundleBusy(err) ? t("campaigns.bundleBusy") : errorMessage(err);
      setError(message);
      toast.error(t("components.couldntImportCampaign", { message }));
    } finally {
      setBusy(false);
    }
  };

  const switchToImported = (imported: ImportSummary) => {
    campaignSwitch.switchTo(
      { id: imported.campaignId, name: imported.name },
      {
        onSwitched: ({ deferred }) => {
          onSwitched?.();
          // A switch deferred by a live Voice Session has not happened yet: the
          // Overview would still be the session's campaign.
          if (!deferred) afterEditorsSettle(() => nav.goCampaignView("overview"));
        },
        onError: (err) =>
          toast.error(t("components.importedCouldntSwitch", { message: errorMessage(err) })),
      },
    );
  };

  return (
    <span className="gx-import-campaign">
      {/* Driven programmatically from the visible trigger, so it's an off-screen,
          non-focusable, screen-reader-hidden element — not a stray tab stop. */}
      <input
        ref={inputRef}
        type="file"
        accept=".gz,.json,application/gzip,application/json"
        className="gx-import-campaign__input"
        tabIndex={-1}
        aria-hidden="true"
        onChange={onFileChosen}
      />
      {/* Busy, not disabled, while the upload runs (WorkingButton). */}
      <WorkingButton type="button" variant={variant} iconStart={<Upload />} working={busy} onClick={openPicker}>
        {busy ? t("components.importing") : t("components.importCampaign")}
      </WorkingButton>

      {/* fetch exposes no upload progress, so a failure shows here as a persistent
          alert rather than a progress bar; it clears on the next pick/open. */}
      {error && (
        <span className="gx-import-campaign__error" role="alert">
          {t("components.couldntImportCampaign", { message: error })}
        </span>
      )}

      {/* Confirm gate — an upload is deliberate; a stray file pick shouldn't mint a
          campaign. */}
      <ConfirmDialog
        open={pendingFile !== null}
        onOpenChange={(o) => {
          if (!o) setPendingFile(null);
        }}
        title={t("components.importConfirmTitle")}
        description={
          // The filename is JSX (<strong>), so the sentence is split into
          // before/after keys around it — both halves stay natural in each
          // language because the German phrasing keeps the same shape.
          <span className="gx-import-campaign__file">
            {t("components.importConfirmBefore")}
            <strong>{pendingFile?.name}</strong>
            {t("components.importConfirmAfter")}
          </span>
        }
        confirmLabel={t("components.importConfirmAction")}
        cancelLabel={t("common.cancel")}
        confirmDisabled={busy}
        destructive={false}
        onConfirm={() => {
          const file = pendingFile;
          if (file) void runImport(file);
        }}
      />

      {/* Success — report the imported campaign and offer an EXPLICIT switch
          (ADR-0053 d7). Declining keeps it in the list, unselected. */}
      <ConfirmDialog
        open={summary !== null}
        onOpenChange={(o) => {
          if (!o) setSummary(null);
        }}
        title={summary ? t("components.importedTitle", { name: summary.name }) : ""}
        description={
          summary ? (
            <span className="gx-import-campaign__success">
              {t("components.importedSwitchPrompt", { name: summary.name })}
              {summary.droppedParticipantRefs > 0 && (
                // Import SUCCEEDED — some chunk participant refs mapped to no
                // imported agent/character and were dropped (#381/#388). A caveat,
                // not an error: warning styling, and the campaign is fully usable.
                <span className="gx-import-campaign__warning" role="status">
                  {droppedRefsNote(t, summary.droppedParticipantRefs)}
                </span>
              )}
            </span>
          ) : undefined
        }
        confirmLabel={t("components.importSwitch")}
        cancelLabel={t("components.importNotNow")}
        confirmDisabled={campaignSwitch.pending}
        destructive={false}
        onConfirm={() => {
          if (summary) switchToImported(summary);
        }}
      />
      {/* Outside both dialogs: the "Discard unsaved changes?" question has to
          outlive the success prompt that closes underneath it. */}
      {campaignSwitch.dialog}
    </span>
  );
}
