import { Link, useParams } from "@tanstack/react-router";

import { Card, CardBody } from "@/components/ui/Card";
import { useI18n } from "@/i18n";

// The in-shell "page not found" screen: a /t/:tenantSlug/:screen visit for a
// screen that does not exist. It says so plainly and links back to Setup, the
// tenant's landing screen. The title arrives pre-translated (the caller picks
// the key), so this screen only owns its own lede and link copy.
export function Placeholder({ title }: { title: string }) {
  const { t } = useI18n();
  const { tenantSlug } = useParams({ strict: false });
  return (
    <div className="gx-providers">
      <h1>{title}</h1>
      <p className="gx-providers__lede">{t("auth.placeholderLede")}</p>
      {tenantSlug && (
        <Card>
          <CardBody>
            <Link to="/t/$tenantSlug/$screen" params={{ tenantSlug, screen: "configuration" }}>
              {t("auth.placeholderBackToSetup")}
            </Link>
          </CardBody>
        </Card>
      )}
    </div>
  );
}
