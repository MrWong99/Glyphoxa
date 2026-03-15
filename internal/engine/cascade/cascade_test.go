package cascade_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	enginepkg "github.com/MrWong99/glyphoxa/internal/engine"
	"github.com/MrWong99/glyphoxa/internal/engine/cascade"
	"github.com/MrWong99/glyphoxa/pkg/audio"
	"github.com/MrWong99/glyphoxa/pkg/memory"
	"github.com/MrWong99/glyphoxa/pkg/provider/llm"
	llmmock "github.com/MrWong99/glyphoxa/pkg/provider/llm/mock"
	"github.com/MrWong99/glyphoxa/pkg/provider/tts"
	ttsmock "github.com/MrWong99/glyphoxa/pkg/provider/tts/mock"
)

// ─── helpers ─────────────────────────────────────────────────────────────────

// drainAudio reads the audio channel to completion so engine goroutines are
// not left blocked.
func drainAudio(ch <-chan []byte) {
	for range ch {
	}
}

// signalDone notifies the engine that playback completed naturally.
// Must be called after drainAudio for tests that check transcript emission.
func signalDone(resp *enginepkg.Response) {
	if resp.NotifyDone != nil {
		resp.NotifyDone <- false
	}
}

// newTTS returns a TTS mock that emits a single "audio" chunk per call.
func newTTS() *ttsmock.Provider {
	return &ttsmock.Provider{
		SynthesizeChunks: [][]byte{[]byte("audio")},
	}
}

// emptyAudioFrame is a zero-value audio frame used in tests that do not
// exercise the STT path.
var emptyAudioFrame = audio.AudioFrame{}

// ─── TestFirstSentenceBoundary ────────────────────────────────────────────────

// TestFirstSentenceBoundary exercises the sentence-boundary heuristic directly
// via the exported FirstSentenceBoundaryForTest helper.
func TestFirstSentenceBoundary(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input string
		want  int // expected index of boundary, -1 if none
	}{
		// ── Basic boundaries ──────────────────────────────────────────────
		{name: "period space", input: "Hello. World", want: 5},
		{name: "exclamation space", input: "Stop! Who goes", want: 4},
		{name: "question space", input: "What? Really", want: 4},
		{name: "period newline", input: "End.\nStart", want: 3},
		{name: "period tab", input: "Done.\tNext", want: 4},
		{name: "no boundary", input: "Hello world", want: -1},
		{name: "period at end no space", input: "Hello.", want: -1},
		{name: "empty string", input: "", want: -1},

		// ── Abbreviations (should NOT split) ─────────────────────────────
		{name: "Dr abbreviation", input: "Dr. Smith is here. Welcome", want: 17},
		{name: "Mr abbreviation", input: "Mr. Jones arrived. Hello", want: 17},
		{name: "Mrs abbreviation", input: "Mrs. Lee spoke. Listen", want: 14},
		{name: "Ms abbreviation", input: "Ms. Park said hello. OK", want: 19},
		{name: "St abbreviation", input: "St. Elmo is burning. Run", want: 19},
		{name: "Lt abbreviation", input: "Lt. Dan fought. Hard", want: 14},
		{name: "Prof abbreviation", input: "Prof. Oak knows. Much", want: 15},
		{name: "vs abbreviation", input: "Red vs. Blue is fun. Yeah", want: 19},
		{name: "no followed by period", input: "Say no. Then leave", want: 6},
		{name: "single letter abbreviation", input: "J. R. R. Tolkien wrote. Lots", want: 22},
		{name: "only abbreviation no real boundary", input: "Dr. Smith", want: -1},

		// ── Decimal numbers (should NOT split) ──────────────────────────
		{name: "decimal number", input: "Costs 2.5 gold. Pay up", want: 14},
		{name: "decimal in sentence", input: "The scroll costs 3.14 coins. Buy it", want: 27},
		{name: "only decimal no boundary", input: "The value is 2.5 gold", want: -1},

		// ── Ellipses (should NOT split) ──────────────────────────────────
		{name: "ellipsis ASCII", input: "But then... it happened. Wow", want: 23},
		{name: "ellipsis at end", input: "Hmm...", want: -1},
		{name: "ellipsis mid sentence", input: "Wait... no. Stop", want: 10},
		{name: "unicode ellipsis", input: "Hmm\u2026 something. OK", want: 16},
		{name: "unicode ellipsis only", input: "Hmm\u2026 something", want: -1},

		// ── Mixed cases ──────────────────────────────────────────────────
		{name: "abbreviation then real boundary", input: "Dr. Smith said hello. Then left", want: 20},
		{name: "decimal then real boundary", input: "It costs 2.5 gold. Pay up now", want: 17},
		{name: "multiple abbreviations then boundary", input: "Dr. J. Smith arrived. Welcome", want: 20},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := cascade.FirstSentenceBoundaryForTest(tc.input)
			if got != tc.want {
				t.Errorf("firstSentenceBoundary(%q) = %d, want %d", tc.input, got, tc.want)
			}
		})
	}
}

// ─── TestProcess_FastModelOnly ────────────────────────────────────────────────

// TestProcess_FastModelOnly verifies that when the fast model returns a response
// that ends with a finish reason (no sentence boundary detected before stream end),
// the strong model is never called.
func TestProcess_FastModelOnly(t *testing.T) {
	t.Parallel()

	fastLLM := &llmmock.Provider{
		StreamChunks: []llm.Chunk{
			{Text: "Well met, traveller.", FinishReason: "stop"},
		},
	}
	strongLLM := &llmmock.Provider{}
	ttsProv := newTTS()

	e := cascade.New(fastLLM, strongLLM, ttsProv, tts.VoiceProfile{})
	t.Cleanup(func() { _ = e.Close() })

	resp, err := e.Process(context.Background(), emptyAudioFrame, enginepkg.PromptContext{
		SystemPrompt: "You are an innkeeper.",
	})
	if err != nil {
		t.Fatalf("Process: unexpected error: %v", err)
	}
	drainAudio(resp.Audio)
	signalDone(resp)
	e.Wait()

	// Fast model must have been called exactly once.
	if len(fastLLM.StreamCalls) != 1 {
		t.Errorf("fastLLM StreamCompletion calls: want 1, got %d", len(fastLLM.StreamCalls))
	}
	// Strong model must NOT have been called.
	if len(strongLLM.StreamCalls) != 0 {
		t.Errorf("strongLLM StreamCompletion calls: want 0, got %d", len(strongLLM.StreamCalls))
	}
	// TTS must have been invoked exactly once.
	if len(ttsProv.SynthesizeStreamCalls) != 1 {
		t.Errorf("TTS SynthesizeStream calls: want 1, got %d", len(ttsProv.SynthesizeStreamCalls))
	}
	// Response text should be the fast model's output.
	if resp.Text != "Well met, traveller." {
		t.Errorf("resp.Text: want %q, got %q", "Well met, traveller.", resp.Text)
	}
	if err := resp.Err(); err != nil {
		t.Errorf("resp.Err(): unexpected error: %v", err)
	}
}

// ─── TestProcess_DualModel ────────────────────────────────────────────────────

// TestProcess_DualModel verifies that when the fast model emits a sentence
// boundary (punctuation followed by a space), both the fast and the strong model
// are called, and TTS receives the merged stream.
func TestProcess_DualModel(t *testing.T) {
	t.Parallel()

	fastLLM := &llmmock.Provider{
		StreamChunks: []llm.Chunk{
			// "! " triggers a sentence boundary → opener = "Ah, traveller!"
			{Text: "Ah, traveller! "},
			// This chunk is drained in the background (never used by the engine).
			{Text: "and more text", FinishReason: "stop"},
		},
	}
	strongLLM := &llmmock.Provider{
		StreamChunks: []llm.Chunk{
			{Text: "What brings you here?", FinishReason: "stop"},
		},
	}
	ttsProv := newTTS()

	e := cascade.New(fastLLM, strongLLM, ttsProv, tts.VoiceProfile{})
	t.Cleanup(func() { _ = e.Close() })

	resp, err := e.Process(context.Background(), emptyAudioFrame, enginepkg.PromptContext{
		SystemPrompt: "You are a guild master.",
	})
	if err != nil {
		t.Fatalf("Process: unexpected error: %v", err)
	}
	drainAudio(resp.Audio)
	signalDone(resp)
	e.Wait()

	if len(fastLLM.StreamCalls) != 1 {
		t.Errorf("fastLLM StreamCompletion calls: want 1, got %d", len(fastLLM.StreamCalls))
	}
	if len(strongLLM.StreamCalls) != 1 {
		t.Errorf("strongLLM StreamCompletion calls: want 1, got %d", len(strongLLM.StreamCalls))
	}
	if len(ttsProv.SynthesizeStreamCalls) != 1 {
		t.Errorf("TTS SynthesizeStream calls: want 1, got %d", len(ttsProv.SynthesizeStreamCalls))
	}
	if resp.Err() != nil {
		t.Errorf("resp.Err(): unexpected error: %v", resp.Err())
	}
}

// ─── TestProcess_OpenerSentenceDetection ─────────────────────────────────────

// TestProcess_OpenerSentenceDetection verifies the sentence-boundary heuristic
// across a range of common NPC speech patterns.
func TestProcess_OpenerSentenceDetection(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		fastChunks   []llm.Chunk
		wantOpener   string
		wantFastFull bool // true → strong model should NOT be called
	}{
		{
			name: "exclamation with trailing space",
			fastChunks: []llm.Chunk{
				{Text: "Hello! "},
				{Text: "Come in.", FinishReason: "stop"},
			},
			wantOpener:   "Hello!",
			wantFastFull: false,
		},
		{
			name: "period with trailing space",
			fastChunks: []llm.Chunk{
				{Text: "The blacksmith strokes his beard. "},
				{Text: "Then he speaks.", FinishReason: "stop"},
			},
			wantOpener:   "The blacksmith strokes his beard.",
			wantFastFull: false,
		},
		{
			name: "question mark with trailing space",
			fastChunks: []llm.Chunk{
				{Text: "What do you seek? "},
				{Text: "Speak.", FinishReason: "stop"},
			},
			wantOpener:   "What do you seek?",
			wantFastFull: false,
		},
		{
			name: "single sentence finish reason no boundary",
			fastChunks: []llm.Chunk{
				{Text: "Indeed.", FinishReason: "stop"},
			},
			wantOpener:   "Indeed.",
			wantFastFull: true,
		},
		{
			name: "multi-token single sentence",
			fastChunks: []llm.Chunk{
				{Text: "Greet"},
				{Text: "ings, friend.", FinishReason: "stop"},
			},
			wantOpener:   "Greetings, friend.",
			wantFastFull: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fastLLM := &llmmock.Provider{StreamChunks: tc.fastChunks}
			strongLLM := &llmmock.Provider{
				StreamChunks: []llm.Chunk{
					{Text: " continuation.", FinishReason: "stop"},
				},
			}
			ttsProv := newTTS()

			e := cascade.New(fastLLM, strongLLM, ttsProv, tts.VoiceProfile{})
			t.Cleanup(func() { _ = e.Close() })

			resp, err := e.Process(context.Background(), emptyAudioFrame, enginepkg.PromptContext{
				SystemPrompt: "NPC persona.",
			})
			if err != nil {
				t.Fatalf("Process: %v", err)
			}
			drainAudio(resp.Audio)
			signalDone(resp)
			e.Wait()

			// Verify opener text.
			if resp.Text != tc.wantOpener {
				t.Errorf("resp.Text: want %q, got %q", tc.wantOpener, resp.Text)
			}

			// Verify whether strong model was called.
			strongCalled := len(strongLLM.StreamCalls) > 0
			if tc.wantFastFull && strongCalled {
				t.Error("strong model was called but fast model response was complete (fastFull=true)")
			}
			if !tc.wantFastFull && !strongCalled {
				t.Error("strong model was not called but fast model returned a sentence boundary (fastFull=false)")
			}

			// If dual-model, the strong model's first request message must end with
			// the opener as an assistant prefix.
			if !tc.wantFastFull && strongCalled {
				calls := strongLLM.StreamCalls
				msgs := calls[0].Req.Messages
				if len(msgs) == 0 {
					t.Fatal("strong model received empty messages slice")
				}
				last := msgs[len(msgs)-1]
				if last.Role != "assistant" {
					t.Errorf("last message role: want %q, got %q", "assistant", last.Role)
				}
				if last.Content != tc.wantOpener {
					t.Errorf("last message content: want %q, got %q", tc.wantOpener, last.Content)
				}
			}
		})
	}
}

// ─── TestProcess_ForcedPrefix ────────────────────────────────────────────────

// TestProcess_ForcedPrefix verifies that the strong model receives the opener
// as an assistant-role message appended after the conversation history, acting
// as a forced continuation prefix.
func TestProcess_ForcedPrefix(t *testing.T) {
	t.Parallel()

	fastLLM := &llmmock.Provider{
		StreamChunks: []llm.Chunk{
			// "! " triggers a sentence boundary.
			{Text: "Ah, the artifact! "},
			{Text: "remaining", FinishReason: "stop"},
		},
	}
	strongLLM := &llmmock.Provider{
		StreamChunks: []llm.Chunk{
			{Text: "It was forged long ago.", FinishReason: "stop"},
		},
	}
	ttsProv := newTTS()

	history := []llm.Message{
		{Role: "user", Content: "Tell me about the artifact."},
	}

	e := cascade.New(fastLLM, strongLLM, ttsProv, tts.VoiceProfile{})
	t.Cleanup(func() { _ = e.Close() })

	resp, err := e.Process(context.Background(), emptyAudioFrame, enginepkg.PromptContext{
		SystemPrompt: "You are a wise sage.",
		Messages:     history,
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	drainAudio(resp.Audio)
	signalDone(resp)
	e.Wait()

	if len(strongLLM.StreamCalls) != 1 {
		t.Fatalf("strong model calls: want 1, got %d", len(strongLLM.StreamCalls))
	}

	req := strongLLM.StreamCalls[0].Req

	// The request must contain the original history plus the opener prefix.
	wantMsgCount := len(history) + 1
	if len(req.Messages) != wantMsgCount {
		t.Fatalf("strong model message count: want %d, got %d", wantMsgCount, len(req.Messages))
	}

	// Last message must be the opener as an assistant role.
	last := req.Messages[len(req.Messages)-1]
	if last.Role != "assistant" {
		t.Errorf("forced-prefix role: want %q, got %q", "assistant", last.Role)
	}
	wantOpener := "Ah, the artifact!"
	if last.Content != wantOpener {
		t.Errorf("forced-prefix content: want %q, got %q", wantOpener, last.Content)
	}

	// Original history must be preserved before the prefix.
	if req.Messages[0].Content != history[0].Content {
		t.Errorf("history[0] content: want %q, got %q", history[0].Content, req.Messages[0].Content)
	}
}

// ─── TestProcess_FastModelInstructionAppended ─────────────────────────────────

// TestProcess_FastModelInstructionAppended verifies that the opener instruction
// is appended to the fast model's system prompt and that the strong model's
// system prompt does NOT contain it.
func TestProcess_FastModelInstructionAppended(t *testing.T) {
	t.Parallel()

	const customSuffix = "CUSTOM_OPENER_INSTRUCTION"

	fastLLM := &llmmock.Provider{
		StreamChunks: []llm.Chunk{
			{Text: "Greetings! "},
			{Text: "Welcome.", FinishReason: "stop"},
		},
	}
	strongLLM := &llmmock.Provider{
		StreamChunks: []llm.Chunk{
			{Text: "How can I help?", FinishReason: "stop"},
		},
	}
	ttsProv := newTTS()

	e := cascade.New(
		fastLLM, strongLLM, ttsProv, tts.VoiceProfile{},
		cascade.WithOpenerPromptSuffix(customSuffix),
	)
	t.Cleanup(func() { _ = e.Close() })

	resp, err := e.Process(context.Background(), emptyAudioFrame, enginepkg.PromptContext{
		SystemPrompt: "You are an NPC.",
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	drainAudio(resp.Audio)
	signalDone(resp)
	e.Wait()

	if len(fastLLM.StreamCalls) == 0 {
		t.Fatal("fast model was not called")
	}
	fastSysPrompt := fastLLM.StreamCalls[0].Req.SystemPrompt
	if !strings.Contains(fastSysPrompt, customSuffix) {
		t.Errorf("fast model system prompt does not contain opener instruction %q; got: %q", customSuffix, fastSysPrompt)
	}
	if !strings.Contains(fastSysPrompt, "You are an NPC.") {
		t.Errorf("fast model system prompt missing original system prompt; got: %q", fastSysPrompt)
	}

	// The strong model's system prompt must NOT contain the opener instruction.
	if len(strongLLM.StreamCalls) > 0 {
		strongSysPrompt := strongLLM.StreamCalls[0].Req.SystemPrompt
		if strings.Contains(strongSysPrompt, customSuffix) {
			t.Errorf("strong model system prompt must not contain opener instruction, got: %q", strongSysPrompt)
		}
	}
}

// ─── TestInjectContext_StoresUpdate ──────────────────────────────────────────

// TestInjectContext_StoresUpdate verifies that a context update injected via
// InjectContext is applied on the next Process call and consumed thereafter.
func TestInjectContext_StoresUpdate(t *testing.T) {
	t.Parallel()

	fastLLM := &llmmock.Provider{
		StreamChunks: []llm.Chunk{
			{Text: "Updated greeting.", FinishReason: "stop"},
		},
	}
	strongLLM := &llmmock.Provider{}
	ttsProv := newTTS()

	e := cascade.New(fastLLM, strongLLM, ttsProv, tts.VoiceProfile{})
	t.Cleanup(func() { _ = e.Close() })

	// Inject a context update with a new identity.
	updatedIdentity := "You are now a wizard named Aldric."
	err := e.InjectContext(context.Background(), enginepkg.ContextUpdate{
		Identity: updatedIdentity,
	})
	if err != nil {
		t.Fatalf("InjectContext: %v", err)
	}

	// Process with a different system prompt — the injected identity should win.
	originalPrompt := enginepkg.PromptContext{
		SystemPrompt: "You are an innkeeper.",
	}
	resp, err := e.Process(context.Background(), emptyAudioFrame, originalPrompt)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	drainAudio(resp.Audio)
	signalDone(resp)
	e.Wait()

	if len(fastLLM.StreamCalls) == 0 {
		t.Fatal("fast model was not called")
	}
	sysPrompt := fastLLM.StreamCalls[0].Req.SystemPrompt
	if !strings.Contains(sysPrompt, updatedIdentity) {
		t.Errorf("fast model system prompt: want %q, got %q", updatedIdentity, sysPrompt)
	}

	// Reset call records to test that the update was consumed.
	fastLLM.Reset()

	// Second Process call: update must not be re-applied.
	resp2, err := e.Process(context.Background(), emptyAudioFrame, originalPrompt)
	if err != nil {
		t.Fatalf("second Process: %v", err)
	}
	drainAudio(resp2.Audio)
	signalDone(resp2)
	e.Wait()

	if len(fastLLM.StreamCalls) == 0 {
		t.Fatal("fast model was not called on second Process")
	}
	sysPrompt2 := fastLLM.StreamCalls[0].Req.SystemPrompt
	if !strings.Contains(sysPrompt2, "You are an innkeeper.") {
		t.Errorf("second call: system prompt should revert to original, got %q", sysPrompt2)
	}
}

// ─── TestSetTools_OnlyStrongModel ────────────────────────────────────────────

// TestSetTools_OnlyStrongModel verifies that tools set via SetTools are forwarded
// to the strong model only, and that the fast model never receives them.
func TestSetTools_OnlyStrongModel(t *testing.T) {
	t.Parallel()

	fastLLM := &llmmock.Provider{
		StreamChunks: []llm.Chunk{
			// Boundary triggers dual-model path.
			{Text: "Let me check. "},
			{Text: "One moment.", FinishReason: "stop"},
		},
	}
	strongLLM := &llmmock.Provider{
		StreamChunks: []llm.Chunk{
			{Text: "Here is the answer.", FinishReason: "stop"},
		},
	}
	ttsProv := newTTS()

	e := cascade.New(fastLLM, strongLLM, ttsProv, tts.VoiceProfile{})
	t.Cleanup(func() { _ = e.Close() })

	tools := []llm.ToolDefinition{
		{Name: "query_lore", Description: "Queries the lore database."},
	}
	if err := e.SetTools(tools); err != nil {
		t.Fatalf("SetTools: %v", err)
	}

	resp, err := e.Process(context.Background(), emptyAudioFrame, enginepkg.PromptContext{
		SystemPrompt: "You are a lore keeper.",
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	drainAudio(resp.Audio)
	signalDone(resp)
	e.Wait()

	if len(fastLLM.StreamCalls) == 0 {
		t.Fatal("fast model not called")
	}
	if len(strongLLM.StreamCalls) == 0 {
		t.Fatal("strong model not called")
	}

	// Fast model must receive no tools.
	if len(fastLLM.StreamCalls[0].Req.Tools) != 0 {
		t.Errorf("fast model tools: want 0, got %d", len(fastLLM.StreamCalls[0].Req.Tools))
	}

	// Strong model must receive the configured tools.
	strongTools := strongLLM.StreamCalls[0].Req.Tools
	if len(strongTools) != 1 {
		t.Fatalf("strong model tools: want 1, got %d", len(strongTools))
	}
	if strongTools[0].Name != "query_lore" {
		t.Errorf("strong model tool name: want %q, got %q", "query_lore", strongTools[0].Name)
	}
}

// ─── TestOnToolCall_RegistersHandler ─────────────────────────────────────────

// TestOnToolCall_RegistersHandler verifies that OnToolCall does not panic, can
// be called multiple times (replacing the previous handler each time), and that
// the engine remains functional after registration.
func TestOnToolCall_RegistersHandler(t *testing.T) {
	t.Parallel()

	fastLLM := &llmmock.Provider{
		StreamChunks: []llm.Chunk{
			{Text: "One moment.", FinishReason: "stop"},
		},
	}
	strongLLM := &llmmock.Provider{}
	ttsProv := newTTS()

	e := cascade.New(fastLLM, strongLLM, ttsProv, tts.VoiceProfile{})
	t.Cleanup(func() { _ = e.Close() })

	var callCount int32

	// Register first handler.
	e.OnToolCall(func(name, args string) (string, error) {
		atomic.AddInt32(&callCount, 1)
		return "result-1", nil
	})

	// Register second handler — must replace the first.
	e.OnToolCall(func(name, args string) (string, error) {
		atomic.AddInt32(&callCount, 10)
		return "result-2", nil
	})

	// Engine must still process correctly after handler registration.
	resp, err := e.Process(context.Background(), emptyAudioFrame, enginepkg.PromptContext{
		SystemPrompt: "You are an NPC.",
	})
	if err != nil {
		t.Fatalf("Process after OnToolCall: %v", err)
	}
	drainAudio(resp.Audio)
	signalDone(resp)
	e.Wait()

	// No tool calls were issued by the LLM in this test, so callCount stays 0.
	if n := atomic.LoadInt32(&callCount); n != 0 {
		t.Errorf("tool handler called unexpectedly: count=%d", n)
	}
}

// ─── TestClose_Idempotent ─────────────────────────────────────────────────────

// TestClose_Idempotent verifies that calling Close multiple times is safe and
// always returns nil.
func TestClose_Idempotent(t *testing.T) {
	t.Parallel()

	e := cascade.New(
		&llmmock.Provider{},
		&llmmock.Provider{},
		&ttsmock.Provider{},
		tts.VoiceProfile{},
	)

	for i := range 5 {
		if err := e.Close(); err != nil {
			t.Errorf("Close() call %d: unexpected error: %v", i, err)
		}
	}

	// Transcripts channel must be closed after the first Close.
	ch := e.Transcripts()
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("Transcripts channel was not closed after Close()")
		}
	default:
		// Channel might buffer — read it.
		for range ch {
		}
	}
}

// ─── TestConcurrentProcess ────────────────────────────────────────────────────

// TestConcurrentProcess verifies that concurrent Process calls do not race or
// deadlock. It runs several goroutines calling Process simultaneously and expects
// all of them to succeed.
func TestConcurrentProcess(t *testing.T) {
	t.Parallel()

	const numGoroutines = 8

	fastLLM := &llmmock.Provider{
		StreamChunks: []llm.Chunk{
			{Text: "Hello! "},
			{Text: "Continuation.", FinishReason: "stop"},
		},
	}
	strongLLM := &llmmock.Provider{
		StreamChunks: []llm.Chunk{
			{Text: "The answer is here.", FinishReason: "stop"},
		},
	}
	ttsProv := newTTS()

	e := cascade.New(fastLLM, strongLLM, ttsProv, tts.VoiceProfile{})
	t.Cleanup(func() { _ = e.Close() })

	var wg sync.WaitGroup
	errs := make([]error, numGoroutines)

	for i := range numGoroutines {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			resp, err := e.Process(context.Background(), emptyAudioFrame, enginepkg.PromptContext{
				SystemPrompt: "Concurrent NPC.",
			})
			if err != nil {
				errs[idx] = err
				return
			}
			drainAudio(resp.Audio)
			signalDone(resp)
		}(i)
	}

	wg.Wait()
	e.Wait() // wait for all strong-model goroutines to finish

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: Process error: %v", i, err)
		}
	}
}

// ─── TestTranscripts_ChannelClosedOnClose ────────────────────────────────────

// TestTranscripts_ChannelClosedOnClose is an additional smoke-test verifying
// that the Transcripts channel is consistently the same channel and is closed
// when the engine is closed.
func TestTranscripts_ChannelClosedOnClose(t *testing.T) {
	t.Parallel()

	e := cascade.New(
		&llmmock.Provider{},
		&llmmock.Provider{},
		&ttsmock.Provider{},
		tts.VoiceProfile{},
	)

	ch1 := e.Transcripts()
	ch2 := e.Transcripts()
	if ch1 != ch2 {
		t.Error("Transcripts() must return the same channel on every call")
	}

	if err := e.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Channel must be closed.
	_, ok := <-ch1
	if ok {
		t.Error("Transcripts channel should be closed after Close()")
	}
}

// ─── TestWithTranscriptBuffer ────────────────────────────────────────────────

// TestWithTranscriptBuffer verifies that WithTranscriptBuffer configures the
// channel capacity. We cannot inspect channel capacity directly from outside the
// package, but we can verify that n entries can be sent without blocking by
// publishing n entries from inside (here we just exercise the option and verify
// the engine still builds and runs cleanly).
func TestWithTranscriptBuffer(t *testing.T) {
	t.Parallel()

	e := cascade.New(
		&llmmock.Provider{StreamChunks: []llm.Chunk{{Text: "Hi.", FinishReason: "stop"}}},
		&llmmock.Provider{},
		newTTS(),
		tts.VoiceProfile{},
		cascade.WithTranscriptBuffer(128),
	)
	t.Cleanup(func() { _ = e.Close() })

	resp, err := e.Process(context.Background(), emptyAudioFrame, enginepkg.PromptContext{})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	drainAudio(resp.Audio)
	signalDone(resp)
	e.Wait()
}

// ─── TestProcess_EmitsTranscriptEntries_SingleModel ──────────────────────────

// TestProcess_EmitsTranscriptEntries_SingleModel verifies that the fast-only path
// emits both a player input entry and an NPC response entry to the transcript channel.
func TestProcess_EmitsTranscriptEntries_SingleModel(t *testing.T) {
	t.Parallel()

	fastLLM := &llmmock.Provider{
		StreamChunks: []llm.Chunk{
			{Text: "Well met, traveller.", FinishReason: "stop"},
		},
	}
	strongLLM := &llmmock.Provider{}
	ttsProv := newTTS()

	e := cascade.New(fastLLM, strongLLM, ttsProv, tts.VoiceProfile{})
	ch := e.Transcripts()

	resp, err := e.Process(context.Background(), emptyAudioFrame, enginepkg.PromptContext{
		SystemPrompt: "You are an innkeeper.",
		Messages: []llm.Message{
			{Role: "user", Content: "Hello there!", Name: "player1"},
		},
	})
	if err != nil {
		t.Fatalf("Process: unexpected error: %v", err)
	}
	drainAudio(resp.Audio)
	signalDone(resp)
	e.Wait()
	_ = e.Close()

	var entries []memory.TranscriptEntry
	for entry := range ch {
		entries = append(entries, entry)
	}

	// Expect two entries: player input + NPC response.
	if len(entries) != 2 {
		t.Fatalf("transcript entries: want 2, got %d: %+v", len(entries), entries)
	}

	// First entry: player input.
	if entries[0].Text != "Hello there!" {
		t.Errorf("player entry text: want %q, got %q", "Hello there!", entries[0].Text)
	}
	if entries[0].SpeakerID != "player1" {
		t.Errorf("player entry SpeakerID: want %q, got %q", "player1", entries[0].SpeakerID)
	}

	// Second entry: NPC response.
	if entries[1].Text != "Well met, traveller." {
		t.Errorf("NPC entry text: want %q, got %q", "Well met, traveller.", entries[1].Text)
	}
	if entries[1].Timestamp.IsZero() {
		t.Error("NPC entry Timestamp should not be zero")
	}
}

// ─── TestProcess_EmitsTranscriptEntries_DualModel ─────────────────────────────

// TestProcess_EmitsTranscriptEntries_DualModel verifies that the dual-model path
// emits transcript entries containing the combined opener + strong continuation.
func TestProcess_EmitsTranscriptEntries_DualModel(t *testing.T) {
	t.Parallel()

	fastLLM := &llmmock.Provider{
		StreamChunks: []llm.Chunk{
			// "! " triggers a sentence boundary → opener = "Ah, traveller!"
			{Text: "Ah, traveller! "},
			{Text: "more", FinishReason: "stop"},
		},
	}
	strongLLM := &llmmock.Provider{
		StreamChunks: []llm.Chunk{
			{Text: "What brings you here?", FinishReason: "stop"},
		},
	}
	ttsProv := newTTS()

	e := cascade.New(fastLLM, strongLLM, ttsProv, tts.VoiceProfile{})
	ch := e.Transcripts()

	resp, err := e.Process(context.Background(), emptyAudioFrame, enginepkg.PromptContext{
		SystemPrompt: "You are a guild master.",
		Messages: []llm.Message{
			{Role: "user", Content: "I seek the guild.", Name: "hero"},
		},
	})
	if err != nil {
		t.Fatalf("Process: unexpected error: %v", err)
	}
	drainAudio(resp.Audio)
	signalDone(resp)
	e.Wait()
	_ = e.Close()

	var entries []memory.TranscriptEntry
	for entry := range ch {
		entries = append(entries, entry)
	}

	// Expect two entries: player input + NPC response.
	if len(entries) != 2 {
		t.Fatalf("transcript entries: want 2, got %d: %+v", len(entries), entries)
	}

	// First entry: player input.
	if entries[0].Text != "I seek the guild." {
		t.Errorf("player entry text: want %q, got %q", "I seek the guild.", entries[0].Text)
	}
	if entries[0].SpeakerID != "hero" {
		t.Errorf("player entry SpeakerID: want %q, got %q", "hero", entries[0].SpeakerID)
	}

	// Second entry: NPC response — should include both opener and continuation.
	npcText := entries[1].Text
	if !strings.Contains(npcText, "Ah, traveller!") {
		t.Errorf("NPC entry should contain opener %q, got %q", "Ah, traveller!", npcText)
	}
	if !strings.Contains(npcText, "What brings you here?") {
		t.Errorf("NPC entry should contain continuation %q, got %q", "What brings you here?", npcText)
	}
}

// ─── TestProcess_EmitsTranscriptEntries_NoPlayerMessage ──────────────────────

// TestProcess_EmitsTranscriptEntries_NoPlayerMessage verifies that when prompt.Messages
// is empty, only the NPC response entry is emitted (no player entry).
func TestProcess_EmitsTranscriptEntries_NoPlayerMessage(t *testing.T) {
	t.Parallel()

	fastLLM := &llmmock.Provider{
		StreamChunks: []llm.Chunk{
			{Text: "Greetings, stranger.", FinishReason: "stop"},
		},
	}
	strongLLM := &llmmock.Provider{}
	ttsProv := newTTS()

	e := cascade.New(fastLLM, strongLLM, ttsProv, tts.VoiceProfile{})
	ch := e.Transcripts()

	resp, err := e.Process(context.Background(), emptyAudioFrame, enginepkg.PromptContext{
		SystemPrompt: "You are a guard.",
		// Messages intentionally empty.
	})
	if err != nil {
		t.Fatalf("Process: unexpected error: %v", err)
	}
	drainAudio(resp.Audio)
	signalDone(resp)
	e.Wait()
	_ = e.Close()

	var entries []memory.TranscriptEntry
	for entry := range ch {
		entries = append(entries, entry)
	}

	// Only NPC response entry — no player entry since Messages was empty.
	if len(entries) != 1 {
		t.Fatalf("transcript entries: want 1, got %d: %+v", len(entries), entries)
	}
	if entries[0].Text != "Greetings, stranger." {
		t.Errorf("NPC entry text: want %q, got %q", "Greetings, stranger.", entries[0].Text)
	}
}

// ─── TestProcess_ToolCallSingleIteration ──────────────────────────────────────

// TestProcess_ToolCallSingleIteration verifies that when the strong model
// requests a tool call, the registered handler is invoked and the result is
// fed back to the LLM, which then produces a text continuation for TTS.
func TestProcess_ToolCallSingleIteration(t *testing.T) {
	t.Parallel()

	fastLLM := &llmmock.Provider{
		StreamChunks: []llm.Chunk{
			{Text: "Let me check. "},
			{Text: "One moment.", FinishReason: "stop"},
		},
	}

	// Strong model: first call returns a tool call, second call returns text.
	strongLLM := &llmmock.Provider{
		StreamChunksSequence: [][]llm.Chunk{
			// First call: request a tool.
			{
				{Text: "Looking that up"},
				{
					ToolCalls:    []llm.ToolCall{{ID: "tc_1", Name: "query_lore", Arguments: `{"q":"artifact"}`}},
					FinishReason: "tool_calls",
				},
			},
			// Second call: return text after receiving tool result.
			{
				{Text: "The artifact is ancient.", FinishReason: "stop"},
			},
		},
	}
	ttsProv := newTTS()

	e := cascade.New(fastLLM, strongLLM, ttsProv, tts.VoiceProfile{})
	t.Cleanup(func() { _ = e.Close() })

	tools := []llm.ToolDefinition{{Name: "query_lore", Description: "Queries the lore database."}}
	if err := e.SetTools(tools); err != nil {
		t.Fatalf("SetTools: %v", err)
	}

	var handlerCalls int32
	e.OnToolCall(func(name, args string) (string, error) {
		atomic.AddInt32(&handlerCalls, 1)
		if name != "query_lore" {
			t.Errorf("tool name: want %q, got %q", "query_lore", name)
		}
		return `{"result": "The artifact was forged in the First Age."}`, nil
	})

	resp, err := e.Process(context.Background(), emptyAudioFrame, enginepkg.PromptContext{
		SystemPrompt: "You are a lore keeper.",
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	drainAudio(resp.Audio)
	signalDone(resp)
	e.Wait()

	// Handler must have been called exactly once.
	if n := atomic.LoadInt32(&handlerCalls); n != 1 {
		t.Errorf("tool handler calls: want 1, got %d", n)
	}

	// Strong model must have been called twice (tool request + continuation).
	if len(strongLLM.StreamCalls) != 2 {
		t.Fatalf("strong model calls: want 2, got %d", len(strongLLM.StreamCalls))
	}

	// Second call must include tool result messages.
	secondReq := strongLLM.StreamCalls[1].Req
	var hasToolResult bool
	for _, m := range secondReq.Messages {
		if m.Role == "tool" && m.ToolCallID == "tc_1" {
			hasToolResult = true
			break
		}
	}
	if !hasToolResult {
		t.Error("second strong model call missing tool result message")
	}

	if resp.Err() != nil {
		t.Errorf("resp.Err(): unexpected error: %v", resp.Err())
	}
}

// ─── TestProcess_ToolCallMultiIteration ──────────────────────────────────────

// TestProcess_ToolCallMultiIteration verifies that the engine handles multiple
// sequential tool call iterations where the model requests different tools.
func TestProcess_ToolCallMultiIteration(t *testing.T) {
	t.Parallel()

	fastLLM := &llmmock.Provider{
		StreamChunks: []llm.Chunk{
			{Text: "Hold on. "},
			{Text: "Checking.", FinishReason: "stop"},
		},
	}

	strongLLM := &llmmock.Provider{
		StreamChunksSequence: [][]llm.Chunk{
			// Iteration 1: request tool A.
			{{ToolCalls: []llm.ToolCall{{ID: "tc_a", Name: "tool_a", Arguments: "{}"}}, FinishReason: "tool_calls"}},
			// Iteration 2: request tool B.
			{{ToolCalls: []llm.ToolCall{{ID: "tc_b", Name: "tool_b", Arguments: "{}"}}, FinishReason: "tool_calls"}},
			// Iteration 3: return text.
			{{Text: "Here is the combined answer.", FinishReason: "stop"}},
		},
	}
	ttsProv := newTTS()

	e := cascade.New(fastLLM, strongLLM, ttsProv, tts.VoiceProfile{})
	t.Cleanup(func() { _ = e.Close() })

	if err := e.SetTools([]llm.ToolDefinition{{Name: "tool_a"}, {Name: "tool_b"}}); err != nil {
		t.Fatalf("SetTools: %v", err)
	}

	var toolsCalled []string
	var callMu sync.Mutex
	e.OnToolCall(func(name, args string) (string, error) {
		callMu.Lock()
		toolsCalled = append(toolsCalled, name)
		callMu.Unlock()
		return `"ok"`, nil
	})

	resp, err := e.Process(context.Background(), emptyAudioFrame, enginepkg.PromptContext{
		SystemPrompt: "You are an NPC.",
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	drainAudio(resp.Audio)
	signalDone(resp)
	e.Wait()

	callMu.Lock()
	defer callMu.Unlock()

	if len(toolsCalled) != 2 {
		t.Fatalf("tools called: want 2, got %d: %v", len(toolsCalled), toolsCalled)
	}
	if toolsCalled[0] != "tool_a" || toolsCalled[1] != "tool_b" {
		t.Errorf("tool call order: want [tool_a, tool_b], got %v", toolsCalled)
	}

	// Strong model must have been called 3 times.
	if len(strongLLM.StreamCalls) != 3 {
		t.Errorf("strong model calls: want 3, got %d", len(strongLLM.StreamCalls))
	}
}

// ─── TestProcess_ToolCallNilHandler ──────────────────────────────────────────

// TestProcess_ToolCallNilHandler verifies that when no tool handler is registered
// but the strong model requests a tool call, the engine does not panic and
// gracefully flushes text.
func TestProcess_ToolCallNilHandler(t *testing.T) {
	t.Parallel()

	fastLLM := &llmmock.Provider{
		StreamChunks: []llm.Chunk{
			{Text: "Let me check. "},
			{Text: "Searching.", FinishReason: "stop"},
		},
	}

	strongLLM := &llmmock.Provider{
		StreamChunksSequence: [][]llm.Chunk{
			// Request a tool call with some preceding text.
			{
				{Text: "Before the tool."},
				{ToolCalls: []llm.ToolCall{{ID: "tc_1", Name: "query", Arguments: "{}"}}, FinishReason: "tool_calls"},
			},
		},
	}
	ttsProv := newTTS()

	e := cascade.New(fastLLM, strongLLM, ttsProv, tts.VoiceProfile{})
	t.Cleanup(func() { _ = e.Close() })

	if err := e.SetTools([]llm.ToolDefinition{{Name: "query"}}); err != nil {
		t.Fatalf("SetTools: %v", err)
	}

	// Deliberately do NOT register a tool handler.

	resp, err := e.Process(context.Background(), emptyAudioFrame, enginepkg.PromptContext{
		SystemPrompt: "You are an NPC.",
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	drainAudio(resp.Audio)
	signalDone(resp)
	e.Wait()

	// Must not panic. Strong model should have been called only once (no retry).
	if len(strongLLM.StreamCalls) != 1 {
		t.Errorf("strong model calls: want 1, got %d", len(strongLLM.StreamCalls))
	}
}

// ─── TestProcess_ToolCallHandlerError ────────────────────────────────────────

// TestProcess_ToolCallHandlerError verifies that when a tool handler returns an
// error, the error message is fed back to the LLM as a tool result (not a crash).
func TestProcess_ToolCallHandlerError(t *testing.T) {
	t.Parallel()

	fastLLM := &llmmock.Provider{
		StreamChunks: []llm.Chunk{
			{Text: "One moment. "},
			{Text: "Please wait.", FinishReason: "stop"},
		},
	}

	strongLLM := &llmmock.Provider{
		StreamChunksSequence: [][]llm.Chunk{
			// Request a tool.
			{{ToolCalls: []llm.ToolCall{{ID: "tc_1", Name: "broken_tool", Arguments: "{}"}}, FinishReason: "tool_calls"}},
			// After error result, return text.
			{{Text: "I cannot access that right now.", FinishReason: "stop"}},
		},
	}
	ttsProv := newTTS()

	e := cascade.New(fastLLM, strongLLM, ttsProv, tts.VoiceProfile{})
	t.Cleanup(func() { _ = e.Close() })

	if err := e.SetTools([]llm.ToolDefinition{{Name: "broken_tool"}}); err != nil {
		t.Fatalf("SetTools: %v", err)
	}

	e.OnToolCall(func(name, args string) (string, error) {
		return "", fmt.Errorf("connection refused")
	})

	resp, err := e.Process(context.Background(), emptyAudioFrame, enginepkg.PromptContext{
		SystemPrompt: "You are an NPC.",
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	drainAudio(resp.Audio)
	signalDone(resp)
	e.Wait()

	// Strong model must have been called twice (tool request + after error result).
	if len(strongLLM.StreamCalls) != 2 {
		t.Fatalf("strong model calls: want 2, got %d", len(strongLLM.StreamCalls))
	}

	// Second call must include tool result with error message.
	secondReq := strongLLM.StreamCalls[1].Req
	var foundToolResult bool
	for _, m := range secondReq.Messages {
		if m.Role == "tool" && m.ToolCallID == "tc_1" {
			if !strings.Contains(m.Content, "Tool error: connection refused") {
				t.Errorf("tool result content: want error message, got %q", m.Content)
			}
			foundToolResult = true
			break
		}
	}
	if !foundToolResult {
		t.Error("second strong model call missing tool result message with error")
	}
}

// ─── TestProcess_ToolCallIterationCap ────────────────────────────────────────

// TestProcess_ToolCallIterationCap verifies that the tool loop terminates when
// the iteration cap is reached, even if the model keeps requesting tools.
func TestProcess_ToolCallIterationCap(t *testing.T) {
	t.Parallel()

	fastLLM := &llmmock.Provider{
		StreamChunks: []llm.Chunk{
			{Text: "Hold on. "},
			{Text: "Working.", FinishReason: "stop"},
		},
	}

	// Every call requests a tool — should be capped at maxToolIters.
	strongLLM := &llmmock.Provider{
		StreamChunks: []llm.Chunk{
			{ToolCalls: []llm.ToolCall{{ID: "tc", Name: "loop_tool", Arguments: "{}"}}, FinishReason: "tool_calls"},
		},
	}
	ttsProv := newTTS()

	const maxIters = 2
	e := cascade.New(fastLLM, strongLLM, ttsProv, tts.VoiceProfile{},
		cascade.WithMaxToolIterations(maxIters),
	)
	t.Cleanup(func() { _ = e.Close() })

	if err := e.SetTools([]llm.ToolDefinition{{Name: "loop_tool"}}); err != nil {
		t.Fatalf("SetTools: %v", err)
	}

	e.OnToolCall(func(name, args string) (string, error) {
		return `"ok"`, nil
	})

	resp, err := e.Process(context.Background(), emptyAudioFrame, enginepkg.PromptContext{
		SystemPrompt: "You are an NPC.",
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	drainAudio(resp.Audio)
	signalDone(resp)
	e.Wait()

	// Strong model should have been called exactly maxIters times.
	if len(strongLLM.StreamCalls) != maxIters {
		t.Errorf("strong model calls: want %d, got %d", maxIters, len(strongLLM.StreamCalls))
	}
}

// ─── TestAccumulateToolCalls ──────────────────────────────────────────────────

// TestAccumulateToolCalls verifies that tool calls spread across multiple chunks
// are correctly merged by index.
func TestAccumulateToolCalls(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		existing []llm.ToolCall
		incoming []llm.ToolCall
		want     []llm.ToolCall
	}{
		{
			name:     "new tool call",
			existing: nil,
			incoming: []llm.ToolCall{{ID: "tc_1", Name: "search", Arguments: `{"q":"hello`}},
			want:     []llm.ToolCall{{ID: "tc_1", Name: "search", Arguments: `{"q":"hello`}},
		},
		{
			name:     "merge arguments by index",
			existing: []llm.ToolCall{{ID: "tc_1", Name: "search", Arguments: `{"q":"hel`}},
			incoming: []llm.ToolCall{{Arguments: `lo"}`}},
			want:     []llm.ToolCall{{ID: "tc_1", Name: "search", Arguments: `{"q":"hello"}`}},
		},
		{
			name:     "multiple tools appended",
			existing: []llm.ToolCall{{ID: "tc_1", Name: "tool_a", Arguments: "{}"}},
			incoming: []llm.ToolCall{{}, {ID: "tc_2", Name: "tool_b", Arguments: `{"x":1}`}},
			want:     []llm.ToolCall{{ID: "tc_1", Name: "tool_a", Arguments: "{}"}, {ID: "tc_2", Name: "tool_b", Arguments: `{"x":1}`}},
		},
		{
			name:     "prefer non-empty ID from later chunk",
			existing: []llm.ToolCall{{Name: "tool"}},
			incoming: []llm.ToolCall{{ID: "tc_1"}},
			want:     []llm.ToolCall{{ID: "tc_1", Name: "tool"}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := cascade.AccumulateToolCallsForTest(tc.existing, tc.incoming)
			if len(got) != len(tc.want) {
				t.Fatalf("len: want %d, got %d: %+v", len(tc.want), len(got), got)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("[%d]: want %+v, got %+v", i, tc.want[i], got[i])
				}
			}
		})
	}
}

// ─── TestWithSTT_OptionStored ─────────────────────────────────────────────────

// TestWithSTT_OptionStored verifies that WithSTT is accepted without panicking.
// Full STT integration is out of scope for unit tests; this test ensures the
// option wires correctly and the engine processes a text-mode request normally.
func TestWithSTT_OptionStored(t *testing.T) {
	t.Parallel()

	// A nil STT is acceptable for text-only mode.
	e := cascade.New(
		&llmmock.Provider{StreamChunks: []llm.Chunk{{Text: "Greetings.", FinishReason: "stop"}}},
		&llmmock.Provider{},
		newTTS(),
		tts.VoiceProfile{},
		cascade.WithSTT(nil),
	)
	t.Cleanup(func() { _ = e.Close() })

	resp, err := e.Process(context.Background(), emptyAudioFrame, enginepkg.PromptContext{
		SystemPrompt: "You are an NPC.",
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	drainAudio(resp.Audio)
	signalDone(resp)
	e.Wait()
}

// ─── TestInjectContext_SceneAndUtterances ─────────────────────────────────────

// TestInjectContext_SceneAndUtterances verifies that Scene and RecentUtterances
// from a ContextUpdate are applied to the prompt sent to the fast model.
func TestInjectContext_SceneAndUtterances(t *testing.T) {
	t.Parallel()

	fastLLM := &llmmock.Provider{
		StreamChunks: []llm.Chunk{
			{Text: "Indeed.", FinishReason: "stop"},
		},
	}
	strongLLM := &llmmock.Provider{}
	ttsProv := newTTS()

	e := cascade.New(fastLLM, strongLLM, ttsProv, tts.VoiceProfile{})
	t.Cleanup(func() { _ = e.Close() })

	err := e.InjectContext(context.Background(), enginepkg.ContextUpdate{
		Scene: "The player stands in a dark dungeon.",
		RecentUtterances: []memory.TranscriptEntry{
			{SpeakerID: "player1", SpeakerName: "Hero", Text: "Is anyone there?"},
		},
	})
	if err != nil {
		t.Fatalf("InjectContext: %v", err)
	}

	resp, err := e.Process(context.Background(), emptyAudioFrame, enginepkg.PromptContext{
		SystemPrompt: "You are a dungeon guardian.",
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	drainAudio(resp.Audio)
	signalDone(resp)
	e.Wait()

	if len(fastLLM.StreamCalls) == 0 {
		t.Fatal("fast model not called")
	}

	req := fastLLM.StreamCalls[0].Req

	// HotContext (Scene) must appear in the system prompt.
	if !strings.Contains(req.SystemPrompt, "dark dungeon") {
		t.Errorf("system prompt missing scene context, got: %q", req.SystemPrompt)
	}

	// RecentUtterances must appear as a user message in the conversation history.
	found := false
	for _, msg := range req.Messages {
		if msg.Role == "user" && strings.Contains(msg.Content, "Is anyone there?") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("recent utterance not found in messages: %+v", req.Messages)
	}
}

// ─── FinalText / Transcript Truncation ────────────────────────────────────────

func TestFinalText_NaturalCompletion_SingleModel(t *testing.T) {
	t.Parallel()

	fastLLM := &llmmock.Provider{
		StreamChunks: []llm.Chunk{
			{Text: "Hello there.", FinishReason: "stop"},
		},
	}
	e := cascade.New(fastLLM, &llmmock.Provider{}, newTTS(), tts.VoiceProfile{})
	defer e.Close()

	resp, err := e.Process(context.Background(), emptyAudioFrame, enginepkg.PromptContext{
		SystemPrompt: "NPC.",
		Messages:     []llm.Message{{Role: "user", Content: "Hi", Name: "p1"}},
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	drainAudio(resp.Audio)

	// Signal natural completion.
	resp.NotifyDone <- false
	<-resp.FinalText

	if resp.FinalTextValue != "Hello there." {
		t.Errorf("FinalTextValue = %q, want %q", resp.FinalTextValue, "Hello there.")
	}
}

func TestFinalText_Interrupted_SingleModel(t *testing.T) {
	t.Parallel()

	fastLLM := &llmmock.Provider{
		StreamChunks: []llm.Chunk{
			{Text: "Hello there.", FinishReason: "stop"},
		},
	}
	e := cascade.New(fastLLM, &llmmock.Provider{}, newTTS(), tts.VoiceProfile{})
	defer e.Close()

	resp, err := e.Process(context.Background(), emptyAudioFrame, enginepkg.PromptContext{
		SystemPrompt: "NPC.",
		Messages:     []llm.Message{{Role: "user", Content: "Hi", Name: "p1"}},
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	drainAudio(resp.Audio)

	// Signal interruption.
	resp.NotifyDone <- true
	<-resp.FinalText

	if resp.FinalTextValue != "Hello there...." {
		t.Errorf("FinalTextValue = %q, want %q", resp.FinalTextValue, "Hello there....")
	}
}

func TestFinalText_NaturalCompletion_DualModel(t *testing.T) {
	t.Parallel()

	fastLLM := &llmmock.Provider{
		StreamChunks: []llm.Chunk{
			{Text: "Greetings! I have much to share.", FinishReason: "stop"},
		},
	}
	strongLLM := &llmmock.Provider{
		StreamChunks: []llm.Chunk{
			{Text: "The ancient lore speaks of dragons.", FinishReason: "stop"},
		},
	}
	e := cascade.New(fastLLM, strongLLM, newTTS(), tts.VoiceProfile{})
	defer e.Close()

	resp, err := e.Process(context.Background(), emptyAudioFrame, enginepkg.PromptContext{
		SystemPrompt: "NPC.",
		Messages:     []llm.Message{{Role: "user", Content: "Tell me.", Name: "p1"}},
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	drainAudio(resp.Audio)

	// Signal natural completion.
	resp.NotifyDone <- false
	<-resp.FinalText

	// Should contain the full text.
	if !strings.Contains(resp.FinalTextValue, "Greetings!") {
		t.Errorf("FinalTextValue missing opener: %q", resp.FinalTextValue)
	}
	if strings.HasSuffix(resp.FinalTextValue, "...") {
		t.Errorf("FinalTextValue should not have truncation marker: %q", resp.FinalTextValue)
	}
}

func TestFinalText_Interrupted_DualModel(t *testing.T) {
	t.Parallel()

	fastLLM := &llmmock.Provider{
		StreamChunks: []llm.Chunk{
			{Text: "Greetings! I have much to share.", FinishReason: "stop"},
		},
	}
	strongLLM := &llmmock.Provider{
		StreamChunks: []llm.Chunk{
			{Text: "The ancient lore speaks of dragons. And also of fire.", FinishReason: "stop"},
		},
	}
	e := cascade.New(fastLLM, strongLLM, newTTS(), tts.VoiceProfile{})
	defer e.Close()

	resp, err := e.Process(context.Background(), emptyAudioFrame, enginepkg.PromptContext{
		SystemPrompt: "NPC.",
		Messages:     []llm.Message{{Role: "user", Content: "Tell me.", Name: "p1"}},
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	drainAudio(resp.Audio)

	// Signal interruption.
	resp.NotifyDone <- true
	<-resp.FinalText

	// Should have truncation marker.
	if !strings.HasSuffix(resp.FinalTextValue, "...") {
		t.Errorf("FinalTextValue should end with '...': %q", resp.FinalTextValue)
	}
}

func TestFinalText_TranscriptEmitsTruncatedText(t *testing.T) {
	t.Parallel()

	fastLLM := &llmmock.Provider{
		StreamChunks: []llm.Chunk{
			{Text: "Well met.", FinishReason: "stop"},
		},
	}
	e := cascade.New(fastLLM, &llmmock.Provider{}, newTTS(), tts.VoiceProfile{})
	ch := e.Transcripts()

	resp, err := e.Process(context.Background(), emptyAudioFrame, enginepkg.PromptContext{
		SystemPrompt: "NPC.",
		Messages:     []llm.Message{{Role: "user", Content: "Hi", Name: "p1"}},
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	drainAudio(resp.Audio)

	// Signal interruption.
	resp.NotifyDone <- true
	e.Wait()
	_ = e.Close()

	var entries []memory.TranscriptEntry
	for entry := range ch {
		entries = append(entries, entry)
	}

	// Find the NPC transcript entry.
	found := false
	for _, entry := range entries {
		if entry.SpeakerID == "" && strings.HasSuffix(entry.Text, "...") {
			found = true
			if entry.Text != "Well met...." {
				t.Errorf("NPC transcript text = %q, want %q", entry.Text, "Well met....")
			}
		}
	}
	if !found {
		t.Errorf("expected truncated NPC transcript entry, got: %+v", entries)
	}
}
