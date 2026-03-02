// White-box tests for SessionManager.recordTranscripts.
// This file is in package app (not app_test) so that it can call the
// unexported method directly.
package app

import (
	"context"
	"testing"
	"time"

	agentmock "github.com/MrWong99/glyphoxa/internal/agent/mock"
	"github.com/MrWong99/glyphoxa/internal/config"
	enginemock "github.com/MrWong99/glyphoxa/internal/engine/mock"
	audiomock "github.com/MrWong99/glyphoxa/pkg/audio/mock"
	"github.com/MrWong99/glyphoxa/pkg/memory"
	memorymock "github.com/MrWong99/glyphoxa/pkg/memory/mock"
)

// newTestSM creates a minimal SessionManager suitable for unit-testing
// recordTranscripts.
func newTestSM(store *memorymock.SessionStore) *SessionManager {
	return NewSessionManager(SessionManagerConfig{
		Platform:     &audiomock.Platform{ConnectResult: &audiomock.Connection{}},
		Config:       &config.Config{},
		Providers:    &Providers{},
		SessionStore: store,
	})
}

// TestRecordTranscripts_NormalExit verifies that entries sent before channel
// close are all written to the session store.
func TestRecordTranscripts_NormalExit(t *testing.T) {
	t.Parallel()

	store := &memorymock.SessionStore{}
	sm := newTestSM(store)

	ch := make(chan memory.TranscriptEntry, 3)
	eng := &enginemock.VoiceEngine{TranscriptsResult: ch}
	ag := &agentmock.NPCAgent{
		IDResult:     "npc-0-test",
		NameResult:   "TestNPC",
		EngineResult: eng,
	}

	entry1 := memory.TranscriptEntry{SpeakerName: "TestNPC", Text: "Hello, adventurer!", Timestamp: time.Now()}
	entry2 := memory.TranscriptEntry{SpeakerName: "Player", Text: "Well met!", Timestamp: time.Now()}

	ch <- entry1
	ch <- entry2
	close(ch) // closing triggers the ok==false exit path

	ctx := context.Background()
	sm.recordTranscripts(ctx, ag, "session-normal")

	calls := store.Calls()
	if len(calls) != 2 {
		t.Fatalf("WriteEntry calls = %d, want 2", len(calls))
	}
	if got := calls[0].Args[0].(string); got != "session-normal" {
		t.Errorf("call[0] sessionID = %q, want %q", got, "session-normal")
	}
	if got := calls[0].Args[1].(memory.TranscriptEntry).Text; got != entry1.Text {
		t.Errorf("call[0] text = %q, want %q", got, entry1.Text)
	}
	if got := calls[1].Args[1].(memory.TranscriptEntry).Text; got != entry2.Text {
		t.Errorf("call[1] text = %q, want %q", got, entry2.Text)
	}
}

// TestRecordTranscripts_DrainOnCancel verifies that when the context is
// cancelled, buffered entries that have not yet been read are still drained
// and written to the session store before the function returns.
func TestRecordTranscripts_DrainOnCancel(t *testing.T) {
	t.Parallel()

	store := &memorymock.SessionStore{}
	sm := newTestSM(store)

	ch := make(chan memory.TranscriptEntry, 3)
	eng := &enginemock.VoiceEngine{TranscriptsResult: ch}
	ag := &agentmock.NPCAgent{
		IDResult:     "npc-0-drain",
		NameResult:   "DrainNPC",
		EngineResult: eng,
	}

	entry1 := memory.TranscriptEntry{SpeakerName: "DrainNPC", Text: "Pre-cancel entry 1"}
	entry2 := memory.TranscriptEntry{SpeakerName: "DrainNPC", Text: "Pre-cancel entry 2"}

	// Pre-load the buffered channel.
	ch <- entry1
	ch <- entry2
	// Close the channel so the drain loop (for entry := range ch) can exit.
	close(ch)

	// Cancel the context before calling recordTranscripts.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// recordTranscripts must return and have written both entries regardless
	// of whether select picks ctx.Done() or a buffered entry first.
	sm.recordTranscripts(ctx, ag, "session-drain")

	calls := store.Calls()
	if len(calls) != 2 {
		t.Fatalf("WriteEntry calls on drain = %d, want 2", len(calls))
	}
	// The set of written texts should match, regardless of ordering.
	texts := map[string]bool{}
	for _, c := range calls {
		texts[c.Args[1].(memory.TranscriptEntry).Text] = true
	}
	if !texts[entry1.Text] {
		t.Errorf("missing entry %q in WriteEntry calls", entry1.Text)
	}
	if !texts[entry2.Text] {
		t.Errorf("missing entry %q in WriteEntry calls", entry2.Text)
	}
}

// TestRecordTranscripts_EmptyChannel verifies that recordTranscripts returns
// immediately when the channel is already closed and empty.
func TestRecordTranscripts_EmptyChannel(t *testing.T) {
	t.Parallel()

	store := &memorymock.SessionStore{}
	sm := newTestSM(store)

	ch := make(chan memory.TranscriptEntry)
	close(ch)
	eng := &enginemock.VoiceEngine{TranscriptsResult: ch}
	ag := &agentmock.NPCAgent{
		IDResult:     "npc-0-empty",
		NameResult:   "EmptyNPC",
		EngineResult: eng,
	}

	sm.recordTranscripts(context.Background(), ag, "session-empty")

	if n := store.CallCount("WriteEntry"); n != 0 {
		t.Errorf("WriteEntry calls = %d, want 0", n)
	}
}

// TestRecordTranscripts_NilStore_Skipped verifies the wiring in Start():
// recorder goroutines are only spawned when sessionStore is non-nil.
// (With nil store, no goroutines are started and Stop() should not block.)
func TestRecordTranscripts_SessionManagerStartStop_NoNPCs(t *testing.T) {
	t.Parallel()

	// newTestSM has a non-nil store but no NPCs → recorderWG never incremented.
	store := &memorymock.SessionStore{}
	sm := newTestSM(store)

	ctx := context.Background()
	if err := sm.Start(ctx, "ch-1", "user-1"); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	if err := sm.Stop(ctx); err != nil {
		t.Fatalf("Stop() error: %v", err)
	}
	// No WriteEntry calls expected (no NPCs means no agents, no goroutines).
	if n := store.CallCount("WriteEntry"); n != 0 {
		t.Errorf("WriteEntry calls = %d, want 0", n)
	}
}
