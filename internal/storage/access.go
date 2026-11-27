package storage

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Web-managed instance access (ADR-0065): Access Grants, the shareable
// invitation links that create them, and the Platform Admin's cross-tenant
// reads. Everything in this file is DEPLOYMENT-scoped — it deliberately takes
// no tenant id, because its only callers are the OAuth admission path and the
// Platform-Admin-gated AdminService. Never reach it from a tenant-scoped
// handler.

// Invitation kinds (the invitation.kind CHECK vocabulary).
const (
	// InvitationAccess admits its redeemer to the instance: an Access Grant.
	InvitationAccess = "access"
	// InvitationCampaign makes its redeemer a Campaign Guest.
	InvitationCampaign = "campaign"
)

// ErrInvitationInvalid reports an invitation that cannot be redeemed: unknown
// token, revoked, expired, used up, or pinned to a different Discord User. One
// sentinel for all five on purpose — the redeemer learns only "this link does
// not work", never which links exist.
var ErrInvitationInvalid = errors.New("storage: invitation is not valid")

// Invitation is one `/join/<token>` link. It never carries the token: only the
// digest is stored, and the plaintext exists once, in the mint response.
type Invitation struct {
	ID            uuid.UUID
	Kind          string
	Label         string
	CampaignID    *uuid.UUID
	Rights        []string
	PlatformKeys  bool
	DiscordUserID string
	MaxUses       *int
	Uses          int
	ExpiresAt     *time.Time
	CreatedBy     *uuid.UUID
	CreatedAt     time.Time
	RevokedAt     *time.Time
}

// Live reports whether the invitation can still be redeemed at now.
func (i Invitation) Live(now time.Time) bool {
	switch {
	case i.RevokedAt != nil:
		return false
	case i.ExpiresAt != nil && !now.Before(*i.ExpiresAt):
		return false
	case i.MaxUses != nil && i.Uses >= *i.MaxUses:
		return false
	}
	return true
}

const invitationColumns = `id, kind, label, campaign_id, rights, platform_keys, discord_user_id,
	max_uses, uses, expires_at, created_by, created_at, revoked_at`

func scanInvitation(row pgx.Row) (Invitation, error) {
	var i Invitation
	err := row.Scan(&i.ID, &i.Kind, &i.Label, &i.CampaignID, &i.Rights, &i.PlatformKeys,
		&i.DiscordUserID, &i.MaxUses, &i.Uses, &i.ExpiresAt, &i.CreatedBy, &i.CreatedAt, &i.RevokedAt)
	return i, err
}

// NewInvitation is the input to CreateInvitation. Token is the plaintext
// secret the auth tier minted; only its digest is persisted.
type NewInvitation struct {
	Kind          string
	Token         string
	Label         string
	CampaignID    *uuid.UUID
	Rights        []string
	PlatformKeys  bool
	DiscordUserID string
	MaxUses       *int
	ExpiresAt     *time.Time
	CreatedBy     uuid.UUID
}

// CreateInvitation persists a freshly minted invitation link.
func (s *Store) CreateInvitation(ctx context.Context, n NewInvitation) (Invitation, error) {
	if n.Token == "" {
		return Invitation{}, errors.New("storage: create invitation: empty token")
	}
	rights := n.Rights
	if rights == nil {
		rights = []string{}
	}
	row := s.db.QueryRow(ctx,
		`INSERT INTO invitation (kind, token_hash, label, campaign_id, rights, platform_keys,
		                         discord_user_id, max_uses, expires_at, created_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		 RETURNING `+invitationColumns,
		n.Kind, sessionTokenHash(n.Token), n.Label, n.CampaignID, rights, n.PlatformKeys,
		strings.TrimSpace(n.DiscordUserID), n.MaxUses, n.ExpiresAt, n.CreatedBy)
	inv, err := scanInvitation(row)
	if err != nil {
		return Invitation{}, fmt.Errorf("storage: create invitation: %w", err)
	}
	return inv, nil
}

// GetInvitationByToken loads an invitation by its plaintext token, or
// ErrNotFound. It returns revoked/expired rows too — callers check
// [Invitation.Live] — so the public preview can tell "not a link" from "a link
// that stopped working" without a second query.
func (s *Store) GetInvitationByToken(ctx context.Context, token string) (Invitation, error) {
	if token == "" {
		return Invitation{}, ErrNotFound
	}
	row := s.db.QueryRow(ctx,
		`SELECT `+invitationColumns+` FROM invitation WHERE token_hash = $1`, sessionTokenHash(token))
	inv, err := scanInvitation(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Invitation{}, ErrNotFound
	}
	if err != nil {
		return Invitation{}, fmt.Errorf("storage: get invitation by token: %w", err)
	}
	return inv, nil
}

// ListAccessInvitations returns every `access` invitation, newest first —
// revoked and spent ones included, so the admin console shows what happened to
// a link rather than having it vanish.
func (s *Store) ListAccessInvitations(ctx context.Context) ([]Invitation, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+invitationColumns+` FROM invitation WHERE kind = 'access' ORDER BY created_at DESC, id`)
	if err != nil {
		return nil, fmt.Errorf("storage: list access invitations: %w", err)
	}
	defer rows.Close()
	var out []Invitation
	for rows.Next() {
		inv, err := scanInvitation(rows)
		if err != nil {
			return nil, fmt.Errorf("storage: scan access invitation: %w", err)
		}
		out = append(out, inv)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list access invitations: %w", err)
	}
	return out, nil
}

// RevokeAccessInvitation marks an `access` invitation revoked. Revoking an
// already-revoked link is a no-op; an unknown id (or a campaign invitation's
// id — those are revoked through their Campaign) is ErrNotFound. Grants the
// link already created are untouched.
func (s *Store) RevokeAccessInvitation(ctx context.Context, id uuid.UUID) error {
	tag, err := s.db.Exec(ctx,
		`UPDATE invitation SET revoked_at = COALESCE(revoked_at, now())
		  WHERE id = $1 AND kind = 'access'`, id)
	if err != nil {
		return fmt.Errorf("storage: revoke access invitation %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// AccessGrant admits one Discord User to an `allowlist` deployment.
type AccessGrant struct {
	ID            uuid.UUID
	DiscordUserID string
	Note          string
	PlatformKeys  bool
	InvitationID  *uuid.UUID
	CreatedAt     time.Time
}

const accessGrantColumns = `id, discord_user_id, note, platform_keys, invitation_id, created_at`

func scanAccessGrant(row pgx.Row) (AccessGrant, error) {
	var g AccessGrant
	err := row.Scan(&g.ID, &g.DiscordUserID, &g.Note, &g.PlatformKeys, &g.InvitationID, &g.CreatedAt)
	return g, err
}

// GetAccessGrant returns the Discord User's Access Grant, or ErrNotFound.
func (s *Store) GetAccessGrant(ctx context.Context, discordUserID string) (AccessGrant, error) {
	row := s.db.QueryRow(ctx,
		`SELECT `+accessGrantColumns+` FROM access_grant WHERE discord_user_id = $1`, discordUserID)
	g, err := scanAccessGrant(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return AccessGrant{}, ErrNotFound
	}
	if err != nil {
		return AccessGrant{}, fmt.Errorf("storage: get access grant: %w", err)
	}
	return g, nil
}

// GrantAccess admits a Discord User by snowflake (a Platform Admin action).
// Granting again updates the note and the Platform Keys choice. When the user
// already operates a Tenant, that Tenant's platform_keys follows the grant —
// otherwise re-granting someone whose access was revoked would silently keep
// whatever the Tenant had before. Invitation refusals the person carries from
// an earlier revocation are left alone (see refuseInvitations): they are
// inert while this grant exists, and must hold again if it is revoked.
func (s *Store) GrantAccess(ctx context.Context, discordUserID, note string, platformKeys bool, grantedBy uuid.UUID) (AccessGrant, error) {
	var g AccessGrant
	err := s.InTx(ctx, func(tx *Store) error {
		row := tx.db.QueryRow(ctx,
			`INSERT INTO access_grant (discord_user_id, note, platform_keys, granted_by)
			 VALUES ($1, $2, $3, $4)
			 ON CONFLICT (discord_user_id) DO UPDATE
			   SET note = EXCLUDED.note, platform_keys = EXCLUDED.platform_keys
			 RETURNING `+accessGrantColumns,
			discordUserID, note, platformKeys, grantedBy)
		var err error
		if g, err = scanAccessGrant(row); err != nil {
			return fmt.Errorf("storage: grant access to %q: %w", discordUserID, err)
		}
		return tx.syncOperatorPlatformKeys(ctx, discordUserID, platformKeys)
	})
	if err != nil {
		return AccessGrant{}, err
	}
	return g, nil
}

// syncOperatorPlatformKeys makes every Tenant the Discord User already
// operates follow a freshly written Access Grant. A no-op for someone who has
// never signed in — the Tenant they found later takes the grant's value at
// founding (ProvisionSignup).
func (s *Store) syncOperatorPlatformKeys(ctx context.Context, discordUserID string, platformKeys bool) error {
	if _, err := s.db.Exec(ctx,
		`UPDATE tenant t SET platform_keys = $2, updated_at = now()
		   FROM users u
		  WHERE u.discord_user_id = $1 AND t.operator_user_id = u.id
		    AND t.platform_keys <> $2`, discordUserID, platformKeys); err != nil {
		return fmt.Errorf("storage: sync tenant platform keys for %q: %w", discordUserID, err)
	}
	return nil
}

// RevokeAccess removes a Discord User's Access Grant and deletes their
// sessions, so the revocation lands on their very next request rather than at
// the next boot. Their Tenant and its data are kept: granting again restores
// everything. ErrNotFound when no grant exists.
//
// In the same transaction it also:
//   - switches Platform Keys OFF on every Tenant they operate. The web session
//     is not the only way to spend: the Tenant's Discord surface and its
//     Campaign Guests' planning chat resolve keys through tenant.platform_keys,
//     which would otherwise keep the granted value. Re-granting re-syncs the
//     switch from the new grant ([Store.GrantAccess]);
//   - refuses them EVERY access invitation link that exists at this moment
//     (refuseInvitations) — however the grant was made — so no still-live
//     link is a way to undo the revocation by opening it. A link created
//     afterwards, or an explicit [Store.GrantAccess], admits them again.
//
// Their standing as GM on Discord and their Tenant's guests end with the
// grant too — not here, but because every read of them tests the grant
// ([AccessPolicy]).
func (s *Store) RevokeAccess(ctx context.Context, discordUserID string) error {
	return s.InTx(ctx, func(tx *Store) error {
		tag, err := tx.db.Exec(ctx,
			`DELETE FROM access_grant WHERE discord_user_id = $1`, discordUserID)
		if err != nil {
			return fmt.Errorf("storage: revoke access of %q: %w", discordUserID, err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		if err := tx.refuseInvitations(ctx, discordUserID, nil); err != nil {
			return err
		}
		if err := tx.syncOperatorPlatformKeys(ctx, discordUserID, false); err != nil {
			return err
		}
		if _, err := tx.db.Exec(ctx,
			`DELETE FROM sessions USING users
			  WHERE sessions.user_id = users.id AND users.discord_user_id = $1`, discordUserID); err != nil {
			return fmt.Errorf("storage: delete sessions of %q: %w", discordUserID, err)
		}
		return nil
	})
}

// RedeemAccessInvitation turns an `access` invitation into an Access Grant for
// discordUserID, atomically: the invitation row is locked, validated (live,
// and pinned to this user if pinned at all) and its use counted in the same
// transaction as the grant insert, so two redeemers can never share the last
// use. A user who already holds a grant redeems nothing and consumes no use —
// opening the link twice, or a link for someone already admitted, is harmless.
// A redeemer who already operates a Tenant (their earlier access was revoked)
// has it follow the new grant's Platform Keys choice, exactly as GrantAccess
// does. Every refusal is ErrInvitationInvalid — including a redeemer this link
// was refused to because their access was revoked while the link existed
// (refuseInvitations): no use is counted and nothing is written.
//
// The invitation is locked FOR NO KEY UPDATE, not FOR UPDATE: that still
// serializes redeemers (and the `uses` update takes the same lock), but does
// not block the foreign-key share lock a concurrent refusal insert takes on
// the row — FOR UPDATE there deadlocks a removal against a redemption.
func (s *Store) RedeemAccessInvitation(ctx context.Context, token, discordUserID string) (AccessGrant, error) {
	var g AccessGrant
	err := s.InTx(ctx, func(tx *Store) error {
		row := tx.db.QueryRow(ctx,
			`SELECT `+invitationColumns+` FROM invitation
			  WHERE token_hash = $1 AND kind = 'access' FOR NO KEY UPDATE`, sessionTokenHash(token))
		inv, err := scanInvitation(row)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrInvitationInvalid
		}
		if err != nil {
			return fmt.Errorf("storage: lock access invitation: %w", err)
		}
		if !inv.Live(time.Now()) || (inv.DiscordUserID != "" && inv.DiscordUserID != discordUserID) {
			return ErrInvitationInvalid
		}

		existing, err := tx.GetAccessGrant(ctx, discordUserID)
		if err == nil {
			g = existing
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		refused, err := tx.invitationRefused(ctx, inv.ID, discordUserID)
		if err != nil {
			return err
		}
		if refused {
			return ErrInvitationInvalid
		}

		// ON CONFLICT DO NOTHING covers the one race the invitation lock does
		// not: the same person redeeming two DIFFERENT links at once (or being
		// granted by a Platform Admin mid-redeem). The loser waits on the
		// winner's unique-index entry, inserts nothing, and is then exactly
		// the "already holds a grant" case above — no use counted.
		row = tx.db.QueryRow(ctx,
			`INSERT INTO access_grant (discord_user_id, note, platform_keys, granted_by, invitation_id)
			 VALUES ($1, $2, $3, $4, $5)
			 ON CONFLICT (discord_user_id) DO NOTHING
			 RETURNING `+accessGrantColumns,
			discordUserID, inv.Label, inv.PlatformKeys, inv.CreatedBy, inv.ID)
		g, err = scanAccessGrant(row)
		if errors.Is(err, pgx.ErrNoRows) {
			g, err = tx.GetAccessGrant(ctx, discordUserID)
			return err
		}
		if err != nil {
			return fmt.Errorf("storage: create grant from invitation: %w", err)
		}
		if _, err := tx.db.Exec(ctx, `UPDATE invitation SET uses = uses + 1 WHERE id = $1`, inv.ID); err != nil {
			return fmt.Errorf("storage: count invitation use: %w", err)
		}
		return tx.syncOperatorPlatformKeys(ctx, discordUserID, inv.PlatformKeys)
	})
	if err != nil {
		return AccessGrant{}, err
	}
	return g, nil
}

// refuseInvitations notes that no invitation in scope may admit this Discord
// User any more (invitation_refusal, migration 00064). The scope is every
// `access` invitation when campaignID is nil, else every invitation of that
// Campaign. It is written when what such a link gives is taken away —
// [Store.RevokeAccess], [Store.RemoveCampaignGuest] — because the links
// themselves usually stay live: a multi-use link has other people still to
// admit, and revoking it for everyone is a separate decision.
//
// EVERY invitation existing at that moment is refused, not just the one the
// person came in through, and regardless of how their grant or guest row was
// made: someone granted or added by Discord User ID could otherwise walk back
// in through any live link, and so could someone holding two links.
//
// A refusal is never cleared. It is inert while the person holds a grant (or
// is a guest of the Campaign) — both redemptions return "already admitted"
// before they look at it — so an explicit [Store.GrantAccess] /
// [Store.AddCampaignGuest] needs no clearing to work, and clearing would
// reopen every old link the next time the person is removed. What lets them
// back in is exactly a decision made AFTER the removal: a link created later,
// or granting / adding them by Discord User ID.
func (s *Store) refuseInvitations(ctx context.Context, discordUserID string, campaignID *uuid.UUID) error {
	if _, err := s.db.Exec(ctx,
		`INSERT INTO invitation_refusal (invitation_id, discord_user_id)
		 SELECT i.id, $1 FROM invitation i
		  WHERE CASE WHEN $2::uuid IS NULL THEN i.kind = 'access' ELSE i.campaign_id = $2 END
		 ON CONFLICT (invitation_id, discord_user_id) DO NOTHING`, discordUserID, campaignID); err != nil {
		return fmt.Errorf("storage: record invitation refusals: %w", err)
	}
	return nil
}

// invitationRefused reports whether the invitation was refused to this
// Discord User (see refuseInvitations).
func (s *Store) invitationRefused(ctx context.Context, invitationID uuid.UUID, discordUserID string) (bool, error) {
	var refused bool
	if err := s.db.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM invitation_refusal
		                 WHERE invitation_id = $1 AND discord_user_id = $2)`,
		invitationID, discordUserID).Scan(&refused); err != nil {
		return false, fmt.Errorf("storage: read invitation refusal: %w", err)
	}
	return refused, nil
}

// AccessPerson is one row of the Platform Admin's people list: a Discord User
// who has signed in, holds an Access Grant, or both — with the Tenant they
// operate, when they operate one. A user operating several Tenants appears
// once per Tenant.
type AccessPerson struct {
	DiscordUserID string
	// Known is false for someone who has never signed in: a grant with no
	// users row yet, or a Campaign Guest added by snowflake whose users row is
	// still the nameless placeholder (ADR-0065). Either way, no name or avatar.
	Known       bool
	Name        string
	Avatar      string
	SuspendedAt *time.Time
	JoinedAt    *time.Time
	LastSeenAt  *time.Time

	HasGrant          bool
	GrantNote         string
	GrantPlatformKeys bool
	GrantedAt         *time.Time
	InvitationLabel   string

	TenantID           *uuid.UUID
	TenantName         string
	TenantPlatformKeys bool
	CampaignCount      int
	// GuestCampaignCount is how many Campaigns the person can open as a
	// Campaign Guest. Archived Campaigns do not count, and neither do those
	// whose host lost their standing (hostStandingClause): they are not
	// reachable for guests, and the Admin screen must not show someone as
	// admitted by a Campaign that does not admit them (always 0 for a grant
	// nobody has used yet).
	GuestCampaignCount int
}

// ListAccessPeople returns the Platform Admin's cross-tenant people list. The
// synthetic dev operator is excluded unless includeDev (a dev instance lists
// it, since it IS that instance's operator).
func (s *Store) ListAccessPeople(ctx context.Context, includeDev bool) ([]AccessPerson, error) {
	needGrant, admins, recorded := s.accessArgs()
	rows, err := s.db.Query(ctx,
		`SELECT COALESCE(u.discord_user_id, g.discord_user_id),
		        u.id IS NOT NULL AND `+userHasSignedIn+`, COALESCE(u.name, ''), COALESCE(u.avatar, ''),
		        u.suspended_at, u.created_at,
		        (SELECT max(se.last_seen_at) FROM sessions se WHERE se.user_id = u.id),
		        g.id IS NOT NULL, COALESCE(g.note, ''), COALESCE(g.platform_keys, false), g.created_at,
		        COALESCE(i.label, ''),
		        t.id, COALESCE(t.name, ''), COALESCE(t.platform_keys, false),
		        (SELECT count(*) FROM campaign c WHERE c.tenant_id = t.id),
		        (SELECT count(*) FROM campaign_guest cg
		           JOIN campaign gc ON gc.id = cg.campaign_id
		           JOIN tenant gt ON gt.id = gc.tenant_id
		          WHERE cg.user_id = u.id AND gc.archived_at IS NULL
		            AND `+hostStandingClause("gt", 3)+`)
		   FROM users u
		   FULL OUTER JOIN access_grant g ON g.discord_user_id = u.discord_user_id
		   LEFT JOIN invitation i ON i.id = g.invitation_id
		   LEFT JOIN tenant t ON t.operator_user_id = u.id
		  WHERE $1 OR u.discord_user_id IS DISTINCT FROM $2
		  ORDER BY COALESCE(u.created_at, g.created_at), 1, t.created_at`,
		includeDev, DevOperatorDiscordID, needGrant, admins, recorded)
	if err != nil {
		return nil, fmt.Errorf("storage: list access people: %w", err)
	}
	defer rows.Close()
	var out []AccessPerson
	for rows.Next() {
		var p AccessPerson
		if err := rows.Scan(&p.DiscordUserID, &p.Known, &p.Name, &p.Avatar,
			&p.SuspendedAt, &p.JoinedAt, &p.LastSeenAt,
			&p.HasGrant, &p.GrantNote, &p.GrantPlatformKeys, &p.GrantedAt,
			&p.InvitationLabel,
			&p.TenantID, &p.TenantName, &p.TenantPlatformKeys, &p.CampaignCount,
			&p.GuestCampaignCount); err != nil {
			return nil, fmt.Errorf("storage: scan access person: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list access people: %w", err)
	}
	return out, nil
}

// TenantPlatformKeys reports whether the Tenant may spend the deployment's
// Platform Keys on an `allowlist` deployment (tenant.platform_keys, ADR-0065).
// An unknown tenant reads false — the gate fails closed.
func (s *Store) TenantPlatformKeys(ctx context.Context, tenantID uuid.UUID) (bool, error) {
	var ok bool
	err := s.db.QueryRow(ctx, `SELECT platform_keys FROM tenant WHERE id = $1`, tenantID).Scan(&ok)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("storage: platform keys flag for tenant %s: %w", tenantID, err)
	}
	return ok, nil
}

// SetTenantPlatformKeys sets tenant.platform_keys (a Platform Admin action),
// keeping the operator's Access Grant — when one exists — in step so the two
// never disagree in the admin console. ErrNotFound for an unknown tenant.
func (s *Store) SetTenantPlatformKeys(ctx context.Context, tenantID uuid.UUID, enabled bool) error {
	return s.InTx(ctx, func(tx *Store) error {
		tag, err := tx.db.Exec(ctx,
			`UPDATE tenant SET platform_keys = $2, updated_at = now() WHERE id = $1`, tenantID, enabled)
		if err != nil {
			return fmt.Errorf("storage: set platform keys for tenant %s: %w", tenantID, err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		if _, err := tx.db.Exec(ctx,
			`UPDATE access_grant g SET platform_keys = $2
			   FROM users u JOIN tenant t ON t.operator_user_id = u.id
			  WHERE t.id = $1 AND g.discord_user_id = u.discord_user_id`, tenantID, enabled); err != nil {
			return fmt.Errorf("storage: sync grant platform keys for tenant %s: %w", tenantID, err)
		}
		return nil
	})
}
