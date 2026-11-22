import { useQuery } from "@connectrpc/connect-query";

import { CampaignService } from "@gen/glyphoxa/management/v1/management_pb";
import { useScreenNav } from "@/app/screenNav";
import { FirstCampaignCTA } from "@/components/FirstCampaignCTA";
import { useCampaignSwitch } from "@/components/useCampaignSwitch";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { useI18n } from "@/i18n";
import { errorMessage } from "@/lib/connectError";

import "./campaigns.css";

// The Campaigns screen (/t/:tenantSlug/campaigns, ADR-0063 amendment): every
// campaign at the table on a page of its own, instead of only inside the
// topbar switcher's popover. This is the skeleton's version — the list and
// Open; the lifecycle actions (settings for any campaign, export, duplicate,
// archive, delete) stay in the topbar switcher until the page grows them.
//
// Open makes the campaign the Active Campaign through the guarded switch
// (useCampaignSwitch: asks before unsaved edits are lost, sweeps the caches,
// says what happened) and then lands on that campaign's Overview.
export function Campaigns() {
  const { t } = useI18n();
  const nav = useScreenNav();
  // The same request the topbar switcher makes (non-archived), so the two share
  // one cache entry and a create/rename shows up in both.
  const listQ = useQuery(CampaignService.method.listCampaigns, { includeArchived: false });
  const campaigns = listQ.data?.campaigns ?? [];
  // retry:false like every other observer of this shared entry.
  const activeQ = useQuery(CampaignService.method.getActiveCampaign, {}, { retry: false });
  const activeId = activeQ.data?.campaign?.id;

  const campaignSwitch = useCampaignSwitch();

  const open = (campaign: { id: string; name: string }) => {
    if (campaign.id === activeId) {
      nav.goCampaignView("overview");
      return;
    }
    campaignSwitch.switchTo(campaign, {
      // A switch deferred by a live Voice Session has not happened yet: the
      // Overview would still show the session's campaign, so stay here and let
      // the toast explain.
      onSwitched: ({ deferred }) => {
        if (!deferred) nav.goCampaignView("overview");
      },
    });
  };

  return (
    <div className="gx-providers gx-campaigns">
      <h1>{t("shell.titleCampaigns")}</h1>
      <p className="gx-providers__lede">{t("shell.campaignsLede")}</p>

      {listQ.isPending ? (
        // role="status": aria-label on a role-less div is not exposed at all.
        <div className="gx-campaigns__loading" role="status" aria-busy="true" aria-label={t("common.loading")}>
          <div className="gx-skeleton" data-testid="campaigns-loading" />
          <div className="gx-skeleton" />
        </div>
      ) : listQ.isError ? (
        <div className="gx-campaigns__error">
          <p className="gx-campaign__error" role="alert">
            {t("components.couldntLoadCampaigns", { message: errorMessage(listQ.error) })}
          </p>
          <Button variant="secondary" size="sm" onClick={() => void listQ.refetch()}>
            {t("auth.errorRetry")}
          </Button>
        </div>
      ) : campaigns.length === 0 ? (
        // No campaign yet: the same create-first-campaign card every
        // campaign-scoped screen shows (#267).
        <Card accent>
          <FirstCampaignCTA />
        </Card>
      ) : (
        <ul className="gx-campaigns__list" aria-label={t("shell.campaignsList")}>
          {campaigns.map((c) => {
            const isActive = c.id === activeId;
            const meta = [c.system, c.language.toUpperCase()].filter(Boolean).join(" · ");
            return (
              <li key={c.id} className="gx-campaigns__row" data-active={isActive || undefined}>
                <div className="gx-campaigns__meta">
                  <span className="gx-campaigns__name">{c.name}</span>
                  {meta && <span className="gx-campaigns__system">{meta}</span>}
                </div>
                {isActive && (
                  <Badge variant="arcane" size="sm">
                    {t("shell.campaignActive")}
                  </Badge>
                )}
                <Button
                  variant={isActive ? "primary" : "secondary"}
                  size="sm"
                  // The visible label is just "Open"; the accessible name says
                  // which campaign, or a screen reader hears a column of "Open".
                  aria-label={t("shell.campaignOpenNamed", { name: c.name })}
                  disabled={campaignSwitch.pending}
                  onClick={() => open({ id: c.id, name: c.name })}
                >
                  {t("shell.campaignOpen")}
                </Button>
              </li>
            );
          })}
        </ul>
      )}
      {campaignSwitch.dialog}
    </div>
  );
}
