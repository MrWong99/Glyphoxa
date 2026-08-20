package highlight

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/voice/providererr"
	"github.com/MrWong99/Glyphoxa/pkg/voice/soundgen"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voicecassette"
)

// --- fakes ---

// fakeSoundEnrichStore is an in-memory SoundEnrichStore keyed by highlight id.
type fakeSoundEnrichStore struct {
	mu        sync.Mutex
	rows      map[uuid.UUID]storage.Highlight
	setCalls  int
	setErr    error // returned by SetHighlightSound when non-nil (overrides the conditional)
	lastKey   string
	lastCT    string
	lastSize  int64
	usageRows []storage.UsageRow
	usageErr  error

	claimed      map[uuid.UUID]time.Time
	claimCalls   int
	releaseCalls int
}

func newFakeSoundEnrichStore() *fakeSoundEnrichStore {
	return &fakeSoundEnrichStore{rows: map[uuid.UUID]storage.Highlight{}}
}

func (f *fakeSoundEnrichStore) GetHighlight(_ context.Context, tenantID, id uuid.UUID) (storage.Highlight, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	h, ok := f.rows[id]
	if !ok || h.TenantID != tenantID {
		return storage.Highlight{}, storage.ErrNotFound
	}
	return h, nil
}

// SetHighlightSound mirrors the real conditional land: it misses (ErrNotFound)
// when the row is gone OR its sound_kind no longer matches.
func (f *fakeSoundEnrichStore) SetHighlightSound(_ context.Context, id uuid.UUID, kind, key, ct string, sz int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setCalls++
	if f.setErr != nil {
		return f.setErr
	}
	h, ok := f.rows[id]
	if !ok || h.SoundKind != kind {
		return storage.ErrNotFound
	}
	h.SoundKey, h.SoundContentType, h.SoundSizeBytes = key, ct, sz
	f.rows[id] = h
	f.lastKey, f.lastCT, f.lastSize = key, ct, sz
	return nil
}

func (f *fakeSoundEnrichStore) TryClaimHighlightSoundEnrich(_ context.Context, id uuid.UUID, ttl time.Duration) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claimCalls++
	if h, ok := f.rows[id]; ok && (h.SoundKey != "" || h.SoundKind == "") {
		return false, nil
	}
	if prev, ok := f.claimed[id]; ok && time.Since(prev) < ttl {
		return false, nil
	}
	if f.claimed == nil {
		f.claimed = map[uuid.UUID]time.Time{}
	}
	f.claimed[id] = time.Now()
	return true, nil
}

func (f *fakeSoundEnrichStore) ReleaseHighlightSoundEnrichClaim(ctx context.Context, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releaseCalls++
	if err := ctx.Err(); err != nil {
		return err
	}
	delete(f.claimed, id)
	return nil
}

func (f *fakeSoundEnrichStore) AddUsage(_ context.Context, rows []storage.UsageRow) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.usageErr != nil {
		return f.usageErr
	}
	f.usageRows = append(f.usageRows, rows...)
	return nil
}

// fakeSoundGen returns a fixed result (or error) and records the requests each
// method saw.
type fakeSoundGen struct {
	mu       sync.Mutex
	res      soundgen.Result
	err      error
	stings   []soundgen.Request
	musics   []soundgen.Request
	genCalls int
}

func (g *fakeSoundGen) GenerateSting(_ context.Context, req soundgen.Request) (soundgen.Result, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.genCalls++
	g.stings = append(g.stings, req)
	return g.res, g.err
}

func (g *fakeSoundGen) ComposeMusic(_ context.Context, req soundgen.Request) (soundgen.Result, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.genCalls++
	g.musics = append(g.musics, req)
	return g.res, g.err
}

func soundFactoryOf(gen soundgen.Generator, err error) SoundGeneratorFactory {
	return func(context.Context, uuid.UUID) (soundgen.Generator, error) { return gen, err }
}

func soundRow(tenantID, id uuid.UUID, kind string) storage.Highlight {
	now := time.Now()
	return storage.Highlight{
		ID: id, TenantID: tenantID, VoiceSessionID: uuid.New(), CampaignID: uuid.New(),
		Status: storage.HighlightPromoted, SoundKind: kind, SoundRequestedAt: &now,
		StartsAt: now.Add(-15 * time.Second), EndsAt: now,
		Excerpt: "I rolled a natural twenty! A critical hit on the dragon!",
		Reason:  "a critical hit felling the dragon",
	}
}

func soundPayload(t *testing.T, id, tenantID uuid.UUID, kind string) json.RawMessage {
	t.Helper()
	p, err := MarshalEnrichSound(id, tenantID, kind)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return p
}

// --- handler ---

// TestEnrichSoundStingLands is the happy path: the sting is generated with a
// clip-derived duration, the blob lands at t/<tenant>/highlight/<id>/sound,
// the conditional land records the triad, and one Usage Ledger row flushes
// under the tts component with the SFX model id (ADR-0004 amendment).
func TestEnrichSoundStingLands(t *testing.T) {
	t.Parallel()
	tenantID, id := uuid.New(), uuid.New()
	store := newFakeSoundEnrichStore()
	store.rows[id] = soundRow(tenantID, id, storage.SoundKindSting)
	blobs := newFakeBlobs()
	gen := &fakeSoundGen{res: soundgen.Result{
		Data: []byte("mp3"), ContentType: "audio/mpeg", Model: "eleven_text_to_sound_v2", CharacterCost: 100,
	}}

	h := EnrichSoundHandler(store, blobs, soundFactoryOf(gen, nil), nil)
	if err := h(context.Background(), soundPayload(t, id, tenantID, storage.SoundKindSting)); err != nil {
		t.Fatalf("handler: %v", err)
	}

	wantKey := "t/" + tenantID.String() + "/highlight/" + id.String() + "/sound"
	if store.lastKey != wantKey {
		t.Errorf("sound key = %q, want %q", store.lastKey, wantKey)
	}
	if store.lastCT != "audio/mpeg" || store.lastSize != 3 {
		t.Errorf("landed (ct=%q, size=%d), want (audio/mpeg, 3)", store.lastCT, store.lastSize)
	}
	if len(gen.stings) != 1 || len(gen.musics) != 0 {
		t.Fatalf("gen calls: stings=%d musics=%d, want 1/0", len(gen.stings), len(gen.musics))
	}
	// The 15s clip fits the sting bounds, so the duration matches the clip.
	if d := gen.stings[0].Duration; d != 15*time.Second {
		t.Errorf("sting duration = %v, want 15s (the clip length)", d)
	}
	if !strings.Contains(gen.stings[0].Prompt, "natural twenty") {
		t.Errorf("prompt %q does not carry the excerpt", gen.stings[0].Prompt)
	}
	if len(store.usageRows) != 1 {
		t.Fatalf("usage rows = %d, want 1", len(store.usageRows))
	}
	u := store.usageRows[0]
	if u.Component != storage.ComponentTTS || u.Model != "eleven_text_to_sound_v2" || u.Provider != "elevenlabs" {
		t.Errorf("usage attribution = (%s, %s, %s), want (tts, elevenlabs, eleven_text_to_sound_v2)", u.Component, u.Provider, u.Model)
	}
	if u.TTSCharacters != 100 {
		t.Errorf("usage character cost = %d, want the vendor-reported 100", u.TTSCharacters)
	}
	if u.EstimatedUSD <= 0 {
		t.Errorf("usage estimate = %v, want > 0", u.EstimatedUSD)
	}
	// Success releases the claim (unlike the never-re-run image handler): a
	// stamped claim would block the next regeneration request for the full ttl.
	if store.releaseCalls != 1 {
		t.Errorf("release calls = %d, want 1 (re-runs must not wait out the ttl)", store.releaseCalls)
	}
}

// TestEnrichSoundMusicDurationBounds pins the music-kind routing and the
// "full track" floor: a 15s clip composes at the 30s minimum, instrumental
// mood prompt included.
func TestEnrichSoundMusicDurationBounds(t *testing.T) {
	t.Parallel()
	tenantID, id := uuid.New(), uuid.New()
	store := newFakeSoundEnrichStore()
	store.rows[id] = soundRow(tenantID, id, storage.SoundKindMusic)
	blobs := newFakeBlobs()
	gen := &fakeSoundGen{res: soundgen.Result{Data: []byte("m"), ContentType: "audio/mpeg", Model: "music_v1"}}

	h := EnrichSoundHandler(store, blobs, soundFactoryOf(gen, nil), nil)
	if err := h(context.Background(), soundPayload(t, id, tenantID, storage.SoundKindMusic)); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if len(gen.musics) != 1 || len(gen.stings) != 0 {
		t.Fatalf("gen calls: stings=%d musics=%d, want 0/1", len(gen.stings), len(gen.musics))
	}
	if d := gen.musics[0].Duration; d != musicMinDuration {
		t.Errorf("music duration = %v, want the %v floor", d, musicMinDuration)
	}
}

// TestEnrichSoundSkips pins the no-spend early exits: a deleted row, a
// superseded kind, and an already-landed sound all complete without touching
// the generator.
func TestEnrichSoundSkips(t *testing.T) {
	t.Parallel()
	tenantID, id := uuid.New(), uuid.New()

	cases := []struct {
		name string
		prep func(*fakeSoundEnrichStore)
	}{
		{"row gone", func(s *fakeSoundEnrichStore) {}},
		{"kind superseded", func(s *fakeSoundEnrichStore) {
			s.rows[id] = soundRow(tenantID, id, storage.SoundKindMusic) // payload asks sting
		}},
		{"already landed", func(s *fakeSoundEnrichStore) {
			row := soundRow(tenantID, id, storage.SoundKindSting)
			row.SoundKey = "t/x/highlight/y/sound"
			s.rows[id] = row
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeSoundEnrichStore()
			tc.prep(store)
			gen := &fakeSoundGen{res: soundgen.Result{Data: []byte("x"), ContentType: "audio/mpeg", Model: "m"}}
			h := EnrichSoundHandler(store, newFakeBlobs(), soundFactoryOf(gen, nil), nil)
			if err := h(context.Background(), soundPayload(t, id, tenantID, storage.SoundKindSting)); err != nil {
				t.Fatalf("handler: %v", err)
			}
			if gen.genCalls != 0 {
				t.Errorf("generator ran %d times, want 0 (no double spend)", gen.genCalls)
			}
		})
	}
}

// TestEnrichSoundNotConfigured pins the ADR-0004 clean no-op: the job completes
// (nil), the claim is released, and the Highlight stays intact without media.
func TestEnrichSoundNotConfigured(t *testing.T) {
	t.Parallel()
	tenantID, id := uuid.New(), uuid.New()
	store := newFakeSoundEnrichStore()
	store.rows[id] = soundRow(tenantID, id, storage.SoundKindSting)

	h := EnrichSoundHandler(store, newFakeBlobs(), soundFactoryOf(nil, ErrSoundNotConfigured), nil)
	if err := h(context.Background(), soundPayload(t, id, tenantID, storage.SoundKindSting)); err != nil {
		t.Fatalf("handler: %v (a missing key must not retry)", err)
	}
	if store.releaseCalls != 1 {
		t.Errorf("release calls = %d, want 1", store.releaseCalls)
	}
	if got := store.rows[id]; got.SoundKey != "" {
		t.Errorf("sound key = %q, want empty (row untouched)", got.SoundKey)
	}
}

// TestEnrichSoundProviderErrorRetries pins the dead-letter posture: EVERY
// provider failure — even a rejected key — returns the error so the runner
// retries / dead-letters and the boot sweep can recover it after a key fix.
func TestEnrichSoundProviderErrorRetries(t *testing.T) {
	t.Parallel()
	tenantID, id := uuid.New(), uuid.New()
	store := newFakeSoundEnrichStore()
	store.rows[id] = soundRow(tenantID, id, storage.SoundKindSting)
	gen := &fakeSoundGen{err: &providererr.HTTPError{Op: "elevenlabs.GenerateSting", StatusCode: 401, Status: "401 Unauthorized"}}

	h := EnrichSoundHandler(store, newFakeBlobs(), soundFactoryOf(gen, nil), nil)
	if err := h(context.Background(), soundPayload(t, id, tenantID, storage.SoundKindSting)); err == nil {
		t.Fatal("handler returned nil for a provider error, want an error (retry/dead-letter)")
	}
	if store.releaseCalls != 1 {
		t.Errorf("release calls = %d, want 1 (claim freed for the retry)", store.releaseCalls)
	}
	if got := store.rows[id]; got.SoundKey != "" {
		t.Errorf("sound key = %q, want empty (row untouched on failure)", got.SoundKey)
	}
}

// TestEnrichSoundLandMissDisambiguates pins the conditional-land miss handling:
// a GONE row compensates the just-stored blob; a kind changed mid-generation
// leaves the blob for the newer job to overwrite (deleting could destroy that
// job's asset) and releases the claim.
func TestEnrichSoundLandMissDisambiguates(t *testing.T) {
	t.Parallel()
	tenantID, id := uuid.New(), uuid.New()

	t.Run("row deleted mid-generation", func(t *testing.T) {
		store := newFakeSoundEnrichStore()
		store.rows[id] = soundRow(tenantID, id, storage.SoundKindSting)
		blobs := newFakeBlobs()
		gen := &fakeSoundGen{res: soundgen.Result{Data: []byte("x"), ContentType: "audio/mpeg", Model: "m"}}
		// The land misses AND the follow-up read finds no row: delete happened.
		store.setErr = storage.ErrNotFound
		h := EnrichSoundHandler(&rowVanishesAfterFirstRead{fakeSoundEnrichStore: store, id: id}, blobs, soundFactoryOf(gen, nil), nil)
		if err := h(context.Background(), soundPayload(t, id, tenantID, storage.SoundKindSting)); err != nil {
			t.Fatalf("handler: %v", err)
		}
		if len(blobs.deleted) != 1 {
			t.Errorf("compensated blob deletes = %d, want 1", len(blobs.deleted))
		}
	})

	t.Run("kind changed mid-generation", func(t *testing.T) {
		store := newFakeSoundEnrichStore()
		row := soundRow(tenantID, id, storage.SoundKindSting)
		store.rows[id] = row
		blobs := newFakeBlobs()
		gen := &fakeSoundGen{res: soundgen.Result{Data: []byte("x"), ContentType: "audio/mpeg", Model: "m"}}
		// Force the conditional miss while the row still exists (kind changed).
		store.setErr = storage.ErrNotFound
		h := EnrichSoundHandler(store, blobs, soundFactoryOf(gen, nil), nil)
		if err := h(context.Background(), soundPayload(t, id, tenantID, storage.SoundKindSting)); err != nil {
			t.Fatalf("handler: %v", err)
		}
		if len(blobs.deleted) != 0 {
			t.Errorf("blob deletes = %d, want 0 (the newer job owns the shared name)", len(blobs.deleted))
		}
		if store.releaseCalls != 1 {
			t.Errorf("release calls = %d, want 1 (the newer job must be able to claim)", store.releaseCalls)
		}
	})
}

// rowVanishesAfterFirstRead delegates to the embedded fake but reports the row
// gone from the SECOND GetHighlight on — the delete-vs-land race.
type rowVanishesAfterFirstRead struct {
	*fakeSoundEnrichStore
	id    uuid.UUID
	reads int
}

func (s *rowVanishesAfterFirstRead) GetHighlight(ctx context.Context, tenantID, id uuid.UUID) (storage.Highlight, error) {
	s.mu.Lock()
	s.reads++
	n := s.reads
	s.mu.Unlock()
	if id == s.id && n > 1 {
		return storage.Highlight{}, storage.ErrNotFound
	}
	return s.fakeSoundEnrichStore.GetHighlight(ctx, tenantID, id)
}

// --- boot sweep, sound half (#312) ---

// soundRecordingEnqueuer records every sound-enrichment enqueue the boot sweep
// makes (the enrichRecordingEnqueuer shape for the sound payload type).
type soundRecordingEnqueuer struct {
	mu  sync.Mutex
	all []soundEnrichPayload
}

func (r *soundRecordingEnqueuer) Enqueue(_ context.Context, kind string, payload any, _ time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p, ok := payload.(soundEnrichPayload); ok && kind == JobKindEnrichSound {
		r.all = append(r.all, p)
	}
	return nil
}

// TestSweepReenqueuesRequestedSounds pins the (a') half: a promoted Highlight
// with a requested-but-unlanded sound and no live job is re-enqueued with the
// row's CURRENT kind in the payload.
func TestSweepReenqueuesRequestedSounds(t *testing.T) {
	t.Parallel()
	hID, tID := uuid.New(), uuid.New()
	store := &fakeReconcileStore{soundTargets: []storage.HighlightSoundEnrichTarget{
		{HighlightID: hID, TenantID: tID, Kind: storage.SoundKindMusic},
	}}
	enq := &soundRecordingEnqueuer{}
	if err := SweepEnrichmentReconciliation(context.Background(), store, newFakeBlobs(), enq, nil); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if store.gotSoundKind != JobKindEnrichSound {
		t.Errorf("sweep asked for kind %q, want %q", store.gotSoundKind, JobKindEnrichSound)
	}
	got := enq.all
	if len(got) != 1 || got[0].HighlightID != hID || got[0].TenantID != tID || got[0].Kind != storage.SoundKindMusic {
		t.Fatalf("re-enqueued payloads = %+v, want one for (%s, %s, music)", got, hID, tID)
	}
}

// --- cassette-backed determinism (ADR-0021) ---

// TestEnrichSoundCassette drives the WHOLE handler through the recorded sound
// cassette: the prompt derivation and clip-derived durations must fingerprint
// to the pinned request hashes, or the test fails pointing at the re-record
// workflow — the wrapper's cassette-style deterministic test (#312 AC).
func TestEnrichSoundCassette(t *testing.T) {
	gen := voicecassette.LoadSound(t, "sound-highlight-dragon")

	for _, kind := range []string{storage.SoundKindSting, storage.SoundKindMusic} {
		t.Run(kind, func(t *testing.T) {
			tenantID, id := uuid.New(), uuid.New()
			store := newFakeSoundEnrichStore()
			row := soundRow(tenantID, id, kind)
			// Pin the clip range the cassette hashes were recorded for: 15s.
			row.EndsAt = row.StartsAt.Add(15 * time.Second)
			store.rows[id] = row
			blobs := newFakeBlobs()

			h := EnrichSoundHandler(store, blobs, soundFactoryOf(gen, nil), nil)
			if err := h(context.Background(), soundPayload(t, id, tenantID, kind)); err != nil {
				t.Fatalf("handler over cassette: %v", err)
			}
			if store.lastCT != "audio/mpeg" {
				t.Errorf("content type = %q, want the cassette's audio/mpeg", store.lastCT)
			}
			if store.lastSize == 0 {
				t.Errorf("landed size = 0, want the stub bytes")
			}
		})
	}
}
