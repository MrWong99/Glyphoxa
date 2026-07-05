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

// GetLatestDeploymentConfig returns the most-recently-updated deployment config,
// or ErrNotFound when none is saved. The standing presence (#102, ADR-0010)
// reads this at boot, before any request — so, like GetActiveCampaign, it has no
// tenant context and reads the single-operator global latest (ADR-0039). More
// than one deployment_config row only exists under the deferred multi-tenant
// tier; this returns the newest, and a later WHERE tenant_id = $1 narrows it
// then (as GetDeploymentConfig already does for the request path).
func (s *Store) GetLatestDeploymentConfig(ctx context.Context) (DeploymentConfig, error) {
	row := s.db.QueryRow(ctx,
		`SELECT `+deploymentConfigColumns+`
		   FROM deployment_config
		  ORDER BY updated_at DESC, tenant_id DESC
		  LIMIT 1`)
	d, err := scanDeploymentConfig(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return DeploymentConfig{}, ErrNotFound
	}
	if err != nil {
		return DeploymentConfig{}, fmt.Errorf("storage: get latest deployment_config: %w", err)
	}
	return d, nil
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
// leaving the Bot token untouched, and returns the resulting row.
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
		return DeploymentConfig{}, fmt.Errorf("storage: save discord channels for tenant %s: %w", tenantID, err)
	}
	return d, nil
}
