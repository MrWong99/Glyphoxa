package orchestrator_test

import (
	"context"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/stt"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voicecassette"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voicetest"
)

// stubRecognizer is a [stt.Recognizer] that returns a pinned [stt.Transcript]
// regardless of input. Used to exercise the orchestrator stage's republish
// contract independently of any real provider or cassette.
type stubRecognizer struct {
	transcript stt.Transcript
}

func (s stubRecognizer) Transcribe(context.Context, []audio.Frame) (stt.Transcript, error) {
	return s.transcript, nil
}

// TestSTT_HelloTest_EmitsFinal is TB5: the first cassette-backed tracer
// bullet (ADR-0021). It feeds the "hello-test" clip through the orchestrator
// STT stage backed by a replay-only [voicecassette.STTRecognizer] and
// asserts that exactly the recorded transcript reaches the shared bus as a
// [voiceevent.STTFinal] (ADR-0020).
//
// The cassette under tests/voice-cassettes/stt-hello-test.yaml pins both the
// audio fingerprint and the expected transcript text. Re-recording will
// happen under `-tags=record` once a real STT provider adapter lands; for
// now the cassette is the authoritative expectation.
func TestSTT_HelloTest_EmitsFinal(t *testing.T) {
	h := voicetest.New(t)

	clip := voicetest.LoadClip(t, "hello-test")
	const frameMs = 32 // mirrors the VAD stage; any consistent framing works
	frames, tail := clip.FramesOf(t, clip.SampleRate*frameMs/1000)
	if tail != 0 {
		t.Logf("hello-test: trailing %d samples (%d ms) not frame-aligned; discarded",
			tail, tail*1000/clip.SampleRate)
	}

	recognizer := voicecassette.LoadSTT(t, "stt-hello-test")
	stage := orchestrator.NewSTT(h.Bus, recognizer)

	if err := stage.Transcribe(context.Background(), frames); err != nil {
		t.Fatalf("stage.Transcribe: %v", err)
	}

	const want = "Glyphoxa, roll a perception check for me."
	voicetest.AssertEvent(t, h,
		func(e voiceevent.STTFinal) bool { return e.Text == want },
		"stt.final with text "+want,
	)
}

// TestSTT_EmptyTranscript_StillPublishes pins the contract documented on
// [orchestrator.STT.Transcribe]: when the recognizer authoritatively returns
// an empty transcript (e.g. all silence reached it), the stage still
// publishes [voiceevent.STTFinal] with Text == "". Filtering "heard nothing"
// signals is a downstream decision, not the orchestrator's.
func TestSTT_EmptyTranscript_StillPublishes(t *testing.T) {
	h := voicetest.New(t)
	stage := orchestrator.NewSTT(h.Bus, stubRecognizer{transcript: stt.Transcript{Text: ""}})

	if err := stage.Transcribe(context.Background(), nil); err != nil {
		t.Fatalf("stage.Transcribe: %v", err)
	}

	voicetest.AssertEvent(t, h,
		func(e voiceevent.STTFinal) bool { return e.Text == "" },
		"stt.final with empty text",
	)
}
