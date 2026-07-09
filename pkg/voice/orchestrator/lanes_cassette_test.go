package orchestrator_test

import (
	"context"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/stt"
	"github.com/MrWong99/Glyphoxa/pkg/voice/vad"
	"github.com/MrWong99/Glyphoxa/pkg/voice/vad/silero"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voicecassette"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voicetest"
)

// clipRecognizer is a [stt.Recognizer] for the two-speaker lane test: it maps a
// VAD-segmented utterance back to its SOURCE clip's ground-truth transcript by frame
// identity (each lane buffers the exact frames the test fed it, so a segment's frames
// are a contiguous run of one clip's frames). This keeps the assertion on the tolerant
// house rule — normalized text vs the recorded ground truth — WITHOUT depending on the
// batch cassettes' whole-clip audio hash, which a real VAD segmentation would not
// reproduce (the clips were recorded as one Transcribe call, not VAD-segmented).
type clipRecognizer struct {
	// frameHash → transcript, one entry per frame of each source clip.
	byFrame map[string]string
}

func newClipRecognizer(clips map[string][]audio.Frame) *clipRecognizer {
	r := &clipRecognizer{byFrame: map[string]string{}}
	for transcript, frames := range clips {
		for _, f := range frames {
			r.byFrame[voicecassette.HashFrames([]audio.Frame{f})] = transcript
		}
	}
	return r
}

func (r *clipRecognizer) Transcribe(_ context.Context, frames []audio.Frame) (stt.Transcript, error) {
	// A middle frame is guaranteed inside the clip's voiced body (VAD trims edges).
	for _, f := range frames {
		if txt, ok := r.byFrame[voicecassette.HashFrames([]audio.Frame{f})]; ok {
			return stt.Transcript{Text: txt}, nil
		}
	}
	return stt.Transcript{Text: ""}, nil
}

// sileroLaneFactory builds a real Silero VAD lane on bus for each Speaker Lane,
// matching the default lane's configuration (16 kHz / 32 ms, 0.5/0.35 hysteresis).
func sileroLaneFactory(t *testing.T, bus *voiceevent.Bus) orchestrator.LaneVADFactory {
	t.Helper()
	engine, err := silero.New()
	if err != nil {
		t.Fatalf("silero.New: %v", err)
	}
	cfg := vad.Config{SampleRate: 16000, FrameSizeMs: 32, SpeechThreshold: 0.5, SilenceThreshold: 0.35}
	return func() (*orchestrator.VAD, func(), error) {
		sess, err := engine.NewSession(cfg)
		if err != nil {
			return nil, nil, err
		}
		return orchestrator.NewVAD(bus, sess), func() { _ = sess.Close() }, nil
	}
}

// clipFrames loads a clip and frames it at the VAD chunk size (512 samples), tagged
// with speaker.
func clipFrames(t *testing.T, name, speaker string) []audio.Frame {
	t.Helper()
	clip := voicetest.LoadClip(t, name)
	frames, _ := clip.FramesOf(t, 512)
	out := make([]audio.Frame, len(frames))
	for i, f := range frames {
		out[i] = f.WithSpeaker(speaker)
	}
	return out
}

// TestConversation_TwoSpeakerCassette_DistinctSpeakerIDs is step 12 / AC1: two
// solo-recorded clips, interleaved frame-by-frame and tagged as two distinct Discord
// speakers, drive two Speaker Lanes through the full Conversation façade and yield two
// STTFinals — each carrying its own SpeakerID and its clip's ground-truth transcript
// (normalized-text match, the tolerant house rule). This is the headline
// two-speaker-attribution proof of ADR-0050.
func TestConversation_TwoSpeakerCassette_DistinctSpeakerIDs(t *testing.T) {
	h := voicetest.New(t)

	const speakerHello, speakerBart = "111", "222"
	helloFrames := clipFrames(t, "hello-test", speakerHello)
	bartFrames := clipFrames(t, "bart-test", speakerBart)

	rec := newClipRecognizer(map[string][]audio.Frame{
		helloUtterance: helloFrames,
		bartUtterance:  bartFrames,
	})

	// The default lane's VAD is a real Silero session (it only ever sees the trailing
	// broadcast silence, never a voiced frame, so it emits nothing).
	engine, err := silero.New()
	if err != nil {
		t.Fatalf("silero.New: %v", err)
	}
	defSess, err := engine.NewSession(vad.Config{SampleRate: 16000, FrameSizeMs: 32, SpeechThreshold: 0.5, SilenceThreshold: 0.35})
	if err != nil {
		t.Fatalf("engine.NewSession: %v", err)
	}
	t.Cleanup(func() { _ = defSess.Close() })

	vadStage := orchestrator.NewVAD(h.Bus, defSess)
	sttStage := orchestrator.NewSTT(h.Bus, rec)
	conv := orchestrator.NewConversation(h.Bus, vadStage, sttStage, nil,
		orchestrator.WithSpeakerLanes(sileroLaneFactory(t, h.Bus)),
		orchestrator.WithErrorHandler(func(err error) { t.Errorf("stage: %v", err) }),
	)
	t.Cleanup(conv.Register(t.Context()))

	// Interleave the two speakers' frames frame-by-frame: the segmenter routes each to
	// its own lane, so each lane's Silero session reconstructs its clip's contiguous
	// stream — real cross-talk, correctly separated (ADR-0050).
	maxLen := len(helloFrames)
	if len(bartFrames) > maxLen {
		maxLen = len(bartFrames)
	}
	for i := 0; i < maxLen; i++ {
		if i < len(helloFrames) {
			if err := conv.Feed(helloFrames[i]); err != nil {
				t.Fatalf("feed hello %d: %v", i, err)
			}
		}
		if i < len(bartFrames) {
			if err := conv.Feed(bartFrames[i]); err != nil {
				t.Fatalf("feed bart %d: %v", i, err)
			}
		}
	}
	if err := conv.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	// Exactly two STTFinals, one per speaker, each matching its clip's ground truth.
	bySpeaker := map[string]string{}
	for _, e := range h.Events() {
		if f, ok := e.(voiceevent.STTFinal); ok {
			bySpeaker[f.SpeakerID] = f.Text
		}
	}
	if len(bySpeaker) != 2 {
		t.Fatalf("STTFinals by speaker = %v, want two distinct SpeakerIDs", bySpeaker)
	}
	assertNormalized(t, bySpeaker[speakerHello], helloUtterance, speakerHello)
	assertNormalized(t, bySpeaker[speakerBart], bartUtterance, speakerBart)
}

func assertNormalized(t *testing.T, got, want, speaker string) {
	t.Helper()
	if voicetest.NormalizeTranscript(got) != voicetest.NormalizeTranscript(want) {
		t.Errorf("speaker %s transcript = %q, want (normalized) %q", speaker, got, want)
	}
}
