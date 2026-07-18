import { Dices } from "lucide-react";
import { useQuery } from "@connectrpc/connect-query";

import { AuthService, AdmissionMode } from "@gen/glyphoxa/management/v1/management_pb";
import { Button } from "@/components/ui/Button";

import "./login.css";

// The Login screen (ADR-0016: Discord-only OAuth). No design handoff exists for
// it, so this is the minimal gate the self-host operator needs: a "Continue with
// Discord" link that starts the net/http OAuth carve-out (/auth/discord/login),
// with Google/GitHub rendered DISABLED + "coming soon" (wired in v1.5+). It is a
// full-page link, not a Connect call — OAuth is HTML redirects (ADR-0015).
//
// notAuthorized surfaces the operator-allowlist rejection (ADR-0041): the OAuth
// callback bounces a Discord User who is not on GLYPHOXA_OPERATOR_IDS back here
// with ?error=not_authorized. The banner is non-leaky — it never echoes the
// rejected account's id. A normal first visit renders unchanged.
//
// The lede frames signup per the deployment's Admission Mode (ADR-0055):
// GetAdmissionMode is public (there is no session yet on this screen), and only
// an explicit `open` answer switches the copy to self-signup framing — while
// the probe is loading or errored the screen fail-safes to today's allowlist
// framing rather than advertising a signup the deployment may not allow. Only
// the copy changes: the OAuth start anchor stays exactly as is in both modes.
export function Login({ notAuthorized = false }: { notAuthorized?: boolean }) {
  const { data } = useQuery(AuthService.method.getAdmissionMode, {}, { retry: false });
  const open = data?.admissionMode === AdmissionMode.OPEN;

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
            This Discord account isn&apos;t on the operator allowlist for this instance.
          </p>
        )}

        <a className="gx-btn gx-btn--primary gx-btn--lg gx-btn--block" href="/auth/discord/login">
          Continue with Discord
        </a>

        <div className="gx-login__soon">
          <Button variant="secondary" block disabled>
            Continue with Google · coming soon
          </Button>
          <Button variant="secondary" block disabled>
            Continue with GitHub · coming soon
          </Button>
        </div>
      </div>
    </div>
  );
}
