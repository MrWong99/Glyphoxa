package agent_test

import (
	"strings"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/agent"
)

// TestReplier_React_CompositePrompt pins the Cross-talk Reaction prompt (ADR-0025,
// #302): React feeds the Lead's delivered text to this Agent as Cross-talk. The
// user message the LLM sees is the SINGLE composition site crossTalkUserText — the
// original utterance plus `<lead> says: "<lead text>"` — and React is PURE (a
// speculative reaction commits nothing until it is actually spoken).
func TestReplier_React_CompositePrompt(t *testing.T) {
	prov := &fakeProvider{reply: "I disagree, actually."}
	r := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "mira", Markdown: "You are Mira.", Voice: testVoice()},
		Provider:    prov,
		Synthesizer: stubSynth{},
	})

	reaction, err := r.React(t.Context(), "Bart, Mira — thoughts?", "Bart", "The bridge is out.")
	if err != nil {
		t.Fatalf("React errored: %v", err)
	}
	if !strings.Contains(reaction, "disagree") {
		t.Fatalf("reaction = %q, want the scripted reply", reaction)
	}

	last := prov.lastRequest(t)
	userMsg := last.Messages[len(last.Messages)-1].Text
	if !strings.Contains(userMsg, "Bart, Mira — thoughts?") {
		t.Fatalf("composite user msg = %q, want the original utterance", userMsg)
	}
	if !strings.Contains(userMsg, `Bart says: "The bridge is out."`) {
		t.Fatalf("composite user msg = %q, want the Lead's cross-talk line", userMsg)
	}
	// Purity: a speculative Reaction mutates no history (ADR-0012).
	if len(r.HistorySnapshot()) != 0 {
		t.Fatal("React must commit nothing to history (purity)")
	}
}

// TestReplier_React_DeclineSentinel pins the decline path (ADR-0025, #302): a
// Reaction whose model output is the "[SILENCE]" sentinel — or empty — is a DECLINE,
// so React returns "", nil (no reaction). History stays untouched.
func TestReplier_React_DeclineSentinel(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reply string
	}{
		{"sentinel", "[SILENCE]"},
		{"sentinel-padded", "  [SILENCE]\n"},
		{"empty", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prov := &fakeProvider{reply: tc.reply}
			r := agent.NewReplier(agent.Config{
				Persona:     agent.Persona{AgentID: "mira", Markdown: "You are Mira.", Voice: testVoice()},
				Provider:    prov,
				Synthesizer: stubSynth{},
			})

			reaction, err := r.React(t.Context(), "Bart, Mira?", "Bart", "The bridge is out.")
			if err != nil {
				t.Fatalf("React errored: %v", err)
			}
			if reaction != "" {
				t.Fatalf("reaction = %q, want empty on decline", reaction)
			}
			if len(r.HistorySnapshot()) != 0 {
				t.Fatal("a declined Reaction must commit nothing to history")
			}
		})
	}
}
