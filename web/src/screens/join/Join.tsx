import { useId, useState, type ReactNode } from "react";
import { Dices } from "lucide-react";
import { useQuery } from "@connectrpc/connect-query";

import {
  AuthService,
  InvitationKind,
  type GetInvitationResponse,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Button } from "@/components/ui/Button";
import { LegalFooter } from "@/components/LegalFooter";
import { LanguageSwitcher } from "@/components/LanguageSwitcher";
import { useI18n } from "@/i18n";
import { describeGuestRights } from "@/lib/guestRights";
import { joinOAuthStart } from "@/lib/invitationLink";
import { useDocumentTitle } from "@/lib/useDocumentTitle";
import { AupConsent } from "@/screens/login/AupConsent";

import "@/screens/login/login.css";
import "./join.css";

// The join page: the public face of an invitation link, /join/<token>
// (ADR-0065). It lives OUTSIDE the tenant shell and the AuthGate, like /login —
// whoever opens the link has no session yet — and reuses the login card.
//
// One read, AuthService.GetInvitation, says what the link is. The server
// answers `valid = false` for an unknown token and a dead link alike, so the
// page has exactly three faces:
//
//   an access invitation    a Platform Admin lets someone onto the instance;
//                           they get a workspace (a table) of their own
//   a campaign invitation   a GM lets someone into one Campaign as a Campaign
//                           Guest, with the Guest Rights listed here
//   a dead link             said calmly, with the way to the start page
//
// Joining makes a new account holder whatever the deployment's Admission Mode,
// so both live faces carry the Nutzungsbedingungen / Datenschutzerklärung
// acknowledgment (#518), and the Discord start — a full-page link into the
// OAuth carve-out, carrying the token and aup=1 — is a disabled button until
// it is ticked. The server enforces the same and bounces a refused join back
// here: ?error=aup_required (not acknowledged) or ?error=invalid (the link
// stopped working, or is pinned to a different Discord account).
export function Join({
  token,
  refused = false,
  aupRequired = false,
}: {
  token: string;
  /** The OAuth callback refused this join (?error=invalid). */
  refused?: boolean;
  /** The server-side acknowledgment gate bounced the start here (?error=aup_required). */
  aupRequired?: boolean;
}) {
  const { t } = useI18n();
  useDocumentTitle(t("access.joinDocTitle"));
  // retry off: a link is either readable now or the visitor presses Retry;
  // backing off silently would leave them on a blank card.
  const invitation = useQuery(AuthService.method.getInvitation, { token }, { retry: false });

  if (invitation.isPending) {
    return <div className="gx-auth-boot" aria-busy="true" aria-label={t("common.loading")} />;
  }

  if (invitation.isError) {
    return (
      <JoinCard>
        <p className="gx-login__error" role="alert">
          {t("access.joinLoadError")}
        </p>
        <Button
          variant="secondary"
          block
          loading={invitation.isFetching}
          onClick={() => void invitation.refetch()}
        >
          {t("common.retry")}
        </Button>
      </JoinCard>
    );
  }

  const live = liveInvitation(invitation.data);
  if (!live) {
    return (
      <JoinCard>
        <h2 className="gx-join__title">{t("access.joinDeadTitle")}</h2>
        {/* After a refused sign-in the preview cannot tell which of the two it
            was, so the sentence names both. */}
        <p className="gx-login__lede">{t(refused ? "access.joinDeadOtherAccount" : "access.joinDeadDesc")}</p>
        <a className="gx-btn gx-btn--secondary gx-btn--block" href="/">
          {t("access.joinHome")}
        </a>
      </JoinCard>
    );
  }

  return <LiveInvitation token={token} invitation={live} refused={refused} aupRequired={aupRequired} />;
}

// A valid invitation of a kind this build knows. Anything else — `valid =
// false`, or a kind a newer server added — is a link this page cannot honour.
type Live = GetInvitationResponse & { kind: InvitationKind.ACCESS | InvitationKind.CAMPAIGN };

function liveInvitation(res: GetInvitationResponse): Live | null {
  if (!res.valid) return null;
  if (res.kind !== InvitationKind.ACCESS && res.kind !== InvitationKind.CAMPAIGN) return null;
  return res as Live;
}

function LiveInvitation({
  token,
  invitation,
  refused,
  aupRequired,
}: {
  token: string;
  invitation: Live;
  refused: boolean;
  aupRequired: boolean;
}) {
  const { t } = useI18n();
  const [accepted, setAccepted] = useState(false);
  const reasonId = useId();
  const inviter = invitation.inviterName.trim();
  const campaign = invitation.kind === InvitationKind.CAMPAIGN;
  const rights = describeGuestRights(invitation.rights);

  const title = campaign
    ? inviter
      ? t("access.joinCampaignTitle", { inviter, campaign: invitation.campaignName })
      : t("access.joinCampaignTitleAnon", { campaign: invitation.campaignName })
    : inviter
      ? t("access.joinAccessTitle", { inviter })
      : t("access.joinAccessTitleAnon");

  return (
    <JoinCard>
      <h2 className="gx-join__title">{title}</h2>

      {/* The link still previews as valid, so the visitor can try again — with
          the right Discord account, if that was it. */}
      {refused && (
        <p className="gx-login__error" role="alert">
          {t("access.joinErrorInvalid")}
        </p>
      )}
      {aupRequired && (
        <p className="gx-login__error" role="alert">
          {t("access.joinAupRequired")}
        </p>
      )}

      {campaign ? (
        <div className="gx-join__what">
          {invitation.tenantName && (
            <p className="gx-login__lede">{t("access.joinCampaignHost", { tenant: invitation.tenantName })}</p>
          )}
          {rights.length > 0 ? (
            <>
              <p className="gx-join__rights-title">{t("access.joinRightsTitle")}</p>
              <ul className="gx-join__rights">
                {rights.map((r) => (
                  <li key={r.right}>
                    <span className="gx-join__right">{t(r.label)}</span>
                    <span className="gx-join__right-desc">{t(r.description)}</span>
                  </li>
                ))}
              </ul>
            </>
          ) : (
            <p className="gx-login__explainer">{t("access.joinNoRights")}</p>
          )}
        </div>
      ) : (
        <p className="gx-login__lede">{t("access.joinAccessGet")}</p>
      )}

      <AupConsent accepted={accepted} onChange={setAccepted} />

      {/* As on the login screen: an anchor cannot be disabled, so the blocked
          state is a real disabled button — an aria-disabled link would still
          navigate, past the acknowledgment. */}
      {accepted ? (
        <a className="gx-btn gx-btn--primary gx-btn--lg gx-btn--block" href={joinOAuthStart(token)}>
          {t("auth.continueDiscord")}
        </a>
      ) : (
        <Button variant="primary" size="lg" block disabled aria-describedby={reasonId}>
          {t("auth.continueDiscord")}
        </Button>
      )}
      <p className="gx-login__reason" id={reasonId}>
        {accepted ? t("access.joinSignInNote") : t("auth.continueNeedsConsent")}
      </p>
    </JoinCard>
  );
}

// The login card around every face of the page: the wordmark (the way to the
// start page, and the page's one h1), then the content; the legal footer and
// the language picker under it, as on /login.
function JoinCard({ children }: { children: ReactNode }) {
  const { t } = useI18n();
  return (
    <div className="gx-login gx-join">
      <div className="gx-login__card">
        <a className="gx-login__home" href="/" aria-label={t("auth.loginHome")}>
          <span className="gx-login__sigil" aria-hidden="true">
            <Dices size={24} />
          </span>
          <h1 className="gx-login__wordmark gx-gradient-text">Glyphoxa</h1>
        </a>
        {children}
      </div>

      <LegalFooter />
      <LanguageSwitcher />
    </div>
  );
}
