package address_test

import (
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/address"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

var (
	wwButler = voiceevent.AddressTarget{AgentID: "butler", AgentRole: "butler", Name: "Glyphoxa"}
	wwBart   = voiceevent.AddressTarget{AgentID: "npc-bart", AgentRole: "character", Name: "Bart"}
)

// TestWholeWordMatcher_RoutesToNamedNPC pins the happy path: a whole-word name
// hit routes to that NPC, exactly one decision.
func TestWholeWordMatcher_RoutesToNamedNPC(t *testing.T) {
	m := address.NewWholeWordMatcher(wwButler, []voiceevent.AddressTarget{wwBart})

	got := m.TargetMatch("Bart, what's the special tonight?")

	if len(got) != 1 {
		t.Fatalf("len(decisions) = %d, want 1", len(got))
	}
	if got[0].Target.AgentID != wwBart.AgentID {
		t.Errorf("routed to %q, want %q", got[0].Target.AgentID, wwBart.AgentID)
	}
}

// TestWholeWordMatcher_FallsBackToButler pins that an utterance naming no NPC —
// including a mishearing the whole-word match can't recover ("bard" for
// "Bart") — falls through to the Butler.
func TestWholeWordMatcher_FallsBackToButler(t *testing.T) {
	m := address.NewWholeWordMatcher(wwButler, []voiceevent.AddressTarget{wwBart})

	for _, text := range []string{"roll a perception check", "bard, what's the special?"} {
		got := m.TargetMatch(text)
		if len(got) != 1 || got[0].Target.AgentID != wwButler.AgentID {
			t.Errorf("TargetMatch(%q) = %+v, want single Butler decision", text, got)
		}
	}
}

// TestWholeWordMatcher_PanicsOnInvalidTargets pins that target validation is the
// matcher's responsibility, caught at construction.
func TestWholeWordMatcher_PanicsOnInvalidTargets(t *testing.T) {
	cases := map[string]struct {
		butler voiceevent.AddressTarget
		npcs   []voiceevent.AddressTarget
	}{
		"butler wrong role": {voiceevent.AddressTarget{AgentRole: "character", Name: "X"}, nil},
		"butler empty name": {voiceevent.AddressTarget{AgentRole: "butler"}, nil},
		"npc wrong role":    {wwButler, []voiceevent.AddressTarget{{AgentRole: "butler", Name: "X"}}},
		"npc empty name":    {wwButler, []voiceevent.AddressTarget{{AgentRole: "character"}}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("NewWholeWordMatcher did not panic on invalid targets")
				}
			}()
			address.NewWholeWordMatcher(tc.butler, tc.npcs)
		})
	}
}
