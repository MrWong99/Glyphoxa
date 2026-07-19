package session_test

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/highlight"
	"github.com/MrWong99/Glyphoxa/internal/session"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/storage/crypto"
	"github.com/MrWong99/Glyphoxa/internal/wirenpc"
)

// fakeStore is an in-memory session.Store: it serves a canned deployment config
// (guild/channel source, #72) and records the voice_sessions Create/End calls so
// the lifecycle can be asserted without Postgres.
type fakeStore struct {
	mu           sync.Mutex
	dep          storage.DeploymentConfig
	depErr       error
	sessions     map[uuid.UUID]storage.VoiceSession
	created      int
	ended        int
	depReads     int
	reconciles   int
	tick         int
	endErr       error           // injected EndVoiceSession failure (#143 Defect A)
	agents       []storage.Agent // the Active Campaign's roster for ListAgents (#211 mute-all)
	onListAgents func()          // mid-op hook: runs during ListAgents, no store lock held (#448)
	caps         storage.SpendCaps
	capsErr      error // injected GetTenantSpendCaps failure (#130)
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		dep: storage.DeploymentConfig{
			GuildID:        "111222333",
			VoiceChannelID: "444555666",
		},
		sessions: map[uuid.UUID]storage.VoiceSession{},
	}
}

func (f *fakeStore) now() time.Time {
	f.tick++
	return time.Date(2026, 6, 27, 12, 0, f.tick, 0, time.UTC)
}

func (f *fakeStore) GetDeploymentConfig(_ context.Context, _ uuid.UUID) (storage.DeploymentConfig, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.depReads++
	if f.depErr != nil {
		return storage.DeploymentConfig{}, f.depErr
	}
	return f.dep, nil
}

func (f *fakeStore) CreateVoiceSession(_ context.Context, campaignID uuid.UUID) (storage.VoiceSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created++
	vs := storage.VoiceSession{
		ID:         uuid.New(),
		CampaignID: campaignID,
		StartedAt:  f.now(),
		Status:     storage.VoiceSessionRunning,
	}
	f.sessions[vs.ID] = vs
	return vs, nil
}

func (f *fakeStore) CloseVoiceSession(ctx context.Context, id uuid.UUID, status storage.VoiceSessionStatus, lineCount int, endReason *string) (storage.VoiceSession, error) {
	// A real CloseVoiceSession fails on an expired context (#143 Defect B pins
	// exactly that), so the fake honours ctx like pgx would.
	if err := ctx.Err(); err != nil {
		return storage.VoiceSession{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.endErr != nil {
		return storage.VoiceSession{}, f.endErr
	}
	f.ended++
	vs, ok := f.sessions[id]
	if !ok {
		return storage.VoiceSession{}, storage.ErrNotFound
	}
	end := f.now()
	vs.EndedAt = &end
	vs.Status = status
	vs.LineCount = lineCount
	vs.EndReason = endReason
	f.sessions[id] = vs
	return vs, nil
}

// ReconcileOrphanedVoiceSessions mirrors the real store's boot reconciliation
// (#143): every 'running' row flips to 'ended' with the orphaned end reason.
func (f *fakeStore) ReconcileOrphanedVoiceSessions(ctx context.Context) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reconciles++
	var n int64
	for id, vs := range f.sessions {
		if vs.Status != storage.VoiceSessionRunning {
			continue
		}
		end := f.now()
		reason := storage.VoiceSessionReasonOrphaned
		vs.EndedAt = &end
		vs.Status = storage.VoiceSessionEnded
		vs.EndReason = &reason
		f.sessions[id] = vs
		n++
	}
	return n, nil
}

// ListAgents serves the campaign's roster for the mute-all path (#211). A test
// may set onListAgents to run mid-op — after the ops' stale-fast entry check but
// before their authoritative revalidation — to open the #448 race window (the
// session ending between the roster read and the write/publish). It runs with
// no fake-store lock held, so it may drive the Manager (e.g. Stop).
func (f *fakeStore) ListAgents(_ context.Context, _ uuid.UUID) ([]storage.Agent, error) {
	f.mu.Lock()
	hook := f.onListAgents
	roster := append([]storage.Agent(nil), f.agents...)
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	return roster, nil
}

// GetTenantSpendCaps serves the canned per-Tenant spend caps snapshot at Start
// (#130). Default: both nil (no caps → today's behavior).
func (f *fakeStore) GetTenantSpendCaps(_ context.Context, _ uuid.UUID) (storage.SpendCaps, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.capsErr != nil {
		return storage.SpendCaps{}, f.capsErr
	}
	return f.caps, nil
}

func (f *fakeStore) counts() (created, ended int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.created, f.ended
}

// session returns the recorded voice_sessions row by id (the terminal state after
// a close), for the spend-cap end-reason assertions (#130).
func (f *fakeStore) session(id uuid.UUID) storage.VoiceSession {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sessions[id]
}

// blockingRunner is a fake loop runner that records the cfg it ran with and the
// fact it was cancelled, then unblocks on ctx cancellation — a stand-in for the
// real wirenpc loop so no Discord/Postgres is touched.
type blockingRunner struct {
	mu        sync.Mutex
	started   chan struct{} // closed once the runner is executing
	gotCfg    wirenpc.Config
	cancelled bool
}

func newBlockingRunner() *blockingRunner {
	return &blockingRunner{started: make(chan struct{})}
}

func (r *blockingRunner) run(ctx context.Context, cfg wirenpc.Config) error {
	r.mu.Lock()
	r.gotCfg = cfg
	r.mu.Unlock()
	close(r.started)
	<-ctx.Done()
	r.mu.Lock()
	r.cancelled = true
	r.mu.Unlock()
	return ctx.Err()
}

func (r *blockingRunner) cfg() wirenpc.Config {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.gotCfg
}

func (r *blockingRunner) wasCancelled() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cancelled
}

func newManager(t *testing.T, store session.Store, run session.LoopRunner, enabled bool) *session.Manager {
	t.Helper()
	return newManagerDeps(t, store, run, enabled, session.Deps{})
}

// newManagerDeps is newManager with explicit construction-time deps (#448): the
// tests that used to back-wire a finalizer/pipeline via a setter now pass it
// here, the same way the composition root does.
func newManagerDeps(t *testing.T, store session.Store, run session.LoopRunner, enabled bool, deps session.Deps) *session.Manager {
	t.Helper()
	return session.NewManager(store, run, wirenpc.Config{Token: "test-token"}, nil,
		slog.New(slog.DiscardHandler), enabled, deps)
}

// fakeFactsRecaller is a no-op agent.FactsRecaller for the Deps.Facts wiring test.
type fakeFactsRecaller struct{}

func (fakeFactsRecaller) Facts(context.Context, string) []string { return nil }

// TestDepsFacts_ThreadsOntoBaseConfig mirrors the Memory/Chunker wiring (#126):
// Deps.Facts lands the recaller on the base voice config every session copies,
// so it flows through Start → RunFromDB → buildConversation into each NPC's loop.
func TestDepsFacts_ThreadsOntoBaseConfig(t *testing.T) {
	if mgr := newManager(t, newFakeStore(), newBlockingRunner().run, true); mgr.BaseFactsForTest() != nil {
		t.Fatal("base Facts should be nil with zero Deps")
	}
	rec := fakeFactsRecaller{}
	mgr := newManagerDeps(t, newFakeStore(), newBlockingRunner().run, true, session.Deps{Facts: rec})
	if mgr.BaseFactsForTest() != rec {
		t.Errorf("Deps.Facts did not thread the recaller onto the base config: got %v", mgr.BaseFactsForTest())
	}
}

// newCipher builds a Cipher on a fresh random AES-256 key for the #87 saved-token
// path — keyless and Docker-free, so the resolution logic is proven in the
// default suite.
func newCipher(t *testing.T) *crypto.Cipher {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand key: %v", err)
	}
	c, err := crypto.New(key)
	if err != nil {
		t.Fatalf("crypto.New: %v", err)
	}
	return c
}

// TestStartStopLifecycle is AC1: Start → status running + row written + the loop
// runs; Stop → ended_at + status ended, and the cancellation propagates to the
// loop (the runner observes ctx.Done).
func TestStartStopLifecycle(t *testing.T) {
	store := newFakeStore()
	runner := newBlockingRunner()
	mgr := newManager(t, store, runner.run, true)

	tenantID, campaignID := uuid.New(), uuid.New()
	vs, err := mgr.Start(context.Background(), tenantID, campaignID)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if vs.Status != storage.VoiceSessionRunning {
		t.Errorf("started status = %q, want running", vs.Status)
	}
	if created, _ := store.counts(); created != 1 {
		t.Errorf("created rows = %d, want 1", created)
	}

	// The loop is running and was handed the saved guild/channel (#72: sourced
	// from the deployment config, not CLI flags).
	select {
	case <-runner.started:
	case <-time.After(2 * time.Second):
		t.Fatal("loop runner never started")
	}
	if got := runner.cfg(); got.Guild != "111222333" || got.Channel != "444555666" {
		t.Errorf("loop cfg guild/channel = %q/%q, want saved 111222333/444555666", got.Guild, got.Channel)
	}

	// Snapshot reflects the active session.
	if snap, active := mgr.Snapshot(); !active || snap.ID != vs.ID {
		t.Errorf("Snapshot = %+v active=%v, want active %s", snap, active, vs.ID)
	}

	ended, err := mgr.Stop(context.Background())
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if ended.Status != storage.VoiceSessionEnded || ended.EndedAt == nil {
		t.Errorf("stopped session = %+v, want ended with ended_at", ended)
	}
	if _, e := store.counts(); e != 1 {
		t.Errorf("ended rows = %d, want 1", e)
	}
	if !runner.wasCancelled() {
		t.Error("cancellation did not propagate to the loop")
	}
	if _, active := mgr.Snapshot(); active {
		t.Error("Snapshot still active after Stop")
	}
}

// TestStart_PassesCampaignIDToLoop is the #323 fix: the selected campaign must
// reach the wirenpc loop so the voiced roster / TTS voices / language come from
// the bound Active Campaign, not the hardcoded seed. Start already stamps the id
// onto the voice_sessions row (CreateVoiceSession); it must also set it on the
// loop cfg (cfg.CampaignID) so RunFromDB's campaign-scoped loader reads it.
func TestStart_PassesCampaignIDToLoop(t *testing.T) {
	store := newFakeStore()
	runner := newBlockingRunner()
	mgr := newManager(t, store, runner.run, true)

	tenantID, campaignID := uuid.New(), uuid.New()
	if _, err := mgr.Start(context.Background(), tenantID, campaignID); err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case <-runner.started:
	case <-time.After(2 * time.Second):
		t.Fatal("loop runner never started")
	}
	if got := runner.cfg().CampaignID; got != campaignID {
		t.Errorf("loop cfg CampaignID = %s, want selected campaign %s", got, campaignID)
	}
}

// TestStart_OverlaysGMSpeakerForTenant pins the per-Tenant Butler gate (#490):
// Start overlays cfg.GMSpeaker with a closure that pins THIS session's Tenant, so
// the loop's GM-address verdict is scoped to the session's Tenant. A GM of Tenant A
// must not read as GM in a Tenant B session — the overlay forwards the Start's
// tenantID (not any other) to Deps.GMSpeakerForTenant.
func TestStart_OverlaysGMSpeakerForTenant(t *testing.T) {
	store := newFakeStore()
	runner := newBlockingRunner()

	type call struct {
		tenantID uuid.UUID
		user     string
	}
	var mu sync.Mutex
	var calls []call
	deps := session.Deps{GMSpeakerForTenant: func(tenantID uuid.UUID, discordUserID string) bool {
		mu.Lock()
		calls = append(calls, call{tenantID, discordUserID})
		mu.Unlock()
		return discordUserID == "gm-of-this-tenant"
	}}
	mgr := newManagerDeps(t, store, runner.run, true, deps)

	tenantID, campaignID := uuid.New(), uuid.New()
	if _, err := mgr.Start(context.Background(), tenantID, campaignID); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _, _ = mgr.Stop(context.Background()) })
	select {
	case <-runner.started:
	case <-time.After(2 * time.Second):
		t.Fatal("loop runner never started")
	}

	gate := runner.cfg().GMSpeaker
	if gate == nil {
		t.Fatal("Start did not overlay cfg.GMSpeaker from Deps.GMSpeakerForTenant")
	}
	if !gate("gm-of-this-tenant") {
		t.Error("the tenant's GM read as non-GM through the overlay")
	}
	if gate("stranger") {
		t.Error("a stranger read as GM through the overlay")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calls) == 0 {
		t.Fatal("the overlay never forwarded to GMSpeakerForTenant")
	}
	for _, c := range calls {
		if c.tenantID != tenantID {
			t.Errorf("overlay forwarded tenant %s, want the session's Tenant %s", c.tenantID, tenantID)
		}
	}
}

// TestSecondStartRejected is AC2: a second Start while one is active is rejected
// (single-active-session guard), and no second row is written.
func TestSecondStartRejected(t *testing.T) {
	store := newFakeStore()
	runner := newBlockingRunner()
	mgr := newManager(t, store, runner.run, true)

	tenantID, campaignID := uuid.New(), uuid.New()
	if _, err := mgr.Start(context.Background(), tenantID, campaignID); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	t.Cleanup(func() { _, _ = mgr.Stop(context.Background()) })

	_, err := mgr.Start(context.Background(), tenantID, campaignID)
	if !errors.Is(err, session.ErrSessionActive) {
		t.Errorf("second Start = %v, want ErrSessionActive", err)
	}
	if created, _ := store.counts(); created != 1 {
		t.Errorf("created rows = %d, want 1 (second Start must not write)", created)
	}
}

// TestStartRequiresDiscordConfig is the #72 precondition: an unset guild/channel
// fails Start with ErrDiscordNotConfigured and writes no row.
func TestStartRequiresDiscordConfig(t *testing.T) {
	store := newFakeStore()
	store.dep = storage.DeploymentConfig{} // no guild/channel
	mgr := newManager(t, store, newBlockingRunner().run, true)

	_, err := mgr.Start(context.Background(), uuid.New(), uuid.New())
	if !errors.Is(err, session.ErrDiscordNotConfigured) {
		t.Errorf("Start with no guild/channel = %v, want ErrDiscordNotConfigured", err)
	}
	if created, _ := store.counts(); created != 0 {
		t.Errorf("created rows = %d, want 0", created)
	}
}

// TestStartUsesSavedToken is issue #87 AC1: a real Bot token saved in the
// deployment config is DECRYPTED with the cipher and handed to the loop in
// cfg.Token — DISCORD_BOT_TOKEN is not required (the base token here is empty).
func TestStartUsesSavedToken(t *testing.T) {
	const savedToken = "MT999.saved.bot.token"
	cipher := newCipher(t)
	sealed, err := cipher.Seal([]byte(savedToken))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	store := newFakeStore()
	store.dep.DiscordBotTokenCiphertext = sealed
	store.dep.DiscordBotTokenLast4 = crypto.Last4(savedToken)

	runner := newBlockingRunner()
	mgr := session.NewManager(store, runner.run, wirenpc.Config{}, cipher,
		slog.New(slog.DiscardHandler), true, session.Deps{})

	if _, err := mgr.Start(context.Background(), uuid.New(), uuid.New()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _, _ = mgr.Stop(context.Background()) })

	select {
	case <-runner.started:
	case <-time.After(2 * time.Second):
		t.Fatal("loop runner never started")
	}
	if got := runner.cfg().Token; got != savedToken {
		t.Errorf("loop cfg token = %q, want decrypted saved token %q", got, savedToken)
	}
}

// TestStartFallsBackToEnvToken is issue #87 AC2: with no real saved token (the
// "env" placeholder), Start uses the base DISCORD_BOT_TOKEN — the
// voice-mode/dev/CI path is preserved.
func TestStartFallsBackToEnvToken(t *testing.T) {
	store := newFakeStore()
	store.dep.DiscordBotTokenLast4 = "env" // seeded placeholder: no real token in the DB
	runner := newBlockingRunner()
	mgr := session.NewManager(store, runner.run, wirenpc.Config{Token: "env-bot-token"}, nil,
		slog.New(slog.DiscardHandler), true, session.Deps{})

	if _, err := mgr.Start(context.Background(), uuid.New(), uuid.New()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _, _ = mgr.Stop(context.Background()) })

	select {
	case <-runner.started:
	case <-time.After(2 * time.Second):
		t.Fatal("loop runner never started")
	}
	if got := runner.cfg().Token; got != "env-bot-token" {
		t.Errorf("loop cfg token = %q, want env fallback %q", got, "env-bot-token")
	}
}

// TestStartMissingToken is issue #87 AC3: neither a saved token nor an env token
// -> a clear precondition (ErrDiscordTokenMissing) and NO voice_sessions row.
func TestStartMissingToken(t *testing.T) {
	store := newFakeStore() // guild/channel set, no token saved
	mgr := session.NewManager(store, newBlockingRunner().run, wirenpc.Config{}, nil,
		slog.New(slog.DiscardHandler), true, session.Deps{})

	_, err := mgr.Start(context.Background(), uuid.New(), uuid.New())
	if !errors.Is(err, session.ErrDiscordTokenMissing) {
		t.Errorf("Start with no token = %v, want ErrDiscordTokenMissing", err)
	}
	if created, _ := store.counts(); created != 0 {
		t.Errorf("created rows = %d, want 0 (missing token must not write)", created)
	}
}

// TestStartUndecryptableToken is issue #87 polish: a REAL saved token the manager
// cannot decrypt (here: no cipher, the boot-without-$GLYPHOXA_SECRET misconfig)
// fails Start with the ErrDiscordTokenUndecryptable precondition (not an opaque
// internal error) and writes NO voice_sessions row.
func TestStartUndecryptableToken(t *testing.T) {
	const savedToken = "MT999.saved.bot.token"
	cipher := newCipher(t)
	sealed, err := cipher.Seal([]byte(savedToken))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	store := newFakeStore()
	store.dep.DiscordBotTokenCiphertext = sealed
	store.dep.DiscordBotTokenLast4 = crypto.Last4(savedToken)

	// nil cipher: a real saved token can no longer be opened.
	mgr := session.NewManager(store, newBlockingRunner().run, wirenpc.Config{}, nil,
		slog.New(slog.DiscardHandler), true, session.Deps{})

	_, err = mgr.Start(context.Background(), uuid.New(), uuid.New())
	if !errors.Is(err, session.ErrDiscordTokenUndecryptable) {
		t.Errorf("Start with undecryptable token = %v, want ErrDiscordTokenUndecryptable", err)
	}
	if created, _ := store.counts(); created != 0 {
		t.Errorf("created rows = %d, want 0 (undecryptable token must not write)", created)
	}
}

// fakeFinalizer is a stand-in TranscriptFinalizer: it records the id it was asked
// to finalize and returns a canned authoritative line count (#74).
type fakeFinalizer struct {
	mu     sync.Mutex
	id     uuid.UUID
	count  int
	called int
}

func (f *fakeFinalizer) Finalize(_ context.Context, id uuid.UUID) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.called++
	f.id = id
	return f.count, nil
}

func (f *fakeFinalizer) seen() (uuid.UUID, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.id, f.called
}

// TestStopFinalizesTranscriptCount is #74 AC2: on Stop the Manager finalizes the
// transcript for the active session and records the authoritative line_count on
// the ended row (replacing the always-0 in-memory count).
func TestStopFinalizesTranscriptCount(t *testing.T) {
	store := newFakeStore()
	runner := newBlockingRunner()
	fin := &fakeFinalizer{count: 9}
	mgr := newManagerDeps(t, store, runner.run, true, session.Deps{Transcript: fin})

	vs, err := mgr.Start(context.Background(), uuid.New(), uuid.New())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case <-runner.started:
	case <-time.After(2 * time.Second):
		t.Fatal("loop runner never started")
	}

	ended, err := mgr.Stop(context.Background())
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if ended.LineCount != 9 {
		t.Errorf("ended line_count = %d, want 9 (the finalized count)", ended.LineCount)
	}
	if id, called := fin.seen(); called != 1 || id != vs.ID {
		t.Errorf("finalize seen id=%s called=%d, want id=%s called=1", id, called, vs.ID)
	}
}

// spyHighlighter records Begin/Finalize so a test can assert the Manager binds
// and unbinds the Session Highlights pipeline at Start/loop-exit (#308).
type spyHighlighter struct {
	mu         sync.Mutex
	beginArgs  [3]uuid.UUID
	beginCalls int
	finalizes  int
}

func (s *spyHighlighter) HandleTrigger(highlight.Trigger) {}

func (s *spyHighlighter) Begin(voiceSessionID, campaignID, tenantID uuid.UUID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.beginArgs = [3]uuid.UUID{voiceSessionID, campaignID, tenantID}
	s.beginCalls++
}

func (s *spyHighlighter) Finalize(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finalizes++
	return nil
}

func (s *spyHighlighter) seen() (begins, finals int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.beginCalls, s.finalizes
}

// TestManagerBeginsAndFinalizesHighlights is #308's Manager slice: Start binds the
// Highlights pipeline to the session's owning ids, and loop exit finalizes it
// (nothing dangles) — the SAME lifecycle transcript.Finalize gets.
func TestManagerBeginsAndFinalizesHighlights(t *testing.T) {
	store := newFakeStore()
	runner := newBlockingRunner()
	spy := &spyHighlighter{}
	mgr := newManagerDeps(t, store, runner.run, true, session.Deps{Highlights: spy})

	tenantID, campaignID := uuid.New(), uuid.New()
	vs, err := mgr.Start(context.Background(), tenantID, campaignID)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case <-runner.started:
	case <-time.After(2 * time.Second):
		t.Fatal("loop runner never started")
	}
	if begins, _ := spy.seen(); begins != 1 {
		t.Fatalf("Begin called %d times, want 1", begins)
	}
	if spy.beginArgs != [3]uuid.UUID{vs.ID, campaignID, tenantID} {
		t.Fatalf("Begin args = %v, want session/campaign/tenant %v/%v/%v", spy.beginArgs, vs.ID, campaignID, tenantID)
	}

	if _, err := mgr.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if begins, finals := spy.seen(); begins != 1 || finals != 1 {
		t.Fatalf("after Stop: begins=%d finals=%d, want 1/1", begins, finals)
	}
}

// slowFinalizer is a TranscriptFinalizer that consumes its ENTIRE context
// budget (a flush barrier stuck behind slow line UPSERTs, #143 Defect B) and
// returns the context error, like the relay would.
type slowFinalizer struct{}

func (slowFinalizer) Finalize(ctx context.Context, _ uuid.UUID) (int, error) {
	<-ctx.Done()
	return 0, ctx.Err()
}

// TestSlowFinalizeStillEndsRow is #143 Defect B: a Finalize that exhausts the
// whole end budget must NOT hand EndVoiceSession an already-expired context —
// the end-write gets its own fresh deadline, so the row still lands 'ended'.
func TestSlowFinalizeStillEndsRow(t *testing.T) {
	store := newFakeStore()
	runner := newBlockingRunner()
	mgr := newManagerDeps(t, store, runner.run, true, session.Deps{Transcript: slowFinalizer{}})
	mgr.SetEndTimeoutForTest(50 * time.Millisecond)

	vs, err := mgr.Start(context.Background(), uuid.New(), uuid.New())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case <-runner.started:
	case <-time.After(2 * time.Second):
		t.Fatal("loop runner never started")
	}

	ended, err := mgr.Stop(context.Background())
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if ended.Status != storage.VoiceSessionEnded || ended.EndedAt == nil {
		t.Errorf("stopped session = %+v, want ended with ended_at", ended)
	}
	store.mu.Lock()
	row := store.sessions[vs.ID]
	store.mu.Unlock()
	if row.Status != storage.VoiceSessionEnded {
		t.Errorf("stored row status = %q, want ended (end-write must get a fresh deadline)", row.Status)
	}
}

// TestStopSurfacesEndWriteFailure is #143 Defect A: when the EndVoiceSession
// write fails, Stop must NOT report plain success carrying the stale 'running'
// row — the caller sees the failure (previously it was log-only and StopSession
// answered success with status='running').
func TestStopSurfacesEndWriteFailure(t *testing.T) {
	store := newFakeStore()
	endErr := errors.New("db down at stop time")
	store.mu.Lock()
	store.endErr = endErr
	store.mu.Unlock()
	runner := newBlockingRunner()
	mgr := newManager(t, store, runner.run, true)

	if _, err := mgr.Start(context.Background(), uuid.New(), uuid.New()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case <-runner.started:
	case <-time.After(2 * time.Second):
		t.Fatal("loop runner never started")
	}

	got, err := mgr.Stop(context.Background())
	if err == nil {
		t.Fatalf("Stop = %+v with nil error, want the end-write failure surfaced", got)
	}
	if !errors.Is(err, endErr) {
		t.Errorf("Stop error = %v, want the underlying end-write failure in the chain", err)
	}
}

// TestStopWithoutActiveSession returns ErrNoActiveSession.
func TestStopWithoutActiveSession(t *testing.T) {
	mgr := newManager(t, newFakeStore(), newBlockingRunner().run, true)
	if _, err := mgr.Stop(context.Background()); !errors.Is(err, session.ErrNoActiveSession) {
		t.Errorf("Stop with no session = %v, want ErrNoActiveSession", err)
	}
}

// TestStartDisabledMode rejects Start when voice is not enabled for the mode
// (web-only, ADR-0039: all-mode drives sessions in-process).
func TestStartDisabledMode(t *testing.T) {
	mgr := newManager(t, newFakeStore(), newBlockingRunner().run, false)
	if _, err := mgr.Start(context.Background(), uuid.New(), uuid.New()); !errors.Is(err, session.ErrVoiceUnavailable) {
		t.Errorf("Start in disabled mode = %v, want ErrVoiceUnavailable", err)
	}
}

// TestStartAfterShutdownRefused is #157: Shutdown moves the Manager to a
// terminal closed state, so a Start that acquires the lock after Shutdown has
// returned fails fast with ErrManagerClosed — it must not touch the store (no
// config read, no running row INSERT) and must not spawn a loop nothing will
// ever cancel.
func TestStartAfterShutdownRefused(t *testing.T) {
	store := newFakeStore()
	runner := newBlockingRunner()
	mgr := newManager(t, store, runner.run, true)

	mgr.Shutdown()

	_, err := mgr.Start(context.Background(), uuid.New(), uuid.New())
	if !errors.Is(err, session.ErrManagerClosed) {
		t.Errorf("Start after Shutdown = %v, want ErrManagerClosed", err)
	}
	store.mu.Lock()
	depReads, created := store.depReads, store.created
	store.mu.Unlock()
	if depReads != 0 || created != 0 {
		t.Errorf("store touched after Shutdown: depReads=%d created=%d, want 0/0", depReads, created)
	}
	select {
	case <-runner.started:
		t.Error("loop spawned after Shutdown")
	case <-time.After(50 * time.Millisecond):
	}
}

// TestReconcileOrphansAtStartup is #143's reconciliation: a row still 'running'
// from a crashed process (kill -9 / OOM — no live loop owns it) is closed at
// Manager startup, marked ended with the distinguishing orphaned reason, so
// GetLatestVoiceSession stops mislabeling a dead session as live.
func TestReconcileOrphansAtStartup(t *testing.T) {
	store := newFakeStore()
	orphan := storage.VoiceSession{
		ID:         uuid.New(),
		CampaignID: uuid.New(),
		StartedAt:  time.Date(2026, 6, 27, 11, 0, 0, 0, time.UTC),
		Status:     storage.VoiceSessionRunning,
	}
	store.mu.Lock()
	store.sessions[orphan.ID] = orphan
	store.mu.Unlock()

	mgr := newManager(t, store, newBlockingRunner().run, true)
	if err := mgr.ReconcileOrphans(context.Background()); err != nil {
		t.Fatalf("ReconcileOrphans: %v", err)
	}

	store.mu.Lock()
	row := store.sessions[orphan.ID]
	store.mu.Unlock()
	if row.Status != storage.VoiceSessionEnded || row.EndedAt == nil {
		t.Errorf("orphaned row = %+v, want ended with ended_at", row)
	}
	if row.EndReason == nil || *row.EndReason != storage.VoiceSessionReasonOrphaned {
		t.Errorf("orphaned row end_reason = %v, want %q", row.EndReason, storage.VoiceSessionReasonOrphaned)
	}
}

// TestReconcileOrphansWebOnlySkips: a web-only Manager (enabled=false) never
// created rows and must not touch ones another process may own — reconciliation
// is a no-op there.
func TestReconcileOrphansWebOnlySkips(t *testing.T) {
	store := newFakeStore()
	mgr := newManager(t, store, newBlockingRunner().run, false)
	if err := mgr.ReconcileOrphans(context.Background()); err != nil {
		t.Fatalf("ReconcileOrphans (web-only): %v", err)
	}
	store.mu.Lock()
	reconciles := store.reconciles
	store.mu.Unlock()
	if reconciles != 0 {
		t.Errorf("web-only reconciles = %d, want 0", reconciles)
	}
}

// TestShutdownIdempotentStopSafe is #157's tail: Shutdown of an active session
// ends it; a second Shutdown is an idempotent no-op; Stop after Shutdown stays
// well-defined (ErrNoActiveSession, no panic).
func TestShutdownIdempotentStopSafe(t *testing.T) {
	store := newFakeStore()
	runner := newBlockingRunner()
	mgr := newManager(t, store, runner.run, true)

	if _, err := mgr.Start(context.Background(), uuid.New(), uuid.New()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case <-runner.started:
	case <-time.After(2 * time.Second):
		t.Fatal("loop runner never started")
	}

	mgr.Shutdown()
	if _, ended := store.counts(); ended != 1 {
		t.Errorf("ended rows after Shutdown = %d, want 1", ended)
	}
	mgr.Shutdown() // idempotent: no second end write, no hang

	if _, err := mgr.Stop(context.Background()); !errors.Is(err, session.ErrNoActiveSession) {
		t.Errorf("Stop after Shutdown = %v, want ErrNoActiveSession", err)
	}
	if _, ended := store.counts(); ended != 1 {
		t.Errorf("ended rows after second Shutdown = %d, want still 1", ended)
	}
}

// TestLoopExitClearsActive asserts a loop that exits on its own (not via Stop)
// still ends the session and clears the active slot, so the guard frees up.
func TestLoopExitClearsActive(t *testing.T) {
	store := newFakeStore()
	// A runner that returns immediately (loop ended by itself).
	mgr := newManager(t, store, func(_ context.Context, _ wirenpc.Config) error { return nil }, true)

	if _, err := mgr.Start(context.Background(), uuid.New(), uuid.New()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Wait for the self-exiting loop to End the session and clear active.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, active := mgr.Snapshot(); !active {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("active session not cleared after the loop exited")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, ended := store.counts(); ended != 1 {
		t.Errorf("ended rows = %d, want 1 after self-exit", ended)
	}
}

// waitIdle polls Snapshot until the Manager reports no active session — the loop
// exited and the terminal row landed. Fails the test if it stays active.
func waitIdle(t *testing.T, mgr *session.Manager) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, active := mgr.Snapshot(); !active {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("session still active; loop did not exit")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestFatalLoopErrorRecordsFailed is #123 (AC1/AC4): a loop that exits with a
// classified fatal gateway rejection (NOT via Stop-cancellation) records the row
// as 'failed' with ended_at set and an end_reason carrying the fatal reason —
// "invalid_bot_token: …". The single-active guard is then freed, so a new Start is
// accepted at once. errors.As recovers the *FatalError through the loop's %w wrap.
func TestFatalLoopErrorRecordsFailed(t *testing.T) {
	store := newFakeStore()
	fatal := fmt.Errorf("wirenpc: run: %w", &wirenpc.FatalError{
		Reason: wirenpc.ReasonInvalidBotToken,
		Err:    errors.New("open gateway: websocket: close 4004: Authentication failed"),
	})
	mgr := newManager(t, store, func(context.Context, wirenpc.Config) error { return fatal }, true)

	vs, err := mgr.Start(context.Background(), uuid.New(), uuid.New())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitIdle(t, mgr)

	store.mu.Lock()
	row := store.sessions[vs.ID]
	store.mu.Unlock()
	if row.Status != storage.VoiceSessionFailed {
		t.Errorf("failed session status = %q, want failed", row.Status)
	}
	if row.EndedAt == nil {
		t.Error("failed session ended_at is nil, want set (no row stuck running, AC4)")
	}
	if row.EndReason == nil || !strings.HasPrefix(*row.EndReason, wirenpc.ReasonInvalidBotToken+":") {
		t.Errorf("failed end_reason = %v, want %q prefix", row.EndReason, wirenpc.ReasonInvalidBotToken+":")
	}

	// Guard freed: idle after the fatal exit, and a new Start is accepted (AC4).
	if _, active := mgr.Snapshot(); active {
		t.Error("Snapshot still active after a fatal loop exit")
	}
	if _, err := mgr.Start(context.Background(), uuid.New(), uuid.New()); err != nil {
		t.Errorf("Start after a fatal failure = %v, want success (single-active guard freed)", err)
	}
	waitIdle(t, mgr) // let the second (also-fatal) loop unwind before the test ends
}

// TestPlainLoopErrorRecordsFailedLoopError is #123: a non-classified loop error
// (an unexpected exit that is NOT a fatal gateway rejection) still ends the session
// 'failed', tagged "loop_error: …" so the durable record distinguishes it from a
// classified fatal.
func TestPlainLoopErrorRecordsFailedLoopError(t *testing.T) {
	store := newFakeStore()
	mgr := newManager(t, store, func(context.Context, wirenpc.Config) error {
		return errors.New("silero: vad init failed")
	}, true)

	vs, err := mgr.Start(context.Background(), uuid.New(), uuid.New())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitIdle(t, mgr)

	store.mu.Lock()
	row := store.sessions[vs.ID]
	store.mu.Unlock()
	if row.Status != storage.VoiceSessionFailed {
		t.Errorf("status = %q, want failed", row.Status)
	}
	if row.EndReason == nil || !strings.HasPrefix(*row.EndReason, "loop_error:") {
		t.Errorf("end_reason = %v, want %q prefix", row.EndReason, "loop_error:")
	}
}

// TestCancelledLoopEndsCleanNilReason pins that a Stop-cancelled loop is a CLEAN
// stop, NOT a failure: even though the runner returns ctx.Err() (non-nil), ctx was
// cancelled, so the row lands 'ended' with a NULL end_reason — never 'failed' (#123).
func TestCancelledLoopEndsCleanNilReason(t *testing.T) {
	store := newFakeStore()
	runner := newBlockingRunner()
	mgr := newManager(t, store, runner.run, true)

	if _, err := mgr.Start(context.Background(), uuid.New(), uuid.New()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case <-runner.started:
	case <-time.After(2 * time.Second):
		t.Fatal("loop runner never started")
	}

	ended, err := mgr.Stop(context.Background())
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if ended.Status != storage.VoiceSessionEnded {
		t.Errorf("status = %q, want ended (a Stop-cancelled loop is a clean stop, not failed)", ended.Status)
	}
	if ended.EndReason != nil {
		t.Errorf("end_reason = %q, want NULL for a clean stop", *ended.EndReason)
	}
}

// fakeChunkFinalizer is a stand-in ChunkFinalizer (#104): it records the id it was
// flushed for, the store's ended-count AT flush time (to prove the flush ran
// BEFORE EndVoiceSession), and can inject an error to prove a flush failure does
// not fail Stop.
type fakeChunkFinalizer struct {
	mu           sync.Mutex
	store        *fakeStore
	id           uuid.UUID
	called       int
	endedAtFlush int
	err          error
}

func (f *fakeChunkFinalizer) FlushSession(_ context.Context, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.called++
	f.id = id
	created, ended := f.store.counts()
	_ = created
	f.endedAtFlush = ended
	return f.err
}

func (f *fakeChunkFinalizer) seen() (uuid.UUID, int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.id, f.called, f.endedAtFlush
}

// TestStopFlushesChunkBeforeEnd is #104: on Stop / loop exit the Manager flushes
// the active session's open Transcript Chunk BEFORE ending the row.
func TestStopFlushesChunkBeforeEnd(t *testing.T) {
	store := newFakeStore()
	runner := newBlockingRunner()
	flusher := &fakeChunkFinalizer{store: store}
	mgr := newManagerDeps(t, store, runner.run, true, session.Deps{Chunker: flusher})

	vs, err := mgr.Start(context.Background(), uuid.New(), uuid.New())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case <-runner.started:
	case <-time.After(2 * time.Second):
		t.Fatal("loop runner never started")
	}

	if _, err := mgr.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	id, called, endedAtFlush := flusher.seen()
	if called != 1 || id != vs.ID {
		t.Errorf("chunk flush seen id=%s called=%d, want id=%s called=1", id, called, vs.ID)
	}
	if endedAtFlush != 0 {
		t.Errorf("EndVoiceSession ran (ended=%d) before the chunk flush; want flush first", endedAtFlush)
	}
}

// TestStopSucceedsDespiteChunkFlushError is #104: a chunk-flush failure logs and
// never fails Stop — the row still ends cleanly.
func TestStopSucceedsDespiteChunkFlushError(t *testing.T) {
	store := newFakeStore()
	runner := newBlockingRunner()
	mgr := newManagerDeps(t, store, runner.run, true,
		session.Deps{Chunker: &fakeChunkFinalizer{store: store, err: errors.New("chunk writer wedged")}})

	if _, err := mgr.Start(context.Background(), uuid.New(), uuid.New()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case <-runner.started:
	case <-time.After(2 * time.Second):
		t.Fatal("loop runner never started")
	}

	ended, err := mgr.Stop(context.Background())
	if err != nil {
		t.Fatalf("Stop returned %v, want nil (a chunk-flush error must not fail Stop)", err)
	}
	if ended.Status != storage.VoiceSessionEnded {
		t.Errorf("stopped session status = %q, want ended despite the flush error", ended.Status)
	}
}
