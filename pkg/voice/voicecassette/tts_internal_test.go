package voicecassette

import (
	"context"
	"strings"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
)

// newTTSForTest constructs a [TTSSynthesizer] from an in-memory cassette so
// the matcher contract can be exercised without a disk fixture per case.
// Whitebox to access the unexported fields LoadTTS would otherwise populate.
func newTTSForTest(name string, sentences ...string) *TTSSynthesizer {
	return &TTSSynthesizer{name: name, cassette: TTSCassette{Sentences: sentences}}
}

func TestTTSSynthesizer_SentenceMismatch_PointsAtRecord(t *testing.T) {
	t.Parallel()
	s := newTTSForTest("tts-fixture", "Wanted sentence.")
	_, err := s.Synthesize(context.Background(), tts.SynthesizeRequest{Sentence: "Different sentence."})
	if err == nil {
		t.Fatal("Synthesize with wrong sentence returned nil error")
	}
	if !strings.Contains(err.Error(), "-tags=record") {
		t.Errorf("error %q does not point at -tags=record", err)
	}
}

func TestTTSSynthesizer_Exhausted_PointsAtRecord(t *testing.T) {
	t.Parallel()
	s := newTTSForTest("tts-fixture", "Only sentence.")
	if _, err := s.Synthesize(context.Background(), tts.SynthesizeRequest{Sentence: "Only sentence."}); err != nil {
		t.Fatalf("first Synthesize: %v", err)
	}
	_, err := s.Synthesize(context.Background(), tts.SynthesizeRequest{Sentence: "Only sentence."})
	if err == nil {
		t.Fatal("Synthesize past end returned nil error")
	}
	if !strings.Contains(err.Error(), "-tags=record") {
		t.Errorf("error %q does not point at -tags=record", err)
	}
}

// TestTTSSynthesizer_Positional is the regression catcher for the matcher's
// positional contract. Two distinct sentences in the recorded order must
// succeed; the same sentence dispatched twice (i.e. the first one reused at
// position 1) must fail. Set-membership matching would silently accept the
// reuse — this test fails it.
func TestTTSSynthesizer_Positional(t *testing.T) {
	t.Parallel()

	inOrder := newTTSForTest("tts-fixture", "First.", "Second.")
	for _, want := range []string{"First.", "Second."} {
		if _, err := inOrder.Synthesize(context.Background(), tts.SynthesizeRequest{Sentence: want}); err != nil {
			t.Fatalf("in-order Synthesize(%q): %v", want, err)
		}
	}

	reused := newTTSForTest("tts-fixture", "First.", "Second.")
	if _, err := reused.Synthesize(context.Background(), tts.SynthesizeRequest{Sentence: "First."}); err != nil {
		t.Fatalf("position 0 Synthesize(\"First.\"): %v", err)
	}
	if _, err := reused.Synthesize(context.Background(), tts.SynthesizeRequest{Sentence: "First."}); err == nil {
		t.Fatal("position 1 Synthesize(\"First.\") accepted reused sentence; matcher is set-membership, not positional")
	}
}

// TestTTSSynthesizer_AudioMarkupPrompt_NonEmpty pins the cassette's contract
// against [tts.Synthesizer.AudioMarkupPrompt] (non-empty per ADR-0022); the
// cassette policy does not pin markup, but it must not return "".
func TestTTSSynthesizer_AudioMarkupPrompt_NonEmpty(t *testing.T) {
	t.Parallel()
	s := newTTSForTest("tts-fixture", "Anything.")
	if got := s.AudioMarkupPrompt(tts.Voice{}); got == "" {
		t.Fatal("AudioMarkupPrompt returned empty string; tts.Synthesizer requires non-empty markup")
	}
}
