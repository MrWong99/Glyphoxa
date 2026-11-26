import { useId } from "react";
import { useQuery } from "@connectrpc/connect-query";
import { toast } from "sonner";
import { Archive, Download, Trash2 } from "lucide-react";

import { CampaignService } from "@gen/glyphoxa/management/v1/management_pb";
import { useScreenNav } from "@/app/screenNav";
import { CampaignSettingsForm } from "@/components/CampaignSettingsForm";
import { useCampaignLifecycle } from "@/components/useCampaignLifecycle";
import { useCampaignSwitch } from "@/components/useCampaignSwitch";
import { Button } from "@/components/ui/Button";
import { QueryBoundary } from "@/components/ui/QueryBoundary";
import { Skeleton } from "@/components/ui/Skeleton";
import { useDirtyGuard } from "@/components/ui/useDirtyGuard";
import { useI18n } from "@/i18n";

import "./campaignSettings.css";

// The campaign's Settings view (ADR-0063 amendment): everything about the
// Active Campaign as a whole, on a page with an address (/campaign/settings).
// Four sections:
//
//   General             name, System, Campaign Language
//   Session Highlights  recording (a consent decision) and the two detector
//                       settings, shown openly rather than behind "Advanced"
//   Data                Export a Campaign Bundle
//   Danger zone         Archive (says which campaign becomes active instead),
//                       Delete (behind the re-typed name)
//
// The first two are one form (CampaignSettingsForm) with a save row that stays
// at the bottom of the screen and says when something is unsaved; the last two
// act at once and belong to useCampaignLifecycle, shared with the Campaigns
// page.
//
// Around them the page adds:
//
//   - the unsaved-edits guard: the form reports its dirtiness and the route
//     layer holds a sidebar click or a tab close while it is dirty
//     (useDirtyGuard → lib/navigationGuard);
//   - feedback: a save says so with a toast and beside the Save button;
//   - honest load states (QueryBoundary): a skeleton while the campaign
//     resolves and, when it cannot be read, the reason and a way to try again.
export function CampaignSettingsPanel() {
  const { t } = useI18n();
  const nav = useScreenNav();
  // retry:false like every other observer of this shared cache entry (the
  // topbar switcher, the shell, Setup, Session), so retry behaviour does not
  // depend on which of them mounted first. It is the FULL campaign row —
  // highlight recording and its tuning included — which the form seeds from.
  const activeQ = useQuery(CampaignService.method.getActiveCampaign, {}, { retry: false });
  const campaign = activeQ.data?.campaign;
  // The campaigns that are not archived: what the Archive and Delete confirms
  // name as "becomes active instead". The topbar switcher's request, so the two
  // share one cache entry.
  const listQ = useQuery(CampaignService.method.listCampaigns, { includeArchived: false });

  // Registers the dirtiness probe the route layer reads. No in-page action
  // replaces the form, so nothing here needs guard() or the dialog: leaving is
  // always a navigation, and NavigationGuard asks.
  const edits = useDirtyGuard();

  const campaignSwitch = useCampaignSwitch();
  const lifecycle = useCampaignLifecycle({
    activeId: campaign?.id,
    campaigns: listQ.data?.campaigns,
    // "Open" on "Duplicated as …": the copy is never the campaign this page is
    // about, so it is always a switch.
    onOpen: (target) =>
      campaignSwitch.switchTo(target, {
        onSwitched: ({ deferred }) => {
          if (!deferred) nav.goCampaignView("overview");
        },
      }),
    // This page is about the Active Campaign. Once that is archived or deleted
    // the page would silently turn into the NEXT campaign's settings; the
    // Campaigns page is where the result (and the Undo) can be seen.
    onActiveRemoved: () => nav.goScreen("campaigns"),
  });

  const dataId = useId();
  const dangerId = useId();

  return (
    <div className="gx-campaign-settings">
      <QueryBoundary
        query={activeQ}
        errorTitle={t("campaigns.settingsLoadErrorTitle")}
        skeleton={
          <Skeleton>
            <Skeleton.Block height={220} />
            <Skeleton.Block height={260} />
          </Skeleton>
        }
      >
        {campaign && (
          <>
            {/* key: the form seeds its fields from the campaign on mount only
                (protecting in-progress edits from background refetches), so a
                change of campaign IDENTITY must remount it — otherwise a save
                would write the old campaign's values under the new id. */}
            <CampaignSettingsForm
              key={campaign.id}
              campaign={campaign}
              variant="page"
              onSaved={() => toast.success(t("shell.campaignSettingsSaved"))}
              onDirtyChange={edits.markDirty}
            />

            <section className="gx-settings-section" aria-labelledby={dataId}>
              <h2 id={dataId} className="gx-settings-section__title">
                {t("campaigns.sectionData")}
              </h2>
              <div className="gx-settings-row">
                <div className="gx-settings-row__text">
                  <p className="gx-settings-row__title">{t("campaigns.dataExportTitle")}</p>
                  <p className="gx-settings-row__desc">{t("campaigns.dataExportDesc")}</p>
                </div>
                <Button
                  variant="secondary"
                  iconStart={<Download />}
                  onClick={() => lifecycle.exportBundle(campaign)}
                >
                  {t("campaigns.menuExport")}
                </Button>
              </div>
            </section>

            <section className="gx-settings-section gx-settings-section--danger" aria-labelledby={dangerId}>
              <h2 id={dangerId} className="gx-settings-section__title">
                {t("campaigns.sectionDanger")}
              </h2>
              <div className="gx-settings-row">
                <div className="gx-settings-row__text">
                  <p className="gx-settings-row__title">{t("campaigns.archiveRowTitle")}</p>
                  <p className="gx-settings-row__desc">{t("campaigns.archiveRowDesc")}</p>
                </div>
                <Button
                  variant="secondary"
                  iconStart={<Archive />}
                  aria-disabled={lifecycle.busy.has(campaign.id) || undefined}
                  onClick={() => {
                    if (!lifecycle.busy.has(campaign.id)) lifecycle.archive(campaign);
                  }}
                >
                  {t("campaigns.menuArchiveAsk")}
                </Button>
              </div>
              <div className="gx-settings-row">
                <div className="gx-settings-row__text">
                  <p className="gx-settings-row__title">{t("campaigns.deleteRowTitle")}</p>
                  <p className="gx-settings-row__desc">{t("campaigns.deleteRowDesc")}</p>
                </div>
                <Button
                  variant="danger"
                  iconStart={<Trash2 />}
                  aria-disabled={lifecycle.busy.has(campaign.id) || undefined}
                  onClick={() => {
                    if (!lifecycle.busy.has(campaign.id)) lifecycle.remove(campaign);
                  }}
                >
                  {t("components.deleteEllipsis")}
                </Button>
              </div>
            </section>
          </>
        )}
      </QueryBoundary>
      {lifecycle.dialogs}
      {campaignSwitch.dialog}
    </div>
  );
}
