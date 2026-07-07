import { ExternalLink } from "lucide-react";

import { Button } from "@/components/ui/Button";

// AddBotLink — the Configuration Discord card's "Add Glyphoxa to your server"
// action (#110). Adding the Bot to a Guild is a SEPARATE, prerequisite step from
// saving the Guild / Voice channel IDs: neither pasted-link format joins the Bot,
// so a Voice Session cannot join voice until the operator authorizes it here.
//
// The URL is built entirely from the server-provided Discord application id (the
// same app that backs operator login, ADR-0016) — nothing secret is hardcoded.
// With no application id (DISCORD_OAUTH_CLIENT_ID unset) the action is disabled
// with an explanatory note instead of producing a broken link.

// botAuthorizeUrl composes Discord's bot-authorization URL for one application.
// scope=bot pulls the Bot into the Guild; applications.commands registers the
// /glyphoxa slash commands in a fresh guild; permissions 3146752 = View Channel
// (0x400) + Connect (0x100000) + Speak (0x200000) — the minimum a voice-join Bot
// needs.
export function botAuthorizeUrl(applicationId: string): string {
  return `https://discord.com/oauth2/authorize?client_id=${encodeURIComponent(applicationId)}&scope=bot%20applications.commands&permissions=3146752`;
}

// The always-visible copy distinguishing this step from saving the IDs and
// marking it a prerequisite for voice-join.
const COPY =
  "Adding the Bot to a server is a separate step from saving the IDs below — the Bot must be a member before a Voice Session can join voice.";

// The anchor wears the Button classes because the Button primitive is
// button-only; a real <a> is needed to open Discord in a new tab.
const BTN_CLASS = "gx-btn gx-btn--secondary gx-btn--sm";

export function AddBotLink({ applicationId }: { applicationId: string }) {
  return (
    <div className="gx-add-bot">
      <p className="gx-add-bot__copy">{COPY}</p>
      {applicationId ? (
        <a className={BTN_CLASS} href={botAuthorizeUrl(applicationId)} target="_blank" rel="noopener noreferrer">
          <span className="gx-btn__icon">
            <ExternalLink size={16} />
          </span>
          Add Glyphoxa to your server
        </a>
      ) : (
        <>
          <Button variant="secondary" size="sm" disabled iconStart={<ExternalLink size={16} />}>
            Add Glyphoxa to your server
          </Button>
          <p className="gx-add-bot__note">
            No Discord application id is configured (DISCORD_OAUTH_CLIENT_ID), so this link can&apos;t be built.
          </p>
        </>
      )}
    </div>
  );
}
