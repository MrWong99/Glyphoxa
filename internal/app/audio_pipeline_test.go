package app

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/MrWong99/glyphoxa/internal/agent"
	agentmock "github.com/MrWong99/glyphoxa/internal/agent/mock"
	"github.com/MrWong99/glyphoxa/internal/agent/orchestrator"
	enginemock "github.com/MrWong99/glyphoxa/internal/engine/mock"
	"github.com/MrWong99/glyphoxa/internal/transcript"
	"github.com/MrWong99/glyphoxa/pkg/provider/stt"
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
