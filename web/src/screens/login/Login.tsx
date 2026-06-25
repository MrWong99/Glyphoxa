import { Dices } from "lucide-react";

import { Button } from "@/components/ui/Button";

import "./login.css";

// The Login screen (ADR-0016: Discord-only OAuth). No design handoff exists for
// it, so this is the minimal gate the self-host operator needs: a "Continue with
// Discord" link that starts the net/http OAuth carve-out (/auth/discord/login),
// with Google/GitHub rendered DISABLED + "coming soon" (wired in v1.5+). It is a
// full-page link, not a Connect call — OAuth is HTML redirects (ADR-0015).
export function Login() {
  return (
    <div className="gx-login">
      <div className="gx-login__card">
        <span className="gx-login__sigil">
          <Dices size={24} />
        </span>
        <h1 className="gx-login__wordmark gx-gradient-text">Glyphoxa</h1>
        <p className="gx-login__lede">Sign in to run your table.</p>

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
