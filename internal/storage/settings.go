package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Deployment-scoped settings (ADR-0055): a singleton row recording posture the
// boot both reads and writes. Today that is exactly one value — the Admission
// Mode — kept as a column, not a key-value bag, so the schema stays honest
// about what is actually persisted.

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
