import type { ReactNode } from "react";
import { useQuery } from "@connectrpc/connect-query";
import { timestampDate } from "@bufbuild/protobuf/wkt";
import { Gauge } from "lucide-react";

import { ProviderService } from "@gen/glyphoxa/management/v1/management_pb";
import { Card } from "@/components/ui/Card";
import { Alert } from "@/components/ui/Alert";
import { ErrorState } from "@/components/ui/ErrorState";
import { Skeleton } from "@/components/ui/Skeleton";
import { errorMessage } from "@/lib/connectError";
import { useI18n } from "@/i18n";
import { SETUP_ANCHORS } from "@/lib/readiness";
import { operatorHref } from "./operatorContact";

// PlanUsageCard shows the table's plan and, where the plan includes a monthly
// allowance (ADR-0055 gate (b)), how much of it this month has used — before
// a feature refuses, not after. Every figure is an estimate (ADR-0046). The
// month is the UTC calendar month the gate itself counts.
//
// Three cases from GetUsageSummary:
//   - an allowance applies: a meter, amber above 80 %, and once it is used up
//     the two ways on (wait for the reset; ask the operator to move the table
//     to the bring-your-own-keys plan) — a saved key does not lift it;
//   - the Platform Keys with no allowance (a self-hosted table): included;
//   - own keys: billed by the providers.

/** Above this share of the allowance the meter turns amber. */
const WARN_AT = 0.8;

const usd = (lang: string, n: number) =>
  n.toLocaleString(lang, { style: "currency", currency: "USD", minimumFractionDigits: 2 });

export function PlanUsageCard() {
  const { t, lang } = useI18n();
  const q = useQuery(ProviderService.method.getUsageSummary, {});

  if (q.isPending && !q.failureReason) {
    return (
      <PlanSection>
        <Card className="gx-plan-card">
          <Skeleton lines={2} label={t("config.planLoading")} />
        </Card>
      </PlanSection>
    );
  }
  if (!q.data) {
    return (
      <PlanSection>
        <Card className="gx-plan-card">
          <ErrorState
            size="inline"
            title={t("config.planLoadError")}
            message={errorMessage(q.error ?? q.failureReason)}
            onRetry={() => void q.refetch()}
            retrying={q.isFetching}
          />
        </Card>
      </PlanSection>
    );
  }

  const u = q.data;
  const resets = u.resetsAt
    ? timestampDate(u.resetsAt).toLocaleDateString(lang, { day: "numeric", month: "long", timeZone: "UTC" })
    : "";
  const planName = u.planName || u.planSlug;
  const included = u.includedUsd;

  let body;
  if (included !== undefined) {
    const share = included > 0 ? Math.min(u.monthUsd / included, 1) : 1;
    const tone = u.exhausted ? "danger" : share > WARN_AT ? "warning" : "ok";
    const used = t("config.usageLine", { used: usd(lang, u.monthUsd), included: usd(lang, included) });
    body = (
      <>
        <div
          className="gx-plan-meter"
          data-tone={tone}
          role="meter"
          aria-label={t("config.usageMeterLabel")}
          aria-valuemin={0}
          aria-valuemax={included}
          aria-valuenow={Math.min(u.monthUsd, included)}
          aria-valuetext={used}
        >
          <span className="gx-plan-meter__fill" style={{ width: `${Math.round(share * 100)}%` }} />
        </div>
        <p className="gx-plan-card__line">
          {used} <span className="gx-plan-card__reset">{t("config.usageResets", { date: resets })}</span>
        </p>
        {u.exhausted ? (
          <Alert tone="danger" title={t("config.usageExhaustedTitle")}>
            <p>{t("config.usageExhaustedLede")}</p>
            <ul className="gx-plan-card__remedies">
              <li>{t("config.usageRemedyWait", { date: resets })}</li>
              <li>
                {t("config.usageRemedyByokPre")}
                <a href={operatorHref(t("config.planChangeSubject"))}>{t("config.usageRemedyAskOperator")}</a>
                {t("config.usageRemedyByokPost")}
              </li>
            </ul>
          </Alert>
        ) : (
          share > WARN_AT && <p className="gx-plan-card__warn">{t("config.usageNearlyUsed", { date: resets })}</p>
        )}
      </>
    );
  } else {
    body = (
      <p className="gx-plan-card__line">
        {t(u.usesPlatformKeys ? "config.usageIncludedNoAllowance" : "config.usageOwnKeys")}
      </p>
    );
  }

  return (
    <PlanSection labelledBy="setup-plan-title">
      <Card className="gx-plan-card">
        <div className="gx-plan-card__head">
          <span className="gx-provider-row__icon" aria-hidden="true">
            <Gauge size={19} />
          </span>
          <div>
            <span className="gx-overline">{t("config.planOverline")}</span>
            <h2 id="setup-plan-title" className="gx-plan-card__name">
              {planName || t("config.planNone")}
            </h2>
          </div>
        </div>
        {body}
        <p className="gx-plan-card__note">{t("config.usageEstimateNote")}</p>
      </Card>
    </PlanSection>
  );
}

// The card's region: the target of the ready banner's "See your plan" link,
// present in every state so that link always lands. Named by the plan's
// heading once there is one (a generic div cannot carry a name, a section can).
function PlanSection({ labelledBy, children }: { labelledBy?: string; children: ReactNode }) {
  const { t } = useI18n();
  return (
    <section
      id={SETUP_ANCHORS.plan}
      tabIndex={-1}
      className="gx-setup-section"
      aria-labelledby={labelledBy}
      aria-label={labelledBy ? undefined : t("config.planOverline")}
    >
      {children}
    </section>
  );
}
