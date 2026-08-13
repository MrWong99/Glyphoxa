package rpc_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/rpc"
	"github.com/MrWong99/Glyphoxa/internal/search"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/voice/embeddings/embeddingstest"
)

// fakeSearchEngineStore is the search.Store the palette RPC tests drive the
// REAL engine over — the handler + engine pair is exercised end-to-end, only
// storage (and optionally the embedder) is faked.
type fakeSearchEngineStore struct {
	chunks     []storage.ChunkMatch
	anchors    map[uuid.UUID]string
	lines      []storage.TranscriptLine
	highlights []storage.Highlight

	highlightTenant   uuid.UUID
	highlightCampaign uuid.UUID
	highlightQuery    string
}

func (f *fakeSearchEngineStore) SearchNodes(context.Context, uuid.UUID, string, int) ([]storage.KGNode, error) {
	return nil, nil
}

func (f *fakeSearchEngineStore) SearchPublicNodes(context.Context, uuid.UUID, string, int) ([]storage.KGNode, error) {
	return nil, nil
}

func (f *fakeSearchEngineStore) SearchChunksByCampaign(context.Context, uuid.UUID, []float32, int) ([]storage.ChunkMatch, error) {
	return f.chunks, nil
}

func (f *fakeSearchEngineStore) SearchTranscriptLines(context.Context, uuid.UUID, string, int) ([]storage.TranscriptLine, error) {
	return f.lines, nil
}

func (f *fakeSearchEngineStore) FirstLineIDAtOrAfter(_ context.Context, sessionID uuid.UUID, _ time.Time) (string, error) {
	id, ok := f.anchors[sessionID]
	if !ok {
		return "", storage.ErrNotFound
	}
	return id, nil
}

func (f *fakeSearchEngineStore) SearchPromotedHighlights(_ context.Context, tenantID, campaignID uuid.UUID, query string, _ int) ([]storage.Highlight, error) {
	f.highlightTenant = tenantID
	f.highlightCampaign = campaignID
	f.highlightQuery = query
	return f.highlights, nil
}

// newSearchClient mounts a SessionServer with the campaign search seam wired
// over the given engine store, an authenticated operator on tenantID, and the
// standard fakeSessionStore campaign resolution. semantic wires the
// deterministic test embedder.
func newSearchClient(t *testing.T, tenantID uuid.UUID, sstore *fakeSessionStore, estore *fakeSearchEngineStore, semantic bool) managementv1connect.SessionServiceClient {
	t.Helper()
	inject := connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			return next(auth.WithTenant(ctx, tenantID), req)
		}
	})
	srv := rpc.NewSessionServer(&fakeSessionManager{}, sstore, nil, nil)
	eng := search.New(estore, nil)
	if semantic {
		eng.SetEmbedder(embeddingstest.Deterministic{}, "ollama", "nomic-embed-text")
	}
	srv.SetSearch(eng)
	mux := http.NewServeMux()
	mux.Handle(srv.Handler(connect.WithInterceptors(inject)))
	httpSrv := httptest.NewServer(mux)
	t.Cleanup(httpSrv.Close)
	return managementv1connect.NewSessionServiceClient(http.DefaultClient, httpSrv.URL, connect.WithProtoJSON())
}

// TestSearchTranscriptsSemanticWire: the semantic path end-to-end — chunk hits
// land on the wire with semantic=true, the {session, line} deep-link pair, the
// chunk's start as the date context, and empty speaker fields.
func TestSearchTranscriptsSemanticWire(t *testing.T) {
	t.Parallel()
	sessionA := uuid.New()
	started := time.Date(2026, 8, 3, 19, 30, 0, 0, time.UTC)
	estore := &fakeSearchEngineStore{
		chunks: []storage.ChunkMatch{{Chunk: storage.TranscriptChunk{
			VoiceSessionID: sessionA,
			Content:        "GM: the cult regroups at the mill",
			StartedAt:      started,
		}}},
		anchors: map[uuid.UUID]string{sessionA: "u:12"},
	}
	client := newSearchClient(t, uuid.New(), activeStore(), estore, true)

	resp, err := client.SearchTranscripts(context.Background(),
		connect.NewRequest(&managementv1.SearchTranscriptsRequest{Query: "cult"}))
	if err != nil {
		t.Fatalf("SearchTranscripts: %v", err)
	}
	if !resp.Msg.GetSemantic() {
		t.Fatal("semantic = false with an embedder wired")
	}
	hits := resp.Msg.GetHits()
	if len(hits) != 1 {
		t.Fatalf("got %d hits, want 1", len(hits))
	}
	h := hits[0]
	if h.GetVoiceSessionId() != sessionA.String() || h.GetLineId() != "u:12" {
		t.Fatalf("deep-link pair = (%s, %s), want (%s, u:12)", h.GetVoiceSessionId(), h.GetLineId(), sessionA)
	}
	if !h.GetAt().AsTime().Equal(started) {
		t.Fatalf("at = %v, want the chunk start %v", h.GetAt().AsTime(), started)
	}
	if h.GetWho() != "" || h.GetKind() != "" {
		t.Fatalf("chunk hit carries speaker fields (%q, %q)", h.GetWho(), h.GetKind())
	}
}

// TestSearchTranscriptsDegradedWire: without an embedder the handler reports
// semantic=false and serves keyword Line hits with their speaker context — the
// palette's honest degraded mode (#591 AC).
func TestSearchTranscriptsDegradedWire(t *testing.T) {
	t.Parallel()
	sessionA := uuid.New()
	ts := time.Date(2026, 8, 4, 21, 0, 0, 0, time.UTC)
	estore := &fakeSearchEngineStore{
		lines: []storage.TranscriptLine{{
			VoiceSessionID: sessionA, LineID: "u:3", Who: "Lena / Vex", Kind: "player",
			TS: ts, Text: "we promised Bart fifty gold",
		}},
	}
	client := newSearchClient(t, uuid.New(), activeStore(), estore, false)

	resp, err := client.SearchTranscripts(context.Background(),
		connect.NewRequest(&managementv1.SearchTranscriptsRequest{Query: "bart"}))
	if err != nil {
		t.Fatalf("SearchTranscripts: %v", err)
	}
	if resp.Msg.GetSemantic() {
		t.Fatal("semantic = true without an embedder")
	}
	hits := resp.Msg.GetHits()
	if len(hits) != 1 {
		t.Fatalf("got %d hits, want 1", len(hits))
	}
	h := hits[0]
	if h.GetVoiceSessionId() != sessionA.String() || h.GetLineId() != "u:3" ||
		h.GetWho() != "Lena / Vex" || h.GetKind() != "player" || h.GetSnippet() != "we promised Bart fifty gold" {
		t.Fatalf("line hit mapped wrong: %+v", h)
	}
}

// TestSearchTranscriptsValidation: empty/whitespace query is InvalidArgument;
// no Active Campaign is an EMPTY result (never-run state, the
// SearchTranscriptLines choice), not an error; an unwired seam is
// Unimplemented.
func TestSearchTranscriptsValidation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	client := newSearchClient(t, uuid.New(), activeStore(), &fakeSearchEngineStore{}, false)
	if _, err := client.SearchTranscripts(ctx,
		connect.NewRequest(&managementv1.SearchTranscriptsRequest{Query: "   "})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("whitespace query: got %v, want InvalidArgument", err)
	}

	// No campaign resolves at all → empty result, no error.
	noCampaign := &fakeSessionStore{campaignErr: storage.ErrNotFound, latestErr: storage.ErrNotFound}
	emptyClient := newSearchClient(t, uuid.New(), noCampaign, &fakeSearchEngineStore{
		lines: []storage.TranscriptLine{{Text: "unreachable"}},
	}, false)
	resp, err := emptyClient.SearchTranscripts(ctx,
		connect.NewRequest(&managementv1.SearchTranscriptsRequest{Query: "anything"}))
	if err != nil {
		t.Fatalf("no-campaign search: %v", err)
	}
	if len(resp.Msg.GetHits()) != 0 {
		t.Fatal("no-campaign search returned hits")
	}

	// Unwired seam → Unimplemented (keyless boots, web-standalone tests).
	unwired := newSessionClient(t, &fakeSessionManager{}, activeStore())
	if _, err := unwired.SearchTranscripts(ctx,
		connect.NewRequest(&managementv1.SearchTranscriptsRequest{Query: "x"})); connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Fatalf("unwired seam: got %v, want Unimplemented", err)
	}
}

// TestSearchHighlightsWire: promoted-Highlight hits land on the wire with the
// deep-link ids and the caller's TENANT + resolved campaign scoped the storage
// query. Validation mirrors SearchTranscripts.
func TestSearchHighlightsWire(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tenantID := uuid.New()
	sessionA := uuid.New()
	hlID := uuid.New()
	starts := time.Date(2026, 7, 19, 20, 45, 0, 0, time.UTC)
	sstore := activeStore()
	estore := &fakeSearchEngineStore{
		highlights: []storage.Highlight{{
			ID: hlID, VoiceSessionID: sessionA, Status: storage.HighlightPromoted,
			Excerpt: "the barbarian ate the contract", Reason: "table erupted", StartsAt: starts,
		}},
	}
	client := newSearchClient(t, tenantID, sstore, estore, false)

	resp, err := client.SearchHighlights(ctx,
		connect.NewRequest(&managementv1.SearchHighlightsRequest{Query: "contract"}))
	if err != nil {
		t.Fatalf("SearchHighlights: %v", err)
	}
	hits := resp.Msg.GetHits()
	if len(hits) != 1 {
		t.Fatalf("got %d hits, want 1", len(hits))
	}
	h := hits[0]
	if h.GetId() != hlID.String() || h.GetVoiceSessionId() != sessionA.String() ||
		h.GetExcerpt() != "the barbarian ate the contract" || h.GetReason() != "table erupted" ||
		!h.GetStartsAt().AsTime().Equal(starts) {
		t.Fatalf("highlight hit mapped wrong: %+v", h)
	}
	if estore.highlightTenant != tenantID {
		t.Fatalf("storage searched tenant %s, want the caller's %s", estore.highlightTenant, tenantID)
	}
	if estore.highlightCampaign != sstore.campaign.ID {
		t.Fatalf("storage searched campaign %s, want the resolved %s", estore.highlightCampaign, sstore.campaign.ID)
	}

	if _, err := client.SearchHighlights(ctx,
		connect.NewRequest(&managementv1.SearchHighlightsRequest{Query: ""})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("empty query: got %v, want InvalidArgument", err)
	}

	unwired := newSessionClient(t, &fakeSessionManager{}, activeStore())
	if _, err := unwired.SearchHighlights(ctx,
		connect.NewRequest(&managementv1.SearchHighlightsRequest{Query: "x"})); connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Fatalf("unwired seam: got %v, want Unimplemented", err)
	}
}
