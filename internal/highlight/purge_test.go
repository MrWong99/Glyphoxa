package highlight

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
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

// fakeScheduleStore returns the sessions the boot sweep should schedule a purge
// for, recording the purge kind the sweep asked with.
type fakeScheduleStore struct {
	candidates []storage.SessionPurgeCandidate
	gotKind    string
	listErr    error
}

func (f *fakeScheduleStore) ListSessionsNeedingCandidatePurge(_ context.Context, purgeKind string) ([]storage.SessionPurgeCandidate, error) {
	f.gotKind = purgeKind
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.candidates, nil
}

// enqueued records one backstop purge enqueue (session + scheduled horizon).
type enqueued struct {
	session  uuid.UUID
	kind     string
	runAfter time.Time
}

// recordingEnqueuer records every purge enqueue the boot sweep makes.
type recordingEnqueuer struct {
	mu  sync.Mutex
	all []enqueued
}

func (r *recordingEnqueuer) Enqueue(_ context.Context, kind string, payload any, runAfter time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	e := enqueued{kind: kind, runAfter: runAfter}
	if p, ok := payload.(purgePayload); ok {
		e.session = p.VoiceSessionID
	}
	r.all = append(r.all, e)
	return nil
}

func TestSweepMissingCandidatePurges_EnqueuesForOrphans(t *testing.T) {
	s1, s2 := uuid.New(), uuid.New()
	store := &fakeScheduleStore{candidates: []storage.SessionPurgeCandidate{
		{VoiceSessionID: s1, EndedAt: time.Now()},
		{VoiceSessionID: s2, EndedAt: time.Now()},
	}}
	enq := &recordingEnqueuer{}

	if err := SweepMissingCandidatePurges(context.Background(), store, enq, testLog()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if store.gotKind != JobKindPurgeCandidates {
		t.Fatalf("sweep asked with wrong kind: %q", store.gotKind)
	}
	if len(enq.all) != 2 || enq.all[0].session != s1 || enq.all[1].session != s2 {
		t.Fatalf("backstop purges not enqueued per orphan: %v", enq.all)
	}
	for _, e := range enq.all {
		if e.kind != JobKindPurgeCandidates {
			t.Fatalf("enqueued wrong job kind: %q", e.kind)
		}
	}
}

// TestSweepMissingCandidatePurges_AnchorsHorizonAtSessionEnd pins ADR-0051's 7-day
// safety window to session END, not boot time: a session that ended 8 days ago is
// past-due (run_after ≈ now), and one that ended a day ago keeps its remaining
// window (run_after ≈ ended_at + 7d).
func TestSweepMissingCandidatePurges_AnchorsHorizonAtSessionEnd(t *testing.T) {
	pastDue := uuid.New()
	recent := uuid.New()
	pastEnded := time.Now().Add(-8 * 24 * time.Hour) // ended > 7d ago → past-due
	recentEnded := time.Now().Add(-24 * time.Hour)   // ended 1d ago → ~6d left
	store := &fakeScheduleStore{candidates: []storage.SessionPurgeCandidate{
		{VoiceSessionID: pastDue, EndedAt: pastEnded},
		{VoiceSessionID: recent, EndedAt: recentEnded},
	}}
	enq := &recordingEnqueuer{}

	before := time.Now()
	if err := SweepMissingCandidatePurges(context.Background(), store, enq, testLog()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	after := time.Now()

	byID := map[uuid.UUID]time.Time{}
	for _, e := range enq.all {
		byID[e.session] = e.runAfter
	}
	// Past-due: floored at now (runs immediately), NOT ended_at+7d (which would be ~1d ago).
	if ra := byID[pastDue]; ra.Before(before) || ra.After(after) {
		t.Fatalf("past-due horizon = %v, want ~now [%v,%v]", ra, before, after)
	}
	// Recent: anchored at ended_at + 7d, well in the future — NOT now+7d.
	wantRecent := recentEnded.Add(purgeDelay)
	if ra := byID[recent]; ra.Sub(wantRecent).Abs() > time.Second {
		t.Fatalf("recent horizon = %v, want ended_at+7d ≈ %v", ra, wantRecent)
	}
}

func TestSweepMissingCandidatePurges_NoOrphansNoEnqueue(t *testing.T) {
	store := &fakeScheduleStore{}
	enq := &recordingEnqueuer{}
	if err := SweepMissingCandidatePurges(context.Background(), store, enq, testLog()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(enq.all) != 0 {
		t.Fatalf("no orphans should enqueue nothing, got %v", enq.all)
	}
}

func TestSweepMissingCandidatePurges_ListErrorReturned(t *testing.T) {
	store := &fakeScheduleStore{listErr: errors.New("db down")}
	if err := SweepMissingCandidatePurges(context.Background(), store, &recordingEnqueuer{}, testLog()); err == nil {
		t.Fatal("want error when the session list fails")
	}
}

func TestPurge_RowDeleteErrorPropagates(t *testing.T) {
	store := &fakePurgeStore{keys: []string{"k1"}, events: &[]string{}, deleteErr: errors.New("row delete boom")}
	blobs := newFakeBlobs()
	blobs.data["k1"] = []byte("a")
	h := PurgeHandler(store, blobs, testLog())
	if err := h(context.Background(), payloadFor(uuid.New())); err == nil {
		t.Fatal("want error when the row delete fails")
	}
}

func TestPurge_BlobDeleteErrorPropagates(t *testing.T) {
	store := &fakePurgeStore{keys: []string{"k1"}, events: &[]string{}}
	blobs := newFakeBlobs()
	blobs.data["k1"] = []byte("a")
	blobs.deleteErr = errors.New("blob backend down")
	h := PurgeHandler(store, blobs, testLog())
	if err := h(context.Background(), payloadFor(uuid.New())); err == nil {
		t.Fatal("want error when the blob delete fails")
	}
	// Blob-first: the row delete must NOT have run after the blob delete failed.
	if store.deleted {
		t.Fatal("rows deleted despite a failed blob delete (blob-first violated)")
	}
}

func TestCampaignSweep_DeletesCarriedKeys(t *testing.T) {
	blobs := newFakeBlobs()
	blobs.data["k1"] = []byte("a")
	blobs.data["k2"] = []byte("b")
	payload, err := MarshalCampaignSweep([]string{"k1", "k2"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	h := CampaignSweepHandler(blobs, testLog())
	if err := h(context.Background(), payload); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if blobs.keys() != 0 {
		t.Fatalf("carried clips not swept: %d left", blobs.keys())
	}
}

func TestCampaignSweep_IdempotentOnMissingKeys(t *testing.T) {
	blobs := newFakeBlobs() // no data: every key is already absent
	payload, _ := MarshalCampaignSweep([]string{"gone-1", "gone-2"})
	h := CampaignSweepHandler(blobs, testLog())
	// Absent keys delete cleanly (blob.Delete absent-key = nil), so a re-run of a
	// partially-or-fully completed sweep succeeds.
	if err := h(context.Background(), payload); err != nil {
		t.Fatalf("sweep over missing keys should be a no-op, got %v", err)
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
