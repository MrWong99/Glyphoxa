package orchestrator_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voicecassette"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voicetest"
)

// closedChanSynth is a [tts.Synthesizer] that accepts any sentence and returns
// an already-closed audio channel, so Dispatch's drain returns immediately. It
// lets the index-assignment contract be tested without a cassette's positional
// sentence match.
type closedChanSynth struct{}

func (closedChanSynth) Synthesize(context.Context, tts.SynthesizeRequest) (<-chan tts.AudioChunk, error) {
	ch := make(chan tts.AudioChunk)
	close(ch)
	return ch, nil
}

func (closedChanSynth) AudioMarkupPrompt(tts.Voice) string { return "" }

// startErrSynth is a [tts.Synthesizer] whose Synthesize always start-errors (nil
// channel, non-nil error), standing in for an empty VoiceID / auth failure / bad
// request — the start-error the #20 visibility fix is about.
type startErrSynth struct{}

func (startErrSynth) Synthesize(context.Context, tts.SynthesizeRequest) (<-chan tts.AudioChunk, error) {
	return nil, errors.New("synth start error")
}

func (startErrSynth) AudioMarkupPrompt(tts.Voice) string { return "" }

// TestTTS_DispatchPublishesInvokedOnStartError pins #20's per-sentence visibility:
// a sentence whose Synthesize start-errors must still publish TTSInvoked — the
// invoked-but-never-spoke signal — and return the error, rather than vanishing
// before any event. The event announces the dispatch ATTEMPT, not a success.
func TestTTS_DispatchPublishesInvokedOnStartError(t *testing.T) {
	h := voicetest.New(t)
	stage := orchestrator.NewTTS(h.Bus, startErrSynth{})

	const sentence = "this will fail to synthesize"
	if err := stage.Dispatch(context.Background(), sentence, voicetest.LiveElevenLabsVoice()); err == nil {
		t.Fatal("Dispatch: expected the synth start error to propagate")
	}

	voicetest.AssertEvent(t, h,
		func(e voiceevent.TTSInvoked) bool { return e.Sentence == sentence && e.Index == 0 },
		"tts.invoked published for a start-errored sentence",
	)
}

// TestTTS_Dispatch_RecordsProviderCallOutcomes pins the #125 provider-health
// wiring on the TTS stage: a successful Dispatch records tts_total exactly once
// and moves provider_calls with outcome=ok and no provider_errors; a Synthesize
// start error moves provider_calls outcome=error PLUS provider_errors and records
// NO tts_total (there was no synthesis to time). Labels stay the bounded
// tts/elevenlabs enums (ADR-0032).
func TestTTS_Dispatch_RecordsProviderCallOutcomes(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		h := voicetest.New(t)
		spy := &metricsSpy{}
		stage := orchestrator.NewTTS(h.Bus, closedChanSynth{},
			orchestrator.WithTTSMetrics(spy, observe.ProviderElevenLabs))

		if err := stage.Dispatch(context.Background(), "Aye.", voicetest.LiveElevenLabsVoice()); err != nil {
			t.Fatalf("Dispatch: %v", err)
		}

		_, ttsTotals, _, calls, errs := spy.snapshot()
		if len(ttsTotals) != 1 || ttsTotals[0] != observe.ProviderElevenLabs {
			t.Errorf("tts_total recorded %v, want exactly one elevenlabs span", ttsTotals)
		}
		want := providerCall{stage: observe.StageTTS, provider: observe.ProviderElevenLabs, outcome: observe.OutcomeOK}
		if len(calls) != 1 || calls[0] != want {
			t.Errorf("provider_calls = %+v, want one %+v", calls, want)
		}
		if len(errs) != 0 {
			t.Errorf("provider_errors = %+v, want none on the success path", errs)
		}
	})

	t.Run("start error", func(t *testing.T) {
		h := voicetest.New(t)
		spy := &metricsSpy{}
		stage := orchestrator.NewTTS(h.Bus, startErrSynth{},
			orchestrator.WithTTSMetrics(spy, observe.ProviderElevenLabs))

		if err := stage.Dispatch(context.Background(), "boom", voicetest.LiveElevenLabsVoice()); err == nil {
			t.Fatal("Dispatch: expected the synth start error to propagate")
		}

		_, ttsTotals, _, calls, errs := spy.snapshot()
		if len(ttsTotals) != 0 {
			t.Errorf("tts_total recorded %v on a start error, want none (no synthesis timed)", ttsTotals)
		}
		want := providerCall{stage: observe.StageTTS, provider: observe.ProviderElevenLabs, outcome: observe.OutcomeError}
		if len(calls) != 1 || calls[0] != want {
			t.Errorf("provider_calls = %+v, want one %+v", calls, want)
		}
		if len(errs) != 1 || errs[0] != (providerErr{stage: observe.StageTTS, provider: observe.ProviderElevenLabs}) {
			t.Errorf("provider_errors = %+v, want one tts/elevenlabs error", errs)
		}
	})
}

// TestTTS_Dispatch_KeylessRecordsNothing pins the keyless default: an option-less
// NewTTS never nil-panics on the metric calls — the recorder defaults to
// observe.Discard, so Dispatch works exactly as before the #125 wiring.
func TestTTS_Dispatch_KeylessRecordsNothing(t *testing.T) {
	h := voicetest.New(t)
	stage := orchestrator.NewTTS(h.Bus, closedChanSynth{})
	if err := stage.Dispatch(context.Background(), "Aye.", voicetest.LiveElevenLabsVoice()); err != nil {
		t.Fatalf("Dispatch on a keyless TTS stage: %v", err)
	}
}

// TestTTS_HelloTest_DispatchesSentence is TB6: the first TTS tracer bullet,
// per ADR-0021's TTS cassette policy.
//
// The orchestrator TTS stage is fed one sentence via Dispatch and a
// [voicecassette.TTSSynthesizer] standing in for the provider. The cassette
// (tests/voice-cassettes/tts-hello-test.yaml) pins the sentence the provider
// is expected to receive; on match it returns a closed empty audio channel.
// The assertion is on the bus event — "TTS invoked with sentence N" reaching
// the shared taxonomy (ADR-0020) — not on rendered audio, which ADR-0021
// explicitly excludes from the TTS cassette contract.
//
// This validates the [tts.Synthesizer] interface against the [voiceevent.Bus]
// contract without depending on any real provider or PCM output.
func TestTTS_HelloTest_DispatchesSentence(t *testing.T) {
	h := voicetest.New(t)
	synthesizer := voicecassette.LoadTTS(t, "tts-hello-test")
	stage := orchestrator.NewTTS(h.Bus, synthesizer)

	const sentence = "Of course — roll a d20 and add your wisdom modifier."
	voice := voicetest.LiveElevenLabsVoice()
	if err := stage.Dispatch(context.Background(), sentence, voice); err != nil {
		t.Fatalf("stage.Dispatch: %v", err)
	}

	voicetest.AssertEvent(t, h,
		func(e voiceevent.TTSInvoked) bool {
			return e.Sentence == sentence && e.Index == 0
		},
		"tts.invoked with sentence "+sentence+" at index 0",
	)
}

// TestTTS_ConcurrentDispatch_AssignsUniqueIndices pins that the per-turn index
// is assigned race-free: concurrent Dispatch calls (an Ensemble Turn or a
// barge-in canceller, both anticipated on the stage) must each publish a
// distinct index covering exactly 0..N-1, never a duplicate or a gap. Run under
// -race it also guards the nextIndex field itself.
func TestTTS_ConcurrentDispatch_AssignsUniqueIndices(t *testing.T) {
	h := voicetest.New(t)
	stage := orchestrator.NewTTS(h.Bus, closedChanSynth{})
	voice := voicetest.LiveElevenLabsVoice()

	const n = 64
	var wg sync.WaitGroup
	wg.Add(n)
	for range n {
		go func() {
			defer wg.Done()
			if err := stage.Dispatch(context.Background(), "line", voice); err != nil {
				t.Errorf("Dispatch: %v", err)
			}
		}()
	}
	wg.Wait()

	seen := make(map[int]bool, n)
	for _, e := range h.Events() {
		if inv, ok := e.(voiceevent.TTSInvoked); ok {
			if seen[inv.Index] {
				t.Errorf("duplicate TTS index %d", inv.Index)
			}
			seen[inv.Index] = true
		}
	}
	if len(seen) != n {
		t.Fatalf("saw %d distinct indices, want %d", len(seen), n)
	}
	for i := range n {
		if !seen[i] {
			t.Errorf("missing index %d (indices must be a gapless 0..%d)", i, n-1)
		}
	}
}
