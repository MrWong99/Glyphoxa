package orchestrator

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// These are the #626 pre-synthesis pipeline tests: sentence k+1 is synthesized
// (and HELD in the pump's look-ahead lane, ADR-0025) while sentence k plays, so
// the inter-sentence gap collapses from one cold TTS TTFB to pacing overhead —
// without breaking ADR-0012's deliver-then-commit or ADR-0027's barge contract.

// laneSpy records the look-ahead lane operations the pipeline coordinator issues.
type laneSpy struct {
	mu     sync.Mutex
	events *eventLog // shared ordering log, so lane ops interleave with synthesis
	ops    []string
}

func (s *laneSpy) ReleaseLookahead(key string) { s.record("release:" + key) }
func (s *laneSpy) DiscardLookahead(key string) { s.record("discard:" + key) }

func (s *laneSpy) record(op string) {
	s.mu.Lock()
	s.ops = append(s.ops, op)
	s.mu.Unlock()
	if s.events != nil {
		s.events.add(op)
	}
}

func (s *laneSpy) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.ops...)
}

// scriptedSynth is a synthFunc whose per-sentence completion the test drives: each
// sentence blocks in "synthesis" until its gate is closed, which is what lets a
// test observe sentence k+1 synthesizing WHILE sentence k is still in flight.
type scriptedSynth struct {
	mu      sync.Mutex
	events  *eventLog
	gates   map[string]chan struct{}
	started map[string]chan struct{}
	errs    map[string]error
}

func newScriptedSynth(log *eventLog, sentences ...string) *scriptedSynth {
	s := &scriptedSynth{
		events:  log,
		gates:   map[string]chan struct{}{},
		started: map[string]chan struct{}{},
		errs:    map[string]error{},
	}
	for _, sentence := range sentences {
		s.gates[sentence] = make(chan struct{})
		s.started[sentence] = make(chan struct{})
	}
	return s
}

func (s *scriptedSynth) fail(sentence string, err error) { s.errs[sentence] = err }

func (s *scriptedSynth) dispatch(ctx context.Context, sentence string, _ tts.Voice) error {
	s.events.add("synth-start:" + sentence)
	if voiceevent.IsPlaybackLookahead(ctx) {
		s.events.add("held:" + voiceevent.LookaheadKeyFrom(ctx))
	}
	s.mu.Lock()
	started, gate, err := s.started[sentence], s.gates[sentence], s.errs[sentence]
	s.mu.Unlock()
	close(started)
	if err != nil {
		return err
	}
	select {
	case <-gate:
	case <-ctx.Done():
		return ctx.Err()
	}
	s.events.add("synth-done:" + sentence)
	return ctx.Err()
}

// release lets one sentence's synthesis complete.
func (s *scriptedSynth) release(sentence string) { close(s.gates[sentence]) }

// awaitStart blocks until a sentence's synthesis has been entered.
func (s *scriptedSynth) awaitStart(t *testing.T, sentence string) {
	t.Helper()
	select {
	case <-s.started[sentence]:
	case <-time.After(2 * time.Second):
		t.Fatalf("synthesis of %q never started", sentence)
	}
}

// eventLog is the ordered, concurrency-safe record the pipeline tests assert on.
type eventLog struct {
	mu  sync.Mutex
	got []string
}

func (l *eventLog) add(ev string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.got = append(l.got, ev)
}

func (l *eventLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.got...)
}

// indexOf reports the position of ev in the log, or -1.
func indexOf(log []string, ev string) int {
	for i, e := range log {
		if e == ev {
			return i
		}
	}
	return -1
}

// mustOrder fails unless every named event is present, in the given order.
func mustOrder(t *testing.T, log []string, evs ...string) {
	t.Helper()
	prev := -1
	for _, ev := range evs {
		i := indexOf(log, ev)
		if i < 0 {
			t.Fatalf("event %q missing from log %v", ev, log)
		}
		if i < prev {
			t.Fatalf("event %q out of order in log %v (want %v)", ev, log, evs)
		}
		prev = i
	}
}

// newTestPipeline builds a pipelined turn over a scripted synthesizer and a spy
// lane, mirroring the production wiring ([Replier.newPipelinedTurn]).
func newTestPipeline(ctx context.Context, log *eventLog, synth *scriptedSynth, lane *laneSpy) *pipelinedTurn {
	return newPipelined(newTurnRun(ctx, synth.dispatch, nil), "T", lane,
		func(turnID, sentence string) { log.add("invoked:" + sentence) })
}

// TestPipelinedTurn_SynthesizesNextWhileCurrentPlays is the #626 headline: the
// second sentence is handed to TTS while the first is still being delivered — its
// TTFB burns during playback — and it is HELD in the look-ahead lane until the
// first resolves, at which point it is announced (TTSInvoked at release, ADR-0040)
// and released to play.
func TestPipelinedTurn_SynthesizesNextWhileCurrentPlays(t *testing.T) {
	log := &eventLog{}
	synth := newScriptedSynth(log, "one.", "two.")
	lane := &laneSpy{events: log}
	p := newTestPipeline(context.Background(), log, synth, lane)

	if err := p.dispatch(Reply{Sentence: "one.", OnDelivered: func() { log.add("committed:one.") }}); err != nil {
		t.Fatalf("dispatch(s1) = %v, want nil (keep producing)", err)
	}
	synth.awaitStart(t, "one.")

	// s2 dispatches while s1 is still in flight: its synthesis starts at once and
	// the call blocks until s1 resolves.
	returned := make(chan error, 1)
	go func() {
		returned <- p.dispatch(Reply{Sentence: "two.", OnDelivered: func() { log.add("committed:two.") }})
	}()
	synth.awaitStart(t, "two.")
	if got := log.snapshot(); indexOf(got, "synth-done:one.") >= 0 {
		t.Fatalf("s2 synthesis must begin BEFORE s1 completes; log %v", got)
	}
	select {
	case err := <-returned:
		t.Fatalf("dispatch(s2) returned (%v) before s1 resolved", err)
	case <-time.After(50 * time.Millisecond):
	}

	// s1 completes: s2 is announced, released to the queue, and dispatch returns.
	synth.release("one.")
	select {
	case err := <-returned:
		if err != nil {
			t.Fatalf("dispatch(s2) = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("dispatch(s2) never returned after s1 resolved")
	}
	synth.release("two.")
	if err := p.flush(); err != nil {
		t.Fatalf("flush = %v, want nil", err)
	}

	got := log.snapshot()
	mustOrder(t, got,
		"synth-start:one.", "held:T#2", "synth-done:one.", "committed:one.",
		"invoked:two.", "release:T#2", "synth-done:two.", "committed:two.")
	if ops := lane.snapshot(); len(ops) == 0 || ops[0] != "release:T#2" {
		t.Fatalf("lane ops = %v, want the held s2 released first", ops)
	}
}

// TestPipelinedTurn_FlushAwaitsTail pins the last sentence (#626): it has no
// successor to release it — it was released by its OWN dispatch call — so flush
// only has to await its resolution, and its keyed discard is a harmless no-op that
// leaves no dangling lane behind.
func TestPipelinedTurn_FlushAwaitsTail(t *testing.T) {
	log := &eventLog{}
	synth := newScriptedSynth(log, "one.", "two.")
	lane := &laneSpy{events: log}
	p := newTestPipeline(context.Background(), log, synth, lane)

	_ = p.dispatch(Reply{Sentence: "one."})
	synth.awaitStart(t, "one.")
	done := make(chan error, 1)
	go func() { done <- p.dispatch(Reply{Sentence: "two."}) }()
	synth.awaitStart(t, "two.")
	synth.release("one.")
	if err := <-done; err != nil {
		t.Fatalf("dispatch(s2) = %v, want nil", err)
	}

	flushed := make(chan error, 1)
	go func() { flushed <- p.flush() }()
	select {
	case err := <-flushed:
		t.Fatalf("flush returned (%v) before the tail sentence resolved", err)
	case <-time.After(50 * time.Millisecond):
	}
	synth.release("two.")
	select {
	case err := <-flushed:
		if err != nil {
			t.Fatalf("flush = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("flush never returned after the tail resolved")
	}
	want := []string{"release:T#2", "discard:T#2"}
	if got := lane.snapshot(); len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("lane ops = %v, want %v (the flush discard is a keyed no-op)", got, want)
	}
}

// TestPipelinedTurn_BargeDiscardsHeldSentence pins ADR-0027 for the pipeline: a
// barge while s1 is speaking must drop the already-synthesized s2 — discarded from
// the lane, never announced (so no Line persists, ADR-0040), never committed — and
// the producer must be told to stop.
func TestPipelinedTurn_BargeDiscardsHeldSentence(t *testing.T) {
	log := &eventLog{}
	synth := newScriptedSynth(log, "one.", "two.")
	lane := &laneSpy{events: log}
	ctx, cancel := context.WithCancel(context.Background())
	p := newTestPipeline(ctx, log, synth, lane)

	_ = p.dispatch(Reply{Sentence: "one.", OnDelivered: func() { log.add("committed:one.") }})
	synth.awaitStart(t, "one.")
	returned := make(chan error, 1)
	go func() {
		returned <- p.dispatch(Reply{Sentence: "two.", OnDelivered: func() { log.add("committed:two.") }})
	}()
	synth.awaitStart(t, "two.")

	// The human barges mid-s1: the turn ctx dies, s1's drain is cut.
	cancel()
	synth.release("one.")

	var err error
	select {
	case err = <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("dispatch(s2) never returned after the barge")
	}
	if OutcomeOf(err) != SentenceCut {
		t.Fatalf("dispatch(s2) outcome = %v, want SentenceCut (stop producing)", OutcomeOf(err))
	}
	if err := p.flush(); OutcomeOf(err) != SentenceCut {
		t.Fatalf("flush outcome = %v, want SentenceCut", OutcomeOf(err))
	}

	got := log.snapshot()
	if indexOf(got, "invoked:two.") >= 0 {
		t.Fatalf("a discarded look-ahead sentence must never be announced; log %v", got)
	}
	for _, ev := range []string{"committed:one.", "committed:two."} {
		if indexOf(got, ev) >= 0 {
			t.Fatalf("%s: a cut sentence must not commit; log %v", ev, got)
		}
	}
	ops := lane.snapshot()
	if indexOf(ops, "discard:T#2") < 0 {
		t.Fatalf("lane ops = %v, want the held s2 discarded", ops)
	}
	if indexOf(ops, "release:T#2") >= 0 {
		t.Fatalf("lane ops = %v, want NO release for the discarded s2", ops)
	}
}

// TestPipelinedTurn_StartErrorClearsStaleLatch pins the release-latch hazard
// (#626): when a held sentence start-errors it is never primed, so the release
// issued for it latches in the pump forever. The next dispatch clears that latch
// with a keyed discard before releasing its own sentence — otherwise the latch
// would let a later sentence bypass the lane and leapfrog the one playing.
func TestPipelinedTurn_StartErrorClearsStaleLatch(t *testing.T) {
	log := &eventLog{}
	synth := newScriptedSynth(log, "one.", "two.", "three.")
	startErr := errors.New("tts start failed")
	synth.fail("two.", startErr)
	lane := &laneSpy{events: log}
	p := newTestPipeline(context.Background(), log, synth, lane)

	_ = p.dispatch(Reply{Sentence: "one."})
	synth.awaitStart(t, "one.")
	s2 := make(chan error, 1)
	go func() { s2 <- p.dispatch(Reply{Sentence: "two."}) }()
	synth.awaitStart(t, "two.")
	synth.release("one.")
	if err := <-s2; err != nil {
		t.Fatalf("dispatch(s2) = %v, want nil (s1 delivered)", err)
	}

	// s3 dispatches; s2 has start-errored, so the producer is told to skip it and
	// keep going, and s2's stale latch is cleared before s3 is released.
	s3 := make(chan error, 1)
	go func() { s3 <- p.dispatch(Reply{Sentence: "three."}) }()
	synth.awaitStart(t, "three.")
	err := <-s3
	if OutcomeOf(err) != SentenceNotDelivered {
		t.Fatalf("dispatch(s3) outcome = %v, want SentenceNotDelivered (s2 start-errored)", OutcomeOf(err))
	}
	synth.release("three.")
	_ = p.flush()

	mustOrder(t, lane.snapshot(), "release:T#2", "discard:T#2", "release:T#3")
	if !p.t.ttsFailed {
		t.Fatal("a start-errored sentence must leave the turn's ttsFailed sticky (tts_error terminal)")
	}
}

// TestPipelinedTurn_CutAlsoClearsPreviousLatch pins the lane-cleanliness rule on
// the barge path: the cut sentence's OWN release may still be latched in the pump
// (release-before-prime, then the barge drains the prime without consuming the
// latch). The coordinator therefore discards BOTH keys — the newly held sentence
// and the cut predecessor — so no release latch outlives the turn that issued it.
func TestPipelinedTurn_CutAlsoClearsPreviousLatch(t *testing.T) {
	log := &eventLog{}
	synth := newScriptedSynth(log, "one.", "two.", "three.")
	lane := &laneSpy{events: log}
	ctx, cancel := context.WithCancel(context.Background())
	p := newTestPipeline(ctx, log, synth, lane)

	// s1 delivers, so s2 is released (its lane key T#2 now owns a release).
	_ = p.dispatch(Reply{Sentence: "one."})
	synth.awaitStart(t, "one.")
	s2 := make(chan error, 1)
	go func() { s2 <- p.dispatch(Reply{Sentence: "two."}) }()
	synth.awaitStart(t, "two.")
	synth.release("one.")
	if err := <-s2; err != nil {
		t.Fatalf("dispatch(s2) = %v, want nil", err)
	}

	// The human barges while s2 is speaking and s3 is pre-rendering.
	s3 := make(chan error, 1)
	go func() { s3 <- p.dispatch(Reply{Sentence: "three."}) }()
	synth.awaitStart(t, "three.")
	cancel()
	synth.release("two.")
	if err := <-s3; OutcomeOf(err) != SentenceCut {
		t.Fatalf("dispatch(s3) outcome = %v, want SentenceCut", OutcomeOf(err))
	}

	ops := lane.snapshot()
	for _, want := range []string{"discard:T#3", "discard:T#2"} {
		if indexOf(ops, want) < 0 {
			t.Fatalf("lane ops = %v, want %q (no release latch may outlive the cut turn)", ops, want)
		}
	}
}
