package orchestrator_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/agent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
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

// reactorEngine is a scripted [agent.Engine] for the REAL-Cast look-ahead text-reactor
// test: a Cross-talk turn (its user message carries the composite `<lead> says: "…"`)
// generates the Reaction; any other call (the speculative Draft) returns "" so this
// Agent never wins the Lead race and is left as the sole remaining reactor.
type reactorEngine struct{ reaction string }

func (e reactorEngine) Generate(_ context.Context, msgs []llm.Message) (string, error) {
	for _, m := range msgs {
		if strings.Contains(m.Text, `says: "`) {
			return e.reaction, nil
		}
	}
	return "", nil
}

// TestReplier_Lookahead_TextReactor_PostsAfterLeadThroughRealCast is the #389
// finding-1/-2 regression, driven through the REAL agent.Cast (production wires the
// look-ahead pump, so prerenderReaction/releaseReaction IS the production Cross-talk
// path). A voiced Character Lead speaks; a VOICELESS Butler reactor with a real
// TextSink is co-addressed. The coordinator must classify the reactor as text BEFORE
// pre-render (ReactionModality), keep it OUT of the pump lane, and post its Reaction
// only via the post-gate tail — so the irreversible TextSink post lands AFTER the Lead
// is audibly on the wire (floor.Speaking), with EnsembleReaction + TurnEnded(text_delivered)
// and NO second TTSInvoked. Look-ahead on now produces the SAME events as off.
func TestReplier_Lookahead_TextReactor_PostsAfterLeadThroughRealCast(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()
	pump := newFakeLookahead()
	synth := &laneSynth{pump: pump, holdSet: map[string]bool{"Bart leads.": true}, holdGate: make(chan struct{})}

	// The real Lead: a voiced Character whose Draft/Speak run through agent.Replier.
	leadRep := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: bartTarget.AgentID, Markdown: "You are Bart.", Voice: tts.Voice{ProviderID: "test", VoiceID: "bart", Name: "Bart"}},
		Engine:      fixedEngine{reply: "Bart leads."},
		Synthesizer: synth,
	})
	// The real reactor: a VOICELESS Butler (empty VoiceID) with a TextSink. Its
	// SpeakReaction routes through the real speakDraftModality TEXT branch.
	var (
		postMu       sync.Mutex
		posted       string
		postSawAudio bool
	)
	butlerRep := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: butlerTarget.AgentID, Markdown: "You are Glyphoxa.", Voice: tts.Voice{ProviderID: "test", VoiceID: "", Name: "Glyphoxa"}},
		Engine:      reactorEngine{reaction: "As you wish, my lord."},
		Synthesizer: synth,
		TextSink: func(_ context.Context, text string) error {
			postMu.Lock()
			posted = text
			postSawAudio = floor.Speaking() // the Lead must be audible when the text posts
			postMu.Unlock()
			return nil
		},
	})
	cast := agent.NewCast(leadRep, butlerRep)

	replier := lookaheadReplier(h, floor, cast, pump, synth)
	t.Cleanup(replier.Bind(t.Context(), h.Bus))
	t.Cleanup(orchestrator.NewBargeIn(floor, 0).Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.EnsembleRouted{TurnID: "Tl", Text: "Bart, Glyphoxa — thoughts?", Targets: []voiceevent.AddressTarget{bartTarget, butlerTarget}})

	// The Lead's sentence is on the wire but HELD by the lane synth (Speak blocked). Arm
	// the floor, then release the Lead so its Speak returns and the post-gate tail runs.
	voicetest.WaitEvent(t, h, 2*time.Second, func(e voiceevent.TTSInvoked) bool {
		return e.TurnID == "Tl" && e.Sentence == "Bart leads."
	}, "the Lead's sentence is on the wire")
	// The reactor's text post must NOT have happened yet — it is forbidden mid-Lead.
	postMu.Lock()
	early := posted
	postMu.Unlock()
	if early != "" {
		t.Fatalf("the text Reaction posted DURING the Lead's playback (%q) — must wait for the post-gate tail", early)
	}
	h.Bus.Publish(voiceevent.FirstOpus{TurnID: "Tl"})
	close(synth.holdGate)

	var rID string
	voicetest.WaitEvent(t, h, 2*time.Second, func(e voiceevent.EnsembleReaction) bool {
		if e.LeadTurnID == "Tl" && e.Target.AgentID == butlerTarget.AgentID {
			rID = e.TurnID
			return true
		}
		return false
	}, "ensemble.reaction announced for the Butler reactor (post-gate)")
	voicetest.WaitEvent(t, h, 2*time.Second, func(e voiceevent.TurnEnded) bool {
		return e.TurnID == rID && e.Reason == voiceevent.TurnEndTextDelivered
	}, "turn.ended(text_delivered) for the text reactor")
	waitFloorFree(t, floor)

	postMu.Lock()
	gotPost, sawAudio := posted, postSawAudio
	postMu.Unlock()
	if gotPost != "As you wish, my lord." {
		t.Errorf("TextSink post = %q, want the reaction delivered as text", gotPost)
	}
	if !sawAudio {
		t.Error("the text Reaction posted before the Lead was audibly on the wire (must be post-gate)")
	}
	if got := ttsInvokedInOrder(t, h); len(got) != 1 || got[0].TurnID != "Tl" {
		t.Fatalf("TTSInvoked = %v, want exactly the Lead's (a text reactor dispatches nothing)", got)
	}
}

// TestReplier_Lookahead_TextReactor_LeadAllStartError_NoPostNoDeadlock is the
// #389×#391 merge regression: a text reactor whose prerender path sends NOTHING on the
// #391 ttsErrCh, combined with a Lead whose EVERY sentence start-errors (floor never
// arms → the audible-Lead gate REFUSES the Reaction, ADR-0027). The text Reaction must
// NOT post (it would react to a line nobody heard, ADR-0012), releaseReaction is never
// reached, and the uniform defer's <-done must not wedge on the ttsErrCh the prerender
// never sent. The unit ends with the Lead's tts_error and a freed floor — no deadlock.
func TestReplier_Lookahead_TextReactor_LeadAllStartError_NoPostNoDeadlock(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()
	pump := newFakeLookahead()
	// The Lead's only sentence start-errors → nothing delivered, no FirstOpus, no arm.
	synth := &laneSynth{pump: pump, failOn: map[string]bool{"Bart leads.": true}, holdGate: make(chan struct{})}

	leadRep := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: bartTarget.AgentID, Markdown: "You are Bart.", Voice: tts.Voice{ProviderID: "test", VoiceID: "bart", Name: "Bart"}},
		Engine:      fixedEngine{reply: "Bart leads."},
		Synthesizer: synth,
	})
	var (
		postMu sync.Mutex
		posted string
	)
	butlerRep := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: butlerTarget.AgentID, Markdown: "You are Glyphoxa.", Voice: tts.Voice{ProviderID: "test", VoiceID: "", Name: "Glyphoxa"}},
		Engine:      reactorEngine{reaction: "As you wish, my lord."},
		Synthesizer: synth,
		TextSink: func(_ context.Context, text string) error {
			postMu.Lock()
			posted = text
			postMu.Unlock()
			return nil
		},
	})
	cast := agent.NewCast(leadRep, butlerRep)

	replier := lookaheadReplier(h, floor, cast, pump, synth)
	t.Cleanup(replier.Bind(t.Context(), h.Bus))
	t.Cleanup(orchestrator.NewBargeIn(floor, 0).Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.EnsembleRouted{TurnID: "Tl", Text: "Bart, Glyphoxa — thoughts?", Targets: []voiceevent.AddressTarget{bartTarget, butlerTarget}})

	// The Lead's all-start-error turn ends tts_error; the unit must fully unwind (no
	// deadlock on the never-sent ttsErrCh) and free the floor.
	voicetest.WaitEvent(t, h, 2*time.Second, func(e voiceevent.TurnEnded) bool {
		return e.TurnID == "Tl" && e.Reason == voiceevent.TurnEndTTSError
	}, "the Lead ends tts_error")
	waitFloorFree(t, floor)

	// The text Reaction reacted to a line nobody heard → it must NOT have posted, and no
	// reaction sub-turn line was ever announced.
	postMu.Lock()
	gotPost := posted
	postMu.Unlock()
	if gotPost != "" {
		t.Errorf("text Reaction posted %q to a Lead line nobody heard — the gate must suppress it", gotPost)
	}
	voicetest.AssertNoEvent[voiceevent.EnsembleReaction](t, h)
}
