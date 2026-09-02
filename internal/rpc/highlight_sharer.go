package rpc

import (
	"context"
	"errors"
	"log/slog"
	"slices"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/discordshare"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/storage/crypto"
	"github.com/MrWong99/Glyphoxa/internal/wirenpc"
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
	// envToken is the deployment's central Bot token (env DISCORD_BOT_TOKEN),
	// the fallback rung when the Tenant has no saved BYOK token (ADR-0057
	// central Bot-token mode); wired via SetEnvBotToken. Empty = no fallback.
	envToken string

	// listFn / listVoiceFn / postFn are seams so tests point the Discord calls at
	// a fake server; they default to the live discordshare functions.
	listFn      func(ctx context.Context, token, guildID string, log *slog.Logger) ([]discordshare.Channel, error)
	listVoiceFn func(ctx context.Context, token, guildID string, log *slog.Logger) ([]discordshare.Channel, error)
	postFn      func(ctx context.Context, token, channelID, caption, filename, contentType string, data []byte, log *slog.Logger) error
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
		deps:        deps,
		cipher:      cipher,
		log:         log,
		listFn:      discordshare.ListTextChannels,
		listVoiceFn: discordshare.ListVoiceChannels,
		postFn:      discordshare.PostFile,
	}
}

// SetEnvBotToken hands the sharer the deployment's central Bot token (env
// DISCORD_BOT_TOKEN) as the fallback rung of its token ladder, mirroring
// ProviderServer.SetEnvBotToken. Without it a central-token Tenant (ADR-0057:
// guild linked, no saved per-tenant token) could START sessions — Manager.Start
// resolves the same ladder — but could not LIST channels, so the Session
// screen's picker and the Highlight share dialog silently failed with "save a
// Discord Bot token first" on exactly the deployments where saving one is not
// required.
func (d *DeploymentSharer) SetEnvBotToken(token string) {
	d.envToken = token
}

// resolve resolves the request Tenant's Bot token under the SAME hybrid policy
// a session start uses (#87, ADR-0057: the Tenant's resolved Bot) — the saved
// BYOK token when one exists, else the deployment's central env token — and
// reads the linked guild id. Neither token resolving is ErrNoDiscordToken
// ("save a Discord Bot token first", now accurate: there is no env token
// either). A REAL saved token that cannot be opened (no cipher, undecryptable)
// stays a hard error, never a silent env fall-through (the wirenpc AC3
// posture). A missing Tenant in ctx is ErrNoDiscordToken too (the path is
// behind the auth stack, so this only guards a mis-wired test).
func (d *DeploymentSharer) resolve(ctx context.Context) (token, guildID string, err error) {
	tenantID, ok := auth.TenantID(ctx)
	if !ok {
		return "", "", ErrNoDiscordToken
	}
	dep, derr := d.deps.GetDeploymentConfig(ctx, tenantID)
	if derr != nil && !errors.Is(derr, storage.ErrNotFound) {
		return "", "", derr
	}
	// ErrNotFound leaves dep zero: an empty last4 resolves straight to the env
	// token, exactly as a session start would for that Tenant.
	tok, rerr := wirenpc.ResolveDiscordToken(d.cipher, dep.DiscordBotTokenLast4, dep.DiscordBotTokenCiphertext, d.envToken)
	if rerr != nil {
		return "", "", rerr
	}
	if tok == "" {
		return "", "", ErrNoDiscordToken
	}
	return tok, dep.GuildID, nil
}

// ListTextChannels implements [HighlightSharer]. An unlinked guild is
// [ErrNoGuildLinked] — with the env-token rung a token can resolve for a
// Tenant that never linked a server, and "save a Discord Bot token first"
// would be the wrong advice for that state.
func (d *DeploymentSharer) ListTextChannels(ctx context.Context) ([]discordshare.Channel, error) {
	token, guildID, err := d.resolve(ctx)
	if err != nil {
		return nil, err
	}
	if guildID == "" {
		return nil, ErrNoGuildLinked
	}
	return d.listFn(ctx, token, guildID, d.log)
}

// ListVoiceChannels implements [VoiceChannelLister]: the linked guild's voice
// channels for the Session screen's channel picker, resolved with the same
// tenant-scoped token + guild read as the text-channel list.
func (d *DeploymentSharer) ListVoiceChannels(ctx context.Context) ([]discordshare.Channel, error) {
	token, guildID, err := d.resolve(ctx)
	if err != nil {
		return nil, err
	}
	if guildID == "" {
		return nil, ErrNoGuildLinked
	}
	return d.listVoiceFn(ctx, token, guildID, d.log)
}

// PostClip implements [HighlightSharer]. The destination is re-validated against
// the linked guild on every share — the channel picker is the SPA's convenience,
// not the tenancy boundary: with a shared central Bot token (ADR-0057) the token
// alone can reach any Tenant's guild, so a channel id that is not one of THIS
// Tenant's text channels is [ErrChannelNotLinked], and a Tenant that never linked
// a server is [ErrNoGuildLinked].
func (d *DeploymentSharer) PostClip(ctx context.Context, channelID, caption, filename, contentType string, data []byte) error {
	token, guildID, err := d.resolve(ctx)
	if err != nil {
		return err
	}
	if guildID == "" {
		return ErrNoGuildLinked
	}
	channels, err := d.listFn(ctx, token, guildID, d.log)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(channels, func(c discordshare.Channel) bool { return c.ID == channelID }) {
		return ErrChannelNotLinked
	}
	return d.postFn(ctx, token, channelID, caption, filename, contentType, data, d.log)
}
