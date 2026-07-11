package orchestrator_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voicetest"
)

// fakeLookahead is the coordinator-side seam for the #375 tests: it records the
// Release/Discard op order and models the pump lane's HOLD by handing laneSynth a
// per-turn release channel — so a look-ahead sentence's synthesis blocks until the
// coordinator releases it (or the turn's ctx is cancelled = discard/barge), exactly
// as the real pump lane back-pressures the lockstep tee.
type fakeLookahead struct {
	mu        sync.Mutex
	ops       []string // "release:<id>" / "discard:<id>" in call order
	rel       map[string]chan struct{}
	closed    map[string]bool
	onRelease func(id string) // invoked (inside Release) for ordering probes

	discardOnce sync.Once
	discardCh   chan struct{} // closed on the FIRST DiscardLookahead — the uniform-defer barrier
}

func newFakeLookahead() *fakeLookahead {
	return &fakeLookahead{rel: map[string]chan struct{}{}, closed: map[string]bool{}, discardCh: make(chan struct{})}
}

// waitDiscard blocks until the coordinator's uniform defer has run its keyed
// DiscardLookahead (fires exactly once on ALL exit paths — barge/gate-fail/decline/
// happy/abort), a happens-before edge for asserting final ops/events/history without
// racing the defer.
func (f *fakeLookahead) waitDiscard(t *testing.T) {
	t.Helper()
	select {
	case <-f.discardCh:
	case <-time.After(2 * time.Second):
		t.Fatal("DiscardLookahead (the uniform defer) never ran")
	}
}

// relCh returns (creating) the per-turn release channel laneSynth waits on.
func (f *fakeLookahead) relCh(id string) chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch, ok := f.rel[id]
	if !ok {
		ch = make(chan struct{})
		f.rel[id] = ch
	}
	return ch
}

func (f *fakeLookahead) ReleaseLookahead(id string) {
	f.mu.Lock()
	f.ops = append(f.ops, "release:"+id)
	ch, ok := f.rel[id]
	if !ok {
		ch = make(chan struct{})
		f.rel[id] = ch
	}
	if !f.closed[id] {
		f.closed[id] = true
		close(ch)
	}
	f.mu.Unlock()
	if f.onRelease != nil {
		f.onRelease(id)
	}
}

func (f *fakeLookahead) DiscardLookahead(id string) {
	f.mu.Lock()
	f.ops = append(f.ops, "discard:"+id)
	f.mu.Unlock()
	f.discardOnce.Do(func() { close(f.discardCh) })
}

func (f *fakeLookahead) opsSnapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.ops...)
}

// synthCall records one Synthesize invocation's observable ctx state.
type synthCall struct {
	sentence  string
	lookahead bool
	turnID    string
}

// laneSynth is a [tts.Synthesizer] that models the pump lane for the coordinator
// tests: it records every call's look-ahead marker + turn id, HOLDS a look-ahead
// sentence's audio channel open until the pump releases that turn (or ctx is
// cancelled), and can hold a named non-look-ahead sentence (the Lead) or start-error
// a named one (a reaction start-error). Without the hold a reaction would "commit"
// the instant it dispatched, defeating the gate/discard the seam exists to enforce.
type laneSynth struct {
	pump     *fakeLookahead
	holdSet  map[string]bool // non-look-ahead sentences held open until holdGate
	holdGate chan struct{}
	failOn   map[string]bool // sentences whose Synthesize start-errors

	mu    sync.Mutex
	calls []synthCall
}

func (s *laneSynth) Synthesize(ctx context.Context, req tts.SynthesizeRequest) (<-chan tts.AudioChunk, error) {
	lookahead := voiceevent.IsPlaybackLookahead(ctx)
	id := voiceevent.TurnIDFrom(ctx)
	s.mu.Lock()
	s.calls = append(s.calls, synthCall{sentence: req.Sentence, lookahead: lookahead, turnID: id})
	s.mu.Unlock()

	if s.failOn[req.Sentence] {
		return nil, errors.New("synth start failed")
	}
	ch := make(chan tts.AudioChunk)
	if lookahead {
		// Held in the lane: the channel closes (the sentence "plays") only once the
		// pump releases this turn, or ctx is cancelled (discard/barge).
		rel := s.pump.relCh(id)
		go func() {
			select {
			case <-rel:
			case <-ctx.Done():
			}
			close(ch)
		}()
		return ch, nil
	}
	if s.holdSet[req.Sentence] {
		go func() {
			select {
			case <-s.holdGate:
			case <-ctx.Done():
			}
			close(ch)
		}()
		return ch, nil
	}
	close(ch)
	return ch, nil
}

func (s *laneSynth) AudioMarkupPrompt(tts.Voice) string { return "" }

func (s *laneSynth) findCall(pred func(synthCall) bool) (synthCall, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.calls {
		if pred(c) {
			return c, true
		}
	}
	return synthCall{}, false
}

// lookaheadReplier wires a floor-backed ensemble Replier with the #375 look-ahead
// pump and a laneSynth, mirroring [ensembleReplier].
func lookaheadReplier(h *voicetest.Harness, floor *orchestrator.Floor, spk orchestrator.EnsembleSpeaker, pump orchestrator.LookaheadPump, synth tts.Synthesizer) *orchestrator.Replier {
	ttsStage := orchestrator.NewTTS(h.Bus, synth)
	replier := orchestrator.NewStreamReplier(ttsStage, func(context.Context, voiceevent.AddressRouted, func(orchestrator.Reply) error) error {
		return nil
	}, nil)
	replier.SetFloor(floor)
	replier.SetEnsemble(spk)
	replier.SetLookahead(pump)
	return replier
}

// waitUntil polls pred until true or the deadline, without a fixed sleep as the
// correctness signal (ADR-0021: observe, don't time).
func waitUntil(t *testing.T, why string, pred func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !pred() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", why)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestReplier_Lookahead_ReactionPrerendersDuringLead pins #375 AC3 (structural): with
// a look-ahead pump wired, the Reaction's FIRST sentence is synthesized under a
// PlaybackLookahead ctx carrying a FRESH sub-turn id — WHILE the Lead is still mid-
// dispatch (its chunk channel held open, Speak blocked). No wall-clock: the Lead's
// held synthesis is the synchronization.
func TestReplier_Lookahead_ReactionPrerendersDuringLead(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()
	pump := newFakeLookahead()
	synth := &laneSynth{pump: pump, holdSet: map[string]bool{"Bart leads.": true}, holdGate: make(chan struct{})}
	spk := &fakeCrossTalk{
		fakeEnsemble: &fakeEnsemble{
			draft:          map[string]string{bartTarget.AgentID: "Bart leads."},
			gate:           map[string]chan struct{}{bartTarget.AgentID: closedGate(), goblinTarget.AgentID: make(chan struct{})},
			speakSentences: map[string][]string{bartTarget.AgentID: {"Bart leads."}},
			spokeCh:        make(chan string, 2),
		},
		react:        map[string]string{goblinTarget.AgentID: "React one."},
		reactStarted: make(chan string, 2),
	}
	replier := lookaheadReplier(h, floor, spk, pump, synth)
	t.Cleanup(replier.Bind(t.Context(), h.Bus))
	t.Cleanup(orchestrator.NewBargeIn(floor, 0).Bind(t.Context(), h.Bus))
	defer close(synth.holdGate) // release the held Lead at the end

	h.Bus.Publish(voiceevent.EnsembleRouted{TurnID: "Te", Text: "Bart, Goblin?", Targets: []voiceevent.AddressTarget{bartTarget, goblinTarget}})

	// The Reaction's first sentence is synthesized under a look-ahead ctx with a fresh
	// sub-turn id — while the Lead is still held (Speak has NOT returned).
	var rc synthCall
	waitUntil(t, "reaction pre-rendered under a look-ahead ctx", func() bool {
		c, ok := synth.findCall(func(c synthCall) bool { return c.sentence == "React one." && c.lookahead })
		if ok {
			rc = c
		}
		return ok
	})
	select {
	case who := <-spk.spokeCh:
		t.Fatalf("the Lead's Speak (%q) returned before the Reaction pre-rendered — not during playback", who)
	default:
	}
	if rc.turnID == "" || rc.turnID == "Te" {
		t.Fatalf("reaction look-ahead turnID = %q, want a FRESH sub-turn id distinct from the Lead's Te", rc.turnID)
	}
}

// TestReplier_Lookahead_HappyReleaseAfterLead pins #375 AC1/AC3 happy path: the Lead
// speaks, then EnsembleReaction is published and the held first sentence RELEASED —
// the event strictly precedes the release — the Reaction commits its text, and no
// spurious TurnEnded fires.
func TestReplier_Lookahead_HappyReleaseAfterLead(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()
	pump := newFakeLookahead()
	synth := &laneSynth{pump: pump, holdGate: make(chan struct{})}

	// Record whether EnsembleReaction AND the reaction's first TTSInvoked were already
	// published when Release fires (F5: atomic, read from another goroutine).
	var releasedAfterEvent, releasedAfterInvoke atomic.Bool
	pump.onRelease = func(id string) {
		var sawEvent, sawInvoke bool
		for _, e := range h.Events() {
			if er, ok := e.(voiceevent.EnsembleReaction); ok && er.TurnID == id {
				sawEvent = true
			}
			if ti, ok := e.(voiceevent.TTSInvoked); ok && ti.TurnID == id {
				sawInvoke = true
			}
		}
		releasedAfterEvent.Store(sawEvent)
		releasedAfterInvoke.Store(sawInvoke)
	}

	spk := &fakeCrossTalk{
		fakeEnsemble: &fakeEnsemble{
			draft:          map[string]string{bartTarget.AgentID: "Bart leads."},
			gate:           map[string]chan struct{}{bartTarget.AgentID: closedGate(), goblinTarget.AgentID: make(chan struct{})},
			speakSentences: map[string][]string{bartTarget.AgentID: {"Bart leads."}},
			speakPause:     make(chan struct{}),
			spokeCh:        make(chan string, 2),
		},
		react:        map[string]string{goblinTarget.AgentID: "I disagree, loudly."},
		reactSpokeCh: make(chan string, 2),
	}
	replier := lookaheadReplier(h, floor, spk, pump, synth)
	t.Cleanup(replier.Bind(t.Context(), h.Bus))
	t.Cleanup(orchestrator.NewBargeIn(floor, 0).Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.EnsembleRouted{TurnID: "Te", Text: "Bart, Goblin?", Targets: []voiceevent.AddressTarget{bartTarget, goblinTarget}})

	// The Lead is audible: arm the floor, then release the paused Lead so its Speak
	// returns and the coordinator releases the held Reaction.
	voicetest.WaitEvent(t, h, 2*time.Second, func(e voiceevent.TTSInvoked) bool {
		return e.TurnID == "Te" && e.Sentence == "Bart leads."
	}, "the Lead's sentence is on the wire")
	h.Bus.Publish(voiceevent.FirstOpus{TurnID: "Te"})
	close(spk.speakPause)

	var rID string
	voicetest.WaitEvent(t, h, 2*time.Second, func(e voiceevent.EnsembleReaction) bool {
		if e.LeadTurnID == "Te" && e.Target.AgentID == goblinTarget.AgentID {
			rID = e.TurnID
			return true
		}
		return false
	}, "ensemble.reaction for the reactor linked to the Lead")

	// The reactor's SpeakReaction ran and committed its text.
	select {
	case <-spk.reactSpokeCh:
	case <-time.After(2 * time.Second):
		t.Fatal("the released Reaction never played")
	}
	if got := spk.reactDeliveredFor(goblinTarget.AgentID); got != "I disagree, loudly." {
		t.Fatalf("reaction committed %q, want the delivered reaction text", got)
	}
	pump.waitDiscard(t) // uniform defer done: ops/events settled

	// The held first sentence was released, and BOTH EnsembleReaction and its first
	// TTSInvoked preceded the release (F1 relay ordering).
	ops := pump.opsSnapshot()
	if len(ops) == 0 || ops[0] != "release:"+rID {
		t.Fatalf("pump ops = %v, want a release of %q first", ops, rID)
	}
	if !releasedAfterEvent.Load() {
		t.Fatal("EnsembleReaction was not published before ReleaseLookahead (relay ordering broken)")
	}
	if !releasedAfterInvoke.Load() {
		t.Fatal("the reaction's first TTSInvoked was not published before ReleaseLookahead (attribution ordering broken)")
	}
	// Order: EnsembleReaction{rID} strictly precedes TTSInvoked{rID,s1} in the log.
	assertOrder(t, h, rID)
	// No barge: no TurnEnded for the reaction sub-turn.
	voicetest.AssertNoEvent[voiceevent.TurnEnded](t, h)
}

// assertOrder asserts EnsembleReaction{rID} precedes the first TTSInvoked{rID} in the
// harness event log (the relay-critical attribution order, #375 F1).
func assertOrder(t *testing.T, h *voicetest.Harness, rID string) {
	t.Helper()
	reactionAt, invokeAt := -1, -1
	for i, e := range h.Events() {
		if er, ok := e.(voiceevent.EnsembleReaction); ok && er.TurnID == rID && reactionAt < 0 {
			reactionAt = i
		}
		if ti, ok := e.(voiceevent.TTSInvoked); ok && ti.TurnID == rID && invokeAt < 0 {
			invokeAt = i
		}
	}
	if reactionAt < 0 || invokeAt < 0 {
		t.Fatalf("missing events: EnsembleReaction@%d TTSInvoked@%d for rID=%s", reactionAt, invokeAt, rID)
	}
	if reactionAt > invokeAt {
		t.Fatalf("EnsembleReaction@%d must precede TTSInvoked{rID}@%d (relay would misattribute)", reactionAt, invokeAt)
	}
}

// TestReplier_Lookahead_BargeDuringLeadDiscards pins #375 for ADR-0027: a barge while
// the Lead speaks (Reaction still generating) discards the held sentence — no
// EnsembleReaction, no release, nothing committed, and no TurnEnded under a reaction id.
func TestReplier_Lookahead_BargeDuringLeadDiscards(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()
	pump := newFakeLookahead()
	synth := &laneSynth{pump: pump, holdGate: make(chan struct{})}
	spk := &fakeCrossTalk{
		fakeEnsemble: &fakeEnsemble{
			draft:          map[string]string{bartTarget.AgentID: "First. Second."},
			gate:           map[string]chan struct{}{bartTarget.AgentID: closedGate(), goblinTarget.AgentID: make(chan struct{})},
			speakSentences: map[string][]string{bartTarget.AgentID: {"First.", "Second."}},
			speakPause:     make(chan struct{}),
			spokeCh:        make(chan string, 2),
		},
		react:          map[string]string{goblinTarget.AgentID: "I would react."},
		reactGate:      map[string]chan struct{}{goblinTarget.AgentID: make(chan struct{})}, // never releases: cut by barge
		reactStarted:   make(chan string, 2),
		reactCancelled: make(chan string, 2),
	}
	replier := lookaheadReplier(h, floor, spk, pump, synth)
	t.Cleanup(replier.Bind(t.Context(), h.Bus))
	t.Cleanup(orchestrator.NewBargeIn(floor, 0).Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.EnsembleRouted{TurnID: "Tb", Text: "Bart, Goblin?", Targets: []voiceevent.AddressTarget{bartTarget, goblinTarget}})

	voicetest.WaitEvent(t, h, 2*time.Second, func(e voiceevent.TTSInvoked) bool {
		return e.TurnID == "Tb" && e.Sentence == "First."
	}, "the Lead's first sentence is on the wire")
	<-spk.reactStarted

	// Barge while the Lead is audible.
	h.Bus.Publish(voiceevent.FirstOpus{TurnID: "Tb"})
	h.Bus.Publish(voiceevent.VADSpeechStart{})

	voicetest.WaitEvent(t, h, 2*time.Second, func(e voiceevent.TurnEnded) bool {
		return e.TurnID == "Tb" && e.Reason == voiceevent.TurnEndBarge
	}, "turn.ended barge for the ensemble")
	<-spk.reactCancelled

	waitFloorFree(t, floor)
	voicetest.AssertNoEvent[voiceevent.EnsembleReaction](t, h)
	if got := spk.reactDeliveredFor(goblinTarget.AgentID); got != "" {
		t.Fatalf("a discarded reaction committed %q, want nothing", got)
	}
	pump.waitDiscard(t) // uniform defer done
	// Exactly one discard, no release; and no reaction-id TurnEnded (only the Lead's Tb).
	ops := pump.opsSnapshot()
	if len(ops) != 1 || ops[0][:8] != "discard:" {
		t.Fatalf("pump ops = %v, want exactly one discard and no release", ops)
	}
}

// TestReplier_Lookahead_GateFailDiscards pins #375 for the audible-delivery gate: a
// Lead that never armed the floor (no FirstOpus, turnCtx still live) discards the
// held Reaction and commits nothing — even though a reactor stood ready.
func TestReplier_Lookahead_GateFailDiscards(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()
	pump := newFakeLookahead()
	synth := &laneSynth{pump: pump, holdGate: make(chan struct{})}
	spk := &fakeCrossTalk{
		fakeEnsemble: &fakeEnsemble{
			draft:          map[string]string{bartTarget.AgentID: "Bart leads."},
			gate:           map[string]chan struct{}{bartTarget.AgentID: closedGate(), goblinTarget.AgentID: make(chan struct{})},
			speakSentences: map[string][]string{bartTarget.AgentID: {"Bart leads."}},
			spokeCh:        make(chan string, 2),
		},
		react:        map[string]string{goblinTarget.AgentID: "I react anyway."},
		reactSpokeCh: make(chan string, 2),
	}
	replier := lookaheadReplier(h, floor, spk, pump, synth)
	t.Cleanup(replier.Bind(t.Context(), h.Bus))
	t.Cleanup(orchestrator.NewBargeIn(floor, 0).Bind(t.Context(), h.Bus))

	// No FirstOpus is ever published, so floor.Speaking() stays false: the gate skips
	// (discards) the Reaction under a still-live turn.
	h.Bus.Publish(voiceevent.EnsembleRouted{TurnID: "Tg", Text: "Bart, Goblin?", Targets: []voiceevent.AddressTarget{bartTarget, goblinTarget}})

	<-spk.spokeCh // the Lead's Speak returned
	voicetest.WaitEvent(t, h, 2*time.Second, func(e voiceevent.EnsembleLead) bool { return e.TurnID == "Tg" }, "the Lead was elected")
	waitFloorFree(t, floor)

	pump.waitDiscard(t) // uniform defer done
	voicetest.AssertNoEvent[voiceevent.EnsembleReaction](t, h)
	if got := spk.reactDeliveredFor(goblinTarget.AgentID); got != "" {
		t.Fatalf("a gate-skipped reaction committed %q, want nothing", got)
	}
	ops := pump.opsSnapshot()
	for _, op := range ops {
		if len(op) >= 8 && op[:8] == "release:" {
			t.Fatalf("pump ops = %v, want no release on a gate-fail", ops)
		}
	}
}

// TestReplier_Lookahead_DeclineNoRelease pins #375 decline: a reactor that returns ""
// publishes nothing, releases nothing, deadlocks nothing — and the uniform defer
// discard still runs.
func TestReplier_Lookahead_DeclineNoRelease(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()
	pump := newFakeLookahead()
	synth := &laneSynth{pump: pump, holdGate: make(chan struct{})}
	spk := &fakeCrossTalk{
		fakeEnsemble: &fakeEnsemble{
			draft:          map[string]string{bartTarget.AgentID: "Bart leads."},
			gate:           map[string]chan struct{}{bartTarget.AgentID: closedGate(), goblinTarget.AgentID: make(chan struct{})},
			speakSentences: map[string][]string{bartTarget.AgentID: {"Bart leads."}},
			speakPause:     make(chan struct{}),
			spokeCh:        make(chan string, 2),
		},
		react:        map[string]string{ /* goblin: no entry → "" → decline */ },
		reactSpokeCh: make(chan string, 2),
	}
	replier := lookaheadReplier(h, floor, spk, pump, synth)
	t.Cleanup(replier.Bind(t.Context(), h.Bus))
	t.Cleanup(orchestrator.NewBargeIn(floor, 0).Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.EnsembleRouted{TurnID: "Td", Text: "Bart, Goblin?", Targets: []voiceevent.AddressTarget{bartTarget, goblinTarget}})

	voicetest.WaitEvent(t, h, 2*time.Second, func(e voiceevent.TTSInvoked) bool {
		return e.TurnID == "Td" && e.Sentence == "Bart leads."
	}, "the Lead's sentence is on the wire")
	h.Bus.Publish(voiceevent.FirstOpus{TurnID: "Td"})
	close(spk.speakPause)
	<-spk.spokeCh

	waitFloorFree(t, floor) // no deadlock: the decline path runs to completion
	pump.waitDiscard(t)     // uniform defer done
	voicetest.AssertNoEvent[voiceevent.EnsembleReaction](t, h)
	voicetest.AssertEventCount[voiceevent.TTSInvoked](t, h, 1) // only the Lead's sentence
	ops := pump.opsSnapshot()
	for _, op := range ops {
		if len(op) >= 8 && op[:8] == "release:" {
			t.Fatalf("pump ops = %v, want no release on a decline", ops)
		}
	}
}

// TestReplier_Lookahead_FirstDispatchStartErrorAborts pins the #362 reconciliation
// (plan test 12): a start-error on the look-ahead first sentence ABORTS the Reaction
// (converted to a non-sentinel error so the second sentence cannot leapfrog the Lead),
// commits nothing, and does NOT wedge runEnsemble.
func TestReplier_Lookahead_FirstDispatchStartErrorAborts(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()
	pump := newFakeLookahead()
	// The reaction's FIRST sentence start-errors; its would-be second must never speak.
	synth := &laneSynth{pump: pump, holdGate: make(chan struct{}), failOn: map[string]bool{"React one.": true}}
	spk := &fakeCrossTalk{
		fakeEnsemble: &fakeEnsemble{
			draft:          map[string]string{bartTarget.AgentID: "Bart leads."},
			gate:           map[string]chan struct{}{bartTarget.AgentID: closedGate(), goblinTarget.AgentID: make(chan struct{})},
			speakSentences: map[string][]string{bartTarget.AgentID: {"Bart leads."}},
			speakPause:     make(chan struct{}),
			spokeCh:        make(chan string, 2),
		},
		react:          map[string]string{goblinTarget.AgentID: "React one. React two."},
		reactSentences: map[string][]string{goblinTarget.AgentID: {"React one.", "React two."}},
		reactSpokeCh:   make(chan string, 2),
	}
	replier := lookaheadReplier(h, floor, spk, pump, synth)
	t.Cleanup(replier.Bind(t.Context(), h.Bus))
	t.Cleanup(orchestrator.NewBargeIn(floor, 0).Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.EnsembleRouted{TurnID: "Ts", Text: "Bart, Goblin?", Targets: []voiceevent.AddressTarget{bartTarget, goblinTarget}})

	voicetest.WaitEvent(t, h, 2*time.Second, func(e voiceevent.TTSInvoked) bool {
		return e.TurnID == "Ts" && e.Sentence == "Bart leads."
	}, "the Lead's sentence is on the wire")
	h.Bus.Publish(voiceevent.FirstOpus{TurnID: "Ts"})
	close(spk.speakPause)

	// runEnsemble is not wedged: the floor is freed and the reaction spoke nothing.
	select {
	case <-spk.reactSpokeCh:
	case <-time.After(2 * time.Second):
		t.Fatal("SpeakReaction never returned after the first-sentence start-error (wedged)")
	}
	waitFloorFree(t, floor)
	pump.waitDiscard(t) // uniform defer done
	if got := spk.reactDeliveredFor(goblinTarget.AgentID); got != "" {
		t.Fatalf("an aborted reaction committed %q, want nothing", got)
	}
	// The second reaction sentence must never have been dispatched (no leapfrog).
	if _, ok := synth.findCall(func(c synthCall) bool { return c.sentence == "React two." }); ok {
		t.Fatal("the second reaction sentence dispatched after the first start-errored (leapfrogged the Lead)")
	}
}

// TestReplier_Lookahead_FirstStartErrorEndsTurn pins #391 for the look-ahead path:
// when the held first (and only) reaction sentence fails to START synthesis, the
// released reaction sub-turn — already announced via EnsembleReaction — is ended with
// TurnEnded{rID, tts_error} so it is never TTL-reaped as abandoned. Nothing commits.
func TestReplier_Lookahead_FirstStartErrorEndsTurn(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()
	pump := newFakeLookahead()
	// The reaction's ONLY (held) sentence start-errors → nothing delivered.
	synth := &laneSynth{pump: pump, holdGate: make(chan struct{}), failOn: map[string]bool{"React one.": true}}
	spk := &fakeCrossTalk{
		fakeEnsemble: &fakeEnsemble{
			draft:          map[string]string{bartTarget.AgentID: "Bart leads."},
			gate:           map[string]chan struct{}{bartTarget.AgentID: closedGate(), goblinTarget.AgentID: make(chan struct{})},
			speakSentences: map[string][]string{bartTarget.AgentID: {"Bart leads."}},
			speakPause:     make(chan struct{}),
			spokeCh:        make(chan string, 2),
		},
		react:        map[string]string{goblinTarget.AgentID: "React one."},
		reactSpokeCh: make(chan string, 2),
	}
	replier := lookaheadReplier(h, floor, spk, pump, synth)
	t.Cleanup(replier.Bind(t.Context(), h.Bus))
	t.Cleanup(orchestrator.NewBargeIn(floor, 0).Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.EnsembleRouted{TurnID: "Tp", Text: "Bart, Goblin?", Targets: []voiceevent.AddressTarget{bartTarget, goblinTarget}})

	voicetest.WaitEvent(t, h, 2*time.Second, func(e voiceevent.TTSInvoked) bool {
		return e.TurnID == "Tp" && e.Sentence == "Bart leads."
	}, "the Lead's sentence is on the wire")
	h.Bus.Publish(voiceevent.FirstOpus{TurnID: "Tp"})
	close(spk.speakPause)

	var rID string
	voicetest.WaitEvent(t, h, 2*time.Second, func(e voiceevent.EnsembleReaction) bool {
		rID = e.TurnID
		return e.LeadTurnID == "Tp"
	}, "ensemble.reaction announced")
	voicetest.WaitEvent(t, h, 2*time.Second, func(e voiceevent.TurnEnded) bool {
		return e.TurnID == rID && e.Reason == voiceevent.TurnEndTTSError
	}, "turn.ended tts_error for the released reaction (no TTL reap)")

	select {
	case <-spk.reactSpokeCh:
	case <-time.After(2 * time.Second):
		t.Fatal("SpeakReaction never returned after the first-sentence start-error")
	}
	waitFloorFree(t, floor)
	pump.waitDiscard(t)
	// No barge fired: the reaction's tts_error is the only turn-end.
	voicetest.AssertEventCount[voiceevent.TurnEnded](t, h, 1)
	if got := spk.reactDeliveredFor(goblinTarget.AgentID); got != "" {
		t.Fatalf("an aborted reaction committed %q, want nothing", got)
	}
}

// TestReplier_Lookahead_BargeDuringReactionEndsSubTurn pins #375 legacy parity: a
// barge WHILE the released Reaction plays ends the reaction sub-turn with a barge
// TurnEnded under its OWN id.
func TestReplier_Lookahead_BargeDuringReactionEndsSubTurn(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()
	pump := newFakeLookahead()
	synth := &laneSynth{pump: pump, holdGate: make(chan struct{})}
	spk := &fakeCrossTalk{
		fakeEnsemble: &fakeEnsemble{
			draft:          map[string]string{bartTarget.AgentID: "Bart done."},
			gate:           map[string]chan struct{}{bartTarget.AgentID: closedGate(), goblinTarget.AgentID: make(chan struct{})},
			speakSentences: map[string][]string{bartTarget.AgentID: {"Bart done."}},
			speakPause:     make(chan struct{}),
			spokeCh:        make(chan string, 2),
		},
		react:          map[string]string{goblinTarget.AgentID: "React one. React two."},
		reactSentences: map[string][]string{goblinTarget.AgentID: {"React one.", "React two."}},
		reactPause:     make(chan struct{}), // pause after reaction sentence 1 until the barge
		reactSpokeCh:   make(chan string, 2),
	}
	replier := lookaheadReplier(h, floor, spk, pump, synth)
	t.Cleanup(replier.Bind(t.Context(), h.Bus))
	t.Cleanup(orchestrator.NewBargeIn(floor, 0).Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.EnsembleRouted{TurnID: "Tr", Text: "Bart, Goblin?", Targets: []voiceevent.AddressTarget{bartTarget, goblinTarget}})

	voicetest.WaitEvent(t, h, 2*time.Second, func(e voiceevent.TTSInvoked) bool {
		return e.TurnID == "Tr" && e.Sentence == "Bart done."
	}, "the Lead's sentence is on the wire")
	h.Bus.Publish(voiceevent.FirstOpus{TurnID: "Tr"})
	close(spk.speakPause)

	var rID string
	voicetest.WaitEvent(t, h, 2*time.Second, func(e voiceevent.EnsembleReaction) bool {
		rID = e.TurnID
		return e.LeadTurnID == "Tr"
	}, "ensemble.reaction published")
	voicetest.WaitEvent(t, h, 2*time.Second, func(e voiceevent.TTSInvoked) bool {
		return e.TurnID == rID && e.Sentence == "React one."
	}, "the released Reaction's first sentence is on the wire")

	// Barge DURING the Reaction's playback.
	h.Bus.Publish(voiceevent.VADSpeechStart{})

	voicetest.WaitEvent(t, h, 2*time.Second, func(e voiceevent.TurnEnded) bool {
		return e.TurnID == rID && e.Reason == voiceevent.TurnEndBarge
	}, "turn.ended barge for the reaction sub-turn")
	voicetest.WaitEvent(t, h, 2*time.Second, func(e voiceevent.TurnEnded) bool {
		return e.TurnID == "Tr" && e.Reason == voiceevent.TurnEndBarge
	}, "turn.ended barge for the Lead's turn (the floor unit)")

	select {
	case <-spk.reactSpokeCh:
	case <-time.After(2 * time.Second):
		t.Fatal("SpeakReaction never returned after the barge")
	}
	pump.waitDiscard(t) // uniform defer done
	if floor.Active() {
		t.Fatal("the floor must be free after the barge")
	}
}
