import { useId, useState } from "react";
import { Dices } from "lucide-react";
import { useQuery } from "@connectrpc/connect-query";

import { AuthService, AdmissionMode } from "@gen/glyphoxa/management/v1/management_pb";
import { Button } from "@/components/ui/Button";
import { LegalFooter } from "@/components/LegalFooter";
import { LanguageSwitcher } from "@/components/LanguageSwitcher";
import { useI18n } from "@/i18n";
import { useDocumentTitle } from "@/lib/useDocumentTitle";
import { AupConsent } from "./AupConsent";

import "./login.css";

// The Login screen (ADR-0016: Discord-only OAuth). No design handoff exists for
// it, so this is the minimal gate the self-host operator needs: a "Continue with
// Discord" link that starts the net/http OAuth carve-out (/auth/discord/login).
// Discord is the only provider: no placeholder buttons for providers that
// don't exist yet. It is a full-page link, not a Connect call — OAuth is HTML
// redirects (ADR-0015).
//
// notAuthorized surfaces an admission rejection: the OAuth callback bounces a
// denied Discord User back here with ?error=not_authorized. The signal is
// mode-broad — an allowlist miss (ADR-0041) or, in open admission mode, a
// suspended account (ADR-0055) — and DELIBERATELY undifferentiated: the banner
// is non-leaky, never echoing the rejected account's id or WHY it was denied,
// so the copy never names the cause. On a deployment that answers `allowlist`
// it does add the way in — the instance is invite-only, ask its admin for an
// invitation link (ADR-0065) — which is true whichever of the causes it was. In
// `open` mode, and while the mode is unknown, it stays the bare refusal:
// "invite-only" would be false there. A normal first visit renders unchanged.
//
// In `open` mode this screen IS the signup screen, so it carries the AUP/ToS
// acknowledgment (#518): the Discord OAuth start is disabled until the visitor
// ticks the box that links the Nutzungsbedingungen and the Datenschutzerklärung.
// In `allowlist` mode nothing changes — those operators agreed to their own
// deployment's terms by running it — and the fail-safe default (probe loading or
// errored) is allowlist framing, so a flaky probe never blocks a legitimate
// operator login behind a checkbox they cannot see the point of.
//
// The lede frames signup per the deployment's Admission Mode (ADR-0055):
// GetAdmissionMode is public (there is no session yet on this screen), and only
// an explicit `open` answer switches the copy to self-signup framing — while
// the probe is loading or errored the screen fail-safes to today's allowlist
// framing rather than advertising a signup the deployment may not allow. Only
// the copy changes: the OAuth start anchor stays exactly as is in both modes.
export function Login({
  notAuthorized = false,
  aupRequired = false,
}: {
  notAuthorized?: boolean;
  /** The server-side #518 gate bounced an unacknowledged open-mode OAuth start here. */
  aupRequired?: boolean;
}) {
  const { t } = useI18n();
  useDocumentTitle(t("auth.loginDocTitle"));
  const { data, status } = useQuery(AuthService.method.getAdmissionMode, {}, { retry: false });
  const open = data?.admissionMode === AdmissionMode.OPEN;
  const inviteOnly = data?.admissionMode === AdmissionMode.ALLOWLIST;
  const [accepted, setAccepted] = useState(false);
  const reasonId = useId();
  // Open mode = signing up: the acknowledgment gates the OAuth start. While the
  // probe is still IN FLIGHT the start is briefly disabled too — otherwise the
  // first render on an open deployment shows an ungated anchor and a fast click
  // slips past the acknowledgment. An ERRORED probe stays fail-open with the
  // allowlist framing (a flaky probe must not lock a legitimate operator out).
  const blocked = status === "pending" || (open && !accepted);
  // Why the start is not available yet, read WITH the disabled button
  // (aria-describedby) rather than left for a visitor to guess from a greyed
  // control: the box above is not ticked, or the server has not said yet
  // whether this is a signup.
  const reason = !blocked
    ? null
    : status === "pending"
      ? t("auth.continueChecking")
      : t("auth.continueNeedsConsent");

  return (
    <div className="gx-login">
      <div className="gx-login__card">
        {/* The wordmark is the way back to the start page (the landing): a
            visitor who followed a "Sign in" link can still go and read what
            this is. The heading stays the page's one h1, inside the link. */}
        <a className="gx-login__home" href="/" aria-label={t("auth.loginHome")}>
          <span className="gx-login__sigil" aria-hidden="true">
            <Dices size={24} />
          </span>
          <h1 className="gx-login__wordmark gx-gradient-text">Glyphoxa</h1>
        </a>
        <p className="gx-login__lede">
          {open ? t("auth.ledeOpen") : t("auth.ledeAllowlist")}
        </p>

        {notAuthorized && (
          <p className="gx-login__error" role="alert">
            {inviteOnly ? t("auth.notAuthorizedInviteOnly") : t("auth.notAuthorized")}
          </p>
        )}

        {aupRequired && (
          <p className="gx-login__error" role="alert">
            {t("auth.aupRequired")}
          </p>
        )}

        {open && <AupConsent accepted={accepted} onChange={setAccepted} />}

        {/* An anchor cannot be `disabled`, so the blocked state is rendered as a
            real disabled button — aria-disabled alone would still navigate on
            click, and a signup that slips past the acknowledgment is the one
            thing this gate exists to prevent. In open mode the start link
            carries ?aup=1 — the server refuses an open-mode start without it
            and the callback records the acceptance time (#518), so the
            checkbox is enforced, not just rendered. */}
        {blocked ? (
          <Button variant="primary" size="lg" block disabled aria-describedby={reasonId}>
            {t("auth.continueDiscord")}
          </Button>
        ) : (
          <a
            className="gx-btn gx-btn--primary gx-btn--lg gx-btn--block"
            href={open ? "/auth/discord/login?aup=1" : "/auth/discord/login"}
          >
            {t("auth.continueDiscord")}
          </a>
        )}
        {reason && (
          <p className="gx-login__reason" id={reasonId}>
            {reason}
          </p>
        )}

        <a className="gx-login__about" href="/">
          {t("auth.whatIsGlyphoxa")}
        </a>
      </div>

      <LegalFooter />
      {/* The display-language picker sits with the legal footer, OUTSIDE the
          session: a German visitor must be able to switch before signing in. */}
      <LanguageSwitcher />
    </div>
  );
}
