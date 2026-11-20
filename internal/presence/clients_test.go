package presence

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/discord"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/storage/crypto"
	"github.com/MrWong99/Glyphoxa/internal/wirenpc"
)

// regCall records one commandRegistrar invocation.
type regCall struct {
	guild   string
	defsLen int
	cleared bool // nil defs = clear-Guild
}

// fakeTenantStore is the per-Tenant deployment-config read surface.
type fakeTenantStore struct {
	mu   sync.Mutex
	deps map[uuid.UUID]storage.DeploymentConfig
}

func newFakeTenantStore() *fakeTenantStore {
	return &fakeTenantStore{deps: map[uuid.UUID]storage.DeploymentConfig{}}
}

func (f *fakeTenantStore) set(id uuid.UUID, d storage.DeploymentConfig) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d.TenantID = id
	f.deps[id] = d
}

func (f *fakeTenantStore) GetDeploymentConfig(_ context.Context, id uuid.UUID) (storage.DeploymentConfig, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.deps[id]
	if !ok {
		return storage.DeploymentConfig{}, storage.ErrNotFound
	}
	return d, nil
}

func (f *fakeTenantStore) ListDeploymentConfigs(context.Context) ([]storage.DeploymentConfig, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]storage.DeploymentConfig, 0, len(f.deps))
	for _, d := range f.deps {
		out = append(out, d)
	}
	return out, nil
}

// GetTenantIDByGuildID resolves a Guild to its newest-updated owning Tenant, the
// storage newest-wins semantics (#490) modelled over the in-memory map.
func (f *fakeTenantStore) GetCampaign(context.Context, uuid.UUID) (storage.Campaign, error) {
	return storage.Campaign{}, storage.ErrNotFound
}

func (f *fakeTenantStore) GetTenantIDByGuildID(_ context.Context, guildID string) (uuid.UUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var owner uuid.UUID
	var newest time.Time
	found := false
	for id, d := range f.deps {
		if d.GuildID != guildID {
			continue
		}
		if !found || d.UpdatedAt.After(newest) {
			owner, newest, found = id, d.UpdatedAt, true
		}
	}
	if !found {
		return uuid.Nil, storage.ErrNotFound
	}
	return owner, nil
}

// clientsRig wires a *Clients with recording seams.
type clientsRig struct {
	c      *Clients
	cipher *crypto.Cipher
	store  *fakeTenantStore

	mu          sync.Mutex
	builds      []*bot.Client
	buildTokens []string
	closed      []*bot.Client
	regs        []regCall
	openErr     error
	registerErr error
	clock       time.Time // the registry's clock; move it with advance
}

// advance moves the registry's clock forward (rebuild pacing reads it).
func (r *clientsRig) advance(d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clock = r.clock.Add(d)
}

func newClientsRig(t *testing.T, envToken string) *clientsRig {
	t.Helper()
	cipher, err := crypto.New(make([]byte, 32))
	if err != nil {
		t.Fatalf("crypto.New: %v", err)
	}
	store := newFakeTenantStore()
	reg := NewRegistry(NewGate(gmInTenants{}, fakeTenants{}), nil)
	reg.Register(Command{Path: "roll", Description: "Roll dice"})

	rig := &clientsRig{cipher: cipher, store: store, clock: time.Unix(1_800_000_000, 0)}
	c := NewClients(store, cipher, reg, envToken, nil)
	c.now = func() time.Time {
		rig.mu.Lock()
		defer rig.mu.Unlock()
		return rig.clock
	}
	c.build = func(token string) (*bot.Client, error) {
		rig.mu.Lock()
		defer rig.mu.Unlock()
		cl := &bot.Client{}
		rig.builds = append(rig.builds, cl)
		rig.buildTokens = append(rig.buildTokens, token)
		return cl, nil
	}
	c.open = func(context.Context, *bot.Client) error {
		rig.mu.Lock()
		defer rig.mu.Unlock()
		return rig.openErr
	}
	c.closeClient = func(cl *bot.Client) {
		rig.mu.Lock()
		defer rig.mu.Unlock()
		rig.closed = append(rig.closed, cl)
	}
	c.register = func(_ context.Context, _ *bot.Client, guild string, defs []discord.ApplicationCommandCreate) error {
		rig.mu.Lock()
		defer rig.mu.Unlock()
		rig.regs = append(rig.regs, regCall{guild: guild, defsLen: len(defs), cleared: defs == nil})
		return rig.registerErr
	}
	rig.c = c
	return rig
}

func (r *clientsRig) numBuilds() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.builds)
}

func (r *clientsRig) numClosed() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.closed)
}

func (r *clientsRig) regsForGuild(guild string, cleared bool) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, rc := range r.regs {
		if rc.guild == guild && rc.cleared == cleared {
			n++
		}
	}
	return n
}

// savedDep builds a deployment config with a sealed Bot token.
func (r *clientsRig) savedDep(t *testing.T, guild, token string) storage.DeploymentConfig {
	t.Helper()
	ct, err := r.cipher.Seal([]byte(token))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	return storage.DeploymentConfig{GuildID: guild, DiscordBotTokenCiphertext: ct, DiscordBotTokenLast4: crypto.Last4(token)}
}

func mustEnsure(t *testing.T, c *Clients, id uuid.UUID) {
	t.Helper()
	if err := c.EnsureTenant(context.Background(), id); err != nil {
		t.Fatalf("EnsureTenant(%s): %v", id, err)
	}
}

// (1) EnsureTenant reads the tenant-scoped config; a config with no resolvable
// token is a wait-state (no build), and a saved token brings the client up.
func TestEnsureTenantScopedReadAndWaitState(t *testing.T) {
	rig := newClientsRig(t, "") // no env fallback
	ctx := context.Background()
	a := uuid.New()

	// No config at all → wait-state.
	mustEnsure(t, rig.c, a)
	if rig.numBuilds() != 0 {
		t.Fatalf("no-config builds=%d, want 0", rig.numBuilds())
	}
	if st := rig.c.IntegrationStatus(a); st.State != IntegrationWaiting {
		t.Fatalf("status = %q, want waiting", st.State)
	}
	if _, err := rig.c.ClientForTenant(ctx, a); !errors.Is(err, ErrNoClient) {
		t.Fatalf("ClientForTenant wait-state = %v, want ErrNoClient", err)
	}

	// Config present but no token (nothing saved, no env) → still wait-state.
	rig.store.set(a, storage.DeploymentConfig{GuildID: "GA"})
	mustEnsure(t, rig.c, a)
	if rig.numBuilds() != 0 {
		t.Fatalf("no-token builds=%d, want 0", rig.numBuilds())
	}

	// A saved token appears → one build + one full register of GA.
	rig.store.set(a, rig.savedDep(t, "GA", "tok-A"))
	mustEnsure(t, rig.c, a)
	if rig.numBuilds() != 1 {
		t.Fatalf("token builds=%d, want 1", rig.numBuilds())
	}
	if rig.regsForGuild("GA", false) != 1 {
		t.Fatalf("register GA = %d, want 1", rig.regsForGuild("GA", false))
	}
	if st := rig.c.IntegrationStatus(a); st.State != IntegrationOK {
		t.Fatalf("status after token = %q, want ok", st.State)
	}
	if rig.c.GuildForTenant(a) != "GA" {
		t.Fatalf("GuildForTenant = %q, want GA", rig.c.GuildForTenant(a))
	}
	if _, err := rig.c.ClientForTenant(ctx, a); err != nil {
		t.Fatalf("ClientForTenant after up: %v", err)
	}
}

// (2) HIJACK REGRESSION: Tenant B saving/ensuring rebuilds only B; A's client
// pointer and command registrations are untouched.
func TestEnsureTenantB_DoesNotDisconnectTenantA(t *testing.T) {
	rig := newClientsRig(t, "")
	ctx := context.Background()
	a, b := uuid.New(), uuid.New()

	rig.store.set(a, rig.savedDep(t, "GA", "tok-A"))
	rig.store.set(b, rig.savedDep(t, "GB", "tok-B"))
	mustEnsure(t, rig.c, a)
	mustEnsure(t, rig.c, b)

	aClient, err := rig.c.ClientForTenant(ctx, a)
	if err != nil {
		t.Fatalf("ClientForTenant(A): %v", err)
	}
	buildsBefore := rig.numBuilds()
	clearsBefore := rig.regsForGuild("GA", true)
	fullRegsBefore := rig.regsForGuild("GA", false)

	// Tenant B changes its token AND guild, then re-ensures.
	rig.store.set(b, rig.savedDep(t, "GB2", "tok-B2"))
	mustEnsure(t, rig.c, b)

	// A is completely untouched: same client pointer, no A rebuild, no GA clear,
	// and no re-PUT churn against GA.
	aAfter, err := rig.c.ClientForTenant(ctx, a)
	if err != nil {
		t.Fatalf("ClientForTenant(A) after B save: %v", err)
	}
	if aAfter != aClient {
		t.Errorf("Tenant A's client changed after Tenant B save (HIJACK)")
	}
	if rig.regsForGuild("GA", true) != clearsBefore {
		t.Errorf("Tenant A's GA commands were cleared by a Tenant B save (HIJACK)")
	}
	if rig.regsForGuild("GA", false) != fullRegsBefore {
		t.Errorf("Tenant A's GA commands were re-PUT by a Tenant B save (churn): got %d, want %d",
			rig.regsForGuild("GA", false), fullRegsBefore)
	}
	// B rebuilt (new token → new client).
	if rig.numBuilds() != buildsBefore+1 {
		t.Errorf("builds after B token change = %d, want %d (only B rebuilt)", rig.numBuilds(), buildsBefore+1)
	}
	if st := rig.c.IntegrationStatus(a); st.State != IntegrationOK {
		t.Errorf("A status after B save = %q, want ok", st.State)
	}
}

// (3) Central token: two Tenants resolve to ONE shared client serving both
// Guilds; a repeat ensure re-PUTs nothing.
func TestCentralTokenSharesOneClient(t *testing.T) {
	rig := newClientsRig(t, "central-token")
	a, b := uuid.New(), uuid.New()

	// Both Tenants carry no saved token → both resolve the env central token.
	rig.store.set(a, storage.DeploymentConfig{GuildID: "GA"})
	rig.store.set(b, storage.DeploymentConfig{GuildID: "GB"})
	mustEnsure(t, rig.c, a)
	mustEnsure(t, rig.c, b)

	if rig.numBuilds() != 1 {
		t.Fatalf("central builds=%d, want 1 (one shared client)", rig.numBuilds())
	}
	if rig.regsForGuild("GA", false) != 1 || rig.regsForGuild("GB", false) != 1 {
		t.Fatalf("register GA=%d GB=%d, want 1/1", rig.regsForGuild("GA", false), rig.regsForGuild("GB", false))
	}

	ca, _ := rig.c.ClientForTenant(context.Background(), a)
	cb, _ := rig.c.ClientForTenant(context.Background(), b)
	if ca != cb {
		t.Errorf("central Tenants got different clients")
	}

	// Repeat ensure of both → no new build, no new register.
	regsBefore := len(rig.regs)
	mustEnsure(t, rig.c, a)
	mustEnsure(t, rig.c, b)
	if rig.numBuilds() != 1 {
		t.Errorf("repeat ensure rebuilt: builds=%d, want 1", rig.numBuilds())
	}
	if len(rig.regs) != regsBefore {
		t.Errorf("repeat ensure re-PUT commands: regs=%d, want %d", len(rig.regs), regsBefore)
	}
}

// (4) BYOK two tokens → two clients; changing B's token closes only B's OLD
// client, refcounted (A untouched).
func TestBYOKTokenChangeClosesOnlyOldClient(t *testing.T) {
	rig := newClientsRig(t, "")
	a, b := uuid.New(), uuid.New()

	rig.store.set(a, rig.savedDep(t, "GA", "tok-A"))
	rig.store.set(b, rig.savedDep(t, "GB", "tok-B"))
	mustEnsure(t, rig.c, a)
	mustEnsure(t, rig.c, b)
	if rig.numBuilds() != 2 {
		t.Fatalf("BYOK builds=%d, want 2", rig.numBuilds())
	}
	bOld, _ := rig.c.ClientForTenant(context.Background(), b)

	// B changes token → B's old client closed, A's client never closed.
	rig.store.set(b, rig.savedDep(t, "GB", "tok-B2"))
	mustEnsure(t, rig.c, b)
	if rig.numClosed() != 1 {
		t.Fatalf("closed=%d, want 1 (only B's old client)", rig.numClosed())
	}
	rig.mu.Lock()
	closedOne := rig.closed[0]
	rig.mu.Unlock()
	if closedOne != bOld {
		t.Errorf("closed the wrong client: got %p, want B's old %p", closedOne, bOld)
	}
}

// (4b) Refcount: a shared central-token client is NOT closed when one of two
// Tenants swaps to its own BYOK token.
func TestCentralClientSurvivesOneTenantTokenSwap(t *testing.T) {
	rig := newClientsRig(t, "central-token")
	a, b := uuid.New(), uuid.New()

	rig.store.set(a, storage.DeploymentConfig{GuildID: "GA"})
	rig.store.set(b, storage.DeploymentConfig{GuildID: "GB"})
	mustEnsure(t, rig.c, a)
	mustEnsure(t, rig.c, b)
	central, _ := rig.c.ClientForTenant(context.Background(), a)

	// B moves to its own BYOK token → B detaches from the shared central client,
	// which A still refs, so it must NOT be closed.
	rig.store.set(b, rig.savedDep(t, "GB", "tok-B"))
	mustEnsure(t, rig.c, b)
	if rig.numClosed() != 0 {
		t.Fatalf("closed=%d, want 0 (central client still ref'd by A)", rig.numClosed())
	}
	if aStill, _ := rig.c.ClientForTenant(context.Background(), a); aStill != central {
		t.Errorf("A's central client changed on B's token swap")
	}
}

// (5) invalidate B on a terminal token death → B failed with detail, A ok; B is
// re-ensured only once the pacing for a terminal failure has run out.
func TestInvalidateMarksTenantFailedAndReEnsures(t *testing.T) {
	rig := newClientsRig(t, "")
	ctx := context.Background()
	a, b := uuid.New(), uuid.New()

	rig.store.set(a, rig.savedDep(t, "GA", "tok-A"))
	rig.store.set(b, rig.savedDep(t, "GB", "tok-B"))
	mustEnsure(t, rig.c, a)
	mustEnsure(t, rig.c, b)
	bClient, _ := rig.c.ClientForTenant(ctx, b)

	// B's gateway dies on a revoked-token close 4004.
	rig.c.invalidate(bClient, &websocket.CloseError{Code: 4004, Text: "Authentication failed"})

	if st := rig.c.IntegrationStatus(b); st.State != IntegrationFailed || st.Detail == "" {
		t.Fatalf("B status = %+v, want failed with detail", st)
	}
	if st := rig.c.IntegrationStatus(a); st.State != IntegrationOK {
		t.Fatalf("A status = %+v, want ok (unaffected)", st)
	}

	// A refused token is not retried on the next borrow: every retry is an
	// IDENTIFY that cannot succeed.
	buildsBefore := rig.numBuilds()
	if _, err := rig.c.ClientForTenant(ctx, b); !errors.Is(err, ErrRebuildBackoff) {
		t.Fatalf("ClientForTenant(B) right after a terminal death = %v, want ErrRebuildBackoff", err)
	}
	if rig.numBuilds() != buildsBefore {
		t.Fatalf("a terminal death was rebuilt immediately: builds=%d, want %d", rig.numBuilds(), buildsBefore)
	}
	if _, err := rig.c.ClientForTenant(ctx, a); err != nil {
		t.Fatalf("ClientForTenant(A) while B backs off: %v (pacing is per token)", err)
	}

	// Once the wait is over the borrow re-ensures B (rebuild), so a fix made on
	// Discord's side takes effect without a restart.
	rig.advance(rebuildMaxDelay)
	if _, err := rig.c.ClientForTenant(ctx, b); err != nil {
		t.Fatalf("ClientForTenant(B) after the backoff: %v", err)
	}
	if rig.numBuilds() != buildsBefore+1 {
		t.Errorf("B did not re-ensure: builds=%d, want %d", rig.numBuilds(), buildsBefore+1)
	}
	if st := rig.c.IntegrationStatus(b); st.State != IntegrationOK {
		t.Errorf("B status after re-ensure = %q, want ok", st.State)
	}
}

// (6) Self-heal: a boot ensure that fails on a Discord blip leaves the wait-state,
// and the next ClientForTenant re-runs the ensure to success.
func TestClientForTenantSelfHealsAfterFailure(t *testing.T) {
	rig := newClientsRig(t, "")
	ctx := context.Background()
	a := uuid.New()
	rig.store.set(a, rig.savedDep(t, "GA", "tok-A"))

	rig.openErr = errors.New("discord blip at boot")
	if err := rig.c.EnsureTenant(ctx, a); err == nil {
		t.Fatal("first EnsureTenant = nil, want the open error")
	}
	// An immediate retry is paced, not sent: it surfaces an error rather than a
	// stale client, without another open.
	if _, err := rig.c.ClientForTenant(ctx, a); !errors.Is(err, ErrRebuildBackoff) {
		t.Fatalf("ClientForTenant during the blip = %v, want ErrRebuildBackoff", err)
	}

	rig.openErr = nil
	rig.advance(rebuildBaseDelay)
	c, err := rig.c.ClientForTenant(ctx, a)
	if err != nil {
		t.Fatalf("ClientForTenant after blip clears: %v", err)
	}
	if c == nil {
		t.Fatal("self-heal returned a nil client")
	}
}

// (7) Guild change: the old Guild is cleared, the new one registered, and no
// other Tenant's Guild is touched.
func TestGuildChangeClearsOldRegistersNew(t *testing.T) {
	rig := newClientsRig(t, "")
	a := uuid.New()
	rig.store.set(a, rig.savedDep(t, "GA", "tok-A"))
	mustEnsure(t, rig.c, a)

	rig.store.set(a, rig.savedDep(t, "GA2", "tok-A"))
	mustEnsure(t, rig.c, a)

	if rig.regsForGuild("GA", true) != 1 {
		t.Errorf("old guild GA not cleared: clears=%d, want 1", rig.regsForGuild("GA", true))
	}
	if rig.regsForGuild("GA2", false) != 1 {
		t.Errorf("new guild GA2 not registered: regs=%d, want 1", rig.regsForGuild("GA2", false))
	}
	if rig.numBuilds() != 1 {
		t.Errorf("guild change rebuilt the client: builds=%d, want 1", rig.numBuilds())
	}
	if rig.c.GuildForTenant(a) != "GA2" {
		t.Errorf("GuildForTenant = %q, want GA2", rig.c.GuildForTenant(a))
	}
}

// (10) EnsureAll seeds every saved row; one broken token is logged non-fatal and
// the others still stand up.
func TestEnsureAllSeedsAllRowsOneBrokenNonFatal(t *testing.T) {
	rig := newClientsRig(t, "")
	a, b := uuid.New(), uuid.New()
	rig.store.set(a, rig.savedDep(t, "GA", "tok-A"))
	// B has a real saved token but its build/open will fail — simulate by a
	// per-token open error.
	rig.store.set(b, rig.savedDep(t, "GB", "tok-B"))

	failToken := "tok-B"
	rig.c.open = func(_ context.Context, _ *bot.Client) error {
		// The last built token is B's on B's ensure; fail exactly it.
		rig.mu.Lock()
		defer rig.mu.Unlock()
		if len(rig.buildTokens) > 0 && rig.buildTokens[len(rig.buildTokens)-1] == failToken {
			return errors.New("boom")
		}
		return nil
	}

	if err := rig.c.EnsureAll(context.Background()); err != nil {
		t.Fatalf("EnsureAll returned fatal error: %v", err)
	}
	// A is live regardless of B's failure.
	if st := rig.c.IntegrationStatus(a); st.State != IntegrationOK {
		t.Errorf("A status = %q, want ok despite B failing", st.State)
	}
}

// invalidate on a NON-fatal (transient) death drops the client without marking
// the Tenant failed — the next borrow self-heals.
func TestInvalidateTransientDoesNotFail(t *testing.T) {
	rig := newClientsRig(t, "")
	a := uuid.New()
	rig.store.set(a, rig.savedDep(t, "GA", "tok-A"))
	mustEnsure(t, rig.c, a)
	c, _ := rig.c.ClientForTenant(context.Background(), a)

	rig.c.invalidate(c, fmt.Errorf("transient network drop"))
	if st := rig.c.IntegrationStatus(a); st.State == IntegrationFailed {
		t.Errorf("transient death marked Tenant failed: %+v", st)
	}
}

// (1, register-failure) A register failure after a successful buildOpen must not
// leak a phantom ref / zombie entry: the tenant state + ref are committed BEFORE
// the fallible register, so a later wait-state still detaches and closes the
// client. A missing tenants entry would strand the gateway forever (IDENTIFY burn
// + conn leak).
func TestRegisterFailureDoesNotLeakEntry(t *testing.T) {
	rig := newClientsRig(t, "")
	a := uuid.New()
	rig.store.set(a, rig.savedDep(t, "GA", "tok-A"))

	rig.mu.Lock()
	rig.registerErr = errors.New("discord 500 on SetGuildCommands")
	rig.mu.Unlock()
	if err := rig.c.EnsureTenant(context.Background(), a); err == nil {
		t.Fatal("EnsureTenant with a failing register = nil, want the register error")
	}
	if rig.numBuilds() != 1 {
		t.Fatalf("builds = %d, want 1 (client built, registration failed)", rig.numBuilds())
	}

	// The Tenant now removes its token → wait-state. A phantom ref (no committed
	// tenants entry) would leave setWaiting unable to detach; with state committed
	// the orphaned entry closes.
	rig.store.set(a, storage.DeploymentConfig{GuildID: "GA"}) // no token
	mustEnsure(t, rig.c, a)
	if rig.numClosed() != 1 {
		t.Errorf("closed = %d, want 1 (entry not leaked after a register failure)", rig.numClosed())
	}
}

// (9, concurrency) invalidate (disgo gateway goroutine) racing EnsureTenant and
// ClientForTenant must not deadlock or data-race — run under -race. The gateway
// I/O runs off the state mutex (atomic client pointers), so none of the three
// blocks the others.
func TestConcurrentEnsureInvalidateBorrow(t *testing.T) {
	rig := newClientsRig(t, "")
	a := uuid.New()
	rig.store.set(a, rig.savedDep(t, "GA", "tok-A"))
	mustEnsure(t, rig.c, a)

	var wg sync.WaitGroup
	stop := make(chan struct{})
	spin := func(f func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					f()
				}
			}
		}()
	}

	spin(func() { _, _ = rig.c.ClientForTenant(context.Background(), a) })
	spin(func() { _ = rig.c.EnsureTenant(context.Background(), a) })
	spin(func() {
		if cl := rig.c.clientFor(a); cl != nil {
			rig.c.invalidate(cl, &websocket.CloseError{Code: 4004, Text: "Authentication failed"})
		}
	})
	spin(func() { rig.c.IntegrationStatus(a); rig.c.GuildForTenant(a) })

	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// TestCloseBlocksLateEnsure covers #483 L7: an EnsureTenant landing AFTER Close
// must not re-open a gateway into the torn-down registry — Close swept the maps,
// so a late build would be a leaked standing client nothing ever closes.
func TestCloseBlocksLateEnsure(t *testing.T) {
	rig := newClientsRig(t, "")
	ctx := context.Background()
	tenant := uuid.New()
	rig.store.set(tenant, rig.savedDep(t, "g-close", "token-close"))

	mustEnsure(t, rig.c, tenant)
	if rig.numBuilds() != 1 {
		t.Fatalf("builds = %d, want 1", rig.numBuilds())
	}
	rig.c.Close()
	if rig.numClosed() != 1 {
		t.Fatalf("closed = %d after Close, want 1", rig.numClosed())
	}

	// A racing reconcile/refresher after Close: no new build, no error.
	if err := rig.c.EnsureTenant(ctx, tenant); err != nil {
		t.Fatalf("late EnsureTenant: %v", err)
	}
	if rig.numBuilds() != 1 {
		t.Fatalf("builds after post-Close ensure = %d, want still 1 (no leaked gateway)", rig.numBuilds())
	}
}

// TestRebuildPacingBoundsIdentifiesPerDay is the regression test for the
// production token reset of 2026-11-20: a standing gateway that dies again
// right after every open used to be rebuilt on every reconcile tick, about
// 2,880 IDENTIFYs a day against Discord's limit of 1,000. Paced, a full day of
// that failure must stay far inside the budget.
func TestRebuildPacingBoundsIdentifiesPerDay(t *testing.T) {
	rig := newClientsRig(t, "")
	ctx := context.Background()
	a := uuid.New()
	rig.store.set(a, rig.savedDep(t, "GA", "tok-A"))

	const tick = 30 * time.Second
	for elapsed := time.Duration(0); elapsed < 24*time.Hour; elapsed += tick {
		// One reconcile tick; whenever it yields a client, that gateway dies.
		if client, err := rig.c.ClientForTenant(ctx, a); err == nil {
			rig.c.invalidate(client, errors.New("gateway closed"))
		}
		rig.advance(tick)
	}

	if got := rig.numBuilds(); got > 100 {
		t.Fatalf("a day of a flapping gateway opened it %d times; Discord resets the token at 1000", got)
	}
	if got := rig.numBuilds(); got < 40 {
		t.Fatalf("a day of a flapping gateway was only retried %d times; the pacing must keep trying", got)
	}
}

// A failed open is paced exactly like a death: the same loop, entered before
// the client ever came up.
func TestRebuildPacingBoundsFailedOpens(t *testing.T) {
	rig := newClientsRig(t, "")
	ctx := context.Background()
	a := uuid.New()
	rig.store.set(a, rig.savedDep(t, "GA", "tok-A"))
	rig.openErr = errors.New("gateway unreachable")

	const tick = 30 * time.Second
	for elapsed := time.Duration(0); elapsed < 24*time.Hour; elapsed += tick {
		_ = rig.c.EnsureTenant(ctx, a)
		rig.advance(tick)
	}
	if got := rig.numBuilds(); got > 100 {
		t.Fatalf("a day of failing opens attempted %d; Discord resets the token at 1000", got)
	}
}

func TestRebuildPacingDoublesAndForgivesAHealthyClient(t *testing.T) {
	rig := newClientsRig(t, "")
	ctx := context.Background()
	a := uuid.New()
	rig.store.set(a, rig.savedDep(t, "GA", "tok-A"))
	blip := errors.New("gateway closed")

	die := func() {
		t.Helper()
		client, err := rig.c.ClientForTenant(ctx, a)
		if err != nil {
			t.Fatalf("ClientForTenant: %v", err)
		}
		rig.c.invalidate(client, blip)
	}
	waitIs := func(want time.Duration) {
		t.Helper()
		if got := rig.c.rebuildWait("tok-A"); got != want {
			t.Fatalf("rebuild wait = %s, want %s", got, want)
		}
	}

	die()
	waitIs(30 * time.Second)
	rig.advance(30 * time.Second)
	die()
	waitIs(time.Minute)
	rig.advance(time.Minute)
	die()
	waitIs(2 * time.Minute)

	// A client that then serves for a while has earned a fresh count: its next
	// death is a first failure again.
	rig.advance(2 * time.Minute)
	if _, err := rig.c.ClientForTenant(ctx, a); err != nil {
		t.Fatalf("ClientForTenant after the backoff: %v", err)
	}
	rig.advance(rebuildHealthyAfter)
	die()
	waitIs(30 * time.Second)
}

func TestRebuildDelayCaps(t *testing.T) {
	if got := rebuildDelay(1); got != rebuildBaseDelay {
		t.Fatalf("rebuildDelay(1) = %s, want %s", got, rebuildBaseDelay)
	}
	if got := rebuildDelay(1000); got != rebuildMaxDelay {
		t.Fatalf("rebuildDelay(1000) = %s, want the %s cap", got, rebuildMaxDelay)
	}
}

// hangOpen makes the rig's gateway open block until its ctx ends — an
// unreachable Discord, where disgo's Open retries for as long as the ctx lives.
// opened is signalled once per open that started.
func (r *clientsRig) hangOpen(opened chan<- struct{}) {
	r.c.open = func(ctx context.Context, _ *bot.Client) error {
		if opened != nil {
			opened <- struct{}{}
		}
		<-ctx.Done()
		return ctx.Err()
	}
}

// TestClientForTenantCancelsWhileEnsureBlocked (#642): an ensure stuck on one
// Tenant's open must not trap another caller on the gate past its own ctx.
func TestClientForTenantCancelsWhileEnsureBlocked(t *testing.T) {
	rig := newClientsRig(t, "")
	a, b := uuid.New(), uuid.New()
	rig.store.set(a, rig.savedDep(t, "GA", "tok-A"))
	rig.store.set(b, rig.savedDep(t, "GB", "tok-B"))
	opened := make(chan struct{}, 1)
	rig.hangOpen(opened)
	t.Cleanup(rig.c.Close)

	go func() { _ = rig.c.EnsureTenant(context.Background(), a) }()
	<-opened // tenant A now holds the gate inside its open

	ctxB, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := rig.c.ClientForTenant(ctxB, b)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ClientForTenant(B) = %v, want its own ctx deadline", err)
	}
	if waited := time.Since(start); waited > time.Second {
		t.Fatalf("ClientForTenant(B) waited %s behind tenant A's open, want it to give up on its own deadline", waited)
	}
}

// TestEnsureAllBoundsEachTenant (#642): a Tenant whose open never returns is cut
// off at the open bound, so the reconcile still reaches the next Tenant — and
// the timeout counts as a failure, so the next attempt is paced rather than
// re-queued in the IDENTIFY pacer on every pass (see TestTimedOutOpensConverge).
func TestEnsureAllBoundsEachTenant(t *testing.T) {
	rig := newClientsRig(t, "")
	rig.c.openTimeout = 20 * time.Millisecond
	a, b := uuid.New(), uuid.New()
	rig.store.set(a, rig.savedDep(t, "GA", "tok-A"))
	rig.store.set(b, rig.savedDep(t, "GB", "tok-B"))
	rig.c.open = func(ctx context.Context, _ *bot.Client) error {
		rig.mu.Lock()
		hang := rig.buildTokens[len(rig.buildTokens)-1] == "tok-A"
		rig.mu.Unlock()
		if hang {
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}

	done := make(chan struct{})
	go func() { defer close(done); _ = rig.c.EnsureAll(context.Background()) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("EnsureAll did not return: a hung open pinned the reconcile")
	}
	if rig.c.clientFor(b) == nil {
		t.Error("tenant B has no client: the reconcile never reached it")
	}
	if wait := rig.c.rebuildWait("tok-A"); wait != rebuildBaseDelay {
		t.Errorf("rebuild wait after a timed-out open = %s, want %s: a timed-out open is paced like any failure", wait, rebuildBaseDelay)
	}
	if st := rig.c.IntegrationStatus(a); st.State != IntegrationReconnecting {
		t.Errorf("tenant A status = %+v, want reconnecting while its client is down", st)
	}
}

// TestRunSeedsWithoutWaitingForTheTicker (#642): the boot seed now happens on
// Run's own goroutine, so Run must ensure every Tenant at once rather than
// after the first reconcile interval.
func TestRunSeedsWithoutWaitingForTheTicker(t *testing.T) {
	rig := newClientsRig(t, "")
	rig.c.reconcile = time.Hour
	a := uuid.New()
	rig.store.set(a, rig.savedDep(t, "GA", "tok-A"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go rig.c.Run(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for rig.c.clientFor(a) == nil {
		if time.Now().After(deadline) {
			t.Fatal("Run did not seed the registry before its first tick")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestCloseUnwindsAHungOpen (#642): Close cancels the registry's lifetime, so
// an ensure stuck in an open on an unreachable Discord unwinds instead of
// holding shutdown up for the whole outage.
func TestCloseUnwindsAHungOpen(t *testing.T) {
	rig := newClientsRig(t, "")
	a := uuid.New()
	rig.store.set(a, rig.savedDep(t, "GA", "tok-A"))
	opened := make(chan struct{}, 1)
	rig.hangOpen(opened)

	go func() { _ = rig.c.EnsureTenant(context.Background(), a) }()
	<-opened

	closed := make(chan struct{})
	go func() { defer close(closed); rig.c.Close() }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close blocked behind an ensure stuck in a gateway open")
	}
}

// TestCloseDoesNotWaitOutAStuckClientClose (S9): disgo's gateway Close blocks
// while an open waits on the IDENTIFY pacer (minutes); shutdown gives each
// close a bound and moves on.
func TestCloseDoesNotWaitOutAStuckClientClose(t *testing.T) {
	rig := newClientsRig(t, "")
	rig.c.closeTimeout = 20 * time.Millisecond
	a := uuid.New()
	rig.store.set(a, rig.savedDep(t, "GA", "tok-A"))
	mustEnsure(t, rig.c, a)

	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	rig.c.closeClient = func(*bot.Client) { <-release }

	closed := make(chan struct{})
	go func() { defer close(closed); rig.c.Close() }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close blocked on a client whose own Close never returned")
	}
}

// TestIntegrationStatusReportsReconnecting (S11): a Tenant whose standing
// gateway died (not terminally) is not "ok" while the rebuild is pending.
func TestIntegrationStatusReportsReconnecting(t *testing.T) {
	rig := newClientsRig(t, "")
	a := uuid.New()
	rig.store.set(a, rig.savedDep(t, "GA", "tok-A"))
	mustEnsure(t, rig.c, a)
	if st := rig.c.IntegrationStatus(a); st.State != IntegrationOK {
		t.Fatalf("status with a live client = %+v, want ok", st)
	}

	rig.c.invalidate(rig.c.clientFor(a), errors.New("gateway closed"))
	st := rig.c.IntegrationStatus(a)
	if st.State != IntegrationReconnecting {
		t.Fatalf("status after the gateway died = %+v, want reconnecting", st)
	}
	if !strings.Contains(st.Detail, "30s") {
		t.Errorf("detail %q should say when the next attempt is due", st.Detail)
	}
}

// pacedGateway models disgo's Open against an outage with the REAL IDENTIFY
// pacer on a virtual clock: Open retries (1s, 2s, 4s, ... up to 60s apart),
// every try first takes a pacer reservation (spent even when the dial is then
// refused, as in disgo), a try that would wait past the open's bound gives up
// and is refunded (wirenpc.IdentifyPacer.Wait), and the first try after the
// outage IDENTIFYs and succeeds.
type pacedGateway struct {
	clock      time.Time
	pacer      *wirenpc.IdentifyPacer
	upAt       time.Time
	identifies int
	tries      int // pacer reservations kept (dials attempted)
}

func (g *pacedGateway) open(bound time.Duration) error {
	start := g.clock
	delay := time.Second
	for {
		g.clock = g.clock.Add(delay)
		if g.clock.Sub(start) >= bound {
			g.clock = start.Add(bound)
			return context.DeadlineExceeded
		}
		delay = min(2*delay, 60*time.Second)
		if d := g.pacer.Reserve(); d > 0 {
			if g.clock.Add(d).Sub(start) >= bound {
				g.pacer.Refund()
				g.clock = start.Add(bound)
				return context.DeadlineExceeded
			}
			g.clock = g.clock.Add(d)
		}
		g.tries++
		if g.clock.Before(g.upAt) {
			continue // refused dial
		}
		g.identifies++
		return nil
	}
}

// runPaced drives Run's reconcile (every 30s, virtual) against g until a client
// is up or until has elapsed; it returns when the client came up (0 = never).
func runPaced(t *testing.T, g *pacedGateway, until time.Duration) time.Duration {
	t.Helper()
	rig := newClientsRig(t, "")
	rig.c.now = func() time.Time { return g.clock }
	rig.c.open = func(context.Context, *bot.Client) error { return g.open(rig.c.openTimeout) }
	a := uuid.New()
	rig.store.set(a, rig.savedDep(t, "GA", "tok-A"))

	begin := g.clock
	tick := g.clock
	for g.clock.Sub(begin) < until {
		_ = rig.c.EnsureAll(context.Background())
		if rig.c.clientFor(a) != nil {
			return g.clock.Sub(begin)
		}
		for !tick.After(g.clock) {
			tick = tick.Add(30 * time.Second)
		}
		g.clock = tick
	}
	return 0
}

// TestTimedOutOpensConverge is the regression test for the #642 review's
// lockout: bounded opens that each re-queued in the IDENTIFY pacer (and kept
// their reservation when they gave up) grew the pacer's hold by ~3 minutes per
// ~1 minute attempt, so after a 35s outage no open ever reached Discord again.
// With timed-out opens paced and abandoned reservations refunded, the registry
// must be connected within minutes of Discord coming back, with one IDENTIFY.
func TestTimedOutOpensConverge(t *testing.T) {
	start := time.Unix(1_800_000_000, 0)
	g := &pacedGateway{clock: start, pacer: wirenpc.NewIdentifyPacer(func() time.Time { return start }), upAt: start.Add(35 * time.Second)}
	g.pacer = wirenpc.NewIdentifyPacer(func() time.Time { return g.clock })

	at := runPaced(t, g, time.Hour)
	if at == 0 {
		t.Fatalf("no client after an hour; a 35s outage locked the token out (%d tries, %d IDENTIFYs)", g.tries, g.identifies)
	}
	if at > 10*time.Minute {
		t.Errorf("connected %s after boot, want within 10 minutes of a 35s outage", at)
	}
	if g.identifies != 1 {
		t.Errorf("IDENTIFYs = %d, want exactly 1", g.identifies)
	}
}

// TestTimedOutOpensStayInsideTheIdentifyCeiling: a day of Discord being
// unreachable must not draw more IDENTIFY reservations than the pacer's own
// ceiling (burst + 24h/refill), the most any earlier build could spend.
func TestTimedOutOpensStayInsideTheIdentifyCeiling(t *testing.T) {
	start := time.Unix(1_800_000_000, 0)
	g := &pacedGateway{clock: start, upAt: start.Add(1000 * time.Hour)}
	g.pacer = wirenpc.NewIdentifyPacer(func() time.Time { return g.clock })

	if at := runPaced(t, g, 24*time.Hour); at != 0 {
		t.Fatal("connected during a permanent outage")
	}
	ceiling := 4 + int(24*time.Hour/wirenpc.IdentifyRefill)
	if g.tries > ceiling {
		t.Errorf("a day of outage spent %d IDENTIFY reservations, want at most the pacer ceiling %d", g.tries, ceiling)
	}
}

// TestFirstOpenRejectedTokenShowsFailed (#642 review): a token Discord refuses
// at the very first open (close 4004) is failed, not "reconnecting".
func TestFirstOpenRejectedTokenShowsFailed(t *testing.T) {
	rig := newClientsRig(t, "")
	a := uuid.New()
	rig.store.set(a, rig.savedDep(t, "GA", "tok-A"))
	rig.openErr = fmt.Errorf("failed to open gateway connection: %w", &websocket.CloseError{Code: 4004, Text: "Authentication failed"})
	if err := rig.c.EnsureTenant(context.Background(), a); err == nil {
		t.Fatal("EnsureTenant with a refused token = nil")
	}
	st := rig.c.IntegrationStatus(a)
	if st.State != IntegrationFailed || !strings.Contains(st.Detail, "invalid_bot_token") {
		t.Errorf("status = %+v, want failed with the classified reason", st)
	}
}

// TestUnopenedTenantBorrowsNothing (#642 review): a Tenant whose own open
// failed must not end up borrowing a client another Tenant opened on the same
// token without holding a ref — the client would close under its session when
// that other Tenant left.
func TestUnopenedTenantBorrowsNothing(t *testing.T) {
	rig := newClientsRig(t, "")
	a, b := uuid.New(), uuid.New()
	rig.store.set(a, rig.savedDep(t, "GA", "tok-S"))
	rig.store.set(b, rig.savedDep(t, "GB", "tok-S"))
	rig.openErr = errors.New("dial refused")
	_ = rig.c.EnsureTenant(context.Background(), a)
	rig.openErr = nil
	rig.advance(rebuildMaxDelay)
	mustEnsure(t, rig.c, b)

	if rig.c.clientFor(a) != nil {
		rig.c.mu.Lock()
		_, ref := rig.c.entries["tok-S"].refs[a]
		rig.c.mu.Unlock()
		if !ref {
			t.Fatal("tenant A borrows the shared client without holding a ref")
		}
	}
	// A's own next ensure takes its ref properly.
	mustEnsure(t, rig.c, a)
	rig.c.mu.Lock()
	_, ref := rig.c.entries["tok-S"].refs[a]
	rig.c.mu.Unlock()
	if !ref || rig.c.IntegrationStatus(a).State != IntegrationOK {
		t.Fatalf("after its own ensure tenant A ref=%v status=%+v, want a ref and ok", ref, rig.c.IntegrationStatus(a))
	}
}
