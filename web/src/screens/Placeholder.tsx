import { Link, useParams } from "@tanstack/react-router";

import { useCurrentUser } from "@/app/currentUser";
import { CAMPAIGN_DESTINATIONS, screenDestinationsFor } from "@/app/destinations";
import { Card, CardBody } from "@/components/ui/Card";
import { useI18n } from "@/i18n";

import "./placeholder.css";

// The in-shell "page not found" screen: a /t/:tenantSlug/:screen visit for a
// screen that does not exist. It says so plainly and then lists every place
// that DOES exist — the campaign's views and the other screens, the same
// destinations the sidebar offers THIS viewer (app/destinations.ts: the Admin
// screen is listed for a Platform Admin only, ADR-0065) — so a mistyped or
// outdated link is one click from wherever it was meant to go. The title
// arrives pre-translated (the caller picks the key); this screen owns its lede
// and the list.
export function Placeholder({ title }: { title: string }) {
  const { t } = useI18n();
  const { tenantSlug } = useParams({ strict: false });
  const screens = screenDestinationsFor(useCurrentUser());
  return (
    <div className="gx-providers">
      <h1>{title}</h1>
      <p className="gx-providers__lede">{t("auth.placeholderLede")}</p>
      {tenantSlug && (
        <Card>
          <CardBody>
            <nav className="gx-placeholder" aria-label={t("shell.placeholderGoTo")}>
              <h2 className="gx-placeholder__heading">{t("shell.placeholderGoTo")}</h2>
              <ul className="gx-placeholder__list">
                {CAMPAIGN_DESTINATIONS.map((d) => {
                  const Icon = d.icon;
                  return (
                    <li key={d.view}>
                      <Link
                        className="gx-placeholder__link"
                        to="/t/$tenantSlug/$screen/$view"
                        params={{ tenantSlug, screen: "campaign", view: d.view }}
                      >
                        <Icon size={16} aria-hidden="true" />
                        {t(d.label)}
                      </Link>
                    </li>
                  );
                })}
                {screens.map((d) => {
                  const Icon = d.icon;
                  return (
                    <li key={d.screen}>
                      <Link
                        className="gx-placeholder__link"
                        to="/t/$tenantSlug/$screen"
                        params={{ tenantSlug, screen: d.screen }}
                      >
                        <Icon size={16} aria-hidden="true" />
                        {t(d.label)}
                      </Link>
                    </li>
                  );
                })}
              </ul>
            </nav>
          </CardBody>
        </Card>
      )}
    </div>
  );
}
