//go:build bench

package voicebench

import (
	"context"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/agent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/stt"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/vad"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voicetest"
)

// stubProvider is a deterministic llm.Provider: it streams one fixed sentence
// and an end_turn, no tools. It sidesteps the prompt_hash entirely — the
// orchestration-floor run measures the pipeline's plumbing latency, for which
// the LLM's content is irrelevant (only that a reply flows).
type stubProvider struct{ text string }

func (s stubProvider) Complete(ctx context.Context, _ llm.Request) (<-chan llm.StreamEvent, error) {
	ch := make(chan llm.StreamEvent, 2)
	ch <- llm.StreamEvent{Type: llm.EventText, Text: s.text}
	ch <- llm.StreamEvent{Type: llm.EventDone, StopReason: "end_turn"}
	close(ch)
	return ch, nil
}

// stubSynth is a deterministic tts.Synthesizer: one PCM chunk per sentence. The
// single chunk is what makes the Tee publish FirstAudio.
type stubSynth struct{}

func (stubSynth) Synthesize(ctx context.Context, _ tts.SynthesizeRequest) (<-chan tts.AudioChunk, error) {
	ch := make(chan tts.AudioChunk, 1)
	ch <- tts.AudioChunk{PCM: make([]byte, 320), SampleRate: 16000, Channels: 1}
	close(ch)
	return ch, nil
}
func (stubSynth) AudioMarkupPrompt(tts.Voice) string { return "Speak plainly." }

// stubRecognizer returns a fixed transcript so the segmenter fires STTFinal.
type stubRecognizer struct{ text string }

func (s stubRecognizer) Transcribe(context.Context, []audio.Frame) (stt.Transcript, error) {
	return stt.Transcript{Text: s.text}, nil
}

// alwaysRoute is a TargetMatcher that routes every utterance to one target —
// a single-NPC bench detector.
type alwaysRoute struct{ target voiceevent.AddressTarget }

func (a alwaysRoute) TargetMatch(text string) []voiceevent.AddressRouted {
	return []voiceevent.AddressRouted{{Text: text, Target: a.target}}
}

// scriptVADRig is the keyless scripted VAD (speech-start then silence) reused
// from driver_test, re-declared here for the bench-tagged build.
type scriptVADRig struct {
	seq []vad.VADEventType
	i   int
}

func (s *scriptVADRig) ProcessFrame(audio.Frame) (vad.VADEvent, error) {
	typ := vad.VADSilence
	if s.i < len(s.seq) {
		typ = s.seq[s.i]
		s.i++
	}
	return vad.VADEvent{Type: typ}, nil
}
func (s *scriptVADRig) Reset()       {}
func (s *scriptVADRig) Close() error { return nil }

func benchFrame(t *testing.T) audio.Frame {
	t.Helper()
	f, err := audio.NewFrame(make([]int16, 512), 16000, 32)
	if err != nil {
		t.Fatalf("audio.NewFrame: %v", err)
	}
	return f
}

// TestRig_StubFlow_PublishesFirstAudio is the headline de-risk: a clip driven
// through the bench-owned Conversation (stub LLM + stub TTS) must produce a
// turn whose first AudioChunk crosses the TeeSynthesizer→sink boundary and
// publishes a voiceevent.FirstAudio — which, paired against the turn's
// STTFinal.SpeechEndAt, yields a response_latency span. This proves the
// TeeSynthesizer→FirstAudio→response_latency path end to end, independent of
// the cassette-fixture decision.
func TestRig_StubFlow_PublishesFirstAudio(t *testing.T) {
	h := voicetest.New(t)
	target := voiceevent.AddressTarget{AgentID: "bart", AgentRole: "character", Name: "Bart"}

	conv := BuildConversation(RigConfig{
		Bus:      h.Bus,
		VAD:      orchestrator.NewVAD(h.Bus, &scriptVADRig{seq: []vad.VADEventType{vad.VADSpeechStart, vad.VADSpeechEnd}}),
		STT:      orchestrator.NewSTT(h.Bus, stubRecognizer{text: "another ale"}),
		Persona:  agent.Persona{AgentID: "bart", Markdown: "You are Bart, the innkeeper."},
		Provider: stubProvider{text: "Coming right up."},
		Synth:    stubSynth{},
		Detector: orchestrator.NewAddressDetector(alwaysRoute{target: target}),
	})

	tap := newRecorderTap()
	acc := NewAccumulator("cassette", []string{"trivial"})
	d := NewDriver(conv, h, tap, acc, benchFrame(t), 3)

	if err := d.RunClip(context.Background(), []audio.Frame{benchFrame(t), benchFrame(t)}); err != nil {
		t.Fatalf("RunClip: %v", err)
	}
	// Give the tee's forward goroutine a moment to publish FirstAudio before the
	// harness snapshot is read (RunClip already snapshots after Flush, but the
	// reply runs async under WithBargeIn).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if hasEvent[voiceevent.FirstAudio](h) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if !hasEvent[voiceevent.FirstAudio](h) {
		t.Fatalf("no voiceevent.FirstAudio published; the Tee→sink boundary never fired. Events: %v", eventNamesOf(h))
	}
	if !hasEvent[voiceevent.STTFinal](h) {
		t.Errorf("no STTFinal; the clip never produced a turn")
	}

	// The headline proof: fold the harvested event log into the accumulator and
	// confirm a response_latency span landed — FirstAudio.At − STTFinal.SpeechEndAt
	// for the turn. (RunClip already folded once; re-extract the snapshot here so
	// the assertion is over the post-FirstAudio event set.)
	r := NewAccumulatorFromEvents("cassette", []string{"trivial"}, h.Events())
	d2, ok := r.Stages[StageResponseLatency]
	if !ok || d2.N == 0 {
		t.Fatalf("no response_latency span captured; STTFinal.SpeechEndAt or FirstAudio missing. Stages: %v", stageKeys(r))
	}
	if d2.P50 <= 0 {
		t.Errorf("response_latency p50 = %v ms, want a positive span", d2.P50)
	}
}

// NewAccumulatorFromEvents is a test helper: one-turn accumulator over a ready
// event slice.
func NewAccumulatorFromEvents(tier string, corpus []string, events []voiceevent.Event) Report {
	acc := NewAccumulator(tier, corpus)
	acc.AddTurn(events)
	return acc.Build()
}

func stageKeys(r Report) []Stage {
	out := make([]Stage, 0, len(r.Stages))
	for s := range r.Stages {
		out = append(out, s)
	}
	return out
}

func hasEvent[E voiceevent.Event](h *voicetest.Harness) bool {
	for _, e := range h.Events() {
		if _, ok := e.(E); ok {
			return true
		}
	}
	return false
}

func eventNamesOf(h *voicetest.Harness) []string {
	var out []string
	for _, e := range h.Events() {
		out = append(out, e.EventName())
	}
	return out
}
