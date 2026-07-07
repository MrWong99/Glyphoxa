package orchestrator_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/providererr"
	"github.com/MrWong99/Glyphoxa/pkg/voice/retry"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voicecassette"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voicetest"
)

// flakySynth returns errsBeforeOK failures (each the pinned err) then a
// completed (closed, empty) audio channel, counting every call so a retry test
// can prove the loop re-drove Synthesize exactly as expected.
type flakySynth struct {
	err          error
	errsBeforeOK int
	calls        int
}

func (s *flakySynth) Synthesize(context.Context, tts.SynthesizeRequest) (<-chan tts.AudioChunk, error) {
	s.calls++
	if s.calls <= s.errsBeforeOK {
		return nil, s.err
	}
	ch := make(chan tts.AudioChunk)
	close(ch)
	return ch, nil
}

func (*flakySynth) AudioMarkupPrompt(tts.Voice) string { return "" }

func synth503() error {
	return &providererr.HTTPError{Op: "elevenlabs.Synthesize", StatusCode: 503, Status: "503 Service Unavailable", Body: "down"}
}

// TestTTS_Dispatch_RetriesTransientThenSucceeds pins the TTS AC: a synthesizer
// that returns one 503 then succeeds delivers the sentence, publishes exactly ONE
// TTSInvoked (per Dispatch, never per attempt — ADR-0044), and records the final
// outcome only: one tts_total span and one provider_call(ok).
func TestTTS_Dispatch_RetriesTransientThenSucceeds(t *testing.T) {
	h := voicetest.New(t)
	spy := &metricsSpy{}
	var invoked int
	voiceevent.On(h.Bus, func(voiceevent.TTSInvoked) { invoked++ })

	synth := &flakySynth{err: synth503(), errsBeforeOK: 1}
	stage := orchestrator.NewTTS(h.Bus, synth,
		orchestrator.WithTTSMetrics(spy, observe.ProviderElevenLabs),
		orchestrator.WithTTSRetry(instantRetry()))

	if err := stage.Dispatch(context.Background(), "Aye.", voicetest.LiveElevenLabsVoice()); err != nil {
		t.Fatalf("Dispatch after one transient 503: %v", err)
	}
	if synth.calls != 2 {
		t.Errorf("synth calls = %d, want 2 (one 503 retried once)", synth.calls)
	}
	if invoked != 1 {
		t.Errorf("TTSInvoked published %d times, want exactly 1 (one per Dispatch, never per attempt)", invoked)
	}

	_, ttsTotals, _, calls, errs := spy.snapshot()
	if len(ttsTotals) != 1 {
		t.Errorf("tts_total recorded %d spans, want 1 (final outcome only)", len(ttsTotals))
	}
	want := providerCall{stage: observe.StageTTS, provider: observe.ProviderElevenLabs, outcome: observe.OutcomeOK}
	if len(calls) != 1 || calls[0] != want {
		t.Errorf("provider_calls = %+v, want one %+v", calls, want)
	}
	if len(errs) != 0 {
		t.Errorf("provider_errors = %+v, want none (the retry recovered)", errs)
	}
}

// TestTTS_Dispatch_CancelMidBackoffIsCanceled pins the barge contract on the TTS
// stage: a ctx cancelled while a retry backoff is sleeping aborts promptly with
// the ctx error, and the final outcome is canceled — a barge is not a vendor
// fault, so provider_errors does not move (#239 rule, ADR-0027).
func TestTTS_Dispatch_CancelMidBackoffIsCanceled(t *testing.T) {
	h := voicetest.New(t)
	spy := &metricsSpy{}
	ctx, cancel := context.WithCancel(context.Background())

	synth := &flakySynth{err: synth503(), errsBeforeOK: 99} // always transient-fails
	p := retry.Policy{
		Rand: func() float64 { return 1 },
		Sleep: func(sctx context.Context, _ time.Duration) error {
			cancel() // the barge lands during the backoff
			<-sctx.Done()
			return sctx.Err()
		},
	}
	stage := orchestrator.NewTTS(h.Bus, synth,
		orchestrator.WithTTSMetrics(spy, observe.ProviderElevenLabs),
		orchestrator.WithTTSRetry(p))

	err := stage.Dispatch(ctx, "Aye.", voicetest.LiveElevenLabsVoice())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Dispatch err = %v, want context.Canceled (barge mid-backoff)", err)
	}
	if synth.calls != 1 {
		t.Errorf("synth calls = %d, want 1 (the cancel aborted before the retry)", synth.calls)
	}

	_, ttsTotals, _, calls, errs := spy.snapshot()
	if len(ttsTotals) != 0 {
		t.Errorf("tts_total = %v, want none (no successful synthesis)", ttsTotals)
	}
	want := providerCall{stage: observe.StageTTS, provider: observe.ProviderElevenLabs, outcome: observe.OutcomeCanceled}
	if len(calls) != 1 || calls[0] != want {
		t.Errorf("provider_calls = %+v, want one %+v", calls, want)
	}
	if len(errs) != 0 {
		t.Errorf("provider_errors = %+v on a barge, want none (a cancel is not a fault)", errs)
	}
}

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

// TestTTS_Dispatch_BargeCancelIsNotFault pins the #239 refinement on the TTS stage:
// a barge-in that cancels the ctx before Synthesize returns records
// provider_calls with outcome=canceled and does NOT bump provider_errors — a
// caller cancel is not a vendor fault. There is no tts_total (no synthesis).
func TestTTS_Dispatch_BargeCancelIsNotFault(t *testing.T) {
	h := voicetest.New(t)
	spy := &metricsSpy{}
	// startErrSynth returns an error; a pre-cancelled ctx makes the outcome canceled
	// (the ctx state is authoritative, matching a real barge cutting the call).
	stage := orchestrator.NewTTS(h.Bus, startErrSynth{},
		orchestrator.WithTTSMetrics(spy, observe.ProviderElevenLabs))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := stage.Dispatch(ctx, "boom", voicetest.LiveElevenLabsVoice()); err == nil {
		t.Fatal("Dispatch: expected an error on the cancelled path")
	}

	_, ttsTotals, _, calls, errs := spy.snapshot()
	if len(ttsTotals) != 0 {
		t.Errorf("tts_total recorded %v on a cancelled start, want none", ttsTotals)
	}
	want := providerCall{stage: observe.StageTTS, provider: observe.ProviderElevenLabs, outcome: observe.OutcomeCanceled}
	if len(calls) != 1 || calls[0] != want {
		t.Errorf("provider_calls = %+v, want one %+v", calls, want)
	}
	if len(errs) != 0 {
		t.Errorf("provider_errors = %+v on a barge cancel, want none (a cancel is not a fault)", errs)
	}
}

// earlyCloseSynth emits a couple of audio chunks then closes the channel WITHOUT
// an error and with a live ctx — a mid-synthesis provider outage the Synthesizer
// contract renders indistinguishable from a clean completion.
type earlyCloseSynth struct{}

func (earlyCloseSynth) Synthesize(context.Context, tts.SynthesizeRequest) (<-chan tts.AudioChunk, error) {
	ch := make(chan tts.AudioChunk, 2)
	ch <- tts.AudioChunk{PCM: []byte{0, 0}}
	ch <- tts.AudioChunk{PCM: []byte{0, 0}}
	close(ch)
	return ch, nil
}

func (earlyCloseSynth) AudioMarkupPrompt(tts.Voice) string { return "" }

// TestTTS_Dispatch_MidStreamCloseIsAcceptedBlindSpot documents the accepted
// Synthesizer-contract blind spot (#239 review): a channel that delivers some
// chunks then closes with no error — a streaming outage mid-synthesis — is
// INVISIBLE at this seam. Dispatch sees a clean close, so it records outcome=ok and
// NO provider_error. This is by design: the tts.Synthesizer channel-close carries
// no error signal, so a truncated stream cannot be distinguished from a complete
// one here. If that blind spot must be closed, it belongs in the adapter, not this
// stage.
func TestTTS_Dispatch_MidStreamCloseIsAcceptedBlindSpot(t *testing.T) {
	h := voicetest.New(t)
	spy := &metricsSpy{}
	stage := orchestrator.NewTTS(h.Bus, earlyCloseSynth{},
		orchestrator.WithTTSMetrics(spy, observe.ProviderElevenLabs))

	if err := stage.Dispatch(context.Background(), "cut short", voicetest.LiveElevenLabsVoice()); err != nil {
		t.Fatalf("Dispatch: %v (a clean mid-stream close is not an error at this seam)", err)
	}

	_, ttsTotals, _, calls, errs := spy.snapshot()
	if len(ttsTotals) != 1 {
		t.Errorf("tts_total recorded %d, want 1 (the drain completed)", len(ttsTotals))
	}
	want := providerCall{stage: observe.StageTTS, provider: observe.ProviderElevenLabs, outcome: observe.OutcomeOK}
	if len(calls) != 1 || calls[0] != want {
		t.Errorf("provider_calls = %+v, want one %+v (mid-stream close reads as ok)", calls, want)
	}
	if len(errs) != 0 {
		t.Errorf("provider_errors = %+v, want none (the blind spot: a truncated stream is invisible here)", errs)
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
