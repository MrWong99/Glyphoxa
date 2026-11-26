import { useEffect, useRef, useState } from "react";
import { useQuery, useMutation } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";

import { ProviderService } from "@gen/glyphoxa/management/v1/management_pb";
import { AdvancedCard } from "@/components/ui/AdvancedCard";
import { Alert } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { Input } from "@/components/ui/Input";
import { errorMessage } from "@/lib/connectError";
import { invalidateMethodQueries } from "@/lib/queryClient";
import { useI18n, type TFunc } from "@/i18n";

// The per-session spend caps (#130, ADR-0046), labelled by what they DO: the
// soft cap pauses new AI replies, the hard cap ends the session. Both figures
// are estimates and apply from the next session. The collapsed header states
// the current limits, so the GM does not have to open it to know.
//
// Validated here before the server sees it: an amount is a non-negative
// number, and the session can only end at or above where replies pause (the
// server enforces the same, ADR-0046). Save is off until something changed.

/** A field's text as a cap: undefined for blank (= no cap), NaN when unreadable. */
function parse(v: string): number | undefined {
  const s = v.trim();
  return s === "" ? undefined : Number(s);
}

const usd = (lang: string, n: number) =>
  n.toLocaleString(lang, { style: "currency", currency: "USD", minimumFractionDigits: 2 });

/** The collapsed header: the limits in force, or that there are none. */
function capsSummary(t: TFunc, lang: string, soft?: number, hard?: number): string {
  if (soft !== undefined && hard !== undefined)
    return t("config.capsBoth", { pause: usd(lang, soft), end: usd(lang, hard) });
  if (soft !== undefined) return t("config.capsPauseOnly", { pause: usd(lang, soft) });
  if (hard !== undefined) return t("config.capsEndOnly", { end: usd(lang, hard) });
  return t("config.capsNone");
}

export function SpendLimitsCard({ anchor }: { anchor: string }) {
  const { t, lang } = useI18n();
  const queryClient = useQueryClient();
  const capsQuery = useQuery(ProviderService.method.getSpendCaps, {});
  const stored = capsQuery.data?.caps;
  const save = useMutation(ProviderService.method.setSpendCaps, {
    onSuccess: (res) => {
      dirty.current = false;
      toast.success(
        t("config.toastLimitsSaved", { limits: capsSummary(t, lang, res.caps?.softUsd, res.caps?.hardUsd) }),
      );
      void invalidateMethodQueries(queryClient, ProviderService.method.getSpendCaps);
    },
  });

  const [soft, setSoft] = useState("");
  const [hard, setHard] = useState("");
  // The seed is guarded so a slow load or a post-save refetch never clobbers typing.
  const dirty = useRef(false);
  useEffect(() => {
    if (capsQuery.data && !dirty.current) {
      setSoft(stored?.softUsd != null ? String(stored.softUsd) : "");
      setHard(stored?.hardUsd != null ? String(stored.hardUsd) : "");
    }
  }, [capsQuery.data, stored]);
  const edit = (set: (v: string) => void) => (v: string) => {
    dirty.current = true;
    save.reset();
    set(v);
  };

  const softV = parse(soft);
  const hardV = parse(hard);
  const invalid = (n: number | undefined) => n !== undefined && (!Number.isFinite(n) || n < 0);
  const softError = invalid(softV) ? t("config.capInvalid") : null;
  const hardError = invalid(hardV)
    ? t("config.capInvalid")
    : softV !== undefined && hardV !== undefined && !softError && hardV < softV
      ? t("config.capHardBelowSoft")
      : null;
  const changed = softV !== stored?.softUsd || hardV !== stored?.hardUsd;

  const header = capsQuery.isSuccess
    ? capsSummary(t, lang, stored?.softUsd, stored?.hardUsd)
    : t("config.spendingLimitsHint");

  return (
    <section id={anchor} tabIndex={-1} className="gx-setup-section" aria-label={t("config.spendingLimitsTitle")}>
      <AdvancedCard title={t("config.spendingLimitsTitle")} hint={header}>
        <div className="gx-spendcaps">
          <p className="gx-spendcaps__lede">{t("config.spendCapsLede")}</p>
          {capsQuery.isError && (
            <Alert
              tone="danger"
              action={
                <Button size="sm" variant="secondary" loading={capsQuery.isFetching} onClick={() => void capsQuery.refetch()}>
                  {t("common.retry")}
                </Button>
              }
            >
              {t("config.spendCapsLoadError", { message: errorMessage(capsQuery.error) })}
            </Alert>
          )}
          <div className="gx-spendcaps__inputs">
            <Input
              label={t("config.softLimitLabel")}
              type="number"
              inputMode="decimal"
              min="0"
              step="0.01"
              placeholder={t("config.softLimitPlaceholder")}
              hint={t("config.softLimitHint")}
              error={softError}
              value={soft}
              onChange={(e) => edit(setSoft)(e.target.value)}
            />
            <Input
              label={t("config.hardLimitLabel")}
              type="number"
              inputMode="decimal"
              min="0"
              step="0.01"
              placeholder={t("config.hardLimitPlaceholder")}
              hint={t("config.hardLimitHint")}
              error={hardError}
              value={hard}
              onChange={(e) => edit(setHard)(e.target.value)}
            />
          </div>
          {save.isError && (
            <Alert tone="danger" title={t("config.couldntSaveLimits")}>
              {errorMessage(save.error)}
            </Alert>
          )}
          <div className="gx-spendcaps__save">
            <Button
              variant="primary"
              size="sm"
              loading={save.isPending}
              // Held until the stored caps have been read: blank means "clear
              // that cap", so a Save before the seed lands (or after a failed
              // read) would wipe limits the GM never saw.
              disabled={!capsQuery.isSuccess || !changed || Boolean(softError || hardError)}
              onClick={() => save.mutate({ softUsd: softV, hardUsd: hardV })}
            >
              {t("config.saveSpendingLimits")}
            </Button>
          </div>
        </div>
      </AdvancedCard>
    </section>
  );
}
