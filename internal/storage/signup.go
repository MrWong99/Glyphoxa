package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Open-mode signup provisioning (ADR-0055): one transactional write composing
// the existing auth/billing pieces. It deliberately never touches
// ResolveOperatorTenant — the claim-earliest-unbound path is exactly what
// ADR-0041's no-TOFU rejection forbids for strangers, so signups are
// CREATE-ONLY: an existing unbound (seed) Tenant stays claimable by an
// allowlisted operator and by no one else.

// ErrUserSuspended reports a signup/login attempt by a suspended user
// (users.suspended_at set, ADR-0055 open-mode revocation). The OAuth callback
// bounces these at the door instead of minting a session the per-request
// re-check would refuse anyway.
var ErrUserSuspended = errors.New("storage: user is suspended")

// SignupParams is the input to ProvisionSignup. Session.UserID is ignored — the
// provision fills it with the upserted user's id.
type SignupParams struct {
	User       UpsertUserParams
	TenantName string
	PlanSlug   string
	Session    NewSession
}

// SignupResult reports what ProvisionSignup did. Created is true when a fresh
// Tenant was founded (a first signup) — the callback uses it to route the user
// into onboarding rather than straight to the app.
type SignupResult struct {
	User    User
	Tenant  Tenant
	Session Session
	Created bool
}

// ProvisionSignup admits an open-mode signup in ONE transaction: Discord user
// upsert → bound-tenant lookup → (when none) create-only Tenant founding +
// default-Plan bind → session mint. All-or-nothing per ADR-0055: a failure at
// any step (most plausibly an unknown or archived default plan slug) leaves no
// user, tenant, subscription, or session behind. A returning signup — the user
// already bound to a Tenant — founds nothing and just refreshes identity +
// mints the session. A suspended user is refused with ErrUserSuspended.
func (s *Store) ProvisionSignup(ctx context.Context, p SignupParams) (SignupResult, error) {
	var res SignupResult
	err := s.InTx(ctx, func(tx *Store) error {
		u, err := tx.UpsertUser(ctx, p.User)
		if err != nil {
			return err
		}
		if u.SuspendedAt != nil {
			return ErrUserSuspended
		}
		res.User = u

		// Already bound? Same lookup as ResolveOperatorTenant's first step —
		// and deliberately ONLY that step (create-only, never claim).
		row := tx.db.QueryRow(ctx,
			`SELECT id, name, created_at, updated_at FROM tenant
			  WHERE operator_user_id = $1 ORDER BY created_at, id LIMIT 1`, u.ID)
		switch err := row.Scan(&res.Tenant.ID, &res.Tenant.Name,
			&res.Tenant.CreatedAt, &res.Tenant.UpdatedAt); {
		case err == nil:
			// Returning signup: nothing to found or bind.
		case errors.Is(err, pgx.ErrNoRows):
			row := tx.db.QueryRow(ctx,
				`INSERT INTO tenant (name, operator_user_id) VALUES ($1, $2)
				 RETURNING id, name, created_at, updated_at`, p.TenantName, u.ID)
			if err := row.Scan(&res.Tenant.ID, &res.Tenant.Name,
				&res.Tenant.CreatedAt, &res.Tenant.UpdatedAt); err != nil {
				return fmt.Errorf("storage: found signup tenant: %w", err)
			}
			// SetTenantPlan's internal InTx flattens into this transaction, so
			// its failure rolls back the founding above too.
			if _, err := tx.SetTenantPlan(ctx, res.Tenant.ID, p.PlanSlug); err != nil {
				return fmt.Errorf("storage: bind signup plan %q: %w", p.PlanSlug, err)
			}
			res.Created = true
		default:
			return fmt.Errorf("storage: signup bound-tenant lookup: %w", err)
		}

		sess := p.Session
		sess.UserID = u.ID
		res.Session, err = tx.CreateSession(ctx, sess)
		return err
	})
	if err != nil {
		return SignupResult{}, err
	}
	return res, nil
}
