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
// a single-NPC bench detector. It stamps At with the route decision time, the
// same as the prod matchers (wholeword.go:69, matcher.go:262); without it the
// span start is the zero time and address_detect comes out as a decades-long
// sentinel.
type alwaysRoute struct{ target voiceevent.AddressTarget }

func (a alwaysRoute) TargetMatch(text string) []voiceevent.AddressRouted {
	return []voiceevent.AddressRouted{{At: time.Now(), Text: text, Target: a.target}}
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

// TestRig_StubFlow_PublishesFirstAudio is the single-turn headline gate: a clip
// driven through the bench-owned Conversation (stub LLM + stub TTS) must produce
// a turn whose first AudioChunk crosses the TeeSynthesizer→sink boundary and
// publishes a voiceevent.FirstAudio — which observe's StageSubscriber (installed
// on the bus by BuildConversation) pairs against STTFinal.SpeechEndAt into a
// response_latency span, and AddressRouted.At against STTFinal.At into
// address_detect. Both must come out POSITIVE and physically small (single-digit
// to low-hundreds ms for a stub pipeline) — a negative or sentinel value frozen
// as the baseline is exactly the meaningless-green the bench exists to prevent.
// This proves the full subscriber→tap path end to end before the corpus baseline
// is recorded.
func TestRig_StubFlow_PublishesFirstAudio(t *testing.T) {
	h := voicetest.New(t)
	target := voiceevent.AddressTarget{AgentID: "bart", AgentRole: "character", Name: "Bart"}

	tap := newRecorderTap()
	conv := BuildConversation(RigConfig{
		Bus:      h.Bus,
		VAD:      orchestrator.NewVAD(h.Bus, &scriptVADRig{seq: []vad.VADEventType{vad.VADSpeechStart, vad.VADSpeechEnd}}),
		STT:      orchestrator.NewSTT(h.Bus, stubRecognizer{text: "another ale"}),
		Persona:  agent.Persona{AgentID: "bart", Markdown: "You are Bart, the innkeeper."},
		Provider: stubProvider{text: "Coming right up."},
		Synth:    stubSynth{},
		Detector: orchestrator.NewAddressDetector(alwaysRoute{target: target}),
		Recorder: tap,
	})

	acc := NewAccumulator("cassette", []string{"trivial"})
	d := NewDriver(conv, h, tap, acc, benchFrame(t), 3)

	// RunClip blocks on the in-Driver completion barrier until response_latency
	// lands on the tap (or hard-fails on timeout); no sleep/poll needed here.
	if err := d.RunClip(context.Background(), []audio.Frame{benchFrame(t), benchFrame(t)}); err != nil {
		t.Fatalf("RunClip: %v", err)
	}

	if !hasEvent[voiceevent.FirstAudio](h) {
		t.Fatalf("no voiceevent.FirstAudio published; the Tee→sink boundary never fired. Events: %v", eventNamesOf(h))
	}
	if !hasEvent[voiceevent.STTFinal](h) {
		t.Errorf("no STTFinal; the clip never produced a turn")
	}

	// The accumulator drained the tap inside RunClip, so read the report it built.
	r := acc.Build()
	if r.N != 1 {
		t.Errorf("report N = %d, want 1 turn", r.N)
	}

	// Both bus-derived spans must be present and positive.
	for _, stage := range []Stage{StageResponseLatency, StageAddressDetect} {
		dist, ok := r.Stages[stage]
		if !ok || dist.N == 0 {
			t.Fatalf("no %s span captured; subscriber path didn't record it. Stages: %v", stage, stageKeys(r))
		}
		if dist.P50 <= 0 {
			t.Errorf("%s p50 = %v ms, want a positive span (negative ⇒ scrambled endpoints)", stage, dist.P50)
		}
		// A stub pipeline's bus spans are sub-second; a multi-second value means a
		// zero-time endpoint leaked through (the sentinel the swap is meant to kill).
		if dist.P50 > 2000 {
			t.Errorf("%s p50 = %v ms, implausibly large for a stub turn (zero-time endpoint?)", stage, dist.P50)
		}
	}

	// llm_round must reach the report via the tap — the B2 thinking-cap signal
	// (#7) flows through the agenttool adapter onto this same tap. Dropping this
	// assertion silently un-covers whether the cap metric reaches the bench.
	if dist, ok := r.Stages[StageLLMRound]; !ok || dist.N == 0 {
		t.Errorf("no llm_round span captured; the B2 adapter signal didn't reach the report. Stages: %v", stageKeys(r))
	}
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
