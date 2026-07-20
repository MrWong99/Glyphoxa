package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Deployment-config persistence (#68, ADR-0039): the single-operator Discord
// integration the Configuration screen edits — the deployment Bot token (a
// write-only secret) plus the non-secret Guild / Voice channel IDs. Reads and
// writes live together because they are one cohesive feature; the secret token
// is sealed by the caller (the RPC layer) before it reaches here, so this layer
// only persists ciphertext + last4 and never interprets the secret.

// ErrGuildTaken is returned by SaveDiscordChannels when the guild_id is already
// bound by a DIFFERENT Tenant — the first-registrar-wins unique index (#483;
// full guild-permission proof is #504). The RPC layer maps it to
// CodeFailedPrecondition so the save is refused instead of silently rebinding
// another Tenant's guild (the old newest-wins read let the rebinder read the
// victim's voice-channel members and hijack its command routing).
var ErrGuildTaken = errors.New("storage: this guild is already bound by another tenant")

const deploymentConfigColumns = `
	tenant_id, discord_bot_token_ciphertext, discord_bot_token_last4,
	guild_id, voice_channel_id, created_at, updated_at`

func scanDeploymentConfig(row pgx.Row) (DeploymentConfig, error) {
	var d DeploymentConfig
	err := row.Scan(
		&d.TenantID, &d.DiscordBotTokenCiphertext, &d.DiscordBotTokenLast4,
		&d.GuildID, &d.VoiceChannelID, &d.CreatedAt, &d.UpdatedAt,
	)
	return d, err
}

// GetDeploymentConfig loads a Tenant's deployment config, or ErrNotFound when
// nothing has been saved yet (the Configuration screen treats that as the empty,
// key-needed state).
func (s *Store) GetDeploymentConfig(ctx context.Context, tenantID uuid.UUID) (DeploymentConfig, error) {
	row := s.db.QueryRow(ctx,
		`SELECT `+deploymentConfigColumns+` FROM deployment_config WHERE tenant_id = $1`, tenantID)
	d, err := scanDeploymentConfig(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return DeploymentConfig{}, ErrNotFound
	}
	if err != nil {
		return DeploymentConfig{}, fmt.Errorf("storage: get deployment_config for tenant %s: %w", tenantID, err)
	}
	return d, nil
}

// ListDeploymentConfigs returns every saved deployment config, one per Tenant,
// ordered oldest-updated first for a deterministic boot seed. The per-tenant
// Discord client registry (#489, ADR-0010) reads this once at boot — before any
// request, with no tenant context — to stand up one standing client per distinct
// Bot token; each request-path read still narrows to a single Tenant via
// GetDeploymentConfig. An empty table returns a nil slice, not an error.
func (s *Store) ListDeploymentConfigs(ctx context.Context) ([]DeploymentConfig, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+deploymentConfigColumns+`
		   FROM deployment_config
		  ORDER BY updated_at ASC, tenant_id ASC`)
	if err != nil {
		return nil, fmt.Errorf("storage: list deployment_config: %w", err)
	}
	defer rows.Close()

	var out []DeploymentConfig
	for rows.Next() {
		d, err := scanDeploymentConfig(rows)
		if err != nil {
			return nil, fmt.Errorf("storage: scan deployment_config: %w", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list deployment_config: %w", err)
	}
	return out, nil
}

// GetTenantIDByGuildID resolves a Discord Guild snowflake to the Tenant that
// configured it — the interaction→Tenant routing read (#490): an inbound slash
// interaction carries its Guild, and this maps it to the owning Tenant before any
// storage read touches campaigns, so a command only ever reaches its own Tenant's
// data. An unknown Guild returns ErrNotFound, which the Gate maps to a clean
// ephemeral rejection.
//
// Since #483 a guild_id is bound by at most ONE Tenant (the first-registrar-wins
// partial UNIQUE index deployment_config_guild_owner; SaveDiscordChannels rejects
// a second Tenant's bind with ErrGuildTaken), so this read is unambiguous by
// construction for every Guild→Tenant consumer — the interaction router here AND
// the member-picker path (presence.Clients.VoiceChannelMembers). The ORDER BY is
// kept as harmless belt-and-braces against a pre-index legacy duplicate.
func (s *Store) GetTenantIDByGuildID(ctx context.Context, guildID string) (uuid.UUID, error) {
	var id uuid.UUID
	err := s.db.QueryRow(ctx,
		`SELECT tenant_id FROM deployment_config
		  WHERE guild_id = $1
		  ORDER BY updated_at DESC
		  LIMIT 1`, guildID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrNotFound
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("storage: tenant for guild %q: %w", guildID, err)
	}
	return id, nil
}

// SaveDiscordBotToken upserts only the deployment Bot token columns (sealed
// ciphertext + last4), leaving the Guild / Voice channel IDs untouched. It
// returns the resulting row. The ciphertext is the caller-sealed secret.
func (s *Store) SaveDiscordBotToken(ctx context.Context, tenantID uuid.UUID, ciphertext []byte, last4 string) (DeploymentConfig, error) {
	row := s.db.QueryRow(ctx,
		`INSERT INTO deployment_config (tenant_id, discord_bot_token_ciphertext, discord_bot_token_last4)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (tenant_id) DO UPDATE
		   SET discord_bot_token_ciphertext = EXCLUDED.discord_bot_token_ciphertext,
		       discord_bot_token_last4 = EXCLUDED.discord_bot_token_last4,
		       updated_at = now()
		 RETURNING `+deploymentConfigColumns,
		tenantID, ciphertext, last4)
	d, err := scanDeploymentConfig(row)
	if err != nil {
		return DeploymentConfig{}, fmt.Errorf("storage: save discord bot token for tenant %s: %w", tenantID, err)
	}
	return d, nil
}

// SaveDiscordChannels upserts only the non-secret Guild / Voice channel IDs,
// leaving the Bot token untouched, and returns the resulting row. A guild_id
// already bound by a DIFFERENT Tenant trips the first-registrar-wins partial
// UNIQUE index (23505 on deployment_config_guild_owner) and yields ErrGuildTaken
// (#483/#504) — the same Tenant re-saving its own guild upserts its own row and
// never conflicts.
func (s *Store) SaveDiscordChannels(ctx context.Context, tenantID uuid.UUID, guildID, voiceChannelID string) (DeploymentConfig, error) {
	row := s.db.QueryRow(ctx,
		`INSERT INTO deployment_config (tenant_id, guild_id, voice_channel_id)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (tenant_id) DO UPDATE
		   SET guild_id = EXCLUDED.guild_id,
		       voice_channel_id = EXCLUDED.voice_channel_id,
		       updated_at = now()
		 RETURNING `+deploymentConfigColumns,
		tenantID, guildID, voiceChannelID)
	d, err := scanDeploymentConfig(row)
	if err != nil {
		// The tenant_id conflict is absorbed by the upsert, so the only unique
		// violation that can escape here is the guild-owner index.
		if code, ok := pgErrCode(err); ok && code == "23505" {
			return DeploymentConfig{}, ErrGuildTaken
		}
		return DeploymentConfig{}, fmt.Errorf("storage: save discord channels for tenant %s: %w", tenantID, err)
	}
	return d, nil
}
