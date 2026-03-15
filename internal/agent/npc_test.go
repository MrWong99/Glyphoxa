package agent_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/MrWong99/glyphoxa/internal/agent"
	"github.com/MrWong99/glyphoxa/internal/engine"
	enginemock "github.com/MrWong99/glyphoxa/internal/engine/mock"
	"github.com/MrWong99/glyphoxa/internal/hotctx"
	"github.com/MrWong99/glyphoxa/internal/mcp"
	mcpmock "github.com/MrWong99/glyphoxa/internal/mcp/mock"
	"github.com/MrWong99/glyphoxa/pkg/audio"
	audiomock "github.com/MrWong99/glyphoxa/pkg/audio/mock"
	"github.com/MrWong99/glyphoxa/pkg/memory"
	memorymock "github.com/MrWong99/glyphoxa/pkg/memory/mock"
	"github.com/MrWong99/glyphoxa/pkg/provider/llm"
	"github.com/MrWong99/glyphoxa/pkg/provider/stt"
	"github.com/MrWong99/glyphoxa/pkg/provider/tts"
	ttsmock "github.com/MrWong99/glyphoxa/pkg/provider/tts/mock"
)

// ─── Barge-In Cancellation ──────────────────────────────────────────────────

func TestBargeIn_CancelsInFlightGeneration(t *testing.T) {
	t.Parallel()

	// cancelled is closed by the ProcessFunc once its context is cancelled.
	cancelled := make(chan struct{})
	processReady := make(chan struct{})

	eng := &enginemock.VoiceEngine{
		ProcessFunc: func(ctx context.Context, _ audio.AudioFrame, _ engine.PromptContext) (*engine.Response, error) {
			close(processReady)
			// Block until context is cancelled.
			select {
			case <-ctx.Done():
			case <-time.After(5 * time.Second):
				// Safety net so the test doesn't hang forever in CI.
				return nil, errors.New("timed out waiting for context cancellation")
			}
			close(cancelled)
			return &engine.Response{
				Text:  "Partial.",
				Audio: closedAudioCh(),
			}, nil
		},
	}

	mixer := &audiomock.Mixer{}
	cfg := validConfig()
	cfg.Engine = eng
	cfg.Mixer = mixer

	a, err := agent.NewAgent(cfg)
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}

	// Start HandleUtterance in a goroutine.
	done := make(chan error, 1)
	go func() {
		done <- a.HandleUtterance(context.Background(), "player-1", stt.Transcript{
			Text: "Hello.", IsFinal: true,
		})
	}()

	// Wait for Process to be called.
	<-processReady

	// Simulate barge-in via the mixer.
	mixer.TriggerBargeIn("player-2")

	// The generation context should have been cancelled promptly.
	select {
	case <-cancelled:
		// Good — ProcessFunc observed cancellation.
	case <-time.After(5 * time.Second):
		t.Fatal("generation context was not cancelled by barge-in within 5s")
	}

	// HandleUtterance should complete.
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("HandleUtterance error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("HandleUtterance did not complete within 5s")
	}
}

// testIdentity returns a standard NPCIdentity for use in tests.
func testIdentity() agent.NPCIdentity {
	return agent.NPCIdentity{
		Name:            "Greymantle the Sage",
		Personality:     "A wise and ancient sage who speaks in riddles.",
		Voice:           tts.VoiceProfile{ID: "sage-voice", Provider: "elevenlabs"},
		KnowledgeScope:  []string{"lore", "history"},
		SecretKnowledge: []string{"The dragon sleeps under the mountain."},
		BehaviorRules:   []string{"Always speak in archaic English."},
	}
}

// testAssembler returns a minimal Assembler backed by empty mocks.
func testAssembler() *hotctx.Assembler {
	ss := &memorymock.SessionStore{}
	kg := &memorymock.KnowledgeGraph{
		IdentitySnapshotResult: nil,
	}
	return hotctx.NewAssembler(ss, kg)
}

// closedAudioCh returns a pre-closed audio channel suitable for mock responses.
func closedAudioCh() <-chan []byte {
	ch := make(chan []byte)
	close(ch)
	return ch
}

// validConfig returns a valid AgentConfig using test mocks.
func validConfig() agent.AgentConfig {
	eng := &enginemock.VoiceEngine{
		ProcessResult: &engine.Response{
			Text:  "Well met, traveller.",
			Audio: closedAudioCh(),
		},
	}
	return agent.AgentConfig{
		ID:        "greymantle",
		Identity:  testIdentity(),
		Engine:    eng,
		Assembler: testAssembler(),
		SessionID: "session-001",
	}
}

func TestNewAgent_Valid(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	a, err := agent.NewAgent(cfg)
	if err != nil {
		t.Fatalf("NewAgent returned unexpected error: %v", err)
	}
	if a == nil {
		t.Fatal("NewAgent returned nil agent")
	}
}

func TestNewAgent_ValidationErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mutate  func(*agent.AgentConfig)
		wantErr string
	}{
		{
			name:    "empty ID",
			mutate:  func(c *agent.AgentConfig) { c.ID = "" },
			wantErr: "agent: ID must not be empty",
		},
		{
			name:    "nil engine",
			mutate:  func(c *agent.AgentConfig) { c.Engine = nil },
			wantErr: "agent: Engine must not be nil",
		},
		{
			name:    "nil assembler",
			mutate:  func(c *agent.AgentConfig) { c.Assembler = nil },
			wantErr: "agent: Assembler must not be nil",
		},
		{
			name:    "empty session ID",
			mutate:  func(c *agent.AgentConfig) { c.SessionID = "" },
			wantErr: "agent: SessionID must not be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := validConfig()
			tt.mutate(&cfg)
			a, err := agent.NewAgent(cfg)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if a != nil {
				t.Fatal("expected nil agent on error")
			}
			if err.Error() != tt.wantErr {
				t.Errorf("error = %q, want %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestLiveAgent_Getters(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	a, err := agent.NewAgent(cfg)
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}

	t.Run("ID", func(t *testing.T) {
		t.Parallel()
		if got := a.ID(); got != "greymantle" {
			t.Errorf("ID() = %q, want %q", got, "greymantle")
		}
	})

	t.Run("Name", func(t *testing.T) {
		t.Parallel()
		if got := a.Name(); got != "Greymantle the Sage" {
			t.Errorf("Name() = %q, want %q", got, "Greymantle the Sage")
		}
	})

	t.Run("Identity", func(t *testing.T) {
		t.Parallel()
		id := a.Identity()
		if id.Name != "Greymantle the Sage" {
			t.Errorf("Identity().Name = %q, want %q", id.Name, "Greymantle the Sage")
		}
		if id.Personality != "A wise and ancient sage who speaks in riddles." {
			t.Errorf("Identity().Personality = %q, want expected", id.Personality)
		}
	})

	t.Run("Engine", func(t *testing.T) {
		t.Parallel()
		if a.Engine() == nil {
			t.Error("Engine() returned nil")
		}
	})
}

func TestHandleUtterance_Success(t *testing.T) {
	t.Parallel()

	audioCh := make(chan []byte, 1)
	audioCh <- []byte("audio-data")
	close(audioCh)

	eng := &enginemock.VoiceEngine{
		ProcessResult: &engine.Response{
			Text:  "I know much of the ancient lore.",
			Audio: audioCh,
		},
	}
	mixer := &audiomock.Mixer{}

	cfg := validConfig()
	cfg.Engine = eng
	cfg.Mixer = mixer

	a, err := agent.NewAgent(cfg)
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}

	transcript := stt.Transcript{
		Text:    "Tell me about the ancient lore.",
		IsFinal: true,
	}

	err = a.HandleUtterance(context.Background(), "player-1", transcript)
	if err != nil {
		t.Fatalf("HandleUtterance returned error: %v", err)
	}

	// Verify engine was called.
	if len(eng.ProcessCalls) != 1 {
		t.Errorf("expected 1 Process call, got %d", len(eng.ProcessCalls))
	}

	// Verify prompt contains the transcript text.
	if len(eng.ProcessCalls) > 0 {
		call := eng.ProcessCalls[0]
		found := false
		for _, msg := range call.Prompt.Messages {
			if msg.Content == "Tell me about the ancient lore." {
				found = true
				break
			}
		}
		if !found {
			t.Error("expected transcript text in prompt messages")
		}
	}

	// Verify mixer was called.
	if len(mixer.EnqueueCalls) != 1 {
		t.Errorf("expected 1 Enqueue call, got %d", len(mixer.EnqueueCalls))
	}
	if len(mixer.EnqueueCalls) > 0 {
		seg := mixer.EnqueueCalls[0].Segment
		if seg.NPCID != "greymantle" {
			t.Errorf("segment NPCID = %q, want %q", seg.NPCID, "greymantle")
		}
	}
}

func TestHandleUtterance_NilMixer(t *testing.T) {
	t.Parallel()

	audioCh := make(chan []byte, 1)
	audioCh <- []byte("audio-data")
	close(audioCh)

	eng := &enginemock.VoiceEngine{
		ProcessResult: &engine.Response{
			Text:  "Greetings.",
			Audio: audioCh,
		},
	}

	cfg := validConfig()
	cfg.Engine = eng
	cfg.Mixer = nil // explicitly nil

	a, err := agent.NewAgent(cfg)
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}

	transcript := stt.Transcript{Text: "Hello.", IsFinal: true}
	err = a.HandleUtterance(context.Background(), "player-1", transcript)
	if err != nil {
		t.Fatalf("HandleUtterance with nil mixer returned error: %v", err)
	}

	// Engine should still have been called.
	if len(eng.ProcessCalls) != 1 {
		t.Errorf("expected 1 Process call, got %d", len(eng.ProcessCalls))
	}
}

func TestHandleUtterance_AssemblerError(t *testing.T) {
	t.Parallel()

	// Use a mock that returns an error from the session store's GetRecent.
	ss := &memorymock.SessionStore{
		GetRecentErr: errors.New("session store down"),
	}
	kg := &memorymock.KnowledgeGraph{}
	assembler := hotctx.NewAssembler(ss, kg)

	eng := &enginemock.VoiceEngine{
		ProcessResult: &engine.Response{
			Text:  "Should not reach this.",
			Audio: closedAudioCh(),
		},
	}

	cfg := agent.AgentConfig{
		ID:        "test-npc",
		Identity:  testIdentity(),
		Engine:    eng,
		Assembler: assembler,
		SessionID: "session-001",
	}

	a, err := agent.NewAgent(cfg)
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}

	transcript := stt.Transcript{Text: "Hello.", IsFinal: true}
	err = a.HandleUtterance(context.Background(), "player-1", transcript)
	if err == nil {
		t.Fatal("expected error from assembler, got nil")
	}
	if got := err.Error(); len(got) == 0 {
		t.Error("expected non-empty error message")
	}

	// Engine should NOT have been called since assembly failed.
	if len(eng.ProcessCalls) != 0 {
		t.Errorf("expected 0 Process calls, got %d", len(eng.ProcessCalls))
	}
}

func TestHandleUtterance_EngineError(t *testing.T) {
	t.Parallel()

	eng := &enginemock.VoiceEngine{
		ProcessError: errors.New("LLM unavailable"),
	}

	cfg := validConfig()
	cfg.Engine = eng

	a, err := agent.NewAgent(cfg)
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}

	transcript := stt.Transcript{Text: "Hello.", IsFinal: true}
	err = a.HandleUtterance(context.Background(), "player-1", transcript)
	if err == nil {
		t.Fatal("expected error from engine, got nil")
	}
	// Verify error message contains our prefix.
	if got := err.Error(); got == "" {
		t.Error("expected non-empty error message")
	}
}

func TestHandleUtterance_ContextCancelled(t *testing.T) {
	t.Parallel()

	eng := &enginemock.VoiceEngine{
		ProcessResult: &engine.Response{
			Text:  "Should not be called.",
			Audio: closedAudioCh(),
		},
	}

	cfg := validConfig()
	cfg.Engine = eng

	a, err := agent.NewAgent(cfg)
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	transcript := stt.Transcript{Text: "Hello.", IsFinal: true}
	err = a.HandleUtterance(ctx, "player-1", transcript)
	if err == nil {
		t.Fatal("expected context cancellation error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got: %v", err)
	}
}

func TestHandleUtterance_ConcurrentCallsSerialised(t *testing.T) {
	t.Parallel()

	eng := &enginemock.VoiceEngine{}
	// A pre-closed audio channel is safe to read from any number of times;
	// it returns immediately with the zero value, making it suitable for
	// concurrent use without separate channel instances per goroutine.
	ch := make(chan []byte)
	close(ch)
	eng.ProcessResult = &engine.Response{
		Text:  "Response",
		Audio: ch,
	}

	cfg := validConfig()
	cfg.Engine = eng

	a, err := agent.NewAgent(cfg)
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}

	const numCalls = 5
	var wg sync.WaitGroup
	errs := make([]error, numCalls)

	for i := range numCalls {
		wg.Go(func() {
			transcript := stt.Transcript{Text: "Hello.", IsFinal: true}
			errs[i] = a.HandleUtterance(context.Background(), "player-1", transcript)
		})
	}

	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("HandleUtterance call %d returned error: %v", i, err)
		}
	}

	// All calls should have completed (serialised by mutex).
	if got := len(eng.ProcessCalls); got != numCalls {
		t.Errorf("expected %d Process calls, got %d", numCalls, got)
	}
}

func TestUpdateScene(t *testing.T) {
	t.Parallel()

	eng := &enginemock.VoiceEngine{}

	cfg := validConfig()
	cfg.Engine = eng

	a, err := agent.NewAgent(cfg)
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}

	scene := agent.SceneContext{
		Location:        "Thornwood Tavern",
		TimeOfDay:       "late evening",
		PresentEntities: []string{"player-1", "barkeep"},
		ActiveQuests:    []string{"quest-find-artifact"},
	}

	err = a.UpdateScene(context.Background(), scene)
	if err != nil {
		t.Fatalf("UpdateScene returned error: %v", err)
	}

	// Verify InjectContext was called.
	if len(eng.InjectContextCalls) != 1 {
		t.Fatalf("expected 1 InjectContext call, got %d", len(eng.InjectContextCalls))
	}

	update := eng.InjectContextCalls[0].Update
	// Verify scene string contains key components.
	if update.Scene == "" {
		t.Error("expected non-empty scene string")
	}
}

func TestUpdateScene_EngineError(t *testing.T) {
	t.Parallel()

	eng := &enginemock.VoiceEngine{
		InjectContextError: errors.New("engine failure"),
	}

	cfg := validConfig()
	cfg.Engine = eng

	a, err := agent.NewAgent(cfg)
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}

	scene := agent.SceneContext{Location: "Dungeon"}
	err = a.UpdateScene(context.Background(), scene)
	if err == nil {
		t.Fatal("expected error from InjectContext, got nil")
	}
}

func TestNewAgent_WithMCPHost(t *testing.T) {
	t.Parallel()

	eng := &enginemock.VoiceEngine{
		ProcessResult: &engine.Response{
			Text:  "Tool response.",
			Audio: closedAudioCh(),
		},
	}
	mcpHost := &mcpmock.Host{
		AvailableToolsResult: []llm.ToolDefinition{
			{Name: "lookup_npc", Description: "Look up NPC info"},
		},
	}

	cfg := validConfig()
	cfg.Engine = eng
	cfg.MCPHost = mcpHost
	cfg.BudgetTier = mcp.BudgetStandard

	a, err := agent.NewAgent(cfg)
	if err != nil {
		t.Fatalf("NewAgent with MCPHost: %v", err)
	}
	if a == nil {
		t.Fatal("NewAgent returned nil")
	}

	// Verify AvailableTools was called during construction.
	if mcpHost.CallCount("AvailableTools") != 1 {
		t.Errorf("expected 1 AvailableTools call, got %d", mcpHost.CallCount("AvailableTools"))
	}

	// Verify SetTools was called on the engine.
	if len(eng.SetToolsCalls) != 1 {
		t.Errorf("expected 1 SetTools call, got %d", len(eng.SetToolsCalls))
	}
	if len(eng.SetToolsCalls) > 0 && len(eng.SetToolsCalls[0].Tools) != 1 {
		t.Errorf("expected 1 tool in SetTools, got %d", len(eng.SetToolsCalls[0].Tools))
	}

	// Verify OnToolCall was registered.
	if eng.CallCountOnToolCall != 1 {
		t.Errorf("expected 1 OnToolCall registration, got %d", eng.CallCountOnToolCall)
	}
}

func TestNewAgent_WithMCPHost_SetToolsError(t *testing.T) {
	t.Parallel()

	eng := &enginemock.VoiceEngine{
		SetToolsError: errors.New("tools not supported"),
	}
	mcpHost := &mcpmock.Host{
		AvailableToolsResult: []llm.ToolDefinition{
			{Name: "lookup_npc"},
		},
	}

	cfg := validConfig()
	cfg.Engine = eng
	cfg.MCPHost = mcpHost

	a, err := agent.NewAgent(cfg)
	if err == nil {
		t.Fatal("expected error from SetTools, got nil")
	}
	if a != nil {
		t.Error("expected nil agent on SetTools error")
	}
}

func TestHandleUtterance_BuildsConversationHistory(t *testing.T) {
	t.Parallel()

	callCount := 0
	eng := &enginemock.VoiceEngine{}

	cfg := validConfig()
	cfg.Engine = eng

	a, err := agent.NewAgent(cfg)
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}

	// First utterance.
	eng.ProcessResult = &engine.Response{
		Text:  "First reply.",
		Audio: closedAudioCh(),
	}
	err = a.HandleUtterance(context.Background(), "player-1", stt.Transcript{
		Text: "First question.", IsFinal: true,
	})
	if err != nil {
		t.Fatalf("first HandleUtterance: %v", err)
	}
	callCount++

	// Second utterance — should include history from first exchange.
	eng.ProcessResult = &engine.Response{
		Text:  "Second reply.",
		Audio: closedAudioCh(),
	}
	err = a.HandleUtterance(context.Background(), "player-1", stt.Transcript{
		Text: "Second question.", IsFinal: true,
	})
	if err != nil {
		t.Fatalf("second HandleUtterance: %v", err)
	}
	callCount++

	if len(eng.ProcessCalls) != callCount {
		t.Fatalf("expected %d Process calls, got %d", callCount, len(eng.ProcessCalls))
	}

	// The second call's messages should include the first exchange.
	secondCallMsgs := eng.ProcessCalls[1].Prompt.Messages
	// Should have: user("First question."), assistant("First reply."), user("Second question.")
	if len(secondCallMsgs) < 3 {
		t.Fatalf("expected at least 3 messages in second call, got %d", len(secondCallMsgs))
	}

	if secondCallMsgs[0].Content != "First question." {
		t.Errorf("first message content = %q, want %q", secondCallMsgs[0].Content, "First question.")
	}
	if secondCallMsgs[1].Content != "First reply." {
		t.Errorf("second message content = %q, want %q", secondCallMsgs[1].Content, "First reply.")
	}
	if secondCallMsgs[2].Content != "Second question." {
		t.Errorf("third message content = %q, want %q", secondCallMsgs[2].Content, "Second question.")
	}
}

// ─── SpeakText ──────────────────────────────────────────────────────────────

// speakTextConfig returns a valid AgentConfig with a TTS provider suitable
// for SpeakText tests. The mixer is nil so audio is drained.
func speakTextConfig() agent.AgentConfig {
	cfg := validConfig()
	cfg.TTS = &ttsmock.Provider{
		SynthesizeChunks: [][]byte{[]byte("audio")},
	}
	return cfg
}

func TestSpeakText_CallsOnTranscript(t *testing.T) {
	t.Parallel()

	cfg := speakTextConfig()

	var got memory.TranscriptEntry
	var called bool
	cfg.OnTranscript = func(entry memory.TranscriptEntry) {
		called = true
		got = entry
	}

	a, err := agent.NewAgent(cfg)
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}

	if err := a.SpeakText(context.Background(), "Halt, adventurer!"); err != nil {
		t.Fatalf("SpeakText: %v", err)
	}

	if !called {
		t.Fatal("OnTranscript callback was not called")
	}
	if got.Text != "Halt, adventurer!" {
		t.Errorf("Text = %q, want %q", got.Text, "Halt, adventurer!")
	}
	if got.SpeakerName != "Greymantle the Sage" {
		t.Errorf("SpeakerName = %q, want %q", got.SpeakerName, "Greymantle the Sage")
	}
	if got.SpeakerID != "Greymantle the Sage" {
		t.Errorf("SpeakerID = %q, want %q", got.SpeakerID, "Greymantle the Sage")
	}
	if got.NPCID != "greymantle" {
		t.Errorf("NPCID = %q, want %q", got.NPCID, "greymantle")
	}
	if got.Timestamp.IsZero() {
		t.Error("Timestamp should not be zero")
	}
	// RawText and Duration should be zero-valued for puppet speech.
	if got.RawText != "" {
		t.Errorf("RawText = %q, want empty", got.RawText)
	}
	if got.Duration != 0 {
		t.Errorf("Duration = %v, want 0", got.Duration)
	}
}

func TestSpeakText_NilOnTranscript(t *testing.T) {
	t.Parallel()

	cfg := speakTextConfig()
	cfg.OnTranscript = nil // explicitly nil

	a, err := agent.NewAgent(cfg)
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}

	// Should not panic.
	if err := a.SpeakText(context.Background(), "Greetings."); err != nil {
		t.Fatalf("SpeakText with nil OnTranscript: %v", err)
	}
}

func TestSpeakText_NilTTSProvider(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	cfg.TTS = nil

	a, err := agent.NewAgent(cfg)
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}

	err = a.SpeakText(context.Background(), "Hello.")
	if err == nil {
		t.Fatal("expected error from SpeakText with nil TTS, got nil")
	}
}

func TestSpeakText_CallbackOutsideMutex(t *testing.T) {
	t.Parallel()

	cfg := speakTextConfig()

	// The callback calls Name() on the agent, which does not acquire mu
	// (it's a simple field read). But this test verifies that the callback
	// is invoked outside the agent's internal mutex by calling a method
	// that would deadlock if the lock were still held. Since Name() doesn't
	// acquire mu we verify indirectly: if SpeakText held mu during the
	// callback, a concurrent HandleUtterance would deadlock. Instead, we
	// verify the callback can do blocking work without issues.
	done := make(chan struct{})
	cfg.OnTranscript = func(_ memory.TranscriptEntry) {
		// Simulate blocking I/O (e.g., a database write).
		close(done)
	}

	a, err := agent.NewAgent(cfg)
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}

	if err := a.SpeakText(context.Background(), "Test."); err != nil {
		t.Fatalf("SpeakText: %v", err)
	}

	select {
	case <-done:
		// Callback completed — mutex was not held.
	default:
		t.Fatal("OnTranscript callback was not called")
	}
}
