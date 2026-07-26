import { useId, useState } from "react";
import { Dices } from "lucide-react";
import { useQuery } from "@connectrpc/connect-query";

import { AuthService, AdmissionMode } from "@gen/glyphoxa/management/v1/management_pb";
import { Button } from "@/components/ui/Button";
import { LegalFooter } from "@/components/LegalFooter";

import "./login.css";

// The Login screen (ADR-0016: Discord-only OAuth). No design handoff exists for
// it, so this is the minimal gate the self-host operator needs: a "Continue with
// Discord" link that starts the net/http OAuth carve-out (/auth/discord/login),
// with Google/GitHub rendered DISABLED + "coming soon" (wired in v1.5+). It is a
// full-page link, not a Connect call — OAuth is HTML redirects (ADR-0015).
//
// notAuthorized surfaces an admission rejection: the OAuth callback bounces a
// denied Discord User back here with ?error=not_authorized. The signal is
// mode-broad — an allowlist miss (ADR-0041) or, in open admission mode, a
// suspended account (ADR-0055) — and DELIBERATELY undifferentiated: the banner
// is non-leaky, never echoing the rejected account's id or WHY it was denied,
// so the copy must stay mode-neutral. A normal first visit renders unchanged.
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
export function Login({ notAuthorized = false }: { notAuthorized?: boolean }) {
  const { data, status } = useQuery(AuthService.method.getAdmissionMode, {}, { retry: false });
  const open = data?.admissionMode === AdmissionMode.OPEN;
  const [accepted, setAccepted] = useState(false);
  const aupId = useId();
  // Open mode = signing up: the acknowledgment gates the OAuth start. While the
  // probe is still IN FLIGHT the start is briefly disabled too — otherwise the
  // first render on an open deployment shows an ungated anchor and a fast click
  // slips past the acknowledgment. An ERRORED probe stays fail-open with the
  // allowlist framing (a flaky probe must not lock a legitimate operator out).
  const blocked = status === "pending" || (open && !accepted);

  return (
    <div className="gx-login">
      <div className="gx-login__card">
        <span className="gx-login__sigil">
          <Dices size={24} />
        </span>
        <h1 className="gx-login__wordmark gx-gradient-text">Glyphoxa</h1>
        <p className="gx-login__lede">
          {open
            ? "Sign in with Discord — your first sign-in creates your own table."
            : "Sign in to run your table."}
        </p>

        {notAuthorized && (
          <p className="gx-login__error" role="alert">
            This Discord account isn&apos;t authorized for this deployment.
          </p>
        )}

        {open && (
          <label className="gx-login__consent" htmlFor={aupId}>
            <input
              id={aupId}
              type="checkbox"
              checked={accepted}
              onChange={(e) => setAccepted(e.target.checked)}
            />
            <span>
              I agree to the <a href="/terms">Nutzungsbedingungen</a> and have read the{" "}
              <a href="/privacy">Datenschutzerklärung</a>.
            </span>
          </label>
        )}

        {/* An anchor cannot be `disabled`, so the blocked state is rendered as a
            real disabled button — aria-disabled alone would still navigate on
            click, and a signup that slips past the acknowledgment is the one
            thing this gate exists to prevent. */}
        {blocked ? (
          <Button variant="primary" size="lg" block disabled>
            Continue with Discord
          </Button>
        ) : (
          <a className="gx-btn gx-btn--primary gx-btn--lg gx-btn--block" href="/auth/discord/login">
            Continue with Discord
          </a>
        )}

        <div className="gx-login__soon">
          <Button variant="secondary" block disabled>
            Continue with Google · coming soon
          </Button>
          <Button variant="secondary" block disabled>
            Continue with GitHub · coming soon
          </Button>
        </div>
      </div>

      <LegalFooter />
    </div>
  );
}
