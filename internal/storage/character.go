package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Player Character (PC) persistence (#276, E4). A Character is played by exactly
// one Discord User — discord_user_id is MANDATORY (ADR-0003: Players are not
// Tenant Members; they are scoped via the Characters they play, and Address
// Detection / transcript attribution need only the Discord User ID). LinkedUserID
// is the nullable dormant link set on first Discord OAuth (turning a Player into a
// Linked Player); it stays nil until then. Reads and writes are always
// Campaign-scoped, and the UNIQUE (campaign_id, discord_user_id) index makes
// rebinding a Character to a different Discord User an UPDATE, never a new row.

// Character is one persisted Player Character in a Campaign.
type Character struct {
	ID            uuid.UUID
	CampaignID    uuid.UUID
	Name          string
	Aliases       []string
	DiscordUserID string
	// LinkedUserID is nil until the Player first signs in via Discord OAuth
	// (ADR-0003); it never becomes NULL-mandatory like discord_user_id.
	LinkedUserID *string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// NewCharacter is the input to CreateCharacter. LinkedUserID is intentionally
// absent — a Character is created from its Discord identity and only gains a
// linked user later, via OAuth.
type NewCharacter struct {
	CampaignID    uuid.UUID
	Name          string
	Aliases       []string
	DiscordUserID string
}

// CharacterUpdate is the input to UpdateCharacter — a full-field save of the
// editor fields. DiscordUserID is included so an operator can rebind a Character
// to a different Discord User (it stays NOT NULL). CampaignID is the owning
// Campaign the write is scoped to (#342): the UPDATE matches (id, campaign_id),
// so a row in another Campaign is invisible and yields ErrNotFound — a Character
// never moves between Campaigns, and no operator can mutate one they do not own.
type CharacterUpdate struct {
	ID            uuid.UUID
	CampaignID    uuid.UUID
	Name          string
	Aliases       []string
	DiscordUserID string
}

const characterColumns = `
	id, campaign_id, name, aliases, discord_user_id, linked_user_id, created_at, updated_at`

func scanCharacter(row pgx.Row) (Character, error) {
	var c Character
	err := row.Scan(
		&c.ID, &c.CampaignID, &c.Name, &c.Aliases, &c.DiscordUserID, &c.LinkedUserID,
		&c.CreatedAt, &c.UpdatedAt,
	)
	return c, err
}

// CreateCharacter inserts a Player Character into a Campaign and returns its id. A
// second Character for the same (campaign, discord_user_id) violates the unique
// index and yields ErrConflict (one Character per Discord User per Campaign).
func (s *Store) CreateCharacter(ctx context.Context, n NewCharacter) (uuid.UUID, error) {
	aliases := n.Aliases
	if aliases == nil {
		aliases = []string{}
	}
	var id uuid.UUID
	err := s.db.QueryRow(ctx,
		`INSERT INTO character (campaign_id, name, aliases, discord_user_id)
		 VALUES ($1, $2, $3, $4)
		 RETURNING id`,
		n.CampaignID, n.Name, aliases, n.DiscordUserID).Scan(&id)
	if err != nil {
		if code, ok := pgErrCode(err); ok && code == "23505" {
			return uuid.Nil, ErrConflict
		}
		return uuid.Nil, fmt.Errorf("storage: create character: %w", err)
	}
	return id, nil
}

// UpdateCharacter saves a Character's editor fields (name/aliases/discord_user_id)
// and returns the updated row, stamping updated_at = now(). The write is scoped to
// (id, campaign_id) (#342), so a Character in another Campaign matches no row and
// yields ErrNotFound — a cross-campaign mutation is refused server-side without a
// separate ownership SELECT. Rebinding discord_user_id is a normal field write; a
// collision with another Character's (campaign, discord_user_id) yields ErrConflict.
// A missing id yields ErrNotFound.
func (s *Store) UpdateCharacter(ctx context.Context, u CharacterUpdate) (Character, error) {
	aliases := u.Aliases
	if aliases == nil {
		aliases = []string{}
	}
	row := s.db.QueryRow(ctx,
		`UPDATE character SET
		    name = $2,
		    aliases = $3,
		    discord_user_id = $4,
		    updated_at = now()
		  WHERE id = $1 AND campaign_id = $5
		 RETURNING `+characterColumns,
		u.ID, u.Name, aliases, u.DiscordUserID, u.CampaignID)
	updated, err := scanCharacter(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Character{}, ErrNotFound
	}
	if err != nil {
		if code, ok := pgErrCode(err); ok && code == "23505" {
			return Character{}, ErrConflict
		}
		return Character{}, fmt.Errorf("storage: update character %s: %w", u.ID, err)
	}
	return updated, nil
}

// DeleteCharacter removes a Character by id, scoped to its owning Campaign (#342):
// the DELETE matches (id, campaign_id), so a Character in another Campaign is not
// deleted and yields ErrNotFound — a cross-campaign delete is refused server-side.
// A missing id likewise yields ErrNotFound so the RPC can distinguish "gone" from
// "never existed".
func (s *Store) DeleteCharacter(ctx context.Context, campaignID, id uuid.UUID) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM character WHERE id = $1 AND campaign_id = $2`, id, campaignID)
	if err != nil {
		return fmt.Errorf("storage: delete character %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListCharacters returns every Player Character in a Campaign in a stable display
// order (case-insensitive name, then id). An empty result is not an error.
func (s *Store) ListCharacters(ctx context.Context, campaignID uuid.UUID) ([]Character, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+characterColumns+`
		   FROM character
		  WHERE campaign_id = $1
		  ORDER BY lower(name), id`, campaignID)
	if err != nil {
		return nil, fmt.Errorf("storage: list characters for campaign %s: %w", campaignID, err)
	}
	defer rows.Close()

	var out []Character
	for rows.Next() {
		c, err := scanCharacter(rows)
		if err != nil {
			return nil, fmt.Errorf("storage: scan character: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list characters for campaign %s: %w", campaignID, err)
	}
	return out, nil
}

// GetCharacterByDiscordUser resolves the Character a Discord User plays in a
// Campaign, or ErrNotFound. It is the speaker → Character lookup Address Detection
// / transcript attribution consume (#281). The (campaign_id, discord_user_id)
// unique index guarantees at most one row, so a cross-campaign lookup for the same
// Discord User simply misses.
func (s *Store) GetCharacterByDiscordUser(ctx context.Context, campaignID uuid.UUID, discordUserID string) (Character, error) {
	row := s.db.QueryRow(ctx,
		`SELECT `+characterColumns+`
		   FROM character
		  WHERE campaign_id = $1 AND discord_user_id = $2`, campaignID, discordUserID)
	c, err := scanCharacter(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Character{}, ErrNotFound
	}
	if err != nil {
		return Character{}, fmt.Errorf("storage: get character (campaign %s, discord_user %s): %w", campaignID, discordUserID, err)
	}
	return c, nil
}
