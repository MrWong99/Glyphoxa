package rpc_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/highlight"
	"github.com/MrWong99/Glyphoxa/internal/rpc"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/voice/soundgen"
)

// stubSoundGen satisfies soundgen.Generator for the factory probe; the RPC
// never calls it (generation is the job's business).
type stubSoundGen struct{}

func (stubSoundGen) GenerateSting(context.Context, soundgen.Request) (soundgen.Result, error) {
	return soundgen.Result{}, nil
}
func (stubSoundGen) ComposeMusic(context.Context, soundgen.Request) (soundgen.Result, error) {
	return soundgen.Result{}, nil
}

// newSoundClient is newHighlightClientEnq plus the sound-enrichment seam
// (#312): the factory backs SetHighlightSound's configured precheck.
func newSoundClient(t *testing.T, tenantID uuid.UUID, hstore rpc.HighlightStore, blobs *fakeRPCBlobs, sstore *fakeSessionStore, enqueue rpc.HighlightEnqueuer, factory highlight.SoundGeneratorFactory) managementv1connect.SessionServiceClient {
	t.Helper()
	if sstore == nil {
		sstore = activeStore()
	}
	inject := connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			ctx = auth.WithTenant(ctx, tenantID)
			return next(ctx, req)
		}
	})
	srv := rpc.NewSessionServer(&fakeSessionManager{}, sstore, nil, nil)
	srv.SetHighlights(hstore, blobs, enqueue)
	if factory != nil {
		srv.SetSoundEnrichment(factory)
	}
	mux := http.NewServeMux()
	mux.Handle(srv.Handler(connect.WithInterceptors(inject)))
	httpSrv := httptest.NewServer(mux)
	t.Cleanup(httpSrv.Close)
	return managementv1connect.NewSessionServiceClient(http.DefaultClient, httpSrv.URL, connect.WithProtoJSON())
}

func configuredSoundFactory(context.Context, uuid.UUID) (soundgen.Generator, error) {
	return stubSoundGen{}, nil
}

func unconfiguredSoundFactory(context.Context, uuid.UUID) (soundgen.Generator, error) {
	return nil, highlight.ErrSoundNotConfigured
}

// TestRPCSetHighlightSound_RequestsAndEnqueues is the happy path: the choice is
// recorded (kind + requested-at on the wire, sound triad cleared) and exactly
// one JobKindEnrichSound is enqueued.
func TestRPCSetHighlightSound_RequestsAndEnqueues(t *testing.T) {
	tenantID, campaignID := uuid.New(), uuid.New()
	store := newFakeHighlightStore(tenantID)
	h := seedRPCHighlight(store, tenantID, uuid.New(), campaignID, storage.HighlightPromoted)
	enq := &fakeHighlightEnqueuer{}

	client := newSoundClient(t, tenantID, store, &fakeRPCBlobs{}, campaignSessionStore(campaignID), enq, configuredSoundFactory)
	res, err := client.SetHighlightSound(context.Background(),
		connect.NewRequest(&managementv1.SetHighlightSoundRequest{Id: h.ID.String(), Kind: storage.SoundKindSting}))
	if err != nil {
		t.Fatalf("SetHighlightSound: %v", err)
	}
	got := res.Msg.GetHighlight()
	if got.GetSoundKind() != storage.SoundKindSting {
		t.Errorf("sound kind = %q, want sting", got.GetSoundKind())
	}
	if got.GetSoundRequestedAt() == nil {
		t.Errorf("sound_requested_at unset, want stamped")
	}
	if got.GetSoundContentType() != "" || got.GetSoundSizeBytes() != 0 {
		t.Errorf("sound triad = (%q, %d), want cleared until the job lands",
			got.GetSoundContentType(), got.GetSoundSizeBytes())
	}
	if enq.calls != 1 || enq.kind != highlight.JobKindEnrichSound {
		t.Errorf("enqueue calls=%d kind=%q, want 1 %q", enq.calls, enq.kind, highlight.JobKindEnrichSound)
	}
}

// TestRPCSetHighlightSound_RemoveDeletesBlob pins kind "": a landed sound's
// blob is dropped through the seam (blob-then-row) and no job is enqueued.
func TestRPCSetHighlightSound_RemoveDeletesBlob(t *testing.T) {
	tenantID, campaignID := uuid.New(), uuid.New()
	store := newFakeHighlightStore(tenantID)
	h := seedRPCHighlight(store, tenantID, uuid.New(), campaignID, storage.HighlightPromoted)
	h.SoundKind = storage.SoundKindSting
	h.SoundKey = "t/" + tenantID.String() + "/highlight/" + h.ID.String() + "/sound"
	h.SoundContentType = "audio/mpeg"
	store.put(h)
	blobs := &fakeRPCBlobs{}
	enq := &fakeHighlightEnqueuer{}

	client := newSoundClient(t, tenantID, store, blobs, campaignSessionStore(campaignID), enq, configuredSoundFactory)
	res, err := client.SetHighlightSound(context.Background(),
		connect.NewRequest(&managementv1.SetHighlightSoundRequest{Id: h.ID.String(), Kind: ""}))
	if err != nil {
		t.Fatalf("SetHighlightSound: %v", err)
	}
	if got := res.Msg.GetHighlight(); got.GetSoundKind() != "" || got.GetSoundRequestedAt() != nil {
		t.Errorf("remove left (kind=%q, requestedAt=%v), want cleared", got.GetSoundKind(), got.GetSoundRequestedAt())
	}
	if len(blobs.deleted) != 1 || blobs.deleted[0] != h.SoundKey {
		t.Errorf("deleted blobs = %v, want [%s]", blobs.deleted, h.SoundKey)
	}
	if enq.calls != 0 {
		t.Errorf("enqueue calls = %d, want 0 for a removal", enq.calls)
	}
}

// TestRPCSetHighlightSound_Preconditions pins the refusal ladder: a candidate
// row, an unconfigured tenant, an unknown kind, and an unwired seam.
func TestRPCSetHighlightSound_Preconditions(t *testing.T) {
	tenantID, campaignID := uuid.New(), uuid.New()

	t.Run("candidate is FailedPrecondition", func(t *testing.T) {
		store := newFakeHighlightStore(tenantID)
		h := seedRPCHighlight(store, tenantID, uuid.New(), campaignID, storage.HighlightCandidate)
		client := newSoundClient(t, tenantID, store, &fakeRPCBlobs{}, campaignSessionStore(campaignID), &fakeHighlightEnqueuer{}, configuredSoundFactory)
		_, err := client.SetHighlightSound(context.Background(),
			connect.NewRequest(&managementv1.SetHighlightSoundRequest{Id: h.ID.String(), Kind: storage.SoundKindSting}))
		if connect.CodeOf(err) != connect.CodeFailedPrecondition {
			t.Fatalf("want CodeFailedPrecondition, got %v", err)
		}
	})

	t.Run("unconfigured is FailedPrecondition before any change", func(t *testing.T) {
		store := newFakeHighlightStore(tenantID)
		h := seedRPCHighlight(store, tenantID, uuid.New(), campaignID, storage.HighlightPromoted)
		enq := &fakeHighlightEnqueuer{}
		client := newSoundClient(t, tenantID, store, &fakeRPCBlobs{}, campaignSessionStore(campaignID), enq, unconfiguredSoundFactory)
		_, err := client.SetHighlightSound(context.Background(),
			connect.NewRequest(&managementv1.SetHighlightSoundRequest{Id: h.ID.String(), Kind: storage.SoundKindMusic}))
		if connect.CodeOf(err) != connect.CodeFailedPrecondition {
			t.Fatalf("want CodeFailedPrecondition, got %v", err)
		}
		if got, _ := store.GetHighlight(context.Background(), tenantID, h.ID); got.SoundKind != "" {
			t.Errorf("row mutated to kind %q despite the refusal", got.SoundKind)
		}
		if enq.calls != 0 {
			t.Errorf("enqueue calls = %d, want 0", enq.calls)
		}
	})

	t.Run("unknown kind is InvalidArgument", func(t *testing.T) {
		store := newFakeHighlightStore(tenantID)
		h := seedRPCHighlight(store, tenantID, uuid.New(), campaignID, storage.HighlightPromoted)
		client := newSoundClient(t, tenantID, store, &fakeRPCBlobs{}, campaignSessionStore(campaignID), &fakeHighlightEnqueuer{}, configuredSoundFactory)
		_, err := client.SetHighlightSound(context.Background(),
			connect.NewRequest(&managementv1.SetHighlightSoundRequest{Id: h.ID.String(), Kind: "dubstep"}))
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("want CodeInvalidArgument, got %v", err)
		}
	})

	t.Run("unwired seam is Unimplemented", func(t *testing.T) {
		store := newFakeHighlightStore(tenantID)
		h := seedRPCHighlight(store, tenantID, uuid.New(), campaignID, storage.HighlightPromoted)
		client := newSoundClient(t, tenantID, store, &fakeRPCBlobs{}, campaignSessionStore(campaignID), &fakeHighlightEnqueuer{}, nil)
		_, err := client.SetHighlightSound(context.Background(),
			connect.NewRequest(&managementv1.SetHighlightSoundRequest{Id: h.ID.String(), Kind: storage.SoundKindSting}))
		if connect.CodeOf(err) != connect.CodeUnimplemented {
			t.Fatalf("want CodeUnimplemented, got %v", err)
		}
	})

	t.Run("cross-campaign is NotFound", func(t *testing.T) {
		store := newFakeHighlightStore(tenantID)
		h := seedRPCHighlight(store, tenantID, uuid.New(), uuid.New(), storage.HighlightPromoted) // other campaign
		client := newSoundClient(t, tenantID, store, &fakeRPCBlobs{}, campaignSessionStore(campaignID), &fakeHighlightEnqueuer{}, configuredSoundFactory)
		_, err := client.SetHighlightSound(context.Background(),
			connect.NewRequest(&managementv1.SetHighlightSoundRequest{Id: h.ID.String(), Kind: storage.SoundKindSting}))
		if connect.CodeOf(err) != connect.CodeNotFound {
			t.Fatalf("want CodeNotFound, got %v", err)
		}
	})
}
