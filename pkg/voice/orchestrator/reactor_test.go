package orchestrator_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/stt"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/vad"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voicetest"
)

// scriptedVAD is a [vad.SessionHandle] that returns a predetermined sequence of
// event types, one per ProcessFrame call, so a test can drive the segmenter's
// speech-active state deterministically without a real detector. Frames past
// the script report silence.
type scriptedVAD struct {
	events []vad.VADEventType
	i      int
}

func (s *scriptedVAD) ProcessFrame(audio.Frame) (vad.VADEvent, error) {
	typ := vad.VADSilence
	if s.i < len(s.events) {
		typ = s.events[s.i]
		s.i++
	}
	return vad.VADEvent{Type: typ}, nil
}

func (s *scriptedVAD) Reset()       {}
func (s *scriptedVAD) Close() error { return nil }

// recordingRecognizer captures the frame batch of every Transcribe call so a
// test can assert which frames a flush handed to STT. It optionally returns err.
type recordingRecognizer struct {
	err   error
	calls [][]audio.Frame
}

func (r *recordingRecognizer) Transcribe(_ context.Context, frames []audio.Frame) (stt.Transcript, error) {
	r.calls = append(r.calls, append([]audio.Frame(nil), frames...))
	return stt.Transcript{Text: "ok"}, r.err
}

// segFrame returns a 32 ms / 16 kHz frame (512 samples), the framing the rest
// of the orchestrator tests use.
func segFrame(t *testing.T) audio.Frame {
	t.Helper()
	f, err := audio.NewFrame(make([]int16, 512), 16000, 32)
	if err != nil {
		t.Fatalf("audio.NewFrame: %v", err)
	}
	return f
}

// newSegmenterRig wires a segmenter over the scripted VAD and recording
// recognizer onto a fresh bus, bound for the test's lifetime.
func newSegmenterRig(t *testing.T, script ...vad.VADEventType) (*orchestrator.Segmenter, *recordingRecognizer) {
	t.Helper()
	bus := voiceevent.NewBus()
	rec := &recordingRecognizer{}
	vadStage := orchestrator.NewVAD(bus, &scriptedVAD{events: script})
	sttStage := orchestrator.NewSTT(bus, rec)
	seg := orchestrator.NewSegmenter(vadStage, sttStage)
	t.Cleanup(seg.Bind(t.Context(), bus))
	return seg, rec
}

func feed(t *testing.T, seg *orchestrator.Segmenter, n int) {
	t.Helper()
	for i := range n {
		if err := seg.Process(segFrame(t)); err != nil {
			t.Fatalf("frame %d: Process: %v", i, err)
		}
	}
}

// TestSegmenter_FlushTranscribesTrailingUtterance is the regression test for the
// dropped-final-turn bug: the audio loop stops while speech is still active (no
// speech-end transition ever fires), so Process never flushes. Without an
// explicit Flush the buffered utterance is lost; with it, the buffered frames
// reach STT.
func TestSegmenter_FlushTranscribesTrailingUtterance(t *testing.T) {
	// Speech starts and continues, but the stream ends before any speech-end.
	seg, rec := newSegmenterRig(t, vad.VADSpeechStart, vad.VADSpeechContinue, vad.VADSpeechContinue)
	feed(t, seg, 3)

	if len(rec.calls) != 0 {
		t.Fatalf("before Flush: %d transcribe calls, want 0 (speech still active)", len(rec.calls))
	}

	if err := seg.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if len(rec.calls) != 1 {
		t.Fatalf("after Flush: %d transcribe calls, want 1", len(rec.calls))
	}
	if got := len(rec.calls[0]); got != 3 {
		t.Errorf("flushed segment had %d frames, want 3 (all buffered speech)", got)
	}
}

// TestSegmenter_FlushIsNoOpWhenEmpty pins that Flush with nothing buffered — no
// audio fed, or already flushed by a speech-end — does not invoke STT, so a
// defensive end-of-stream Flush after a clean turn is harmless.
func TestSegmenter_FlushIsNoOpWhenEmpty(t *testing.T) {
	seg, rec := newSegmenterRig(t)
	if err := seg.Flush(); err != nil {
		t.Fatalf("Flush on empty: %v", err)
	}
	if len(rec.calls) != 0 {
		t.Errorf("empty Flush made %d transcribe calls, want 0", len(rec.calls))
	}
}

// TestSegmenter_ProcessFlushesOnSpeechEnd pins the normal path: the frame that
// ends speech triggers the flush and is itself excluded from the utterance, and
// a redundant Flush afterwards is a no-op (the buffer was already drained).
func TestSegmenter_ProcessFlushesOnSpeechEnd(t *testing.T) {
	seg, rec := newSegmenterRig(t, vad.VADSpeechStart, vad.VADSpeechContinue, vad.VADSpeechEnd)
	feed(t, seg, 3)

	if len(rec.calls) != 1 {
		t.Fatalf("%d transcribe calls, want 1 (flush on speech-end)", len(rec.calls))
	}
	if got := len(rec.calls[0]); got != 2 {
		t.Errorf("utterance had %d frames, want 2 (the speech-end frame is excluded)", got)
	}

	if err := seg.Flush(); err != nil {
		t.Fatalf("redundant Flush: %v", err)
	}
	if len(rec.calls) != 1 {
		t.Errorf("redundant Flush re-transcribed: %d calls, want 1", len(rec.calls))
	}
}

// TestSegmenter_BufferClearedAfterFlushError pins the "a failed utterance does
// not bleed into the next" contract: when STT errors on flush, the buffer is
// still cleared, so the following utterance contains only its own frames.
func TestSegmenter_BufferClearedAfterFlushError(t *testing.T) {
	seg, rec := newSegmenterRig(t,
		vad.VADSpeechStart, vad.VADSpeechEnd, // first utterance: 1 frame, then end
		vad.VADSpeechStart, vad.VADSpeechEnd, // second utterance: 1 frame, then end
	)
	rec.err = errors.New("boom")

	// First utterance: frame 0 buffered, frame 1 ends speech and flushes → error.
	if err := seg.Process(segFrame(t)); err != nil {
		t.Fatalf("frame 0: %v", err)
	}
	if err := seg.Process(segFrame(t)); err == nil {
		t.Fatal("frame 1: expected the recognizer error to propagate")
	}

	// Second utterance must not carry the first's frame.
	if err := seg.Process(segFrame(t)); err != nil {
		t.Fatalf("frame 2: %v", err)
	}
	if err := seg.Process(segFrame(t)); err == nil {
		t.Fatal("frame 3: expected the recognizer error to propagate")
	}

	if len(rec.calls) != 2 {
		t.Fatalf("%d transcribe calls, want 2", len(rec.calls))
	}
	if got := len(rec.calls[1]); got != 1 {
		t.Errorf("second utterance had %d frames, want 1 (first utterance must not bleed in)", got)
	}
}

// recordReactor is a [orchestrator.Reactor] whose teardown appends its name to
// a shared log, so a test can observe Bind's teardown ordering.
type recordReactor struct {
	name string
	log  *[]string
}

func (r recordReactor) Bind(context.Context, *voiceevent.Bus) func() {
	return func() { *r.log = append(*r.log, r.name) }
}

// TestBind_TearsDownInReverseOrder pins the documented contract that Bind's
// returned cancel tears reactors down in reverse registration order.
func TestBind_TearsDownInReverseOrder(t *testing.T) {
	bus := voiceevent.NewBus()
	var log []string
	cancel := orchestrator.Bind(t.Context(), bus,
		recordReactor{name: "a", log: &log},
		recordReactor{name: "b", log: &log},
		recordReactor{name: "c", log: &log},
	)
	cancel()

	want := []string{"c", "b", "a"}
	if len(log) != len(want) {
		t.Fatalf("teardown order = %v, want %v", log, want)
	}
	for i := range want {
		if log[i] != want[i] {
			t.Fatalf("teardown order = %v, want %v (reverse of registration)", log, want)
		}
	}
}

// TestReplier_BindCancelUnsubscribes proves the reactor stops reacting after
// its returned cancel runs: an AddressRouted published post-teardown drives no
// further TTS dispatch.
func TestReplier_BindCancelUnsubscribes(t *testing.T) {
	h := voicetest.New(t)
	ttsStage := orchestrator.NewTTS(h.Bus, selectiveSynth{})
	reply := func(voiceevent.AddressRouted) []orchestrator.Reply {
		return []orchestrator.Reply{{Sentence: "hi"}}
	}
	cancel := orchestrator.NewReplier(ttsStage, reply, nil).Bind(t.Context(), h.Bus)

	h.Bus.Publish(voiceevent.AddressRouted{})
	cancel()
	h.Bus.Publish(voiceevent.AddressRouted{})

	voicetest.AssertEventCount[voiceevent.TTSInvoked](t, h, 1)
}

// selectiveSynth is a [tts.Synthesizer] that fails Synthesize for sentences in
// failOn and otherwise returns an already-closed audio channel.
type selectiveSynth struct {
	failOn map[string]bool
}

func (s selectiveSynth) Synthesize(_ context.Context, req tts.SynthesizeRequest) (<-chan tts.AudioChunk, error) {
	if s.failOn[req.Sentence] {
		return nil, errors.New("synth failed")
	}
	ch := make(chan tts.AudioChunk)
	close(ch)
	return ch, nil
}

func (selectiveSynth) AudioMarkupPrompt(tts.Voice) string { return "" }

// TestReplier_DispatchErrorReportedAndDoesNotStopRemaining pins the documented
// error contract: a failing Dispatch is surfaced via the ErrorFunc and the
// remaining replies in the same turn are still dispatched.
func TestReplier_DispatchErrorReportedAndDoesNotStopRemaining(t *testing.T) {
	h := voicetest.New(t)
	ttsStage := orchestrator.NewTTS(h.Bus, selectiveSynth{failOn: map[string]bool{"boom": true}})
	reply := func(voiceevent.AddressRouted) []orchestrator.Reply {
		return []orchestrator.Reply{{Sentence: "boom"}, {Sentence: "ok"}}
	}
	var errs []error
	replier := orchestrator.NewReplier(ttsStage, reply, func(e error) { errs = append(errs, e) })
	t.Cleanup(replier.Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.AddressRouted{})

	if len(errs) != 1 {
		t.Fatalf("ErrorFunc saw %d errors, want 1", len(errs))
	}
	// The first reply failed (no event); the second still dispatched.
	voicetest.AssertEventCount[voiceevent.TTSInvoked](t, h, 1)
	voicetest.AssertEvent(t, h,
		func(e voiceevent.TTSInvoked) bool { return e.Sentence == "ok" },
		"tts.invoked for the reply after the failed one",
	)
}

// TestReplier_NilErrorFuncDropsErrorWithoutPanic pins that a nil ErrorFunc is
// tolerated: a dispatch failure is dropped silently and later replies proceed.
func TestReplier_NilErrorFuncDropsErrorWithoutPanic(t *testing.T) {
	h := voicetest.New(t)
	ttsStage := orchestrator.NewTTS(h.Bus, selectiveSynth{failOn: map[string]bool{"boom": true}})
	reply := func(voiceevent.AddressRouted) []orchestrator.Reply {
		return []orchestrator.Reply{{Sentence: "boom"}, {Sentence: "ok"}}
	}
	t.Cleanup(orchestrator.NewReplier(ttsStage, reply, nil).Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.AddressRouted{}) // must not panic on the dropped error

	voicetest.AssertEventCount[voiceevent.TTSInvoked](t, h, 1)
}

// TestSegmenter_ConcurrentFeedAndFlush is a -race probe: an audio loop feeding
// frames while a separate goroutine flushes must not race on the shared buffer.
func TestSegmenter_ConcurrentFeedAndFlush(t *testing.T) {
	seg, _ := newSegmenterRig(t, vad.VADSpeechStart, vad.VADSpeechContinue, vad.VADSpeechContinue)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); feed(t, seg, 3) }()
	go func() { defer wg.Done(); _ = seg.Flush() }()
	wg.Wait()
}
