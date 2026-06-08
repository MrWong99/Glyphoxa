package voicebench

import (
	"context"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/stt"
	"github.com/MrWong99/Glyphoxa/pkg/voice/vad"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voicetest"
)

// scriptVAD is a minimal [vad.SessionHandle] that emits a fixed event sequence,
// one per frame, then silence — enough to drive a single utterance (speech-start
// … speech-end) through the segmenter without a real detector or CGO.
type scriptVAD struct {
	seq []vad.VADEventType
	i   int
}

func (s *scriptVAD) ProcessFrame(audio.Frame) (vad.VADEvent, error) {
	typ := vad.VADSilence
	if s.i < len(s.seq) {
		typ = s.seq[s.i]
		s.i++
	}
	return vad.VADEvent{Type: typ}, nil
}
func (s *scriptVAD) Reset()       {}
func (s *scriptVAD) Close() error { return nil }

// fakeRecognizer returns a fixed transcript so the segmenter's speech-end fires
// an STTFinal onto the bus (the bench's address_detect/llm_turn anchor).
type fakeRecognizer struct{}

func (fakeRecognizer) Transcribe(context.Context, []audio.Frame) (stt.Transcript, error) {
	return stt.Transcript{Text: "another ale"}, nil
}

func frame(t *testing.T) audio.Frame {
	t.Helper()
	f, err := audio.NewFrame(make([]int16, 512), 16000, 32) // 32ms @ 16kHz
	if err != nil {
		t.Fatalf("audio.NewFrame: %v", err)
	}
	return f
}

// TestDriver_RunClip_HarvestsTurnAndDrainsRecorder pins the driving loop's
// contract on the keyless path: feeding a clip drives the segmenter to an
// STTFinal, the Driver harvests that turn's bus events, AND it drains the
// recorder tap per clip so each turn's recorder-emitted spans (here a synthetic
// llm_round) are attributed to that turn — not pooled across clips.
func TestDriver_RunClip_HarvestsTurnAndDrainsRecorder(t *testing.T) {
	h := voicetest.New(t)
	// speech-start on frame 0, speech-end on frame 1 → one utterance; the rest
	// (the appended silence) stays silent.
	v := orchestrator.NewVAD(h.Bus, &scriptVAD{seq: []vad.VADEventType{vad.VADSpeechStart, vad.VADSpeechEnd}})
	sttStage := orchestrator.NewSTT(h.Bus, fakeRecognizer{})
	conv := orchestrator.NewConversation(h.Bus, v, sttStage, nil)

	tap := newRecorderTap()
	acc := NewAccumulator("cassette", []string{"trivial"})
	d := NewDriver(conv, h, tap, acc, frame(t), 3)

	// Simulate the orchestrator recording one LLM round for this turn.
	tap.LLMRound("gemini", 0, false, 420*time.Millisecond)

	if err := d.RunClip(context.Background(), []audio.Frame{frame(t), frame(t)}); err != nil {
		t.Fatalf("RunClip: %v", err)
	}

	// The turn produced an STTFinal (harvested) — proven via the report having
	// counted one turn, and the drained llm_round landing under its stage.
	r := acc.Build()
	if r.N != 1 {
		t.Errorf("report N = %d, want 1 turn", r.N)
	}
	if d := r.Stages[StageLLMRound]; d.N != 1 || d.P50 != 420 {
		t.Errorf("llm_round = %+v, want one 420ms sample drained from the tap", d)
	}
	// The tap must be empty after the drain so a second clip starts clean.
	if got := tap.samples(StageLLMRound); len(got) != 0 {
		t.Errorf("tap not drained: %d llm_round samples remain", len(got))
	}
}
