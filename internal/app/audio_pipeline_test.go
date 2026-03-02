package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/MrWong99/glyphoxa/internal/agent"
	agentmock "github.com/MrWong99/glyphoxa/internal/agent/mock"
	"github.com/MrWong99/glyphoxa/internal/agent/orchestrator"
	enginemock "github.com/MrWong99/glyphoxa/internal/engine/mock"
	"github.com/MrWong99/glyphoxa/internal/transcript"
	"github.com/MrWong99/glyphoxa/pkg/audio"
	audiomock "github.com/MrWong99/glyphoxa/pkg/audio/mock"
	"github.com/MrWong99/glyphoxa/pkg/provider/stt"
	sttmock "github.com/MrWong99/glyphoxa/pkg/provider/stt/mock"
	"github.com/MrWong99/glyphoxa/pkg/provider/vad"
	vadmock "github.com/MrWong99/glyphoxa/pkg/provider/vad/mock"
)

// ─── mockSTTSession ───────────────────────────────────────────────────────────

// mockSTTSession is a minimal in-memory [stt.SessionHandle] for unit tests.
// Pre-fill the finals channel with transcripts and close it to simulate an
// STT session that produces exactly those results, then terminates.
type mockSTTSession struct {
	finals chan stt.Transcript
}

// Compile-time interface check.
var _ stt.SessionHandle = (*mockSTTSession)(nil)

func newMockSTTSession(transcripts ...stt.Transcript) *mockSTTSession {
	ch := make(chan stt.Transcript, len(transcripts))
	for _, t := range transcripts {
		ch <- t
	}
	close(ch)
	return &mockSTTSession{finals: ch}
}

func (m *mockSTTSession) Finals() <-chan stt.Transcript { return m.finals }
func (m *mockSTTSession) Partials() <-chan stt.Transcript {
	ch := make(chan stt.Transcript)
	close(ch)
	return ch
}
func (m *mockSTTSession) SendAudio(_ []byte) error               { return nil }
func (m *mockSTTSession) SetKeywords(_ []stt.KeywordBoost) error { return nil }
func (m *mockSTTSession) Close() error                           { return nil }

// ─── mockPipeline ─────────────────────────────────────────────────────────────

// mockPipeline is a controllable [transcript.Pipeline] for unit tests.
// Set correctResult to control the corrected output, or correctErr to
// simulate a failure.
type mockPipeline struct {
	mu            sync.Mutex
	correctResult *transcript.CorrectedTranscript
	correctErr    error
	calls         int
}

// Compile-time interface check.
var _ transcript.Pipeline = (*mockPipeline)(nil)

func (m *mockPipeline) Correct(_ context.Context, t stt.Transcript, _ []string) (*transcript.CorrectedTranscript, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if m.correctErr != nil {
		return nil, m.correctErr
	}
	if m.correctResult != nil {
		return m.correctResult, nil
	}
	// Default: return transcript unchanged.
	return &transcript.CorrectedTranscript{
		Original:    t,
		Corrected:   t.Text,
		Corrections: []transcript.Correction{},
	}, nil
}

// ─── helpers ──────────────────────────────────────────────────────────────────

// newTestNPCAndOrch creates a single-agent orchestrator. The orchestrator's
// single-NPC fallback means any non-empty transcript routes to this agent,
// regardless of text content.
func newTestNPCAndOrch() (*orchestrator.Orchestrator, *agentmock.NPCAgent) {
	eng := &enginemock.VoiceEngine{}
	npc := &agentmock.NPCAgent{
		IDResult:     "npc-0-eldrinax",
		NameResult:   "Eldrinax",
		EngineResult: eng,
	}
	orch := orchestrator.New([]agent.NPCAgent{npc})
	return orch, npc
}

// ─── TestCollectAndRoute_WithCorrection ───────────────────────────────────────

// TestCollectAndRoute_WithCorrection verifies that the pipeline corrects the
// raw STT text before it is forwarded to the NPC agent via the orchestrator.
func TestCollectAndRoute_WithCorrection(t *testing.T) {
	t.Parallel()

	orch, npc := newTestNPCAndOrch()

	pipe := &mockPipeline{
		correctResult: &transcript.CorrectedTranscript{
			Original:  stt.Transcript{Text: "Elder Nax"},
			Corrected: "Eldrinax",
			Corrections: []transcript.Correction{
				{Original: "Elder Nax", Corrected: "Eldrinax", Confidence: 0.92, Method: "phonetic"},
			},
		},
	}

	p := &audioPipeline{
		orch:     orch,
		pipeline: pipe,
		entities: func() []string { return []string{"Eldrinax"} },
	}

	session := newMockSTTSession(stt.Transcript{Text: "Elder Nax", Confidence: 0.8})
	p.collectAndRoute(context.Background(), "player-1", session)

	calls := npc.HandleUtteranceCalls
	if len(calls) != 1 {
		t.Fatalf("HandleUtterance called %d times, want 1", len(calls))
	}
	if got := calls[0].Transcript.Text; got != "Eldrinax" {
		t.Errorf("HandleUtterance received text %q, want %q", got, "Eldrinax")
	}
	if pipe.calls != 1 {
		t.Errorf("pipeline.Correct called %d times, want 1", pipe.calls)
	}
}

// ─── TestCollectAndRoute_WithoutCorrection ────────────────────────────────────

// TestCollectAndRoute_WithoutCorrection verifies that when no correction
// pipeline is configured (nil), the raw STT transcript is forwarded unchanged.
func TestCollectAndRoute_WithoutCorrection(t *testing.T) {
	t.Parallel()

	orch, npc := newTestNPCAndOrch()

	p := &audioPipeline{
		orch:     orch,
		pipeline: nil, // no correction pipeline
		entities: nil,
	}

	session := newMockSTTSession(stt.Transcript{Text: "Elder Nax", Confidence: 0.8})
	p.collectAndRoute(context.Background(), "player-1", session)

	calls := npc.HandleUtteranceCalls
	if len(calls) != 1 {
		t.Fatalf("HandleUtterance called %d times, want 1", len(calls))
	}
	if got := calls[0].Transcript.Text; got != "Elder Nax" {
		t.Errorf("HandleUtterance received text %q, want raw %q", got, "Elder Nax")
	}
}

// ─── TestAudioPipeline_UpdateKeywords ─────────────────────────────────────────

// TestAudioPipeline_UpdateKeywords verifies that UpdateKeywords atomically
// replaces the keyword list in the STT config used for future sessions.
func TestAudioPipeline_UpdateKeywords(t *testing.T) {
	t.Parallel()

	p := &audioPipeline{
		sttCfg: stt.StreamConfig{SampleRate: 16000, Channels: 1},
	}

	if len(p.sttCfg.Keywords) != 0 {
		t.Fatal("expected empty keywords initially")
	}

	keywords := []stt.KeywordBoost{
		{Keyword: "Eldrinax", Boost: 1.0},
		{Keyword: "Greymantle", Boost: 1.0},
	}
	p.UpdateKeywords(keywords)

	if got := len(p.sttCfg.Keywords); got != 2 {
		t.Fatalf("expected 2 keywords, got %d", got)
	}
	if p.sttCfg.Keywords[0].Keyword != "Eldrinax" {
		t.Errorf("keyword 0: got %q, want %q", p.sttCfg.Keywords[0].Keyword, "Eldrinax")
	}
	if p.sttCfg.Keywords[1].Keyword != "Greymantle" {
		t.Errorf("keyword 1: got %q, want %q", p.sttCfg.Keywords[1].Keyword, "Greymantle")
	}

	// Overwrite with a single keyword.
	p.UpdateKeywords([]stt.KeywordBoost{{Keyword: "Strahd", Boost: 0.8}})
	if got := len(p.sttCfg.Keywords); got != 1 {
		t.Fatalf("expected 1 keyword after overwrite, got %d", got)
	}
	if p.sttCfg.Keywords[0].Keyword != "Strahd" {
		t.Errorf("keyword 0 after overwrite: got %q, want %q", p.sttCfg.Keywords[0].Keyword, "Strahd")
	}
}

// ─── TestCollectAndRoute_ContextCancellation ──────────────────────────────────

// TestCollectAndRoute_ContextCancellation verifies that collectAndRoute exits
// promptly when its context is cancelled, even if the STT session's Finals()
// channel is never closed. This guards against goroutine leaks when a
// participant leaves mid-utterance and the STT provider doesn't clean up.
func TestCollectAndRoute_ContextCancellation(t *testing.T) {
	t.Parallel()

	orch, npc := newTestNPCAndOrch()

	p := &audioPipeline{
		orch:     orch,
		pipeline: nil,
		entities: nil,
	}

	// Create an STT session whose Finals() channel is never closed.
	hangingSession := &mockSTTSession{
		finals: make(chan stt.Transcript), // unbuffered, never written to or closed
	}

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		p.collectAndRoute(ctx, "player-1", hangingSession)
		close(done)
	}()

	// Cancel the context — collectAndRoute must exit.
	cancel()

	select {
	case <-done:
		// Success: goroutine exited.
	case <-time.After(2 * time.Second):
		t.Fatal("collectAndRoute did not exit after context cancellation (goroutine leak)")
	}

	// No utterances should have been handled.
	if len(npc.HandleUtteranceCalls) != 0 {
		t.Errorf("HandleUtterance called %d times, want 0", len(npc.HandleUtteranceCalls))
	}
}

// ─── TestCollectAndRoute_CorrectionError ──────────────────────────────────────

// TestCollectAndRoute_CorrectionError verifies that when the correction
// pipeline returns an error, the raw transcript is still routed to the agent
// (graceful degradation — no transcript is dropped).
func TestCollectAndRoute_CorrectionError(t *testing.T) {
	t.Parallel()

	orch, npc := newTestNPCAndOrch()

	pipe := &mockPipeline{
		correctErr: errors.New("llm corrector: context deadline exceeded"),
	}

	p := &audioPipeline{
		orch:     orch,
		pipeline: pipe,
		entities: func() []string { return []string{"Eldrinax"} },
	}

	session := newMockSTTSession(stt.Transcript{Text: "Elder Nax", Confidence: 0.8})
	p.collectAndRoute(context.Background(), "player-1", session)

	calls := npc.HandleUtteranceCalls
	if len(calls) != 1 {
		t.Fatalf("HandleUtterance called %d times, want 1", len(calls))
	}
	// Raw text must pass through unchanged when correction fails.
	if got := calls[0].Transcript.Text; got != "Elder Nax" {
		t.Errorf("HandleUtterance received text %q, want raw %q", got, "Elder Nax")
	}
	if pipe.calls != 1 {
		t.Errorf("pipeline.Correct called %d times, want 1", pipe.calls)
	}
}

// ─── TestAudioPipeline_ConcurrentKeywordUpdate ────────────────────────────────

// TestAudioPipeline_ConcurrentKeywordUpdate verifies that concurrent calls to
// UpdateKeywords do not race with processParticipant reading sttCfg. The VAD
// mock always returns SpeechStart so every frame triggers a StartStream call
// (and thus an sttCfg read). Run with -race to detect unsynchronized access.
func TestAudioPipeline_ConcurrentKeywordUpdate(t *testing.T) {
	t.Parallel()

	// VAD always returns SpeechStart so each frame triggers StartStream.
	vadSess := &vadmock.Session{
		EventResult: vad.VADEvent{Type: vad.VADSpeechStart, Probability: 0.9},
	}
	vadEng := &vadmock.Engine{Session: vadSess}

	sttProv := &sttmock.Provider{}
	orch, _ := newTestNPCAndOrch()
	mixer := &audiomock.Mixer{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	p := newAudioPipeline(audioPipelineConfig{
		vadEngine:   vadEng,
		sttProvider: sttProv,
		orch:        orch,
		mixer:       mixer,
		vadCfg:      vad.Config{SampleRate: 16000, FrameSizeMs: 30},
		sttCfg:      stt.StreamConfig{SampleRate: 16000, Channels: 1},
		ctx:         ctx,
	})

	frames := make(chan audio.AudioFrame, 100)
	frameSize := 16000 * 30 / 1000 * 2 // bytes per VAD frame

	// Pre-fill frames and close the channel.
	for range 20 {
		frames <- audio.AudioFrame{
			Data:       make([]byte, frameSize),
			SampleRate: 16000,
			Channels:   1,
		}
	}
	close(frames)

	// Goroutine: concurrently update keywords while processParticipant runs.
	var kwWG sync.WaitGroup
	kwWG.Add(1)
	go func() {
		defer kwWG.Done()
		for i := range 200 {
			p.UpdateKeywords([]stt.KeywordBoost{
				{Keyword: "Eldrinax", Boost: float64(i)},
				{Keyword: "Greymantle", Boost: 1.0},
			})
		}
	}()

	// Run processParticipant — it reads from the pre-filled frames channel.
	p.processParticipant(ctx, "player-test", frames)

	kwWG.Wait()

	// The test passes if -race detects no data race. Verify StartStream
	// was called at least once (sttCfg was actually read).
	if len(sttProv.StartStreamCalls) == 0 {
		t.Fatal("expected at least 1 StartStream call (sttCfg must have been read)")
	}
}
