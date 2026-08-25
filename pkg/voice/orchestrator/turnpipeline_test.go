package orchestrator_test

import (
	"context"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voicetest"
)

// pipelineReplier wires a routed streaming Replier over the look-ahead pump seam
// and a laneSynth — the production shape of a pipelined turn (#626) minus the
// floor, so the turn runs inline on the publishing goroutine.
func pipelineReplier(h *voicetest.Harness, stream orchestrator.StreamReplyFunc, pump orchestrator.LookaheadPump, synth tts.Synthesizer) *orchestrator.Replier {
	replier := orchestrator.NewStreamReplier(orchestrator.NewTTS(h.Bus, synth), stream, nil)
	if pump != nil {
		replier.SetLookahead(pump)
	}
	return replier
}

// TestReplier_Pipelined_RoutedTurnPreRendersNextSentence is the #626 routed-path
// headline: on an ordinary single-target turn every sentence after the first is
// synthesized under a look-ahead ctx keyed to ITSELF while its predecessor is
// still being delivered, then announced and released in order. The gap the GM
// hears shrinks from a cold TTS TTFB to pacing overhead.
func TestReplier_Pipelined_RoutedTurnPreRendersNextSentence(t *testing.T) {
	h := voicetest.New(t)
	pump := newFakeLookahead()
	synth := &laneSynth{pump: pump}

	var outcomes []orchestrator.SentenceOutcome
	stream := func(_ context.Context, _ voiceevent.AddressRouted, dispatch func(orchestrator.Reply) error) error {
		for _, s := range []string{"one.", "two.", "three."} {
			outcomes = append(outcomes, orchestrator.OutcomeOf(dispatch(orchestrator.Reply{Sentence: s})))
		}
		return nil
	}
	replier := pipelineReplier(h, stream, pump, synth)
	defer replier.Bind(t.Context(), h.Bus)()

	h.Bus.Publish(voiceevent.AddressRouted{TurnID: "T", Text: "Bart, report", Target: voiceevent.AddressTarget{AgentID: "bart"}})

	for i, got := range outcomes {
		if got != orchestrator.SentenceDelivered {
			t.Fatalf("sentence %d outcome = %v, want SentenceDelivered (keep producing)", i+1, got)
		}
	}
	// s1 takes the ordinary queue; s2 and s3 are held under their own lane keys.
	for _, want := range []struct {
		sentence string
		held     bool
		key      string
	}{
		{"one.", false, "T"},
		{"two.", true, "T#2"},
		{"three.", true, "T#3"},
	} {
		call, ok := synth.findCall(func(c synthCall) bool { return c.sentence == want.sentence })
		if !ok {
			t.Fatalf("sentence %q was never synthesized", want.sentence)
		}
		if call.lookahead != want.held || call.turnID != want.key {
			t.Fatalf("%q synthesized held=%v key=%q, want held=%v key=%q", want.sentence, call.lookahead, call.turnID, want.held, want.key)
		}
	}
	// Each held sentence is released only after its predecessor resolved, and the
	// tail's keyed discard (the uniform flush) is a no-op on the clean path.
	wantOps := []string{"release:T#2", "release:T#3", "discard:T#3"}
	got := pump.opsSnapshot()
	if len(got) != len(wantOps) {
		t.Fatalf("lane ops = %v, want %v", got, wantOps)
	}
	for i := range wantOps {
		if got[i] != wantOps[i] {
			t.Fatalf("lane ops = %v, want %v", got, wantOps)
		}
	}
	// Announcement order is the SPOKEN order: a held sentence announces at release.
	var invoked []string
	for _, ev := range h.Events() {
		if e, ok := ev.(voiceevent.TTSInvoked); ok {
			invoked = append(invoked, e.Sentence)
		}
	}
	if len(invoked) != 3 || invoked[0] != "one." || invoked[1] != "two." || invoked[2] != "three." {
		t.Fatalf("TTSInvoked order = %v, want [one. two. three.]", invoked)
	}
}

// TestReplier_NoLookahead_KeepsLegacyDispatch pins the feature-off default: with
// no look-ahead pump wired there is no lane to hold a pre-synthesized sentence in,
// so every sentence dispatches synchronously exactly as before #626 — the dispatch
// call returns only once the sentence has been synthesized, and nothing is held.
func TestReplier_NoLookahead_KeepsLegacyDispatch(t *testing.T) {
	h := voicetest.New(t)
	synth := &laneSynth{pump: newFakeLookahead()}

	stream := func(_ context.Context, _ voiceevent.AddressRouted, dispatch func(orchestrator.Reply) error) error {
		if err := dispatch(orchestrator.Reply{Sentence: "one."}); err != nil {
			t.Errorf("dispatch(one.) = %v, want nil", err)
		}
		// Synchronous contract: the sentence is fully synthesized by the time
		// dispatch returns, so the next dispatch sees it in the call log.
		if _, ok := synth.findCall(func(c synthCall) bool { return c.sentence == "one." }); !ok {
			t.Error("legacy dispatch returned before the sentence was synthesized")
		}
		return dispatch(orchestrator.Reply{Sentence: "two."})
	}
	replier := pipelineReplier(h, stream, nil, synth)
	defer replier.Bind(t.Context(), h.Bus)()

	h.Bus.Publish(voiceevent.AddressRouted{TurnID: "T", Text: "Bart, report", Target: voiceevent.AddressTarget{AgentID: "bart"}})

	for _, s := range []string{"one.", "two."} {
		call, ok := synth.findCall(func(c synthCall) bool { return c.sentence == s })
		if !ok {
			t.Fatalf("sentence %q was never synthesized", s)
		}
		if call.lookahead {
			t.Fatalf("%q was held in the look-ahead lane with no pump wired", s)
		}
	}
	voicetest.AssertEventCount[voiceevent.TTSInvoked](t, h, 2)
}
