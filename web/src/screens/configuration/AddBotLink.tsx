import { ExternalLink } from "lucide-react";

import { Button } from "@/components/ui/Button";
import { useI18n } from "@/i18n";

// AddBotLink — the Configuration Discord card's "Add Glyphoxa to your server"
// action (#110). Adding the Bot to a Guild is a SEPARATE, prerequisite step from
// saving the Server ID: neither pasted-link format joins the Bot, so a voice
// session cannot join voice until the operator authorizes it here.
//
// The URL is built entirely from the server-provided Discord application id (the
// same app that backs operator login, ADR-0016) — nothing secret is hardcoded.
// With no application id (DISCORD_OAUTH_CLIENT_ID unset) the action is disabled
// with an explanatory note instead of producing a broken link.

// botAuthorizeUrl composes Discord's bot-authorization URL for one application.
// scope=bot pulls the Bot into the Guild; applications.commands registers the
// /glyphoxa slash commands in a fresh server; permissions 3181568 = View Channel
// (0x400) + Send Messages (0x800) + Attach Files (0x8000) + Connect (0x100000) +
// Speak (0x200000). Connect + Speak are the voice join; Send Messages carries the
// session-start transcription/consent notice and the Butler's text answers into
// the voice channel's chat; Attach Files carries shared highlight clips. No Embed
// Links: the Bot posts no embeds.
export function botAuthorizeUrl(applicationId: string): string {
  return `https://discord.com/oauth2/authorize?client_id=${encodeURIComponent(applicationId)}&scope=bot%20applications.commands&permissions=3181568`;
}

// The always-visible copy (config.addBotCopy) marks this a prerequisite for
// voice-join: it is step 1 of the Discord card, before linking the server.
//
// The anchor wears the Button classes because the Button primitive is
// button-only; a real <a> is needed to open Discord in a new tab. As step 1
// it is the card's primary action (variant "primary").
export function AddBotLink({
  applicationId,
  variant = "secondary",
}: {
  applicationId: string;
  variant?: "primary" | "secondary";
}) {
  const { t } = useI18n();
  return (
    <div className="gx-add-bot">
      <p className="gx-add-bot__copy">{t("config.addBotCopy")}</p>
      {applicationId ? (
        <a
          className={`gx-btn gx-btn--${variant} gx-btn--sm`}
          href={botAuthorizeUrl(applicationId)}
          target="_blank"
          rel="noopener noreferrer"
        >
          <span className="gx-btn__icon">
            <ExternalLink size={16} />
          </span>
          {t("config.addBotAction")}
        </a>
      ) : (
        <>
          <Button variant={variant} size="sm" disabled iconStart={<ExternalLink size={16} />}>
            {t("config.addBotAction")}
          </Button>
          <p className="gx-add-bot__note">{t("config.addBotNoAppId")}</p>
        </>
      )}
    </div>
  );
}
