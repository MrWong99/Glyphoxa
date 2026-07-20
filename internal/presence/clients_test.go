package presence

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/discord"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/storage/crypto"
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

	rig := &clientsRig{cipher: cipher, store: store}
	c := NewClients(store, cipher, reg, envToken, nil)
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

// (5) invalidate B on a terminal token death → B failed with detail, A ok; the
// next ClientForTenant(B) re-ensures.
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

	// The next borrow re-ensures B (rebuild).
	buildsBefore := rig.numBuilds()
	if _, err := rig.c.ClientForTenant(ctx, b); err != nil {
		t.Fatalf("ClientForTenant(B) after invalidate: %v", err)
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
	// The blip persists on the immediate retry too: the self-heal re-ensure
	// surfaces the still-failing open error rather than a stale client.
	if _, err := rig.c.ClientForTenant(ctx, a); err == nil {
		t.Fatal("ClientForTenant during the blip = nil, want the surfaced open error")
	}

	rig.openErr = nil
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
	if rig.c.KnownGuild("GA") {
		t.Errorf("KnownGuild(GA) still true after guild change")
	}
	if !rig.c.KnownGuild("GA2") {
		t.Errorf("KnownGuild(GA2) false after guild change")
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

// KnownGuild backs the interim Gate check: a resolved Tenant's Guild is known, a
// DM and an unknown Guild are not.
func TestKnownGuild(t *testing.T) {
	rig := newClientsRig(t, "")
	a := uuid.New()
	rig.store.set(a, rig.savedDep(t, "GA", "tok-A"))
	mustEnsure(t, rig.c, a)

	if !rig.c.KnownGuild("GA") {
		t.Errorf("KnownGuild(GA) = false, want true")
	}
	if rig.c.KnownGuild("") {
		t.Errorf("KnownGuild(DM) = true, want false")
	}
	if rig.c.KnownGuild("nope") {
		t.Errorf("KnownGuild(unknown) = true, want false")
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
	spin(func() { rig.c.IntegrationStatus(a); rig.c.KnownGuild("GA") })

	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
}
