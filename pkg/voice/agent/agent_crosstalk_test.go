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

	reaction, err := r.React(t.Context(), "", "Bart, Mira — thoughts?", "Bart", "The bridge is out.")
	if err != nil {
		t.Fatalf("React errored: %v", err)
	}
	if !strings.Contains(reaction, "disagree") {
		t.Fatalf("reaction = %q, want the scripted reply", reaction)
	}

	last := prov.lastRequest(t)
	userMsg := lastUserText(t, last.Messages)
	if !strings.Contains(userMsg, "Bart, Mira — thoughts?") {
		t.Fatalf("composite user msg = %q, want the original utterance", userMsg)
	}
	if !strings.Contains(userMsg, `Bart says: "The bridge is out."`) {
		t.Fatalf("composite user msg = %q, want the Lead's cross-talk line", userMsg)
	}
	// The cross-talk instruction rides the volatile tail, not the stable system
	// prompt (ADR-0059), so the reaction framing reaches the model per-turn.
	if tail := volatileTail(t, last.Messages); !strings.Contains(tail, "Another character has just spoken") {
		t.Fatalf("volatile tail = %q, want the cross-talk instruction", tail)
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
		{"lowercase", "[silence]"},
		{"trailing-period", "[SILENCE]."},
		{"quoted-punct", `"[silence]."`},
		{"mixed-case-bang", "  [Silence]!  "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prov := &fakeProvider{reply: tc.reply}
			r := agent.NewReplier(agent.Config{
				Persona:     agent.Persona{AgentID: "mira", Markdown: "You are Mira.", Voice: testVoice()},
				Provider:    prov,
				Synthesizer: stubSynth{},
			})

			reaction, err := r.React(t.Context(), "", "Bart, Mira?", "Bart", "The bridge is out.")
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

// TestReplier_React_SpeakerName_CompositePrefixed pins speaker attribution on the
// Cross-talk composite (ADR-0025 + the transcript-names seam): with a SpeakerName
// resolver, the composite user message React reasons over carries the
// name-prefixed utterance — `Artusas: <utterance>` — followed by the Lead's
// attributed line, while memory recall stays keyed on the RAW utterance.
func TestReplier_React_SpeakerName_CompositePrefixed(t *testing.T) {
	rec := &recordingRecaller{}
	prov := &fakeProvider{reply: "I disagree, actually."}
	r := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "mira", Markdown: "You are Mira.", Voice: testVoice()},
		Provider:    prov,
		Synthesizer: stubSynth{},
		Memory:      rec,
		SpeakerName: func(id string) string {
			if id == "111" {
				return "Artusas"
			}
			return ""
		},
	})

	if _, err := r.React(t.Context(), "111", "Bart, Mira — thoughts?", "Bart", "The bridge is out."); err != nil {
		t.Fatalf("React errored: %v", err)
	}

	last := prov.lastRequest(t)
	userMsg := lastUserText(t, last.Messages)
	want := "Artusas: Bart, Mira — thoughts?\n\nBart says: \"The bridge is out.\""
	if userMsg != want {
		t.Fatalf("composite user msg = %q, want %q", userMsg, want)
	}
	if got := rec.got(); len(got) != 1 || got[0] != "Bart, Mira — thoughts?" {
		t.Errorf("Recall keyed on %q, want the RAW utterance (ADR-0042)", got)
	}
}

// TestReplier_React_SentinelSubstringIsNotDecline pins that the decline is a
// contains-ONLY-sentinel check (#302 hardening): a real reply that merely mentions the
// token is spoken, not swallowed.
func TestReplier_React_SentinelSubstringIsNotDecline(t *testing.T) {
	prov := &fakeProvider{reply: "[SILENCE] would be rude, so I will answer."}
	r := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "mira", Markdown: "You are Mira.", Voice: testVoice()},
		Provider:    prov,
		Synthesizer: stubSynth{},
	})

	reaction, err := r.React(t.Context(), "", "Bart, Mira?", "Bart", "The bridge is out.")
	if err != nil {
		t.Fatalf("React errored: %v", err)
	}
	if reaction != "[SILENCE] would be rude, so I will answer." {
		t.Fatalf("reaction = %q, want the full reply (a reply that only mentions the sentinel is not a decline)", reaction)
	}
}
