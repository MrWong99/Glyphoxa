package orchestrator_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/pkg/voice/address"
	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/providererr"
	"github.com/MrWong99/Glyphoxa/pkg/voice/retry"
	"github.com/MrWong99/Glyphoxa/pkg/voice/stt"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/vad"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voicecassette"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voicetest"
)

// flakyRecognizer returns errsBeforeOK failures (each the pinned err) then the
// pinned transcript, counting every call so a test can prove the retry loop
// re-drove the recognizer exactly as many times as expected.
type flakyRecognizer struct {
	err          error
	errsBeforeOK int
	transcript   stt.Transcript
	calls        int
}

func (r *flakyRecognizer) Transcribe(context.Context, []audio.Frame) (stt.Transcript, error) {
	r.calls++
	if r.calls <= r.errsBeforeOK {
		return stt.Transcript{}, r.err
	}
	return r.transcript, nil
}

// countingHungRecognizer is a [hangingRecognizer] that counts its calls, so a
// budget test can prove the total-budget timeout wraps the WHOLE retry loop —
// the hung call is entered once, never once per attempt (the #91 wedge guard).
type countingHungRecognizer struct{ calls int }

func (r *countingHungRecognizer) Transcribe(ctx context.Context, _ []audio.Frame) (stt.Transcript, error) {
	r.calls++
	<-ctx.Done()
	return stt.Transcript{}, ctx.Err()
}

// instantRetry is a [retry.Policy] with the wall-clock backoff replaced by a
// no-op sleep and a fixed jitter, so a retry test is deterministic and never
// sleeps (ADR-0021). Defaults (3 attempts) otherwise stand.
func instantRetry() retry.Policy {
	return retry.Policy{
		Sleep: func(context.Context, time.Duration) error { return nil },
		Rand:  func() float64 { return 0 },
	}
}

// http429 / http401 build the typed provider errors the retry helper classifies.
func http429() error {
	return &providererr.HTTPError{Op: "elevenlabs.Transcribe", StatusCode: 429, Status: "429 Too Many Requests", Body: "slow down"}
}
func http401() error {
	return &providererr.HTTPError{Op: "elevenlabs.Transcribe", StatusCode: 401, Status: "401 Unauthorized", Body: "bad key"}
}

// TestSTT_Transcribe_RetriesTransientThenSucceeds pins the headline AC: a
// recognizer that returns one 429 then succeeds completes the turn normally
// (one STTFinal), the worker recovers, and metrics record the FINAL outcome only
// — exactly one stt_request span and one provider_call(ok), never one per
// attempt (ADR-0044 §metrics).
func TestSTT_Transcribe_RetriesTransientThenSucceeds(t *testing.T) {
	h := voicetest.New(t)
	spy := &metricsSpy{}
	var finals int
	voiceevent.On(h.Bus, func(voiceevent.STTFinal) { finals++ })

	rec := &flakyRecognizer{err: http429(), errsBeforeOK: 1, transcript: stt.Transcript{Text: "roll a d20"}}
	stage := orchestrator.NewSTT(h.Bus, rec,
		orchestrator.WithSTTMetrics(spy, observe.ProviderElevenLabs),
		orchestrator.WithSTTRetry(instantRetry()))

	if err := stage.Transcribe(context.Background(), []audio.Frame{}); err != nil {
		t.Fatalf("Transcribe after one transient 429: %v", err)
	}
	if rec.calls != 2 {
		t.Errorf("recognizer calls = %d, want 2 (one 429 retried once)", rec.calls)
	}
	if finals != 1 {
		t.Errorf("STTFinal published %d times, want exactly 1", finals)
	}

	sttReqs, _, _, calls, errs := spy.snapshot()
	if len(sttReqs) != 1 {
		t.Errorf("stt_request recorded %d spans, want 1 (final outcome only)", len(sttReqs))
	}
	want := providerCall{stage: observe.StageSTT, provider: observe.ProviderElevenLabs, outcome: observe.OutcomeOK}
	if len(calls) != 1 || calls[0] != want {
		t.Errorf("provider_calls = %+v, want one %+v", calls, want)
	}
	if len(errs) != 0 {
		t.Errorf("provider_errors = %+v, want none (the retry recovered)", errs)
	}
}

// TestSTT_Transcribe_NonRetryableFailsFast pins that a 401 (invalid key) fails
// on the first attempt with no retry and no sleep — under-retry is the safe
// default for a non-transient auth failure.
func TestSTT_Transcribe_NonRetryableFailsFast(t *testing.T) {
	h := voicetest.New(t)
	spy := &metricsSpy{}
	p := instantRetry()
	p.Sleep = func(context.Context, time.Duration) error {
		t.Error("a non-retryable 401 must not sleep")
		return nil
	}
	rec := &flakyRecognizer{err: http401(), errsBeforeOK: 99}
	stage := orchestrator.NewSTT(h.Bus, rec,
		orchestrator.WithSTTMetrics(spy, observe.ProviderElevenLabs),
		orchestrator.WithSTTRetry(p))

	err := stage.Transcribe(context.Background(), []audio.Frame{})
	if err == nil {
		t.Fatal("Transcribe returned nil on a 401; want the auth error")
	}
	if rec.calls != 1 {
		t.Errorf("recognizer calls = %d, want 1 (a 401 is not retried)", rec.calls)
	}
	_, _, _, calls, errs := spy.snapshot()
	want := providerCall{stage: observe.StageSTT, provider: observe.ProviderElevenLabs, outcome: observe.OutcomeError}
	if len(calls) != 1 || calls[0] != want {
		t.Errorf("provider_calls = %+v, want one %+v", calls, want)
	}
	if len(errs) != 1 {
		t.Errorf("provider_errors = %+v, want exactly one fault", errs)
	}
}

// TestSTT_Transcribe_HungRecognizerBudgetNotMultiplied is THE budget test: the
// per-request timeout is the TOTAL budget wrapping the whole retry loop, not a
// per-attempt bound. A hung recognizer under a 50ms timeout is entered exactly
// once and the whole call returns in ~one timeout, never N×timeout — the fix
// that stops the retry policy from recreating the #91 serial-worker wedge.
func TestSTT_Transcribe_HungRecognizerBudgetNotMultiplied(t *testing.T) {
	h := voicetest.New(t)
	rec := &countingHungRecognizer{}
	stage := orchestrator.NewSTT(h.Bus, rec,
		orchestrator.WithSTTTimeout(50*time.Millisecond),
		orchestrator.WithSTTRetry(instantRetry()))

	start := time.Now()
	err := stage.Transcribe(context.Background(), []audio.Frame{})
	elapsed := time.Since(start)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context.DeadlineExceeded (the total budget firing)", err)
	}
	if rec.calls != 1 {
		t.Errorf("recognizer called %d times, want 1 (total budget wraps the loop, not per-attempt)", rec.calls)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("Transcribe took %v; a per-attempt budget would be ~N×50ms — the total budget was multiplied", elapsed)
	}
}

// batchFrames builds n 32 ms / 16 kHz PCM frames (512 samples each) for the STT
// audio-seconds meter tests — n*32ms of submitted audio.
func batchFrames(t *testing.T, n int) []audio.Frame {
	t.Helper()
	out := make([]audio.Frame, n)
	for i := range out {
		f, err := audio.NewFrame(make([]int16, 512), 16000, 32)
		if err != nil {
			t.Fatalf("audio.NewFrame: %v", err)
		}
		out[i] = f
	}
	return out
}

// TestSTT_Transcribe_MetersSubmittedAudioSeconds is the #127 STT AC (ADR-0045/0042):
// a successful batch Transcribe meters the submitted audio length (N*32ms), and a
// failed one meters nothing — only audio the recognizer actually consumed is billed.
func TestSTT_Transcribe_MetersSubmittedAudioSeconds(t *testing.T) {
	t.Run("success meters N*32ms", func(t *testing.T) {
		h := voicetest.New(t)
		spy := &metricsSpy{}
		stage := orchestrator.NewSTT(h.Bus, stubRecognizer{transcript: stt.Transcript{Text: "roll a d20"}},
			orchestrator.WithSTTMetrics(spy, observe.ProviderElevenLabs))

		if err := stage.Transcribe(context.Background(), batchFrames(t, 5)); err != nil {
			t.Fatalf("Transcribe: %v", err)
		}
		got := spy.audioSeconds()
		if len(got) != 1 || got[0] != 5*32*time.Millisecond {
			t.Errorf("stt_audio_seconds = %v, want one 160ms span (5×32ms)", got)
		}
	})

	t.Run("failure meters nothing", func(t *testing.T) {
		h := voicetest.New(t)
		spy := &metricsSpy{}
		stage := orchestrator.NewSTT(h.Bus, errorRecognizer{},
			orchestrator.WithSTTMetrics(spy, observe.ProviderElevenLabs))

		if err := stage.Transcribe(context.Background(), batchFrames(t, 5)); err == nil {
			t.Fatal("Transcribe returned nil on a provider error")
		}
		if got := spy.audioSeconds(); len(got) != 0 {
			t.Errorf("stt_audio_seconds on a failed transcribe = %v, want none (no audio billed)", got)
		}
	})
}

// stubRecognizer is a [stt.Recognizer] that returns a pinned [stt.Transcript]
// regardless of input. Used to exercise the orchestrator stage's republish
// contract independently of any real provider or cassette.
type stubRecognizer struct {
	transcript stt.Transcript
}

func (s stubRecognizer) Transcribe(context.Context, []audio.Frame) (stt.Transcript, error) {
	return s.transcript, nil
}

// hangingRecognizer is a [stt.Recognizer] that never returns on its own — it
// blocks until its context is cancelled, then returns ctx.Err(). It stands in for
// a black-holed provider POST (the live wedge: a hung ElevenLabs scribe call that
// the conversation-lifetime context never cancels).
type hangingRecognizer struct{}

func (hangingRecognizer) Transcribe(ctx context.Context, _ []audio.Frame) (stt.Transcript, error) {
	<-ctx.Done()
	return stt.Transcript{}, ctx.Err()
}

// errorRecognizer is a [stt.Recognizer] that always returns a non-nil error with
// a live context — a provider-side failure (a 500, a bad request), distinct from
// the hung-then-cancelled recognizer whose error is ctx-driven.
type errorRecognizer struct{}

func (errorRecognizer) Transcribe(context.Context, []audio.Frame) (stt.Transcript, error) {
	return stt.Transcript{}, errors.New("scribe 500")
}

// TestSTT_Transcribe_RecordsProviderCallOutcomes pins the #125 provider-health
// wiring on the batch STT stage: a successful Transcribe moves provider_calls with
// outcome=ok and no provider_errors; a provider error moves outcome=error PLUS
// provider_errors; a call that hangs past the per-request timeout moves
// outcome=timeout PLUS provider_errors. In all three the stt_request round-trip
// histogram still records (the regression guard — it must not be dropped by the
// new counter wiring). Labels stay the bounded stt/elevenlabs enums (ADR-0032).
func TestSTT_Transcribe_RecordsProviderCallOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name        string
		recognizer  stt.Recognizer
		opts        []orchestrator.STTOption
		wantOutcome observe.Outcome
		wantErr     bool
	}{
		{
			name:        "success",
			recognizer:  stubRecognizer{transcript: stt.Transcript{Text: "roll a d20"}},
			wantOutcome: observe.OutcomeOK,
		},
		{
			name:        "provider error",
			recognizer:  errorRecognizer{},
			wantOutcome: observe.OutcomeError,
			wantErr:     true,
		},
		{
			name:        "hung past timeout",
			recognizer:  hangingRecognizer{},
			opts:        []orchestrator.STTOption{orchestrator.WithSTTTimeout(30 * time.Millisecond)},
			wantOutcome: observe.OutcomeTimeout,
			wantErr:     true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := voicetest.New(t)
			spy := &metricsSpy{}
			opts := append([]orchestrator.STTOption{
				orchestrator.WithSTTMetrics(spy, observe.ProviderElevenLabs),
			}, tc.opts...)
			stage := orchestrator.NewSTT(h.Bus, tc.recognizer, opts...)

			err := stage.Transcribe(context.Background(), []audio.Frame{})
			if tc.wantErr != (err != nil) {
				t.Fatalf("Transcribe err = %v, wantErr = %v", err, tc.wantErr)
			}

			sttReqs, _, _, calls, errs := spy.snapshot()

			// The round-trip histogram records on every path — success, error, timeout.
			if len(sttReqs) != 1 || sttReqs[0] != observe.ProviderElevenLabs {
				t.Errorf("stt_request recorded %v, want exactly one elevenlabs span", sttReqs)
			}
			if len(calls) != 1 {
				t.Fatalf("provider_calls recorded %d times, want exactly 1", len(calls))
			}
			want := providerCall{stage: observe.StageSTT, provider: observe.ProviderElevenLabs, outcome: tc.wantOutcome}
			if calls[0] != want {
				t.Errorf("provider_call = %+v, want %+v", calls[0], want)
			}
			if tc.wantErr {
				if len(errs) != 1 || errs[0] != (providerErr{stage: observe.StageSTT, provider: observe.ProviderElevenLabs}) {
					t.Errorf("provider_errors = %+v, want one stt/elevenlabs error", errs)
				}
			} else if len(errs) != 0 {
				t.Errorf("provider_errors = %+v, want none on the success path", errs)
			}
		})
	}
}

// TestSTT_Transcribe_BargeCancelIsNotFault pins the #239 refinement: a barge-in
// that cancels the recognizer ctx mid-call records provider_calls with
// outcome=canceled and does NOT bump provider_errors — a caller cancel is not a
// vendor fault and must not inflate the error ratio. The per-request timeout is
// disabled so ONLY the caller cancel drives the outcome. stt_request still records.
func TestSTT_Transcribe_BargeCancelIsNotFault(t *testing.T) {
	h := voicetest.New(t)
	spy := &metricsSpy{}
	stage := orchestrator.NewSTT(h.Bus, hangingRecognizer{},
		orchestrator.WithSTTMetrics(spy, observe.ProviderElevenLabs),
		orchestrator.WithSTTTimeout(0)) // disable the per-request bound: only the caller cancel drives it

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- stage.Transcribe(ctx, []audio.Frame{}) }()
	time.Sleep(20 * time.Millisecond) // let it reach the blocked recognizer
	cancel()                          // barge-in

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Transcribe returned nil on a cancelled ctx; want the cancel error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Transcribe did not return after the ctx was cancelled")
	}

	sttReqs, _, _, calls, errs := spy.snapshot()
	if len(sttReqs) != 1 {
		t.Errorf("stt_request recorded %d spans on a barge, want 1", len(sttReqs))
	}
	want := providerCall{stage: observe.StageSTT, provider: observe.ProviderElevenLabs, outcome: observe.OutcomeCanceled}
	if len(calls) != 1 || calls[0] != want {
		t.Errorf("provider_calls = %+v, want one %+v", calls, want)
	}
	if len(errs) != 0 {
		t.Errorf("provider_errors = %+v on a barge cancel, want none (a cancel is not a fault)", errs)
	}
}

// TestSTT_Transcribe_BoundsHungRecognizer pins the wedge fix: a recognizer call
// that would otherwise block forever is bounded by the stage's per-request
// timeout ([orchestrator.WithSTTTimeout]). Without the bound, the single serial
// transcription worker stalls on the hung POST and every later utterance starves
// — the NPC goes permanently silent (observed live). With it, the call returns a
// deadline error promptly so the worker recovers, and no STTFinal is published
// for the failed turn.
func TestSTT_Transcribe_BoundsHungRecognizer(t *testing.T) {
	h := voicetest.New(t)

	var finals int
	voiceevent.On(h.Bus, func(voiceevent.STTFinal) { finals++ }) // sync bus: runs on Publish's goroutine

	stage := orchestrator.NewSTT(h.Bus, hangingRecognizer{}, orchestrator.WithSTTTimeout(50*time.Millisecond))

	start := time.Now()
	err := stage.Transcribe(context.Background(), []audio.Frame{})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Transcribe returned nil on a hung recognizer; want a deadline error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v; want context.DeadlineExceeded (the per-request timeout firing)", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("Transcribe blocked %v on a 50ms timeout; the per-request bound did not fire", elapsed)
	}
	if finals != 0 {
		t.Fatalf("published %d STTFinal for a timed-out transcription; want 0 (no transcript to relay)", finals)
	}
}

// TestSTT_HelloTest_EmitsFinal is TB5: the first cassette-backed tracer
// bullet (ADR-0021). It feeds the "hello-test" clip through the orchestrator
// STT stage backed by a replay-only [voicecassette.STTRecognizer] and
// asserts that exactly the recorded transcript reaches the shared bus as a
// [voiceevent.STTFinal] (ADR-0020).
//
// The cassette under tests/voice-cassettes/stt-hello-test.yaml pins both the
// audio fingerprint and the transcript text; re-recording happens under
// `-tags=record` against the live ElevenLabs scribe_v2 adapter (ADR-0021).
// The assertion compares the bus event against the clip's known utterance
// after normalization (see [voicetest.NormalizeTranscript]) so it pins the
// transcribed words while tolerating provider-specific casing, spacing, and
// punctuation — scribe_v2 drops this utterance's trailing period, which an
// exact-match assertion would (and did) flag as a spurious failure.
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

	want := voicetest.NormalizeTranscript(helloUtterance)
	voicetest.AssertEvent(t, h,
		func(e voiceevent.STTFinal) bool { return voicetest.NormalizeTranscript(e.Text) == want },
		"stt.final matching utterance "+helloUtterance,
	)
}

// TestSTT_TTRPGIntro_TranscribesBothLanguages drives the full slice-1 voice
// pipeline — audio → VAD → STT → address detection → TTS — through the
// [orchestrator.Conversation] façade (ADR-0026) rather than spelling the bus
// wiring out inline. Feeding frames is all the test does imperatively; the
// conversation segments utterances, transcribes, routes, and replies on the
// bus. The assertions read the recorded event log the same way as before.
func TestSTT_TTRPGIntro_TranscribesBothLanguages(t *testing.T) {
	for _, testCase := range []struct {
		clipName string
		lang     string
		want     string
		response string
	}{
		{
			clipName: "ttrpg-intro-de",
			lang:     "de",
			want: "Hallo zusammen, dann lasst uns doch mal die heutige Session beginnen. " +
				"Okay, Glyphoxa Butler, wiederhol doch einfach einmal bitte was letzte Session so passiert ist, " +
				"was - mach ne kurze Zusammenfassung und - ja wo sind wir am Ende bei raus gekommen?",
			response: "Ja natürlich, letztes mal habt ihr alles umgehauen!",
		},
		{
			clipName: "ttrpg-intro-en",
			lang:     "en",
			want: "Hey everyone, so let's start our session for today. " +
				"Okay, Glyphoxa Butler, can you give us a quick intro what happend last session " +
				"and what did we do, where did we leave the session? What's the current status?",
			response: "Yes of course. Last time you mowed down everything!",
		},
	} {
		t.Run(testCase.clipName, func(t *testing.T) {
			// VAD + audio sample + test harness.
			vadStage, h, frames := voicetest.NewVADRig(t, testCase.clipName)

			// STT transcribes each VAD-segmented utterance.
			recognizer := voicecassette.LoadSTT(t, "stt-"+testCase.clipName)
			sttStage := orchestrator.NewSTT(h.Bus, recognizer)

			// TTS speaks the Butler's reply.
			synthesizer := voicecassette.LoadTTS(t, "tts-"+testCase.clipName)
			ttsStage := orchestrator.NewTTS(h.Bus, synthesizer)
			voice := voicetest.LiveElevenLabsVoice()

			// Address detection: the Butler is the default route; Jamalaka is an
			// active NPC who is never named, so every utterance routes to the Butler.
			detector := orchestrator.NewAddressDetector(
				address.NewWholeWordMatcher(
					voiceevent.AddressTarget{AgentID: "but1", AgentRole: "butler", Name: "Glyphoxa Butler"},
					[]voiceevent.AddressTarget{
						{AgentID: "distraction", AgentRole: "character", Name: "Jamalaka"},
					},
				),
			)

			// Reply strategy: answer the Butler exactly once per turn. The single
			// TTS cassette segment is matched positionally, so a second dispatch
			// would exhaust it — sync.Once pins "one reply".
			var answered sync.Once
			reply := func(_ context.Context, e voiceevent.AddressRouted) []orchestrator.Reply {
				var out []orchestrator.Reply
				if e.Target.AgentRole == "butler" {
					answered.Do(func() {
						out = []orchestrator.Reply{{Sentence: testCase.response, Voice: voice}}
					})
				}
				return out
			}

			// Wire the whole reactive pipeline onto the bus in one call.
			conv := orchestrator.NewConversation(h.Bus, vadStage, sttStage, ttsStage,
				orchestrator.WithDetector(detector),
				orchestrator.WithReply(reply),
				orchestrator.WithErrorHandler(func(err error) { t.Errorf("reply dispatch: %v", err) }),
			)
			t.Cleanup(conv.Register(t.Context()))

			for i, frame := range frames {
				if err := conv.Feed(frame); err != nil {
					t.Fatalf("frame %d: conv.Feed: %v", i, err)
				}
			}
			// Drain the off-loop transcription workers (#24) so every STTFinal — and
			// the turn it fans out — has landed before the assertions read the log.
			if err := conv.Flush(); err != nil {
				t.Fatalf("conv.Flush: %v", err)
			}

			// VAD is the most basic thing that should have been noticed.
			voicetest.AssertEventOccurred[voiceevent.VADSpeechStart](t, h)
			voicetest.AssertEventOccurred[voiceevent.VADSpeechEnd](t, h)

			// STT should have been triggered at least once and the transcripts should match.
			voicetest.AssertEventOccurred[voiceevent.STTFinal](t, h)
			want := voicetest.NormalizeTranscript(testCase.want)
			actual := voicetest.NormalizeTranscript(joinTranscripts(h))
			if !voicetest.WordsMatch(want, actual, 0.7) {
				t.Errorf("stt transcript diverged beyond tolerance (<70%% word overlap).\nwant: %q\ngot: %q", want, actual)
			}

			// Every routing decision went to the Butler — no NPC is named in the clip.
			for _, e := range h.Events() {
				if routed, ok := e.(voiceevent.AddressRouted); ok && routed.Target.AgentRole != "butler" {
					t.Errorf("address detection routed to %q, want butler", routed.Target.AgentRole)
				}
			}
			voicetest.AssertEvent(t, h,
				func(e voiceevent.AddressRouted) bool {
					return e.Target.AgentRole == "butler" && e.Target.Name == "Glyphoxa Butler"
				},
				"address.routed exists for Glyphoxa Butler",
			)

			// TTS should have been dispatched with the reply.
			voicetest.AssertEvent(t, h,
				func(e voiceevent.TTSInvoked) bool {
					return e.Sentence == testCase.response
				},
				"tts.invoked should have TTS input",
			)
		})
	}
}

// TestTurnID_PropagatesThroughStages pins the A3 correlation spine: a single
// turn driven through STT → address → reply → TTS produces an STTFinal,
// AddressRouted and TTSInvoked that all carry the SAME non-empty TurnID, so the
// metrics subscriber can join the turn's stage spans.
func TestTurnID_PropagatesThroughStages(t *testing.T) {
	h := voicetest.New(t)

	sttStage := orchestrator.NewSTT(h.Bus, stubRecognizer{transcript: stt.Transcript{Text: "roll a d20"}})
	ttsStage := orchestrator.NewTTS(h.Bus, closedChanSynth{})
	voice := tts.Voice{ProviderID: "elevenlabs", VoiceID: "george"}

	// Lone-NPC route: every utterance routes to bart (the scoring matcher's
	// single-NPC fallback catches an unnamed utterance).
	detector := orchestrator.NewAddressDetector(
		address.NewMatcher(address.Config{Language: "en"},
			address.Agent{Target: voiceevent.AddressTarget{AgentID: "bart", AgentRole: "character", Name: "Bart"}}),
	)
	reply := func(_ context.Context, _ voiceevent.AddressRouted) []orchestrator.Reply {
		return []orchestrator.Reply{{Sentence: "Aye.", Voice: voice}}
	}

	// Bind the reply chain (detector → replier → TTS) directly; driving STT via
	// the stage is enough to mint the TurnID and fan the turn out across the bus.
	t.Cleanup(orchestrator.Bind(t.Context(), h.Bus,
		detector,
		orchestrator.NewReplier(ttsStage, reply, func(err error) { t.Errorf("dispatch: %v", err) }),
	))

	if err := sttStage.Transcribe(context.Background(), nil); err != nil {
		t.Fatalf("Transcribe: %v", err)
	}

	var sttID, routedID, ttsID string
	for _, e := range h.Events() {
		switch ev := e.(type) {
		case voiceevent.STTFinal:
			sttID = ev.TurnID
		case voiceevent.AddressRouted:
			routedID = ev.TurnID
		case voiceevent.TTSInvoked:
			ttsID = ev.TurnID
		}
	}
	if sttID == "" {
		t.Fatal("STTFinal carried no TurnID")
	}
	if routedID != sttID {
		t.Errorf("AddressRouted.TurnID = %q, want STTFinal's %q", routedID, sttID)
	}
	if ttsID != sttID {
		t.Errorf("TTSInvoked.TurnID = %q, want STTFinal's %q", ttsID, sttID)
	}
}

// TestTurnID_PropagatesThroughBargeInFloor is the production-path twin of
// TestTurnID_PropagatesThroughStages: with WithBargeIn(0) the turn runs on the
// floor's per-turn context (a goroutine), not synchronously. The TurnID is
// installed before Floor.Take, so it must survive the floor's WithCancel-derived
// context and reach TTSInvoked — the only configuration that actually ships.
func TestTurnID_PropagatesThroughBargeInFloor(t *testing.T) {
	h := voicetest.New(t)

	sttStage := orchestrator.NewSTT(h.Bus, stubRecognizer{transcript: stt.Transcript{Text: "hi bart"}})
	ttsStage := orchestrator.NewTTS(h.Bus, closedChanSynth{})
	voice := tts.Voice{ProviderID: "elevenlabs", VoiceID: "george"}

	detector := orchestrator.NewAddressDetector(
		address.NewMatcher(address.Config{Language: "en"},
			address.Agent{Target: voiceevent.AddressTarget{AgentID: "bart", AgentRole: "character", Name: "Bart"}}),
	)
	reply := func(_ context.Context, _ voiceevent.AddressRouted) []orchestrator.Reply {
		return []orchestrator.Reply{{Sentence: "Aye.", Voice: voice}}
	}

	// VAD stage is unused here (we drive STT directly), but the Conversation
	// requires one; the scripted VAD with no script reports only silence.
	vadStage := orchestrator.NewVAD(h.Bus, &scriptedVAD{})
	conv := orchestrator.NewConversation(h.Bus, vadStage, sttStage, ttsStage,
		orchestrator.WithDetector(detector),
		orchestrator.WithReply(reply),
		orchestrator.WithBargeIn(0), // production wiring: turn runs on the floor goroutine
		orchestrator.WithErrorHandler(func(err error) { t.Errorf("dispatch: %v", err) }),
	)
	t.Cleanup(conv.Register(t.Context()))

	if err := sttStage.Transcribe(context.Background(), nil); err != nil {
		t.Fatalf("Transcribe: %v", err)
	}

	// The turn dispatches on a goroutine; wait for the TTSInvoked it produces.
	var sttID, ttsID string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		sttID, ttsID = "", ""
		for _, e := range h.Events() {
			switch ev := e.(type) {
			case voiceevent.STTFinal:
				sttID = ev.TurnID
			case voiceevent.TTSInvoked:
				ttsID = ev.TurnID
			}
		}
		if ttsID != "" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if sttID == "" {
		t.Fatal("STTFinal carried no TurnID")
	}
	if ttsID == "" {
		t.Fatal("no TTSInvoked observed (turn never dispatched through the floor)")
	}
	if ttsID != sttID {
		t.Errorf("TTSInvoked.TurnID = %q, want STTFinal's %q — the floor's context dropped the turn id", ttsID, sttID)
	}
}

// TestSTT_CarriesSpeechEndAt pins that an utterance segmented by the Segmenter
// carries the turn's VADSpeechEnd time onto the published STTFinal (A3), so the
// headline response-latency span is anchored at the true end-of-speech — and a
// non-empty TurnID is minted. Driven through the scripted-VAD segmenter rig so
// the speech-end transition fires through the real stage.
func TestSTT_CarriesSpeechEndAt(t *testing.T) {
	bus := voiceevent.NewBus()
	var speechEnd time.Time
	voiceevent.On(bus, func(e voiceevent.VADSpeechEnd) { speechEnd = e.At })
	var final voiceevent.STTFinal
	var sawFinal bool
	voiceevent.On(bus, func(e voiceevent.STTFinal) { final, sawFinal = e, true })

	sttStage := orchestrator.NewSTT(bus, stubRecognizer{transcript: stt.Transcript{Text: "hi"}})
	// Script: start, continue (buffered), end → the frame after end flushes.
	vadStage := orchestrator.NewVAD(bus, &scriptedVAD{events: []vad.VADEventType{
		vad.VADSpeechStart, vad.VADSpeechContinue, vad.VADSpeechEnd,
	}})
	seg := orchestrator.NewSegmenter(vadStage, sttStage)
	t.Cleanup(seg.Bind(t.Context(), bus))

	feed(t, seg, 4) // start, continue, end, (flush dispatched to a worker)
	// Transcription runs off-loop (#24); Flush drains it so the STTFinal is observable.
	if err := seg.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	if speechEnd.IsZero() {
		t.Fatal("no VADSpeechEnd was published")
	}
	if !sawFinal {
		t.Fatal("no STTFinal was published")
	}
	if !final.SpeechEndAt.Equal(speechEnd) {
		t.Errorf("STTFinal.SpeechEndAt = %v, want the VADSpeechEnd time %v", final.SpeechEndAt, speechEnd)
	}
	if final.TurnID == "" {
		t.Error("STTFinal carried no TurnID")
	}
}

// joinTranscripts concatenates the text of every STTFinal observed by h, in
// arrival order, into a single transcript for whole-utterance assertions.
func joinTranscripts(h *voicetest.Harness) string {
	var b strings.Builder
	for _, e := range h.Events() {
		final, ok := e.(voiceevent.STTFinal)
		if !ok {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(final.Text)
	}
	return b.String()
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
