package orchestrator_test

import (
	"context"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voicetest"
)

// textLeadSpeaker is a minimal [orchestrator.EnsembleSpeaker] whose elected Lead
// (leadID) delivers its draft as TEXT: Speak returns [orchestrator.ErrTextDelivered]
// WITHOUT calling dispatch, exactly as the real [agent.Replier.SpeakDraft] does for a
// voiceless / long / text-requested Butler draft (#389). It implements NO CrossTalker,
// so the coordinator runs the Lead-only path. spokeCh reports when Speak returns.
type textLeadSpeaker struct {
	draft   map[string]string
	leadID  string
	spokeCh chan string
}

func (s *textLeadSpeaker) Draft(_ context.Context, e voiceevent.AddressRouted) (string, error) {
	return s.draft[e.Target.AgentID], nil
}

func (s *textLeadSpeaker) Speak(_ context.Context, e voiceevent.AddressRouted, draft string, dispatch func(orchestrator.Reply) error) (string, error) {
	if s.spokeCh != nil {
		defer func() { s.spokeCh <- e.Target.AgentID }()
	}
	if e.Target.AgentID == s.leadID {
		// Text delivery: post handled inside the real Replier; the coordinator sees the
		// terminal sentinel and must publish TurnEnded(text_delivered) with ZERO dispatch.
		return draft, orchestrator.ErrTextDelivered
	}
	_ = dispatch(orchestrator.Reply{Sentence: draft})
	return draft, nil
}

// TestReplier_Ensemble_TextDeliveredLead_PublishesTerminal pins the coordinator side
// of #389: when the elected Lead delivers as text (Speak returns ErrTextDelivered),
// the ensemble publishes TurnEnded(text_delivered) — mirroring the routed path's
// ErrTextDelivered→TurnEndTextDelivered mapping — dispatches NO TTS (no TTSInvoked),
// and opens no Cross-talk Reaction (a text Lead is never audibly on the wire).
func TestReplier_Ensemble_TextDeliveredLead_PublishesTerminal(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()
	spk := &textLeadSpeaker{
		draft:   map[string]string{butlerTarget.AgentID: "A long recap posted as text."},
		leadID:  butlerTarget.AgentID,
		spokeCh: make(chan string, 2),
	}
	replier := ensembleReplier(h, floor, spk)
	t.Cleanup(replier.Bind(t.Context(), h.Bus))

	// Bart has no draft (empty → skipped); the Butler is the sole non-empty draft → Lead.
	h.Bus.Publish(voiceevent.EnsembleRouted{TurnID: "Ttext", Text: "Bart, Glyphoxa — recap?", Targets: []voiceevent.AddressTarget{bartTarget, butlerTarget}})

	select {
	case who := <-spk.spokeCh:
		if who != butlerTarget.AgentID {
			t.Fatalf("Speak ran for %q, want the Butler Lead", who)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the ensemble never elected / spoke a Lead")
	}

	voicetest.AssertEvent(t, h, func(e voiceevent.EnsembleLead) bool {
		return e.TurnID == "Ttext" && e.Target.AgentID == butlerTarget.AgentID
	}, "ensemble.lead → Butler")
	// The terminal: a text-delivered Lead is a SUCCESS with no first audio.
	voicetest.AssertEvent(t, h, func(e voiceevent.TurnEnded) bool {
		return e.TurnID == "Ttext" && e.Reason == voiceevent.TurnEndTextDelivered
	}, "turn.ended(text_delivered) for the text Lead")
	// A text turn dispatches no TTS.
	voicetest.AssertNoEvent[voiceevent.TTSInvoked](t, h)
	// No Cross-talk Reaction opens off a text Lead.
	voicetest.AssertNoEvent[voiceevent.EnsembleReaction](t, h)
}

// textReactorSpeaker: a spoken Character Lead (leadID) plus a voiceless-Butler
// reactor (reactorID) whose SpeakReaction routes to text — it returns
// ErrTextDelivered WITHOUT dispatching, exactly as the real Replier does for a
// voiceless Butler elected as Cross-talk reactor (#389/#302). The reactor's
// speculative draft is empty so it is the sole remaining candidate (the coordinator
// makes it the reactor without waiting on a draft).
type textReactorSpeaker struct {
	leadID     string
	leadDraft  string
	reaction   string
	speakPause chan struct{} // Lead pauses after its sentence so the test can arm the floor
	reactSpoke chan string   // reactorID pushed when SpeakReaction returns
}

func (s *textReactorSpeaker) Draft(_ context.Context, e voiceevent.AddressRouted) (string, error) {
	if e.Target.AgentID == s.leadID {
		return s.leadDraft, nil
	}
	return "", nil // reactor: empty speculative draft (regenerates a Reaction)
}

func (s *textReactorSpeaker) Speak(ctx context.Context, e voiceevent.AddressRouted, draft string, dispatch func(orchestrator.Reply) error) (string, error) {
	if err := dispatch(orchestrator.Reply{Sentence: draft, Voice: tts.Voice{VoiceID: e.Target.AgentID, Name: e.Target.AgentID}}); err != nil {
		return "", nil
	}
	if s.speakPause != nil {
		select {
		case <-s.speakPause:
		case <-ctx.Done():
		}
	}
	return draft, nil
}

func (s *textReactorSpeaker) React(_ context.Context, _ voiceevent.AddressRouted, _, _ string) (string, error) {
	return s.reaction, nil
}

func (s *textReactorSpeaker) SpeakReaction(_ context.Context, e voiceevent.AddressRouted, _, _, reaction string, dispatch func(orchestrator.Reply) error) (string, error) {
	if s.reactSpoke != nil {
		defer func() { s.reactSpoke <- e.Target.AgentID }()
	}
	// Voiceless Butler reactor: deliver as text, ZERO dispatch, return the sentinel.
	return reaction, orchestrator.ErrTextDelivered
}

// TestReplier_Ensemble_TextDeliveredReactor_PublishesTerminal pins the Cross-talk
// Reaction side of #389: a voiceless Butler elected as reactor delivers its Reaction
// as TEXT (SpeakReaction → ErrTextDelivered, no dispatch). Its sub-turn line was
// already announced via EnsembleReaction, so the coordinator publishes
// TurnEnded(rID, text_delivered) — the reactor never dispatches a second TTSInvoked,
// and the sub-turn does not hang or leak.
func TestReplier_Ensemble_TextDeliveredReactor_PublishesTerminal(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()
	spk := &textReactorSpeaker{
		leadID:     bartTarget.AgentID,
		leadDraft:  "Bart leads.",
		reaction:   "As you wish, my lord.",
		speakPause: make(chan struct{}),
		reactSpoke: make(chan string, 2),
	}
	replier := ensembleReplier(h, floor, spk)
	t.Cleanup(replier.Bind(t.Context(), h.Bus))
	// BargeIn wires FirstOpus → Floor.MarkSpeaking so the Lead's FirstOpus arms the floor.
	t.Cleanup(orchestrator.NewBargeIn(floor, 0).Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.EnsembleRouted{TurnID: "Tr", Text: "Bart, Glyphoxa — thoughts?", Targets: []voiceevent.AddressTarget{bartTarget, butlerTarget}})

	// The Lead's sentence reaches the wire; arm the floor so the Reaction may play.
	voicetest.WaitEvent(t, h, 2*time.Second, func(e voiceevent.TTSInvoked) bool {
		return e.TurnID == "Tr" && e.Sentence == "Bart leads."
	}, "the Lead's sentence is on the wire")
	h.Bus.Publish(voiceevent.FirstOpus{TurnID: "Tr"})

	// Release the Lead so its Speak returns and the reactor's text Reaction runs.
	close(spk.speakPause)

	var rID string
	voicetest.WaitEvent(t, h, 2*time.Second, func(e voiceevent.EnsembleReaction) bool {
		if e.LeadTurnID == "Tr" && e.Target.AgentID == butlerTarget.AgentID {
			rID = e.TurnID
			return true
		}
		return false
	}, "ensemble.reaction announced for the Butler reactor")

	select {
	case who := <-spk.reactSpoke:
		if who != butlerTarget.AgentID {
			t.Fatalf("SpeakReaction ran for %q, want the Butler reactor", who)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the reactor's SpeakReaction never returned (a text reactor must not hang)")
	}

	// The reactor's sub-turn ends text_delivered, never a second TTSInvoked.
	voicetest.WaitEvent(t, h, 2*time.Second, func(e voiceevent.TurnEnded) bool {
		return e.TurnID == rID && e.Reason == voiceevent.TurnEndTextDelivered
	}, "turn.ended(text_delivered) for the text reactor")
	waitFloorFree(t, floor)
	if got := ttsInvokedInOrder(t, h); len(got) != 1 || got[0].TurnID != "Tr" {
		t.Fatalf("TTSInvoked = %v, want exactly the Lead's (the text reactor dispatches none)", got)
	}
}

// TestReplier_Lookahead_TextDeliveredReactor_NoHang pins the #389 look-ahead guard:
// a voiceless Butler reactor text-delivers (SpeakReaction never calls the dispatch
// closure), so the prerender goroutine holds NO first sentence — s1Ch stays empty and
// done closes. releaseReaction's <-done arm must return instead of blocking forever on
// s1Ch. The turn completes (floor freed), the uniform defer's DiscardLookahead fires,
// and no EnsembleReaction / reaction TTSInvoked is published (the Reaction went to
// channel text, not the wire).
func TestReplier_Lookahead_TextDeliveredReactor_NoHang(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()
	pump := newFakeLookahead()
	synth := &laneSynth{pump: pump, holdGate: make(chan struct{})}
	defer close(synth.holdGate)

	spk := &textReactorSpeaker{
		leadID:     bartTarget.AgentID,
		leadDraft:  "Bart leads.",
		reaction:   "As you wish, my lord.",
		speakPause: make(chan struct{}),
		reactSpoke: make(chan string, 2),
	}
	replier := lookaheadReplier(h, floor, spk, pump, synth)
	t.Cleanup(replier.Bind(t.Context(), h.Bus))
	t.Cleanup(orchestrator.NewBargeIn(floor, 0).Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.EnsembleRouted{TurnID: "Tl", Text: "Bart, Glyphoxa?", Targets: []voiceevent.AddressTarget{bartTarget, butlerTarget}})

	// Arm the floor once the Lead is audible, then release the paused Lead.
	voicetest.WaitEvent(t, h, 2*time.Second, func(e voiceevent.TTSInvoked) bool {
		return e.TurnID == "Tl" && e.Sentence == "Bart leads."
	}, "the Lead's sentence is on the wire")
	h.Bus.Publish(voiceevent.FirstOpus{TurnID: "Tl"})
	close(spk.speakPause)

	// The reactor's SpeakReaction returned (text-delivered) — the coordinator must not
	// wedge on s1Ch.
	select {
	case <-spk.reactSpoke:
	case <-time.After(2 * time.Second):
		t.Fatal("the text reactor's SpeakReaction never returned (look-ahead release wedged on s1Ch?)")
	}

	// The uniform defer ran and the whole unit completed.
	pump.waitDiscard(t)
	waitFloorFree(t, floor)

	// No audio-less reaction line was created, and the reactor dispatched no audio.
	voicetest.AssertNoEvent[voiceevent.EnsembleReaction](t, h)
	if got := ttsInvokedInOrder(t, h); len(got) != 1 || got[0].TurnID != "Tl" {
		t.Fatalf("TTSInvoked = %v, want exactly the Lead's (a text reactor holds/dispatches nothing)", got)
	}
}
