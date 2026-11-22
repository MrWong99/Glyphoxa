import { useQuery } from "@connectrpc/connect-query";
import { toast } from "sonner";

import { CampaignService } from "@gen/glyphoxa/management/v1/management_pb";
import { CampaignSettingsForm } from "@/components/CampaignSettingsForm";
import { Button } from "@/components/ui/Button";
import { Card, CardBody } from "@/components/ui/Card";
import { useDirtyGuard } from "@/components/ui/useDirtyGuard";
import { useI18n } from "@/i18n";
import { errorMessage } from "@/lib/connectError";
import { useCampaignScreen } from "./campaignContext";

import "./campaignSettings.css";

// The campaign's Settings view (ADR-0063 amendment): the Active Campaign's
// name, System, Campaign Language and highlight recording as a page of their
// own, with an address (/campaign/settings). Until now these were editable only
// inside the topbar switcher's 300px popover — which also threw the edits away
// if the GM clicked anywhere else.
//
// It reuses CampaignSettingsForm as it is. What the page adds around it:
//
//   - the unsaved-edits guard: the form reports its dirtiness and the route
//     layer holds a sidebar click or a tab close while it is dirty
//     (useDirtyGuard → lib/navigationGuard);
//   - feedback: a save says so with a toast (the form stays open — there is no
//     popover to close any more);
//   - honest load states: a skeleton while the campaign resolves and, when it
//     cannot be read, the reason and a way to try again.
export function CampaignSettingsPanel() {
  const { t } = useI18n();
  const { openView } = useCampaignScreen();
  // retry:false like every other observer of this shared cache entry (the
  // topbar switcher, the shell, Setup, Session), so retry behaviour does not
  // depend on which of them mounted first. It is the FULL campaign row —
  // highlight recording and its tuning included — which the form seeds from.
  const activeQ = useQuery(CampaignService.method.getActiveCampaign, {}, { retry: false });
  const campaign = activeQ.data?.campaign;
  // Registers the dirtiness probe the route layer reads. No in-page action
  // replaces the form, so nothing here needs guard() or the dialog: leaving is
  // always a navigation, and NavigationGuard asks.
  const edits = useDirtyGuard();

  if (!campaign) {
    if (activeQ.isError) {
      return (
        <div className="gx-campaign-settings">
          <p className="gx-campaign__error" role="alert">
            {t("campaign.loadError", { message: errorMessage(activeQ.error) })}
          </p>
          <Button variant="secondary" size="sm" onClick={() => void activeQ.refetch()}>
            {t("auth.errorRetry")}
          </Button>
        </div>
      );
    }
    return (
      <div className="gx-campaign-settings" aria-busy="true" aria-label={t("common.loading")}>
        <div className="gx-skeleton" data-testid="campaign-settings-loading" />
      </div>
    );
  }

  return (
    <div className="gx-campaign-settings">
      <Card>
        <CardBody>
          {/* key: the form seeds its fields from the campaign on mount only
              (protecting in-progress edits from background refetches), so a
              change of campaign IDENTITY must remount it — otherwise a save
              would write the old campaign's values under the new id. */}
          <CampaignSettingsForm
            key={campaign.id}
            campaign={campaign}
            onSaved={() => toast.success(t("shell.campaignSettingsSaved"))}
            // Cancel leaves the page for the Overview; with unsaved edits the
            // route layer asks first, like any other way out.
            onCancel={() => openView("overview")}
            onDirtyChange={edits.markDirty}
          />
        </CardBody>
      </Card>
    </div>
  );
}
