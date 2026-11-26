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
// whatever the Tenant had before.
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
		if _, err := tx.db.Exec(ctx,
			`UPDATE tenant t SET platform_keys = $2, updated_at = now()
			   FROM users u
			  WHERE u.discord_user_id = $1 AND t.operator_user_id = u.id
			    AND t.platform_keys <> $2`, discordUserID, platformKeys); err != nil {
			return fmt.Errorf("storage: sync tenant platform keys for %q: %w", discordUserID, err)
		}
		return nil
	})
	if err != nil {
		return AccessGrant{}, err
	}
	return g, nil
}

// RevokeAccess removes a Discord User's Access Grant and deletes their
// sessions, so the revocation lands on their very next request rather than at
// the next boot. Their Tenant and its data are kept: granting again restores
// everything. ErrNotFound when no grant exists.
func (s *Store) RevokeAccess(ctx context.Context, discordUserID string) error {
	return s.InTx(ctx, func(tx *Store) error {
		tag, err := tx.db.Exec(ctx, `DELETE FROM access_grant WHERE discord_user_id = $1`, discordUserID)
		if err != nil {
			return fmt.Errorf("storage: revoke access of %q: %w", discordUserID, err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
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
// Every refusal is ErrInvitationInvalid.
func (s *Store) RedeemAccessInvitation(ctx context.Context, token, discordUserID string) (AccessGrant, error) {
	var g AccessGrant
	err := s.InTx(ctx, func(tx *Store) error {
		row := tx.db.QueryRow(ctx,
			`SELECT `+invitationColumns+` FROM invitation
			  WHERE token_hash = $1 AND kind = 'access' FOR UPDATE`, sessionTokenHash(token))
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

		row = tx.db.QueryRow(ctx,
			`INSERT INTO access_grant (discord_user_id, note, platform_keys, granted_by, invitation_id)
			 VALUES ($1, $2, $3, $4, $5)
			 RETURNING `+accessGrantColumns,
			discordUserID, inv.Label, inv.PlatformKeys, inv.CreatedBy, inv.ID)
		if g, err = scanAccessGrant(row); err != nil {
			return fmt.Errorf("storage: create grant from invitation: %w", err)
		}
		if _, err := tx.db.Exec(ctx, `UPDATE invitation SET uses = uses + 1 WHERE id = $1`, inv.ID); err != nil {
			return fmt.Errorf("storage: count invitation use: %w", err)
		}
		return nil
	})
	if err != nil {
		return AccessGrant{}, err
	}
	return g, nil
}

// AccessPerson is one row of the Platform Admin's people list: a Discord User
// who has signed in, holds an Access Grant, or both — with the Tenant they
// operate, when they operate one. A user operating several Tenants appears
// once per Tenant.
type AccessPerson struct {
	DiscordUserID string
	// Known is false for a grant whose Discord User has never signed in: there
	// is no users row yet, so no name or avatar.
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
}

// ListAccessPeople returns the Platform Admin's cross-tenant people list. The
// synthetic dev operator is excluded unless includeDev (a dev instance lists
// it, since it IS that instance's operator).
func (s *Store) ListAccessPeople(ctx context.Context, includeDev bool) ([]AccessPerson, error) {
	rows, err := s.db.Query(ctx,
		`SELECT COALESCE(u.discord_user_id, g.discord_user_id),
		        u.id IS NOT NULL, COALESCE(u.name, ''), COALESCE(u.avatar, ''),
		        u.suspended_at, u.created_at,
		        (SELECT max(se.last_seen_at) FROM sessions se WHERE se.user_id = u.id),
		        g.id IS NOT NULL, COALESCE(g.note, ''), COALESCE(g.platform_keys, false), g.created_at,
		        COALESCE(i.label, ''),
		        t.id, COALESCE(t.name, ''), COALESCE(t.platform_keys, false),
		        (SELECT count(*) FROM campaign c WHERE c.tenant_id = t.id)
		   FROM users u
		   FULL OUTER JOIN access_grant g ON g.discord_user_id = u.discord_user_id
		   LEFT JOIN invitation i ON i.id = g.invitation_id
		   LEFT JOIN tenant t ON t.operator_user_id = u.id
		  WHERE $1 OR u.discord_user_id IS DISTINCT FROM $2
		  ORDER BY COALESCE(u.created_at, g.created_at), 1, t.created_at`,
		includeDev, DevOperatorDiscordID)
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
			&p.TenantID, &p.TenantName, &p.TenantPlatformKeys, &p.CampaignCount); err != nil {
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
