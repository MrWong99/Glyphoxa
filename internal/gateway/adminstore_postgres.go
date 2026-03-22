package gateway

import (
	"context"
	"embed"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MrWong99/glyphoxa/internal/config"
	"github.com/MrWong99/glyphoxa/internal/gateway/vault"
)

//go:embed migrations/*.sql
var adminMigrationsFS embed.FS

// Compile-time interface assertion.
var _ AdminStore = (*PostgresAdminStore)(nil)

// PostgresAdminStore is a PostgreSQL-backed implementation of AdminStore.
// It manages tenant records in the tenants table.
//
// When a [vault.TokenEncryptor] is configured, bot tokens are encrypted via
// Vault Transit before INSERT and decrypted after SELECT. Pre-existing
// plaintext tokens (without the "vault:v1:" prefix) are read transparently.
//
// All methods are safe for concurrent use (backed by pgxpool).
type PostgresAdminStore struct {
	pool      *pgxpool.Pool
	encryptor vault.TokenEncryptor
}

// adminMigrationFiles lists the up-migration files in order.
var adminMigrationFiles = []string{
	"migrations/000001_tenants.up.sql",
}

// NewPostgresAdminStore creates a PostgreSQL-backed admin store.
// It runs migrations to ensure the tenants table exists.
//
// If enc is nil, a [vault.NoopEncryptor] is used (no encryption).
func NewPostgresAdminStore(ctx context.Context, pool *pgxpool.Pool, enc vault.TokenEncryptor) (*PostgresAdminStore, error) {
	if err := runAdminMigrations(ctx, pool); err != nil {
		return nil, fmt.Errorf("gateway: run admin migrations: %w", err)
	}
	if enc == nil {
		enc = vault.NoopEncryptor{}
	}
	return &PostgresAdminStore{pool: pool, encryptor: enc}, nil
}

// runAdminMigrations applies the embedded SQL migration files in order.
func runAdminMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	for _, f := range adminMigrationFiles {
		upSQL, err := adminMigrationsFS.ReadFile(f)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", f, err)
		}

		_, err = pool.Exec(ctx, string(upSQL))
		if err != nil {
			return fmt.Errorf("exec migration %s: %w", f, err)
		}
	}

	slog.Info("gateway: admin migrations applied")
	return nil
}

// CreateTenant inserts a new tenant. Returns an error if the ID already exists.
// The bot token is encrypted via the configured [vault.TokenEncryptor] before storage.
func (s *PostgresAdminStore) CreateTenant(ctx context.Context, t Tenant) error {
	encToken, err := s.encryptor.Encrypt(ctx, t.BotToken)
	if err != nil {
		return fmt.Errorf("gateway: encrypt bot token for tenant %q: %w", t.ID, err)
	}
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO tenants (id, license_tier, bot_token, guild_ids, monthly_session_hours, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, t.ID, t.LicenseTier.String(), encToken, t.GuildIDs, t.MonthlySessionHours, t.CreatedAt, t.UpdatedAt)
	if err != nil {
		return fmt.Errorf("gateway: create tenant %q: %w", t.ID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("gateway: tenant %q already exists", t.ID)
	}
	return nil
}

// GetTenant returns a tenant by ID. Returns an error if not found.
// The bot token is decrypted via the configured [vault.TokenEncryptor].
func (s *PostgresAdminStore) GetTenant(ctx context.Context, id string) (Tenant, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, license_tier, bot_token, guild_ids, monthly_session_hours, created_at, updated_at
		FROM tenants
		WHERE id = $1
	`, id)

	t, err := scanTenant(row)
	if err != nil {
		return Tenant{}, err
	}
	t.BotToken, err = s.encryptor.Decrypt(ctx, t.BotToken)
	if err != nil {
		return Tenant{}, fmt.Errorf("gateway: decrypt bot token for tenant %q: %w", id, err)
	}
	return t, nil
}

// UpdateTenant updates an existing tenant record. Returns an error if not found.
// The bot token is encrypted via the configured [vault.TokenEncryptor] before storage.
func (s *PostgresAdminStore) UpdateTenant(ctx context.Context, t Tenant) error {
	encToken, err := s.encryptor.Encrypt(ctx, t.BotToken)
	if err != nil {
		return fmt.Errorf("gateway: encrypt bot token for tenant %q: %w", t.ID, err)
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE tenants
		SET license_tier = $2, bot_token = $3, guild_ids = $4,
		    monthly_session_hours = $5, updated_at = now()
		WHERE id = $1
	`, t.ID, t.LicenseTier.String(), encToken, t.GuildIDs, t.MonthlySessionHours)
	if err != nil {
		return fmt.Errorf("gateway: update tenant %q: %w", t.ID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("gateway: tenant %q not found", t.ID)
	}
	return nil
}

// DeleteTenant removes a tenant by ID. Returns an error if not found.
func (s *PostgresAdminStore) DeleteTenant(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM tenants WHERE id = $1
	`, id)
	if err != nil {
		return fmt.Errorf("gateway: delete tenant %q: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("gateway: tenant %q not found", id)
	}
	return nil
}

// ListTenants returns all tenants ordered by ID.
// Bot tokens are decrypted via the configured [vault.TokenEncryptor].
func (s *PostgresAdminStore) ListTenants(ctx context.Context) ([]Tenant, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, license_tier, bot_token, guild_ids, monthly_session_hours, created_at, updated_at
		FROM tenants
		ORDER BY id
	`)
	if err != nil {
		return nil, fmt.Errorf("gateway: list tenants: %w", err)
	}
	defer rows.Close()

	var tenants []Tenant
	for rows.Next() {
		t, err := scanTenant(rows)
		if err != nil {
			return nil, err
		}
		t.BotToken, err = s.encryptor.Decrypt(ctx, t.BotToken)
		if err != nil {
			return nil, fmt.Errorf("gateway: decrypt bot token for tenant %q: %w", t.ID, err)
		}
		tenants = append(tenants, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("gateway: list tenants: %w", err)
	}
	return tenants, nil
}

// scanTenant reads a single tenant row into a Tenant struct.
func scanTenant(row pgx.Row) (Tenant, error) {
	var t Tenant
	var tierStr string

	err := row.Scan(&t.ID, &tierStr, &t.BotToken, &t.GuildIDs, &t.MonthlySessionHours, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return Tenant{}, fmt.Errorf("gateway: tenant not found")
		}
		return Tenant{}, fmt.Errorf("gateway: scan tenant: %w", err)
	}

	tier, err := config.ParseLicenseTier(tierStr)
	if err != nil {
		return Tenant{}, fmt.Errorf("gateway: parse license tier %q: %w", tierStr, err)
	}
	t.LicenseTier = tier

	return t, nil
}
