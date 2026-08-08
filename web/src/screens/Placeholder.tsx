import { Card, CardBody } from "@/components/ui/Card";
import { Badge } from "@/components/ui/Badge";
import { useI18n } from "@/i18n";

// Styled "coming soon" placeholder for the Campaign and Session screens. Those
// screens wire to their RPCs in later stages (#67+); this stage ships only the
// Configuration screen on the live GetActiveCampaign RPC (ADR-0039). Uses the
// arcane gx- vocabulary so it reads as part of the design, not a stub.
// The title arrives pre-translated (the caller picks the key), so this screen
// only owns its own lede and badge copy.
export function Placeholder({ title }: { title: string }) {
  const { t } = useI18n();
  return (
    <div className="gx-providers">
      <h1>{title}</h1>
      <p className="gx-providers__lede">{t("auth.placeholderLede")}</p>
      <Card>
        <CardBody>
          <Badge variant="neutral" dot size="sm">
            {t("auth.placeholderSoon")}
          </Badge>
        </CardBody>
      </Card>
    </div>
  );
}
