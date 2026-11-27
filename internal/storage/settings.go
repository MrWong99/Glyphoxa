package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Deployment-scoped settings (ADR-0055): a singleton row recording posture the
// boot both reads and writes: the Admission Mode, and a copy of the Platform
// Admin list for the processes that are not given it. Kept as columns, not a
// key-value bag, so the schema stays honest about what is actually persisted.

// GetAdmissionPosture returns the recorded Admission Mode ('allowlist' or
// 'open'), or ErrNotFound when no posture has ever been recorded (a pre-0055
// deployment's first boot). The value is stored verbatim; vocabulary
// validation lives in the auth tier (auth.ParseAdmissionMode).
func (s *Store) GetAdmissionPosture(ctx context.Context) (string, error) {
	var mode string
	err := s.db.QueryRow(ctx,
		`SELECT admission_mode FROM deployment_settings WHERE id`).Scan(&mode)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("storage: get admission posture: %w", err)
	}
	return mode, nil
}

// RecordAdmissionPosture upserts the singleton deployment_settings row with the
// effective Admission Mode, so the posture survives env-var loss and stays
// visible to operators (ADR-0055's rollback-trap mitigation).
func (s *Store) RecordAdmissionPosture(ctx context.Context, mode string) error {
	_, err := s.db.Exec(ctx,
		`INSERT INTO deployment_settings (id, admission_mode) VALUES (true, $1)
		 ON CONFLICT (id) DO UPDATE
		   SET admission_mode = EXCLUDED.admission_mode, updated_at = now()`, mode)
	if err != nil {
		return fmt.Errorf("storage: record admission posture: %w", err)
	}
	return nil
}

// RecordPlatformAdmins stores the Platform Admin snowflakes the web tier
// booted with (GLYPHOXA_OPERATOR_IDS) on the singleton deployment_settings
// row, so a process that is not given the env list — a voice worker — can
// still tell a Platform Admin from an operator whose access was revoked
// ([AccessPolicy] RecordedAdmins). It is a copy for reading, never a second
// place to configure them: every web boot overwrites it with the env list.
// mode is the effective Admission Mode, used only when the row does not exist
// yet; an existing row keeps the posture it has.
func (s *Store) RecordPlatformAdmins(ctx context.Context, mode string, discordUserIDs []string) error {
	if discordUserIDs == nil {
		discordUserIDs = []string{}
	}
	_, err := s.db.Exec(ctx,
		`INSERT INTO deployment_settings (id, admission_mode, platform_admin_ids) VALUES (true, $1, $2)
		 ON CONFLICT (id) DO UPDATE
		   SET platform_admin_ids = EXCLUDED.platform_admin_ids, updated_at = now()`, mode, discordUserIDs)
	if err != nil {
		return fmt.Errorf("storage: record platform admins: %w", err)
	}
	return nil
}
