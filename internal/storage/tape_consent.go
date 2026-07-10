package storage

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// UpsertTapeConsent records that a Speaker has consented to the rollover tape for
// a Campaign (#306, ADR-0051). It is idempotent: consenting twice keeps the
// original created_at. The (campaign_id, discord_user_id) primary key makes the
// row's presence the single source of truth for "this Speaker consented".
func (s *Store) UpsertTapeConsent(ctx context.Context, campaignID uuid.UUID, discordUserID string) error {
	_, err := s.db.Exec(ctx,
		`INSERT INTO tape_consent (campaign_id, discord_user_id)
		 VALUES ($1, $2)
		 ON CONFLICT (campaign_id, discord_user_id) DO NOTHING`,
		campaignID, discordUserID)
	if err != nil {
		return fmt.Errorf("storage: upsert tape consent: %w", err)
	}
	return nil
}

// DeleteTapeConsent revokes a Speaker's tape consent for a Campaign (#306). It is
// idempotent — deleting an absent row is a no-op — so a double revoke is harmless.
func (s *Store) DeleteTapeConsent(ctx context.Context, campaignID uuid.UUID, discordUserID string) error {
	_, err := s.db.Exec(ctx,
		`DELETE FROM tape_consent WHERE campaign_id = $1 AND discord_user_id = $2`,
		campaignID, discordUserID)
	if err != nil {
		return fmt.Errorf("storage: delete tape consent: %w", err)
	}
	return nil
}

// ListTapeConsent returns the Discord user ids that have consented to the rollover
// tape for a Campaign (#306), the set the tape is seeded with when a Voice Session
// arms it. Order is stable by created_at for deterministic reads.
func (s *Store) ListTapeConsent(ctx context.Context, campaignID uuid.UUID) ([]string, error) {
	rows, err := s.db.Query(ctx,
		`SELECT discord_user_id FROM tape_consent
		  WHERE campaign_id = $1 ORDER BY created_at, discord_user_id`,
		campaignID)
	if err != nil {
		return nil, fmt.Errorf("storage: list tape consent: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("storage: scan tape consent: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: iterate tape consent: %w", err)
	}
	return ids, nil
}
