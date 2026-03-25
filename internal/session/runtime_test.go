package session

import (
	"context"
	"testing"

	"github.com/MrWong99/glyphoxa/internal/agent"
	agentmock "github.com/MrWong99/glyphoxa/internal/agent/mock"
	enginemock "github.com/MrWong99/glyphoxa/internal/engine/mock"
	audiomock "github.com/MrWong99/glyphoxa/pkg/audio/mock"
	"github.com/MrWong99/glyphoxa/pkg/memory"
	memorymock "github.com/MrWong99/glyphoxa/pkg/memory/mock"
)

func TestRuntime_StartStop(t *testing.T) {
	t.Parallel()

	rt := NewRuntime(RuntimeConfig{
		SessionID: "test-session",
	})

	ctx := context.Background()

	if err := rt.Start(ctx, nil); err != nil {
		t.Fatalf("start: %v", err)
	}

	if rt.SessionID() != "test-session" {
		t.Errorf("got session ID %q, want %q", rt.SessionID(), "test-session")
	}

	if err := rt.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestRuntime_DoubleStart(t *testing.T) {
	t.Parallel()

	rt := NewRuntime(RuntimeConfig{SessionID: "test"})
	ctx := context.Background()

	if err := rt.Start(ctx, nil); err != nil {
		t.Fatalf("first start: %v", err)
	}
	defer func() { _ = rt.Stop(ctx) }()

	if err := rt.Start(ctx, nil); err == nil {
		t.Fatal("expected error on double start")
	}
}

func TestRuntime_StopNotRunning(t *testing.T) {
	t.Parallel()

	rt := NewRuntime(RuntimeConfig{SessionID: "test"})
	ctx := context.Background()

	if err := rt.Stop(ctx); err == nil {
		t.Fatal("expected error on stop when not running")
	}
}

func TestRuntime_ClosersCalledInReverse(t *testing.T) {
	t.Parallel()

	rt := NewRuntime(RuntimeConfig{SessionID: "test"})

	var order []int
	rt.AddCloser(func() error { order = append(order, 1); return nil })
	rt.AddCloser(func() error { order = append(order, 2); return nil })
	rt.AddCloser(func() error { order = append(order, 3); return nil })

	ctx := context.Background()
	if err := rt.Start(ctx, nil); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := rt.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}

	if len(order) != 3 {
		t.Fatalf("got %d closers called, want 3", len(order))
	}
	if order[0] != 3 || order[1] != 2 || order[2] != 1 {
		t.Errorf("closers called in wrong order: %v, want [3 2 1]", order)
	}
}

func TestRuntime_Agents(t *testing.T) {
	t.Parallel()

	npc1 := &agentmock.NPCAgent{IDResult: "npc-1", NameResult: "Gandalf"}
	npc2 := &agentmock.NPCAgent{IDResult: "npc-2", NameResult: "Frodo"}

	rt := NewRuntime(RuntimeConfig{
		SessionID: "test-agents",
		Agents:    []agent.NPCAgent{npc1, npc2},
	})

	agents := rt.Agents()
	if len(agents) != 2 {
		t.Fatalf("got %d agents, want 2", len(agents))
	}
	if agents[0].Name() != "Gandalf" {
		t.Errorf("agents[0].Name() = %q, want Gandalf", agents[0].Name())
	}
	if agents[1].Name() != "Frodo" {
		t.Errorf("agents[1].Name() = %q, want Frodo", agents[1].Name())
	}
}

func TestRuntime_Agents_Empty(t *testing.T) {
	t.Parallel()

	rt := NewRuntime(RuntimeConfig{
		SessionID: "test-agents-empty",
	})

	agents := rt.Agents()
	if agents != nil {
		t.Errorf("expected nil agents, got %v", agents)
	}
}

func TestRuntime_Flush_WithFlusher(t *testing.T) {
	t.Parallel()

	conn := &flushableConnection{}
	rt := NewRuntime(RuntimeConfig{
		SessionID:  "test-flush",
		Connection: conn,
	})

	rt.Flush()

	if conn.flushCount != 1 {
		t.Errorf("expected 1 Flush call, got %d", conn.flushCount)
	}
}

func TestRuntime_Flush_WithoutFlusher(t *testing.T) {
	t.Parallel()

	// Regular mock.Connection does not implement audio.Flusher.
	conn := &audiomock.Connection{}
	rt := NewRuntime(RuntimeConfig{
		SessionID:  "test-flush-noop",
		Connection: conn,
	})

	// Should not panic.
	rt.Flush()
}

func TestRuntime_Flush_NilConnection(t *testing.T) {
	t.Parallel()

	rt := NewRuntime(RuntimeConfig{
		SessionID: "test-flush-nil",
	})

	// Should not panic.
	rt.Flush()
}

// flushableConnection is a mock audio.Connection that also implements audio.Flusher.
type flushableConnection struct {
	audiomock.Connection
	flushCount int
}

func (f *flushableConnection) Flush() {
	f.flushCount++
}

func TestRuntime_RecordTranscripts(t *testing.T) {
	t.Parallel()

	// Create a transcript channel and feed entries into it.
	transcriptCh := make(chan memory.TranscriptEntry, 4)
	transcriptCh <- memory.TranscriptEntry{SpeakerName: "Player", Text: "Hello"}
	transcriptCh <- memory.TranscriptEntry{SpeakerName: "Gandalf", Text: "Well met"}

	eng := &enginemock.VoiceEngine{
		TranscriptsResult: transcriptCh,
	}
	npc := &agentmock.NPCAgent{
		IDResult:     "npc-1",
		NameResult:   "Gandalf",
		EngineResult: eng,
	}

	store := &memorymock.SessionStore{}

	rt := NewRuntime(RuntimeConfig{
		SessionID: "transcript-test",
		Agents:    []agent.NPCAgent{npc},
	})

	ctx := context.Background()
	if err := rt.Start(ctx, store); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Close the transcript channel so the recorder goroutine finishes
	// after draining the buffer.
	close(transcriptCh)

	// Stop waits for recorderWG to finish.
	if err := rt.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}

	// The store should have received 2 WriteEntry calls.
	if got := store.CallCount("WriteEntry"); got != 2 {
		t.Errorf("expected 2 WriteEntry calls, got %d", got)
	}
}
