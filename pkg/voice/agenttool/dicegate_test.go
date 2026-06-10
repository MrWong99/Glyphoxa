package agenttool

import (
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
)

func TestNeedsDice(t *testing.T) {
	cases := []struct {
		name string
		text string
		want bool
	}{
		// Explicit die notation.
		{"d20", "Roll a d20 for me.", true},
		{"NdM", "I attack with 2d6 damage.", true},
		{"d100", "Give me a d100.", true},
		{"d-percent", "Roll d% for the table.", true},
		{"bare die word", "Throw the dice.", true},

		// ttrpg roll intent without explicit notation.
		{"saving throw", "Make a saving throw against the poison.", true},
		{"initiative", "Everyone roll initiative.", true},
		{"check", "Give me an ability check.", true},
		{"to hit", "What's my to hit?", true},

		// Plain conversation — must NOT arm dice.
		{"room price", "How much for a room and a pint?", false},
		{"greeting", "Hello Bart, how are you?", false},
		{"rumors", "Heard any good rumors lately?", false},

		// False-positive guards: dice-shaped substrings inside unrelated words.
		{"model not d-something", "Tell me about your business model.", false},
		{"add not a die", "Can you add another log to the fire?", false},
		{"addled", "The traveler looks addled.", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := needsDice([]llm.Message{{Role: llm.RoleUser, Text: tc.text}})
			if got != tc.want {
				t.Errorf("needsDice(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

// TestNeedsDice_LatestUserOnly proves the gate inspects only the most recent
// user message, not the whole history: an old dice turn must not arm a later
// plain turn, and the system prompt is ignored.
func TestNeedsDice_LatestUserOnly(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleSystem, Text: "You are Bart. Roll dice when asked."}, // system mentions dice — ignored
		{Role: llm.RoleUser, Text: "Roll a d20."},                            // older user turn
		{Role: llm.RoleAssistant, Text: "You rolled a 14."},
		{Role: llm.RoleUser, Text: "Thanks, where's the bar?"}, // current: plain
	}
	if needsDice(msgs) {
		t.Error("needsDice must key on the latest user message (plain), not history or the system prompt")
	}
}
