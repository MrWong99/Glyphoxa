package session_test

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/session"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/storage/crypto"
	"github.com/MrWong99/Glyphoxa/internal/wirenpc"
)

// fakeStore is an in-memory session.Store: it serves a canned deployment config
// (guild/channel source, #72) and records the voice_sessions Create/End calls so
// the lifecycle can be asserted without Postgres.
type fakeStore struct {
	mu       sync.Mutex
	dep      storage.DeploymentConfig
	depErr   error
	sessions map[uuid.UUID]storage.VoiceSession
	created  int
	ended    int
	depReads int
	tick     int
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

func (f *fakeStore) EndVoiceSession(ctx context.Context, id uuid.UUID, lineCount int) (storage.VoiceSession, error) {
	// A real EndVoiceSession fails on an expired context (#143 Defect B pins
	// exactly that), so the fake honours ctx like pgx would.
	if err := ctx.Err(); err != nil {
		return storage.VoiceSession{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ended++
	vs, ok := f.sessions[id]
	if !ok {
		return storage.VoiceSession{}, storage.ErrNotFound
	}
	end := f.now()
	vs.EndedAt = &end
	vs.Status = storage.VoiceSessionEnded
	vs.LineCount = lineCount
	f.sessions[id] = vs
	return vs, nil
}

func (f *fakeStore) counts() (created, ended int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.created, f.ended
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
	return session.NewManager(store, run, wirenpc.Config{Token: "test-token"}, nil,
		slog.New(slog.DiscardHandler), enabled)
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
		slog.New(slog.DiscardHandler), true)

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
		slog.New(slog.DiscardHandler), true)

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
		slog.New(slog.DiscardHandler), true)

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
		slog.New(slog.DiscardHandler), true)

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
	mgr := newManager(t, store, runner.run, true)
	fin := &fakeFinalizer{count: 9}
	mgr.SetTranscript(fin)

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
	mgr := newManager(t, store, runner.run, true)
	mgr.SetEndTimeoutForTest(50 * time.Millisecond)
	mgr.SetTranscript(slowFinalizer{})

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
