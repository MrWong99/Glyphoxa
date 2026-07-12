package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Discord highlight delivery (#310, Epic 8): the text channel the GM last shared a
// Highlight file to, remembered per Campaign so the share dialog pre-selects it
// (ADR-0051 GM-only sharing). It is a single scalar on the campaign row
// (00031_campaign_share_channel.sql), not a Campaign-struct field: the value is
// read/written in isolation by the ShareHighlight RPC, so a narrow getter/setter
// keeps the Campaign model unchanged.

// GetCampaignShareChannel returns the Campaign's remembered highlight-share text
// channel id, or "" when none has been chosen yet. An unknown campaign is
// ErrNotFound (distinct from the empty-string "never shared" state of a known one).
func (s *Store) GetCampaignShareChannel(ctx context.Context, campaignID uuid.UUID) (string, error) {
	var channelID string
	err := s.db.QueryRow(ctx,
		`SELECT highlight_share_channel_id FROM campaign WHERE id = $1`, campaignID).
		Scan(&channelID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("storage: get campaign share channel %s: %w", campaignID, err)
	}
	return channelID, nil
}

// SetCampaignShareChannel remembers channelID as the Campaign's highlight-share
// text channel (last-choice-wins). An unknown campaign is ErrNotFound. Persisting
// it is best-effort from the RPC's view — a failure never fails the share itself,
// only the pre-selection memory.
func (s *Store) SetCampaignShareChannel(ctx context.Context, campaignID uuid.UUID, channelID string) error {
	tag, err := s.db.Exec(ctx,
		`UPDATE campaign SET highlight_share_channel_id = $2, updated_at = now() WHERE id = $1`,
		campaignID, channelID)
	if err != nil {
		return fmt.Errorf("storage: set campaign share channel %s: %w", campaignID, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
