package session_test

import (
	"context"
	"testing"
	"time"

	"github.com/MrWong99/glyphoxa/internal/mcp/tools/memorytool"
	"github.com/MrWong99/glyphoxa/internal/session"
	"github.com/MrWong99/glyphoxa/pkg/memory"
	"github.com/MrWong99/glyphoxa/pkg/memory/mock"
	embmock "github.com/MrWong99/glyphoxa/pkg/provider/embeddings/mock"
	"github.com/MrWong99/glyphoxa/pkg/provider/llm"
)

// noopSummariser satisfies session.Summariser without doing anything.
type noopSummariser struct{}

func (noopSummariser) Summarise(_ context.Context, _ []llm.Message) (string, error) {
	return "[summary]", nil
}

// TestIntegration_WritePathToQueryPath verifies the full round-trip:
//
//  1. Messages are written via the consolidator (L1 write + L2 embed + index).
//  2. The MCP search_facts tool finds results via semantic search (L2).
//  3. The MCP search_graph tool finds results via GraphRAG.
//  4. Graceful degradation: nil embeddings skips L2 cleanly.
func TestIntegration_WritePathToQueryPath(t *testing.T) {
	t.Parallel()

	const sessionID = "session-test"

	t.Run("write path populates L2 and query path finds results", func(t *testing.T) {
		t.Parallel()

		store := &mock.SessionStore{}
		semIdx := &mock.SemanticIndex{}
		embedProv := &embmock.Provider{
			EmbedResult:      []float32{0.1, 0.2, 0.3},
			EmbedBatchResult: [][]float32{{0.1, 0.2, 0.3}, {0.4, 0.5, 0.6}},
		}

		ctxMgr := session.NewContextManager(session.ContextManagerConfig{
			MaxTokens:  100000,
			Summariser: noopSummariser{},
		})
		_ = ctxMgr.AddMessages(context.Background(),
			llm.Message{Role: "user", Name: "Alice", Content: "Tell me about the ancient forge"},
			llm.Message{Role: "assistant", Name: "Grimjaw", Content: "The ancient forge was built by dwarven smiths centuries ago"},
		)

		consolidator := session.NewConsolidator(session.ConsolidatorConfig{
			Store:         store,
			ContextMgr:    ctxMgr,
			SessionID:     sessionID,
			SemanticIndex: semIdx,
			EmbedProvider: embedProv,
		})

		if err := consolidator.ConsolidateNow(context.Background()); err != nil {
			t.Fatalf("ConsolidateNow() error = %v", err)
		}

		// Verify L1 writes happened.
		if n := store.CallCount("WriteEntry"); n != 2 {
			t.Errorf("WriteEntry called %d times, want 2", n)
		}

		// Verify L2 indexing happened.
		if n := semIdx.CallCount("IndexChunk"); n != 2 {
			t.Errorf("IndexChunk called %d times, want 2", n)
		}

		// Verify embeddings were requested.
		if len(embedProv.EmbedBatchCalls) != 1 {
			t.Errorf("EmbedBatch called %d times, want 1", len(embedProv.EmbedBatchCalls))
		}

		// ── Query path: search_facts with semantic search ───────────────
		semIdx.SearchResult = []memory.ChunkResult{
			{
				Chunk: memory.Chunk{
					SpeakerID: "Grimjaw",
					Content:   "The ancient forge was built by dwarven smiths centuries ago",
					Timestamp: time.Now(),
				},
				Distance: 0.1,
			},
		}

		tools := memorytool.NewTools(store, semIdx, nil, embedProv)
		var searchFactsTool func(ctx context.Context, args string) (string, error)
		for _, tool := range tools {
			if tool.Definition.Name == "search_facts" {
				searchFactsTool = tool.Handler
				break
			}
		}
		if searchFactsTool == nil {
			t.Fatal("search_facts tool not found")
		}

		out, err := searchFactsTool(context.Background(), `{"query":"ancient forge"}`)
		if err != nil {
			t.Fatalf("search_facts error: %v", err)
		}
		if out == "" || out == "null" || out == "[]" {
			t.Error("search_facts returned empty result")
		}
	})

	t.Run("search_graph returns entity-anchored results", func(t *testing.T) {
		t.Parallel()

		graph := &mock.GraphRAGQuerier{
			QueryWithContextResult: []memory.ContextResult{
				{
					Entity:  memory.Entity{ID: "loc-1", Name: "The Ancient Forge"},
					Content: "Built by dwarven smiths, the forge has burned for centuries",
					Score:   0.9,
				},
			},
		}

		tools := memorytool.NewTools(&mock.SessionStore{}, nil, graph, nil)
		var searchGraphTool func(ctx context.Context, args string) (string, error)
		for _, tool := range tools {
			if tool.Definition.Name == "search_graph" {
				searchGraphTool = tool.Handler
				break
			}
		}
		if searchGraphTool == nil {
			t.Fatal("search_graph tool not found")
		}

		out, err := searchGraphTool(context.Background(), `{"query":"ancient forge","scope":["loc-1"]}`)
		if err != nil {
			t.Fatalf("search_graph error: %v", err)
		}
		if out == "" || out == "null" || out == "[]" {
			t.Error("search_graph returned empty result")
		}
	})

	t.Run("nil embeddings degrades gracefully", func(t *testing.T) {
		t.Parallel()

		store := &mock.SessionStore{}
		semIdx := &mock.SemanticIndex{}

		ctxMgr := session.NewContextManager(session.ContextManagerConfig{
			MaxTokens:  100000,
			Summariser: noopSummariser{},
		})
		_ = ctxMgr.AddMessages(context.Background(),
			llm.Message{Role: "user", Name: "Alice", Content: "Hello there"},
		)

		consolidator := session.NewConsolidator(session.ConsolidatorConfig{
			Store:         store,
			ContextMgr:    ctxMgr,
			SessionID:     sessionID,
			SemanticIndex: semIdx,
			EmbedProvider: nil, // nil → skip L2
		})

		if err := consolidator.ConsolidateNow(context.Background()); err != nil {
			t.Fatalf("ConsolidateNow() error = %v", err)
		}

		// L1 should still succeed.
		if n := store.CallCount("WriteEntry"); n != 1 {
			t.Errorf("WriteEntry called %d times, want 1", n)
		}

		// L2 should be skipped entirely.
		if n := semIdx.CallCount("IndexChunk"); n != 0 {
			t.Errorf("IndexChunk called %d times, want 0 (L2 skipped)", n)
		}
	})
}
