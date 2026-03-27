package web

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// validTenantID enforces safe tenant IDs that can be used as PostgreSQL
// schema names (tenant_<id>). Must match the canonical pattern in
// config.validTenantID: starts with a lowercase letter, followed by
// lowercase alphanumeric and underscores, max 63 chars.
var validTenantID = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// User represents a web management user.
type User struct {
	ID          string          `json:"id"`
	TenantID    string          `json:"tenant_id"`
	DiscordID   *string         `json:"discord_id,omitempty"`
	GoogleID    *string         `json:"google_id,omitempty"`
	GitHubID    *string         `json:"github_id,omitempty"`
	Email       *string         `json:"email,omitempty"`
	DisplayName string          `json:"display_name"`
	AvatarURL   *string         `json:"avatar_url,omitempty"`
	Role        string          `json:"role"`
	Preferences json.RawMessage `json:"preferences,omitempty"`
	LastLoginAt *time.Time      `json:"last_login_at,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

// Invite represents an invitation to join a tenant.
type Invite struct {
	ID        string     `json:"id"`
	TenantID  string     `json:"tenant_id"`
	Role      string     `json:"role"`
	CreatedBy string     `json:"created_by"`
	Token     string     `json:"token"`
	ExpiresAt time.Time  `json:"expires_at"`
	UsedBy    *string    `json:"used_by,omitempty"`
	UsedAt    *time.Time `json:"used_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

// ValidRoles is the set of assignable roles.
var ValidRoles = map[string]bool{
	"viewer":       true,
	"dm":           true,
	"tenant_admin": true,
}

// Campaign represents a campaign owned by a tenant.
type Campaign struct {
	ID          string    `json:"id"`
	TenantID    string    `json:"tenant_id"`
	Name        string    `json:"name"`
	System      string    `json:"game_system,omitempty"`
	Language    string    `json:"language,omitempty"`
	Description string    `json:"description,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// LoreDocument is a rich-text lore entry attached to a campaign.
type LoreDocument struct {
	ID              string    `json:"id"`
	CampaignID      string    `json:"campaign_id"`
	Title           string    `json:"title"`
	ContentMarkdown string    `json:"content_markdown"`
	SortOrder       int       `json:"sort_order"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// CampaignNPCLink records an NPC linked to a secondary campaign.
type CampaignNPCLink struct {
	CampaignID string    `json:"campaign_id"`
	NPCID      string    `json:"npc_id"`
	CreatedAt  time.Time `json:"created_at"`
}

// KnowledgeEntity represents a knowledge-graph entity stored in a
// tenant-specific schema.
type KnowledgeEntity struct {
	CampaignID string         `json:"campaign_id"`
	ID         string         `json:"id"`
	Type       string         `json:"type"`
	Name       string         `json:"name"`
	Attributes map[string]any `json:"attributes,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
}

// AuditLogEntry represents a single audit log record.
type AuditLogEntry struct {
	ID           int64           `json:"id"`
	TenantID     *string         `json:"tenant_id,omitempty"`
	UserID       *string         `json:"user_id,omitempty"`
	Action       string          `json:"action"`
	ResourceType string          `json:"resource_type"`
	ResourceID   string          `json:"resource_id"`
	Changes      json.RawMessage `json:"changes,omitempty"`
	IPAddress    *string         `json:"ip_address,omitempty"`
	UserAgent    *string         `json:"user_agent,omitempty"`
	CreatedAt    time.Time       `json:"created_at"`
}

// AdminDashboardStats holds system-wide stats for the super admin dashboard.
type AdminDashboardStats struct {
	TotalTenants      int     `json:"total_tenants"`
	TotalUsers        int     `json:"total_users"`
	TotalCampaigns    int     `json:"total_campaigns"`
	ActiveSessions    int     `json:"active_sessions"`
	TotalSessionHours float64 `json:"total_session_hours"`
	AuditLogCount     int     `json:"audit_log_count"`
}

// WebStore defines the data operations required by the web management handlers.
// Implementations must be safe for concurrent use.
type WebStore interface {
	Ping(ctx context.Context) error
	UpsertDiscordUser(ctx context.Context, discordID, email, displayName, avatarURL, tenantID string) (*User, error)
	UpsertGoogleUser(ctx context.Context, googleID, email, displayName, avatarURL, tenantID string) (*User, error)
	UpsertGitHubUser(ctx context.Context, githubID, email, displayName, avatarURL, tenantID string) (*User, error)
	EnsureAdminUser(ctx context.Context, tenantID string) (*User, error)
	GetUser(ctx context.Context, id string) (*User, error)
	ListUsers(ctx context.Context, tenantID, role string, limit, offset int) ([]User, int, error)
	UpdateUser(ctx context.Context, u *User) error
	UpdateUserTenant(ctx context.Context, userID, tenantID, role string) error
	DeleteUser(ctx context.Context, tenantID, id string) error
	UpdateUserPreferences(ctx context.Context, id string, prefs json.RawMessage) (*User, error)
	CreateInvite(ctx context.Context, inv *Invite) error
	GetInviteByToken(ctx context.Context, token string) (*Invite, error)
	UseInvite(ctx context.Context, inviteID, userID string) error
	CreateCampaign(ctx context.Context, c *Campaign) error
	GetCampaign(ctx context.Context, tenantID, id string) (*Campaign, error)
	ListCampaigns(ctx context.Context, tenantID string, page CursorPage) ([]Campaign, error)
	UpdateCampaign(ctx context.Context, c *Campaign) error
	DeleteCampaign(ctx context.Context, tenantID, id string) error
	ListSessions(ctx context.Context, tenantID string, limit, offset int) ([]SessionSummary, error)
	SessionExists(ctx context.Context, tenantID, sessionID string) (bool, error)
	GetTranscript(ctx context.Context, tenantID, sessionID string) ([]TranscriptEntry, error)
	GetUsage(ctx context.Context, tenantID string, from, to time.Time) ([]UsageRecord, error)

	// Lore documents.
	CreateLoreDocument(ctx context.Context, doc *LoreDocument) error
	GetLoreDocument(ctx context.Context, campaignID, id string) (*LoreDocument, error)
	ListLoreDocuments(ctx context.Context, campaignID string) ([]LoreDocument, error)
	UpdateLoreDocument(ctx context.Context, doc *LoreDocument) error
	DeleteLoreDocument(ctx context.Context, campaignID, id string) error

	// Campaign-NPC links.
	LinkNPCToCampaign(ctx context.Context, campaignID, npcID string) error
	UnlinkNPCFromCampaign(ctx context.Context, campaignID, npcID string) error
	ListCampaignNPCLinks(ctx context.Context, campaignID string) ([]CampaignNPCLink, error)

	// Knowledge graph.
	ListKnowledgeEntities(ctx context.Context, tenantID, campaignID string, page CursorPage) ([]KnowledgeEntity, error)
	DeleteKnowledgeEntity(ctx context.Context, tenantID, campaignID, entityID string) error

	// Dashboard.
	GetDashboardStats(ctx context.Context, tenantID string) (*DashboardStats, error)
	GetRecentActivity(ctx context.Context, tenantID string, limit int) ([]ActivityItem, error)

	// Audit log.
	CreateAuditLog(ctx context.Context, entry *AuditLogEntry) error
	ListAuditLogs(ctx context.Context, tenantID string, limit, offset int, resourceType, action string) ([]AuditLogEntry, int, error)

	// Admin dashboard.
	GetAdminDashboardStats(ctx context.Context) (*AdminDashboardStats, error)
	ListAllTenantUsers(ctx context.Context, limit, offset int) ([]User, int, error)
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

// adminUserID is the well-known user ID for API-key authenticated admins.
const adminUserID = "apikey-admin"

// EnsureAdminUser upserts the well-known API-key admin user, returning it
// with the super_admin role. This is used by the API-key login flow.
func (s *Store) EnsureAdminUser(ctx context.Context, tenantID string) (*User, error) {
	now := time.Now().UTC()
	var user User
	err := s.pool.QueryRow(ctx, `
		INSERT INTO mgmt.users (id, tenant_id, display_name, role, last_login_at, created_at, updated_at)
		VALUES ($1, $2, 'Admin', 'super_admin', $3, $3, $3)
		ON CONFLICT (id) DO UPDATE SET
			last_login_at = EXCLUDED.last_login_at,
			updated_at = EXCLUDED.updated_at
		RETURNING id, tenant_id, discord_id, email, display_name, avatar_url, role, last_login_at, created_at, updated_at
	`, adminUserID, tenantID, now).Scan(
		&user.ID, &user.TenantID, &user.DiscordID, &user.Email,
		&user.DisplayName, &user.AvatarURL, &user.Role, &user.LastLoginAt,
		&user.CreatedAt, &user.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("web: ensure admin user: %w", err)
	}
	return &user, nil
}

// GetUser retrieves a user by ID.
func (s *Store) GetUser(ctx context.Context, id string) (*User, error) {
	var user User
	err := s.pool.QueryRow(ctx, `
		SELECT id, tenant_id, discord_id, email, display_name, avatar_url, role, preferences, last_login_at, created_at, updated_at
		FROM mgmt.users
		WHERE id = $1 AND deleted_at IS NULL
	`, id).Scan(
		&user.ID, &user.TenantID, &user.DiscordID, &user.Email,
		&user.DisplayName, &user.AvatarURL, &user.Role, &user.Preferences,
		&user.LastLoginAt, &user.CreatedAt, &user.UpdatedAt,
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
		INSERT INTO mgmt.campaigns (id, tenant_id, name, system, language, description, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, c.ID, c.TenantID, c.Name, c.System, c.Language, c.Description, c.CreatedAt, c.UpdatedAt)
	if err != nil {
		return fmt.Errorf("web: create campaign %q: %w", c.ID, err)
	}
	return nil
}

// GetCampaign retrieves a campaign by ID within a tenant.
func (s *Store) GetCampaign(ctx context.Context, tenantID, id string) (*Campaign, error) {
	var c Campaign
	err := s.pool.QueryRow(ctx, `
		SELECT id, tenant_id, name, system, language, description, created_at, updated_at
		FROM mgmt.campaigns
		WHERE id = $1 AND tenant_id = $2 AND deleted_at IS NULL
	`, id, tenantID).Scan(&c.ID, &c.TenantID, &c.Name, &c.System, &c.Language, &c.Description, &c.CreatedAt, &c.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("web: get campaign %q: %w", id, err)
	}
	return &c, nil
}

// ListCampaigns returns campaigns for a tenant with cursor-based pagination.
func (s *Store) ListCampaigns(ctx context.Context, tenantID string, page CursorPage) ([]Campaign, error) {
	limit := page.Limit
	if limit <= 0 || limit > 100 {
		limit = 25
	}

	var rows pgx.Rows
	var err error
	if page.Cursor != "" {
		cd, cerr := DecodeCursor(page.Cursor)
		if cerr != nil {
			return nil, fmt.Errorf("web: list campaigns: %w", cerr)
		}
		cursorTime := time.UnixMicro(cd.UnixMicros).UTC()
		rows, err = s.pool.Query(ctx, `
			SELECT id, tenant_id, name, system, language, description, created_at, updated_at
			FROM mgmt.campaigns
			WHERE tenant_id = $1 AND deleted_at IS NULL
			  AND (created_at, id) < ($3, $4)
			ORDER BY created_at DESC, id DESC
			LIMIT $2
		`, tenantID, limit+1, cursorTime, cd.ID)
	} else {
		rows, err = s.pool.Query(ctx, `
			SELECT id, tenant_id, name, system, language, description, created_at, updated_at
			FROM mgmt.campaigns
			WHERE tenant_id = $1 AND deleted_at IS NULL
			ORDER BY created_at DESC, id DESC
			LIMIT $2
		`, tenantID, limit+1)
	}
	if err != nil {
		return nil, fmt.Errorf("web: list campaigns: %w", err)
	}
	defer rows.Close()

	var campaigns []Campaign
	for rows.Next() {
		var c Campaign
		if err := rows.Scan(&c.ID, &c.TenantID, &c.Name, &c.System, &c.Language, &c.Description, &c.CreatedAt, &c.UpdatedAt); err != nil {
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
		SET name = $3, system = $4, language = $5, description = $6, updated_at = $7
		WHERE id = $1 AND tenant_id = $2 AND deleted_at IS NULL
	`, c.ID, c.TenantID, c.Name, c.System, c.Language, c.Description, c.UpdatedAt)
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
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
}

// ListSessions returns sessions for a tenant from the public.sessions table.
func (s *Store) ListSessions(ctx context.Context, tenantID string, limit, offset int) ([]SessionSummary, error) {
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, tenant_id, state, started_at, ended_at
		FROM sessions
		WHERE tenant_id = $1
		ORDER BY started_at DESC
		LIMIT $2 OFFSET $3
	`, tenantID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("web: list sessions: %w", err)
	}
	defer rows.Close()

	var sessions []SessionSummary
	for rows.Next() {
		var ss SessionSummary
		if err := rows.Scan(&ss.ID, &ss.TenantID, &ss.State, &ss.StartedAt, &ss.EndedAt); err != nil {
			return nil, fmt.Errorf("web: scan session: %w", err)
		}
		sessions = append(sessions, ss)
	}
	return sessions, rows.Err()
}

// SessionExists checks whether a session exists for the given tenant.
func (s *Store) SessionExists(ctx context.Context, tenantID, sessionID string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM sessions WHERE id = $1 AND tenant_id = $2)`,
		sessionID, tenantID,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("web: check session exists: %w", err)
	}
	return exists, nil
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

// ---------------------------------------------------------------------------
// Lore documents
// ---------------------------------------------------------------------------

// CreateLoreDocument inserts a new lore document.
func (s *Store) CreateLoreDocument(ctx context.Context, doc *LoreDocument) error {
	if doc.ID == "" {
		doc.ID = uuid.NewString()
	}
	now := time.Now().UTC()
	doc.CreatedAt = now
	doc.UpdatedAt = now
	_, err := s.pool.Exec(ctx, `
		INSERT INTO mgmt.lore_documents (id, campaign_id, title, content_markdown, sort_order, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, doc.ID, doc.CampaignID, doc.Title, doc.ContentMarkdown, doc.SortOrder, doc.CreatedAt, doc.UpdatedAt)
	if err != nil {
		return fmt.Errorf("web: create lore document %q: %w", doc.ID, err)
	}
	return nil
}

// GetLoreDocument retrieves a lore document by campaign and ID.
func (s *Store) GetLoreDocument(ctx context.Context, campaignID, id string) (*LoreDocument, error) {
	var doc LoreDocument
	err := s.pool.QueryRow(ctx, `
		SELECT id, campaign_id, title, content_markdown, sort_order, created_at, updated_at
		FROM mgmt.lore_documents
		WHERE id = $1 AND campaign_id = $2
	`, id, campaignID).Scan(&doc.ID, &doc.CampaignID, &doc.Title, &doc.ContentMarkdown, &doc.SortOrder, &doc.CreatedAt, &doc.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("web: get lore document %q: %w", id, err)
	}
	return &doc, nil
}

// ListLoreDocuments returns all lore documents for a campaign ordered by
// sort_order ascending.
func (s *Store) ListLoreDocuments(ctx context.Context, campaignID string) ([]LoreDocument, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, campaign_id, title, content_markdown, sort_order, created_at, updated_at
		FROM mgmt.lore_documents
		WHERE campaign_id = $1
		ORDER BY sort_order ASC, created_at ASC
	`, campaignID)
	if err != nil {
		return nil, fmt.Errorf("web: list lore documents: %w", err)
	}
	defer rows.Close()

	var docs []LoreDocument
	for rows.Next() {
		var d LoreDocument
		if err := rows.Scan(&d.ID, &d.CampaignID, &d.Title, &d.ContentMarkdown, &d.SortOrder, &d.CreatedAt, &d.UpdatedAt); err != nil {
			return nil, fmt.Errorf("web: scan lore document: %w", err)
		}
		docs = append(docs, d)
	}
	return docs, rows.Err()
}

// UpdateLoreDocument updates an existing lore document.
func (s *Store) UpdateLoreDocument(ctx context.Context, doc *LoreDocument) error {
	doc.UpdatedAt = time.Now().UTC()
	tag, err := s.pool.Exec(ctx, `
		UPDATE mgmt.lore_documents
		SET title = $3, content_markdown = $4, sort_order = $5, updated_at = $6
		WHERE id = $1 AND campaign_id = $2
	`, doc.ID, doc.CampaignID, doc.Title, doc.ContentMarkdown, doc.SortOrder, doc.UpdatedAt)
	if err != nil {
		return fmt.Errorf("web: update lore document %q: %w", doc.ID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("web: lore document %q not found", doc.ID)
	}
	return nil
}

// DeleteLoreDocument removes a lore document.
func (s *Store) DeleteLoreDocument(ctx context.Context, campaignID, id string) error {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM mgmt.lore_documents WHERE id = $1 AND campaign_id = $2
	`, id, campaignID)
	if err != nil {
		return fmt.Errorf("web: delete lore document %q: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("web: lore document %q not found", id)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Campaign-NPC links
// ---------------------------------------------------------------------------

// LinkNPCToCampaign creates a link between an NPC and a secondary campaign.
// If the link already exists it is a no-op.
func (s *Store) LinkNPCToCampaign(ctx context.Context, campaignID, npcID string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO mgmt.campaign_npcs (campaign_id, npc_id, created_at)
		VALUES ($1, $2, now())
		ON CONFLICT DO NOTHING
	`, campaignID, npcID)
	if err != nil {
		return fmt.Errorf("web: link NPC %q to campaign %q: %w", npcID, campaignID, err)
	}
	return nil
}

// UnlinkNPCFromCampaign removes a campaign-NPC link.
func (s *Store) UnlinkNPCFromCampaign(ctx context.Context, campaignID, npcID string) error {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM mgmt.campaign_npcs WHERE campaign_id = $1 AND npc_id = $2
	`, campaignID, npcID)
	if err != nil {
		return fmt.Errorf("web: unlink NPC %q from campaign %q: %w", npcID, campaignID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("web: link not found for NPC %q in campaign %q", npcID, campaignID)
	}
	return nil
}

// ListCampaignNPCLinks returns all NPC links for a campaign.
func (s *Store) ListCampaignNPCLinks(ctx context.Context, campaignID string) ([]CampaignNPCLink, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT campaign_id, npc_id, created_at
		FROM mgmt.campaign_npcs
		WHERE campaign_id = $1
		ORDER BY created_at ASC
	`, campaignID)
	if err != nil {
		return nil, fmt.Errorf("web: list campaign NPC links: %w", err)
	}
	defer rows.Close()

	var links []CampaignNPCLink
	for rows.Next() {
		var l CampaignNPCLink
		if err := rows.Scan(&l.CampaignID, &l.NPCID, &l.CreatedAt); err != nil {
			return nil, fmt.Errorf("web: scan campaign NPC link: %w", err)
		}
		links = append(links, l)
	}
	return links, rows.Err()
}

// ---------------------------------------------------------------------------
// Knowledge graph
// ---------------------------------------------------------------------------

// ListKnowledgeEntities retrieves knowledge-graph entities from the
// tenant-specific schema (tenant_<id>.entities) for a given campaign.
func (s *Store) ListKnowledgeEntities(ctx context.Context, tenantID, campaignID string, page CursorPage) ([]KnowledgeEntity, error) {
	if !validTenantID.MatchString(tenantID) {
		return nil, fmt.Errorf("web: invalid tenant ID %q", tenantID)
	}
	schema := "tenant_" + tenantID
	table := pgx.Identifier{schema, "entities"}.Sanitize()

	limit := page.Limit
	if limit <= 0 || limit > 100 {
		limit = 25
	}

	var rows pgx.Rows
	var err error
	if page.Cursor != "" {
		cd, cerr := DecodeCursor(page.Cursor)
		if cerr != nil {
			return nil, fmt.Errorf("web: list knowledge entities: %w", cerr)
		}
		cursorTime := time.UnixMicro(cd.UnixMicros).UTC()
		query := fmt.Sprintf(`
			SELECT id, type, name, attributes, created_at, updated_at
			FROM %s
			WHERE (created_at, id) < ($2, $3)
			ORDER BY created_at DESC, id DESC
			LIMIT $1
		`, table)
		rows, err = s.pool.Query(ctx, query, limit+1, cursorTime, cd.ID)
	} else {
		query := fmt.Sprintf(`
			SELECT id, type, name, attributes, created_at, updated_at
			FROM %s
			ORDER BY created_at DESC, id DESC
			LIMIT $1
		`, table)
		rows, err = s.pool.Query(ctx, query, limit+1)
	}
	if err != nil {
		return nil, fmt.Errorf("web: list knowledge entities: %w", err)
	}
	defer rows.Close()

	var entities []KnowledgeEntity
	for rows.Next() {
		var e KnowledgeEntity
		if err := rows.Scan(&e.ID, &e.Type, &e.Name, &e.Attributes, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, fmt.Errorf("web: scan knowledge entity: %w", err)
		}
		e.CampaignID = campaignID
		entities = append(entities, e)
	}
	return entities, rows.Err()
}

// DeleteKnowledgeEntity removes a knowledge-graph entity from the
// tenant-specific schema.
func (s *Store) DeleteKnowledgeEntity(ctx context.Context, tenantID, campaignID, entityID string) error {
	if !validTenantID.MatchString(tenantID) {
		return fmt.Errorf("web: invalid tenant ID %q", tenantID)
	}
	schema := "tenant_" + tenantID
	table := pgx.Identifier{schema, "entities"}.Sanitize()

	query := fmt.Sprintf(`DELETE FROM %s WHERE id = $1`, table)
	tag, err := s.pool.Exec(ctx, query, entityID)
	if err != nil {
		return fmt.Errorf("web: delete knowledge entity %q: %w", entityID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("web: knowledge entity %q not found", entityID)
	}
	return nil
}

// ---------------------------------------------------------------------------
// User management
// ---------------------------------------------------------------------------

// ListUsers returns users for a tenant, optionally filtered by role. Returns
// the user slice and total count for pagination.
func (s *Store) ListUsers(ctx context.Context, tenantID, role string, limit, offset int) ([]User, int, error) {
	if limit <= 0 || limit > 100 {
		limit = 25
	}

	where := "tenant_id = $1 AND deleted_at IS NULL"
	args := []any{tenantID}
	if role != "" {
		args = append(args, role)
		where += fmt.Sprintf(" AND role = $%d", len(args))
	}

	var total int
	err := s.pool.QueryRow(ctx, "SELECT count(*) FROM mgmt.users WHERE "+where, args...).Scan(&total)
	if err != nil {
		return nil, 0, fmt.Errorf("web: count users: %w", err)
	}

	args = append(args, limit, offset)
	query := fmt.Sprintf(`
		SELECT id, tenant_id, discord_id, email, display_name, avatar_url, role, preferences, last_login_at, created_at, updated_at
		FROM mgmt.users
		WHERE %s
		ORDER BY created_at DESC
		LIMIT $%d OFFSET $%d
	`, where, len(args)-1, len(args))

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("web: list users: %w", err)
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.TenantID, &u.DiscordID, &u.Email,
			&u.DisplayName, &u.AvatarURL, &u.Role, &u.Preferences,
			&u.LastLoginAt, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, 0, fmt.Errorf("web: scan user: %w", err)
		}
		users = append(users, u)
	}
	return users, total, rows.Err()
}

// UpdateUser updates mutable fields on a user (display_name, role).
func (s *Store) UpdateUser(ctx context.Context, u *User) error {
	u.UpdatedAt = time.Now().UTC()

	sets := []string{}
	args := []any{u.ID}
	idx := 2

	if u.DisplayName != "" {
		sets = append(sets, fmt.Sprintf("display_name = $%d", idx))
		args = append(args, u.DisplayName)
		idx++
	}
	if u.Role != "" {
		sets = append(sets, fmt.Sprintf("role = $%d", idx))
		args = append(args, u.Role)
		idx++
	}

	sets = append(sets, fmt.Sprintf("updated_at = $%d", idx))
	args = append(args, u.UpdatedAt)

	query := fmt.Sprintf("UPDATE mgmt.users SET %s WHERE id = $1 AND deleted_at IS NULL", strings.Join(sets, ", "))
	tag, err := s.pool.Exec(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("web: update user %q: %w", u.ID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("web: user %q not found", u.ID)
	}
	return nil
}

// DeleteUser soft-deletes a user. The tenant_id parameter ensures users
// cannot be deleted across tenant boundaries (defense-in-depth).
func (s *Store) DeleteUser(ctx context.Context, tenantID, id string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE mgmt.users SET deleted_at = now() WHERE id = $1 AND tenant_id = $2 AND deleted_at IS NULL
	`, id, tenantID)
	if err != nil {
		return fmt.Errorf("web: delete user %q: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("web: user %q not found", id)
	}
	return nil
}

// UpdateUserPreferences merges preferences JSON into the existing preferences
// column and returns the updated user.
func (s *Store) UpdateUserPreferences(ctx context.Context, id string, prefs json.RawMessage) (*User, error) {
	var user User
	err := s.pool.QueryRow(ctx, `
		UPDATE mgmt.users
		SET preferences = preferences || $2, updated_at = now()
		WHERE id = $1 AND deleted_at IS NULL
		RETURNING id, tenant_id, discord_id, email, display_name, avatar_url, role, preferences, last_login_at, created_at, updated_at
	`, id, prefs).Scan(
		&user.ID, &user.TenantID, &user.DiscordID, &user.Email,
		&user.DisplayName, &user.AvatarURL, &user.Role, &user.Preferences,
		&user.LastLoginAt, &user.CreatedAt, &user.UpdatedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("web: update preferences for user %q: %w", id, err)
	}
	return &user, nil
}

// CreateInvite persists a new invite, generating a secure random token.
func (s *Store) CreateInvite(ctx context.Context, inv *Invite) error {
	if inv.ID == "" {
		inv.ID = uuid.NewString()
	}
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return fmt.Errorf("web: generate invite token: %w", err)
	}
	inv.Token = hex.EncodeToString(b)
	inv.CreatedAt = time.Now().UTC()
	if inv.ExpiresAt.IsZero() {
		inv.ExpiresAt = inv.CreatedAt.Add(7 * 24 * time.Hour)
	}

	_, err := s.pool.Exec(ctx, `
		INSERT INTO mgmt.invites (id, tenant_id, role, created_by, token, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, inv.ID, inv.TenantID, inv.Role, inv.CreatedBy, inv.Token, inv.ExpiresAt, inv.CreatedAt)
	if err != nil {
		return fmt.Errorf("web: create invite: %w", err)
	}
	return nil
}

// GetInviteByToken retrieves an unused, non-expired invite by its token.
func (s *Store) GetInviteByToken(ctx context.Context, token string) (*Invite, error) {
	var inv Invite
	err := s.pool.QueryRow(ctx, `
		SELECT id, tenant_id, role, created_by, token, expires_at, used_by, used_at, created_at
		FROM mgmt.invites
		WHERE token = $1 AND used_at IS NULL AND expires_at > now()
	`, token).Scan(
		&inv.ID, &inv.TenantID, &inv.Role, &inv.CreatedBy, &inv.Token,
		&inv.ExpiresAt, &inv.UsedBy, &inv.UsedAt, &inv.CreatedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("web: get invite by token: %w", err)
	}
	return &inv, nil
}

// UseInvite marks an invite as used by the given user.
func (s *Store) UseInvite(ctx context.Context, inviteID, userID string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE mgmt.invites SET used_by = $2, used_at = now() WHERE id = $1 AND used_at IS NULL
	`, inviteID, userID)
	if err != nil {
		return fmt.Errorf("web: use invite %q: %w", inviteID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("web: invite %q not found or already used", inviteID)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Dashboard
// ---------------------------------------------------------------------------

// GetDashboardStats returns aggregate statistics for a tenant's dashboard.
func (s *Store) GetDashboardStats(ctx context.Context, tenantID string) (*DashboardStats, error) {
	stats := &DashboardStats{}

	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM mgmt.campaigns
		WHERE tenant_id = $1 AND deleted_at IS NULL
	`, tenantID).Scan(&stats.CampaignCount)
	if err != nil {
		return nil, fmt.Errorf("web: dashboard campaign count: %w", err)
	}

	err = s.pool.QueryRow(ctx, `
		SELECT count(*) FROM sessions
		WHERE tenant_id = $1 AND state = 'running'
	`, tenantID).Scan(&stats.ActiveSessionCount)
	if err != nil {
		stats.ActiveSessionCount = 0
	}

	now := time.Now().UTC()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	err = s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(session_hours), 0) FROM usage_records
		WHERE tenant_id = $1 AND period >= $2
	`, tenantID, monthStart).Scan(&stats.HoursUsed)
	if err != nil {
		stats.HoursUsed = 0
	}

	return stats, nil
}

// GetRecentActivity returns the most recent activity items for a tenant,
// synthesised from campaigns, sessions, and users.
func (s *Store) GetRecentActivity(ctx context.Context, tenantID string, limit int) ([]ActivityItem, error) {
	if limit <= 0 {
		limit = 10
	}
	var items []ActivityItem

	rows, err := s.pool.Query(ctx, `
		SELECT id, name, created_at FROM mgmt.campaigns
		WHERE tenant_id = $1 AND deleted_at IS NULL
		ORDER BY created_at DESC LIMIT $2
	`, tenantID, limit)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var id, name string
			var ts time.Time
			if err := rows.Scan(&id, &name, &ts); err != nil {
				continue
			}
			items = append(items, ActivityItem{
				ID:          id,
				Type:        "campaign_created",
				Description: "Campaign created: " + name,
				Timestamp:   ts,
				CampaignID:  id,
			})
		}
	}

	srows, err := s.pool.Query(ctx, `
		SELECT id, state, started_at, ended_at FROM sessions
		WHERE tenant_id = $1
		ORDER BY started_at DESC LIMIT $2
	`, tenantID, limit)
	if err == nil {
		defer srows.Close()
		for srows.Next() {
			var ss SessionSummary
			if err := srows.Scan(&ss.ID, &ss.State, &ss.StartedAt, &ss.EndedAt); err != nil {
				continue
			}
			if ss.State == "running" {
				items = append(items, ActivityItem{
					ID:          ss.ID,
					Type:        "session_started",
					Description: "Session started",
					Timestamp:   ss.StartedAt,
				})
			} else if ss.EndedAt != nil {
				items = append(items, ActivityItem{
					ID:          ss.ID,
					Type:        "session_ended",
					Description: "Session ended",
					Timestamp:   *ss.EndedAt,
				})
			}
		}
	}

	for i := 0; i < len(items); i++ {
		for j := i + 1; j < len(items); j++ {
			if items[j].Timestamp.After(items[i].Timestamp) {
				items[i], items[j] = items[j], items[i]
			}
		}
	}
	if len(items) > limit {
		items = items[:limit]
	}

	return items, nil
}

// UpsertGoogleUser creates or updates a user based on their Google ID.
func (s *Store) UpsertGoogleUser(ctx context.Context, googleID, email, displayName, avatarURL, tenantID string) (*User, error) {
	id := uuid.NewString()
	now := time.Now().UTC()

	var user User
	err := s.pool.QueryRow(ctx, `
		INSERT INTO mgmt.users (id, tenant_id, google_id, email, display_name, avatar_url, role, last_login_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, 'dm', $7, $7, $7)
		ON CONFLICT (google_id) DO UPDATE SET
			email = COALESCE(EXCLUDED.email, mgmt.users.email),
			display_name = EXCLUDED.display_name,
			avatar_url = EXCLUDED.avatar_url,
			last_login_at = EXCLUDED.last_login_at,
			updated_at = EXCLUDED.updated_at
		RETURNING id, tenant_id, google_id, email, display_name, avatar_url, role, last_login_at, created_at, updated_at
	`, id, tenantID, googleID, email, displayName, avatarURL, now).Scan(
		&user.ID, &user.TenantID, &user.GoogleID, &user.Email,
		&user.DisplayName, &user.AvatarURL, &user.Role, &user.LastLoginAt,
		&user.CreatedAt, &user.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("web: upsert google user: %w", err)
	}
	return &user, nil
}

// UpsertGitHubUser creates or updates a user based on their GitHub ID.
func (s *Store) UpsertGitHubUser(ctx context.Context, githubID, email, displayName, avatarURL, tenantID string) (*User, error) {
	id := uuid.NewString()
	now := time.Now().UTC()

	var user User
	err := s.pool.QueryRow(ctx, `
		INSERT INTO mgmt.users (id, tenant_id, github_id, email, display_name, avatar_url, role, last_login_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, 'dm', $7, $7, $7)
		ON CONFLICT (github_id) DO UPDATE SET
			email = COALESCE(EXCLUDED.email, mgmt.users.email),
			display_name = EXCLUDED.display_name,
			avatar_url = EXCLUDED.avatar_url,
			last_login_at = EXCLUDED.last_login_at,
			updated_at = EXCLUDED.updated_at
		RETURNING id, tenant_id, github_id, email, display_name, avatar_url, role, last_login_at, created_at, updated_at
	`, id, tenantID, githubID, email, displayName, avatarURL, now).Scan(
		&user.ID, &user.TenantID, &user.GitHubID, &user.Email,
		&user.DisplayName, &user.AvatarURL, &user.Role, &user.LastLoginAt,
		&user.CreatedAt, &user.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("web: upsert github user: %w", err)
	}
	return &user, nil
}

// CreateAuditLog inserts a new audit log entry.
func (s *Store) CreateAuditLog(ctx context.Context, entry *AuditLogEntry) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO mgmt.audit_log (tenant_id, user_id, action, resource_type, resource_id, changes, ip_address, user_agent, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7::INET, $8, COALESCE($9, now()))
	`, entry.TenantID, entry.UserID, entry.Action, entry.ResourceType, entry.ResourceID,
		entry.Changes, entry.IPAddress, entry.UserAgent, entry.CreatedAt)
	if err != nil {
		return fmt.Errorf("web: create audit log: %w", err)
	}
	return nil
}

// ListAuditLogs returns audit log entries with pagination and optional filtering.
func (s *Store) ListAuditLogs(ctx context.Context, tenantID string, limit, offset int, resourceType, action string) ([]AuditLogEntry, int, error) {
	if limit <= 0 || limit > 100 {
		limit = 25
	}

	// Build WHERE clause dynamically.
	where := "WHERE 1=1"
	args := []any{}
	argIdx := 1

	if tenantID != "" {
		where += fmt.Sprintf(" AND tenant_id = $%d", argIdx)
		args = append(args, tenantID)
		argIdx++
	}
	if resourceType != "" {
		where += fmt.Sprintf(" AND resource_type = $%d", argIdx)
		args = append(args, resourceType)
		argIdx++
	}
	if action != "" {
		where += fmt.Sprintf(" AND action = $%d", argIdx)
		args = append(args, action)
		argIdx++
	}

	// Count total.
	var total int
	countQuery := "SELECT COUNT(*) FROM mgmt.audit_log " + where
	if err := s.pool.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("web: count audit logs: %w", err)
	}

	// Fetch entries.
	query := fmt.Sprintf(`SELECT id, tenant_id, user_id, action, resource_type, resource_id, changes, ip_address::TEXT, user_agent, created_at
		FROM mgmt.audit_log %s ORDER BY created_at DESC LIMIT $%d OFFSET $%d`, where, argIdx, argIdx+1)
	args = append(args, limit, offset)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("web: list audit logs: %w", err)
	}
	defer rows.Close()

	var entries []AuditLogEntry
	for rows.Next() {
		var e AuditLogEntry
		if err := rows.Scan(&e.ID, &e.TenantID, &e.UserID, &e.Action, &e.ResourceType, &e.ResourceID, &e.Changes, &e.IPAddress, &e.UserAgent, &e.CreatedAt); err != nil {
			return nil, 0, fmt.Errorf("web: scan audit log entry: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, total, nil
}

// GetAdminDashboardStats returns system-wide aggregate stats for the super admin dashboard.
func (s *Store) GetAdminDashboardStats(ctx context.Context) (*AdminDashboardStats, error) {
	stats := &AdminDashboardStats{}

	// Count tenants.
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM public.tenants`).Scan(&stats.TotalTenants); err != nil {
		slog.Warn("web: admin stats count tenants", "err", err)
	}

	// Count users.
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM mgmt.users WHERE deleted_at IS NULL`).Scan(&stats.TotalUsers); err != nil {
		slog.Warn("web: admin stats count users", "err", err)
	}

	// Count campaigns.
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM mgmt.campaigns WHERE deleted_at IS NULL`).Scan(&stats.TotalCampaigns); err != nil {
		slog.Warn("web: admin stats count campaigns", "err", err)
	}

	// Active sessions.
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM public.sessions WHERE state = 'running'`).Scan(&stats.ActiveSessions); err != nil {
		slog.Warn("web: admin stats active sessions", "err", err)
	}

	// Total session hours this month.
	now := time.Now().UTC()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	if err := s.pool.QueryRow(ctx, `SELECT COALESCE(SUM(session_hours), 0) FROM public.usage_records WHERE period >= $1`, monthStart).Scan(&stats.TotalSessionHours); err != nil {
		slog.Warn("web: admin stats session hours", "err", err)
	}

	// Audit log count.
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM mgmt.audit_log`).Scan(&stats.AuditLogCount); err != nil {
		slog.Warn("web: admin stats audit count", "err", err)
	}

	return stats, nil
}

// ListAllTenantUsers returns all users across all tenants (super_admin use).
func (s *Store) ListAllTenantUsers(ctx context.Context, limit, offset int) ([]User, int, error) {
	if limit <= 0 || limit > 100 {
		limit = 25
	}

	var total int
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM mgmt.users WHERE deleted_at IS NULL`).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("web: count all users: %w", err)
	}

	rows, err := s.pool.Query(ctx, `
		SELECT id, tenant_id, discord_id, email, display_name, avatar_url, role, last_login_at, created_at, updated_at
		FROM mgmt.users WHERE deleted_at IS NULL
		ORDER BY created_at DESC
		LIMIT $1 OFFSET $2
	`, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("web: list all users: %w", err)
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.TenantID, &u.DiscordID, &u.Email, &u.DisplayName, &u.AvatarURL, &u.Role, &u.LastLoginAt, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, 0, fmt.Errorf("web: scan user: %w", err)
		}
		users = append(users, u)
	}
	return users, total, nil
}
