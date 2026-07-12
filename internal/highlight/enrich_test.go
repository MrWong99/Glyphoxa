package highlight

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/imagegen"
	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// --- fakes ---

// fakeEnrichStore is an in-memory EnrichStore keyed by highlight id.
type fakeEnrichStore struct {
	mu         sync.Mutex
	rows       map[uuid.UUID]storage.Highlight
	setCalls   int
	setErr     error // returned by SetHighlightImage when non-nil
	lastImgKey string
	lastImgCT  string
	lastImgSz  int64

	claimed      map[uuid.UUID]time.Time // live claims (id -> claimed_at)
	claimErr     error                   // returned by TryClaimHighlightEnrich when non-nil
	claimCalls   int
	releaseCalls int
}

func newFakeEnrichStore() *fakeEnrichStore {
	return &fakeEnrichStore{rows: map[uuid.UUID]storage.Highlight{}}
}

func (f *fakeEnrichStore) GetHighlight(_ context.Context, tenantID, id uuid.UUID) (storage.Highlight, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	h, ok := f.rows[id]
	if !ok || h.TenantID != tenantID {
		return storage.Highlight{}, storage.ErrNotFound
	}
	return h, nil
}

func (f *fakeEnrichStore) SetHighlightImage(_ context.Context, id uuid.UUID, key, ct string, sz int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setCalls++
	if f.setErr != nil {
		return f.setErr
	}
	h, ok := f.rows[id]
	if !ok {
		return storage.ErrNotFound
	}
	h.ImageKey, h.ImageContentType, h.ImageSizeBytes = key, ct, sz
	f.rows[id] = h
	f.lastImgKey, f.lastImgCT, f.lastImgSz = key, ct, sz
	return nil
}

func (f *fakeEnrichStore) TryClaimHighlightEnrich(_ context.Context, id uuid.UUID, ttl time.Duration) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.claimErr != nil {
		return false, f.claimErr
	}
	if h, ok := f.rows[id]; ok && h.ImageKey != "" {
		return false, nil // already enriched: nothing to claim
	}
	if prev, ok := f.claimed[id]; ok && time.Since(prev) < ttl {
		return false, nil // a fresh claim is held by a concurrent worker
	}
	if f.claimed == nil {
		f.claimed = map[uuid.UUID]time.Time{}
	}
	f.claimed[id] = time.Now()
	f.claimCalls++
	return true, nil
}

func (f *fakeEnrichStore) ReleaseHighlightEnrichClaim(_ context.Context, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releaseCalls++
	delete(f.claimed, id)
	return nil
}

// fakeBlobs (in-memory blob.Store) is declared in saver_test.go and shared here.

// fakeGen is a Generator that returns a canned Result and counts calls.
type fakeGen struct {
	mu      sync.Mutex
	calls   int
	res     imagegen.Result
	err     error
	gotArgs string
}

func (g *fakeGen) Generate(_ context.Context, prompt string) (imagegen.Result, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls++
	g.gotArgs = prompt
	return g.res, g.err
}

// provRecorder captures the LLMTokens provider + counts (the package spyRecorder
// drops the provider).
type provRecorder struct {
	observe.Discard
	mu       sync.Mutex
	provider observe.Provider
	model    string
	in, out  int
	seen     bool
}

func (r *provRecorder) LLMTokens(p observe.Provider, model string, in, out int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.provider, r.model, r.in, r.out, r.seen = p, model, in, out, true
}

// seedRow inserts a promoted, imageless highlight and returns it.
func seedRow(store *fakeEnrichStore, tenantID uuid.UUID) storage.Highlight {
	h := storage.Highlight{
		ID:              uuid.New(),
		TenantID:        tenantID,
		CampaignID:      uuid.New(),
		Status:          storage.HighlightPromoted,
		Excerpt:         "natural 20 against the ancient red dragon",
		Reason:          "a clutch critical hit that saved the party",
		ClipKey:         "t/" + tenantID.String() + "/highlight/x/clip.wav",
		ClipContentType: "audio/wav",
	}
	store.rows[h.ID] = h
	return h
}

func factoryReturning(gen imagegen.Generator, model string, err error) GeneratorFactory {
	return func(context.Context, uuid.UUID) (imagegen.Generator, string, error) {
		return gen, model, err
	}
}

// --- tests ---

func TestEnrichImageHandler_HappyPath(t *testing.T) {
	tenantID := uuid.New()
	store := newFakeEnrichStore()
	h := seedRow(store, tenantID)
	blobs := newFakeBlobs()
	gen := &fakeGen{res: imagegen.Result{Data: []byte("PNGDATA"), ContentType: "image/png", PromptTokens: 40, OutputTokens: 1290}}
	rec := &provRecorder{}

	handler := EnrichImageHandler(store, blobs, factoryReturning(gen, "gemini-2.5-flash-image", nil), rec, nil)
	payload, _ := MarshalEnrichImage(h.ID, tenantID)

	if err := handler(context.Background(), payload); err != nil {
		t.Fatalf("handler: %v", err)
	}

	wantKey := "t/" + tenantID.String() + "/highlight/" + h.ID.String() + "/image"
	if !blobs.has(wantKey) {
		t.Fatalf("image blob not stored at %q; have %v", wantKey, blobs.data)
	}
	if store.lastImgKey != wantKey || store.lastImgCT != "image/png" || store.lastImgSz != int64(len("PNGDATA")) {
		t.Fatalf("row image not set: %q/%q/%d", store.lastImgKey, store.lastImgCT, store.lastImgSz)
	}
	// Prompt uses excerpt + reason, never speaker ids.
	if !strings.Contains(gen.gotArgs, "natural 20") || !strings.Contains(gen.gotArgs, "clutch critical hit") {
		t.Errorf("prompt missing caption material: %q", gen.gotArgs)
	}
	// Metered as Gemini LLM tokens.
	if !rec.seen || rec.provider != observe.ProviderGemini || rec.in != 40 || rec.out != 1290 {
		t.Errorf("metering wrong: seen=%v provider=%q in=%d out=%d", rec.seen, rec.provider, rec.in, rec.out)
	}
}

// TestEnrichImageHandler_Claim_GeneratesAtMostOnce pins AC2 (#406): two enrich
// jobs racing for the SAME Highlight run the provider Generate at most once — the
// conditional claim lets exactly one win; the loser never spends.
func TestEnrichImageHandler_Claim_GeneratesAtMostOnce(t *testing.T) {
	tenantID := uuid.New()
	store := newFakeEnrichStore()
	h := seedRow(store, tenantID)
	blobs := newFakeBlobs()
	gen := &fakeGen{res: imagegen.Result{Data: []byte("PNGDATA"), ContentType: "image/png", OutputTokens: 1290}}

	handler := EnrichImageHandler(store, blobs, factoryReturning(gen, "m", nil), nil, nil)
	payload, _ := MarshalEnrichImage(h.ID, tenantID)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = handler(context.Background(), payload)
		}(i)
	}
	wg.Wait()

	if gen.calls != 1 {
		t.Fatalf("Generate ran %d times; the claim must pin it to exactly 1", gen.calls)
	}
	// Exactly one image blob was stored (the winner's), and the row is enriched.
	if blobs.keys() != 1 {
		t.Fatalf("want exactly one image blob stored, got %d", blobs.keys())
	}
	got, _ := store.GetHighlight(context.Background(), tenantID, h.ID)
	if got.ImageKey == "" {
		t.Fatal("winner did not land the image on the row")
	}
}

// --- boot reconciliation sweep (#406) ---

// fakeReconcileStore feeds the boot enrichment reconciliation sweep: the promoted
// imageless targets to re-enqueue, and the orphan image blob keys to collect.
type fakeReconcileStore struct {
	targets   []storage.HighlightEnrichTarget
	gotKind   string
	orphans   []string
	targetErr error
	orphanErr error
}

func (f *fakeReconcileStore) ListPromotedHighlightsNeedingEnrichment(_ context.Context, enrichKind string) ([]storage.HighlightEnrichTarget, error) {
	f.gotKind = enrichKind
	if f.targetErr != nil {
		return nil, f.targetErr
	}
	return f.targets, nil
}

func (f *fakeReconcileStore) ListOrphanHighlightImageKeys(_ context.Context) ([]string, error) {
	if f.orphanErr != nil {
		return nil, f.orphanErr
	}
	return f.orphans, nil
}

// enrichEnqueued records one backstop enrichment enqueue.
type enrichEnqueued struct {
	kind    string
	payload enrichPayload
}

type enrichRecordingEnqueuer struct {
	mu  sync.Mutex
	all []enrichEnqueued
}

func (r *enrichRecordingEnqueuer) Enqueue(_ context.Context, kind string, payload any, _ time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	e := enrichEnqueued{kind: kind}
	if p, ok := payload.(enrichPayload); ok {
		e.payload = p
	}
	r.all = append(r.all, e)
	return nil
}

// TestSweepEnrichmentReconciliation_EnqueuesForImagelessPromoted pins AC1 (#406):
// the boot sweep enqueues image enrichment for every promoted Highlight left
// imageless (the crash-between-promote-and-enqueue backstop).
func TestSweepEnrichmentReconciliation_EnqueuesForImagelessPromoted(t *testing.T) {
	t1, t2 := uuid.New(), uuid.New()
	h1, h2 := uuid.New(), uuid.New()
	store := &fakeReconcileStore{targets: []storage.HighlightEnrichTarget{
		{HighlightID: h1, TenantID: t1},
		{HighlightID: h2, TenantID: t2},
	}}
	enq := &enrichRecordingEnqueuer{}

	if err := SweepEnrichmentReconciliation(context.Background(), store, newFakeBlobs(), enq, testLog()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if store.gotKind != JobKindEnrichImage {
		t.Fatalf("sweep asked with wrong kind: %q", store.gotKind)
	}
	if len(enq.all) != 2 {
		t.Fatalf("want 2 backstop enrichments enqueued, got %d", len(enq.all))
	}
	for _, e := range enq.all {
		if e.kind != JobKindEnrichImage {
			t.Fatalf("enqueued wrong kind: %q", e.kind)
		}
	}
	if enq.all[0].payload.HighlightID != h1 || enq.all[0].payload.TenantID != t1 {
		t.Fatalf("first enqueue payload wrong: %+v", enq.all[0].payload)
	}
	if enq.all[1].payload.HighlightID != h2 || enq.all[1].payload.TenantID != t2 {
		t.Fatalf("second enqueue payload wrong: %+v", enq.all[1].payload)
	}
}

// TestSweepEnrichmentReconciliation_CollectsOrphanImageBlobs pins AC3 (#406): the
// boot sweep drops image blobs whose Highlight row is gone (the delete-vs-enrich
// interleaving orphan) through the seam.
func TestSweepEnrichmentReconciliation_CollectsOrphanImageBlobs(t *testing.T) {
	blobs := newFakeBlobs()
	k1 := "t/" + uuid.New().String() + "/highlight/" + uuid.New().String() + "/image"
	k2 := "t/" + uuid.New().String() + "/highlight/" + uuid.New().String() + "/image"
	blobs.data[k1] = []byte("a")
	blobs.data[k2] = []byte("b")
	store := &fakeReconcileStore{orphans: []string{k1, k2}}

	if err := SweepEnrichmentReconciliation(context.Background(), store, blobs, &enrichRecordingEnqueuer{}, testLog()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if blobs.keys() != 0 {
		t.Fatalf("orphan image blobs not swept: %d left", blobs.keys())
	}
}

// TestSweepEnrichmentReconciliation_NoWorkNoSideEffects: an empty catalog enqueues
// and deletes nothing.
func TestSweepEnrichmentReconciliation_NoWorkNoSideEffects(t *testing.T) {
	blobs := newFakeBlobs()
	blobs.data["t/x/highlight/y/image"] = []byte("keep") // not reported as orphan
	enq := &enrichRecordingEnqueuer{}
	store := &fakeReconcileStore{}
	if err := SweepEnrichmentReconciliation(context.Background(), store, blobs, enq, testLog()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(enq.all) != 0 {
		t.Fatalf("no targets should enqueue nothing, got %v", enq.all)
	}
	if blobs.keys() != 1 {
		t.Fatalf("no orphans should delete nothing, got %d left", blobs.keys())
	}
}

// TestSweepEnrichmentReconciliation_ListErrorNonFatal pins AC4 (#406): a store
// list failure is reported (so boot logs it) but never aborts — the OTHER half of
// the sweep still runs. Here the target list fails yet the orphan sweep proceeds.
func TestSweepEnrichmentReconciliation_ListErrorNonFatal(t *testing.T) {
	blobs := newFakeBlobs()
	orphan := "t/" + uuid.New().String() + "/highlight/" + uuid.New().String() + "/image"
	blobs.data[orphan] = []byte("a")
	store := &fakeReconcileStore{
		targetErr: errors.New("db down"),
		orphans:   []string{orphan},
	}
	err := SweepEnrichmentReconciliation(context.Background(), store, blobs, &enrichRecordingEnqueuer{}, testLog())
	if err == nil {
		t.Fatal("want an error surfaced when a list fails (boot logs it non-fatally)")
	}
	// The orphan sweep still ran despite the target-list failure.
	if blobs.keys() != 0 {
		t.Fatalf("orphan sweep should still run when the target list fails; %d blobs left", blobs.keys())
	}
}

func TestEnrichImageHandler_Idempotent_AlreadyEnriched(t *testing.T) {
	tenantID := uuid.New()
	store := newFakeEnrichStore()
	h := seedRow(store, tenantID)
	h.ImageKey = "t/" + tenantID.String() + "/highlight/" + h.ID.String() + "/image"
	store.rows[h.ID] = h
	blobs := newFakeBlobs()
	gen := &fakeGen{res: imagegen.Result{Data: []byte("X"), ContentType: "image/png"}}

	handler := EnrichImageHandler(store, blobs, factoryReturning(gen, "m", nil), nil, nil)
	payload, _ := MarshalEnrichImage(h.ID, tenantID)
	if err := handler(context.Background(), payload); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if gen.calls != 0 {
		t.Fatalf("generator called %d times on an already-enriched highlight; want 0", gen.calls)
	}
}

func TestEnrichImageHandler_RowGone_Nil(t *testing.T) {
	store := newFakeEnrichStore()
	gen := &fakeGen{}
	handler := EnrichImageHandler(store, newFakeBlobs(), factoryReturning(gen, "m", nil), nil, nil)
	payload, _ := MarshalEnrichImage(uuid.New(), uuid.New())
	if err := handler(context.Background(), payload); err != nil {
		t.Fatalf("deleted highlight should be a clean nil, got %v", err)
	}
	if gen.calls != 0 {
		t.Fatalf("generator called for a missing row")
	}
}

func TestEnrichImageHandler_NotConfigured_Nil(t *testing.T) {
	tenantID := uuid.New()
	store := newFakeEnrichStore()
	h := seedRow(store, tenantID)
	blobs := newFakeBlobs()

	handler := EnrichImageHandler(store, blobs, factoryReturning(nil, "", ErrImageNotConfigured), nil, nil)
	payload, _ := MarshalEnrichImage(h.ID, tenantID)
	if err := handler(context.Background(), payload); err != nil {
		t.Fatalf("unconfigured provider should be a clean nil, got %v", err)
	}
	// Highlight intact, no image.
	got, _ := store.GetHighlight(context.Background(), tenantID, h.ID)
	if got.ImageKey != "" {
		t.Fatalf("row should stay imageless when not configured, got %q", got.ImageKey)
	}
}

func TestEnrichImageHandler_GeneratorError_ReturnsErr_RowUntouched(t *testing.T) {
	tenantID := uuid.New()
	store := newFakeEnrichStore()
	h := seedRow(store, tenantID)
	blobs := newFakeBlobs()
	gen := &fakeGen{err: errors.New("provider 503")}

	handler := EnrichImageHandler(store, blobs, factoryReturning(gen, "m", nil), nil, nil)
	payload, _ := MarshalEnrichImage(h.ID, tenantID)
	if err := handler(context.Background(), payload); err == nil {
		t.Fatal("generator error must return an error so the runner retries")
	}
	// Nothing stored, row untouched (AC).
	if len(blobs.data) != 0 {
		t.Fatalf("no blob should be stored on generator failure")
	}
	got, _ := store.GetHighlight(context.Background(), tenantID, h.ID)
	if got.ImageKey != "" {
		t.Fatalf("row must be untouched on generator failure")
	}
}

func TestEnrichImageHandler_Compensation_RowDeletedDuringWrite(t *testing.T) {
	tenantID := uuid.New()
	store := newFakeEnrichStore()
	h := seedRow(store, tenantID)
	// SetHighlightImage reports the row vanished after generation.
	store.setErr = storage.ErrNotFound
	blobs := newFakeBlobs()
	gen := &fakeGen{res: imagegen.Result{Data: []byte("PNG"), ContentType: "image/png", OutputTokens: 1290}}

	handler := EnrichImageHandler(store, blobs, factoryReturning(gen, "m", nil), nil, nil)
	payload, _ := MarshalEnrichImage(h.ID, tenantID)
	if err := handler(context.Background(), payload); err != nil {
		t.Fatalf("a row deleted mid-write should be a clean nil, got %v", err)
	}
	wantKey := "t/" + tenantID.String() + "/highlight/" + h.ID.String() + "/image"
	// The just-stored orphan blob was compensated (deleted).
	if blobs.has(wantKey) {
		t.Fatalf("orphan image blob was not compensated")
	}
	if len(blobs.deleted) != 1 || blobs.deleted[0] != wantKey {
		t.Fatalf("expected exactly the orphan key deleted, got %v", blobs.deleted)
	}
}
