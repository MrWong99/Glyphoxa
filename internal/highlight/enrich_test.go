package highlight

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

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
