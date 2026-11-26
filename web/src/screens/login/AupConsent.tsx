import { useId } from "react";

import { useI18n } from "@/i18n";

// AupConsent is the Nutzungsbedingungen / Datenschutzerklärung acknowledgment
// (#518): the checkbox a new account holder ticks before the Discord OAuth
// start becomes available. One component for the two places a person becomes
// an account holder — the open-mode login screen and the join page of an
// invitation link (ADR-0065) — so both ask in the same words and link the same
// two documents.
//
// It only renders the question. Enforcing it is the caller's job (the start
// stays a disabled button until `accepted`) and, behind that, the server's:
// the OAuth start is refused without ?aup=1.
export function AupConsent({
  accepted,
  onChange,
}: {
  accepted: boolean;
  onChange: (accepted: boolean) => void;
}) {
  const { t } = useI18n();
  const id = useId();
  return (
    <label className="gx-login__consent" htmlFor={id}>
      <input id={id} type="checkbox" checked={accepted} onChange={(e) => onChange(e.target.checked)} />
      {/* The sentence is split around the two document links; the documents'
          German proper names stay verbatim in both display languages — they
          are the titles of the authored pages (#518). */}
      <span>
        {t("auth.consentPre")}
        <a href="/terms">Nutzungsbedingungen</a>
        {t("auth.consentMid")}
        <a href="/privacy">Datenschutzerklärung</a>
        {t("auth.consentPost")}
      </span>
    </label>
  );
}
