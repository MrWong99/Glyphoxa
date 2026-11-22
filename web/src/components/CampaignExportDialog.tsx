import { useState } from "react";
import { toast } from "sonner";
import { Download } from "lucide-react";

import { Alert } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { Dialog, DialogClose } from "@/components/ui/Dialog";
import { Switch } from "@/components/ui/Switch";
import { WorkingButton } from "@/components/WorkingButton";
import { useI18n } from "@/i18n";
import { errorMessage } from "@/lib/connectError";
import { downloadBlob, fetchCampaignExport, isBundleBusy } from "@/lib/download";

import "./campaignRowActions.css";

// CampaignExportDialog — exporting a Campaign Bundle (ADR-0053), with the two
// things the server leaves out unless asked on the table: the session history
// and the map images. Export used to be one menu click that sent neither
// parameter, so the file a GM kept "as the campaign" had no transcripts and no
// map pictures in it — and nothing said so.
//
// Both are ON by default: someone exporting to keep a copy wants all of it.
// They can be switched off for a lighter bundle to hand to another GM.
//
// Mount it while open (the caller renders it conditionally), so every open
// starts with both switches on.
export function CampaignExportDialog({
  campaign,
  onClose,
}: {
  campaign: { id: string; name: string };
  onClose: () => void;
}) {
  const { t } = useI18n();
  const [includeHistory, setIncludeHistory] = useState(true);
  const [includeImages, setIncludeImages] = useState(true);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<{ serverBusy: boolean; message: string } | null>(null);

  const run = async () => {
    if (busy) return;
    setBusy(true);
    setFailure(null);
    try {
      const { blob, filename } = await fetchCampaignExport(campaign.id, { includeHistory, includeImages });
      downloadBlob(blob, filename);
      toast.success(t("campaigns.exported", { name: campaign.name }));
      onClose();
    } catch (err) {
      // Stays open with the reason: the choices are still there for the retry.
      setFailure({ serverBusy: isBundleBusy(err), message: errorMessage(err) });
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        // The server is packing the bundle; closing now would lose the download.
        if (!open && !busy) onClose();
      }}
      title={t("campaigns.exportTitle", { name: campaign.name })}
      description={t("campaigns.exportDesc")}
      size="sm"
      footer={
        <>
          <DialogClose>
            <Button variant="ghost">{t("common.cancel")}</Button>
          </DialogClose>
          {/* Busy, not disabled, while the server packs (WorkingButton). */}
          <WorkingButton variant="primary" iconStart={<Download />} working={busy} onClick={() => void run()}>
            {busy ? t("campaigns.exportPacking") : t("campaigns.exportDownload")}
          </WorkingButton>
        </>
      }
    >
      <div className="gx-campaign-export">
        <div className="gx-field">
          <Switch
            checked={includeHistory}
            onCheckedChange={setIncludeHistory}
            label={t("campaigns.exportHistory")}
            disabled={busy}
          />
          <span className="gx-field__hint">{t("campaigns.exportHistoryHint")}</span>
        </div>
        <div className="gx-field">
          <Switch
            checked={includeImages}
            onCheckedChange={setIncludeImages}
            label={t("campaigns.exportImages")}
            disabled={busy}
          />
          <span className="gx-field__hint">{t("campaigns.exportImagesHint")}</span>
        </div>
        <p className="gx-campaign-export__note">{t("campaigns.exportSizeNote")}</p>
        {failure &&
          (failure.serverBusy ? (
            // Not a failure of this campaign: the pod packs one bundle at a
            // time. The answer is to press Download again in a moment.
            <Alert tone="warning" title={t("campaigns.bundleBusyTitle")}>
              {t("campaigns.bundleBusy")}
            </Alert>
          ) : (
            <Alert tone="danger" title={t("campaigns.exportFailed")}>
              {failure.message}
            </Alert>
          ))}
      </div>
    </Dialog>
  );
}
