// Package presence owns the Bot's standing Discord gateway (ADR-0010 amendment,
// #102): one shared disgo client, created lazily once a Bot token exists in the
// deployment config and rebuilt when the token changes, that both registers the
// v1.0 Slash Command surface against the configured Guild and — shared with the
// voice Manager — backs live Voice Sessions. It replaces the per-Voice-Session
// client with a single boot-owned connection so /roll answers with no Voice
// Session active and a session starting or stopping never breaks the presence.
package presence

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/disgoorg/disgo"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"

	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/storage/crypto"
	"github.com/MrWong99/Glyphoxa/internal/wirenpc"
	gxvoice "github.com/MrWong99/Glyphoxa/pkg/voice"
)

// ErrNoClient is returned by Client/ClientProvider while the presence is in its
// wait-state (no Bot token yet), so a Voice Session cycle that borrows the shared
// client backs off and retries instead of dialing its own.
var ErrNoClient = errors.New("presence: no standing Discord client yet (waiting for a Bot token)")

// Store is the deployment-config read the presence needs at boot. It reads the
// single-operator latest config, tenant-unscoped, because the standing presence
// starts before any request (no tenant context) — see
// storage.GetLatestDeploymentConfig.
type Store interface {
	GetLatestDeploymentConfig(ctx context.Context) (storage.DeploymentConfig, error)
}

// ClientBuilder constructs the standing disgo client for a Bot token. The prod
// default wires the SAME gateway options the voice loop used (Guilds +
// GuildVoiceStates intents, DAVE) PLUS the interaction listeners and async event
// delivery; a test injects a fake that returns a sentinel without dialing
// Discord.
type ClientBuilder func(token string) (*bot.Client, error)

// commandRegistrar performs the per-Guild command registration (SetGuildCommands
// PUT). Injected so Ensure is unit-tested without a live REST client; nil defs
// clears a Guild.
type commandRegistrar func(ctx context.Context, client *bot.Client, guildID string, defs []discord.ApplicationCommandCreate) error

// Presence is the boot-owned standing gateway + command surface. It is created
// once at web-tier boot and lives for the process; Ensure is called at boot and
// again whenever the deployment config changes (the RPC refresher).
type Presence struct {
	store    Store
	cipher   *crypto.Cipher
	reg      *Registry
	envToken string
	log      *slog.Logger

	// Injected seams (prod defaults set in New; tests override).
	build       ClientBuilder
	open        func(ctx context.Context, client *bot.Client) error
	register    commandRegistrar
	closeClient func(client *bot.Client)
	// fetchMember resolves one guild member via the standing client's REST; injected
	// so MemberDisplayName (#281) is unit-tested without a live gateway.
	fetchMember func(ctx context.Context, guildID, userID snowflake.ID) (*discord.Member, error)

	// mu serializes Ensure/Close so builds and registrations never overlap; token
	// is Ensure-local state guarded by it. The read-hot client and guildID are
	// ATOMICS, not mu-guarded, so the Gate's GuildID() and the voice loop's
	// Client() never block on a rebuild that holds mu across OpenGateway +
	// SetGuildCommands REST (seconds of network I/O).
	mu      sync.Mutex
	token   string                     // token the current client was built with
	client  atomic.Pointer[bot.Client] // nil = wait-state
	guildID atomic.Value               // string; last-ensured configured Guild ("" in wait-state)
}

// New builds a Presence. envToken is the DISCORD_BOT_TOKEN fallback (the
// deployment-shared Bot); reg is the command surface registered before the first
// Ensure.
func New(store Store, cipher *crypto.Cipher, reg *Registry, envToken string, log *slog.Logger) *Presence {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	p := &Presence{
		store:    store,
		cipher:   cipher,
		reg:      reg,
		envToken: envToken,
		log:      log,
	}
	p.guildID.Store("")
	p.build = defaultClientBuilder(reg, log)
	p.open = func(ctx context.Context, client *bot.Client) error { return client.OpenGateway(ctx) }
	p.register = restRegister
	p.closeClient = func(client *bot.Client) { client.Close(context.Background()) }
	p.fetchMember = p.restGetMember
	return p
}

// Ensure reconciles the standing client and command registration against the
// current deployment config. It is lazy and idempotent, serialized under p.mu:
//
//   - no config / no token: a wait-state — log and return nil (NOT an error), so
//     a bad or absent token never kills the web tier.
//   - token changed (or first token): close the old client, build + open a new
//     one.
//   - Guild changed or the client was (re)built: PUT the command definitions to
//     the configured Guild (idempotent) and best-effort clear the old Guild.
//
// A repeat Ensure with the same token and Guild does nothing.
func (p *Presence) Ensure(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	dep, err := p.store.GetLatestDeploymentConfig(ctx)
	if errors.Is(err, storage.ErrNotFound) {
		p.log.Info("presence: no deployment config yet; standing by for a Bot token")
		return nil
	}
	if err != nil {
		return fmt.Errorf("presence: load deployment config: %w", err)
	}

	token, err := wirenpc.ResolveDiscordToken(p.cipher, dep.DiscordBotTokenLast4, dep.DiscordBotTokenCiphertext, p.envToken)
	if err != nil {
		return fmt.Errorf("presence: resolve Discord bot token: %w", err)
	}
	if token == "" {
		p.log.Info("presence: no Bot token yet; standing by")
		return nil
	}

	rebuilt := false
	client := p.client.Load()
	if client == nil || token != p.token {
		if old := p.client.Load(); old != nil {
			p.closeClient(old)
			p.client.Store(nil)
		}
		c, err := p.build(token)
		if err != nil {
			return fmt.Errorf("presence: build Discord client: %w", err)
		}
		if err := p.open(ctx, c); err != nil {
			p.closeClient(c)
			return fmt.Errorf("presence: open gateway: %w", err)
		}
		client = c
		p.client.Store(c)
		p.token = token
		rebuilt = true
		p.log.Info("presence: standing Discord gateway up")
	}

	guild := dep.GuildID
	oldGuild, _ := p.guildID.Load().(string)
	switch {
	case guild == "":
		// The Guild was cleared while a token is still configured: best-effort
		// remove the commands from the old Guild so a stale /roll doesn't linger.
		if oldGuild != "" {
			if err := p.register(ctx, client, oldGuild, nil); err != nil {
				p.log.Warn("presence: clear commands from removed guild", "guild", oldGuild, "err", err)
			}
		}
	case rebuilt || guild != oldGuild:
		if oldGuild != "" && oldGuild != guild {
			if err := p.register(ctx, client, oldGuild, nil); err != nil {
				p.log.Warn("presence: clear old guild commands", "guild", oldGuild, "err", err)
			}
		}
		if err := p.register(ctx, client, guild, p.reg.Definitions()); err != nil {
			return fmt.Errorf("presence: register guild commands: %w", err)
		}
		p.log.Info("presence: registered slash commands", "guild", guild)
	}
	p.guildID.Store(guild)
	return nil
}

// GuildID is the last-ensured configured Guild, "" while in the wait-state. It
// backs the Gate's Guild check and reads an atomic, so an in-flight interaction
// never blocks on a token rebuild that holds mu across REST calls.
func (p *Presence) GuildID() string {
	v, _ := p.guildID.Load().(string)
	return v
}

// Client returns the standing shared client, or ErrNoClient in the wait-state.
// It reads an atomic, so the voice loop's acquireClient never blocks on a
// rebuild in progress.
func (p *Presence) Client() (*bot.Client, error) {
	c := p.client.Load()
	if c == nil {
		return nil, ErrNoClient
	}
	return c, nil
}

// ClientProvider adapts the presence to the wirenpc shared-client seam: each
// Voice Session cycle borrows this one client instead of dialing its own. When
// the presence is in the wait-state (never ensured, or a prior Ensure failed on
// a Discord blip), the provider re-runs Ensure so the standing client self-heals
// on the next Voice Session cycle instead of staying dead until a restart —
// runWithReconnect's backoff paces the retries. A still-failing Ensure surfaces
// its error, which the reconnect loop treats as transient.
func (p *Presence) ClientProvider() wirenpc.ClientProvider {
	return func(ctx context.Context) (*bot.Client, error) {
		if c, err := p.Client(); err == nil {
			return c, nil
		}
		if err := p.Ensure(ctx); err != nil {
			return nil, err
		}
		return p.Client()
	}
}

// Close tears down the standing client. Called after the voice Manager's
// shutdown so a live session releases the shared client first.
func (p *Presence) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if c := p.client.Load(); c != nil {
		p.closeClient(c)
		p.client.Store(nil)
	}
}

// defaultClientBuilder is the production ClientBuilder: it constructs the shared
// disgo client with the SAME options the per-session voice wiring used (so a
// shared-client Voice Session keeps its DAVE encryption and voice-state intents,
// ADR-0006) plus the interaction listeners and async event delivery.
func defaultClientBuilder(reg *Registry, log *slog.Logger) ClientBuilder {
	return func(token string) (*bot.Client, error) {
		return disgo.New(token,
			bot.WithLogger(log),
			bot.WithDefaultGateway(),
			// Guilds + GuildVoiceStates are the minimum for the voice join path
			// (see wirenpc.connectAndServe); DAVE is wired at construction.
			bot.WithGatewayConfigOpts(gateway.WithIntents(
				gateway.IntentGuilds|gateway.IntentGuildVoiceStates,
			)),
			gxvoice.DaveOption(),
			bot.WithEventListenerFunc(reg.HandleCommand),
			bot.WithEventListenerFunc(reg.HandleAutocomplete),
			// Deliver events asynchronously so an interaction handler never runs on
			// the gateway read goroutine and starves voice events (ADR-0010).
			bot.WithEventManagerConfigOpts(bot.WithAsyncEventsEnabled()),
		)
	}
}

// restRegister is the production commandRegistrar: an idempotent per-Guild PUT.
func restRegister(ctx context.Context, client *bot.Client, guildID string, defs []discord.ApplicationCommandCreate) error {
	gid, err := snowflake.Parse(guildID)
	if err != nil {
		return fmt.Errorf("presence: parse guild id %q: %w", guildID, err)
	}
	if _, err := client.Rest.SetGuildCommands(client.ApplicationID, gid, defs, rest.WithCtx(ctx)); err != nil {
		return fmt.Errorf("presence: set guild commands: %w", err)
	}
	return nil
}
