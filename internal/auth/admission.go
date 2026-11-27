package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// Admission Mode (ADR-0055): how a deployment admits web identities.
//   - allowlist: the operator allowlist names the Platform Admins; everyone
//     else is admitted from the web (ADR-0065) — an Access Grant, or an access
//     invitation link that creates one. Nobody else gets in.
//   - open:      any Discord User completing OAuth is admitted; a stranger's
//     signup founds a fresh Tenant (create-only). The allowlist survives as
//     the platform-administration list, not as the admission gate.
//
// Campaign Guests (ADR-0065 lane 2) are admitted in BOTH modes: by redeeming a
// campaign invitation link, or because they already are a guest somewhere.
// Neither ever founds a Tenant.
//
// Distinct from Mode, the process role (-mode voice|web|all). GLYPHOXA_DEV_MODE
// preempts Admission Mode entirely (the dev boot never consults it).

// AdmissionMode is the validated admission posture vocabulary.
type AdmissionMode string

const (
	// AdmissionAllowlist is the default: ADR-0041 behavior, unchanged.
	AdmissionAllowlist AdmissionMode = "allowlist"
	// AdmissionOpen admits any Discord User completing OAuth (ADR-0055).
	AdmissionOpen AdmissionMode = "open"
)

// ParseAdmissionMode validates an admission-mode string (trimmed, lowercased).
// Unlike the log-format enum precedent, an unknown value is a loud error, not a
// silent default — a typo'd posture must never silently run allowlist-locked or,
// worse, open. The empty string is also an error: callers decide what "unset"
// means (the boot falls back to the persisted posture, then to allowlist).
func ParseAdmissionMode(s string) (AdmissionMode, error) {
	switch AdmissionMode(strings.ToLower(strings.TrimSpace(s))) {
	case AdmissionAllowlist:
		return AdmissionAllowlist, nil
	case AdmissionOpen:
		return AdmissionOpen, nil
	}
	return "", fmt.Errorf("auth: invalid admission mode %q (want %q or %q)",
		s, AdmissionAllowlist, AdmissionOpen)
}

// SignupProvisioner is the extra persistence open-mode admission needs: the
// ADR-0055 all-or-nothing signup transaction. *storage.Store satisfies it.
type SignupProvisioner interface {
	ProvisionSignup(ctx context.Context, p storage.SignupParams) (storage.SignupResult, error)
}

// AccessStore is the persistence `allowlist`-mode admission needs beyond the
// Platform Admin list (ADR-0065): who holds an Access Grant, and the atomic
// redemption of an access invitation link into one. *storage.Store satisfies
// it.
type AccessStore interface {
	GetAccessGrant(ctx context.Context, discordUserID string) (storage.AccessGrant, error)
	RedeemAccessInvitation(ctx context.Context, token, discordUserID string) (storage.AccessGrant, error)
	GetUserByDiscordID(ctx context.Context, discordUserID string) (storage.User, error)
}

// GuestAdmissionStore is the persistence Campaign Guest admission needs
// (ADR-0065 lane 2): what kind of invitation a join token names, the atomic
// redemption of a campaign invitation into a guest row, and whether a Discord
// User already is a guest somewhere. *storage.Store satisfies it.
type GuestAdmissionStore interface {
	GetInvitationByToken(ctx context.Context, token string) (storage.Invitation, error)
	RedeemCampaignInvitation(ctx context.Context, token string, p storage.CampaignRedemption) (storage.CampaignRedeemed, error)
	GuestStandingOf(ctx context.Context, discordUserID string) (storage.GuestStanding, error)
}

// Admission is the OAuth callback's admission policy (ADR-0055 / ADR-0065).
// The zero value fails closed: allowlist posture with an empty Platform Admin
// list and no way to admit anyone else. In AdmissionOpen, Signup and
// SignupPlanSlug must be set (the boot preflight guarantees the slug resolves
// to a live plan); a nil Signup falls back to rejecting strangers with a loud
// log rather than admitting people it cannot provision. In AdmissionAllowlist,
// Access and Signup together admit Access Grant holders — a nil of either
// admits Platform Admins only, exactly ADR-0041. Guests admits Campaign Guests
// in both modes; nil admits none.
type Admission struct {
	Mode AdmissionMode
	// Allowlist is the Platform Admin list (GLYPHOXA_OPERATOR_IDS).
	Allowlist      OperatorAllowlist
	SignupPlanSlug string
	Signup         SignupProvisioner
	Access         AccessStore
	Guests         GuestAdmissionStore
}

// open reports whether this policy actually admits strangers: open mode with a
// working provisioner.
func (a Admission) open() bool {
	return a.Mode == AdmissionOpen && a.Signup != nil
}

// grants reports whether this policy admits people from the web-managed
// access tables: allowlist posture with both stores it needs. Open mode never
// consults grants — self-signup admits there.
func (a Admission) grants() bool {
	return a.Mode != AdmissionOpen && a.Signup != nil && a.Access != nil
}

// admissionRoute is how the OAuth callback lets a Discord User in — or that it
// does not.
type admissionRoute int

const (
	// admitNobody is the zero value on purpose: a decision nobody filled in
	// rejects.
	admitNobody admissionRoute = iota
	// admitPlatformAdmin is the ADR-0041 claim-or-create login.
	admitPlatformAdmin
	// admitOpenSignup is ADR-0055 self-signup (create-only, Plan bound).
	admitOpenSignup
	// admitGranted is an Access Grant holder (ADR-0065): create-only, no Plan,
	// the Tenant's Platform Keys switch taken from the grant.
	admitGranted
	// admitCampaignJoin is a join whose token names a CAMPAIGN invitation
	// (ADR-0065 lane 2). The callback redeems it — the redemption needs the
	// Discord identity, and writes the user and the guest row in one
	// transaction — and never founds a Tenant, whoever the redeemer is.
	admitCampaignJoin
	// admitGuest is a Discord User who already is a Campaign Guest and came
	// with nothing else that admits them: a session acting in guestCampaign,
	// no Tenant founded.
	admitGuest
)

// rejection says why a Discord User was not admitted, so the callback can
// send them somewhere that explains it without leaking more than it must.
type rejection int

const (
	// rejectNotAuthorized is the generic refusal.
	rejectNotAuthorized rejection = iota
	// rejectAUPRequired: a join without the Nutzungsbedingungen/Datenschutz
	// acknowledgment. Nothing was redeemed; ticking the box and retrying works.
	rejectAUPRequired
	// rejectSuspended: a suspended user tried to redeem a link.
	rejectSuspended
)

// admissionDecision is the outcome of [Admission.decide].
type admissionDecision struct {
	route admissionRoute
	// platformKeys is the Access Grant's choice (admitGranted only).
	platformKeys bool
	// guestCampaign is the Campaign the session starts in (admitGuest only).
	guestCampaign uuid.UUID
	// why is set when route is admitNobody.
	why rejection
}

// decide is the callback's admission decision (ADR-0065), an ordered walk in
// which the first step that claims the Discord User wins:
//
//  0. a join whose token names a CAMPAIGN invitation — in every Admission
//     Mode and for everybody, a Platform Admin included: they opened a link to
//     someone's Campaign, so that is where they go. This step is terminal: if
//     the link does not admit them the join fails, it never falls through to
//     a step that would found a Tenant;
//  1. a Platform Admin — in every Admission Mode;
//  2. `open` mode: someone who JOINED a Campaign as a guest (redeemed a
//     campaign link at some point) and operates no Tenant signs in as that
//     guest; everyone else self-signs-up (grants and access invitations are
//     not consulted);
//  3. the holder of an Access Grant;
//  4. someone redeeming a live ACCESS invitation, which creates their grant;
//  5. someone who already is a Campaign Guest;
//  6. nobody else.
//
// Step 2's guest rule is deliberate: on an `open` deployment a returning guest
// would otherwise found a Tenant the moment they signed in, which is not what
// "open the campaign I was invited to" means. A guest founding their own
// Tenant later is not built (ADR-0065).
//
// It applies only to a guest who chose to be one ([storage.GuestStanding]
// Joined). A GM can add ANY Discord User ID as a guest, without that person's
// consent and before they ever signed in; if that alone routed them to the
// guest sign-in, any GM could keep anybody from founding a Tenant on an open
// deployment, for good. Someone merely added therefore self-signs-up like
// everyone else, and reaches the Campaign they were added to from the
// workspace switcher. Step 5 (`allowlist`) has no such split: there the guest
// row is the only thing that admits them at all.
//
// joinToken is the invitation token the join cookie carried ("" for an
// ordinary login) and aupAccepted whether this round trip acknowledged the
// Nutzungsbedingungen. A redemption is refused without the acknowledgment
// BEFORE it is attempted, so the refusal never spends a use.
//
// An error is a store failure: the caller answers 500 and admits nobody.
func (a Admission) decide(ctx context.Context, discordUserID, joinToken string, aupAccepted bool) (admissionDecision, error) {
	if joinToken != "" && a.Guests != nil {
		switch inv, err := a.Guests.GetInvitationByToken(ctx, joinToken); {
		case err == nil && inv.Kind == storage.InvitationCampaign:
			if !aupAccepted {
				return admissionDecision{why: rejectAUPRequired}, nil
			}
			return admissionDecision{route: admitCampaignJoin}, nil
		case err != nil && !errors.Is(err, storage.ErrNotFound):
			return admissionDecision{}, fmt.Errorf("auth: admission: read invitation: %w", err)
		}
	}
	if a.Allowlist.Contains(discordUserID) {
		return admissionDecision{route: admitPlatformAdmin}, nil
	}
	if a.open() {
		standing, isGuest, err := a.guestStanding(ctx, discordUserID)
		if err != nil {
			return admissionDecision{}, err
		}
		if isGuest && standing.Joined && !standing.OperatesTenant {
			return admissionDecision{route: admitGuest, guestCampaign: standing.CampaignID}, nil
		}
		return admissionDecision{route: admitOpenSignup}, nil
	}

	if a.grants() {
		switch g, err := a.Access.GetAccessGrant(ctx, discordUserID); {
		case err == nil:
			return admissionDecision{route: admitGranted, platformKeys: g.PlatformKeys}, nil
		case !errors.Is(err, storage.ErrNotFound):
			return admissionDecision{}, fmt.Errorf("auth: admission: read access grant: %w", err)
		}

		if joinToken != "" {
			if !aupAccepted {
				return admissionDecision{why: rejectAUPRequired}, nil
			}
			// A suspended user is refused before the redemption, not after it by
			// the provisioning transaction: otherwise their attempt would burn a
			// use of the link and leave them a grant.
			switch u, err := a.Access.GetUserByDiscordID(ctx, discordUserID); {
			case err == nil && u.SuspendedAt != nil:
				return admissionDecision{why: rejectSuspended}, nil
			case err != nil && !errors.Is(err, storage.ErrNotFound):
				return admissionDecision{}, fmt.Errorf("auth: admission: read user: %w", err)
			}
			switch g, err := a.Access.RedeemAccessInvitation(ctx, joinToken, discordUserID); {
			case err == nil:
				return admissionDecision{route: admitGranted, platformKeys: g.PlatformKeys}, nil
			case !errors.Is(err, storage.ErrInvitationInvalid):
				return admissionDecision{}, fmt.Errorf("auth: admission: redeem access invitation: %w", err)
			}
		}
	}

	// An existing Campaign Guest, whatever else they are. Someone who also
	// operates a Tenant but holds no grant for it lands in the guest
	// workspace: [Workspaces] refuses them their own Tenant on every request.
	standing, isGuest, err := a.guestStanding(ctx, discordUserID)
	if err != nil {
		return admissionDecision{}, err
	}
	if isGuest {
		return admissionDecision{route: admitGuest, guestCampaign: standing.CampaignID}, nil
	}
	return admissionDecision{}, nil
}

// guestStanding reads whether the Discord User is a Campaign Guest anywhere.
// No Guests store means nobody is.
func (a Admission) guestStanding(ctx context.Context, discordUserID string) (storage.GuestStanding, bool, error) {
	if a.Guests == nil {
		return storage.GuestStanding{}, false, nil
	}
	switch st, err := a.Guests.GuestStandingOf(ctx, discordUserID); {
	case err == nil:
		return st, true, nil
	case errors.Is(err, storage.ErrNotFound):
		return storage.GuestStanding{}, false, nil
	default:
		return storage.GuestStanding{}, false, fmt.Errorf("auth: admission: read guest standing: %w", err)
	}
}
