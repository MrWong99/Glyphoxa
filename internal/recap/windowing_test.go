package recap

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
)

func bigLines(n, chars int) []storage.TranscriptLine {
	lines := make([]storage.TranscriptLine, n)
	for i := range lines {
		lines[i] = storage.TranscriptLine{Seq: int64(i + 1), Who: "GM", Text: strings.Repeat("x", chars)}
	}
	return lines
}

// TestWindowedMapReduce: an over-budget session takes the map-reduce path — N map
// calls plus one reduce — and reports Windowed=true.
func TestWindowedMapReduce(t *testing.T) {
	st := newFakeStore()
	tenantID := uuid.New()
	butler := storage.Agent{Role: storage.AgentRoleButler, Persona: "Butler."}
	// 30 lines of ~1000 chars => ~30k rendered > singleCallBudgetChars(24k); windows of
	// <=20k chars => 2 windows => 2 maps + 1 reduce = 3 calls.
	sid := seedSession(st, tenantID, uuid.New(), "English", butler, time.Now(), bigLines(30, 1000))

	var calls int
	factory := func(_, _ string) (llm.Provider, error) {
		return &stubProvider{text: "partial", capture: func(llm.Request) { calls++ }}, nil
	}
	eng := NewEngine(st, nil, observe.Discard{}, nil, WithProviderFactory(factory))
	res, err := eng.Recap(context.Background(), []uuid.UUID{sid})
	if err != nil {
		t.Fatalf("Recap: %v", err)
	}
	if !res.Windowed {
		t.Error("Windowed = false, want true for an over-budget session")
	}
	if calls != 3 {
		t.Errorf("provider calls = %d, want 3 (2 map + 1 reduce)", calls)
	}
}

// TestTooManyWindows: a transcript needing more than maxWindows map windows fails
// loudly rather than fanning out unbounded calls.
func TestTooManyWindows(t *testing.T) {
	st := newFakeStore()
	tenantID := uuid.New()
	butler := storage.Agent{Role: storage.AgentRoleButler, Persona: "Butler."}
	// 33 lines each longer than windowChars => each is its own window => 33 > maxWindows(32).
	sid := seedSession(st, tenantID, uuid.New(), "English", butler, time.Now(), bigLines(maxWindows+1, windowChars+10))

	eng := NewEngine(st, nil, observe.Discard{}, nil, WithProviderFactory(okFactory))
	if _, err := eng.Recap(context.Background(), []uuid.UUID{sid}); err != ErrTranscriptTooLong {
		t.Fatalf("err = %v, want ErrTranscriptTooLong", err)
	}
}

// TestStreamError: a mid-stream EventError fails the recap — never returns truncated
// text as a complete recap.
func TestStreamError(t *testing.T) {
	st := newFakeStore()
	tenantID := uuid.New()
	butler := storage.Agent{Role: storage.AgentRoleButler, Persona: "Butler."}
	sid := seedSession(st, tenantID, uuid.New(), "English", butler, time.Now(), sampleLines())

	eng := NewEngine(st, nil, observe.Discard{}, nil, WithProviderFactory(func(_, _ string) (llm.Provider, error) {
		return &stubProvider{text: "partial cut off", errText: "boom"}, nil
	}))
	_, err := eng.Recap(context.Background(), []uuid.UUID{sid})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v, want stream error wrapping boom", err)
	}
}

// TestTruncatedStream: a stream that closes without EventDone fails the recap.
func TestTruncatedStream(t *testing.T) {
	st := newFakeStore()
	tenantID := uuid.New()
	butler := storage.Agent{Role: storage.AgentRoleButler, Persona: "Butler."}
	sid := seedSession(st, tenantID, uuid.New(), "English", butler, time.Now(), sampleLines())

	eng := NewEngine(st, nil, observe.Discard{}, nil, WithProviderFactory(func(_, _ string) (llm.Provider, error) {
		return &stubProvider{text: "partial", noDone: true}, nil
	}))
	if _, err := eng.Recap(context.Background(), []uuid.UUID{sid}); err == nil {
		t.Fatal("err = nil, want a truncation error for a stream missing EventDone")
	}
}
