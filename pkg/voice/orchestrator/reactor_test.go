package orchestrator_test

import (
	"context"
	"errors"
	"sync"
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
// Transcription now runs on the segmenter's worker goroutine (#24), so calls is
// mutex-guarded for -race; read it via [recordingRecognizer.batches] after a
// [orchestrator.Segmenter.Flush] (which drains in-flight transcriptions).
type recordingRecognizer struct {
	err error

	mu    sync.Mutex
	calls [][]audio.Frame
}

func (r *recordingRecognizer) Transcribe(_ context.Context, frames []audio.Frame) (stt.Transcript, error) {
	r.mu.Lock()
	r.calls = append(r.calls, append([]audio.Frame(nil), frames...))
	r.mu.Unlock()
	return stt.Transcript{Text: "ok"}, r.err
}

// batches returns a snapshot of the captured Transcribe frame batches.
func (r *recordingRecognizer) batches() [][]audio.Frame {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]audio.Frame(nil), r.calls...)
}

// blockingRecognizer blocks every Transcribe call on release after announcing it
// started, so a test can hold STT "in flight" and observe whether the audio
// intake keeps draining (the #24 decoupling) or stalls behind it.
type blockingRecognizer struct {
	started chan struct{}
	release chan struct{}
}

func (r *blockingRecognizer) Transcribe(ctx context.Context, _ []audio.Frame) (stt.Transcript, error) {
	r.started <- struct{}{}
	select {
	case <-r.release:
	case <-ctx.Done():
		return stt.Transcript{}, ctx.Err()
	}
	return stt.Transcript{Text: "ok"}, nil
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

// TestSegmenter_ProcessDoesNotBlockOnSTT is the #24 regression: the inbound audio
// loop must keep draining while a slow, network-bound STT call is in flight, so an
// utterance spoken during a previous utterance's transcription is not dropped at
// the bounded inbound buffer. With STT decoupled from intake, Process hands the
// segment to the transcription worker and returns; the old inline-STT coupling
// stalled the feeding goroutine inside the recognizer call until it returned.
func TestSegmenter_ProcessDoesNotBlockOnSTT(t *testing.T) {
	bus := voiceevent.NewBus()
	rec := &blockingRecognizer{started: make(chan struct{}, 4), release: make(chan struct{})}
	vadStage := orchestrator.NewVAD(bus, &scriptedVAD{events: []vad.VADEventType{
		vad.VADSpeechStart, vad.VADSpeechEnd, // utterance 1 → flush (transcription blocks the worker)
	}})
	sttStage := orchestrator.NewSTT(bus, rec)
	seg := orchestrator.NewSegmenter(vadStage, sttStage)
	t.Cleanup(seg.Bind(t.Context(), bus))

	frame := segFrame(t)
	fedAll := make(chan struct{})
	go func() {
		// Frame 0 starts speech, frame 1 ends it (flushing utterance 1 to STT, which
		// blocks the worker), frames 2-3 are post-script silence. All four Process
		// calls must return even though the recognizer is still blocked.
		for range 4 {
			_ = seg.Process(frame)
		}
		close(fedAll)
	}()

	// Utterance 1's transcription is in flight (the worker is blocked in Transcribe).
	select {
	case <-rec.started:
	case <-time.After(2 * time.Second):
		t.Fatal("transcription never started")
	}

	// The audio loop must keep draining while STT is blocked — Process hands the
	// segment to the worker and returns rather than running the recognizer inline.
	select {
	case <-fedAll:
		// intake kept draining: correct.
	case <-time.After(2 * time.Second):
		t.Fatal("Segmenter.Process blocked while STT was in flight — the #24 intake coupling")
	}
	close(rec.release) // let the worker unwind so Bind's cancel can stop it
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

	if got := rec.batches(); len(got) != 0 {
		t.Fatalf("before Flush: %d transcribe calls, want 0 (speech still active)", len(got))
	}

	if err := seg.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	batches := rec.batches()
	if len(batches) != 1 {
		t.Fatalf("after Flush: %d transcribe calls, want 1", len(batches))
	}
	if got := len(batches[0]); got != 3 {
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
	if got := rec.batches(); len(got) != 0 {
		t.Errorf("empty Flush made %d transcribe calls, want 0", len(got))
	}
}

// TestSegmenter_ProcessFlushesOnSpeechEnd pins the normal path: the frame that
// ends speech triggers the flush and is itself excluded from the utterance, and
// a redundant Flush afterwards is a no-op (the buffer was already drained).
func TestSegmenter_ProcessFlushesOnSpeechEnd(t *testing.T) {
	seg, rec := newSegmenterRig(t, vad.VADSpeechStart, vad.VADSpeechContinue, vad.VADSpeechEnd)
	feed(t, seg, 3)

	// The speech-end flush transcribes on a worker goroutine (#24); Flush drains it
	// (and is a no-op on the already-emptied buffer) so the call is observable.
	if err := seg.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	batches := rec.batches()
	if len(batches) != 1 {
		t.Fatalf("%d transcribe calls, want 1 (flush on speech-end)", len(batches))
	}
	if got := len(batches[0]); got != 2 {
		t.Errorf("utterance had %d frames, want 2 (the speech-end frame is excluded)", got)
	}

	if err := seg.Flush(); err != nil {
		t.Fatalf("redundant Flush: %v", err)
	}
	if got := rec.batches(); len(got) != 1 {
		t.Errorf("redundant Flush re-transcribed: %d calls, want 1", len(got))
	}
}

// TestSegmenter_BufferClearedAfterFlushError pins the "a failed utterance does
// not bleed into the next" contract under the off-loop STT (#24): when the
// recognizer errors, the error surfaces via onError (not Process's return, which
// has no caller to take it now), and the buffer is still cleared so the following
// utterance contains only its own frames.
func TestSegmenter_BufferClearedAfterFlushError(t *testing.T) {
	seg, rec := newSegmenterRig(t,
		vad.VADSpeechStart, vad.VADSpeechEnd, // first utterance: 1 frame, then end
		vad.VADSpeechStart, vad.VADSpeechEnd, // second utterance: 1 frame, then end
	)
	rec.err = errors.New("boom")
	var mu sync.Mutex
	var errs []error
	seg.SetErrorHandler(func(err error) { mu.Lock(); errs = append(errs, err); mu.Unlock() })

	// Two utterances, each one frame then a speech-end that flushes to the failing
	// recognizer. Process returns nil now (the STT call is off-loop); Flush drains
	// both workers so their errors and frame batches are observable.
	feed(t, seg, 4)
	if err := seg.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	batches := rec.batches()
	if len(batches) != 2 {
		t.Fatalf("%d transcribe calls, want 2", len(batches))
	}
	if got := len(batches[1]); got != 1 {
		t.Errorf("second utterance had %d frames, want 1 (first utterance must not bleed in)", got)
	}
	mu.Lock()
	gotErrs := len(errs)
	mu.Unlock()
	if gotErrs != 2 {
		t.Errorf("onError saw %d recognizer errors, want 2 (both utterances failed)", gotErrs)
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
	reply := func(context.Context, voiceevent.AddressRouted) []orchestrator.Reply {
		return []orchestrator.Reply{{Sentence: "hi"}}
	}
	cancel := orchestrator.NewReplier(ttsStage, reply, nil).Bind(t.Context(), h.Bus)

	h.Bus.Publish(voiceevent.AddressRouted{})
	cancel()
	h.Bus.Publish(voiceevent.AddressRouted{})

	voicetest.AssertEventCount[voiceevent.TTSInvoked](t, h, 1)
}

// TestReplier_CoalescingFloorKeepsFirstSegmentTurn is the end-to-end proof of
// root cause #2's fix: with a coalescing floor, two AddressRouted decisions from
// one VAD-over-split utterance (two segments arriving back-to-back) must NOT
// cancel each other. The first segment's turn keeps running; the second is
// coalesced — its producer never runs (the fragment is not spoken) and a
// TurnYielded carrying its dropped transcript is published for the metrics
// subscriber. Without the coalesce window the second Floor.Take would cancel the
// first turn's ctx — the self-cancel that produced no audio and no metric sample.
func TestReplier_CoalescingFloorKeepsFirstSegmentTurn(t *testing.T) {
	h := voicetest.New(t)
	ttsStage := orchestrator.NewTTS(h.Bus, selectiveSynth{})

	// A streaming reply that records which TurnIDs actually ran a producer, and
	// blocks seg1 until its ctx is cancelled (which must NOT happen due to seg2).
	var mu sync.Mutex
	var ran []string
	started := make(chan string, 4)
	firstCancelled := make(chan struct{}, 1)
	reply := func(ctx context.Context, e voiceevent.AddressRouted, _ func(orchestrator.Reply) error) error {
		mu.Lock()
		ran = append(ran, e.TurnID)
		mu.Unlock()
		started <- e.TurnID
		if e.TurnID == "seg1" {
			select {
			case <-ctx.Done():
				close(firstCancelled)
			case <-time.After(500 * time.Millisecond):
			}
		}
		return nil
	}

	floor := orchestrator.NewFloorWithCoalesce(300 * time.Millisecond)
	replier := orchestrator.NewStreamReplier(ttsStage, reply, nil)
	replier.SetFloor(floor)
	t.Cleanup(replier.Bind(t.Context(), h.Bus))

	// Two segments of one utterance, back-to-back (well inside the window), both
	// routed to the SAME agent — coalescing is same-target only (#146).
	bart := voiceevent.AddressTarget{AgentID: "bart", AgentRole: "character", Name: "Bart"}
	h.Bus.Publish(voiceevent.AddressRouted{TurnID: "seg1", Text: "Bart, what's a room", Target: bart})
	// Wait for seg1's producer to actually start and take the floor before seg2.
	if got := <-started; got != "seg1" {
		t.Fatalf("first producer started for %q, want seg1", got)
	}
	h.Bus.Publish(voiceevent.AddressRouted{TurnID: "seg2", Text: "and have you seen Gandalf", Target: bart})

	// seg1 must run to its natural end without being cancelled by seg2.
	select {
	case <-firstCancelled:
		t.Fatal("the first segment's turn was cancelled by the second — the self-cancel the coalesce window must prevent")
	case <-time.After(200 * time.Millisecond):
		// seg1 still running uninterrupted: correct.
	}

	// seg2 must have been coalesced: its producer never ran (the fragment is not
	// spoken) and a TurnYielded carrying its transcript was published.
	mu.Lock()
	gotRan := append([]string(nil), ran...)
	mu.Unlock()
	for _, id := range gotRan {
		if id == "seg2" {
			t.Fatal("the coalesced segment's producer must not run (the fragment must not be spoken)")
		}
	}
	voicetest.AssertEvent(t, h,
		func(e voiceevent.TurnEnded) bool {
			return e.TurnID == "seg2" && e.Reason == voiceevent.TurnEndSupersedeCoalesced && e.Text == "and have you seen Gandalf"
		},
		"turn.ended (supersede_coalesced) for the coalesced seg2 carrying its dropped transcript",
	)
}

// TestReplier_CrossTargetAddressWithinWindowSupersedes pins #146 end-to-end:
// "Bart, hold the door. Greta, run!" — VAD splits at the internal pause and the
// address detector routes segment 1 → Bart, segment 2 → Greta, with Greta's
// take landing inside the floor's coalesce window. The window's "same utterance
// continuing" inference is provably false across targets, so Greta's
// directly-addressed turn must be DISPATCHED (her producer runs under a live
// ctx, superseding Bart's turn) — not silently dropped as supersede_coalesced,
// which left nobody answering a direct, named address.
func TestReplier_CrossTargetAddressWithinWindowSupersedes(t *testing.T) {
	h := voicetest.New(t)
	ttsStage := orchestrator.NewTTS(h.Bus, selectiveSynth{})

	started := make(chan string, 4)
	bartCancelled := make(chan struct{})
	gretaRan := make(chan struct{})
	reply := func(ctx context.Context, e voiceevent.AddressRouted, _ func(orchestrator.Reply) error) error {
		started <- e.TurnID
		switch e.TurnID {
		case "seg-bart":
			// Bart's turn is mid-generation when Greta's address arrives; a
			// cross-target supersede must cancel it.
			select {
			case <-ctx.Done():
				close(bartCancelled)
			case <-time.After(2 * time.Second):
			}
		case "seg-greta":
			if ctx.Err() == nil {
				close(gretaRan)
			}
		}
		return nil
	}

	floor := orchestrator.NewFloorWithCoalesce(600 * time.Millisecond)
	replier := orchestrator.NewStreamReplier(ttsStage, reply, nil)
	replier.SetFloor(floor)
	t.Cleanup(replier.Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.AddressRouted{
		TurnID: "seg-bart", Text: "Bart, hold the door",
		Target: voiceevent.AddressTarget{AgentID: "bart", AgentRole: "character", Name: "Bart"},
	})
	// Bart's producer must hold the floor before Greta's segment lands.
	if got := <-started; got != "seg-bart" {
		t.Fatalf("first producer started for %q, want seg-bart", got)
	}
	h.Bus.Publish(voiceevent.AddressRouted{
		TurnID: "seg-greta", Text: "Greta, run",
		Target: voiceevent.AddressTarget{AgentID: "greta", AgentRole: "character", Name: "Greta"},
	})

	// Greta's turn is dispatched: the addressed agent's producer sees the route.
	select {
	case <-gretaRan:
	case <-time.After(2 * time.Second):
		t.Fatal("Greta's directly-addressed turn was never dispatched — coalesced away by a target-blind floor (#146)")
	}
	// Bart's turn was superseded, not left speaking over Greta's.
	select {
	case <-bartCancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("Bart's in-flight turn must be cancelled by Greta's cross-target supersede")
	}
	// The cross-target segment must not be announced as a coalesced drop.
	for _, ev := range h.Events() {
		if e, ok := ev.(voiceevent.TurnEnded); ok && e.TurnID == "seg-greta" && e.Reason == voiceevent.TurnEndSupersedeCoalesced {
			t.Fatal("seg-greta was published as supersede_coalesced — a cross-target address must supersede, not coalesce")
		}
	}
}

// waitTurnEnded subscribes a channel to TurnEnded on bus so a test can block until
// the async (floor goroutine) turn publishes its end signal — AssertEvent does not
// poll, so a direct synchronization is needed for the goroutine-driven turns.
func waitTurnEnded(t *testing.T, bus *voiceevent.Bus) <-chan voiceevent.TurnEnded {
	t.Helper()
	ch := make(chan voiceevent.TurnEnded, 4)
	t.Cleanup(voiceevent.On(bus, func(e voiceevent.TurnEnded) { ch <- e }))
	return ch
}

// TestReplier_FloorTurnPublishesTTSErrorEnded pins task #4's tts_error path: a
// turn whose only sentence fails synthesis (no audio, ctx not cancelled) publishes
// TurnEnded(tts_error) carrying the TurnID, so the metrics subscriber attributes
// the death precisely instead of the coarse no-first-audio reap.
func TestReplier_FloorTurnPublishesTTSErrorEnded(t *testing.T) {
	h := voicetest.New(t)
	ttsStage := orchestrator.NewTTS(h.Bus, selectiveSynth{failOn: map[string]bool{"boom": true}})
	reply := func(_ context.Context, _ voiceevent.AddressRouted, dispatch func(orchestrator.Reply) error) error {
		return dispatch(orchestrator.Reply{Sentence: "boom"})
	}
	replier := orchestrator.NewStreamReplier(ttsStage, reply, nil)
	replier.SetFloor(orchestrator.NewFloor())
	ended := waitTurnEnded(t, h.Bus)
	t.Cleanup(replier.Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.AddressRouted{TurnID: "terr"})

	select {
	case e := <-ended:
		if e.TurnID != "terr" || e.Reason != voiceevent.TurnEndTTSError {
			t.Fatalf("TurnEnded = %+v, want {terr tts_error}", e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no TurnEnded(tts_error) published for a failed-synthesis turn")
	}
}

// TestReplier_SyncTurnPublishesTTSErrorEnded pins #20's sync-path gap: a no-floor
// replier (the voicebench rig's config — WithReplyStream, no barge-in) whose only
// sentence start-errors must publish TurnEnded(tts_error) carrying the TurnID, so
// the metrics subscriber records abandoned/tts_error instead of the coarse
// no-first-audio TTL reap. The floor path already does this
// (TestReplier_FloorTurnPublishesTTSErrorEnded); the sync path discarded the
// reason, so a start-error there was completely silent.
func TestReplier_SyncTurnPublishesTTSErrorEnded(t *testing.T) {
	h := voicetest.New(t)
	ttsStage := orchestrator.NewTTS(h.Bus, selectiveSynth{failOn: map[string]bool{"boom": true}})
	reply := func(_ context.Context, _ voiceevent.AddressRouted, dispatch func(orchestrator.Reply) error) error {
		return dispatch(orchestrator.Reply{Sentence: "boom"})
	}
	// No SetFloor: the synchronous (no-barge-in) dispatch path runs on the bus
	// goroutine, so the TurnEnded is published before Publish returns.
	replier := orchestrator.NewStreamReplier(ttsStage, reply, nil)
	t.Cleanup(replier.Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.AddressRouted{TurnID: "tsync"})

	voicetest.AssertEvent(t, h,
		func(e voiceevent.TurnEnded) bool {
			return e.TurnID == "tsync" && e.Reason == voiceevent.TurnEndTTSError
		},
		"turn.ended (tts_error) for a sync-path failed-synthesis turn",
	)
}

// TestReplier_FloorTurnPublishesProviderErrorEnded pins task #4's provider_error
// path: a producer (LLM/tool loop) that errors before any audio publishes
// TurnEnded(provider_error).
func TestReplier_FloorTurnPublishesProviderErrorEnded(t *testing.T) {
	h := voicetest.New(t)
	ttsStage := orchestrator.NewTTS(h.Bus, selectiveSynth{})
	reply := func(_ context.Context, _ voiceevent.AddressRouted, _ func(orchestrator.Reply) error) error {
		return errors.New("gemini round failed")
	}
	replier := orchestrator.NewStreamReplier(ttsStage, reply, nil)
	replier.SetFloor(orchestrator.NewFloor())
	ended := waitTurnEnded(t, h.Bus)
	t.Cleanup(replier.Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.AddressRouted{TurnID: "tperr"})

	select {
	case e := <-ended:
		if e.TurnID != "tperr" || e.Reason != voiceevent.TurnEndProviderError {
			t.Fatalf("TurnEnded = %+v, want {tperr provider_error}", e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no TurnEnded(provider_error) published for a failed-producer turn")
	}
}

// TestReplier_FloorTextDeliveredTurnPublishesTextEnded pins the Butler
// text-modality terminal signal (#299): a producer that delivers its whole answer
// as text (via a TextSink) dispatches NO TTS, so it reaches no first audio. It
// signals completion by returning the sentinel [orchestrator.ErrTextDelivered],
// which the reactor maps to TurnEnded(text_delivered) — a SUCCESS, not a
// provider_error — so the metrics subscriber does not TTL-reap it as abandoned.
// The sentinel is NOT reported through the ErrorFunc.
func TestReplier_FloorTextDeliveredTurnPublishesTextEnded(t *testing.T) {
	h := voicetest.New(t)
	ttsStage := orchestrator.NewTTS(h.Bus, selectiveSynth{})
	var onErrCalled bool
	reply := func(_ context.Context, _ voiceevent.AddressRouted, _ func(orchestrator.Reply) error) error {
		return orchestrator.ErrTextDelivered
	}
	replier := orchestrator.NewStreamReplier(ttsStage, reply, func(error) { onErrCalled = true })
	replier.SetFloor(orchestrator.NewFloor())
	ended := waitTurnEnded(t, h.Bus)
	t.Cleanup(replier.Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.AddressRouted{TurnID: "ttxt"})

	select {
	case e := <-ended:
		if e.TurnID != "ttxt" || e.Reason != voiceevent.TurnEndTextDelivered {
			t.Fatalf("TurnEnded = %+v, want {ttxt text_delivered}", e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no TurnEnded(text_delivered) published for a text-delivered turn")
	}
	if onErrCalled {
		t.Fatal("ErrTextDelivered must not be reported through the ErrorFunc")
	}
}

// fixedEngine is a non-streaming [agent.Engine] returning one canned completion,
// so a Butler [agent.Replier] runs the batch fallback (one whole-answer dispatch).
type fixedEngine struct{ reply string }

func (e fixedEngine) Generate(context.Context, []llm.Message) (string, error) { return e.reply, nil }

// butlerBargeSynth records each rendered sentence and, on its first synthesis,
// yields the shared floor mid-drain — modelling a confirmed human barge cutting
// the turn (ADR-0027). The closed channel cuts the sentence's tail, so it is NOT
// delivered (ADR-0012). It reports whether a turn was actually held when it
// yielded, so a test can prove the Butler turn ran on the shared floor.
type butlerBargeSynth struct {
	floor *orchestrator.Floor
	mu    sync.Mutex
	spoke []string
	fired bool
	held  bool
}

func (s *butlerBargeSynth) Synthesize(_ context.Context, req tts.SynthesizeRequest) (<-chan tts.AudioChunk, error) {
	s.mu.Lock()
	s.spoke = append(s.spoke, req.Sentence)
	first := !s.fired
	s.fired = true
	s.mu.Unlock()
	if first {
		_, yielded := s.floor.Yield()
		s.mu.Lock()
		s.held = yielded
		s.mu.Unlock()
	}
	ch := make(chan tts.AudioChunk)
	close(ch)
	return ch, nil
}

func (*butlerBargeSynth) AudioMarkupPrompt(tts.Voice) string { return "" }

func (s *butlerBargeSynth) rendered() int   { s.mu.Lock(); defer s.mu.Unlock(); return len(s.spoke) }
func (s *butlerBargeSynth) heldFloor() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.held }

// TestReplier_ButlerSpokenTurnRunsOnSharedFloor pins the #299 finding-4c gap: the
// voiced Butler is a first-class roster member, so its SPOKEN turn runs on the
// same single barge-in Floor as every NPC (ADR-0027/ADR-0038 one-turn-at-a-time)
// and is barge-able. Driven through the REAL agent Butler Replier: a confirmed
// barge mid-drain cuts the turn, so the Butler delivers nothing and — per ADR-0012
// (zero delivered sentences are not logged) — commits no history line, and the
// yield proves the Butler turn actually held the shared floor.
func TestReplier_ButlerSpokenTurnRunsOnSharedFloor(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()
	synth := &butlerBargeSynth{floor: floor}
	ttsStage := orchestrator.NewTTS(h.Bus, synth)

	butlerVoice := tts.Voice{ProviderID: "test", VoiceID: "glyphoxa", Name: "Glyphoxa"}
	butler := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "glyphoxa", Markdown: "You are Glyphoxa.", Voice: butlerVoice},
		Engine:      fixedEngine{reply: "At your service, my liege."},
		Synthesizer: synth,
	})
	// Wrap the producer so the test can barrier on its return: every synth write
	// (spoke/held) and the history commit happen inside it, so <-done establishes a
	// happens-before for the assertions below (no poll, race-clean).
	base := butler.ReplyStream()
	done := make(chan struct{})
	wrapped := func(ctx context.Context, e voiceevent.AddressRouted, dispatch func(orchestrator.Reply) error) error {
		defer close(done)
		return base(ctx, e, dispatch)
	}
	streamRep := orchestrator.NewStreamReplier(ttsStage, wrapped, nil)
	streamRep.SetFloor(floor) // wire the shared barge-in floor into the pipeline
	t.Cleanup(streamRep.Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.AddressRouted{
		TurnID: "b1",
		Text:   "Glyphoxa, are you ready?",
		Target: voiceevent.AddressTarget{AgentID: "glyphoxa", AgentRole: voiceevent.AgentRoleButler, Name: "Glyphoxa"},
	})

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Butler turn never completed")
	}
	if synth.rendered() == 0 {
		t.Fatal("Butler turn never reached TTS — it did not run on the floor")
	}
	// The yield cut a HELD turn: the Butler ran on the shared floor.
	if !synth.heldFloor() {
		t.Error("Butler spoken turn did not hold the shared floor (barge yielded nothing)")
	}
	// Barge cut delivery before any sentence's tail forwarded: nothing committed.
	for _, m := range butler.HistorySnapshot() {
		if m.Role == llm.RoleAssistant {
			t.Errorf("barged Butler turn committed an assistant line (ADR-0012 violated): %+v", m)
		}
	}
}

// TestReplier_FloorCleanTurnPublishesNoTurnEnded proves a turn that produces audio
// cleanly publishes no TurnEnded (the success path is silent; first audio is the
// terminal signal). It synchronizes on the producer returning, then drains.
func TestReplier_FloorCleanTurnPublishesNoTurnEnded(t *testing.T) {
	h := voicetest.New(t)
	ttsStage := orchestrator.NewTTS(h.Bus, selectiveSynth{}) // all sentences succeed
	done := make(chan struct{})
	reply := func(_ context.Context, _ voiceevent.AddressRouted, dispatch func(orchestrator.Reply) error) error {
		defer close(done)
		return dispatch(orchestrator.Reply{Sentence: "hello there"})
	}
	replier := orchestrator.NewStreamReplier(ttsStage, reply, nil)
	replier.SetFloor(orchestrator.NewFloor())
	ended := waitTurnEnded(t, h.Bus)
	t.Cleanup(replier.Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.AddressRouted{TurnID: "tok"})

	<-done // producer finished; the goroutine publishes TurnEnded (if any) before exiting
	// The publish happens after dispatchAll returns, just after the producer; give
	// that a beat, then assert nothing arrived.
	select {
	case e := <-ended:
		t.Fatalf("clean turn must publish no TurnEnded, got %+v", e)
	case <-time.After(100 * time.Millisecond):
	}
}

// yieldSynth cancels the active turn (floor.Yield) DURING Synthesize, then hands
// back an already-closed audio channel — modelling a sentence whose tail audio is
// cut mid-drain by a barge/mute that lands after Synthesize was accepted but
// before the last frame is forwarded. The vendor call itself succeeded, so
// TTS.Dispatch returns nil; the sentence was nonetheless NOT delivered.
type yieldSynth struct{ floor *orchestrator.Floor }

func (s yieldSynth) Synthesize(_ context.Context, _ tts.SynthesizeRequest) (<-chan tts.AudioChunk, error) {
	s.floor.Yield() // cancel the turn ctx mid-sentence
	ch := make(chan tts.AudioChunk)
	close(ch)
	return ch, nil
}

func (yieldSynth) AudioMarkupPrompt(tts.Voice) string { return "" }

// TestDispatchStream_TurnCutMidDrain_ReportsUndelivered pins the reactor half of
// deliver-then-commit (ADR-0012): when a turn is cancelled mid-sentence-drain, the
// dispatch callback handed to the streaming producer must report a non-nil error,
// so the producer does NOT commit that sentence (its tail was never forwarded).
// A nil Dispatch return is not enough — the turn ctx may have gone cancelled
// during the drain, which the dispatch closure must re-check.
func TestDispatchStream_TurnCutMidDrain_ReportsUndelivered(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()
	ttsStage := orchestrator.NewTTS(h.Bus, yieldSynth{floor: floor})

	dispErr := make(chan error, 1)
	reply := func(_ context.Context, _ voiceevent.AddressRouted, dispatch func(orchestrator.Reply) error) error {
		err := dispatch(orchestrator.Reply{Sentence: "First."})
		dispErr <- err
		return err
	}
	replier := orchestrator.NewStreamReplier(ttsStage, reply, nil)
	replier.SetFloor(floor)
	t.Cleanup(replier.Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.AddressRouted{
		TurnID: "tcut",
		Target: voiceevent.AddressTarget{AgentID: "bart", AgentRole: "character", Name: "Bart"},
	})

	select {
	case err := <-dispErr:
		if err == nil {
			t.Fatal("dispatch returned nil for a sentence whose tail audio was cut mid-drain — the producer would commit an undelivered sentence (ADR-0012)")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the streaming producer never ran")
	}
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
	reply := func(context.Context, voiceevent.AddressRouted) []orchestrator.Reply {
		return []orchestrator.Reply{{Sentence: "boom"}, {Sentence: "ok"}}
	}
	var errs []error
	replier := orchestrator.NewReplier(ttsStage, reply, func(e error) { errs = append(errs, e) })
	t.Cleanup(replier.Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.AddressRouted{})

	if len(errs) != 1 {
		t.Fatalf("ErrorFunc saw %d errors, want 1", len(errs))
	}
	// Both sentences are announced: TTSInvoked is the dispatch attempt (#20), so
	// the start-errored sentence is visible as invoked-but-never-spoke AND the
	// reply after it still dispatched.
	voicetest.AssertEventCount[voiceevent.TTSInvoked](t, h, 2)
	voicetest.AssertEvent(t, h,
		func(e voiceevent.TTSInvoked) bool { return e.Sentence == "boom" },
		"tts.invoked for the start-errored sentence (invoked-but-never-spoke)",
	)
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
	reply := func(context.Context, voiceevent.AddressRouted) []orchestrator.Reply {
		return []orchestrator.Reply{{Sentence: "boom"}, {Sentence: "ok"}}
	}
	t.Cleanup(orchestrator.NewReplier(ttsStage, reply, nil).Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.AddressRouted{}) // must not panic on the dropped error

	// Both sentences are announced (TTSInvoked = dispatch attempt, #20); the
	// failed one's error is dropped silently and the next reply still dispatched.
	voicetest.AssertEventCount[voiceevent.TTSInvoked](t, h, 2)
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
