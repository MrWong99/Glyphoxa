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
	f.claimCalls++ // count EVERY attempt (win or lose), not just winners
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
	return true, nil
}

// ReleaseHighlightEnrichClaim mimics a real DB call: a cancelled ctx fails the
// statement and the claim is NOT cleared. The handler must therefore release with
// a cancel-immune ctx (context.WithoutCancel) so an error-path release under a
// dead handler ctx still frees the claim.
func (f *fakeEnrichStore) ReleaseHighlightEnrichClaim(ctx context.Context, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releaseCalls++
	if err := ctx.Err(); err != nil {
		return err // a cancelled/expired ctx aborts the release; the claim lingers
	}
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
	// Both jobs attempted the claim; the winner landed the image, so NO release
	// happened (release is only for non-image exits), and the loser bailed before
	// owning the claim.
	if store.claimCalls != 2 {
		t.Fatalf("want 2 claim attempts (one per job), got %d", store.claimCalls)
	}
	if store.releaseCalls != 0 {
		t.Fatalf("a winning enrichment must not release its claim, got %d releases", store.releaseCalls)
	}
}

// TestEnrichImageHandler_ReleasesClaimUnderCancelledCtx pins the finding-2 fix
// (#421): on an error-path exit the handler releases its claim so a fast retry can
// re-claim without waiting out the TTL — and that release must succeed even when
// the handler ctx is already cancelled (lease-timeout or shutdown). The release
// therefore runs on a cancel-immune ctx.
func TestEnrichImageHandler_ReleasesClaimUnderCancelledCtx(t *testing.T) {
	tenantID := uuid.New()
	store := newFakeEnrichStore()
	h := seedRow(store, tenantID)
	blobs := newFakeBlobs()
	// A provider error is a release-then-return exit path.
	gen := &fakeGen{err: errors.New("provider 503")}

	handler := EnrichImageHandler(store, blobs, factoryReturning(gen, "m", nil), nil, nil)
	payload, _ := MarshalEnrichImage(h.ID, tenantID)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the handler runs with an already-dead ctx

	if err := handler(ctx, payload); err == nil {
		t.Fatal("provider error must surface so the runner retries")
	}
	// The claim was released despite the cancelled ctx: a real retry can re-claim
	// immediately instead of burning attempts against a lingering claim.
	if store.releaseCalls != 1 {
		t.Fatalf("want exactly one release attempt, got %d", store.releaseCalls)
	}
	store.mu.Lock()
	_, stillClaimed := store.claimed[h.ID]
	store.mu.Unlock()
	if stillClaimed {
		t.Fatal("claim lingered: release ran with the dead handler ctx instead of a cancel-immune one")
	}
}

// --- boot reconciliation sweep (#406) ---

// fakeReconcileStore feeds the boot enrichment reconciliation sweep: the promoted
// imageless targets to re-enqueue, and the set of Highlight ids that still have a
// row (the orphan-image anti-join's membership half).
type fakeReconcileStore struct {
	targets   []storage.HighlightEnrichTarget
	gotKind   string
	live      map[uuid.UUID]bool // ids with a surviving row
	targetErr error
	existErr  error
	gotExist  []uuid.UUID // ids the sweep asked HighlightsExist about
}

func (f *fakeReconcileStore) ListPromotedHighlightsNeedingEnrichment(_ context.Context, enrichKind string) ([]storage.HighlightEnrichTarget, error) {
	f.gotKind = enrichKind
	if f.targetErr != nil {
		return nil, f.targetErr
	}
	return f.targets, nil
}

func (f *fakeReconcileStore) HighlightsExist(_ context.Context, ids []uuid.UUID) (map[uuid.UUID]bool, error) {
	f.gotExist = ids
	if f.existErr != nil {
		return nil, f.existErr
	}
	present := map[uuid.UUID]bool{}
	for _, id := range ids {
		if f.live[id] {
			present[id] = true
		}
	}
	return present, nil
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

// imageKey builds a Highlight image blob key for the given tenant/highlight.
func imageKey(tenantID, highlightID uuid.UUID) string {
	return "t/" + tenantID.String() + "/highlight/" + highlightID.String() + "/image"
}

// TestSweepEnrichmentReconciliation_CollectsOrphanImageBlobs pins AC3 (#406/#421):
// the boot sweep enumerates blobs THROUGH the seam (blob.Store.List), anti-joins
// their embedded Highlight ids against live rows in Go, and drops ONLY the images
// whose row is gone — the delete-vs-enrich interleaving orphans. A live row's
// image, its audio clip (same owner-kind, different name), and another owner's
// blob are all left untouched.
func TestSweepEnrichmentReconciliation_CollectsOrphanImageBlobs(t *testing.T) {
	tenantID := uuid.New()
	liveID, goneID := uuid.New(), uuid.New()

	blobs := newFakeBlobs()
	liveImg := imageKey(tenantID, liveID)
	liveClip := "t/" + tenantID.String() + "/highlight/" + liveID.String() + "/clip.wav"
	orphanImg := imageKey(tenantID, goneID)
	otherOwner := "t/" + tenantID.String() + "/campaign/" + uuid.New().String() + "/image"
	blobs.data[liveImg] = []byte("live")
	blobs.data[liveClip] = []byte("clip")
	blobs.data[orphanImg] = []byte("orphan")
	blobs.data[otherOwner] = []byte("other")

	// Only liveID still has a row.
	store := &fakeReconcileStore{live: map[uuid.UUID]bool{liveID: true}}

	if err := SweepEnrichmentReconciliation(context.Background(), store, blobs, &enrichRecordingEnqueuer{}, testLog()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if blobs.has(orphanImg) {
		t.Fatal("orphan image blob (row gone) was not swept")
	}
	if !blobs.has(liveImg) || !blobs.has(liveClip) || !blobs.has(otherOwner) {
		t.Fatalf("sweep touched a non-orphan blob: live=%v clip=%v other=%v",
			blobs.has(liveImg), blobs.has(liveClip), blobs.has(otherOwner))
	}
	// The anti-join only asked about the highlight IMAGE keys, never the clip or
	// the other owner's blob.
	if len(store.gotExist) != 2 {
		t.Fatalf("want membership checked for the 2 highlight image ids, got %v", store.gotExist)
	}
}

// TestSweepEnrichmentReconciliation_NoWorkNoSideEffects: an empty catalog enqueues
// and deletes nothing — a live row's image is kept.
func TestSweepEnrichmentReconciliation_NoWorkNoSideEffects(t *testing.T) {
	tenantID, liveID := uuid.New(), uuid.New()
	blobs := newFakeBlobs()
	liveImg := imageKey(tenantID, liveID)
	blobs.data[liveImg] = []byte("keep") // a live row's image, never an orphan
	enq := &enrichRecordingEnqueuer{}
	store := &fakeReconcileStore{live: map[uuid.UUID]bool{liveID: true}}
	if err := SweepEnrichmentReconciliation(context.Background(), store, blobs, enq, testLog()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(enq.all) != 0 {
		t.Fatalf("no targets should enqueue nothing, got %v", enq.all)
	}
	if !blobs.has(liveImg) {
		t.Fatal("a live row's image must not be swept")
	}
}

// TestSweepEnrichmentReconciliation_ListErrorNonFatal pins AC4 (#406): a list
// failure is reported (so boot logs it) but never aborts — the OTHER half of the
// sweep still runs. Here the (a) target list fails yet the (b) orphan sweep
// proceeds and drops the orphan.
func TestSweepEnrichmentReconciliation_ListErrorNonFatal(t *testing.T) {
	tenantID, goneID := uuid.New(), uuid.New()
	blobs := newFakeBlobs()
	orphan := imageKey(tenantID, goneID)
	blobs.data[orphan] = []byte("a")
	store := &fakeReconcileStore{
		targetErr: errors.New("db down"),
		live:      map[uuid.UUID]bool{}, // goneID has no row → orphan
	}
	err := SweepEnrichmentReconciliation(context.Background(), store, blobs, &enrichRecordingEnqueuer{}, testLog())
	if err == nil {
		t.Fatal("want an error surfaced when a list fails (boot logs it non-fatally)")
	}
	// The orphan sweep still ran despite the target-list failure.
	if blobs.has(orphan) {
		t.Fatal("orphan sweep should still run when the target list fails")
	}
}

// TestSweepEnrichmentReconciliation_BlobListErrorNonFatal: the (b) blob-seam List
// failing is reported but does not abort the (a) enqueue half.
func TestSweepEnrichmentReconciliation_BlobListErrorNonFatal(t *testing.T) {
	t1, h1 := uuid.New(), uuid.New()
	blobs := newFakeBlobs()
	blobs.listErr = errors.New("seam list down")
	enq := &enrichRecordingEnqueuer{}
	store := &fakeReconcileStore{targets: []storage.HighlightEnrichTarget{{HighlightID: h1, TenantID: t1}}}

	err := SweepEnrichmentReconciliation(context.Background(), store, blobs, enq, testLog())
	if err == nil {
		t.Fatal("want an error surfaced when the blob-seam List fails")
	}
	// The (a) enqueue half still ran despite the (b) list failure.
	if len(enq.all) != 1 {
		t.Fatalf("target enqueue should still run when the blob list fails, got %d", len(enq.all))
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
