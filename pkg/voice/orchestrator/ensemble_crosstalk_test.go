package orchestrator_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voicetest"
)

// miraTarget is a third addressed candidate for the 3+-target reactor-pick tests.
var miraTarget = voiceevent.AddressTarget{AgentID: "npc-mira", AgentRole: voiceevent.AgentRoleCharacter, Name: "Mira"}

// fakeCrossTalk is the gated [orchestrator.CrossTalker] for the #302 coordinator
// tests: it embeds the #301 [fakeEnsemble] (Draft/Speak) and adds React/SpeakReaction
// with the same per-Agent gate/pause machinery so a test drives the Cross-talk
// Reaction phase deterministically without sleeps (ADR-0021).
type fakeCrossTalk struct {
	*fakeEnsemble

	react          map[string]string        // agentID -> reaction text React returns
	reactErr       map[string]error         // agentID -> React error (optional)
	reactGate      map[string]chan struct{} // agentID -> React release gate; nil/absent = immediate
	reactStarted   chan string              // agentID pushed when React begins (optional)
	reactCancelled chan string              // agentID pushed when React's ctx was cancelled (optional)

	reactSentences map[string][]string // agentID -> sentences SpeakReaction dispatches (default: [reaction])
	reactPause     chan struct{}       // if set, SpeakReaction blocks after dispatching sentence[0]
	reactSpokeCh   chan string         // agentID pushed when SpeakReaction returns (optional)

	reactMu        sync.Mutex
	reactDelivered map[string]string // agentID -> text SpeakReaction committed (#375)
}

// recordReactDelivered stores what the Reaction committed for id, so a #375 test can
// assert an aborted/discarded/declined reaction commits nothing.
func (s *fakeCrossTalk) recordReactDelivered(id, text string) {
	s.reactMu.Lock()
	if s.reactDelivered == nil {
		s.reactDelivered = map[string]string{}
	}
	s.reactDelivered[id] = text
	s.reactMu.Unlock()
}

// reactDeliveredFor returns the Reaction text committed for id ("" = nothing).
func (s *fakeCrossTalk) reactDeliveredFor(id string) string {
	s.reactMu.Lock()
	defer s.reactMu.Unlock()
	return s.reactDelivered[id]
}

func (s *fakeCrossTalk) React(ctx context.Context, e voiceevent.AddressRouted, leadName, leadText string) (string, error) {
	id := e.Target.AgentID
	if s.reactStarted != nil {
		s.reactStarted <- id
	}
	if g := s.reactGate[id]; g != nil {
		select {
		case <-g:
		case <-ctx.Done():
			if s.reactCancelled != nil {
				s.reactCancelled <- id
			}
			return "", ctx.Err()
		}
	}
	if err := s.reactErr[id]; err != nil {
		return "", err
	}
	return s.react[id], nil
}

func (s *fakeCrossTalk) SpeakReaction(ctx context.Context, e voiceevent.AddressRouted, leadName, leadText, reaction string, dispatch func(orchestrator.Reply) error) (string, error) {
	id := e.Target.AgentID
	if s.reactSpokeCh != nil {
		defer func() { s.reactSpokeCh <- id }()
	}
	var delivered strings.Builder
	// Record what the Reaction committed (deliver-then-commit) so the #375 look-ahead
	// tests can assert an aborted/discarded reaction commits nothing.
	defer func() { s.recordReactDelivered(id, delivered.String()) }()
	sentences := []string{reaction}
	if s.reactSentences != nil {
		sentences = s.reactSentences[id]
	}
	for i, snt := range sentences {
		if err := dispatch(orchestrator.Reply{Sentence: snt, Voice: tts.Voice{VoiceID: id, Name: id}}); err != nil {
			// Three-class dispatch contract (#362, mirrors real SpeakReaction; #375): a
			// start-error (ErrNotDelivered) skips this sentence but keeps draining; any
			// other error (a barge cancel, or the look-ahead abort) stops the drain,
			// delivered-only.
			if errors.Is(err, orchestrator.ErrNotDelivered) {
				continue
			}
			return delivered.String(), nil
		}
		if delivered.Len() > 0 {
			delivered.WriteByte(' ')
		}
		delivered.WriteString(snt)
		if s.reactPause != nil && i == 0 {
			select {
			case <-s.reactPause:
			case <-ctx.Done():
				return delivered.String(), nil
			}
		}
	}
	return delivered.String(), nil
}

// waitFloorFree blocks until floor is released (runEnsemble has fully returned) or
// fails after 2s. It synchronizes a negative assertion on the whole ensemble turn: a
// free floor means the coordinator ran its last statement, so the observed-event log
// is complete.
func waitFloorFree(t *testing.T, floor *orchestrator.Floor) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for floor.Active() {
		if time.Now().After(deadline) {
			t.Fatal("the floor was never released — runEnsemble did not return")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// ttsInvokedInOrder returns the observed [voiceevent.TTSInvoked] events in order.
func ttsInvokedInOrder(t *testing.T, h *voicetest.Harness) []voiceevent.TTSInvoked {
	t.Helper()
	var out []voiceevent.TTSInvoked
	for _, e := range h.Events() {
		if ti, ok := e.(voiceevent.TTSInvoked); ok {
			out = append(out, ti)
		}
	}
	return out
}

// TestReplier_Ensemble_ReactionFollowsLead is the #302 AC1 headline (ADR-0025): after
// the Lead speaks, exactly one other addressed Agent's Cross-talk Reaction follows —
// under a FRESH TurnID linked to the Lead's, its sentences AFTER the Lead's last, and
// its React generation STARTED during the Lead's playback (before Speak returned).
func TestReplier_Ensemble_ReactionFollowsLead(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()
	spk := &fakeCrossTalk{
		fakeEnsemble: &fakeEnsemble{
			draft:          map[string]string{bartTarget.AgentID: "Bart leads."},
			gate:           map[string]chan struct{}{bartTarget.AgentID: closedGate(), goblinTarget.AgentID: make(chan struct{})},
			speakSentences: map[string][]string{bartTarget.AgentID: {"Bart leads."}},
			speakPause:     make(chan struct{}), // the Lead pauses after its sentence until the test releases it
			spokeCh:        make(chan string, 2),
		},
		react:        map[string]string{goblinTarget.AgentID: "I disagree, loudly."},
		reactStarted: make(chan string, 2),
		reactSpokeCh: make(chan string, 2),
	}
	replier := ensembleReplier(h, floor, spk)
	t.Cleanup(replier.Bind(t.Context(), h.Bus))
	// BargeIn drives FirstOpus → Floor.MarkSpeaking (production always wires barge with
	// an ensemble), so the Lead's FirstOpus below actually arms the floor.
	t.Cleanup(orchestrator.NewBargeIn(floor, 0).Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.EnsembleRouted{TurnID: "Te", Text: "Bart, Goblin — thoughts?", Targets: []voiceevent.AddressTarget{bartTarget, goblinTarget}})

	// The Lead dispatched its sentence and is now paused; the reactor's React ran
	// DURING that playback (before the Lead's Speak returned).
	voicetest.WaitEvent(t, h, 2*time.Second, func(e voiceevent.TTSInvoked) bool {
		return e.TurnID == "Te" && e.Sentence == "Bart leads."
	}, "the Lead's sentence is on the wire")
	// Arm the floor with the Lead's FirstOpus (as the wire would) — a Reaction plays
	// only once the Lead is audible, keeping the whole unit barge-able.
	h.Bus.Publish(voiceevent.FirstOpus{TurnID: "Te"})
	select {
	case who := <-spk.reactStarted:
		if who != goblinTarget.AgentID {
			t.Fatalf("React started for %q, want the remaining candidate Goblin", who)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the reactor's React never started during the Lead's playback")
	}

	// Release the Lead so its Speak returns; the queued Reaction then plays.
	close(spk.speakPause)

	var rID string
	voicetest.WaitEvent(t, h, 2*time.Second, func(e voiceevent.EnsembleReaction) bool {
		if e.LeadTurnID == "Te" && e.Target.AgentID == goblinTarget.AgentID {
			rID = e.TurnID
			return true
		}
		return false
	}, "ensemble.reaction for Goblin linked to the Lead's turn")

	if rID == "" || rID == "Te" {
		t.Fatalf("reaction TurnID = %q, want a FRESH id distinct from the Lead's Te", rID)
	}

	// The reaction's sentence lands under the fresh sub-turn id, AFTER the Lead's last.
	voicetest.WaitEvent(t, h, 2*time.Second, func(e voiceevent.TTSInvoked) bool {
		return e.TurnID == rID && e.Sentence == "I disagree, loudly."
	}, "the reaction sentence on the wire under the reaction sub-turn")

	got := ttsInvokedInOrder(t, h)
	if len(got) != 2 {
		t.Fatalf("TTSInvoked = %d, want 2 (Lead then reaction)", len(got))
	}
	if got[0].TurnID != "Te" || got[1].TurnID != rID {
		t.Fatalf("TTSInvoked order = [%s, %s], want [Te, %s] (reactor AFTER the Lead's last)", got[0].TurnID, got[1].TurnID, rID)
	}
}

// TestReplier_Ensemble_ReactorDeclines pins the decline path (ADR-0025, #302): when
// the reactor's React returns "" (the "[SILENCE]" sentinel upstream), the Lead speaks
// alone — no EnsembleReaction, no second TTSInvoked, no reaction TurnEnded.
func TestReplier_Ensemble_ReactorDeclines(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()
	spk := &fakeCrossTalk{
		fakeEnsemble: &fakeEnsemble{
			draft:          map[string]string{bartTarget.AgentID: "Bart leads."},
			gate:           map[string]chan struct{}{bartTarget.AgentID: closedGate(), goblinTarget.AgentID: make(chan struct{})},
			speakSentences: map[string][]string{bartTarget.AgentID: {"Bart leads."}},
			speakPause:     make(chan struct{}), // pause so the floor is armed before the reaction gate — the decline branch must actually run
			spokeCh:        make(chan string, 2),
		},
		react:        map[string]string{ /* goblin: no entry → "" → decline */ },
		reactSpokeCh: make(chan string, 2),
	}
	replier := ensembleReplier(h, floor, spk)
	t.Cleanup(replier.Bind(t.Context(), h.Bus))
	// Arm the floor exactly as the reaction-playing tests do — otherwise the audible
	// gate would skip the reaction BEFORE the decline branch runs, passing vacuously.
	t.Cleanup(orchestrator.NewBargeIn(floor, 0).Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.EnsembleRouted{TurnID: "Td", Text: "Bart, Goblin?", Targets: []voiceevent.AddressTarget{bartTarget, goblinTarget}})

	// The Lead is audible: arm the floor, then release it so the reactor's decline is
	// actually reached (React returns "" → no event, no line).
	voicetest.WaitEvent(t, h, 2*time.Second, func(e voiceevent.TTSInvoked) bool {
		return e.TurnID == "Td" && e.Sentence == "Bart leads."
	}, "the Lead's sentence is on the wire")
	h.Bus.Publish(voiceevent.FirstOpus{TurnID: "Td"})
	close(spk.speakPause)

	<-spk.spokeCh // the Lead's Speak returned

	voicetest.WaitEvent(t, h, 2*time.Second, func(e voiceevent.EnsembleLead) bool { return e.TurnID == "Td" }, "the Lead was elected")
	// Wait for runEnsemble to FULLY return — the floor is released only after the
	// reactor's decline path ran to completion (speakReaction: read React's "" → return).
	// Without this, the assertions below snapshot BEFORE the coordinator reaches the
	// decline branch, passing vacuously even if a mutation published EnsembleReaction.
	waitFloorFree(t, floor)
	// A declined Reaction publishes nothing and speaks nothing.
	voicetest.AssertNoEvent[voiceevent.EnsembleReaction](t, h)
	voicetest.AssertEventCount[voiceevent.TTSInvoked](t, h, 1)
	voicetest.AssertNoEvent[voiceevent.TurnEnded](t, h)
}

// TestReplier_Ensemble_BargeDuringLeadDiscardsReaction pins ADR-0027 for the Reaction
// queue: a human barge while the Lead speaks — with the reactor's React still
// generating — tears the WHOLE unit down. The reaction never plays: React's ctx is
// cancelled, no EnsembleReaction is published, and the only turn-end is the barge for
// the ensemble's original TurnID. A queued Reaction after a barge is FORBIDDEN.
func TestReplier_Ensemble_BargeDuringLeadDiscardsReaction(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()
	spk := &fakeCrossTalk{
		fakeEnsemble: &fakeEnsemble{
			draft:          map[string]string{bartTarget.AgentID: "First. Second."},
			gate:           map[string]chan struct{}{bartTarget.AgentID: closedGate(), goblinTarget.AgentID: make(chan struct{})},
			speakSentences: map[string][]string{bartTarget.AgentID: {"First.", "Second."}},
			speakPause:     make(chan struct{}), // the Lead pauses after sentence 1 until the barge cancels ctx
			spokeCh:        make(chan string, 2),
		},
		react:          map[string]string{goblinTarget.AgentID: "I would react."},
		reactGate:      map[string]chan struct{}{goblinTarget.AgentID: make(chan struct{})}, // React never releases — it is cut by the barge
		reactStarted:   make(chan string, 2),
		reactCancelled: make(chan string, 2),
	}
	replier := ensembleReplier(h, floor, spk)
	t.Cleanup(replier.Bind(t.Context(), h.Bus))
	t.Cleanup(orchestrator.NewBargeIn(floor, 0).Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.EnsembleRouted{TurnID: "Tb", Text: "Bart, Goblin?", Targets: []voiceevent.AddressTarget{bartTarget, goblinTarget}})

	// The Lead is audibly speaking and the reactor's React has started.
	voicetest.WaitEvent(t, h, 2*time.Second, func(e voiceevent.TTSInvoked) bool {
		return e.TurnID == "Tb" && e.Sentence == "First."
	}, "the Lead's first sentence is on the wire")
	<-spk.reactStarted

	// A human barges while the Lead is audible.
	h.Bus.Publish(voiceevent.FirstOpus{TurnID: "Tb"})
	h.Bus.Publish(voiceevent.VADSpeechStart{})

	// The whole unit tears down: barge for the ensemble's original TurnID.
	voicetest.WaitEvent(t, h, 2*time.Second, func(e voiceevent.TurnEnded) bool {
		return e.TurnID == "Tb" && e.Reason == voiceevent.TurnEndBarge
	}, "turn.ended barge for the ensemble")

	// The reactor's React was cancelled and it never reached the wire.
	select {
	case who := <-spk.reactCancelled:
		if who != goblinTarget.AgentID {
			t.Fatalf("React cancelled for %q, want the reactor Goblin", who)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the reactor's React was never cancelled by the barge")
	}
	voicetest.AssertNoEvent[voiceevent.EnsembleReaction](t, h)
	// Only the Lead's first sentence reached the wire (its second and any reaction cut).
	voicetest.AssertEventCount[voiceevent.TTSInvoked](t, h, 1)
	if floor.Active() {
		t.Fatal("the floor must be free after the barge")
	}
}

// TestReplier_Ensemble_BargeDuringReactionEndsSubTurn pins the independently-barge-able
// Reaction (ADR-0025/0027, #302): a human barge WHILE the Reaction plays tears the
// unit down. The Reaction's dispatch unwinds (delivered-only), the reaction sub-turn
// ends with a barge TurnEnded carrying its OWN id, and the Lead's already-delivered
// line stays committed. The floor stays armed across the whole unit from the Lead's
// FirstOpus, so the barge lands during the Reaction's playback.
func TestReplier_Ensemble_BargeDuringReactionEndsSubTurn(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()
	spk := &fakeCrossTalk{
		fakeEnsemble: &fakeEnsemble{
			draft:          map[string]string{bartTarget.AgentID: "Bart done."},
			gate:           map[string]chan struct{}{bartTarget.AgentID: closedGate(), goblinTarget.AgentID: make(chan struct{})},
			speakSentences: map[string][]string{bartTarget.AgentID: {"Bart done."}},
			speakPause:     make(chan struct{}), // pause so the floor is armed before the reaction gate
			spokeCh:        make(chan string, 2),
		},
		react:          map[string]string{goblinTarget.AgentID: "React one. React two."},
		reactSentences: map[string][]string{goblinTarget.AgentID: {"React one.", "React two."}},
		reactPause:     make(chan struct{}), // the Reaction pauses after sentence 1 until the barge cancels ctx
		reactSpokeCh:   make(chan string, 2),
	}
	replier := ensembleReplier(h, floor, spk)
	t.Cleanup(replier.Bind(t.Context(), h.Bus))
	t.Cleanup(orchestrator.NewBargeIn(floor, 0).Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.EnsembleRouted{TurnID: "Tr", Text: "Bart, Goblin?", Targets: []voiceevent.AddressTarget{bartTarget, goblinTarget}})

	// The Lead delivered its sentence; arm the floor with its FirstOpus (as the wire
	// would), so the unit stays barge-able through the gap and the Reaction. Release the
	// paused Lead only after the floor is armed.
	voicetest.WaitEvent(t, h, 2*time.Second, func(e voiceevent.TTSInvoked) bool {
		return e.TurnID == "Tr" && e.Sentence == "Bart done."
	}, "the Lead's sentence is on the wire")
	h.Bus.Publish(voiceevent.FirstOpus{TurnID: "Tr"})
	close(spk.speakPause)

	// The Reaction begins under its fresh sub-turn id and pauses after sentence 1.
	var rID string
	voicetest.WaitEvent(t, h, 2*time.Second, func(e voiceevent.EnsembleReaction) bool {
		rID = e.TurnID
		return e.LeadTurnID == "Tr"
	}, "ensemble.reaction published")
	voicetest.WaitEvent(t, h, 2*time.Second, func(e voiceevent.TTSInvoked) bool {
		return e.TurnID == rID && e.Sentence == "React one."
	}, "the Reaction's first sentence is on the wire")

	// A human barges DURING the Reaction's playback.
	h.Bus.Publish(voiceevent.VADSpeechStart{})

	// The reaction sub-turn ends with a barge under its OWN id.
	voicetest.WaitEvent(t, h, 2*time.Second, func(e voiceevent.TurnEnded) bool {
		return e.TurnID == rID && e.Reason == voiceevent.TurnEndBarge
	}, "turn.ended barge for the reaction sub-turn")
	// INTENTIONAL: the barge ALSO ends the Lead's turn under the ensemble's original
	// TurnID (the floor's holder turn throughout the one-unit floor, ADR-0027). Both
	// TurnEnded are correct floor-unit semantics; the Lead's already-delivered line
	// stays committed (the relay treats a post-delivery TurnEnded as an interruption).
	voicetest.WaitEvent(t, h, 2*time.Second, func(e voiceevent.TurnEnded) bool {
		return e.TurnID == "Tr" && e.Reason == voiceevent.TurnEndBarge
	}, "turn.ended barge for the Lead's turn (the floor unit)")

	select {
	case <-spk.reactSpokeCh:
	case <-time.After(2 * time.Second):
		t.Fatal("the Reaction's SpeakReaction never returned after the barge")
	}
	// Only the Reaction's first sentence reached the wire (Lead + reaction sentence 1).
	voicetest.AssertEventCount[voiceevent.TTSInvoked](t, h, 2)
	if floor.Active() {
		t.Fatal("the floor must be free after the barge")
	}
}

// TestReplier_Ensemble_ThreeTargets_FastestRemainingReacts pins the ADR-0025 breadth
// bound (#302): with 3+ Agents addressed, only ONE — the fastest of the candidates
// remaining after the Lead — reacts; the rest stay silent that turn. The Lead (Bart)
// wins the race with the others gated; the first remaining draft to arrive (Goblin)
// is the reactor, and the third (Mira) is silent and its speculative draft cancelled.
func TestReplier_Ensemble_ThreeTargets_FastestRemainingReacts(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()
	goblinGate := make(chan struct{})
	spk := &fakeCrossTalk{
		fakeEnsemble: &fakeEnsemble{
			draft: map[string]string{bartTarget.AgentID: "Bart leads.", goblinTarget.AgentID: "Goblin draft (discarded).", miraTarget.AgentID: "Mira draft (discarded)."},
			gate: map[string]chan struct{}{
				bartTarget.AgentID:   closedGate(),        // wins the Lead race alone (others gated)
				goblinTarget.AgentID: goblinGate,          // released post-election → fastest remaining → reactor
				miraTarget.AgentID:   make(chan struct{}), // never released → cancelled, silent
			},
			cancelled: make(chan string, 4),
			spokeCh:   make(chan string, 2),
		},
		react:        map[string]string{goblinTarget.AgentID: "I have a hot take."},
		reactStarted: make(chan string, 4),
		reactSpokeCh: make(chan string, 2),
	}
	replier := ensembleReplier(h, floor, spk)
	t.Cleanup(replier.Bind(t.Context(), h.Bus))
	t.Cleanup(orchestrator.NewBargeIn(floor, 0).Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.EnsembleRouted{TurnID: "T3", Text: "Bart, Goblin, Mira?", Targets: []voiceevent.AddressTarget{bartTarget, goblinTarget, miraTarget}})

	voicetest.WaitEvent(t, h, 2*time.Second, func(e voiceevent.EnsembleLead) bool {
		return e.TurnID == "T3" && e.Target.AgentID == bartTarget.AgentID
	}, "Bart elected Lead")

	// Arm the floor with the Lead's FirstOpus so the Reaction may play (barge-able).
	h.Bus.Publish(voiceevent.FirstOpus{TurnID: "T3"})
	// Release Goblin so it is the fastest remaining draft to arrive — the reactor.
	close(goblinGate)

	voicetest.WaitEvent(t, h, 2*time.Second, func(e voiceevent.EnsembleReaction) bool {
		return e.LeadTurnID == "T3" && e.Target.AgentID == goblinTarget.AgentID
	}, "Goblin (fastest remaining) reacts")

	// Only Goblin reacted — Mira never did.
	select {
	case who := <-spk.reactStarted:
		if who != goblinTarget.AgentID {
			t.Fatalf("React started for %q, want only the reactor Goblin", who)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the reactor's React never started")
	}

	// Mira stayed silent: its speculative draft was cancelled and it never reacted.
	sawMiraCancelled := false
	for !sawMiraCancelled {
		select {
		case who := <-spk.cancelled:
			if who == miraTarget.AgentID {
				sawMiraCancelled = true
			}
		case <-time.After(2 * time.Second):
			t.Fatal("the silent third candidate Mira's draft was never cancelled")
		}
	}
	select {
	case who := <-spk.reactStarted:
		t.Fatalf("a third candidate reacted (%q); only the fastest remaining may react", who)
	case <-time.After(100 * time.Millisecond):
	}
}

// TestReplier_Ensemble_ReactionDepthCappedAtOne pins the ADR-0025 depth cap (#302): a
// Reaction never re-triggers the Ensemble machinery. After a full Lead + Reaction, the
// reaction sub-turn publishes NO new routing (no AddressRouted / EnsembleRouted), no
// second EnsembleReaction, and no second EnsembleLead — it runs exactly once.
func TestReplier_Ensemble_ReactionDepthCappedAtOne(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()
	spk := &fakeCrossTalk{
		fakeEnsemble: &fakeEnsemble{
			draft:          map[string]string{bartTarget.AgentID: "Bart leads."},
			gate:           map[string]chan struct{}{bartTarget.AgentID: closedGate(), goblinTarget.AgentID: make(chan struct{})},
			speakSentences: map[string][]string{bartTarget.AgentID: {"Bart leads."}},
			speakPause:     make(chan struct{}), // pause so the test can arm the floor before the reaction gate
			spokeCh:        make(chan string, 2),
		},
		react:        map[string]string{goblinTarget.AgentID: "I have a reaction."},
		reactSpokeCh: make(chan string, 2),
	}
	replier := ensembleReplier(h, floor, spk)
	t.Cleanup(replier.Bind(t.Context(), h.Bus))
	t.Cleanup(orchestrator.NewBargeIn(floor, 0).Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.EnsembleRouted{TurnID: "Tc", Text: "Bart, Goblin?", Targets: []voiceevent.AddressTarget{bartTarget, goblinTarget}})

	// The Lead is audible: arm the floor, then release it so the Reaction plays.
	voicetest.WaitEvent(t, h, 2*time.Second, func(e voiceevent.TTSInvoked) bool {
		return e.TurnID == "Tc" && e.Sentence == "Bart leads."
	}, "the Lead's sentence is on the wire")
	h.Bus.Publish(voiceevent.FirstOpus{TurnID: "Tc"})
	close(spk.speakPause)

	// Wait for the reaction to actually play out.
	select {
	case <-spk.reactSpokeCh:
	case <-time.After(2 * time.Second):
		t.Fatal("the reaction never played")
	}
	voicetest.WaitEvent(t, h, 2*time.Second, func(e voiceevent.EnsembleReaction) bool { return e.LeadTurnID == "Tc" }, "the reaction played once")

	// Depth 1: no re-routing, exactly one of each ensemble signal, no reaction-to-reaction.
	voicetest.AssertNoEvent[voiceevent.AddressRouted](t, h)
	voicetest.AssertEventCount[voiceevent.EnsembleRouted](t, h, 1)
	voicetest.AssertEventCount[voiceevent.EnsembleLead](t, h, 1)
	voicetest.AssertEventCount[voiceevent.EnsembleReaction](t, h, 1)
}

// TestReplier_Ensemble_ZeroDeliveredLeadSkipsReaction pins the "Lead delivered ≥1
// sentence" precondition (ADR-0025, #302): if the elected Lead speaks nothing (its
// dispatch delivered zero sentences — a barge or synth gap), there is nothing to
// cross-talk, so no Reaction is published or spoken even though a reactor stood ready.
func TestReplier_Ensemble_ZeroDeliveredLeadSkipsReaction(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()
	spk := &fakeCrossTalk{
		fakeEnsemble: &fakeEnsemble{
			draft:          map[string]string{bartTarget.AgentID: "Bart leads."},
			gate:           map[string]chan struct{}{bartTarget.AgentID: closedGate(), goblinTarget.AgentID: make(chan struct{})},
			speakSentences: map[string][]string{bartTarget.AgentID: {}}, // the Lead delivers NOTHING
			spokeCh:        make(chan string, 2),
		},
		react:        map[string]string{goblinTarget.AgentID: "I would have reacted."},
		reactSpokeCh: make(chan string, 2),
	}
	replier := ensembleReplier(h, floor, spk)
	t.Cleanup(replier.Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.EnsembleRouted{TurnID: "Tz", Text: "Bart, Goblin?", Targets: []voiceevent.AddressTarget{bartTarget, goblinTarget}})

	<-spk.spokeCh // the Lead's (empty) Speak returned
	voicetest.WaitEvent(t, h, 2*time.Second, func(e voiceevent.EnsembleLead) bool { return e.TurnID == "Tz" }, "the Lead was elected")

	// The Lead delivered nothing, so no Reaction plays.
	voicetest.AssertNoEvent[voiceevent.EnsembleReaction](t, h)
	voicetest.AssertEventCount[voiceevent.TTSInvoked](t, h, 0)
	select {
	case who := <-spk.reactSpokeCh:
		t.Fatalf("%q spoke a reaction after a zero-delivered Lead; none may play", who)
	case <-time.After(100 * time.Millisecond):
	}
}

// TestReplier_Ensemble_ReactionAllStartErrorEndsTurn pins #391 (legacy #302 path):
// when EVERY sentence of the Cross-talk Reaction fails to START synthesis — nothing
// delivered — under a live turn, the reaction sub-turn is announced (EnsembleReaction)
// and THEN ended with TurnEnded{rID, tts_error}, mirroring the Lead. Without the
// terminal event the reaction id emits EnsembleReaction but no FirstAudio and no
// TurnEnded, so the metrics TTL sweep miscounts it as an abandoned/no_first_audio turn.
func TestReplier_Ensemble_ReactionAllStartErrorEndsTurn(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()
	spk := &fakeCrossTalk{
		fakeEnsemble: &fakeEnsemble{
			draft:          map[string]string{bartTarget.AgentID: "Bart leads."},
			gate:           map[string]chan struct{}{bartTarget.AgentID: closedGate(), goblinTarget.AgentID: make(chan struct{})},
			speakSentences: map[string][]string{bartTarget.AgentID: {"Bart leads."}},
			speakPause:     make(chan struct{}), // pause so the floor arms before the reaction gate
			spokeCh:        make(chan string, 2),
		},
		react:        map[string]string{goblinTarget.AgentID: "I react anyway."},
		reactSpokeCh: make(chan string, 2),
	}
	// The Lead's sentence synthesizes fine; the reaction's ONLY sentence start-errors.
	ttsStage := orchestrator.NewTTS(h.Bus, selectiveSynth{failOn: map[string]bool{"I react anyway.": true}})
	replier := orchestrator.NewStreamReplier(ttsStage, func(context.Context, voiceevent.AddressRouted, func(orchestrator.Reply) error) error {
		return nil
	}, nil)
	replier.SetFloor(floor)
	replier.SetEnsemble(spk)
	t.Cleanup(replier.Bind(t.Context(), h.Bus))
	t.Cleanup(orchestrator.NewBargeIn(floor, 0).Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.EnsembleRouted{TurnID: "Tp", Text: "Bart, Goblin?", Targets: []voiceevent.AddressTarget{bartTarget, goblinTarget}})

	// The Lead is audible: arm the floor, then release it so the Reaction is reached.
	voicetest.WaitEvent(t, h, 2*time.Second, func(e voiceevent.TTSInvoked) bool {
		return e.TurnID == "Tp" && e.Sentence == "Bart leads."
	}, "the Lead's sentence is on the wire")
	h.Bus.Publish(voiceevent.FirstOpus{TurnID: "Tp"})
	close(spk.speakPause)

	// The reaction is announced, then ended tts_error (all its sentences failed to start).
	var rID string
	voicetest.WaitEvent(t, h, 2*time.Second, func(e voiceevent.EnsembleReaction) bool {
		if e.LeadTurnID == "Tp" && e.Target.AgentID == goblinTarget.AgentID {
			rID = e.TurnID
			return true
		}
		return false
	}, "ensemble.reaction announced for the reactor")
	voicetest.WaitEvent(t, h, 2*time.Second, func(e voiceevent.TurnEnded) bool {
		return e.TurnID == rID && e.Reason == voiceevent.TurnEndTTSError
	}, "turn.ended tts_error for the reaction sub-turn (no TTL reap)")

	waitFloorFree(t, floor)
	// Nothing delivered, and the ONLY turn-end is the reaction's tts_error (no barge, and
	// the Lead's own turn cleanly delivered so it emits none).
	if got := spk.reactDeliveredFor(goblinTarget.AgentID); got != "" {
		t.Fatalf("an all-start-error reaction committed %q, want nothing", got)
	}
	voicetest.AssertEventCount[voiceevent.TurnEnded](t, h, 1)
}

// TestReplier_Ensemble_TTSFailedLeadSkipsReaction pins ADR-0027 for the Reaction gate
// (#302): a Lead whose ONLY sentence fails synthesis produced NO audio and NO
// FirstOpus, so the floor never armed. Even though its (undelivered) text is committed
// to history, the Reaction must NOT play — a Reaction after the Lead's
// TurnEnded{tts_error}, atop an un-armed floor, would be UNBARGEABLE (ADR-0027). The
// gate keys on Floor.Speaking (audible delivery), not committed text.
func TestReplier_Ensemble_TTSFailedLeadSkipsReaction(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()
	spk := &fakeCrossTalk{
		fakeEnsemble: &fakeEnsemble{
			draft:   map[string]string{bartTarget.AgentID: "Bart leads."},
			gate:    map[string]chan struct{}{bartTarget.AgentID: closedGate(), goblinTarget.AgentID: make(chan struct{})},
			spokeCh: make(chan string, 2),
		},
		react:        map[string]string{goblinTarget.AgentID: "I react anyway."},
		reactSpokeCh: make(chan string, 2),
	}
	// The Lead's ONLY sentence fails synth (no FirstOpus ever); a bound BargeIn drives
	// the FirstOpus→MarkSpeaking wiring so this is a faithful "no audio" scenario.
	ttsStage := orchestrator.NewTTS(h.Bus, selectiveSynth{failOn: map[string]bool{"Bart leads.": true}})
	replier := orchestrator.NewStreamReplier(ttsStage, func(context.Context, voiceevent.AddressRouted, func(orchestrator.Reply) error) error {
		return nil
	}, nil)
	replier.SetFloor(floor)
	replier.SetEnsemble(spk)
	t.Cleanup(replier.Bind(t.Context(), h.Bus))
	t.Cleanup(orchestrator.NewBargeIn(floor, 0).Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.EnsembleRouted{TurnID: "Tf", Text: "Bart, Goblin?", Targets: []voiceevent.AddressTarget{bartTarget, goblinTarget}})

	voicetest.WaitEvent(t, h, 2*time.Second, func(e voiceevent.TurnEnded) bool {
		return e.TurnID == "Tf" && e.Reason == voiceevent.TurnEndTTSError
	}, "the ensemble ended tts_error (the Lead produced no audio)")

	// No Reaction plays atop the un-armed floor, and nothing reacted.
	select {
	case who := <-spk.reactSpokeCh:
		t.Fatalf("%q spoke a reaction after a no-audio Lead; the floor never armed (unbargeable)", who)
	case <-time.After(200 * time.Millisecond):
	}
	voicetest.AssertNoEvent[voiceevent.EnsembleReaction](t, h)
	if floor.Speaking() {
		t.Fatal("floor must not be Speaking after a no-audio Lead")
	}
}

// TestReplier_Ensemble_ThreeTargets_FastEmptyDraftsNoWedge pins that the reactor pick
// never blocks on a result that will never arrive (#302, reviewer finding 1): with 3
// targets where two finish FAST with empty drafts (consumed by the Lead race before
// the slow winner lands), the reactor pick must not wait on the drained results
// channel. The Lead still speaks; the pick keys on OUTSTANDING results, not the
// remaining-targets slice.
func TestReplier_Ensemble_ThreeTargets_FastEmptyDraftsNoWedge(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()
	bartGate := make(chan struct{})
	spk := &fakeCrossTalk{
		fakeEnsemble: &fakeEnsemble{
			draft: map[string]string{
				bartTarget.AgentID:   "Bart leads.",
				goblinTarget.AgentID: "", // fast decline
				miraTarget.AgentID:   "", // fast decline
			},
			gate: map[string]chan struct{}{
				bartTarget.AgentID:   bartGate,     // slow: finishes AFTER the empties are consumed
				goblinTarget.AgentID: closedGate(), // instant
				miraTarget.AgentID:   closedGate(), // instant
			},
			started: make(chan string, 4),
			spokeCh: make(chan string, 2),
		},
		react:        map[string]string{},
		reactSpokeCh: make(chan string, 2),
	}
	replier := ensembleReplier(h, floor, spk)
	t.Cleanup(replier.Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.EnsembleRouted{TurnID: "Tw", Text: "Bart, Goblin, Mira?", Targets: []voiceevent.AddressTarget{bartTarget, goblinTarget, miraTarget}})

	// All three drafts started; the two empties are already buffered/consumed.
	for i := 0; i < 3; i++ {
		<-spk.started
	}
	// Let the race loop consume both empty results, then let Bart finish.
	time.Sleep(50 * time.Millisecond)
	close(bartGate)

	select {
	case who := <-spk.spokeCh:
		if who != bartTarget.AgentID {
			t.Fatalf("spoke = %q, want Bart", who)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("WEDGED: the Lead never spoke — reactor pick waited on a drained results channel")
	}
	// Both remaining candidates declined their drafts, so no one reacts.
	voicetest.AssertNoEvent[voiceevent.EnsembleReaction](t, h)
}
