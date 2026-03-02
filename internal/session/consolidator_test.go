package session

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MrWong99/glyphoxa/pkg/memory"
	memorymock "github.com/MrWong99/glyphoxa/pkg/memory/mock"
	embedmock "github.com/MrWong99/glyphoxa/pkg/provider/embeddings/mock"
	"github.com/MrWong99/glyphoxa/pkg/provider/llm"
)

func TestConsolidator_ConsolidateNow(t *testing.T) {
	t.Run("writes new messages to store", func(t *testing.T) {
		store := &memorymock.SessionStore{}
		s := &mockSummariser{result: "summary"}
		cm := NewContextManager(ContextManagerConfig{
			MaxTokens:  100000,
			Summariser: s,
		})

		_ = cm.AddMessages(context.Background(),
			llm.Message{Role: "user", Name: "Player1", Content: "I attack the goblin!"},
			llm.Message{Role: "assistant", Name: "Grek", Content: "The goblin dodges!"},
		)

		c := NewConsolidator(ConsolidatorConfig{
			Store:      store,
			ContextMgr: cm,
			SessionID:  "session-1",
		})

		err := c.ConsolidateNow(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		writeCount := store.CallCount("WriteEntry")
		if writeCount != 2 {
			t.Errorf("expected 2 WriteEntry calls, got %d", writeCount)
		}
	})

	t.Run("does not re-write already consolidated messages", func(t *testing.T) {
		store := &memorymock.SessionStore{}
		s := &mockSummariser{result: "summary"}
		cm := NewContextManager(ContextManagerConfig{
			MaxTokens:  100000,
			Summariser: s,
		})

		_ = cm.AddMessages(context.Background(),
			llm.Message{Role: "user", Content: "First message"},
		)

		c := NewConsolidator(ConsolidatorConfig{
			Store:      store,
			ContextMgr: cm,
			SessionID:  "session-1",
		})

		_ = c.ConsolidateNow(context.Background())
		firstCount := store.CallCount("WriteEntry")

		// Consolidate again without new messages — should not write.
		store.Reset()
		_ = c.ConsolidateNow(context.Background())
		secondCount := store.CallCount("WriteEntry")

		if secondCount != 0 {
			t.Errorf("expected 0 writes on second consolidation, got %d (first had %d)", secondCount, firstCount)
		}
	})

	t.Run("writes only new messages on subsequent consolidation", func(t *testing.T) {
		store := &memorymock.SessionStore{}
		s := &mockSummariser{result: "summary"}
		cm := NewContextManager(ContextManagerConfig{
			MaxTokens:  100000,
			Summariser: s,
		})

		_ = cm.AddMessages(context.Background(),
			llm.Message{Role: "user", Content: "First"},
		)

		c := NewConsolidator(ConsolidatorConfig{
			Store:      store,
			ContextMgr: cm,
			SessionID:  "session-1",
		})

		_ = c.ConsolidateNow(context.Background())
		store.Reset()

		_ = cm.AddMessages(context.Background(),
			llm.Message{Role: "user", Content: "Second"},
			llm.Message{Role: "assistant", Content: "Reply"},
		)

		_ = c.ConsolidateNow(context.Background())
		if store.CallCount("WriteEntry") != 2 {
			t.Errorf("expected 2 writes for new messages, got %d", store.CallCount("WriteEntry"))
		}
	})

	t.Run("skips summary messages", func(t *testing.T) {
		store := &memorymock.SessionStore{}
		s := &mockSummariser{result: "condensed history"}
		cm := NewContextManager(ContextManagerConfig{
			MaxTokens:      40,
			ThresholdRatio: 0.5,
			Summariser:     s,
		})

		// Force summarisation by exceeding threshold.
		_ = cm.AddMessages(context.Background(),
			llm.Message{Role: "user", Content: strings.Repeat("a", 80)},
			llm.Message{Role: "assistant", Content: strings.Repeat("b", 80)},
		)

		c := NewConsolidator(ConsolidatorConfig{
			Store:      store,
			ContextMgr: cm,
			SessionID:  "session-1",
		})

		_ = c.ConsolidateNow(context.Background())

		// Verify that summary messages (starting with '[') are skipped.
		calls := store.Calls()
		for _, call := range calls {
			if call.Method == "WriteEntry" && len(call.Args) > 1 {
				entry, ok := call.Args[1].(memory.TranscriptEntry)
				if ok && len(entry.Text) > 0 && entry.Text[0] == '[' {
					t.Errorf("summary message should not be written to store, got: %s", entry.Text)
				}
			}
		}
	})
}

func TestConsolidator_L2Indexing(t *testing.T) {
	t.Parallel()

	t.Run("indexes chunks when semantic and embed provider configured", func(t *testing.T) {
		t.Parallel()

		store := &memorymock.SessionStore{}
		semantic := &memorymock.SemanticIndex{}
		embedProv := &embedmock.Provider{
			EmbedBatchResult: [][]float32{{0.1, 0.2}, {0.3, 0.4}},
		}
		s := &mockSummariser{result: "summary"}
		cm := NewContextManager(ContextManagerConfig{
			MaxTokens:  100000,
			Summariser: s,
		})

		_ = cm.AddMessages(context.Background(),
			llm.Message{Role: "user", Name: "Alice", Content: "Hello"},
			llm.Message{Role: "assistant", Name: "Grek", Content: "Welcome!"},
		)

		c := NewConsolidator(ConsolidatorConfig{
			Store:         store,
			ContextMgr:    cm,
			SessionID:     "session-l2",
			SemanticIndex: semantic,
			EmbedProvider: embedProv,
		})

		err := c.ConsolidateNow(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// L1 writes should still happen.
		if store.CallCount("WriteEntry") != 2 {
			t.Errorf("expected 2 WriteEntry calls, got %d", store.CallCount("WriteEntry"))
		}

		// EmbedBatch should have been called once with both texts.
		if len(embedProv.EmbedBatchCalls) != 1 {
			t.Fatalf("expected 1 EmbedBatch call, got %d", len(embedProv.EmbedBatchCalls))
		}
		if len(embedProv.EmbedBatchCalls[0].Texts) != 2 {
			t.Errorf("expected 2 texts in EmbedBatch, got %d", len(embedProv.EmbedBatchCalls[0].Texts))
		}

		// IndexChunk should have been called for each entry.
		if semantic.CallCount("IndexChunk") != 2 {
			t.Errorf("expected 2 IndexChunk calls, got %d", semantic.CallCount("IndexChunk"))
		}
	})

	t.Run("skips L2 when semantic index is nil", func(t *testing.T) {
		t.Parallel()

		store := &memorymock.SessionStore{}
		embedProv := &embedmock.Provider{}
		s := &mockSummariser{result: "summary"}
		cm := NewContextManager(ContextManagerConfig{
			MaxTokens:  100000,
			Summariser: s,
		})

		_ = cm.AddMessages(context.Background(),
			llm.Message{Role: "user", Content: "Hello"},
		)

		c := NewConsolidator(ConsolidatorConfig{
			Store:         store,
			ContextMgr:    cm,
			SessionID:     "session-no-l2",
			SemanticIndex: nil,
			EmbedProvider: embedProv,
		})

		err := c.ConsolidateNow(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// L1 writes should succeed.
		if store.CallCount("WriteEntry") != 1 {
			t.Errorf("expected 1 WriteEntry call, got %d", store.CallCount("WriteEntry"))
		}

		// No embedding should have been attempted.
		if len(embedProv.EmbedBatchCalls) != 0 {
			t.Errorf("expected 0 EmbedBatch calls, got %d", len(embedProv.EmbedBatchCalls))
		}
	})

	t.Run("skips L2 when embed provider is nil", func(t *testing.T) {
		t.Parallel()

		store := &memorymock.SessionStore{}
		semantic := &memorymock.SemanticIndex{}
		s := &mockSummariser{result: "summary"}
		cm := NewContextManager(ContextManagerConfig{
			MaxTokens:  100000,
			Summariser: s,
		})

		_ = cm.AddMessages(context.Background(),
			llm.Message{Role: "user", Content: "Hello"},
		)

		c := NewConsolidator(ConsolidatorConfig{
			Store:         store,
			ContextMgr:    cm,
			SessionID:     "session-no-embed",
			SemanticIndex: semantic,
			EmbedProvider: nil,
		})

		err := c.ConsolidateNow(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if store.CallCount("WriteEntry") != 1 {
			t.Errorf("expected 1 WriteEntry call, got %d", store.CallCount("WriteEntry"))
		}

		if semantic.CallCount("IndexChunk") != 0 {
			t.Errorf("expected 0 IndexChunk calls, got %d", semantic.CallCount("IndexChunk"))
		}
	})

	t.Run("L1 succeeds even when embedding fails", func(t *testing.T) {
		t.Parallel()

		store := &memorymock.SessionStore{}
		semantic := &memorymock.SemanticIndex{}
		embedProv := &embedmock.Provider{
			EmbedBatchErr: errors.New("model unavailable"),
		}
		s := &mockSummariser{result: "summary"}
		cm := NewContextManager(ContextManagerConfig{
			MaxTokens:  100000,
			Summariser: s,
		})

		_ = cm.AddMessages(context.Background(),
			llm.Message{Role: "user", Content: "Hello"},
		)

		c := NewConsolidator(ConsolidatorConfig{
			Store:         store,
			ContextMgr:    cm,
			SessionID:     "session-embed-fail",
			SemanticIndex: semantic,
			EmbedProvider: embedProv,
		})

		err := c.ConsolidateNow(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// L1 writes must succeed despite embedding failure.
		if store.CallCount("WriteEntry") != 1 {
			t.Errorf("expected 1 WriteEntry call, got %d", store.CallCount("WriteEntry"))
		}

		// No chunks should be indexed since embedding failed.
		if semantic.CallCount("IndexChunk") != 0 {
			t.Errorf("expected 0 IndexChunk calls, got %d", semantic.CallCount("IndexChunk"))
		}
	})

	t.Run("L1 succeeds even when IndexChunk fails", func(t *testing.T) {
		t.Parallel()

		store := &memorymock.SessionStore{}
		semantic := &memorymock.SemanticIndex{
			IndexChunkErr: errors.New("index write failed"),
		}
		embedProv := &embedmock.Provider{
			EmbedBatchResult: [][]float32{{0.1, 0.2}},
		}
		s := &mockSummariser{result: "summary"}
		cm := NewContextManager(ContextManagerConfig{
			MaxTokens:  100000,
			Summariser: s,
		})

		_ = cm.AddMessages(context.Background(),
			llm.Message{Role: "user", Content: "Hello"},
		)

		c := NewConsolidator(ConsolidatorConfig{
			Store:         store,
			ContextMgr:    cm,
			SessionID:     "session-index-fail",
			SemanticIndex: semantic,
			EmbedProvider: embedProv,
		})

		err := c.ConsolidateNow(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// L1 writes must succeed.
		if store.CallCount("WriteEntry") != 1 {
			t.Errorf("expected 1 WriteEntry call, got %d", store.CallCount("WriteEntry"))
		}

		// IndexChunk was attempted (and failed, but that's best-effort).
		if semantic.CallCount("IndexChunk") != 1 {
			t.Errorf("expected 1 IndexChunk call, got %d", semantic.CallCount("IndexChunk"))
		}
	})
}

func TestConsolidator_DefaultInterval(t *testing.T) {
	c := NewConsolidator(ConsolidatorConfig{
		Store:      &memorymock.SessionStore{},
		ContextMgr: NewContextManager(ContextManagerConfig{MaxTokens: 1000, Summariser: &mockSummariser{}}),
		SessionID:  "s1",
	})
	if c.interval != 30*time.Minute {
		t.Errorf("expected default interval of 30m, got %v", c.interval)
	}
}

func TestConsolidator_StartStop(t *testing.T) {
	store := &memorymock.SessionStore{}
	s := &mockSummariser{result: "summary"}
	cm := NewContextManager(ContextManagerConfig{
		MaxTokens:  100000,
		Summariser: s,
	})

	c := NewConsolidator(ConsolidatorConfig{
		Store:      store,
		ContextMgr: cm,
		SessionID:  "session-1",
		Interval:   10 * time.Millisecond, // very short for testing
	})

	_ = cm.AddMessages(context.Background(),
		llm.Message{Role: "user", Content: "Hello"},
	)

	ctx := t.Context()

	c.Start(ctx)

	// Wait long enough for at least one tick.
	time.Sleep(50 * time.Millisecond)

	c.Stop()

	// Should have written at least once.
	if store.CallCount("WriteEntry") == 0 {
		t.Error("expected at least one periodic consolidation")
	}

	// Calling Stop again should not panic.
	c.Stop()
}
