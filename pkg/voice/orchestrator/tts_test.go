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

// TestTTS_Dispatch_MetersSubmittedCharacters is the #127 TTS AC (ADR-0045): a
// successful Synthesize start meters the submitted characters counted as utf8 RUNES
// (not bytes — ElevenLabs bills characters), even if a later barge cuts the audio; a
// start error meters nothing. "Hällo." is 6 runes but 7 bytes (ä is two bytes).
func TestTTS_Dispatch_MetersSubmittedCharacters(t *testing.T) {
	t.Run("successful start meters runes", func(t *testing.T) {
		h := voicetest.New(t)
		spy := &metricsSpy{}
		stage := orchestrator.NewTTS(h.Bus, closedChanSynth{},
			orchestrator.WithTTSMetrics(spy, observe.ProviderElevenLabs))

		if err := stage.Dispatch(context.Background(), "Hällo.", voicetest.LiveElevenLabsVoice()); err != nil {
			t.Fatalf("Dispatch: %v", err)
		}
		got := spy.characters()
		want := ttsCharsRec{provider: observe.ProviderElevenLabs, chars: 6}
		if len(got) != 1 || got[0] != want {
			t.Errorf("tts_characters = %v, want one %+v (6 runes, not 7 bytes)", got, want)
		}
	})

	t.Run("start error meters nothing", func(t *testing.T) {
		h := voicetest.New(t)
		spy := &metricsSpy{}
		stage := orchestrator.NewTTS(h.Bus, startErrSynth{},
			orchestrator.WithTTSMetrics(spy, observe.ProviderElevenLabs))

		if err := stage.Dispatch(context.Background(), "Hällo.", voicetest.LiveElevenLabsVoice()); err == nil {
			t.Fatal("Dispatch: expected the synth start error to propagate")
		}
		if got := spy.characters(); len(got) != 0 {
			t.Errorf("tts_characters on a start error = %v, want none (no submission billed)", got)
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

// errTerminalSynth emits some audio chunks, then a terminal Err chunk (#436 —
// the mid-stream failure signal), then closes. cancel, when non-nil, is called
// before the terminal chunk so a test can simulate a barge racing the failure.
type errTerminalSynth struct {
	err    error
	cancel context.CancelFunc
}

func (s errTerminalSynth) Synthesize(context.Context, tts.SynthesizeRequest) (<-chan tts.AudioChunk, error) {
	ch := make(chan tts.AudioChunk, 3)
	ch <- tts.AudioChunk{PCM: []byte{0, 0}}
	ch <- tts.AudioChunk{PCM: []byte{0, 0}}
	if s.cancel != nil {
		s.cancel()
	}
	ch <- tts.AudioChunk{Err: s.err}
	close(ch)
	return ch, nil
}

func (errTerminalSynth) AudioMarkupPrompt(tts.Voice) string { return "" }

// TestTTS_Dispatch_MidStreamErrTerminalFails pins the #436 fix: a synthesis
// stream that dies mid-delivery (terminal Err chunk) under a LIVE ctx is a
// FAILED dispatch — Dispatch returns an error (so the turn module never commits
// the half-heard sentence), records the vendor fault (provider_call error +
// provider_error, no tts_total), and publishes [voiceevent.TTSStreamFailed] so
// the Transcript Chunker retracts the sentence.
func TestTTS_Dispatch_MidStreamErrTerminalFails(t *testing.T) {
	h := voicetest.New(t)
	spy := &metricsSpy{}
	stage := orchestrator.NewTTS(h.Bus, errTerminalSynth{err: errors.New("websocket dropped")},
		orchestrator.WithTTSMetrics(spy, observe.ProviderElevenLabs))

	ctx := voiceevent.WithTurnID(context.Background(), "T436")
	err := stage.Dispatch(ctx, "Half a sentence.", voicetest.LiveElevenLabsVoice())
	if err == nil {
		t.Fatal("Dispatch: expected an error for a mid-stream terminal Err chunk")
	}

	_, ttsTotals, _, calls, errs := spy.snapshot()
	if len(ttsTotals) != 0 {
		t.Errorf("tts_total recorded %v for a failed delivery, want none", ttsTotals)
	}
	want := providerCall{stage: observe.StageTTS, provider: observe.ProviderElevenLabs, outcome: observe.OutcomeError}
	if len(calls) != 1 || calls[0] != want {
		t.Errorf("provider_calls = %+v, want one %+v", calls, want)
	}
	if len(errs) != 1 {
		t.Errorf("provider_errors = %+v, want exactly one (a mid-stream failure is a vendor fault)", errs)
	}
	voicetest.WaitEvent(t, h, 2*time.Second, func(e voiceevent.TTSStreamFailed) bool {
		return e.TurnID == "T436"
	}, "tts.stream_failed published for the failed sentence's turn")
}

// TestTTS_Dispatch_MidStreamErrDuringBargeIsCut pins the #401 carve-out: when the
// turn ctx is CANCELLED by the time the stream ends (a barge racing a provider
// hiccup), the cut owns the semantics — Dispatch returns nil exactly as for any
// barge-cut stream, no fault is recorded, and no TTSStreamFailed is published.
// The sentence-grain commit decision stays with the dispatch site's ctx re-check.
func TestTTS_Dispatch_MidStreamErrDuringBargeIsCut(t *testing.T) {
	h := voicetest.New(t)
	spy := &metricsSpy{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stage := orchestrator.NewTTS(h.Bus, errTerminalSynth{err: errors.New("websocket dropped"), cancel: cancel},
		orchestrator.WithTTSMetrics(spy, observe.ProviderElevenLabs))

	if err := stage.Dispatch(ctx, "Barged anyway.", voicetest.LiveElevenLabsVoice()); err != nil {
		t.Fatalf("Dispatch: %v (a barge-cut stream keeps its nil return; the ctx re-check owns the commit)", err)
	}

	_, _, _, _, errs := spy.snapshot()
	if len(errs) != 0 {
		t.Errorf("provider_errors = %+v on a barged stream, want none (the caller cut it)", errs)
	}
	voicetest.AssertNoEvent[voiceevent.TTSStreamFailed](t, h)
}

// TestTTS_Dispatch_MidStreamCloseIsAcceptedBlindSpot documents the remaining
// LEGACY blind spot (#239 review, narrowed by #436): a channel that delivers some
// chunks then closes with no error AND no terminal Err chunk — an adapter that
// does not implement the #436 terminal-Err contract — still reads as a clean
// completion here. Dispatch records outcome=ok and NO provider_error. Adapters
// close the gap by emitting the terminal Err chunk (see tts.AudioChunk.Err);
// this stage cannot distinguish a truncated no-signal stream from a complete one.
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

// TestTTS_Dispatch_LookaheadSkipsInvoke pins F1 (#375): a Dispatch under a
// PlaybackLookahead ctx does NOT publish TTSInvoked and does NOT reserve an index —
// the reaction's first sentence is announced later, at release, via PublishInvoked —
// but it is still fully synthesized, drained, and character-metered (ADR-0045).
func TestTTS_Dispatch_LookaheadSkipsInvoke(t *testing.T) {
	h := voicetest.New(t)
	spy := &metricsSpy{}
	var invoked int
	voiceevent.On(h.Bus, func(voiceevent.TTSInvoked) { invoked++ })

	synth := &flakySynth{} // succeeds first call, closed channel
	stage := orchestrator.NewTTS(h.Bus, synth, orchestrator.WithTTSMetrics(spy, observe.ProviderElevenLabs))

	ctx := voiceevent.WithPlaybackLookahead(voiceevent.WithTurnID(context.Background(), "R"))
	if err := stage.Dispatch(ctx, "Aye.", voicetest.LiveElevenLabsVoice()); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if invoked != 0 {
		t.Fatalf("TTSInvoked published %d times for a look-ahead Dispatch, want 0", invoked)
	}
	if synth.calls != 1 {
		t.Fatalf("Synthesize called %d times, want 1 (still synthesized)", synth.calls)
	}
	chars := spy.characters()
	if len(chars) != 1 || chars[0].chars != 4 {
		t.Fatalf("TTSCharacters = %+v, want one record of 4 (still metered)", chars)
	}
}

// TestTTS_PublishInvoked_MonotonicIndex pins F1 (#375): PublishInvoked emits a
// TTSInvoked carrying the given turn id + sentence and a monotonically increasing
// index drawn from the SAME counter Dispatch uses, so a released look-ahead sentence
// takes a distinct in-turn slot.
func TestTTS_PublishInvoked_MonotonicIndex(t *testing.T) {
	h := voicetest.New(t)
	var got []voiceevent.TTSInvoked
	voiceevent.On(h.Bus, func(e voiceevent.TTSInvoked) { got = append(got, e) })

	synth := &flakySynth{}
	stage := orchestrator.NewTTS(h.Bus, synth)

	// A normal dispatch takes index 0; PublishInvoked then takes index 1.
	if err := stage.Dispatch(voiceevent.WithTurnID(context.Background(), "T"), "First.", voicetest.LiveElevenLabsVoice()); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	stage.PublishInvoked("R", "Reaction.")

	if len(got) != 2 {
		t.Fatalf("TTSInvoked count = %d, want 2", len(got))
	}
	if got[1].TurnID != "R" || got[1].Sentence != "Reaction." {
		t.Fatalf("PublishInvoked event = %+v, want TurnID=R Sentence=Reaction.", got[1])
	}
	if got[1].Index <= got[0].Index {
		t.Fatalf("PublishInvoked index %d not > Dispatch index %d (must be monotonic)", got[1].Index, got[0].Index)
	}
	if got[1].At.IsZero() {
		t.Fatal("PublishInvoked event has zero At")
	}
}
