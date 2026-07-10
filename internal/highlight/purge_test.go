package highlight

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/google/uuid"
)

// fakePurgeStore records candidate keys and delete calls; order of blob-vs-row is
// asserted via a shared event log.
type fakePurgeStore struct {
	mu        sync.Mutex
	keys      []string
	deleted   bool
	deleteN   int
	events    *[]string
	listErr   error
	deleteErr error
}

func (f *fakePurgeStore) ListSessionCandidateClipKeys(_ context.Context, _ uuid.UUID) ([]string, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deleted {
		return nil, nil // already purged: idempotent re-run sees nothing
	}
	return append([]string(nil), f.keys...), nil
}

func (f *fakePurgeStore) DeleteSessionCandidates(_ context.Context, _ uuid.UUID) (int, error) {
	if f.deleteErr != nil {
		return 0, f.deleteErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	*f.events = append(*f.events, "row-delete")
	if f.deleted {
		return 0, nil
	}
	f.deleted = true
	n := len(f.keys)
	f.deleteN = n
	return n, nil
}

func testLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func payloadFor(id uuid.UUID) json.RawMessage {
	b, _ := json.Marshal(purgePayload{VoiceSessionID: id})
	return b
}

func TestPurge_BlobFirstThenRows(t *testing.T) {
	var events []string
	store := &fakePurgeStore{keys: []string{"k1", "k2"}, events: &events}
	blobs := newFakeBlobs()
	blobs.data["k1"] = []byte("a")
	blobs.data["k2"] = []byte("b")
	// Record blob deletes into the same event log by wrapping.
	del := recordingBlobs{fakeBlobs: blobs, events: &events}

	h := PurgeHandler(store, &del, testLog())
	if err := h(context.Background(), payloadFor(uuid.New())); err != nil {
		t.Fatalf("purge: %v", err)
	}

	// Every blob delete happens BEFORE the single row delete.
	if len(events) != 3 || events[2] != "row-delete" {
		t.Fatalf("blob-first ordering violated: %v", events)
	}
	if !store.deleted || store.deleteN != 2 {
		t.Fatalf("rows not deleted: %+v", store)
	}
	if blobs.keys() != 0 {
		t.Fatalf("clips not dropped: %d left", blobs.keys())
	}
}

func TestPurge_Idempotent(t *testing.T) {
	var events []string
	store := &fakePurgeStore{keys: []string{"k1"}, events: &events}
	blobs := newFakeBlobs()
	blobs.data["k1"] = []byte("a")
	del := recordingBlobs{fakeBlobs: blobs, events: &events}
	h := PurgeHandler(store, &del, testLog())

	id := uuid.New()
	if err := h(context.Background(), payloadFor(id)); err != nil {
		t.Fatalf("first purge: %v", err)
	}
	// Second run: nothing left to do, still succeeds.
	if err := h(context.Background(), payloadFor(id)); err != nil {
		t.Fatalf("second purge (idempotent): %v", err)
	}
}

func TestPurge_BadPayload(t *testing.T) {
	store := &fakePurgeStore{events: &[]string{}}
	h := PurgeHandler(store, newFakeBlobs(), testLog())
	if err := h(context.Background(), json.RawMessage("not json")); err == nil {
		t.Fatal("want error on bad payload")
	}
}

func TestPurge_ListErrorPropagates(t *testing.T) {
	store := &fakePurgeStore{listErr: errors.New("db down"), events: &[]string{}}
	h := PurgeHandler(store, newFakeBlobs(), testLog())
	if err := h(context.Background(), payloadFor(uuid.New())); err == nil {
		t.Fatal("want error when list fails")
	}
}

// recordingBlobs wraps fakeBlobs to log each Delete into the shared event slice.
type recordingBlobs struct {
	*fakeBlobs
	events *[]string
}

func (r *recordingBlobs) Delete(ctx context.Context, key string) error {
	*r.events = append(*r.events, "blob-delete")
	return r.fakeBlobs.Delete(ctx, key)
}
