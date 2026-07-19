package rpc

import (
	"context"
	"errors"
	"log/slog"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/discordshare"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/storage/crypto"
)

// DeploymentSharer is the production [HighlightSharer] (#310): it resolves the
// Discord Bot token + guild from the request Tenant's deployment config (#489 —
// tenant-scoped, never the global-latest read) and its cipher, then makes the
// plain net/http Discord REST calls via internal/discordshare (ADR-0047 — never
// disgo's rest client). A missing/unsaved token is [ErrNoDiscordToken], which the
// RPC renders as "save a Discord Bot token first".
type DeploymentSharer struct {
	deps   deploymentReader
	cipher *crypto.Cipher
	log    *slog.Logger

	// listFn / postFn are seams so tests point the Discord calls at a fake server;
	// they default to the live discordshare functions.
	listFn func(ctx context.Context, token, guildID string, log *slog.Logger) ([]discordshare.Channel, error)
	postFn func(ctx context.Context, token, channelID, caption, filename, contentType string, data []byte, log *slog.Logger) error
}

// deploymentReader is the narrow store surface DeploymentSharer needs; *storage.Store
// satisfies it. The read is tenant-scoped (#489), resolved from the request ctx.
type deploymentReader interface {
	GetDeploymentConfig(ctx context.Context, tenantID uuid.UUID) (storage.DeploymentConfig, error)
}

// NewDeploymentSharer builds the production sharer over deps + cipher. A nil cipher
// makes every share fail with ErrNoDiscordToken (a sealed token cannot be opened),
// matching the keyless-degradation posture. log may be nil.
func NewDeploymentSharer(deps deploymentReader, cipher *crypto.Cipher, log *slog.Logger) *DeploymentSharer {
	if log == nil {
		log = slog.Default()
	}
	return &DeploymentSharer{
		deps:   deps,
		cipher: cipher,
		log:    log,
		listFn: discordshare.ListTextChannels,
		postFn: discordshare.PostFile,
	}
}

// resolve opens the request Tenant's saved Bot token and reads the guild id. An
// unsaved token (no deployment row, empty last4, or no cipher) is
// ErrNoDiscordToken; a missing Tenant in ctx is ErrNoDiscordToken too (the share
// path is behind the auth stack, so this only guards a mis-wired test).
func (d *DeploymentSharer) resolve(ctx context.Context) (token, guildID string, err error) {
	tenantID, ok := auth.TenantID(ctx)
	if !ok {
		return "", "", ErrNoDiscordToken
	}
	dep, derr := d.deps.GetDeploymentConfig(ctx, tenantID)
	if errors.Is(derr, storage.ErrNotFound) {
		return "", "", ErrNoDiscordToken
	}
	if derr != nil {
		return "", "", derr
	}
	if !isSaved(dep.DiscordBotTokenLast4) || d.cipher == nil {
		return "", "", ErrNoDiscordToken
	}
	plain, oerr := d.cipher.Open(dep.DiscordBotTokenCiphertext)
	if oerr != nil {
		return "", "", oerr
	}
	return string(plain), dep.GuildID, nil
}

// ListTextChannels implements [HighlightSharer].
func (d *DeploymentSharer) ListTextChannels(ctx context.Context) ([]discordshare.Channel, error) {
	token, guildID, err := d.resolve(ctx)
	if err != nil {
		return nil, err
	}
	return d.listFn(ctx, token, guildID, d.log)
}

// PostClip implements [HighlightSharer].
func (d *DeploymentSharer) PostClip(ctx context.Context, channelID, caption, filename, contentType string, data []byte) error {
	token, _, err := d.resolve(ctx)
	if err != nil {
		return err
	}
	return d.postFn(ctx, token, channelID, caption, filename, contentType, data, d.log)
}
