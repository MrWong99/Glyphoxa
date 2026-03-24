package web

import (
	"context"
	"embed"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// validTenantID matches lowercase alphanumeric IDs with optional hyphens/
// underscores — the characters safe for use in a PostgreSQL schema name.
var validTenantID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,62}$`)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// User represents a web management user.
type User struct {
	ID          string     `json:"id"`
	TenantID    string     `json:"tenant_id"`
	DiscordID   string     `json:"discord_id,omitempty"`
	Email       string     `json:"email,omitempty"`
	DisplayName string     `json:"display_name"`
	AvatarURL   string     `json:"avatar_url,omitempty"`
	Role        string     `json:"role"`
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// Campaign represents a campaign owned by a tenant.
type Campaign struct {
	ID          string    `json:"id"`
	TenantID    string    `json:"tenant_id"`
	Name        string    `json:"name"`
	System      string    `json:"system,omitempty"`
	Description string    `json:"description,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// WebStore defines the data operations required by the web management handlers.
// Implementations must be safe for concurrent use.
type WebStore interface {
	Ping(ctx context.Context) error
	UpsertDiscordUser(ctx context.Context, discordID, email, displayName, avatarURL, tenantID string) (*User, error)
	GetUser(ctx context.Context, id string) (*User, error)
	CreateCampaign(ctx context.Context, c *Campaign) error
	GetCampaign(ctx context.Context, tenantID, id string) (*Campaign, error)
	ListCampaigns(ctx context.Context, tenantID string) ([]Campaign, error)
	UpdateCampaign(ctx context.Context, c *Campaign) error
	DeleteCampaign(ctx context.Context, tenantID, id string) error
	ListSessions(ctx context.Context, tenantID string, limit, offset int) ([]SessionSummary, error)
	GetTranscript(ctx context.Context, tenantID, sessionID string) ([]TranscriptEntry, error)
	GetUsage(ctx context.Context, tenantID string, from, to time.Time) ([]UsageRecord, error)
}

// Compile-time assertion that *Store implements WebStore.
var _ WebStore = (*Store)(nil)

// Store provides database operations for the web management service.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore creates a Store and runs embedded migrations.
func NewStore(ctx context.Context, pool *pgxpool.Pool) (*Store, error) {
	s := &Store{pool: pool}
	if err := s.migrate(ctx); err != nil {
		return nil, fmt.Errorf("web: run migrations: %w", err)
	}
	return s, nil
}

// Ping checks database connectivity.
func (s *Store) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

func (s *Store) migrate(ctx context.Context) error {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("web: read migrations dir: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		data, err := migrationsFS.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return fmt.Errorf("web: read migration %s: %w", entry.Name(), err)
		}
		if _, err := s.pool.Exec(ctx, string(data)); err != nil {
			return fmt.Errorf("web: exec migration %s: %w", entry.Name(), err)
		}
		slog.Info("web: applied migration", "file", entry.Name())
	}
	return nil
}

// UpsertDiscordUser creates or updates a user based on their Discord ID.
// Used during OAuth2 callback to ensure the user exists.
func (s *Store) UpsertDiscordUser(ctx context.Context, discordID, email, displayName, avatarURL, tenantID string) (*User, error) {
	id := uuid.NewString()
	now := time.Now().UTC()

	var user User
	err := s.pool.QueryRow(ctx, `
		INSERT INTO mgmt.users (id, tenant_id, discord_id, email, display_name, avatar_url, role, last_login_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, 'dm', $7, $7, $7)
		ON CONFLICT (discord_id) DO UPDATE SET
			email = COALESCE(EXCLUDED.email, mgmt.users.email),
			display_name = EXCLUDED.display_name,
			avatar_url = EXCLUDED.avatar_url,
			last_login_at = EXCLUDED.last_login_at,
			updated_at = EXCLUDED.updated_at
		RETURNING id, tenant_id, discord_id, email, display_name, avatar_url, role, last_login_at, created_at, updated_at
	`, id, tenantID, discordID, email, displayName, avatarURL, now).Scan(
		&user.ID, &user.TenantID, &user.DiscordID, &user.Email,
		&user.DisplayName, &user.AvatarURL, &user.Role, &user.LastLoginAt,
		&user.CreatedAt, &user.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("web: upsert discord user: %w", err)
	}
	return &user, nil
}

// GetUser retrieves a user by ID.
func (s *Store) GetUser(ctx context.Context, id string) (*User, error) {
	var user User
	err := s.pool.QueryRow(ctx, `
		SELECT id, tenant_id, discord_id, email, display_name, avatar_url, role, last_login_at, created_at, updated_at
		FROM mgmt.users
		WHERE id = $1 AND deleted_at IS NULL
	`, id).Scan(
		&user.ID, &user.TenantID, &user.DiscordID, &user.Email,
		&user.DisplayName, &user.AvatarURL, &user.Role, &user.LastLoginAt,
		&user.CreatedAt, &user.UpdatedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("web: get user %q: %w", id, err)
	}
	return &user, nil
}

// CreateCampaign inserts a new campaign.
func (s *Store) CreateCampaign(ctx context.Context, c *Campaign) error {
	if c.ID == "" {
		c.ID = uuid.NewString()
	}
	now := time.Now().UTC()
	c.CreatedAt = now
	c.UpdatedAt = now
	_, err := s.pool.Exec(ctx, `
		INSERT INTO mgmt.campaigns (id, tenant_id, name, system, description, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, c.ID, c.TenantID, c.Name, c.System, c.Description, c.CreatedAt, c.UpdatedAt)
	if err != nil {
		return fmt.Errorf("web: create campaign %q: %w", c.ID, err)
	}
	return nil
}

// GetCampaign retrieves a campaign by ID within a tenant.
func (s *Store) GetCampaign(ctx context.Context, tenantID, id string) (*Campaign, error) {
	var c Campaign
	err := s.pool.QueryRow(ctx, `
		SELECT id, tenant_id, name, system, description, created_at, updated_at
		FROM mgmt.campaigns
		WHERE id = $1 AND tenant_id = $2 AND deleted_at IS NULL
	`, id, tenantID).Scan(&c.ID, &c.TenantID, &c.Name, &c.System, &c.Description, &c.CreatedAt, &c.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("web: get campaign %q: %w", id, err)
	}
	return &c, nil
}

// ListCampaigns returns all campaigns for a tenant.
func (s *Store) ListCampaigns(ctx context.Context, tenantID string) ([]Campaign, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, tenant_id, name, system, description, created_at, updated_at
		FROM mgmt.campaigns
		WHERE tenant_id = $1 AND deleted_at IS NULL
		ORDER BY created_at DESC
	`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("web: list campaigns: %w", err)
	}
	defer rows.Close()

	var campaigns []Campaign
	for rows.Next() {
		var c Campaign
		if err := rows.Scan(&c.ID, &c.TenantID, &c.Name, &c.System, &c.Description, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("web: scan campaign: %w", err)
		}
		campaigns = append(campaigns, c)
	}
	return campaigns, rows.Err()
}

// UpdateCampaign updates an existing campaign.
func (s *Store) UpdateCampaign(ctx context.Context, c *Campaign) error {
	c.UpdatedAt = time.Now().UTC()
	tag, err := s.pool.Exec(ctx, `
		UPDATE mgmt.campaigns
		SET name = $3, system = $4, description = $5, updated_at = $6
		WHERE id = $1 AND tenant_id = $2 AND deleted_at IS NULL
	`, c.ID, c.TenantID, c.Name, c.System, c.Description, c.UpdatedAt)
	if err != nil {
		return fmt.Errorf("web: update campaign %q: %w", c.ID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("web: campaign %q not found", c.ID)
	}
	return nil
}

// DeleteCampaign soft-deletes a campaign.
func (s *Store) DeleteCampaign(ctx context.Context, tenantID, id string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE mgmt.campaigns SET deleted_at = now() WHERE id = $1 AND tenant_id = $2 AND deleted_at IS NULL
	`, id, tenantID)
	if err != nil {
		return fmt.Errorf("web: delete campaign %q: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("web: campaign %q not found", id)
	}
	return nil
}

// SessionSummary is a lightweight session record for list views.
type SessionSummary struct {
	ID        string     `json:"id"`
	TenantID  string     `json:"tenant_id"`
	State     string     `json:"state"`
	CreatedAt time.Time  `json:"created_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
}

// ListSessions returns sessions for a tenant from the public.sessions table.
func (s *Store) ListSessions(ctx context.Context, tenantID string, limit, offset int) ([]SessionSummary, error) {
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, tenant_id, state, created_at, ended_at
		FROM sessions
		WHERE tenant_id = $1
		ORDER BY created_at DESC
		LIMIT $2 OFFSET $3
	`, tenantID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("web: list sessions: %w", err)
	}
	defer rows.Close()

	var sessions []SessionSummary
	for rows.Next() {
		var ss SessionSummary
		if err := rows.Scan(&ss.ID, &ss.TenantID, &ss.State, &ss.CreatedAt, &ss.EndedAt); err != nil {
			return nil, fmt.Errorf("web: scan session: %w", err)
		}
		sessions = append(sessions, ss)
	}
	return sessions, rows.Err()
}

// TranscriptEntry represents a single entry in a session transcript.
type TranscriptEntry struct {
	ID        int64     `json:"id"`
	Speaker   string    `json:"speaker"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

// GetTranscript retrieves transcript entries for a session from the
// tenant-specific schema.
func (s *Store) GetTranscript(ctx context.Context, tenantID, sessionID string) ([]TranscriptEntry, error) {
	if !validTenantID.MatchString(tenantID) {
		return nil, fmt.Errorf("web: invalid tenant ID %q", tenantID)
	}
	// Per-tenant schema: tenant_<id>.session_entries
	schema := "tenant_" + tenantID
	query := fmt.Sprintf(`
		SELECT id, speaker, content, created_at
		FROM %s.session_entries
		WHERE session_id = $1
		ORDER BY created_at ASC
	`, pgx.Identifier{schema}.Sanitize())

	rows, err := s.pool.Query(ctx, query, sessionID)
	if err != nil {
		return nil, fmt.Errorf("web: get transcript for session %q: %w", sessionID, err)
	}
	defer rows.Close()

	var entries []TranscriptEntry
	for rows.Next() {
		var e TranscriptEntry
		if err := rows.Scan(&e.ID, &e.Speaker, &e.Content, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("web: scan transcript entry: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// UsageRecord represents monthly usage for a tenant.
type UsageRecord struct {
	TenantID     string    `json:"tenant_id"`
	Period       time.Time `json:"period"`
	SessionHours float64   `json:"session_hours"`
	LLMTokens    int64     `json:"llm_tokens"`
	TTSChars     int64     `json:"tts_chars"`
	STTSeconds   float64   `json:"stt_seconds"`
}

// GetUsage retrieves usage records for a tenant over a date range.
func (s *Store) GetUsage(ctx context.Context, tenantID string, from, to time.Time) ([]UsageRecord, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT tenant_id, period, session_hours, llm_tokens, tts_chars, stt_seconds
		FROM usage_records
		WHERE tenant_id = $1 AND period >= $2 AND period <= $3
		ORDER BY period DESC
	`, tenantID, from, to)
	if err != nil {
		return nil, fmt.Errorf("web: get usage: %w", err)
	}
	defer rows.Close()

	var records []UsageRecord
	for rows.Next() {
		var r UsageRecord
		if err := rows.Scan(&r.TenantID, &r.Period, &r.SessionHours, &r.LLMTokens, &r.TTSChars, &r.STTSeconds); err != nil {
			return nil, fmt.Errorf("web: scan usage record: %w", err)
		}
		records = append(records, r)
	}
	return records, rows.Err()
}
