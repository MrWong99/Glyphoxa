// Package presence owns the Bot's standing Discord gateway (ADR-0010 amendment,
// #489): a REGISTRY of standing disgo clients, keyed by resolved Bot token, that
// both registers the v1.0 Slash Command surface against each Tenant's configured
// Guild and — shared with the voice Manager — backs live Voice Sessions. It
// replaces the singleton [Presence] built from the globally-newest deployment
// config (the presence-hijack blocker, ADR-0055): a per-Tenant ensure/rebuild
// now touches only that Tenant's client, so Tenant B saving Discord settings can
// never tear down Tenant A's presence. Central-token deployments resolve every
// Tenant to one shared client serving many Guilds; a BYOK Tenant's own token
// gets its own client whose terminal token-death surfaces per Tenant (#489).
package presence

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/disgoorg/disgo"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/storage/crypto"
	"github.com/MrWong99/Glyphoxa/internal/wirenpc"
	gxvoice "github.com/MrWong99/Glyphoxa/pkg/voice"
)

// ErrNoClient is returned by ClientForTenant while a Tenant's client is in its
// wait-state (no Bot token yet), so a Voice Session cycle that borrows the shared
// client backs off and retries instead of dialing its own.
var ErrNoClient = errors.New("presence: no standing Discord client yet (waiting for a Bot token)")

// Integration state codes surfaced by IntegrationStatus. They are the observable
// per-Tenant health of the Discord integration the Configuration screen renders.
const (
	// IntegrationOK: the Tenant's standing client is up (a Bot token is resolved
	// and its gateway opened).
	IntegrationOK = "ok"
	// IntegrationWaiting: no Bot token yet (nothing saved, no env fallback) — the
	// wait-state, not a failure.
	IntegrationWaiting = "waiting"
	// IntegrationFailed: the Tenant's Bot token was rejected terminally (a revoked
	// token's gateway close 4004 / REST 401, ADR-0043). Visible only to this
	// Tenant; other Tenants are untouched.
	IntegrationFailed = "failed"
)

// IntegrationStatus is one Tenant's Discord integration health: a state code plus
// a human detail (the classified failure prose when State is "failed").
type IntegrationStatus struct {
	State  string
	Detail string
}

// TenantStore is the deployment-config read surface the registry needs:
// per-Tenant for a scoped ensure, and a full list for the boot seed.
// *storage.Store satisfies it.
type TenantStore interface {
	GetDeploymentConfig(ctx context.Context, tenantID uuid.UUID) (storage.DeploymentConfig, error)
	ListDeploymentConfigs(ctx context.Context) ([]storage.DeploymentConfig, error)
}

// ClientBuilder constructs a standing disgo client for a Bot token. The prod
// default wires the SAME gateway options the voice loop used (Guilds +
// GuildVoiceStates intents, DAVE, ADR-0006) PLUS the interaction listeners and
// async event delivery; a test injects a fake that returns a sentinel without
// dialing Discord.
type ClientBuilder func(token string) (*bot.Client, error)

// commandRegistrar performs the per-Guild command registration (SetGuildCommands
// PUT). Injected so ensure is unit-tested without a live REST client; nil defs
// clears a Guild.
type commandRegistrar func(ctx context.Context, client *bot.Client, guildID string, defs []discord.ApplicationCommandCreate) error

// clientEntry is one standing client, shared by every Tenant whose resolved Bot
// token equals its key. refs is the reference set (Tenant ids sharing this
// client): the client is closed only when the last ref drops, so a token swap of
// one Tenant never kills a central-token client another Tenant still holds.
// registeredGuilds is the set of Guilds whose command surface is already PUT, so
// a repeat ensure re-PUTs nothing.
type clientEntry struct {
	token            string
	client           atomic.Pointer[bot.Client] // nil = dead/rebuilding
	refs             map[uuid.UUID]struct{}
	registeredGuilds map[string]bool
}

// tenantState is one Tenant's resolved binding: the token its client is keyed by,
// its configured Guild, and a terminal failure detail (empty = healthy).
type tenantState struct {
	token   string
	guild   string
	failure string // "" = ok; else the classified terminal-failure detail
}

// Clients is the boot-owned registry of standing gateways + command surfaces. It
// is created once at web-tier boot and lives for the process; EnsureAll seeds it
// at boot, EnsureTenant reconciles one Tenant after a Discord settings save, and
// Run polls a periodic reconcile.
type Clients struct {
	store    TenantStore
	cipher   *crypto.Cipher
	reg      *Registry
	envToken string
	log      *slog.Logger

	// Injected seams (prod defaults set in NewClients; tests override).
	build       ClientBuilder
	open        func(ctx context.Context, client *bot.Client) error
	register    commandRegistrar
	closeClient func(client *bot.Client)
	// fetchMember resolves one guild member via a REST client the caller has
	// already borrowed; injected so MemberDisplayName + VoiceChannelMembers are
	// unit-tested without a live gateway.
	fetchMember func(ctx context.Context, r rest.Rest, guildID, userID snowflake.ID) (*discord.Member, error)

	// reconcile is the Run poll interval (GLYPHOXA_PRESENCE_RECONCILE_INTERVAL,
	// default 30s).
	reconcile time.Duration

	// budget observes EVERY standing client's gateway session establishments for
	// the IDENTIFY-budget metrics (#486). Set once at boot via SetGatewayBudget
	// before the first EnsureAll/EnsureTenant; read lazily by the default client
	// builder (each build, incl. rebuilds), so a nil budget attaches no
	// instrumentation and a mid-life set lands on the next rebuild.
	//
	// This write is intentionally NOT mu-guarded: SetGatewayBudget must be called
	// on the boot goroutine BEFORE the first Ensure — which is what spawns the
	// gateway read goroutines that (indirectly) read it — so boot ordering, not a
	// mutex, establishes the happens-before edge. Calling it after a gateway is
	// open would be a data race.
	budget wirenpc.GatewayBudgetRecorder

	// ensureMu serializes EnsureTenant/EnsureAll so builds and registrations never
	// overlap; it is held ACROSS gateway/REST I/O. mu guards the entries/tenants
	// maps and their non-atomic fields and is NEVER held across I/O — the read-hot
	// client pointers are atomics, so invalidate (on disgo's gateway goroutine)
	// takes only mu briefly and never blocks on an in-flight ensure's network I/O
	// (mirrors the old singleton's CAS design).
	ensureMu sync.Mutex
	mu       sync.Mutex
	entries  map[string]*clientEntry
	tenants  map[uuid.UUID]*tenantState
}

// NewClients builds a registry. envToken is the DISCORD_BOT_TOKEN fallback (the
// deployment-shared central Bot); reg is the command surface registered per
// Guild on the first ensure that resolves a token.
func NewClients(store TenantStore, cipher *crypto.Cipher, reg *Registry, envToken string, log *slog.Logger) *Clients {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	c := &Clients{
		store:     store,
		cipher:    cipher,
		reg:       reg,
		envToken:  envToken,
		log:       log,
		reconcile: reconcileInterval(),
		entries:   map[string]*clientEntry{},
		tenants:   map[uuid.UUID]*tenantState{},
	}
	// The budget opts are read at build time (each Ensure that builds/rebuilds an
	// entry's client), so a SetGatewayBudget between NewClients and the first
	// Ensure still lands on every standing client.
	c.build = defaultClientBuilder(reg, log, c.invalidate, func(token string) []bot.ConfigOpt {
		return wirenpc.GatewayBudgetClientOpts(token, c.budget)
	})
	c.open = func(ctx context.Context, client *bot.Client) error { return client.OpenGateway(ctx) }
	c.register = restRegister
	c.closeClient = func(client *bot.Client) { client.Close(context.Background()) }
	c.fetchMember = c.restGetMember
	return c
}

// SetGatewayBudget installs the IDENTIFY-budget observer (#486) the default
// client builder attaches to EVERY standing client the registry builds. Call it
// once at boot before the first Ensure; a later Ensure that builds/rebuilds a
// client (a new token, or a rebuild after a gateway death) re-reads it, so a
// mid-life set also takes effect on the next build.
func (c *Clients) SetGatewayBudget(b wirenpc.GatewayBudgetRecorder) {
	c.budget = b
}

// reconcileInterval reads GLYPHOXA_PRESENCE_RECONCILE_INTERVAL (a Go duration),
// defaulting to 30s for a missing/blank/invalid value.
func reconcileInterval() time.Duration {
	const def = 30 * time.Second
	v := os.Getenv("GLYPHOXA_PRESENCE_RECONCILE_INTERVAL")
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return def
	}
	return d
}

// EnsureTenant reconciles ONE Tenant's standing client and command registration
// against its tenant-scoped deployment config. It is lazy and idempotent, and
// touches only this Tenant's client (and the shared entry it refs): a wait-state
// (no token) detaches the Tenant; a resolved token builds or reuses the entry for
// that token, adds this Tenant's ref, and PUTs the command surface for this
// Tenant's Guild. A repeat with the same token and Guild does nothing.
func (c *Clients) EnsureTenant(ctx context.Context, tenantID uuid.UUID) error {
	c.ensureMu.Lock()
	defer c.ensureMu.Unlock()

	dep, err := c.store.GetDeploymentConfig(ctx, tenantID)
	if errors.Is(err, storage.ErrNotFound) {
		c.setWaiting(tenantID)
		return nil
	}
	if err != nil {
		return fmt.Errorf("presence: load deployment config for tenant %s: %w", tenantID, err)
	}

	token, err := wirenpc.ResolveDiscordToken(c.cipher, dep.DiscordBotTokenLast4, dep.DiscordBotTokenCiphertext, c.envToken)
	if err != nil {
		return fmt.Errorf("presence: resolve Discord bot token: %w", err)
	}
	if token == "" {
		c.setWaiting(tenantID)
		return nil
	}
	return c.reconcileTenant(ctx, tenantID, token, dep.GuildID)
}

// reconcileTenant does the token/guild reconciliation for one Tenant under
// ensureMu. Gateway/REST I/O (buildOpen, register) runs without mu held.
func (c *Clients) reconcileTenant(ctx context.Context, tenantID uuid.UUID, token, guild string) error {
	c.mu.Lock()
	var prevToken, prevGuild string
	if prev := c.tenants[tenantID]; prev != nil {
		prevToken, prevGuild = prev.token, prev.guild
	}
	entry := c.entries[token]
	c.mu.Unlock()

	// Ensure the entry has a live client, building/rebuilding off mu. client is
	// carried out of the build (NOT re-Loaded): a concurrent invalidate could
	// CAS the freshly-Stored pointer back to nil between the Store and a re-Load,
	// and a re-Loaded nil handed to register() would nil-deref on the gateway
	// goroutine (finding 3).
	rebuilt := false
	var client *bot.Client
	switch {
	case entry == nil:
		cl, err := c.buildOpen(ctx, token)
		if err != nil {
			return err
		}
		entry = &clientEntry{token: token, refs: map[uuid.UUID]struct{}{}, registeredGuilds: map[string]bool{}}
		entry.client.Store(cl)
		client = cl
		rebuilt = true
		c.mu.Lock()
		c.entries[token] = entry
		c.mu.Unlock()
	case entry.client.Load() == nil:
		cl, err := c.buildOpen(ctx, token)
		if err != nil {
			return err
		}
		c.mu.Lock()
		entry.registeredGuilds = map[string]bool{}
		c.mu.Unlock()
		entry.client.Store(cl)
		client = cl
		rebuilt = true
	default:
		client = entry.client.Load()
	}
	if client == nil {
		// The entry's client died between the snapshot and here (a concurrent
		// invalidate). Transient: the next ClientForTenant/refresher re-ensures and
		// rebuilds — no commands are PUT against a nil client.
		return fmt.Errorf("presence: standing client for tenant %s died during ensure", tenantID)
	}

	// Token change: release this Tenant's ref on the OLD entry (refcounted close).
	if prevToken != "" && prevToken != token {
		c.detachFromEntry(prevToken, tenantID)
	}

	// Commit the ref AND the tenant state together, BEFORE the (fallible)
	// registration: otherwise a register failure returns with the ref added but
	// c.tenants[tenantID] never stored, so a later setWaiting/token-change (which
	// resolves the old entry via c.tenants) can't detach the phantom ref and the
	// entry leaks a live gateway forever (finding 1). Registration is idempotent
	// and self-heals on the next ensure (registeredGuilds tracks what actually
	// landed), so committing state first only means a transient "client up,
	// commands pending" window, never a leak.
	c.mu.Lock()
	entry.refs[tenantID] = struct{}{}
	c.tenants[tenantID] = &tenantState{token: token, guild: guild}
	c.mu.Unlock()

	// Clear this Tenant's stale OLD-Guild commands when its Guild changed on the
	// SAME entry (Discord-side registration survives a gateway death, so a rebuild
	// must clear it too — finding 5) and no other ref still serves that Guild.
	if prevToken == token && prevGuild != "" && prevGuild != guild && !c.guildUsedByOther(entry, tenantID, prevGuild) {
		if err := c.register(ctx, client, prevGuild, nil); err != nil {
			c.log.Warn("presence: clear old guild commands", "guild", prevGuild, "err", err)
		}
		c.mu.Lock()
		delete(entry.registeredGuilds, prevGuild)
		c.mu.Unlock()
	}

	if rebuilt {
		// A fresh client carries no registrations: re-PUT every ref-Tenant's Guild
		// (a shared central client serves many Guilds), including this Tenant's.
		for g := range c.entryGuilds(entry, tenantID, guild) {
			if err := c.register(ctx, client, g, c.reg.Definitions()); err != nil {
				return fmt.Errorf("presence: register guild commands: %w", err)
			}
			c.mu.Lock()
			entry.registeredGuilds[g] = true
			c.mu.Unlock()
		}
	} else if guild != "" {
		// Same live client: PUT this Tenant's Guild once (idempotent — skip when it
		// is already registered so a repeat ensure re-PUTs nothing).
		c.mu.Lock()
		already := entry.registeredGuilds[guild]
		c.mu.Unlock()
		if !already {
			if err := c.register(ctx, client, guild, c.reg.Definitions()); err != nil {
				return fmt.Errorf("presence: register guild commands: %w", err)
			}
			c.mu.Lock()
			entry.registeredGuilds[guild] = true
			c.mu.Unlock()
		}
	}
	return nil
}

// entryGuilds is the union of every ref-Tenant's Guild on entry plus thisGuild
// (skipping empty), for the rebuild re-registration.
func (c *Clients) entryGuilds(entry *clientEntry, thisTenant uuid.UUID, thisGuild string) map[string]bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[string]bool{}
	for id := range entry.refs {
		if id == thisTenant {
			continue
		}
		if ts := c.tenants[id]; ts != nil && ts.guild != "" {
			out[ts.guild] = true
		}
	}
	if thisGuild != "" {
		out[thisGuild] = true
	}
	return out
}

// guildUsedByOther reports whether a ref-Tenant OTHER than thisTenant serves
// guild on entry — so a Guild change does not clear commands still in use.
func (c *Clients) guildUsedByOther(entry *clientEntry, thisTenant uuid.UUID, guild string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id := range entry.refs {
		if id == thisTenant {
			continue
		}
		if ts := c.tenants[id]; ts != nil && ts.guild == guild {
			return true
		}
	}
	return false
}

// buildOpen builds and opens a client, closing it on an open failure so a failed
// ensure leaks nothing.
func (c *Clients) buildOpen(ctx context.Context, token string) (*bot.Client, error) {
	client, err := c.build(token)
	if err != nil {
		return nil, fmt.Errorf("presence: build Discord client: %w", err)
	}
	if err := c.open(ctx, client); err != nil {
		c.closeClient(client)
		return nil, fmt.Errorf("presence: open gateway: %w", err)
	}
	return client, nil
}

// setWaiting moves a Tenant to the wait-state (no token) and releases its client
// ref (refcounted close of an orphaned entry).
func (c *Clients) setWaiting(tenantID uuid.UUID) {
	c.mu.Lock()
	var prevToken string
	if prev := c.tenants[tenantID]; prev != nil {
		prevToken = prev.token
	}
	c.tenants[tenantID] = &tenantState{}
	c.mu.Unlock()
	if prevToken != "" {
		c.detachFromEntry(prevToken, tenantID)
	}
}

// detachFromEntry removes a Tenant's ref from the token's entry, closing the
// client (off mu) only when the last ref drops — the refcount that keeps a shared
// central-token client alive while another Tenant still refs it.
func (c *Clients) detachFromEntry(token string, tenantID uuid.UUID) {
	c.mu.Lock()
	entry := c.entries[token]
	if entry == nil {
		c.mu.Unlock()
		return
	}
	delete(entry.refs, tenantID)
	var toClose *bot.Client
	if len(entry.refs) == 0 {
		toClose = entry.client.Load()
		entry.client.Store(nil)
		delete(c.entries, token)
	}
	c.mu.Unlock()
	if toClose != nil {
		c.closeClient(toClose)
	}
}

// EnsureAll seeds the registry from every saved deployment config at boot
// (ADR-0039: presence-before-request) and is the periodic reconcile (Run). A
// per-Tenant ensure failure is logged non-fatal so one broken token never blocks
// the others from standing up.
//
// TODO(#489): EnsureAll only ADDS/updates Tenants present in the config table; it
// never reconciles a DELETED deployment_config row — a removed Tenant's ref +
// KnownGuild entry persist until process restart. Latent today (there is no
// deployment-config delete path), so it is left for the reconcile to grow a
// "prune tenants absent from the list" pass when a delete path lands.
func (c *Clients) EnsureAll(ctx context.Context) error {
	deps, err := c.store.ListDeploymentConfigs(ctx)
	if err != nil {
		return fmt.Errorf("presence: list deployment configs: %w", err)
	}
	for _, dep := range deps {
		if err := c.EnsureTenant(ctx, dep.TenantID); err != nil {
			c.log.Warn("presence: ensure tenant at boot failed; standing by", "tenant", dep.TenantID, "err", err)
		}
	}
	return nil
}

// Run polls a periodic full reconcile every reconcile interval until ctx is
// cancelled, so a Tenant added/changed out of band (a raw DB write, or a missed
// refresher) still converges (#489; the interval is wired by #491 later).
func (c *Clients) Run(ctx context.Context) {
	t := time.NewTicker(c.reconcile)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := c.EnsureAll(ctx); err != nil {
				c.log.Warn("presence: periodic reconcile failed", "err", err)
			}
		}
	}
}

// ClientForTenant resolves the standing client for a Tenant, self-healing: a
// wait-state (never ensured, or a prior Ensure died) re-runs EnsureTenant so the
// next Voice Session cycle rebuilds instead of borrowing a dead client. Still no
// client after the re-ensure is ErrNoClient (the reconnect loop backs off); a
// still-failing Ensure surfaces its error (a revoked token's 4004 reaches
// classifyFatal so the session ends failed, ADR-0043).
func (c *Clients) ClientForTenant(ctx context.Context, tenantID uuid.UUID) (*bot.Client, error) {
	if client := c.clientFor(tenantID); client != nil {
		return client, nil
	}
	if err := c.EnsureTenant(ctx, tenantID); err != nil {
		return nil, err
	}
	if client := c.clientFor(tenantID); client != nil {
		return client, nil
	}
	return nil, ErrNoClient
}

// clientFor reads a Tenant's live client via the entry's atomic pointer, nil in
// the wait-state or when the entry's gateway died.
func (c *Clients) clientFor(tenantID uuid.UUID) *bot.Client {
	c.mu.Lock()
	ts := c.tenants[tenantID]
	var entry *clientEntry
	if ts != nil && ts.token != "" {
		entry = c.entries[ts.token]
	}
	c.mu.Unlock()
	if entry == nil {
		return nil
	}
	return entry.client.Load()
}

// GuildForTenant is a Tenant's last-ensured configured Guild, "" in the
// wait-state.
func (c *Clients) GuildForTenant(tenantID uuid.UUID) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ts := c.tenants[tenantID]; ts != nil {
		return ts.guild
	}
	return ""
}

// KnownGuild reports whether guildID is the configured Guild of ANY resolved
// Tenant — the interim Gate check the interaction dispatch uses until #490's
// TenantResolver maps a Guild to its Tenant. A DM ("") is never known.
func (c *Clients) KnownGuild(guildID string) bool {
	if guildID == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, ts := range c.tenants {
		if ts.guild == guildID {
			return true
		}
	}
	return false
}

// IntegrationStatus is a Tenant's Discord integration health: "waiting" with no
// resolved token, "failed" (+ detail) after a terminal token death (#489), else
// "ok". A failure on one Tenant's BYOK token never changes another's.
func (c *Clients) IntegrationStatus(tenantID uuid.UUID) IntegrationStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	ts := c.tenants[tenantID]
	if ts == nil || ts.token == "" {
		return IntegrationStatus{State: IntegrationWaiting}
	}
	if ts.failure != "" {
		return IntegrationStatus{State: IntegrationFailed, Detail: ts.failure}
	}
	return IntegrationStatus{State: IntegrationOK}
}

// invalidate drops a standing client whose gateway died unexpectedly and — when
// the death is a terminal token rejection (close 4004 / REST 401, ADR-0043) —
// marks every Tenant refing that entry Discord-integration-failed with the
// classified detail. It runs on disgo's event goroutine, so it takes only mu
// (never ensureMu / the network path) and CAS-clears the entry's client only when
// dead is still the standing one — a token change may have replaced it, and a
// late death of a superseded client must not nil the fresh one. Other entries are
// untouched; the next ClientForTenant on this Tenant re-ensures.
func (c *Clients) invalidate(dead *bot.Client, cause error) {
	if dead == nil {
		return
	}
	c.mu.Lock()
	var entry *clientEntry
	for _, e := range c.entries {
		if e.client.Load() == dead {
			entry = e
			break
		}
	}
	if entry == nil || !entry.client.CompareAndSwap(dead, nil) {
		c.mu.Unlock()
		return
	}
	entry.registeredGuilds = map[string]bool{}
	if fe := wirenpc.ClassifyFatal(cause); fe != nil {
		detail := fe.Error()
		for id := range entry.refs {
			if ts := c.tenants[id]; ts != nil {
				ts.failure = detail
			}
		}
	}
	c.mu.Unlock()
	c.log.Warn("presence: standing gateway died; invalidating standing client", "err", cause)
	c.closeClient(dead)
}

// Close tears down every standing client. Called after the voice Manager's
// shutdown so a live session releases its borrowed client first.
func (c *Clients) Close() {
	c.mu.Lock()
	var toClose []*bot.Client
	for _, e := range c.entries {
		if cl := e.client.Load(); cl != nil {
			toClose = append(toClose, cl)
			e.client.Store(nil)
		}
	}
	c.entries = map[string]*clientEntry{}
	c.mu.Unlock()
	for _, cl := range toClose {
		c.closeClient(cl)
	}
}

// defaultClientBuilder is the production ClientBuilder: it constructs a standing
// disgo client with the SAME options the per-session voice wiring used (so a
// shared-client Voice Session keeps its DAVE encryption and voice-state intents,
// ADR-0006) plus the interaction listeners and async event delivery.
func defaultClientBuilder(reg *Registry, log *slog.Logger, onDead func(dead *bot.Client, cause error), budgetOpts func(token string) []bot.ConfigOpt) ClientBuilder {
	return func(token string) (*bot.Client, error) {
		// client is captured by the close handler below; disgo.New assigns it
		// before any gateway open, so it is non-nil by the time a close can fire.
		var client *bot.Client
		var err error
		opts := []bot.ConfigOpt{
			bot.WithLogger(log),
			bot.WithDefaultGateway(),
			// Guilds + GuildVoiceStates are the minimum for the voice join path
			// (see wirenpc.connectAndServe); DAVE is wired at construction. The
			// close handler drops this client from the registry when its gateway
			// dies unexpectedly (#489) — disgo calls it only on a non-reconnectable
			// close or an exhausted reconnect, not on our own Close.
			bot.WithGatewayConfigOpts(
				gateway.WithIntents(
					gateway.IntentGuilds|gateway.IntentGuildVoiceStates,
				),
				gateway.WithCloseHandler(func(_ gateway.Gateway, cerr error, _ bool) {
					onDead(client, cerr)
				}),
			),
			gxvoice.DaveOption(),
			bot.WithEventListenerFunc(reg.HandleCommand),
			bot.WithEventListenerFunc(reg.HandleAutocomplete),
			// Message-component (button) interactions: the rollover-tape consent
			// buttons (#306) and any future component surface fan out from here.
			bot.WithEventListenerFunc(reg.HandleComponent),
			// Deliver events asynchronously so an interaction handler never runs on
			// the gateway read goroutine and starves voice events (ADR-0010).
			bot.WithEventManagerConfigOpts(bot.WithAsyncEventsEnabled()),
		}
		// Gateway IDENTIFY-budget observability (#486): count this standing client's
		// IDENTIFYs at send time (identify rate-limiter wrapper) and RESUMEs on the
		// Resumed event. Empty when no budget is set. The voice-cycle clients that
		// BORROW this client (session.ClientSource) inherit this instrumentation, so
		// they are NOT re-instrumented on the borrow path — no double-counting.
		opts = append(opts, budgetOpts(token)...)
		client, err = disgo.New(token, opts...)
		return client, err
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
