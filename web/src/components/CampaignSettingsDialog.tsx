import { toast } from "sonner";

import type { Campaign } from "@gen/glyphoxa/management/v1/management_pb";
import { CampaignSettingsForm } from "@/components/CampaignSettingsForm";
import { Dialog } from "@/components/ui/Dialog";
import { useDirtyGuard } from "@/components/ui/useDirtyGuard";
import { useI18n } from "@/i18n";

// CampaignSettingsDialog — the settings of ANY campaign, from its card on the
// Campaigns page. Renaming a campaign, changing its System or its Campaign
// Language used to require making it the Active Campaign first (the editor only
// ever opened on the active one); UpdateCampaign takes an explicit id, so that
// limit was the web's, not the server's.
//
// It is fed the full Campaign row (the list returns it), not just id and name:
// the form sends every field it shows, so seeding it from a partial row would
// write defaults over the campaign's real highlight settings.
//
// Closing with unsaved edits asks first — by the X, Escape or Cancel alike —
// and the edits are listed in the app-wide register (useDirtyGuard), so a tab
// close asks too.
//
// Mount it while open (the caller renders it conditionally).
export function CampaignSettingsDialog({
  campaign,
  onClose,
}: {
  campaign: Campaign;
  onClose: () => void;
}) {
  const { t } = useI18n();
  const edits = useDirtyGuard();
  const close = () => edits.guard(onClose);
  return (
    <>
      <Dialog
        open
        onOpenChange={(open) => {
          if (!open) close();
        }}
        title={t("campaigns.settingsTitle", { name: campaign.name })}
        size="lg"
        // A stray click on the backdrop must not start a "discard?" question.
        closeOnOutsideClick={false}
      >
        <CampaignSettingsForm
          campaign={campaign}
          variant="dialog"
          onSaved={() => {
            toast.success(t("shell.campaignSettingsSaved"));
            onClose();
          }}
          onCancel={close}
          onDirtyChange={edits.markDirty}
        />
      </Dialog>
      {edits.dialog}
    </>
  );
}
