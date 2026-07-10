import { useRef, useState } from "react";
import { useMutation, createConnectQueryKey } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Upload } from "lucide-react";

import { CampaignService } from "@gen/glyphoxa/management/v1/management_pb";
import { invalidateActiveCampaignScopedQueries } from "@/lib/campaignCache";
import { importCampaignBundle, type ImportSummary } from "@/lib/download";
import { ConfirmDialog } from "@/components/ui/ConfirmDialog";

import "./importCampaignButton.css";

// ImportCampaignButton — the campaign-restore affordance in the switcher's
// manage-view footer (#294, beside CreateCampaignForm). It uploads a Campaign
// Bundle (the export's counterpart) over the plain-net/http importer, which mints
// a NEW campaign and does NOT auto-activate it (ADR-0053 d7). So the flow is:
// pick a file → confirm (nothing is overwritten, but an upload is deliberate) →
// pending upload → a success prompt that reports the imported name and offers an
// EXPLICIT switch. Declining leaves the imported campaign in the list, unselected.
//
// listCampaigns is campaign-INVARIANT — the Active-Campaign sweep deliberately
// skips it (campaignCache.ts), so a fresh import would be invisible in the picker
// until a reload unless we invalidate it explicitly. We do that the moment the
// import succeeds, BEFORE any switch decision, so the new campaign shows up either
// way. A "Switch" runs the normal SetActiveCampaign path, whose success then runs
// the campaign-scoped sweep so every screen follows the new selection.
//
// fetch exposes no upload progress, so the pending state is a disabled/"Importing…"
// affordance rather than a percentage bar.
export function ImportCampaignButton({ onSwitched }: { onSwitched?: () => void }) {
  const queryClient = useQueryClient();
  const inputRef = useRef<HTMLInputElement>(null);

  // pendingFile drives the confirm dialog; summary drives the success/switch
  // dialog. They never coexist — a successful import clears the file and sets the
  // summary. error is the last upload failure, shown inline in the confirm dialog.
  const [pendingFile, setPendingFile] = useState<File | null>(null);
  const [summary, setSummary] = useState<ImportSummary | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const invalidateList = () =>
    queryClient.invalidateQueries({
      queryKey: createConnectQueryKey({
        schema: CampaignService.method.listCampaigns,
        cardinality: "finite",
      }),
    });

  const setActive = useMutation(CampaignService.method.setActiveCampaign, {
    onSuccess: () => {
      void invalidateActiveCampaignScopedQueries(queryClient);
      setSummary(null);
      onSwitched?.();
    },
    onError: (err) => toast.error(`Imported it, but couldn't switch to it: ${err.message}`),
  });

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
  // that value — a failure surfaces in the standalone alert below the trigger
  // (not the now-closed dialog), and a success opens the switch prompt.
  //
  // Both outcomes ALSO toast (ADR-0017): between the confirm dialog closing and
  // the success dialog opening no modal is mounted, so an Escape / outside-click
  // can dismiss the whole switcher panel and unmount this component mid-upload —
  // the inline alert/dialog would then report nowhere. The toast is anchored
  // outside the panel, so feedback survives that unmount either way.
  const runImport = async (file: File) => {
    setBusy(true);
    setError(null);
    try {
      const result = await importCampaignBundle(file);
      // The new campaign must appear in the picker whether or not the operator
      // switches to it — listCampaigns is sweep-invariant, so invalidate it now.
      void invalidateList();
      setSummary(result);
      toast.success(`Imported “${result.name}”`);
    } catch (err) {
      const message = (err as Error).message;
      setError(message);
      toast.error(`Couldn't import campaign: ${message}`);
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
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
      <button
        type="button"
        className="gx-campaign-switcher__new gx-import-campaign__trigger"
        onClick={openPicker}
        disabled={busy}
      >
        <Upload size={15} /> {busy ? "Importing…" : "Import campaign"}
      </button>

      {/* fetch exposes no upload progress, so a failure shows here as a persistent
          alert rather than a progress bar; it clears on the next pick/open. */}
      {error && (
        <p className="gx-import-campaign__error" role="alert">
          {error}
        </p>
      )}

      {/* Confirm gate — an upload is deliberate; a stray file pick shouldn't mint a
          campaign. */}
      <ConfirmDialog
        open={pendingFile !== null}
        onOpenChange={(o) => {
          if (!o) setPendingFile(null);
        }}
        title="Import campaign?"
        description={
          <span className="gx-import-campaign__file">
            Create a new campaign from <strong>{pendingFile?.name}</strong>. Nothing existing is
            overwritten.
          </span>
        }
        confirmLabel="Import"
        cancelLabel="Cancel"
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
        title={summary ? `Imported “${summary.name}”` : ""}
        description={
          summary
            ? `“${summary.name}” was imported as a new campaign — switch to it now?`
            : undefined
        }
        confirmLabel="Switch"
        cancelLabel="Not now"
        confirmDisabled={setActive.isPending}
        destructive={false}
        onConfirm={() => summary && setActive.mutate({ campaignId: summary.campaignId })}
      />
    </>
  );
}
