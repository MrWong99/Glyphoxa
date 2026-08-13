package search_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/search"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/voice/embeddings"
	"github.com/MrWong99/Glyphoxa/pkg/voice/embeddings/embeddingstest"
)

// fakeStore records which storage path each engine call took, so the tests can
// prove the access mapping and the semantic/keyword mode selection — the two
// #591 guarantees this package owns.
type fakeStore struct {
	nodes       []storage.KGNode
	publicNodes []storage.KGNode
	nodeCalls   int // SearchNodes (GM vector) calls
	publicCalls int // SearchPublicNodes calls

	chunks        []storage.ChunkMatch
	chunkErr      error
	chunkCalls    int
	chunkVec      []float32
	chunkCampaign uuid.UUID

	lines     []storage.TranscriptLine
	lineCalls int

	anchors     map[uuid.UUID]string // per-session anchor line id
	anchorErr   error
	anchorCalls int

	highlights      []storage.Highlight
	highlightCalls  int
	highlightTenant uuid.UUID
}

func (f *fakeStore) SearchNodes(_ context.Context, _ uuid.UUID, _ string, _ int) ([]storage.KGNode, error) {
	f.nodeCalls++
	return f.nodes, nil
}

func (f *fakeStore) SearchPublicNodes(_ context.Context, _ uuid.UUID, _ string, _ int) ([]storage.KGNode, error) {
	f.publicCalls++
	return f.publicNodes, nil
}

func (f *fakeStore) SearchChunksByCampaign(_ context.Context, campaignID uuid.UUID, vec []float32, _ int) ([]storage.ChunkMatch, error) {
	f.chunkCalls++
	f.chunkCampaign = campaignID
	f.chunkVec = vec
	if f.chunkErr != nil {
		return nil, f.chunkErr
	}
	return f.chunks, nil
}

func (f *fakeStore) SearchTranscriptLines(_ context.Context, _ uuid.UUID, _ string, _ int) ([]storage.TranscriptLine, error) {
	f.lineCalls++
	return f.lines, nil
}

func (f *fakeStore) FirstLineIDAtOrAfter(_ context.Context, sessionID uuid.UUID, _ time.Time) (string, error) {
	f.anchorCalls++
	if f.anchorErr != nil {
		return "", f.anchorErr
	}
	id, ok := f.anchors[sessionID]
	if !ok {
		return "", storage.ErrNotFound
	}
	return id, nil
}

func (f *fakeStore) SearchPromotedHighlights(_ context.Context, tenantID, _ uuid.UUID, _ string, _ int) ([]storage.Highlight, error) {
	f.highlightCalls++
	f.highlightTenant = tenantID
	return f.highlights, nil
}

// countingEmbedder wraps a provider and counts calls — the denied-tier tests
// assert NO provider call happens (no spend for a tier that may not search).
type countingEmbedder struct {
	inner search.Embedder
	calls int
}

func (c *countingEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	c.calls++
	return c.inner.Embed(ctx, texts)
}

// badDimEmbedder returns a wrong-dimension vector — the degrade guard's third
// arm (a wrong dim would fail the ::vector cast server-side).
type badDimEmbedder struct{}

func (badDimEmbedder) Embed(context.Context, []string) ([][]float32, error) {
	return [][]float32{make([]float32, 3)}, nil
}

// failingEmbedder always errors — the degrade guard's second arm.
type failingEmbedder struct{}

func (failingEmbedder) Embed(context.Context, []string) ([][]float32, error) {
	return nil, errors.New("provider down")
}

// TestSearchNodesAccessMapping proves the one-flag privacy routing (#591): the
// operator searches the GM vector (private material included), and EVERY player
// tier is routed to the public vector — gm_private Nodes and Aspects can never
// leave the server for a player-tier caller, not even through the hit itself.
func TestSearchNodesAccessMapping(t *testing.T) {
	t.Parallel()
	gm := []storage.KGNode{{Name: "Bart (with secret)"}}
	public := []storage.KGNode{{Name: "Bart"}}

	for _, tc := range []struct {
		name       string
		access     search.Access
		wantName   string
		wantGM     int
		wantPublic int
	}{
		{"operator gets the GM vector", search.AccessOperator, "Bart (with secret)", 1, 0},
		{"campaign-transcripts gets the public vector", search.AccessCampaignTranscripts, "Bart", 0, 1},
		{"campaign-highlights gets the public vector", search.AccessCampaignHighlights, "Bart", 0, 1},
		{"own-character gets the public vector", search.AccessOwnCharacter, "Bart", 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := &fakeStore{nodes: gm, publicNodes: public}
			eng := search.New(store, nil)
			got, err := eng.SearchNodes(context.Background(), uuid.New(), tc.access, "bart", 8)
			if err != nil {
				t.Fatalf("SearchNodes: %v", err)
			}
			if len(got) != 1 || got[0].Name != tc.wantName {
				t.Fatalf("got %v, want one node named %q", got, tc.wantName)
			}
			if store.nodeCalls != tc.wantGM || store.publicCalls != tc.wantPublic {
				t.Fatalf("path counts gm=%d public=%d, want gm=%d public=%d",
					store.nodeCalls, store.publicCalls, tc.wantGM, tc.wantPublic)
			}
		})
	}
}

// TestSearchTranscriptsSemantic is the happy semantic path: with an embedder
// wired the engine embeds the query, runs the campaign ANN, anchors each chunk
// hit on its first Line, and reports Semantic=true. The snippet is the chunk
// content and the At is the chunk's start.
func TestSearchTranscriptsSemantic(t *testing.T) {
	t.Parallel()
	campaignID := uuid.New()
	sessionA := uuid.New()
	started := time.Date(2026, 8, 1, 19, 0, 0, 0, time.UTC)
	store := &fakeStore{
		chunks: []storage.ChunkMatch{{
			Chunk: storage.TranscriptChunk{
				VoiceSessionID: sessionA,
				Content:        "GM: the cult scatters\nBart: not my problem",
				StartedAt:      started,
			},
			Distance: 0.1,
		}},
		anchors: map[uuid.UUID]string{sessionA: "u:7"},
		lines:   []storage.TranscriptLine{{Text: "should not be used"}},
	}
	eng := search.New(store, nil)
	eng.SetEmbedder(embeddingstest.Deterministic{}, "ollama", "nomic-embed-text")

	res, err := eng.SearchTranscripts(context.Background(), campaignID, search.AccessOperator, "what happened with the cult", 8)
	if err != nil {
		t.Fatalf("SearchTranscripts: %v", err)
	}
	if !res.Semantic {
		t.Fatal("Semantic = false with a working embedder")
	}
	if store.chunkCampaign != campaignID {
		t.Fatalf("ANN ran against campaign %s, want %s", store.chunkCampaign, campaignID)
	}
	if len(store.chunkVec) != embeddings.Dim {
		t.Fatalf("query vector has %d dims, want %d", len(store.chunkVec), embeddings.Dim)
	}
	if store.lineCalls != 0 {
		t.Fatal("keyword line search ran on the semantic path")
	}
	if len(res.Hits) != 1 {
		t.Fatalf("got %d hits, want 1", len(res.Hits))
	}
	h := res.Hits[0]
	if h.VoiceSessionID != sessionA || h.LineID != "u:7" {
		t.Fatalf("deep-link pair = (%s, %q), want (%s, %q)", h.VoiceSessionID, h.LineID, sessionA, "u:7")
	}
	if !h.At.Equal(started) || !strings.Contains(h.Snippet, "the cult scatters") {
		t.Fatalf("hit context = (%v, %q), want chunk start + content", h.At, h.Snippet)
	}
	if h.Who != "" || h.Kind != "" {
		t.Fatalf("chunk hit carries speaker fields (%q, %q); they belong to line hits", h.Who, h.Kind)
	}
}

// TestSearchTranscriptsAnchorMiss: a chunk whose session has no Line at/after
// its start (ErrNotFound) still returns the hit — just without a scroll target.
// Any OTHER anchor error is a real failure and surfaces.
func TestSearchTranscriptsAnchorMiss(t *testing.T) {
	t.Parallel()
	sessionA := uuid.New()
	store := &fakeStore{
		chunks: []storage.ChunkMatch{{Chunk: storage.TranscriptChunk{
			VoiceSessionID: sessionA, Content: "orphan chunk", StartedAt: time.Now(),
		}}},
		anchors: map[uuid.UUID]string{}, // miss → ErrNotFound
	}
	eng := search.New(store, nil)
	eng.SetEmbedder(embeddingstest.Deterministic{}, "ollama", "nomic-embed-text")

	res, err := eng.SearchTranscripts(context.Background(), uuid.New(), search.AccessOperator, "orphan", 8)
	if err != nil {
		t.Fatalf("SearchTranscripts: %v", err)
	}
	if len(res.Hits) != 1 || res.Hits[0].LineID != "" {
		t.Fatalf("hits = %+v, want one hit with an empty LineID", res.Hits)
	}

	store.anchorErr = errors.New("db down")
	if _, err := eng.SearchTranscripts(context.Background(), uuid.New(), search.AccessOperator, "orphan", 8); err == nil {
		t.Fatal("a non-NotFound anchor error was swallowed")
	}
}

// TestSearchTranscriptsDegrades proves every arm of the degradation guard lands
// on the keyword Line path with Semantic=false: no embedder, an erroring
// embedder, and a wrong-dimension embedder. The keyword hits carry the Line's
// speaker context and deep-link pair.
func TestSearchTranscriptsDegrades(t *testing.T) {
	t.Parallel()
	sessionA := uuid.New()
	ts := time.Date(2026, 8, 2, 20, 15, 0, 0, time.UTC)
	line := storage.TranscriptLine{
		VoiceSessionID: sessionA, LineID: "u:3", Who: "Lena / Vex", Kind: "player",
		TS: ts, Text: "we promised Bart fifty gold",
	}

	for _, tc := range []struct {
		name string
		wire func(*search.Engine)
	}{
		{"no embedder", func(*search.Engine) {}},
		{"embed error", func(e *search.Engine) { e.SetEmbedder(failingEmbedder{}, "ollama", "m") }},
		{"wrong dimension", func(e *search.Engine) { e.SetEmbedder(badDimEmbedder{}, "ollama", "m") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := &fakeStore{lines: []storage.TranscriptLine{line}}
			eng := search.New(store, nil)
			tc.wire(eng)

			res, err := eng.SearchTranscripts(context.Background(), uuid.New(), search.AccessOperator, "bart gold", 8)
			if err != nil {
				t.Fatalf("SearchTranscripts: %v", err)
			}
			if res.Semantic {
				t.Fatal("Semantic = true on a degraded path")
			}
			if store.chunkCalls != 0 {
				t.Fatal("ANN ran without a usable query vector")
			}
			if len(res.Hits) != 1 {
				t.Fatalf("got %d hits, want 1", len(res.Hits))
			}
			h := res.Hits[0]
			if h.VoiceSessionID != sessionA || h.LineID != "u:3" || h.Who != "Lena / Vex" || h.Kind != "player" || !h.At.Equal(ts) {
				t.Fatalf("line hit mapped wrong: %+v", h)
			}
		})
	}
}

// TestSearchTranscriptsPlayerTierExclusions is the #591 access guarantee: only
// the operator and the campaign-transcripts level search transcript text at
// all. campaign-highlights and own-character get an empty result WITHOUT a
// provider call (no spend for a denied tier) and WITHOUT touching either
// storage path.
func TestSearchTranscriptsPlayerTierExclusions(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		access  search.Access
		allowed bool
	}{
		{"operator searches", search.AccessOperator, true},
		{"campaign-transcripts searches", search.AccessCampaignTranscripts, true},
		{"campaign-highlights is excluded", search.AccessCampaignHighlights, false},
		{"own-character is excluded", search.AccessOwnCharacter, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := &fakeStore{
				chunks:  []storage.ChunkMatch{{Chunk: storage.TranscriptChunk{Content: "secret table talk"}}},
				lines:   []storage.TranscriptLine{{Text: "secret table talk"}},
				anchors: map[uuid.UUID]string{},
			}
			emb := &countingEmbedder{inner: embeddingstest.Deterministic{}}
			eng := search.New(store, nil)
			eng.SetEmbedder(emb, "ollama", "nomic-embed-text")

			res, err := eng.SearchTranscripts(context.Background(), uuid.New(), tc.access, "secret", 8)
			if err != nil {
				t.Fatalf("SearchTranscripts: %v", err)
			}
			if tc.allowed {
				if len(res.Hits) == 0 {
					t.Fatal("an allowed tier got no hits")
				}
				return
			}
			if len(res.Hits) != 0 || res.Semantic {
				t.Fatalf("denied tier got %+v", res)
			}
			if emb.calls != 0 {
				t.Fatal("denied tier still embedded the query (spent provider quota)")
			}
			if store.chunkCalls != 0 || store.lineCalls != 0 {
				t.Fatal("denied tier reached transcript storage")
			}
		})
	}
}

// TestSearchHighlightsAccess: own-character fails CLOSED (its "highlights they
// appear in" slice needs Character links that don't exist yet); every other
// tier reaches the promoted-only storage search with the caller's tenant.
func TestSearchHighlightsAccess(t *testing.T) {
	t.Parallel()
	tenantID := uuid.New()
	rows := []storage.Highlight{{Excerpt: "the barbarian ate the contract"}}

	for _, tc := range []struct {
		name    string
		access  search.Access
		wantHit bool
	}{
		{"operator searches", search.AccessOperator, true},
		{"campaign-transcripts searches", search.AccessCampaignTranscripts, true},
		{"campaign-highlights searches", search.AccessCampaignHighlights, true},
		{"own-character fails closed", search.AccessOwnCharacter, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := &fakeStore{highlights: rows}
			eng := search.New(store, nil)
			got, err := eng.SearchHighlights(context.Background(), tenantID, uuid.New(), tc.access, "contract", 8)
			if err != nil {
				t.Fatalf("SearchHighlights: %v", err)
			}
			if tc.wantHit {
				if len(got) != 1 || store.highlightTenant != tenantID {
					t.Fatalf("got %d rows under tenant %s; want 1 under %s", len(got), store.highlightTenant, tenantID)
				}
				return
			}
			if len(got) != 0 || store.highlightCalls != 0 {
				t.Fatal("own-character reached highlight storage")
			}
		})
	}
}

// TestSnippetTruncation: an oversized chunk content is rune-truncated to
// SnippetRunes + ellipsis, never a split codepoint (the ü sits at the cut).
func TestSnippetTruncation(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("ü", search.SnippetRunes+50)
	store := &fakeStore{
		chunks:  []storage.ChunkMatch{{Chunk: storage.TranscriptChunk{Content: long}}},
		anchors: map[uuid.UUID]string{},
	}
	eng := search.New(store, nil)
	eng.SetEmbedder(embeddingstest.Deterministic{}, "ollama", "nomic-embed-text")

	res, err := eng.SearchTranscripts(context.Background(), uuid.New(), search.AccessOperator, "ü", 8)
	if err != nil {
		t.Fatalf("SearchTranscripts: %v", err)
	}
	got := []rune(res.Hits[0].Snippet)
	if len(got) != search.SnippetRunes+1 || got[len(got)-1] != '…' {
		t.Fatalf("snippet = %d runes ending %q, want %d + ellipsis", len(got), got[len(got)-1], search.SnippetRunes)
	}
}
