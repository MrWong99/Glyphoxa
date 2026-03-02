package memorytool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MrWong99/glyphoxa/pkg/memory"
	"github.com/MrWong99/glyphoxa/pkg/memory/mock"
	embmock "github.com/MrWong99/glyphoxa/pkg/provider/embeddings/mock"
)

// ─────────────────────────────────────────────────────────────────────────────
// search_sessions
// ─────────────────────────────────────────────────────────────────────────────

func TestSearchSessions_Success(t *testing.T) {
	t.Parallel()
	store := &mock.SessionStore{
		SearchResult: []memory.TranscriptEntry{
			{SpeakerID: "player1", Text: "I attack the goblin", Timestamp: time.Now()},
			{SpeakerID: "npc1", Text: "The goblin screams", Timestamp: time.Now()},
		},
	}

	handler := makeSearchSessionsHandler(store)
	ctx := context.Background()

	out, err := handler(ctx, `{"query":"goblin"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var entries []memory.TranscriptEntry
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("failed to unmarshal: %v\noutput: %s", err, out)
	}
	if len(entries) != 2 {
		t.Errorf("expected 2 entries, got %d", len(entries))
	}

	if n := store.CallCount("Search"); n != 1 {
		t.Errorf("expected 1 Search call, got %d", n)
	}
}

func TestSearchSessions_WithSessionID(t *testing.T) {
	t.Parallel()
	store := &mock.SessionStore{
		SearchResult: []memory.TranscriptEntry{
			{SpeakerID: "player1", Text: "hello", Timestamp: time.Now()},
		},
	}

	handler := makeSearchSessionsHandler(store)
	ctx := context.Background()

	_, err := handler(ctx, `{"query":"hello","session_id":"session-42"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	calls := store.Calls()
	if len(calls) == 0 {
		t.Fatal("no calls recorded")
	}
	opts := calls[0].Args[1].(memory.SearchOpts)
	if opts.SessionID != "session-42" {
		t.Errorf("SessionID = %q, want %q", opts.SessionID, "session-42")
	}
}

func TestSearchSessions_EmptyQuery(t *testing.T) {
	t.Parallel()
	store := &mock.SessionStore{}
	handler := makeSearchSessionsHandler(store)

	_, err := handler(context.Background(), `{"query":""}`)
	if err == nil {
		t.Error("expected error for empty query")
	}
	if !strings.HasPrefix(err.Error(), "memory tool:") {
		t.Errorf("error %q should be prefixed with 'memory tool:'", err.Error())
	}
}

func TestSearchSessions_StoreError(t *testing.T) {
	t.Parallel()
	store := &mock.SessionStore{
		SearchErr: errors.New("database unavailable"),
	}
	handler := makeSearchSessionsHandler(store)

	_, err := handler(context.Background(), `{"query":"anything"}`)
	if err == nil {
		t.Error("expected error from store")
	}
}

func TestSearchSessions_BadJSON(t *testing.T) {
	t.Parallel()
	store := &mock.SessionStore{}
	handler := makeSearchSessionsHandler(store)

	_, err := handler(context.Background(), `{bad json}`)
	if err == nil {
		t.Error("expected error for bad JSON")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// query_entities
// ─────────────────────────────────────────────────────────────────────────────

func TestQueryEntities_Success(t *testing.T) {
	t.Parallel()
	graph := &mock.KnowledgeGraph{
		FindEntitiesResult: []memory.Entity{
			{ID: "npc-1", Type: "npc", Name: "Eldrinax", Attributes: map[string]any{"occupation": "wizard"}},
		},
	}

	handler := makeQueryEntitiesHandler(graph)
	ctx := context.Background()

	out, err := handler(ctx, `{"name":"Eldrinax","type":"npc"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var entities []memory.Entity
	if err := json.Unmarshal([]byte(out), &entities); err != nil {
		t.Fatalf("failed to unmarshal: %v\noutput: %s", err, out)
	}
	if len(entities) != 1 {
		t.Errorf("expected 1 entity, got %d", len(entities))
	}
	if entities[0].Name != "Eldrinax" {
		t.Errorf("Name = %q, want %q", entities[0].Name, "Eldrinax")
	}

	// Verify filter was passed correctly.
	calls := graph.Calls()
	if len(calls) == 0 {
		t.Fatal("no calls recorded")
	}
	filter := calls[0].Args[0].(memory.EntityFilter)
	if filter.Name != "Eldrinax" {
		t.Errorf("filter.Name = %q, want %q", filter.Name, "Eldrinax")
	}
	if filter.Type != "npc" {
		t.Errorf("filter.Type = %q, want %q", filter.Type, "npc")
	}
}

func TestQueryEntities_NoFilters(t *testing.T) {
	t.Parallel()
	graph := &mock.KnowledgeGraph{
		FindEntitiesResult: []memory.Entity{
			{ID: "1", Name: "A"},
			{ID: "2", Name: "B"},
		},
	}

	handler := makeQueryEntitiesHandler(graph)
	out, err := handler(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var entities []memory.Entity
	if err := json.Unmarshal([]byte(out), &entities); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if len(entities) != 2 {
		t.Errorf("expected 2 entities, got %d", len(entities))
	}
}

func TestQueryEntities_GraphError(t *testing.T) {
	t.Parallel()
	graph := &mock.KnowledgeGraph{
		FindEntitiesErr: errors.New("connection refused"),
	}
	handler := makeQueryEntitiesHandler(graph)

	_, err := handler(context.Background(), `{"name":"test"}`)
	if err == nil {
		t.Error("expected error from graph")
	}
}

func TestQueryEntities_EmptyResult(t *testing.T) {
	t.Parallel()
	graph := &mock.KnowledgeGraph{} // no result configured → returns empty slice
	handler := makeQueryEntitiesHandler(graph)

	out, err := handler(context.Background(), `{"name":"nobody"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var entities []memory.Entity
	if err := json.Unmarshal([]byte(out), &entities); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if len(entities) != 0 {
		t.Errorf("expected empty result, got %d entities", len(entities))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// get_summary
// ─────────────────────────────────────────────────────────────────────────────

func TestGetSummary_Success(t *testing.T) {
	t.Parallel()
	graph := &mock.KnowledgeGraph{
		IdentitySnapshotResult: &memory.NPCIdentity{
			Entity: memory.Entity{ID: "npc-1", Name: "Eldrinax"},
			Relationships: []memory.Relationship{
				{SourceID: "npc-1", TargetID: "faction-1", RelType: "member_of"},
			},
			RelatedEntities: []memory.Entity{
				{ID: "faction-1", Name: "The Arcane Brotherhood"},
			},
		},
	}

	handler := makeGetSummaryHandler(graph)
	out, err := handler(context.Background(), `{"entity_id":"npc-1"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var snapshot memory.NPCIdentity
	if err := json.Unmarshal([]byte(out), &snapshot); err != nil {
		t.Fatalf("failed to unmarshal: %v\noutput: %s", err, out)
	}
	if snapshot.Entity.ID != "npc-1" {
		t.Errorf("Entity.ID = %q, want %q", snapshot.Entity.ID, "npc-1")
	}
	if len(snapshot.Relationships) != 1 {
		t.Errorf("expected 1 relationship, got %d", len(snapshot.Relationships))
	}
}

func TestGetSummary_EmptyEntityID(t *testing.T) {
	t.Parallel()
	graph := &mock.KnowledgeGraph{}
	handler := makeGetSummaryHandler(graph)

	_, err := handler(context.Background(), `{"entity_id":""}`)
	if err == nil {
		t.Error("expected error for empty entity_id")
	}
	if !strings.HasPrefix(err.Error(), "memory tool:") {
		t.Errorf("error %q should be prefixed with 'memory tool:'", err.Error())
	}
}

func TestGetSummary_NotFound(t *testing.T) {
	t.Parallel()
	graph := &mock.KnowledgeGraph{
		IdentitySnapshotResult: nil, // nil means not found
	}
	handler := makeGetSummaryHandler(graph)

	_, err := handler(context.Background(), `{"entity_id":"nonexistent"}`)
	if err == nil {
		t.Error("expected error for missing entity")
	}
}

func TestGetSummary_GraphError(t *testing.T) {
	t.Parallel()
	graph := &mock.KnowledgeGraph{
		IdentitySnapshotErr: errors.New("timeout"),
	}
	handler := makeGetSummaryHandler(graph)

	_, err := handler(context.Background(), `{"entity_id":"npc-1"}`)
	if err == nil {
		t.Error("expected error from graph")
	}
}

func TestGetSummary_BadJSON(t *testing.T) {
	t.Parallel()
	graph := &mock.KnowledgeGraph{}
	handler := makeGetSummaryHandler(graph)

	_, err := handler(context.Background(), `{bad json}`)
	if err == nil {
		t.Error("expected error for bad JSON")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// search_facts
// ─────────────────────────────────────────────────────────────────────────────

func TestSearchFacts_Success(t *testing.T) {
	t.Parallel()
	store := &mock.SessionStore{
		SearchResult: []memory.TranscriptEntry{
			{SpeakerID: "npc1", Text: "The ancient prophecy speaks of a chosen one"},
		},
	}

	handler := makeSearchFactsHandler(store, nil, nil)
	out, err := handler(context.Background(), `{"query":"prophecy","top_k":5}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var entries []memory.TranscriptEntry
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("failed to unmarshal: %v\noutput: %s", err, out)
	}
	if len(entries) != 1 {
		t.Errorf("expected 1 entry, got %d", len(entries))
	}

	// Verify top_k was forwarded as Limit.
	calls := store.Calls()
	if len(calls) == 0 {
		t.Fatal("no calls recorded")
	}
	opts := calls[0].Args[1].(memory.SearchOpts)
	if opts.Limit != 5 {
		t.Errorf("Limit = %d, want 5", opts.Limit)
	}
}

func TestSearchFacts_DefaultTopK(t *testing.T) {
	t.Parallel()
	store := &mock.SessionStore{}
	handler := makeSearchFactsHandler(store, nil, nil)

	_, err := handler(context.Background(), `{"query":"anything"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	calls := store.Calls()
	if len(calls) == 0 {
		t.Fatal("no calls recorded")
	}
	opts := calls[0].Args[1].(memory.SearchOpts)
	if opts.Limit != defaultTopK {
		t.Errorf("Limit = %d, want %d (default)", opts.Limit, defaultTopK)
	}
}

func TestSearchFacts_EmptyQuery(t *testing.T) {
	t.Parallel()
	store := &mock.SessionStore{}
	handler := makeSearchFactsHandler(store, nil, nil)

	_, err := handler(context.Background(), `{"query":""}`)
	if err == nil {
		t.Error("expected error for empty query")
	}
}

func TestSearchFacts_StoreError(t *testing.T) {
	t.Parallel()
	store := &mock.SessionStore{
		SearchErr: errors.New("disk full"),
	}
	handler := makeSearchFactsHandler(store, nil, nil)

	_, err := handler(context.Background(), `{"query":"anything"}`)
	if err == nil {
		t.Error("expected error from store")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// NewTools
// ─────────────────────────────────────────────────────────────────────────────

func TestNewTools_ReturnsExpectedTools(t *testing.T) {
	t.Parallel()
	store := &mock.SessionStore{}
	index := &mock.SemanticIndex{}
	graph := &mock.KnowledgeGraph{}

	ts := NewTools(store, index, graph, nil)
	if len(ts) != 5 {
		t.Fatalf("NewTools returned %d tools, want 5", len(ts))
	}

	wantNames := map[string]bool{
		"search_sessions": true,
		"query_entities":  true,
		"get_summary":     true,
		"search_facts":    true,
		"search_graph":    true,
	}

	for _, tool := range ts {
		if !wantNames[tool.Definition.Name] {
			t.Errorf("unexpected tool name %q", tool.Definition.Name)
		}
		delete(wantNames, tool.Definition.Name)

		if tool.Handler == nil {
			t.Errorf("tool %q has nil Handler", tool.Definition.Name)
		}
		if tool.DeclaredP50 <= 0 {
			t.Errorf("tool %q DeclaredP50 = %d, want > 0", tool.Definition.Name, tool.DeclaredP50)
		}
		if tool.DeclaredMax <= 0 {
			t.Errorf("tool %q DeclaredMax = %d, want > 0", tool.Definition.Name, tool.DeclaredMax)
		}
	}

	for missing := range wantNames {
		t.Errorf("NewTools missing tool %q", missing)
	}
}

func TestNewTools_NoGraphOmitsSearchGraph(t *testing.T) {
	t.Parallel()
	store := &mock.SessionStore{}

	ts := NewTools(store, nil, nil, nil)
	for _, tool := range ts {
		if tool.Definition.Name == "search_graph" {
			t.Error("search_graph should not be present when graph is nil")
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// search_facts — semantic search path
// ─────────────────────────────────────────────────────────────────────────────

func TestSearchFacts_SemanticSearch(t *testing.T) {
	t.Parallel()
	store := &mock.SessionStore{
		SearchResult: []memory.TranscriptEntry{
			{SpeakerID: "npc1", Text: "FTS result about dragons"},
		},
	}
	index := &mock.SemanticIndex{
		SearchResult: []memory.ChunkResult{
			{
				Chunk: memory.Chunk{
					SpeakerID: "npc1",
					Content:   "Semantic result about dragons",
					Timestamp: time.Now(),
				},
				Distance: 0.05,
			},
		},
	}
	embedProv := &embmock.Provider{
		EmbedResult: []float32{0.1, 0.2, 0.3},
	}

	handler := makeSearchFactsHandler(store, index, embedProv)
	out, err := handler(context.Background(), `{"query":"dragons","top_k":10}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var entries []memory.TranscriptEntry
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("failed to unmarshal: %v\noutput: %s", err, out)
	}

	// Should have both semantic and FTS results (merged).
	if len(entries) != 2 {
		t.Errorf("expected 2 entries (semantic + FTS), got %d", len(entries))
	}
	// Semantic result should come first.
	if len(entries) > 0 && entries[0].Text != "Semantic result about dragons" {
		t.Errorf("first entry = %q, want semantic result", entries[0].Text)
	}

	// Verify embed was called.
	if len(embedProv.EmbedCalls) != 1 {
		t.Errorf("Embed called %d times, want 1", len(embedProv.EmbedCalls))
	}
	// Verify index.Search was called.
	if index.CallCount("Search") != 1 {
		t.Errorf("index.Search called %d times, want 1", index.CallCount("Search"))
	}
}

func TestSearchFacts_SemanticDedup(t *testing.T) {
	t.Parallel()
	store := &mock.SessionStore{
		SearchResult: []memory.TranscriptEntry{
			{SpeakerID: "npc1", Text: "same text about dragons"},
		},
	}
	index := &mock.SemanticIndex{
		SearchResult: []memory.ChunkResult{
			{
				Chunk: memory.Chunk{
					SpeakerID: "npc1",
					Content:   "same text about dragons",
					Timestamp: time.Now(),
				},
				Distance: 0.05,
			},
		},
	}
	embedProv := &embmock.Provider{
		EmbedResult: []float32{0.1, 0.2, 0.3},
	}

	handler := makeSearchFactsHandler(store, index, embedProv)
	out, err := handler(context.Background(), `{"query":"dragons"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var entries []memory.TranscriptEntry
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	// Duplicate text should be deduplicated.
	if len(entries) != 1 {
		t.Errorf("expected 1 entry (deduped), got %d", len(entries))
	}
}

func TestSearchFacts_EmbedFailureFallsBackToFTS(t *testing.T) {
	t.Parallel()
	store := &mock.SessionStore{
		SearchResult: []memory.TranscriptEntry{
			{SpeakerID: "npc1", Text: "FTS only result"},
		},
	}
	index := &mock.SemanticIndex{}
	embedProv := &embmock.Provider{
		EmbedErr: errors.New("embedding service down"),
	}

	handler := makeSearchFactsHandler(store, index, embedProv)
	out, err := handler(context.Background(), `{"query":"anything"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var entries []memory.TranscriptEntry
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("expected 1 FTS entry, got %d", len(entries))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// search_graph
// ─────────────────────────────────────────────────────────────────────────────

func TestSearchGraph_Success(t *testing.T) {
	t.Parallel()
	graph := &mock.GraphRAGQuerier{
		QueryWithContextResult: []memory.ContextResult{
			{Entity: memory.Entity{ID: "loc-1", Name: "The Forge"}, Content: "A roaring furnace", Score: 0.9},
		},
	}

	handler := makeSearchGraphHandler(graph, nil)
	out, err := handler(context.Background(), `{"query":"forge","scope":["loc-1"]}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var results []memory.ContextResult
	if err := json.Unmarshal([]byte(out), &results); err != nil {
		t.Fatalf("failed to unmarshal: %v\noutput: %s", err, out)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 result, got %d", len(results))
	}
	if results[0].Entity.Name != "The Forge" {
		t.Errorf("Entity.Name = %q, want %q", results[0].Entity.Name, "The Forge")
	}

	// Should have used QueryWithContext (no embedding provider).
	if graph.CallCount("QueryWithContext") != 1 {
		t.Errorf("QueryWithContext called %d times, want 1", graph.CallCount("QueryWithContext"))
	}
	if graph.CallCount("QueryWithEmbedding") != 0 {
		t.Errorf("QueryWithEmbedding called %d times, want 0", graph.CallCount("QueryWithEmbedding"))
	}
}

func TestSearchGraph_WithEmbeddings(t *testing.T) {
	t.Parallel()
	graph := &mock.GraphRAGQuerier{
		QueryWithEmbeddingResult: []memory.ContextResult{
			{Entity: memory.Entity{ID: "npc-1", Name: "Grimjaw"}, Content: "A gruff blacksmith", Score: 0.85},
		},
	}
	embedProv := &embmock.Provider{
		EmbedResult: []float32{0.1, 0.2, 0.3},
	}

	handler := makeSearchGraphHandler(graph, embedProv)
	out, err := handler(context.Background(), `{"query":"blacksmith"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var results []memory.ContextResult
	if err := json.Unmarshal([]byte(out), &results); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 result, got %d", len(results))
	}

	// Should prefer QueryWithEmbedding when embeddings available.
	if graph.CallCount("QueryWithEmbedding") != 1 {
		t.Errorf("QueryWithEmbedding called %d times, want 1", graph.CallCount("QueryWithEmbedding"))
	}
	if graph.CallCount("QueryWithContext") != 0 {
		t.Errorf("QueryWithContext called %d times, want 0 (should prefer embeddings)", graph.CallCount("QueryWithContext"))
	}
}

func TestSearchGraph_EmbedFailureFallsBackToFTS(t *testing.T) {
	t.Parallel()
	graph := &mock.GraphRAGQuerier{
		QueryWithContextResult: []memory.ContextResult{
			{Entity: memory.Entity{ID: "loc-1", Name: "Tavern"}, Content: "A dimly lit tavern", Score: 0.7},
		},
	}
	embedProv := &embmock.Provider{
		EmbedErr: errors.New("embedding model unavailable"),
	}

	handler := makeSearchGraphHandler(graph, embedProv)
	out, err := handler(context.Background(), `{"query":"tavern"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var results []memory.ContextResult
	if err := json.Unmarshal([]byte(out), &results); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 result, got %d", len(results))
	}

	// Should fall back to QueryWithContext after embed failure.
	if graph.CallCount("QueryWithContext") != 1 {
		t.Errorf("QueryWithContext called %d times, want 1 (fallback)", graph.CallCount("QueryWithContext"))
	}
}

func TestSearchGraph_PlainKnowledgeGraphReturnsError(t *testing.T) {
	t.Parallel()
	// Plain KnowledgeGraph does not implement GraphRAGQuerier.
	graph := &mock.KnowledgeGraph{}

	handler := makeSearchGraphHandler(graph, nil)
	_, err := handler(context.Background(), `{"query":"anything"}`)
	if err == nil {
		t.Error("expected error for non-GraphRAGQuerier graph")
	}
	if !strings.Contains(err.Error(), "does not support GraphRAG") {
		t.Errorf("error %q should mention GraphRAG not supported", err.Error())
	}
}

func TestSearchGraph_EmptyQuery(t *testing.T) {
	t.Parallel()
	graph := &mock.GraphRAGQuerier{}

	handler := makeSearchGraphHandler(graph, nil)
	_, err := handler(context.Background(), `{"query":""}`)
	if err == nil {
		t.Error("expected error for empty query")
	}
}

func TestSearchGraph_BadJSON(t *testing.T) {
	t.Parallel()
	graph := &mock.GraphRAGQuerier{}

	handler := makeSearchGraphHandler(graph, nil)
	_, err := handler(context.Background(), `{bad json}`)
	if err == nil {
		t.Error("expected error for bad JSON")
	}
}
