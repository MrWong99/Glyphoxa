package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MrWong99/glyphoxa/pkg/memory"
)

// RecapStoreImpl is the PostgreSQL-backed implementation of [memory.RecapStore].
// Obtain one via [Store.RecapStore] rather than constructing directly.
// All methods are safe for concurrent use.
type RecapStoreImpl struct {
	pool       *pgxpool.Pool
	schema     SchemaName
	campaignID string
}

// Ensure RecapStoreImpl satisfies the interface at compile time.
var _ memory.RecapStore = (*RecapStoreImpl)(nil)

// SaveRecap implements [memory.RecapStore].
func (r *RecapStoreImpl) SaveRecap(ctx context.Context, recap memory.Recap) error {
	q := fmt.Sprintf(`
		INSERT INTO %s
		    (session_id, campaign_id, text, audio_data, sample_rate, channels, duration_ns, generated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (session_id) DO UPDATE SET
		    text = EXCLUDED.text,
		    audio_data = EXCLUDED.audio_data,
		    sample_rate = EXCLUDED.sample_rate,
		    channels = EXCLUDED.channels,
		    duration_ns = EXCLUDED.duration_ns,
		    generated_at = EXCLUDED.generated_at`,
		r.schema.TableRef("recaps"))

	_, err := r.pool.Exec(ctx, q,
		recap.SessionID,
		recap.CampaignID,
		recap.Text,
		recap.AudioData,
		recap.SampleRate,
		recap.Channels,
		recap.Duration.Nanoseconds(),
		recap.GeneratedAt,
	)
	if err != nil {
		return fmt.Errorf("recap store: save recap: %w", err)
	}
	return nil
}

// GetRecap implements [memory.RecapStore].
func (r *RecapStoreImpl) GetRecap(ctx context.Context, sessionID string) (*memory.Recap, error) {
	q := fmt.Sprintf(`
		SELECT session_id, campaign_id, text, audio_data, sample_rate, channels, duration_ns, generated_at
		FROM   %s
		WHERE  session_id = $1`,
		r.schema.TableRef("recaps"))

	var (
		recap      memory.Recap
		durationNS int64
	)
	err := r.pool.QueryRow(ctx, q, sessionID).Scan(
		&recap.SessionID,
		&recap.CampaignID,
		&recap.Text,
		&recap.AudioData,
		&recap.SampleRate,
		&recap.Channels,
		&durationNS,
		&recap.GeneratedAt,
	)
	if err != nil {
		if err.Error() == "no rows in result set" {
			return nil, nil
		}
		return nil, fmt.Errorf("recap store: get recap: %w", err)
	}
	recap.Duration = time.Duration(durationNS)
	return &recap, nil
}
