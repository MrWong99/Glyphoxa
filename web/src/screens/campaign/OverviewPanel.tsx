import { CAMPAIGN_DESTINATIONS } from "@/app/destinations";
import { useI18n } from "@/i18n";
import { useCampaignScreen } from "./campaignContext";

import "./overview.css";

// The campaign's Overview — where a campaign opens (ADR-0063 amendment). This
// is the skeleton's version: one tile per view of the campaign, each saying
// what the view is for and taking the GM there. It is deliberately built only
// from what the host already has, so it renders instantly and cannot fail; the
// dashboard that answers "what happened, what needs me, what is next" replaces
// it in this file.
//
// The tiles are buttons, not anchors: a panel is router-free and navigates
// through the host (useCampaignScreen), and the same destinations are real
// links in the sidebar beside it.
export function OverviewPanel() {
  const { t } = useI18n();
  const { openView } = useCampaignScreen();
  return (
    <div className="gx-overview">
      <ul className="gx-overview__tiles" aria-label={t("shell.overviewSections")}>
        {CAMPAIGN_DESTINATIONS.filter((d) => d.view !== "overview").map((d) => {
          const Icon = d.icon;
          return (
            <li key={d.view}>
              <button type="button" className="gx-overview__tile" onClick={() => openView(d.view)}>
                <span className="gx-overview__icon" aria-hidden="true">
                  <Icon size={18} />
                </span>
                <span className="gx-overview__text">
                  <span className="gx-overview__label">{t(d.label)}</span>
                  <span className="gx-overview__lede">{t(d.lede)}</span>
                </span>
              </button>
            </li>
          );
        })}
      </ul>
    </div>
  );
}
