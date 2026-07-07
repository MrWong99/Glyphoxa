package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Per-Tenant spend caps (#130, ADR-0046): the two nullable USD thresholds on the
// tenant row that stop a Voice Session's Agent turns once its estimated spend
// crosses them. Both NULL is the no-cap default. Reads and writes live together
// because they are one small cohesive feature.

// GetTenantSpendCaps loads a Tenant's soft/hard spend caps, each nil when that cap
// is unset. ErrNotFound when no tenant row exists for the id — distinct from a
// tenant that exists with both caps NULL (which returns a zero-value SpendCaps).
func (s *Store) GetTenantSpendCaps(ctx context.Context, tenantID uuid.UUID) (SpendCaps, error) {
	var caps SpendCaps
	err := s.db.QueryRow(ctx,
		`SELECT spend_cap_soft_usd, spend_cap_hard_usd FROM tenant WHERE id = $1`, tenantID).
		Scan(&caps.SoftUSD, &caps.HardUSD)
	if errors.Is(err, pgx.ErrNoRows) {
		return SpendCaps{}, ErrNotFound
	}
	if err != nil {
		return SpendCaps{}, fmt.Errorf("storage: get spend caps for tenant %s: %w", tenantID, err)
	}
	return caps, nil
}

// SetTenantSpendCaps writes a Tenant's soft/hard spend caps, storing NULL for a nil
// pointer (that cap cleared). It does NOT enforce hard >= soft — that validation is
// the RPC's (InvalidArgument), leaving storage a faithful persistence layer.
// ErrNotFound when the tenant row does not exist.
func (s *Store) SetTenantSpendCaps(ctx context.Context, tenantID uuid.UUID, caps SpendCaps) error {
	tag, err := s.db.Exec(ctx,
		`UPDATE tenant
		    SET spend_cap_soft_usd = $2, spend_cap_hard_usd = $3, updated_at = now()
		  WHERE id = $1`,
		tenantID, caps.SoftUSD, caps.HardUSD)
	if err != nil {
		return fmt.Errorf("storage: set spend caps for tenant %s: %w", tenantID, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
