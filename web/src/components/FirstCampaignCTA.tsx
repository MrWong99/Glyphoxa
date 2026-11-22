import { CreateCampaignForm, useCreateCampaign } from "@/components/CreateCampaignForm";
import { useI18n } from "@/i18n";

import "./firstCampaignCTA.css";

// FirstCampaignCTA is the zero-campaign state of every campaign-scoped screen
// (#267): a fresh install's GetActiveCampaign (and every read scoped to it) is
// CodeNotFound, so instead of a raw "no active campaign" error the screen offers
// the create form. Creating here mints the campaign (auto-Butler, ADR-0009) and
// selects it, and the Active-Campaign sweep re-resolves every scoped read, so
// the host screen swaps itself for its live content — no reload, no
// `glyphoxa seed`. Used by Setup, Campaign and Session.
//
// It ends the way every create does (useCreateCampaign): a toast that says the
// campaign exists and its Butler is ready, and the new campaign's Overview —
// the place that says what to do next, rather than whichever screen the GM
// happened to be on when they had no campaign.
export function FirstCampaignCTA() {
  const { t } = useI18n();
  const create = useCreateCampaign();
  return (
    <div className="gx-first-campaign">
      <span className="gx-overline">{t("config.activeCampaign")}</span>
      <h2 className="gx-first-campaign__title">{t("config.firstCampaignTitle")}</h2>
      <p className="gx-first-campaign__lede">{t("config.firstCampaignLede")}</p>
      <CreateCampaignForm onSubmit={create.submit} pending={create.pending} error={create.error} autoFocusName />
      {create.dialog}
    </div>
  );
}
