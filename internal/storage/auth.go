package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Auth control-plane persistence (ADR-0016 / ADR-0039): users, server-side
// sessions, and the thin single-operator↔tenant binding. Reads and writes live
// together here because they are one cohesive feature; the broader read/write
// split in store.go / write.go predates it. The cookie token is an opaque
// random secret minted by the auth tier (internal/auth) — this layer only
// persists and validates it, never interprets it.

const userColumns = `id, discord_user_id, name, avatar, role, created_at, updated_at, suspended_at`

// DevOperatorDiscordID is the synthetic Discord identity the GLYPHOXA_DEV_MODE
// boot upserts as the dev operator (ADR-0041). It is deliberately NOT a real
// snowflake so it can never collide with a genuine Discord user. It lives here
// because ResolveOperatorTenant treats a tenant bound to it as still claimable:
// the first REAL operator login takes the tenant (and everything configured in
// dev mode) over, instead of being stranded next to it in a fresh empty tenant.
const DevOperatorDiscordID = "glyphoxa-dev-operator"

func scanUser(row pgx.Row) (User, error) {
	var u User
	err := row.Scan(
		&u.ID, &u.DiscordUserID, &u.Name, &u.Avatar, &u.Role,
		&u.CreatedAt, &u.UpdatedAt, &u.SuspendedAt,
	)
	return u, err
}

// UpsertUserParams is the input to UpsertUser: the Discord identity to insert or
// refresh. Role is not an input — a new user defaults to 'operator' (the DB
// default) and an existing user's role is left untouched on refresh.
type UpsertUserParams struct {
	DiscordUserID string
	Name          string
	Avatar        string
}

// UpsertUser inserts a user keyed by discord_user_id, or refreshes the display
// name/avatar of the existing row (Discord is the source of truth for those on
// every login). It returns the resulting user. The role is preserved on
// conflict so an operator promotion is never clobbered by a login.
func (s *Store) UpsertUser(ctx context.Context, p UpsertUserParams) (User, error) {
	row := s.db.QueryRow(ctx,
		`INSERT INTO users (discord_user_id, name, avatar)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (discord_user_id) DO UPDATE
		   SET name = EXCLUDED.name, avatar = EXCLUDED.avatar, updated_at = now()
		 RETURNING `+userColumns,
		p.DiscordUserID, p.Name, p.Avatar)
	u, err := scanUser(row)
	if err != nil {
		return User{}, fmt.Errorf("storage: upsert user %q: %w", p.DiscordUserID, err)
	}
	return u, nil
}

// GetUserByDiscordID loads a user by Discord snowflake, or ErrNotFound.
func (s *Store) GetUserByDiscordID(ctx context.Context, discordUserID string) (User, error) {
	row := s.db.QueryRow(ctx,
		`SELECT `+userColumns+` FROM users WHERE discord_user_id = $1`, discordUserID)
	u, err := scanUser(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("storage: get user %q: %w", discordUserID, err)
	}
	return u, nil
}

// SetUserSuspended stamps or clears users.suspended_at by Discord snowflake —
// the open-mode revocation mechanism (ADR-0055). Suspension is enforced by
// AuthenticateSession's per-request re-check, so it takes effect immediately
// without deleting sessions; clearing it restores access with the same tokens.
// Idempotent per direction; ErrNotFound for an unknown user.
func (s *Store) SetUserSuspended(ctx context.Context, discordUserID string, suspended bool) error {
	tag, err := s.db.Exec(ctx,
		`UPDATE users
		    SET suspended_at = CASE WHEN $2 THEN COALESCE(suspended_at, now()) END,
		        updated_at = now()
		  WHERE discord_user_id = $1`, discordUserID, suspended)
	if err != nil {
		return fmt.Errorf("storage: set suspended for %q: %w", discordUserID, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetActiveCampaign records the operator's durable Active Campaign choice (#108,
// ADR-0009) on the users row keyed by Discord snowflake. It UPSERTS the row so a
// GM who drives the slash-command surface before ever logging into the web tier
// still gets a persisted selection; a later OAuth login refreshes the display
// fields (UpsertUser) and preserves this selection. Idempotent. A campaignID that
// no longer exists trips the active_campaign_id FK (23503) and yields ErrNotFound
// — the write can never store a dangling pointer, even if the campaign vanishes
// between a caller's existence check and this write.
func (s *Store) SetActiveCampaign(ctx context.Context, discordUserID string, campaignID uuid.UUID) error {
	_, err := s.db.Exec(ctx,
		`INSERT INTO users (discord_user_id, active_campaign_id)
		 VALUES ($1, $2)
		 ON CONFLICT (discord_user_id) DO UPDATE
		   SET active_campaign_id = EXCLUDED.active_campaign_id, updated_at = now()`,
		discordUserID, campaignID)
	if code, ok := pgErrCode(err); ok && code == "23503" { // foreign_key_violation
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("storage: set active campaign for %q: %w", discordUserID, err)
	}
	return nil
}

// GetActiveCampaignForUserInTenant is the tenant-scoped durable-selection read
// (#473, and the ONLY per-operator durable read after #490 removed the tenant-free
// GetActiveCampaignForUser — every slash/RPC caller now carries a resolved Tenant,
// so a tenant-free read can no longer be grabbed by the next handler): it resolves
// the operator's active_campaign_id ONLY when the selected campaign is in the
// caller's tenant (`AND c.tenant_id = $2`). A selection pointing at another tenant's
// campaign — which SetActiveCampaign's FK-only write cannot prevent by itself —
// reads back as ErrNotFound here, so it can never pivot a stranger's campaign-scoped
// surfaces onto the victim tenant (self-signup design §0a). The standalone voice
// node uses the context-free GetOperatorActiveCampaign instead. Absent-selection
// semantics: no row / no selection / deleted (FK ON DELETE SET NULL) / archived
// (#269) → ErrNotFound, so the caller falls through to the GetActiveCampaignInTenant
// most-recent fallback. The columns are qualified to `c` because users and campaign
// share id/name/created_at/updated_at.
func (s *Store) GetActiveCampaignForUserInTenant(ctx context.Context, tenantID uuid.UUID, discordUserID string) (Campaign, error) {
	row := s.db.QueryRow(ctx,
		`SELECT c.id, c.tenant_id, c.gm_member_id, c.name, c.system, c.language,
		        c.created_at, c.updated_at, c.archived_at, c.tape_armed
		   FROM users u JOIN campaign c ON c.id = u.active_campaign_id
		  WHERE u.discord_user_id = $1 AND c.tenant_id = $2 AND c.archived_at IS NULL`,
		discordUserID, tenantID)
	c, err := scanCampaign(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Campaign{}, ErrNotFound
	}
	if err != nil {
		return Campaign{}, fmt.Errorf("storage: active campaign for %q in tenant %s: %w", discordUserID, tenantID, err)
	}
	return c, nil
}

// GetOperatorActiveCampaign returns the durable Active Campaign selection of the
// single operator, for a surface with NO logged-in user context — the standalone
// voice node (#323, ADR-0039 single-operator pass-through). It is the
// context-free analogue of GetActiveCampaignForUserInTenant: it joins any users row
// carrying a non-null active_campaign_id to a non-archived campaign. Single
// operator, so at most one such selection is meaningful; a tie breaks on the
// most-recently-updated users row — the freshest login-or-selection, NOT strictly
// the freshest campaign selection: UpsertUser bumps users.updated_at on EVERY OAuth
// login, not only on SetActiveCampaign, so `ORDER BY u.updated_at` is a login clock,
// not a selection clock. Acceptable under the single-operator model (ADR-0039),
// where at most one operator has a durable selection anyway. ErrNotFound when
// no operator has a durable, non-archived selection (deleted/archived selections
// are treated as absent, matching GetActiveCampaignForUserInTenant), so the caller falls
// through to the GetActiveCampaign recent fallback.
func (s *Store) GetOperatorActiveCampaign(ctx context.Context) (Campaign, error) {
	row := s.db.QueryRow(ctx,
		`SELECT c.id, c.tenant_id, c.gm_member_id, c.name, c.system, c.language,
		        c.created_at, c.updated_at, c.archived_at, c.tape_armed
		   FROM users u JOIN campaign c ON c.id = u.active_campaign_id
		  WHERE c.archived_at IS NULL
		  ORDER BY u.updated_at DESC, u.id
		  LIMIT 1`)
	c, err := scanCampaign(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Campaign{}, ErrNotFound
	}
	if err != nil {
		return Campaign{}, fmt.Errorf("storage: operator active campaign: %w", err)
	}
	return c, nil
}

// TenantOperatorBinding pairs a Tenant with its operator's Discord snowflake —
// one row of the per-Tenant GM-identity source (#490). It lets GMIdentity scope GM
// standing to the OWNING Tenant (IsGMInTenant) instead of the deployment-wide union
// ListTenantOperatorDiscordIDs feeds, closing ADR-0055's deployment-scope caveat: a
// Tenant A operator is GM in Tenant A only, never in Tenant B's Guild.
type TenantOperatorBinding struct {
	TenantID      uuid.UUID
	DiscordUserID string
}

// ListTenantOperatorBindings returns each Tenant paired with its operator's Discord
// snowflake (tenant.operator_user_id → users.discord_user_id) — the per-Tenant
// GM-identity source (#490, ADR-0055). It is the tenant-scoped analogue of
// ListTenantOperatorDiscordIDs and shares its exclusions: the synthetic dev operator
// ([DevOperatorDiscordID]) never appears (it can match no real speaker/interaction
// user) and a SUSPENDED operator drops out (open-mode revocation reaches GM on the
// next snapshot refresh). Sorted for determinism. An empty result is not an error.
func (s *Store) ListTenantOperatorBindings(ctx context.Context) ([]TenantOperatorBinding, error) {
	rows, err := s.db.Query(ctx,
		`SELECT t.id, u.discord_user_id
		   FROM tenant t JOIN users u ON u.id = t.operator_user_id
		  WHERE u.discord_user_id <> $1
		    AND u.suspended_at IS NULL
		  ORDER BY u.discord_user_id, t.id`, DevOperatorDiscordID)
	if err != nil {
		return nil, fmt.Errorf("storage: list tenant operator bindings: %w", err)
	}
	defer rows.Close()
	var out []TenantOperatorBinding
	for rows.Next() {
		var b TenantOperatorBinding
		if err := rows.Scan(&b.TenantID, &b.DiscordUserID); err != nil {
			return nil, fmt.Errorf("storage: scan tenant operator binding: %w", err)
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list tenant operator bindings: %w", err)
	}
	return out, nil
}

// NewSession is the input to CreateSession. Token is the opaque random secret
// the auth tier minted; ExpiresAt is the absolute expiry the validator enforces.
type NewSession struct {
	UserID    uuid.UUID
	Token     string
	ExpiresAt time.Time
	IP        string
	UA        string
}

// CreateSession inserts a session row and returns it.
func (s *Store) CreateSession(ctx context.Context, n NewSession) (Session, error) {
	row := s.db.QueryRow(ctx,
		`INSERT INTO sessions (user_id, token, expires_at, ip, ua)
		 VALUES ($1, $2, $3, $4, $5)
		 RETURNING id, user_id, token, created_at, last_seen_at, expires_at, ip, ua`,
		n.UserID, n.Token, n.ExpiresAt, n.IP, n.UA)
	var sess Session
	err := row.Scan(
		&sess.ID, &sess.UserID, &sess.Token, &sess.CreatedAt,
		&sess.LastSeenAt, &sess.ExpiresAt, &sess.IP, &sess.UA,
	)
	if err != nil {
		return Session{}, fmt.Errorf("storage: create session: %w", err)
	}
	return sess, nil
}

// AuthenticateSession validates a session token and returns the owning user. It
// bumps last_seen_at as a side effect of a successful, non-expired lookup, in a
// single round trip. A missing or expired token yields ErrNotFound — the RPC
// layer maps that to CodeUnauthenticated. A SUSPENDED owner yields the same
// ErrNotFound: this is the ADR-0055 per-request authorization re-check, and it
// lives in this query because every gated request on both transports funnels
// through here — suspension takes effect on the very next request with zero
// extra round trips. The session row is not deleted (suspension is
// non-destructive; unsuspending restores the same token).
func (s *Store) AuthenticateSession(ctx context.Context, token string) (User, error) {
	row := s.db.QueryRow(ctx,
		`WITH s AS (
		     UPDATE sessions SET last_seen_at = now()
		      WHERE token = $1 AND expires_at > now()
		      RETURNING user_id
		 )
		 SELECT u.id, u.discord_user_id, u.name, u.avatar, u.role,
		        u.created_at, u.updated_at, u.suspended_at
		   FROM users u JOIN s ON s.user_id = u.id
		  WHERE u.suspended_at IS NULL`, token)
	u, err := scanUser(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("storage: authenticate session: %w", err)
	}
	return u, nil
}

// DeleteSession removes a session row by token (logout / revocation). Deleting a
// token that no longer exists is not an error — logout is idempotent.
func (s *Store) DeleteSession(ctx context.Context, token string) error {
	if _, err := s.db.Exec(ctx, `DELETE FROM sessions WHERE token = $1`, token); err != nil {
		return fmt.Errorf("storage: delete session: %w", err)
	}
	return nil
}

// RevokeSessionsOutsideAllowlist deletes every session whose owning user's
// discord_user_id is not on the operator allowlist (ADR-0041 amendment, issue
// #184). The allowlist gates only NEW logins at the OAuth callback; this sweep
// is the revocation half, run at every non-dev ALLOWLIST-Admission-Mode web/all
// boot — which is exactly when a grant change takes effect, since the env var
// is parsed at boot. The sweep splits by Admission Mode (ADR-0055): an `open`
// boot must never run it (it would log out every signup each restart —
// revocation there is suspension-based), and flipping open -> allowlist plus a
// restart evicts every signup, the deliberate lock-down. It also clears
// leftover GLYPHOXA_DEV_MODE sessions ([DevOperatorDiscordID] is never
// allowlisted). An empty allowlist is refused defensively: it would revoke
// every session, and in allowlist mode the boot preflight guarantees a
// non-empty list at the only call site (sweepAllowlistSessions).
func (s *Store) RevokeSessionsOutsideAllowlist(ctx context.Context, discordUserIDs []string) (int64, error) {
	if len(discordUserIDs) == 0 {
		return 0, errors.New("storage: refusing to revoke sessions against an empty allowlist")
	}
	tag, err := s.db.Exec(ctx,
		`DELETE FROM sessions USING users
		  WHERE sessions.user_id = users.id
		    AND users.discord_user_id <> ALL($1)`, discordUserIDs)
	if err != nil {
		return 0, fmt.Errorf("storage: revoke sessions outside allowlist: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ResolveOperatorTenant binds the single seeded Tenant to the first operator and
// returns it (ADR-0039). It is idempotent and atomic: if a tenant is already
// bound to the user it is returned; otherwise the earliest claimable tenant —
// unbound (the seed's) or held by the synthetic dev operator (ADR-0041
// GLYPHOXA_DEV_MODE, see [DevOperatorDiscordID]) — is claimed; otherwise — a
// fresh DB with no seed — a new 'Glyphoxa' tenant is created bound to the user.
// Called once on OAuth login and by the dev-mode boot.
func (s *Store) ResolveOperatorTenant(ctx context.Context, userID uuid.UUID) (Tenant, error) {
	var t Tenant
	err := s.InTx(ctx, func(tx *Store) error {
		// Already bound to this operator?
		row := tx.db.QueryRow(ctx,
			`SELECT id, name, created_at, updated_at FROM tenant
			  WHERE operator_user_id = $1 ORDER BY created_at, id LIMIT 1`, userID)
		switch err := row.Scan(&t.ID, &t.Name, &t.CreatedAt, &t.UpdatedAt); {
		case err == nil:
			return nil
		case !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("storage: find bound tenant: %w", err)
		}

		// Claim the earliest claimable tenant, locking it so two concurrent
		// first-logins can't both claim the same row. A tenant held by the
		// synthetic dev operator is claimable too: the caller reaching this
		// step is a DIFFERENT user (their own binding was checked above), so a
		// real first login takes over what dev mode configured rather than
		// being stranded next to it.
		row = tx.db.QueryRow(ctx,
			`UPDATE tenant SET operator_user_id = $1, updated_at = now()
			  WHERE id = (
			      SELECT t.id FROM tenant t
			        LEFT JOIN users u ON u.id = t.operator_user_id
			       WHERE t.operator_user_id IS NULL OR u.discord_user_id = $2
			       ORDER BY t.created_at, t.id LIMIT 1
			         FOR UPDATE OF t SKIP LOCKED
			  )
			  RETURNING id, name, created_at, updated_at`, userID, DevOperatorDiscordID)
		switch err := row.Scan(&t.ID, &t.Name, &t.CreatedAt, &t.UpdatedAt); {
		case err == nil:
			return nil
		case !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("storage: claim unbound tenant: %w", err)
		}

		// No tenant at all (unseeded DB): create one bound to the operator.
		row = tx.db.QueryRow(ctx,
			`INSERT INTO tenant (name, operator_user_id) VALUES ('Glyphoxa', $1)
			 RETURNING id, name, created_at, updated_at`, userID)
		if err := row.Scan(&t.ID, &t.Name, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return fmt.Errorf("storage: seed operator tenant: %w", err)
		}
		return nil
	})
	if err != nil {
		return Tenant{}, err
	}
	return t, nil
}

// TenantForUser returns the id of the tenant bound to the operator, or
// ErrNotFound when none is bound. The X-Tenant-Id interceptor uses it as the
// thin single-operator pass-through (ADR-0039).
func (s *Store) TenantForUser(ctx context.Context, userID uuid.UUID) (uuid.UUID, error) {
	var id uuid.UUID
	err := s.db.QueryRow(ctx,
		`SELECT id FROM tenant WHERE operator_user_id = $1 ORDER BY created_at, id LIMIT 1`,
		userID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrNotFound
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("storage: tenant for user %s: %w", userID, err)
	}
	return id, nil
}

// ListTenantOperatorDiscordIDs returns the DISTINCT Discord snowflakes bound as
// tenant operators (tenant.operator_user_id → users.discord_user_id), sorted
// for determinism — the interim GM-identity source (ADR-0055, amending
// ADR-0050's allowlist-membership clause). auth.GMIdentity snapshots it;
// unbound tenants and unbound users simply don't appear (the env-allowlist
// fallback covers the NULL-binding migration edge). The synthetic dev operator
// ([DevOperatorDiscordID]) is excluded: it can never match a real Discord
// speaker or interaction user, so on a dev-touched DB it would only make the
// GM set look non-empty — suppressing the "Butler unaddressable" boot warning
// — while admitting nobody. An empty result is not an error.
func (s *Store) ListTenantOperatorDiscordIDs(ctx context.Context) ([]string, error) {
	rows, err := s.db.Query(ctx,
		`SELECT DISTINCT u.discord_user_id
		   FROM tenant t JOIN users u ON u.id = t.operator_user_id
		  WHERE u.discord_user_id <> $1
		    AND u.suspended_at IS NULL
		  ORDER BY u.discord_user_id`, DevOperatorDiscordID)
	if err != nil {
		return nil, fmt.Errorf("storage: list tenant operator discord ids: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("storage: scan tenant operator discord id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list tenant operator discord ids: %w", err)
	}
	return ids, nil
}
