package web

import (
	"context"
	"fmt"
	"time"
)

// UpdateUserTenant assigns a user to a tenant and sets their role.
func (s *Store) UpdateUserTenant(ctx context.Context, userID, tenantID, role string) error {
	now := time.Now().UTC()
	tag, err := s.pool.Exec(ctx, `
		UPDATE mgmt.users SET tenant_id = $2, role = $3, updated_at = $4
		WHERE id = $1 AND deleted_at IS NULL
	`, userID, tenantID, role, now)
	if err != nil {
		return fmt.Errorf("web: update user tenant %q: %w", userID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("web: user %q not found", userID)
	}
	return nil
}
