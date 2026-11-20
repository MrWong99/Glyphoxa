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
	// IntegrationReconnecting: a Bot token is resolved but its standing client is
	// not up — its gateway died, or the open failed or timed out, and the next
	// attempt is pending (paced per token, rebuild.go). Not terminal.
	IntegrationReconnecting = "reconnecting"
)

// Ensure bounds (#642).
const (
	// openTimeout bounds one standing-client gateway open. disgo's Open retries
	// a dial or handshake error with no cap of its own for as long as its ctx
	// lives, so an unreachable Discord would otherwise hold the ensure gate for
	// the whole outage. It is longer than the IDENTIFY pacer's single hold
	// (wirenpc.IdentifyRefill): an open that has to wait its turn in the pacer
	// can outwait it instead of always timing out just short of its IDENTIFY.
	// An open that does time out is a failure like any other (rebuild.go), so
	// the next attempt is paced, not immediate.
	openTimeout = wirenpc.IdentifyRefill + time.Minute
	// ensureTimeout bounds one Tenant's ensure inside EnsureAll (DB read, open,
	// command registration), so one Tenant's outage never stops the reconcile
	// from visiting the others. It leaves the open its full bound.
	ensureTimeout = openTimeout + time.Minute
	// closeTimeout bounds one standing client's Close. disgo's gateway Close
	// takes the connection lock a pending open holds while it waits for the
	// IDENTIFY pacer — which can be minutes — so a close is given this long and
	// then left to finish in the background (S9).
	closeTimeout = 5 * time.Second
)

// IntegrationStatus is one Tenant's Discord integration health: a state code plus
// a human detail (the classified failure prose when State is "failed").
type IntegrationStatus struct {
	State  string
	Detail string
}

// TenantStore is the deployment-config read surface the registry needs:
// per-Tenant for a scoped ensure, a full list for the boot seed, the
// Guild→Tenant read the member picker uses to confirm ownership (#490), and the
// Campaign→Tenant read the display-name lookup narrows with (#483).
// *storage.Store satisfies it.
type TenantStore interface {
	GetDeploymentConfig(ctx context.Context, tenantID uuid.UUID) (storage.DeploymentConfig, error)
	ListDeploymentConfigs(ctx context.Context) ([]storage.DeploymentConfig, error)
	// GetTenantIDByGuildID resolves a Guild to its OWNING Tenant (single owner
	// since #483's first-registrar-wins index) — the SAME authority the interaction
	// router uses, so the member picker and interaction routing agree (#490).
	GetTenantIDByGuildID(ctx context.Context, guildID string) (uuid.UUID, error)
	// GetCampaign resolves a Campaign (hence its owning Tenant) so
	// MemberDisplayName narrows its member fetch to that Tenant's own Guild (#483).
	GetCampaign(ctx context.Context, id uuid.UUID) (storage.Campaign, error)
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

// unopenedState is a Tenant's token whose first open has not succeeded yet.
type unopenedState struct {
	token   string
	failure string // the classified terminal failure, "" while merely retrying
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

	// voiceMetrics feeds the voice UDP transport counters (#633) on EVERY
	// standing client the registry builds — gxvoice.TransportOption reads it
	// lazily at build time, exactly like budget above (same set-at-boot,
	// not-mu-guarded contract). nil discards the counters; the keepalive
	// transport itself is attached unconditionally.
	voiceMetrics gxvoice.MetricsRecorder

	// ensureSem (capacity 1) serializes EnsureTenant/EnsureAll so builds and
	// registrations never overlap; it is held ACROSS gateway/REST I/O. It is a
	// semaphore rather than a mutex so a waiter honours its own ctx (#642): a
	// Voice Session start queued behind another Tenant's slow open gives up on
	// its own deadline instead of waiting the open out. mu guards the
	// entries/tenants maps and their non-atomic fields and is NEVER held across
	// I/O — the read-hot client pointers are atomics, so invalidate (on disgo's
	// gateway goroutine) takes only mu briefly and never blocks on an in-flight
	// ensure's network I/O (mirrors the old singleton's CAS design).
	ensureSem chan struct{}
	// life is the registry's own lifetime: every ensure's I/O runs under it as
	// well as its caller's ctx, and Close cancels it first, so an open stuck on
	// an unreachable Discord (or waiting out the IDENTIFY pacer) unwinds at
	// shutdown instead of holding Close up.
	life     context.Context
	stopLife context.CancelFunc
	// openTimeout / closeTimeout are the consts above; fields so tests shrink them.
	openTimeout  time.Duration
	closeTimeout time.Duration
	mu           sync.Mutex
	entries      map[string]*clientEntry
	tenants      map[uuid.UUID]*tenantState
	// unopened records, per Tenant, a resolved token whose client could not be
	// opened yet (and the classified terminal failure, if the open was refused
	// for good). It is read ONLY by IntegrationStatus (S11): the Tenant holds no
	// entry ref and has no binding in tenants, so nothing borrows a client or
	// clears a Guild through it. Guarded by mu; cleared on a successful ensure
	// or the wait-state.
	unopened map[uuid.UUID]unopenedState
	// rebuilds paces gateway opens per Bot token (rebuild.go): every open sends
	// an IDENTIFY, and Discord resets a token that sends 1000 in 24 hours.
	// Guarded by mu.
	rebuilds map[string]*rebuildState
	// now is the clock the rebuild pacing reads; a test seam.
	now func() time.Time
	// closed marks the registry torn down (#483 L7). Written by Close while
	// holding BOTH the ensure gate and mu; read by EnsureTenant under the gate.
	// Close cancels life and then takes the gate, so an in-flight ensure unwinds
	// (or finishes) BEFORE the teardown — an ensure that overlapped Close could
	// otherwise re-insert a fresh gateway into a registry every other path
	// believes closed, leaking it forever.
	closed bool
}

// NewClients builds a registry. envToken is the DISCORD_BOT_TOKEN fallback (the
// deployment-shared central Bot); reg is the command surface registered per
// Guild on the first ensure that resolves a token.
func NewClients(store TenantStore, cipher *crypto.Cipher, reg *Registry, envToken string, log *slog.Logger) *Clients {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	life, stopLife := context.WithCancel(context.Background())
	c := &Clients{
		store:        store,
		cipher:       cipher,
		reg:          reg,
		envToken:     envToken,
		log:          log,
		reconcile:    reconcileInterval(),
		ensureSem:    make(chan struct{}, 1),
		life:         life,
		stopLife:     stopLife,
		openTimeout:  openTimeout,
		closeTimeout: closeTimeout,
		entries:      map[string]*clientEntry{},
		tenants:      map[uuid.UUID]*tenantState{},
		rebuilds:     map[string]*rebuildState{},
		unopened:     map[uuid.UUID]unopenedState{},
		now:          time.Now,
	}
	// The budget opts are read at build time (each Ensure that builds/rebuilds an
	// entry's client), so a SetGatewayBudget between NewClients and the first
	// Ensure still lands on every standing client.
	c.build = defaultClientBuilder(reg, log, c.invalidate, func(token string) []bot.ConfigOpt {
		return wirenpc.GatewayBudgetClientOpts(token, c.budget)
	}, func() bot.ConfigOpt {
		// #633: read at build time like the budget opts, so a SetVoiceMetrics
		// between NewClients and the first Ensure still lands on every client.
		return gxvoice.TransportOption(log, c.voiceMetrics)
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

// SetVoiceMetrics installs the recorder the voice UDP transport counters (#633)
// on every standing client feed. Same contract as [Clients.SetGatewayBudget]:
// call once at boot before the first Ensure; later builds re-read it.
func (c *Clients) SetVoiceMetrics(rec gxvoice.MetricsRecorder) {
	c.voiceMetrics = rec
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
//
// Waiting for the ensure gate honours ctx (#642), and the work itself runs
// under ctx AND the registry's lifetime, so Close is never held up by it.
func (c *Clients) EnsureTenant(ctx context.Context, tenantID uuid.UUID) error {
	select {
	case c.ensureSem <- struct{}{}:
	case <-ctx.Done():
		return fmt.Errorf("presence: waiting to ensure tenant %s: %w", tenantID, ctx.Err())
	case <-c.life.Done():
		return nil // closed: see below
	}
	defer func() { <-c.ensureSem }()

	// A closed registry ensures nothing (#483 L7): a late reconcile/refresher
	// racing shutdown must not re-open a gateway Close can never reap.
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return nil
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer context.AfterFunc(c.life, cancel)()

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
// the ensure gate. Gateway/REST I/O (buildOpen, register) runs without mu held.
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
	// Every build below sends an IDENTIFY. A gateway that keeps dying, or a
	// token Discord refuses, must not be re-opened on every 30s reconcile and
	// every Voice Session start (rebuild.go).
	if entry == nil || entry.client.Load() == nil {
		if wait := c.rebuildWait(token); wait > 0 {
			return fmt.Errorf("%w: next attempt in %s", ErrRebuildBackoff, wait.Round(time.Second))
		}
	}
	switch {
	case entry == nil:
		cl, err := c.buildOpen(ctx, token)
		if err != nil {
			c.noteOpenFailed(tenantID, token, err)
			return err
		}
		c.noteOpened(token)
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
			c.noteOpenFailed(tenantID, token, err)
			return err
		}
		c.noteOpened(token)
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
	delete(c.unopened, tenantID)
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
// ensure leaks nothing. The open is bounded by openTimeout; one that ran out of
// time (or whose ctx was cancelled) fails like any other.
func (c *Clients) buildOpen(ctx context.Context, token string) (*bot.Client, error) {
	client, err := c.build(token)
	if err != nil {
		return nil, fmt.Errorf("presence: build Discord client: %w", err)
	}
	openCtx, cancel := context.WithTimeout(ctx, c.openTimeout)
	defer cancel()
	if err := c.open(openCtx, client); err != nil {
		c.closeBounded(client)
		return nil, fmt.Errorf("presence: open gateway: %w", err)
	}
	return client, nil
}

// closeBounded closes a standing client, giving up waiting after closeTimeout
// (S9): disgo's gateway Close blocks on the connection lock an open holds
// while it waits for the IDENTIFY pacer, which can be minutes. The close keeps
// running in the background and finishes once that open lets go.
func (c *Clients) closeBounded(client *bot.Client) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.closeClient(client)
	}()
	t := time.NewTimer(c.closeTimeout)
	defer t.Stop()
	select {
	case <-done:
	case <-t.C:
		c.log.Warn("presence: standing client close is still blocked; finishing it in the background",
			"app_id", client.ApplicationID, "waited", c.closeTimeout)
	}
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
	delete(c.unopened, tenantID)
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
		c.closeBounded(toClose)
	}
}

// EnsureAll seeds the registry from every saved deployment config at boot
// (ADR-0039: presence-before-request) and is the periodic reconcile (Run). A
// per-Tenant ensure failure is logged non-fatal so one broken token never blocks
// the others from standing up.
//
// TODO(#489): EnsureAll only ADDS/updates Tenants present in the config table; it
// never reconciles a DELETED deployment_config row — a removed Tenant's ref +
// Guild binding persist until process restart. Latent today (there is no
// deployment-config delete path), so it is left for the reconcile to grow a
// "prune tenants absent from the list" pass when a delete path lands.
func (c *Clients) EnsureAll(ctx context.Context) error {
	deps, err := c.store.ListDeploymentConfigs(ctx)
	if err != nil {
		return fmt.Errorf("presence: list deployment configs: %w", err)
	}
	for _, dep := range deps {
		// Bounded per Tenant (#642): one Tenant's unreachable Discord must not
		// stop the reconcile from reaching the rest.
		tctx, cancel := context.WithTimeout(ctx, ensureTimeout)
		err := c.EnsureTenant(tctx, dep.TenantID)
		cancel()
		if err != nil {
			if errors.Is(err, ErrRebuildBackoff) {
				// Expected on every reconcile tick while a token is being paced;
				// the death or the failed open that started it was already logged.
				c.log.Debug("presence: standing client rebuild is backing off", "tenant", dep.TenantID, "err", err)
				continue
			}
			c.log.Warn("presence: ensure tenant failed; standing by", "tenant", dep.TenantID, "err", err)
		}
	}
	return nil
}

// Run seeds the registry from every saved deployment config at once (the boot
// seed, ADR-0039 presence-before-request), then polls a periodic full
// reconcile every reconcile interval until ctx is cancelled, so a Tenant
// added/changed out of band (a raw DB write, or a missed refresher) still
// converges (#489).
//
// Boot runs it on its own goroutine (#642): the seed opens one gateway per Bot
// token, and an unreachable Discord must delay neither the web listener nor a
// worker's claim loop. Until a Tenant's client is up, a Voice Session start
// ensures it on demand (ClientForTenant).
func (c *Clients) Run(ctx context.Context) {
	if err := c.EnsureAll(ctx); err != nil {
		c.log.Warn("presence: initial seed failed; retrying on the reconcile interval", "err", err)
	}
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

// IntegrationStatus is a Tenant's Discord integration health: "waiting" with no
// resolved token, "failed" (+ detail) after a terminal token death (#489),
// "reconnecting" (+ when the next attempt is due) while its standing client is
// down and being re-opened (S11), else "ok". A failure on one Tenant's BYOK
// token never changes another's.
func (c *Clients) IntegrationStatus(tenantID uuid.UUID) IntegrationStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	if u, ok := c.unopened[tenantID]; ok {
		if u.failure != "" {
			return IntegrationStatus{State: IntegrationFailed, Detail: u.failure}
		}
		return IntegrationStatus{State: IntegrationReconnecting, Detail: c.reconnectDetailLocked(u.token)}
	}
	ts := c.tenants[tenantID]
	if ts == nil || ts.token == "" {
		return IntegrationStatus{State: IntegrationWaiting}
	}
	if ts.failure != "" {
		return IntegrationStatus{State: IntegrationFailed, Detail: ts.failure}
	}
	if entry := c.entries[ts.token]; entry == nil || entry.client.Load() == nil {
		return IntegrationStatus{State: IntegrationReconnecting, Detail: c.reconnectDetailLocked(ts.token)}
	}
	return IntegrationStatus{State: IntegrationOK}
}

// reconnectDetailLocked words when token's next open is due. Caller holds mu.
func (c *Clients) reconnectDetailLocked(token string) string {
	if st := c.rebuilds[token]; st != nil {
		if wait := st.notBefore.Sub(c.now()); wait > 0 {
			return fmt.Sprintf("Lost the connection to Discord; next attempt in %s.", wait.Round(time.Second))
		}
	}
	return "Reconnecting to Discord."
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
	wait := c.noteDiedLocked(entry.token, cause)
	if fe := wirenpc.ClassifyFatal(cause); fe != nil {
		detail := fe.Error()
		for id := range entry.refs {
			if ts := c.tenants[id]; ts != nil {
				ts.failure = detail
			}
		}
	}
	tenants := make([]uuid.UUID, 0, len(entry.refs))
	for id := range entry.refs {
		tenants = append(tenants, id)
	}
	c.mu.Unlock()
	c.log.Warn("presence: standing gateway died; invalidating standing client",
		"err", cause, "app_id", dead.ApplicationID, "tenants", tenants, "next_attempt_in", wait.Round(time.Second))
	c.closeBounded(dead)
}

// Close tears down every standing client. Called after the voice Manager's
// shutdown so a live session releases its borrowed client first. It cancels the
// registry's lifetime, which unwinds an in-flight ensure's open (#642), and then
// takes the ensure gate (#483 L7): an in-flight EnsureTenant could otherwise
// Store a freshly-opened gateway into an entry AFTER Close swept the maps — a
// leaked gateway no path would ever close. The closed flag then keeps any later
// ensure a no-op. Each client close is bounded by closeTimeout (S9).
func (c *Clients) Close() {
	c.stopLife()
	c.ensureSem <- struct{}{}
	defer func() { <-c.ensureSem }()
	c.mu.Lock()
	c.closed = true
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
		c.closeBounded(cl)
	}
}

// defaultClientBuilder is the production ClientBuilder: it constructs a standing
// disgo client with the SAME options the per-session voice wiring used (so a
// shared-client Voice Session keeps its DAVE encryption and voice-state intents,
// ADR-0006) plus the interaction listeners and async event delivery.
func defaultClientBuilder(reg *Registry, log *slog.Logger, onDead func(dead *bot.Client, cause error), budgetOpts func(token string) []bot.ConfigOpt, transportOpt func() bot.ConfigOpt) ClientBuilder {
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
			// Voice UDP transport hardening (#633): keepalives + the speaking-event
			// observer, mirroring wirenpc's per-cycle client. Every Voice Session
			// borrowing this standing client rides this transport; without it the
			// session goes silently deaf after ~13-15 outbound-quiet minutes.
			transportOpt(),
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
